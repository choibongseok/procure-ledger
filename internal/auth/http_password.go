package auth

import (
	"encoding/json"
	"errors"
	"github.com/choibongseok/procure-ledger/internal/privacy"
	"log"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/rbac"
)

// registerPassword 는 이메일+비밀번호 라우트를 선언과 함께 등록한다. 공개 라우트는 PermPublic 을 명시하고,
// 로그인 자격을 다루는 관리자 라우트는 auth:manage + SessionOnly(개인 토큰으로는 호출할 수 없다)다.
func (s *Service) registerPassword(r contract.Registrar) {
	r.HandleFunc("POST", "/api/auth/login/password", contract.PermPublic, s.handleLoginPassword)
	r.HandleFunc("POST", "/api/auth/password/forgot", contract.PermPublic, s.handleForgot)
	r.HandleFunc("POST", "/api/auth/password/reset", contract.PermPublic, s.handleReset)
	// 제한 세션에서도 열려야 하므로 PermSetup. 비밀번호는 로그인 세션에서만 바꾼다.
	r.HandleFunc("POST", "/api/auth/password/change", contract.PermSetup, rbac.SessionOnly(s.audit, s.txs, s.handleChangePassword))

	r.HandleFunc("GET", "/api/admin/auth/users", contract.PermAuthManage, rbac.SessionOnly(s.audit, s.txs, s.handleCredentialList))
	r.HandleFunc("POST", "/api/admin/users/{id}/auth/start", contract.PermAuthManage, rbac.SessionOnly(s.audit, s.txs, s.handleStartLogin))
	r.HandleFunc("POST", "/api/admin/users/{id}/auth/unlock", contract.PermAuthManage, rbac.SessionOnly(s.audit, s.txs, s.handleUnlock))
}

type apiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Until   string `json:"until,omitempty"`
}

func writeErr(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, apiError{Code: code, Message: msg})
}

// authError 는 인증 서비스 오류를 상태 코드와 한국어 문구로 옮긴다. 알 수 없는 오류는 내용을 숨긴다.
func authError(w http.ResponseWriter, err error) {
	var locked LockedError
	var pe PolicyError
	switch {
	case errors.As(err, &locked):
		writeJSON(w, http.StatusLocked, apiError{Code: "locked", Message: locked.Error(), Until: locked.Until.UTC().Format(time.RFC3339)})
	case errors.Is(err, ErrBadCredentials):
		writeErr(w, http.StatusUnauthorized, "bad_credentials", ErrBadCredentials.Error())
	case errors.Is(err, ErrEmailLoginOff):
		writeErr(w, http.StatusForbidden, "email_login_off", ErrEmailLoginOff.Error())
	case errors.Is(err, ErrBadTicket):
		writeErr(w, http.StatusUnauthorized, "bad_ticket", ErrBadTicket.Error())
	case errors.Is(err, ErrBadCode):
		writeErr(w, http.StatusUnauthorized, "bad_code", ErrBadCode.Error())
	case errors.Is(err, ErrBusy):
		writeErr(w, http.StatusServiceUnavailable, "busy", ErrBusy.Error())
	case errors.Is(err, ErrBadLink):
		writeErr(w, http.StatusBadRequest, "bad_link", "링크가 만료되었거나 이미 사용되었습니다. 다시 요청해 주세요")
	case errors.As(err, &pe):
		writeErr(w, http.StatusBadRequest, "policy", pe.Msg)
	case errors.Is(err, contract.ErrValidation):
		writeErr(w, http.StatusBadRequest, "validation", err.Error())
	case errors.Is(err, contract.ErrInvalidState):
		writeErr(w, http.StatusConflict, "invalid_state", err.Error())
	case errors.Is(err, contract.ErrNotFound):
		writeErr(w, http.StatusNotFound, "not_found", err.Error())
	case errors.Is(err, contract.ErrForbidden), errors.Is(err, contract.ErrSetupRequired):
		writeErr(w, http.StatusForbidden, "forbidden", err.Error())
	case errors.Is(err, contract.ErrUnauthenticated):
		writeErr(w, http.StatusUnauthorized, "unauthenticated", err.Error())
	case errors.Is(err, contract.ErrAuditFailed):
		log.Printf("auth: 감사 기록 실패: %v", err)
		writeErr(w, http.StatusInternalServerError, "audit_failed", contract.ErrAuditFailed.Error())
	default:
		log.Printf("auth: %v", err)
		writeErr(w, http.StatusInternalServerError, "server", "서버 오류가 발생했습니다")
	}
}

// loginPostOK 는 세션이 없는 로그인·재설정 POST 의 CSRF 방어다(명세 3절): JSON 본문만, 같은 사이트 요청만.
func (s *Service) loginPostOK(w http.ResponseWriter, r *http.Request) bool {
	if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mt != "application/json" {
		writeErr(w, http.StatusUnsupportedMediaType, "content_type", "JSON 요청만 받습니다")
		return false
	}
	switch r.Header.Get("Sec-Fetch-Site") {
	case "", "same-origin", "same-site", "none":
	default:
		writeErr(w, http.StatusForbidden, "cross_site", "다른 사이트에서 보낸 요청은 받지 않습니다")
		return false
	}
	if r.Header.Get("Origin") == "" { // 브라우저는 POST 에 항상 Origin 을 보낸다. 없으면 Referer 로 확인하고 둘 다 없으면 거부한다
		ref := r.Header.Get("Referer")
		ru, err := url.Parse(ref)
		if ref == "" || err != nil || !(ru.Host == r.Host || strings.EqualFold(ru.Host, s.baseHost)) {
			writeErr(w, http.StatusForbidden, "cross_site", "출처를 확인할 수 없는 요청은 받지 않습니다")
			return false
		}
	}
	if !s.sameOrigin(r) {
		writeErr(w, http.StatusForbidden, "cross_site", "다른 사이트에서 보낸 요청은 받지 않습니다")
		return false
	}
	return true
}

func decodeBody(w http.ResponseWriter, r *http.Request, v any) bool {
	return decodeBodyLimit(w, r, v, 8<<10)
}

func decodeBodyLimit(w http.ResponseWriter, r *http.Request, v any, limit int64) bool {
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(v); err != nil {
		writeErr(w, http.StatusBadRequest, "validation", "요청 형식이 올바르지 않습니다")
		return false
	}
	return true
}

func clientIP(r *http.Request) string {
	h, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return h
}

// rateLimit 은 IP 별 분당 20회 제한이다. 초과하면 429 로 끝내고, 감사는 범위별 10분당 1행만 남긴다.
func (s *Service) rateLimit(w http.ResponseWriter, r *http.Request, scope string) bool {
	ok, doAudit := s.limiter.allow(scope, clientIP(r), s.now().UTC())
	if ok {
		return true
	}
	if doAudit {
		ctx := r.Context()
		if err := s.txs.WithTx(ctx, func(tx contract.Tx) error {
			return s.record(ctx, tx, contract.Principal{Channel: contract.ChannelWeb}, "auth.rate_limited", audit.TargetCredential, "ip",
				audit.ResultDenied, []audit.DiffItem{{Key: "scope", New: scope}}, "")
		}); err != nil {
			log.Printf("auth: 속도 제한 감사 기록 실패: %v", err)
		}
	}
	w.Header().Set("Retry-After", "60")
	writeErr(w, http.StatusTooManyRequests, "rate_limited", "요청이 너무 많습니다. 잠시 뒤 다시 시도해 주세요")
	return false
}

func (s *Service) setSessionCookie(w http.ResponseWriter, raw string, expires time.Time) {
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: raw, Path: "/", Expires: expires, HttpOnly: true,
		Secure: s.cookieSecure, SameSite: http.SameSiteLaxMode})
}

func (s *Service) handleLoginPassword(w http.ResponseWriter, r *http.Request) {
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
	prev := ""
	if c, err := r.Cookie(sessionCookie); err == nil {
		prev = c.Value
	}
	res, err := s.LoginPassword(r.Context(), PasswordLogin{Email: in.Email, Password: in.Password, PrevSession: prev})
	if err != nil {
		authError(w, err)
		return
	}
	if res.Ticket != "" { // 2단계 인증 등록자: 세션 대신 티켓(5분·5회)
		writeJSON(w, http.StatusOK, map[string]any{"totp": true, "ticket": res.Ticket})
		return
	}
	s.setSessionCookie(w, res.RawSession, res.Expires)
	writeJSON(w, http.StatusOK, map[string]any{"setup": res.Setup})
}

func (s *Service) handleForgot(w http.ResponseWriter, r *http.Request) {
	if !s.loginPostOK(w, r) || !s.rateLimit(w, r, "forgot") {
		return
	}
	var in struct {
		Email string `json:"email"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if err := s.RequestReset(r.Context(), in.Email); err != nil {
		authError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"message": "등록된 이메일이면 재설정 링크를 보냈습니다"})
}

func (s *Service) handleReset(w http.ResponseWriter, r *http.Request) {
	if !s.loginPostOK(w, r) || !s.rateLimit(w, r, "forgot") {
		return
	}
	var in struct {
		Token    string `json:"token"`
		Password string `json:"password"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if err := s.ResetPassword(r.Context(), in.Token, in.Password); err != nil {
		authError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"message": "비밀번호를 바꿨습니다. 새 비밀번호로 로그인하세요"})
}

func (s *Service) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	p, _ := contract.PrincipalFrom(r.Context())
	c, err := r.Cookie(sessionCookie)
	if err != nil || c.Value == "" {
		authError(w, contract.ErrUnauthenticated)
		return
	}
	var in struct {
		Current string `json:"current"`
		New     string `json:"new"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if err := s.ChangePassword(r.Context(), p, c.Value, in.Current, in.New); err != nil {
		authError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) handleCredentialList(w http.ResponseWriter, r *http.Request) {
	p, _ := contract.PrincipalFrom(r.Context())
	list, err := s.ListCredentialStatus(r.Context(), p)
	if err != nil {
		authError(w, err)
		return
	}
	// 다른 사람의 이메일 원문이 실리므로 응답 전에 열람 기록을 남긴다(B41, 기록하지 못하면 목록을 내주지 않는다).
	if s.priv != nil {
		subs := make([]privacy.Subject, 0, len(list))
		for _, u := range list {
			if u.Email != "" {
				subs = append(subs, privacy.Subject{Type: privacy.SubjectUser, ID: u.UserID})
			}
		}
		if err := s.priv.Note(r.Context(), p, privacy.Access{Category: privacy.CatContact, Screen: privacy.ScreenAuthUsers, Fields: []string{privacy.FieldEmail}, Subjects: privacy.ExcludeSelf(p, subs)}); err != nil {
			log.Printf("auth: 열람 기록 실패(로그인 설정 사용자 목록): %v", err)
			authError(w, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"users": list, "mailEnabled": s.mailer != nil})
}

func pathID(r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	return id, err == nil && id > 0
}

func (s *Service) handleStartLogin(w http.ResponseWriter, r *http.Request) {
	p, _ := contract.PrincipalFrom(r.Context())
	id, ok := pathID(r)
	if !ok {
		authError(w, contract.ErrNotFound)
		return
	}
	var in struct {
		Via string `json:"via"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	res, err := s.StartLogin(r.Context(), p, id, in.Via)
	if err != nil {
		authError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Service) handleUnlock(w http.ResponseWriter, r *http.Request) {
	p, _ := contract.PrincipalFrom(r.Context())
	id, ok := pathID(r)
	if !ok {
		authError(w, contract.ErrNotFound)
		return
	}
	if err := s.UnlockAccount(r.Context(), p, id); err != nil {
		authError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
