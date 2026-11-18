package backup_test

import (
	"context"
	"strings"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/backup"
)

// 법인별 평가 잔량 불변식(B51 0104): (법인, 품목)의 마지막 평가 줄 잔량은 그 법인 창고의 원장 수량 합과 같다. 한 줄을 바꾸면 그 지점을 짚는다.
func TestInvariantValuationBalancePerEntity(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	f.exec(`INSERT INTO items (id, sku, name) VALUES (1, 'SK-1', '볼트')`)
	f.exec(`INSERT INTO entities (id, code, name, is_default, created_at, updated_at) VALUES (2, 'ABC', '둘째 법인', 0, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`)
	f.exec(`INSERT INTO warehouses (id, code, name, entity_id, created_at, updated_at) VALUES (2, 'WH-02', '둘째 창고', 2, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`)
	// 원장: 기본 법인 창고 +10, 둘째 법인 창고 +4. 평가 줄은 법인마다 자기 잔량을 이어 간다.
	f.exec(`INSERT INTO stock_ledger (id, item_id, qty_delta, reason, ref_type, ref_id, actor_id, created_at, warehouse_id, book_date) VALUES (101, 1, 10, 'receipt', 'receipt', 1, 1, '2026-11-01T00:00:00Z', 1, '2026-11-01')`)
	f.exec(`INSERT INTO stock_ledger (id, item_id, qty_delta, reason, ref_type, ref_id, actor_id, created_at, warehouse_id, book_date) VALUES (102, 1, 4, 'receipt', 'receipt', 2, 1, '2026-11-02T00:00:00Z', 2, '2026-11-02')`)
	f.exec(`INSERT INTO stock_valuation (ledger_id, item_id, book_date, unit_cost, amount, bal_qty, bal_amount, avg_cost, entity_id) VALUES (101, 1, '2026-11-01', 100, 1000, 10, 1000, 100, 1)`)
	f.exec(`INSERT INTO stock_valuation (ledger_id, item_id, book_date, unit_cost, amount, bal_qty, bal_amount, avg_cost, entity_id) VALUES (102, 1, '2026-11-02', 200, 800, 4, 800, 200, 2)`)
	if v := backup.CheckInvariants(ctx, f.db); len(v) != 0 {
		t.Fatalf("법인별 잔량이 맞는 DB 에 위반이 보고됨: %v", v)
	}
	// 둘째 법인 줄 하나만 어긋나게 한다(닫힌 달이 아니므로 갱신된다).
	f.exec(`UPDATE stock_valuation SET bal_qty = 5 WHERE ledger_id = 102`)
	v := backup.CheckInvariants(ctx, f.db)
	if len(v) != 1 || !strings.Contains(v[0], "법인별 평가 잔량") || !strings.Contains(v[0], "1건") {
		t.Fatalf("한 줄 변조를 짚지 못함: %v", v)
	}
	// 품목 전체 기준(이관 전 값, entity_basis = 0) 줄은 점검 밖이다.
	f.exec(`UPDATE stock_valuation SET entity_basis = 0 WHERE ledger_id = 102`)
	if v := backup.CheckInvariants(ctx, f.db); len(v) != 0 {
		t.Fatalf("entity_basis = 0 줄은 건너뛰어야 함: %v", v)
	}
}
