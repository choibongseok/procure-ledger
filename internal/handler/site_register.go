// 현재 법인의 활성 사업장 목록(B44 3단계). 전표·창고·부가세 화면의 선택기가 쓴다. 로그인한 사람이면 읽을 수 있다(이름과 부가세 방식뿐이다).
package handler

import (
	"net/http"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/deps"
)

// RegisterSites 는 GET /api/entity/sites 를 등록한다. 호출: cmd/server/routes_erp.go.
func RegisterSites(r contract.Registrar, d deps.Deps) {
	if d.Sites == nil {
		return
	}
	r.HandleFunc("GET", "/api/entity/sites", contract.PermAuthenticated, func(w http.ResponseWriter, req *http.Request) {
		rows, err := d.Sites.List(req.Context())
		if err != nil {
			writeErr(w, err)
			return
		}
		out := make([]map[string]any, len(rows))
		for i, t := range rows {
			out[i] = map[string]any{"id": t.ID, "siteNo": t.SiteNo, "name": t.Name, "kind": t.Kind, "vatMode": t.VATMode}
		}
		writeJSON(w, http.StatusOK, map[string]any{"sites": out})
	})
}
