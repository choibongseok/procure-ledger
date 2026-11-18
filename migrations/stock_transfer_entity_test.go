package migrations_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/dbutil"
	"github.com/choibongseok/procure-ledger/migrations"
)

// B51 4단계 0105: 이동 전표에 출발·도착 법인, 이동 줄에 동결 금액, 창고의 법인 변경 금지.
// 0104 까지 쌓인 이동 전표·줄의 기존 값은 한 글자도 바뀌지 않고, 법인 열은 두 창고의 법인으로 채워진다. 이동 전표 수정 금지 트리거는 그대로 살아 있다.
func TestStockTransferEntityMigrationKeepsRowsSQLite(t *testing.T) {
	ctx := context.Background()
	db, err := dbutil.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := migrations.ApplyThrough(ctx, db, "0104_stock_valuation_entity.sql"); err != nil {
		t.Fatal(err)
	}
	mustExec(t, db, `INSERT INTO departments (id, name) VALUES (900, '운영')`)
	mustExec(t, db, `INSERT INTO users (id, email, name, role, department_id) VALUES (1, 'u@x', '직원', 'staff', 900)`)
	mustExec(t, db, `INSERT INTO items (id, sku, name) VALUES (1,'A','품목')`)
	mustExec(t, db, `INSERT INTO entities (id, code, name, is_default, created_at, updated_at) VALUES (2,'ABC','둘째 법인',0,'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`)
	mustExec(t, db, `INSERT INTO warehouses (id, code, name, entity_id, created_at, updated_at) VALUES (2,'WH-02','같은법인 창고',1,'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z'),(3,'WH-03','둘째 창고',2,'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`)
	// 이동 1: 같은 법인(1→2). 이동 2: 이관 전에 이미 있던 다른 법인 창고가 낀 전표(1→3, 예전에는 만들 수 없었지만 열을 채우는 규칙을 시험한다).
	mustExec(t, db, `INSERT INTO stock_transfers (id, no, from_wh_id, to_wh_id, move_date, memo, actor_id, created_at) VALUES (1,'MV-2026-0001',1,2,'2026-01-10','기본',1,'2026-01-10T00:00:00Z'),(2,'MV-2026-0002',1,3,'2026-01-11','',1,'2026-01-11T00:00:00Z')`)
	mustExec(t, db, `INSERT INTO stock_transfer_lines (id, transfer_id, item_id, qty) VALUES (1,1,1,5),(2,2,1,7)`)

	dump := func() string {
		s := ""
		rows, err := db.Query(`SELECT id, no, from_wh_id, to_wh_id, move_date, memo, actor_id, created_at FROM stock_transfers ORDER BY id`)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var id, f, to, actor int64
			var no, day, memo, at string
			if err := rows.Scan(&id, &no, &f, &to, &day, &memo, &actor, &at); err != nil {
				t.Fatal(err)
			}
			s += fmt.Sprintln(id, no, f, to, day, memo, actor, at)
		}
		rows.Close()
		lines, err := db.Query(`SELECT id, transfer_id, item_id, qty FROM stock_transfer_lines ORDER BY id`)
		if err != nil {
			t.Fatal(err)
		}
		for lines.Next() {
			var a, b, c, d int64
			if err := lines.Scan(&a, &b, &c, &d); err != nil {
				t.Fatal(err)
			}
			s += fmt.Sprintln("line", a, b, c, d)
		}
		lines.Close()
		return s
	}
	before := dump()
	if err := migrations.Apply(ctx, db); err != nil {
		t.Fatal(err)
	}
	if after := dump(); after != before {
		t.Fatalf("기존 값이 바뀌었습니다.\n--- 이전\n%s--- 이후\n%s", before, after)
	}
	for id, want := range map[int64][2]int64{1: {1, 1}, 2: {1, 2}} {
		var f, to int64
		if err := db.QueryRow(`SELECT from_entity_id, to_entity_id FROM stock_transfers WHERE id = ?`, id).Scan(&f, &to); err != nil {
			t.Fatal(err)
		}
		if f != want[0] || to != want[1] {
			t.Fatalf("전표 %d: 출발 %d 도착 %d, 기대 %v", id, f, to, want)
		}
	}
	var cost int64
	if err := db.QueryRow(`SELECT SUM(cost_amount) FROM stock_transfer_lines`).Scan(&cost); err != nil || cost != 0 {
		t.Fatalf("동결 금액 기본값: %d %v", cost, err)
	}
	// 이동 전표는 여전히 고칠 수도 지울 수도 없다.
	wantErr(t, db, "고칠 수 없습니다", `UPDATE stock_transfers SET memo = 'x' WHERE id = 1`)
	wantErr(t, db, "지울 수 없습니다", `DELETE FROM stock_transfers WHERE id = 1`)
	// 새 전표의 기본값은 기본 법인이다. 동결 금액은 음수가 될 수 없다.
	mustExec(t, db, `INSERT INTO stock_transfers (id, no, from_wh_id, to_wh_id, move_date, actor_id, created_at) VALUES (3,'MV-2026-0003',1,2,'2026-01-12',1,'2026-01-12T00:00:00Z')`)
	var f, to int64
	_ = db.QueryRow(`SELECT from_entity_id, to_entity_id FROM stock_transfers WHERE id = 3`).Scan(&f, &to)
	if f != 1 || to != 1 {
		t.Fatalf("새 전표 기본값 %d %d", f, to)
	}
	wantErr(t, db, "CHECK", `INSERT INTO stock_transfer_lines (transfer_id, item_id, qty, cost_amount) VALUES (3,1,1,-1)`)
	// 창고의 법인은 바꿀 수 없고 다른 열은 바꿀 수 있다.
	wantErr(t, db, "창고의 법인은 바꿀 수 없습니다", `UPDATE warehouses SET entity_id = 1 WHERE id = 3`)
	mustExec(t, db, `UPDATE warehouses SET name = '새 이름' WHERE id = 3`)
	mustExec(t, db, `UPDATE warehouses SET entity_id = 1 WHERE id = 3 AND entity_id = 1`)
}
