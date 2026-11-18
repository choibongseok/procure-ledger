package evalset_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/llm"
	"github.com/choibongseok/procure-ledger/internal/evalset"
	"github.com/choibongseok/procure-ledger/internal/rbac"
)

// fakeLLM 은 질문 글에 따라 응답을 고르는 결정적 호출기다.
type fakeLLM struct {
	reply func(req llm.Request, n int) (string, error)
	calls atomic.Int32
}

func (f *fakeLLM) Complete(_ context.Context, req llm.Request) (llm.Response, error) {
	n := int(f.calls.Add(1))
	text, err := f.reply(req, n)
	if err != nil {
		return llm.Response{}, err
	}
	return llm.Response{Text: text, Usage: llm.Usage{InputTokens: 10, OutputTokens: 5}}, nil
}

func echo(req llm.Request, _ int) (string, error) {
	return req.Messages[len(req.Messages)-1].Content, nil
}

func (e *env) runSvc(client llm.Client, rehearsal bool) *evalset.Service {
	e.t.Helper()
	svc, err := evalset.New(evalset.Options{DB: e.db, Txs: contract.SQLTxManager{DB: e.db}, Audit: e.rec, Masker: rbac.Masker{}, LLM: client, Rehearsal: rehearsal,
		Force: func(ctx context.Context, code string) context.Context { return ctx }, Now: func() time.Time { return time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC) }})
	if err != nil {
		e.t.Fatal(err)
	}
	return svc
}

func (e *env) published(code string, rows ...[3]string) {
	e.t.Helper()
	e.newSet(code, evalset.PurposeText)
	for _, r := range rows {
		e.text(code, r[0], r[1], r[2])
	}
	if _, err := e.svc.Publish(admin, code, ""); err != nil {
		e.t.Fatal(err)
	}
}

func waitDone(t *testing.T, svc *evalset.Service, id int64) evalset.RunView {
	t.Helper()
	svc.Wait()
	v, err := svc.GetRun(admin, id)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// 채점기 4종: 규칙표대로(공백·쉼표 정규화, 허용 오차, 모의 채점기).
func TestGraders(t *testing.T) {
	cases := []struct {
		name string
		it   evalset.ItemView
		resp string
		pass bool
	}{
		{"exact 공백·쉼표", evalset.ItemView{Grader: "exact", Expected: "합계 1,000원"}, "  합계   1000원 ", true},
		{"exact 대소문자 구분", evalset.ItemView{Grader: "exact", Expected: "OK"}, "ok", false},
		{"contains 줄마다", evalset.ItemView{Grader: "contains", Expected: "결재\n신청"}, "결재 문서로 신청하세요", true},
		{"contains 하나 빠짐", evalset.ItemView{Grader: "contains", Expected: "결재\n신청"}, "결재만", false},
		{"number abs 안", evalset.ItemView{Grader: "number", Expected: "100", Tolerance: "abs:2"}, "약 101.5 입니다", true},
		{"number abs 밖", evalset.ItemView{Grader: "number", Expected: "100", Tolerance: "abs:2"}, "103", false},
		{"number rel 안", evalset.ItemView{Grader: "number", Expected: "1000", Tolerance: "rel:5"}, "1,049", true},
		{"number rel 밖", evalset.ItemView{Grader: "number", Expected: "1000", Tolerance: "rel:5"}, "1051", false},
		{"number 숫자 없음", evalset.ItemView{Grader: "number", Expected: "1", Tolerance: "abs:0"}, "없음", false},
		{"llm 필수 모두", evalset.ItemView{Grader: "llm", Rubric: "필수: 환불, 7일"}, "7일 안에 환불됩니다", true},
		{"llm 필수 빠짐", evalset.ItemView{Grader: "llm", Rubric: "필수: 환불, 7일"}, "환불됩니다", false},
		{"llm 금지어", evalset.ItemView{Grader: "llm", Rubric: "필수: 환불\n금지: 불가"}, "환불 불가", false},
	}
	for _, c := range cases {
		g := evalset.Grade(c.it, c.resp)
		if g.Pass != c.pass {
			t.Errorf("%s: pass=%v detail=%q", c.name, g.Pass, g.Detail)
		}
		if !g.Pass && g.Detail == "" {
			t.Errorf("%s: 실패 사유가 비었습니다", c.name)
		}
		if g.Pass && g.Score != 100 {
			t.Errorf("%s: 통과 점수 %d", c.name, g.Score)
		}
	}
}

// AC-4604·4605: 실행 → 문항별 결과·합격률·치명·지연(p95)·토큰, 응답 전문은 저장하지 않고 감사에 값이 없다.
func TestRunStoresResultsWithoutResponseBodies(t *testing.T) {
	e := newEnv(t)
	e.published("faq", [3]string{"연차는 15일", "15일", "contains"}, [3]string{"출장비 한도", "반드시나오지않는말", "exact"})
	svc := e.runSvc(&fakeLLM{reply: func(req llm.Request, n int) (string, error) { return echo(req, n) }}, false)
	v, err := svc.Start(admin, "faq", evalset.RunInput{ConfirmCost: true})
	if err != nil || v.Status != "running" && v.Status != "done" {
		t.Fatalf("시작: %+v %v", v, err)
	}
	r := waitDone(t, svc, v.ID)
	if r.Status != "done" || r.Total != 2 || r.Passed != 1 || r.PassRatePct != 50 || r.GatePassed || r.InputTokens != 20 || r.OutputTokens != 10 || len(r.Items) != 2 {
		t.Fatalf("결과: %+v", r)
	}
	if r.PromptRef == "" || !strings.HasPrefix(r.PromptRef, "eval.text@") || r.Mode != "manual" || r.SetVersion != 1 {
		t.Fatalf("연결 정보: %+v", r)
	}
	if r.Items[1].Pass || r.Items[1].Detail == "" || strings.Contains(r.Items[1].Detail, "반드시나오지않는말") {
		t.Fatalf("실패 사유에 값이 없어야 한다: %+v", r.Items[1])
	}
	// 저장소 어디에도 응답 본문이 없다.
	var bodies int
	_ = e.db.QueryRow(`SELECT COUNT(*) FROM eval_run_items WHERE detail LIKE '%연차는 15일%'`).Scan(&bodies)
	if bodies != 0 {
		t.Fatal("응답 본문이 결과 상세에 저장됐습니다")
	}
	found := false
	for _, a := range e.rec.Records() {
		if a.Action != "eval.run" {
			continue
		}
		found = true
		keys := map[string]string{}
		for _, d := range a.Diff {
			keys[d.Key] = d.New
		}
		if keys["set_code"] != "faq" || keys["set_version"] != "1" || keys["run_mode"] != "manual" || keys["pass_rate_pct"] != "50" || keys["prompt_ref"] == "" {
			t.Fatalf("감사 diff: %+v", a.Diff)
		}
		for _, d := range a.Diff {
			if strings.Contains(d.New, "연차") {
				t.Fatalf("감사에 문항 글이 있습니다: %+v", d)
			}
		}
	}
	if !found {
		t.Fatal("eval.run 감사가 없습니다")
	}
}

func TestPermissionsAndStateGuards(t *testing.T) {
	e := newEnv(t)
	e.published("faq", [3]string{"q", "q", "contains"})
	svc := e.runSvc(&fakeLLM{reply: echo}, false)
	if _, err := svc.Start(reader, "faq", evalset.RunInput{ConfirmCost: true}); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("eval:run 없이 시작: %v", err)
	}
	if _, err := svc.SetBaseline(reader, "faq", 1); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("eval:write 없이 기준선: %v", err)
	}
	if _, err := svc.Start(admin, "faq", evalset.RunInput{}); !errors.Is(err, contract.ErrValidation) || !strings.Contains(err.Error(), "비용") {
		t.Fatalf("비용 확인 없이 시작: %v", err)
	}
	if _, err := svc.Start(admin, "faq", evalset.RunInput{Provider: "nope", ConfirmCost: true}); !errors.Is(err, contract.ErrValidation) {
		t.Fatalf("없는 공급자: %v", err)
	}
	e.newSet("empty", evalset.PurposeText)
	if _, err := svc.Start(admin, "empty", evalset.RunInput{ConfirmCost: true}); !errors.Is(err, contract.ErrInvalidState) || !strings.Contains(err.Error(), "게시된 버전이 없습니다") {
		t.Fatalf("게시 전 실행: %v", err)
	}
	// 외부 공급자: 확인 없으면 거부, 리허설이면 거부, 켜져 있고 확인하면 통과.
	if _, err := e.db.Exec(`INSERT INTO llm_providers (code, kind, name, default_model, external, active, created_at, updated_at) VALUES ('ext1', 'openai_compat', '외부', 'm1', 1, 1, 'x', 'x')`); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Start(admin, "faq", evalset.RunInput{Provider: "ext1"}); !errors.Is(err, contract.ErrValidation) {
		t.Fatalf("외부 공급자 확인 없음: %v", err)
	}
	if _, err := e.runSvc(&fakeLLM{reply: echo}, true).Start(admin, "faq", evalset.RunInput{Provider: "ext1", ConfirmCost: true}); !errors.Is(err, contract.ErrInvalidState) || !strings.Contains(err.Error(), "리허설") {
		t.Fatalf("리허설 외부 공급자: %v", err)
	}
	v, err := svc.Start(admin, "faq", evalset.RunInput{Provider: "ext1", ConfirmCost: true})
	if err != nil {
		t.Fatal(err)
	}
	if r := waitDone(t, svc, v.ID); r.Model != "m1" || r.Provider != "ext1" {
		t.Fatalf("공급자 기본 모델: %+v", r)
	}
	// 비활성 세트는 실행 못 한다.
	off := false
	if _, err := svc.UpdateSet(admin, "faq", evalset.SetInput{Active: &off}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Start(admin, "faq", evalset.RunInput{ConfirmCost: true}); !errors.Is(err, contract.ErrInvalidState) {
		t.Fatalf("꺼진 세트: %v", err)
	}
}

// 한도 초과·허용 안 된 모델은 실행 전체 실패이고 부분 결과를 남기지 않는다. 연속 5회 공급자 오류도 중단.
func TestRunFailureModes(t *testing.T) {
	e := newEnv(t)
	var rows [][3]string
	for i := 0; i < 8; i++ {
		rows = append(rows, [3]string{fmt.Sprintf("문항%d", i), "x", "contains"})
	}
	e.published("many", rows...)
	cases := []struct {
		name   string
		reply  func(llm.Request, int) (string, error)
		reason string
		items  int
	}{
		{"한도", func(_ llm.Request, n int) (string, error) {
			if n == 3 {
				return "", fmt.Errorf("LLM 호출 실패: %w", contract.ErrQuotaExceeded)
			}
			return "x", nil
		}, "한도를 넘었습니다", 0},
		{"모델", func(_ llm.Request, n int) (string, error) {
			if n == 2 {
				return "", contract.ErrModelNotAllowed
			}
			return "x", nil
		}, "허용 목록에 없는 모델입니다", 0},
		{"연속 오류", func(_ llm.Request, n int) (string, error) {
			if n >= 2 {
				return "", errors.New("boom")
			}
			return "x", nil
		}, "연속 5회", 5},
	}
	for _, c := range cases {
		svc := e.runSvc(&fakeLLM{reply: c.reply}, false)
		v, err := svc.Start(admin, "many", evalset.RunInput{ConfirmCost: true})
		if err != nil {
			t.Fatal(err)
		}
		r := waitDone(t, svc, v.ID)
		if r.Status != "failed" || !strings.Contains(r.FailReason, c.reason) || len(r.Items) != c.items {
			t.Fatalf("%s: %+v", c.name, r)
		}
	}
	// 일부 오류(연속 아님)는 그 문항만 실패하고 실행은 계속된다.
	svc := e.runSvc(&fakeLLM{reply: func(_ llm.Request, n int) (string, error) {
		if n%2 == 0 {
			return "", errors.New("boom")
		}
		return "x", nil
	}}, false)
	v, _ := svc.Start(admin, "many", evalset.RunInput{ConfirmCost: true})
	r := waitDone(t, svc, v.ID)
	if r.Status != "done" || r.Passed != 4 || r.PassRatePct != 50 {
		t.Fatalf("산발 오류: %+v", r)
	}
	// 서버 중단 정리.
	if _, err := e.db.Exec(`UPDATE eval_runs SET status = 'running' WHERE id = ?`, v.ID); err == nil || !strings.Contains(err.Error(), "추가 전용") {
		t.Fatalf("끝난 실행은 고칠 수 없다: %v", err)
	}
	if _, err := e.db.Exec(`INSERT INTO eval_runs (at, provider, total, passed, pass_rate_pct, threshold_pct, gate_passed, set_id, set_version, status) VALUES ('x','p',1,0,0,90,0,(SELECT id FROM eval_sets WHERE code='many'),1,'running')`); err != nil {
		t.Fatal(err)
	}
	var stale int64
	_ = e.db.QueryRow(`SELECT MAX(id) FROM eval_runs`).Scan(&stale)
	if err := svc.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if r2, _ := svc.GetRun(admin, stale); r2.Status != "failed" || r2.FailReason != "중단" {
		t.Fatalf("중단 정리: %+v", r2)
	}
	if _, err := e.db.Exec(`UPDATE eval_runs SET fail_reason = 'x' WHERE id = ?`, stale); err == nil {
		t.Fatal("정리된 실행도 고칠 수 없어야 한다")
	}
	if _, err := e.db.Exec(`DELETE FROM eval_runs WHERE id = ?`, stale); err == nil {
		t.Fatal("삭제는 막혀야 한다")
	}
}

// 같은 세트의 동시 실행은 3개까지.
func TestConcurrentRunLimit(t *testing.T) {
	e := newEnv(t)
	e.published("faq", [3]string{"q", "q", "contains"})
	block := make(chan struct{})
	svc := e.runSvc(&fakeLLM{reply: func(req llm.Request, n int) (string, error) { <-block; return echo(req, n) }}, false)
	for i := 0; i < 3; i++ {
		if _, err := svc.Start(admin, "faq", evalset.RunInput{ConfirmCost: true}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := svc.Start(admin, "faq", evalset.RunInput{ConfirmCost: true}); !errors.Is(err, contract.ErrInvalidState) || !strings.Contains(err.Error(), "3개까지") {
		t.Fatalf("4번째: %v", err)
	}
	close(block)
	svc.Wait()
}

// AC-4606·4607: 같은 버전은 문항별 회귀·개선, 다른 버전은 합계만. 기준선은 끝난 실행만, 하락·치명은 경고.
func TestCompareBaselineAndWarning(t *testing.T) {
	e := newEnv(t)
	e.published("faq", [3]string{"가", "가", "contains"}, [3]string{"나", "나", "contains"}, [3]string{"다", "다", "contains"})
	mode := "all"
	svc := e.runSvc(&fakeLLM{reply: func(req llm.Request, _ int) (string, error) {
		q := req.Messages[len(req.Messages)-1].Content
		if mode == "all" || (mode == "drop2" && q != "나") {
			return q, nil
		}
		return "엉뚱한 답", nil
	}}, false)
	run := func() evalset.RunView {
		v, err := svc.Start(admin, "faq", evalset.RunInput{ConfirmCost: true})
		if err != nil {
			t.Fatal(err)
		}
		return waitDone(t, svc, v.ID)
	}
	a := run()
	if a.PassRatePct != 100 || !a.GatePassed {
		t.Fatalf("기준 실행: %+v", a)
	}
	failedID := int64(0)
	if _, err := e.db.Exec(`INSERT INTO eval_runs (at, provider, total, passed, pass_rate_pct, threshold_pct, gate_passed, set_id, set_version, status) VALUES ('x','p',1,0,0,90,0,(SELECT id FROM eval_sets WHERE code='faq'),1,'failed')`); err != nil {
		t.Fatal(err)
	}
	_ = e.db.QueryRow(`SELECT MAX(id) FROM eval_runs`).Scan(&failedID)
	if _, err := svc.SetBaseline(admin, "faq", failedID); !errors.Is(err, contract.ErrInvalidState) {
		t.Fatalf("실패 실행을 기준선으로: %v", err)
	}
	if _, err := svc.SetBaseline(admin, "faq", a.ID); err != nil {
		t.Fatal(err)
	}
	mode = "drop2"
	b := run()
	if b.PassRatePct != 66 || b.GatePassed || b.BaselinePct != 100 || !strings.Contains(b.Warning, "기준선(100%)보다 34%p 낮습니다") {
		t.Fatalf("하락 경고: %+v", b)
	}
	c, err := svc.Compare(admin, a.ID, b.ID)
	if err != nil || !c.SameVersion || c.Regressions != 1 || c.Improvements != 0 || len(c.Rows) != 3 {
		t.Fatalf("비교: %+v %v", c, err)
	}
	for _, r := range c.Rows {
		if (r.Seq == 2) != (r.Change == "regression") {
			t.Fatalf("회귀 표시: %+v", r)
		}
	}
	// 새 실패가 실행에 남는다.
	var nf string
	_ = e.db.QueryRow(`SELECT new_failures_json FROM eval_runs WHERE id = ?`, b.ID).Scan(&nf)
	if nf != `["문항 2"]` {
		t.Fatalf("새 실패: %s", nf)
	}
	// 반대 방향은 개선.
	if c2, _ := svc.Compare(admin, b.ID, a.ID); c2.Improvements != 1 || c2.Regressions != 0 {
		t.Fatalf("개선: %+v", c2)
	}
	// 다른 버전은 합계만.
	if _, err := svc.NewDraft(admin, "faq"); err != nil {
		t.Fatal(err)
	}
	e.text("faq", "라", "라", "contains")
	if _, err := svc.Publish(admin, "faq", ""); err != nil {
		t.Fatal(err)
	}
	mode = "all"
	d := run()
	if d.SetVersion != 2 || d.Total != 4 {
		t.Fatalf("2판 실행: %+v", d)
	}
	c3, err := svc.Compare(admin, a.ID, d.ID)
	if err != nil || c3.SameVersion || len(c3.Rows) != 0 || !strings.Contains(c3.Note, "합계만") {
		t.Fatalf("다른 버전 비교: %+v %v", c3, err)
	}
	// 다른 버전 실행을 기준선으로 지정하면 경고(현재 버전과 다른 실행: 1판 실행 a, 현재 2판).
	if warn, err := svc.SetBaseline(admin, "faq", a.ID); err != nil || !strings.Contains(warn, "버전이 다른") {
		t.Fatalf("버전 경고: %q %v", warn, err)
	}
	// 해제.
	if _, err := svc.SetBaseline(admin, "faq", 0); err != nil {
		t.Fatal(err)
	}
	set, _ := svc.Get(admin, "faq", 0)
	if set.BaselineRunID != 0 {
		t.Fatalf("해제: %+v", set.SetView)
	}
	n := 0
	for _, a := range e.rec.Records() {
		if a.Action == "eval.baseline_set" {
			n++
		}
	}
	if n != 3 {
		t.Fatalf("기준선 감사 %d건", n)
	}
}

// 치명 문항이 실패하면 합격률이 높아도 합격이 아니다.
func TestCriticalFailureBlocksGatePass(t *testing.T) {
	e := newEnv(t)
	e.newSet("crit", evalset.PurposeText)
	for i := 0; i < 10; i++ {
		in := evalset.ItemInput{Question: fmt.Sprintf("q%d", i), Expected: "ok", Grader: evalset.GraderContains, Critical: i == 0}
		if _, err := e.svc.SaveItem(admin, "crit", in); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := e.svc.Publish(admin, "crit", ""); err != nil {
		t.Fatal(err)
	}
	svc := e.runSvc(&fakeLLM{reply: func(req llm.Request, _ int) (string, error) {
		if req.Messages[0].Content == "q0" {
			return "no", nil
		}
		return "ok", nil
	}}, false)
	v, _ := svc.Start(admin, "crit", evalset.RunInput{ConfirmCost: true})
	r := waitDone(t, svc, v.ID)
	if r.PassRatePct != 90 || r.CriticalFails != 1 || r.GatePassed || !strings.Contains(r.Warning, "치명 문항 1건") {
		t.Fatalf("치명: %+v", r)
	}
}

// CLI·게이트 경로: 권한 점검 없이 동기로 돌고, 호출기를 직접 넘기며, 앞의 N개만 돈다.
func TestRunSyncWithClientAndLimit(t *testing.T) {
	e := newEnv(t)
	e.published("faq", [3]string{"a", "a", "contains"}, [3]string{"b", "b", "contains"}, [3]string{"c", "c", "contains"})
	svc := e.runSvc(nil, false)
	fl := &fakeLLM{reply: echo}
	v, err := svc.RunSync(context.Background(), contract.Principal{}, "faq", evalset.RunInput{Client: fl, Label: "mock", Mode: evalset.ModeGate, Limit: 2})
	if err != nil || v.Status != "done" || v.Total != 2 || v.Mode != "gate" || v.Provider != "mock" || fl.calls.Load() != 2 {
		t.Fatalf("동기 실행: %+v %v", v, err)
	}
}

func TestP95AndLatencyAggregation(t *testing.T) {
	e := newEnv(t)
	e.published("faq", [3]string{"q", "q", "contains"})
	svc := e.runSvc(&fakeLLM{reply: echo}, false)
	// 합성 실행: 진행 중 행에 지연 1..20ms 를 넣고 끝낸다(끝난 실행의 문항 결과는 고칠 수 없다) → p95 = 정렬 19번째 = 19.
	var runID int64
	if err := e.db.QueryRow(`INSERT INTO eval_runs (at, provider, total, passed, pass_rate_pct, threshold_pct, gate_passed, set_id, set_version, status) VALUES ('x','p',20,20,100,90,1,(SELECT id FROM eval_sets WHERE code='faq'),1,'running') RETURNING id`).Scan(&runID); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 20; i++ {
		if _, err := e.db.Exec(`INSERT INTO eval_run_items (run_id, item_seq, pass, score, latency_ms) VALUES (?, ?, 1, 100, ?)`, runID, i, i); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := e.db.Exec(`UPDATE eval_runs SET status = 'done' WHERE id = ?`, runID); err != nil {
		t.Fatal(err)
	}
	r, err := svc.GetRun(admin, runID)
	if err != nil || r.P95LatencyMS != 19 {
		t.Fatalf("p95: %+v %v", r.P95LatencyMS, err)
	}
}
