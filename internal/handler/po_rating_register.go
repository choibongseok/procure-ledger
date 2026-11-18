// 발주서 출력과 거래처 평가 라우트(B25 단계 3, 3.3·3.4·9절). 권한은 등록기가 선언하고 서비스가 한 번 더 확인한다.
package handler

import (
	"net/http"
	"strconv"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/deps"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
)

// RegisterPOAndRating 은 /api/purchase-requests/{id}/po 와 /api/vendors/rating 라우트를 등록한다. 호출: cmd/server/routes_erp.go.
func RegisterPOAndRating(r contract.Registrar, d deps.Deps) {
	if d.POPrint != nil {
		h := poPrintHandler{svc: d.POPrint}
		r.HandleFunc("GET", "/api/purchase-requests/{id}/po", contract.PermPurchaseRead, h.get)
		r.HandleFunc("POST", "/api/purchase-requests/{id}/po/print", contract.PermPurchaseRead, h.print)
	}
	if d.VendorRating != nil {
		h := ratingHandler{svc: d.VendorRating}
		r.HandleFunc("GET", "/api/vendors/rating", contract.PermVendorRatingRead, h.list)
		r.HandleFunc("GET", "/api/vendors/{id}/rating", contract.PermVendorRatingRead, h.detail)
	}
}

type poPrintHandler struct{ svc domain.POPrintService }

func (h poPrintHandler) get(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	p, err := h.svc.Print(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	lines := make([]map[string]any, len(p.Lines))
	for i, l := range p.Lines {
		lines[i] = map[string]any{"no": l.No, "itemName": l.ItemName, "unit": l.Unit, "quantity": l.Quantity, "unitPrice": l.UnitPrice, "amount": l.Amount}
	}
	writeJSON(w, http.StatusOK, map[string]any{"prId": p.PRID, "prNo": p.PRNo, "poNo": p.PONo, "poDate": ts(p.PODate), "company": p.Company, "companyBizNo": p.CompanyBizNo, "companyRep": p.CompanyRep, "companyAddr": p.CompanyAddr, "companyPhone": p.CompanyPhone,
		"hasLogo": p.HasLogo, "hasSeal": p.HasSeal, "department": p.Department,
		"requester": p.Requester, "supplierName": p.SupplierName, "businessNo": p.BusinessNo, "phone": p.Phone, "email": p.Email, "contactEmpty": p.ContactEmpty,
		"masked": p.Masked, "deliveryPlace": p.DeliveryPlace, "dueDate": p.DueDate, "lines": lines, "supply": p.Supply, "vatRate": p.VATRate, "vat": p.VAT, "total": p.Total, "note": p.Note})
}

func (h poPrintHandler) print(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	if err := h.svc.RecordPrint(r.Context(), id); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type ratingHandler struct{ svc domain.VendorRatingService }

func ratingDTO(v domain.VendorRating) map[string]any {
	return map[string]any{"vendorId": v.VendorID, "vendorName": v.VendorName, "orders": v.Orders, "onTime": v.OnTime, "withDue": v.WithDue, "noDue": v.NoDue,
		"onTimePct": v.OnTimePct, "onTimeBadge": v.OnTimeBadge, "verdicts": v.Verdicts, "mismatches": v.Mismatches, "mismatchPct": v.MismatchPct,
		"mismatchBadge": v.MismatchBadge, "lateOpen": v.LateOpen}
}

func (h ratingHandler) list(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	rows, err := h.svc.Rate(r.Context(), contract.Day(q.Get("from")), contract.Day(q.Get("to")))
	if err != nil {
		writeErr(w, err)
		return
	}
	out := make([]map[string]any, len(rows))
	for i, v := range rows {
		out[i] = ratingDTO(v)
	}
	writeJSON(w, http.StatusOK, out)
}

func (h ratingHandler) detail(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		http.Error(w, "경로의 번호가 올바르지 않습니다.", http.StatusBadRequest)
		return
	}
	q := r.URL.Query()
	d, err := h.svc.Detail(r.Context(), id, contract.Day(q.Get("from")), contract.Day(q.Get("to")))
	if err != nil {
		writeErr(w, err)
		return
	}
	late := func(in []domain.VendorRatingLate) []map[string]any {
		out := make([]map[string]any, len(in))
		for i, l := range in {
			out[i] = map[string]any{"poNo": l.PONo, "dueDate": l.DueDate, "receivedOn": l.ReceivedOn, "daysLate": l.DaysLate}
		}
		return out
	}
	mis := make([]map[string]any, len(d.Mismatched))
	for i, m := range d.Mismatched {
		mis[i] = map[string]any{"verdictId": m.VerdictID, "documentId": m.DocumentID, "poNo": m.PONo, "confirmedOn": m.ConfirmedOn}
	}
	out := ratingDTO(d.VendorRating)
	out["late"], out["openLate"], out["mismatched"] = late(d.Late), late(d.OpenLate), mis
	writeJSON(w, http.StatusOK, out)
}
