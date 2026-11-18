package taxaspadmin

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strings"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/rbac"
)

// Register 는 대행사 설정 화면 API 를 선언과 함께 등록한다(명세 7·9절). 전부 taxasp:manage 이고 변경·시험은 로그인 세션만(개인 토큰·API 토큰은 403)이다.
func (s *Service) Register(reg contract.Registrar) {
	so := func(h http.HandlerFunc) http.HandlerFunc { return rbac.SessionOnly(s.o.Audit, s.o.Txs, h) }
	m := contract.PermTaxASPManage
	reg.HandleFunc("GET", "/api/admin/tax-asp", m, s.hGet)
	reg.HandleFunc("PUT", "/api/admin/tax-asp", m, so(s.hSave))
	reg.HandleFunc("POST", "/api/admin/tax-asp/activate", m, so(s.hActive(true)))
	reg.HandleFunc("POST", "/api/admin/tax-asp/deactivate", m, so(s.hActive(false)))
	reg.HandleFunc("POST", "/api/admin/tax-asp/test", m, so(s.hTest))
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
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "권한이 없습니다"})
	case errors.Is(err, contract.ErrNotFound):
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "찾을 수 없습니다"})
	case errors.Is(err, contract.ErrInvalidState):
		writeJSON(w, http.StatusConflict, map[string]string{"error": msg(contract.ErrInvalidState)})
	case errors.Is(err, contract.ErrValidation):
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": msg(contract.ErrValidation)})
	default:
		log.Printf("taxaspadmin: 처리 실패") // 오류 문장에 값이 섞이지 않도록 원문을 찍지 않는다
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "처리 중 오류가 발생했습니다. 잠시 후 다시 시도해 주세요."})
	}
}

func (s *Service) hGet(w http.ResponseWriter, r *http.Request) {
	v, err := s.Get(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Service) hSave(w http.ResponseWriter, r *http.Request) {
	var in SaveInput
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<17)).Decode(&in); err != nil {
		writeErr(w, valErr("요청 형식이 올바르지 않습니다"))
		return
	}
	v, err := s.Save(r.Context(), in)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Service) hActive(on bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		v, err := s.SetActive(r.Context(), on)
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, v)
	}
}

func (s *Service) hTest(w http.ResponseWriter, r *http.Request) {
	res, err := s.Test(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}
