// 매입 세금계산서 3자 대조·수신 동기화 라우트(B43 2단계, 명세 6절·9절). 권한 선언은 라우트 등록기가 하고 핸들러는 서비스만 부른다.
package handler

import (
	"net/http"
	"strconv"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/deps"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
)

// RegisterTaxMatch 는 /api/finance/tax-matches 와 /api/finance/tax-sync 를 등록한다. 호출: cmd/server/routes_erp.go.
func RegisterTaxMatch(r contract.Registrar, d deps.Deps) {
	if d.TaxMatch == nil {
		return
	}
	h := taxMatchHandler{svc: d.TaxMatch, ctl: d.TaxSyncCtl, syncer: d.TaxSyncer}
	r.HandleFunc("GET", "/api/finance/tax-matches", contract.PermTaxInvoiceRead, h.list)
	r.HandleFunc("POST", "/api/finance/tax-matches/propose", contract.PermTaxInvoiceWrite, h.propose)
	r.HandleFunc("POST", "/api/finance/tax-matches/confirm-bulk", contract.PermTaxInvoiceWrite, h.confirmBulk)
	r.HandleFunc("GET", "/api/finance/tax-matches/{id}", contract.PermTaxInvoiceRead, h.get)
	r.HandleFunc("POST", "/api/finance/tax-matches/{id}/confirm", contract.PermTaxInvoiceWrite, h.confirm)
	r.HandleFunc("POST", "/api/finance/tax-matches/{id}/dismiss", contract.PermTaxInvoiceWrite, h.dismiss)
	r.HandleFunc("POST", "/api/finance/tax-matches/{id}/unlink", contract.PermTaxInvoiceWrite, h.unlink)
	if d.TaxContact != nil {
		r.HandleFunc("GET", "/api/finance/tax-invoices/{id}/contact", contract.PermTaxInvoiceRead, func(w http.ResponseWriter, req *http.Request) {
			id, ok := pathID(w, req, "id")
			if !ok {
				return
			}
			c, err := d.TaxContact.RevealContact(req.Context(), id)
			if err != nil {
				writeErr(w, err)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"name": c.Name, "email": c.Email})
		})
	}
	if d.TaxSyncCtl != nil {
		r.HandleFunc("GET", "/api/finance/tax-sync", contract.PermTaxInvoiceRead, h.syncStatus)
		if d.TaxSyncer != nil {
			r.HandleFunc("POST", "/api/finance/tax-sync/run", contract.PermTaxInvoiceWrite, h.syncRun)
		}
	}
}

type taxMatchHandler struct {
	svc    domain.TaxMatchService
	ctl    domain.TaxSyncControl
	syncer deps.TaxSyncer
}

func matchOut(m domain.TaxMatch) map[string]any {
	recs := make([]map[string]any, len(m.Receipts))
	for i, c := range m.Receipts {
		recs[i] = map[string]any{"receiptId": c.ReceiptID, "poId": c.POID, "poNo": c.PONo, "receivedAt": c.ReceivedAt, "supply": c.Supply, "journalNo": c.JournalNo}
	}
	warns := make([]string, 0, len(m.Warnings))
	for _, w := range m.Warnings {
		warns = append(warns, domain.MatchWarnLabels[w])
	}
	return map[string]any{
		"id": m.ID, "invoiceId": m.InvoiceID, "invoiceNo": m.InvoiceNo, "approvalNo": m.ApprovalNo, "issueDate": m.IssueDate, "supplierName": m.SupplierName,
		"supply": m.Supply, "tax": m.Tax, "total": m.Total, "status": m.Status, "statusName": domain.MatchStatusLabels[m.Status], "result": m.Result, "resultName": domain.MatchResultLabels[m.Result],
		"diff": m.Diff, "warnings": warns, "receipts": recs, "proposedAt": m.ProposedAt, "decidedAt": m.DecidedAt, "comment": m.Comment, "journalNote": m.JournalNote, "confirmable": m.Confirmable, "bulk": m.Bulk,
	}
}

func (h taxMatchHandler) list(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))
	page, err := h.svc.ListMatches(r.Context(), domain.TaxMatchFilter{Status: q.Get("status"), Result: q.Get("result"), Limit: limit, Offset: offset})
	if err != nil {
		writeErr(w, err)
		return
	}
	rows := make([]map[string]any, len(page.Rows))
	for i, m := range page.Rows {
		rows[i] = matchOut(m)
	}
	writeJSON(w, http.StatusOK, map[string]any{"rows": rows, "total": page.Total})
}

func (h taxMatchHandler) get(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	m, err := h.svc.GetMatch(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, matchOut(m))
}

func (h taxMatchHandler) propose(w http.ResponseWriter, r *http.Request) {
	s, err := h.svc.Propose(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s)
}

type commentDTO struct {
	Comment string `json:"comment"`
	Reason  string `json:"reason"`
}

func (h taxMatchHandler) decide(w http.ResponseWriter, r *http.Request, do func(id int64, in commentDTO) (domain.TaxMatch, error)) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	var in commentDTO
	if r.ContentLength != 0 && !decode(w, r, &in) {
		return
	}
	m, err := do(id, in)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, matchOut(m))
}

func (h taxMatchHandler) confirm(w http.ResponseWriter, r *http.Request) {
	h.decide(w, r, func(id int64, in commentDTO) (domain.TaxMatch, error) {
		return h.svc.Confirm(r.Context(), id, in.Comment)
	})
}

func (h taxMatchHandler) dismiss(w http.ResponseWriter, r *http.Request) {
	h.decide(w, r, func(id int64, in commentDTO) (domain.TaxMatch, error) {
		return h.svc.Dismiss(r.Context(), id, in.Reason)
	})
}

func (h taxMatchHandler) unlink(w http.ResponseWriter, r *http.Request) {
	h.decide(w, r, func(id int64, in commentDTO) (domain.TaxMatch, error) {
		return h.svc.Unlink(r.Context(), id, in.Reason)
	})
}

func (h taxMatchHandler) confirmBulk(w http.ResponseWriter, r *http.Request) {
	var in struct {
		IDs []int64 `json:"ids"`
	}
	if !decode(w, r, &in) {
		return
	}
	res, err := h.svc.ConfirmBulk(r.Context(), in.IDs)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func syncOut(st domain.TaxSyncState) map[string]any {
	return map[string]any{"lastRunAt": st.LastRunAt, "lastDone": st.LastDone, "result": st.LastResult, "resultName": domain.TaxSyncResultLabels[st.LastResult], "counts": st.LastCounts, "remaining": st.Cursor != ""}
}

func (h taxMatchHandler) syncStatus(w http.ResponseWriter, r *http.Request) {
	st, err := h.ctl.SyncStatus(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, syncOut(st))
}

// syncRun 은 수동 동기화다: 권한을 확인한 뒤 작업을 한 번 돌리고 마지막 상태를 돌려준다.
func (h taxMatchHandler) syncRun(w http.ResponseWriter, r *http.Request) {
	if err := h.ctl.AuthorizeSync(r.Context()); err != nil {
		writeErr(w, err)
		return
	}
	if _, _, err := h.syncer.RunOnce(r.Context()); err != nil {
		writeErr(w, err)
		return
	}
	h.syncStatus(w, r)
}
