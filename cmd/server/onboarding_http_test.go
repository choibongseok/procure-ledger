package main

// B38 3단계: 도입 점검표·백업 상태 라우트를 실제 라우트·세션·권한 표로 태운다(AC-1810·1840).

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/contract"
)

func TestOnboardingRoutesDeclared(t *testing.T) {
	e := newPlatformEnv(t, false)
	got := map[string]contract.Permission{}
	for _, r := range e.rt.Routes() {
		got[r.Method+" "+r.Pattern] = r.Permission
	}
	for k, p := range map[string]contract.Permission{
		"GET /api/admin/onboarding":                 contract.PermOnboardingRead,
		"POST /api/admin/onboarding/skips":          contract.PermOnboardingRead,
		"DELETE /api/admin/onboarding/skips/{item}": contract.PermOnboardingRead,
		"GET /api/admin/backup/status":              contract.PermBackupRead,
	} {
		if got[k] != p {
			t.Errorf("%s: 선언 %q, 기대 %q", k, got[k], p)
		}
	}
	// 백업·복구를 실행하는 라우트는 없다(D16: 웹 세션 탈취 시 DB 를 덮어쓰는 경로가 생기지 않게).
	for k := range got {
		if strings.Contains(k, "/backup") && !strings.HasPrefix(k, "GET ") {
			t.Errorf("백업 라우트 %s 가 읽기 전용이 아님", k)
		}
		if strings.Contains(k, "restore") {
			t.Errorf("복구 실행 라우트가 있음: %s", k)
		}
	}
	if problems := contract.CheckRoutes(e.rt.Routes()); len(problems) != 0 {
		t.Fatal(problems)
	}
}

func TestOnboardingRoutesByRole(t *testing.T) {
	e := newPlatformEnv(t, true)
	type want struct {
		email              string
		onboarding, backup int
	}
	for _, w := range []want{
		{"admin@pl.test", 200, 200},
		{"auditor@pl.test", 403, 200},
		{"finance@pl.test", 403, 403},
		{"lead@pl.test", 403, 403},
		{"staff@pl.test", 403, 403},
	} {
		c := e.browser()
		e.login(c, w.email)
		if code, body := e.call(c, "GET", "/api/admin/onboarding", "", nil); code != w.onboarding {
			t.Errorf("%s 점검표: %d(기대 %d) %s", w.email, code, w.onboarding, body)
		}
		if code, body := e.call(c, "GET", "/api/admin/backup/status", "", nil); code != w.backup {
			t.Errorf("%s 백업 상태: %d(기대 %d) %s", w.email, code, w.backup, body)
		}
	}
	// 로그인하지 않은 호출은 401.
	if code, _ := e.call(e.browser(), "GET", "/api/admin/onboarding", "", nil); code != 401 {
		t.Errorf("미인증 점검표: %d", code)
	}
}

func TestOnboardingAdminFlow(t *testing.T) {
	e := newPlatformEnv(t, true)
	admin, aud := e.browser(), e.browser()
	e.login(admin, "admin@pl.test")
	e.login(aud, "auditor@pl.test")
	tok := map[string]string{"X-CSRF-Token": e.csrf(admin), "Content-Type": "application/json"}

	code, body := e.call(admin, "GET", "/api/admin/onboarding", "", nil)
	if code != 200 {
		t.Fatalf("점검표: %d %s", code, body)
	}
	var list struct {
		Items []struct {
			ID, State, Evidence, Link string
			Skippable                 bool
		} `json:"items"`
		Summary struct{ Total, Done, NotApplicable int } `json:"summary"`
	}
	if err := json.Unmarshal([]byte(body), &list); err != nil || len(list.Items) != 21 || list.Summary.Total != 21 {
		t.Fatalf("점검표 본문: %v %s", err, body)
	}
	for _, bad := range []string{"PL_", "argon2", "secret"} {
		if strings.Contains(body, bad) {
			t.Errorf("응답에 %q 가 있음", bad)
		}
	}
	// 표시: CSRF 토큰 없이는 거부, 권한 없는 역할은 403, 표시할 수 없는 항목·빈 사유는 422.
	if code, _ := e.call(admin, "POST", "/api/admin/onboarding/skips", `{"item":"opening_data","reason":"새로 시작"}`, map[string]string{"Content-Type": "application/json"}); code != 403 {
		t.Errorf("CSRF 없는 표시: %d", code)
	}
	if code, _ := e.call(aud, "POST", "/api/admin/onboarding/skips", `{"item":"opening_data","reason":"x"}`, map[string]string{"X-CSRF-Token": e.csrf(aud), "Content-Type": "application/json"}); code != 403 {
		t.Errorf("감사의 표시: %d", code)
	}
	if code, _ := e.call(admin, "POST", "/api/admin/onboarding/skips", `{"item":"company","reason":"x"}`, tok); code != 422 {
		t.Errorf("표시할 수 없는 항목: %d", code)
	}
	if code, _ := e.call(admin, "POST", "/api/admin/onboarding/skips", `{"item":"opening_data","reason":"  "}`, tok); code != 422 {
		t.Errorf("빈 사유: %d", code)
	}
	if code, _ := e.call(admin, "POST", "/api/admin/onboarding/skips", `{"item":"zzz","reason":"x"}`, tok); code != 404 {
		t.Errorf("없는 항목: %d", code)
	}
	if code, body := e.call(admin, "POST", "/api/admin/onboarding/skips", `{"item":"opening_data","reason":"처음부터 새로 시작"}`, tok); code != 204 {
		t.Fatalf("표시: %d %s", code, body)
	}
	if n := e.auditCount("onboarding.skip", "success"); n != 1 {
		t.Errorf("skip 감사 %d건", n)
	}
	_, body = e.call(admin, "GET", "/api/admin/onboarding", "", nil)
	if !strings.Contains(body, `"not_applicable"`) || !strings.Contains(body, "처음부터 새로 시작") {
		t.Errorf("표시가 점검표에 보이지 않음: %s", body)
	}
	if code, _ := e.call(admin, "DELETE", "/api/admin/onboarding/skips/opening_data", "", tok); code != 204 {
		t.Errorf("해제: %d", code)
	}
	if n := e.auditCount("onboarding.unskip", "success"); n != 1 {
		t.Errorf("unskip 감사 %d건", n)
	}
	// 백업 상태: 기록이 없으면 비어 있고 실행 버튼에 해당하는 쓰기 경로가 없다.
	code, body = e.call(admin, "GET", "/api/admin/backup/status", "", nil)
	if code != 200 || !strings.Contains(body, `"available":true`) {
		t.Errorf("백업 상태: %d %s", code, body)
	}
	if code, _ := e.call(admin, "POST", "/api/admin/backup/status", `{}`, tok); code == 200 || code == 204 {
		t.Errorf("백업 상태에 쓰기 경로가 있음: %d", code)
	}
}
