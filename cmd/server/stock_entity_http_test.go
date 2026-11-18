package main

import (
	"strings"
	"testing"
	"time"
)

// B44 후속: 재고 경고 화면(재고 상태 보드의 below-safety·expiring)과 재고 조회가 현재 법인의 창고 재고만 보인다.
func TestStockScreensAreEntityScopedOverHTTP(t *testing.T) {
	e := newERPOrgEnv(t)
	exec := func(q string, args ...any) int64 {
		t.Helper()
		var id int64
		if err := e.d.DB.QueryRow(q+` RETURNING id`, args...).Scan(&id); err != nil {
			t.Fatalf("시드: %v\n%s", err, q)
		}
		return id
	}
	abc := exec(`INSERT INTO entities (code, name, is_default, active, version, created_at, updated_at) VALUES ('ABC', '계열사', 0, 1, 1, 'x', 'x')`)
	whA := exec(`INSERT INTO warehouses (code, name, entity_id, created_at, updated_at) VALUES ('WH-A', '본사창고', 1, 't', 't')`)
	whB := exec(`INSERT INTO warehouses (code, name, entity_id, created_at, updated_at) VALUES ('WH-B', '계열사창고', ?, 't', 't')`, abc)
	soon := time.Now().AddDate(0, 0, 5).Format("2006-01-02")
	item := exec(`INSERT INTO items (sku, name, safety_stock, track_lot) VALUES ('HTTP-STK', '교차 품목', 20, 1)`)
	lotA := exec(`INSERT INTO lots (item_id, lot_no, expiry_date) VALUES (?, 'HTTP-LOT-A', ?)`, item, soon)
	lotB := exec(`INSERT INTO lots (item_id, lot_no, expiry_date) VALUES (?, 'HTTP-LOT-B', ?)`, item, soon)
	actor := e.userID("admin@pl.test")
	for _, r := range []struct{ lot, wh, qty int64 }{{lotA, whA, 4}, {lotB, whB, 9}} {
		exec(`INSERT INTO stock_ledger (item_id, lot_id, qty_delta, reason, actor_id, warehouse_id) VALUES (?, ?, ?, 'receipt', ?, ?)`, item, r.lot, r.qty, actor, r.wh)
	}
	// 관리자에게 계열사 법인의 관리자 역할을 부여한다(법인 전환은 그 법인에서 권한이 있을 때만 된다).
	for _, ent := range []int64{1, abc} {
		exec(`INSERT INTO role_grants (role_id, target_type, target_id, source, created_at, scope, scope_entity_id)
			SELECT id, 'user', ?, 'manual', 't', 'entity', ? FROM roles WHERE key = 'admin'`, actor, ent)
	}
	for ent, want := range map[string]struct{ has, not string }{
		"MAIN": {"HTTP-LOT-A", "HTTP-LOT-B"},
		"ABC":  {"HTTP-LOT-B", "HTTP-LOT-A"},
	} {
		if code, body := e.do("admin", "PUT", "/api/me/entity", `{"entity":"`+ent+`"}`); code != 200 && code != 204 {
			t.Fatalf("법인 전환 %s: %d %s", ent, code, body)
		}
		code, body := e.do("admin", "GET", "/api/stock/expiring?days=30", "")
		if code != 200 || !strings.Contains(body, want.has) || strings.Contains(body, want.not) {
			t.Errorf("임박 로트(%s): %d %s", ent, code, body)
		}
		code, body = e.do("admin", "GET", "/api/stock/below-safety", "")
		qty := map[string]string{"MAIN": `"onHand":4`, "ABC": `"onHand":9`}[ent]
		if code != 200 || !strings.Contains(body, "HTTP-STK") || !strings.Contains(strings.ReplaceAll(body, " ", ""), qty) {
			t.Errorf("안전재고 미달(%s): %d %s (기대 %s)", ent, code, body, qty)
		}
	}
}
