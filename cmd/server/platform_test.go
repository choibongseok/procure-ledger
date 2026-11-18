package main

// platform 플랫폼 통합 테스트: 실제 SQLite·dev IdP·세션·토큰·감사 체인을 httptest 서버로 끝까지 태운다.
// 업무(erp)·AI(ai) 서비스는 연결하지 않는다(wirePlatform 만).

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/choibongseok/procure-ledger/internal/auth"
	"github.com/choibongseok/procure-ledger/internal/bootstrap"
	"github.com/choibongseok/procure-ledger/internal/config"
	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/deps"
	"github.com/choibongseok/procure-ledger/internal/dbtest"
	"github.com/choibongseok/procure-ledger/migrations"
)

type platformEnv struct {
	t    *testing.T
	ts   *httptest.Server
	d    deps.Deps
	rt   *contract.RouteTable
	base string
}

func newPlatformEnv(t *testing.T, devIDP bool) *platformEnv {
	t.Helper()
	return newPlatformEnvWith(t, devIDP, nil)
}

// newPlatformEnvWith 는 설정을 고칠 수 있는 변형이다(외부 앵커를 켠 구성 등).
func newPlatformEnvWith(t *testing.T, devIDP bool, tweak func(*config.Config)) *platformEnv {
	t.Helper()
	db := dbtest.Open(t)
	if err := migrations.Apply(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewUnstartedServer(nil)
	base := "http://" + ts.Listener.Addr().String()
	cfg := config.Config{DevIDP: devIDP, BaseURL: base}
	if tweak != nil {
		tweak(&cfg)
	}
	d := deps.Deps{DB: db}
	if err := bootstrap.WirePlatform(cfg, &d); err != nil {
		t.Fatal(err)
	}
	rt := buildRoutes(cfg, d)
	ts.Config.Handler = handlerFor(rt, d)
	ts.Start()
	t.Cleanup(ts.Close)
	return &platformEnv{t: t, ts: ts, d: d, rt: rt, base: base}
}

// browser 는 쿠키 항아리를 가진 클라이언트다.
func (e *platformEnv) browser() *http.Client {
	jar, _ := cookiejar.New(nil)
	return &http.Client{Jar: jar, Timeout: 15 * time.Second} // 멈춘 서버는 시험을 영구히 붙들지 않고 실패시킨다
}

func (e *platformEnv) login(c *http.Client, email string) {
	e.t.Helper()
	res, err := c.Get(e.base + "/api/auth/login?login_hint=" + url.QueryEscape(email))
	if err != nil {
		e.t.Fatal(err)
	}
	res.Body.Close()
	if !strings.HasSuffix(res.Request.URL.Path, "/") && res.Request.URL.Path != "/" {
		e.t.Fatalf("로그인 후 이동 경로가 이상함: %s", res.Request.URL)
	}
}

func (e *platformEnv) call(c *http.Client, method, path, body string, hdr map[string]string) (int, string) {
	e.t.Helper()
	req, err := http.NewRequest(method, e.base+path, strings.NewReader(body))
	if err != nil {
		e.t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Origin", e.base) // 브라우저는 상태 변경 요청에 항상 Origin 을 보낸다
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	res, err := c.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(b)
}

func (e *platformEnv) csrf(c *http.Client) string {
	e.t.Helper()
	code, body := e.call(c, "GET", "/api/me", "", nil)
	if code != 200 {
		e.t.Fatalf("/api/me: %d %s", code, body)
	}
	var me struct {
		CSRFToken string `json:"csrfToken"`
	}
	_ = json.Unmarshal([]byte(body), &me)
	return me.CSRFToken
}

func (e *platformEnv) auditCount(action, result string) int {
	var n int
	if err := e.d.DB.QueryRow(`SELECT COUNT(*) FROM audit_logs WHERE action = ? AND result = ?`, action, result).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

// ---- 권한 선언 누락 검사 (실제 구성) ----

func TestPermissionDeclarationsPlatformRoutes(t *testing.T) {
	e := newPlatformEnv(t, true)
	routes := e.rt.Routes()
	if problems := contract.CheckRoutes(routes); len(problems) != 0 {
		t.Fatalf("권한 선언 문제: %v", problems)
	}
	want := map[string]contract.Permission{
		"GET /api/audit-logs": contract.PermAuditRead, "POST /api/audit-logs/verify": contract.PermAuditVerify,
		"POST /api/audit-logs/anchor": contract.PermAuditVerify,
		"GET /api/tokens":             contract.PermTokenManage, "POST /api/tokens": contract.PermTokenManage,
		"DELETE /api/tokens/{id}": contract.PermTokenManage, "GET /api/admin/users": contract.PermUserManage,
		"GET /api/me": contract.PermSetup, "POST /api/auth/logout": contract.PermSetup,
		"GET /api/auth/login": contract.PermPublic, "GET /dev-idp/authorize": contract.PermPublic,
	}
	got := map[string]contract.Permission{}
	for _, r := range routes {
		got[r.Method+" "+r.Pattern] = r.Permission
	}
	for k, p := range want {
		if got[k] != p {
			t.Errorf("%s 선언 %q, 기대 %q", k, got[k], p)
		}
	}
	// 보호된 모든 라우트는 미인증에 401, 쿠키 없는 임의 Bearer 에도 401.
	for _, r := range routes {
		if r.Permission == contract.PermPublic {
			continue
		}
		if strings.HasPrefix(r.Pattern, "/scim/v2") { // SCIM 은 연동 토큰(pls_)으로만 인증하고 인증 실패 반복에는 IP 제한이 걸린다: TestSCIMRoutesDeclared 가 따로 본다
			continue
		}
		path := strings.NewReplacer("{id}", "1", "{path...}", "x").Replace(r.Pattern)
		if code, _ := e.call(http.DefaultClient, r.Method, path, "", nil); code != http.StatusUnauthorized {
			t.Errorf("%s %s 미인증이 %d", r.Method, r.Pattern, code)
		}
		if code, _ := e.call(http.DefaultClient, r.Method, path, "", map[string]string{"Authorization": "Bearer plp_bogus"}); code != http.StatusUnauthorized {
			t.Errorf("%s %s 잘못된 토큰이 %d", r.Method, r.Pattern, code)
		}
	}
}

// 역할별로 권한 표대로 강제되고, 거부는 감사 로그에 남는다.
func TestRoleEnforcementAndDeniedAudit(t *testing.T) {
	e := newPlatformEnv(t, true)
	staff, auditor, admin := e.browser(), e.browser(), e.browser()
	e.login(staff, "staff@pl.test")
	e.login(auditor, "auditor@pl.test")
	e.login(admin, "admin@pl.test")

	if code, _ := e.call(staff, "GET", "/api/audit-logs", "", nil); code != http.StatusForbidden {
		t.Fatalf("staff 감사 로그: %d", code)
	}
	if e.auditCount("access.denied", "denied") != 1 {
		t.Fatal("staff 의 거부가 감사 로그에 없음")
	}
	if code, _ := e.call(staff, "GET", "/api/admin/users", "", nil); code != http.StatusForbidden {
		t.Fatalf("staff 사용자 목록: %d", code)
	}
	if code, body := e.call(auditor, "GET", "/api/audit-logs", "", nil); code != 200 || !strings.Contains(body, "session.login") {
		t.Fatalf("auditor 감사 로그: %d %s", code, body)
	}
	if code, _ := e.call(auditor, "GET", "/api/admin/users", "", nil); code != http.StatusForbidden {
		t.Fatalf("auditor 는 사용자 관리 불가: %d", code)
	}
	if code, _ := e.call(admin, "GET", "/api/admin/users", "", nil); code != 200 {
		t.Fatalf("admin 사용자 목록: %d", code)
	}
	// 체인 검증: auditor 가능, staff 불가.
	h := map[string]string{"X-CSRF-Token": e.csrf(auditor)}
	code, body := e.call(auditor, "POST", "/api/audit-logs/verify", "", h)
	if code != 200 || !strings.Contains(body, `"ok":true`) {
		t.Fatalf("체인 검증: %d %s", code, body)
	}
	if code, _ := e.call(staff, "POST", "/api/audit-logs/verify", "", map[string]string{"X-CSRF-Token": e.csrf(staff)}); code != http.StatusForbidden {
		t.Fatalf("staff 체인 검증: %d", code)
	}
}

// ---- OIDC 로그인 ----

func TestOIDCLoginViaDevIdP(t *testing.T) {
	e := newPlatformEnv(t, true)
	// 단계별로 따라가며 쿠키 속성과 리다이렉트를 확인한다.
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := c.Get(e.base + "/api/auth/login?returnTo=/audit-logs&login_hint=staff@pl.test")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	authURL, _ := url.Parse(res.Header.Get("Location"))
	q := authURL.Query()
	if !strings.HasSuffix(authURL.Path, "/dev-idp/authorize") || q.Get("code_challenge_method") != "S256" ||
		q.Get("code_challenge") == "" || q.Get("state") == "" || q.Get("nonce") == "" || q.Get("response_type") != "code" {
		t.Fatalf("인증 요청이 표준 흐름이 아님: %s", authURL)
	}
	// PKCE 없는 인증 요청은 IdP 가 거부한다.
	bad := *authURL
	bq := bad.Query()
	bq.Del("code_challenge")
	bad.RawQuery = bq.Encode()
	if res, _ := c.Get(bad.String()); res.StatusCode != http.StatusBadRequest {
		t.Fatalf("PKCE 없는 요청이 %d", res.StatusCode)
	}

	res, _ = c.Get(authURL.String())
	res.Body.Close()
	cb := res.Header.Get("Location")
	if !strings.Contains(cb, "/api/auth/callback?code=") {
		t.Fatalf("콜백 주소: %s", cb)
	}
	// 코드를 훔쳐도 verifier 가 없으면 토큰 교환이 안 된다.
	cbu, _ := url.Parse(cb)
	form := url.Values{"grant_type": {"authorization_code"}, "code": {cbu.Query().Get("code")}, "redirect_uri": {e.base + "/api/auth/callback"},
		"client_id": {"procure-ledger-dev"}, "client_secret": {"procure-ledger-dev-secret"}, "code_verifier": {"wrong-verifier"}}
	pres, _ := http.PostForm(e.base+"/dev-idp/token", form)
	pres.Body.Close()
	if pres.StatusCode != http.StatusBadRequest {
		t.Fatalf("잘못된 verifier 가 %d", pres.StatusCode)
	}

	// 코드는 위 시도로 소비됐다. 새로 받아 정상 콜백한다.
	res, _ = c.Get(authURL.String())
	res.Body.Close()
	res, _ = c.Get(res.Header.Get("Location"))
	res.Body.Close()
	if res.StatusCode != http.StatusFound || res.Header.Get("Location") != "/audit-logs" {
		t.Fatalf("로그인 후 이동: %d %s", res.StatusCode, res.Header.Get("Location"))
	}
	var sess *http.Cookie
	for _, ck := range res.Cookies() {
		if ck.Name == "pl_session" {
			sess = ck
		}
	}
	if sess == nil || !sess.HttpOnly || sess.SameSite != http.SameSiteLaxMode || sess.Path != "/" {
		t.Fatalf("세션 쿠키 속성: %+v", sess)
	}
	// 세션 원문은 DB 에 없다(해시만).
	var n int
	_ = e.d.DB.QueryRow(`SELECT COUNT(*) FROM sessions WHERE id_hash = ? OR csrf_hash = ?`, sess.Value, sess.Value).Scan(&n)
	if n != 0 {
		t.Fatal("세션 원문이 저장됨")
	}
	if e.auditCount("session.login", "success") != 1 {
		t.Fatal("로그인 감사 기록 없음")
	}
}

func TestLoginFailures(t *testing.T) {
	e := newPlatformEnv(t, true)
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	get := func(u string) *http.Response {
		res, err := c.Get(u)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res
	}
	begin := func(hint string) (callback string) {
		res := get(e.base + "/api/auth/login?login_hint=" + hint)
		return get(res.Header.Get("Location")).Header.Get("Location")
	}

	// 모르는 state
	if loc := get(e.base + "/api/auth/callback?code=x&state=nope").Header.Get("Location"); loc != "/login?error=state" {
		t.Errorf("모르는 state: %s", loc)
	}
	// state 재사용(재생)
	cb := begin("staff@pl.test")
	if loc := get(cb).Header.Get("Location"); loc != "/" {
		t.Fatalf("첫 콜백: %s", loc)
	}
	if loc := get(cb).Header.Get("Location"); loc != "/login?error=state" {
		t.Errorf("콜백 재생이 통과됨: %s", loc)
	}
	// nonce 불일치
	cb = begin("staff@pl.test")
	if _, err := e.d.DB.Exec(`UPDATE oidc_auth_requests SET nonce = 'tampered'`); err != nil {
		t.Fatal(err)
	}
	if loc := get(cb).Header.Get("Location"); loc != "/login?error=idp" {
		t.Errorf("nonce 불일치: %s", loc)
	}
	// 만료된 로그인 요청
	cb = begin("staff@pl.test")
	if _, err := e.d.DB.Exec(`UPDATE oidc_auth_requests SET expires_at = '2000-01-01T00:00:00Z'`); err != nil {
		t.Fatal(err)
	}
	if loc := get(cb).Header.Get("Location"); loc != "/login?error=state" {
		t.Errorf("만료된 요청: %s", loc)
	}
	// 비활성 사용자는 로그인되지 않고 거부가 기록된다.
	if _, err := e.d.DB.Exec(`UPDATE users SET active = 0 WHERE email = 'staff2@pl.test'`); err != nil {
		t.Fatal(err)
	}
	if loc := get(begin("staff2@pl.test")).Header.Get("Location"); loc != "/login?error=forbidden" {
		t.Errorf("비활성 사용자: %s", loc)
	}
	if e.auditCount("session.login", "denied") != 1 {
		t.Error("로그인 거부가 감사 로그에 없음")
	}
	// 오픈 리다이렉트 차단
	res := get(e.base + "/api/auth/login?returnTo=//evil.example&login_hint=staff@pl.test")
	cb = get(res.Header.Get("Location")).Header.Get("Location")
	if loc := get(cb).Header.Get("Location"); loc != "/" {
		t.Errorf("외부 returnTo 가 통과됨: %s", loc)
	}
}

// 외부 IdP 설정이 없고 dev IdP 도 꺼져 있으면 로그인은 열리지 않고, dev IdP 라우트는 존재하지 않는다.
func TestDevIdPOffByDefault(t *testing.T) {
	e := newPlatformEnv(t, false)
	for _, r := range e.rt.Routes() {
		if strings.HasPrefix(r.Pattern, "/dev-idp") {
			t.Errorf("dev IdP 라우트가 기본값에서 등록됨: %s", r.Pattern)
		}
	}
	if code, _ := e.call(http.DefaultClient, "GET", "/api/auth/login", "", nil); code != http.StatusServiceUnavailable {
		t.Errorf("로그인 미설정이 %d", code)
	}
	_, body := e.call(http.DefaultClient, "GET", "/api/auth/config", "", nil)
	if !strings.Contains(body, `"devIdp":false`) || strings.Contains(body, "pl.test") {
		t.Errorf("config: %s", body)
	}
	var n int
	_ = e.d.DB.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&n)
	if n != 0 {
		t.Error("dev IdP 가 꺼져 있는데 시드 사용자가 들어감")
	}
	// 기본 설정값 자체가 꺼짐이다.
	if config.Load().DevIDP {
		t.Error("config 기본값에서 DevIDP 가 켜져 있음")
	}
	// 외부 IdP 와 동시에 켤 수 없다.
	_, err := auth.New(auth.Options{DB: e.d.DB, Txs: e.d.Txs, Audit: e.d.Audit, DevIDP: true, OIDC: auth.OIDCConfig{Issuer: "https://idp.example", ClientID: "x"}})
	if err == nil {
		t.Error("dev IdP 와 외부 IdP 동시 설정이 허용됨")
	}
}

// ---- 세션·CSRF ----

func TestCSRFAndSessionLifecycle(t *testing.T) {
	e := newPlatformEnv(t, true)
	c := e.browser()
	e.login(c, "staff@pl.test")
	token := e.csrf(c)
	if token == "" {
		t.Fatal("csrfToken 이 없음")
	}
	body := `{"name":"노트북","ttlDays":7}`
	if code, _ := e.call(c, "POST", "/api/tokens", body, nil); code != http.StatusForbidden {
		t.Errorf("CSRF 헤더 없는 POST 가 %d", code)
	}
	if code, _ := e.call(c, "POST", "/api/tokens", body, map[string]string{"X-CSRF-Token": "wrong"}); code != http.StatusForbidden {
		t.Errorf("틀린 CSRF 가 %d", code)
	}
	if code, _ := e.call(c, "POST", "/api/tokens", body, map[string]string{"X-CSRF-Token": token, "Origin": "http://evil.example"}); code != http.StatusForbidden {
		t.Errorf("다른 Origin 이 %d", code)
	}
	if code, _ := e.call(c, "POST", "/api/tokens", body, map[string]string{"X-CSRF-Token": token, "Origin": e.base}); code != http.StatusCreated {
		t.Errorf("정상 요청이 %d", code)
	}
	// 다른 세션의 토큰은 쓸 수 없다.
	other := e.browser()
	e.login(other, "staff@pl.test")
	if code, _ := e.call(other, "POST", "/api/tokens", body, map[string]string{"X-CSRF-Token": token}); code != http.StatusForbidden {
		t.Errorf("남의 세션 CSRF 토큰이 통과됨: %d", code)
	}

	// 로그아웃하면 세션이 폐기된다.
	if code, _ := e.call(c, "POST", "/api/auth/logout", "", map[string]string{"X-CSRF-Token": token}); code != http.StatusNoContent {
		t.Fatalf("로그아웃: %d", code)
	}
	if code, _ := e.call(c, "GET", "/api/me", "", nil); code != http.StatusUnauthorized {
		t.Errorf("로그아웃 뒤 /api/me: %d", code)
	}
	if e.auditCount("session.logout", "success") != 1 {
		t.Error("로그아웃 감사 기록 없음")
	}
	// 만료된 세션
	if _, err := e.d.DB.Exec(`UPDATE sessions SET expires_at = '2000-01-01T00:00:00Z'`); err != nil {
		t.Fatal(err)
	}
	if code, _ := e.call(other, "GET", "/api/me", "", nil); code != http.StatusUnauthorized {
		t.Errorf("만료 세션: %d", code)
	}
}

// ---- 개인 토큰 ----

func TestPersonalTokenLifecycle(t *testing.T) {
	e := newPlatformEnv(t, true)
	c := e.browser()
	e.login(c, "staff@pl.test")
	h := map[string]string{"X-CSRF-Token": e.csrf(c)}

	code, body := e.call(c, "POST", "/api/tokens", `{"name":"mcp","ttlDays":30}`, h)
	if code != http.StatusCreated {
		t.Fatalf("발급: %d %s", code, body)
	}
	var issued struct {
		Token string `json:"token"`
		Info  struct {
			ID     int64  `json:"id"`
			Prefix string `json:"prefix"`
		} `json:"info"`
	}
	_ = json.Unmarshal([]byte(body), &issued)
	if !strings.HasPrefix(issued.Token, "plp_") || len(issued.Token) < 30 {
		t.Fatalf("토큰 형식: %q", issued.Token)
	}
	// 원문은 저장되지 않고 목록에도 없다.
	var stored int
	_ = e.d.DB.QueryRow(`SELECT COUNT(*) FROM personal_tokens WHERE token_hash = ? OR token_prefix = ? OR name = ?`, issued.Token, issued.Token, issued.Token).Scan(&stored)
	if stored != 0 {
		t.Fatal("토큰 원문이 저장됨")
	}
	if _, list := e.call(c, "GET", "/api/tokens", "", nil); strings.Contains(list, issued.Token) {
		t.Fatal("목록에 토큰 원문이 노출됨")
	}
	// 감사 로그에도 원문이 없다.
	var leaked int
	_ = e.d.DB.QueryRow(`SELECT COUNT(*) FROM audit_logs WHERE detail LIKE ? OR diff LIKE ?`, "%"+issued.Token+"%", "%"+issued.Token+"%").Scan(&leaked)
	if leaked != 0 || e.auditCount("token.issue", "success") != 1 {
		t.Fatal("토큰 발급 감사 기록이 없거나 원문이 섞임")
	}

	// Bearer 로 인증되고(쿠키 없음), 쓰기에는 CSRF 가 필요 없다. 역할은 토큰 소유자의 것이다.
	bearer := map[string]string{"Authorization": "Bearer " + issued.Token}
	if code, body := e.call(http.DefaultClient, "GET", "/api/me", "", bearer); code != 200 || !strings.Contains(body, `"role":"staff"`) {
		t.Fatalf("Bearer /api/me: %d %s", code, body)
	}
	if code, _ := e.call(http.DefaultClient, "GET", "/api/audit-logs", "", bearer); code != http.StatusForbidden {
		t.Fatalf("staff 토큰으로 감사 로그: %d", code)
	}
	// Authenticator(MCP 가 쓰는 계약)도 같은 토큰을 받아들인다.
	p, err := e.d.Authenticator.AuthenticateToken(context.Background(), issued.Token)
	if err != nil || p.Role != contract.RoleStaff || p.Channel != contract.ChannelMCP {
		t.Fatalf("AuthenticateToken: %+v %v", p, err)
	}

	// 다른 사용자는 폐기할 수 없다.
	other := e.browser()
	e.login(other, "lead@pl.test")
	if code, _ := e.call(other, "DELETE", "/api/tokens/"+itoa(issued.Info.ID), "", map[string]string{"X-CSRF-Token": e.csrf(other)}); code != http.StatusNotFound {
		t.Fatalf("남의 토큰 폐기: %d", code)
	}
	// 폐기
	if code, _ := e.call(c, "DELETE", "/api/tokens/"+itoa(issued.Info.ID), "", h); code != http.StatusNoContent {
		t.Fatalf("폐기: %d", code)
	}
	if code, _ := e.call(http.DefaultClient, "GET", "/api/me", "", bearer); code != http.StatusUnauthorized {
		t.Fatalf("폐기된 토큰이 통과: %d", code)
	}
	if _, err := e.d.Authenticator.AuthenticateToken(context.Background(), issued.Token); err == nil {
		t.Fatal("폐기된 토큰이 Authenticator 를 통과")
	}
	if e.auditCount("token.revoke", "success") != 1 {
		t.Fatal("폐기 감사 기록 없음")
	}

	// 만료
	_, body = e.call(c, "POST", "/api/tokens", `{"name":"짧은","ttlDays":1}`, h)
	_ = json.Unmarshal([]byte(body), &issued)
	if _, err := e.d.DB.Exec(`UPDATE personal_tokens SET expires_at = '2000-01-01T00:00:00Z'`); err != nil {
		t.Fatal(err)
	}
	if code, _ := e.call(http.DefaultClient, "GET", "/api/me", "", map[string]string{"Authorization": "Bearer " + issued.Token}); code != http.StatusUnauthorized {
		t.Fatalf("만료 토큰: %d", code)
	}
	// 만료는 필수이고 범위가 있다.
	if code, _ := e.call(c, "POST", "/api/tokens", `{"name":"x","ttlDays":9999}`, h); code != http.StatusBadRequest {
		t.Fatalf("과도한 유효기간: %d", code)
	}
	// 비활성 사용자의 토큰은 거부된다.
	_, body = e.call(c, "POST", "/api/tokens", `{"name":"y","ttlDays":5}`, h)
	_ = json.Unmarshal([]byte(body), &issued)
	_, _ = e.d.DB.Exec(`UPDATE users SET active = 0 WHERE email = 'staff@pl.test'`)
	if code, _ := e.call(http.DefaultClient, "GET", "/api/me", "", map[string]string{"Authorization": "Bearer " + issued.Token}); code != http.StatusUnauthorized {
		t.Fatalf("비활성 사용자 토큰: %d", code)
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

// ---- 사용자 관리 + 체인 변조 검출 (HTTP 경유) ----

func TestUserUpdateAuditedAndChainTamperDetectedOverHTTP(t *testing.T) {
	e := newPlatformEnv(t, true)
	admin, auditor := e.browser(), e.browser()
	e.login(admin, "admin@pl.test")
	e.login(auditor, "auditor@pl.test")

	var staffID int64
	_ = e.d.DB.QueryRow(`SELECT id FROM users WHERE email='staff@pl.test'`).Scan(&staffID)
	ah := map[string]string{"X-CSRF-Token": e.csrf(admin)}
	if code, body := e.call(admin, "PATCH", "/api/admin/users/"+itoa(staffID), `{"role":"nope"}`, ah); code != http.StatusBadRequest {
		t.Fatalf("알 수 없는 역할: %d %s", code, body)
	}
	if code, body := e.call(admin, "PATCH", "/api/admin/users/"+itoa(staffID), `{"role":"manager"}`, ah); code != 200 || !strings.Contains(body, `"role":"manager"`) {
		t.Fatalf("역할 변경: %d %s", code, body)
	}
	var diff string
	_ = e.d.DB.QueryRow(`SELECT diff FROM audit_logs WHERE action='role.template_set'`).Scan(&diff)
	if !strings.Contains(diff, `"old":"staff"`) || !strings.Contains(diff, `"new":"manager"`) {
		t.Fatalf("변경 전후 diff: %s", diff)
	}
	var adminID int64
	_ = e.d.DB.QueryRow(`SELECT id FROM users WHERE email='admin@pl.test'`).Scan(&adminID)
	if code, _ := e.call(admin, "PATCH", "/api/admin/users/"+itoa(adminID), `{"active":false}`, ah); code != http.StatusBadRequest {
		t.Fatalf("본인 비활성화: %d", code)
	}

	// 한 행을 바꾸면 화면이 쓰는 검증 API 가 그 지점을 짚는다.
	dbtest.DropTrigger(t, e.d.DB, "trg_audit_no_update")
	if _, err := e.d.DB.Exec(`UPDATE audit_logs SET result = 'denied' WHERE seq = 2`); err != nil {
		t.Fatal(err)
	}
	code, body := e.call(auditor, "POST", "/api/audit-logs/verify", "", map[string]string{"X-CSRF-Token": e.csrf(auditor)})
	if code != 200 || !strings.Contains(body, `"ok":false`) || !strings.Contains(body, `"brokenSeq":2`) {
		t.Fatalf("변조 검출: %d %s", code, body)
	}
	if e.auditCount("audit_log.verify", "error") != 1 {
		t.Fatal("검증 실패가 감사 로그에 error 로 남아야 함")
	}
}

// 개인 토큰으로는 토큰을 발급·폐기할 수 없다(유출된 단기 토큰이 장기 토큰을 찍어 내는 경로 차단).
func TestTokenCannotMintOrRevokeTokens(t *testing.T) {
	e := newPlatformEnv(t, true)
	c := e.browser()
	e.login(c, "staff@pl.test")
	h := map[string]string{"X-CSRF-Token": e.csrf(c)}
	_, body := e.call(c, "POST", "/api/tokens", `{"name":"짧은","ttlDays":1}`, h)
	var short struct {
		Token string `json:"token"`
		Info  struct {
			ID int64 `json:"id"`
		} `json:"info"`
	}
	_ = json.Unmarshal([]byte(body), &short)
	bearer := map[string]string{"Authorization": "Bearer " + short.Token}

	if code, _ := e.call(http.DefaultClient, "POST", "/api/tokens", `{"name":"minted","ttlDays":365}`, bearer); code != http.StatusForbidden {
		t.Fatalf("Bearer 로 토큰 발급: %d", code)
	}
	if code, _ := e.call(http.DefaultClient, "DELETE", "/api/tokens/"+itoa(short.Info.ID), "", bearer); code != http.StatusForbidden {
		t.Fatalf("Bearer 로 토큰 폐기: %d", code)
	}
	var n int
	_ = e.d.DB.QueryRow(`SELECT COUNT(*) FROM personal_tokens WHERE name = 'minted'`).Scan(&n)
	if n != 0 {
		t.Fatal("Bearer 요청으로 토큰이 만들어졌음")
	}
	if got := e.auditCount("access.denied", "denied"); got != 2 {
		t.Fatalf("거부 2건이 감사 로그에 남아야 함: %d", got)
	}
	// 읽기(목록)는 허용된다.
	if code, _ := e.call(http.DefaultClient, "GET", "/api/tokens", "", bearer); code != 200 {
		t.Fatalf("Bearer 목록 조회: %d", code)
	}
	// 세션으로는 그대로 발급·폐기된다.
	if code, _ := e.call(c, "POST", "/api/tokens", `{"name":"세션발급","ttlDays":30}`, h); code != http.StatusCreated {
		t.Fatalf("세션 발급: %d", code)
	}
	if code, _ := e.call(c, "DELETE", "/api/tokens/"+itoa(short.Info.ID), "", h); code != http.StatusNoContent {
		t.Fatalf("세션 폐기: %d", code)
	}
}

// dev IdP 는 로컬 주소에서만 켤 수 있다. 외부에서 닿는 BaseURL 이면 기동(조립)이 실패한다.
func TestDevIdPRefusedOnNonLocalBaseURL(t *testing.T) {
	db := dbtest.Open(t)
	if err := migrations.Apply(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	for _, base := range []string{"https://erp.example.com", "http://10.0.0.5:8080", "http://erp.internal", "http://localhost.evil.com"} {
		d := deps.Deps{DB: db}
		if err := bootstrap.WirePlatform(config.Config{DevIDP: true, BaseURL: base}, &d); err == nil {
			t.Errorf("%s 에서 dev IdP 가 허용됨", base)
		}
	}
	for _, base := range []string{"http://localhost:8080", "http://127.0.0.1:9000", "http://[::1]:8080", "http://app.localhost:3000"} {
		d := deps.Deps{DB: db}
		if err := bootstrap.WirePlatform(config.Config{DevIDP: true, BaseURL: base}, &d); err != nil {
			t.Errorf("%s 에서 dev IdP 가 거부됨: %v", base, err)
		}
	}
	// 꺼져 있으면 BaseURL 이 무엇이든 상관없다(운영 기본).
	d := deps.Deps{DB: db}
	if err := bootstrap.WirePlatform(config.Config{BaseURL: "https://erp.example.com"}, &d); err != nil {
		t.Errorf("dev IdP 꺼짐 + 운영 주소: %v", err)
	}
}
