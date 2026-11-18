package main

// PL_DEV_SEED_PASSWORD: dev IdP 를 끈 채(비밀번호 없는 로그인 없음) 테일넷 https 주소로 미리보기를 여는 구성.
// tailscale serve 가 https 를 끝내고 localhost http 로 넘기므로, 서버는 http 로 받아도 Secure 쿠키·출처 검사가 https 주소 기준으로 동작해야 한다.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/bootstrap"
	"github.com/choibongseok/procure-ledger/internal/config"
	"github.com/choibongseok/procure-ledger/internal/contract/deps"
	"github.com/choibongseok/procure-ledger/internal/org"
)

const tailnetURL = "https://erp-demo.tailnet.example.ts.net"

func TestDevSeedPasswordServerAndLogin(t *testing.T) {
	const seedPw = "Dev-Seed-Pw-1!"
	var cfg config.Config
	e := newPlatformEnvWith(t, false, func(c *config.Config) {
		cheapArgon(c)
		c.BaseURL, c.CookieSecure, c.DevSeedPassword = tailnetURL, true, seedPw
		cfg = *c
	})
	ctx := context.Background()
	if did, err := e.d.Org.(*org.Service).SeedDev(ctx); err != nil || !did {
		t.Fatalf("개발 조직 시드: %v %v", did, err)
	}
	if err := seedDevPasswords(ctx, cfg, e.d); err != nil {
		t.Fatal(err)
	}
	if err := seedDevPasswords(ctx, cfg, e.d); err != nil { // 재기동: 멱등
		t.Fatal(err)
	}
	var users, creds int
	_ = e.d.DB.QueryRow(`SELECT COUNT(*) FROM users WHERE active = 1`).Scan(&users)
	_ = e.d.DB.QueryRow(`SELECT COUNT(*) FROM user_credentials WHERE must_change = 1`).Scan(&creds)
	if users < 60 || creds != users {
		t.Fatalf("시드 사용자 %d명 중 자격 %d", users, creds)
	}

	c := e.browser()
	if code, body := e.call(c, "GET", "/healthz", "", nil); code != 200 || !strings.Contains(body, "ok") {
		t.Fatalf("헬스체크: %d %s", code, body)
	}
	// dev IdP 는 꺼져 있다: 로그인 화면에 개발 계정이 없고 dev 라우트도 없다.
	code, body := e.call(c, "GET", "/api/auth/config", "", nil)
	if code != 200 || !strings.Contains(body, `"devIdp":false`) || !strings.Contains(body, `"emailLogin":true`) || strings.Contains(body, "devAccounts") {
		t.Fatalf("config: %d %s", code, body)
	}
	if code, _ := e.call(c, "GET", "/api/auth/login?login_hint=admin@pl.test", "", nil); code == 302 || code == 200 {
		t.Fatalf("dev IdP 로그인이 열려 있음: %d", code)
	}

	// 브라우저는 https 주소로 접속한다(Origin 이 https 주소). 서버는 프록시 뒤 http 로 받는다.
	hdr := func(extra map[string]string) map[string]string {
		h := jsonHdr(map[string]string{"Origin": tailnetURL})
		for k, v := range extra {
			h[k] = v
		}
		return h
	}
	req, _ := http.NewRequest("POST", e.base+"/api/auth/login/password", strings.NewReader(`{"email":"staff@pl.test","password":"`+seedPw+`"}`))
	for k, v := range hdr(nil) {
		req.Header.Set(k, v)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 || !strings.Contains(string(b), `"setup":"password_change"`) {
		t.Fatalf("이메일 로그인: %d %s", res.StatusCode, b)
	}
	var cookie *http.Cookie
	for _, ck := range res.Cookies() {
		if ck.Name == "pl_session" {
			cookie = ck
		}
	}
	if cookie == nil || !cookie.Secure || !cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode {
		t.Fatalf("https 기준 세션 쿠키(Secure·HttpOnly·Lax)여야 함: %+v", cookie)
	}
	ck := map[string]string{"Cookie": "pl_session=" + cookie.Value}
	_, body = e.call(c, "GET", "/api/me", "", ck)
	var me struct {
		CSRF  string `json:"csrfToken"`
		Setup string `json:"setup"`
	}
	_ = json.Unmarshal([]byte(body), &me)
	if me.Setup != "password_change" || me.CSRF == "" {
		t.Fatalf("제한 세션이어야 함: %s", body)
	}
	if code, _ := e.call(c, "GET", "/api/tokens", "", ck); code != 403 {
		t.Fatalf("변경 전 업무 API: %d", code)
	}
	change := `{"current":"` + seedPw + `","new":"My-Own-Pass-77"}`
	// 다른 출처의 상태 변경은 PL_BASE_URL 기준으로 거부된다.
	if code, _ := e.call(c, "POST", "/api/auth/password/change", change, hdr(map[string]string{"Origin": "https://evil.example", "X-CSRF-Token": me.CSRF, "Cookie": ck["Cookie"]})); code != 403 {
		t.Fatalf("다른 출처: %d", code)
	}
	if code, b := e.call(c, "POST", "/api/auth/password/change", change, hdr(map[string]string{"X-CSRF-Token": me.CSRF, "Cookie": ck["Cookie"]})); code != 204 {
		t.Fatalf("변경: %d %s", code, b)
	}
	_, body = e.call(c, "GET", "/api/me", "", ck)
	if !strings.Contains(body, `"setup":""`) {
		t.Fatalf("변경 뒤에도 제한 세션: %s", body)
	}
	if code, _ := e.call(c, "GET", "/api/tokens", "", ck); code != 200 {
		t.Fatalf("정상 세션의 업무 API: %d", code)
	}
	var leaked int
	_ = e.d.DB.QueryRow(`SELECT COUNT(*) FROM audit_logs WHERE diff LIKE '%' || ? || '%' OR detail LIKE '%' || ? || '%'`, seedPw, "My-Own-Pass-77").Scan(&leaked)
	if leaked != 0 {
		t.Fatal("비밀번호가 감사에 남음")
	}
}

// 정책에 맞지 않는 값이면 기동을 거부하고, 값 자체는 오류 문구에 넣지 않는다.
func TestDevSeedPasswordWeakRefusesBoot(t *testing.T) {
	cfg := config.Config{BaseURL: "http://localhost:8080", DevSeedPassword: "weakweak"}
	d := deps.Deps{}
	if err := bootstrap.WirePlatform(cfg, &d); err == nil || strings.Contains(err.Error(), "weakweak") {
		t.Fatalf("기동 거부(값 미노출) 기대: %v", err)
	}
}
