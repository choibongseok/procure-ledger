package auth

import (
	"net/http"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/rbac"
)

// registerProviders 는 로그인 방식 설정 라우트를 선언과 함께 등록한다(auth:manage, 세션 전용: 토큰으로 방식 설정을 바꿀 수 없다).
func (s *Service) registerProviders(r contract.Registrar) {
	so := func(h http.HandlerFunc) http.HandlerFunc { return rbac.SessionOnly(s.audit, s.txs, h) }
	r.HandleFunc("GET", "/api/admin/auth/providers", contract.PermAuthManage, so(s.handleProviderList))
	r.HandleFunc("PUT", "/api/admin/auth/providers/{id}", contract.PermAuthManage, so(s.handleProviderUpdate))
	r.HandleFunc("POST", "/api/admin/auth/providers/{id}/test", contract.PermAuthManage, so(s.handleProviderTest))
	r.HandleFunc("PUT", "/api/admin/auth/policy", contract.PermAuthManage, so(s.handlePolicyUpdate))
	r.HandleFunc("POST", "/api/admin/users/{id}/auth/unlink", contract.PermAuthManage, so(s.handleUnlink))
	r.HandleFunc("POST", "/api/auth/login/emergency", contract.PermPublic, s.handleLoginEmergency)
	r.HandleFunc("GET", "/api/admin/auth/providers/{id}/test-result", contract.PermAuthManage, so(s.handleProviderTestResult))
}

func (s *Service) handleProviderList(w http.ResponseWriter, r *http.Request) {
	p, _ := contract.PrincipalFrom(r.Context())
	list, err := s.ListProviders(r.Context(), p)
	if err != nil {
		authError(w, err)
		return
	}
	pol, err := s.Policy(r.Context())
	if err != nil {
		authError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"providers": list, "policy": pol, "mailEnabled": s.mailer != nil, "totpAvailable": s.box != nil, "recentEmergencyLogin": s.RecentEmergencyLogin(r.Context()),
		"serverTime": s.now().UTC().Format("2006-01-02T15:04:05Z07:00")})
}

func (s *Service) handleProviderUpdate(w http.ResponseWriter, r *http.Request) {
	p, _ := contract.PrincipalFrom(r.Context())
	var in ProviderInput
	if !decodeBodyLimit(w, r, &in, 320<<10) {
		return
	}
	v, err := s.UpdateProvider(r.Context(), p, r.PathValue("id"), in)
	if err != nil {
		authError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Service) handleProviderTest(w http.ResponseWriter, r *http.Request) {
	p, _ := contract.PrincipalFrom(r.Context())
	u, err := s.StartProviderTest(r.Context(), p, r.PathValue("id"))
	if err != nil {
		authError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"url": u})
}

func (s *Service) handleProviderTestResult(w http.ResponseWriter, r *http.Request) {
	p, _ := contract.PrincipalFrom(r.Context())
	res, err := s.LastTestResult(r.Context(), p, r.PathValue("id"))
	if err != nil {
		authError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"result": res})
}

func (s *Service) handlePolicyUpdate(w http.ResponseWriter, r *http.Request) {
	p, _ := contract.PrincipalFrom(r.Context())
	var in PolicyInput
	if !decodeBody(w, r, &in) {
		return
	}
	pol, err := s.UpdatePolicy(r.Context(), p, in)
	if err != nil {
		authError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, pol)
}

func (s *Service) handleUnlink(w http.ResponseWriter, r *http.Request) {
	p, _ := contract.PrincipalFrom(r.Context())
	id, ok := pathID(r)
	if !ok {
		authError(w, contract.ErrNotFound)
		return
	}
	var in struct {
		Provider string `json:"provider"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if err := s.UnlinkIdentity(r.Context(), p, id, in.Provider); err != nil {
		authError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleLoginEmergency 는 비상 로그인(/login/emergency 화면용)이다. 이메일 로그인이 꺼져 있어도 비상 관리자만 비밀번호+2단계 코드로 들어온다.
func (s *Service) handleLoginEmergency(w http.ResponseWriter, r *http.Request) {
	if !s.loginPostOK(w, r) || !s.rateLimit(w, r, "login") {
		return
	}
	var in struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	res, err := s.LoginPassword(r.Context(), PasswordLogin{Email: in.Email, Password: in.Password, PrevSession: rawSessionCookie(r), Emergency: true})
	if err != nil {
		authError(w, err)
		return
	}
	if res.Ticket == "" { // 비상 관리자는 항상 2단계 인증을 거친다
		authError(w, ErrBadCredentials)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"totp": true, "ticket": res.Ticket})
}
