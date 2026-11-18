package aicost_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/aicost"
	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/sod"
)

// capture 는 Register 가 등록한 처리기를 모아 HTTP 요청으로 직접 부를 수 있게 한다.
type capture struct{ h map[string]http.HandlerFunc }

func (c *capture) Handle(method, pattern string, _ contract.Permission, h http.Handler) {
	c.h[method+" "+pattern] = h.ServeHTTP
}
func (c *capture) HandleFunc(method, pattern string, _ contract.Permission, h http.HandlerFunc) {
	c.h[method+" "+pattern] = h
}

func (c *capture) call(ctx context.Context, method, path, route, body string) (int, string) {
	req := httptest.NewRequest(method, path, strings.NewReader(body)).WithContext(ctx)
	rec := httptest.NewRecorder()
	c.h[method+" "+route](rec, req)
	return rec.Code, rec.Body.String()
}

// B49 판정 후속 2: entity 매개변수는 B44 12절대로 법인 코드(대소문자 무시)를 받고, 숫자 번호도 전처럼 받는다. 모르는 코드는 400, 범위 밖은 403.
func TestEntityParamAcceptsCodeAndNumberAcrossRoutes(t *testing.T) {
	r := newRunEnv(t)
	r.seedTwoEntities()
	r.seedTwoEntityUsage()
	if _, err := r.svc.SaveRuleSet(finance, groupRules(aicost.DriverHeadcount)); err != nil {
		t.Fatal(err)
	}
	cap := &capture{h: map[string]http.HandlerFunc{}}
	r.svc.Register(cap)
	only1 := contract.WithPrincipal(context.Background(), contract.Principal{UserID: 1, Role: contract.RoleFinance, DepartmentID: 900, Channel: contract.ChannelWeb, Auth: contract.AuthSession,
		Perms:       contract.NewPermSet(contract.PermCostRead, contract.PermCostAllocate, contract.PermLedgerWrite, contract.PermReportRead),
		EntityPerms: map[contract.Permission]contract.EntitySet{contract.PermCostRead: contract.NewEntitySet(1), contract.PermCostAllocate: contract.NewEntitySet(1), contract.PermLedgerWrite: contract.NewEntitySet(1)}})
	full := allocator

	type route struct{ method, route, path string }
	routes := map[string]route{
		"집계":    {"GET", "/api/admin/ai-cost", "/api/admin/ai-cost?month=2026-10&entity=%s"},
		"미리보기":  {"GET", "/api/admin/ai-cost/preview", "/api/admin/ai-cost/preview?month=2026-10&entity=%s"},
		"실행 조회": {"GET", "/api/admin/ai-cost/run", "/api/admin/ai-cost/run?month=2026-10&entity=%s"},
		"예산":    {"GET", "/api/admin/ai-cost/budgets", "/api/admin/ai-cost/budgets?month=2026-10&entity=%s"},
		"CSV":   {"GET", "/api/admin/ai-cost/export", "/api/admin/ai-cost/export?month=2026-10&entity=%s"},
	}
	at := func(tmpl, e string) string { return strings.Replace(tmpl, "%s", e, 1) }
	// 모르는 코드는 모든 경로에서 400(한국어 사유).
	for name, rt := range routes {
		code, body := cap.call(full, rt.method, at(rt.path, "ZZZ"), rt.route, "")
		if code != http.StatusBadRequest || !strings.Contains(body, "알 수 없는 법인 코드입니다") {
			t.Errorf("%s: 모르는 코드는 400 이어야 한다: %d %s", name, code, body)
		}
	}
	if code, body := cap.call(full, "POST", "/api/admin/ai-cost/runs", "/api/admin/ai-cost/runs", `{"month":"2026-10","entity":"ZZZ"}`); code != http.StatusBadRequest || !strings.Contains(body, "알 수 없는 법인 코드입니다") {
		t.Errorf("확정: 모르는 코드는 400: %d %s", code, body)
	}
	// 코드(대소문자 무시)와 번호는 같은 법인(계열사)으로 해석된다.
	for _, e := range []string{"ABC", "abc", "2"} {
		code, body := cap.call(full, "GET", at(routes["미리보기"].path, e), routes["미리보기"].route, "")
		var pv aicost.Preview
		if code != 200 || json.Unmarshal([]byte(body), &pv) != nil || pv.EntityID != 2 || pv.GroupShareKRW != 250 {
			t.Errorf("미리보기 entity=%s: %d %s", e, code, body)
		}
		code, body = cap.call(full, "GET", at(routes["집계"].path, e), routes["집계"].route, "")
		var rep aicost.Report
		if code != 200 || json.Unmarshal([]byte(body), &rep) != nil || rep.TotalMicro != 1_400_000 { // 계열사: 1.0 + 0.4
			t.Errorf("집계 entity=%s: %d %s", e, code, body)
		}
	}
	// 기본 법인 코드 MAIN 은 기본 법인이고, 값이 없으면 현재(기본) 법인이다.
	for _, e := range []string{"MAIN", "main", "1", ""} {
		code, body := cap.call(full, "GET", at(routes["미리보기"].path, e), routes["미리보기"].route, "")
		var pv aicost.Preview
		if code != 200 || json.Unmarshal([]byte(body), &pv) != nil || pv.EntityID != 1 || pv.GroupShareKRW != 750 {
			t.Errorf("미리보기 entity=%q: %d %s", e, code, body)
		}
	}
	// CSV: 코드로 계열사를 주면 계열사 부서가 나오고 기본 법인 부서는 없다(전엔 코드가 무시돼 기본 법인이 나왔다).
	code, body := cap.call(full, "GET", at(routes["CSV"].path, "ABC"), routes["CSV"].route, "")
	if code != 200 || !strings.Contains(body, "계열사팀") || strings.Contains(body, "영업") || !strings.Contains(body, ",2,계열사팀,") {
		t.Errorf("CSV entity=ABC: %d %q", code, body)
	}
	code, body = cap.call(full, "GET", at(routes["CSV"].path, "MAIN"), routes["CSV"].route, "")
	if code != 200 || strings.Contains(body, "계열사팀") || !strings.Contains(body, "영업") {
		t.Errorf("CSV entity=MAIN: %d %q", code, body)
	}
	// 법인 범위가 기본 법인뿐이면 코드든 번호든 계열사는 403(집계는 전부터 그랬듯 계열사 행이 없는 빈 결과).
	for _, e := range []string{"ABC", "2"} {
		for _, name := range []string{"미리보기", "실행 조회", "예산", "CSV"} {
			rt := routes[name]
			if code, body := cap.call(only1, rt.method, at(rt.path, e), rt.route, ""); code != http.StatusForbidden {
				t.Errorf("%s entity=%s: 범위 밖은 403 이어야 한다: %d %s", name, e, code, body)
			}
		}
		code, body := cap.call(only1, "GET", at(routes["집계"].path, e), routes["집계"].route, "")
		var rep aicost.Report
		if code != 200 || json.Unmarshal([]byte(body), &rep) != nil || rep.TotalMicro != 0 || len(rep.Rows) != 0 {
			t.Errorf("집계 entity=%s: 범위 밖은 빈 결과: %d %s", e, code, body)
		}
		for _, ent := range []string{`"ABC"`, "2"} {
			if code, _ := cap.call(only1, "POST", "/api/admin/ai-cost/runs", "/api/admin/ai-cost/runs", `{"month":"2026-10","entity":`+ent+`}`); code != http.StatusForbidden {
				t.Errorf("확정 entity=%s: 범위 밖은 403: %d", ent, code)
			}
		}
	}
	// 확정: 본문 entity 도 코드와 번호 모두 받는다(계열사 확정은 코드로).
	code, body = cap.call(full, "POST", "/api/admin/ai-cost/runs", "/api/admin/ai-cost/runs", `{"month":"2026-10","entity":"abc"}`)
	var run aicost.RunInfo
	if code != 200 || json.Unmarshal([]byte(body), &run) != nil || run.EntityID != 2 {
		t.Errorf("확정 entity=abc: %d %s", code, body)
	}
	code, body = cap.call(full, "POST", "/api/admin/ai-cost/runs", "/api/admin/ai-cost/runs", `{"month":"2026-10","entity":1}`)
	if code != 200 || json.Unmarshal([]byte(body), &run) != nil || run.EntityID != 1 {
		t.Errorf("확정 entity=1: %d %s", code, body)
	}
	// 확정된 법인의 실행 조회: 코드로.
	code, body = cap.call(full, "GET", at(routes["실행 조회"].path, "ABC"), routes["실행 조회"].route, "")
	var got struct {
		Run *aicost.RunInfo `json:"run"`
	}
	if code != 200 || json.Unmarshal([]byte(body), &got) != nil || got.Run == nil || got.Run.EntityID != 2 {
		t.Errorf("실행 조회 entity=ABC: %d %s", code, body)
	}
}

// B49 판정 후속 3: 규칙 작성자의 확정 판정(aicost.rule_confirm)의 대상 번호가 0 이 아니라 확정 실행 번호다(같은 트랜잭션에서 실행을 만든 뒤 판정).
func TestRuleConfirmSoDSubjectIsTheRun(t *testing.T) {
	r := newRunEnv(t)
	r.seedOctober() // 규칙 작성자 = 사용자 1(allocator 와 같은 사람)
	run, err := r.svc.ConfirmRun(allocator, aicost.ConfirmInput{Month: "2026-10"})
	if err != nil {
		t.Fatal(err)
	}
	var got *sod.Check
	for i := range r.sod.checked {
		if r.sod.checked[i].Rule == sod.RuleAICostRuleConfirm {
			got = &r.sod.checked[i]
		}
	}
	if got == nil || got.Subject.Type != "ai_cost_run" || got.Subject.ID != run.ID || got.Subject.ID == 0 || got.Subject.No != "2026-10" {
		t.Fatalf("판정 대상: %+v (실행 %d)", got, run.ID)
	}
	// 차단 모드에서는 같은 판정이 실행을 만든 뒤 일어나 통째로 취소된다(실행·줄·전표가 남지 않는다).
	r.sod.block[sod.RuleAICostRuleConfirm] = true
	if err := r.svc.SetFX(finance, "2026-08", 1_000_000); err != nil {
		t.Fatal(err)
	}
	before := len(r.j.created)
	if _, err := r.svc.ConfirmRun(allocator, aicost.ConfirmInput{Month: "2026-08"}); err == nil {
		t.Fatal("차단되어야 한다")
	}
	var n int
	_ = r.db.QueryRow(`SELECT COUNT(*) FROM ai_cost_runs WHERE month = '2026-08'`).Scan(&n)
	if n != 0 || len(r.j.created) != before {
		t.Fatalf("차단된 확정이 남음: 실행 %d, 전표 %d", n, len(r.j.created)-before)
	}
	if len(r.sod.blocked) != 1 || r.sod.blocked[0].Subject.ID == 0 {
		t.Fatalf("차단 기록 대상: %+v", r.sod.blocked)
	}
}
