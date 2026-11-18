package llmgw

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/choibongseok/procure-ledger/internal/dbutil"
	"strconv"
	"strings"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
)

// ActionEvalRun 은 평가 실행 기록 감사 동작이다.
const ActionEvalRun = "eval.run"

// EvalCase 는 평가 실행 한 건 안의 케이스 결과다.
type EvalCase struct {
	Suite    string `json:"suite"` // docs | tools
	ID       string `json:"id"`
	Pass     bool   `json:"pass"`
	Critical bool   `json:"critical"`
	Detail   string `json:"detail"`
}

// EvalRun 은 평가 실행 한 건의 요약이다(관리자 화면의 이력 표 한 줄).
type EvalRun struct {
	ID           int64      `json:"id"`
	At           string     `json:"at"`
	Provider     string     `json:"provider"`
	Model        string     `json:"model"`
	Total        int        `json:"total"`
	Passed       int        `json:"passed"`
	PassRatePct  int        `json:"passRatePct"`
	ThresholdPct int        `json:"thresholdPct"`
	GatePassed   bool       `json:"gatePassed"`
	NewFailures  []string   `json:"newFailures"`
	NewPasses    []string   `json:"newPasses"`
	Cases        []EvalCase `json:"cases"`
	// 비교용(B45 3단계): 같은 골든셋인지 가리는 해시와 합계(지연 ms·비용 1e-6 USD).
	SuiteHash    string `json:"suiteHash"`
	LatencyMS    int64  `json:"latencyMs"`
	CostMicroUSD int64  `json:"costMicroUsd"`
}

// evalActor 는 평가 실행을 기록하는 시스템 주체다(사용자 없는 사건은 actor 0).
var evalActor = contract.Principal{Role: "system", Channel: contract.ChannelLLMGW}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// RecordEvalRun 은 평가 실행을 저장하고 감사 로그를 같은 트랜잭션에 남긴다. 저장된 id 를 돌려준다.
func RecordEvalRun(ctx context.Context, txs contract.TxManager, rec audit.Recorder, run EvalRun) (int64, error) {
	if run.At == "" {
		run.At = time.Now().UTC().Format(time.RFC3339)
	}
	failures, _ := json.Marshal(nonNil(run.NewFailures))
	passes, _ := json.Marshal(nonNil(run.NewPasses))
	cases := run.Cases
	if cases == nil {
		cases = []EvalCase{}
	}
	results, _ := json.Marshal(cases)
	gate := 0
	if run.GatePassed {
		gate = 1
	}
	var id int64
	err := txs.WithTx(ctx, func(tx contract.Tx) error {
		var err error
		id, err = dbutil.InsertID(ctx, tx, `INSERT INTO eval_runs (at, provider, model, total, passed, pass_rate_pct, threshold_pct, gate_passed,
			new_failures_json, new_passes_json, results_json, suite_hash, latency_ms_total, cost_micro_usd_total) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			run.At, run.Provider, run.Model, run.Total, run.Passed, run.PassRatePct, run.ThresholdPct, gate, string(failures), string(passes), string(results), run.SuiteHash, run.LatencyMS, run.CostMicroUSD)
		if err != nil {
			return fmt.Errorf("평가 실행 저장: %w", err)
		}
		return rec.Record(ctx, tx, audit.Entry{Actor: evalActor, Action: ActionEvalRun, TargetType: audit.TargetEval, TargetID: strconv.FormatInt(id, 10),
			Result: audit.ResultSuccess, Diff: []audit.DiffItem{
				{Key: "pass_rate_pct", New: strconv.Itoa(run.PassRatePct)},
				{Key: "new_failures", New: strings.Join(nonNil(run.NewFailures), ",")},
				{Key: "provider", New: run.Provider},
			}})
	})
	return id, err
}

// ListEvalRuns 는 최근 평가 실행 이력이다(최신 순). eval:read 가 필요하다.
func (s *Store) ListEvalRuns(ctx context.Context, limit int) ([]EvalRun, error) {
	if _, err := contract.Authorize(ctx, contract.PermEvalRead); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 200 {
		limit = 30
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT id, at, provider, model, total, passed, pass_rate_pct, threshold_pct, gate_passed,
		new_failures_json, new_passes_json, results_json, suite_hash, latency_ms_total, cost_micro_usd_total FROM eval_runs WHERE status = 'done' ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []EvalRun{}
	for rows.Next() {
		var r EvalRun
		var gate int
		var failures, passes, results string
		if err := rows.Scan(&r.ID, &r.At, &r.Provider, &r.Model, &r.Total, &r.Passed, &r.PassRatePct, &r.ThresholdPct, &gate, &failures, &passes, &results, &r.SuiteHash, &r.LatencyMS, &r.CostMicroUSD); err != nil {
			return nil, err
		}
		r.GatePassed = gate == 1
		r.NewFailures, r.NewPasses, r.Cases = []string{}, []string{}, []EvalCase{}
		_ = json.Unmarshal([]byte(failures), &r.NewFailures)
		_ = json.Unmarshal([]byte(passes), &r.NewPasses)
		_ = json.Unmarshal([]byte(results), &r.Cases)
		out = append(out, r)
	}
	return out, rows.Err()
}
