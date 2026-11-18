package service_test

import (
	"context"
	"testing"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/repository"
	"github.com/choibongseok/procure-ledger/internal/service"
)

// 부팅 이관 SplitValuationByEntity(B51 5.2, AC-5121·5123): 열린 달 줄만 법인 기준으로 다시 계산하고, 감사 한 건을 남기며, 재실행은 아무것도 하지 않는다.
func TestSplitValuationByEntityBootStep(t *testing.T) {
	e := newEnv(t)
	for _, s := range []string{
		`INSERT INTO items (id, sku, name) VALUES (901,'SPLIT-1','분리 품목')`,
		`INSERT INTO entities (id, code, name, created_at, updated_at) VALUES (2,'SPL','분리 법인','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`,
		`INSERT INTO warehouses (id, code, name, created_at, updated_at, entity_id) VALUES (91,'SPL-01','분리 창고','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z',2)`,
	} {
		if _, err := e.db.Exec(s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
	ctx := context.Background()
	inv := repository.SQLInventory{}
	at := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	for i, op := range []repository.LedgerEntry{
		{WarehouseID: 1, ItemID: 901, Qty: 10, Reason: "receipt", RefType: "receipt", RefID: 9001, BookDate: "2026-10-05", UnitCost: 100},
		{WarehouseID: 91, ItemID: 901, Qty: 10, Reason: "receipt", RefType: "receipt", RefID: 9002, BookDate: "2026-11-06", UnitCost: 300},
		{WarehouseID: 1, ItemID: 901, Qty: -4, Reason: "issue", RefType: "issue", RefID: 9003, BookDate: "2026-11-07"},
	} {
		op.ActorID, op.At = 1, at
		if err := inv.AppendLedger(ctx, e.db, op); err != nil {
			t.Fatalf("원장 %d: %v", i, err)
		}
	}
	// 0104 직후 모양: 법인 91 창고 줄은 법인 2, 모든 줄이 품목 전체 기준. 기본 법인의 2026-10 은 닫혔다.
	if _, err := e.db.Exec(`UPDATE stock_valuation SET entity_id = 2 WHERE ledger_id IN (SELECT id FROM stock_ledger WHERE warehouse_id = 91)`); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.Exec(`UPDATE stock_valuation SET entity_basis = 0 WHERE item_id = 901`); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.Exec(`INSERT INTO month_closes (entity_id, month, status, version, closed_by, closed_at) VALUES (1, '2026-10', 'closed', 1, 1, '2026-11-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	closedRow := func() [3]int64 {
		var r [3]int64
		if err := e.db.QueryRow(`SELECT amount, bal_qty, bal_amount FROM stock_valuation WHERE item_id = 901 AND book_date = '2026-10-05'`).Scan(&r[0], &r[1], &r[2]); err != nil {
			t.Fatal(err)
		}
		return r
	}
	closedBefore := closedRow()

	b := service.Base{DB: e.db, Txs: contract.SQLTxManager{DB: e.db}, Audit: e.rec, Now: func() time.Time { return e.now }}
	if err := service.SplitValuationByEntity(ctx, b); err != nil {
		t.Fatal(err)
	}
	if closedRow() != closedBefore {
		t.Fatalf("닫힌 달 줄이 바뀌었습니다: %v → %v", closedBefore, closedRow())
	}
	var bal [2]int64
	if err := e.db.QueryRow(`SELECT bal_qty, bal_amount FROM stock_valuation WHERE entity_id = 2 AND item_id = 901 ORDER BY book_date DESC, ledger_id DESC LIMIT 1`).Scan(&bal[0], &bal[1]); err != nil || bal != [2]int64{10, 3000} {
		t.Fatalf("법인 2 잔량·잔액 %v %v(10, 3000 이어야 합니다)", bal, err)
	}
	if err := e.db.QueryRow(`SELECT bal_qty, bal_amount FROM stock_valuation WHERE entity_id = 1 AND item_id = 901 ORDER BY book_date DESC, ledger_id DESC LIMIT 1`).Scan(&bal[0], &bal[1]); err != nil || bal != [2]int64{6, 600} {
		t.Fatalf("기본 법인 잔량·잔액 %v %v(6, 600 이어야 합니다)", bal, err)
	}
	splits := func() int {
		n := 0
		for _, r := range e.rec.Records() {
			if r.Action == "valuation.split" && r.Result == audit.ResultSuccess {
				n++
			}
		}
		return n
	}
	if splits() != 1 {
		t.Fatalf("이관 감사 %d건", splits())
	}
	if err := service.SplitValuationByEntity(ctx, b); err != nil {
		t.Fatal(err)
	}
	if splits() != 1 {
		t.Fatalf("재실행이 감사를 더했습니다(%d건)", splits())
	}
}
