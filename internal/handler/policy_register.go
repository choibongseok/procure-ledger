// (internal/handler/policy*).
package handler

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/deps"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
)

// RegisterPolicy 는 사내 규정 검색 라우트를 등록한다(B12). 호출: cmd/server/routes_ai.go.
//
//	GET /api/policies/search?q=...&limit=5               policy:search  질의 검색
//	GET /api/policies/related?amount=<원>&items=<a,b>    policy:search  구매요청 작성 화면용: 금액 구간의 결재선 조항 + 품목 관련 재고·구매 조항
//
// 응답은 {"hits":[{doc_slug, doc_title, clause_no, heading, text, source, score}]} 다.
// 열람 범위 밖 문서는 결과에 없고, 전체 건수·"더 있음" 같은 필드도 없다(범위 밖 문서의 존재가 새지 않게).
// 검색은 감사 로그에 남고 기록에 실패하면 결과 없이 오류가 난다. 강제는 서비스 계층이 한다.
func RegisterPolicy(r contract.Registrar, d deps.Deps) {
	h := &policyHandler{svc: d.Policy}
	r.HandleFunc("GET", "/api/policies/search", contract.PermPolicySearch, h.search)
	r.HandleFunc("GET", "/api/policies/related", contract.PermPolicySearch, h.related)
}

type policyHandler struct{ svc domain.PolicyService }

type policyHitJSON struct {
	DocSlug  string `json:"doc_slug"`
	DocTitle string `json:"doc_title"`
	ClauseNo string `json:"clause_no"`
	Heading  string `json:"heading"`
	Text     string `json:"text"`
	Source   string `json:"source"`
	Score    int    `json:"score"`
	// 작성 화면용 관련 조항(/related)에서만 채운다. reason: tier | item
	Reason      string `json:"reason,omitempty"`
	ReasonLabel string `json:"reason_label,omitempty"`
}

var reasonLabels = map[string]string{"tier": "금액 구간 결재선", "item": "품목 관련 규정"}

func writeHits(w http.ResponseWriter, hits []domain.PolicyHit) {
	out := make([]policyHitJSON, 0, len(hits))
	for _, h := range hits {
		out = append(out, policyHitJSON{h.DocSlug, h.DocTitle, h.ClauseNo, h.Heading, h.Text, h.Source, h.Score, h.Reason, reasonLabels[h.Reason]})
	}
	aiWriteJSON(w, http.StatusOK, map[string]any{"hits": out})
}

func (h *policyHandler) search(w http.ResponseWriter, r *http.Request) {
	if h.svc == nil {
		aiWriteJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "사내 규정 검색을 사용할 수 없습니다"})
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	hits, err := h.svc.Search(r.Context(), r.URL.Query().Get("q"), limit)
	if err != nil {
		aiWriteErr(w, err)
		return
	}
	writeHits(w, hits)
}

func (h *policyHandler) related(w http.ResponseWriter, r *http.Request) {
	if h.svc == nil {
		aiWriteJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "사내 규정 검색을 사용할 수 없습니다"})
		return
	}
	q := r.URL.Query()
	var amount int64
	if s := strings.TrimSpace(q.Get("amount")); s != "" {
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil || n < 0 {
			aiWriteJSON(w, http.StatusBadRequest, map[string]string{"error": "금액은 0 이상의 정수여야 합니다"})
			return
		}
		amount = n
	}
	var items []string
	if s := q.Get("items"); s != "" {
		items = strings.Split(s, ",")
	}
	hits, err := h.svc.Related(r.Context(), domain.PolicyRelatedInput{Amount: amount, Items: items})
	if err != nil {
		aiWriteErr(w, err)
		return
	}
	writeHits(w, hits)
}
