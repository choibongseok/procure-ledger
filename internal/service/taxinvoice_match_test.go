package service_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
)

// B43 2단계(erp): 매입 세금계산서 3자 대조 엔진·확정·해제. 엔진은 코드가 결정적으로 제안하고 확정은 사람이 한다.

var taxBizSeq int

// taxVendor 는 거래처를 만들고 각 가격으로 한 줄(수량 1) 발주를 전량 입고해 입고 번호들을 돌려준다.
func (e *env) taxVendor(t *testing.T, prices ...int64) (domain.Vendor, []int64) {
	t.Helper()
	taxBizSeq++
	biz := fmt.Sprintf("888-88-%05d", taxBizSeq)
	v := e.poVendor(t, fmt.Sprintf("대조상사%d", taxBizSeq), biz)
	var rcs []int64
	for _, p := range prices {
		po := e.orderFor(t, v, "", p, 1)
		rc, err := e.inventory.Receive(as(uFinance), recv(po.ID, po.Lines[0].ID, 1, "", ""))
		if err != nil {
			t.Fatal(err)
		}
		rcs = append(rcs, rc.ID)
	}
	return v, rcs
}

var approvalSeq int

// taxPurchase 는 거래처 v 가 보낸 매입 세금계산서를 수기로 등록한다(등록 직후 대조 제안이 만들어진다).
func (e *env) taxPurchase(t *testing.T, v domain.Vendor, supply int64, date string) domain.TaxInvoice {
	t.Helper()
	approvalSeq++
	inv, err := e.tax.RegisterReceived(as(uFinance), domain.TaxReceivedInput{ApprovalNo: fmt.Sprintf("20261110MATCH%011d", approvalSeq), IssueDate: date,
		Supplier: domain.TaxParty{BizNo: digitsForTest(v.BusinessNo), Name: v.Name, Rep: "대표"}, SupplyTotal: supply, TaxTotal: supply / 10})
	if err != nil {
		t.Fatal(err)
	}
	return inv
}

func digitsForTest(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func (e *env) matchFor(t *testing.T, invoiceNo string, status string) domain.TaxMatch {
	t.Helper()
	page, err := e.tax.ListMatches(as(uFinance), domain.TaxMatchFilter{Status: status})
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range page.Rows {
		if m.InvoiceNo == invoiceNo {
			return m
		}
	}
	t.Fatalf("%s 의 %s 대조가 없다: %+v", invoiceNo, status, page.Rows)
	return domain.TaxMatch{}
}

func TestTaxMatchSingleReceiptExactAndConfirm(t *testing.T) {
	e := newEnv(t)
	e.taxCompany(t)
	v, rcs := e.taxVendor(t, 100_000, 40_000)
	inv := e.taxPurchase(t, v, 100_000, "2026-11-10")
	m := e.matchFor(t, inv.No, "proposed")
	if m.Result != domain.MatchResultMatch || len(m.Receipts) != 1 || m.Receipts[0].ReceiptID != rcs[0] || m.Diff.Diff != 0 || len(m.Warnings) != 0 || !m.Confirmable || !m.Bulk {
		t.Fatalf("정확히 일치하는 입고 하나: %+v", m)
	}
	// 두 번 제안해도 같은 제안 한 건이다(멱등).
	if _, err := e.tax.Propose(as(uFinance)); err != nil {
		t.Fatal(err)
	}
	if page, _ := e.tax.ListMatches(as(uFinance), domain.TaxMatchFilter{Status: "proposed"}); page.Total != 1 {
		t.Fatalf("제안은 한 건: %d", page.Total)
	}
	// 직원은 제안을 볼 수도 확정할 수도 없고, 감사는 볼 수만 있다.
	if _, err := e.tax.ListMatches(as(uStaff), domain.TaxMatchFilter{}); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("직원 조회: %v", err)
	}
	if _, err := e.tax.ListMatches(as(uAuditor), domain.TaxMatchFilter{}); err != nil {
		t.Fatalf("감사 조회: %v", err)
	}
	if _, err := e.tax.Confirm(as(uAuditor), m.ID, ""); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("감사 확정: %v", err)
	}
	got, err := e.tax.Confirm(as(uFinance), m.ID, "확인")
	if err != nil || got.Status != domain.MatchConfirmed || got.Comment != "확인" {
		t.Fatalf("확정: %v %+v", err, got)
	}
	var flagged int
	if err := e.db.QueryRow(`SELECT COUNT(*) FROM tax_invoice_match_receipts WHERE match_id = ? AND confirmed = 1`, m.ID).Scan(&flagged); err != nil || flagged != 1 {
		t.Fatalf("확정 표시: %d %v", flagged, err)
	}
	if !e.rec.Has("taxinvoice.match_confirm", "tax_invoice", "success") {
		t.Error("확정 감사")
	}
	// 확정된 세금계산서는 대조 대상이 아니고(제안 없음), 상세에 연결됨이 보인다.
	if d, _ := e.tax.Get(as(uFinance), inv.ID); d.MatchStatus != "연결됨" || d.MatchResult != "일치" {
		t.Fatalf("상세의 대조 상태: %+v", d)
	}
	// 같은 입고에 대한 두 번째 세금계산서는 그 입고를 후보로 삼지 않는다(남은 40,000 입고만).
	inv2 := e.taxPurchase(t, v, 100_000, "2026-11-10")
	if m2 := e.matchFor(t, inv2.No, "proposed"); m2.Result != domain.MatchResultAmountDiff || m2.Diff.CandidatesSum != 40_000 {
		t.Fatalf("연결된 입고는 후보에서 빠진다: %+v", m2)
	}
	// DB 유일 색인: 같은 입고를 다른 확정에 넣을 수 없다.
	if _, err := e.db.Exec(`INSERT INTO tax_invoice_matches (invoice_id, status, result, proposed_at) VALUES (?, 'proposed', 'match', 'x')`, inv2.ID); err == nil {
		t.Log("제안이 이미 있어 유일 색인이 막는다")
	}
}

func TestTaxMatchMultiReceiptCombinationAndAmbiguity(t *testing.T) {
	e := newEnv(t)
	e.taxCompany(t)
	// 30,000 + 70,000 = 100,000 이 유일한 조합.
	v, rcs := e.taxVendor(t, 30_000, 70_000, 55_000)
	inv := e.taxPurchase(t, v, 100_000, "2026-11-10")
	m := e.matchFor(t, inv.No, "proposed")
	if m.Result != domain.MatchResultMatch || len(m.Receipts) != 2 || m.Receipts[0].ReceiptID != rcs[0] || m.Receipts[1].ReceiptID != rcs[1] {
		t.Fatalf("다건 조합: %+v", m)
	}
	// 같은 합을 만드는 조합이 둘이면 ambiguous: 제안은 가장 작고 오래된 조합이고 후보 수가 보인다.
	v2, rc2 := e.taxVendor(t, 50_000, 50_000)
	inv2 := e.taxPurchase(t, v2, 50_000, "2026-11-10")
	m2 := e.matchFor(t, inv2.No, "proposed")
	if m2.Result != domain.MatchResultAmbiguous || m2.Diff.Alternatives != 1 || len(m2.Receipts) != 1 || m2.Receipts[0].ReceiptID != rc2[0] || !m2.Confirmable || m2.Bulk {
		t.Fatalf("후보가 여럿: %+v", m2)
	}
	// 확정하면 오래된 입고가 연결되고, 같은 값 입고가 남아 있으므로 다음 세금계산서는 그것과 일치한다.
	if _, err := e.tax.Confirm(as(uFinance), m2.ID, ""); err != nil {
		t.Fatal(err)
	}
	inv3 := e.taxPurchase(t, v2, 50_000, "2026-11-10")
	if m3 := e.matchFor(t, inv3.No, "proposed"); m3.Result != domain.MatchResultMatch || m3.Receipts[0].ReceiptID != rc2[1] {
		t.Fatalf("남은 입고: %+v", m3)
	}
}

func TestTaxMatchNoSilentFixAmountDiffVendorUnknownNoReceipt(t *testing.T) {
	e := newEnv(t)
	e.taxCompany(t)
	v, _ := e.taxVendor(t, 60_000)
	// 금액이 맞지 않으면 amount_diff, 가장 가까운 조합과 차액이 보이고 확정은 거부된다(D9: 공급가액은 정확히 일치).
	inv := e.taxPurchase(t, v, 90_000, "2026-11-10")
	m := e.matchFor(t, inv.No, "proposed")
	if m.Result != domain.MatchResultAmountDiff || m.Diff.Diff != -30_000 || m.Confirmable {
		t.Fatalf("금액 불일치: %+v", m)
	}
	if _, err := e.tax.Confirm(as(uFinance), m.ID, ""); !errors.Is(err, contract.ErrInvalidState) {
		t.Fatalf("금액 불일치 확정: %v", err)
	}
	// 등록되지 않은 거래처.
	unk, err := e.tax.RegisterReceived(as(uFinance), domain.TaxReceivedInput{ApprovalNo: "20261110UNKNOWN000000001", IssueDate: "2026-11-10",
		Supplier: domain.TaxParty{BizNo: "9998887776", Name: "모르는상사", Rep: "x"}, SupplyTotal: 1000, TaxTotal: 100})
	if err != nil {
		t.Fatal(err)
	}
	if m := e.matchFor(t, unk.No, "proposed"); m.Result != domain.MatchResultVendorUnknown || len(m.Receipts) != 0 || m.Confirmable {
		t.Fatalf("거래처 미등록: %+v", m)
	}
	// 입고가 아직 없는 거래처.
	taxBizSeq++
	v3 := e.poVendor(t, "입고없음상사", fmt.Sprintf("888-88-%05d", 90000+taxBizSeq))
	inv3 := e.taxPurchase(t, v3, 5000, "2026-11-10")
	if m := e.matchFor(t, inv3.No, "proposed"); m.Result != domain.MatchResultNoReceipt {
		t.Fatalf("입고 없음: %+v", m)
	}
}

func TestTaxMatchWarningsDoNotChangeResult(t *testing.T) {
	e := newEnv(t)
	e.taxCompany(t)
	v, _ := e.taxVendor(t, 100_000)
	// 세액이 계산값과 다르고(15,000) 작성일의 달(10월)이 입고일의 달(11월)과 다르다. 공급자 상호도 다르다.
	approvalSeq++
	inv, err := e.tax.RegisterReceived(as(uFinance), domain.TaxReceivedInput{ApprovalNo: fmt.Sprintf("20261031WARN%012d", approvalSeq), IssueDate: "2026-10-31",
		Supplier: domain.TaxParty{BizNo: digitsForTest(v.BusinessNo), Name: "전혀다른회사", Rep: "x"}, SupplyTotal: 100_000, TaxTotal: 15_000})
	if err != nil {
		t.Fatal(err)
	}
	m := e.matchFor(t, inv.No, "proposed")
	want := map[string]bool{domain.MatchWarnTaxDiff: true, domain.MatchWarnDateGap: true, domain.MatchWarnVendorName: true}
	if m.Result != domain.MatchResultMatch || len(m.Warnings) != 3 || !m.Confirmable || m.Bulk {
		t.Fatalf("경고가 있어도 결과는 일치이고 일괄 확정 대상은 아니다: %+v", m)
	}
	for _, w := range m.Warnings {
		if !want[w] {
			t.Errorf("뜻밖의 경고: %s", w)
		}
	}
}

func TestTaxMatchDismissNeverReproposesSameCombinationAndUnlink(t *testing.T) {
	e := newEnv(t)
	e.taxCompany(t)
	v, _ := e.taxVendor(t, 100_000)
	inv := e.taxPurchase(t, v, 100_000, "2026-11-10")
	m := e.matchFor(t, inv.No, "proposed")
	if _, err := e.tax.Dismiss(as(uFinance), m.ID, ""); !errors.Is(err, contract.ErrValidation) {
		t.Fatalf("사유 필수: %v", err)
	}
	if got, err := e.tax.Dismiss(as(uFinance), m.ID, "다른 거래입니다"); err != nil || got.Status != domain.MatchDismissed {
		t.Fatalf("기각: %v %+v", err, got)
	}
	e.tax.Propose(as(uFinance))
	if page, _ := e.tax.ListMatches(as(uFinance), domain.TaxMatchFilter{Status: "proposed"}); page.Total != 0 {
		t.Fatalf("기각한 조합은 다시 제안하지 않는다: %d", page.Total)
	}
	if _, err := e.tax.Confirm(as(uFinance), m.ID, ""); !errors.Is(err, contract.ErrInvalidState) {
		t.Fatalf("기각된 대조 확정: %v", err)
	}
	// 새 입고가 생겨 다른 조합이 가능하면 새로 제안한다. 확정 → 해제 → 다시 제안.
	v2, _ := e.taxVendor(t, 70_000)
	inv2 := e.taxPurchase(t, v2, 70_000, "2026-11-10")
	m2 := e.matchFor(t, inv2.No, "proposed")
	if _, err := e.tax.Confirm(as(uFinance), m2.ID, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := e.tax.Unlink(as(uFinance), m2.ID, ""); !errors.Is(err, contract.ErrValidation) {
		t.Fatalf("해제 사유 필수: %v", err)
	}
	if got, err := e.tax.Unlink(as(uFinance), m2.ID, "잘못 연결"); err != nil || got.Status != domain.MatchUnlinked {
		t.Fatalf("해제: %v %+v", err, got)
	}
	var flagged int
	_ = e.db.QueryRow(`SELECT COUNT(*) FROM tax_invoice_match_receipts WHERE match_id = ? AND confirmed = 1`, m2.ID).Scan(&flagged)
	if flagged != 0 {
		t.Fatal("해제하면 확정 표시가 풀린다")
	}
	e.tax.Propose(as(uFinance))
	if again := e.matchFor(t, inv2.No, "proposed"); again.Result != domain.MatchResultMatch {
		t.Fatalf("해제 뒤 다시 제안: %+v", again)
	}
	// 확정·기각·해제의 감사가 남는다.
	for _, a := range []string{"taxinvoice.match_dismiss", "taxinvoice.match_confirm", "taxinvoice.match_unlink"} {
		if !e.rec.Has(a, "tax_invoice", "success") {
			t.Errorf("감사 %s", a)
		}
	}
	// DB: 확정·기각 뒤에는 결과 칸과 입고 연결을 고칠 수 없다.
	if _, err := e.db.Exec(`UPDATE tax_invoice_matches SET result = 'no_receipt' WHERE id = ?`, m.ID); err == nil {
		t.Fatal("기각된 대조의 결과를 고칠 수 있다")
	}
	if _, err := e.db.Exec(`DELETE FROM tax_invoice_match_receipts WHERE match_id = ?`, m.ID); err == nil {
		t.Fatal("기각된 대조의 입고 연결을 지울 수 있다")
	}
	if _, err := e.db.Exec(`DELETE FROM tax_invoice_matches WHERE id = ?`, m.ID); err == nil {
		t.Fatal("대조 기록을 지울 수 있다")
	}
}

// 한 입고를 두 세금계산서가 확정하려 하면 둘째가 거부된다(확정된 연결은 입고당 하나).
func TestTaxMatchReceiptCanBeConfirmedOnlyOnce(t *testing.T) {
	e := newEnv(t)
	e.taxCompany(t)
	v, _ := e.taxVendor(t, 100_000)
	a := e.taxPurchase(t, v, 100_000, "2026-11-10")
	b := e.taxPurchase(t, v, 100_000, "2026-11-10") // 첫째가 아직 확정 전이라 같은 입고를 후보로 제안받는다
	ma, mb := e.matchFor(t, a.No, "proposed"), e.matchFor(t, b.No, "proposed")
	if ma.Receipts[0].ReceiptID != mb.Receipts[0].ReceiptID {
		t.Fatalf("두 제안이 같은 입고를 가리켜야 이 시험이 성립한다: %+v %+v", ma.Receipts, mb.Receipts)
	}
	if _, err := e.tax.Confirm(as(uFinance), ma.ID, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := e.tax.Confirm(as(uFinance), mb.ID, ""); !errors.Is(err, contract.ErrInvalidState) {
		t.Fatalf("둘째 확정은 거부: %v", err)
	}
	if got := e.matchFor(t, b.No, "proposed"); got.Status != domain.MatchProposed {
		t.Fatal("거부된 확정은 제안을 그대로 둔다")
	}
}

func TestTaxMatchBulkConfirmOnlyCleanMatches(t *testing.T) {
	e := newEnv(t)
	e.taxCompany(t)
	var clean []int64
	for i := 0; i < 3; i++ {
		v, _ := e.taxVendor(t, int64(10_000*(i+1)))
		inv := e.taxPurchase(t, v, int64(10_000*(i+1)), "2026-11-10")
		clean = append(clean, e.matchFor(t, inv.No, "proposed").ID)
	}
	vd, _ := e.taxVendor(t, 99_000)
	diff := e.taxPurchase(t, vd, 50_000, "2026-11-10") // 금액 불일치
	vw, _ := e.taxVendor(t, 80_000)
	approvalSeq++
	warn, err := e.tax.RegisterReceived(as(uFinance), domain.TaxReceivedInput{ApprovalNo: fmt.Sprintf("20261110BULKW%011d", approvalSeq), IssueDate: "2026-11-10",
		Supplier: domain.TaxParty{BizNo: digitsForTest(vw.BusinessNo), Name: vw.Name, Rep: "x"}, SupplyTotal: 80_000, TaxTotal: 1}) // 세액 경고
	if err != nil {
		t.Fatal(err)
	}
	ids := append(append([]int64{}, clean...), e.matchFor(t, diff.No, "proposed").ID, e.matchFor(t, warn.No, "proposed").ID)
	res, err := e.tax.ConfirmBulk(as(uFinance), ids)
	if err != nil || res.Confirmed != 3 || res.Skipped != 2 {
		t.Fatalf("일괄 확정: %v %+v", err, res)
	}
	if _, err := e.tax.ConfirmBulk(as(uFinance), nil); !errors.Is(err, contract.ErrValidation) {
		t.Fatalf("빈 목록: %v", err)
	}
	if _, err := e.tax.ConfirmBulk(as(uStaff), ids); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("직원 일괄 확정: %v", err)
	}
}

func TestTaxMatchSkipsAmendmentsAndSyncRegistersProposal(t *testing.T) {
	e := newEnv(t)
	e.taxCompany(t)
	v, _ := e.taxVendor(t, 100_000)
	approvalSeq++
	am, err := e.tax.RegisterReceived(as(uFinance), domain.TaxReceivedInput{ApprovalNo: fmt.Sprintf("20261110AMEND%011d", approvalSeq), IssueDate: "2026-11-10", AmendReason: domain.AmendReturn,
		Supplier: domain.TaxParty{BizNo: digitsForTest(v.BusinessNo), Name: v.Name, Rep: "x"}, SupplyTotal: -10_000, TaxTotal: -1_000})
	if err != nil {
		t.Fatal(err)
	}
	page, _ := e.tax.ListMatches(as(uFinance), domain.TaxMatchFilter{})
	for _, m := range page.Rows {
		if m.InvoiceNo == am.No {
			t.Fatalf("수정 건은 입고와 대조하지 않는다: %+v", m)
		}
	}
	// 수신 동기화가 새 건을 넣으면 같은 트랜잭션 흐름에서 대조 제안이 만들어진다.
	e.aspOwner(t, uAdmin)
	ctx := context.Background()
	items := []domain.TaxReceivedItem{{ApprovalNo: "20261110SYNCPRO000000001", TaxType: "taxable", IssueDate: "2026-11-10", Supplier: domain.TaxParty{BizNo: digitsForTest(v.BusinessNo), Name: v.Name, Rep: "x"},
		Buyer: domain.TaxParty{BizNo: "1234567890", Name: "에이엑스 주식회사"}, SupplyTotal: 100_000, TaxTotal: 10_000}}
	c, err := e.tax.ApplyReceived(ctx, items)
	if err != nil || c.Inserted != 1 {
		t.Fatalf("반영: %v %+v", err, c)
	}
	if err := e.tax.FinishSync(ctx, domain.TaxSyncState{LastDone: "2026-11-10"}, c, "ok"); err != nil {
		t.Fatal(err)
	}
	page, _ = e.tax.ListMatches(as(uFinance), domain.TaxMatchFilter{Status: "proposed"})
	if page.Total != 1 || page.Rows[0].Result != domain.MatchResultMatch {
		t.Fatalf("동기화 뒤 제안: %+v", page)
	}
}
