package access

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/rbac"
)

// Register 는 역할·부여 라우트를 선언과 함께 등록한다. 모두 role:manage 이고 로그인 세션으로만 호출할 수 있다(개인 토큰으로 권한 체계를 바꿀 수 없다).
// 본인의 권한 출처(/api/me/access)만 로그인한 누구나 본다.
func (s *Service) Register(r contract.Registrar) {
	so := func(h http.HandlerFunc) http.HandlerFunc { return rbac.SessionOnly(s.audit, s.txs, h) }
	m := contract.PermRoleManage
	r.HandleFunc("GET", "/api/admin/permissions", m, so(s.hPermissions))
	r.HandleFunc("GET", "/api/admin/role-targets", m, so(s.hTargets))
	r.HandleFunc("GET", "/api/admin/roles", m, so(s.hListRoles))
	r.HandleFunc("POST", "/api/admin/roles", m, so(s.hCreateRole))
	r.HandleFunc("PUT", "/api/admin/roles/{id}", m, so(s.hUpdateRole))
	r.HandleFunc("DELETE", "/api/admin/roles/{id}", m, so(s.hDeleteRole))
	r.HandleFunc("POST", "/api/admin/roles/{id}/clone", m, so(s.hCloneRole))
	r.HandleFunc("GET", "/api/admin/roles/{id}/grants", m, so(s.hListGrants))
	r.HandleFunc("POST", "/api/admin/roles/{id}/grants/preview", m, so(s.hPreviewGrant))
	r.HandleFunc("POST", "/api/admin/roles/{id}/grants", m, so(s.hAddGrant))
	r.HandleFunc("DELETE", "/api/admin/role-grants/{id}", m, so(s.hRevokeGrant))
	r.HandleFunc("GET", "/api/admin/role-requests", m, so(s.hListRequests))
	r.HandleFunc("POST", "/api/admin/role-requests/{id}/approve", m, so(s.hApprove))
	r.HandleFunc("POST", "/api/admin/role-requests/{id}/reject", m, so(s.hReject))
	r.HandleFunc("GET", "/api/admin/users/{id}/access", m, so(s.hUserAccess))
	r.HandleFunc("GET", "/api/admin/roles/separation", m, so(s.hSeparation))
	r.HandleFunc("GET", "/api/me/access", contract.PermAuthenticated, s.hMyAccess)
	// 역할 키 → 이름(키·이름만, 권한 내용 없음). LLM 정책·사용량 화면이 사용자 정의 역할 이름을 보이는 데 쓴다.
	// 라우트에는 권한 하나만 선언하므로 로그인만 선언하고, 서비스가 role:manage·llm:policy_manage·llm:usage_read·usage:read 중 하나를 요구한다.
	r.HandleFunc("GET", "/api/role-names", contract.PermAuthenticated, s.hRoleNames)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// httpErr 는 계약 오류를 상태 코드와 한국어 문구로 옮긴다. 알 수 없는 오류는 내용을 숨긴다.
func httpErr(w http.ResponseWriter, err error) {
	code := http.StatusInternalServerError
	switch {
	case errors.Is(err, contract.ErrUnauthenticated):
		code = http.StatusUnauthorized
	case errors.Is(err, contract.ErrForbidden), errors.Is(err, contract.ErrSetupRequired):
		code = http.StatusForbidden
	case errors.Is(err, contract.ErrNotFound):
		code = http.StatusNotFound
	case errors.Is(err, contract.ErrValidation):
		code = http.StatusBadRequest
	case errors.Is(err, contract.ErrInvalidState):
		code = http.StatusConflict
	}
	if code == http.StatusInternalServerError {
		log.Printf("access: %v", err)
		if errors.Is(err, contract.ErrAuditFailed) {
			http.Error(w, contract.ErrAuditFailed.Error(), code)
			return
		}
		http.Error(w, "서버 오류가 발생했습니다", code)
		return
	}
	http.Error(w, err.Error(), code)
}

func principal(r *http.Request) contract.Principal {
	p, _ := contract.PrincipalFrom(r.Context())
	return p
}

func pathID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		httpErr(w, contract.ErrNotFound)
		return 0, false
	}
	return id, true
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		httpErr(w, verr("요청 형식이 올바르지 않습니다"))
		return false
	}
	return true
}

func (s *Service) hPermissions(w http.ResponseWriter, r *http.Request) {
	if err := s.authorize(r.Context(), principal(r)); err != nil {
		httpErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"permissions": Permissions()})
}

func (s *Service) hListRoles(w http.ResponseWriter, r *http.Request) {
	list, err := s.ListRoles(r.Context(), principal(r))
	if err != nil {
		httpErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"roles": list})
}

type roleBody struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Permissions []string `json:"permissions"`
	Version     int64    `json:"version"`
	Reason      string   `json:"reason"`
}

func (s *Service) hCreateRole(w http.ResponseWriter, r *http.Request) {
	var in roleBody
	if !decode(w, r, &in) {
		return
	}
	v, err := s.CreateRole(r.Context(), principal(r), in.Name, in.Description, in.Permissions)
	if err != nil {
		httpErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, v)
}

func (s *Service) hUpdateRole(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in roleBody
	if !decode(w, r, &in) {
		return
	}
	res, err := s.UpdateRole(r.Context(), principal(r), id, in.Version, in.Name, in.Description, in.Permissions, in.Reason)
	if err != nil {
		httpErr(w, err)
		return
	}
	status := http.StatusOK
	if res.Request != nil {
		status = http.StatusAccepted // 2인 확인 대기
	}
	writeJSON(w, status, res)
}

func (s *Service) hDeleteRole(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := s.DeleteRole(r.Context(), principal(r), id); err != nil {
		httpErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) hCloneRole(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := s.authorize(r.Context(), principal(r)); err != nil {
		httpErr(w, err)
		return
	}
	var in roleBody
	if !decode(w, r, &in) {
		return
	}
	v, err := s.CloneRole(r.Context(), principal(r), id, in.Name)
	if err != nil {
		httpErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, v)
}

func (s *Service) hListGrants(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	list, err := s.ListGrants(r.Context(), principal(r), id)
	if err != nil {
		httpErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"grants": list})
}

func (s *Service) hPreviewGrant(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in GrantInput
	if !decode(w, r, &in) {
		return
	}
	v, err := s.PreviewGrant(r.Context(), principal(r), id, in)
	if err != nil {
		httpErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Service) hAddGrant(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in GrantInput
	if !decode(w, r, &in) {
		return
	}
	res, err := s.AddGrant(r.Context(), principal(r), id, in)
	if err != nil {
		httpErr(w, err)
		return
	}
	status := http.StatusCreated
	if res.Request != nil {
		status = http.StatusAccepted // 2인 확인 대기
	}
	writeJSON(w, status, res)
}

func (s *Service) hRevokeGrant(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := s.RevokeGrant(r.Context(), principal(r), id); err != nil {
		httpErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) hListRequests(w http.ResponseWriter, r *http.Request) {
	list, err := s.ListRequests(r.Context(), principal(r))
	if err != nil {
		httpErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"requests": list})
}

func (s *Service) hApprove(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := s.Approve(r.Context(), principal(r), id); err != nil {
		httpErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) hReject(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in struct {
		Reason string `json:"reason"`
	}
	if !decode(w, r, &in) {
		return
	}
	if err := s.Reject(r.Context(), principal(r), id, in.Reason); err != nil {
		httpErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) hUserAccess(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	v, err := s.UserAccess(r.Context(), principal(r), id)
	if err != nil {
		httpErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Service) hMyAccess(w http.ResponseWriter, r *http.Request) {
	v, err := s.MyAccess(r.Context(), principal(r))
	if err != nil {
		httpErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Service) hSeparation(w http.ResponseWriter, r *http.Request) {
	list, err := s.Separation(r.Context(), principal(r))
	if err != nil {
		httpErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"findings": list})
}

func (s *Service) hTargets(w http.ResponseWriter, r *http.Request) {
	v, err := s.Targets(r.Context(), principal(r))
	if err != nil {
		httpErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Service) hRoleNames(w http.ResponseWriter, r *http.Request) {
	p, ok := contract.PrincipalFrom(r.Context())
	if !ok {
		httpErr(w, contract.ErrUnauthenticated)
		return
	}
	if !p.Can(contract.PermRoleManage) && !p.Can(contract.PermLLMPolicyManage) && !p.Can(contract.PermLLMUsageRead) && !p.Can(contract.PermUsageRead) {
		// 서비스 안에서 거부된 조회도 거부 감사로 남긴다(라우트 선언은 로그인만이라 라우트 수준 거부가 없다).
		_ = s.txs.WithTx(r.Context(), func(tx contract.Tx) error {
			return s.audit.Record(r.Context(), tx, audit.Entry{Actor: p, Action: "role.names_read", TargetType: audit.TargetRole, TargetID: "names", Result: audit.ResultDenied, Detail: "역할 이름 조회 권한 없음"})
		})
		httpErr(w, contract.ErrForbidden)
		return
	}
	rows, err := s.db.QueryContext(r.Context(), `SELECT key, name FROM roles WHERE deleted_at IS NULL ORDER BY kind DESC, id`)
	if err != nil {
		httpErr(w, err)
		return
	}
	defer rows.Close()
	type item struct {
		Key  string `json:"key"`
		Name string `json:"name"`
	}
	out := []item{}
	for rows.Next() {
		var it item
		if err := rows.Scan(&it.Key, &it.Name); err != nil {
			httpErr(w, err)
			return
		}
		out = append(out, it)
	}
	writeJSON(w, http.StatusOK, map[string]any{"roles": out})
}
