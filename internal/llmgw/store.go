package llmgw

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"github.com/choibongseok/procure-ledger/internal/dbutil"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
)

// Store 는 정책·한도 관리와 사용량 조회다(핸들러가 쓴다). 쓰기는 감사 기록과 같은 트랜잭션이다.
type Store struct {
	DB    *sql.DB
	Txs   contract.TxManager
	Audit audit.Recorder
	Now   func() time.Time
}

func (s *Store) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// Policy 는 모델 허용 규칙 한 줄이다. Role 이 ""·DepartmentID 가 0 이면 해당 조건 없음.
type Policy struct {
	ID           int64  `json:"id"`
	Role         string `json:"role"`
	DepartmentID int64  `json:"departmentId"`
	ModelPattern string `json:"modelPattern"`
	Allowed      bool   `json:"allowed"`
}

// Quota 는 일일 토큰 한도 규칙이다.
type Quota struct {
	ID         int64  `json:"id"`
	ScopeType  string `json:"scopeType"` // role | department | user
	ScopeValue string `json:"scopeValue"`
	DailyLimit int64  `json:"dailyTokenLimit"`
}

// EnsureDefaultPolicy 는 정책 규칙이 하나도 없을 때 기동용 기본 규칙(모든 역할에 기본 모델 허용)을 넣는다.
// 이미 규칙이 있으면 아무것도 하지 않는다(운영자가 정한 정책을 덮어쓰지 않는다).
func EnsureDefaultPolicy(ctx context.Context, db *sql.DB, model string) error {
	var n int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM llm_model_policies`).Scan(&n); err != nil {
		return err
	}
	if n > 0 || model == "" {
		return nil
	}
	_, err := db.ExecContext(ctx, `INSERT INTO llm_model_policies (model_pattern, allowed) VALUES (?, 1)`, model)
	return err
}

// ListPolicies 는 모델 허용 규칙 목록이다.
func (s *Store) ListPolicies(ctx context.Context) ([]Policy, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id, COALESCE(role,''), COALESCE(department_id,0), model_pattern, allowed FROM llm_model_policies ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Policy{}
	for rows.Next() {
		var p Policy
		var a int
		if err := rows.Scan(&p.ID, &p.Role, &p.DepartmentID, &p.ModelPattern, &a); err != nil {
			return nil, err
		}
		p.Allowed = a == 1
		out = append(out, p)
	}
	return out, rows.Err()
}

// ListQuotas 는 일일 한도 규칙 목록이다.
func (s *Store) ListQuotas(ctx context.Context) ([]Quota, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id, scope_type, scope_value, daily_token_limit FROM llm_daily_quotas ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Quota{}
	for rows.Next() {
		var q Quota
		if err := rows.Scan(&q.ID, &q.ScopeType, &q.ScopeValue, &q.DailyLimit); err != nil {
			return nil, err
		}
		out = append(out, q)
	}
	return out, rows.Err()
}

func (s *Store) manage(ctx context.Context, action, target string, diff []audit.DiffItem, fn func(tx contract.Tx) (int64, error)) (int64, error) {
	p, err := contract.Authorize(ctx, contract.PermLLMPolicyManage)
	if err != nil {
		if p.UserID != 0 {
			if aerr := s.Txs.WithTx(ctx, func(tx contract.Tx) error {
				return s.Audit.Record(ctx, tx, audit.Entry{Actor: p, Action: action, TargetType: audit.TargetLLMPolicy, TargetID: target, Result: audit.ResultDenied})
			}); aerr != nil {
				log.Printf("llmgw: 거부 감사 기록 실패(%s): %v", action, aerr)
			}
		}
		return 0, err
	}
	var id int64
	err = s.Txs.WithTx(ctx, func(tx contract.Tx) error {
		var e error
		id, e = fn(tx)
		if e != nil {
			return e
		}
		tid := target
		if tid == "" {
			tid = strconv.FormatInt(id, 10)
		}
		return s.Audit.Record(ctx, tx, audit.Entry{Actor: p, Action: action, TargetType: audit.TargetLLMPolicy, TargetID: tid, Result: audit.ResultSuccess, Diff: diff})
	})
	return id, err
}

// AddPolicy 는 규칙을 추가한다(llm:policy_manage).
func (s *Store) AddPolicy(ctx context.Context, p Policy) (int64, error) {
	if strings.TrimSpace(p.ModelPattern) == "" {
		return 0, fmt.Errorf("%w: 모델 패턴이 필요합니다", contract.ErrValidation)
	}
	if p.Role != "" && !knownRoleKey(ctx, s.DB, p.Role) {
		return 0, fmt.Errorf("%w: 알 수 없는 역할입니다. 기본 역할 키(admin·finance·manager·staff·auditor)나 사용자 정의 역할 키(r_번호)를 쓰세요", contract.ErrValidation)
	}
	allowed := 0
	if p.Allowed {
		allowed = 1
	}
	diff := []audit.DiffItem{{Key: "model_pattern", New: p.ModelPattern}, {Key: "allowed", New: strconv.Itoa(allowed)}}
	if p.Role != "" {
		diff = append(diff, audit.DiffItem{Key: "role", New: p.Role})
	}
	if p.DepartmentID != 0 {
		diff = append(diff, audit.DiffItem{Key: "department_id", New: strconv.FormatInt(p.DepartmentID, 10)})
	}
	return s.manage(ctx, "llm_policy.add", "new", diff, func(tx contract.Tx) (int64, error) {
		var role any
		if p.Role != "" {
			role = p.Role
		}
		var dept any
		if p.DepartmentID != 0 {
			dept = p.DepartmentID
		}
		id, err := dbutil.InsertID(ctx, tx, `INSERT INTO llm_model_policies (role, department_id, model_pattern, allowed) VALUES (?,?,?,?)`,
			role, dept, p.ModelPattern, allowed)
		if err != nil {
			return 0, fmt.Errorf("%w: %v", contract.ErrValidation, err)
		}
		return id, nil
	})
}

// DeletePolicy 는 규칙을 지운다.
func (s *Store) DeletePolicy(ctx context.Context, id int64) error {
	_, err := s.manage(ctx, "llm_policy.delete", strconv.FormatInt(id, 10), []audit.DiffItem{{Key: "id", Old: strconv.FormatInt(id, 10)}},
		func(tx contract.Tx) (int64, error) {
			r, err := tx.ExecContext(ctx, `DELETE FROM llm_model_policies WHERE id = ?`, id)
			if err != nil {
				return 0, err
			}
			if n, _ := r.RowsAffected(); n == 0 {
				return 0, contract.ErrNotFound
			}
			return id, nil
		})
	return err
}

// SetQuota 는 한도를 만들거나 바꾼다(scope 당 하나).
func (s *Store) SetQuota(ctx context.Context, q Quota) error {
	_, err := s.SaveQuota(ctx, q)
	return err
}

// SaveQuota 는 SetQuota 와 같고 저장된 행(id 포함)을 돌려준다. 한도 0 은 "그 범위의 AI 호출을 모두 막는다"는 뜻이다.
// 범위 값은 실제로 있는 것만 받는다: 역할은 알려진 역할 이름, 부서·사용자는 존재하는 번호. 검증에 실패하면 저장도 감사 기록도 하지 않는다.
func (s *Store) SaveQuota(ctx context.Context, q Quota) (Quota, error) {
	switch q.ScopeType {
	case "role", "department", "user":
	default:
		return Quota{}, fmt.Errorf("%w: 범위는 role·department·user 중 하나입니다", contract.ErrValidation)
	}
	q.ScopeValue = strings.TrimSpace(q.ScopeValue)
	if q.ScopeValue == "" || q.DailyLimit < 0 {
		return Quota{}, fmt.Errorf("%w: 범위 값과 0 이상의 한도가 필요합니다", contract.ErrValidation)
	}
	target := q.ScopeType + ":" + q.ScopeValue
	id, err := s.manage(ctx, "llm_quota.set", target, []audit.DiffItem{{Key: "daily_token_limit", New: strconv.FormatInt(q.DailyLimit, 10)}},
		func(tx contract.Tx) (int64, error) {
			if err := checkQuotaScope(ctx, tx, q.ScopeType, q.ScopeValue); err != nil {
				return 0, err
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO llm_daily_quotas (scope_type, scope_value, daily_token_limit) VALUES (?,?,?)
				ON CONFLICT(scope_type, scope_value) DO UPDATE SET daily_token_limit = excluded.daily_token_limit`,
				q.ScopeType, q.ScopeValue, q.DailyLimit); err != nil {
				return 0, err
			}
			var id int64
			err := tx.QueryRowContext(ctx, `SELECT id FROM llm_daily_quotas WHERE scope_type = ? AND scope_value = ?`, q.ScopeType, q.ScopeValue).Scan(&id)
			return id, err
		})
	if err != nil {
		return Quota{}, err
	}
	q.ID = id
	return q, nil
}

// checkQuotaScope 는 범위 값이 실제로 있는 역할·부서·사용자인지 본다.
func checkQuotaScope(ctx context.Context, tx contract.Tx, typ, val string) error {
	if typ == "role" {
		if knownRoleKey(ctx, tx, val) {
			return nil
		}
		return fmt.Errorf("%w: 알 수 없는 역할입니다. 기본 역할 키(admin·finance·manager·staff·auditor)나 사용자 정의 역할 키(r_번호)를 쓰세요", contract.ErrValidation)
	}
	n, err := strconv.ParseInt(val, 10, 64)
	if err != nil || n <= 0 || strconv.FormatInt(n, 10) != val {
		return fmt.Errorf("%w: 부서·사용자는 번호(양의 정수)로 입력하세요", contract.ErrValidation)
	}
	table, label := "departments", "부서"
	if typ == "user" {
		table, label = "users", "사용자"
	}
	var one int
	err = tx.QueryRowContext(ctx, `SELECT 1 FROM `+table+` WHERE id = ?`, n).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: 없는 %s 번호입니다", contract.ErrValidation, label)
	}
	return err
}

// ---- 사용량 ----

// UsageRow 는 집계 한 줄이다.
type UsageRow struct {
	Provider     string `json:"provider"`
	Fallbacks    int64  `json:"fallbacks"` // 대체로 응답한 호출 수(B45)
	Day          string `json:"day"`
	Role         string `json:"role"`
	Model        string `json:"model"`
	Purpose      string `json:"purpose"`
	Calls        int64  `json:"calls"`
	Blocked      int64  `json:"blocked"`
	InputTokens  int64  `json:"inputTokens"`
	OutputTokens int64  `json:"outputTokens"`
	CostMicroUSD int64  `json:"costMicroUsd"`
	MaskedCount  int64  `json:"maskedCount"`
}

// UsageSummary 는 최근 days 일의 일·역할·모델·용도별 집계다(llm:usage_read).
func (s *Store) UsageSummary(ctx context.Context, days int) ([]UsageRow, error) {
	if _, err := contract.Authorize(ctx, contract.PermLLMUsageRead); err != nil {
		return nil, err
	}
	if days <= 0 || days > 90 {
		days = 7
	}
	from := s.now().UTC().AddDate(0, 0, -days+1)
	from = time.Date(from.Year(), from.Month(), from.Day(), 0, 0, 0, 0, time.UTC)
	rows, err := s.DB.QueryContext(ctx, `SELECT substr(at,1,10) AS day, role, provider, model, purpose,
		SUM(CASE WHEN fallback_from <> '' THEN 1 ELSE 0 END),
		SUM(CASE WHEN status='ok' THEN 1 ELSE 0 END),
		SUM(CASE WHEN status LIKE 'blocked%' THEN 1 ELSE 0 END),
		SUM(input_tokens), SUM(output_tokens), SUM(cost_micro_usd), SUM(masked_count)
		FROM llm_usage WHERE at >= ? GROUP BY day, role, provider, model, purpose ORDER BY day DESC, role, provider, model, purpose`,
		from.Format(time.RFC3339))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []UsageRow{}
	for rows.Next() {
		var r UsageRow
		if err := rows.Scan(&r.Day, &r.Role, &r.Provider, &r.Model, &r.Purpose, &r.Fallbacks, &r.Calls, &r.Blocked, &r.InputTokens, &r.OutputTokens, &r.CostMicroUSD, &r.MaskedCount); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
