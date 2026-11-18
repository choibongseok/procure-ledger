package service_test

import (
	"strings"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/service"
)

// taxVerdict 는 증빙 종류가 세금계산서(tax_invoice)인 판정을 심는다.
func (e *env) taxVerdict(t *testing.T, po domain.PurchaseOrder, receiptID int64) int64 {
	t.Helper()
	id := e.verdictFor(t, po, receiptID)
	if _, err := e.db.Exec(`UPDATE documents SET kind = 'tax_invoice' WHERE id = (SELECT document_id FROM document_verdicts WHERE id = ?)`, id); err != nil {
		t.Fatal(err)
	}
	return id
}

// autoPurchase 는 입고 한 건의 자동 매입 전표를 만들어 승인한다(증빙 종류는 tax 로 고른다).
func (e *env) autoPurchase(t *testing.T, price int64, tax bool) (domain.Journal, domain.PurchaseOrder, int64) {
	t.Helper()
	po, rc := e.receivedPO(t, price, 1)
	var vid int64
	if tax {
		vid = e.taxVerdict(t, po, rc)
	} else {
		vid = e.verdictFor(t, po, rc)
	}
	out, err := e.hook(t, uFinance, matchVerdict(vid, po, rc, nil))
	if err != nil || !out.Created {
		t.Fatalf("자동 전표: %+v %v", out, err)
	}
	j, err := e.journals.Get(as(uFinance), out.JournalNo)
	if err != nil {
		t.Fatal(err)
	}
	return e.approveJV(t, j), po, rc
}

// AC-1270·1271: 승인된 전표만, 거래일 기준으로 표 1(세금계산서)·표 2(미수취)·표 3(카드 공제·불공제)이 나뉘고 1310 과 검산된다. 세금계산서를 나중에 연결하면 표 2 에서 표 1 로 옮겨 간다.
func TestVATPurchaseSummaryTablesAndLedgerCheck(t *testing.T) {
	e := newEnv(t)
	e.jvForm(t)
	e.card(t)
	vat := service.NewVATService(e.journals.Base)
	a, _, _ := e.autoPurchase(t, 10_000, true) // 공급가 10,000 부가세 1,000
	b, poB, rcB := e.autoPurchase(t, 20_000, false)
	// 승인 전 전표는 세지 않고 제외 건수로 안내한다.
	po3, rc3 := e.receivedPO(t, 5_000, 1)
	if out, err := e.hook(t, uFinance, matchVerdict(e.taxVerdict(t, po3, rc3), po3, rc3, nil)); err != nil || !out.Created {
		t.Fatalf("임시 전표: %+v %v", out, err)
	}
	// 카드: 공제(5220) 12,500원, 불공제(접대비) 66,000원. 확인된 비용 전표를 승인한다.
	e.importCards(t, cardRow("2026-11-08 12:00:00", "스타벅스", 12_500, "A1", "승인")+cardRow("2026-11-08 13:00:00", "회식", 66_000, "A2", "승인"))
	t1, t2 := e.txnByApproval(t, "A1", "approve"), e.txnByApproval(t, "A2", "approve")
	e.explainOK(t, t1, "5220", true)
	e.explainOK(t, t2, "5250", false, e.receipt(t))
	res, _ := e.cards.Confirm(as(uLead), []int64{t1.ID, t2.ID})
	for _, r := range res {
		if !r.OK {
			t.Fatalf("확인: %+v", res)
		}
		j, _ := e.journals.Get(as(uFinance), r.JournalNo)
		e.approveJV(t, j)
	}
	sum, err := vat.PurchaseSummary(as(uAuditor), domain.VATFilter{Unit: "month", Month: "2026-11"})
	if err != nil {
		t.Fatal(err)
	}
	if len(sum.Vendors) != 1 || sum.Vendors[0].VAT != 1_000 || sum.Vendors[0].Supply != 10_000 || sum.Vendors[0].Count != 1 || sum.Vendors[0].BusinessNo == "" {
		t.Fatalf("표 1: %+v", sum.Vendors)
	}
	if len(sum.Unreceived) != 1 || sum.Unreceived[0].JournalNo != b.No || sum.Unreceived[0].VAT != 2_000 || sum.Unreceived[0].Supply != 20_000 {
		t.Fatalf("표 2: %+v", sum.Unreceived)
	}
	if len(sum.Cards) != 2 || sum.Cards[0].Kind != "deductible" || sum.Cards[0].VAT != 1_136 || sum.Cards[1].Kind != "nondeductible" || sum.Cards[1].ReasonLabel != "접대비" || sum.Cards[1].Total != 66_000 {
		t.Fatalf("표 3: %+v", sum.Cards)
	}
	if sum.DeductibleVAT != 2_136 || sum.UnreceivedVAT != 2_000 || sum.LedgerVAT != 4_136 || sum.Difference != 2_000 || sum.Matches || sum.ExcludedCount != 1 {
		t.Fatalf("합계·검산: %+v", sum)
	}
	if len(sum.Hints) == 0 || !strings.Contains(sum.Hints[0], "세금계산서를 받지 못한") {
		t.Fatalf("차이 원인 힌트: %v", sum.Hints)
	}
	_ = a
	// 세금계산서를 나중에 연결하면(같은 입고의 두 번째 증빙) 다음 조회부터 표 1 로 옮겨 가고 검산이 맞는다.
	if out, err := e.hook(t, uFinance, matchVerdict(e.taxVerdict(t, poB, rcB), poB, rcB, nil)); err != nil || !out.Attached {
		t.Fatalf("증빙 연결: %+v %v", out, err)
	}
	after, _ := vat.PurchaseSummary(as(uFinance), domain.VATFilter{Unit: "month", Month: "2026-11"})
	if len(after.Vendors) != 2 || len(after.Unreceived) != 0 || after.DeductibleVAT != 4_136 || !after.Matches || len(after.Hints) != 0 {
		t.Fatalf("연결 뒤: %+v", after)
	}
	// 다른 달·분기·사용자 지정 기간.
	if other, _ := vat.PurchaseSummary(as(uFinance), domain.VATFilter{Unit: "month", Month: "2026-10"}); len(other.Vendors) != 0 || other.LedgerVAT != 0 || !other.Matches {
		t.Fatalf("10월: %+v", other)
	}
	q4, err := vat.PurchaseSummary(as(uFinance), domain.VATFilter{Unit: "quarter", Year: 2026, Quarter: 4})
	if err != nil || q4.From != "2026-10-01" || q4.To != "2026-12-31" || q4.DeductibleVAT != 4_136 {
		t.Fatalf("4분기: %+v %v", q4, err)
	}
	if _, err := vat.PurchaseSummary(as(uFinance), domain.VATFilter{Unit: "custom", From: "2025-01-01", To: "2026-11-30"}); !isErr(err, contract.ErrValidation) {
		t.Fatalf("12개월 초과: %v", err)
	}
	if _, err := vat.PurchaseSummary(as(uFinance), domain.VATFilter{Unit: "quarter", Year: 2026, Quarter: 5}); !isErr(err, contract.ErrValidation) {
		t.Fatalf("분기 5: %v", err)
	}
	for _, who := range []int64{uStaff, uLead} {
		if _, err := vat.PurchaseSummary(as(who), domain.VATFilter{}); !isErr(err, contract.ErrForbidden) {
			t.Errorf("%d 부가세 집계: %v", who, err)
		}
	}
	if _, err := vat.PurchaseSummary(as(uAdmin), domain.VATFilter{}); err != nil {
		t.Errorf("관리자 조회: %v", err)
	}
}
