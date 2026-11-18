// (internal/handler/approval*).
package handler

import (
	"context"
	"net/http"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/deps"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/service"
)

// RegisterApproval 은 결재함·승인·반려 라우트(/api/approvals...)를 등록한다.
// 호출: cmd/server/routes_erp.go. d.Approval 은 domain.ApprovalService 이다.
func RegisterApproval(r contract.Registrar, d deps.Deps) {
	if d.Approval == nil {
		return
	}
	h := approvalHandler{svc: d.Approval}
	r.HandleFunc("GET", "/api/approvals/pending", contract.PermApprovalRead, h.pending)
	// 결재함 탭(대기·진행·완료). 권한·열람 범위는 /pending 과 같다. 구현체가 inboxLister 를 제공할 때만 연다.
	if _, ok := d.Approval.(inboxLister); ok {
		r.HandleFunc("GET", "/api/approvals/inbox", contract.PermApprovalRead, h.inbox)
	}
	// 결재(approval:decide)와 합의(approval:agree)가 같은 경로다. 합의 권한은 결재 권한 보유자의 상위 집합이라 게이트는 agree 이고, 결재 단계는 서비스가 결재 권한을 다시 본다.
	r.HandleFunc("POST", "/api/approvals/{id}/decision", contract.PermApprovalAgree, h.decide)
	r.HandleFunc("GET", "/api/approvals/{id}/history", contract.PermApprovalRead, h.history)
	// 위임으로 대신 결재하는 건의 안내(위임자·기간). 구현체가 actingLister 를 제공할 때만 연다.
	if _, ok := d.Approval.(actingLister); ok {
		r.HandleFunc("GET", "/api/approvals/{id}/acting", contract.PermApprovalDecide, h.acting)
	}
	if d.Delegation != nil {
		registerDelegation(r, d.Delegation)
	}
	if d.Line != nil {
		registerLine(r, d.Line)
	}
	if d.Reassign != nil {
		registerReassign(r, d.Reassign)
	}
}

type approvalHandler struct{ svc domain.ApprovalService }

// inboxLister 는 구현체가 제공하면 결재함 탭 목록을 낸다(계약 밖 보조 조회, 웹 전용).
type inboxLister interface {
	ListInbox(ctx context.Context, tab string) ([]domain.PurchaseRequest, error)
}

// actingLister 는 구현체가 제공하면 위임 결재 안내를 낸다(계약 밖 보조 조회, 웹 전용).
type actingLister interface {
	ActingFor(ctx context.Context, prID int64) (service.ActingNotice, bool, error)
}

// acting 은 위임으로 대신 결재하는 건이면 {delegatorName, from, to}, 아니면 null 이다. 시각은 RFC3339 UTC 문자열이다.
func (h approvalHandler) acting(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	n, on, err := h.svc.(actingLister).ActingFor(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	if !on {
		writeJSON(w, http.StatusOK, nil)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		DelegatorID   int64  `json:"delegatorId"`
		DelegatorName string `json:"delegatorName"`
		From          string `json:"from"`
		To            string `json:"to"`
	}{n.DelegatorID, n.DelegatorName, ts(n.From), ts(n.To)})
}

func (h approvalHandler) inbox(w http.ResponseWriter, r *http.Request) {
	prs, err := h.svc.(inboxLister).ListInbox(r.Context(), r.URL.Query().Get("tab"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toPRDTOs(prs))
}

func (h approvalHandler) pending(w http.ResponseWriter, r *http.Request) {
	prs, err := h.svc.ListPending(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toPRDTOs(prs))
}

func (h approvalHandler) decide(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	var b struct {
		Approve bool   `json:"approve"`
		Comment string `json:"comment"`
	}
	if !decode(w, r, &b) {
		return
	}
	dec := domain.Decision{Approve: b.Approve, Comment: b.Comment}
	var res domain.DecisionResult
	var err error
	if p, ok := contract.PrincipalFrom(r.Context()); ok && p.Can(contract.PermApprovalDecide) {
		res, err = h.svc.Decide(r.Context(), id, dec)
	} else {
		res, err = h.svc.Agree(r.Context(), id, dec)
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	out := struct {
		Request       prDTO  `json:"request"`
		PurchaseOrder *poDTO `json:"purchaseOrder"`
	}{Request: toPRDTO(res.Request)}
	if res.PurchaseOrder != nil {
		po := toPODTO(*res.PurchaseOrder)
		out.PurchaseOrder = &po
	}
	writeJSON(w, http.StatusOK, out)
}

func (h approvalHandler) history(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	hist, err := h.svc.History(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	out := make([]historyDTO, len(hist))
	for i, x := range hist {
		out[i] = historyDTO{ID: x.ID, PRID: x.PRID, Seq: x.Seq, ActorID: x.ActorID, ActorName: x.ActorName, OnBehalfOfID: x.OnBehalfOfID, OnBehalfOfName: x.OnBehalfOfName, Action: string(x.Action), Comment: x.Comment, At: ts(x.At)}
	}
	writeJSON(w, http.StatusOK, out)
}
