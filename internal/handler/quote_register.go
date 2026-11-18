// 견적서·견적 비교 라우트(B25 단계 2, 8·9절). 권한은 등록기가 선언하고 서비스가 한 번 더 확인한다.
package handler

import (
	"io"
	"net/http"
	"strconv"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/deps"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
)

// RegisterQuotes 는 /api/purchase-requests/{id}/quotes 와 /api/quotes/{id} 라우트를 등록한다. 호출: cmd/server/routes_erp.go.
func RegisterQuotes(r contract.Registrar, d deps.Deps) {
	if d.Quotes == nil {
		return
	}
	limit := d.AttachMaxFileBytes
	if limit <= 0 {
		limit = 10 << 20
	}
	h := quoteHandler{svc: d.Quotes, limit: limit}
	r.HandleFunc("GET", "/api/purchase-requests/{id}/quotes", contract.PermPurchaseRead, h.compare)
	r.HandleFunc("POST", "/api/purchase-requests/{id}/quotes", contract.PermPurchaseCreate, h.attach)
	r.HandleFunc("PUT", "/api/purchase-requests/{id}/quote-selection", contract.PermPurchaseCreate, h.selectQuote)
	r.HandleFunc("PUT", "/api/quotes/{id}", contract.PermPurchaseCreate, h.update)
	r.HandleFunc("DELETE", "/api/quotes/{id}", contract.PermPurchaseCreate, h.remove)
}

type quoteHandler struct {
	svc   domain.QuoteService
	limit int64
}

type quoteLineBody struct {
	Name      string `json:"name"`
	Quantity  int64  `json:"quantity"`
	UnitPrice int64  `json:"unitPrice"`
}

func quoteDTO(q domain.Quote) map[string]any {
	lines := make([]map[string]any, len(q.Lines))
	for i, l := range q.Lines {
		lines[i] = map[string]any{"name": l.Name, "quantity": l.Quantity, "unitPrice": l.UnitPrice, "amount": l.Amount}
	}
	var docTotal, leadDays any
	if q.HasDocTotal {
		docTotal = q.DocTotal
	}
	if q.HasLead {
		leadDays = q.LeadDays
	}
	warns := q.Warnings
	if warns == nil {
		warns = []string{}
	}
	// 원문 텍스트는 내보내지 않는다.
	return map[string]any{"id": q.ID, "prId": q.PRID, "vendorId": q.VendorID, "vendorName": q.VendorName, "supplier": q.Supplier, "filename": q.Filename,
		"status": q.Status, "edited": q.Edited, "lines": lines, "total": q.Total, "docTotal": docTotal, "leadDays": leadDays, "leadText": q.LeadText,
		"warnings": warns, "filled": q.Filled, "selected": q.Selected, "lowest": q.Lowest, "overLowest": q.OverLowest, "createdAt": ts(q.CreatedAt)}
}

func compareDTO(c domain.QuoteCompare) map[string]any {
	qs := make([]map[string]any, len(c.Quotes))
	for i, q := range c.Quotes {
		qs[i] = quoteDTO(q)
	}
	return map[string]any{"quotes": qs, "editable": c.Editable, "selectedId": c.SelectedID, "reason": c.Reason, "reasonRequired": c.ReasonRequired,
		"lowestTotal": c.LowestTotal, "prTotal": c.PRTotal, "totalMismatch": c.TotalMismatch, "note": c.Note}
}

func (h quoteHandler) compare(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	c, err := h.svc.Compare(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, compareDTO(c))
}

// attach 는 multipart 파일 하나를 받는다. 본문은 상한 + 1MB 에서 서버가 먼저 자른다.
func (h quoteHandler) attach(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, h.limit+(1<<20))
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		http.Error(w, "견적서는 파일당 "+strconv.FormatInt(h.limit>>20, 10)+"MB 까지 올릴 수 있습니다.", http.StatusRequestEntityTooLarge)
		return
	}
	file, hdr, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "파일을 선택하세요.", http.StatusBadRequest)
		return
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, h.limit+1))
	if err != nil {
		http.Error(w, "파일을 읽을 수 없습니다.", http.StatusBadRequest)
		return
	}
	q, err := h.svc.Attach(r.Context(), id, hdr.Filename, hdr.Header.Get("Content-Type"), data)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, quoteDTO(q))
}

func (h quoteHandler) update(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	var b struct {
		VendorID int64           `json:"vendorId"`
		Lines    []quoteLineBody `json:"lines"`
		DocTotal *int64          `json:"docTotal"`
		LeadDays *int            `json:"leadDays"`
		LeadText string          `json:"leadText"`
	}
	if !decode(w, r, &b) {
		return
	}
	in := domain.QuoteInput{VendorID: b.VendorID, DocTotal: b.DocTotal, LeadDays: b.LeadDays, LeadText: b.LeadText}
	for _, l := range b.Lines {
		in.Lines = append(in.Lines, domain.QuoteLine{Name: l.Name, Quantity: l.Quantity, UnitPrice: l.UnitPrice})
	}
	q, err := h.svc.Update(r.Context(), id, in)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, quoteDTO(q))
}

func (h quoteHandler) remove(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	if err := h.svc.Remove(r.Context(), id); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h quoteHandler) selectQuote(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	var b struct {
		QuoteID int64  `json:"quoteId"`
		Reason  string `json:"reason"`
	}
	if !decode(w, r, &b) {
		return
	}
	c, err := h.svc.Select(r.Context(), id, b.QuoteID, b.Reason)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, compareDTO(c))
}
