package main

// B30 실제 서버 경로 시험: 모의 SMTP 로 재설정 링크, 모의 OIDC IdP 로 구글·MS 거부 사례, 설정 화면 API 보호 규칙을 라우트·쿠키·감사 체인으로 본다.

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/choibongseok/procure-ledger/internal/auth"
	"github.com/choibongseok/procure-ledger/internal/auth/authtest"
	"github.com/choibongseok/procure-ledger/internal/config"
)

// 재설정 링크: 30분·1회·새 발급 시 이전 무효, 링크 기준 주소는 PL_BASE_URL(Host 헤더 위조 무시), 응답은 계정 유무와 무관.
func TestResetLinkOverRealSMTP(t *testing.T) {
	smtp := authtest.NewFakeSMTP(t)
	e := newPlatformEnvWith(t, true, func(c *config.Config) {
		cheapArgon(c)
		c.Auth.SMTPHost, c.Auth.SMTPPort, c.Auth.SMTPFrom, c.Auth.SMTPInsecure = smtp.Host, smtp.Port, "erp@example.test", true
	})
	admin := e.browser()
	e.login(admin, "admin@pl.test")
	csrf := e.csrf(admin)
	var staffID int64
	_ = e.d.DB.QueryRow(`SELECT id FROM users WHERE email='staff@pl.test'`).Scan(&staffID)

	// 관리자가 설정 링크로 시작(SMTP 가 있으므로 mail).
	if code, body := e.call(admin, "POST", "/api/admin/users/"+itoa(staffID)+"/auth/start", `{"via":"mail"}`, map[string]string{"X-CSRF-Token": csrf}); code != 200 {
		t.Fatalf("설정 링크: %d %s", code, body)
	}
	wait := func(n int) []string {
		for i := 0; i < 100; i++ {
			if m := smtp.Messages(); len(m) >= n {
				return m
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Fatalf("메일 %d통을 받지 못함: %d", n, len(smtp.Messages()))
		return nil
	}
	tokenOf := func(msg string) string {
		m := regexp.MustCompile(`token=([A-Za-z0-9_%-]+)`).FindStringSubmatch(msg)
		if m == nil {
			t.Fatalf("링크가 메일에 없음: %q", msg)
		}
		tok, _ := url.QueryUnescape(m[1])
		return tok
	}
	if msg := wait(1)[0]; !strings.Contains(msg, e.base+"/login/reset?token=") {
		t.Fatalf("설정 링크 기준 주소: %q", msg)
	}
	setupTok := tokenOf(smtp.Messages()[0])
	reset := func(tok, pw string) (int, string) {
		return e.call(e.browser(), "POST", "/api/auth/password/reset", `{"token":"`+tok+`","password":"`+pw+`"}`, jsonHdr(nil))
	}
	if code, body := reset(setupTok, "Setup-Pass-123"); code != 200 {
		t.Fatalf("설정 링크로 비밀번호: %d %s", code, body)
	}
	if code, _ := reset(setupTok, "Another-Pass-9!"); code != 400 {
		t.Fatalf("링크 재사용: %d", code)
	}

	// 재설정 요청: 없는 이메일과 같은 응답, Host 헤더를 위조해도 링크는 PL_BASE_URL.
	c := e.browser()
	cNo, bNo := e.call(c, "POST", "/api/auth/password/forgot", `{"email":"nobody@pl.test"}`, jsonHdr(nil))
	forged := jsonHdr(nil)
	req, _ := http.NewRequest("POST", e.base+"/api/auth/password/forgot", strings.NewReader(`{"email":"staff@pl.test"}`))
	for k, v := range forged {
		req.Header.Set(k, v)
	}
	req.Header.Set("Origin", e.base)
	req.Host = "evil.example"
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	realBody, _ := io.ReadAll(res.Body)
	res.Body.Close()
	cYes, bYes := e.call(c, "POST", "/api/auth/password/forgot", `{"email":"nobody2@pl.test"}`, jsonHdr(nil))
	if cNo != 200 || cYes != 200 || res.StatusCode != 200 || bNo != bYes || string(realBody) != bNo {
		t.Fatalf("응답이 계정 유무에 따라 다름: %d %q / %d %q / %d", cNo, bNo, cYes, bYes, res.StatusCode)
	}
	msgs := wait(2)
	if len(msgs) != 2 || strings.Contains(msgs[1], "evil.example") || !strings.Contains(msgs[1], e.base+"/login/reset?token=") {
		t.Fatalf("Host 위조가 링크에 반영됨: %q", msgs[len(msgs)-1])
	}
	tok1 := tokenOf(msgs[1])
	// 만료(30분): 만료 시각을 과거로 돌리면 거부된다. 새로 발급하면 이전 링크는 무효.
	e.d.DB.Exec(`UPDATE password_reset_tokens SET expires_at = '2000-01-01T00:00:00Z' WHERE token_hash IS NOT NULL`)
	if code, _ := reset(tok1, "Brand-New-Pass-5!"); code != 400 {
		t.Fatalf("만료된 링크: %d", code)
	}
	e.call(c, "POST", "/api/auth/password/forgot", `{"email":"staff@pl.test"}`, jsonHdr(nil))
	tok2 := tokenOf(wait(3)[2])
	if tok2 == tok1 {
		t.Fatal("새 토큰이 같음")
	}
	if code, _ := reset(tok2, "Brand-New-Pass-5!"); code != 200 {
		t.Fatalf("새 링크: %d", code)
	}
	if code, body := e.call(e.browser(), "POST", "/api/auth/login/password", `{"email":"staff@pl.test","password":"Brand-New-Pass-5!"}`, jsonHdr(nil)); code != 200 || strings.Contains(body, "password_change") {
		t.Fatalf("재설정한 비밀번호로 로그인: %d %s", code, body)
	}
	var leaked int
	_ = e.d.DB.QueryRow(`SELECT COUNT(*) FROM audit_logs WHERE diff LIKE '%' || ? || '%' OR detail LIKE '%' || ? || '%'`, tok2, "Brand-New-Pass-5!").Scan(&leaked)
	if leaked != 0 {
		t.Fatal("링크·비밀번호가 감사에 남음")
	}
	if r, err := e.d.AuditReader.Verify(t.Context()); err != nil || !r.OK {
		t.Fatalf("체인: %+v %v", r, err)
	}
}

// 모의 IdP 로 구글·MS 로그인을 실제 라우트(/api/auth/login?provider=…, /api/auth/callback)로 돌린다.
func TestGoogleMicrosoftOverRealRoutes(t *testing.T) {
	const tenant = "11111111-2222-3333-4444-555555555555"
	gIdP := authtest.NewMockIdP(t, "", "g-client")
	mIdP := authtest.NewMockIdP(t, "/"+tenant+"/v2.0", "m-client")
	e := newPlatformEnvWith(t, true, func(c *config.Config) {
		cheapArgon(c)
		c.Auth.GoogleClientID, c.Auth.GoogleClientSecret = "g-client", "g-secret"
		c.Auth.MSClientID, c.Auth.MSClientSecret = "m-client", "m-secret"
	})
	svc := e.d.Authenticator.(*auth.Service)
	svc.SetIssuerForTest(auth.MethodGoogle, gIdP.Issuer)
	svc.SetIssuerForTest(auth.MethodMicrosoft, mIdP.Issuer)
	noRedirect := func() *http.Client {
		c := e.browser()
		c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		return c
	}
	get := func(c *http.Client, path string) *http.Response {
		res, err := c.Get(e.base + path)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res
	}
	// 꺼진 방식은 404.
	if res := get(noRedirect(), "/api/auth/login?provider=google"); res.StatusCode != 404 {
		t.Fatalf("꺼진 구글: %d", res.StatusCode)
	}
	e.d.DB.Exec(`UPDATE auth_providers SET enabled=1, tested_at='2026-11-12T00:00:00Z', config_json='{"allowedDomains":["pl.test"]}' WHERE provider='google'`)
	e.d.DB.Exec(`UPDATE auth_providers SET enabled=1, tested_at='2026-11-12T00:00:00Z', config_json='{"tenantId":"` + tenant + `"}' WHERE provider='microsoft'`)
	_, body := e.call(e.browser(), "GET", "/api/auth/config", "", nil)
	if !strings.Contains(body, `"id":"google"`) || !strings.Contains(body, `"id":"microsoft"`) {
		t.Fatalf("켜진 방식이 로그인 화면 설정에 없음: %s", body)
	}

	run := func(provider string, idp *authtest.MockIdP, claims map[string]any) *http.Response {
		c := noRedirect()
		res := get(c, "/api/auth/login?provider="+provider)
		if res.StatusCode != 302 {
			t.Fatalf("%s 시작: %d", provider, res.StatusCode)
		}
		loc := res.Header.Get("Location")
		state, code := idp.Issue(loc, claims)
		return get(c, "/api/auth/callback?state="+url.QueryEscape(state)+"&code="+url.QueryEscape(code))
	}
	good := func() map[string]any {
		return map[string]any{"sub": "g-1", "email": "staff@pl.test", "email_verified": true, "hd": "pl.test"}
	}
	with := func(k string, v any) map[string]any { m := good(); m[k] = v; return m }
	for name, claims := range map[string]map[string]any{
		"hd 없음(개인 계정)": with("hd", nil), "hd 불일치": with("hd", "evil.test"), "이메일 미검증": with("email_verified", false), "미등록": with("email", "ghost@pl.test"),
	} {
		res := run("google", gIdP, claims)
		if res.StatusCode != 302 || !strings.Contains(res.Header.Get("Location"), "/login?error=forbidden") || len(res.Cookies()) != 0 {
			t.Errorf("구글 %s: %d %s", name, res.StatusCode, res.Header.Get("Location"))
		}
	}
	if res := run("google", gIdP, with("aud", "other")); !strings.Contains(res.Header.Get("Location"), "error=idp") {
		t.Errorf("구글 aud 불일치: %s", res.Header.Get("Location"))
	}
	ok := run("google", gIdP, good())
	if ok.StatusCode != 302 || ok.Header.Get("Location") != "/" || len(ok.Cookies()) == 0 || !ok.Cookies()[0].HttpOnly {
		t.Fatalf("구글 정상: %d %s", ok.StatusCode, ok.Header.Get("Location"))
	}
	ms := func(oid, tid, email string) map[string]any {
		return map[string]any{"sub": "p-" + oid, "oid": oid, "tid": tid, "email": email}
	}
	if res := run("microsoft", mIdP, ms("oid-1", "99999999-2222-3333-4444-555555555555", "lead@pl.test")); !strings.Contains(res.Header.Get("Location"), "error=forbidden") {
		t.Errorf("MS tid 불일치: %s", res.Header.Get("Location"))
	}
	if res := run("microsoft", mIdP, ms("oid-1", tenant, "lead@pl.test")); res.StatusCode != 302 || res.Header.Get("Location") != "/" || len(res.Cookies()) == 0 {
		t.Fatalf("MS 정상: %d %s", res.StatusCode, res.Header.Get("Location"))
	}
	// 같은 oid 가 이메일을 다른 사람 것으로 바꿔 와도 원래 사용자, 다른 oid 가 같은 이메일로 오면 거부.
	if res := run("microsoft", mIdP, ms("oid-2", tenant, "lead@pl.test")); !strings.Contains(res.Header.Get("Location"), "error=forbidden") {
		t.Errorf("다른 oid 의 재연결: %s", res.Header.Get("Location"))
	}
	var users int
	_ = e.d.DB.QueryRow(`SELECT COUNT(*) FROM users WHERE email NOT LIKE '%@pl.test'`).Scan(&users)
	if users != 0 {
		t.Fatal("외부 로그인이 계정을 만듦")
	}
	var leaked int
	_ = e.d.DB.QueryRow(`SELECT COUNT(*) FROM audit_logs WHERE diff LIKE '%g-secret%' OR diff LIKE '%m-secret%' OR detail LIKE '%secret%'`).Scan(&leaked)
	if leaked != 0 {
		t.Fatal("클라이언트 비밀이 감사에 남음")
	}
	if e.auditCount("session.login", "denied") < 5 {
		t.Fatal("거부 감사가 남지 않음")
	}
	if r, err := e.d.AuditReader.Verify(t.Context()); err != nil || !r.OK {
		t.Fatalf("체인: %+v %v", r, err)
	}
}

// 설정 화면 API: 권한 없는 역할은 403, 비밀값이 응답에 없고, 이메일 로그인 끄기 보호 규칙이 서버 경로에서도 동작한다.
func TestAuthSettingsAPI(t *testing.T) {
	e := newPlatformEnvWith(t, true, func(c *config.Config) {
		cheapArgon(c)
		c.Auth.GoogleClientID, c.Auth.GoogleClientSecret = "g-client", "super-secret-value"
	})
	admin := e.browser()
	e.login(admin, "admin@pl.test")
	h := map[string]string{"X-CSRF-Token": e.csrf(admin)}
	staff := e.browser()
	e.login(staff, "staff@pl.test")
	if code, _ := e.call(staff, "GET", "/api/admin/auth/providers", "", nil); code != 403 {
		t.Fatalf("일반 사용자: %d", code)
	}
	code, body := e.call(admin, "GET", "/api/admin/auth/providers", "", nil)
	if code != 200 || strings.Contains(body, "super-secret-value") || !strings.Contains(body, `"envReady":true`) {
		t.Fatalf("목록: %d %s", code, body)
	}
	if !strings.Contains(body, `"emergencyAdmins":[]`) || !strings.Contains(body, `"totpRequiredPerms":["role:manage","auth:manage"]`) {
		t.Fatalf("정책 배열은 null 이 아니라 []: %s", body)
	}
	var list struct {
		Providers []struct {
			Provider string `json:"provider"`
			EnvReady bool   `json:"envReady"`
		} `json:"providers"`
	}
	_ = json.Unmarshal([]byte(body), &list)
	if len(list.Providers) != 3 {
		t.Fatalf("카드 3장이어야 함: %s", body)
	}
	if code, b := e.call(admin, "PUT", "/api/admin/auth/providers/google", `{"enabled":true}`, jsonHdr(h)); code != 409 {
		t.Fatalf("도메인·시험 없이 켬: %d %s", code, b)
	}
	if code, b := e.call(admin, "PUT", "/api/admin/auth/providers/google", `{"allowedDomains":["pl.test"]}`, jsonHdr(h)); code != 200 {
		t.Fatalf("도메인 저장: %d %s", code, b)
	}
	if code, b := e.call(admin, "PUT", "/api/admin/auth/policy", `{"emailLoginEnabled":false}`, jsonHdr(h)); code != 409 {
		t.Fatalf("보호 조건 없이 이메일 로그인 끄기: %d %s", code, b)
	}
	if code, b := e.call(admin, "PUT", "/api/admin/auth/policy", `{"password":{"minLength":12,"maxLength":128,"requireClasses":3,"historyCount":3,"expireDays":0,"banCommon":true}}`, jsonHdr(h)); code != 200 {
		t.Fatalf("정책 저장: %d %s", code, b)
	}
	// 개인 토큰으로는 설정을 바꿀 수 없다(세션 전용).
	_, tb := e.call(admin, "POST", "/api/tokens", `{"name":"t","ttlDays":1}`, jsonHdr(h))
	var tk struct {
		Token string `json:"token"`
	}
	_ = json.Unmarshal([]byte(tb), &tk)
	if code, _ := e.call(e.browser(), "PUT", "/api/admin/auth/policy", `{"emailLoginEnabled":false}`, jsonHdr(map[string]string{"Authorization": "Bearer " + tk.Token})); code != 403 {
		t.Fatalf("개인 토큰으로 정책 변경: %d", code)
	}
	if e.auditCount("auth.policy_update", "success") != 1 || e.auditCount("auth.provider_update", "success") != 1 {
		t.Fatal("정책·방식 변경 감사")
	}
}
