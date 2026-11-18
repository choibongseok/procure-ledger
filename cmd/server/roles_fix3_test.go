package main

import "testing"

// B33 재판정 3(낮음): 역할 이름 조회 거부 감사, 사용자 수정 PATCH 가 위험하지 않은 사용자 정의 역할 키도 받는다.
func TestRoleNamesDeniedIsAudited(t *testing.T) {
	e := newPlatformEnvWith(t, true, cheapArgon)
	staff := e.browser()
	e.login(staff, "staff@pl.test")
	if code, _ := e.call(staff, "GET", "/api/role-names", "", nil); code != 403 {
		t.Fatalf("직원: %d", code)
	}
	if e.auditCount("role.names_read", "denied") != 1 {
		t.Fatal("역할 이름 조회 거부 감사가 없음")
	}
}

func TestPatchAcceptsSafeCustomRole(t *testing.T) {
	e := newPlatformEnvWith(t, true, cheapArgon)
	admin := e.browser()
	e.login(admin, "admin@pl.test")
	ah := jsonHdr(map[string]string{"X-CSRF-Token": e.csrf(admin)})
	var staffID int64
	_ = e.d.DB.QueryRow(`SELECT id FROM users WHERE email='staff@pl.test'`).Scan(&staffID)
	code, rb := e.call(admin, "POST", "/api/admin/roles", `{"name":"조회 보조","permissions":["report:read"]}`, ah)
	if code != 201 {
		t.Fatalf("역할 만들기: %d %s", code, rb)
	}
	var key string
	_ = e.d.DB.QueryRow(`SELECT key FROM roles WHERE name='조회 보조'`).Scan(&key)
	if code, b := e.call(admin, "PATCH", "/api/admin/users/"+itoa(staffID), `{"role":"`+key+`"}`, ah); code != 200 {
		t.Fatalf("사용자 정의 역할 지정: %d %s", code, b)
	}
	has := func(perm string) bool {
		var n int
		_ = e.d.DB.QueryRow(`SELECT COUNT(*) FROM user_effective_permissions WHERE user_id=? AND permission=?`, staffID, perm).Scan(&n)
		return n > 0
	}
	if !has("report:read") || has("purchase:create") {
		t.Fatalf("역할이 바뀐 실효 권한: report:read=%v purchase:create=%v", has("report:read"), has("purchase:create"))
	}
	if code, _ := e.call(admin, "PATCH", "/api/admin/users/"+itoa(staffID), `{"role":"r_nope"}`, ah); code != 400 {
		t.Fatalf("없는 역할: %d", code)
	}
}
