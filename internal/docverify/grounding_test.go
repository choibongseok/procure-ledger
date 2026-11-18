package docverify_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/docverify"
)

// 모델이 발주에 맞춘 숫자를 지어내도(원문엔 없음) 일치로 통과하지 못한다.
func TestFabricatedNumbersNotInSourceAreHeld(t *testing.T) {
	e := newEnv(t)
	doc := strings.Replace(goodDoc, "합계: 7000", "합계: 9000\n비고: 합계보정", 1)
	v := e.verify(t, staff, boltSpec(), doc)
	if v.Outcome != domain.OutcomeHold {
		t.Fatalf("원문에 없는 합계는 보류: %s %+v", v.Outcome, v.Items)
	}
	it, ok := find(v, "extraction")
	if !ok || !strings.Contains(it.Note, "근거를 확인하지 못한") || !strings.Contains(it.Note, "합계 7000") {
		t.Fatalf("근거 문구: %+v", v.Items)
	}
}

func cite(q string) domain.Citation { return domain.Citation{Quote: q} }

func TestGroundAcceptsSeparatorsAndRejectsPartialTokens(t *testing.T) {
	text := "품목 a | 수량 10 | 단가 700 | 금액 7,000\n합계 7,000원"
	mk := func() domain.Extraction {
		return domain.Extraction{Supplier: "a", TotalAmount: 7000, Lines: []domain.ExtractedLine{{Name: "a", Quantity: 10, UnitPrice: 700, Amount: 7000}},
			Citations: map[string]domain.Citation{"supplier": cite("a"), "line[0].name": cite("a"), "line[0].quantity": cite("수량 10"),
				"line[0].unit_price": cite("단가 700"), "line[0].amount": cite("금액 7,000"), "total_amount": cite("합계 7,000원")}}
	}
	ex := mk()
	if err := docverify.Ground(&ex, nil, text); err != nil {
		t.Fatalf("쉼표 표기는 통과: %v", err)
	}
	if c := ex.Citations["total_amount"]; string([]rune(text)[c.Start:c.End]) != "합계 7,000원" {
		t.Fatalf("위치가 채워져야 한다: %+v", c)
	}
	// 70000 안의 7000 처럼 숫자 일부는 근거가 아니다.
	var ug *docverify.UngroundedError
	ex = mk()
	err := docverify.Ground(&ex, nil, "품목 a | 수량 10 | 단가 700 | 금액 70000\n합계 70000")
	if !errors.As(err, &ug) || len(ug.Values) == 0 {
		t.Fatalf("토큰 일부는 근거가 아님: %v", err)
	}
}

func TestGroundRejectsMissingForgedAndMisplacedCitations(t *testing.T) {
	text := "공급자: 한빛상사\n품목: 볼트 | 수량 10 | 단가 500 | 금액 5000\n품목: 너트 | 수량 10 | 단가 100 | 금액 1000\n합계: 6000"
	base := func() domain.Extraction {
		return domain.Extraction{Supplier: "한빛상사", TotalAmount: 6000, Lines: []domain.ExtractedLine{
			{Name: "볼트", Quantity: 10, UnitPrice: 500, Amount: 5000}, {Name: "너트", Quantity: 10, UnitPrice: 100, Amount: 1000}},
			Citations: map[string]domain.Citation{"supplier": cite("한빛상사"),
				"line[0].name": cite("볼트"), "line[0].quantity": cite("수량 10"), "line[0].unit_price": cite("단가 500"), "line[0].amount": cite("금액 5000"),
				"line[1].name": cite("너트"), "line[1].quantity": cite("수량 10"), "line[1].unit_price": cite("단가 100"), "line[1].amount": cite("금액 1000"),
				"total_amount": cite("합계: 6000")}}
	}
	ex := base()
	if err := docverify.Ground(&ex, nil, text); err != nil {
		t.Fatalf("정상 인용: %v", err)
	}
	// 두 번째 줄 수량 인용이 첫 줄 위치에 있다: 같은 숫자 10 이지만 줄이 다르다. 코드는 같은 줄(너트)의 구절을 찾아 인정한다.
	if c := ex.Citations["line[1].quantity"]; string([]rune(text)[c.Start:c.End]) != "수량 10" || c.Start < 30 {
		t.Fatalf("두 번째 줄의 위치여야 한다: %+v", c)
	}
	for name, mut := range map[string]func(*domain.Extraction){
		"인용 없음":     func(e *domain.Extraction) { delete(e.Citations, "total_amount") },
		"원문에 없는 구절": func(e *domain.Extraction) { e.Citations["total_amount"] = cite("합계: 9999") },
		"값이 구절에 없음": func(e *domain.Extraction) { e.Citations["line[0].unit_price"] = cite("단가 100") },
		"다른 줄의 구절":  func(e *domain.Extraction) { e.Citations["line[1].unit_price"] = cite("단가 500") },
	} {
		ex := base()
		mut(&ex)
		var ug *docverify.UngroundedError
		if err := docverify.Ground(&ex, nil, text); !errors.As(err, &ug) {
			t.Errorf("%s: 근거 없음이어야 한다: %v", name, err)
		}
	}
}
