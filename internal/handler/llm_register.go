// (internal/handler/llm*).
package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/deps"
	"github.com/choibongseok/procure-ledger/internal/llmgw"
)

// RegisterLLM 은 LLM 사용량 조회·모델 정책 라우트(/api/llm...)를 등록한다.
// 호출: cmd/server/routes_ai.go.
//
//	GET    /api/llm/usage?days=7        llm:usage_read     일·역할·모델·용도별 집계
//	GET    /api/llm/usage/today         authenticated      오늘 본인 호출 수·토큰·한도 대비(전사 합계는 llm:usage_read 일 때만)
//	GET    /api/llm/usage/limits        llm:usage_read     역할별 오늘 사용량과 하루 한도(관리자는 모든 역할, 그 밖은 본인 역할)
//	GET    /api/llm/policies            llm:policy_manage  모델 허용 규칙
//	POST   /api/llm/policies            llm:policy_manage
//	DELETE /api/llm/policies/{id}       llm:policy_manage
//	GET    /api/llm/quotas              llm:policy_manage  일일 토큰 한도
//	PUT    /api/llm/quotas              llm:policy_manage
//	GET    /api/eval/runs?limit=30      eval:read          평가 실행 이력(최신 순)
//	GET    /api/eval/compare            eval:read          공급자별 골든셋 비교 표(B45: 같은 골든셋 실행끼리, 기준 대비 회귀 표시)
//	GET    /api/mcp/usage?from=&to=     usage:read         AI 연결(MCP) 사용 현황(도구별·토큰별, B13). llm_mcpusage.go
func RegisterLLM(r contract.Registrar, d deps.Deps) {
	h := &llmHandler{st: &llmgw.Store{DB: d.DB, Txs: d.Txs, Audit: d.Audit}}
	r.HandleFunc("GET", "/api/llm/usage", contract.PermLLMUsageRead, h.usage)
	r.HandleFunc("GET", "/api/llm/usage/today", contract.PermAuthenticated, h.today)
	r.HandleFunc("GET", "/api/llm/usage/limits", contract.PermLLMUsageRead, h.limits)
	r.HandleFunc("GET", "/api/llm/policies", contract.PermLLMPolicyManage, h.listPolicies)
	r.HandleFunc("POST", "/api/llm/policies", contract.PermLLMPolicyManage, h.addPolicy)
	r.HandleFunc("DELETE", "/api/llm/policies/{id}", contract.PermLLMPolicyManage, h.deletePolicy)
	r.HandleFunc("GET", "/api/llm/quotas", contract.PermLLMPolicyManage, h.listQuotas)
	r.HandleFunc("PUT", "/api/llm/quotas", contract.PermLLMPolicyManage, h.setQuota)
	r.HandleFunc("GET", "/api/eval/runs", contract.PermEvalRead, h.evalRuns)
	r.HandleFunc("GET", "/api/eval/compare", contract.PermEvalRead, h.evalCompare)
	registerMCPUsage(r, d)
}

type llmHandler struct{ st *llmgw.Store }

func (h *llmHandler) usage(w http.ResponseWriter, r *http.Request) {
	days, _ := strconv.Atoi(r.URL.Query().Get("days"))
	rows, err := h.st.UsageSummary(r.Context(), days)
	if err != nil {
		aiWriteErr(w, err)
		return
	}
	aiWriteJSON(w, http.StatusOK, map[string]any{"rows": rows})
}

func (h *llmHandler) today(w http.ResponseWriter, r *http.Request) {
	t, err := h.st.TodaySummary(r.Context())
	if err != nil {
		aiWriteErr(w, err)
		return
	}
	aiWriteJSON(w, http.StatusOK, t)
}

func (h *llmHandler) limits(w http.ResponseWriter, r *http.Request) {
	rows, err := h.st.RoleLimits(r.Context())
	if err != nil {
		aiWriteErr(w, err)
		return
	}
	aiWriteJSON(w, http.StatusOK, map[string]any{"roles": rows})
}

func (h *llmHandler) evalRuns(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	runs, err := h.st.ListEvalRuns(r.Context(), limit)
	if err != nil {
		aiWriteErr(w, err)
		return
	}
	aiWriteJSON(w, http.StatusOK, map[string]any{"runs": runs})
}

func (h *llmHandler) listPolicies(w http.ResponseWriter, r *http.Request) {
	ps, err := h.st.ListPolicies(r.Context())
	if err != nil {
		aiWriteErr(w, err)
		return
	}
	aiWriteJSON(w, http.StatusOK, map[string]any{"policies": ps})
}

func (h *llmHandler) addPolicy(w http.ResponseWriter, r *http.Request) {
	var p llmgw.Policy
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&p); err != nil {
		aiWriteErr(w, errors.Join(contract.ErrValidation, errors.New("요청 본문을 읽을 수 없습니다")))
		return
	}
	id, err := h.st.AddPolicy(r.Context(), p)
	if err != nil {
		aiWriteErr(w, err)
		return
	}
	p.ID = id
	aiWriteJSON(w, http.StatusCreated, p)
}

func (h *llmHandler) deletePolicy(w http.ResponseWriter, r *http.Request) {
	id, ok := aiPathID(r)
	if !ok {
		aiWriteErr(w, contract.ErrNotFound)
		return
	}
	if err := h.st.DeletePolicy(r.Context(), id); err != nil {
		aiWriteErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *llmHandler) listQuotas(w http.ResponseWriter, r *http.Request) {
	qs, err := h.st.ListQuotas(r.Context())
	if err != nil {
		aiWriteErr(w, err)
		return
	}
	aiWriteJSON(w, http.StatusOK, map[string]any{"quotas": qs})
}

func (h *llmHandler) setQuota(w http.ResponseWriter, r *http.Request) {
	// 한도는 포인터로 받아 null·누락을 0 으로 저장하지 않고 거부한다(0 은 "AI 호출 모두 차단"이라는 별개의 값이다).
	var in struct {
		ScopeType  string `json:"scopeType"`
		ScopeValue string `json:"scopeValue"`
		DailyLimit *int64 `json:"dailyTokenLimit"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in); err != nil {
		aiWriteErr(w, fmt.Errorf("%w: 요청 본문을 읽을 수 없습니다. 하루 한도는 0 이상의 정수여야 합니다", contract.ErrValidation))
		return
	}
	if in.DailyLimit == nil {
		aiWriteErr(w, fmt.Errorf("%w: 하루 한도(토큰)는 0 이상의 정수여야 합니다", contract.ErrValidation))
		return
	}
	q, err := h.st.SaveQuota(r.Context(), llmgw.Quota{ScopeType: in.ScopeType, ScopeValue: in.ScopeValue, DailyLimit: *in.DailyLimit})
	if err != nil {
		aiWriteErr(w, err)
		return
	}
	aiWriteJSON(w, http.StatusOK, q)
}

func (h *llmHandler) evalCompare(w http.ResponseWriter, r *http.Request) {
	c, err := h.st.CompareEvalRuns(r.Context())
	if err != nil {
		aiWriteErr(w, err)
		return
	}
	aiWriteJSON(w, http.StatusOK, c)
}
