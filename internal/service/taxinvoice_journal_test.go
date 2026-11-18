package service_test

import (
	"fmt"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/service"
)

// AC-4320(D7): 설정을 켜면 확정 시 입고당 임시 매입 전표가 한 번만 생기고, 끄면 생기지 않는다. 이미 전표가 있는 입고는 연결만 한다.

func (e *env) invoiceForReceipt(t *testing.T, poVendorID int64, supply int64) domain.TaxInvoice {
	t.Helper()
	var biz string
	if err := e.db.QueryRow(`SELECT business_no FROM vendors WHERE id = ?`, poVendorID).Scan(&biz); err != nil {
		t.Fatal(err)
	}
	approvalSeq++
	inv, err := e.tax.RegisterReceived(as(uFinance), domain.TaxReceivedInput{ApprovalNo: fmt.Sprintf("20261110AUTOJ%011d", approvalSeq), IssueDate: "2026-11-10",
		Supplier: domain.TaxParty{BizNo: digitsForTest(biz), Name: "공급사", Rep: "x"}, SupplyTotal: supply, TaxTotal: supply / 10})
	if err != nil {
		t.Fatal(err)
	}
	return inv
}

func (e *env) countTaxJournals(t *testing.T) (n int, status string, total int64) {
	t.Helper()
	if err := e.db.QueryRow(`SELECT COUNT(*), COALESCE(MAX(status),''), COALESCE(MAX(total_amount),0) FROM journals WHERE source_type = 'tax_invoice'`).Scan(&n, &status, &total); err != nil {
		t.Fatal(err)
	}
	return
}

func TestTaxMatchAutoJournalOffByDefaultAndOnCreatesOncePerReceipt(t *testing.T) {
	e := newEnv(t)
	e.jvForm(t)
	e.taxCompany(t)
	po, _ := e.receivedPO(t, 20_000, 1)
	inv := e.invoiceForReceipt(t, po.VendorID, 20_000)
	m := e.matchFor(t, inv.No, "proposed")
	if _, err := e.tax.Confirm(as(uFinance), m.ID, ""); err != nil {
		t.Fatal(err)
	}
	if n, _, _ := e.countTaxJournals(t); n != 0 {
		t.Fatalf("기본은 꺼짐: 전표 %d건", n)
	}
	// 해제 후 설정을 켜고 다시 확정하면 전표가 한 번 생긴다(임시, 출처 tax_invoice).
	if _, err := e.tax.Unlink(as(uFinance), m.ID, "시험"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.Exec(`INSERT INTO app_settings (key, value, updated_at) VALUES ('taxinvoice.auto_journal', 'on', '2026-11-13T00:00:00Z') ON CONFLICT (key) DO UPDATE SET value = excluded.value`); err != nil {
		t.Fatal(err)
	}
	if _, err := e.tax.Propose(as(uFinance)); err != nil {
		t.Fatal(err)
	}
	m2 := e.matchFor(t, inv.No, "proposed")
	if _, err := e.tax.Confirm(as(uFinance), m2.ID, ""); err != nil {
		t.Fatal(err)
	}
	n, status, total := e.countTaxJournals(t)
	if n != 1 || status != "draft" || total != 22_000 {
		t.Fatalf("자동 전표: %d건 %s %d", n, status, total)
	}
	var linked int
	if err := e.db.QueryRow(`SELECT COUNT(*) FROM tax_invoice_match_receipts WHERE match_id = ? AND journal_id IS NOT NULL AND confirmed = 1`, m2.ID).Scan(&linked); err != nil || linked != 1 {
		t.Fatalf("전표 연결: %d %v", linked, err)
	}
	// 해제 → 다시 확정해도 같은 입고에는 전표가 새로 생기지 않고 기존 전표에 연결된다.
	if _, err := e.tax.Unlink(as(uFinance), m2.ID, "다시"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.tax.Propose(as(uFinance)); err != nil {
		t.Fatal(err)
	}
	m3 := e.matchFor(t, inv.No, "proposed")
	if _, err := e.tax.Confirm(as(uFinance), m3.ID, ""); err != nil {
		t.Fatal(err)
	}
	if n, _, _ := e.countTaxJournals(t); n != 1 {
		t.Fatalf("한 입고 한 전표: %d건", n)
	}
	// 연결된 전표가 승인되면 부가세 표 1 에 들어간다(전표 값).
	var no string
	if err := e.db.QueryRow(`SELECT no FROM journals WHERE source_type = 'tax_invoice'`).Scan(&no); err != nil {
		t.Fatal(err)
	}
	j, err := e.journals.Get(as(uFinance), no)
	if err != nil {
		t.Fatal(err)
	}
	e.approveJV(t, j)
	sum, err := service.NewVATService(e.journals.Base).PurchaseSummary(as(uFinance), domain.VATFilter{Unit: "month", Month: j.TradeDate[:7]})
	if err != nil || len(sum.Vendors) != 1 || sum.Vendors[0].VAT != 2_000 || len(sum.Unreceived) != 0 {
		t.Fatalf("표 1: %+v %v", sum, err)
	}
}
