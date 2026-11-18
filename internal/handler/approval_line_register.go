// 조직도 기반 결재선 미리보기·결재자 소멸 목록·결재자 재지정 라우트(B21).
package handler

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
)

// registerLine 은 결재선 미리보기(approval:read)를 등록한다. 서비스가 "누구의 결재선을 물을 수 있는가"를 판정한다.
func registerLine(r contract.Registrar, svc domain.LineService) {
	h := lineHandler{svc: svc}
	r.HandleFunc("GET", "/api/approvals/line-preview", contract.PermApprovalRead, h.preview)
	r.HandleFunc("GET", "/api/approvals/orphaned", contract.PermOrgManage, h.orphaned)
}

type lineHandler struct{ svc domain.LineService }

type lineStepDTO struct {
	Order        int    `json:"order"`
	Kind         string `json:"kind"`
	Title        string `json:"title"`
	UnitID       int64  `json:"unitId"`
	UnitName     string `json:"unitName"`
	UnitPath     string `json:"unitPath"`
	ApproverID   int64  `json:"approverId"`
	ApproverName string `json:"approverName"`
	Acting       bool   `json:"acting"`
	RaisedFrom   string `json:"raisedFrom"`
	RaiseReason  string `json:"raiseReason"`
	RaisedNote   string `json:"raisedNote"`
	LayerNote    string `json:"layerNote"`
	Omitted      bool   `json:"omitted"`
	Missing      bool   `json:"missing"`
	Reason       string `json:"reason"`
	PoolSize     int    `json:"poolSize"`
	Role         string `json:"role"`
	Stage        int    `json:"stage"`
	Required     bool   `json:"required"`
	DefIndex     int    `json:"defIndex"`
	Skipped      bool   `json:"skipped,omitempty"`
	SkipNote     string `json:"skipNote,omitempty"`
	Forced       bool   `json:"forced,omitempty"` // B25: 예산 초과로 강제로 더한 재무 단계
	// B29: 결재자가 부재 중이면 종료 시각(RFC3339)과 대결자 이름(결재선은 그대로다).
	AbsentUntil     string `json:"absentUntil,omitempty"`
	AbsenceDelegate string `json:"absenceDelegate,omitempty"`
}

type linePreviewDTO struct {
	RequesterID   int64          `json:"requesterId"`
	RequesterName string         `json:"requesterName"`
	UnitPath      string         `json:"unitPath"`
	AsOf          string         `json:"asOf"`
	Steps         []lineStepDTO  `json:"steps"`
	Complete      bool           `json:"complete"`
	Problems      []string       `json:"problems"`
	Notes         []string       `json:"notes"`
	Recipients    []recipientDTO `json:"recipients"`
	Authority     *authorityDTO  `json:"authority,omitempty"`
}

func toLinePreviewDTO(p domain.LinePreview) linePreviewDTO {
	out := linePreviewDTO{RequesterID: p.RequesterID, RequesterName: p.RequesterName, UnitPath: p.UnitPath, AsOf: string(p.AsOf),
		Steps: []lineStepDTO{}, Complete: p.Complete, Problems: append([]string{}, p.Problems...), Notes: append([]string{}, p.Notes...)}
	for _, s := range p.Steps {
		st := lineStepDTO{Order: s.Order, Kind: string(s.Kind), Title: s.Title, UnitID: s.UnitID, UnitName: s.UnitName,
			UnitPath: s.UnitPath, ApproverID: s.ApproverID, ApproverName: s.ApproverName, Acting: s.Acting, RaisedFrom: s.RaisedFrom,
			RaiseReason: s.RaiseReason, RaisedNote: s.RaisedNote, LayerNote: s.LayerNote, Omitted: s.Omitted, Missing: s.Missing,
			Reason: s.Reason, PoolSize: s.PoolSize, Role: s.Role, Stage: s.Stage, Required: s.Required, DefIndex: s.DefIndex, Skipped: s.Skipped, SkipNote: s.SkipNote, Forced: s.Forced, AbsenceDelegate: s.AbsenceDelegate}
		if s.AbsentUntil != nil {
			st.AbsentUntil = ts(*s.AbsentUntil)
		}
		out.Steps = append(out.Steps, st)
	}
	out.Recipients = []recipientDTO{}
	for _, r := range p.Recipients {
		out.Recipients = append(out.Recipients, recipientDTO{Kind: r.Kind, TargetType: r.TargetType, TargetID: r.TargetID, Name: r.Name, Path: r.Path, Note: r.Note})
	}
	return out
}

func (h lineHandler) preview(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	amount, err := strconv.ParseInt(q.Get("amount"), 10, 64)
	if err != nil {
		http.Error(w, "금액은 숫자로 입력해야 합니다.", http.StatusBadRequest)
		return
	}
	var requester int64
	if v := q.Get("requesterId"); v != "" {
		if requester, err = strconv.ParseInt(v, 10, 64); err != nil {
			http.Error(w, "요청자 번호가 올바르지 않습니다.", http.StatusBadRequest)
			return
		}
	}
	var fieldUnit int64
	if v := q.Get("fieldUnit"); v != "" {
		if fieldUnit, err = strconv.ParseInt(v, 10, 64); err != nil {
			http.Error(w, "협조 요청 부서 번호가 올바르지 않습니다.", http.StatusBadRequest)
			return
		}
	}
	p, err := h.svc.PreviewLine(r.Context(), domain.LinePreviewInput{RequesterID: requester, Amount: amount, AsOf: contract.Day(q.Get("asOf")), FormCode: q.Get("formCode"), FieldUnit: fieldUnit})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toLinePreviewDTO(p))
}

type orphanDTO struct {
	PRID          int64  `json:"prId"`
	RequesterID   int64  `json:"requesterId"`
	PRTitle       string `json:"prTitle"`
	Round         int    `json:"round"`
	Order         int    `json:"order"`
	Seq           int    `json:"seq"`
	Title         string `json:"title"`
	StepKind      string `json:"stepKind"`
	ApproverID    int64  `json:"approverId"`
	ApproverName  string `json:"approverName"`
	Reason        string `json:"reason"`
	SinceDay      string `json:"sinceDay"`
	Reassignable  bool   `json:"reassignable"`
	Role          string `json:"role"`
	FormKind      string `json:"formKind"`
	FormCode      string `json:"formCode"`
	FormName      string `json:"formName"`
	RequesterName string `json:"requesterName"`
	Stage         int    `json:"stage"`
}

func (h lineHandler) orphaned(w http.ResponseWriter, r *http.Request) {
	list, err := h.svc.ListOrphanedSteps(r.Context(), contract.Day(r.URL.Query().Get("asOf")))
	if err != nil {
		writeErr(w, err)
		return
	}
	out := make([]orphanDTO, len(list))
	for i, o := range list {
		out[i] = orphanDTO{PRID: o.PRID, RequesterID: o.RequesterID, PRTitle: o.PRTitle, Round: o.Round, Order: o.Order, Seq: o.Seq, Title: o.Title,
			StepKind: string(o.StepKind), ApproverID: o.ApproverID, ApproverName: o.ApproverName, Reason: o.Reason, SinceDay: string(o.SinceDay), Reassignable: o.Reassignable, Role: o.Role, FormKind: o.FormKind, FormCode: o.FormCode, FormName: o.FormName, RequesterName: o.RequesterName, Stage: o.Stage}
	}
	writeJSON(w, http.StatusOK, out)
}

// registerReassign 는 관리자 결재자 재지정 라우트를 등록한다(approval:reassign, admin 만). MCP 에는 노출하지 않는다.
func registerReassign(r contract.Registrar, svc domain.ReassignService) {
	h := reassignHandler{svc: svc}
	r.HandleFunc("POST", "/api/approvals/{id}/steps/{seq}/reassign", contract.PermApprovalReassign, h.reassign)
	r.HandleFunc("GET", "/api/approvals/{id}/steps/{seq}/reassign-candidates", contract.PermApprovalReassign, h.candidates)
}

type reassignHandler struct{ svc domain.ReassignService }

func stepSeq(w http.ResponseWriter, r *http.Request) (int, bool) {
	n, err := strconv.Atoi(r.PathValue("seq"))
	if err != nil || n < 1 {
		http.Error(w, "단계 번호가 올바르지 않습니다.", http.StatusBadRequest)
		return 0, false
	}
	return n, true
}

func (h reassignHandler) reassign(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	seq, ok := stepSeq(w, r)
	if !ok {
		return
	}
	var b struct {
		UserID        int64  `json:"userId"`
		Cause         string `json:"cause"`
		Reason        string `json:"reason"`
		ConfirmActive bool   `json:"confirmActive"`
	}
	if !decode(w, r, &b) {
		return
	}
	pr, err := h.svc.ReassignStep(r.Context(), id, seq, domain.ReassignInput{UserID: b.UserID, Cause: strings.TrimSpace(b.Cause), Reason: b.Reason, ConfirmActive: b.ConfirmActive})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toPRDTO(pr))
}

func (h reassignHandler) candidates(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	seq, ok := stepSeq(w, r)
	if !ok {
		return
	}
	list, err := h.svc.ReassignCandidates(r.Context(), id, seq, r.URL.Query().Get("text"))
	if err != nil {
		writeErr(w, err)
		return
	}
	type dto struct {
		UserID   int64  `json:"userId"`
		Name     string `json:"name"`
		Title    string `json:"title"`
		Acting   bool   `json:"acting"`
		UnitName string `json:"unitName"`
		UnitPath string `json:"unitPath"`
	}
	out := make([]dto, len(list))
	for i, c := range list {
		out[i] = dto{c.UserID, c.Name, c.Title, c.Acting, c.UnitName, c.UnitPath}
	}
	writeJSON(w, http.StatusOK, out)
}
