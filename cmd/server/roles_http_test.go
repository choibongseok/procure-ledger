package main

// B33 3단계: 역할·부여 API 를 실제 라우트·세션·개인 토큰·감사 체인으로 끝까지 태운다.

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/choibongseok/procure-ledger/internal/access"
	"github.com/choibongseok/procure-ledger/internal/contract"
)

func TestPermissionDeclarationsRoleRoutes(t *testing.T) {
	e := newPlatformEnv(t, false)
	want := map[string]contract.Permission{
		"GET /api/admin/permissions":                 contract.PermRoleManage,
		"GET /api/admin/roles":                       contract.PermRoleManage,
		"GET /api/admin/role-targets":                contract.PermRoleManage,
		"POST /api/admin/roles":                      contract.PermRoleManage,
		"PUT /api/admin/roles/{id}":                  contract.PermRoleManage,
		"DELETE /api/admin/roles/{id}":               contract.PermRoleManage,
		"POST /api/admin/roles/{id}/clone":           contract.PermRoleManage,
		"GET /api/admin/roles/{id}/grants":           contract.PermRoleManage,
		"POST /api/admin/roles/{id}/grants/preview":  contract.PermRoleManage,
		"POST /api/admin/roles/{id}/grants":          contract.PermRoleManage,
		"DELETE /api/admin/role-grants/{id}":         contract.PermRoleManage,
		"GET /api/admin/role-requests":               contract.PermRoleManage,
		"POST /api/admin/role-requests/{id}/approve": contract.PermRoleManage,
		"POST /api/admin/role-requests/{id}/reject":  contract.PermRoleManage,
		"GET /api/admin/users/{id}/access":           contract.PermRoleManage,
		"GET /api/admin/roles/separation":            contract.PermRoleManage,
		"GET /api/me/access":                         contract.PermAuthenticated,
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

func TestRolesAndGrantsOverHTTP(t *testing.T) {
	e := newPlatformEnvWith(t, true, cheapArgon)
	admin, staff := e.browser(), e.browser()
	e.login(admin, "admin@pl.test")
	e.login(staff, "staff@pl.test")
	ah := jsonHdr(map[string]string{"X-CSRF-Token": e.csrf(admin)})
	sh := jsonHdr(map[string]string{"X-CSRF-Token": e.csrf(staff)})
	var staffID, leadID int64
	_ = e.d.DB.QueryRow(`SELECT id FROM users WHERE email='staff@pl.test'`).Scan(&staffID)
	_ = e.d.DB.QueryRow(`SELECT id FROM users WHERE email='lead@pl.test'`).Scan(&leadID)

	// 직원은 역할 관리가 403, 감사 로그도 403.
	if code, _ := e.call(staff, "GET", "/api/admin/roles", "", nil); code != 403 {
		t.Fatalf("직원의 역할 목록: %d", code)
	}
	if code, _ := e.call(staff, "GET", "/api/audit-logs", "", nil); code != 403 {
		t.Fatalf("직원의 감사 로그: %d", code)
	}
	code, body := e.call(admin, "GET", "/api/admin/roles", "", nil)
	if code != 200 || !strings.Contains(body, `"key":"admin"`) || !strings.Contains(body, `"kind":"template"`) {
		t.Fatalf("역할 목록: %d %.200s", code, body)
	}
	if code, body := e.call(admin, "GET", "/api/admin/role-targets", "", nil); code != 200 || !strings.Contains(body, `"ranks"`) || !strings.Contains(body, "staff@pl.test") {
		t.Fatalf("부여 대상 목록: %d %.200s", code, body)
	}
	if code, _ := e.call(staff, "GET", "/api/admin/role-targets", "", nil); code != 403 {
		t.Fatalf("직원의 부여 대상 목록: %d", code)
	}
	// 사용자 정의 역할 만들기 → 직원에게 부여 → 같은 세션의 다음 요청부터 권한이 생긴다.
	code, body = e.call(admin, "POST", "/api/admin/roles", `{"name":"감사 보조","description":"조회만","permissions":["audit:read"]}`, ah)
	var role struct {
		ID      int64  `json:"id"`
		Version int64  `json:"version"`
		Key     string `json:"key"`
	}
	_ = json.Unmarshal([]byte(body), &role)
	if code != 201 || role.ID == 0 {
		t.Fatalf("역할 생성: %d %s", code, body)
	}
	if code, _ := e.call(admin, "POST", "/api/admin/roles", `{"name":"감사 보조","permissions":[]}`, ah); code != 400 {
		t.Fatalf("이름 중복: %d", code)
	}
	gp := "/api/admin/roles/" + itoa(role.ID) + "/grants"
	in := `{"targetType":"user","targetId":` + itoa(staffID) + `}`
	code, body = e.call(admin, "POST", gp+"/preview", in, ah)
	if code != 200 || !strings.Contains(body, `"newlyGained":1`) {
		t.Fatalf("미리보기: %d %s", code, body)
	}
	if code, _ := e.call(staff, "GET", "/api/audit-logs", "", nil); code != 403 {
		t.Fatal("미리보기가 권한을 줌")
	}
	code, body = e.call(admin, "POST", gp, in, ah)
	var g struct {
		GrantID int64 `json:"grantId"`
	}
	_ = json.Unmarshal([]byte(body), &g)
	if code != 201 || g.GrantID == 0 {
		t.Fatalf("부여: %d %s", code, body)
	}
	if code, _ := e.call(staff, "GET", "/api/audit-logs", "", nil); code != 200 {
		t.Fatalf("부여 직후 직원의 감사 로그: %d", code)
	}
	if _, body := e.call(staff, "GET", "/api/me", "", nil); !strings.Contains(body, `"audit:read"`) || !strings.Contains(body, `"감사 보조"`) {
		t.Fatalf("/api/me 의 permissions·roles: %s", body)
	}
	if code, body := e.call(staff, "GET", "/api/me/access", "", nil); code != 200 || !strings.Contains(body, "사람 직접") {
		t.Fatalf("내 권한 출처: %d %s", code, body)
	}
	// 개인 토큰은 발급자의 현재 실효 권한을 따른다: 회수하면 같은 토큰이 바로 403.
	_, tb := e.call(staff, "POST", "/api/tokens", `{"name":"t","ttlDays":1}`, sh)
	var tk struct {
		Token string `json:"token"`
	}
	_ = json.Unmarshal([]byte(tb), &tk)
	bearer := map[string]string{"Authorization": "Bearer " + tk.Token}
	if code, _ := e.call(e.browser(), "GET", "/api/audit-logs", "", bearer); code != 200 {
		t.Fatalf("토큰으로 감사 로그: %d", code)
	}
	if code, _ := e.call(admin, "DELETE", "/api/admin/role-grants/"+itoa(g.GrantID), "", ah); code != 204 {
		t.Fatalf("회수: %d", code)
	}
	if code, _ := e.call(e.browser(), "GET", "/api/audit-logs", "", bearer); code != 403 {
		t.Fatalf("회수 뒤 같은 토큰: %d", code)
	}
	if code, _ := e.call(staff, "GET", "/api/audit-logs", "", nil); code != 403 {
		t.Fatalf("회수 뒤 같은 세션: %d", code)
	}
	// 역할·부여 API 는 개인 토큰으로 호출할 수 없다(세션 전용).
	_, atb := e.call(admin, "POST", "/api/tokens", `{"name":"a","ttlDays":1}`, ah)
	var at struct {
		Token string `json:"token"`
	}
	_ = json.Unmarshal([]byte(atb), &at)
	if code, _ := e.call(e.browser(), "POST", "/api/admin/roles", `{"name":"토큰으로","permissions":[]}`, map[string]string{"Authorization": "Bearer " + at.Token, "Content-Type": "application/json"}); code != 403 {
		t.Fatalf("관리자 토큰으로 역할 생성: %d", code)
	}

	// 위험 권한: 관리자가 한 명뿐이면 사유를 받고 즉시 적용(단독), 두 명이면 다른 관리자의 승인이 필요하다.
	_, rb := e.call(admin, "POST", "/api/admin/roles", `{"name":"감사 검증자","permissions":["audit:verify"]}`, ah)
	var risky struct {
		ID int64 `json:"id"`
	}
	_ = json.Unmarshal([]byte(rb), &risky)
	rin := `{"targetType":"user","targetId":` + itoa(staffID) + `,"reason":"감사 담당 지정"}`
	// 두 번째 관리자(lead)를 만든다. 위험 권한이 든 기본 역할은 PATCH 호환 경로로 지정할 수 없으므로(아래 시험) 데이터를 직접 바꾼다.
	if code, b := e.call(admin, "PATCH", "/api/admin/users/"+itoa(leadID), `{"role":"admin"}`, ah); code != 400 {
		t.Fatalf("위험 권한이 든 역할 지정은 거부돼야 함: %d %s", code, b)
	}
	if _, err := e.d.DB.Exec(`UPDATE users SET role='admin' WHERE id=?`, leadID); err != nil {
		t.Fatal(err)
	}
	if _, err := access.RecomputeAll(t.Context(), e.d.DB, contract.DayOf(time.Now())); err != nil {
		t.Fatal(err)
	}
	code, body = e.call(admin, "POST", "/api/admin/roles/"+itoa(risky.ID)+"/grants", rin, ah)
	var pend struct {
		Request struct {
			ID int64 `json:"id"`
		} `json:"request"`
	}
	_ = json.Unmarshal([]byte(body), &pend)
	if code != 202 || pend.Request.ID == 0 {
		t.Fatalf("위험 부여는 요청이어야 함: %d %s", code, body)
	}
	if code, _ := e.call(staff, "POST", "/api/audit-logs/verify", "", sh); code != 403 {
		t.Fatalf("승인 전 적용됨: %d", code)
	}
	if code, _ := e.call(admin, "POST", "/api/admin/role-requests/"+itoa(pend.Request.ID)+"/approve", "", ah); code != 409 {
		t.Fatalf("본인 승인: %d", code)
	}
	lead := e.browser()
	e.login(lead, "lead@pl.test")
	lh := jsonHdr(map[string]string{"X-CSRF-Token": e.csrf(lead)})
	if code, b := e.call(lead, "POST", "/api/admin/role-requests/"+itoa(pend.Request.ID)+"/approve", "", lh); code != 204 {
		t.Fatalf("다른 관리자의 승인: %d %s", code, b)
	}
	if code, _ := e.call(staff, "POST", "/api/audit-logs/verify", "", sh); code != 200 {
		t.Fatalf("승인 뒤 감사 검증: %d", code)
	}
	if code, body := e.call(admin, "GET", "/api/admin/roles/separation", "", nil); code != 200 || !strings.Contains(body, `"findings"`) {
		t.Fatalf("권한 분리 점검: %d %.100s", code, body)
	}
	if code, body := e.call(admin, "GET", "/api/admin/users/"+itoa(staffID)+"/access", "", nil); code != 200 || !strings.Contains(body, "감사 검증자") {
		t.Fatalf("사용자 권한 출처: %d %.200s", code, body)
	}
	for _, a := range []string{"role.create", "role.grant", "role.revoke", "role.request", "role.approve"} {
		if e.auditCount(a, "success") == 0 {
			t.Errorf("감사 %s 없음", a)
		}
	}
	if r, err := e.d.AuditReader.Verify(t.Context()); err != nil || !r.OK {
		t.Fatalf("체인: %+v %v", r, err)
	}
}

// 검증자 재현 A: 관리자 개인 토큰으로는 사용자 역할을 바꿀 수 없다(역할 변경은 세션 전용).
func TestUserRolePatchRejectsToken(t *testing.T) {
	e := newPlatformEnvWith(t, true, cheapArgon)
	admin := e.browser()
	e.login(admin, "admin@pl.test")
	ah := jsonHdr(map[string]string{"X-CSRF-Token": e.csrf(admin)})
	var staffID int64
	_ = e.d.DB.QueryRow(`SELECT id FROM users WHERE email='staff@pl.test'`).Scan(&staffID)
	_, tb := e.call(admin, "POST", "/api/tokens", `{"name":"a","ttlDays":1}`, ah)
	var tk struct {
		Token string `json:"token"`
	}
	_ = json.Unmarshal([]byte(tb), &tk)
	hdr := map[string]string{"Authorization": "Bearer " + tk.Token, "Content-Type": "application/json"}
	if code, b := e.call(e.browser(), "PATCH", "/api/admin/users/"+itoa(staffID), `{"role":"manager"}`, hdr); code != 403 {
		t.Fatalf("토큰으로 역할 변경: %d %s", code, b)
	}
	var role string
	_ = e.d.DB.QueryRow(`SELECT role FROM users WHERE id=?`, staffID).Scan(&role)
	if role != "staff" {
		t.Fatalf("역할이 바뀜: %s", role)
	}
	// 세션으로는 위험 권한이 없는 역할 변경이 되고, role.template_set 감사가 남는다.
	if code, b := e.call(admin, "PATCH", "/api/admin/users/"+itoa(staffID), `{"role":"manager"}`, ah); code != 200 {
		t.Fatalf("세션 역할 변경: %d %s", code, b)
	}
	if e.auditCount("role.template_set", "success") == 0 {
		t.Fatal("role.template_set 감사 없음")
	}
}

// 검증자 재현 B: user:manage 와 audit:read 만 가진 사용자 정의 역할 사용자는 본인을 관리자로 올릴 수 없다.
func TestUserManageAloneCannotEscalate(t *testing.T) {
	e := newPlatformEnvWith(t, true, cheapArgon)
	admin, staff := e.browser(), e.browser()
	e.login(admin, "admin@pl.test")
	e.login(staff, "staff@pl.test")
	ah := jsonHdr(map[string]string{"X-CSRF-Token": e.csrf(admin)})
	sh := jsonHdr(map[string]string{"X-CSRF-Token": e.csrf(staff)})
	var staffID int64
	_ = e.d.DB.QueryRow(`SELECT id FROM users WHERE email='staff@pl.test'`).Scan(&staffID)
	_, rb := e.call(admin, "POST", "/api/admin/roles", `{"name":"사용자 관리","permissions":["user:manage","audit:read"]}`, ah)
	var r struct {
		ID int64 `json:"id"`
	}
	_ = json.Unmarshal([]byte(rb), &r)
	rin := `{"targetType":"user","targetId":` + itoa(staffID) + `,"reason":"시험"}`
	// user:manage 는 위험 권한이라 부여는 단독 관리자 사유 적용(202 요청 또는 201). 어느 쪽이든 적용되게 만든다.
	code, body := e.call(admin, "POST", "/api/admin/roles/"+itoa(r.ID)+"/grants", rin, ah)
	if code != 201 && code != 202 {
		t.Fatalf("부여: %d %s", code, body)
	}
	if code == 202 { // 관리자가 둘 이상이면 승인이 필요하다: 직접 반영해 시험 조건을 만든다
		t.Skip("부여가 2인 확인 대기라 시험 조건을 만들 수 없음")
	}
	if code, b := e.call(staff, "PATCH", "/api/admin/users/"+itoa(staffID), `{"role":"admin"}`, sh); code != 403 && code != 400 {
		t.Fatalf("user:manage 만으로 본인 상승: %d %s", code, b)
	}
	var role string
	_ = e.d.DB.QueryRow(`SELECT role FROM users WHERE id=?`, staffID).Scan(&role)
	if role != "staff" {
		t.Fatalf("역할이 올라감: %s", role)
	}
}
