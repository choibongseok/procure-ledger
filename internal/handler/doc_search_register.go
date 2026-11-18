// 문서 통합 검색 라우트(B29 ②, 4절). 권한은 등록기가 선언하고 서비스가 한 번 더 확인한다.
package handler

import (
	"net/http"
	"strings"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/deps"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
)

// RegisterDocSearch 는 GET /api/docs/search 를 등록한다. 호출: cmd/server/routes_erp.go.
func RegisterDocSearch(r contract.Registrar, d deps.Deps) {
	if d.DocSearch == nil {
		return
	}
	h := docSearchHandler{svc: d.DocSearch}
	r.HandleFunc("GET", "/api/docs/search", contract.PermDraftRead, h.search)
}

type docSearchHandler struct{ svc domain.DocSearchService }

func (h docSearchHandler) search(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	var statuses []string
	if v := q.Get("status"); v != "" {
		statuses = strings.Split(v, ",")
	}
	res, err := h.svc.Search(r.Context(), domain.DocSearchFilter{Query: q.Get("q"), From: q.Get("from"), To: q.Get("to"), FormCode: q.Get("formCode"),
		Statuses: statuses, Requester: q.Get("requester"), Offset: queryInt(r, "offset", 0)})
	if err != nil {
		writeErr(w, err)
		return
	}
	rows := make([]map[string]any, len(res.Rows))
	for i, x := range res.Rows {
		rows[i] = map[string]any{"id": x.ID, "no": x.No, "title": x.Title, "formCode": x.FormCode, "formName": x.FormName, "formKind": x.FormKind, "requesterName": x.RequesterName,
			"unitPath": x.UnitPath, "status": x.Status, "disposed": x.Disposed, "createdAt": rfc(x.CreatedAt), "updatedAt": rfc(x.UpdatedAt), "matched": x.Matched}
	}
	writeJSON(w, http.StatusOK, map[string]any{"rows": rows, "total": res.Total, "pageSize": 50})
}
