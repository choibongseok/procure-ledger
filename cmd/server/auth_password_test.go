package main

// B30 단계 2: 이메일+비밀번호를 실제 라우트·세션·감사 체인으로 끝까지 태운다.

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/config"
	"github.com/choibongseok/procure-ledger/internal/contract"
)

func cheapArgon(c *config.Config) {
	c.Auth.ArgonMemKiB, c.Auth.ArgonTime, c.Auth.ArgonPar = 64, 1, 1
}

func jsonHdr(extra map[string]string) map[string]string {
	h := map[string]string{"Content-Type": "application/json"}
	for k, v := range extra {
		h[k] = v
	}
	return h
}

func TestPasswordLoginHTTPFlow(t *testing.T) {
	e := newPlatformEnvWith(t, true, cheapArgon)
	admin := e.browser()
	e.login(admin, "admin@pl.test")
	csrf := e.csrf(admin)

	var staffID int64
	_ = e.d.DB.QueryRow(`SELECT id FROM users WHERE email='staff@pl.test'`).Scan(&staffID)

	// 관리자가 임시 비밀번호를 발급한다(개인 토큰으로는 못 한다: SessionOnly).
	code, body := e.call(admin, "POST", "/api/admin/users/"+itoa(staffID)+"/auth/start", `{"via":"temp"}`, map[string]string{"X-CSRF-Token": csrf})
	if code != 200 {
		t.Fatalf("임시 비밀번호 발급: %d %s", code, body)
	}
	var started struct {
		TempPassword string `json:"tempPassword"`
	}
	_ = json.Unmarshal([]byte(body), &started)
	if started.TempPassword == "" {
		t.Fatal("임시 비밀번호가 응답에 없음")
	}
	if code, _ := e.call(admin, "POST", "/api/admin/users/"+itoa(staffID)+"/auth/start", `{"via":"temp"}`, map[string]string{"X-CSRF-Token": csrf, "Authorization": "Bearer nope"}); code != 401 {
		t.Fatalf("잘못된 토큰: %d", code)
	}

	// 로그인 POST 는 JSON 만, 같은 사이트만.
	staff := e.browser()
	creds := `{"email":"staff@pl.test","password":"` + started.TempPassword + `"}`
	if code, _ := e.call(staff, "POST", "/api/auth/login/password", creds, map[string]string{"Content-Type": "text/plain"}); code != http.StatusUnsupportedMediaType {
		t.Fatalf("JSON 이 아닌 본문: %d", code)
	}
	if code, _ := e.call(staff, "POST", "/api/auth/login/password", creds, jsonHdr(map[string]string{"Origin": "http://evil.example"})); code != http.StatusForbidden {
		t.Fatalf("다른 사이트 Origin: %d", code)
	}
	if code, _ := e.call(staff, "POST", "/api/auth/login/password", creds, jsonHdr(map[string]string{"Sec-Fetch-Site": "cross-site"})); code != http.StatusForbidden {
		t.Fatalf("cross-site: %d", code)
	}
	code, body = e.call(staff, "POST", "/api/auth/login/password", creds, jsonHdr(nil))
	if code != 200 || !strings.Contains(body, `"setup":"password_change"`) {
		t.Fatalf("로그인: %d %s", code, body)
	}

	// 제한 세션: /api/me 는 되고 업무 API 는 403 + X-Auth-Setup, 토큰 발급도 불가.
	code, body = e.call(staff, "GET", "/api/me", "", nil)
	if code != 200 || !strings.Contains(body, `"setup":"password_change"`) {
		t.Fatalf("/api/me: %d %s", code, body)
	}
	var me struct {
		CSRF string `json:"csrfToken"`
	}
	_ = json.Unmarshal([]byte(body), &me)
	req, _ := http.NewRequest("GET", e.base+"/api/tokens", nil)
	res, err := staff.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 403 || res.Header.Get("X-Auth-Setup") != "password_change" {
		t.Fatalf("제한 세션이 /api/tokens 를 엶: %d %q", res.StatusCode, res.Header.Get("X-Auth-Setup"))
	}
	if code, _ := e.call(staff, "POST", "/api/tokens", `{"name":"x"}`, map[string]string{"X-CSRF-Token": me.CSRF}); code != 403 {
		t.Fatalf("제한 세션에서 토큰 발급: %d", code)
	}

	// 정책 위반은 구체 문구, 정상 변경 뒤에는 풀린다.
	change := func(cur, next string) (int, string) {
		return e.call(staff, "POST", "/api/auth/password/change", `{"current":"`+cur+`","new":"`+next+`"}`, jsonHdr(map[string]string{"X-CSRF-Token": me.CSRF}))
	}
	if code, body := change(started.TempPassword, "short"); code != 400 || !strings.Contains(body, "10자 이상") {
		t.Fatalf("정책 위반: %d %s", code, body)
	}
	if code, body := change(started.TempPassword, "Correct-Horse-9"); code != 204 {
		t.Fatalf("변경: %d %s", code, body)
	}
	if code, body = e.call(staff, "GET", "/api/me", "", nil); code != 200 || !strings.Contains(body, `"setup":""`) {
		t.Fatalf("변경 뒤 /api/me: %d %s", code, body)
	}
	if code, _ := e.call(staff, "GET", "/api/tokens", "", nil); code != 200 {
		t.Fatalf("변경 뒤 업무 API: %d", code)
	}

	// 같은 응답: 없는 이메일·틀린 비밀번호.
	other := e.browser()
	c1, b1 := e.call(other, "POST", "/api/auth/login/password", `{"email":"nobody@pl.test","password":"Correct-Horse-9"}`, jsonHdr(nil))
	c2, b2 := e.call(other, "POST", "/api/auth/login/password", `{"email":"staff@pl.test","password":"Wrong-Horse-9!"}`, jsonHdr(nil))
	if c1 != 401 || c1 != c2 || b1 != b2 {
		t.Fatalf("계정 열거 가능: %d %q / %d %q", c1, b1, c2, b2)
	}

	// 5회 실패 → 423 잠금, 관리자 해제.
	for i := 0; i < 5; i++ {
		e.call(other, "POST", "/api/auth/login/password", `{"email":"staff@pl.test","password":"Wrong-Horse-9!"}`, jsonHdr(nil))
	}
	if code, body := e.call(other, "POST", "/api/auth/login/password", `{"email":"staff@pl.test","password":"Correct-Horse-9"}`, jsonHdr(nil)); code != 423 || !strings.Contains(body, "locked") {
		t.Fatalf("잠금: %d %s", code, body)
	}
	if code, _ := e.call(admin, "POST", "/api/admin/users/"+itoa(staffID)+"/auth/unlock", "", map[string]string{"X-CSRF-Token": csrf}); code != 204 {
		t.Fatalf("해제: %d", code)
	}
	if code, _ := e.call(other, "POST", "/api/auth/login/password", `{"email":"staff@pl.test","password":"Correct-Horse-9"}`, jsonHdr(nil)); code != 200 {
		t.Fatalf("해제 뒤 로그인: %d", code)
	}

	// 일반 사용자는 관리 API 에 403, 관리자 목록에는 상태가 보이고 비밀이 없다.
	if code, _ := e.call(staff, "GET", "/api/admin/auth/users", "", nil); code != 403 {
		t.Fatalf("일반 사용자가 자격 목록을 봄: %d", code)
	}
	code, body = e.call(admin, "GET", "/api/admin/auth/users", "", nil)
	if code != 200 || !strings.Contains(body, `"hasPassword":true`) || strings.Contains(body, "argon2id") || strings.Contains(body, started.TempPassword) {
		t.Fatalf("목록: %d %s", code, body)
	}

	// 감사: 방식·잠금·변경이 남고 체인 검증이 통과한다.
	for _, a := range []string{"auth.password_temp_issue", "auth.password_change", "auth.account_lock", "auth.account_unlock"} {
		if e.auditCount(a, "success") != 1 {
			t.Errorf("감사 %s 가 1행이 아님", a)
		}
	}
	var leaked int
	_ = e.d.DB.QueryRow(`SELECT COUNT(*) FROM audit_logs WHERE diff LIKE '%' || ? || '%' OR detail LIKE '%' || ? || '%'`, "Correct-Horse-9", started.TempPassword).Scan(&leaked)
	if leaked != 0 {
		t.Fatal("비밀번호가 감사에 남음")
	}
	if res, err := e.d.AuditReader.Verify(t.Context()); err != nil || !res.OK {
		t.Fatalf("체인 검증: %+v %v", res, err)
	}

	// /api/auth/config 는 켜진 방식만 알린다.
	code, body = e.call(e.browser(), "GET", "/api/auth/config", "", nil)
	if code != 200 || !strings.Contains(body, `"emailLogin":true`) || !strings.Contains(body, `"passwordResetByMail":false`) {
		t.Fatalf("config: %d %s", code, body)
	}
	_ = contract.PermAuthManage
}

// 새 라우트는 모두 권한 선언을 가지며, 자격 관리 라우트는 auth:manage 이다(AC-1074).
func TestPermissionDeclarationsPasswordRoutes(t *testing.T) {
	e := newPlatformEnv(t, false)
	want := map[string]contract.Permission{
		"POST /api/auth/login/password":              contract.PermPublic,
		"POST /api/auth/password/forgot":             contract.PermPublic,
		"POST /api/auth/password/reset":              contract.PermPublic,
		"POST /api/auth/password/change":             contract.PermSetup,
		"POST /api/auth/login/totp":                  contract.PermPublic,
		"POST /api/auth/totp/enroll":                 contract.PermSetup,
		"POST /api/auth/totp/confirm":                contract.PermSetup,
		"POST /api/auth/totp/disable":                contract.PermSetup,
		"POST /api/admin/users/{id}/auth/totp-reset": contract.PermAuthManage,
		"GET /api/admin/auth/users":                  contract.PermAuthManage,
		"POST /api/admin/users/{id}/auth/start":      contract.PermAuthManage,
		"POST /api/admin/users/{id}/auth/unlock":     contract.PermAuthManage,
	}
	got := map[string]contract.Permission{}
	for _, r := range e.rt.Routes() {
		got[r.Method+" "+r.Pattern] = r.Permission
	}
	for k, p := range want {
		if got[k] != p {
			t.Errorf("%s: 선언 %q, 기대 %q", k, got[k], p)
		}
	}
	if problems := contract.CheckRoutes(e.rt.Routes()); len(problems) != 0 {
		t.Fatal(problems)
	}
}
