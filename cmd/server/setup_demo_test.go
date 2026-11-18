package main

// B38 4.6·D5: 설치 마법사의 데모 데이터. 실제 서버 조립(bootstrap.Build)에 마법사를 올리고, 데모 시드는 main 과 같이 조립된 deps 로 부른다.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/auth"
	"github.com/choibongseok/procure-ledger/internal/bootstrap"
	"github.com/choibongseok/procure-ledger/internal/config"
	"github.com/choibongseok/procure-ledger/internal/contract/deps"
	"github.com/choibongseok/procure-ledger/internal/dbtest"
	"github.com/choibongseok/procure-ledger/internal/demo"
	"github.com/choibongseok/procure-ledger/internal/setup"
)

func newWizardEnvFull(t *testing.T, seed func(context.Context, deps.Deps) error) *wizardEnv {
	t.Helper()
	dbtest.SetEnv(t)
	cfg := config.Load()
	cfg.BaseURL, cfg.SetupCode = "http://localhost:8080", "demo-flow-setup-code-0123456789"
	d, closeFn, err := bootstrap.Build(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(closeFn)
	logs := &bytes.Buffer{}
	prev := log.Writer()
	log.SetOutput(logs)
	t.Cleanup(func() { log.SetOutput(prev) })
	ss, err := setup.New(context.Background(), setup.Options{DB: d.DB, Txs: d.Txs, Audit: d.Audit, Auth: d.Authenticator.(*auth.Service), Code: cfg.SetupCode,
		Demo: func(ctx context.Context, _ int64) error { return seed(ctx, d) }})
	if err != nil {
		t.Fatal(err)
	}
	d.Setup = ss
	rt := buildRoutes(cfg, d)
	return &wizardEnv{h: handlerFor(rt, d), rt: rt, d: d, logs: logs, code: cfg.SetupCode}
}

func (e *wizardEnv) login(email, pw string) int {
	b, _ := json.Marshal(map[string]string{"email": email, "password": pw})
	r := httptest.NewRequest("POST", "/api/auth/login/password", bytes.NewReader(b))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", "http://localhost:8080")
	w := httptest.NewRecorder()
	e.h.ServeHTTP(w, r)
	return w.Code
}

func (e *wizardEnv) authConfig() map[string]any {
	w := e.req(http.MethodGet, "/api/auth/config", "", nil)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return out
}

func demoBody() map[string]any {
	return map[string]any{"companyName": "에이씨엠이", "adminName": "오너", "adminEmail": "owner@acme.test", "adminPassword": "Wizard-Pass-2026!", "demo": true, "demoConfirm": "에이씨엠이"}
}

// 데모를 고른 설치: 설치 완료가 먼저 커밋된 뒤 데모 세트가 들어가고(교착 없음), demo_mode 표시가 켜지며, 데모 사용자는 로그인할 수 없다(AC-1808).
func TestSetupWithDemoSeedsAfterCommitAndDemoUsersCannotLogIn(t *testing.T) {
	var sawDone bool
	e := newWizardEnvFull(t, func(ctx context.Context, d deps.Deps) error {
		st, _ := setup.Status(ctx, d.DB)
		sawDone = st == setup.StatusDone // 완료 커밋 뒤에 불린다
		var adminID int64
		_ = d.DB.QueryRow(`SELECT id FROM users WHERE email = 'owner@acme.test'`).Scan(&adminID)
		return demo.SeedForInstall(ctx, d, adminID) // main 의 훅과 같다: 데모 세트 + 첫 관리자 배치
	})
	w := e.req("POST", "/api/setup/complete", e.code, demoBody())
	if w.Code != 201 || strings.Contains(w.Body.String(), "demoFailed") {
		t.Fatalf("완료: %d %s\n로그: %s", w.Code, w.Body.String(), e.logs.String())
	}
	if !sawDone {
		t.Error("데모 시드가 설치 완료 커밋 전에 불렸다")
	}
	var users, demoUsers, creds int
	_ = e.d.DB.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&users)
	_ = e.d.DB.QueryRow(`SELECT COUNT(*) FROM users WHERE email LIKE '%@demo.invalid'`).Scan(&demoUsers)
	_ = e.d.DB.QueryRow(`SELECT COUNT(*) FROM user_credentials`).Scan(&creds)
	if demoUsers == 0 || users != demoUsers+1 {
		t.Fatalf("사용자 %d명, 데모 사용자 %d명(첫 관리자 1 + 데모여야 함)", users, demoUsers)
	}
	if creds != 1 {
		t.Errorf("로그인 자격 %d건(첫 관리자 1건만 있어야 함)", creds)
	}
	for _, pw := range []string{"Wizard-Pass-2026!", "password123", ""} {
		if code := e.login("purchase.lead@demo.invalid", pw); code == 200 || code == 204 {
			t.Errorf("데모 사용자가 로그인함: %d", code)
		}
	}
	if code := e.login("owner@acme.test", "Wizard-Pass-2026!"); code != 200 && code != 204 {
		t.Errorf("첫 관리자 로그인: %d", code)
	}
	if cfg := e.authConfig(); cfg["demoMode"] != true || cfg["setupPending"] != false {
		t.Errorf("auth config: %v", cfg)
	}
	// 첫 관리자는 데모 조직(경영지원팀)에 발령으로 배치된다.
	var dept string
	_ = e.d.DB.QueryRow(`SELECT d.name FROM users u JOIN departments d ON d.id = u.department_id WHERE u.email = 'owner@acme.test'`).Scan(&dept)
	var hires int
	_ = e.d.DB.QueryRow(`SELECT COUNT(*) FROM org_events e JOIN users u ON u.id = e.subject_id WHERE e.kind = 'hire' AND u.email = 'owner@acme.test'`).Scan(&hires)
	if dept != "경영지원팀" || hires != 1 {
		t.Errorf("첫 관리자 배치: 부서 %q, 입사 발령 %d건", dept, hires)
	}
	if strings.Contains(e.logs.String(), "배치하지 못했습니다") {
		t.Errorf("배치 실패 로그: %s", e.logs.String())
	}
	if res, err := e.d.AuditReader.Verify(context.Background()); err != nil || !res.ChainOK {
		t.Fatalf("감사 체인: %+v %v", res, err)
	}
}

// 데모 세트가 실패해도 설치는 완료로 남고(되돌리지 않음) 응답이 알리되 내부 원인은 싣지 않는다.
func TestSetupDemoFailureKeepsInstallation(t *testing.T) {
	called := 0
	e := newWizardEnvFull(t, func(context.Context, deps.Deps) error { called++; return errors.New("샘플 입고 실패") })
	w := e.req("POST", "/api/setup/complete", e.code, demoBody())
	if w.Code != 201 || !strings.Contains(w.Body.String(), `"demoFailed":true`) {
		t.Fatalf("데모 실패 응답: %d %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "샘플 입고 실패") {
		t.Error("응답에 내부 원인이 노출됨")
	}
	if !strings.Contains(e.logs.String(), "샘플 입고 실패") {
		t.Error("서버 로그에 원인이 없음")
	}
	if st, _ := setup.Status(context.Background(), e.d.DB); st != setup.StatusDone {
		t.Errorf("설치 상태 %q(완료로 남아야 함)", st)
	}
	if code := e.login("owner@acme.test", "Wizard-Pass-2026!"); code != 200 && code != 204 {
		t.Errorf("첫 관리자 로그인: %d", code)
	}
	if w := e.req("POST", "/api/setup/complete", e.code, demoBody()); w.Code != 404 {
		t.Errorf("재진입: %d", w.Code)
	}
	if called != 1 {
		t.Errorf("시드 호출 %d회", called)
	}
}

// 데모를 고르지 않으면 시드는 불리지 않고 demoMode 는 false 다.
func TestSetupWithoutDemoNeverSeeds(t *testing.T) {
	called := 0
	e := newWizardEnvFull(t, func(context.Context, deps.Deps) error { called++; return nil })
	body := map[string]any{"companyName": "에이씨엠이", "adminName": "오너", "adminEmail": "owner@acme.test", "adminPassword": "Wizard-Pass-2026!"}
	if w := e.req("POST", "/api/setup/complete", e.code, body); w.Code != 201 {
		t.Fatalf("완료: %d", w.Code)
	}
	var n int
	_ = e.d.DB.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&n)
	if called != 0 || n != 1 {
		t.Errorf("시드 호출 %d회, 사용자 %d명", called, n)
	}
	if cfg := e.authConfig(); cfg["demoMode"] != false {
		t.Errorf("demoMode: %v", cfg["demoMode"])
	}
}
