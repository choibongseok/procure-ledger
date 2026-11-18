package llmgw_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/evalset"
	"github.com/choibongseok/procure-ledger/internal/llmgw"
)

func gatePerms() []contract.Permission {
	return []contract.Permission{contract.PermLLMProviderManage, contract.PermLLMPolicyManage, contract.PermEvalRead, contract.PermEvalWrite, contract.PermEvalRun}
}

func newGateEnv(t *testing.T) (*env, *evalset.Service, *llmgw.ProviderAdmin) {
	t.Helper()
	e := newEnv(t)
	e.allow(t, "", 0, "*", 1)
	gw := e.gwWith(t, false)
	es, err := evalset.New(evalset.Options{DB: e.db, Txs: e.txs, Audit: e.rec, Masker: e.mask, LLM: gw, Force: llmgw.WithForcedProvider, Now: func() time.Time { return e.now }})
	if err != nil {
		t.Fatal(err)
	}
	ad := e.admin(t, false)
	ad.Gate = es
	return e, es, ad
}

func mkProvider(t *testing.T, ad *llmgw.ProviderAdmin, code, mode string) llmgw.ProviderView {
	t.Helper()
	ctx := adminCtx(gatePerms()...)
	pv, err := ad.SaveProvider(ctx, llmgw.ProviderInput{Code: code, Kind: "mock", Name: code, DefaultModel: code + "-1", Mode: mode})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ad.SetActive(ctx, pv.ID, true); err != nil {
		t.Fatal(err)
	}
	return pv
}

// mkSet 는 질문과 기대 답이 같은 문항 n 개의 게시된 세트다(모의 공급자는 질문을 그대로 돌려주므로 후보가 통과한다).
// hard 면 기대 답이 질문에 없어 후보가 모두 틀린다. 기준선은 모두 통과한 것으로 직접 넣는다.
func mkSet(t *testing.T, e *env, es *evalset.Service, code string, n int, hard bool) int64 {
	t.Helper()
	ctx := adminCtx(gatePerms()...)
	if _, err := es.CreateSet(ctx, evalset.SetInput{Code: code, Name: code + " 세트", Purpose: evalset.PurposeText}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < n; i++ {
		exp := "답변"
		if hard {
			exp = "질문에없는말"
		}
		if _, err := es.SaveItem(ctx, code, evalset.ItemInput{Question: "답변 문항 " + string(rune('A'+i)), Expected: exp, Grader: evalset.GraderContains}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := es.Publish(ctx, code, ""); err != nil {
		t.Fatal(err)
	}
	var setID int64
	_ = e.db.QueryRow(`SELECT id FROM eval_sets WHERE code = ?`, code).Scan(&setID)
	var runID int64
	if err := e.db.QueryRow(`INSERT INTO eval_runs (at, provider, total, passed, pass_rate_pct, threshold_pct, gate_passed, set_id, set_version, status, mode) VALUES ('x','base',?,?,100,90,1,?,1,'done','manual') RETURNING id`, n, n, setID).Scan(&runID); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= n; i++ {
		if _, err := e.db.Exec(`INSERT INTO eval_run_items (run_id, item_seq, pass, score) VALUES (?, ?, 1, 100)`, runID, i); err != nil {
			t.Fatal(err)
		}
	}
	return runID
}

func routeCount(t *testing.T, e *env) int { return e.count(t, `SELECT COUNT(*) FROM llm_routes`) }

// AC-4608~4610·4612: 지정 세트가 없으면 평가 없이 저장, 기준선이 없으면 건너뜀, 통과하면 저장,
// 기준선 미달은 저장하지 않고 409 상당 오류(게이트 결과), 무시하면 저장하고 감사에 사유 유무가 남는다.
func TestSaveRouteGateFlow(t *testing.T) {
	e, es, ad := newGateEnv(t)
	ctx := adminCtx(gatePerms()...)
	cand := mkProvider(t, ad, "cand", "")
	save := func(in llmgw.RouteInput) (llmgw.RouteView, error) {
		in.Active = true
		in.PrimaryProviderID = cand.ID
		return ad.SaveRoute(ctx, in)
	}

	// 지정이 없으면 평가가 돌지 않는다.
	v, err := save(llmgw.RouteInput{PurposePattern: "docverify.extract"})
	if err != nil || v.Gate != nil {
		t.Fatalf("지정 없음: %+v %v", v.Gate, err)
	}
	if n := e.count(t, `SELECT COUNT(*) FROM eval_runs WHERE mode = 'gate'`); n != 0 {
		t.Fatalf("지정 없이 게이트 실행 %d건", n)
	}

	// 기준선 없는 세트를 지정하면 건너뛰고 저장은 진행한다.
	mkSet(t, e, es, "soft", 3, false)
	if _, err := e.db.Exec(`UPDATE eval_sets SET baseline_run_id = 0`); err != nil {
		t.Fatal(err)
	}
	if _, err := es.SaveGate(ctx, evalset.GateInput{PurposePattern: "docverify.*", SetCode: "soft", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	v, err = save(llmgw.RouteInput{PurposePattern: "docverify.quote"})
	if err != nil || v.Gate == nil || v.Gate.Status != evalset.GateSkipped || !strings.Contains(v.Gate.Reason, "기준선이 없습니다") {
		t.Fatalf("기준선 없음: %+v %v", v.Gate, err)
	}

	// 기준선이 있고 후보가 통과하면 저장되고 결과가 따라온다(문항 3개 모두 통과 = 100%).
	var softID int64
	_ = e.db.QueryRow(`SELECT id FROM eval_runs WHERE set_id = (SELECT id FROM eval_sets WHERE code = 'soft') AND mode = 'manual'`).Scan(&softID)
	if _, err := es.SetBaseline(ctx, "soft", softID); err != nil {
		t.Fatal(err)
	}
	v, err = save(llmgw.RouteInput{PurposePattern: "docverify.extract"})
	if err != nil || v.Gate == nil || v.Gate.Status != evalset.GatePass || v.Gate.Score != 100 || v.Gate.Items != 3 {
		t.Fatalf("통과: %+v %v", v.Gate, err)
	}
	if n := e.count(t, `SELECT COUNT(*) FROM eval_runs WHERE mode = 'gate' AND status = 'done'`); n != 1 {
		t.Fatalf("게이트 실행 기록 %d건", n)
	}

	// 경로를 바꾸지 않는 저장(순서만 수정)은 평가하지 않는다.
	before := e.count(t, `SELECT COUNT(*) FROM eval_runs WHERE mode = 'gate'`)
	v, err = save(llmgw.RouteInput{ID: v.ID, PurposePattern: "docverify.extract", SortOrder: 5})
	if err != nil || v.Gate != nil || e.count(t, `SELECT COUNT(*) FROM eval_runs WHERE mode = 'gate'`) != before {
		t.Fatalf("순서만 수정: %+v %v", v.Gate, err)
	}

	// 더 구체적인 패턴의 세트가 우선한다. 기준선(100%)보다 낮으면 저장하지 않고 게이트 결과를 돌려준다.
	mkSet(t, e, es, "hard", 4, true)
	var hardBase int64
	_ = e.db.QueryRow(`SELECT id FROM eval_runs WHERE set_id = (SELECT id FROM eval_sets WHERE code = 'hard') AND mode = 'manual'`).Scan(&hardBase)
	if _, err := es.SetBaseline(ctx, "hard", hardBase); err != nil {
		t.Fatal(err)
	}
	if _, err := es.SaveGate(ctx, evalset.GateInput{PurposePattern: "docverify.summary", SetCode: "hard", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	routes := routeCount(t, e)
	_, err = save(llmgw.RouteInput{PurposePattern: "docverify.summary"})
	var ge *llmgw.GateError
	if !errors.As(err, &ge) || ge.Result.Status != evalset.GateBlocked || ge.Result.Score != 0 || ge.Result.BaselineScore != 100 || ge.Result.SetCode != "hard" ||
		!strings.Contains(ge.Result.Message, "기준선(100%)보다 낮습니다(0%, 치명 실패 0건). 그래도 저장하시겠습니까?") {
		t.Fatalf("미달: %+v %v", ge, err)
	}
	if routeCount(t, e) != routes {
		t.Fatal("미달인데 규칙이 저장됐습니다")
	}
	if e.rec.Has("eval.gate_override", "", "") {
		t.Fatal("무시하지 않았는데 무시 감사가 있습니다")
	}

	// 무시하고 저장: 규칙이 저장되고 감사에 점수·치명·사유 유무(값 없음)가 남는다.
	v, err = save(llmgw.RouteInput{PurposePattern: "docverify.summary", GateOverride: true, GateReason: "시험 환경이라 괜찮음"})
	if err != nil || v.Gate == nil || v.Gate.Status != evalset.GateBlocked || routeCount(t, e) != routes+1 {
		t.Fatalf("무시 저장: %+v %v", v.Gate, err)
	}
	v2, err := save(llmgw.RouteInput{PurposePattern: "docverify.summary", SortOrder: 1, GateOverride: true})
	if err != nil || v2.Gate == nil {
		t.Fatalf("사유 없이 무시 저장: %+v %v", v2.Gate, err)
	}
	var given []string
	for _, r := range e.rec.Records() {
		if r.Action != "eval.gate_override" {
			continue
		}
		keys := map[string]string{}
		for _, d := range r.Diff {
			keys[d.Key] = d.New
			if strings.Contains(d.New, "시험 환경") {
				t.Fatalf("감사에 사유 원문이 있습니다: %+v", d)
			}
		}
		if keys["set_code"] != "hard" || keys["candidate_score"] != "0" || keys["baseline_score"] != "100" || keys["critical_fails"] != "0" {
			t.Fatalf("무시 감사 diff: %+v", r.Diff)
		}
		given = append(given, keys["reason_given"])
	}
	if len(given) != 2 || given[0] != "있음" || given[1] != "없음" {
		t.Fatalf("사유 유무: %v", given)
	}

	// 지정을 끄면 평가 없이 저장된다.
	if _, err := es.SaveGate(ctx, evalset.GateInput{PurposePattern: "docverify.summary", SetCode: "hard", Enabled: false}); err != nil {
		t.Fatal(err)
	}
	if v, err := save(llmgw.RouteInput{PurposePattern: "docverify.summary", SortOrder: 9, ID: v.ID}); err != nil || v.Gate != nil {
		t.Fatalf("꺼진 지정: %+v %v", v.Gate, err)
	}
	// 꺼진 규칙 저장은 평가하지 않는다.
	if _, err := es.SaveGate(ctx, evalset.GateInput{PurposePattern: "docverify.summary", SetCode: "hard", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	inactive, err := ad.SaveRoute(ctx, llmgw.RouteInput{PurposePattern: "docverify.summary", PrimaryProviderID: cand.ID, Active: false})
	if err != nil || inactive.Gate != nil {
		t.Fatalf("꺼진 규칙: %+v %v", inactive.Gate, err)
	}
}

// 후보 공급자 오류·시간 초과는 점수가 아니라 건너뜀이고 저장은 진행된다. 외부 후보는 비용 확인 전에는 돌리지 않는다.
func TestSaveRouteGateSkipsOnFailureAndAsksCost(t *testing.T) {
	e, es, ad := newGateEnv(t)
	ctx := adminCtx(gatePerms()...)
	mkSet(t, e, es, "hard", 3, true)
	var base int64
	_ = e.db.QueryRow(`SELECT id FROM eval_runs WHERE mode = 'manual'`).Scan(&base)
	if _, err := es.SetBaseline(ctx, "hard", base); err != nil {
		t.Fatal(err)
	}
	if _, err := es.SaveGate(ctx, evalset.GateInput{PurposePattern: "*", SetCode: "hard", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	bad := mkProvider(t, ad, "bad", "fail_5xx")
	v, err := ad.SaveRoute(ctx, llmgw.RouteInput{PurposePattern: "docverify.extract", PrimaryProviderID: bad.ID, Active: true})
	if err != nil || v.Gate == nil || v.Gate.Status != evalset.GateSkipped || !strings.Contains(v.Gate.Reason, "오류") {
		t.Fatalf("공급자 오류: %+v %v", v.Gate, err)
	}
	old := evalset.GateTimeout
	evalset.GateTimeout = 200 * time.Millisecond
	defer func() { evalset.GateTimeout = old }()
	slow := mkProvider(t, ad, "slow", "slow:5000")
	v, err = ad.SaveRoute(ctx, llmgw.RouteInput{PurposePattern: "docverify.quote", PrimaryProviderID: slow.ID, Active: true})
	if err != nil || v.Gate == nil || v.Gate.Status != evalset.GateSkipped || !strings.Contains(v.Gate.Reason, "시간 초과") {
		t.Fatalf("시간 초과: %+v %v", v.Gate, err)
	}
	if n := e.count(t, `SELECT COUNT(*) FROM eval_runs WHERE mode = 'gate' AND status = 'running'`); n != 0 {
		t.Fatalf("끝나지 않은 게이트 실행 %d건", n)
	}
	// 외부 후보: 확인 없이는 돌리지 않고(저장 안 함), 확인하면 돌고 미달이면 다시 막힌다.
	ext := mkProvider(t, ad, "ext", "")
	if _, err := e.db.Exec(`UPDATE llm_providers SET external = 1 WHERE code = 'ext'`); err != nil {
		t.Fatal(err)
	}
	evalset.GateTimeout = old
	routes := routeCount(t, e)
	gateRuns := e.count(t, `SELECT COUNT(*) FROM eval_runs WHERE mode = 'gate'`)
	_, err = ad.SaveRoute(ctx, llmgw.RouteInput{PurposePattern: "docverify.summary", PrimaryProviderID: ext.ID, Active: true})
	var ge *llmgw.GateError
	if !errors.As(err, &ge) || ge.Result.Status != evalset.GateConfirmCost || !ge.Result.External || !strings.Contains(ge.Result.Message, "비용이 들고") {
		t.Fatalf("비용 확인 요청: %+v %v", ge, err)
	}
	if routeCount(t, e) != routes || e.count(t, `SELECT COUNT(*) FROM eval_runs WHERE mode = 'gate'`) != gateRuns {
		t.Fatal("확인 전에 저장하거나 평가를 돌렸습니다")
	}
	_, err = ad.SaveRoute(ctx, llmgw.RouteInput{PurposePattern: "docverify.summary", PrimaryProviderID: ext.ID, Active: true, GateConfirmCost: true})
	if !errors.As(err, &ge) || ge.Result.Status != evalset.GateBlocked {
		t.Fatalf("비용 확인 뒤 판정: %+v %v", ge, err)
	}
}

func TestGateSettingsPermissionsAndAudit(t *testing.T) {
	e, es, _ := newGateEnv(t)
	ctx := adminCtx(gatePerms()...)
	mkSet(t, e, es, "soft", 2, false)
	if _, err := es.SaveGate(adminCtx(contract.PermEvalRead), evalset.GateInput{PurposePattern: "docverify.*", SetCode: "soft", Enabled: true}); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("eval:write 없이 지정: %v", err)
	}
	for _, bad := range []evalset.GateInput{{PurposePattern: "Docverify", SetCode: "soft"}, {PurposePattern: "docverify.*", SetCode: "nope"}, {PurposePattern: "", SetCode: "soft"}} {
		if _, err := es.SaveGate(ctx, bad); !errors.Is(err, contract.ErrValidation) {
			t.Fatalf("잘못된 입력 %+v: %v", bad, err)
		}
	}
	g, err := es.SaveGate(ctx, evalset.GateInput{PurposePattern: "docverify.*", SetCode: "soft", Enabled: true})
	if err != nil || !g.Enabled || g.SetCode != "soft" {
		t.Fatalf("지정: %+v %v", g, err)
	}
	g2, err := es.SaveGate(ctx, evalset.GateInput{PurposePattern: "docverify.*", SetCode: "soft", Enabled: false})
	if err != nil || g2.ID != g.ID || g2.Enabled {
		t.Fatalf("같은 패턴은 한 줄: %+v %v", g2, err)
	}
	list, err := es.ListGates(adminCtx(contract.PermEvalRead))
	if err != nil || len(list) != 1 {
		t.Fatalf("목록: %+v %v", list, err)
	}
	if !e.rec.Has("eval.gate_set", "eval_set", "success") {
		t.Fatal("eval.gate_set 감사가 없습니다")
	}
}
