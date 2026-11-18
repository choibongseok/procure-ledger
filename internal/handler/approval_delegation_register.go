package handler

import (
	"net/http"
	"strings"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
)

// registerDelegation 은 결재 위임 라우트(/api/approvals/delegations...)를 등록한다.
// 권한은 라우트 선언(approval:delegate)으로만 강제하고, 위임자 본인 여부 등 규칙은 서비스가 판정한다.
func registerDelegation(r contract.Registrar, svc domain.DelegationService) {
	h := delegationHandler{svc: svc}
	r.HandleFunc("GET", "/api/approvals/delegations", contract.PermApprovalDelegate, h.list)
	r.HandleFunc("POST", "/api/approvals/delegations", contract.PermApprovalDelegate, h.create)
	r.HandleFunc("GET", "/api/approvals/delegations/candidates", contract.PermApprovalDelegate, h.candidates)
	r.HandleFunc("DELETE", "/api/approvals/delegations/{id}", contract.PermApprovalDelegate, h.revoke)
}

type delegationHandler struct{ svc domain.DelegationService }

type delegationDTO struct {
	ID            int64   `json:"id"`
	DelegatorID   int64   `json:"delegatorId"`
	DelegatorName string  `json:"delegatorName"`
	DelegateID    int64   `json:"delegateId"`
	DelegateName  string  `json:"delegateName"`
	From          string  `json:"from"`
	To            string  `json:"to"`
	RevokedAt     *string `json:"revokedAt"`
	CreatedAt     string  `json:"createdAt"`
	Kind          string  `json:"kind"`
	Reason        string  `json:"reason"`
	Note          string  `json:"note"`
}

func toDelegationDTO(d domain.Delegation) delegationDTO {
	out := delegationDTO{ID: d.ID, DelegatorID: d.DelegatorID, DelegatorName: d.DelegatorName, DelegateID: d.DelegateID,
		DelegateName: d.DelegateName, From: ts(d.From), To: ts(d.To), CreatedAt: ts(d.CreatedAt), Kind: d.Kind, Reason: d.Reason, Note: d.Note}
	if d.RevokedAt != nil {
		s := ts(*d.RevokedAt)
		out.RevokedAt = &s
	}
	return out
}

func toDelegationDTOs(in []domain.Delegation) []delegationDTO {
	out := make([]delegationDTO, len(in))
	for i, d := range in {
		out[i] = toDelegationDTO(d)
	}
	return out
}

func (h delegationHandler) list(w http.ResponseWriter, r *http.Request) {
	given, received, err := h.svc.ListMine(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Given    []delegationDTO `json:"given"`
		Received []delegationDTO `json:"received"`
	}{toDelegationDTOs(given), toDelegationDTOs(received)})
}

func (h delegationHandler) create(w http.ResponseWriter, r *http.Request) {
	var b struct {
		DelegateID int64  `json:"delegateId"`
		From       string `json:"from"`
		To         string `json:"to"`
		Kind       string `json:"kind"`
		Reason     string `json:"reason"`
		Note       string `json:"note"`
	}
	if !decode(w, r, &b) {
		return
	}
	from, err1 := time.Parse(time.RFC3339, b.From)
	to, err2 := time.Parse(time.RFC3339, b.To)
	if err1 != nil || err2 != nil {
		http.Error(w, "위임 기간(시작·종료)을 올바르게 입력해야 합니다.", http.StatusBadRequest)
		return
	}
	d, err := h.svc.Create(r.Context(), domain.DelegationInput{DelegateID: b.DelegateID, From: from, To: to, Kind: b.Kind, Reason: b.Reason, Note: strings.TrimSpace(b.Note)})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, toDelegationDTO(d))
}

func (h delegationHandler) revoke(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	if err := h.svc.Revoke(r.Context(), id); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h delegationHandler) candidates(w http.ResponseWriter, r *http.Request) {
	cs, err := h.svc.Candidates(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	type dto struct {
		ID           int64  `json:"id"`
		Name         string `json:"name"`
		Role         string `json:"role"`
		DepartmentID int64  `json:"departmentId"`
		Title        string `json:"title"`    // 직책 이름(팀장·부서장·본부장·대표·재무)
		UnitName     string `json:"unitName"` // 조직장인 조직 이름(재무는 빈 값)
		Away         bool   `json:"away"`     // 지금 부재 중이라 대결자로 고를 수 없음
	}
	out := make([]dto, len(cs))
	for i, c := range cs {
		out[i] = dto{c.ID, c.Name, string(c.Role), c.DepartmentID, c.Title, c.UnitName, c.Away}
	}
	writeJSON(w, http.StatusOK, out)
}
