package docverify_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/docverify"
	"github.com/choibongseok/procure-ledger/internal/docverify/kit"
)

func TestExtractPDFText(t *testing.T) {
	txt, err := docverify.ExtractPDFText(kit.TextPDF([]string{"Statement", "Total: 7000 (KRW)"}))
	if err != nil || !strings.Contains(txt, "Statement") || !strings.Contains(txt, "7000") {
		t.Fatalf("텍스트 PDF: %q %v", txt, err)
	}
	if _, err := docverify.ExtractPDFText(kit.BlankPDF()); !errors.Is(err, docverify.ErrNoTextLayer) {
		t.Fatalf("텍스트 레이어 없음: %v", err)
	}
	if _, err := docverify.ExtractPDFText([]byte("%PDF-1.4 garbage")); err == nil || errors.Is(err, docverify.ErrNoTextLayer) {
		t.Fatalf("깨진 PDF 는 해석 오류여야 함: %v", err)
	}
}

// 텍스트 PDF 는 추출 텍스트가 기존 경로(게이트웨이 마스킹 포함)로 LLM 에 가고, 판정은 코드가 한다.
func TestPDFTextGoesThroughGatewayMaskingAndComparison(t *testing.T) {
	e := newEnv(t)
	po, _, err := e.inv.Seed(context.Background(), e.db, kit.POSpec{VendorName: "Hanbit", VendorBusinessNo: "123-45-67890",
		Lines: []kit.LineSpec{{Name: "Bolt", Qty: 10, UnitPrice: 500}}})
	if err != nil {
		t.Fatal(err)
	}
	pdf := kit.TextPDF([]string{"Statement", "Business No 123-45-67890", "Total 5000"})
	v, err := e.svc.Verify(ctxAs(staff), domain.VerifyInput{Filename: "s.pdf", ContentType: docverify.PDFContentType, Image: pdf, POID: po})
	if err != nil {
		t.Fatal(err)
	}
	if len(e.spy.sent) != 1 {
		t.Fatalf("LLM 호출 1회여야 함: %d", len(e.spy.sent))
	}
	sent := e.spy.sent[0]
	if !strings.Contains(sent.Messages[0].Content, "Statement") || strings.Contains(sent.Messages[0].Content, "123-45-67890") || len(sent.Images) != 0 {
		t.Fatalf("추출 텍스트가 마스킹되어 텍스트로 가야 함(이미지 아님): %q images=%d", sent.Messages[0].Content, len(sent.Images))
	}
	// mock 은 한국어 줄 형식만 읽으므로 이 영문 PDF 는 스키마에 못 맞춰 보류가 된다. 어느 쪽이든 자동 일치는 아니다.
	if v.Outcome == domain.OutcomeMatch {
		t.Fatalf("일치로 나오면 안 됨: %+v", v)
	}
	var kind, ct string
	var hasImage int
	e.db.QueryRow(`SELECT kind, content_type, CAST(image IS NOT NULL AS INTEGER) FROM documents WHERE id = ?`, v.DocumentID).Scan(&kind, &ct, &hasImage)
	if ct != docverify.PDFContentType || hasImage != 1 {
		t.Fatalf("원본 PDF 가 보관되어야 함: %s %d", ct, hasImage)
	}
}

func TestPDFWithoutTextLayerIsHeldWithoutCallingLLM(t *testing.T) {
	e := newEnv(t)
	po, _, _ := e.inv.Seed(context.Background(), e.db, boltSpec())
	v, err := e.svc.Verify(ctxAs(staff), domain.VerifyInput{Filename: "scan.pdf", ContentType: docverify.PDFContentType, Image: kit.BlankPDF(), POID: po})
	if err != nil {
		t.Fatal(err)
	}
	if v.Outcome != domain.OutcomeHold {
		t.Fatalf("보류여야 함: %s", v.Outcome)
	}
	it, ok := find(v, "extraction")
	if !ok || !strings.Contains(it.Note, "텍스트 레이어가 없는 PDF") || !strings.Contains(it.Note, "추측하지 않고") {
		t.Fatalf("근거 문구: %+v", v.Items)
	}
	if len(e.spy.sent) != 0 {
		t.Fatal("텍스트가 없는 PDF 로 LLM 을 호출하면 안 됨")
	}
}

func TestBrokenPDFIsValidationError(t *testing.T) {
	e := newEnv(t)
	po, _, _ := e.inv.Seed(context.Background(), e.db, boltSpec())
	_, err := e.svc.Verify(ctxAs(staff), domain.VerifyInput{Filename: "x.pdf", ContentType: docverify.PDFContentType, Image: []byte("not a pdf"), POID: po})
	if !errors.Is(err, contract.ErrValidation) {
		t.Fatalf("깨진 PDF: %v", err)
	}
}
