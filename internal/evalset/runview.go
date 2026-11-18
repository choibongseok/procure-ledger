package evalset

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
)

// RunItemView 는 문항별 결과다. 응답 전문은 없다(명세 7절).
type RunItemView struct {
	Seq       int    `json:"seq"`
	Question  string `json:"question"` // 앞부분(마스킹된 글)
	Expected  string `json:"expected"`
	Pass      bool   `json:"pass"`
	Score     int    `json:"score"`
	Critical  bool   `json:"critical"`
	LatencyMS int64  `json:"latencyMs"`
	Detail    string `json:"detail"`
}

// RunView 는 실행 한 건의 요약과(상세 조회 시) 문항 결과다.
type RunView struct {
	ID            int64         `json:"id"`
	At            string        `json:"at"`
	SetCode       string        `json:"setCode"`
	SetName       string        `json:"setName"`
	SetVersion    int           `json:"setVersion"`
	PromptRef     string        `json:"promptRef"`
	Provider      string        `json:"provider"`
	Model         string        `json:"model"`
	Mode          string        `json:"mode"`
	Status        string        `json:"status"`
	FailReason    string        `json:"failReason"`
	Total         int           `json:"total"`
	DoneItems     int           `json:"doneItems"`
	Passed        int           `json:"passed"`
	PassRatePct   int           `json:"passRatePct"`
	ThresholdPct  int           `json:"thresholdPct"`
	CriticalFails int           `json:"criticalFails"`
	GatePassed    bool          `json:"gatePassed"`
	AvgScore      int           `json:"avgScore"`
	LatencyMS     int64         `json:"latencyMs"`
	AvgLatencyMS  int64         `json:"avgLatencyMs"`
	P95LatencyMS  int64         `json:"p95LatencyMs"`
	InputTokens   int64         `json:"inputTokens"`
	OutputTokens  int64         `json:"outputTokens"`
	CostMicroUSD  int64         `json:"costMicroUsd"`
	Baseline      bool          `json:"baseline"`
	BaselinePct   int           `json:"baselinePct"` // 기준선 합격률(없으면 -1)
	Warning       string        `json:"warning"`
	Items         []RunItemView `json:"items"`
}

// p95 는 정렬 뒤 ceil(0.95·n) 번째 값이다(명세 14절).
func p95(v []int64) int64 {
	if len(v) == 0 {
		return 0
	}
	s := append([]int64(nil), v...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	idx := int(math.Ceil(0.95*float64(len(s)))) - 1
	if idx < 0 {
		idx = 0
	}
	return s[idx]
}

func (s *Service) loadRun(ctx context.Context, id int64, withItems bool) (RunView, error) {
	v := RunView{BaselinePct: -1, Items: []RunItemView{}}
	var gate int
	var setID int64
	err := s.o.DB.QueryRowContext(ctx, `SELECT id, at, set_id, set_version, prompt_ref, provider, model, mode, status, fail_reason, total, done_items, passed, pass_rate_pct, threshold_pct, gate_passed,
		latency_ms_total, cost_micro_usd_total FROM eval_runs WHERE id = ? AND set_id > 0`, id).
		Scan(&v.ID, &v.At, &setID, &v.SetVersion, &v.PromptRef, &v.Provider, &v.Model, &v.Mode, &v.Status, &v.FailReason, &v.Total, &v.DoneItems, &v.Passed, &v.PassRatePct, &v.ThresholdPct, &gate, &v.LatencyMS, &v.CostMicroUSD)
	if errors.Is(err, sql.ErrNoRows) {
		return v, contract.ErrNotFound
	}
	if err != nil {
		return v, err
	}
	v.GatePassed = gate == 1
	var baseID int64
	var drop int
	if err := s.o.DB.QueryRowContext(ctx, `SELECT code, name, baseline_run_id, gate_drop_pct FROM eval_sets WHERE id = ?`, setID).Scan(&v.SetCode, &v.SetName, &baseID, &drop); err != nil {
		return v, err
	}
	v.Baseline = baseID == v.ID
	rows, err := s.o.DB.QueryContext(ctx, `SELECT ri.item_seq, ri.pass, ri.score, ri.latency_ms, ri.input_tokens, ri.output_tokens, ri.detail, COALESCE(i.question, ''), COALESCE(i.expected, ''), COALESCE(i.critical, 0)
		FROM eval_run_items ri
		LEFT JOIN eval_set_versions sv ON sv.set_id = ? AND sv.version = ?
		LEFT JOIN eval_items i ON i.version_id = sv.id AND i.seq = ri.item_seq
		WHERE ri.run_id = ? ORDER BY ri.item_seq`, setID, v.SetVersion, id)
	if err != nil {
		return v, err
	}
	var lats []int64
	var scoreSum int
	for rows.Next() {
		var it RunItemView
		var pass, crit int
		var in, out int64
		if err := rows.Scan(&it.Seq, &pass, &it.Score, &it.LatencyMS, &in, &out, &it.Detail, &it.Question, &it.Expected, &crit); err != nil {
			rows.Close()
			return v, err
		}
		it.Pass, it.Critical = pass == 1, crit == 1
		it.Question, it.Expected = clip(it.Question, 80), clip(it.Expected, 80)
		v.InputTokens += in
		v.OutputTokens += out
		lats = append(lats, it.LatencyMS)
		scoreSum += it.Score
		if !it.Pass && it.Critical {
			v.CriticalFails++
		}
		if withItems {
			v.Items = append(v.Items, it)
		}
	}
	if err := rows.Close(); err != nil {
		return v, err
	}
	if n := len(lats); n > 0 {
		v.AvgScore = scoreSum / n
		v.P95LatencyMS = p95(lats)
	}
	if v.Total > 0 && v.Status == StatusDone {
		v.AvgLatencyMS = v.LatencyMS / int64(v.Total)
	}
	if baseID != 0 && baseID != v.ID {
		var bpct int
		if err := s.o.DB.QueryRowContext(ctx, `SELECT pass_rate_pct FROM eval_runs WHERE id = ?`, baseID).Scan(&bpct); err == nil {
			v.BaselinePct = bpct
		}
	}
	if v.Status == StatusDone {
		var parts []string
		if v.BaselinePct >= 0 && v.BaselinePct-v.PassRatePct > drop {
			parts = append(parts, fmt.Sprintf("기준선(%d%%)보다 %d%%p 낮습니다(허용 %d%%p)", v.BaselinePct, v.BaselinePct-v.PassRatePct, drop))
		}
		if v.CriticalFails > 0 {
			parts = append(parts, fmt.Sprintf("치명 문항 %d건이 실패했습니다", v.CriticalFails))
		}
		v.Warning = strings.Join(parts, ". ")
	}
	return v, nil
}

// ListRuns 는 세트 실행 이력이다(최신 순, eval:read). setCode 가 비면 모든 세트.
func (s *Service) ListRuns(ctx context.Context, setCode string, limit int) ([]RunView, error) {
	if _, err := contract.Authorize(ctx, contract.PermEvalRead); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	q := `SELECT r.id FROM eval_runs r JOIN eval_sets s ON s.id = r.set_id`
	var args []any
	if setCode != "" {
		q += ` WHERE s.code = ?`
		args = append(args, setCode)
	}
	q += ` ORDER BY r.id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.o.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	out := []RunView{}
	for _, id := range ids {
		v, err := s.loadRun(ctx, id, false)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

// GetRun 은 실행 상세다(문항 결과 포함, eval:read).
func (s *Service) GetRun(ctx context.Context, id int64) (RunView, error) {
	if _, err := contract.Authorize(ctx, contract.PermEvalRead); err != nil {
		return RunView{}, err
	}
	return s.loadRun(ctx, id, true)
}

// CompareRow 는 비교 표의 한 줄(문항)이다. Change 는 "regression"(기준 통과 → 실패)·"improvement"(기준 실패 → 통과)·"".
type CompareRow struct {
	Seq      int    `json:"seq"`
	Question string `json:"question"`
	APass    bool   `json:"aPass"`
	BPass    bool   `json:"bPass"`
	Change   string `json:"change"`
}

// Comparison 은 같은 세트의 두 실행 비교다. A 가 기준이다.
type Comparison struct {
	A            RunView      `json:"a"`
	B            RunView      `json:"b"`
	SameVersion  bool         `json:"sameVersion"`
	Note         string       `json:"note"`
	Regressions  int          `json:"regressions"`
	Improvements int          `json:"improvements"`
	Rows         []CompareRow `json:"rows"`
}

// Compare 는 두 실행을 비교한다(eval:read). 버전이 같을 때만 문항별로 비교한다(명세 7절).
func (s *Service) Compare(ctx context.Context, aID, bID int64) (Comparison, error) {
	if _, err := contract.Authorize(ctx, contract.PermEvalRead); err != nil {
		return Comparison{}, err
	}
	a, err := s.loadRun(ctx, aID, true)
	if err != nil {
		return Comparison{}, err
	}
	b, err := s.loadRun(ctx, bID, true)
	if err != nil {
		return Comparison{}, err
	}
	if a.SetCode != b.SetCode {
		return Comparison{}, valErr("같은 세트의 실행끼리만 비교할 수 있습니다.")
	}
	if a.Status != StatusDone || b.Status != StatusDone {
		return Comparison{}, stateErr("끝난 실행끼리만 비교할 수 있습니다.")
	}
	c := Comparison{A: a, B: b, SameVersion: a.SetVersion == b.SetVersion, Rows: []CompareRow{}}
	if !c.SameVersion {
		c.Note = "문항이 달라 합계만 비교합니다."
		return c, nil
	}
	bm := map[int]RunItemView{}
	for _, it := range b.Items {
		bm[it.Seq] = it
	}
	for _, ai := range a.Items {
		bi, ok := bm[ai.Seq]
		if !ok {
			continue
		}
		row := CompareRow{Seq: ai.Seq, Question: ai.Question, APass: ai.Pass, BPass: bi.Pass}
		switch {
		case ai.Pass && !bi.Pass:
			row.Change = "regression"
			c.Regressions++
		case !ai.Pass && bi.Pass:
			row.Change = "improvement"
			c.Improvements++
		}
		c.Rows = append(c.Rows, row)
	}
	return c, nil
}

// SetBaseline 은 세트의 기준선 실행을 지정한다(eval:write). runID 0 이면 해제. 끝난 실행만 된다.
// 다른 버전의 실행이면 지정은 되고 경고 글을 돌려준다.
func (s *Service) SetBaseline(ctx context.Context, code string, runID int64) (string, error) {
	p, err := contract.Authorize(ctx, contract.PermEvalWrite)
	if err != nil {
		return "", err
	}
	set, err := s.loadSet(ctx, s.o.DB, code)
	if err != nil {
		return "", err
	}
	warn, pct := "", 0
	if runID != 0 {
		r, err := s.loadRun(ctx, runID, false)
		if err != nil || r.SetCode != code {
			return "", valErr("이 세트의 실행이 아닙니다.")
		}
		if r.Status != StatusDone {
			return "", stateErr("끝난 실행만 기준선으로 지정할 수 있습니다.")
		}
		pct = r.PassRatePct
		if r.SetVersion != set.CurrentVersion {
			warn = "버전이 다른 실행입니다. 문항이 달라 점수 비교가 거칩니다."
		}
	}
	err = s.o.Txs.WithTx(ctx, func(tx contract.Tx) error {
		if _, e := tx.ExecContext(ctx, `UPDATE eval_sets SET baseline_run_id = ?, updated_at = ? WHERE id = ?`, runID, s.now(), set.ID); e != nil {
			return e
		}
		return s.o.Audit.Record(ctx, tx, audit.Entry{Actor: p, Action: "eval.baseline_set", TargetType: audit.TargetEvalSet, TargetID: code, Result: audit.ResultSuccess,
			Diff: []audit.DiffItem{{Key: "set_code", New: code}, {Key: "baseline_run", Old: strconv.FormatInt(set.BaselineRunID, 10), New: strconv.FormatInt(runID, 10)}, {Key: "pass_rate_pct", New: strconv.Itoa(pct)}}})
	})
	return warn, err
}

// ProviderChoice 는 실행 화면의 공급자 선택지다.
type ProviderChoice struct {
	Code         string `json:"code"`
	Name         string `json:"name"`
	DefaultModel string `json:"defaultModel"`
	External     bool   `json:"external"`
}

// Providers 는 켜져 있는 등록 공급자 목록이다(eval:run).
func (s *Service) Providers(ctx context.Context) ([]ProviderChoice, error) {
	if _, err := contract.Authorize(ctx, contract.PermEvalRun); err != nil {
		return nil, err
	}
	rows, err := s.o.DB.QueryContext(ctx, `SELECT code, name, default_model, external FROM llm_providers WHERE active = 1 ORDER BY code`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ProviderChoice{}
	for rows.Next() {
		var c ProviderChoice
		var ext int
		if err := rows.Scan(&c.Code, &c.Name, &c.DefaultModel, &ext); err != nil {
			return nil, err
		}
		c.External = ext == 1
		out = append(out, c)
	}
	return out, rows.Err()
}
