package repository_test

import (
	"testing"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/repository"
)

// 월 마감·보고의 법인 거름 시험(B51 3단계, AC-5110·5111·5113·5115·5142). 법인 둘: 기본 법인(창고 1)과 ABC(창고 10).

func TestMonthCloseReadsAreScopedToEntity(t *testing.T) {
	ctx, db := entitySeed(t)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(appendOp(t, ctx, db, 1, 1, 10, "receipt", "receipt", 1, "2026-01-05", 100))
	must(appendOp(t, ctx, db, 10, 1, 5, "receipt", "receipt", 2, "2026-01-06", 200))
	must(appendOp(t, ctx, db, 10, 2, 3, "receipt", "receipt", 3, "2025-12-20", 50)) // ABC 에만 더 이른 기록
	must(appendOp(t, ctx, db, 10, 2, 2, "adjust", "count", 4, "2026-01-10", 0))     // 입고 이력이 있어 경고 아님
	must(appendOp(t, ctx, db, 10, 1, -1, "adjust", "count", 5, "2026-01-11", 0))
	must(appendOp(t, ctx, db, 1, 2, 4, "adjust", "count", 6, "2026-01-12", 0)) // 기본 법인: 입고 없이 조정(+) → 경고

	e1 := contract.WithActiveEntity(ctx, contract.ActiveEntity{ID: 1})
	e2 := contract.WithActiveEntity(ctx, contract.ActiveEntity{ID: 2})
	mc := repository.SQLMonthClose{}

	// 스냅샷: 그 법인 창고 줄만, 평균은 (법인, 품목) 기준(AC-5110).
	l1, err := mc.BuildSnapshot(e1, db, "2026-01-01", "2026-01-31")
	must(err)
	for _, l := range l1 {
		if l.WarehouseID != 1 || l.EntityID != 1 {
			t.Fatalf("기본 법인 스냅샷에 다른 법인 줄이 섞였습니다: %+v", l)
		}
	}
	if len(l1) != 2 || l1[0].ItemID != 1 || l1[0].CloseQty != 10 || l1[0].AvgCost != 100 || l1[0].CloseAmount != 1000 {
		t.Fatalf("기본 법인 스냅샷: %+v", l1)
	}
	l2, err := mc.BuildSnapshot(e2, db, "2026-01-01", "2026-01-31")
	must(err)
	if len(l2) != 2 || l2[0].WarehouseID != 10 || l2[0].CloseQty != 4 || l2[0].AvgCost != 200 || l2[0].CloseAmount != 800 {
		t.Fatalf("ABC 스냅샷: %+v", l2)
	}

	// 첫 기록월: 다른 법인의 더 이른 기록은 영향이 없다(AC-5111).
	if m, err := mc.EarliestLedgerMonth(e1, db); err != nil || m != "2026-01" {
		t.Fatalf("기본 법인 첫 기록월 %q %v", m, err)
	}
	if m, err := mc.EarliestLedgerMonth(e2, db); err != nil || m != "2025-12" {
		t.Fatalf("ABC 첫 기록월 %q %v", m, err)
	}

	// 평균을 몰라 0원으로 평가한 조정 건수는 그 법인 기준(AC-5113).
	if n, err := mc.WarnedAdjustments(e1, db, "2026-01"); err != nil || n != 1 {
		t.Fatalf("기본 법인 경고 %d %v", n, err)
	}
	if n, err := mc.WarnedAdjustments(e2, db, "2026-01"); err != nil || n != 0 {
		t.Fatalf("ABC 경고 %d %v", n, err)
	}

	// 품목 평가 읽기와 전체 모드의 법인별 분리(AC-5101·5144).
	inv := repository.SQLInventory{}
	if avg, amt, ok, err := inv.ItemValuation(e2, db, 1); err != nil || !ok || avg != 200 || amt != 800 {
		t.Fatalf("ABC 품목 1 평가 %d %d %v %v", avg, amt, ok, err)
	}
	all := contract.WithActiveEntity(ctx, contract.ActiveEntity{All: true})
	vals, err := inv.ItemValuations(all, db, 1)
	must(err)
	if len(vals) != 2 || vals[0].EntityID != 1 || vals[0].AvgCost != 100 || vals[1].EntityID != 2 || vals[1].AvgCost != 200 {
		t.Fatalf("법인별 평가: %+v", vals)
	}

	// 가져오기 대조: 재고 수량·금액이 현재 법인 기준, 전체 모드는 합(AC-5142).
	imp := repository.SQLImport{}
	c1, err := imp.ReconCounts(e1, db)
	must(err)
	c2, err := imp.ReconCounts(e2, db)
	must(err)
	ca, err := imp.ReconCounts(all, db)
	must(err)
	if c1.StockQty != 14 || c2.StockQty != 9 || ca.StockQty != 23 {
		t.Fatalf("대조 재고 수량 %d %d %d", c1.StockQty, c2.StockQty, ca.StockQty)
	}
	if c1.StockValue+c2.StockValue != ca.StockValue || c1.StockValue == c2.StockValue {
		t.Fatalf("대조 재고 금액 %d %d %d", c1.StockValue, c2.StockValue, ca.StockValue)
	}
}

// 법인을 나누기 전에 마감한 달의 스냅샷에 다른 법인 창고 줄이 섞여 있으면 읽을 때 거르고 제외한 줄 수를 알린다(명세 5.4, AC-5115).
func TestMixedClosedSnapshotIsFilteredOnRead(t *testing.T) {
	ctx, db := entitySeed(t)
	e1 := contract.WithActiveEntity(ctx, contract.ActiveEntity{ID: 1})
	mc := repository.SQLMonthClose{}
	lines := []repository.SnapLine{
		{ItemID: 1, WarehouseID: 1, CloseQty: 10, CloseAmount: 1000, AvgCost: 100},
		{ItemID: 1, WarehouseID: 10, CloseQty: 5, CloseAmount: 1000, AvgCost: 200}, // 옛 B44 마감: ABC 창고 줄이 기본 법인 마감에 들어갔다
	}
	if _, _, err := mc.InsertSnapshot(e1, db, "2026-01", 1, lines); err != nil {
		t.Fatal(err)
	}
	got, err := mc.SnapshotLines(e1, db, "2026-01", 1)
	if err != nil || len(got) != 1 || got[0].WarehouseID != 1 {
		t.Fatalf("읽은 줄 %+v %v", got, err)
	}
	if n, err := mc.SnapshotForeignLines(e1, db, "2026-01", 1); err != nil || n != 1 {
		t.Fatalf("제외한 줄 %d %v", n, err)
	}
}
