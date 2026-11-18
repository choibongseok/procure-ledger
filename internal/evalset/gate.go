package evalset

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
)

// 게이트(B46 3단계, 명세 8절): 라우팅 저장 전에 지정 세트를 후보 경로로 돌려 기준선과 견준다.

// 게이트 결과 상태.
const (
	GatePass        = "pass"
	GateBlocked     = "blocked"
	GateSkipped     = "skipped"
	GateConfirmCost = "confirm_cost" // 외부 공급자 후보: 비용 확인 전에는 돌리지 않는다
)

// GateTimeout 은 게이트 실행 시간 상한이다. 넘으면 skipped. 시험이 줄인다.
var GateTimeout = 60 * time.Second

// Candidate 는 평가할 후보 경로다(주 공급자 코드와 모델).
type Candidate struct {
	Provider string
	Model    string
}

// GateResult 는 게이트 판정이다. Message 는 화면에 그대로 보이는 한국어 문장이다.
type GateResult struct {
	Status        string `json:"status"`
	Reason        string `json:"reason"`
	Message       string `json:"message"`
	SetCode       string `json:"setCode"`
	SetName       string `json:"setName"`
	Score         int    `json:"score"`
	BaselineScore int    `json:"baselineScore"`
	CriticalFails int    `json:"criticalFails"`
	DropPct       int    `json:"dropPct"`
	Items         int    `json:"items"`
	RunID         int64  `json:"runId"`
	External      bool   `json:"external"`
}

var gatePatternRe = regexp.MustCompile(`^(\*|[a-z0-9][a-z0-9_.-]{0,78}(\.\*)?)$`)

func splitPattern(p string) (prefix string, wild bool) {
	if strings.HasSuffix(p, ".*") {
		return strings.TrimSuffix(p, "*"), true
	}
	return p, false
}

// overlap 은 두 용도 패턴(정확한 라벨·접두 `x.*`·`*`)이 같은 용도를 겹쳐 가리키는지다.
func overlap(a, b string) bool {
	if a == "*" || b == "*" {
		return true
	}
	pa, wa := splitPattern(a)
	pb, wb := splitPattern(b)
	switch {
	case !wa && !wb:
		return pa == pb
	case wa && !wb:
		return strings.HasPrefix(pb, pa)
	case !wa && wb:
		return strings.HasPrefix(pa, pb)
	}
	return strings.HasPrefix(pa, pb) || strings.HasPrefix(pb, pa)
}

// GateView 는 게이트 지정 한 줄이다.
type GateView struct {
	ID             int64  `json:"id"`
	PurposePattern string `json:"purposePattern"`
	SetCode        string `json:"setCode"`
	SetName        string `json:"setName"`
	Enabled        bool   `json:"enabled"`
	GateDropPct    int    `json:"gateDropPct"`
}

// ListGates 는 게이트 지정 목록이다(eval:read).
func (s *Service) ListGates(ctx context.Context) ([]GateView, error) {
	if _, err := contract.Authorize(ctx, contract.PermEvalRead); err != nil {
		return nil, err
	}
	rows, err := s.o.DB.QueryContext(ctx, `SELECT g.id, g.purpose_label, s.code, s.name, g.enabled, s.gate_drop_pct FROM eval_gates g JOIN eval_sets s ON s.id = g.set_id ORDER BY g.purpose_label`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []GateView{}
	for rows.Next() {
		var g GateView
		var en int
		if err := rows.Scan(&g.ID, &g.PurposePattern, &g.SetCode, &g.SetName, &en, &g.GateDropPct); err != nil {
			return nil, err
		}
		g.Enabled = en == 1
		out = append(out, g)
	}
	return out, rows.Err()
}

// GateInput 은 게이트 지정 입력이다.
type GateInput struct {
	PurposePattern string `json:"purposePattern"`
	SetCode        string `json:"setCode"`
	Enabled        bool   `json:"enabled"`
}

// SaveGate 는 용도 패턴에 지정 세트를 두거나 바꾼다(eval:write). 같은 패턴은 한 줄이다.
func (s *Service) SaveGate(ctx context.Context, in GateInput) (GateView, error) {
	p, err := contract.Authorize(ctx, contract.PermEvalWrite)
	if err != nil {
		return GateView{}, err
	}
	in.PurposePattern, in.SetCode = strings.TrimSpace(in.PurposePattern), strings.TrimSpace(in.SetCode)
	if !gatePatternRe.MatchString(in.PurposePattern) {
		return GateView{}, valErr("용도 패턴은 정확한 라벨(예: docverify.extract), 접두(예: docverify.*) 또는 *(전체)여야 합니다.")
	}
	set, err := s.loadSet(ctx, s.o.DB, in.SetCode)
	if err != nil {
		if errors.Is(err, contract.ErrNotFound) {
			return GateView{}, valErr("평가 세트를 고르세요.")
		}
		return GateView{}, err
	}
	err = s.o.Txs.WithTx(ctx, func(tx contract.Tx) error {
		res, e := tx.ExecContext(ctx, `UPDATE eval_gates SET set_id = ?, enabled = ?, updated_by = ?, updated_at = ? WHERE purpose_label = ?`, set.ID, boolInt(in.Enabled), nullID(p.UserID), s.now(), in.PurposePattern)
		if e != nil {
			return e
		}
		if n, _ := res.RowsAffected(); n == 0 {
			if _, e = tx.ExecContext(ctx, `INSERT INTO eval_gates (purpose_label, set_id, enabled, updated_by, updated_at) VALUES (?,?,?,?,?)`, in.PurposePattern, set.ID, boolInt(in.Enabled), nullID(p.UserID), s.now()); e != nil {
				return e
			}
		}
		state := "꺼짐"
		if in.Enabled {
			state = "켜짐"
		}
		return s.o.Audit.Record(ctx, tx, audit.Entry{Actor: p, Action: "eval.gate_set", TargetType: audit.TargetEvalSet, TargetID: set.Code, Result: audit.ResultSuccess,
			Diff: []audit.DiffItem{{Key: "set_code", New: set.Code}, {Key: "route_purpose", New: in.PurposePattern}, {Key: "gate_drop_pct", New: strconv.Itoa(set.GateDropPct)}, {Key: "gate_enabled", New: state}}})
	})
	if err != nil {
		return GateView{}, err
	}
	gates, err := s.ListGates(ctx)
	if err != nil {
		return GateView{}, err
	}
	for _, g := range gates {
		if g.PurposePattern == in.PurposePattern {
			return g, nil
		}
	}
	return GateView{}, contract.ErrNotFound
}

func skipped(set SetView, reason string) GateResult {
	return GateResult{Status: GateSkipped, Reason: reason, SetCode: set.Code, SetName: set.Name,
		Message: "저장 전 평가를 건너뛰었습니다: " + reason}
}

// CheckGate 는 후보 경로를 지정 세트로 평가해 저장 전 판정을 낸다. 프롬프트 저장도 같은 함수를 부른다(호출 지점은 편집 기능이 생길 때).
// 오류는 돌려주지 않고 skipped 로 바꾼다: 평가 실패가 저장을 막아서는 안 된다(명세 8절 6항).
// 비용이 드는 외부 후보는 confirmCost 가 있어야 돌리고, 없으면 confirm_cost 를 돌려 확인을 요청한다.
// actor 는 실행 기록·감사의 주체다(eval:run 권한은 따로 묻지 않는다: 저장 권한이 곧 호출 권한이다).
func (s *Service) CheckGate(ctx context.Context, actor contract.Principal, purpose string, c Candidate, confirmCost bool) GateResult {
	set, drop, ok := s.gateFor(ctx, purpose)
	if !ok {
		return GateResult{Status: GateSkipped, Reason: "지정된 평가 세트 없음", Message: ""}
	}
	if !set.Active || set.CurrentVersion == 0 {
		return skipped(set, "지정한 세트가 꺼져 있거나 게시된 버전이 없습니다")
	}
	if set.BaselineRunID == 0 {
		return skipped(set, "기준선이 없습니다")
	}
	var bver, bn int
	if err := s.o.DB.QueryRowContext(ctx, `SELECT set_version, total FROM eval_runs WHERE id = ? AND status = 'done'`, set.BaselineRunID).Scan(&bver, &bn); err != nil {
		return skipped(set, "기준선 실행을 찾을 수 없습니다")
	}
	if c.Provider == "" {
		return skipped(set, "후보 공급자가 없습니다")
	}
	var external, active int
	if err := s.o.DB.QueryRowContext(ctx, `SELECT external, active FROM llm_providers WHERE code = ?`, c.Provider).Scan(&external, &active); err != nil {
		return skipped(set, "후보 공급자를 찾을 수 없습니다")
	}
	if active != 1 {
		return skipped(set, "후보 공급자가 꺼져 있습니다")
	}
	if external == 1 && s.o.Rehearsal {
		return skipped(set, "복구 리허설 중에는 외부 공급자로 평가할 수 없습니다")
	}
	if external == 1 && !confirmCost {
		return GateResult{Status: GateConfirmCost, External: true, SetCode: set.Code, SetName: set.Name,
			Message: "저장 전에 평가 세트 '" + set.Name + "' 를 후보 공급자로 돌립니다. 외부 공급자라 평가 실행에 비용이 들고 일일 한도를 씁니다. 계속하시겠습니까?"}
	}
	v, err := s.RunSync(ctx, actor, set.Code, RunInput{Provider: c.Provider, Model: c.Model, Mode: ModeGate, Limit: GateMaxItems, CallTimeout: GateTimeout})
	if err != nil {
		log.Printf("evalset: 게이트 실행 시작 실패: %v", err)
		return skipped(set, "평가를 시작하지 못했습니다")
	}
	if v.Status != StatusDone {
		reason := v.FailReason
		if reason == "" {
			reason = "평가가 끝나지 못했습니다"
		}
		if v.FailReason == "시간 초과" {
			reason = fmt.Sprintf("시간 초과(%d초), 확인 없이 저장할 수 있습니다", int(GateTimeout.Seconds()))
		}
		return skipped(set, reason)
	}
	base := s.baselineScoreFor(ctx, set, v)
	res := GateResult{SetCode: set.Code, SetName: set.Name, Score: v.PassRatePct, BaselineScore: base, CriticalFails: v.CriticalFails, DropPct: drop, Items: v.Total, RunID: v.ID, External: external == 1}
	if v.PassRatePct < base-drop || v.CriticalFails > 0 {
		res.Status = GateBlocked
		res.Message = fmt.Sprintf("기준선(%d%%)보다 낮습니다(%d%%, 치명 실패 %d건). 그래도 저장하시겠습니까?", base, v.PassRatePct, v.CriticalFails)
		return res
	}
	res.Status = GatePass
	res.Message = fmt.Sprintf("저장 전 평가를 통과했습니다(%d%%, 기준선 %d%%, %d문항).", v.PassRatePct, base, v.Total)
	return res
}

// gateFor 는 용도와 겹치는 켜진 지정 세트를 고른다(더 구체적인 패턴 우선).
func (s *Service) gateFor(ctx context.Context, purpose string) (SetView, int, bool) {
	rows, err := s.o.DB.QueryContext(ctx, `SELECT g.purpose_label, s.code FROM eval_gates g JOIN eval_sets s ON s.id = g.set_id WHERE g.enabled = 1`)
	if err != nil {
		return SetView{}, 0, false
	}
	type cand struct{ pattern, code string }
	var cands []cand
	for rows.Next() {
		var c cand
		if rows.Scan(&c.pattern, &c.code) == nil && overlap(c.pattern, purpose) {
			cands = append(cands, c)
		}
	}
	rows.Close()
	if len(cands) == 0 {
		return SetView{}, 0, false
	}
	sort.SliceStable(cands, func(i, j int) bool {
		wi := strings.HasSuffix(cands[i].pattern, "*")
		wj := strings.HasSuffix(cands[j].pattern, "*")
		if wi != wj {
			return !wi
		}
		return len(cands[i].pattern) > len(cands[j].pattern)
	})
	set, err := s.loadSet(ctx, s.o.DB, cands[0].code)
	if err != nil {
		return SetView{}, 0, false
	}
	return set, set.GateDropPct, true
}

// baselineScoreFor 는 비교할 기준선 점수다. 기준선이 같은 버전이면 게이트가 돈 앞쪽 문항에서의 합격률, 아니면 기준선 합격률 그대로다.
func (s *Service) baselineScoreFor(ctx context.Context, set SetView, v RunView) int {
	var bver, bpct int
	if err := s.o.DB.QueryRowContext(ctx, `SELECT set_version, pass_rate_pct FROM eval_runs WHERE id = ?`, set.BaselineRunID).Scan(&bver, &bpct); err != nil {
		return 0
	}
	if bver != v.SetVersion {
		return bpct
	}
	var n, passed int
	err := s.o.DB.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(pass), 0) FROM eval_run_items WHERE run_id = ? AND item_seq IN (SELECT item_seq FROM eval_run_items WHERE run_id = ?)`, set.BaselineRunID, v.ID).Scan(&n, &passed)
	if err != nil && !errors.Is(err, sql.ErrNoRows) || n == 0 {
		return bpct
	}
	return passed * 100 / n
}
