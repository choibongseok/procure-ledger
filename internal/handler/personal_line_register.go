// 개인 결재선 라우트(B23 명세 3.9). 본인 것만 다룬다(draft:create).
package handler

import (
	"net/http"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/deps"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
)

// RegisterPersonalLines 는 /api/personal-lines... 라우트를 등록한다. 호출: cmd/server/routes_erp.go.
func RegisterPersonalLines(r contract.Registrar, d deps.Deps) {
	if d.PersonalLines == nil {
		return
	}
	h := personalLineHandler{svc: d.PersonalLines}
	r.HandleFunc("GET", "/api/personal-lines", contract.PermDraftCreate, h.list)
	r.HandleFunc("POST", "/api/personal-lines", contract.PermDraftCreate, h.save)
	r.HandleFunc("GET", "/api/personal-lines/{id}", contract.PermDraftCreate, h.load)
	r.HandleFunc("DELETE", "/api/personal-lines/{id}", contract.PermDraftCreate, h.remove)
}

type personalLineHandler struct{ svc domain.PersonalLineService }

type personalLineDTO struct {
	ID       int64         `json:"id"`
	Name     string        `json:"name"`
	FormCode string        `json:"formCode"`
	Edits    []lineEditDTO `json:"edits"`
}

func toPersonalLineDTO(l domain.PersonalLine) personalLineDTO {
	out := personalLineDTO{ID: l.ID, Name: l.Name, FormCode: l.FormCode, Edits: []lineEditDTO{}}
	for _, e := range l.Edits {
		out.Edits = append(out.Edits, lineEditDTO{Role: e.Role, TargetType: e.TargetType, TargetID: e.TargetID, Group: e.Group, Name: e.Name})
	}
	return out
}

func (h personalLineHandler) list(w http.ResponseWriter, r *http.Request) {
	list, err := h.svc.List(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	out := make([]personalLineDTO, len(list))
	for i, l := range list {
		out[i] = toPersonalLineDTO(l)
	}
	writeJSON(w, http.StatusOK, out)
}

func (h personalLineHandler) save(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Name     string        `json:"name"`
		FormCode string        `json:"formCode"`
		Edits    []lineEditDTO `json:"edits"`
	}
	if !decode(w, r, &b) {
		return
	}
	l, err := h.svc.Save(r.Context(), domain.PersonalLineInput{Name: b.Name, FormCode: b.FormCode, Edits: toEdits(b.Edits)})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, toPersonalLineDTO(l))
}

func (h personalLineHandler) load(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	l, notes, err := h.svc.Load(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	if notes == nil {
		notes = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"line": toPersonalLineDTO(l), "notes": notes})
}

func (h personalLineHandler) remove(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	if err := h.svc.Delete(r.Context(), id); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
