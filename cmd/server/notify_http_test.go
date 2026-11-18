package main

// B36 단계 1: 알림 채널 관리 라우트를 실제 라우트·세션·권한 표로 태운다.

import (
	"strings"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/dbtest"
)

func TestNotifyRoutesPermissionDeclarations(t *testing.T) {
	e := newPlatformEnv(t, false)
	got := map[string]contract.Permission{}
	for _, r := range e.rt.Routes() {
		got[r.Method+" "+r.Pattern] = r.Permission
	}
	want := map[string]contract.Permission{
		"GET /api/notify/status":                  contract.PermNotificationManage,
		"GET /api/notify/deliveries":              contract.PermNotificationManage,
		"GET /api/audit/notify-deliveries":        contract.PermAuditRead,
		"POST /api/notify/deliveries/{id}/resend": contract.PermNotificationManage,
		"POST /api/notify/mail-test":              contract.PermNotificationManage,
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

func TestNotifyRoutesByRoleAndNoSecretsInResponses(t *testing.T) {
	e := newPlatformEnv(t, true)
	admin, aud, staff, fin := e.browser(), e.browser(), e.browser(), e.browser()
	e.login(admin, "admin@pl.test")
	e.login(aud, "auditor@pl.test")
	e.login(staff, "staff@pl.test")
	e.login(fin, "finance@pl.test")
	var staffID int64
	_ = e.d.DB.QueryRow(`SELECT id FROM users WHERE email='staff@pl.test'`).Scan(&staffID)
	// 알림 한 건과 발송 건(이미 실패)을 직접 만든다.
	nid := dbtest.InsertID(t, e.d.DB, `INSERT INTO notifications (user_id, kind, title, body, link, ref_type, ref_id, created_at) VALUES (?, 'approval_pending', '민감한 제목', '금액 9,999원', '/approvals/1', 'purchase_request', 1, '2026-11-12T00:00:00Z')`, staffID)
	if _, err := e.d.DB.Exec(`INSERT INTO notification_deliveries (notification_id, user_id, channel, kind, status, attempts, next_try_at, last_error_code, created_at) VALUES (?, ?, 'email', 'approval_pending', 'failed', 1, '2026-11-12T00:00:00Z', 'smtp_perm', '2026-11-12T00:00:00Z')`, nid, staffID); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		who  string
		path string
		want int
	}{
		{"admin", "/api/notify/status", 200}, {"admin", "/api/notify/deliveries", 200},
		{"auditor", "/api/notify/deliveries", 403}, {"auditor", "/api/audit/notify-deliveries", 200},
		{"staff", "/api/notify/deliveries", 403}, {"staff", "/api/audit/notify-deliveries", 403}, {"staff", "/api/notify/status", 403},
		{"finance", "/api/notify/deliveries", 403}, {"admin", "/api/audit/notify-deliveries", 200},
	}
	clients := map[string]interface{}{"admin": admin, "auditor": aud, "staff": staff, "finance": fin}
	_ = clients
	for _, c := range cases {
		cl := admin
		switch c.who {
		case "auditor":
			cl = aud
		case "staff":
			cl = staff
		case "finance":
			cl = fin
		}
		code, body := e.call(cl, "GET", c.path, "", nil)
		if code != c.want {
			t.Errorf("%s GET %s: %d(기대 %d)", c.who, c.path, code, c.want)
		}
		if code == 200 {
			for _, secret := range []string{"민감한 제목", "9,999", "staff@pl.test", "http", "smtp_perm"} {
				if strings.Contains(body, secret) {
					t.Errorf("%s 응답에 %q 가 있음: %.200s", c.path, secret, body)
				}
			}
		}
	}
	_, body := e.call(admin, "GET", "/api/notify/deliveries", "", nil)
	if !strings.Contains(body, "메일 서버가 수신을 거절했습니다") || !strings.Contains(body, `"canResend":true`) {
		t.Errorf("발송 기록: %.300s", body)
	}
	// 다시 보내기·시험 메일은 관리자의 로그인 화면에서만.
	hdr := jsonHdr(map[string]string{"X-CSRF-Token": e.csrf(admin)})
	if code, b := e.call(admin, "POST", "/api/notify/mail-test", "", hdr); code != 409 || !strings.Contains(b, "설정되지 않았습니다") {
		t.Errorf("메일 미설정 시험 메일: %d %.100s", code, b)
	}
	if code, _ := e.call(staff, "POST", "/api/notify/mail-test", "", jsonHdr(map[string]string{"X-CSRF-Token": e.csrf(staff)})); code != 403 {
		t.Errorf("직원의 시험 메일: %d", code)
	}
	var did int64
	_ = e.d.DB.QueryRow(`SELECT id FROM notification_deliveries LIMIT 1`).Scan(&did)
	if code, b := e.call(admin, "POST", "/api/notify/deliveries/"+itoa(did)+"/resend", "", hdr); code != 204 {
		t.Errorf("다시 보내기: %d %s", code, b)
	}
	if e.auditCount("notification.resend", "success") != 1 {
		t.Error("다시 보내기 감사가 없음")
	}
	// 개인 토큰으로는 다시 보내기를 못 한다(세션 전용).
	_, tb := e.call(admin, "POST", "/api/tokens", `{"name":"n","ttlDays":1}`, hdr)
	i := strings.Index(tb, `"token":"`)
	if i < 0 {
		t.Fatalf("토큰 발급: %s", tb)
	}
	tok := tb[i+len(`"token":"`):]
	tok = tok[:strings.Index(tok, `"`)]
	if code, _ := e.call(e.browser(), "POST", "/api/notify/mail-test", "", map[string]string{"Authorization": "Bearer " + tok}); code != 403 {
		t.Errorf("토큰으로 시험 메일: %d", code)
	}
}
