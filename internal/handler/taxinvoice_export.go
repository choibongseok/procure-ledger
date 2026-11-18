// (내보내기 원천만). 세금계산서 목록 내보내기 데이터 세트 tax_invoices(B43 명세 9절 1항, B28 방식). 권한 taxinvoice:read.
// 이메일·주소·대표자·품목·사업자번호는 싣지 않는다(사업자번호 원문은 화면의 조회 기록 경로로만 나간다, B41). 열람 판정과 값 가공은 서비스(List)가 한 곳에서 하고 이 원천은 마스킹 해제 호출을 하지 않는다.
package handler

import (
	"context"
	"net/url"

	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/export"
)

type taxInvoiceSource struct{ svc domain.TaxInvoiceService }

func (s *taxInvoiceSource) Dataset() export.Dataset {
	return export.Dataset{Code: "tax_invoices", Label: "전자세금계산서", Columns: []export.Column{
		{Label: "번호"}, {Label: "구분"}, {Label: "과세 유형"}, {Label: "승인번호"}, {Label: "작성일", Kind: export.KindDate}, {Label: "상대방"},
		{Label: "공급가액(원)", Kind: export.KindMoney}, {Label: "세액(원)", Kind: export.KindMoney}, {Label: "합계(원)", Kind: export.KindMoney}, {Label: "상태"},
	}}
}

func (s *taxInvoiceSource) Filters(q url.Values) []export.FilterItem {
	return []export.FilterItem{export.Code("구분", q.Get("direction")), export.Code("상태", q.Get("status")), export.Date("시작일", q.Get("from")), export.Date("종료일", q.Get("to")),
		export.Free("상대방", q.Get("counterparty")), export.Free("승인번호", q.Get("approvalNo"))}
}

func (s *taxInvoiceSource) filter(q url.Values, limit int) domain.TaxInvoiceFilter {
	return domain.TaxInvoiceFilter{Direction: q.Get("direction"), Status: q.Get("status"), From: q.Get("from"), To: q.Get("to"),
		Counterparty: q.Get("counterparty"), ApprovalNo: q.Get("approvalNo"), Limit: limit}
}

func (s *taxInvoiceSource) Count(ctx context.Context, q url.Values, max int) (int, bool, error) {
	page, err := s.svc.List(ctx, s.filter(q, 1))
	if err != nil {
		return 0, false, err
	}
	return page.Total, false, nil
}

func (s *taxInvoiceSource) Rows(ctx context.Context, q url.Values, _ bool, max int) ([][]export.Cell, error) {
	var out [][]export.Cell
	for offset := 0; len(out) <= max; offset += 200 { // 서비스 목록은 한 쪽 200건까지라 쪽을 넘기며 읽는다
		f := s.filter(q, 200)
		f.Offset = offset
		page, err := s.svc.List(ctx, f)
		if err != nil {
			return nil, err
		}
		for _, v := range page.Rows {
			party := v.Buyer
			if v.Direction == domain.TaxPurchase {
				party = v.Supplier
			}
			out = append(out, []export.Cell{v.No, domain.TaxDirectionLabels[v.Direction], domain.TaxTypeLabels[v.TaxType], v.ApprovalNo, v.IssueDate, party.Name,
				v.SupplyTotal, v.TaxTotal, v.Total, domain.TaxStatusLabels[v.Status]})
		}
		if len(page.Rows) < 200 {
			break
		}
	}
	if len(out) > max+1 {
		out = out[:max+1]
	}
	return out, nil
}
