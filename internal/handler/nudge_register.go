// 결재 독촉 라우트(B29 ④, 5절). 권한은 draft:create 로 선언하고 서비스가 기안자인지 판정한다(핸들러에 역할 분기 없음).
package handler

import (
	"net/http"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/deps"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
)

// RegisterNudge 는 POST /api/approvals/{id}/nudge 와 GET /api/nudges?docId= 를 등록한다. 호출: cmd/server/routes_erp.go.
func RegisterNudge(r contract.Registrar, d deps.Deps) {
	if d.Nudge == nil {
		return
	}
	h := nudgeHandler{svc: d.Nudge}
	r.HandleFunc("POST", "/api/approvals/{id}/nudge", contract.PermDraftCreate, h.nudge)
	r.HandleFunc("GET", "/api/nudges", contract.PermDraftCreate, h.status)
}

type nudgeHandler struct{ svc domain.NudgeService }

func (h nudgeHandler) nudge(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	res, err := h.svc.Nudge(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"stepTitle": res.StepTitle, "recipients": res.Recipients, "waitingMinutes": res.WaitingMinutes})
}

func (h nudgeHandler) status(w http.ResponseWriter, r *http.Request) {
	id := int64(queryInt(r, "docId", 0))
	if id <= 0 {
		http.Error(w, "문서 번호가 올바르지 않습니다.", http.StatusBadRequest)
		return
	}
	st, err := h.svc.Status(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	recs := make([]map[string]any, len(st.Records))
	for i, x := range st.Records {
		recs[i] = map[string]any{"at": rfc(x.At), "day": x.Day, "stepTitle": x.StepTitle, "recipients": x.Recipients, "recipientNames": x.RecipientNames}
	}
	out := map[string]any{"delayed": st.Delayed, "stepTitle": st.StepTitle, "waitingMinutes": st.WaitingMinutes, "noApprover": st.NoApprover, "holiday": st.Holiday, "records": recs, "doneToday": nil}
	if st.DoneToday != nil {
		out["doneToday"] = rfc(*st.DoneToday)
	}
	writeJSON(w, http.StatusOK, out)
}
