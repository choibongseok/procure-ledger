// 계정과목·전표 라우트(B31, 9절). 권한은 등록기가 선언하고 서비스가 한 번 더 확인한다(핸들러에 역할 분기 없음).
package handler

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/deps"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
)

// RegisterLedger 는 /api/finance/accounts·account-rules·journals 를 등록한다. 호출: cmd/server/routes_erp.go.
func RegisterLedger(r contract.Registrar, d deps.Deps) {
	registerLedgerReports(r, d)
	if d.Accounts != nil {
		h := accountHandler{svc: d.Accounts}
		r.HandleFunc("GET", "/api/finance/accounts", contract.PermLedgerRead, h.list)
		r.HandleFunc("POST", "/api/finance/accounts", contract.PermAccountManage, h.create)
		r.HandleFunc("PUT", "/api/finance/accounts/{code}", contract.PermAccountManage, h.rename)
		r.HandleFunc("POST", "/api/finance/accounts/{code}/active", contract.PermAccountManage, h.setActive)
		r.HandleFunc("GET", "/api/finance/account-rules", contract.PermLedgerRead, h.rules)
		r.HandleFunc("PUT", "/api/finance/account-rules/{key}", contract.PermAccountManage, h.setRule)
	}
	if d.Journals != nil {
		h := journalHandler{svc: d.Journals}
		r.HandleFunc("GET", "/api/finance/journals", contract.PermLedgerRead, h.list)
		r.HandleFunc("POST", "/api/finance/journals", contract.PermLedgerWrite, h.create)
		r.HandleFunc("GET", "/api/finance/journals/{no}", contract.PermLedgerRead, h.get)
		r.HandleFunc("PUT", "/api/finance/journals/{no}", contract.PermLedgerWrite, h.update)
		r.HandleFunc("POST", "/api/finance/journals/{no}/request", contract.PermLedgerWrite, h.request)
		r.HandleFunc("POST", "/api/finance/journals/{no}/cancel", contract.PermLedgerWrite, h.cancel)
		r.HandleFunc("POST", "/api/finance/journals/{no}/reverse", contract.PermLedgerWrite, h.reverse)
		r.HandleFunc("POST", "/api/finance/journals/request-batch", contract.PermLedgerWrite, h.requestBatch)
	}
	if d.JournalQueue != nil {
		h := queueHandler{svc: d.JournalQueue}
		r.HandleFunc("GET", "/api/finance/journal-queue", contract.PermLedgerRead, h.list)
		r.HandleFunc("POST", "/api/finance/journal-queue/{id}/retry", contract.PermLedgerWrite, h.retry)
		r.HandleFunc("POST", "/api/finance/journal-queue/{id}/manual", contract.PermLedgerWrite, h.manual)
		r.HandleFunc("POST", "/api/finance/journal-queue/{id}/skip", contract.PermLedgerWrite, h.skip)
	}
}

type accountHandler struct{ svc domain.AccountService }

func accountDTO(a domain.Account) map[string]any {
	return map[string]any{"id": a.ID, "code": a.Code, "name": a.Name, "kind": a.Kind, "kindLabel": domain.AccountKindLabels[a.Kind], "active": a.Active, "system": a.System}
}

func (h accountHandler) list(w http.ResponseWriter, r *http.Request) {
	as, err := h.svc.List(r.Context(), r.URL.Query().Get("all") == "1")
	if err != nil {
		writeErr(w, err)
		return
	}
	out := make([]map[string]any, len(as))
	for i, a := range as {
		out[i] = accountDTO(a)
	}
	writeJSON(w, http.StatusOK, out)
}

func (h accountHandler) create(w http.ResponseWriter, r *http.Request) {
	var b struct{ Code, Name, Kind string }
	if !decode(w, r, &b) {
		return
	}
	a, err := h.svc.Create(r.Context(), domain.AccountInput{Code: b.Code, Name: b.Name, Kind: b.Kind})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, accountDTO(a))
}

func (h accountHandler) rename(w http.ResponseWriter, r *http.Request) {
	var b struct{ Name string }
	if !decode(w, r, &b) {
		return
	}
	a, err := h.svc.Rename(r.Context(), r.PathValue("code"), b.Name)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, accountDTO(a))
}

func (h accountHandler) setActive(w http.ResponseWriter, r *http.Request) {
	var b struct{ Active bool }
	if !decode(w, r, &b) {
		return
	}
	a, err := h.svc.SetActive(r.Context(), r.PathValue("code"), b.Active)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, accountDTO(a))
}

func (h accountHandler) rules(w http.ResponseWriter, r *http.Request) {
	rs, err := h.svc.Rules(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	out := make([]map[string]any, len(rs))
	for i, x := range rs {
		out[i] = map[string]any{"key": x.Key, "label": x.Label, "accountCode": x.AccountCode, "accountName": x.AccountName, "active": x.Active}
	}
	writeJSON(w, http.StatusOK, out)
}

func (h accountHandler) setRule(w http.ResponseWriter, r *http.Request) {
	var b struct {
		AccountCode string `json:"accountCode"`
	}
	if !decode(w, r, &b) {
		return
	}
	if err := h.svc.SetRule(r.Context(), r.PathValue("key"), b.AccountCode); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type journalHandler struct{ svc domain.JournalService }

func journalDTO(j domain.Journal, withLines bool) map[string]any {
	m := map[string]any{"id": j.ID, "no": j.No, "kind": j.Kind, "kindLabel": domain.JournalKindLabels[j.Kind], "status": j.Status, "statusLabel": domain.JournalStatusLabels[j.Status],
		"postingDate": j.PostingDate, "tradeDate": j.TradeDate, "memo": j.Memo, "totalAmount": j.TotalAmount, "reversesNo": j.ReversesNo, "reversedByNo": j.ReversedByNo, "corrected": j.Corrected,
		"docId": j.DocID, "docNo": j.DocNo, "createdBy": j.CreatedBy, "createdAt": rfc(j.CreatedAt), "approvedBy": j.ApprovedBy, "cancelReason": j.CancelReason,
		"sourceType": j.SourceType, "sourceId": j.SourceID}
	if j.ApprovedAt != nil {
		m["approvedAt"] = rfc(*j.ApprovedAt)
	}
	// 사업장·법인 간 거래 표시는 값이 있을 때만 싣는다(법인·사업장이 하나뿐인 설치의 응답은 그대로다).
	if j.SiteID != 0 {
		m["siteId"], m["siteName"] = j.SiteID, j.SiteName
	}
	if j.Intercompany != "" {
		m["intercompany"] = j.Intercompany
	}
	if withLines {
		lines := make([]map[string]any, len(j.Lines))
		for i, l := range j.Lines {
			lines[i] = map[string]any{"seq": l.Seq, "accountCode": l.AccountCode, "accountName": l.AccountName, "side": l.Side, "amount": l.Amount, "vendorId": l.VendorID, "vendorName": l.VendorName, "memo": l.Memo}
		}
		m["lines"] = lines
		ev := make([]map[string]any, len(j.Evidence))
		for i, e := range j.Evidence {
			ev[i] = map[string]any{"kind": e.Kind, "refId": e.RefID}
		}
		m["evidence"] = ev
	}
	return m
}

type journalBody struct {
	Kind        string `json:"kind"`
	PostingDate string `json:"postingDate"`
	TradeDate   string `json:"tradeDate"`
	Memo        string `json:"memo"`
	SiteID      int64  `json:"siteId"`
	Lines       []struct {
		AccountCode string `json:"accountCode"`
		Side        string `json:"side"`
		Amount      int64  `json:"amount"`
		VendorID    int64  `json:"vendorId"`
		Memo        string `json:"memo"`
	} `json:"lines"`
}

func (b journalBody) input() domain.JournalInput {
	in := domain.JournalInput{Kind: b.Kind, PostingDate: b.PostingDate, TradeDate: b.TradeDate, Memo: b.Memo, SiteID: b.SiteID}
	for _, l := range b.Lines {
		in.Lines = append(in.Lines, domain.JournalLineInput{AccountCode: l.AccountCode, Side: l.Side, Amount: l.Amount, VendorID: l.VendorID, Memo: l.Memo})
	}
	return in
}

func journalFilterOf(q url.Values) domain.JournalFilter {
	vid, _ := strconv.ParseInt(q.Get("vendorId"), 10, 64)
	off, _ := strconv.Atoi(q.Get("offset"))
	return domain.JournalFilter{Status: q.Get("status"), Kind: q.Get("kind"), From: q.Get("from"), To: q.Get("to"), AccountCode: strings.TrimSpace(q.Get("account")), VendorID: vid, Offset: off}
}

func (h journalHandler) list(w http.ResponseWriter, r *http.Request) {
	pg, err := h.svc.List(r.Context(), journalFilterOf(r.URL.Query()))
	if err != nil {
		writeErr(w, err)
		return
	}
	rows := make([]map[string]any, len(pg.Rows))
	for i, j := range pg.Rows {
		rows[i] = journalDTO(j, false)
	}
	writeJSON(w, http.StatusOK, map[string]any{"rows": rows, "total": pg.Total})
}

func (h journalHandler) get(w http.ResponseWriter, r *http.Request) {
	j, err := h.svc.Get(r.Context(), r.PathValue("no"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, journalDTO(j, true))
}

func (h journalHandler) create(w http.ResponseWriter, r *http.Request) {
	var b journalBody
	if !decode(w, r, &b) {
		return
	}
	j, err := h.svc.Create(r.Context(), b.input())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, journalDTO(j, true))
}

func (h journalHandler) update(w http.ResponseWriter, r *http.Request) {
	var b journalBody
	if !decode(w, r, &b) {
		return
	}
	j, err := h.svc.Update(r.Context(), r.PathValue("no"), b.input())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, journalDTO(j, true))
}

func (h journalHandler) request(w http.ResponseWriter, r *http.Request) {
	j, err := h.svc.RequestApproval(r.Context(), r.PathValue("no"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, journalDTO(j, true))
}

func (h journalHandler) requestBatch(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Nos []string `json:"nos"`
	}
	if !decode(w, r, &b) {
		return
	}
	res, err := h.svc.RequestApprovalBatch(r.Context(), b.Nos)
	if err != nil {
		writeErr(w, err)
		return
	}
	out := make([]map[string]any, len(res))
	for i, x := range res {
		out[i] = map[string]any{"no": x.No, "ok": x.OK, "error": x.Error}
	}
	writeJSON(w, http.StatusOK, out)
}

type queueHandler struct{ svc domain.JournalQueueService }

func queueDTO(q domain.QueueItem) map[string]any {
	m := map[string]any{"id": q.ID, "sourceType": q.SourceType, "sourceId": q.SourceID, "sourceLabel": q.SourceLabel, "reason": q.Reason, "reasonLabel": domain.QueueReasonLabels[q.Reason],
		"reasonText": q.ReasonText, "poNo": q.PONo, "vendorName": q.VendorName, "status": q.Status, "statusLabel": domain.QueueStatusLabels[q.Status], "journalNo": q.JournalNo,
		"handledBy": q.HandledBy, "note": q.Note, "createdAt": rfc(q.CreatedAt)}
	if q.Amount != nil {
		m["amount"] = *q.Amount
	}
	if q.HandledAt != nil {
		m["handledAt"] = rfc(*q.HandledAt)
	}
	return m
}

func (h queueHandler) list(w http.ResponseWriter, r *http.Request) {
	rows, err := h.svc.List(r.Context(), r.URL.Query().Get("status"))
	if err != nil {
		writeErr(w, err)
		return
	}
	out := make([]map[string]any, len(rows))
	for i, q := range rows {
		out[i] = queueDTO(q)
	}
	writeJSON(w, http.StatusOK, out)
}

func queueID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeErr(w, contract.ErrNotFound)
		return 0, false
	}
	return id, true
}

func (h queueHandler) retry(w http.ResponseWriter, r *http.Request) {
	id, ok := queueID(w, r)
	if !ok {
		return
	}
	var b struct {
		PostingDate string `json:"postingDate"`
	}
	if r.ContentLength > 0 && !decode(w, r, &b) {
		return
	}
	q, err := h.svc.Retry(r.Context(), id, b.PostingDate)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, queueDTO(q))
}

func (h queueHandler) manual(w http.ResponseWriter, r *http.Request) {
	id, ok := queueID(w, r)
	if !ok {
		return
	}
	var b struct {
		JournalNo string `json:"journalNo"`
	}
	if !decode(w, r, &b) {
		return
	}
	q, err := h.svc.Manual(r.Context(), id, b.JournalNo)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, queueDTO(q))
}

func (h queueHandler) skip(w http.ResponseWriter, r *http.Request) {
	id, ok := queueID(w, r)
	if !ok {
		return
	}
	var b struct {
		Note string `json:"note"`
	}
	if !decode(w, r, &b) {
		return
	}
	q, err := h.svc.Skip(r.Context(), id, b.Note)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, queueDTO(q))
}

func (h journalHandler) cancel(w http.ResponseWriter, r *http.Request) {
	var b struct{ Reason string }
	if !decode(w, r, &b) {
		return
	}
	j, err := h.svc.Cancel(r.Context(), r.PathValue("no"), b.Reason)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, journalDTO(j, true))
}

func (h journalHandler) reverse(w http.ResponseWriter, r *http.Request) {
	var b struct {
		PostingDate string `json:"postingDate"`
	}
	if r.ContentLength != 0 && !decode(w, r, &b) {
		return
	}
	j, err := h.svc.Reverse(r.Context(), r.PathValue("no"), b.PostingDate)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, journalDTO(j, true))
}
