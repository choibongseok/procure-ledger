package auth

import (
	"net/http"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/rbac"
)

// registerTOTP 는 2단계 인증 라우트를 선언과 함께 등록한다. 등록·확인·해제는 제한 세션(필수 역할의 첫 등록)에서도
// 열려야 하므로 PermSetup 이고 로그인 세션에서만(SessionOnly) 호출할 수 있다.
func (s *Service) registerTOTP(r contract.Registrar) {
	r.HandleFunc("POST", "/api/auth/login/totp", contract.PermPublic, s.handleLoginTOTP)
	r.HandleFunc("POST", "/api/auth/totp/enroll", contract.PermSetup, rbac.SessionOnly(s.audit, s.txs, s.handleTOTPEnroll))
	r.HandleFunc("POST", "/api/auth/totp/confirm", contract.PermSetup, rbac.SessionOnly(s.audit, s.txs, s.handleTOTPConfirm))
	r.HandleFunc("POST", "/api/auth/totp/disable", contract.PermSetup, rbac.SessionOnly(s.audit, s.txs, s.handleTOTPDisable))
	r.HandleFunc("POST", "/api/admin/users/{id}/auth/totp-reset", contract.PermAuthManage, rbac.SessionOnly(s.audit, s.txs, s.handleTOTPReset))
}

func (s *Service) handleLoginTOTP(w http.ResponseWriter, r *http.Request) {
	if !s.loginPostOK(w, r) || !s.rateLimit(w, r, "login") {
		return
	}
	var in struct {
		Ticket string `json:"ticket"`
		Code   string `json:"code"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	res, err := s.LoginTOTP(r.Context(), in.Ticket, in.Code)
	if err != nil {
		authError(w, err)
		return
	}
	s.setSessionCookie(w, res.RawSession, res.Expires)
	writeJSON(w, http.StatusOK, map[string]any{"setup": res.Setup})
}

func (s *Service) handleTOTPEnroll(w http.ResponseWriter, r *http.Request) {
	p, _ := contract.PrincipalFrom(r.Context())
	var in struct {
		Password string `json:"password"`
		Code     string `json:"code"`
	}
	if r.ContentLength != 0 && !decodeBody(w, r, &in) {
		return
	}
	secret, uri, err := s.EnrollTOTP(r.Context(), p, in.Password, in.Code)
	if err != nil {
		authError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"secret": secret, "uri": uri})
}

func (s *Service) handleTOTPConfirm(w http.ResponseWriter, r *http.Request) {
	p, _ := contract.PrincipalFrom(r.Context())
	c, err := r.Cookie(sessionCookie)
	if err != nil || c.Value == "" {
		authError(w, contract.ErrUnauthenticated)
		return
	}
	var in struct {
		Code string `json:"code"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	codes, err := s.ConfirmTOTP(r.Context(), p, c.Value, in.Code)
	if err != nil {
		authError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"recoveryCodes": codes})
}

func (s *Service) handleTOTPDisable(w http.ResponseWriter, r *http.Request) {
	p, _ := contract.PrincipalFrom(r.Context())
	var in struct {
		Password string `json:"password"`
		Code     string `json:"code"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if err := s.DisableTOTP(r.Context(), p, in.Password, in.Code); err != nil {
		authError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) handleTOTPReset(w http.ResponseWriter, r *http.Request) {
	p, _ := contract.PrincipalFrom(r.Context())
	id, ok := pathID(r)
	if !ok {
		authError(w, contract.ErrNotFound)
		return
	}
	if err := s.ResetTOTP(r.Context(), p, id); err != nil {
		authError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
