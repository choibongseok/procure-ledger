package llmgw

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"github.com/choibongseok/procure-ledger/internal/dbutil"
	"strconv"
	"strings"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/contract/llm"
)

// Prices 는 1백만 토큰당 가격이다(1e-6 USD 단위 정수). 비어 있으면 비용 0.
type Prices struct {
	InPerMTok  int64
	OutPerMTok int64
}

// Options 는 게이트웨이 구성이다.
type Options struct {
	Provider     Provider
	DefaultModel string
	DB           *sql.DB
	Txs          contract.TxManager
	Audit        audit.Recorder
	Masker       contract.Masker
	Prices       Prices
	Timeout      time.Duration
	Now          func() time.Time // 테스트용. 기본 time.Now
	// 아래는 B45 라우팅용이다. llm_routes 에 맞는 규칙이 없으면(기본) 전부 쓰이지 않고 동작은 이전과 같다.
	Sealer     Sealer // 공급자 설정(봉인) 풀이. 라우팅 공급자(mock 외)에 필요하다
	AllowLocal bool   // 루프백 주소 허용(로컬 시험 전용)
	Rehearsal  bool   // 리허설이면 mock 외 공급자 호출을 거부한다
	// 장애 대체(B45 2단계): 회로 차단 상태는 프로세스 메모리다(D8). 관리 서비스와 하나를 공유하려면 같은 값을 넘긴다. 비면 게이트웨이가 만든다.
	Breakers *BreakerSet
	// Sleep 은 재시도 대기다(시험이 가짜 시계로 바꾼다). 비면 실제로 기다린다.
	Sleep func(ctx context.Context, d time.Duration)
}

// Gateway 는 llm.Client 를 구현한다. 한도 확인·예약은 DB 트랜잭션 하나로 원자적이다(Complete 주석).
type Gateway struct {
	o        Options
	dialect  dbutil.Dialect
	breakers *BreakerSet
}

var _ llm.Client = (*Gateway)(nil)

// New 는 게이트웨이를 만든다. 필수 의존성이 없으면 오류다.
func New(o Options) (*Gateway, error) {
	switch {
	case o.Provider == nil:
		return nil, errors.New("llmgw: 공급자가 필요합니다")
	case o.DB == nil || o.Txs == nil || o.Audit == nil || o.Masker == nil:
		return nil, errors.New("llmgw: DB·트랜잭션·감사 기록기·마스킹이 필요합니다")
	case o.Provider.Name() != "mock" && o.DefaultModel == "":
		return nil, errors.New("llmgw: 기본 모델(PL_LLM_MODEL)이 설정되지 않았습니다")
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Timeout <= 0 {
		o.Timeout = 30 * time.Second
	}
	g := &Gateway{o: o, dialect: dbutil.DialectOf(o.DB), breakers: o.Breakers}
	if g.breakers == nil {
		g.breakers = NewBreakerSet(o.Now)
	}
	g.breakers.OnChange = g.recordCircuit
	return g, nil
}

// Provider 는 사용 중인 공급자 이름이다.
func (g *Gateway) Provider() string { return g.o.Provider.Name() }

// DefaultMaxTokens 는 요청이 MaxTokens 를 주지 않았을 때 한도 예약에 쓰는 출력 상한이다.
const DefaultMaxTokens = 1024

// Complete 는 llm.Client 를 구현한다.
//
// 한도는 "확인 → 예약 → 호출 → 확정"으로 원자적으로 처리한다. 예약은 한도 확인과 같은 트랜잭션에서
// llm_usage 행(status=ok, 토큰=예상 입력+최대 출력)을 넣는 것이라 동시 호출이 서로의 예약을 본다.
// 호출이 끝나면 같은 행을 실제 사용량으로 고치고(감사 기록과 같은 트랜잭션), 실패하면 0 토큰·error 로 고쳐 예약을 푼다.
// 예약이 한도를 넘기면 호출 전에 거부한다(보수적: 최악의 출력 길이까지 더해 판정).
func (g *Gateway) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	p, ok := contract.PrincipalFrom(ctx)
	if !ok || p.UserID == 0 {
		return llm.Response{}, contract.ErrUnauthenticated
	}
	model := req.Model
	if model == "" {
		model = g.o.DefaultModel
	}
	if model == "" {
		model = "mock"
	}
	// 0. 경로 판정(B45): 맞는 규칙이 있으면 그 공급자·모델로, 없으면 기본 경로(아래가 이전과 같다).
	st := &callStats{}
	ctx = context.WithValue(ctx, statsKey{}, st)
	rt, rerr := g.route(ctx, p, req)
	if rerr != nil {
		g.record(ctx, p, req, model, llm.Usage{}, 0, "error", "공급자 경로를 정하지 못했습니다")
		return llm.Response{}, fmt.Errorf("LLM 호출 실패: %w", rerr)
	}
	if rt != nil {
		ctx = context.WithValue(ctx, routedKey{}, rt)
		st.routeID = rt.rule.ID
		st.fallbackOff = rt.fallbackSkip
		if req.Model == "" && rt.primary.model != "" {
			model = rt.primary.model
		}
		rt.primary.model = model
	}
	req.Model = model

	// 1. 모델 허용
	if err := g.checkModel(ctx, p, model); err != nil {
		g.record(ctx, p, req, model, llm.Usage{}, 0, "blocked_model", err.Error())
		return llm.Response{}, err
	}

	// 1-1. 이미지: 권한·관리자 설정 점검(이미지는 마스킹할 수 없다)
	if err := g.guardImages(ctx, p, req); err != nil {
		return llm.Response{}, err
	}

	// 2. 마스킹(전송 직전, 무조건)
	masked, maskedCount := g.maskRequest(req)

	// 3. 일일 한도 확인 + 예약(원자적)
	maxOut := req.MaxTokens
	if maxOut <= 0 {
		maxOut = DefaultMaxTokens
	}
	reserve := llm.Usage{InputTokens: estimateInput(masked), OutputTokens: maxOut}
	id, err := g.reserve(ctx, p, req, model, reserve, maskedCount)
	if err != nil {
		if errors.Is(err, contract.ErrQuotaExceeded) {
			g.record(ctx, p, req, model, llm.Usage{}, 0, "blocked_quota", err.Error())
		}
		return llm.Response{}, err
	}

	// 4. 호출
	var resp llm.Response
	if rt != nil {
		resp, err = g.invoke(ctx, rt, masked, st)
		model = st.model // 최종(또는 마지막으로 시도한) 공급자의 모델
	} else {
		cctx, cancel := context.WithTimeout(ctx, g.o.Timeout)
		defer cancel()
		resp, err = g.o.Provider.Complete(cctx, masked)
	}
	if err != nil {
		_ = g.finalize(ctx, id, p, req, model, llm.Usage{}, maskedCount, "error", "공급자 호출 실패")
		return llm.Response{}, fmt.Errorf("LLM 호출 실패: %w", err)
	}
	if resp.Model == "" {
		resp.Model = model
	}
	// 5. 확정: 실제 사용량·감사. 기록 실패 시 결과를 돌려주지 않는다.
	if err := g.finalize(ctx, id, p, req, model, resp.Usage, maskedCount, "ok", ""); err != nil {
		// 감사 기록은 실패했지만 호출은 일어났으므로 사용량(과금)은 실제 값으로 남겨 예약을 풀고, 결과는 돌려주지 않는다.
		_, _ = g.o.DB.ExecContext(ctx, `UPDATE llm_usage SET input_tokens = ?, output_tokens = ?, cost_micro_usd = ? WHERE id = ?`,
			resp.Usage.InputTokens, resp.Usage.OutputTokens, g.costCtx(ctx, resp.Usage), id)
		return llm.Response{}, err
	}
	return resp, nil
}

func estimateInput(r llm.Request) int {
	n := len([]rune(r.System))
	for _, m := range r.Messages {
		n += len([]rune(m.Content))
	}
	for _, im := range r.Images {
		n += len(im.Data) / 4
	}
	return n/4 + 1
}

// ---- 마스킹 ----

func (g *Gateway) maskRequest(req llm.Request) (llm.Request, int) {
	out := req
	n := 0
	mask := func(s string) string {
		m := g.o.Masker.MaskText(s)
		n += countChanged(s, m)
		return m
	}
	out.System = mask(req.System)
	out.Messages = make([]llm.Message, len(req.Messages))
	for i, m := range req.Messages {
		out.Messages[i] = llm.Message{Role: m.Role, Content: mask(m.Content)}
	}
	return out, n
}

// countChanged 는 마스킹으로 바뀐 공백 구분 조각 수다. 조각 수가 달라졌으면 바뀐 텍스트 1건으로 센다.
func countChanged(before, after string) int {
	if before == after {
		return 0
	}
	a, b := strings.Fields(before), strings.Fields(after)
	if len(a) != len(b) {
		return 1
	}
	n := 0
	for i := range a {
		if a[i] != b[i] {
			n++
		}
	}
	if n == 0 {
		return 1
	}
	return n
}

// ---- 허용 목록 ----

type policyRule struct {
	role    sql.NullString
	dept    sql.NullInt64
	pattern string
	allowed bool
}

func matchPattern(pattern, model string) bool {
	if strings.HasSuffix(pattern, "*") {
		return strings.HasPrefix(model, strings.TrimSuffix(pattern, "*"))
	}
	return pattern == model
}

// checkModel 은 역할·부서에 적용되는 규칙 중 가장 구체적인 규칙으로 판정한다. 같은 구체성이면 거부가 이긴다.
// 적용되는 규칙이 하나도 없으면 거부한다(fail-closed).
func (g *Gateway) checkModel(ctx context.Context, p contract.Principal, model string) error {
	keys, kerr := roleKeys(ctx, g.o.DB, p) // 이 사람이 가진 역할 키 전체(사용자 정의 역할 포함). 못 읽으면 거부(fail-closed)
	if kerr != nil {
		return fmt.Errorf("%w: 정책 조회 실패", contract.ErrModelNotAllowed)
	}
	rows, err := g.o.DB.QueryContext(ctx, `SELECT role, department_id, model_pattern, allowed FROM llm_model_policies`)
	if err != nil {
		return fmt.Errorf("%w: 정책 조회 실패", contract.ErrModelNotAllowed)
	}
	defer rows.Close()
	best, allowed, found := -1, false, false
	for rows.Next() {
		var r policyRule
		var a int
		if err := rows.Scan(&r.role, &r.dept, &r.pattern, &a); err != nil {
			return fmt.Errorf("%w: 정책 조회 실패", contract.ErrModelNotAllowed)
		}
		if r.role.Valid && !hasKey(keys, r.role.String) {
			continue
		}
		if r.dept.Valid && r.dept.Int64 != p.DepartmentID {
			continue
		}
		if !matchPattern(r.pattern, model) {
			continue
		}
		spec := 0
		if r.role.Valid {
			spec++
		}
		if r.dept.Valid {
			spec++
		}
		switch {
		case spec > best:
			best, allowed, found = spec, a == 1, true
		case spec == best && a == 0:
			allowed = false
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("%w: 정책 조회 실패", contract.ErrModelNotAllowed)
	}
	if !found || !allowed {
		return fmt.Errorf("%w: %s", contract.ErrModelNotAllowed, model)
	}
	return nil
}

// ---- 일일 한도 ----

func (g *Gateway) dayStart() string {
	n := g.o.Now().UTC()
	return time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, time.UTC).Format(time.RFC3339)
}

// reserve 는 가장 좁은 한도 규칙(user > department > role)을 골라 (오늘 사용량 + 예약)이 한도를 넘는지 보고,
// 넘지 않으면 같은 트랜잭션에서 예약 행을 넣는다. 규칙이 없으면 한도 없음(예약 행은 그대로 넣는다).
// 사용량에는 진행 중인 예약(status=ok 행)이 이미 들어 있으므로 동시 호출이 서로의 예약을 본다.
func (g *Gateway) reserve(ctx context.Context, p contract.Principal, req llm.Request, model string, rsv llm.Usage, masked int) (int64, error) {
	scopes := []struct{ typ, val, col string }{
		{"user", strconv.FormatInt(p.UserID, 10), "user_id"},
		{"department", strconv.FormatInt(p.DepartmentID, 10), "department_id"},
	}
	var id int64
	err := g.o.Txs.WithTx(ctx, func(tx contract.Tx) error {
		// PG 는 읽기 커밋이라 동시 예약이 같은 사용량을 읽고 함께 통과한다. 확인·예약 구간을 한 줄로 세운다(SQLite 는 연결 하나가 직렬화해 아무 일도 안 한다).
		if err := dbutil.AcquireTxLock(ctx, tx, g.dialect, dbutil.LockLLMQuota); err != nil {
			return fmt.Errorf("%w: 한도 조회 실패", contract.ErrQuotaExceeded)
		}
		keys, kerr := roleKeys(ctx, tx, p)
		if kerr != nil {
			return fmt.Errorf("%w: 한도 조회 실패", contract.ErrQuotaExceeded)
		}
		// 사용자 → 부서 중 먼저 있는 한도 하나(가장 좁은 범위). 둘 다 없으면 역할 한도: 가진 역할마다 한도가 있으면 모두 지켜야 한다.
		check := func(typ, val string, usedQuery string, usedArgs []any) (bool, error) {
			var limit int64
			err := tx.QueryRowContext(ctx,
				`SELECT daily_token_limit FROM llm_daily_quotas WHERE scope_type = ? AND scope_value = ?`, typ, val).Scan(&limit)
			if errors.Is(err, sql.ErrNoRows) {
				return false, nil
			}
			if err != nil {
				return false, fmt.Errorf("%w: 한도 조회 실패", contract.ErrQuotaExceeded)
			}
			var used int64
			if err := tx.QueryRowContext(ctx, usedQuery, usedArgs...).Scan(&used); err != nil {
				return true, fmt.Errorf("%w: 사용량 조회 실패", contract.ErrQuotaExceeded)
			}
			if used+int64(rsv.InputTokens+rsv.OutputTokens) > limit {
				return true, fmt.Errorf("%w: %s 일일 한도 %d 토큰", contract.ErrQuotaExceeded, typ, limit)
			}
			return true, nil
		}
		found := false
		for _, s := range scopes {
			n, _ := strconv.ParseInt(s.val, 10, 64)
			ok, err := check(s.typ, s.val,
				`SELECT COALESCE(SUM(input_tokens + output_tokens), 0) FROM llm_usage WHERE status = 'ok' AND at >= ? AND `+s.col+` = ?`, []any{g.dayStart(), n})
			if err != nil {
				return err
			}
			if ok {
				found = true
				break
			}
		}
		if !found {
			for _, k := range keys {
				if _, err := check("role", k,
					`SELECT COALESCE(SUM(input_tokens + output_tokens), 0) FROM llm_usage WHERE status = 'ok' AND at >= ? AND `+roleUsageFilter, []any{g.dayStart(), k, k}); err != nil {
					return err
				}
			}
		}
		usageRole := primaryRole(p, keys)
		var err error
		id, err = dbutil.InsertID(ctx, tx, `INSERT INTO llm_usage
			(at, user_id, department_id, role, provider, model, purpose, input_tokens, output_tokens, cost_micro_usd, masked_count, status, entity_id)
			VALUES (?,?,?,?,?,?,?,?,?,0,?,'ok',?)`,
			g.o.Now().UTC().Format(time.RFC3339), p.UserID, p.DepartmentID, usageRole, g.provName(ctx), model,
			req.Purpose, rsv.InputTokens, rsv.OutputTokens, masked, contract.EntityFrom(ctx))
		if err != nil {
			return fmt.Errorf("%w: 사용량 예약: %v", contract.ErrAuditFailed, err)
		}
		// 이미지 전송 감사는 예약과 같은 트랜잭션이다. 실패하면 예약도 롤백되어 전송하지 않는다.
		if err := g.recordImageSent(ctx, tx, p, req, model, id); err != nil {
			if !errors.Is(err, contract.ErrAuditFailed) {
				err = fmt.Errorf("%w: %v", contract.ErrAuditFailed, err)
			}
			return err
		}
		return nil
	})
	return id, err
}

// finalize 는 예약 행을 실제 사용량·비용·상태로 고치고 감사 로그를 같은 트랜잭션으로 남긴다.
func (g *Gateway) finalize(ctx context.Context, id int64, p contract.Principal, req llm.Request, model string, u llm.Usage, masked int, status, detail string) error {
	res := audit.ResultSuccess
	if status == "error" {
		res = audit.ResultError
	}
	actor := p
	actor.Channel = contract.ChannelLLMGW
	return g.o.Txs.WithTx(ctx, func(tx contract.Tx) error {
		if _, err := tx.ExecContext(ctx, `UPDATE llm_usage SET input_tokens = ?, output_tokens = ?, cost_micro_usd = ?, masked_count = ?, status = ?, provider = ?, model = ?,
			route_id = ?, attempts = ?, fallback_from = ?, fallback_reason = ?, latency_ms = ? WHERE id = ?`,
			u.InputTokens, u.OutputTokens, g.costCtx(ctx, u), masked, status, g.provName(ctx), model, g.statsRoute(ctx), g.statsAttempts(ctx), g.statsFrom(ctx), g.statsReason(ctx), g.statsLatency(ctx), id); err != nil {
			return fmt.Errorf("%w: 사용량 기록: %v", contract.ErrAuditFailed, err)
		}
		d := fmt.Sprintf("status=%s provider=%s model=%s purpose=%s in=%d out=%d masked=%d",
			status, g.provName(ctx), model, req.Purpose, u.InputTokens, u.OutputTokens, masked)
		if detail != "" {
			d += " " + detail
		}
		d += g.routeDetail(ctx)
		return g.o.Audit.Record(ctx, tx, audit.Entry{
			Actor: actor, Action: "llm.complete", TargetType: audit.TargetLLMCall,
			TargetID: strconv.FormatInt(id, 10), Result: res, Detail: d, Diff: g.routeDiff(ctx),
		})
	})
}

// ---- 사용량·감사 기록 ----

func (g *Gateway) cost(u llm.Usage) int64 {
	return (int64(u.InputTokens)*g.o.Prices.InPerMTok + int64(u.OutputTokens)*g.o.Prices.OutPerMTok) / 1_000_000
}

// record 는 차단·실패 기록이다. 기록 자체가 실패해도 원래 오류를 우선한다.
func (g *Gateway) record(ctx context.Context, p contract.Principal, req llm.Request, model string, u llm.Usage, masked int, status, detail string) {
	_ = g.recordStrict(ctx, p, req, model, u, masked, status, detail)
}

func (g *Gateway) recordStrict(ctx context.Context, p contract.Principal, req llm.Request, model string, u llm.Usage, masked int, status, detail string) error {
	res := audit.ResultSuccess
	switch status {
	case "blocked_quota", "blocked_model":
		res = audit.ResultDenied
	case "error":
		res = audit.ResultError
	}
	actor := p
	actor.Channel = contract.ChannelLLMGW
	return g.o.Txs.WithTx(ctx, func(tx contract.Tx) error {
		keys, kerr := roleKeys(ctx, tx, p)
		if kerr != nil {
			keys = nil // 대표 역할을 못 구해도 사용량 기록은 남긴다(호환용 Principal.Role 만 쓴다)
		}
		id, err := dbutil.InsertID(ctx, tx, `INSERT INTO llm_usage
			(at, user_id, department_id, role, provider, model, purpose, input_tokens, output_tokens, cost_micro_usd, masked_count, status, entity_id)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			g.o.Now().UTC().Format(time.RFC3339), p.UserID, p.DepartmentID, primaryRole(p, keys), g.provName(ctx), model,
			req.Purpose, u.InputTokens, u.OutputTokens, g.costCtx(ctx, u), masked, status, contract.EntityFrom(ctx))
		if err != nil {
			return fmt.Errorf("%w: 사용량 기록: %v", contract.ErrAuditFailed, err)
		}
		d := fmt.Sprintf("status=%s provider=%s model=%s purpose=%s in=%d out=%d masked=%d",
			status, g.provName(ctx), model, req.Purpose, u.InputTokens, u.OutputTokens, masked)
		if detail != "" {
			d += " " + detail
		}
		return g.o.Audit.Record(ctx, tx, audit.Entry{
			Actor: actor, Action: "llm.complete", TargetType: audit.TargetLLMCall,
			TargetID: strconv.FormatInt(id, 10), Result: res, Detail: d,
		})
	})
}
