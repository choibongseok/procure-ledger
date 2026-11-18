package main

// B33 재판정 반영: 거부된 역할 변경의 감사, 휴면 위험 권한 보유자 재활성 제한, 역할 이름 목록 경로.

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDeniedRoleChangesAreAudited(t *testing.T) {
	e := newPlatformEnvWith(t, true, cheapArgon)
	admin := e.browser()
	e.login(admin, "admin@pl.test")
	ah := jsonHdr(map[string]string{"X-CSRF-Token": e.csrf(admin)})
	var staffID int64
	_ = e.d.DB.QueryRow(`SELECT id FROM users WHERE email='staff@pl.test'`).Scan(&staffID)
	// 위험 권한이 든 기본 역할 지정은 400 이고 거부 감사가 남는다.
	if code, b := e.call(admin, "PATCH", "/api/admin/users/"+itoa(staffID), `{"role":"admin"}`, ah); code != 400 {
		t.Fatalf("위험 역할 지정: %d %s", code, b)
	}
	if e.auditCount("role.template_set", "denied") != 1 {
		t.Fatal("위험 역할 지정 거부 감사가 없음")
	}
	// 개인 토큰으로 역할을 바꾸려 하면 403 이고 거부 감사가 남는다.
	_, tb := e.call(admin, "POST", "/api/tokens", `{"name":"a","ttlDays":1}`, ah)
	var tk struct {
		Token string `json:"token"`
	}
	_ = json.Unmarshal([]byte(tb), &tk)
	hdr := map[string]string{"Authorization": "Bearer " + tk.Token, "Content-Type": "application/json"}
	if code, b := e.call(e.browser(), "PATCH", "/api/admin/users/"+itoa(staffID), `{"role":"manager"}`, hdr); code != 403 {
		t.Fatalf("토큰으로 역할 변경: %d %s", code, b)
	}
	if e.auditCount("role.template_set", "denied") != 2 {
		t.Fatalf("토큰 거부 감사: %d건", e.auditCount("role.template_set", "denied"))
	}
}

// 휴면 위험 권한 보유자의 재활성은 role:manage 가 있어야 한다(user:manage 만으로 되살려 권한을 얻을 수 없다).
func TestReactivatingRiskyHolderNeedsRoleManage(t *testing.T) {
	e := newPlatformEnvWith(t, true, cheapArgon)
	admin, staff := e.browser(), e.browser()
	e.login(admin, "admin@pl.test")
	e.login(staff, "staff@pl.test")
	ah := jsonHdr(map[string]string{"X-CSRF-Token": e.csrf(admin)})
	sh := jsonHdr(map[string]string{"X-CSRF-Token": e.csrf(staff)})
	var staffID, finID int64
	_ = e.d.DB.QueryRow(`SELECT id FROM users WHERE email='staff@pl.test'`).Scan(&staffID)
	_ = e.d.DB.QueryRow(`SELECT id FROM users WHERE email='finance@pl.test'`).Scan(&finID)
	// 직원에게 user:manage(사용자 수정)만 가진 사용자 정의 역할을 준다(role:manage 는 없다).
	_, rb := e.call(admin, "POST", "/api/admin/roles", `{"name":"사용자 관리","permissions":["user:manage"]}`, ah)
	var r struct {
		ID int64 `json:"id"`
	}
	_ = json.Unmarshal([]byte(rb), &r)
	if code, b := e.call(admin, "POST", "/api/admin/roles/"+itoa(r.ID)+"/grants", `{"targetType":"user","targetId":`+itoa(staffID)+`,"reason":"시험"}`, ah); code != 201 {
		t.Fatalf("부여: %d %s", code, b)
	}
	if code, b := e.call(admin, "PATCH", "/api/admin/users/"+itoa(finID), `{"active":false}`, ah); code != 200 {
		t.Fatalf("재무 비활성: %d %s", code, b)
	}
	if code, b := e.call(staff, "PATCH", "/api/admin/users/"+itoa(finID), `{"active":true}`, sh); code != 403 {
		t.Fatalf("user:manage 만으로 위험 권한 보유자 재활성: %d %s", code, b)
	}
	var active int
	_ = e.d.DB.QueryRow(`SELECT active FROM users WHERE id=?`, finID).Scan(&active)
	if active != 0 {
		t.Fatal("거부됐는데 활성화됨")
	}
	if e.auditCount("user.update", "denied") != 1 {
		t.Fatal("재활성 거부 감사가 없음")
	}
	if code, b := e.call(admin, "PATCH", "/api/admin/users/"+itoa(finID), `{"active":true}`, ah); code != 200 {
		t.Fatalf("role:manage 가 있으면 재활성: %d %s", code, b)
	}
	// 위험 권한이 없는 휴면 사용자는 user:manage 만으로 재활성된다.
	var s2 int64
	_ = e.d.DB.QueryRow(`SELECT id FROM users WHERE email='staff2@pl.test'`).Scan(&s2)
	if s2 != 0 {
		if code, _ := e.call(admin, "PATCH", "/api/admin/users/"+itoa(s2), `{"active":false}`, ah); code != 200 {
			t.Fatal("직원2 비활성")
		}
		if code, b := e.call(staff, "PATCH", "/api/admin/users/"+itoa(s2), `{"active":true}`, sh); code != 200 {
			t.Fatalf("비위험 사용자 재활성: %d %s", code, b)
		}
	}
}

func TestRoleNamesRoute(t *testing.T) {
	e := newPlatformEnvWith(t, true, cheapArgon)
	admin, fin, staff := e.browser(), e.browser(), e.browser()
	e.login(admin, "admin@pl.test")
	e.login(fin, "finance@pl.test")
	e.login(staff, "staff@pl.test")
	ah := jsonHdr(map[string]string{"X-CSRF-Token": e.csrf(admin)})
	if code, b := e.call(admin, "POST", "/api/admin/roles", `{"name":"구매 보조","permissions":["report:read"]}`, ah); code != 201 {
		t.Fatalf("역할 만들기: %d %s", code, b)
	}
	code, body := e.call(admin, "GET", "/api/role-names", "", nil)
	if code != 200 || !strings.Contains(body, "구매 보조") || !strings.Contains(body, `"key":"r_`) || strings.Contains(body, "permission") {
		t.Fatalf("관리자: %d %.200s", code, body)
	}
	if code, _ := e.call(fin, "GET", "/api/role-names", "", nil); code != 200 { // llm:usage_read 보유
		t.Fatalf("재무: %d", code)
	}
	if code, _ := e.call(staff, "GET", "/api/role-names", "", nil); code != 403 {
		t.Fatalf("직원: %d", code)
	}
}
