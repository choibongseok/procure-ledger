package main

// B40 1단계: SCIM 2.0 서버를 실제 라우트·세션·권한 표·발령 경로로 태운다(AC-4001~4007·4009·4011·4013·4015·4017·4019·4021).
// 모의 IdP 는 internal/scimtest 다.

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/config"
	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/org"
	"github.com/choibongseok/procure-ledger/internal/scimtest"
)

type scimEnv struct {
	*platformEnv
	admin *http.Client
	hdr   map[string]string
}

func newSCIMEnv(t *testing.T, tweak func(*config.Config)) *scimEnv {
	t.Helper()
	e := newPlatformEnvWith(t, true, tweak)
	if _, err := e.d.Org.(*org.Service).SeedDev(context.Background()); err != nil {
		t.Fatal(err)
	}
	s := &scimEnv{platformEnv: e, admin: e.browser()}
	e.login(s.admin, "admin@pl.test")
	s.hdr = map[string]string{"X-CSRF-Token": e.csrf(s.admin), "Content-Type": "application/json"}
	return s
}

func (s *scimEnv) enable() {
	s.t.Helper()
	code, body := s.call(s.admin, "PUT", "/api/admin/scim/settings", `{"enabled":true,"groupMode":"off","defaultUnit":"T-PU1","defaultRank":"사원","defaultRole":"staff","adoptExisting":true}`, s.hdr)
	if code != 200 {
		s.t.Fatalf("설정 저장: %d %s", code, body)
	}
}

// token 은 관리자 세션으로 연동 토큰을 발급하고 원문을 돌려준다.
func (s *scimEnv) token(source, scope string) string {
	s.t.Helper()
	code, body := s.call(s.admin, "POST", "/api/admin/scim/tokens", `{"name":"시험 `+source+`","source":"`+source+`","scope":"`+scope+`","days":30}`, s.hdr)
	if code != 201 {
		s.t.Fatalf("토큰 발급: %d %s", code, body)
	}
	var out struct{ Token string }
	if err := json.Unmarshal([]byte(body), &out); err != nil || !strings.HasPrefix(out.Token, "pls_") {
		s.t.Fatalf("토큰 응답: %v %s", err, body)
	}
	return out.Token
}

func (s *scimEnv) client(tok string) *scimtest.Client {
	return &scimtest.Client{T: s.t, Base: s.base, Token: tok}
}

func (s *scimEnv) count(q string, args ...any) int {
	s.t.Helper()
	var n int
	if err := s.d.DB.QueryRow(q, args...).Scan(&n); err != nil {
		s.t.Fatal(err)
	}
	return n
}

func TestSCIMRoutesDeclared(t *testing.T) {
	e := newPlatformEnv(t, true)
	got := map[string]contract.Permission{}
	for _, r := range e.rt.Routes() {
		got[r.Method+" "+r.Pattern] = r.Permission
	}
	for k, p := range got {
		if strings.Contains(k, " /scim/v2") && p != contract.PermSCIMProvision {
			t.Errorf("%s: scim:provision 이어야 함(%q)", k, p)
		}
	}
	for _, k := range []string{"GET /scim/v2/Users", "POST /scim/v2/Users", "PATCH /scim/v2/Users/{id}", "DELETE /scim/v2/Users/{id}", "GET /scim/v2/ServiceProviderConfig", "GET /scim/v2/Groups"} {
		if _, ok := got[k]; !ok {
			t.Errorf("라우트 %s 없음", k)
		}
	}
	for k, p := range map[string]contract.Permission{
		"GET /api/admin/scim": contract.PermSCIMRead, "GET /api/admin/scim/log": contract.PermSCIMRead,
		"PUT /api/admin/scim/settings": contract.PermSCIMManage, "POST /api/admin/scim/tokens": contract.PermSCIMManage, "DELETE /api/admin/scim/tokens/{id}": contract.PermSCIMManage,
	} {
		if got[k] != p {
			t.Errorf("%s: 선언 %q, 기대 %q", k, got[k], p)
		}
	}
	if problems := contract.CheckRoutes(e.rt.Routes()); len(problems) != 0 {
		t.Fatal(problems)
	}
	// 모든 SCIM 라우트는 토큰 없이 부르면 401 이다(요청 하나씩이라 인증 실패 IP 제한 20회 안이다).
	n := 0
	for _, r := range e.rt.Routes() {
		if !strings.HasPrefix(r.Pattern, "/scim/v2") {
			continue
		}
		n++
		path := strings.NewReplacer("{id}", "1").Replace(r.Pattern)
		if code, _ := e.call(http.DefaultClient, r.Method, path, "", nil); code != http.StatusUnauthorized {
			t.Errorf("%s %s 미인증이 %d", r.Method, r.Pattern, code)
		}
	}
	if n == 0 || n > 19 {
		t.Fatalf("SCIM 라우트 수 %d(이 시험은 인증 실패 제한 20회 안에서만 유효)", n)
	}
	// scim:provision 은 어느 역할 템플릿에도 없다(사람에게 줄 수 없는 내부 권한).
	for _, r := range contract.Roles {
		if contract.RoleHas(r, contract.PermSCIMProvision) {
			t.Errorf("역할 %s 가 scim:provision 을 가짐", r)
		}
	}
}

func TestSCIMAuthAndTokens(t *testing.T) {
	s := newSCIMEnv(t, func(c *config.Config) { c.SCIMMaxActiveTokens = 2 })
	anon := s.client("")
	if r := anon.Get("/ServiceProviderConfig"); r.Status != 401 || !strings.Contains(r.Header.Get("WWW-Authenticate"), "Bearer") {
		t.Fatalf("토큰 없음: %d %s", r.Status, r.Raw)
	}
	tok := s.token("entra", "users")
	c := s.client(tok)
	// 꺼져 있으면 토큰이 맞아도 403.
	if r := c.Get("/ServiceProviderConfig"); r.Status != 403 {
		t.Fatalf("꺼진 상태: %d %s", r.Status, r.Raw)
	}
	s.enable()
	if r := c.Get("/ServiceProviderConfig"); r.Status != 200 {
		t.Fatalf("켠 뒤: %d %s", r.Status, r.Raw)
	} else if p, _ := r.Body["patch"].(map[string]any); p["supported"] != true {
		t.Errorf("patch 지원 표시: %v", r.Body["patch"])
	}
	if r := s.client("pls_wrong").Get("/Users"); r.Status != 401 {
		t.Errorf("틀린 토큰: %d", r.Status)
	}
	// 쿠키 세션·개인 토큰은 SCIM 에서 인정하지 않는다.
	if code, _ := s.call(s.admin, "GET", "/scim/v2/Users", "", nil); code != 401 {
		t.Errorf("세션 쿠키로 SCIM: %d", code)
	}
	// SCIM 토큰은 /api 에서 인정하지 않는다.
	if code, _ := s.call(s.browser(), "GET", "/api/me", "", map[string]string{"Authorization": "Bearer " + tok}); code != 401 {
		t.Errorf("SCIM 토큰으로 /api: %d", code)
	}
	// 발급은 세션 전용: 감사자는 403, Bearer 도 403.
	aud := s.browser()
	s.login(aud, "auditor@pl.test")
	if code, _ := s.call(aud, "POST", "/api/admin/scim/tokens", `{"name":"x","source":"okta"}`, map[string]string{"X-CSRF-Token": s.csrf(aud), "Content-Type": "application/json"}); code != 403 {
		t.Errorf("감사자의 발급: %d", code)
	}
	if code, _ := s.call(s.browser(), "POST", "/api/admin/scim/tokens", `{"name":"x","source":"okta"}`, map[string]string{"Authorization": "Bearer " + tok, "Content-Type": "application/json"}); code == 200 || code == 201 {
		t.Errorf("Bearer 발급: %d", code)
	}
	// 감사자는 조회만 된다.
	if code, body := s.call(aud, "GET", "/api/admin/scim", "", nil); code != 200 || strings.Contains(body, tok) {
		t.Errorf("감사자 조회: %d", code)
	}
	// 원문은 DB 어디에도 없다.
	if n := s.count(`SELECT COUNT(*) FROM scim_tokens WHERE token_hash = ? OR token_prefix = ?`, tok, tok); n != 0 {
		t.Error("원문이 DB 에 저장됨")
	}
	if n := s.count(`SELECT COUNT(*) FROM audit_logs WHERE detail LIKE ? OR diff LIKE ?`, "%"+tok+"%", "%"+tok+"%"); n != 0 {
		t.Error("원문이 감사에 남음")
	}
	// 목록에는 원문이 없고, 폐기하면 즉시 401.
	code, body := s.call(s.admin, "GET", "/api/admin/scim", "", nil)
	if code != 200 || strings.Contains(body, tok) {
		t.Fatalf("개요: %d", code)
	}
	var ov struct {
		Tokens []struct{ ID int64 } `json:"tokens"`
	}
	_ = json.Unmarshal([]byte(body), &ov)
	if len(ov.Tokens) == 0 {
		t.Fatal("토큰 목록이 비어 있음")
	}
	if code, b := s.call(s.admin, "DELETE", "/api/admin/scim/tokens/"+itoa(ov.Tokens[0].ID), "", s.hdr); code != 204 {
		t.Fatalf("폐기: %d %s", code, b)
	}
	if r := c.Get("/Users"); r.Status != 401 {
		t.Errorf("폐기 뒤: %d", r.Status)
	}
	if s.auditCount("scim.token_issue", "success") < 1 || s.auditCount("scim.token_revoke", "success") != 1 {
		t.Error("발급·폐기 감사가 없음")
	}
	// 같은 출처의 유효 토큰은 2개까지(교체용).
	s.token("okta", "users")
	s.token("okta", "users")
	if code, _ := s.call(s.admin, "POST", "/api/admin/scim/tokens", `{"name":"셋째","source":"okta","days":30}`, s.hdr); code != 409 {
		t.Errorf("상한 초과 발급: %d", code)
	}
}

func TestSCIMRiskyDefaultRoleRejected(t *testing.T) {
	s := newSCIMEnv(t, nil)
	if code, body := s.call(s.admin, "PUT", "/api/admin/scim/settings", `{"enabled":true,"defaultRole":"admin"}`, s.hdr); code != 400 {
		t.Errorf("위험 역할을 기본 역할로 저장: %d %s", code, body)
	}
	if code, _ := s.call(s.admin, "PUT", "/api/admin/scim/settings", `{"enabled":true,"groupMode":"bogus"}`, s.hdr); code != 400 {
		t.Errorf("알 수 없는 그룹 방식 저장: %d", code)
	}
}

func TestSCIMCreateIdempotentAndHire(t *testing.T) {
	s := newSCIMEnv(t, nil)
	s.enable()
	c := s.client(s.token("entra", "users"))
	r := c.EntraSync("ext-1", "new.hire@corp.test", "신입 사원", "T-PU1", "사원", true)
	if r.Status != 201 || r.ID() == "" || !r.Active() || r.Header.Get("Location") == "" {
		t.Fatalf("생성: %d %s", r.Status, r.Raw)
	}
	id := r.ID()
	if s.count(`SELECT COUNT(*) FROM org_events WHERE subject_type='person' AND kind='hire' AND subject_id = ?`, id) != 1 {
		t.Error("입사 발령 이벤트가 1건이 아님")
	}
	if s.count(`SELECT COUNT(*) FROM audit_logs WHERE action='hr.hire' AND channel='scim' AND target_id = ?`, id) != 1 {
		t.Error("hr.hire 감사(채널 scim)가 없음")
	}
	var role string
	_ = s.d.DB.QueryRow(`SELECT role FROM users WHERE id = ?`, id).Scan(&role)
	if role != "staff" {
		t.Errorf("기본 역할 %q", role)
	}
	// 같은 POST 를 다시 보내도 사용자·발령은 1건(멱등).
	r2 := c.Post("/Users", scimtest.User("new.hire@corp.test", "ext-1", "신입 사원", "T-PU1", "사원"))
	if r2.Status != 200 || r2.ID() != id {
		t.Fatalf("멱등 재전송: %d %s", r2.Status, r2.Raw)
	}
	if s.count(`SELECT COUNT(*) FROM org_events WHERE subject_type='person' AND kind='hire' AND subject_id = ?`, id) != 1 || s.count(`SELECT COUNT(*) FROM users WHERE lower(email)='new.hire@corp.test'`) != 1 {
		t.Error("재전송이 중복을 만듦")
	}
	// 같은 externalId 에 다른 이메일, 다른 externalId 에 같은 이메일 → 409.
	if r := c.Post("/Users", scimtest.User("other@corp.test", "ext-1", "다른 사람", "T-PU1", "사원")); r.Status != 409 || r.ScimType() != "uniqueness" {
		t.Errorf("같은 externalId 다른 이메일: %d %s", r.Status, r.Raw)
	}
	if r := c.Post("/Users", scimtest.User("new.hire@corp.test", "ext-2", "신입 사원", "T-PU1", "사원")); r.Status != 409 || r.ScimType() != "uniqueness" {
		t.Errorf("다른 externalId 같은 이메일: %d %s", r.Status, r.Raw)
	}
	// 부서·직위가 없으면 기본값, 없는 부서·직위는 400, 이름·이메일 오류도 400.
	if r := c.Post("/Users", scimtest.User("defaults@corp.test", "ext-3", "기본값 사용자", "", "")); r.Status != 201 {
		t.Errorf("기본값 입사: %d %s", r.Status, r.Raw)
	}
	for name, body := range map[string]map[string]any{
		"없는 부서":  scimtest.User("a1@corp.test", "e-a1", "가", "NOPE", "사원"),
		"없는 직위":  scimtest.User("a2@corp.test", "e-a2", "나", "T-PU1", "우주대장"),
		"이메일 형식": scimtest.User("not-an-email", "e-a3", "다", "T-PU1", "사원"),
		"이름 없음":  scimtest.User("a4@corp.test", "e-a4", "", "T-PU1", "사원"),
	} {
		if r := c.Post("/Users", body); r.Status != 400 || r.ScimType() != "invalidValue" {
			t.Errorf("%s: %d %s", name, r.Status, r.Raw)
		}
	}
	if r := c.Post("/Users", "{not json"); r.Status != 400 || r.ScimType() != "invalidSyntax" {
		t.Errorf("깨진 JSON: %d %s", r.Status, r.Raw)
	}
	// 비활성 상태로는 새 사용자를 만들지 않는다.
	if r := c.EntraSync("ext-9", "off@corp.test", "비활성", "T-PU1", "사원", false); r.Status != 400 {
		t.Errorf("비활성 생성: %d %s", r.Status, r.Raw)
	}
	// 동기화 기록: 성공 2건 이상, 거부·충돌이 있다.
	if s.count(`SELECT COUNT(*) FROM scim_sync_log WHERE result='success'`) < 2 || s.count(`SELECT COUNT(*) FROM scim_sync_log WHERE result='conflict'`) < 2 || s.count(`SELECT COUNT(*) FROM scim_sync_log WHERE result='denied'`) < 4 {
		t.Error("동기화 기록의 성공·충돌·거부 건수가 부족함")
	}
	if code, body := s.call(s.admin, "GET", "/api/admin/scim/log?result=conflict", "", nil); code != 200 || strings.Contains(body, "new.hire@corp.test") || !strings.Contains(body, "conflict") {
		t.Errorf("기록 API(이메일 원문 금지): %d %s", code, body)
	}
	assertChainOK(t, s)
}

func assertChainOK(t *testing.T, s *scimEnv) {
	t.Helper()
	res, err := s.d.AuditReader.Verify(context.Background())
	if err != nil || !res.ChainOK {
		t.Fatalf("감사 체인 검증: %+v %v", res, err)
	}
}

func TestSCIMDeactivateAndDelete(t *testing.T) {
	s := newSCIMEnv(t, nil)
	s.enable()
	c := s.client(s.token("entra", "users"))
	// 기존 사용자(오래전 입사)를 이메일로 채택한 뒤 퇴사시킨다.
	r := c.Post("/Users", scimtest.User("p001@pl.test", "ext-p001", "", "", ""))
	if r.Status != 201 {
		t.Fatalf("채택: %d %s", r.Status, r.Raw)
	}
	id := r.ID()
	if s.count(`SELECT COUNT(*) FROM org_events WHERE kind='hire' AND subject_id = ?`, id) != 1 {
		t.Error("채택이 입사 발령을 또 만듦")
	}
	// Entra 형: 문자열 "False".
	if r := c.EntraSync("ext-p001", "p001@pl.test", "", "", "", false); r.Status != 200 || r.Active() {
		t.Fatalf("비활성: %d %s", r.Status, r.Raw)
	}
	if s.count(`SELECT COUNT(*) FROM audit_logs WHERE action='hr.leave' AND channel='scim' AND target_id = ?`, id) != 1 {
		t.Error("hr.leave 감사가 1건이 아님")
	}
	var active int
	_ = s.d.DB.QueryRow(`SELECT active FROM users WHERE id = ?`, id).Scan(&active)
	if active != 0 {
		t.Error("퇴사 뒤 users.active 가 0 이 아님")
	}
	// 같은 요청을 다시: 멱등(발령 1건 그대로).
	if r := c.Patch("/Users/"+id, scimtest.Patch(scimtest.Op("replace", "active", false))); r.Status != 200 || r.Active() {
		t.Errorf("재전송: %d %s", r.Status, r.Raw)
	}
	if r := c.Delete("/Users/" + id); r.Status != 204 {
		t.Errorf("이미 퇴사한 사용자 DELETE: %d", r.Status)
	}
	if s.count(`SELECT COUNT(*) FROM org_events WHERE kind='leave' AND subject_id = ?`, id) != 1 {
		t.Error("퇴사 발령이 1건이 아님")
	}
	// 재입사: active=true (부서·직위는 기본값).
	if r := c.Patch("/Users/"+id, scimtest.Patch(scimtest.Op("replace", "", map[string]any{"active": true}))); r.Status != 200 || !r.Active() {
		t.Errorf("재입사: %d %s", r.Status, r.Raw)
	}
	if s.count(`SELECT COUNT(*) FROM audit_logs WHERE action='hr.hire' AND channel='scim' AND target_id = ?`, id) != 1 {
		t.Error("재입사 감사가 없음")
	}
	// 재입사한 날에는 퇴사 발령을 낼 수 없다(입사 당일 규칙).
	if r := c.Delete("/Users/" + id); r.Status != 409 {
		t.Errorf("재입사 당일 DELETE: %d", r.Status)
	}
	// DELETE 도 퇴사 처리다(행은 지우지 않는다).
	d := c.Post("/Users", scimtest.User("p004@pl.test", "ext-p004", "", "", ""))
	if d.Status != 201 {
		t.Fatalf("채택: %d %s", d.Status, d.Raw)
	}
	if r := c.Delete("/Users/" + d.ID()); r.Status != 204 {
		t.Errorf("DELETE: %d", r.Status)
	}
	if s.count(`SELECT COUNT(*) FROM users WHERE id = ?`, d.ID()) != 1 {
		t.Error("DELETE 가 사용자 행을 지움")
	}
	if r := c.Get("/Users/" + d.ID()); r.Status != 200 || r.Active() {
		t.Errorf("삭제 뒤 조회: %d %s", r.Status, r.Raw)
	}
	// 입사 당일 퇴사는 409(발령 화면에서 처리).
	nu := c.EntraSync("ext-fresh", "fresh@corp.test", "당일 입사", "T-PU1", "사원", true)
	if nu.Status != 201 {
		t.Fatalf("신규: %d %s", nu.Status, nu.Raw)
	}
	if r := c.Patch("/Users/"+nu.ID(), scimtest.Patch(scimtest.Op("replace", "active", false))); r.Status != 409 {
		t.Errorf("입사 당일 퇴사: %d %s", r.Status, r.Raw)
	}
	// 효력일은 항상 오늘: 본문의 날짜 속성은 이벤트에 반영되지 않는다.
	for _, d := range s.eventDays(`kind IN ('hire','leave') AND created_by = (SELECT created_by FROM scim_tokens LIMIT 1)`) {
		if d > string(s.d.Org.(*org.Service).Today()) {
			t.Errorf("미래 효력일 %s", d)
		}
	}
	assertChainOK(t, s)
}

func (s *scimEnv) eventDays(where string) []string {
	rows, err := s.d.DB.Query(`SELECT effective_date FROM org_events WHERE ` + where)
	if err != nil {
		s.t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var d string
		_ = rows.Scan(&d)
		out = append(out, d)
	}
	return out
}

func TestSCIMProtectedAccounts(t *testing.T) {
	s := newSCIMEnv(t, nil)
	s.enable()
	c := s.client(s.token("okta", "users"))
	// 위험 권한 보유자(관리자)는 채택·조회·수정이 모두 막힌다.
	if r := c.Post("/Users", scimtest.User("admin@pl.test", "ext-admin", "관리자", "T-PU1", "사원")); r.Status != 409 || r.ScimType() != "uniqueness" {
		t.Errorf("관리자 채택: %d %s", r.Status, r.Raw)
	}
	if r := c.Get("/Users" + scimtest.Filter(`userName eq "admin@pl.test"`)); r.Status != 200 || r.Total() != 0 {
		t.Errorf("관리자 조회: %d %s", r.Status, r.Raw)
	}
	var adminID string
	_ = s.d.DB.QueryRow(`SELECT CAST(id AS TEXT) FROM users WHERE email='admin@pl.test'`).Scan(&adminID)
	if r := c.Patch("/Users/"+adminID, scimtest.Patch(scimtest.Op("replace", "active", false))); r.Status != 404 {
		t.Errorf("미연결 관리자 PATCH: %d %s", r.Status, r.Raw)
	}
	if r := c.Delete("/Users/" + adminID); r.Status != 404 {
		t.Errorf("미연결 관리자 DELETE: %d %s", r.Status, r.Raw)
	}
	var active int
	_ = s.d.DB.QueryRow(`SELECT active FROM users WHERE email='admin@pl.test'`).Scan(&active)
	if active != 1 {
		t.Error("관리자가 비활성화됨")
	}
	// 연결된 뒤 위험 권한이 생긴 사용자(역할 부여)도 쓰기가 막힌다.
	r := c.Post("/Users", scimtest.User("p002@pl.test", "ext-p002", "", "", ""))
	if r.Status != 201 {
		t.Fatalf("채택: %d %s", r.Status, r.Raw)
	}
	if _, err := s.d.DB.Exec(`UPDATE users SET role = 'admin' WHERE id = ?`, r.ID()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.d.DB.Exec(`INSERT INTO user_effective_permissions (user_id, permission, role_id, grant_id) SELECT ?, 'role:manage', id, 0 FROM roles WHERE key = 'admin'`, r.ID()); err != nil {
		t.Fatal(err)
	}
	if r2 := c.Patch("/Users/"+r.ID(), scimtest.Patch(scimtest.Op("replace", "active", false))); r2.Status != 400 || r2.ScimType() != "mutability" {
		t.Errorf("위험 권한 보유자 PATCH: %d %s", r2.Status, r2.Raw)
	}
	if s.auditCount("scim.rejected", "denied") < 1 {
		t.Error("보호 계정 거부 감사가 없음")
	}
	// 바꿀 수 없는 속성.
	r3 := c.Post("/Users", scimtest.User("p003@pl.test", "ext-p003", "", "", ""))
	if r3.Status != 201 {
		t.Fatalf("채택: %d %s", r3.Status, r3.Raw)
	}
	if r4 := c.Patch("/Users/"+r3.ID(), scimtest.Patch(scimtest.Op("replace", "userName", "renamed@corp.test"))); r4.Status != 400 || r4.ScimType() != "mutability" {
		t.Errorf("이메일 변경: %d %s", r4.Status, r4.Raw)
	}
	if r4 := c.Patch("/Users/"+r3.ID(), scimtest.Patch(scimtest.Op("replace", "bogus.path", "x"))); r4.Status != 400 || r4.ScimType() != "invalidPath" {
		t.Errorf("알 수 없는 경로: %d %s", r4.Status, r4.Raw)
	}
	assertChainOK(t, s)
}

func TestSCIMFilterAndPaging(t *testing.T) {
	s := newSCIMEnv(t, nil)
	s.enable()
	c := s.client(s.token("entra", "users"))
	for i, n := range []string{"alpha", "beta", "gamma"} {
		if r := c.EntraSync("ext-"+n, n+"@corp.test", "사람 "+n, "T-PU1", "사원", true); r.Status != 201 {
			t.Fatalf("%d 생성: %d %s", i, r.Status, r.Raw)
		}
	}
	all := c.Get("/Users")
	if all.Status != 200 || all.Total() != 3 || len(all.Resources()) != 3 {
		t.Fatalf("전체: %d %s", all.Status, all.Raw)
	}
	pg := c.Get("/Users?startIndex=2&count=1")
	if pg.Total() != 3 || len(pg.Resources()) != 1 || pg.Resources()[0]["userName"] != "beta@corp.test" {
		t.Errorf("페이지: %s", pg.Raw)
	}
	if r := c.Get("/Users?count=0"); r.Total() != 3 || len(r.Resources()) != 0 {
		t.Errorf("count=0: %s", r.Raw)
	}
	for filter, want := range map[string]int{
		`userName eq "ALPHA@corp.test"`:                    1,
		`externalId eq "ext-gamma"`:                        1,
		`userName sw "be"`:                                 1,
		`displayName co "사람"`:                              3,
		`active eq true and userName sw "b"`:               1,
		`active eq false`:                                  0,
		`userName eq "alpha@corp.test" and active eq true`: 1,
	} {
		if r := c.Get("/Users" + scimtest.Filter(filter)); r.Status != 200 || r.Total() != want {
			t.Errorf("필터 %s: %d total=%d(기대 %d) %s", filter, r.Status, r.Total(), want, r.Raw)
		}
	}
	for _, bad := range []string{`userName eq "a" or userName eq "b"`, `userName pr`, `unknownAttr eq "x"`, `emails[type eq "work"].value eq "x"`, `active co true`, `userName eq alpha`} {
		if r := c.Get("/Users" + scimtest.Filter(bad)); r.Status != 400 || r.ScimType() != "invalidFilter" {
			t.Errorf("미지원 필터 %q: %d %s", bad, r.Status, r.Raw)
		}
	}
	// 다른 출처 토큰에는 보이지 않는다.
	other := s.client(s.token("okta", "users"))
	if r := other.Get("/Users"); r.Total() != 0 {
		t.Errorf("다른 출처의 목록: %s", r.Raw)
	}
	if r := other.Get("/Users/" + all.Resources()[0]["id"].(string)); r.Status != 404 {
		t.Errorf("다른 출처의 단건: %d", r.Status)
	}
	// 단건 조회·존재하지 않는 id.
	if r := c.Get("/Users/" + all.Resources()[0]["id"].(string)); r.Status != 200 || r.Body["userName"] != "alpha@corp.test" {
		t.Errorf("단건: %s", r.Raw)
	}
	if r := c.Get("/Users/999999"); r.Status != 404 {
		t.Errorf("없는 id: %d", r.Status)
	}
	// 디스커버리와 그룹(꺼짐).
	if r := c.Get("/Schemas"); r.Status != 200 || r.Total() != 3 {
		t.Errorf("Schemas: %s", r.Raw)
	}
	if r := c.Get("/ResourceTypes"); r.Status != 200 || r.Total() != 2 {
		t.Errorf("ResourceTypes: %s", r.Raw)
	}
	if r := c.Get("/Groups"); r.Status != 403 { // 토큰 범위가 users 라 그룹은 403
		t.Errorf("그룹(범위 밖): %d", r.Status)
	}
	g := s.client(s.token("google", "users_groups"))
	if r := g.Get("/Groups"); r.Status != 200 || r.Total() != 0 {
		t.Errorf("그룹 목록(꺼짐): %d %s", r.Status, r.Raw)
	}
	if r := g.Post("/Groups", map[string]any{"displayName": "x"}); r.Status != 403 {
		t.Errorf("그룹 쓰기(꺼짐): %d", r.Status)
	}
	if r := c.Get("/Nope"); r.Status != 404 {
		t.Errorf("없는 경로(SPA 로 새지 않아야 함): %d", r.Status)
	}
}

func TestSCIMRateLimitAndAuthFailures(t *testing.T) {
	s := newSCIMEnv(t, func(c *config.Config) { c.SCIMRatePerMin = 3 })
	s.enable()
	c := s.client(s.token("entra", "users"))
	for i := 0; i < 3; i++ {
		if r := c.Get("/ServiceProviderConfig"); r.Status != 200 {
			t.Fatalf("%d 번째: %d", i, r.Status)
		}
	}
	r := c.Get("/ServiceProviderConfig")
	if r.Status != 429 || r.Header.Get("Retry-After") == "" {
		t.Fatalf("한도 초과: %d %v", r.Status, r.Header)
	}
	if s.auditCount("scim.rejected", "denied") != 1 {
		t.Error("한도 거부 감사가 1건이 아님")
	}
	c.Get("/ServiceProviderConfig")
	if s.auditCount("scim.rejected", "denied") != 1 {
		t.Error("한도 거부 감사가 10분당 1행이 아님")
	}
	// 인증 실패가 IP 당 분당 20회를 넘으면 429.
	bad := s.client("pls_nope")
	last := 0
	for i := 0; i < 25; i++ {
		last = bad.Get("/Users").Status
	}
	if last != 429 {
		t.Errorf("인증 실패 반복의 마지막 상태 %d", last)
	}
	if s.auditCount("scim.auth_failed", "denied") != 1 {
		t.Errorf("인증 실패 감사 %d건(10분당 1행)", s.auditCount("scim.auth_failed", "denied"))
	}
}

func TestSCIMDisabledInRehearsal(t *testing.T) {
	s := newSCIMEnv(t, func(c *config.Config) { c.Rehearsal = true })
	if r := s.client("pls_whatever").Get("/Users"); r.Status != 403 {
		t.Errorf("리허설 모드: %d", r.Status)
	}
}

func TestSCIMOktaFlowAndNameUpdate(t *testing.T) {
	s := newSCIMEnv(t, nil)
	s.enable()
	c := s.client(s.token("okta", "users"))
	// externalId 를 보내지 않는 Okta: 이메일이 식별자다. 새 사용자는 userName 조회 → POST.
	r := c.OktaSync("okta.user@corp.test", "오크타 사용자", "T-PU1", "사원", true)
	if r.Status != 201 || r.Body["externalId"] != "okta.user@corp.test" {
		t.Fatalf("생성: %d %s", r.Status, r.Raw)
	}
	id := r.ID()
	// 다음 주기: 조회로 찾아 PUT. 이름이 바뀌면 표시 이름만 갱신하고(발령 아님) 감사 user.update 가 남는다.
	r2 := c.OktaSync("okta.user@corp.test", "오크타 새이름", "T-PU1", "사원", true)
	if r2.Status != 200 || r2.Body["displayName"] != "오크타 새이름" {
		t.Fatalf("이름 갱신: %d %s", r2.Status, r2.Raw)
	}
	if s.count(`SELECT COUNT(*) FROM audit_logs WHERE action='user.update' AND channel='scim' AND target_id = ?`, id) != 1 {
		t.Error("user.update 감사(채널 scim)가 1건이 아님")
	}
	if s.count(`SELECT COUNT(*) FROM org_events WHERE subject_type='person' AND subject_id = ?`, id) != 1 {
		t.Error("이름 갱신이 발령을 만듦")
	}
	// 같은 내용으로 다시 보내면 변경 없음: 감사·기록이 늘지 않는다.
	logs := s.count(`SELECT COUNT(*) FROM scim_sync_log`)
	audits := s.count(`SELECT COUNT(*) FROM audit_logs`)
	if r3 := c.OktaSync("okta.user@corp.test", "오크타 새이름", "T-PU1", "사원", true); r3.Status != 200 {
		t.Fatalf("변경 없음: %d %s", r3.Status, r3.Raw)
	}
	if s.count(`SELECT COUNT(*) FROM scim_sync_log`) != logs || s.count(`SELECT COUNT(*) FROM audit_logs`) != audits {
		t.Error("변경 없는 요청이 기록·감사를 남김")
	}
	// 받지 않는 속성(password·roles)은 무시되고 경고가 기록된다. 역할은 바뀌지 않는다.
	u := scimtest.User("okta.user@corp.test", "", "오크타 새이름", "T-PU1", "사원")
	u["password"] = "Secret-1!"
	u["roles"] = []map[string]any{{"value": "admin"}}
	if r4 := c.Put("/Users/"+id, u); r4.Status != 200 {
		t.Fatalf("PUT: %d %s", r4.Status, r4.Raw)
	}
	var role string
	_ = s.d.DB.QueryRow(`SELECT role FROM users WHERE id = ?`, id).Scan(&role)
	if role != "staff" {
		t.Errorf("roles 속성이 역할을 바꿈: %q", role)
	}
	if code, body := s.call(s.admin, "GET", "/api/admin/scim/log?limit=5", "", nil); code != 200 || !strings.Contains(body, "roles") {
		t.Errorf("무시한 속성 경고가 기록에 없음: %d %s", code, body)
	}
	assertChainOK(t, s)
}
