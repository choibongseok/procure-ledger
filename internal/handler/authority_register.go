// 전결 규정 표 관리 라우트와 전결 판정 DTO(B23 단계 3, 명세 8.2). 권한은 등록기가 선언한다(authority:manage, admin).
package handler

import (
	"net/http"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/deps"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/service"
)

type authorityDTO struct {
	RuleID        int64  `json:"ruleId"`
	MinAmount     int64  `json:"minAmount"`
	MaxAmount     int64  `json:"maxAmount"`
	FinalOrder    int    `json:"finalOrder"`
	FinalTitle    string `json:"finalTitle"`
	SkippedOrders []int  `json:"skippedOrders"`
	Slug          string `json:"slug"`
	ClauseNo      string `json:"clauseNo"`
	Warning       string `json:"warning,omitempty"`
}

func toAuthorityDTO(n *domain.AuthorityNote) *authorityDTO {
	if n == nil {
		return nil
	}
	skipped := n.SkippedOrders
	if skipped == nil {
		skipped = []int{}
	}
	return &authorityDTO{RuleID: n.RuleID, MinAmount: n.MinAmount, MaxAmount: n.MaxAmount, FinalOrder: n.FinalOrder, FinalTitle: n.FinalTitle,
		SkippedOrders: skipped, Slug: n.Slug, ClauseNo: n.ClauseNo, Warning: n.Warning}
}

type authorityRuleDTO struct {
	ID          int64  `json:"id"`
	FormCode    string `json:"formCode"`
	FormName    string `json:"formName"`
	MinAmount   int64  `json:"minAmount"`
	MaxAmount   int64  `json:"maxAmount"`
	FinalLevel  int    `json:"finalLevel"`
	PolicySlug  string `json:"policySlug"`
	PolicyTitle string `json:"policyTitle"`
	ClauseNo    string `json:"clauseNo"`
	Active      bool   `json:"active"`
}

func toRuleDTO(r domain.AuthorityRule) authorityRuleDTO {
	return authorityRuleDTO{ID: r.ID, FormCode: r.FormCode, FormName: r.FormName, MinAmount: r.MinAmount, MaxAmount: r.MaxAmount, FinalLevel: r.FinalLevel,
		PolicySlug: r.PolicySlug, PolicyTitle: r.PolicyTitle, ClauseNo: r.ClauseNo, Active: r.Active}
}

// RegisterAuthority 는 /api/authority-rules... 라우트를 등록한다. 호출: cmd/server/routes_erp.go.
func RegisterAuthority(r contract.Registrar, d deps.Deps) {
	// 근거 조항 원문: 규정 검색 서비스(B12)가 ClauseBy 를 제공하면 연결되고, 아니면 "볼 수 없음"이다.
	reader, _ := d.Policy.(domain.ClauseReader)
	r.HandleFunc("GET", "/api/authority-clause", contract.PermDraftRead, clauseHandler{svc: service.ClauseService{Reader: reader}}.get)
	if d.Authority == nil {
		return
	}
	h := authorityHandler{svc: d.Authority}
	r.HandleFunc("GET", "/api/authority-rules", contract.PermAuthorityManage, h.list)
	r.HandleFunc("GET", "/api/authority-policies", contract.PermAuthorityManage, h.policies)
	r.HandleFunc("POST", "/api/authority-rules", contract.PermAuthorityManage, h.save)
	r.HandleFunc("PUT", "/api/authority-rules/{id}", contract.PermAuthorityManage, h.save)
	r.HandleFunc("DELETE", "/api/authority-rules/{id}", contract.PermAuthorityManage, h.del)
}

type authorityHandler struct{ svc domain.AuthorityService }

func (h authorityHandler) list(w http.ResponseWriter, r *http.Request) {
	rules, err := h.svc.List(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	out := make([]authorityRuleDTO, len(rules))
	for i, x := range rules {
		out[i] = toRuleDTO(x)
	}
	writeJSON(w, http.StatusOK, out)
}

func (h authorityHandler) policies(w http.ResponseWriter, r *http.Request) {
	list, err := h.svc.Policies(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	out := make([]map[string]string, len(list))
	for i, x := range list {
		out[i] = map[string]string{"slug": x.Slug, "title": x.Title}
	}
	writeJSON(w, http.StatusOK, out)
}

func (h authorityHandler) save(w http.ResponseWriter, r *http.Request) {
	var b authorityRuleDTO
	if !decode(w, r, &b) {
		return
	}
	status := http.StatusCreated
	if v := r.PathValue("id"); v != "" {
		id, ok := pathID(w, r, "id")
		if !ok {
			return
		}
		b.ID, status = id, http.StatusOK
	}
	rule, err := h.svc.Save(r.Context(), domain.AuthorityRuleInput{ID: b.ID, FormCode: b.FormCode, MinAmount: b.MinAmount, MaxAmount: b.MaxAmount,
		FinalLevel: b.FinalLevel, PolicySlug: b.PolicySlug, ClauseNo: b.ClauseNo, Active: b.Active})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, status, toRuleDTO(rule))
}

func (h authorityHandler) del(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	if err := h.svc.Delete(r.Context(), id); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

type clauseHandler struct{ svc service.ClauseService }

func (h clauseHandler) get(w http.ResponseWriter, r *http.Request) {
	hit, err := h.svc.Clause(r.Context(), r.URL.Query().Get("slug"), r.URL.Query().Get("no"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"docSlug": hit.DocSlug, "docTitle": hit.DocTitle, "clauseNo": hit.ClauseNo, "heading": hit.Heading, "source": hit.Source, "text": hit.Text})
}
