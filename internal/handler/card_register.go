// 법인카드 라우트(B32 단계 2, 9절). 권한은 등록기가 선언하고 서비스가 본인 범위(소지자·조직 범위)를 한 번 더 판정한다.
// 응답에는 카드번호 조각이 없다(카드 별칭만).
package handler

import (
	"io"
	"net/http"
	"strconv"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/deps"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
)

const cardUploadLimit = 5 << 20

// RegisterCards 는 /api/finance/cards 아래 라우트를 등록한다. 호출: cmd/server/routes_erp.go.
func RegisterCards(r contract.Registrar, d deps.Deps) {
	if d.CardSvc == nil {
		return
	}
	h := cardHandler{svc: d.CardSvc}
	r.HandleFunc("GET", "/api/finance/cards", contract.PermCardReadAll, h.cards)
	r.HandleFunc("POST", "/api/finance/cards", contract.PermCardManage, h.createCard)
	r.HandleFunc("PUT", "/api/finance/cards/{id}", contract.PermCardManage, h.updateCard)
	r.HandleFunc("POST", "/api/finance/cards/{id}/holder", contract.PermCardHolderManage, h.holder)
	r.HandleFunc("GET", "/api/finance/card-holders/{id}", contract.PermCardReadAll, h.holderHistory)
	r.HandleFunc("POST", "/api/finance/cards/{id}/active", contract.PermCardManage, h.active)
	r.HandleFunc("POST", "/api/finance/cards/import/preview", contract.PermCardImport, h.preview)
	r.HandleFunc("POST", "/api/finance/cards/import/confirm", contract.PermCardImport, h.confirmImport)
	r.HandleFunc("GET", "/api/finance/cards/overview", contract.PermCardReadAll, h.overview)
	r.HandleFunc("GET", "/api/finance/cards/txns", contract.PermCardReadAll, h.all)
	r.HandleFunc("GET", "/api/finance/cards/txns/{id}", contract.PermCardReadAll, h.get)
	r.HandleFunc("POST", "/api/finance/cards/txns/{id}/refund-request", contract.PermCardManage, h.refundRequest)
	r.HandleFunc("POST", "/api/finance/cards/txns/{id}/refund-done", contract.PermCardManage, h.refundDone)
	r.HandleFunc("POST", "/api/finance/cards/txns/{id}/reassign", contract.PermCardHolderManage, h.reassign)
	r.HandleFunc("GET", "/api/finance/cards/mine", contract.PermCardExplain, h.mine)
	r.HandleFunc("GET", "/api/finance/cards/mine/{id}", contract.PermCardExplain, h.getMine)
	r.HandleFunc("POST", "/api/finance/cards/mine/{id}/explain", contract.PermCardExplain, h.explain)
	r.HandleFunc("POST", "/api/finance/cards/mine/{id}/withdraw", contract.PermCardExplain, h.withdraw)
	r.HandleFunc("GET", "/api/finance/cards/explain-accounts", contract.PermCardExplain, h.explainAccounts)
	r.HandleFunc("GET", "/api/finance/cards/confirm", contract.PermCardConfirm, h.toConfirm)
	r.HandleFunc("POST", "/api/finance/cards/confirm", contract.PermCardConfirm, h.confirm)
	r.HandleFunc("GET", "/api/finance/cards/confirm/{id}", contract.PermCardConfirm, h.get)
	r.HandleFunc("POST", "/api/finance/cards/confirm/{id}/return", contract.PermCardConfirm, h.returnTxn)
}

type cardHandler struct{ svc domain.CardService }

func corpCardDTO(c domain.Card) map[string]any {
	return map[string]any{"id": c.ID, "alias": c.Alias, "issuer": c.Issuer, "issuerLabel": c.IssuerLabel, "holderId": c.HolderID, "holderName": c.HolderName, "active": c.Active}
}

func ptrInt(p *int64) any {
	if p == nil {
		return nil
	}
	return *p
}

func explanationDTO(e domain.CardExplanation) map[string]any {
	att := make([]map[string]any, len(e.Attachments))
	for i, a := range e.Attachments {
		att[i] = map[string]any{"id": a.ID, "filename": a.Filename, "size": a.Size}
	}
	return map[string]any{"id": e.ID, "purpose": e.Purpose, "accountCode": e.AccountCode, "accountName": e.AccountName, "vatDeduct": e.VATDeduct, "vatReason": e.VATReason, "vatReasonLabel": domain.CardVATReasons[e.VATReason],
		"supply": e.Supply, "vat": e.VAT, "personal": e.Personal, "status": e.Status, "submittedAt": rfc(e.SubmittedAt), "confirmedBy": e.ConfirmedBy, "returnReason": e.ReturnReason, "attachments": att}
}

func txnDTO(t domain.CardTxn) map[string]any {
	m := map[string]any{"id": t.ID, "cardId": t.CardID, "cardAlias": t.CardAlias, "holderId": t.HolderID, "holderName": t.HolderName, "usedAt": rfc(t.UsedAt), "merchant": t.Merchant,
		"amount": t.Amount, "supply": ptrInt(t.Supply), "vat": ptrInt(t.VAT), "tip": ptrInt(t.Tip), "kind": t.Kind, "status": t.Status, "statusLabel": domain.CardTxnStatusLabels[t.Status],
		"dueDate": t.DueDate, "daysLeft": t.DaysLeft, "overdue": t.Overdue, "journalNo": t.JournalNo, "refundNote": t.RefundNote}
	if t.Explanation != nil {
		m["explanation"] = explanationDTO(*t.Explanation)
	}
	if len(t.History) > 0 {
		hist := make([]map[string]any, len(t.History))
		for i, e := range t.History {
			hist[i] = explanationDTO(e)
		}
		m["history"] = hist
	}
	return m
}

func txnList(ts []domain.CardTxn) []map[string]any {
	out := make([]map[string]any, len(ts))
	for i, t := range ts {
		out[i] = txnDTO(t)
	}
	return out
}

func (h cardHandler) cards(w http.ResponseWriter, r *http.Request) {
	cs, err := h.svc.Cards(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	out := make([]map[string]any, len(cs))
	for i, c := range cs {
		out[i] = corpCardDTO(c)
	}
	writeJSON(w, http.StatusOK, out)
}

type cardBody struct {
	Alias    string `json:"alias"`
	Issuer   string `json:"issuer"`
	Last4    string `json:"last4"`
	HolderID int64  `json:"holderId"`
}

func (h cardHandler) createCard(w http.ResponseWriter, r *http.Request) {
	var b cardBody
	if !decode(w, r, &b) {
		return
	}
	c, err := h.svc.SaveCard(r.Context(), 0, domain.CardInput{Alias: b.Alias, Issuer: b.Issuer, Last4: b.Last4, HolderID: b.HolderID})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, corpCardDTO(c))
}

func (h cardHandler) updateCard(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	var b cardBody
	if !decode(w, r, &b) {
		return
	}
	c, err := h.svc.SaveCard(r.Context(), id, domain.CardInput{Alias: b.Alias})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, corpCardDTO(c))
}

func (h cardHandler) holder(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	var b struct {
		HolderID int64 `json:"holderId"`
	}
	if !decode(w, r, &b) {
		return
	}
	c, err := h.svc.ChangeHolder(r.Context(), id, b.HolderID)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, corpCardDTO(c))
}

func (h cardHandler) holderHistory(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	hs, err := h.svc.HolderHistory(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	out := make([]map[string]any, len(hs))
	for i, x := range hs {
		out[i] = map[string]any{"holderName": x.HolderName, "fromDate": x.FromDate, "changedBy": x.ChangedBy, "changedAt": rfc(x.ChangedAt)}
	}
	writeJSON(w, http.StatusOK, out)
}

func (h cardHandler) active(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	var b struct {
		Active bool `json:"active"`
	}
	if !decode(w, r, &b) {
		return
	}
	c, err := h.svc.SetCardActive(r.Context(), id, b.Active)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, corpCardDTO(c))
}

// readUpload 는 멀티파트의 file 과 format 을 읽는다(5MB 상한).
func readUpload(w http.ResponseWriter, r *http.Request) (format string, data []byte, ok bool) {
	r.Body = http.MaxBytesReader(w, r.Body, cardUploadLimit+(1<<20))
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		http.Error(w, "파일은 5MB 까지 올릴 수 있습니다.", http.StatusRequestEntityTooLarge)
		return "", nil, false
	}
	file, _, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "파일을 선택하세요.", http.StatusBadRequest)
		return "", nil, false
	}
	defer file.Close()
	data, err = io.ReadAll(io.LimitReader(file, cardUploadLimit+1))
	if err != nil || len(data) > cardUploadLimit {
		http.Error(w, "파일은 5MB 까지 올릴 수 있습니다.", http.StatusRequestEntityTooLarge)
		return "", nil, false
	}
	return r.FormValue("format"), data, true
}

func (h cardHandler) preview(w http.ResponseWriter, r *http.Request) {
	format, data, ok := readUpload(w, r)
	if !ok {
		return
	}
	p, err := h.svc.PreviewImport(r.Context(), format, data)
	if err != nil {
		writeErr(w, err)
		return
	}
	errs := make([]map[string]any, len(p.ErrorRows))
	for i, e := range p.ErrorRows {
		errs[i] = map[string]any{"rowNo": e.RowNo, "reason": e.Reason}
	}
	if len(errs) > 100 {
		errs = errs[:100]
	}
	writeJSON(w, http.StatusOK, map[string]any{"format": p.Format, "formatLabel": p.FormatLabel, "total": p.Total, "new": p.New, "duplicate": p.Duplicate, "errors": p.Errors,
		"errorRows": errs, "totalAmount": p.TotalAmount, "unregistered": p.Unregistered, "warnings": p.Warnings})
}

func (h cardHandler) confirmImport(w http.ResponseWriter, r *http.Request) {
	format, data, ok := readUpload(w, r)
	if !ok {
		return
	}
	res, err := h.svc.ConfirmImport(r.Context(), format, data)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"importId": res.ImportID, "total": res.Total, "new": res.New, "duplicate": res.Duplicate, "errors": res.Errors})
}

func cardFilterOf(r *http.Request) domain.CardFilter {
	q := r.URL.Query()
	off, _ := strconv.Atoi(q.Get("offset"))
	return domain.CardFilter{Status: q.Get("status"), From: q.Get("from"), To: q.Get("to"), Offset: off}
}

func (h cardHandler) mine(w http.ResponseWriter, r *http.Request) {
	ts, err := h.svc.Mine(r.Context(), cardFilterOf(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, txnList(ts))
}

func (h cardHandler) all(w http.ResponseWriter, r *http.Request) {
	ts, total, err := h.svc.All(r.Context(), cardFilterOf(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"rows": txnList(ts), "total": total})
}

func (h cardHandler) get(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	t, err := h.svc.GetTxn(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, txnDTO(t))
}

func (h cardHandler) getMine(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	t, err := h.svc.GetMyTxn(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, txnDTO(t))
}

func (h cardHandler) explain(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	var b struct {
		Purpose       string  `json:"purpose"`
		AccountCode   string  `json:"accountCode"`
		VATDeduct     *bool   `json:"vatDeduct"`
		VATReason     string  `json:"vatReason"`
		Personal      bool    `json:"personal"`
		AttachmentIDs []int64 `json:"attachmentIds"`
	}
	if !decode(w, r, &b) {
		return
	}
	t, err := h.svc.Explain(r.Context(), id, domain.ExplainInput{Purpose: b.Purpose, AccountCode: b.AccountCode, VATDeduct: b.VATDeduct, VATReason: b.VATReason, Personal: b.Personal, AttachmentIDs: b.AttachmentIDs})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, txnDTO(t))
}

func (h cardHandler) withdraw(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	t, err := h.svc.Withdraw(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, txnDTO(t))
}

func (h cardHandler) explainAccounts(w http.ResponseWriter, r *http.Request) {
	as, err := h.svc.ExplainAccounts(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	out := make([]map[string]any, len(as))
	for i, a := range as {
		out[i] = map[string]any{"code": a.Code, "name": a.Name}
	}
	writeJSON(w, http.StatusOK, out)
}

func (h cardHandler) toConfirm(w http.ResponseWriter, r *http.Request) {
	ts, err := h.svc.ToConfirm(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, txnList(ts))
}

func (h cardHandler) confirm(w http.ResponseWriter, r *http.Request) {
	var b struct {
		IDs []int64 `json:"ids"`
	}
	if !decode(w, r, &b) {
		return
	}
	res, err := h.svc.Confirm(r.Context(), b.IDs)
	if err != nil {
		writeErr(w, err)
		return
	}
	out := make([]map[string]any, len(res))
	for i, x := range res {
		out[i] = map[string]any{"id": x.ID, "ok": x.OK, "error": x.Error, "journalNo": x.JournalNo}
	}
	writeJSON(w, http.StatusOK, out)
}

func (h cardHandler) returnTxn(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	var b struct {
		Reason string `json:"reason"`
	}
	if !decode(w, r, &b) {
		return
	}
	t, err := h.svc.Return(r.Context(), id, b.Reason)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, txnDTO(t))
}

func (h cardHandler) overview(w http.ResponseWriter, r *http.Request) {
	o, err := h.svc.Overview(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	labels := map[string]any{}
	for k, v := range o.Counts {
		labels[k] = map[string]any{"count": v, "amount": o.Amounts[k], "label": domain.CardTxnStatusLabels[k]}
	}
	writeJSON(w, http.StatusOK, map[string]any{"statuses": labels, "delayed": txnList(o.Delayed), "noHolder": txnList(o.NoHolder), "needsFinance": txnList(o.NeedsFinance), "refundRequested": txnList(o.RefundRequested)})
}

func (h cardHandler) refundRequest(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	var b struct {
		Reason string `json:"reason"`
	}
	if !decode(w, r, &b) {
		return
	}
	t, err := h.svc.RequestRefund(r.Context(), id, b.Reason)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, txnDTO(t))
}

func (h cardHandler) refundDone(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	var b struct {
		RefundDate string `json:"refundDate"`
		Note       string `json:"note"`
	}
	if !decode(w, r, &b) {
		return
	}
	t, err := h.svc.CompleteRefund(r.Context(), id, b.RefundDate, b.Note)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, txnDTO(t))
}

func (h cardHandler) reassign(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	var b struct {
		HolderID int64 `json:"holderId"`
	}
	if !decode(w, r, &b) {
		return
	}
	t, err := h.svc.ReassignHolder(r.Context(), id, b.HolderID)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, txnDTO(t))
}
