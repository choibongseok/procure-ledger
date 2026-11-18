package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/access"
	"github.com/choibongseok/procure-ledger/internal/apitoken"
	"github.com/choibongseok/procure-ledger/internal/bootstrap"
	"github.com/choibongseok/procure-ledger/internal/config"
	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/dbtest"
)

// B42 1단계: 조립된 서버에서 /api/v1 은 pli_ 토큰으로만 인증하고(쿠키·개인 토큰 불가), pli_ 토큰은 다른 경로에서 인정되지 않는다.
func TestAPIv1AuthIsolationAndScopes(t *testing.T) {
	dbtest.SetEnv(t)
	t.Setenv("PL_DEV_IDP", "1")
	t.Setenv("PL_LLM_PROVIDER", "mock")
	cfg := config.Load()
	ctx := context.Background()
	d, closeDB, err := bootstrap.Build(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer closeDB()
	rt := buildRoutes(cfg, d)
	h := handlerFor(rt, d)
	session := func(email string) contract.Principal {
		var p contract.Principal
		var role string
		if err := d.DB.QueryRowContext(ctx, `SELECT id, name, role, department_id FROM users WHERE email = ?`, email).Scan(&p.UserID, &p.Name, &role, &p.DepartmentID); err != nil {
			t.Fatal(err)
		}
		p.Role, p.Channel, p.Auth = contract.Role(role), contract.ChannelWeb, contract.AuthSession
		if p.Perms, err = access.PermsOf(ctx, d.DB, p.UserID); err != nil {
			t.Fatal(err)
		}
		return p
	}
	admin, finance, staff := session("admin@pl.test"), session("finance@pl.test"), session("staff@pl.test")
	if !admin.Can(contract.PermAPIManage) || finance.Can(contract.PermAPIManage) || staff.Can(contract.PermAPIManage) {
		t.Fatal("api:manage 는 관리자만 가진다")
	}
	get := func(path, token, cookieOrOther string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", path, nil)
		req.RemoteAddr = "127.0.0.1:5000"
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		if cookieOrOther != "" {
			req.Header.Set("Cookie", cookieOrOther)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w
	}
	// 관리자는 자기가 가진 범위만 발급할 수 있다(상한). 서비스에서 가능한 범위를 찾아 발급한다.
	scopes, err := d.APITokens.Scopes(ctx, admin)
	if err != nil {
		t.Fatal(err)
	}
	var held []string
	for _, s := range scopes {
		if s.Held && s.Name != "changes:read" {
			held = append(held, s.Name)
		}
	}
	if len(held) == 0 {
		t.Skip("관리자가 가진 읽기 범위가 없어 발급 시험을 건너뜀")
	}
	raw, info, err := d.APITokens.Issue(ctx, admin, apitoken.IssueInput{Name: "통합 시험", Scopes: append([]string{"changes:read"}, held...)})
	if err != nil {
		t.Fatal(err)
	}
	// API 가 꺼져 있으면 403(토큰이 맞아도).
	if w := get("/api/v1/changes/head", raw, ""); w.Code != http.StatusForbidden {
		t.Fatalf("꺼진 API: %d %s", w.Code, w.Body.String())
	}
	if err := d.APITokens.SetEnabled(ctx, admin, true); err != nil {
		t.Fatal(err)
	}
	w := get("/api/v1/changes/head", raw, "")
	if w.Code != http.StatusOK {
		t.Fatalf("토큰으로 변경 피드 위치: %d %s", w.Code, w.Body.String())
	}
	if w.Header().Get("X-Request-Id") == "" {
		t.Error("요청 번호 헤더가 없다")
	}
	// OpenAPI 문서는 인증 없이 나간다(스키마만).
	if w := get("/api/v1/openapi.json", "", ""); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "openapi") {
		t.Fatalf("openapi: %d", w.Code)
	}
	// 토큰이 없거나 틀리면 401 + WWW-Authenticate, 오류 틀은 JSON.
	for _, tok := range []string{"", "pli_nonexistent", "plp_whatever"} {
		w := get("/api/v1/items", tok, "")
		var body struct{ Error struct{ Code string } }
		if w.Code != http.StatusUnauthorized || w.Header().Get("WWW-Authenticate") != "Bearer" || json.Unmarshal(w.Body.Bytes(), &body) != nil || body.Error.Code != "unauthenticated" {
			t.Errorf("토큰 %q: %d %s", tok, w.Code, w.Body.String())
		}
	}
	// 세션 쿠키로는 /api/v1 을 부를 수 없다(로그인한 사용자의 쿠키가 있어도 토큰이 없으면 401).
	if w := get("/api/v1/items", "", "pl_session=anything"); w.Code != http.StatusUnauthorized {
		t.Errorf("쿠키: %d", w.Code)
	}
	// 개인 토큰(plp_)은 /api/v1 에서 인정하지 않는다.
	ptok, _, err := d.Tokens.Issue(ctx, finance, "개인", 0)
	if err != nil {
		t.Fatal(err)
	}
	if w := get("/api/v1/items", ptok, ""); w.Code != http.StatusUnauthorized {
		t.Errorf("개인 토큰으로 /api/v1: %d", w.Code)
	}
	// 반대로 pli_ 토큰은 다른 /api/* 경로에서 인정하지 않는다.
	for _, path := range []string{"/api/me", "/api/audit-logs", "/mcp"} {
		anon := get(path, "", "")
		if w := get(path, raw, ""); w.Code != anon.Code {
			t.Errorf("pli_ 토큰으로 %s: %d(토큰 없는 호출 %d 와 같아야 한다: 인정하면 안 된다)", path, w.Code, anon.Code)
		}
	}
	// 토큰 발급·폐기는 개인 토큰·API 토큰으로 못 한다(서비스 방어선): 토큰 주체는 로그인 세션이 아니다.
	apiPrincipal := admin
	apiPrincipal.Auth = contract.AuthAPI
	if _, _, err := d.APITokens.Issue(ctx, apiPrincipal, apitoken.IssueInput{Name: "x", Scopes: []string{"changes:read"}}); err == nil {
		t.Error("API 인증 주체로 토큰을 발급했다")
	}
	// 범위 밖: 가진 범위가 없는 자원은 403(forbidden_scope) 이고 거부 감사가 남는다.
	narrow, _, err := d.APITokens.Issue(ctx, admin, apitoken.IssueInput{Name: "좁은", Scopes: []string{"changes:read"}})
	if err != nil {
		t.Fatal(err)
	}
	w = get("/api/v1/items", narrow, "")
	var body struct{ Error struct{ Code string } }
	if w.Code != http.StatusForbidden || json.Unmarshal(w.Body.Bytes(), &body) != nil || body.Error.Code != "forbidden_scope" {
		t.Fatalf("범위 밖: %d %s", w.Code, w.Body.String())
	}
	var denied int
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM audit_logs WHERE action = 'api.denied' AND channel = 'api'`).Scan(&denied); err != nil || denied < 1 {
		t.Fatalf("범위 밖 거부 감사 %d %v", denied, err)
	}
	// 폐기하면 즉시 401, 감사의 주체 이름은 API(<토큰 이름>)이고 채널은 api.
	if err := d.APITokens.Revoke(ctx, admin, info.ID); err != nil {
		t.Fatal(err)
	}
	if w := get("/api/v1/changes/head", raw, ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("폐기 뒤: %d", w.Code)
	}
	d.APITokens.Flush(ctx, true)
	var n int
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM audit_logs WHERE action = 'api.read' AND channel = 'api' AND actor_id = ?`, admin.UserID).Scan(&n); err != nil || n < 1 {
		t.Fatalf("읽기 요약 감사 %d %v", n, err)
	}
}
