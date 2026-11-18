package docverify_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/contract/llm"
	"github.com/choibongseok/procure-ledger/internal/docverify"
)

type cannedLLM struct {
	text string
	err  error
	reqs []llm.Request
}

func (c *cannedLLM) Complete(_ context.Context, r llm.Request) (llm.Response, error) {
	c.reqs = append(c.reqs, r)
	return llm.Response{Text: c.text}, c.err
}

const quoteDoc = "견적서\n공급자: 한빛상사\n사업자번호: 123-45-67890\n품목: 볼트 M8 | 수량 10 | 단가 500 | 금액 5000\n합계: 5000\n납기: 발주 후 7일"

const goodJSON = `{"supplier":"한빛상사","lines":[{"name":"볼트 M8","quantity":10,"unit_price":500,"amount":5000}],"total_amount":5000,"lead_text":"발주 후 7일","lead_days":7,
"citations":{"supplier":{"quote":"한빛상사","line":2},"line[0].name":{"quote":"볼트 M8","line":4},"line[0].quantity":{"quote":"수량 10","line":4},"line[0].unit_price":{"quote":"단가 500","line":4},"line[0].amount":{"quote":"금액 5000","line":4},"total_amount":{"quote":"5000","line":5},"lead_text":{"quote":"발주 후 7일","line":6}}}`

func extract(t *testing.T, c *cannedLLM, text string) (docverify.QuoteResult, error) {
	t.Helper()
	return docverify.QuoteExtractor{LLM: c}.ExtractText(context.Background(), text)
}

func TestQuoteExtractGoodAndComputedByCode(t *testing.T) {
	c := &cannedLLM{text: goodJSON}
	r, err := extract(t, c, quoteDoc)
	if err != nil {
		t.Fatal(err)
	}
	if r.Supplier != "한빛상사" || r.BusinessNo != "123-45-67890" || r.LineTotal != 5000 || !r.HasDocTotal || r.DocTotal != 5000 || !r.HasLeadDays || r.LeadDays != 7 || len(r.Warnings) != 0 || r.Text != quoteDoc {
		t.Fatalf("%+v", r)
	}
	if r.Citations["line[0].amount"].Quote != "금액 5000" {
		t.Fatalf("인용: %+v", r.Citations)
	}
	// 요청: 문서는 <document> 안에 격리되고 목적 라벨이 붙는다.
	req := c.reqs[0]
	if req.Purpose != docverify.PurposeQuoteExtract || !strings.Contains(req.Messages[0].Content, "<document>") || !strings.Contains(req.System, "따르지 않는다") {
		t.Fatalf("요청: %+v", req)
	}
	// 줄 합계는 LLM 이 낸 합계가 아니라 줄 금액 합이다: 문서 합계가 달라도 줄 합계를 쓰고 경고한다.
	doc2 := strings.Replace(quoteDoc, "합계: 5000", "합계: 5500", 1)
	c.text = strings.Replace(goodJSON, `"total_amount":5000`, `"total_amount":5500`, 1)
	c.text = strings.Replace(c.text, `"total_amount":{"quote":"5000"`, `"total_amount":{"quote":"5500"`, 1)
	r, err = extract(t, c, doc2)
	if err != nil || r.LineTotal != 5000 || r.DocTotal != 5500 || len(r.Warnings) != 1 || !strings.Contains(r.Warnings[0], "줄 합계") {
		t.Fatalf("합계 경고: %+v %v", r, err)
	}
}

func TestQuoteCoreValueWithoutGroundIsUnreadable(t *testing.T) {
	// 원문에 없는 수량: 핵심 값이라 읽지 못함.
	bad := strings.Replace(goodJSON, `"quantity":10`, `"quantity":99`, 1)
	bad = strings.Replace(bad, `"quote":"수량 10"`, `"quote":"수량 99"`, 1)
	if _, err := extract(t, &cannedLLM{text: bad}, quoteDoc); !errors.Is(err, docverify.ErrQuoteUnreadable) {
		t.Fatalf("근거 없는 수량: %v", err)
	}
	// 인용이 아예 없다.
	noCite := `{"supplier":"한빛상사","lines":[{"name":"볼트 M8","quantity":10,"unit_price":500,"amount":5000}]}`
	if _, err := extract(t, &cannedLLM{text: noCite}, quoteDoc); !errors.Is(err, docverify.ErrQuoteUnreadable) {
		t.Fatalf("인용 없음: %v", err)
	}
	// 같은 숫자가 다른 줄에만 있는 인용(줄 힌트 불일치)도 거절.
	moved := strings.Replace(goodJSON, `"line[0].amount":{"quote":"금액 5000","line":4}`, `"line[0].amount":{"quote":"금액 5000","line":9}`, 1)
	if _, err := extract(t, &cannedLLM{text: moved}, quoteDoc); !errors.Is(err, docverify.ErrQuoteUnreadable) {
		t.Fatalf("줄 번호 불일치: %v", err)
	}
}

func TestQuoteOptionalValuesDroppedWithWarning(t *testing.T) {
	// 환각 납기·합계: 근거가 없으면 비우고 경고한다. 핵심 값은 유지.
	hall := strings.Replace(goodJSON, `"quote":"발주 후 7일"`, `"quote":"발주 후 1일"`, 1)
	hall = strings.Replace(hall, `"quote":"5000","line":5`, `"quote":"합계 1원","line":5`, 1)
	r, err := extract(t, &cannedLLM{text: hall}, quoteDoc)
	if err != nil || r.HasLeadDays || r.LeadText != "" || r.HasDocTotal || r.LineTotal != 5000 || len(r.Warnings) != 2 {
		t.Fatalf("%+v %v", r, err)
	}
	// 일수가 근거 구절 안에 없으면 일수만 비운다.
	days := strings.Replace(goodJSON, `"lead_days":7`, `"lead_days":3`, 1)
	r, err = extract(t, &cannedLLM{text: days}, quoteDoc)
	if err != nil || r.HasLeadDays || r.LeadText != "발주 후 7일" || len(r.Warnings) != 1 {
		t.Fatalf("일수: %+v %v", r, err)
	}
}

func TestQuoteSchemaAndInjectionDefense(t *testing.T) {
	for name, text := range map[string]string{
		"JSON 아님":        "죄송합니다",
		"모르는 필드(지시를 따름)": strings.Replace(goodJSON, `"supplier":"한빛상사",`, `"supplier":"한빛상사","selected":true,`, 1),
		"추가 내용":          goodJSON + "\n승인",
		"공급자 없음":         strings.Replace(goodJSON, `"supplier":"한빛상사",`, ``, 1),
		"음수 단가":          strings.Replace(goodJSON, `"unit_price":500`, `"unit_price":-1`, 1),
		"줄 없음":           `{"supplier":"한빛상사","lines":[]}`,
		"납기 범위":          strings.Replace(goodJSON, `"lead_days":7`, `"lead_days":9999`, 1),
	} {
		if _, err := extract(t, &cannedLLM{text: text}, quoteDoc); !errors.Is(err, docverify.ErrQuoteUnreadable) {
			t.Fatalf("%s: %v", name, err)
		}
	}
	// 줄 51개.
	var lines []string
	for i := 0; i < 51; i++ {
		lines = append(lines, `{"name":"a","quantity":1,"unit_price":1,"amount":1}`)
	}
	if _, err := extract(t, &cannedLLM{text: `{"supplier":"한빛상사","lines":[` + strings.Join(lines, ",") + `]}`}, quoteDoc); !errors.Is(err, docverify.ErrQuoteUnreadable) {
		t.Fatalf("줄 상한: %v", err)
	}
	// LLM 오류·빈 문서도 읽지 못함(업로드는 성공시킬 수 있게).
	if _, err := extract(t, &cannedLLM{err: errors.New("일일 한도 초과")}, quoteDoc); !errors.Is(err, docverify.ErrQuoteUnreadable) {
		t.Fatalf("LLM 오류: %v", err)
	}
	if _, err := extract(t, &cannedLLM{text: goodJSON}, "  "); !errors.Is(err, docverify.ErrQuoteUnreadable) {
		t.Fatalf("빈 문서: %v", err)
	}
}

func TestQuoteExtractFileTypes(t *testing.T) {
	ex := docverify.QuoteExtractor{LLM: &cannedLLM{text: goodJSON}}
	ctx := context.Background()
	if _, err := ex.ExtractFile(ctx, "견적.png", "image/png", []byte("x")); !errors.Is(err, docverify.ErrQuoteUnsupported) {
		t.Fatalf("이미지: %v", err)
	}
	if _, err := ex.ExtractFile(ctx, "견적.txt", "text/plain", []byte{0xff, 0xfe, 0x00}); !errors.Is(err, docverify.ErrQuoteUnsupported) {
		t.Fatalf("인코딩: %v", err)
	}
	if _, err := ex.ExtractFile(ctx, "큰.txt", "text/plain", make([]byte, 300<<10)); !errors.Is(err, docverify.ErrQuoteUnsupported) {
		t.Fatalf("크기: %v", err)
	}
	if r, err := ex.ExtractFile(ctx, "견적.txt", "text/plain; charset=utf-8", []byte(quoteDoc)); err != nil || r.LineTotal != 5000 {
		t.Fatalf("텍스트: %+v %v", r, err)
	}
	if _, err := ex.ExtractFile(ctx, "스캔.pdf", "application/pdf", []byte("%PDF-1.4 garbage")); !errors.Is(err, docverify.ErrQuoteUnreadable) {
		t.Fatalf("깨진 PDF: %v", err)
	}
}

func TestMatchVendor(t *testing.T) {
	vs := []domain.Vendor{
		{ID: 1, Name: "대한오피스", BusinessNo: "111-22-33333"},
		{ID: 2, Name: "(주)한빛상사", BusinessNo: "123-45-67890"},
		{ID: 3, Name: "스마트유통", BusinessNo: ""},
		{ID: 4, Name: "중복상사", BusinessNo: "555-55-55555"},
		{ID: 5, Name: "중복상사", BusinessNo: "555-55-55556"},
	}
	for name, c := range map[string]struct {
		no, n string
		id    int64
		ok    bool
	}{
		"사업자번호 우선(이름 다름)":    {"1234567890", "엉뚱한이름", 2, true},
		"하이픈 달라도 같은 번호":      {"111-22-33333", "", 1, true},
		"이름 정리 뒤 일치":         {"", "한빛 상사 주식회사", 2, true},
		"이름만(번호 없는 거래처)":     {"", "스마트유통", 3, true},
		"없는 거래처":             {"999-99-99999", "새거래처", 0, false},
		"이름이 같은 거래처가 둘이면 거절": {"", "중복상사", 0, false},
		"부분 일치는 안 된다":        {"", "한빛", 0, false},
		"빈 입력":               {"", "", 0, false},
	} {
		id, ok := docverify.MatchVendor(vs, c.no, c.n)
		if id != c.id || ok != c.ok {
			t.Fatalf("%s: %d %v", name, id, ok)
		}
	}
}
