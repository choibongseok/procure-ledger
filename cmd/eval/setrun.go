package main

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/choibongseok/procure-ledger/internal/audit"
	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/dbutil"
	"github.com/choibongseok/procure-ledger/internal/evalset"
	"github.com/choibongseok/procure-ledger/internal/rbac"
	"github.com/choibongseok/procure-ledger/migrations"
)

// 회사 평가 세트 실행(B46 2단계, 명세 7절): --set <코드>[@버전] --db <파일>.
// DB 의 게시된 세트를 공급자별로 돌리고 같은 저장 규칙(문항별 결과·감사)을 쓴다. 응답 전문은 출력하지도 저장하지도 않는다.
// 채점은 결정적 채점기뿐이다(llm 채점은 결정적 모의 채점기, 실제 LLM 채점은 아직 없다).

func parseSetArg(arg string) (code string, version int, err error) {
	code = arg
	if i := strings.Index(arg, "@"); i >= 0 {
		code = arg[:i]
		version, err = strconv.Atoi(arg[i+1:])
		if err != nil || version < 1 {
			return "", 0, fmt.Errorf("버전은 1 이상의 정수여야 합니다(--set 코드@버전)")
		}
	}
	if code == "" {
		return "", 0, fmt.Errorf("세트 코드가 비었습니다")
	}
	return code, version, nil
}

func runSetMode(setArg, provider, model, keyEnv, dbPath string, live bool, out, errw io.Writer) int {
	code, version, err := parseSetArg(setArg)
	if err != nil {
		fmt.Fprintln(errw, err)
		return 2
	}
	if dbPath == "" {
		fmt.Fprintln(errw, "--set 은 세트가 들어 있는 DB 가 필요합니다(--db <파일>)")
		return 2
	}
	specs := splitProviders(provider, model)
	if len(specs) == 0 {
		fmt.Fprintln(errw, "공급자가 없습니다(--provider)")
		return 2
	}
	ctx := context.Background()
	db, err := dbutil.Open(dbPath)
	if err != nil {
		fmt.Fprintln(errw, "DB 를 열 수 없습니다:", err)
		return 2
	}
	defer db.Close()
	if err := migrations.Apply(ctx, db); err != nil {
		fmt.Fprintln(errw, "마이그레이션 오류:", err)
		return 2
	}
	svc, err := evalset.New(evalset.Options{DB: db, Txs: contract.SQLTxManager{DB: db}, Audit: audit.New(db), Masker: rbac.Masker{}})
	if err != nil {
		fmt.Fprintln(errw, err)
		return 2
	}
	type setResult struct {
		label string
		view  evalset.RunView
		cost  int64
	}
	var results []setResult
	worst := 0
	for _, sp := range specs {
		prov, mdl, _, skip, err := buildEvalProvider(sp, keyEnv, live)
		if err != nil {
			fmt.Fprintln(errw, "공급자 설정 오류:", err)
			return 2
		}
		if skip != "" {
			fmt.Fprintln(out, "건너뜀:", skip)
			continue
		}
		mp := &meteredProvider{Provider: prov}
		label := sp.name
		if mdl != "" {
			label += "(" + mdl + ")"
		}
		v, err := svc.RunSync(ctx, contract.Principal{}, code, evalset.RunInput{Version: version, Model: mdl, Client: mp, Label: label, Mode: evalset.ModeCLI})
		if err != nil {
			fmt.Fprintln(errw, "세트 실행 오류:", err)
			return 2
		}
		cost := mp.costMicro(envInt64("PL_LLM_PRICE_IN_MICRO_PER_MTOK"), envInt64("PL_LLM_PRICE_OUT_MICRO_PER_MTOK"))
		printSetRun(out, v, cost)
		results = append(results, setResult{label: label, view: v, cost: cost})
		if v.Status != evalset.StatusDone || !v.GatePassed {
			worst = 1
		}
	}
	if len(results) >= 2 {
		var ls []string
		var vs []evalset.RunView
		for _, r := range results {
			ls, vs = append(ls, r.label), append(vs, r.view)
		}
		printSetCompare(out, ls, vs)
	}
	return worst
}

func printSetRun(w io.Writer, v evalset.RunView, cost int64) {
	fmt.Fprintf(w, "\n=== 평가 세트 %s v%d · %s ===\n", v.SetCode, v.SetVersion, v.Provider)
	if v.Status != evalset.StatusDone {
		fmt.Fprintf(w, "실패: %s\n", v.FailReason)
		return
	}
	verdict := "합격"
	if !v.GatePassed {
		verdict = "기준 미달"
	}
	fmt.Fprintf(w, "합격률 %d%% (%d/%d), 치명 실패 %d건, 지연 합 %dms(p95 %dms), 토큰 입력 %d·출력 %d, 비용 %d(1e-6 USD), 프롬프트 %s → %s\n",
		v.PassRatePct, v.Passed, v.Total, v.CriticalFails, v.LatencyMS, v.P95LatencyMS, v.InputTokens, v.OutputTokens, cost, v.PromptRef, verdict)
	if v.Warning != "" {
		fmt.Fprintln(w, "경고:", v.Warning)
	}
	for _, it := range v.Items {
		if !it.Pass {
			mark := ""
			if it.Critical {
				mark = " (치명)"
			}
			fmt.Fprintf(w, "  실패 문항 %d%s: %s\n", it.Seq, mark, it.Detail)
		}
	}
	fmt.Fprintf(w, "실행 번호 #%d 로 저장했습니다(%s)\n", v.ID, time.Now().UTC().Format("2006-01-02"))
}

// printSetCompare 는 문항 × 공급자 표다. 첫 공급자가 기준이고 기준에서 통과하던 문항이 실패하면 "실패(회귀)".
func printSetCompare(w io.Writer, labels []string, runs []evalset.RunView) {
	fmt.Fprintln(w, "\n=== 공급자별 세트 비교(참고용, 기준: 첫 공급자) ===")
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprint(tw, "문항")
	for _, l := range labels {
		fmt.Fprint(tw, "\t", l)
	}
	fmt.Fprintln(tw)
	state := make([]map[int]bool, len(runs))
	var order []int
	for i, r := range runs {
		state[i] = map[int]bool{}
		for _, it := range r.Items {
			state[i][it.Seq] = it.Pass
			if i == 0 {
				order = append(order, it.Seq)
			}
		}
	}
	for _, seq := range order {
		fmt.Fprint(tw, seq)
		for i := range runs {
			pass, has := state[i][seq]
			cell := "-"
			switch {
			case !has:
			case pass:
				cell = "통과"
			case i > 0 && state[0][seq]:
				cell = "실패(회귀)"
			default:
				cell = "실패"
			}
			fmt.Fprint(tw, "\t", cell)
		}
		fmt.Fprintln(tw)
	}
	fmt.Fprint(tw, "합격률")
	for _, r := range runs {
		fmt.Fprintf(tw, "\t%d%%", r.PassRatePct)
	}
	fmt.Fprintln(tw)
	_ = tw.Flush()
}
