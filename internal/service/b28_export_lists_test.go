package service_test

import (
	"testing"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
)

// B28 erp: 입고 이력·발주·재고 현황 목록(AC-720·721·711). 열람 범위는 구매요청과 같다.
func TestExportListsScopeAndColumns(t *testing.T) {
	e := newEnv(t)
	svc := e.exportLists
	v := e.poVendor(t, "내보내기상사", "777-77-77777")
	po := e.orderFor(t, v, "2026-12-31", 1_000, 10)
	e.receiveAll(t, po)

	rs, err := svc.Receipts(as(uFinance), domain.ReceiptFilter{}, 100)
	if err != nil || len(rs) != 1 {
		t.Fatalf("입고 이력: %+v %v", rs, err)
	}
	if rs[0].PONo != po.No || rs[0].Quantity != 10 || rs[0].WarehouseName == "" || rs[0].ReceiptNo == "" {
		t.Fatalf("입고 줄 값: %+v", rs[0])
	}
	pos, err := svc.PurchaseOrders(as(uFinance), domain.POExportFilter{}, 100)
	if err != nil || len(pos) != 1 || pos[0].VendorName != "내보내기상사" || pos[0].TotalAmount != 10_000 || pos[0].DueDate != "2026-12-31" {
		t.Fatalf("발주: %+v %v", pos, err)
	}
	if pos, _ := svc.PurchaseOrders(as(uFinance), domain.POExportFilter{Status: domain.POOpen}, 100); len(pos) != 0 {
		t.Fatalf("상태 조건: %+v", pos)
	}
	st, err := svc.Stock(as(uFinance), domain.StockExportFilter{}, 100)
	if err != nil || len(st) != 1 || st[0].Quantity != 10 {
		t.Fatalf("재고 현황: %+v %v", st, err)
	}
	// 열람 범위 밖(타 부서 팀장)은 입고·발주가 비어 있다.
	if rs, err := svc.Receipts(as(uLead2), domain.ReceiptFilter{}, 100); err != nil || len(rs) != 0 {
		t.Fatalf("범위 밖 입고 이력: %+v %v", rs, err)
	}
	if pos, err := svc.PurchaseOrders(as(uLead2), domain.POExportFilter{}, 100); err != nil || len(pos) != 0 {
		t.Fatalf("범위 밖 발주: %+v %v", pos, err)
	}
	// 입고일 조건과 형식 오류.
	if rs, _ := svc.Receipts(as(uFinance), domain.ReceiptFilter{From: "2999-01-01"}, 100); len(rs) != 0 {
		t.Fatalf("기간 조건: %+v", rs)
	}
	if _, err := svc.Receipts(as(uFinance), domain.ReceiptFilter{From: "내일"}, 100); err == nil {
		t.Fatal("날짜 형식 오류가 거부되어야 함")
	}
	// 권한 없는 사용자.
	if _, err := svc.Stock(as(uStaff), domain.StockExportFilter{}, 100); err != nil && !isErr(err, contract.ErrForbidden) {
		t.Fatalf("권한: %v", err)
	}
}
