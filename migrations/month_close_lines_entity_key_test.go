package migrations_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/dbutil"
	"github.com/choibongseok/procure-ledger/migrations"
)

// B51 판정 후속 0106: 월말 스냅샷 줄의 유일 키에 법인을 넣는다.
// B44 시기에 섞인 옛 스냅샷(기본 법인 스냅샷에 둘째 법인 창고 줄)이 있어도 기존 행은 번호·값 그대로 남고,
// 둘째 법인이 같은 달·버전·품목·창고를 마감할 때 막히지 않으며, 같은 법인 안의 중복은 여전히 거부되고, 추가 전용 트리거는 살아 있다.
func TestMonthCloseLinesEntityKeyMigrationSQLite(t *testing.T) {
	ctx := context.Background()
	db, err := dbutil.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := migrations.ApplyThrough(ctx, db, "0105_stock_transfer_entity.sql"); err != nil {
		t.Fatal(err)
	}
	mustExec(t, db, `INSERT INTO items (id, sku, name) VALUES (1,'A','품목')`)
	mustExec(t, db, `INSERT INTO entities (id, code, name, is_default, created_at, updated_at) VALUES (2,'ABC','둘째 법인',0,'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`)
	mustExec(t, db, `INSERT INTO warehouses (id, code, name, entity_id, created_at, updated_at) VALUES (2,'WH-02','둘째 창고',2,'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`)
	line := `INSERT INTO month_close_lines (month, version, item_id, warehouse_id, open_qty, open_amount, in_qty, in_amount, out_qty, out_amount, transfer_qty, transfer_amount, adjust_qty, adjust_amount, close_qty, close_amount, avg_cost, entity_id)
		VALUES ('2026-01', 1, 1, ?, 0,0,0,0,0,0,0,0,0,0, 9, ?, 7, ?)`
	// 옛 키에서도 들어가는 행: 기본 법인 스냅샷의 기본 창고 줄과, 섞여 들어간 둘째 법인 창고 줄(법인은 기본 법인으로 적혀 있다).
	mustExec(t, db, line, 1, 900, 1)
	mustExec(t, db, line, 2, 6300, 1)
	// 옛 키는 법인을 몰라 둘째 법인의 같은 줄을 거부했다(이 재현이 0106 이 고치는 결함이다).
	wantErr(t, db, "UNIQUE", line, 2, 6300, 2)

	dump := func() string {
		rows, err := db.Query(`SELECT id, warehouse_id, close_amount, avg_cost, entity_id FROM month_close_lines ORDER BY id`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		s := ""
		for rows.Next() {
			var id, wh, amt, avg, ent int64
			if err := rows.Scan(&id, &wh, &amt, &avg, &ent); err != nil {
				t.Fatal(err)
			}
			s += fmt.Sprintln(id, wh, amt, avg, ent)
		}
		return s
	}
	before := dump()
	if err := migrations.Apply(ctx, db); err != nil {
		t.Fatal(err)
	}
	if after := dump(); after != before {
		t.Fatalf("기존 스냅샷 줄이 바뀌었다: %q → %q", before, after)
	}
	// 둘째 법인이 같은 달을 마감할 때의 같은 줄이 이제 들어간다. 같은 법인 안의 중복은 거부된다.
	mustExec(t, db, line, 2, 6300, 2)
	wantErr(t, db, "UNIQUE", line, 2, 6300, 2)
	// 추가 전용 트리거가 살아 있다. 새 줄의 번호는 이어진다.
	wantErr(t, db, "고칠 수 없습니다", `UPDATE month_close_lines SET close_qty = 1 WHERE id = 1`)
	wantErr(t, db, "지울 수 없습니다", `DELETE FROM month_close_lines WHERE id = 1`)
	var maxID int64
	_ = db.QueryRow(`SELECT MAX(id) FROM month_close_lines`).Scan(&maxID)
	if maxID != 3 {
		t.Fatalf("새 줄 번호 %d", maxID)
	}
}
