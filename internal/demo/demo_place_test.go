package demo_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/contract/deps"
	"github.com/choibongseok/procure-ledger/internal/demo"
)

// installAdmin 은 설치 마법사가 만드는 모습의 첫 관리자(데모 루트 이름의 부서, 조직 발령 없음)를 만든다.
func installAdmin(t *testing.T, d deps.Deps) int64 {
	t.Helper()
	if _, err := d.DB.Exec(`INSERT INTO departments (name) VALUES ('데모 주식회사')`); err != nil {
		t.Fatal(err)
	}
	// PG 드라이버는 LastInsertId 를 지원하지 않으므로 RETURNING id 로 받는다(SQLite 3.35+ 와 PG 공통).
	var id int64
	if err := d.DB.QueryRow(`INSERT INTO users (email, name, role, department_id, active) VALUES ('owner@acme.test', '오너', 'admin', (SELECT id FROM departments WHERE name = '데모 주식회사'), 1) RETURNING id`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

// 첫 관리자 배치: 발령(입사 이벤트)·감사가 남고 부서가 데모 조직으로 옮겨지며, 다시 해도 중복 발령이 없다.
func TestPlaceAdminUsesAppointmentPathAndIsIdempotent(t *testing.T) {
	d, closeDB := build(t)
	defer closeDB()
	ctx := context.Background()
	adminID := installAdmin(t, d)
	if err := demo.Seed(ctx, d); err != nil {
		t.Fatalf("데모 데이터: %v", err)
	}
	placed, err := demo.PlaceAdmin(ctx, d, adminID)
	if err != nil || !placed {
		t.Fatalf("배치: %v %v", placed, err)
	}
	hires := `SELECT COUNT(*) FROM org_events WHERE subject_type = 'person' AND subject_id = ? AND kind = 'hire'`
	if got := scalar(t, d, hires, adminID); got != "1" {
		t.Errorf("입사 발령 %s건", got)
	}
	if got := scalar(t, d, `SELECT created_by FROM org_events WHERE subject_id = ? AND kind = 'hire'`, adminID); got != fmt.Sprint(adminID) {
		t.Errorf("발령자 %s(관리자 %d 여야 함)", got, adminID)
	}
	if got := scalar(t, d, `SELECT COUNT(*) FROM audit_logs WHERE action = 'hr.hire' AND actor_id = ? AND result = 'success'`, adminID); got != "1" {
		t.Errorf("배치 감사 기록 %s건(관리자 이름으로 1건이어야 함)", got)
	}
	if got := scalar(t, d, `SELECT d.name FROM users u JOIN departments d ON d.id = u.department_id WHERE u.id = ?`, adminID); got != "경영지원팀" {
		t.Errorf("관리자 부서 %q", got)
	}
	// 멱등: 두 번째는 아무것도 바꾸지 않는다.
	auditBefore := scalar(t, d, `SELECT COUNT(*) FROM audit_logs`)
	placed, err = demo.PlaceAdmin(ctx, d, adminID)
	if err != nil || placed {
		t.Fatalf("다시 배치: %v %v", placed, err)
	}
	if got := scalar(t, d, hires, adminID); got != "1" {
		t.Errorf("중복 발령 %s건", got)
	}
	if got := scalar(t, d, `SELECT COUNT(*) FROM audit_logs`); got != auditBefore {
		t.Errorf("다시 배치하며 감사 기록이 늘었다: %s -> %s", auditBefore, got)
	}
	if res, err := d.AuditReader.Verify(ctx); err != nil || !res.ChainOK {
		t.Fatalf("감사 체인: %+v %v", res, err)
	}
}

// 데모 조직이 없으면(시드 전) 배치는 실패하고 아무것도 남기지 않는다. 설치 훅은 이 실패를 안내만 한다.
func TestPlaceAdminWithoutDemoOrgFailsCleanly(t *testing.T) {
	d, closeDB := build(t)
	defer closeDB()
	adminID := installAdmin(t, d)
	if placed, err := demo.PlaceAdmin(context.Background(), d, adminID); err == nil || placed {
		t.Fatalf("조직 없이 배치됨: %v %v", placed, err)
	}
	if got := scalar(t, d, `SELECT COUNT(*) FROM org_events`); got != "0" {
		t.Errorf("조직 발령 %s건이 남음", got)
	}
}
