package module

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

// HRRole 은 "인사 담당" 사용자 정의 역할 템플릿이다(명세 D8: 기본 역할에 추가하지 않고 템플릿으로 제공). 화면의 "인사 담당 역할 만들기" 단추가 이 값으로
// 기존 역할 API(POST /api/admin/roles, role:manage·세션 전용·감사)를 부른다. 권한은 부여가 아니라 역할 정의일 뿐이고 사람에게 주는 것은 역할 관리 화면에서 한다.
var HRRole = struct {
	Name, Description string
	Permissions       []contract.Permission
}{
	Name:        "인사 담당",
	Description: "근태·휴가 모듈의 인사 담당: 현재 법인의 전사 근태·잔여 열람, 조정·정정·근태 마감, 월 근태 내보내기, 인사 발령 이력 열람",
	Permissions: []contract.Permission{contract.PermAttendanceHR, contract.PermAttendanceExport, contract.PermOrgHistory},
}

// Register 는 모듈 관리 API 를 등록한다. 지금 모듈은 근태·휴가 하나라 모든 라우트가 attendance:manage_module 이고 변경은 로그인 세션만(개인 토큰은 403)이다.
// 모듈이 늘면 모듈별 권한을 라우트가 아니라 서비스가 판정한다(라우트는 가장 넓은 권한으로 등록).
func (s *Service) Register(reg contract.Registrar) {
	so := func(h http.HandlerFunc) http.HandlerFunc { return rbac.SessionOnly(s.o.Audit, s.o.Txs, h) }
	m := contract.PermAttendanceManageModule
	reg.HandleFunc("GET", "/api/admin/modules", m, s.hList)
	reg.HandleFunc("GET", "/api/admin/modules/{key}/checklist", m, s.hChecklist)
	reg.HandleFunc("PUT", "/api/admin/modules/{key}", m, so(s.hSet))
	reg.HandleFunc("GET", "/api/admin/modules/attendance/hr-template", m, s.hHRTemplate)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, err error) {
	trim := func(prefix error) string { return strings.TrimPrefix(err.Error(), prefix.Error()+": ") }
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
		log.Printf("module: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "처리 중 오류가 발생했습니다. 잠시 후 다시 시도해 주세요."})
	}
}

func (s *Service) hList(w http.ResponseWriter, r *http.Request) {
	vs, err := s.List(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	out := make([]map[string]any, len(vs))
	for i, v := range vs {
		out[i] = map[string]any{"key": v.Key, "name": v.Name, "description": v.Description, "enabled": v.Enabled, "needs": v.Needs}
	}
	writeJSON(w, http.StatusOK, map[string]any{"modules": out})
}

func (s *Service) hChecklist(w http.ResponseWriter, r *http.Request) {
	cs, err := s.Checklist(r.Context(), r.PathValue("key"))
	if err != nil {
		writeErr(w, err)
		return
	}
	out := make([]map[string]any, len(cs))
	ready := true
	for i, c := range cs {
		out[i] = map[string]any{"label": c.Label, "required": c.Required, "ok": c.OK, "detail": c.Detail}
		if c.Required && !c.OK {
			ready = false
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out, "ready": ready})
}

func (s *Service) hSet(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Enabled *bool `json:"enabled"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<12)).Decode(&in); err != nil || in.Enabled == nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "켜기 여부(enabled)를 보내세요"})
		return
	}
	if err := s.SetEnabled(r.Context(), r.PathValue("key"), *in.Enabled); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"enabled": s.Enabled(r.Context(), r.PathValue("key"))})
}

func (s *Service) hHRTemplate(w http.ResponseWriter, r *http.Request) {
	if _, err := contract.Authorize(r.Context(), contract.PermAttendanceManageModule); err != nil {
		writeErr(w, err)
		return
	}
	perms := make([]string, len(HRRole.Permissions))
	for i, p := range HRRole.Permissions {
		perms[i] = string(p)
	}
	writeJSON(w, http.StatusOK, map[string]any{"name": HRRole.Name, "description": HRRole.Description, "permissions": perms})
}
