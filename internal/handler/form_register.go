// 결재 양식 라우트(B23, 명세 8.2). 읽기는 draft:create, 관리는 form:manage(admin).
package handler

import (
	"net/http"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/deps"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
)

// RegisterForms 는 /api/forms... 와 /api/admin/forms... 라우트를 등록한다. 호출: cmd/server/routes_erp.go.
func RegisterForms(r contract.Registrar, d deps.Deps) {
	if d.Forms == nil {
		return
	}
	h := formHandler{svc: d.Forms}
	r.HandleFunc("GET", "/api/forms", contract.PermDraftCreate, h.listUsable)
	r.HandleFunc("GET", "/api/forms/{code}", contract.PermDraftCreate, h.get)
	r.HandleFunc("GET", "/api/admin/forms", contract.PermFormManage, h.list)
	r.HandleFunc("GET", "/api/admin/forms/versions/{id}", contract.PermFormManage, h.version)
	r.HandleFunc("POST", "/api/admin/forms/{code}/versions", contract.PermFormManage, h.createDraft)
	r.HandleFunc("PUT", "/api/admin/forms/versions/{id}", contract.PermFormManage, h.saveDraft)
	r.HandleFunc("POST", "/api/admin/forms/versions/{id}/publish", contract.PermFormManage, h.publish)
	r.HandleFunc("GET", "/api/admin/forms/versions/{id}/number-preview", contract.PermFormManage, h.numberPreview)
	r.HandleFunc("POST", "/api/admin/forms/{code}/active", contract.PermFormManage, h.setActive)
}

type formHandler struct{ svc domain.FormService }

type formSummaryDTO struct {
	ID       int64  `json:"id"`
	Code     string `json:"code"`
	Category string `json:"category"`
	Name     string `json:"name"`
	Kind     string `json:"kind"`
	Version  int    `json:"version"`
	Active   bool   `json:"active"`
}

func toFormSummaryDTO(f domain.FormSummary) formSummaryDTO {
	return formSummaryDTO{ID: f.ID, Code: f.Code, Category: f.Category, Name: f.Name, Kind: string(f.Kind), Version: f.Version, Active: f.Active}
}

type formDetailDTO struct {
	formSummaryDTO
	VersionID  int64                 `json:"versionId"`
	Definition domain.FormDefinition `json:"definition"`
}

type formVersionDTO struct {
	ID          int64                 `json:"id"`
	FormCode    string                `json:"formCode"`
	Version     int                   `json:"version"`
	Status      string                `json:"status"`
	Definition  domain.FormDefinition `json:"definition"`
	CreatedAt   string                `json:"createdAt"`
	PublishedAt string                `json:"publishedAt"`
}

func toFormVersionDTO(v domain.FormVersion) formVersionDTO {
	return formVersionDTO{ID: v.ID, FormCode: v.FormCode, Version: v.Version, Status: v.Status, Definition: v.Definition, CreatedAt: v.CreatedAt, PublishedAt: v.PublishedAt}
}

func (h formHandler) listUsable(w http.ResponseWriter, r *http.Request) {
	list, err := h.svc.ListUsable(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	out := make([]formSummaryDTO, len(list))
	for i, f := range list {
		out[i] = toFormSummaryDTO(f)
	}
	writeJSON(w, http.StatusOK, out)
}

func (h formHandler) get(w http.ResponseWriter, r *http.Request) {
	f, err := h.svc.Get(r.Context(), r.PathValue("code"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, formDetailDTO{formSummaryDTO: toFormSummaryDTO(f.FormSummary), VersionID: f.VersionID, Definition: f.Definition})
}

func (h formHandler) list(w http.ResponseWriter, r *http.Request) {
	list, err := h.svc.List(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	type row struct {
		formSummaryDTO
		LatestVersion   int    `json:"latestVersion"`
		LatestStatus    string `json:"latestStatus"`
		PublishedCount  int    `json:"publishedCount"`
		HasDraftVersion bool   `json:"hasDraftVersion"`
		Searchable      bool   `json:"searchable"`
	}
	out := make([]row, len(list))
	for i, f := range list {
		out[i] = row{toFormSummaryDTO(f.FormSummary), f.LatestVersion, f.LatestStatus, f.PublishedCount, f.HasDraftVersion, f.Searchable}
	}
	writeJSON(w, http.StatusOK, out)
}

func (h formHandler) version(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	v, err := h.svc.GetVersion(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toFormVersionDTO(v))
}

func (h formHandler) createDraft(w http.ResponseWriter, r *http.Request) {
	v, err := h.svc.CreateVersionDraft(r.Context(), r.PathValue("code"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, toFormVersionDTO(v))
}

func (h formHandler) saveDraft(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	var in struct {
		Definition domain.FormDefinition `json:"definition"`
	}
	if !decode(w, r, &in) {
		return
	}
	v, err := h.svc.SaveVersionDraft(r.Context(), id, domain.FormVersionInput{Definition: in.Definition})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toFormVersionDTO(v))
}

func (h formHandler) publish(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	v, err := h.svc.Publish(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toFormVersionDTO(v))
}

func (h formHandler) numberPreview(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	s, err := h.svc.PreviewNumber(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"number": s})
}

func (h formHandler) setActive(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Active bool `json:"active"`
	}
	if !decode(w, r, &in) {
		return
	}
	if err := h.svc.SetActive(r.Context(), r.PathValue("code"), in.Active); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"active": in.Active})
}
