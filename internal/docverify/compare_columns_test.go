package docverify

import (
	"testing"

	"github.com/choibongseok/procure-ledger/internal/contract/domain"
)

func colsInput(withReceipt bool) CompareInput {
	in := CompareInput{
		Extraction: domain.Extraction{BusinessNo: "123-45-67890", TotalAmount: 3000 * 30,
			Lines: []domain.ExtractedLine{{Name: "복사용지", Quantity: 30, UnitPrice: 3000, Amount: 90000}}},
		PO:               domain.PurchaseOrder{Lines: []domain.POLine{{ID: 1, ItemName: "복사용지", Quantity: 100, UnitPrice: 3000}}},
		VendorBusinessNo: "123-45-67890",
	}
	if withReceipt {
		in.Receipt = &domain.Receipt{Lines: []domain.ReceiptLine{{POLineID: 1, Quantity: 30}}}
	}
	return in
}

func item(t *testing.T, items []domain.VerdictItem, field string) domain.VerdictItem {
	t.Helper()
	for _, it := range items {
		if it.Field == field {
			return it
		}
	}
	t.Fatalf("항목 없음: %s", field)
	return domain.VerdictItem{}
}

func TestCompareFillsPOReceiptDocumentColumns(t *testing.T) {
	out, items := Compare(colsInput(true))
	if out != domain.OutcomeMatch {
		t.Fatalf("일치여야 함: %s %+v", out, items)
	}
	bn := item(t, items, "business_no")
	if bn.PO != "-" || bn.Receipt != "-" || bn.Actual != "123-45-67890" {
		t.Fatalf("사업자번호 열: %+v", bn)
	}
	q := item(t, items, "line[0].quantity")
	if q.PO != "100" || q.Receipt != "30" || q.Actual != "30" || q.Expected != "30" {
		t.Fatalf("수량 열: %+v", q)
	}
	p := item(t, items, "line[0].unit_price")
	if p.PO != "3000" || p.Receipt != "-" || p.Actual != "3000" {
		t.Fatalf("단가 열(입고 열 '-'): %+v", p)
	}
	tot := item(t, items, "total_amount")
	if tot.PO != "300000" || tot.Receipt != "90000" || tot.Actual != "90000" {
		t.Fatalf("합계 열(발주 전체·입고 기준·문서): %+v", tot)
	}
}

func TestCompareWithoutReceiptLeavesReceiptColumnDash(t *testing.T) {
	in := colsInput(false)
	in.Extraction.Lines[0].Quantity = 100
	in.Extraction.Lines[0].Amount = 300000
	in.Extraction.TotalAmount = 300000
	out, items := Compare(in)
	if out != domain.OutcomeMatch {
		t.Fatalf("일치여야 함: %s %+v", out, items)
	}
	for _, it := range items {
		if it.Receipt != "-" || it.PO == "" {
			t.Fatalf("입고 없는 판정은 입고 열이 전부 '-' 여야 함: %+v", it)
		}
	}
	if q := item(t, items, "line[0].quantity"); q.PO != "100" || q.Actual != "100" {
		t.Fatalf("수량: %+v", q)
	}
}
