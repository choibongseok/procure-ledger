package main

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/dbtest"
)

// B50 판정 후속(AC-5040): 휴가 신청서(LEAVE) 문서는 신청자·결재선·직속 조직장·인사 담당만 연다. 관리자·감사자처럼 전체 열람 권한이 있어도 문서 메타(GET /api/approval-docs/{id})는 404 이고,
// 문서 목록에도 나오지 않는다. 같은 범위는 다른 양식 문서에는 영향이 없다(관리자는 일반 양식 문서를 그대로 연다).
func TestLeaveDocVisibilityHidesFromAdminAndAuditor(t *testing.T) {
	e := newFlowEnv(t, dbtest.Open(t))
	staff := e.userID("staff@pl.test")
	y := time.Now().In(contract.KST).Year()
	for _, d := range []string{fmt.Sprintf("%d-12-25", y), fmt.Sprintf("%d-12-25", y+1)} {
		e.do("admin", "POST", "/api/company/holidays", `{"items":[{"date":"`+d+`","name":"휴일"}]}`)
	}
	if code, b := e.do("admin", "POST", "/api/admin/attendance/policies", goodPolicy); code != 201 {
		t.Fatalf("규칙 저장: %d %s", code, b)
	}
	e.must("admin", "PUT", "/api/admin/modules/attendance", `{"enabled":true}`, 200)
	// 인사 담당은 finance2 에게 사용자 정의 역할로 준다(관리자·감사자에게는 주지 않는다).
	for _, q := range []string{
		`INSERT INTO roles (key, name, kind, created_at, updated_at) VALUES ('r_hr','인사 담당','custom','a','a')`,
		`INSERT INTO role_permissions (role_id, permission) SELECT id, 'attendance:hr' FROM roles WHERE key='r_hr'`,
		fmt.Sprintf(`INSERT INTO role_grants (role_id, target_type, target_id, unit_kind, include_descendants, source, reason, created_at) SELECT id, 'user', %d, '', 0, 'manual', '시험', 'a' FROM roles WHERE key = 'r_hr'`, e.userID("finance2@pl.test")),
	} {
		if _, err := e.d.DB.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	e.must("finance2", "POST", "/api/attendance/leave/adjust", fmt.Sprintf(`{"userId":%d,"balance":"annual","kind":"opening","minutes":%d,"reason":"초기 이관"}`, staff, 10*480), 204)
	mon := time.Date(y+1, 3, 1, 0, 0, 0, 0, contract.KST)
	for mon.Weekday() != time.Monday {
		mon = mon.AddDate(0, 0, 1)
	}
	out := e.must("staff", "POST", "/api/attendance/leave/requests", fmt.Sprintf(`{"leaveCode":"annual","startDate":"%s","endDate":"%s","part":"full"}`, mon.Format("2006-01-02"), mon.Format("2006-01-02")), 201)
	var doc int64
	if err := e.d.DB.QueryRow(`SELECT doc_id FROM leave_requests WHERE id = ?`, num(out["id"])).Scan(&doc); err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/api/approval-docs/%d", doc)
	// 열리는 사람: 신청자, 결재선(head), 인사 담당(finance2). 그 밖은 404.
	for who, want := range map[string]int{"staff": 200, "head": 200, "finance2": 200, "admin": 404, "auditor": 404, "finance": 404, "staff2": 404} {
		if code, body := e.do(who, "GET", path, ""); code != want {
			t.Errorf("%s 의 휴가 문서 메타: %d(기대 %d) %.120s", who, code, want, body)
		}
	}
	// 문서 목록: 열리는 사람에게만 보인다.
	needle := fmt.Sprintf(`"id":%d,`, doc)
	has := func(who string) bool {
		code, body := e.do(who, "GET", "/api/approval-docs?form=LEAVE&limit=50", "")
		if code != 200 {
			t.Fatalf("%s 문서 목록: %d %s", who, code, body)
		}
		return strings.Contains(body, needle)
	}
	for who, want := range map[string]bool{"staff": true, "head": true, "finance2": true, "admin": false, "auditor": false, "finance": false} {
		if got := has(who); got != want {
			t.Errorf("%s 문서 목록에 휴가 문서 있음=%v(기대 %v)", who, got, want)
		}
	}
	// 결재 대기함: 결재선(head)에게는 나오고 관리자에게는 나오지 않는다.
	if code, body := e.do("head", "GET", "/api/approvals/pending", ""); code != 200 || !strings.Contains(body, needle) {
		t.Errorf("결재선의 대기함: %d %.120s", code, body)
	}
	if code, body := e.do("admin", "GET", "/api/approvals/pending", ""); code != 200 || strings.Contains(body, needle) {
		t.Errorf("관리자의 대기함: %d %.120s", code, body)
	}
	// 일반 양식 문서는 그대로다: 관리자는 휴가가 아닌 양식 문서를 전체 열람 권한으로 계속 연다.
	var other int64
	if err := e.d.DB.QueryRow(`INSERT INTO purchase_requests (no, requester_id, department_id, title, status, form_id, fields, created_at, updated_at)
		VALUES ('GEN-VIS-1', ?, (SELECT department_id FROM users WHERE id = ?), '일반 문서', 'submitted', (SELECT id FROM approval_forms WHERE code = 'GEN'), '{}', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z') RETURNING id`, staff, staff).Scan(&other); err != nil {
		t.Fatal(err)
	}
	if code, body := e.do("admin", "GET", fmt.Sprintf("/api/approval-docs/%d", other), ""); code != 200 {
		t.Errorf("관리자의 일반 문서: %d %.120s", code, body)
	}
}
