package mcp_test

import (
	"context"
	"strings"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
)

// B24: 서비스가 창고별 수량(ByWarehouse)을 주어도 기존 재고 도구의 결과는 합계 기준 그대로다(명세 24 의 10절, AC-401).
// 첫 단계에서는 도구가 창고별 수량을 내보내지 않는다(응답 길이·"어느 창고 재고인가" 혼동을 피하려는 결정).
type stubWarehouseStock struct {
	domain.InventoryService
}

func warehouseStock() domain.ItemStock {
	return domain.ItemStock{
		Item: domain.Item{ID: 7, SKU: "PAPER-A4", Name: "복사용지 A4", Unit: "박스", SafetyStock: 100}, OnHand: 80, BelowSafety: true,
		Lots:        []domain.LotStock{{LotID: 1, LotNo: "L-1", Quantity: 80}},
		ByWarehouse: []domain.WarehouseQty{{WarehouseID: 1, WarehouseName: "본사 창고", OnHand: 50}, {WarehouseID: 2, WarehouseName: "물류센터", OnHand: 30}},
	}
}

func (stubWarehouseStock) GetStock(context.Context, int64) (domain.ItemStock, error) {
	return warehouseStock(), nil
}
func (stubWarehouseStock) ListBelowSafety(context.Context) ([]domain.ItemStock, error) {
	return []domain.ItemStock{warehouseStock()}, nil
}

func (stubWarehouseStock) ListExpiring(context.Context, int) ([]domain.ExpiringLot, error) {
	return nil, nil
}

func TestStockToolsStayTotalBasedWithWarehouses(t *testing.T) {
	_, d := newDeps()
	d.Inventory = stubWarehouseStock{}
	cs := connect(t, d, staffP)
	for _, c := range []struct {
		tool string
		args map[string]any
	}{{contract.ToolGetStock, map[string]any{"item_id": 7}}, {contract.ToolListStockAlerts, map[string]any{}}} {
		res, text := call(t, cs, c.tool, c.args)
		if res.IsError {
			t.Fatalf("%s: %s", c.tool, text)
		}
		if !strings.Contains(text, `"on_hand":80`) {
			t.Fatalf("%s: 합계 80 이어야 함: %s", c.tool, text)
		}
		for _, bad := range []string{"warehouse", "by_warehouse", "byWarehouse", "본사 창고", "물류센터", `"on_hand":50`, `"on_hand":30`} {
			if strings.Contains(text, bad) {
				t.Fatalf("%s: 창고별 값이 나가면 안 됨 %q: %s", c.tool, bad, text)
			}
		}
	}
}
