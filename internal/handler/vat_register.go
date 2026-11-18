// 부가세 매입 집계 라우트와 내보내기 원천 3개(B32 단계 3, 3.4). 권한 vat:read, 금액은 이 권한 없이는 나가지 않는다.
package handler

import (
	"context"
	"net/http"
	"net/url"
	"strconv"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/deps"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/export"
)

// RegisterVAT 는 /api/finance/vat-purchase 와 내보내기 3개를 등록한다. 호출: cmd/server/routes_erp.go.
func RegisterVAT(r contract.Registrar, d deps.Deps) {
	if d.VAT == nil {
		return
	}
	h := vatHandler{svc: d.VAT}
	r.HandleFunc("GET", "/api/finance/vat-purchase", contract.PermVATRead, h.summary)
	if d.Export != nil {
		export.Register(r, "/api/finance/vat-purchase/export/vendors", contract.PermVATRead, d.Export, &vatSource{svc: d.VAT, table: "vendors"})
		export.Register(r, "/api/finance/vat-purchase/export/cards", contract.PermVATRead, d.Export, &vatSource{svc: d.VAT, table: "cards"})
		export.Register(r, "/api/finance/vat-purchase/export/unreceived", contract.PermVATRead, d.Export, &vatSource{svc: d.VAT, table: "unreceived"})
		export.Register(r, "/api/finance/vat-purchase/export/unlinked", contract.PermVATRead, d.Export, &vatSource{svc: d.VAT, table: "unlinked"})
		export.Register(r, "/api/finance/vat-purchase/export/diff", contract.PermVATRead, d.Export, &vatSource{svc: d.VAT, table: "diff"})
	}
}

type vatHandler struct{ svc domain.VATService }

func vatFilterOf(q url.Values) domain.VATFilter {
	y, _ := strconv.Atoi(q.Get("year"))
	qu, _ := strconv.Atoi(q.Get("quarter"))
	site, _ := strconv.ParseInt(q.Get("siteId"), 10, 64)
	return domain.VATFilter{Unit: q.Get("unit"), Month: q.Get("month"), Year: y, Quarter: qu, From: q.Get("from"), To: q.Get("to"), SiteID: site}
}

func (h vatHandler) summary(w http.ResponseWriter, r *http.Request) {
	v, err := h.svc.PurchaseSummary(r.Context(), vatFilterOf(r.URL.Query()))
	if err != nil {
		writeErr(w, err)
		return
	}
	vendors := make([]map[string]any, len(v.Vendors))
	for i, x := range v.Vendors {
		vendors[i] = map[string]any{"vendorId": x.VendorID, "vendorName": x.VendorName, "businessNo": x.BusinessNo, "count": x.Count, "supply": x.Supply, "vat": x.VAT}
	}
	unr := make([]map[string]any, len(v.Unreceived))
	for i, x := range v.Unreceived {
		unr[i] = map[string]any{"journalNo": x.JournalNo, "vendorName": x.VendorName, "supply": x.Supply, "vat": x.VAT, "tradeDate": x.TradeDate}
	}
	unl := make([]map[string]any, len(v.Unlinked))
	for i, x := range v.Unlinked {
		unl[i] = map[string]any{"invoiceNo": x.InvoiceNo, "approvalNo": x.ApprovalNo, "supplier": x.Supplier, "issueDate": x.IssueDate, "supply": x.Supply, "vat": x.VAT, "state": x.State}
	}
	diffs := make([]map[string]any, len(v.Diffs))
	for i, x := range v.Diffs {
		diffs[i] = map[string]any{"invoiceNo": x.InvoiceNo, "journalNos": x.JournalNos, "issueDate": x.IssueDate, "invoiceVat": x.InvoiceVAT, "journalVat": x.JournalVAT, "diff": x.Diff}
	}
	cards := make([]map[string]any, len(v.Cards))
	for i, x := range v.Cards {
		cards[i] = map[string]any{"kind": x.Kind, "reason": x.Reason, "reasonLabel": x.ReasonLabel, "count": x.Count, "supply": x.Supply, "vat": x.VAT, "total": x.Total}
	}
	hints := v.Hints
	if hints == nil {
		hints = []string{}
	}
	out := map[string]any{"from": v.From, "to": v.To, "vendors": vendors, "unreceived": unr, "unlinked": unl, "unlinkedVat": v.UnlinkedVAT, "diffs": diffs, "cards": cards, "vendorVat": v.VendorVAT, "cardDeductVat": v.CardDeductVAT,
		"deductibleVat": v.DeductibleVAT, "unreceivedVat": v.UnreceivedVAT, "ledgerVat": v.LedgerVAT, "difference": v.Difference, "matches": v.Matches, "hints": hints, "excludedCount": v.ExcludedCount}
	// 사업장 표시(법인에 사업장이 둘 이상일 때만 채워진다, B44 14.2). 하나뿐인 설치의 응답은 그대로다.
	if len(v.Sites) > 0 {
		rows := make([]map[string]any, len(v.Sites))
		for i, t := range v.Sites {
			rows[i] = map[string]any{"siteId": t.SiteID, "siteNo": t.SiteNo, "name": t.Name, "kind": t.Kind, "vatMode": t.VATMode, "count": t.Count, "vat": t.VAT}
		}
		out["sites"] = rows
	}
	if v.SiteID != 0 {
		out["siteId"], out["siteName"], out["siteScoped"] = v.SiteID, v.SiteName, v.SiteScoped
		if len(v.ConsolidatedIn) > 0 {
			out["consolidatedIn"] = v.ConsolidatedIn
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// ---- 내보내기 원천 ----

type vatSource struct {
	svc   domain.VATService
	table string // vendors | cards | unreceived | unlinked | diff
}

func (s *vatSource) Dataset() export.Dataset {
	switch s.table {
	case "cards":
		return export.Dataset{Code: "vat_purchase_cards", Label: "부가세매입_카드", Columns: []export.Column{{Label: "구분"}, {Label: "불공제 사유"}, {Label: "건수", Kind: export.KindInt},
			{Label: "공급가액(원)", Kind: export.KindMoney}, {Label: "부가세(원)", Kind: export.KindMoney}, {Label: "합계(원)", Kind: export.KindMoney}}}
	case "unlinked":
		return export.Dataset{Code: "vat_purchase_unlinked", Label: "부가세매입_미연결세금계산서", Columns: []export.Column{{Label: "세금계산서 번호"}, {Label: "승인번호"}, {Label: "공급자"}, {Label: "작성일", Kind: export.KindDate},
			{Label: "공급가액(원)", Kind: export.KindMoney}, {Label: "세액(원)", Kind: export.KindMoney}, {Label: "대조 상태"}}}
	case "diff":
		return export.Dataset{Code: "vat_purchase_diff", Label: "부가세매입_세액차이", Columns: []export.Column{{Label: "세금계산서 번호"}, {Label: "작성일", Kind: export.KindDate}, {Label: "연결된 전표"},
			{Label: "세금계산서 세액(원)", Kind: export.KindMoney}, {Label: "전표 부가세(원)", Kind: export.KindMoney}, {Label: "차이(원)", Kind: export.KindMoney}}}
	case "unreceived":
		return export.Dataset{Code: "vat_purchase_unreceived", Label: "부가세매입_세금계산서미수취", Columns: []export.Column{{Label: "전표 번호"}, {Label: "거래처"}, {Label: "거래일", Kind: export.KindDate},
			{Label: "공급가액(원)", Kind: export.KindMoney}, {Label: "부가세(원)", Kind: export.KindMoney}}}
	}
	return export.Dataset{Code: "vat_purchase_vendors", Label: "부가세매입_매입처별", Columns: []export.Column{{Label: "거래처"}, {Label: "사업자등록번호"}, {Label: "건수", Kind: export.KindInt},
		{Label: "공급가액(원)", Kind: export.KindMoney}, {Label: "부가세(원)", Kind: export.KindMoney}}}
}

func (s *vatSource) Filters(q url.Values) []export.FilterItem {
	return []export.FilterItem{export.Code("집계 단위", q.Get("unit")), export.Month("월", q.Get("month")), export.ID("연도", q.Get("year")), export.ID("분기", q.Get("quarter")),
		export.Date("시작일", q.Get("from")), export.Date("종료일", q.Get("to")), export.ID("사업장", q.Get("siteId"))}
}

func (s *vatSource) rows(ctx context.Context, q url.Values) ([][]export.Cell, error) {
	v, err := s.svc.PurchaseSummary(ctx, vatFilterOf(q))
	if err != nil {
		return nil, err
	}
	var out [][]export.Cell
	switch s.table {
	case "cards":
		for _, c := range v.Cards {
			kind, reason := "공제", ""
			if c.Kind != "deductible" {
				kind, reason = "불공제", c.ReasonLabel
			}
			out = append(out, []export.Cell{kind, reason, c.Count, c.Supply, c.VAT, c.Total})
		}
		out = append(out, []export.Cell{"공제 부가세 합계", nil, nil, nil, v.CardDeductVAT, nil})
	case "unlinked":
		for _, u := range v.Unlinked {
			out = append(out, []export.Cell{u.InvoiceNo, u.ApprovalNo, u.Supplier, u.IssueDate, u.Supply, u.VAT, u.State})
		}
	case "diff":
		for _, d := range v.Diffs {
			out = append(out, []export.Cell{d.InvoiceNo, d.IssueDate, d.JournalNos, d.InvoiceVAT, d.JournalVAT, d.Diff})
		}
	case "unreceived":
		for _, u := range v.Unreceived {
			out = append(out, []export.Cell{u.JournalNo, u.VendorName, u.TradeDate, u.Supply, u.VAT})
		}
		out = append(out, []export.Cell{"합계", nil, nil, nil, v.UnreceivedVAT})
	default:
		var cnt int
		var sup int64
		for _, x := range v.Vendors {
			out = append(out, []export.Cell{x.VendorName, x.BusinessNo, x.Count, x.Supply, x.VAT})
			cnt += x.Count
			sup += x.Supply
		}
		out = append(out, []export.Cell{"합계", nil, cnt, sup, v.VendorVAT})
	}
	return out, nil
}

func (s *vatSource) Count(ctx context.Context, q url.Values, _ int) (int, bool, error) {
	r, err := s.rows(ctx, q)
	return len(r), false, err
}

func (s *vatSource) Rows(ctx context.Context, q url.Values, _ bool, max int) ([][]export.Cell, error) {
	r, err := s.rows(ctx, q)
	if len(r) > max+1 {
		r = r[:max+1]
	}
	return r, err
}
