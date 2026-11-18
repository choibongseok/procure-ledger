package service_test

import (
	"testing"
	"time"
)

// B44 후속: 재고 경고(안전재고 미달·유효기한 임박)와 품목 재고는 현재 법인의 창고 원장 합이다. 법인이 둘이어도 다른 법인의 수량·로트가 섞이지 않고,
// 전체 모드(그룹 범위)에서만 모든 창고를 합친다.
func TestStockAlertsAndStockAreEntityScoped(t *testing.T) {
	e := newEnv(t)
	abc := e.secondEntity(t)
	exec := func(q string, args ...any) int64 {
		t.Helper()
		var id int64
		if err := e.db.QueryRow(q+` RETURNING id`, args...).Scan(&id); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		return id
	}
	whA := exec(`INSERT INTO warehouses (code, name, entity_id, created_at, updated_at) VALUES ('WH-A', '본사창고', 1, 't', 't')`)
	whB := exec(`INSERT INTO warehouses (code, name, entity_id, created_at, updated_at) VALUES ('WH-B', '계열사창고', ?, 't', 't')`, abc)
	soon := time.Now().AddDate(0, 0, 5).Format("2006-01-02")
	// 공통 품목(안전재고 10): 본사 6개, 계열사 3개. 합 9 라 어느 한 법인 기준으로도 미달이다.
	shared := exec(`INSERT INTO items (sku, name, safety_stock) VALUES ('SH-1', '공통 품목', 10)`)
	// 본사만 다루는 품목: 본사 12개(충분). 계열사 창고에는 원장이 없다.
	onlyA := exec(`INSERT INTO items (sku, name, safety_stock) VALUES ('A-ONLY', '본사 전용', 10)`)
	// 로트 품목: 법인마다 다른 로트를 임박 기한으로 입고.
	lotItem := exec(`INSERT INTO items (sku, name, safety_stock, track_lot) VALUES ('LOT-1', '로트 품목', 0, 1)`)
	lotA := exec(`INSERT INTO lots (item_id, lot_no, expiry_date) VALUES (?, 'LOT-A', ?)`, lotItem, soon)
	lotB := exec(`INSERT INTO lots (item_id, lot_no, expiry_date) VALUES (?, 'LOT-B', ?)`, lotItem, soon)
	led := func(item, lot, wh, qty int64) {
		var lotArg any
		if lot != 0 {
			lotArg = lot
		}
		exec(`INSERT INTO stock_ledger (item_id, lot_id, qty_delta, reason, actor_id, warehouse_id) VALUES (?, ?, ?, 'receipt', ?, ?)`, item, lotArg, qty, uFinance, wh)
	}
	led(shared, 0, whA, 6)
	led(shared, 0, whB, 3)
	led(onlyA, 0, whA, 12)
	led(lotItem, lotA, whA, 4)
	led(lotItem, lotB, whB, 7)

	// 본사 기준: 공통 품목은 6개(미달), 본사 전용은 충분, 로트 품목은 안전재고 0 이라 미달 아님. 임박 로트는 LOT-A 뿐이다.
	ctxA, ctxB, ctxAll := inEntity(uFinance, 1), inEntity(uFinance, abc), inEntity(uFinance, 0)
	low, err := e.inventory.ListBelowSafety(ctxA)
	if err != nil || len(low) != 1 || low[0].Item.SKU != "SH-1" || low[0].OnHand != 6 {
		t.Fatalf("본사 미달: %+v %v", low, err)
	}
	exp, err := e.inventory.ListExpiring(ctxA, 30)
	if err != nil || len(exp) != 1 || exp[0].Lot.LotNo != "LOT-A" || exp[0].Lot.Quantity != 4 {
		t.Fatalf("본사 임박: %+v %v", exp, err)
	}
	// 계열사 기준: 공통 품목 3개(미달), 본사 전용 품목은 이 법인 원장이 없어 경고에서 빠진다. 임박 로트는 LOT-B 뿐이다.
	low, err = e.inventory.ListBelowSafety(ctxB)
	if err != nil || len(low) != 1 || low[0].Item.SKU != "SH-1" || low[0].OnHand != 3 {
		t.Fatalf("계열사 미달: %+v %v", low, err)
	}
	exp, err = e.inventory.ListExpiring(ctxB, 30)
	if err != nil || len(exp) != 1 || exp[0].Lot.LotNo != "LOT-B" || exp[0].Lot.Quantity != 7 {
		t.Fatalf("계열사 임박: %+v %v", exp, err)
	}
	// 전체 모드: 합계(9)와 두 로트.
	low, err = e.inventory.ListBelowSafety(ctxAll)
	if err != nil || len(low) != 1 || low[0].OnHand != 9 {
		t.Fatalf("전체 미달: %+v %v", low, err)
	}
	if exp, err = e.inventory.ListExpiring(ctxAll, 30); err != nil || len(exp) != 2 {
		t.Fatalf("전체 임박: %+v %v", exp, err)
	}
	// 품목 재고(GetStock)도 현재 법인 창고 합이다: 로트 목록에 다른 법인 로트가 없다.
	st, err := e.inventory.GetStock(ctxA, lotItem)
	if err != nil || st.OnHand != 4 || len(st.Lots) != 1 || st.Lots[0].LotNo != "LOT-A" {
		t.Fatalf("본사 재고: %+v %v", st, err)
	}
	if st, err = e.inventory.GetStock(ctxB, lotItem); err != nil || st.OnHand != 7 || len(st.Lots) != 1 || st.Lots[0].LotNo != "LOT-B" {
		t.Fatalf("계열사 재고: %+v %v", st, err)
	}
	if st, err = e.inventory.GetStock(ctxAll, lotItem); err != nil || st.OnHand != 11 || len(st.Lots) != 2 {
		t.Fatalf("전체 재고: %+v %v", st, err)
	}
}
