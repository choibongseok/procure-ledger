package auth

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
)

const authRequestTTL = 10 * time.Minute

// 로그인 흐름 오류. 콜백 핸들러가 /login?error= 코드로 옮긴다.
var (
	ErrLoginNotConfigured = errors.New("로그인이 설정되지 않았습니다")
	ErrInvalidState       = errors.New("로그인 요청이 만료되었거나 올바르지 않습니다")
	ErrLoginFailed        = errors.New("IdP 인증에 실패했습니다")
)

// oidcClient 는 표준 OIDC Authorization Code + PKCE(S256) 클라이언트다. 공급자 메타데이터는 첫 사용 때
// 내려받는다(dev IdP 는 같은 프로세스가 아직 뜨는 중일 수 있어 기동 시점에 받지 않는다).
type oidcClient struct {
	cfg OIDCConfig

	mu       sync.Mutex
	oauth    *oauth2.Config
	verifier *oidc.IDTokenVerifier
}

func newOIDCClient(c OIDCConfig) *oidcClient { return &oidcClient{cfg: c} }

var httpClient = &http.Client{Timeout: 10 * time.Second}

func (c *oidcClient) init(ctx context.Context) (*oauth2.Config, *oidc.IDTokenVerifier, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.oauth != nil {
		return c.oauth, c.verifier, nil
	}
	ctx = oidc.ClientContext(ctx, httpClient)
	provider, err := oidc.NewProvider(ctx, c.cfg.Issuer)
	if err != nil {
		return nil, nil, fmt.Errorf("IdP 메타데이터 조회: %w", err)
	}
	c.oauth = &oauth2.Config{
		ClientID: c.cfg.ClientID, ClientSecret: c.cfg.ClientSecret, RedirectURL: c.cfg.RedirectURL,
		Endpoint: provider.Endpoint(), Scopes: []string{oidc.ScopeOpenID, "email", "profile"},
	}
	c.verifier = provider.Verifier(&oidc.Config{ClientID: c.cfg.ClientID})
	return c.oauth, c.verifier, nil
}

// safeRedirect 는 오픈 리다이렉트를 막는다: 같은 사이트의 절대 경로만 허용한다.
func safeRedirect(to string) string {
	if !strings.HasPrefix(to, "/") || strings.HasPrefix(to, "//") || strings.HasPrefix(to, "/\\") || strings.ContainsAny(to, "\r\n") {
		return "/"
	}
	return to
}

// 로그인 요청 목적: 일반 로그인, 또는 관리자의 연결 시험(세션을 만들지 않는다).
const (
	purposeLogin = "login"
	purposeTest  = "test"
)

// BeginLogin 은 기존 범용 OIDC(또는 dev IdP) 로그인을 시작한다.
func (s *Service) BeginLogin(ctx context.Context, returnTo, loginHint string) (string, error) {
	return s.BeginProviderLogin(ctx, "", returnTo, loginHint, purposeLogin, 0)
}

// oidcMethod 는 provider 이름을 정규화한다: 빈 값은 범용 외부 OIDC(또는 켜져 있으면 dev IdP).
func (s *Service) oidcMethod(provider string) string {
	switch provider {
	case MethodGoogle, MethodMicrosoft:
		return provider
	case "", MethodOIDC, MethodDev:
		if s.devIdP != nil {
			return MethodDev
		}
		return MethodOIDC
	}
	return ""
}

// BeginProviderLogin 은 state·nonce·PKCE verifier 를 만들어 저장하고 해당 방식의 IdP 인증 URL 을 돌려준다.
// 켜지지 않은 구글·MS 는 로그인 목적으로는 ErrLoginNotConfigured(라우트는 404)다. 시험(purposeTest)은 켜기 전에도 할 수 있다.
func (s *Service) BeginProviderLogin(ctx context.Context, provider, returnTo, loginHint, purpose string, testUser int64) (string, error) {
	method := s.oidcMethod(provider)
	if method == "" {
		return "", ErrLoginNotConfigured
	}
	client, pc, err := s.clientFor(ctx, method, purpose)
	if err != nil {
		return "", err
	}
	cfg, _, err := client.init(ctx)
	if err != nil {
		return "", err
	}
	state, err := randomString(24)
	if err != nil {
		return "", err
	}
	nonce, err := randomString(24)
	if err != nil {
		return "", err
	}
	verifier := oauth2.GenerateVerifier()
	now := s.now().UTC()
	var tu any
	if testUser != 0 {
		tu = testUser
	}
	err = s.txs.WithTx(ctx, func(tx contract.Tx) error {
		// 만료된 요청 정리.
		if _, err := tx.ExecContext(ctx, `DELETE FROM oidc_auth_requests WHERE expires_at < ?`, now.Format(timeLayout)); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO oidc_auth_requests (state, nonce, code_verifier, redirect_to, created_at, expires_at, provider, purpose, test_user_id) VALUES (?,?,?,?,?,?,?,?,?)`,
			state, nonce, verifier, safeRedirect(returnTo), now.Format(timeLayout), now.Add(authRequestTTL).Format(timeLayout), method, purpose, tu)
		return err
	})
	if err != nil {
		return "", err
	}
	opts := []oauth2.AuthCodeOption{oauth2.S256ChallengeOption(verifier), oidc.Nonce(nonce)}
	if loginHint != "" {
		opts = append(opts, oauth2.SetAuthURLParam("login_hint", loginHint))
	}
	if method == MethodGoogle { // hd 는 편의일 뿐 검증은 서버가 한다(완료 단계)
		hd := "*"
		if len(pc.AllowedDomains) == 1 {
			hd = pc.AllowedDomains[0]
		}
		opts = append(opts, oauth2.SetAuthURLParam("hd", hd))
	}
	return cfg.AuthCodeURL(state, opts...), nil
}

type idClaims struct {
	Subject           string `json:"sub"`
	Email             string `json:"email"`
	EmailVerified     bool   `json:"email_verified"`
	Name              string `json:"name"`
	Nonce             string `json:"nonce"`
	HD                string `json:"hd"`
	TID               string `json:"tid"`
	OID               string `json:"oid"`
	PreferredUsername string `json:"preferred_username"`
}

// oidcResult 는 IdP 에서 검증을 마친 신원이다(세션은 아직 없다).
type oidcResult struct {
	Identity Identity
	Summary  map[string]string // 연결 시험이 보여 줄 가린 클레임 요약
	Purpose  string
	TestUser int64
	Redirect string
	Denied   error // 연결 시험에서 방식별 검사에 걸린 경우(시험은 거부도 결과로 보여 준다)
}

// CompleteLogin 은 콜백을 처리한다: state 1회 소비 → 코드 교환(PKCE verifier) → ID 토큰 서명·aud·exp·nonce 검증 →
// 방식별 클레임 검사(구글 hd·email_verified, MS tid) → 사전 등록된 사용자 매칭 → 같은 트랜잭션에서 세션 생성과 감사 기록.
// 성공하면 원문 세션 ID 와 이동 경로를 돌려준다. 연결 시험이면 세션 없이 결과만 저장하고 rawSession 은 비어 있다.
func (s *Service) CompleteLogin(ctx context.Context, state, code string) (rawSession string, expires time.Time, redirectTo string, err error) {
	res, err := s.completeOIDC(ctx, state, code)
	if err != nil {
		return "", time.Time{}, "", err
	}
	if res.Purpose == purposeTest {
		return "", time.Time{}, res.Redirect, s.finishTest(ctx, res)
	}
	rawSession, expires, err = s.loginWith(ctx, res.Identity)
	if err != nil {
		return "", time.Time{}, "", err
	}
	return rawSession, expires, res.Redirect, nil
}

func (s *Service) completeOIDC(ctx context.Context, state, code string) (oidcResult, error) {
	if state == "" || code == "" {
		return oidcResult{}, ErrInvalidState
	}
	var nonce, verifierStr, method, purpose, redirectTo string
	var testUser sql.NullInt64
	now := s.now().UTC()
	err := s.txs.WithTx(ctx, func(tx contract.Tx) error {
		var expiresAt string
		err := tx.QueryRowContext(ctx, `SELECT nonce, code_verifier, redirect_to, expires_at, provider, purpose, test_user_id FROM oidc_auth_requests WHERE state = ?`, state).
			Scan(&nonce, &verifierStr, &redirectTo, &expiresAt, &method, &purpose, &testUser)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrInvalidState
		}
		if err != nil {
			return err
		}
		// 성공·실패와 무관하게 1회용으로 소비한다(재생 방지).
		if _, err := tx.ExecContext(ctx, `DELETE FROM oidc_auth_requests WHERE state = ?`, state); err != nil {
			return err
		}
		if !now.Before(parseTime(expiresAt)) {
			return ErrInvalidState
		}
		return nil
	})
	if err != nil {
		return oidcResult{}, err
	}
	client, pc, err := s.clientFor(ctx, method, purpose)
	if err != nil {
		return oidcResult{}, err
	}
	cfg, verifier, err := client.init(ctx)
	if err != nil {
		return oidcResult{}, err
	}
	xctx := oidc.ClientContext(ctx, httpClient)
	tok, err := cfg.Exchange(xctx, code, oauth2.VerifierOption(verifierStr))
	if err != nil {
		return oidcResult{}, fmt.Errorf("%w: 코드 교환: %v", ErrLoginFailed, err)
	}
	rawID, _ := tok.Extra("id_token").(string)
	if rawID == "" {
		return oidcResult{}, fmt.Errorf("%w: id_token 없음", ErrLoginFailed)
	}
	idt, err := verifier.Verify(xctx, rawID)
	if err != nil {
		return oidcResult{}, fmt.Errorf("%w: ID 토큰 검증: %v", ErrLoginFailed, err)
	}
	var claims idClaims
	if err := idt.Claims(&claims); err != nil {
		return oidcResult{}, fmt.Errorf("%w: 클레임: %v", ErrLoginFailed, err)
	}
	if subtle.ConstantTimeCompare([]byte(claims.Nonce), []byte(nonce)) != 1 {
		return oidcResult{}, fmt.Errorf("%w: nonce 불일치", ErrLoginFailed)
	}
	claims.Subject = idt.Subject
	id, summary, derr := identityFromClaims(method, pc, claims)
	res := oidcResult{Identity: id, Summary: summary, Purpose: purpose, TestUser: testUser.Int64, Redirect: safeRedirect(redirectTo)}
	if derr != nil {
		if purpose == purposeTest {
			res.Denied = derr
			res.Identity.Provider = method
			return res, nil
		}
		s.auditLoginDenied(ctx, method, "sub:"+truncate(claims.Subject, 60), derr)
		return oidcResult{}, contract.ErrForbidden
	}
	return res, nil
}

// identityFromClaims 는 방식별 클레임 검사를 하고 resolveAccount 에 넘길 신원을 만든다. 검사를 통과하지 못하면 denial 이다.
func identityFromClaims(method string, pc providerConfig, c idClaims) (Identity, map[string]string, error) {
	switch method {
	case MethodGoogle:
		if !c.EmailVerified {
			return Identity{}, nil, denial{ReasonEmailUnverified}
		}
		hd := strings.ToLower(strings.TrimSpace(c.HD))
		if hd == "" || !containsFold(pc.AllowedDomains, hd) { // 개인 구글 계정은 hd 가 없다
			return Identity{}, nil, denial{ReasonDomainMismatch}
		}
		email := normEmail(c.Email)
		if _, dom, ok := strings.Cut(email, "@"); !ok || dom != hd {
			return Identity{}, nil, denial{ReasonDomainMismatch}
		}
		return Identity{Provider: MethodGoogle, Issuer: "accounts.google.com", Subject: c.Subject, Email: email},
			map[string]string{"이메일": maskEmail(email), "도메인": hd, "이메일 검증": "예"}, nil
	case MethodMicrosoft:
		tid := strings.ToLower(strings.TrimSpace(c.TID))
		if tid == "" || pc.TenantID == "" || tid != strings.ToLower(pc.TenantID) {
			return Identity{}, nil, denial{ReasonTenantMismatch}
		}
		if strings.TrimSpace(c.OID) == "" {
			return Identity{}, nil, denial{ReasonNoSubject}
		}
		// Entra 의 이메일은 사용자가 바꿀 수 있고 검증 표시가 없다. 테넌트가 맞은 뒤 첫 연결에만 쓰고, 이후엔 (tid, oid)로만 인식한다.
		email := normEmail(c.Email)
		if email == "" {
			email = normEmail(c.PreferredUsername)
		}
		return Identity{Provider: MethodMicrosoft, Issuer: tid, Subject: strings.ToLower(strings.TrimSpace(c.OID)), Email: email},
			map[string]string{"이메일": maskEmail(email), "테넌트": maskID(tid), "개체 ID": maskID(c.OID)}, nil
	default:
		email := ""
		if c.EmailVerified { // 검증되지 않은 이메일로는 첫 연결을 하지 않는다
			email = c.Email
		}
		return Identity{Provider: method, Subject: c.Subject, Email: email}, map[string]string{"이메일": maskEmail(email)}, nil
	}
}

func containsFold(list []string, v string) bool {
	for _, x := range list {
		if strings.EqualFold(strings.TrimSpace(x), v) {
			return true
		}
	}
	return false
}

// maskEmail 은 시험 화면에 보일 이메일을 가린다(k**@corp.com).
func maskEmail(e string) string {
	local, dom, ok := strings.Cut(e, "@")
	if !ok || local == "" {
		return ""
	}
	r := []rune(local)
	return string(r[:1]) + strings.Repeat("*", min(len(r)-1, 4)) + "@" + dom
}

func maskID(s string) string {
	if len(s) <= 8 {
		return strings.Repeat("*", len(s))
	}
	return s[:8] + "…"
}

// auditLoginDenied 는 방식별 검사에서 거부된 로그인을 감사에 남긴다(입력한 이메일 원문은 남기지 않는다).
func (s *Service) auditLoginDenied(ctx context.Context, method, target string, derr error) {
	_ = s.txs.WithTx(ctx, func(tx contract.Tx) error {
		return s.record(ctx, tx, contract.Principal{Channel: contract.ChannelWeb}, "session.login", audit.TargetSession, target, audit.ResultDenied,
			[]audit.DiffItem{{Key: "login_method", New: method}, {Key: "reason", New: denialReason(derr)}}, "방식별 검사에서 거부")
	})
}

// loginWith 는 외부 신원으로 로그인한다: 계정 판정(resolveAccount) → 세션 생성 → 감사를 한 트랜잭션에서 한다.
// 거부(등록되지 않음·비활성·퇴사 등)도 감사에 남기고 contract.ErrForbidden 으로 돌려준다.
func (s *Service) loginWith(ctx context.Context, id Identity) (rawSession string, expires time.Time, err error) {
	return s.loginWithPrev(ctx, id, "")
}

// loginWithPrev 는 loginWith 에 더해 요청의 기존 세션을 폐기한다(로그인마다 새 세션 ID: 고정 공격 방지, ACS 가 쓴다).
func (s *Service) loginWithPrev(ctx context.Context, id Identity, prevRaw string) (rawSession string, expires time.Time, err error) {
	denied := false
	err = s.txs.WithTx(ctx, func(tx contract.Tx) error {
		u, err := s.resolveAccount(ctx, tx, id)
		if err != nil {
			actor := contract.Principal{Channel: contract.ChannelWeb}
			if errors.Is(err, contract.ErrForbidden) {
				// 거부도 기록한다. 기록을 커밋해야 하므로 nil 로 끝내고 denied 로 알린다.
				if rerr := s.record(ctx, tx, actor, "session.login", audit.TargetSession, "sub:"+truncate(id.Subject, 60), audit.ResultDenied,
					[]audit.DiffItem{{Key: "login_method", New: id.Provider}, {Key: "reason", New: denialReason(err)}}, "등록되지 않았거나 비활성인 사용자"); rerr != nil {
					return rerr
				}
				denied = true
				return nil
			}
			return err
		}
		if err := s.revokeSessionRaw(ctx, tx, u.UserID, prevRaw); err != nil {
			return err
		}
		raw, exp, prefix, err := s.createSession(ctx, tx, u.UserID, id.Provider, "")
		if err != nil {
			return err
		}
		rawSession, expires = raw, exp
		return s.record(ctx, tx, u, "session.login", audit.TargetSession, prefix, audit.ResultSuccess,
			[]audit.DiffItem{{Key: "login_method", New: id.Provider}}, "")
	})
	if err != nil {
		return "", time.Time{}, err
	}
	if denied {
		return "", time.Time{}, contract.ErrForbidden
	}
	return rawSession, expires, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
