package org

import (
	"context"
	"strings"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/dbutil"
	"github.com/choibongseok/procure-ledger/migrations"
)

// B50 0103: 사람 구간 표에 absence_kind 열을 더한다. 이미 구간 행이 쌓인 설치(0101 까지 적용)에 적용해도 행이 그대로이고
// 새 열은 빈 값이며, 열거 CHECK 와 조직 서비스만 쓸 수 있다는 쓰기 보호 트리거가 남아 있다(SQLite 전용. PG 는 짝 파일이고 verify-pg 의 일반 적용 시험이 지난다).
func TestAbsenceMigrationKeepsExistingSpans(t *testing.T) {
	ctx := context.Background()
	db, err := dbutil.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := migrations.ApplyThrough(ctx, db, "0101_leave_requests.sql"); err != nil {
		t.Fatal(err)
	}
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	exec(`INSERT INTO departments (id, name) VALUES (1, '운영')`)
	exec(`INSERT INTO users (id, email, name, role, department_id) VALUES (1, 'a@x.test', '가', 'staff', 1), (2, 'b@x.test', '나', 'staff', 1)`)
	exec(`INSERT INTO org_units (id, code) VALUES (1, 'CO')`)
	exec(`INSERT INTO org_write_guard (id) VALUES (1)`)
	exec(`INSERT INTO org_person_spans (user_id, valid_from, valid_to, left_flag, rank_id, primary_unit_id) VALUES (1, '2024-01-01', '2025-06-01', 0, 1, 1), (1, '2025-06-01', NULL, 0, 1, 1), (2, '2024-03-01', NULL, 0, 1, 1)`)
	exec(`DELETE FROM org_write_guard`)

	if err := migrations.Apply(ctx, db); err != nil {
		t.Fatal(err)
	}
	var n, blank int
	if err := db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(CASE WHEN absence_kind = '' THEN 1 ELSE 0 END),0) FROM org_person_spans`).Scan(&n, &blank); err != nil {
		t.Fatal(err)
	}
	if n != 3 || blank != 3 {
		t.Fatalf("기존 구간 행이 보존되고 새 열이 빈 값이어야 함: 행 %d, 빈 값 %d", n, blank)
	}
	var primary int64
	if err := db.QueryRow(`SELECT primary_unit_id FROM org_person_spans WHERE user_id = 2`).Scan(&primary); err != nil || primary != 1 {
		t.Fatalf("기존 열 값: %d %v", primary, err)
	}
	// 열거 CHECK: 모르는 휴직 구분은 거부된다.
	exec(`INSERT INTO org_write_guard (id) VALUES (1)`)
	if _, err := db.Exec(`UPDATE org_person_spans SET absence_kind = 'bogus' WHERE user_id = 2`); err == nil || !strings.Contains(strings.ToLower(err.Error()), "check") {
		t.Fatalf("모르는 구분은 CHECK 로 거부되어야 함: %v", err)
	}
	exec(`UPDATE org_person_spans SET absence_kind = 'illness' WHERE user_id = 2`)
	exec(`DELETE FROM org_write_guard`)
	// 쓰기 보호 트리거는 그대로다: 보호 행 없이는 새 열도 고칠 수 없다.
	if _, err := db.Exec(`UPDATE org_person_spans SET absence_kind = 'other' WHERE user_id = 2`); err == nil || !strings.Contains(err.Error(), "조직 서비스만") {
		t.Fatalf("보호 행 없는 쓰기는 거부되어야 함: %v", err)
	}
}
