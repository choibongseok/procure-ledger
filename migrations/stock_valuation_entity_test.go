package migrations_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/dbutil"
	"github.com/choibongseok/procure-ledger/migrations"
)

// B51 1단계 0104: 평가 줄에 법인(entity_id)·기준 표시(entity_basis)를 더한다. 0103 까지 쌓인 평가 줄의 값 열은 한 글자도 바뀌지 않고,
// 비기본 법인 창고의 줄만 법인이 채워지며, 여러 법인에 원장이 걸친 품목의 기존 줄만 entity_basis = 0 이 된다(명세 5절).
// 닫힌 달의 줄도 열을 채울 수 있어야 하고(트리거를 다시 만든다), 새 트리거는 평가 줄의 법인으로 마감 월을 판정한다.
func TestStockValuationEntityMigrationKeepsRowsSQLite(t *testing.T) {
	ctx := context.Background()
	db, err := dbutil.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := migrations.ApplyThrough(ctx, db, "0103_org_absence.sql"); err != nil {
		t.Fatal(err)
	}
	mustExec(t, db, `INSERT INTO departments (id, name) VALUES (900, '운영')`)
	mustExec(t, db, `INSERT INTO users (id, email, name, role, department_id) VALUES (1, 'u@x', '직원', 'staff', 900)`)
	mustExec(t, db, `INSERT INTO items (id, sku, name) VALUES (1,'A','단일법인품목'),(2,'B','두법인품목'),(3,'C','둘째법인품목')`)
	mustExec(t, db, `INSERT INTO entities (id, code, name, is_default, created_at, updated_at) VALUES (2,'ABC','둘째 법인',0,'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`)
	mustExec(t, db, `INSERT INTO warehouses (id, code, name, entity_id, created_at, updated_at) VALUES (2,'WH-02','둘째 창고',2,'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`)
	// 원장 1: 품목1 기본 법인, 2: 품목2 기본 법인, 3: 품목2 둘째 법인(품목2 는 두 법인에 걸침), 4: 품목3 둘째 법인(닫힌 달 줄).
	type led struct{ id, item, wh int64 }
	for _, l := range []led{{1, 1, 1}, {2, 2, 1}, {3, 2, 2}, {4, 3, 2}} {
		mustExec(t, db, `INSERT INTO stock_ledger (id, item_id, qty_delta, reason, actor_id, warehouse_id, book_date) VALUES (?,?,?,?,1,?,?)`,
			l.id, l.item, 5, "receipt", l.wh, "2026-01-10")
		mustExec(t, db, `INSERT INTO stock_valuation (ledger_id, item_id, book_date, in_unit_cost, unit_cost, amount, bal_qty, bal_amount, avg_cost, warned) VALUES (?,?,?,?,?,?,?,?,?,0)`,
			l.id, l.item, "2026-01-10", 100+l.id, 100+l.id, 5*(100+l.id), 5*l.id, 5*(100+l.id)*l.id, 100+l.id)
	}
	mustExec(t, db, `INSERT INTO month_closes (month, status, version, closed_at, entity_id) VALUES ('2026-01','closed',1,'2026-02-01T00:00:00Z',2)`)

	dump := func() string {
		rows, err := db.Query(`SELECT ledger_id, item_id, book_date, COALESCE(in_unit_cost,-1), unit_cost, amount, bal_qty, bal_amount, avg_cost, warned FROM stock_valuation ORDER BY ledger_id`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		s := ""
		for rows.Next() {
			var a, b, c, d, e, f, g, h, i int64
			var day string
			if err := rows.Scan(&a, &b, &day, &c, &d, &e, &f, &g, &h, &i); err != nil {
				t.Fatal(err)
			}
			s += fmt.Sprintln(a, b, day, c, d, e, f, g, h, i)
		}
		return s
	}
	before := dump()
	if err := migrations.Apply(ctx, db); err != nil {
		t.Fatal(err)
	}
	if after := dump(); after != before {
		t.Fatalf("값 열이 바뀌었습니다.\n--- 이전\n%s--- 이후\n%s", before, after)
	}
	want := map[int64][2]int64{1: {1, 1}, 2: {1, 0}, 3: {2, 0}, 4: {2, 1}} // 원장 번호 → {법인, 기준 표시}
	for id, w := range want {
		var ent, basis int64
		if err := db.QueryRow(`SELECT entity_id, entity_basis FROM stock_valuation WHERE ledger_id = ?`, id).Scan(&ent, &basis); err != nil {
			t.Fatal(err)
		}
		if ent != w[0] || basis != w[1] {
			t.Fatalf("원장 %d: 법인 %d 기준 %d, 기대 %v", id, ent, basis, w)
		}
	}
	// 새 트리거: 닫힌 달(둘째 법인 2026-01)의 둘째 법인 줄은 갱신·삭제가 막히고, 기본 법인 줄은 막히지 않는다.
	wantErr(t, db, "마감된 월", `UPDATE stock_valuation SET amount = amount WHERE ledger_id = 4`)
	wantErr(t, db, "마감된 월", `DELETE FROM stock_valuation WHERE ledger_id = 3`)
	mustExec(t, db, `UPDATE stock_valuation SET unit_cost = unit_cost WHERE ledger_id = 1`)
	// 새 줄의 기본값은 기본 법인·법인 기준이다.
	mustExec(t, db, `INSERT INTO stock_ledger (id, item_id, qty_delta, reason, actor_id, warehouse_id, book_date) VALUES (5,1,1,'receipt',1,1,'2026-02-01')`)
	mustExec(t, db, `INSERT INTO stock_valuation (ledger_id, item_id, book_date, unit_cost, amount, bal_qty, bal_amount) VALUES (5,1,'2026-02-01',1,1,6,6)`)
	var ent, basis int64
	_ = db.QueryRow(`SELECT entity_id, entity_basis FROM stock_valuation WHERE ledger_id = 5`).Scan(&ent, &basis)
	if ent != 1 || basis != 1 {
		t.Fatalf("새 줄 기본값 %d %d", ent, basis)
	}
	wantErr(t, db, "CHECK", `UPDATE stock_valuation SET entity_basis = 2 WHERE ledger_id = 1`)
}
