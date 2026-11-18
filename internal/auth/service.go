// Package auth 는 OIDC 로그인(Authorization Code + PKCE), 서버 측 세션, 개인 토큰, 사용자 관리,
// 내장 dev IdP 를 구현한다.
//
// 불변식
//   - 세션 ID·개인 토큰 원문은 DB 에 저장하지 않는다(SHA-256 해시만). 토큰 원문은 발급 응답에서 한 번만 나간다.
//   - 쿠키 세션으로 들어온 상태 변경 요청은 CSRF 토큰(X-CSRF-Token)과 Origin 을 검사한다.
//     Authorization: Bearer 요청은 앰비언트 자격이 아니므로 CSRF 대상이 아니다.
//   - dev IdP 는 Options.DevIDP 가 켜졌을 때만 라우트가 등록된다(기본 꺼짐). 외부 IdP 설정과 동시에 켤 수 없다.
//   - 쓰기에는 같은 트랜잭션의 감사 기록이 따른다. 주의: WithTx 안에서는 s.db 를 직접 쓰지 말고 tx 만 쓴다
//     (SQLite 연결이 1개라 교착된다).
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/choibongseok/procure-ledger/internal/privacy"
	"net"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
)

// OIDCConfig 는 외부 IdP 설정이다.
type OIDCConfig struct {
	Issuer       string
	ClientID     string
	ClientSecret string
	RedirectURL  string
}

// Options 는 Service 생성 설정이다.
type Options struct {
	DB           *sql.DB
	Txs          contract.TxManager
	Audit        audit.Recorder
	BaseURL      string // 브라우저가 접속하는 주소(끝 슬래시 없음)
	CookieSecure bool
	DevIDP       bool
	OIDC         OIDCConfig
	Now          func() time.Time
	// MaxActiveTokens 는 사용자당 유효(폐기·만료 아님) 개인 토큰 개수 상한이다. 0 이면 상한 없음.
	MaxActiveTokens int
	// Argon 은 비밀번호 해시 비용이다(0 이면 기본 64MiB·3·2). Mailer 가 nil 이면 메일 발송이 꺼진다(비밀번호 재설정 링크 없음, 관리자 임시 비밀번호만).
	Argon  ArgonParams
	Mailer Mailer
	// Google·Microsoft 는 OAuth 클라이언트 자격이다(환경변수, 비밀). 비어 있으면 그 방식은 켤 수 없다.
	Google, Microsoft OAuthEnv
	// SAML SP 인증서·키 파일 경로(환경변수). 비어 있으면 SAML 은 켤 수 없다.
	SAML SAMLEnv
	// SecretKey 는 TOTP 비밀키 암호화용 32바이트 키다(PL_AUTH_SECRET_KEY). 비어 있으면 2단계 인증 기능이 꺼진다.
	SecretKey []byte
	// SyncMail 은 시험용이다: 메일을 응답 안에서 동기로 보낸다.
	SyncMail bool
}

// Service 는 contract.Authenticator·TokenManager·UserDirectory·AuthHTTP 를 구현한다.
type Service struct {
	db    *sql.DB
	txs   contract.TxManager
	audit audit.Recorder
	base  string
	now   func() time.Time

	maxActiveTokens int

	priv privacy.Noter // 다른 사람의 이메일 원문을 싣는 관리 목록의 열람 기록(B41). nil 이면 기록하지 않는다(서버 조립은 항상 꽂는다)

	cookieSecure bool
	devIdP       *DevIdP
	oidc         *oidcClient // nil 이면 로그인 미설정(fail-closed)

	box               *secretBox // nil 이면 2단계 인증 꺼짐
	google, microsoft OAuthEnv
	clientMu          sync.Mutex
	clients           map[string]*oidcClient
	// issuerOverride 는 시험이 구글·MS 발급자를 모의 IdP 로 바꾸는 자리다(운영 코드는 쓰지 않는다).
	issuerOverride map[string]string
	baseHost       string
	samlKeys       *samlSPKeys
	hasher         *hasher
	mailer         Mailer
	syncMail       bool
	limiter        *ipLimiter
	dummyMu        sync.Mutex
	dummyHash      string
	dummyDone      chan struct{}

	// unitName 은 사용자의 오늘 기준 주소속 조직 이름을 돌려준다(조직도 값). 조직 조회를 늦게 붙이므로 SetUnitLookup 으로 넣는다.
	unitName func(ctx context.Context, userID int64) string
	modules  func(ctx context.Context) []string // 켜진 업무 모듈 키(B50). 없으면 빈 목록
}

// SetUnitLookup 은 /api/me 가 보이는 주소속 조직 이름의 출처를 정한다. 비워 두거나 빈 이름을 돌려주면 부서(departments) 이름을 쓴다.
func (s *Service) SetUnitLookup(f func(ctx context.Context, userID int64) string) { s.unitName = f }

var (
	_ contract.Authenticator = (*Service)(nil)
	_ contract.TokenManager  = (*Service)(nil)
	_ contract.AuthHTTP      = (*Service)(nil)
)

// New 는 Service 를 만든다. dev IdP 와 외부 IdP 를 동시에 설정하면 오류다(운영 오설정 방지).
func New(o Options) (*Service, error) {
	if o.DB == nil || o.Txs == nil || o.Audit == nil {
		return nil, errors.New("auth: DB·Txs·Audit 이 필요합니다")
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	base := strings.TrimRight(o.BaseURL, "/")
	if base == "" {
		base = "http://localhost:8080"
	}
	s := &Service{db: o.DB, txs: o.Txs, audit: o.Audit, base: base, now: o.Now, maxActiveTokens: o.MaxActiveTokens,
		cookieSecure: o.CookieSecure || strings.HasPrefix(base, "https://"),
		baseHost:     hostOf(base), hasher: newHasher(o.Argon), google: o.Google, microsoft: o.Microsoft, clients: map[string]*oidcClient{}, issuerOverride: map[string]string{}, mailer: o.Mailer, syncMail: o.SyncMail, limiter: newIPLimiter()}
	s.samlKeys = loadSAMLKeys(o.SAML)
	s.startDummyHash()
	if len(o.SecretKey) > 0 {
		box, err := newSecretBox(o.SecretKey)
		if err != nil {
			return nil, err
		}
		s.box = box
	}
	redirect := o.OIDC.RedirectURL
	if redirect == "" {
		redirect = base + "/api/auth/callback"
	}
	switch {
	case o.DevIDP && o.OIDC.Issuer != "":
		return nil, errors.New("auth: dev IdP(PL_DEV_IDP)와 외부 IdP(PL_OIDC_ISSUER)를 동시에 설정할 수 없습니다")
	case o.DevIDP:
		if !isLoopbackURL(base) {
			return nil, fmt.Errorf("auth: dev IdP(PL_DEV_IDP)는 로컬 주소(localhost·127.0.0.1·::1)에서만 켤 수 있습니다: PL_BASE_URL=%s. 운영에서는 PL_DEV_IDP 를 끄세요", base)
		}
		idp, err := NewDevIdP(base+devIdPPrefix, redirect, o.Now)
		if err != nil {
			return nil, err
		}
		idp.lookup = s.devLookup
		s.devIdP = idp
		s.oidc = newOIDCClient(OIDCConfig{Issuer: idp.issuer, ClientID: devClientID, ClientSecret: devClientSecret, RedirectURL: redirect})
	case o.OIDC.Issuer != "":
		if o.OIDC.ClientID == "" {
			return nil, errors.New("auth: PL_OIDC_CLIENT_ID 가 필요합니다")
		}
		c := o.OIDC
		c.RedirectURL = redirect
		s.oidc = newOIDCClient(c)
	}
	return s, nil
}

// IsLoopbackURL 은 URL 의 호스트가 로컬인지 본다(bootstrap 의 로컬 전용 설정 검사가 쓴다).
func IsLoopbackURL(raw string) bool { return isLoopbackURL(raw) }

// isLoopbackURL 은 URL 의 호스트가 로컬(localhost, *.localhost, 127.0.0.0/8, ::1)인지 본다.
// 비밀번호 없는 dev IdP 가 외부에서 닿는 주소로 켜지는 운영 유입을 기동 단계에서 막는 데 쓴다.
func isLoopbackURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return false
	}
	h := strings.ToLower(u.Hostname())
	if h == "localhost" || strings.HasSuffix(h, ".localhost") {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

// DevIDPEnabled 는 dev IdP 가 켜져 있는지다.
func (s *Service) DevIDPEnabled() bool { return s.devIdP != nil }

func randomString(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

const timeLayout = time.RFC3339Nano

func parseTime(s string) time.Time {
	t, err := time.Parse(timeLayout, s)
	if err != nil {
		return time.Time{}
	}
	return t.UTC()
}

func parseTimePtr(ns sql.NullString) *time.Time {
	if !ns.Valid || ns.String == "" {
		return nil
	}
	t := parseTime(ns.String)
	return &t
}

func (s *Service) webActor(p contract.Principal) contract.Principal {
	p.Channel = contract.ChannelWeb
	return p
}

// record 는 tx 안에서 감사 기록을 남긴다.
func (s *Service) record(ctx context.Context, tx contract.Tx, actor contract.Principal, action string, tt audit.TargetType, id string, result audit.Result, diff []audit.DiffItem, detail string) error {
	if actor.Channel == "" {
		actor.Channel = contract.ChannelWeb
	}
	return s.audit.Record(ctx, tx, audit.Entry{Actor: actor, Action: action, TargetType: tt, TargetID: id, Result: result, Diff: diff, Detail: detail})
}

func idStr(id int64) string { return fmt.Sprintf("%d", id) }

func hostOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Host
}

// SetPrivacy 는 열람 기록기를 꽂는다(기록기는 인증 서비스보다 늦게 만들어져 조립 때 나중에 붙인다).
func (s *Service) SetPrivacy(n privacy.Noter) { s.priv = n }

// SetModules 는 켜진 업무 모듈 키 조회를 꽂는다(B50). /api/me 가 화면에 알려 꺼진 모듈의 메뉴를 숨기게 한다(서버는 어차피 꺼진 모듈 라우트를 404 로 막는다).
func (s *Service) SetModules(f func(ctx context.Context) []string) { s.modules = f }
