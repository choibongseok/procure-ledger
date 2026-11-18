package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/choibongseok/procure-ledger/evals"
	"github.com/choibongseok/procure-ledger/internal/audit"
	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/dbutil"
	"github.com/choibongseok/procure-ledger/internal/llmgw"
	"github.com/choibongseok/procure-ledger/migrations"
)

// 기본 기준선 파일 경로(저장소 루트 기준). 갱신 플래그를 쓸 때만 이 경로에 쓴다.
const defaultBaselineFile = "evals/baseline.json"

// States 는 이번 실행의 케이스별 통과 여부다.
func (r Report) States() []evals.CaseState {
	out := make([]evals.CaseState, 0, len(r.Results))
	for _, c := range r.Results {
		out = append(out, evals.CaseState{Suite: c.Suite, ID: c.ID, Pass: c.Pass})
	}
	return out
}

func (r Report) suites() map[string]bool {
	m := map[string]bool{}
	for _, g := range r.gates() {
		m[g.suite] = true
	}
	return m
}

// summary 는 전체 합격 수·합격률(%, 반올림)·가장 높은 기준 임계치(%)다.
func (r Report) summary() (total, passed, ratePct, thresholdPct int) {
	for _, c := range r.Results {
		total++
		if c.Pass {
			passed++
		}
	}
	if total > 0 {
		ratePct = int(math.Round(float64(passed) * 100 / float64(total)))
	}
	for _, g := range r.gates() {
		if t := int(math.Round(g.threshold * 100)); t > thresholdPct {
			thresholdPct = t
		}
	}
	return
}

// resolveBaseline 은 비교할 기준선을 고른다. --baseline 이 있으면 그 파일, 없으면 mock 공급자에 한해
// 바이너리에 내장된(= 커밋된) 기준선이다. 다른 공급자는 점수 분포가 달라 기본 기준선과 비교하지 않는다.
func resolveBaseline(path, provider string) (*evals.Baseline, string, error) {
	if path != "" {
		b, err := evals.ReadBaseline(path)
		if err != nil {
			return nil, path, err
		}
		return &b, path, nil
	}
	if provider != "mock" {
		return nil, "", nil
	}
	b, err := evals.LoadBaseline()
	if err != nil {
		return nil, "", err
	}
	return &b, defaultBaselineFile, nil
}

func printDiff(w io.Writer, src string, d evals.Diff) {
	fmt.Fprintf(w, "기준선(%s) 대비\n", src)
	if len(d.NewFailures) == 0 {
		fmt.Fprintln(w, "  새로 실패한 케이스: 없음")
	} else {
		fmt.Fprintf(w, "  새로 실패한 케이스: %s\n", strings.Join(d.NewFailures, ", "))
	}
	if len(d.NewPasses) > 0 {
		fmt.Fprintf(w, "  새로 통과한 케이스: %s\n", strings.Join(d.NewPasses, ", "))
	}
	if len(d.Removed) > 0 {
		fmt.Fprintf(w, "  경고: 기준선에는 있으나 이번 실행에 없는 케이스: %s\n", strings.Join(d.Removed, ", "))
	}
}

// saveRun 은 실행 결과를 DB 에 이력으로 남긴다(관리자 화면 표의 원본). 서버 DB 파일을 --db 로 지정한다.
func saveRun(ctx context.Context, dbPath, provider, model string, rep Report, diff evals.Diff, gate bool, suiteHash string, latencyMS, costMicro int64) (int64, error) {
	db, err := dbutil.Open(dbPath)
	if err != nil {
		return 0, err
	}
	defer db.Close()
	if err := migrations.Apply(ctx, db); err != nil {
		return 0, err
	}
	total, passed, rate, thr := rep.summary()
	run := llmgw.EvalRun{At: time.Now().UTC().Format(time.RFC3339), Provider: provider, Model: model, Total: total, Passed: passed,
		PassRatePct: rate, ThresholdPct: thr, GatePassed: gate, NewFailures: diff.NewFailures, NewPasses: diff.NewPasses,
		SuiteHash: suiteHash, LatencyMS: latencyMS, CostMicroUSD: costMicro}
	for _, c := range rep.Results {
		run.Cases = append(run.Cases, llmgw.EvalCase{Suite: c.Suite, ID: c.ID, Pass: c.Pass, Critical: c.Critical, Detail: c.Detail})
	}
	return llmgw.RecordEvalRun(ctx, contract.SQLTxManager{DB: db}, audit.New(db), run)
}

// writeBaseline 은 이번 결과를 기준선 파일로 저장한다.
func writeBaseline(path, provider string, rep Report) error {
	if dir := filepath.Dir(path); dir != "." {
		if st, err := os.Stat(dir); err != nil || !st.IsDir() {
			return fmt.Errorf("기준선을 쓸 폴더가 없습니다(%s). 저장소 루트에서 실행하거나 --baseline 으로 경로를 지정하세요", dir)
		}
	}
	b := evals.Baseline{Version: evals.BaselineVersion, Provider: provider, Cases: rep.States()}
	data, err := b.Marshal()
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// finishOptions 는 결과 채점 뒤의 처리(기준선 비교·갱신, 이력 저장) 설정이다.
type finishOptions struct {
	Provider     string
	Model        string
	BaselinePath string
	Update       bool
	Only         string
	DBPath       string
	SuiteHash    string
	LatencyMS    int64
	CostMicro    int64
}

// finish 는 합격 기준과 기준선 비교를 합쳐 종료 코드를 정한다: 0 통과, 1 기준 미달 또는 회귀, 2 설정 오류.
func finish(rep Report, o finishOptions, out, errw io.Writer) int {
	base, baseSrc, err := resolveBaseline(o.BaselinePath, o.Provider)
	if err != nil && o.Update && errors.Is(err, fs.ErrNotExist) {
		base, err = nil, nil // 갱신 대상 파일이 아직 없으면 새로 만든다.
	}
	if err != nil {
		fmt.Fprintln(errw, "기준선 오류:", err)
		return 2
	}
	var diff evals.Diff
	if base != nil {
		diff = evals.Compare(*base, rep.States(), rep.suites())
		fmt.Fprintln(out)
		printDiff(out, baseSrc, diff)
	}
	regressed := len(diff.NewFailures) > 0
	if regressed {
		fmt.Fprintln(out, "결과: 회귀 감지(기준선 대비 새로 실패한 케이스가 있습니다)")
	}

	if o.Update {
		if o.Only != "" {
			fmt.Fprintln(errw, "--only 로 일부만 돌린 결과로는 기준선을 갱신할 수 없습니다.")
			return 2
		}
		if !rep.Passed() {
			fmt.Fprintln(errw, "합격하지 못한 결과는 기준선으로 저장하지 않습니다.")
			return 1
		}
		path := o.BaselinePath
		if path == "" {
			path = defaultBaselineFile
		}
		if err := writeBaseline(path, o.Provider, rep); err != nil {
			fmt.Fprintln(errw, "기준선 저장 오류:", err)
			return 2
		}
		fmt.Fprintf(out, "기준선을 갱신했습니다: %s (%d건)\n", path, len(rep.Results))
		regressed = false // 갱신은 이번 결과를 새 기준으로 받아들인다는 명시적 선택이다.
	}

	ok := rep.Passed() && !regressed
	if o.DBPath != "" {
		id, err := saveRun(context.Background(), o.DBPath, o.Provider, o.Model, rep, diff, ok, o.SuiteHash, o.LatencyMS, o.CostMicro)
		if err != nil {
			fmt.Fprintln(errw, "이력 저장 오류:", err)
			return 2
		}
		fmt.Fprintf(out, "실행 이력을 저장했습니다: #%d (%s)\n", id, o.DBPath)
	}
	if !ok {
		return 1
	}
	return 0
}
