package docverify_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/docverify"
	"github.com/choibongseok/procure-ledger/internal/docverify/kit"
)

// B47: 쪽 단위 추출. 한글 PDF 가 쪽마다 따로 나오고, 글자 없는 PDF·한글이 깨지는 PDF·깨진 파일은 구분된 오류다.
func TestExtractPDFPagesKorean(t *testing.T) {
	pages, err := docverify.ExtractPDFPages(kit.KoreanMultiPagePDF([][]string{{"제1조 목적", "이 규정은 구매 절차를 정한다"}, {"제2조 결재선", "금액 구간별로 결재선을 둔다"}}, true))
	if err != nil {
		t.Fatal(err)
	}
	if len(pages) != 2 || !strings.Contains(pages[0], "제1조 목적") || !strings.Contains(pages[1], "금액 구간별로 결재선을 둔다") || strings.Contains(pages[0], "제2조") {
		t.Fatalf("쪽별 글: %q", pages)
	}
}

func TestExtractPDFPagesErrors(t *testing.T) {
	if _, err := docverify.ExtractPDFPages(kit.BlankPDF()); !errors.Is(err, docverify.ErrNoTextLayer) {
		t.Fatalf("스캔본: %v", err)
	}
	if _, err := docverify.ExtractPDFPages(kit.KoreanMultiPagePDF([][]string{{"제1조 목적"}}, false)); !errors.Is(err, docverify.ErrUnreadableText) {
		t.Fatalf("글꼴 매핑 없음: %v", err)
	}
	if _, err := docverify.ExtractPDFPages([]byte("%PDF-1.4 garbage")); err == nil || errors.Is(err, docverify.ErrNoTextLayer) {
		t.Fatalf("깨진 파일: %v", err)
	}
	if got, err := docverify.ExtractPDFPages(kit.TextPDF([]string{"Purchase rule"})); err != nil || len(got) != 1 || got[0] != "Purchase rule" {
		t.Fatalf("영문: %q %v", got, err)
	}
}
