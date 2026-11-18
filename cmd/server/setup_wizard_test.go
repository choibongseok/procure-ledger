package main

// B38: 설치 마법사를 실제 서버 조립(WirePlatform → setup.New → buildRoutes → 인증 미들웨어)으로 태운다.

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/auth"
	"github.com/choibongseok/procure-ledger/internal/bootstrap"
	"github.com/choibongseok/procure-ledger/internal/config"
	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/deps"
	"github.com/choibongseok/procure-ledger/internal/dbtest"
	"github.com/choibongseok/procure-ledger/internal/setup"
	"github.com/choibongseok/procure-ledger/migrations"
)

type wizardEnv struct {
	h    http.Handler
	rt   *contract.RouteTable
	d    deps.Deps
	logs *bytes.Buffer
	code string
}

func newWizardEnv(t *testing.T, envCode string) *wizardEnv {
	t.Helper()
	db := dbtest.Open(t)
	if err := migrations.Apply(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{BaseURL: "http://localhost:8080", SetupCode: envCode}
	d := deps.Deps{DB: db}
	if err := bootstrap.WirePlatform(cfg, &d); err != nil {
		t.Fatal(err)
	}
	logs := &bytes.Buffer{}
	prev := log.Writer()
	log.SetOutput(logs)
	t.Cleanup(func() { log.SetOutput(prev) })
	// cmd/server 의 run() 과 같은 순서: 설치 상태 확정 → 서비스(코드 출력) → 라우트.
	if _, err := setup.Reconcile(context.Background(), d.DB, nil); err != nil {
		t.Fatal(err)
	}
	ss, err := setup.New(context.Background(), setup.Options{DB: d.DB, Txs: d.Txs, Audit: d.Audit, Auth: d.Authenticator.(*auth.Service), Code: cfg.SetupCode})
	if err != nil {
		t.Fatal(err)
	}
	d.Setup = ss
	rt := buildRoutes(cfg, d)
	e := &wizardEnv{h: handlerFor(rt, d), rt: rt, d: d, logs: logs}
	for _, line := range strings.Split(logs.String(), "\n") {
		if i := strings.Index(line, "설치 코드를 입력하세요: "); i >= 0 {
			e.code = strings.TrimSpace(line[i+len("설치 코드를 입력하세요: "):])
		}
	}
	return e
}

func (e *wizardEnv) req(method, path, code string, body any) *httptest.ResponseRecorder {
	var r *http.Request
	if body != nil {
		b, _ := json.Marshal(body)
		r = httptest.NewRequest(method, path, bytes.NewReader(b))
		r.Header.Set("Content-Type", "application/json")
	} else {
		r = httptest.NewRequest(method, path, nil)
	}
	if code != "" {
		r.Header.Set(setup.CodeHeader, code)
	}
	w := httptest.NewRecorder()
	e.h.ServeHTTP(w, r)
	return w
}

// 마법사 라우트는 모두 선언이 있고(공개 + 설치 코드), 선언 검사(CheckRoutes)를 통과한다.
func TestSetupRoutesDeclared(t *testing.T) {
	e := newWizardEnv(t, "")
	found := map[string]contract.Permission{}
	for _, r := range e.rt.Routes() {
		if strings.HasPrefix(r.Pattern, "/api/setup/") || r.Pattern == "/setup" {
			found[r.Method+" "+r.Pattern] = r.Permission
		}
	}
	for _, want := range []string{"GET /api/setup/status", "POST /api/setup/check", "POST /api/setup/validate", "POST /api/setup/complete", "GET /setup"} {
		if p, ok := found[want]; !ok || p != contract.PermPublic {
			t.Errorf("%s 선언: %q(공개여야 함, 보호는 설치 코드)", want, p)
		}
	}
	if problems := contract.CheckRoutes(e.rt.Routes()); len(problems) != 0 {
		t.Fatalf("라우트 선언 검사: %v", problems)
	}
}

// AC-1800·1801·1803·1807·1870: 빈 DB 첫 기동 → 코드 없는 원격 클라이언트는 아무것도 못 한다 → 코드로 완료(세션 없음) → 로그인 → 마법사 경로 404.
func TestSetupWizardFirstBootFlow(t *testing.T) {
	e := newWizardEnv(t, "")
	if e.code == "" {
		t.Fatalf("서버 로그에 설치 코드가 없음: %q", e.logs.String())
	}
	cfgOf := func() map[string]any {
		w := e.req("GET", "/api/auth/config", "", nil)
		var m map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &m)
		return m
	}
	if cfgOf()["setupPending"] != true {
		t.Fatal("pending 인데 setupPending 이 true 가 아님")
	}
	if w := e.req("GET", "/setup", "", nil); w.Code == 404 {
		t.Fatalf("pending 의 /setup 이 404")
	}
	body := map[string]any{"companyName": "에이씨엠이", "adminName": "오너", "adminEmail": "owner@acme.test", "adminPassword": "Wizard-Pass-2026!"}
	// 원격 첫 접속(E1·AC-1807): 코드 없이는 어떤 단계도 못 하고 사용자가 만들어지지 않는다.
	for _, p := range []string{"/api/setup/check", "/api/setup/validate", "/api/setup/complete"} {
		if w := e.req("POST", p, "", body); w.Code != 401 {
			t.Errorf("%s 코드 없음: %d", p, w.Code)
		}
	}
	var n int
	_ = e.d.DB.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&n)
	if n != 0 {
		t.Fatalf("코드 없는 요청이 사용자를 만듦: %d", n)
	}
	w := e.req("POST", "/api/setup/complete", e.code, body)
	if w.Code != 201 || w.Header().Get("Set-Cookie") != "" {
		t.Fatalf("완료: %d cookie=%q %s", w.Code, w.Header().Get("Set-Cookie"), w.Body.String())
	}
	// 완료 직후: 마법사는 닫히고 로그인 화면이 setupPending=false 를 받는다.
	if cfgOf()["setupPending"] != false {
		t.Error("완료 뒤 setupPending 이 false 가 아님")
	}
	for _, c := range []struct{ m, p string }{{"GET", "/api/setup/status"}, {"GET", "/setup"}, {"POST", "/api/setup/complete"}} {
		if w := e.req(c.m, c.p, e.code, body); w.Code != 404 {
			t.Errorf("%s %s: %d(404)", c.m, c.p, w.Code)
		}
	}
	// 방금 정한 이메일·비밀번호로 로그인된다(세션은 여기서 생긴다). 응답의 Set-Cookie 는 HttpOnly 세션 쿠키다.
	lb, _ := json.Marshal(map[string]string{"email": "owner@acme.test", "password": "Wizard-Pass-2026!"})
	lr := httptest.NewRequest("POST", "/api/auth/login/password", bytes.NewReader(lb))
	lr.Header.Set("Content-Type", "application/json")
	lr.Header.Set("Origin", "http://localhost:8080") // 로그인 POST 는 출처 검사를 한다
	lw := httptest.NewRecorder()
	e.h.ServeHTTP(lw, lr)
	if lw.Code != 200 && lw.Code != 204 {
		t.Fatalf("로그인: %d %s", lw.Code, lw.Body.String())
	}
	if c := lw.Result().Cookies(); len(c) == 0 || !c[0].HttpOnly {
		var out map[string]any
		_ = json.Unmarshal(lw.Body.Bytes(), &out)
		if out["totp"] != true { // 2단계 인증이 필요한 구성이면 코드 단계가 남는다
			t.Fatalf("세션 쿠키도 2단계 안내도 없음: %v %v", c, out)
		}
	}
	// 같은 코드·같은 설치 요청으로 두 번째 관리자를 만들 수 없다.
	_ = e.d.DB.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&n)
	if n != 1 {
		t.Errorf("사용자 %d명", n)
	}
	if strings.Contains(e.logs.String(), "Wizard-Pass-2026!") {
		t.Error("로그에 비밀번호가 있음")
	}
}

// AC-1870: 사용자가 이미 있는 DB 는 마법사 없이 그대로 동작한다(코드를 출력하지 않고 경로가 모두 404).
func TestSetupClosedForExistingInstall(t *testing.T) {
	db := dbtest.Open(t)
	if err := migrations.Apply(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	d := deps.Deps{DB: db}
	cfg := config.Config{BaseURL: "http://localhost:8080", DevIDP: true} // dev IdP 가 시드 사용자를 만든다
	if err := bootstrap.WirePlatform(cfg, &d); err != nil {
		t.Fatal(err)
	}
	logs := &bytes.Buffer{}
	prev := log.Writer()
	log.SetOutput(logs)
	t.Cleanup(func() { log.SetOutput(prev) })
	if did, err := setup.Reconcile(context.Background(), d.DB, nil); err != nil || !did {
		t.Fatalf("시드가 사용자를 만든 pending 은 done 으로 확정돼야 함: %v %v", did, err)
	}
	ss, err := setup.New(context.Background(), setup.Options{DB: d.DB, Txs: d.Txs, Audit: d.Audit, Auth: d.Authenticator.(*auth.Service)})
	if err != nil {
		t.Fatal(err)
	}
	d.Setup = ss
	if strings.Contains(logs.String(), "설치 코드") {
		t.Errorf("done 인데 설치 코드 안내가 나옴: %q", logs.String())
	}
	h := handlerFor(buildRoutes(cfg, d), d)
	for _, c := range []struct{ m, p string }{{"GET", "/api/setup/status"}, {"POST", "/api/setup/check"}, {"POST", "/api/setup/complete"}} {
		r := httptest.NewRequest(c.m, c.p, strings.NewReader("{}"))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 404 {
			t.Errorf("%s %s: %d(404)", c.m, c.p, w.Code)
		}
	}
}
