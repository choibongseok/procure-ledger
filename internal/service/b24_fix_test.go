package service_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
)

// 재고 화면의 창고 선택: 창고를 고르면 합계·로트·경보 판정이 그 창고 기준이고, 전체(0)는 기존 결과 그대로이며, 없는 창고는 검증 오류다.
func TestGetStockAtFiltersByWarehouse(t *testing.T) {
	e := newEnv(t)
	s := e.stock()
	w2, _ := s.wh.Save(as(uFinance), domain.WarehouseInput{Code: "WH-02", Name: "물류센터"})
	it := e.item(t, "WHF-1", 25, true)
	lot := e.seedLot(t, it.ID, "L1", "2027-12-31", 1, 30, "2026-09-10T00:00:00Z")
	if _, err := s.tr.Transfer(as(uFinance), domain.TransferInput{FromID: 1, ToID: w2.ID, Lines: []domain.TransferLineInput{{ItemID: it.ID, LotID: lot, Qty: 12}}}); err != nil {
		t.Fatal(err)
	}
	all, err := e.inventory.GetStock(as(uStaff), it.ID)
	if err != nil || all.OnHand != 30 || len(all.Lots) != 1 || all.Lots[0].Quantity != 30 || len(all.WarehouseLots) != 2 || all.BelowSafety {
		t.Fatalf("전체: %+v %v", all, err)
	}
	at, err := e.inventory.GetStockAt(as(uStaff), it.ID, 0)
	if err != nil || at.OnHand != all.OnHand || len(at.Lots) != len(all.Lots) {
		t.Fatalf("창고 0 은 전체와 같음: %+v %v", at, err)
	}
	main, err := e.inventory.GetStockAt(as(uStaff), it.ID, 1)
	if err != nil || main.OnHand != 18 || len(main.Lots) != 1 || main.Lots[0].Quantity != 18 || len(main.WarehouseLots) != 1 || main.WarehouseLots[0].WarehouseName != "본사 창고" || !main.BelowSafety {
		t.Fatalf("본사 창고(18, 안전재고 25 미만): %+v %v", main, err)
	}
	dc, _ := e.inventory.GetStockAt(as(uStaff), it.ID, w2.ID)
	if dc.OnHand != 12 || len(dc.Lots) != 1 || dc.Lots[0].LotNo != "L1" || dc.WarehouseLots[0].WarehouseName != "물류센터" || len(dc.ByWarehouse) != 2 {
		t.Fatalf("물류센터: %+v", dc)
	}
	if _, err := e.inventory.GetStockAt(as(uStaff), it.ID, 999); !errors.Is(err, contract.ErrValidation) || !strings.Contains(err.Error(), "알 수 없는 창고") {
		t.Fatalf("없는 창고: %v", err)
	}
	if _, err := e.inventory.GetStockAt(as(uAuditor), it.ID, 1); err != nil {
		t.Fatalf("재고 조회 권한이 있으면 볼 수 있음: %v", err)
	}
}

// 유효기한이 지난 로트는 출고할 수 없지만(제안·선택 차단) 창고 이동은 허용한다(폐기 전 보관 위치로 옮기는 일이 있어서).
func TestExpiredLotCanBeTransferredButNotIssued(t *testing.T) {
	e := newEnv(t)
	s := e.stock()
	w2, _ := s.wh.Save(as(uFinance), domain.WarehouseInput{Code: "WH-02", Name: "폐기 보관"})
	it := e.item(t, "EXP-LOT", 0, true)
	lot := e.seedLot(t, it.ID, "L-OLD", "2026-01-01", 1, 10, "2025-12-01T00:00:00Z")
	if _, err := s.is.Issue(as(uFinance), domain.IssueInput{DepartmentID: deptBuy, WarehouseID: 1, Lines: []domain.IssueLineInput{{ItemID: it.ID, LotID: lot, Qty: 1}}}); !errors.Is(err, contract.ErrValidation) || !strings.Contains(err.Error(), "유효기한이 지난 로트") {
		t.Fatalf("만료 로트 출고는 차단: %v", err)
	}
	if _, err := s.tr.Transfer(as(uFinance), domain.TransferInput{FromID: 1, ToID: w2.ID, Lines: []domain.TransferLineInput{{ItemID: it.ID, LotID: lot, Qty: 10}}}); err != nil {
		t.Fatalf("만료 로트 이동은 허용: %v", err)
	}
	if whQty(t, e, w2.ID, it.ID, lot) != 10 || whQty(t, e, 1, it.ID, lot) != 0 {
		t.Fatal("이동 결과가 맞지 않음")
	}
}

// 감사 diff 에는 값이 비어 있는 칸(입력하지 않은 비고·위치)이 남지 않는다.
func TestAuditDiffOmitsEmptyValues(t *testing.T) {
	e := newEnv(t)
	s := e.stock()
	if _, err := s.wh.Save(as(uFinance), domain.WarehouseInput{Code: "WH-09", Name: "빈 위치"}); err != nil {
		t.Fatal(err)
	}
	for _, r := range e.rec.Records() {
		if r.Action != "warehouse.create" {
			continue
		}
		for _, d := range r.Diff {
			if d.Old == "" && d.New == "" {
				t.Fatalf("빈 값 칸이 기록됨: %+v", d)
			}
			if d.Key == "location" {
				t.Fatal("입력하지 않은 위치가 기록됨")
			}
		}
		return
	}
	t.Fatal("창고 등록 감사 기록 없음")
}
