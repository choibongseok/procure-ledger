// (B45 1단계, 게이트웨이 연결은 ai 몫이지만 이번엔 platform 이 함께 맡았다). LLM 공급자·라우팅 규칙 관리 라우트.
package handler

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/deps"
	"github.com/choibongseok/procure-ledger/internal/llmgw"
	"github.com/choibongseok/procure-ledger/internal/rbac"
)

// RegisterLLMProviders 는 공급자·라우팅 관리 API 를 선언과 함께 등록한다(B45 명세 8·10절).
//
//	GET    /api/admin/llm/providers              llm:provider_manage  공급자 목록(비밀 없음)
//	POST   /api/admin/llm/providers              llm:provider_manage  등록(세션 전용)
//	PUT    /api/admin/llm/providers/{id}         llm:provider_manage  수정(세션 전용, 비밀 칸이 비면 그대로)
//	POST   /api/admin/llm/providers/{id}/active  llm:provider_manage  켜기·끄기 {active}(세션 전용)
//	POST   /api/admin/llm/providers/{id}/test    llm:provider_manage  시험 연결(세션 전용)
//	GET    /api/admin/llm/routes                 llm:policy_manage    규칙 목록
//	POST   /api/admin/llm/routes                 llm:policy_manage    등록
//	PUT    /api/admin/llm/routes/{id}            llm:policy_manage    수정
//	DELETE /api/admin/llm/routes/{id}            llm:policy_manage    삭제
//	POST   /api/admin/llm/routes/simulate        llm:policy_manage    "이 호출은 어디로 가나"(호출 없음)
func RegisterLLMProviders(r contract.Registrar, d deps.Deps) {
	if d.LLMProviders == nil {
		return
	}
	h := &llmProvHandler{a: d.LLMProviders}
	so := func(f http.HandlerFunc) http.HandlerFunc { return rbac.SessionOnly(d.Audit, d.Txs, f) }
	pm, pol := contract.PermLLMProviderManage, contract.PermLLMPolicyManage
	r.HandleFunc("GET", "/api/admin/llm/providers", pm, h.listProviders)
	r.HandleFunc("POST", "/api/admin/llm/providers", pm, so(h.saveProvider))
	r.HandleFunc("PUT", "/api/admin/llm/providers/{id}", pm, so(h.saveProvider))
	r.HandleFunc("POST", "/api/admin/llm/providers/{id}/active", pm, so(h.setActive))
	r.HandleFunc("POST", "/api/admin/llm/providers/{id}/test", pm, so(h.testProvider))
	r.HandleFunc("GET", "/api/admin/llm/routes", pol, h.listRoutes)
	r.HandleFunc("POST", "/api/admin/llm/routes", pol, h.saveRoute)
	r.HandleFunc("PUT", "/api/admin/llm/routes/{id}", pol, h.saveRoute)
	r.HandleFunc("DELETE", "/api/admin/llm/routes/{id}", pol, h.deleteRoute)
	r.HandleFunc("POST", "/api/admin/llm/routes/simulate", pol, h.simulate)
}

type llmProvHandler struct{ a *llmgw.ProviderAdmin }

func llmErr(w http.ResponseWriter, err error) {
	trim := func(p error) string { return strings.TrimPrefix(err.Error(), p.Error()+": ") }
	switch {
	case errors.Is(err, contract.ErrUnauthenticated):
		aiWriteJSON(w, http.StatusUnauthorized, map[string]string{"error": "로그인이 필요합니다"})
	case errors.Is(err, contract.ErrForbidden):
		aiWriteJSON(w, http.StatusForbidden, map[string]string{"error": "권한이 없습니다"})
	case errors.Is(err, contract.ErrNotFound):
		aiWriteJSON(w, http.StatusNotFound, map[string]string{"error": "찾을 수 없습니다"})
	case errors.Is(err, contract.ErrInvalidState):
		aiWriteJSON(w, http.StatusConflict, map[string]string{"error": trim(contract.ErrInvalidState)})
	case errors.Is(err, contract.ErrValidation):
		aiWriteJSON(w, http.StatusBadRequest, map[string]string{"error": trim(contract.ErrValidation)})
	default:
		log.Printf("llm admin: %v", err)
		aiWriteJSON(w, http.StatusInternalServerError, map[string]string{"error": "처리 중 오류가 발생했습니다. 잠시 후 다시 시도해 주세요."})
	}
}

func llmDecode(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(v); err != nil {
		llmErr(w, errBadBody)
		return false
	}
	return true
}

var errBadBody = errors.Join(contract.ErrValidation)

func (h *llmProvHandler) listProviders(w http.ResponseWriter, r *http.Request) {
	out, err := h.a.List(r.Context())
	if err != nil {
		llmErr(w, err)
		return
	}
	aiWriteJSON(w, http.StatusOK, out)
}

func (h *llmProvHandler) saveProvider(w http.ResponseWriter, r *http.Request) {
	var in llmgw.ProviderInput
	if !llmDecode(w, r, &in) {
		return
	}
	if v := r.PathValue("id"); v != "" {
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil || id <= 0 {
			llmErr(w, contract.ErrNotFound)
			return
		}
		in.ID = id
	} else {
		in.ID = 0
	}
	out, err := h.a.SaveProvider(r.Context(), in)
	if err != nil {
		llmErr(w, err)
		return
	}
	code := http.StatusOK
	if r.Method == http.MethodPost {
		code = http.StatusCreated
	}
	aiWriteJSON(w, code, out)
}

func (h *llmProvHandler) setActive(w http.ResponseWriter, r *http.Request) {
	id, ok := aiPathID(r)
	if !ok {
		llmErr(w, contract.ErrNotFound)
		return
	}
	var in struct {
		Active bool `json:"active"`
	}
	if !llmDecode(w, r, &in) {
		return
	}
	out, err := h.a.SetActive(r.Context(), id, in.Active)
	if err != nil {
		llmErr(w, err)
		return
	}
	aiWriteJSON(w, http.StatusOK, out)
}

func (h *llmProvHandler) testProvider(w http.ResponseWriter, r *http.Request) {
	id, ok := aiPathID(r)
	if !ok {
		llmErr(w, contract.ErrNotFound)
		return
	}
	out, err := h.a.Test(r.Context(), id)
	if err != nil {
		llmErr(w, err)
		return
	}
	aiWriteJSON(w, http.StatusOK, out)
}

func (h *llmProvHandler) listRoutes(w http.ResponseWriter, r *http.Request) {
	out, err := h.a.ListRoutes(r.Context())
	if err != nil {
		llmErr(w, err)
		return
	}
	aiWriteJSON(w, http.StatusOK, out)
}

func (h *llmProvHandler) saveRoute(w http.ResponseWriter, r *http.Request) {
	in := llmgw.RouteInput{Active: true}
	if !llmDecode(w, r, &in) {
		return
	}
	if v := r.PathValue("id"); v != "" {
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil || id <= 0 {
			llmErr(w, contract.ErrNotFound)
			return
		}
		in.ID = id
	} else {
		in.ID = 0
	}
	out, err := h.a.SaveRoute(r.Context(), in)
	if err != nil {
		var ge *llmgw.GateError
		if errors.As(err, &ge) {
			// 저장 전 평가가 확인을 요구한다(기준선 미달 또는 외부 후보의 비용 확인). 저장은 일어나지 않았다.
			aiWriteJSON(w, http.StatusConflict, map[string]any{"error": ge.Result.Message, "gate": ge.Result})
			return
		}
		llmErr(w, err)
		return
	}
	code := http.StatusOK
	if r.Method == http.MethodPost {
		code = http.StatusCreated
	}
	aiWriteJSON(w, code, out)
}

func (h *llmProvHandler) deleteRoute(w http.ResponseWriter, r *http.Request) {
	id, ok := aiPathID(r)
	if !ok {
		llmErr(w, contract.ErrNotFound)
		return
	}
	if err := h.a.DeleteRoute(r.Context(), id); err != nil {
		llmErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *llmProvHandler) simulate(w http.ResponseWriter, r *http.Request) {
	var in llmgw.SimInput
	if !llmDecode(w, r, &in) {
		return
	}
	out, err := h.a.Simulate(r.Context(), in)
	if err != nil {
		llmErr(w, err)
		return
	}
	aiWriteJSON(w, http.StatusOK, out)
}
