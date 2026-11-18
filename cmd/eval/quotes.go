package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/choibongseok/procure-ledger/evals"
	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/docverify"
	"github.com/choibongseok/procure-ledger/internal/docverify/kit"
	"github.com/choibongseok/procure-ledger/internal/llmgw"
	"github.com/choibongseok/procure-ledger/internal/rbac"
)

// runQuotes 는 견적서 추출 골든셋(B25)을 게이트웨이 경유로 돌려 채점한다. 값 추출·근거 확인·줄 합계·거래처 연결은 코드가 하고,
// LLM 출력(mock 포함)은 그 검증을 통과한 값만 결과에 영향을 준다.
func runQuotes(ctx context.Context, gw *llmgw.Gateway, set evals.QuoteSet) []CaseResult {
	ex := docverify.QuoteExtractor{LLM: gw, Masker: rbac.Masker{}}
	pctx := contract.WithPrincipal(ctx, kit.Principal(contract.RoleStaff))
	var vendors []domain.Vendor
	for _, v := range set.Vendors {
		vendors = append(vendors, domain.Vendor{ID: v.ID, Name: v.Name, BusinessNo: v.BusinessNo})
	}
	var out []CaseResult
	for _, c := range set.Cases {
		cr := CaseResult{Suite: "quotes", ID: c.ID, Critical: c.Critical}
		var data []byte
		name, ct := c.ID+".txt", "text/plain"
		switch c.Format {
		case "pdf":
			data, name, ct = kit.KoreanTextPDF(strings.Split(c.Doc, "\n")), c.ID+".pdf", docverify.PDFContentType
		case "pdf_garbled":
			data, name, ct = kit.KoreanTextPDFWithoutToUnicode(strings.Split(c.Doc, "\n")), c.ID+".pdf", docverify.PDFContentType
		case "pdf_blank":
			data, name, ct = kit.BlankPDF(), c.ID+".pdf", docverify.PDFContentType
		default:
			data = []byte(c.Doc)
		}
		res, err := ex.ExtractFile(pctx, name, ct, data)
		status := "ok"
		switch {
		case errors.Is(err, docverify.ErrQuoteUnsupported):
			status = "unsupported"
		case err != nil:
			status = "unreadable"
		}
		var problems []string
		e := c.Expect
		if status != e.Status {
			problems = append(problems, fmt.Sprintf("상태 %s (기대 %s)", status, e.Status))
		}
		if status == "ok" && e.Status == "ok" {
			problems = append(problems, checkQuote(res, e, vendors)...)
		}
		cr.Pass = len(problems) == 0
		cr.Detail = "상태 " + status
		if !cr.Pass {
			cr.Detail = strings.Join(problems, "; ")
		} else if err != nil {
			cr.Detail += ": " + err.Error()
		}
		out = append(out, cr)
	}
	return out
}

func checkQuote(r docverify.QuoteResult, e evals.QuoteExpect, vendors []domain.Vendor) []string {
	var p []string
	if e.Supplier != "" && r.Supplier != e.Supplier {
		p = append(p, fmt.Sprintf("공급자 %q (기대 %q)", r.Supplier, e.Supplier))
	}
	if e.SupplierContains != "" && !strings.Contains(r.Supplier, e.SupplierContains) {
		p = append(p, fmt.Sprintf("공급자 %q 에 %q 없음", r.Supplier, e.SupplierContains))
	}
	for _, bad := range e.SupplierNotContains {
		if strings.Contains(r.Supplier, bad) {
			p = append(p, fmt.Sprintf("공급자에 마스킹 전 값 %q 이 있음", bad))
		}
	}
	if e.LineTotal != nil && r.LineTotal != *e.LineTotal {
		p = append(p, fmt.Sprintf("줄 합계 %d (기대 %d)", r.LineTotal, *e.LineTotal))
	}
	if e.DocTotal != nil && (!r.HasDocTotal || r.DocTotal != *e.DocTotal) {
		p = append(p, fmt.Sprintf("문서 합계 %d/%v (기대 %d)", r.DocTotal, r.HasDocTotal, *e.DocTotal))
	}
	if e.NoDocTotal && r.HasDocTotal {
		p = append(p, "문서 합계가 비어 있어야 함")
	}
	if e.LeadDays != nil && (!r.HasLeadDays || r.LeadDays != *e.LeadDays) {
		p = append(p, fmt.Sprintf("납기 일수 %d/%v (기대 %d)", r.LeadDays, r.HasLeadDays, *e.LeadDays))
	}
	if e.LeadText != "" && r.LeadText != e.LeadText {
		p = append(p, fmt.Sprintf("납기 문구 %q (기대 %q)", r.LeadText, e.LeadText))
	}
	if e.NoLead && (r.HasLeadDays || r.LeadText != "") {
		p = append(p, "납기가 비어 있어야 함")
	}
	for _, w := range e.Warn {
		found := false
		for _, x := range r.Warnings {
			found = found || strings.Contains(x, w)
		}
		if !found {
			p = append(p, fmt.Sprintf("경고에 %q 없음", w))
		}
	}
	if e.NoWarn && len(r.Warnings) > 0 {
		p = append(p, fmt.Sprintf("경고가 없어야 함: %v", r.Warnings))
	}
	if e.BusinessNo != "" && r.BusinessNo != e.BusinessNo {
		p = append(p, fmt.Sprintf("사업자번호 %q (기대 %q)", r.BusinessNo, e.BusinessNo))
	}
	if e.VendorID != nil {
		id, _ := docverify.MatchVendor(vendors, r.BusinessNo, r.Supplier)
		if id != *e.VendorID {
			p = append(p, fmt.Sprintf("거래처 연결 %d (기대 %d)", id, *e.VendorID))
		}
	}
	return p
}
