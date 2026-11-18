package docverify_test

import (
	"context"
	"strings"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/docverify"
	"github.com/choibongseok/procure-ledger/internal/docverify/kit"
)

var koreanDocLines = []string{
	"거래명세서", "공급자: 한빛상사", "사업자번호: 123-45-67890",
	"품목: 볼트 M8 | 수량 10 | 단가 500 | 금액 5000",
	"품목: 너트 M8 | 수량 20 | 단가 100 | 금액 2000",
	"합계: 7000",
}

// 한글 텍스트 레이어(Type0 + Identity-H + ToUnicode)에서 한글이 깨지지 않고 추출된다.
func TestExtractKoreanPDFText(t *testing.T) {
	txt, err := docverify.ExtractPDFText(kit.KoreanTextPDF(koreanDocLines))
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%q", txt)
	for _, l := range koreanDocLines {
		if !strings.Contains(txt, l) {
			t.Fatalf("줄이 그대로 추출되어야 함: %q\n전체: %q", l, txt)
		}
	}
}

// 한글 PDF 는 추출 텍스트가 한글 그대로 LLM 경로로 가고, 원문 교차 확인을 거쳐 일치로 판정된다.
func TestKoreanPDFEndToEndMatch(t *testing.T) {
	e := newEnv(t)
	po, rc, err := e.inv.Seed(context.Background(), e.db, boltSpec())
	if err != nil {
		t.Fatal(err)
	}
	v, err := e.svc.Verify(ctxAs(staff), domain.VerifyInput{Filename: "명세서.pdf", ContentType: docverify.PDFContentType,
		Image: kit.KoreanTextPDF(koreanDocLines), POID: po, ReceiptID: rc})
	if err != nil {
		t.Fatal(err)
	}
	if v.Outcome != domain.OutcomeMatch {
		t.Fatalf("한글 PDF 는 일치여야 함: %s %+v", v.Outcome, v.Items)
	}
	if len(e.spy.sent) != 1 || !strings.Contains(e.spy.sent[0].Messages[0].Content, "거래명세서") {
		t.Fatalf("한글 텍스트가 LLM 요청에 실려야 함: %+v", e.spy.sent)
	}
}

// ToUnicode 가 없어 한글이 깨져 추출되는 PDF 는 깨진 글자로 판정하지 않고 판단 보류로 떨어진다.
func TestGarbledKoreanPDFIsHeld(t *testing.T) {
	e := newEnv(t)
	po, rc, _ := e.inv.Seed(context.Background(), e.db, boltSpec())
	pdf := kit.KoreanTextPDFWithoutToUnicode(koreanDocLines)
	txt, xerr := docverify.ExtractPDFText(pdf)
	t.Logf("ToUnicode 없는 PDF 추출 결과: %q, %v", txt, xerr)
	v, err := e.svc.Verify(ctxAs(staff), domain.VerifyInput{Filename: "깨짐.pdf", ContentType: docverify.PDFContentType, Image: pdf, POID: po, ReceiptID: rc})
	if err != nil {
		t.Fatal(err)
	}
	if v.Outcome != domain.OutcomeHold {
		t.Fatalf("보류여야 함: %s %+v", v.Outcome, v.Items)
	}
	if it, ok := find(v, "extraction"); !ok || !strings.Contains(it.Note, "깨짐") {
		t.Fatalf("근거 문구에 깨짐이 드러나야 함: %+v", v.Items)
	}
	if len(e.spy.sent) != 0 {
		t.Fatal("깨진 텍스트로 LLM 을 호출하면 안 됨")
	}
}
