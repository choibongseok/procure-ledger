package entity

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

// Register 는 법인 관리 화면 API 를 선언과 함께 등록한다(명세 15.1). 전부 entity:manage 이고 변경은 로그인 세션만(개인 토큰·API 토큰은 403)이다.
func (s *Service) Register(reg contract.Registrar) {
	so := func(h http.HandlerFunc) http.HandlerFunc { return rbac.SessionOnly(s.o.Audit, s.o.Txs, h) }
	m := contract.PermEntityManage
	reg.HandleFunc("GET", "/api/admin/entities", m, s.hList)
	reg.HandleFunc("POST", "/api/admin/entities", m, so(s.hCreate))
	reg.HandleFunc("GET", "/api/admin/entities/{id}", m, s.hGet)
	reg.HandleFunc("PUT", "/api/admin/entities/{id}", m, so(s.hUpdate))
	reg.HandleFunc("POST", "/api/admin/entities/{id}/activate", m, so(s.hActive(true)))
	reg.HandleFunc("POST", "/api/admin/entities/{id}/deactivate", m, so(s.hActive(false)))
	reg.HandleFunc("PUT", "/api/admin/entities/{id}/root", m, so(s.hRoot))
	reg.HandleFunc("GET", "/api/admin/entities/{id}/sites", m, s.hSites)
	reg.HandleFunc("POST", "/api/admin/entities/{id}/sites", m, so(s.hSaveSite))
	reg.HandleFunc("PUT", "/api/admin/entities/{id}/sites/{siteId}", m, so(s.hSaveSite))
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
		log.Printf("entity: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "처리 중 오류가 발생했습니다. 잠시 후 다시 시도해 주세요."})
	}
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(v); err != nil {
		writeErr(w, valErr("요청 형식이 올바르지 않습니다"))
		return false
	}
	return true
}

func pathInt(w http.ResponseWriter, r *http.Request, name string) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue(name), 10, 64)
	if err != nil || id <= 0 {
		writeErr(w, contract.ErrNotFound)
		return 0, false
	}
	return id, true
}

func (s *Service) hList(w http.ResponseWriter, r *http.Request) {
	out, err := s.List(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Service) hGet(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt(w, r, "id")
	if !ok {
		return
	}
	e, err := s.Get(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, e)
}

func (s *Service) hCreate(w http.ResponseWriter, r *http.Request) {
	var in Input
	if !decode(w, r, &in) {
		return
	}
	e, err := s.Create(r.Context(), in)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, e)
}

func (s *Service) hUpdate(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt(w, r, "id")
	if !ok {
		return
	}
	var in Input
	if !decode(w, r, &in) {
		return
	}
	e, err := s.Update(r.Context(), id, in)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, e)
}

func (s *Service) hActive(on bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathInt(w, r, "id")
		if !ok {
			return
		}
		e, err := s.SetActive(r.Context(), id, on)
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, e)
	}
}

func (s *Service) hRoot(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt(w, r, "id")
	if !ok {
		return
	}
	var in struct {
		RootUnitID int64 `json:"rootUnitId"`
	}
	if !decode(w, r, &in) {
		return
	}
	e, err := s.SetRoot(r.Context(), id, in.RootUnitID)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, e)
}

func (s *Service) hSites(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt(w, r, "id")
	if !ok {
		return
	}
	out, err := s.ListSites(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Service) hSaveSite(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt(w, r, "id")
	if !ok {
		return
	}
	var siteID int64
	if v := r.PathValue("siteId"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n <= 0 {
			writeErr(w, contract.ErrNotFound)
			return
		}
		siteID = n
	}
	var in SiteInput
	if !decode(w, r, &in) {
		return
	}
	t, err := s.SaveSite(r.Context(), id, siteID, in)
	if err != nil {
		writeErr(w, err)
		return
	}
	status := http.StatusOK
	if siteID == 0 {
		status = http.StatusCreated
	}
	writeJSON(w, status, t)
}
