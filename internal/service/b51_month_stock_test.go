package service_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/repository"
)

// 월별 재고 현황의 법인 거름과 전체 모드(B51 3단계, AC-5110·5114·5144).
func TestMonthStockIsPerEntityAndAllModeSplitsRows(t *testing.T) {
	e := newEnv(t)
	abc := e.secondEntity(t)
	for _, s := range []string{
		`INSERT INTO items (id, sku, name) VALUES (951,'MS-1','법인 품목')`,
		`INSERT INTO warehouses (id, code, name, created_at, updated_at, entity_id) VALUES (95,'ABC-W','계열사 창고','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z',` + "2" + `)`,
	} {
		if _, err := e.db.Exec(s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
	_ = abc
	inv := repository.SQLInventory{}
	at := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	for i, op := range []repository.LedgerEntry{
		{WarehouseID: 1, ItemID: 951, Qty: 10, Reason: "receipt", RefType: "receipt", RefID: 9501, BookDate: "2026-10-05", UnitCost: 100},
		{WarehouseID: 95, ItemID: 951, Qty: 5, Reason: "receipt", RefType: "receipt", RefID: 9502, BookDate: "2026-10-06", UnitCost: 300},
	} {
		op.ActorID, op.At = 1, at
		if err := inv.AppendLedger(context.Background(), e.db, op); err != nil {
			t.Fatalf("원장 %d: %v", i, err)
		}
	}
	ms := e.monthStock()
	f := domain.MonthStockFilter{Month: "2026-10"}

	one, err := ms.Report(inEntity(uFinance, contract.DefaultEntityID), f)
	if err != nil || len(one.Rows) != 1 || one.Rows[0].WarehouseID != 1 || one.Rows[0].CloseQty != 10 || one.Rows[0].AvgCost != 100 || one.AllEntities {
		t.Fatalf("기본 법인: %+v %v", one, err)
	}
	two, err := ms.Report(inEntity(uFinance, 2), f)
	if err != nil || len(two.Rows) != 1 || two.Rows[0].WarehouseID != 95 || two.Rows[0].WarehouseName != "계열사 창고" || two.Rows[0].AvgCost != 300 || two.Total.CloseAmount != 1500 {
		t.Fatalf("ABC: %+v %v", two, err)
	}
	all, err := ms.Report(inEntity(uFinance, 0), f)
	if err != nil || !all.AllEntities || len(all.Rows) != 2 || len(all.EntityTotals) != 2 {
		t.Fatalf("전체 모드: %+v %v", all, err)
	}
	for _, r := range all.Rows {
		if r.AvgCost != 0 || r.EntityName == "" {
			t.Fatalf("전체 모드 줄은 법인 이름이 있고 단가가 비어야 합니다: %+v", r)
		}
	}
	if all.Total.CloseQty != 15 || all.Total.CloseAmount != 1000+1500 {
		t.Fatalf("전체 합계: %+v", all.Total)
	}
	// 창고 조건이 한 법인의 창고를 가리키면 다른 법인 쪽은 빈 줄이고 오류가 아니다.
	byWh, err := ms.Report(inEntity(uFinance, 0), domain.MonthStockFilter{Month: "2026-10", WarehouseID: 95})
	if err != nil || len(byWh.Rows) != 1 || byWh.Rows[0].WarehouseID != 95 {
		t.Fatalf("전체 모드 창고 조건: %+v %v", byWh, err)
	}
	// 그룹 범위가 없으면 전체 모드는 거부된다.
	p := users[uFinance]
	p.Channel = contract.ChannelWeb
	p.EntityPerms = map[contract.Permission]contract.EntitySet{contract.PermInventoryRead: contract.NewEntitySet(contract.DefaultEntityID), contract.PermInventoryValuation: contract.NewEntitySet(contract.DefaultEntityID)}
	ctx := contract.WithActiveEntity(contract.WithPrincipal(context.Background(), p), contract.ActiveEntity{All: true})
	if _, err := ms.Report(ctx, f); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("그룹 범위 없는 전체 모드: %v", err)
	}
}
