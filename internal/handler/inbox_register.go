// 문서함·수신 처리·일괄 승인·상신 취소 라우트(B23 단계 2, 명세 8.2). 권한 판단은 등록기와 서비스가 한다.
package handler

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/deps"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
)

// batchDecider 는 계약 밖 확장이다: 일괄 승인(service.ApprovalService 가 구현).
type batchDecider interface {
	DecideBatch(ctx context.Context, items []domain.BatchItem, comment string) ([]domain.BatchResult, error)
}

// purchaseCanceler 는 계약 밖 확장이다: 구매 품의서 상신 취소(service.PurchaseService 가 구현).
type purchaseCanceler interface {
	CancelSubmit(ctx context.Context, id int64, reason string) (domain.PurchaseRequest, error)
}

// RegisterInbox 는 /api/inbox... /api/receipts... /api/units/{id}/receivers 와 상신 취소·일괄 승인 라우트를 등록한다. 호출: cmd/server/routes_erp.go.
func RegisterInbox(r contract.Registrar, d deps.Deps) {
	if d.Inbox != nil {
		h := inboxHandler{svc: d.Inbox}
		r.HandleFunc("GET", "/api/inbox/counts", contract.PermInboxRead, h.counts)
		r.HandleFunc("GET", "/api/inbox/{box}", contract.PermInboxRead, h.list)
		r.HandleFunc("POST", "/api/inbox/read/{id}", contract.PermInboxRead, h.markRead)
	}
	if d.Receipts != nil {
		h := receiptHandler{svc: d.Receipts}
		r.HandleFunc("GET", "/api/receipts", contract.PermInboxRead, h.list)
		r.HandleFunc("POST", "/api/receipts/{id}/accept", contract.PermReceiptHandle, h.accept)
		r.HandleFunc("POST", "/api/receipts/{id}/assign", contract.PermReceiptHandle, h.assign)
		r.HandleFunc("POST", "/api/receipts/{id}/complete", contract.PermReceiptHandle, h.complete)
		r.HandleFunc("POST", "/api/receipts/{id}/return", contract.PermReceiptHandle, h.returnIt)
		r.HandleFunc("GET", "/api/units/{id}/receivers", contract.PermReceiptHandle, h.receivers)
		r.HandleFunc("POST", "/api/units/{id}/receivers", contract.PermReceiptHandle, h.addReceiver)
		r.HandleFunc("DELETE", "/api/units/{id}/receivers/{userId}", contract.PermReceiptHandle, h.removeReceiver)
	}
	if b, ok := d.Approval.(batchDecider); ok {
		r.HandleFunc("POST", "/api/approvals/batch", contract.PermApprovalAgree, batchHandler{svc: b}.run)
	}
	if d.Docs != nil {
		r.HandleFunc("POST", "/api/approval-docs/{id}/cancel-submit", contract.PermDraftCreate, cancelHandler{doc: d.Docs}.cancelDoc)
	}
	if c, ok := d.Purchase.(purchaseCanceler); ok {
		r.HandleFunc("POST", "/api/purchase-requests/{id}/cancel-submit", contract.PermPurchaseCreate, cancelHandler{pur: c}.cancelPurchase)
	}
}

func rfc(t time.Time) string { return t.UTC().Format(time.RFC3339) }

func rfcp(t *time.Time) string {
	if t == nil {
		return ""
	}
	return rfc(*t)
}

type inboxHandler struct{ svc domain.InboxService }

func (h inboxHandler) list(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := domain.InboxFilter{Tab: q.Get("tab")}
	for k, dst := range map[string]*int{"limit": &f.Limit, "offset": &f.Offset} {
		if v := q.Get(k); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n < 0 {
				http.Error(w, k+" 이 올바르지 않습니다.", http.StatusBadRequest)
				return
			}
			*dst = n
		}
	}
	list, err := h.svc.List(r.Context(), domain.InboxBox(r.PathValue("box")), f)
	if err != nil {
		writeErr(w, err)
		return
	}
	out := make([]map[string]any, len(list))
	for i, it := range list {
		out[i] = map[string]any{"id": it.ID, "no": it.No, "title": it.Title, "formCode": it.FormCode, "formName": it.FormName, "formKind": it.FormKind,
			"requesterName": it.RequesterName, "unitPath": it.UnitPath, "status": it.Status, "urgent": it.Urgent, "unread": it.Unread, "disposed": it.Disposed,
			"updatedAt": rfc(it.UpdatedAt), "amount": it.Amount, "receiptId": it.ReceiptID, "receiptStatus": it.ReceiptStatus, "receiptUnit": it.ReceiptUnit}
	}
	writeJSON(w, http.StatusOK, out)
}

func (h inboxHandler) counts(w http.ResponseWriter, r *http.Request) {
	c, err := h.svc.Counts(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	out := map[string]map[string]int{}
	for box, v := range c {
		out[string(box)] = map[string]int{"total": v.Total, "unread": v.Unread}
	}
	writeJSON(w, http.StatusOK, out)
}

func (h inboxHandler) markRead(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	if err := h.svc.MarkRead(r.Context(), id); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

type receiptHandler struct{ svc domain.DocReceiptService }

func docReceiptDTO(c domain.DocReceipt) map[string]any {
	return map[string]any{"id": c.ID, "docId": c.DocID, "docNo": c.DocNo, "docTitle": c.DocTitle, "formName": c.FormName, "requesterName": c.RequesterName,
		"unitId": c.UnitID, "unitName": c.UnitName, "status": c.Status, "createdAt": rfc(c.CreatedAt), "receivedBy": c.ReceivedBy, "receivedAt": rfcp(c.ReceivedAt),
		"assigneeId": c.AssigneeID, "assigneeName": c.AssigneeName, "assignedAt": rfcp(c.AssignedAt), "doneBy": c.DoneBy, "doneAt": rfcp(c.DoneAt),
		"returnedBy": c.ReturnedBy, "returnedAt": rfcp(c.ReturnedAt), "comment": c.Comment, "reason": c.Reason, "canHandle": c.CanHandle}
}

func (h receiptHandler) list(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := domain.DocReceiptFilter{Tab: q.Get("tab")}
	if v := q.Get("docId"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 0 {
			http.Error(w, "docId 가 올바르지 않습니다.", http.StatusBadRequest)
			return
		}
		f.DocID = n
	}
	list, err := h.svc.List(r.Context(), f)
	if err != nil {
		writeErr(w, err)
		return
	}
	out := make([]map[string]any, len(list))
	for i, c := range list {
		out[i] = docReceiptDTO(c)
	}
	writeJSON(w, http.StatusOK, out)
}

func (h receiptHandler) one(w http.ResponseWriter, r *http.Request, fn func(id int64) (domain.DocReceipt, error)) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	c, err := fn(id)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, docReceiptDTO(c))
}

func (h receiptHandler) accept(w http.ResponseWriter, r *http.Request) {
	h.one(w, r, func(id int64) (domain.DocReceipt, error) { return h.svc.Accept(r.Context(), id) })
}

func (h receiptHandler) assign(w http.ResponseWriter, r *http.Request) {
	var b struct {
		UserID int64 `json:"userId"`
	}
	if !decode(w, r, &b) {
		return
	}
	h.one(w, r, func(id int64) (domain.DocReceipt, error) { return h.svc.Assign(r.Context(), id, b.UserID) })
}

func (h receiptHandler) complete(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Comment string `json:"comment"`
	}
	if !decode(w, r, &b) {
		return
	}
	h.one(w, r, func(id int64) (domain.DocReceipt, error) { return h.svc.Complete(r.Context(), id, b.Comment) })
}

func (h receiptHandler) returnIt(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Reason string `json:"reason"`
	}
	if !decode(w, r, &b) {
		return
	}
	h.one(w, r, func(id int64) (domain.DocReceipt, error) { return h.svc.Return(r.Context(), id, b.Reason) })
}

func (h receiptHandler) receivers(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	list, err := h.svc.Receivers(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	out := make([]map[string]any, len(list))
	for i, u := range list {
		out[i] = map[string]any{"userId": u.UserID, "name": u.Name}
	}
	writeJSON(w, http.StatusOK, out)
}

func (h receiptHandler) addReceiver(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	var b struct {
		UserID int64 `json:"userId"`
	}
	if !decode(w, r, &b) {
		return
	}
	if err := h.svc.AddReceiver(r.Context(), id, b.UserID); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]bool{"ok": true})
}

func (h receiptHandler) removeReceiver(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	uid, ok := pathID(w, r, "userId")
	if !ok {
		return
	}
	if err := h.svc.RemoveReceiver(r.Context(), id, uid); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

type batchHandler struct{ svc batchDecider }

func (h batchHandler) run(w http.ResponseWriter, r *http.Request) {
	var b struct {
		DocIDs  []int64 `json:"docIds"`
		Comment string  `json:"comment"`
	}
	if !decode(w, r, &b) {
		return
	}
	items := make([]domain.BatchItem, len(b.DocIDs))
	for i, id := range b.DocIDs {
		items[i] = domain.BatchItem{DocID: id}
	}
	res, err := h.svc.DecideBatch(r.Context(), items, b.Comment)
	if err != nil {
		writeErr(w, err)
		return
	}
	out := make([]map[string]any, len(res))
	for i, x := range res {
		out[i] = map[string]any{"docId": x.DocID, "ok": x.OK, "message": x.Message, "status": x.Status}
	}
	writeJSON(w, http.StatusOK, map[string]any{"results": out})
}

type cancelHandler struct {
	doc domain.ApprovalDocService
	pur purchaseCanceler
}

func (h cancelHandler) body(w http.ResponseWriter, r *http.Request) (int64, string, bool) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return 0, "", false
	}
	var b struct {
		Reason string `json:"reason"`
	}
	if !decode(w, r, &b) {
		return 0, "", false
	}
	return id, b.Reason, true
}

func (h cancelHandler) cancelDoc(w http.ResponseWriter, r *http.Request) {
	id, reason, ok := h.body(w, r)
	if !ok {
		return
	}
	pr, err := h.doc.CancelSubmit(r.Context(), id, reason)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toPRDTO(pr))
}

func (h cancelHandler) cancelPurchase(w http.ResponseWriter, r *http.Request) {
	id, reason, ok := h.body(w, r)
	if !ok {
		return
	}
	pr, err := h.pur.CancelSubmit(r.Context(), id, reason)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toPRDTO(pr))
}
