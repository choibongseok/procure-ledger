package auth

import (
	"context"
	"errors"
	"log"
	"net/http"
	"net/url"
	"strings"

	"github.com/choibongseok/procure-ledger/internal/contract"
)

const csrfHeader = "X-CSRF-Token"

func isSafeMethod(m string) bool {
	return m == http.MethodGet || m == http.MethodHead || m == http.MethodOptions
}

func bearerToken(r *http.Request) (string, bool) {
	h := r.Header.Get("Authorization")
	if h == "" {
		return "", false
	}
	const p = "Bearer "
	if len(h) <= len(p) || !strings.EqualFold(h[:len(p)], p) {
		return "", true // 헤더는 있으나 Bearer 형식이 아님: 빈 토큰으로 거부시킨다
	}
	return strings.TrimSpace(h[len(p):]), true
}

// sameOrigin 은 Origin 헤더가 있으면 요청 Host 또는 서버가 알고 있는 접속 주소(PL_BASE_URL)의 호스트와 같은지 본다(없으면 CSRF 토큰 검사에 맡긴다).
// 프록시(tailscale serve 등)가 https 를 끝내고 localhost http 로 넘기며 Host 를 바꿔도 PL_BASE_URL 기준으로 판정이 유지된다.
func (s *Service) sameOrigin(r *http.Request) bool {
	o := r.Header.Get("Origin")
	if o == "" {
		return true
	}
	u, err := url.Parse(o)
	if err != nil {
		return false
	}
	return u.Host == r.Host || strings.EqualFold(u.Host, s.baseHost)
}

var publicLoginPosts = map[string]bool{
	"/api/auth/login/password": true, "/api/auth/password/forgot": true, "/api/auth/password/reset": true, "/api/auth/login/totp": true, "/api/auth/saml/acs": true, "/api/auth/login/emergency": true,
}

// Wrap 은 인증 미들웨어다. Principal 을 만들어 context 에 넣을 뿐 권한 판정은 하지 않는다
// (판정은 라우트 선언의 Guard 가 한다). 자격이 없거나 틀리면 Principal 없이 통과하고, 보호된 라우트는 401 이 된다.
// 단 Authorization 헤더가 있는데 유효하지 않으면 바로 401 이다(조용한 익명 강등 금지).
// /mcp 는 MCP 핸들러가 Bearer 를 직접 검증하므로 건드리지 않는다.
func (s *Service) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/mcp" || strings.HasPrefix(r.URL.Path, "/mcp/") {
			next.ServeHTTP(w, r)
			return
		}
		if raw, present := bearerToken(r); present {
			p, err := s.AuthenticateToken(r.Context(), raw)
			if err != nil {
				http.Error(w, contract.ErrUnauthenticated.Error(), http.StatusUnauthorized)
				return
			}
			p.Channel, p.Auth = contract.ChannelWeb, contract.AuthToken
			a, err := s.resolveEntity(r.Context(), p, r, 0)
			if err != nil {
				s.denyHeaderEntity(r, p)
				s.entityDenied(w, err)
				return
			}
			if groupWriteBlocked(r, a) {
				groupWriteDenied(w)
				return
			}
			s.serveEntity(next, w, r.WithContext(contract.WithActiveEntity(contract.WithPrincipal(r.Context(), p), a)), p, a)
			return
		}
		if r.Method == http.MethodPost && (publicLoginPosts[r.URL.Path] || strings.HasPrefix(r.URL.Path, "/api/setup/")) {
			next.ServeHTTP(w, r) // 세션 없이 오는 로그인·재설정 POST 는 Origin·JSON 검사(loginPostOK)로 막는다(기존 세션의 CSRF 토큰을 요구하지 않는다)
			return
		}
		if c, err := r.Cookie(sessionCookie); err == nil && c.Value != "" {
			p, csrfHash, err := s.sessionPrincipal(r.Context(), c.Value)
			if err == nil {
				if !isSafeMethod(r.Method) && (!s.sameOrigin(r) || !checkCSRF(r.Header.Get(csrfHeader), csrfHash)) {
					http.Error(w, "CSRF 검증에 실패했습니다", http.StatusForbidden)
					return
				}
				a, err := s.resolveEntity(r.Context(), p, r, s.sessionEntity(r.Context(), c.Value))
				if err != nil {
					s.denyHeaderEntity(r, p)
					s.entityDenied(w, err)
					return
				}
				if groupWriteBlocked(r, a) {
					groupWriteDenied(w)
					return
				}
				s.serveEntity(next, w, r.WithContext(contract.WithActiveEntity(contract.WithPrincipal(r.Context(), p), a)), p, a)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// serveEntity 는 법인이 정해진 요청을 처리하면서 법인 불일치 접근의 감사(entity_denied.go)를 붙인다.
func (s *Service) serveEntity(next http.Handler, w http.ResponseWriter, r *http.Request, p contract.Principal, a contract.ActiveEntity) {
	r, st := s.withEntityAudit(r, p, a)
	er, idPart, ok := matchEntityRoute(r.URL.Path)
	if !ok {
		next.ServeHTTP(w, r)
		return
	}
	sw := &statusWriter{ResponseWriter: w}
	next.ServeHTTP(sw, r)
	if sw.status == http.StatusNotFound {
		s.auditEntityMiss(r, p, a, st, er, idPart)
	}
}

// entityDenied 는 고를 수 없는 법인을 지정한 요청의 응답이다(오류가 아니면 서버 오류).
func (s *Service) entityDenied(w http.ResponseWriter, err error) {
	if errors.Is(err, errEntityDenied) {
		http.Error(w, errEntityDenied.Error(), http.StatusForbidden)
		return
	}
	http.Error(w, "처리 중 오류가 발생했습니다", http.StatusInternalServerError)
}

// Register 는 인증 라우트를 선언과 함께 등록한다. dev IdP 는 켜졌을 때만 등록된다.
func (s *Service) Register(r contract.Registrar) {
	r.HandleFunc("GET", "/api/auth/config", contract.PermPublic, s.handleConfig)
	r.HandleFunc("GET", "/api/auth/login", contract.PermPublic, s.handleLogin)
	r.HandleFunc("GET", "/api/auth/callback", contract.PermPublic, s.handleCallback)
	r.HandleFunc("POST", "/api/auth/logout", contract.PermSetup, s.handleLogout)
	r.HandleFunc("GET", "/api/me", contract.PermSetup, s.handleMe)
	r.HandleFunc("PUT", "/api/me/entity", contract.PermAuthenticated, s.handleSetEntity) // 현재 법인 바꾸기(B44, 세션 값)
	s.registerPassword(r)
	s.registerTOTP(r)
	s.registerProviders(r)
	s.registerSAML(r)
	if s.devIdP != nil {
		log.Printf("auth: dev IdP 가 켜져 있습니다(%s). 로컬 검증 전용이며 운영에서는 PL_DEV_IDP 를 켜지 마세요", s.devIdP.issuer)
		s.devIdP.register(r)
	}
}

func (s *Service) handleConfig(w http.ResponseWriter, r *http.Request) {
	type acct struct {
		Email      string `json:"email"`
		Name       string `json:"name"`
		Role       string `json:"role"`
		Department string `json:"department"`
	}
	pol, _ := loadPolicy(r.Context(), s.db)
	out := map[string]any{"login": s.oidc != nil, "devIdp": s.devIdP != nil,
		"emailLogin": pol.EmailLoginEnabled, "passwordResetByMail": s.mailer != nil}
	out["providers"] = s.loginProviders(r.Context())
	// 설치 마법사(B38): 초기 설정이 필요하면 로그인 화면이 링크를 보인다. 시연 환경이면 화면 상단에 배너를 보인다.
	out["setupPending"], out["demoMode"] = s.setupPending(r.Context()), s.demoMode(r.Context())
	if s.devIdP != nil {
		list := make([]acct, 0, len(DevAccounts))
		for _, a := range DevAccounts {
			// 목록의 부서는 조직도 값(주소속 조직)이다. 예전 부서 이름(a.Department)은 users 시드에만 쓴다.
			list = append(list, acct{a.Email, a.Name, string(a.Role), a.OrgUnit})
		}
		out["devAccounts"] = list
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Service) handleLogin(w http.ResponseWriter, r *http.Request) {
	prov := r.URL.Query().Get("provider")
	if prov != "" && s.oidcMethod(prov) == "" {
		http.Error(w, "알 수 없는 로그인 방식입니다", http.StatusBadRequest)
		return
	}
	u, err := s.BeginProviderLogin(r.Context(), prov, r.URL.Query().Get("returnTo"), r.URL.Query().Get("login_hint"), purposeLogin, 0)
	if err != nil {
		if errors.Is(err, ErrLoginNotConfigured) {
			// 꺼져 있거나 설정이 덜 된 방식의 라우트는 없는 것처럼 404(범용 OIDC 미설정은 기존대로 503).
			code := http.StatusServiceUnavailable
			if isOAuthProvider(prov) {
				code = http.StatusNotFound
			}
			http.Error(w, err.Error(), code)
			return
		}
		log.Printf("auth: 로그인 시작 실패: %v", err)
		http.Error(w, "로그인을 시작할 수 없습니다", http.StatusBadGateway)
		return
	}
	http.Redirect(w, r, u, http.StatusFound)
}

func (s *Service) handleCallback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	fail := func(code string) { http.Redirect(w, r, "/login?error="+code, http.StatusFound) }
	if q.Get("error") != "" {
		fail("idp")
		return
	}
	raw, expires, to, err := s.CompleteLogin(r.Context(), q.Get("state"), q.Get("code"))
	switch {
	case err == nil:
	case errors.Is(err, contract.ErrForbidden):
		fail("forbidden")
		return
	case errors.Is(err, ErrInvalidState):
		fail("state")
		return
	case errors.Is(err, contract.ErrAuditFailed):
		log.Printf("auth: 로그인 감사 기록 실패: %v", err)
		fail("server")
		return
	default:
		log.Printf("auth: 로그인 실패: %v", err)
		fail("idp")
		return
	}
	if raw != "" { // 연결 시험은 세션 없이 결과만 남긴다
		s.setSessionCookie(w, raw, expires)
	}
	http.Redirect(w, r, to, http.StatusFound)
}

func (s *Service) handleLogout(w http.ResponseWriter, r *http.Request) {
	p, _ := contract.PrincipalFrom(r.Context())
	if c, err := r.Cookie(sessionCookie); err == nil && c.Value != "" {
		if err := s.logout(r.Context(), p, c.Value); err != nil {
			http.Error(w, contract.ErrAuditFailed.Error(), http.StatusInternalServerError)
			return
		}
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true,
		Secure: s.cookieSecure, SameSite: http.SameSiteLaxMode})
	w.WriteHeader(http.StatusNoContent)
}

func roleNamesOrEmpty(n []string) []string {
	if n == nil {
		return []string{}
	}
	return n
}

func rawSessionCookie(r *http.Request) string {
	if c, err := r.Cookie(sessionCookie); err == nil {
		return c.Value
	}
	return ""
}

func (s *Service) handleMe(w http.ResponseWriter, r *http.Request) {
	p, _ := contract.PrincipalFrom(r.Context())
	var deptName string
	if err := s.db.QueryRowContext(r.Context(), `SELECT name FROM departments WHERE id = ?`, p.DepartmentID).Scan(&deptName); err != nil {
		deptName = ""
	}
	unit := ""
	if s.unitName != nil {
		unit = s.unitName(r.Context(), p.UserID)
	}
	if unit == "" {
		unit = deptName // 조직도에 아직 없는 사용자는 예전 부서 이름으로 대신한다
	}
	csrf := ""
	if c, err := r.Cookie(sessionCookie); err == nil && c.Value != "" {
		csrf = csrfTokenFor(c.Value)
	}
	perms := []contract.Permission{}
	for _, perm := range contract.AllPermissions {
		if p.Can(perm) {
			perms = append(perms, perm)
		}
	}
	me := map[string]any{
		"user": map[string]any{
			"id": p.UserID, "name": p.Name, "role": p.Role, "departmentId": p.DepartmentID, "departmentName": deptName, "unitName": unit,
		},
		"csrfToken":   csrf,
		"permissions": perms,
		"roles":       roleNamesOrEmpty(p.RoleNames), // 권한을 준 역할의 이름(표시용)
		"setup":       p.Setup,                       // 제한 세션 사유(비어 있으면 정상): password_change | totp_enroll
		"auth":        s.authSummary(context.WithValue(r.Context(), rawSessionKey{}, rawSessionCookie(r)), p),
	}
	mods := []string{}
	if s.modules != nil {
		mods = s.modules(r.Context())
	}
	me["modules"] = mods                             // 켜진 업무 모듈(B50): 화면이 꺼진 모듈의 메뉴를 숨긴다
	for k, v := range s.meEntities(r.Context(), p) { // 고를 수 있는 법인·현재 법인·그룹 범위 여부(B44). 법인이 하나뿐이면 entities 가 한 개라 화면이 선택기를 그리지 않는다
		me[k] = v
	}
	writeJSON(w, http.StatusOK, me)
}

// groupWriteBlocked 는 "전체 법인" 조회 모드의 쓰기 요청인지다(B44 10절: 전체 모드는 읽기 전용). 현재 법인을 바꾸는 요청과 로그아웃은 막지 않는다.
func groupWriteBlocked(r *http.Request, a contract.ActiveEntity) bool {
	if !a.All || isSafeMethod(r.Method) {
		return false
	}
	return r.URL.Path != "/api/me/entity" && r.URL.Path != "/api/auth/logout"
}

func groupWriteDenied(w http.ResponseWriter) {
	http.Error(w, "전체 법인 조회 중에는 저장할 수 없습니다. 법인을 하나 고른 뒤에 하세요.", http.StatusConflict)
}
