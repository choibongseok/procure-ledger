package aicost

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"strconv"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
)

// 부서별 AI 비용 예산과 한도 임박 알림(명세 9절). 구매 예산(budgets)과 합치지 않는다(D6).
// 비교하는 금액은 부서의 직접 비용(요청 시점 부서)이고, 원화 환산은 그 달 환율을 쓰되 없으면 가장 가까운 이전 달 환율(추정)을 쓴다.

// BudgetStatus 는 부서 한 곳의 예산 대비다.
type BudgetStatus struct {
	AmountKRW   int64 `json:"amountKrw"`
	UsedKRW     int64 `json:"usedKrw"`
	Pct         int   `json:"pct"`
	FxEstimated bool  `json:"fxEstimated"` // 그 달 환율이 없어 이전 달 환율로 어림한 값
}

// BudgetRow 는 예산 화면의 한 줄이다(예산이 없으면 AmountKRW 가 비어 있다).
type BudgetRow struct {
	EntityID       int64  `json:"entityId"`
	DepartmentID   int64  `json:"departmentId"`
	DepartmentName string `json:"departmentName"`
	AmountKRW      *int64 `json:"amountKrw"`
	UsedKRW        int64  `json:"usedKrw"`
	Pct            int    `json:"pct"`
	FxEstimated    bool   `json:"fxEstimated"`
}

// fxFallback 은 월 환율, 없으면 가장 가까운 이전 달 환율이다. 둘 다 없으면 0.
func (s *Service) fxFallback(ctx context.Context, month string) (fx int64, estimated bool, err error) {
	if fx, err = s.fxFor(ctx, month); err != nil || fx > 0 {
		return fx, false, err
	}
	err = s.o.DB.QueryRowContext(ctx, `SELECT COALESCE((SELECT usd_krw_x1000 FROM ai_cost_fx WHERE month < ? ORDER BY month DESC LIMIT 1), 0)`, month).Scan(&fx)
	return fx, fx > 0, err
}

func pctOf(used, amount int64) int {
	if amount <= 0 {
		return 0
	}
	return int(used * 100 / amount)
}

// deptDirectMicro 는 월의 부서 직접 비용(성공 호출)이다.
func (s *Service) deptDirectMicro(ctx context.Context, month string, dept int64) (int64, error) {
	from, to, err := monthRange(month)
	if err != nil {
		return 0, err
	}
	var m int64
	err = s.o.DB.QueryRowContext(ctx, `SELECT COALESCE(SUM(cost_micro_usd), 0) FROM llm_usage WHERE at >= ? AND at < ? AND status = 'ok' AND department_id = ?`, from, to, dept).Scan(&m)
	return m, err
}

// budgetFor 는 부서의 월 예산 대비(예산이 없으면 nil)다.
func (s *Service) budgetFor(ctx context.Context, month string, dept int64) (*BudgetStatus, error) {
	var amount int64
	err := s.o.DB.QueryRowContext(ctx, `SELECT amount_krw FROM ai_cost_budgets WHERE department_id = ? AND month = ? ORDER BY id LIMIT 1`, dept, month).Scan(&amount)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	micro, err := s.deptDirectMicro(ctx, month, dept)
	if err != nil {
		return nil, err
	}
	fx, est, err := s.fxFallback(ctx, month)
	if err != nil {
		return nil, err
	}
	used := microToKRW(micro, fx)
	return &BudgetStatus{AmountKRW: amount, UsedKRW: used, Pct: pctOf(used, amount), FxEstimated: est}, nil
}

// SetBudget 은 부서·월의 AI 비용 예산(원)을 정한다(cost:allocate). 바꾸면 이력이 남는다. 0원으로 바꿀 수는 있어도 지울 수는 없다.
func (s *Service) SetBudget(ctx context.Context, month string, dept, amountKRW int64) error {
	p, err := contract.Authorize(ctx, contract.PermCostAllocate)
	if err != nil {
		return err
	}
	if !reMonth.MatchString(month) {
		return valErr("월은 YYYY-MM 형식으로 입력하세요")
	}
	if amountKRW < 0 || amountKRW > 100_000_000_000 {
		return valErr("예산은 0원 이상 1,000억 원 이하의 정수여야 합니다")
	}
	if dept <= 0 || !s.deptExists(ctx, dept) {
		return valErr("없는 부서입니다")
	}
	entity := s.unitEntity(ctx, lastDay(month), dept)
	scope, err := contract.ReadScope(ctx, contract.PermCostAllocate)
	if err != nil {
		return err
	}
	if !scope.Has(entity) {
		return fmt.Errorf("%w: 그 법인의 예산은 정할 수 없습니다", contract.ErrForbidden)
	}
	var old sql.NullInt64
	var have int
	_ = s.o.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM ai_cost_budgets WHERE entity_id = ? AND department_id = ? AND month = ?`, entity, dept, month).Scan(&have)
	if have > 0 {
		_ = s.o.DB.QueryRowContext(ctx, `SELECT amount_krw FROM ai_cost_budgets WHERE entity_id = ? AND department_id = ? AND month = ?`, entity, dept, month).Scan(&old)
		if old.Valid && old.Int64 == amountKRW {
			return fmt.Errorf("%w: 이미 같은 예산입니다.", contract.ErrInvalidState)
		}
	}
	return s.o.Txs.WithTx(ctx, func(tx contract.Tx) error {
		now := s.stamp()
		if _, err := tx.ExecContext(ctx, `INSERT INTO ai_cost_budgets (entity_id, department_id, month, amount_krw, updated_by, updated_at) VALUES (?,?,?,?,?,?)
			ON CONFLICT (entity_id, department_id, month) DO UPDATE SET amount_krw = ?, updated_by = ?, updated_at = ?`, entity, dept, month, amountKRW, p.UserID, now, amountKRW, p.UserID, now); err != nil {
			return err
		}
		var oldV any
		if old.Valid {
			oldV = old.Int64
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO ai_cost_budget_revisions (entity_id, department_id, month, old_amount, new_amount, changed_by, changed_at) VALUES (?,?,?,?,?,?,?)`,
			entity, dept, month, oldV, amountKRW, p.UserID, now); err != nil {
			return err
		}
		it := audit.DiffItem{Key: "amount", New: strconv.FormatInt(amountKRW, 10)}
		if old.Valid {
			it.Old = strconv.FormatInt(old.Int64, 10)
		}
		return s.o.Audit.Record(ctx, tx, audit.Entry{Actor: p, Action: "ai_cost.budget_set", TargetType: audit.TargetAICost, TargetID: fmt.Sprintf("budget/%s/%d", month, dept), Result: audit.ResultSuccess,
			Diff: []audit.DiffItem{{Key: "month", New: month}, {Key: "department_id", New: strconv.FormatInt(dept, 10)}, it}})
	})
}

// ListBudgets 는 법인의 부서별 예산과 사용(직접 비용) 현황이다(cost:read). 예산이 없는 부서도 줄에 나온다.
func (s *Service) ListBudgets(ctx context.Context, month string, entityID int64) ([]BudgetRow, error) {
	entity, err := s.scopedEntity(ctx, entityID)
	if err != nil {
		return nil, err
	}
	month, err = s.normMonth(month)
	if err != nil {
		return nil, err
	}
	depts, err := s.ListDepartments(ctx)
	if err != nil {
		return nil, err
	}
	fx, est, err := s.fxFallback(ctx, month)
	if err != nil {
		return nil, err
	}
	out := []BudgetRow{}
	for _, d := range depts {
		if d.EntityID != entity {
			continue
		}
		row := BudgetRow{EntityID: entity, DepartmentID: d.ID, DepartmentName: d.Name, FxEstimated: est}
		var amount int64
		err := s.o.DB.QueryRowContext(ctx, `SELECT amount_krw FROM ai_cost_budgets WHERE entity_id = ? AND department_id = ? AND month = ?`, entity, d.ID, month).Scan(&amount)
		if err == nil {
			a := amount
			row.AmountKRW = &a
		} else if !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		micro, err := s.deptDirectMicro(ctx, month, d.ID)
		if err != nil {
			return nil, err
		}
		row.UsedKRW = microToKRW(micro, fx)
		if row.AmountKRW != nil {
			row.Pct = pctOf(row.UsedKRW, *row.AmountKRW)
		}
		out = append(out, row)
	}
	return out, nil
}

// ---- 한도 임박 알림 ----

var limitThresholds = []int{80, 100}

type limitHit struct {
	entity, dept, amount, used int64
	threshold                  int
	name                       string
	head                       int64
}

// CheckLimits 는 이번 달 예산이 있는 부서의 실제 누계(직접 비용)가 80%·100% 에 닿았는지 보고 아직 알리지 않은 문턱마다 한 번 알린다(부서장·재무).
// 같은 달·부서·문턱은 한 번뿐이라 재시도·재시작에도 중복되지 않는다. 환율이 없으면 비교할 수 없어 건너뛴다. 알린 건수를 돌려준다.
func (s *Service) CheckLimits(ctx context.Context) (int, error) {
	month := s.currentMonth()
	fx, _, err := s.fxFallback(ctx, month)
	if err != nil || fx == 0 {
		return 0, err
	}
	rows, err := s.o.DB.QueryContext(ctx, `SELECT entity_id, department_id, amount_krw FROM ai_cost_budgets WHERE month = ? AND amount_krw > 0 ORDER BY entity_id, department_id`, month)
	if err != nil {
		return 0, err
	}
	type bud struct{ entity, dept, amount int64 }
	var buds []bud
	for rows.Next() {
		var b bud
		if err := rows.Scan(&b.entity, &b.dept, &b.amount); err != nil {
			rows.Close()
			return 0, err
		}
		buds = append(buds, b)
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	var hits []limitHit
	for _, b := range buds {
		micro, err := s.deptDirectMicro(ctx, month, b.dept)
		if err != nil {
			return 0, err
		}
		used := microToKRW(micro, fx)
		pct := pctOf(used, b.amount)
		for _, th := range limitThresholds {
			if pct < th {
				continue
			}
			var n int
			if err := s.o.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM ai_cost_limit_notices WHERE month = ? AND entity_id = ? AND department_id = ? AND threshold = ?`, month, b.entity, b.dept, th).Scan(&n); err != nil {
				return 0, err
			}
			if n > 0 {
				continue
			}
			name, _ := s.deptName(ctx, b.dept)
			var head int64
			_ = s.o.DB.QueryRowContext(ctx, `SELECT COALESCE((SELECT head_user_id FROM org_unit_spans WHERE unit_id = ? AND valid_to IS NULL), 0)`, b.dept).Scan(&head)
			hits = append(hits, limitHit{entity: b.entity, dept: b.dept, amount: b.amount, used: used, threshold: th, name: name, head: head})
		}
	}
	if len(hits) == 0 {
		return 0, nil
	}
	finance, err := s.allocators(ctx)
	if err != nil {
		return 0, err
	}
	sent := 0
	for _, h := range hits {
		h := h
		err := s.o.Txs.WithTx(ctx, func(tx contract.Tx) error {
			res, err := tx.ExecContext(ctx, `INSERT INTO ai_cost_limit_notices (month, entity_id, department_id, threshold, notified_at) VALUES (?,?,?,?,?) ON CONFLICT DO NOTHING`, month, h.entity, h.dept, h.threshold, s.stamp())
			if err != nil {
				return err
			}
			if n, _ := res.RowsAffected(); n == 0 {
				return nil // 다른 곳이 먼저 알렸다
			}
			title := fmt.Sprintf("AI 비용이 예산의 %d%% 에 닿았습니다", h.threshold)
			body := fmt.Sprintf("%s 의 %s AI 사용 비용이 예산의 %d%% 에 닿았습니다(사용 %s원 / 예산 %s원).", h.name, month, h.threshold, thousandsKRW(h.used), thousandsKRW(h.amount))
			seen := map[int64]bool{}
			send := func(user int64, link string) error {
				if user <= 0 || seen[user] {
					return nil
				}
				seen[user] = true
				_, err := tx.ExecContext(ctx, `INSERT INTO notifications (user_id, kind, title, body, link, ref_type, ref_id, created_at) VALUES (?,?,?,?,?,?,?,?)`,
					user, "aicost_limit", title, body, link, "ai_cost", h.dept, s.stamp())
				return err
			}
			if err := send(h.head, "/ai-cost/my"); err != nil {
				return err
			}
			for _, u := range finance {
				if err := send(u, "/admin/ai-cost/budgets"); err != nil {
					return err
				}
			}
			sent++
			return nil
		})
		if err != nil {
			return sent, err
		}
	}
	return sent, nil
}

// allocators 는 AI 비용 배부 권한(cost:allocate)을 가진 활성 사용자다.
func (s *Service) allocators(ctx context.Context) ([]int64, error) {
	rows, err := s.o.DB.QueryContext(ctx, `SELECT DISTINCT e.user_id FROM user_effective_permissions e JOIN users u ON u.id = e.user_id AND u.active = 1 WHERE e.permission = ? ORDER BY e.user_id`, string(contract.PermCostAllocate))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// Run 은 한 시간마다 한도 임박을 점검한다(서버 주기 작업). 종료 신호가 오면 끝난다.
func (s *Service) Run(ctx context.Context) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		if n, err := s.CheckLimits(ctx); err != nil && ctx.Err() == nil {
			log.Printf("aicost: 한도 점검 실패: %v", err)
		} else if n > 0 {
			log.Printf("aicost: 예산 한도 임박 알림 %d건", n)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
