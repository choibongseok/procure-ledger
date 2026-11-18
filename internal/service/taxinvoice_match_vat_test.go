package service_test

import (
	"fmt"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/service"
)

// D10: 확정된 연결은 표 1 의 "세금계산서 수취" 조건을 채운다(금액은 전표 값). 연결되지 않은 수신분은 표 4, 세액이 전표와 다르면 차이 목록에 나온다.
func TestTaxMatchFeedsVATTablesAndDiffList(t *testing.T) {
	e := newEnv(t)
	e.jvForm(t)
	e.taxCompany(t)
	vat := service.NewVATService(e.journals.Base)
	_, po, _ := e.autoPurchase(t, 20_000, false) // 증빙 종류가 세금계산서가 아닌 거래명세서 → 표 2(미수취)
	var biz string
	if err := e.db.QueryRow(`SELECT business_no FROM vendors WHERE id = ?`, po.VendorID).Scan(&biz); err != nil {
		t.Fatal(err)
	}
	f := domain.VATFilter{Unit: "month", Month: "2026-11"}
	before, err := vat.PurchaseSummary(as(uFinance), f)
	if err != nil || len(before.Vendors) != 0 || len(before.Unreceived) != 1 || len(before.Unlinked) != 0 {
		t.Fatalf("연결 전: %+v %v", before, err)
	}
	approvalSeq++
	inv, err := e.tax.RegisterReceived(as(uFinance), domain.TaxReceivedInput{ApprovalNo: fmt.Sprintf("20261110VAT%013d", approvalSeq), IssueDate: "2026-11-10",
		Supplier: domain.TaxParty{BizNo: digitsForTest(biz), Name: "공급사", Rep: "x"}, SupplyTotal: 20_000, TaxTotal: 1_500})
	if err != nil {
		t.Fatal(err)
	}
	mid, _ := vat.PurchaseSummary(as(uFinance), f)
	if len(mid.Unlinked) != 1 || mid.Unlinked[0].InvoiceNo != inv.No || mid.Unlinked[0].VAT != 1_500 || mid.UnlinkedVAT != 1_500 || len(mid.Vendors) != 0 {
		t.Fatalf("확정 전 표 4: %+v", mid.Unlinked)
	}
	m := e.matchFor(t, inv.No, "proposed")
	if _, err := e.tax.Confirm(as(uFinance), m.ID, ""); err != nil {
		t.Fatal(err)
	}
	after, _ := vat.PurchaseSummary(as(uFinance), f)
	if len(after.Vendors) != 1 || after.Vendors[0].VAT != 2_000 || after.Vendors[0].Supply != 20_000 || len(after.Unreceived) != 0 || len(after.Unlinked) != 0 {
		t.Fatalf("확정 뒤 표 1 은 전표 값: %+v", after)
	}
	if len(after.Diffs) != 1 || after.Diffs[0].InvoiceNo != inv.No || after.Diffs[0].InvoiceVAT != 1_500 || after.Diffs[0].JournalVAT != 2_000 || after.Diffs[0].Diff != 500 {
		t.Fatalf("차이 목록: %+v", after.Diffs)
	}
	// 연결을 해제하면 다시 표 2·표 4 로 돌아간다.
	if _, err := e.tax.Unlink(as(uFinance), m.ID, "시험"); err != nil {
		t.Fatal(err)
	}
	back, _ := vat.PurchaseSummary(as(uFinance), f)
	if len(back.Vendors) != 0 || len(back.Unreceived) != 1 || len(back.Unlinked) != 1 || len(back.Diffs) != 0 {
		t.Fatalf("해제 뒤: %+v", back)
	}
}
