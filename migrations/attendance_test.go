package migrations_test

import (
	"context"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/dbutil"
	"github.com/choibongseok/procure-ledger/migrations"
)

// B50 5단계 0102: 출퇴근·근무 유형·정정·근태 마감 표를 더한다. 새 표만 만들므로 0101 까지 쌓인 휴가 신청·원장·결재 양식 행은 그대로여야 하고,
// 새 표의 추가 전용·유일 키·상태 전이 보호가 DB 에서 걸려야 한다.
func TestAttendanceMigrationKeepsLeaveDataAndGuardsNewTablesSQLite(t *testing.T) {
	ctx := context.Background()
	db, err := dbutil.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := migrations.ApplyThrough(ctx, db, "0101_leave_requests.sql"); err != nil {
		t.Fatal(err)
	}
	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	mustExec(`INSERT INTO departments (id, name) VALUES (900, '운영')`)
	mustExec(`INSERT INTO users (id, email, name, role, department_id) VALUES (1, 'u@x', '직원', 'staff', 900)`)
	mustExec(`INSERT INTO leave_ledger (entity_id, user_id, balance, kind, minutes, period_key, expires_on, created_at) VALUES (1, 1, 'annual', 'grant', 7200, 'hire:y01', '2027-03-15', 't')`)
	var before int
	_ = db.QueryRow(`SELECT COUNT(*) FROM approval_forms WHERE code = 'LEAVE'`).Scan(&before)
	if before != 1 {
		t.Fatalf("LEAVE 양식 %d", before)
	}
	if err := migrations.Apply(ctx, db); err != nil {
		t.Fatal(err)
	}
	var minutes int64
	if err := db.QueryRow(`SELECT minutes FROM leave_ledger WHERE period_key = 'hire:y01'`).Scan(&minutes); err != nil || minutes != 7200 {
		t.Fatalf("원장 행이 보존되지 않음: %d %v", minutes, err)
	}
	var after int
	_ = db.QueryRow(`SELECT COUNT(*) FROM approval_forms WHERE code = 'LEAVE'`).Scan(&after)
	if after != 1 {
		t.Fatalf("0102 뒤 LEAVE 양식 %d", after)
	}
	// 기록: 사용자·근무일 유일, 삭제 거부, 사용자·근무일 변경 거부.
	mustExec(`INSERT INTO attendance_records (user_id, work_date, check_in_at, source, created_at, updated_at) VALUES (1, '2026-11-10', '2026-11-10T00:00:00Z', 'web', 't', 't')`)
	if _, err := db.Exec(`INSERT INTO attendance_records (user_id, work_date, source, created_at, updated_at) VALUES (1, '2026-11-10', 'web', 't', 't')`); err == nil {
		t.Fatal("같은 날 기록이 두 번 들어감")
	}
	if _, err := db.Exec(`INSERT INTO attendance_records (user_id, work_date, source, created_at, updated_at) VALUES (1, '2026-11-11', 'mobile', 't', 't')`); err == nil {
		t.Fatal("알 수 없는 출처가 들어감")
	}
	if _, err := db.Exec(`DELETE FROM attendance_records`); err == nil {
		t.Fatal("기록 삭제가 막히지 않음")
	}
	if _, err := db.Exec(`UPDATE attendance_records SET work_date = '2026-11-12'`); err == nil {
		t.Fatal("근무일 변경이 막히지 않음")
	}
	mustExec(`UPDATE attendance_records SET check_out_at = '2026-11-10T09:00:00Z'`) // 퇴근 시각은 서비스가 채운다
	// 정정 줄: 요청 중인 줄의 결정만 기록되고, 결정된 뒤에는 바뀌지 않으며, 내용은 고칠 수 없다.
	mustExec(`INSERT INTO attendance_corrections (user_id, work_date, new_in, reason, requested_by, status, created_at) VALUES (1, '2026-11-09', '2026-11-09T00:00:00Z', '누락', 1, 'requested', 't')`)
	if _, err := db.Exec(`UPDATE attendance_corrections SET reason = '다른 사유'`); err == nil {
		t.Fatal("정정 사유 수정이 막히지 않음")
	}
	if _, err := db.Exec(`UPDATE attendance_corrections SET status = 'weird'`); err == nil {
		t.Fatal("알 수 없는 정정 상태")
	}
	mustExec(`UPDATE attendance_corrections SET status = 'applied', approved_by = 2, decided_at = 't2'`)
	if _, err := db.Exec(`UPDATE attendance_corrections SET status = 'rejected'`); err == nil {
		t.Fatal("결정된 정정 줄이 바뀜")
	}
	if _, err := db.Exec(`DELETE FROM attendance_corrections`); err == nil {
		t.Fatal("정정 줄 삭제가 막히지 않음")
	}
	// 근무 유형·배정: 코드 형식, 유일 키, 배정 이력 추가 전용, 경고 기록 유일 키.
	mustExec(`INSERT INTO work_types (code, name, kind, start_time, end_time, created_at) VALUES ('early', '조기', 'fixed', '08:00', '17:00', 't')`)
	if _, err := db.Exec(`INSERT INTO work_types (code, name, kind, created_at) VALUES ('early', '중복', 'fixed', 't')`); err == nil {
		t.Fatal("근무 유형 코드 중복")
	}
	if _, err := db.Exec(`INSERT INTO work_types (code, name, kind, start_time, created_at) VALUES ('late', '늦', 'fixed', '25:00x', 't')`); err == nil {
		t.Fatal("잘못된 시각 형식")
	}
	if _, err := db.Exec(`DELETE FROM work_types`); err == nil {
		t.Fatal("근무 유형 삭제가 막히지 않음")
	}
	mustExec(`INSERT INTO work_assignments (user_id, work_type_id, valid_from, created_at) VALUES (1, 1, '2026-11-01', 't')`)
	if _, err := db.Exec(`INSERT INTO work_assignments (user_id, work_type_id, valid_from, created_at) VALUES (1, 1, '2026-11-01', 't')`); err == nil {
		t.Fatal("같은 효력일 배정 중복")
	}
	if _, err := db.Exec(`UPDATE work_assignments SET valid_from = '2020-01-01'`); err == nil {
		t.Fatal("배정 이력 수정이 막히지 않음")
	}
	mustExec(`INSERT INTO attendance_closes (month, closed_by, closed_at) VALUES ('2026-10', 1, 't')`)
	if _, err := db.Exec(`DELETE FROM attendance_closes`); err == nil {
		t.Fatal("마감 기록 삭제가 막히지 않음")
	}
	mustExec(`INSERT INTO attendance_warnings (user_id, week_start, level, created_at) VALUES (1, '2026-11-09', 'caution', 't')`)
	if _, err := db.Exec(`INSERT INTO attendance_warnings (user_id, week_start, level, created_at) VALUES (1, '2026-11-09', 'caution', 't')`); err == nil {
		t.Fatal("같은 주 같은 단계 경고가 두 번 들어감")
	}
}
