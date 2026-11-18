package importflow

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
)

// uploadLimit 은 업로드 본문 상한이다(파일 5MB + 양식 필드 여유). 파일 크기 검사는 파서가 한 번 더 한다.
const uploadLimit = 5<<20 + 64<<10

// Register 는 가져오기 라우트를 선언과 함께 등록한다.
//
// 권한은 대상마다 다르다(마스터 import:master, 금액성 import:balance). 한 라우트에는 권한 하나만 선언할 수 있으므로
// 대상별 경로를 따로 등록한다: /api/imports/templates/{대상}, /api/imports/{대상}/preview, /api/imports/{대상}/batches/{묶음}/apply·cancel·errors.
// 서비스가 같은 권한을 한 번 더 확인한다. 이력·상세·오류 파일 조회는 import:read 다.
// 쓰기 라우트는 개인 토큰이 아니라 로그인 세션으로만 부를 수 있게 하지는 않는다(MCP 에 노출하지 않으며 화면이 부른다).
func (s *Service) Register(r contract.Registrar) {
	r.HandleFunc("GET", "/api/imports/targets", contract.PermAuthenticated, s.hTargets) // 서비스가 가져오기 권한(마스터·금액성·조회) 중 하나를 요구한다
	for _, t := range domain.ImportTargets {
		t := t
		perm := domain.ImportPerm(t)
		r.HandleFunc("GET", "/api/imports/templates/"+t, perm, func(w http.ResponseWriter, req *http.Request) { s.hTemplate(w, req, t) })
		r.HandleFunc("POST", "/api/imports/"+t+"/preview", perm, func(w http.ResponseWriter, req *http.Request) { s.hPreview(w, req, t) })
		r.HandleFunc("POST", "/api/imports/"+t+"/batches/{batch}/apply", perm, func(w http.ResponseWriter, req *http.Request) { s.hApply(w, req, t) })
		r.HandleFunc("POST", "/api/imports/"+t+"/batches/{batch}/cancel", perm, func(w http.ResponseWriter, req *http.Request) { s.hCancel(w, req, t) })
		r.HandleFunc("GET", "/api/imports/"+t+"/batches/{batch}/errors", perm, s.hErrors)
	}
	r.HandleFunc("GET", "/api/imports", contract.PermImportRead, s.hHistory)
	r.HandleFunc("GET", "/api/imports/{batch}", contract.PermImportRead, s.hBatch)
	r.HandleFunc("GET", "/api/imports/{batch}/errors", contract.PermImportRead, s.hErrors)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// httpErr 는 계약 오류를 상태 코드와 한국어 문구로 옮긴다. 알 수 없는 오류의 내용은 밖으로 내지 않는다.
func httpErr(w http.ResponseWriter, err error) {
	code, msg := http.StatusInternalServerError, "처리 중 오류가 발생했습니다. 잠시 후 다시 시도해 주세요."
	switch {
	case errors.Is(err, contract.ErrUnauthenticated):
		code, msg = http.StatusUnauthorized, err.Error()
	case errors.Is(err, contract.ErrForbidden), errors.Is(err, contract.ErrSetupRequired):
		code, msg = http.StatusForbidden, err.Error()
	case errors.Is(err, contract.ErrNotFound):
		code, msg = http.StatusNotFound, err.Error()
	case errors.Is(err, contract.ErrValidation):
		code, msg = http.StatusBadRequest, err.Error()
	case errors.Is(err, contract.ErrInvalidState):
		code, msg = http.StatusConflict, err.Error()
	default:
		log.Printf("importflow: 내부 오류: %v", err)
	}
	http.Error(w, msg, code)
}

func sendFile(w http.ResponseWriter, f domain.ImportFile) {
	w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	w.Header().Set("Content-Disposition", `attachment; filename="data.xlsx"; filename*=UTF-8''`+url.PathEscape(f.Name))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Length", strconv.Itoa(len(f.Data)))
	_, _ = w.Write(f.Data)
}

// readUpload 는 멀티파트의 file 과 텍스트 필드를 읽는다. 본문은 상한까지만 읽는다.
func readUpload(w http.ResponseWriter, r *http.Request) (data []byte, fields map[string]string, ok bool) {
	r.Body = http.MaxBytesReader(w, r.Body, uploadLimit)
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		http.Error(w, "파일이 너무 크거나 올바르지 않습니다. 5MB 이하의 양식 파일(XLSX)만 올릴 수 있습니다.", http.StatusRequestEntityTooLarge)
		return nil, nil, false
	}
	defer func() {
		if r.MultipartForm != nil {
			_ = r.MultipartForm.RemoveAll()
		}
	}()
	f, _, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "가져올 파일을 선택하세요.", http.StatusBadRequest)
		return nil, nil, false
	}
	defer f.Close()
	data, err = io.ReadAll(io.LimitReader(f, uploadLimit))
	if err != nil {
		http.Error(w, "파일을 읽을 수 없습니다.", http.StatusBadRequest)
		return nil, nil, false
	}
	return data, map[string]string{"baseDate": r.FormValue("baseDate"), "token": r.FormValue("token")}, true
}

func (s *Service) hTargets(w http.ResponseWriter, r *http.Request) {
	out, err := s.Targets(r.Context())
	if err != nil {
		httpErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"targets": out})
}

func (s *Service) hTemplate(w http.ResponseWriter, r *http.Request, target string) {
	f, err := s.Template(r.Context(), target)
	if err != nil {
		httpErr(w, err)
		return
	}
	sendFile(w, f)
}

func (s *Service) hPreview(w http.ResponseWriter, r *http.Request, target string) {
	data, fields, ok := readUpload(w, r)
	if !ok {
		return
	}
	out, err := s.Preview(r.Context(), target, fields["baseDate"], data)
	if err != nil {
		httpErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Service) hApply(w http.ResponseWriter, r *http.Request, target string) {
	data, fields, ok := readUpload(w, r)
	if !ok {
		return
	}
	out, err := s.Apply(r.Context(), target, r.PathValue("batch"), fields["token"], data)
	if err != nil {
		httpErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Service) hCancel(w http.ResponseWriter, r *http.Request, target string) {
	if err := s.Cancel(r.Context(), target, r.PathValue("batch")); err != nil {
		httpErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) hHistory(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	out, err := s.History(r.Context(), domain.ImportFilter{Target: q.Get("target"), Status: q.Get("status"), Limit: limit})
	if err != nil {
		httpErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"batches": out})
}

func (s *Service) hBatch(w http.ResponseWriter, r *http.Request) {
	out, err := s.Batch(r.Context(), r.PathValue("batch"))
	if err != nil {
		httpErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Service) hErrors(w http.ResponseWriter, r *http.Request) {
	f, err := s.ErrorFile(r.Context(), r.PathValue("batch"))
	if err != nil {
		httpErr(w, err)
		return
	}
	sendFile(w, f)
}
