package main

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/choibongseok/procure-ledger/evals"
	"github.com/choibongseok/procure-ledger/internal/access"
	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/fake"
	"github.com/choibongseok/procure-ledger/internal/dbutil"
	"github.com/choibongseok/procure-ledger/internal/docverify"
	"github.com/choibongseok/procure-ledger/internal/docverify/kit"
	"github.com/choibongseok/procure-ledger/internal/llmgw"
	"github.com/choibongseok/procure-ledger/internal/rbac"
	"github.com/choibongseok/procure-ledger/migrations"
)

// Config 는 평가 실행 설정이다.
type Config struct {
	Provider      llmgw.Provider
	Model         string
	PolicyPattern string
	Only          string // "", "docs", "tools", "policy", "quotes", "knowledge", "journals"
}

// CaseResult 는 케이스 한 건의 채점 결과다.
type CaseResult struct {
	Suite    string // docs | tools
	ID       string
	Pass     bool
	Critical bool
	Detail   string
}

// Report 는 평가 전체 결과다.
type Report struct {
	Results []CaseResult
	Failure []string // 케이스 수 하한 같은 구조적 실패
	only    string
}

func (r Report) counts(suite string) (pass, total int) {
	for _, c := range r.Results {
		if c.Suite != suite {
			continue
		}
		total++
		if c.Pass {
			pass++
		}
	}
	return
}

func rate(p, t int) float64 {
	if t == 0 {
		return 0
	}
	return float64(p) / float64(t)
}

type gate struct {
	suite     string
	label     string
	threshold float64
	min       int
}

func (r Report) gates() []gate {
	var gs []gate
	if r.only == "" || r.only == "docs" {
		gs = append(gs, gate{"docs", "문서 판정", evals.DocPassRate, evals.MinDocCases})
	}
	if r.only == "" || r.only == "tools" {
		gs = append(gs, gate{"tools", "도구 선택", evals.ToolPassRate, evals.MinToolCases})
	}
	if r.only == "" || r.only == "policy" {
		gs = append(gs, gate{"policy", "규정 검색", evals.PolicyPassRate, evals.MinPolicyCases})
	}
	if r.only == "" || r.only == "quotes" {
		gs = append(gs, gate{"quotes", "견적서 추출", evals.QuotePassRate, evals.MinQuoteCases})
	}
	if r.only == "" || r.only == "knowledge" {
		gs = append(gs, gate{"knowledge", "질문하기", evals.KnowledgePassRate, evals.MinKnowledgeCases})
	}
	// 자동 전표 묶음은 전표 서비스가 연결되어 실제로 돌았을 때만 합격 기준에 든다(연결 전에는 건너뛴다).
	if _, ran := r.counts("journals"); ran > 0 {
		gs = append(gs, gate{"journals", "자동 전표", evals.JournalPassRate, evals.MinJournalCases})
	}
	return gs
}

// Passed 는 모든 합격 기준을 만족했는지다: 합격률 ≥ 임계치, 케이스 수 ≥ 하한, critical 케이스 전부 통과.
func (r Report) Passed() bool {
	if len(r.Failure) > 0 {
		return false
	}
	for _, g := range r.gates() {
		p, t := r.counts(g.suite)
		if t < g.min || rate(p, t) < g.threshold {
			return false
		}
	}
	for _, c := range r.Results {
		if c.Critical && !c.Pass {
			return false
		}
	}
	return true
}

// Print 는 케이스별 표와 요약을 출력한다.
func (r Report) Print(w io.Writer, provider string) {
	fmt.Fprintf(w, "ProcureLedger 평가 (공급자: %s)\n", provider)
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "구분\t케이스\t결과\t비고")
	for _, c := range r.Results {
		res := "통과"
		if !c.Pass {
			res = "실패"
		}
		if c.Critical {
			res += "(필수)"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", c.Suite, c.ID, res, c.Detail)
	}
	_ = tw.Flush()
	fmt.Fprintln(w)
	for _, g := range r.gates() {
		p, t := r.counts(g.suite)
		status := "통과"
		if t < g.min {
			status = fmt.Sprintf("미달(케이스 %d건 < 하한 %d건)", t, g.min)
		} else if rate(p, t) < g.threshold {
			status = "미달"
		}
		fmt.Fprintf(w, "%s: %d/%d (%.1f%%) 기준 %.0f%% → %s\n", g.label, p, t, rate(p, t)*100, g.threshold*100, status)
	}
	for _, c := range r.Results {
		if c.Critical && !c.Pass {
			fmt.Fprintf(w, "필수 케이스 실패: %s\n", c.ID)
		}
	}
	for _, f := range r.Failure {
		fmt.Fprintln(w, "실패:", f)
	}
	if r.Passed() {
		fmt.Fprintln(w, "결과: 합격")
	} else {
		fmt.Fprintln(w, "결과: 불합격")
	}
}

// Run 은 골든셋을 게이트웨이(허용 목록·한도·마스킹·감사 경유)로 돌려 채점한다.
func Run(ctx context.Context, cfg Config) (Report, error) {
	rep := Report{only: cfg.Only}
	db, err := dbutil.Open(":memory:")
	if err != nil {
		return rep, err
	}
	defer db.Close()
	if err := migrations.Apply(ctx, db); err != nil {
		return rep, err
	}
	if _, err := access.SyncTemplates(ctx, db); err != nil {
		return rep, err
	}
	if err := kit.SeedBase(ctx, db); err != nil {
		return rep, err
	}
	rec := fake.NewRecorder()
	txs := contract.SQLTxManager{DB: db}
	gw, err := llmgw.New(llmgw.Options{Provider: cfg.Provider, DefaultModel: cfg.Model, DB: db, Txs: txs, Audit: rec, Masker: rbac.Masker{}})
	if err != nil {
		return rep, err
	}
	if err := llmgw.EnsureDefaultPolicy(ctx, db, cfg.PolicyPattern); err != nil {
		return rep, err
	}

	if cfg.Only == "" || cfg.Only == "docs" {
		set, err := evals.LoadDocSet()
		if err != nil {
			return rep, err
		}
		res, err := runDocs(ctx, db, txs, rec, gw, set)
		if err != nil {
			return rep, err
		}
		rep.Results = append(rep.Results, res...)
	}
	if cfg.Only == "" || cfg.Only == "tools" {
		cases, err := evals.LoadToolCases()
		if err != nil {
			return rep, err
		}
		rep.Results = append(rep.Results, runTools(ctx, gw, cases)...)
	}
	if cfg.Only == "" || cfg.Only == "policy" {
		cases, err := evals.LoadPolicyCases()
		if err != nil {
			return rep, err
		}
		res, err := runPolicy(ctx, cases)
		if err != nil {
			return rep, err
		}
		rep.Results = append(rep.Results, res...)
	}
	if cfg.Only == "" || cfg.Only == "quotes" {
		set, err := evals.LoadQuoteSet()
		if err != nil {
			return rep, err
		}
		rep.Results = append(rep.Results, runQuotes(ctx, gw, set)...)
	}
	if cfg.Only == "" || cfg.Only == "knowledge" {
		set, err := evals.LoadKnowledgeSet()
		if err != nil {
			return rep, err
		}
		res, err := runKnowledge(ctx, db, txs, rec, gw, set)
		if err != nil {
			return rep, err
		}
		rep.Results = append(rep.Results, res...)
	}
	if cfg.Only == "" || cfg.Only == "journals" {
		switch {
		case newJournalRig == nil && cfg.Only == "journals":
			return rep, fmt.Errorf("자동 전표 묶음은 전표 서비스(B31 t2 2단계)가 연결된 뒤에 돌릴 수 있습니다")
		case newJournalRig != nil:
			set, err := evals.LoadJournalSet()
			if err != nil {
				return rep, err
			}
			rig, err := newJournalRig(db, txs, rec)
			if err != nil {
				return rep, err
			}
			res, err := runJournals(ctx, db, txs, rec, gw, set, rig)
			if err != nil {
				return rep, err
			}
			rep.Results = append(rep.Results, res...)
		}
	}
	return rep, nil
}

func runDocs(ctx context.Context, db *sql.DB, txs contract.TxManager, rec *fake.Recorder, gw *llmgw.Gateway, set evals.DocSet) ([]CaseResult, error) {
	inv := kit.NewInventory()
	svc := &docverify.Service{DB: db, Txs: txs, Audit: rec, LLM: gw, Inventory: inv, Vendors: docverify.SQLVendors{DB: db}, Masker: rbac.Masker{}}
	pctx := contract.WithPrincipal(ctx, kit.Principal(contract.RoleStaff))
	// 이미지 케이스는 재무 권한과 관리자 설정(이미지 전송 켜짐)이 필요하다. 기본은 꺼짐이라 하니스가 켠다.
	fctx := contract.WithPrincipal(ctx, kit.Principal(contract.RoleFinance))
	if _, err := db.ExecContext(ctx, `INSERT INTO app_settings (key, value) VALUES (?, 'true') ON CONFLICT(key) DO UPDATE SET value = 'true'`, llmgw.SettingImageTransmission); err != nil {
		return nil, fmt.Errorf("이미지 전송 설정: %w", err)
	}
	var out []CaseResult
	for _, c := range set.Cases {
		spec := set.POs[c.PO]
		poID, rcID, err := inv.Seed(ctx, db, spec)
		if err != nil {
			return nil, fmt.Errorf("케이스 %s 시드: %w", c.ID, err)
		}
		in := domainInput(c, poID, rcID)
		cr := CaseResult{Suite: "docs", ID: c.ID, Critical: c.Critical}
		vctx := pctx
		if c.AsImage {
			vctx = fctx
		}
		v, err := svc.Verify(vctx, in)
		if err != nil {
			cr.Detail = "호출 오류: " + err.Error()
			out = append(out, cr)
			continue
		}
		var problems []string
		if string(v.Outcome) != c.Expect.Outcome {
			problems = append(problems, fmt.Sprintf("판정 %s (기대 %s)", v.Outcome, c.Expect.Outcome))
		}
		for _, e := range c.Expect.Evidence {
			found := false
			for _, it := range v.Items {
				if it.Field == e.Field && string(it.Result) == e.Result {
					found = true
					break
				}
			}
			if !found {
				problems = append(problems, fmt.Sprintf("근거 없음 %s=%s", e.Field, e.Result))
			}
		}
		cr.Pass = len(problems) == 0
		cr.Detail = fmt.Sprintf("판정 %s", v.Outcome)
		if !cr.Pass {
			cr.Detail = strings.Join(problems, "; ")
		}
		out = append(out, cr)
	}
	return out, nil
}
