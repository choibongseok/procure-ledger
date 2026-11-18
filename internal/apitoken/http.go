package apitoken

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/rbac"
)

// Register 는 연동 관리 화면 API 를 선언과 함께 등록한다. 발급·폐기·설정 변경은 로그인 세션만(개인 토큰·API 토큰은 403)이다.
func (s *Service) Register(reg contract.Registrar) {
	reg.HandleFunc("GET", "/api/admin/integrations/settings", contract.PermAPIManage, s.hGetSettings)
	reg.HandleFunc("PUT", "/api/admin/integrations/settings", contract.PermAPIManage, rbac.SessionOnly(s.audit, s.txs, s.hPutSettings))
	reg.HandleFunc("GET", "/api/admin/integrations/scopes", contract.PermAPIManage, s.hScopes)
	reg.HandleFunc("GET", "/api/admin/integrations/tokens", contract.PermAPIManage, s.hList)
	reg.HandleFunc("POST", "/api/admin/integrations/tokens", contract.PermAPIManage, rbac.SessionOnly(s.audit, s.txs, s.hIssue))
	reg.HandleFunc("DELETE", "/api/admin/integrations/tokens/{id}", contract.PermAPIManage, rbac.SessionOnly(s.audit, s.txs, s.hRevoke))
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, err error) {
	msg := func(prefix error) string { return strings.TrimPrefix(err.Error(), prefix.Error()+": ") }
	switch {
	case errors.Is(err, contract.ErrUnauthenticated):
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "로그인이 필요합니다"})
	case errors.Is(err, contract.ErrForbidden), errors.Is(err, contract.ErrSetupRequired):
		m := "권한이 없습니다"
		if strings.Contains(err.Error(), "발급하는 분") || strings.Contains(err.Error(), "로그인 세션") {
			m = msg(contract.ErrForbidden)
		}
		writeJSON(w, http.StatusForbidden, map[string]string{"error": m})
	case errors.Is(err, contract.ErrNotFound):
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "찾을 수 없습니다"})
	case errors.Is(err, contract.ErrInvalidState):
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
	case errors.Is(err, contract.ErrValidation):
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": msg(contract.ErrValidation)})
	default:
		log.Printf("apitoken: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "처리 중 오류가 발생했습니다. 잠시 후 다시 시도해 주세요."})
	}
}

func principal(w http.ResponseWriter, r *http.Request) (contract.Principal, bool) {
	p, ok := contract.PrincipalFrom(r.Context())
	if !ok {
		writeErr(w, contract.ErrUnauthenticated)
	}
	return p, ok
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(v); err != nil {
		writeErr(w, verr("요청 형식이 올바르지 않습니다"))
		return false
	}
	return true
}

func (s *Service) hGetSettings(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	out, err := s.GetSettings(r.Context(), p)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Service) hPutSettings(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	var in struct {
		Enabled     *bool `json:"enabled"`
		RatePerMin  *int  `json:"ratePerMin"`
		WritePerMin *int  `json:"writePerMin"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.Enabled == nil && in.RatePerMin == nil && in.WritePerMin == nil {
		writeErr(w, verr("바꿀 설정이 없습니다"))
		return
	}
	if in.RatePerMin != nil {
		if err := s.SetRate(r.Context(), p, *in.RatePerMin); err != nil {
			writeErr(w, err)
			return
		}
	}
	if in.WritePerMin != nil {
		if err := s.SetWriteRate(r.Context(), p, *in.WritePerMin); err != nil {
			writeErr(w, err)
			return
		}
	}
	if in.Enabled != nil {
		if err := s.SetEnabled(r.Context(), p, *in.Enabled); err != nil {
			writeErr(w, err)
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) hScopes(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	out, err := s.Scopes(r.Context(), p)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Service) hList(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	out, err := s.List(r.Context(), p)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Service) hIssue(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	var in IssueInput
	if !decode(w, r, &in) {
		return
	}
	raw, info, err := s.Issue(r.Context(), p, in)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"token": raw, "info": info, "warnings": s.IssueWarnings(r.Context(), p, info.Scopes)})
}

func (s *Service) hRevoke(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, contract.ErrNotFound)
		return
	}
	if err := s.Revoke(r.Context(), p, id); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
