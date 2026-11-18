package mcptool

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

// Register 는 사용자 정의 도구 관리 API 를 선언과 함께 등록한다(명세 9·10절). 쓰기는 위험 권한이라 세션 전용이다(개인 토큰은 403).
//
//	GET  /api/admin/mcp-tools                 mcptool:read    도구 목록
//	GET  /api/admin/mcp-tools/catalog         mcptool:read    고를 수 있는 자원·칸
//	GET  /api/admin/mcp-tools/{id}            mcptool:read    도구 상세(현재 정의·버전)
//	POST /api/admin/mcp-tools                 mcptool:manage  새 도구 {title, definition, note}(세션 전용)
//	PUT  /api/admin/mcp-tools/{id}            mcptool:manage  새 버전 {title, definition, note}(세션 전용)
//	POST /api/admin/mcp-tools/{id}/rollback   mcptool:manage  옛 버전을 복사한 새 버전 {version, note}(세션 전용)
//	POST /api/admin/mcp-tools/test            mcptool:manage  시험 실행 {definition, input}(세션 전용, 실행자 권한 기준)
//	POST /api/admin/mcp-tools/{id}/enabled    mcptool:manage  켜기·끄기 {enabled}(세션 전용, 켜기는 현재 버전의 시험 실행 성공이 필요)
//	PUT  /api/admin/mcp-tools/{id}/limit      mcptool:manage  도구별 사용자 분당 호출 한도 {perMin}(세션 전용)
//	POST /api/admin/mcp-tools/{id}/enable-request  mcptool:manage  초안 도구 켜기 요청(세션 전용)
//	POST /api/admin/mcp-tools/{id}/enable-confirm  mcptool:manage  다른 관리자의 켜기 요청 확인 {evalOverride, reason}(세션 전용)
//	POST /api/admin/mcp-tools/{id}/enable-cancel   mcptool:manage  켜기 요청 취소·반려(세션 전용)
//	PUT  /api/admin/mcp-tools/{id}/eval-link       mcptool:manage  평가 세트 연결·해제 {setCode}(세션 전용)
//	GET  /api/admin/mcp-tools/export          mcptool:manage  모든 도구 정의 JSON 내보내기
//	POST /api/admin/mcp-tools/import          mcptool:manage  JSON 가져오기(본문 = 파일, 통과한 것만 꺼진 도구로, 세션 전용)
func (s *Service) Register(reg contract.Registrar) {
	r, m := contract.PermMCPToolRead, contract.PermMCPToolManage
	so := func(f http.HandlerFunc) http.HandlerFunc { return rbac.SessionOnly(s.o.Audit, s.o.Txs, f) }
	reg.HandleFunc("GET", "/api/admin/mcp-tools", r, s.hList)
	reg.HandleFunc("GET", "/api/admin/mcp-tools/catalog", r, s.hCatalog)
	reg.HandleFunc("GET", "/api/admin/mcp-tools/{id}", r, s.hGet)
	reg.HandleFunc("POST", "/api/admin/mcp-tools", m, so(s.hSave))
	reg.HandleFunc("PUT", "/api/admin/mcp-tools/{id}", m, so(s.hSave))
	reg.HandleFunc("POST", "/api/admin/mcp-tools/{id}/rollback", m, so(s.hRollback))
	reg.HandleFunc("POST", "/api/admin/mcp-tools/test", m, so(s.hTest))
	reg.HandleFunc("POST", "/api/admin/mcp-tools/{id}/enabled", m, so(s.hEnabled))
	reg.HandleFunc("PUT", "/api/admin/mcp-tools/{id}/limit", m, so(s.hLimit))
	reg.HandleFunc("POST", "/api/admin/mcp-tools/{id}/enable-request", m, so(s.hEnableRequest))
	reg.HandleFunc("POST", "/api/admin/mcp-tools/{id}/enable-confirm", m, so(s.hEnableConfirm))
	reg.HandleFunc("POST", "/api/admin/mcp-tools/{id}/enable-cancel", m, so(s.hEnableCancel))
	reg.HandleFunc("PUT", "/api/admin/mcp-tools/{id}/eval-link", m, so(s.hEvalLink))
	reg.HandleFunc("GET", "/api/admin/mcp-tools/export", m, s.hExport)
	reg.HandleFunc("POST", "/api/admin/mcp-tools/import", m, so(s.hImport))
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, err error) {
	trim := func(p error) string { return strings.TrimPrefix(err.Error(), p.Error()+": ") }
	var ve *ValidationError
	var eg *EvalGateError
	switch {
	case errors.As(err, &eg):
		writeJSON(w, http.StatusConflict, map[string]any{"error": eg.Result.Message, "evalGate": eg.Result})
	case errors.As(err, &ve):
		fe := map[string]string{}
		for _, e := range ve.Errors {
			if _, dup := fe[e.Path]; !dup {
				fe[e.Path] = e.Message
			}
		}
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": ve.Error(), "fieldErrors": fe})
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
		log.Printf("mcptool: %v", err)
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

func pathID(r *http.Request) int64 {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	return id
}

func (s *Service) hList(w http.ResponseWriter, r *http.Request) {
	out, err := s.List(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tools": out})
}

func (s *Service) hCatalog(w http.ResponseWriter, r *http.Request) {
	out, err := s.Catalog(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"resources": out, "draftTargets": s.DraftTargets()})
}

func (s *Service) hGet(w http.ResponseWriter, r *http.Request) {
	out, err := s.Get(r.Context(), pathID(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Service) hSave(w http.ResponseWriter, r *http.Request) {
	var in SaveInput
	if !decode(w, r, &in) {
		return
	}
	in.ID = pathID(r)
	out, err := s.Save(r.Context(), in)
	if err != nil {
		writeErr(w, err)
		return
	}
	status := http.StatusOK
	if r.Method == http.MethodPost {
		status = http.StatusCreated
	}
	writeJSON(w, status, out)
}

func (s *Service) hRollback(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Version int    `json:"version"`
		Note    string `json:"note"`
	}
	if !decode(w, r, &in) {
		return
	}
	out, err := s.Rollback(r.Context(), pathID(r), in.Version, in.Note)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Service) hTest(w http.ResponseWriter, r *http.Request) {
	var in TestInput
	if !decode(w, r, &in) {
		return
	}
	out, err := s.Test(r.Context(), in)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Service) hEnabled(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Enabled      bool   `json:"enabled"`
		EvalOverride bool   `json:"evalOverride"`
		Reason       string `json:"reason"`
	}
	if !decode(w, r, &in) {
		return
	}
	out, err := s.SetEnabled(r.Context(), pathID(r), in.Enabled, EnableOpts{EvalOverride: in.EvalOverride, Reason: in.Reason})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Service) hLimit(w http.ResponseWriter, r *http.Request) {
	var in struct {
		PerMin int `json:"perMin"`
	}
	if !decode(w, r, &in) {
		return
	}
	out, err := s.SetRate(r.Context(), pathID(r), in.PerMin)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Service) hEnableRequest(w http.ResponseWriter, r *http.Request) {
	out, err := s.RequestEnable(r.Context(), pathID(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Service) hEnableConfirm(w http.ResponseWriter, r *http.Request) {
	var in EnableOpts
	if r.ContentLength != 0 && !decode(w, r, &in) {
		return
	}
	out, err := s.ConfirmEnable(r.Context(), pathID(r), in)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Service) hEnableCancel(w http.ResponseWriter, r *http.Request) {
	out, err := s.CancelEnable(r.Context(), pathID(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Service) hEvalLink(w http.ResponseWriter, r *http.Request) {
	var in struct {
		SetCode string `json:"setCode"`
	}
	if !decode(w, r, &in) {
		return
	}
	out, err := s.SetEvalLink(r.Context(), pathID(r), in.SetCode)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Service) hExport(w http.ResponseWriter, r *http.Request) {
	out, err := s.Export(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename*=UTF-8''ai-tools.json")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(out)
}

func (s *Service) hImport(w http.ResponseWriter, r *http.Request) {
	data, err := io.ReadAll(io.LimitReader(r.Body, MaxImportBytes+1))
	if err != nil {
		writeErr(w, valErr("요청 형식이 올바르지 않습니다"))
		return
	}
	out, err := s.Import(r.Context(), data)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}
