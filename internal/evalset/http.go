package evalset

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/choibongseok/procure-ledger/internal/contract"
)

// Register 는 평가 세트 API 를 선언과 함께 등록한다(명세 10절).
//
//	GET    /api/admin/eval/sets                        eval:read   세트 목록
//	POST   /api/admin/eval/sets                        eval:write  세트 만들기
//	GET    /api/admin/eval/sets/{code}?version=N       eval:read   상세(버전·문항)
//	PUT    /api/admin/eval/sets/{code}                 eval:write  이름·설명·허용 하락·사용 여부
//	POST   /api/admin/eval/sets/{code}/draft           eval:write  새 초안(직전 게시 버전 복사)
//	POST   /api/admin/eval/sets/{code}/items           eval:write  문항 추가(초안)
//	PUT    /api/admin/eval/sets/{code}/items/{id}      eval:write  문항 수정(초안)
//	DELETE /api/admin/eval/sets/{code}/items/{id}      eval:write  문항 삭제(초안, ?revision=)
//	POST   /api/admin/eval/sets/{code}/publish         eval:write  게시 {note}
//	POST   /api/admin/eval/sets/{code}/import          eval:write  CSV 가져오기(본문 = CSV)
//	GET    /api/admin/eval/sets/{code}/export?version= eval:read   CSV 내보내기
func (s *Service) Register(reg contract.Registrar) {
	s.RegisterRuns(reg)
	r, w := contract.PermEvalRead, contract.PermEvalWrite
	reg.HandleFunc("GET", "/api/admin/eval/sets", r, s.hList)
	reg.HandleFunc("POST", "/api/admin/eval/sets", w, s.hCreate)
	reg.HandleFunc("GET", "/api/admin/eval/sets/{code}", r, s.hGet)
	reg.HandleFunc("PUT", "/api/admin/eval/sets/{code}", w, s.hUpdate)
	reg.HandleFunc("POST", "/api/admin/eval/sets/{code}/draft", w, s.hDraft)
	reg.HandleFunc("POST", "/api/admin/eval/sets/{code}/items", w, s.hSaveItem)
	reg.HandleFunc("PUT", "/api/admin/eval/sets/{code}/items/{id}", w, s.hSaveItem)
	reg.HandleFunc("DELETE", "/api/admin/eval/sets/{code}/items/{id}", w, s.hDeleteItem)
	reg.HandleFunc("POST", "/api/admin/eval/sets/{code}/publish", w, s.hPublish)
	reg.HandleFunc("POST", "/api/admin/eval/sets/{code}/import", w, s.hImport)
	reg.HandleFunc("GET", "/api/admin/eval/sets/{code}/export", r, s.hExport)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, err error) {
	trim := func(p error) string { return strings.TrimPrefix(err.Error(), p.Error()+": ") }
	switch {
	case errors.Is(err, contract.ErrUnauthenticated):
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "로그인이 필요합니다"})
	case errors.Is(err, contract.ErrForbidden):
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "권한이 없습니다"})
	case errors.Is(err, contract.ErrNotFound):
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "찾을 수 없습니다"})
	case errors.Is(err, contract.ErrInvalidState):
		writeJSON(w, http.StatusConflict, map[string]string{"error": trim(contract.ErrInvalidState)})
	case errors.Is(err, contract.ErrValidation):
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": trim(contract.ErrValidation)})
	default:
		log.Printf("evalset: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "처리 중 오류가 발생했습니다. 잠시 후 다시 시도해 주세요."})
	}
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<17)).Decode(v); err != nil {
		writeErr(w, valErr("요청 형식이 올바르지 않습니다"))
		return false
	}
	return true
}

func (s *Service) hList(w http.ResponseWriter, r *http.Request) {
	out, err := s.ListSets(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sets": out})
}

func (s *Service) hCreate(w http.ResponseWriter, r *http.Request) {
	var in SetInput
	if !decode(w, r, &in) {
		return
	}
	out, err := s.CreateSet(r.Context(), in)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

func versionParam(r *http.Request) int {
	n, _ := strconv.Atoi(r.URL.Query().Get("version"))
	return n
}

func (s *Service) hGet(w http.ResponseWriter, r *http.Request) {
	out, err := s.Get(r.Context(), r.PathValue("code"), versionParam(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Service) hUpdate(w http.ResponseWriter, r *http.Request) {
	var in SetInput
	if !decode(w, r, &in) {
		return
	}
	out, err := s.UpdateSet(r.Context(), r.PathValue("code"), in)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Service) hDraft(w http.ResponseWriter, r *http.Request) {
	out, err := s.NewDraft(r.Context(), r.PathValue("code"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

func (s *Service) hSaveItem(w http.ResponseWriter, r *http.Request) {
	var in ItemInput
	if !decode(w, r, &in) {
		return
	}
	if v := r.PathValue("id"); v != "" {
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil || id <= 0 {
			writeErr(w, contract.ErrNotFound)
			return
		}
		in.ID = id
	} else {
		in.ID = 0
	}
	out, err := s.SaveItem(r.Context(), r.PathValue("code"), in)
	if err != nil {
		writeErr(w, err)
		return
	}
	code := http.StatusOK
	if r.Method == http.MethodPost {
		code = http.StatusCreated
	}
	writeJSON(w, code, out)
}

func (s *Service) hDeleteItem(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeErr(w, contract.ErrNotFound)
		return
	}
	rev, _ := strconv.Atoi(r.URL.Query().Get("revision"))
	newRev, err := s.DeleteItem(r.Context(), r.PathValue("code"), id, rev)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"revision": newRev})
}

func (s *Service) hPublish(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Note string `json:"note"`
	}
	if r.ContentLength != 0 && !decode(w, r, &in) {
		return
	}
	out, err := s.Publish(r.Context(), r.PathValue("code"), in.Note)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Service) hImport(w http.ResponseWriter, r *http.Request) {
	data, err := io.ReadAll(io.LimitReader(r.Body, maxCSVBytes+1))
	if err != nil {
		writeErr(w, valErr("파일을 읽지 못했습니다."))
		return
	}
	out, err := s.ImportCSV(r.Context(), r.PathValue("code"), data)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Service) hExport(w http.ResponseWriter, r *http.Request) {
	data, name, err := s.ExportCSV(r.Context(), r.PathValue("code"), versionParam(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"; filename*=UTF-8''`+url.PathEscape(name))
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(data)
}
