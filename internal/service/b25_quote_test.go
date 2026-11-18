package service_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/docverify"
)

// fakeQuoteReader 는 견적서 추출기 대역이다. 시험이 res/err 를 정한다(AI 호출 없음).
type fakeQuoteReader struct {
	res   docverify.QuoteResult
	err   error
	calls int
}

func (f *fakeQuoteReader) ExtractFile(_ context.Context, _, _ string, _ []byte) (docverify.QuoteResult, error) {
	f.calls++
	return f.res, f.err
}

func quoteResult(supplier, bizNo string, price int64) docverify.QuoteResult {
	return docverify.QuoteResult{Supplier: supplier, BusinessNo: bizNo,
		Lines:     []domain.ExtractedLine{{Name: "볼트 M8", Quantity: 10, UnitPrice: price, Amount: 10 * price}},
		LineTotal: 10 * price, Text: "원문"}
}

func (e *env) vendorNamed(t *testing.T, name, bizNo string) domain.Vendor {
	t.Helper()
	v, err := e.inventory.CreateVendor(as(uFinance), domain.Vendor{Name: name, BusinessNo: bizNo, Phone: "02-111-2222"})
	if err != nil {
		t.Fatal(err)
	}
	v.BusinessNo = bizNo // 서비스 응답은 마스킹되어 있어 원래 값으로 되돌린다
	return v
}

// attach 는 거래처로 연결되는 견적서를 올린다(사업자번호가 마스터와 같다).
func (e *env) attachQuote(t *testing.T, by, prID int64, v domain.Vendor, price int64) domain.Quote {
	t.Helper()
	e.quoteFake.res, e.quoteFake.err = quoteResult(v.Name, v.BusinessNo, price), nil
	q, err := e.quotes.Attach(as(by), prID, "견적.txt", "text/plain", []byte("견적서 본문"))
	if err != nil {
		t.Fatal(err)
	}
	return q
}

func TestQuoteAttachReadsAndLinksVendor(t *testing.T) {
	e := newEnv(t)
	v := e.vendorNamed(t, "별도상사", "555-55-55555")
	pr := e.draft(t, uStaff, 1_000)
	q := e.attachQuote(t, uStaff, pr.ID, v, 1_000)
	if q.Status != domain.QuoteRead || q.VendorID != v.ID || q.Total != 10_000 || !q.Filled || len(q.Lines) != 1 {
		t.Fatalf("%+v", q)
	}
	// 같은 거래처를 다시 올리면 교체(번호 그대로, 한 건).
	q2 := e.attachQuote(t, uStaff, pr.ID, v, 900)
	if q2.ID != q.ID || q2.Total != 9_000 {
		t.Fatalf("교체 %+v", q2)
	}
	if n := count(t, e.db, `SELECT COUNT(*) FROM purchase_quotes WHERE pr_id = ?`, pr.ID); n != 1 {
		t.Fatalf("견적서 %d건", n)
	}
	// 거래처를 못 찾으면 연결되지 않은 채 저장된다.
	e.quoteFake.res = quoteResult("모르는상사", "999-99-99999", 500)
	u, err := e.quotes.Attach(as(uStaff), pr.ID, "b.txt", "text/plain", []byte("x"))
	if err != nil || u.VendorID != 0 || u.Supplier != "모르는상사" {
		t.Fatalf("%+v %v", u, err)
	}
}

func TestQuoteAttachRules(t *testing.T) {
	e := newEnv(t)
	pr := e.draft(t, uStaff, 1_000)
	other := e.draft(t, uStaff2, 1_000)
	if _, err := e.quotes.Attach(as(uStaff), pr.ID, "a.docx", "", []byte("PK")); !isErr(err, contract.ErrValidation) || !strings.Contains(err.Error(), "PDF 나 텍스트") {
		t.Fatalf("형식: %v", err)
	}
	if _, err := e.quotes.Attach(as(uStaff), pr.ID, "a.txt", "", nil); !isErr(err, contract.ErrValidation) {
		t.Fatalf("빈 파일: %v", err)
	}
	if _, err := e.quotes.Attach(as(uStaff), pr.ID, "a.pdf", "", []byte("텍스트인데 pdf")); !isErr(err, contract.ErrValidation) {
		t.Fatalf("내용 서명: %v", err)
	}
	// 다른 사람의 요청에는 올릴 수 없다(범위 밖은 없는 것으로, 같은 부서면 본인이 아님).
	if _, err := e.quotes.Attach(as(uStaff2), pr.ID, "a.txt", "", []byte("x")); !isErr(err, contract.ErrNotFound) {
		t.Fatalf("타 부서: %v", err)
	}
	if _, err := e.quotes.Attach(as(uLead), pr.ID, "a.txt", "", []byte("x")); err == nil {
		t.Fatal("작성 중인 타인의 요청에 견적서를 올릴 수 있음")
	}
	if _, err := e.quotes.Attach(as(uAuditor), pr.ID, "a.txt", "", []byte("x")); !isErr(err, contract.ErrForbidden) {
		t.Fatalf("권한 없음: %v", err)
	}
	_ = other
	// 10건 상한.
	for i := 0; i < 10; i++ {
		e.quoteFake.res = quoteResult(fmt.Sprintf("상사%d", i), "", 100)
		if _, err := e.quotes.Attach(as(uStaff), pr.ID, "a.txt", "", []byte("x")); err != nil {
			t.Fatalf("%d번째: %v", i, err)
		}
	}
	if _, err := e.quotes.Attach(as(uStaff), pr.ID, "a.txt", "", []byte("x")); !isErr(err, contract.ErrValidation) || !strings.Contains(err.Error(), "10건") {
		t.Fatalf("상한: %v", err)
	}
}

// 읽지 못한 견적서도 업로드는 성공하고 "읽지 못함"으로 저장되며 비교·선택에서 빠진다. 추출기가 없을 때도 같다.
func TestQuoteUnreadableStillStored(t *testing.T) {
	e := newEnv(t)
	pr := e.draft(t, uStaff, 1_000)
	e.quoteFake.res, e.quoteFake.err = docverify.QuoteResult{}, fmt.Errorf("%w: 스캔", docverify.ErrQuoteUnreadable)
	q, err := e.quotes.Attach(as(uStaff), pr.ID, "scan.pdf", "application/pdf", []byte("%PDF-1.4"))
	if err != nil || q.Status != domain.QuoteUnreadable || q.Filled {
		t.Fatalf("%+v %v", q, err)
	}
	e.quoteFake.err = docverify.ErrQuoteUnsupported
	if _, err := e.quotes.Attach(as(uStaff), pr.ID, "a.txt", "", []byte("x")); !isErr(err, contract.ErrValidation) {
		t.Fatalf("지원하지 않는 형식은 거절: %v", err)
	}
	e.quotes.Extractor = nil
	q2, err := e.quotes.Attach(as(uStaff), pr.ID, "b.txt", "", []byte("x"))
	if err != nil || q2.Status != domain.QuoteUnreadable {
		t.Fatalf("추출기 없음: %+v %v", q2, err)
	}
	if _, err := e.quotes.Select(as(uStaff), pr.ID, q.ID, ""); !isErr(err, contract.ErrValidation) {
		t.Fatalf("읽지 못한 견적 선택: %v", err)
	}
	// 직접 입력하면 수정됨 + 거래처를 고르면 선택할 수 있다.
	v := e.vendorNamed(t, "직접상사", "111-11-11111")
	in := domain.QuoteInput{VendorID: v.ID, Lines: []domain.QuoteLine{{Name: "볼트", Quantity: 2, UnitPrice: 500}}}
	u, err := e.quotes.Update(as(uStaff), q.ID, in)
	if err != nil || !u.Edited || u.Total != 1_000 || !u.Filled || u.VendorID != v.ID {
		t.Fatalf("%+v %v", u, err)
	}
	if _, err := e.quotes.Select(as(uStaff), pr.ID, q.ID, ""); err != nil {
		t.Fatalf("선택: %v", err)
	}
}

func TestQuoteUpdateValidation(t *testing.T) {
	e := newEnv(t)
	v1 := e.vendorNamed(t, "가나상사", "111-11-11111")
	v2 := e.vendorNamed(t, "다라상사", "222-22-22222")
	pr := e.draft(t, uStaff, 1_000)
	q1 := e.attachQuote(t, uStaff, pr.ID, v1, 100)
	q2 := e.attachQuote(t, uStaff, pr.ID, v2, 200)
	line := []domain.QuoteLine{{Name: "볼트", Quantity: 1, UnitPrice: 100}}
	bad := map[string]domain.QuoteInput{
		"줄 없음":      {VendorID: v1.ID},
		"수량 0":      {VendorID: v1.ID, Lines: []domain.QuoteLine{{Name: "볼트", Quantity: 0, UnitPrice: 1}}},
		"음수 단가":     {VendorID: v1.ID, Lines: []domain.QuoteLine{{Name: "볼트", Quantity: 1, UnitPrice: -1}}},
		"이름 없음":     {VendorID: v1.ID, Lines: []domain.QuoteLine{{Name: " ", Quantity: 1, UnitPrice: 1}}},
		"없는 거래처":    {VendorID: 999, Lines: line},
		"같은 거래처 중복": {VendorID: v2.ID, Lines: line}, // q1 을 v2 로 바꾸려 하면 q2 와 겹친다
	}
	for name, in := range bad {
		if _, err := e.quotes.Update(as(uStaff), q1.ID, in); !isErr(err, contract.ErrValidation) {
			t.Fatalf("%s: %v", name, err)
		}
	}
	neg := -1
	if _, err := e.quotes.Update(as(uStaff), q1.ID, domain.QuoteInput{VendorID: v1.ID, Lines: line, LeadDays: &neg}); !isErr(err, contract.ErrValidation) {
		t.Fatalf("납기: %v", err)
	}
	// 문서 합계가 줄 합계와 다르면 경고만 붙는다.
	dt := int64(110)
	u, err := e.quotes.Update(as(uStaff), q2.ID, domain.QuoteInput{VendorID: v2.ID, Lines: line, DocTotal: &dt})
	if err != nil || len(u.Warnings) == 0 || !strings.Contains(u.Warnings[len(u.Warnings)-1], "합계가 다릅니다") {
		t.Fatalf("%+v %v", u, err)
	}
}

// 비교: 최저가는 합계 기준이고 동률은 모두 최저가, 차액은 최저가 대비 +N원.
func TestQuoteCompareLowestAndTies(t *testing.T) {
	e := newEnv(t)
	a := e.vendorNamed(t, "에이상사", "111-11-11111")
	b := e.vendorNamed(t, "비상사", "222-22-22222")
	c := e.vendorNamed(t, "씨상사", "333-33-33333")
	pr := e.draft(t, uStaff, 1_000)
	e.attachQuote(t, uStaff, pr.ID, a, 100)
	e.attachQuote(t, uStaff, pr.ID, b, 100)
	e.attachQuote(t, uStaff, pr.ID, c, 130)
	cmp, err := e.quotes.Compare(as(uStaff), pr.ID)
	if err != nil || len(cmp.Quotes) != 3 || cmp.LowestTotal != 1_000 || !cmp.Editable {
		t.Fatalf("%+v %v", cmp, err)
	}
	for _, q := range cmp.Quotes {
		wantLow := q.VendorID != c.ID
		if q.Lowest != wantLow || (wantLow && q.OverLowest != 0) || (!wantLow && q.OverLowest != 300) {
			t.Fatalf("%s: lowest=%v over=%d", q.VendorName, q.Lowest, q.OverLowest)
		}
	}
	// 열람 범위: 작성 중인 요청은 본인만 본다(전체 열람 권한자 제외). 타 부서는 못 본다.
	if _, err := e.quotes.Compare(as(uStaff2), pr.ID); !isErr(err, contract.ErrNotFound) {
		t.Fatalf("타 부서: %v", err)
	}
	if _, err := e.quotes.Compare(as(uLead), pr.ID); !isErr(err, contract.ErrNotFound) {
		t.Fatalf("작성 중 요청은 본인만: %v", err)
	}
	sub := e.submitted(t, uStaff, 1_000)
	cmp2, err := e.quotes.Compare(as(uLead), sub.ID)
	if err != nil || cmp2.Editable {
		t.Fatalf("상신한 요청은 같은 부서가 읽기만: %+v %v", cmp2, err)
	}
	if _, err := e.quotes.Compare(as(uAuditor), sub.ID); err != nil {
		t.Fatalf("감사: %v", err)
	}
}

// 사유 규칙(AC-513·514): 견적 2건 이상 + 최저가 아님 → 상신 때 사유 필수. 최저가·1건이면 불필요. 선택하면 요청 거래처가 바뀐다.
func TestQuoteReasonRequiredAtSubmit(t *testing.T) {
	e := newEnv(t)
	cheap := e.vendorNamed(t, "싼상사", "111-11-11111")
	dear := e.vendorNamed(t, "비싼상사", "222-22-22222")
	pr := e.draft(t, uStaff, 1_000)
	qc := e.attachQuote(t, uStaff, pr.ID, cheap, 100)
	// 견적 1건: 선택해도 사유 불필요.
	cmp, err := e.quotes.Select(as(uStaff), pr.ID, qc.ID, "")
	if err != nil || cmp.ReasonRequired {
		t.Fatalf("%+v %v", cmp, err)
	}
	qd := e.attachQuote(t, uStaff, pr.ID, dear, 150)
	cmp, err = e.quotes.Select(as(uStaff), pr.ID, qd.ID, "")
	if err != nil || !cmp.ReasonRequired || cmp.Note == "" || !strings.Contains(cmp.Note, "비싼상사는") || !strings.Contains(cmp.Note, "500원") {
		t.Fatalf("%+v %v", cmp, err)
	}
	got, _ := e.purchase.Get(as(uStaff), pr.ID)
	if got.VendorID != dear.ID || got.QuoteID != qd.ID {
		t.Fatalf("거래처 %d 선택 %d", got.VendorID, got.QuoteID)
	}
	// 사유 없이 상신 거부(서버 검사), 수정된 입력도 같다.
	if _, err := e.purchase.Submit(as(uStaff), pr.ID); !isErr(err, contract.ErrValidation) || !strings.Contains(err.Error(), "선택 사유를 입력하세요") {
		t.Fatalf("사유 없는 상신: %v", err)
	}
	// 사유를 넣고 상신 → 결재 이력에 사유가 남는다.
	if _, err := e.quotes.Select(as(uStaff), pr.ID, qd.ID, "납기가 빨라서"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.purchase.Submit(as(uStaff), pr.ID); err != nil {
		t.Fatalf("상신: %v", err)
	}
	h, err := e.approval.History(as(uStaff), pr.ID)
	if err != nil || len(h) == 0 || !strings.Contains(h[0].Comment, "견적 선택 사유: 납기가 빨라서") {
		t.Fatalf("이력 %+v %v", h, err)
	}
	// 상신 뒤에는 견적서·선택을 바꿀 수 없다(서비스와 트리거 모두).
	if _, err := e.quotes.Select(as(uStaff), pr.ID, qc.ID, ""); !isErr(err, contract.ErrInvalidState) {
		t.Fatalf("상신 뒤 선택: %v", err)
	}
	if err := e.quotes.Remove(as(uStaff), qd.ID); !isErr(err, contract.ErrInvalidState) {
		t.Fatalf("상신 뒤 삭제: %v", err)
	}
	if _, err := e.db.Exec(`DELETE FROM purchase_quotes WHERE id = ?`, qd.ID); err == nil {
		t.Fatal("트리거: 상신한 요청의 견적서 삭제가 허용됨")
	}
	if _, err := e.db.Exec(`UPDATE purchase_quotes SET total_amount = 1 WHERE id = ?`, qd.ID); err == nil {
		t.Fatal("트리거: 상신한 요청의 견적서 수정이 허용됨")
	}
}

// 최저가(동률 포함)를 고르면 사유가 필요 없고 입력한 사유는 저장하지 않는다.
func TestQuoteLowestNeedsNoReason(t *testing.T) {
	e := newEnv(t)
	a := e.vendorNamed(t, "에이상사", "111-11-11111")
	b := e.vendorNamed(t, "비상사", "222-22-22222")
	pr := e.draft(t, uStaff, 1_000)
	qa := e.attachQuote(t, uStaff, pr.ID, a, 100)
	e.attachQuote(t, uStaff, pr.ID, b, 100)
	cmp, err := e.quotes.Select(as(uStaff), pr.ID, qa.ID, "쓸데없는 사유")
	if err != nil || cmp.ReasonRequired || cmp.Reason != "" || cmp.Note != "" {
		t.Fatalf("%+v %v", cmp, err)
	}
	if _, err := e.purchase.Submit(as(uStaff), pr.ID); err != nil {
		t.Fatalf("최저가 선택 상신: %v", err)
	}
	h, _ := e.approval.History(as(uStaff), pr.ID)
	if strings.Contains(h[0].Comment, "사유") {
		t.Fatalf("사유가 남음: %q", h[0].Comment)
	}
}

// 요청 거래처를 따로 바꾸면 어긋난 선택은 풀리고, 선택한 견적을 지워도 풀린다. 견적서 없이도 상신할 수 있다.
func TestQuoteSelectionClearedOnVendorChangeAndRemove(t *testing.T) {
	e := newEnv(t)
	a := e.vendorNamed(t, "에이상사", "111-11-11111")
	b := e.vendorNamed(t, "비상사", "222-22-22222")
	pr := e.draft(t, uStaff, 1_000)
	qa := e.attachQuote(t, uStaff, pr.ID, a, 100)
	e.attachQuote(t, uStaff, pr.ID, b, 200)
	if _, err := e.quotes.Select(as(uStaff), pr.ID, qa.ID, ""); err != nil {
		t.Fatal(err)
	}
	cur, _ := e.purchase.Get(as(uStaff), pr.ID)
	in := domain.DraftInput{VendorID: b.ID, Title: cur.Title, Reason: cur.Reason,
		Lines: []domain.PRLineInput{{ItemID: cur.Lines[0].ItemID, Quantity: 1, UnitPrice: 1_000}}}
	upd, err := e.purchase.UpdateDraft(as(uStaff), pr.ID, in)
	if err != nil || upd.QuoteID != 0 {
		t.Fatalf("거래처를 바꾸면 선택이 풀려야 함: %+v %v", upd, err)
	}
	if _, err := e.quotes.Select(as(uStaff), pr.ID, qa.ID, ""); err != nil {
		t.Fatal(err)
	}
	if err := e.quotes.Remove(as(uStaff), qa.ID); err != nil {
		t.Fatal(err)
	}
	cur, _ = e.purchase.Get(as(uStaff), pr.ID)
	if cur.QuoteID != 0 || cur.VendorID != a.ID {
		t.Fatalf("삭제 뒤 선택 해제, 요청 거래처는 그대로: %+v", cur)
	}
	if _, err := e.quotes.Select(as(uStaff), pr.ID, 0, ""); err != nil {
		t.Fatalf("해제: %v", err)
	}
	bare := e.submitted(t, uStaff, 5_000)
	if bare.Status != domain.PRSubmitted {
		t.Fatal("견적서 없이 상신")
	}
}

func TestQuoteAuditRecords(t *testing.T) {
	e := newEnv(t)
	v := e.vendorNamed(t, "별도상사", "555-55-55555")
	pr := e.draft(t, uStaff, 1_000)
	q := e.attachQuote(t, uStaff, pr.ID, v, 100)
	if _, err := e.quotes.Select(as(uStaff), pr.ID, q.ID, ""); err != nil {
		t.Fatal(err)
	}
	in := domain.QuoteInput{VendorID: v.ID, Lines: []domain.QuoteLine{{Name: "볼트", Quantity: 1, UnitPrice: 10}}}
	if _, err := e.quotes.Update(as(uStaff), q.ID, in); err != nil {
		t.Fatal(err)
	}
	if err := e.quotes.Remove(as(uStaff), q.ID); err != nil {
		t.Fatal(err)
	}
	for _, a := range []string{"quote.attach", "quote.select", "quote.edit", "quote.remove"} {
		if !e.rec.Has(a, audit.TargetPurchaseQuote, audit.ResultSuccess) {
			t.Errorf("감사 %s 없음", a)
		}
	}
}
