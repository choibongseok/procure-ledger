// Package config 는 환경변수 기반 서버 설정이다.
// 비밀값은 설정 파일이 아니라 환경변수로만 받고, 로그·감사에 남기지 않는다.
package config

import (
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract/llm"
	"github.com/choibongseok/procure-ledger/internal/export"
)

// Config 는 서버 설정이다. 기본값은 로컬 개발용이며 운영은 환경변수로 덮어쓴다.
type Config struct {
	Addr   string // PL_ADDR, 기본 :8080
	DBPath string // PL_DB_PATH, 기본 data/erp.db

	// DBURL 이 postgres:// 또는 postgresql:// 이면 PostgreSQL 을 쓴다(PL_DB_URL, B37). 비어 있으면 SQLite(DBPath). 비밀번호가 들어 있어 로그·오류에 그대로 쓰지 않는다(dbutil.Redact).
	DBURL string
	// DBMaxConns 는 PostgreSQL 연결 풀 상한이다(PL_DB_MAX_CONNS, 기본 10, 범위 2~100). 범위 밖이거나 숫자가 아니면 -1 로 읽고 조립 단계가 기동을 거부한다. SQLite 는 항상 1이다.
	DBMaxConns int
	// DBAllowInsecure 는 루프백 호스트에서만 TLS 없는 PostgreSQL 연결을 허용한다(PL_DB_ALLOW_INSECURE=1).
	DBAllowInsecure bool

	// DevIDP 는 내장 dev IdP 핸들러를 켠다(PL_DEV_IDP=1). 운영 빌드에서는 꺼져 있어야 한다(기본 꺼짐).
	// 켜도 같은 OIDC 코드 경로로 붙는다. 담당: platform.
	DevIDP bool

	// DevSeedPassword 는 개발 시드 계정의 이메일 로그인 초기 비밀번호다(PL_DEV_SEED_PASSWORD, 비밀). 있으면 dev IdP 가 꺼져 있어도 개발 시드(사용자·조직)가 돌고,
	// 이메일 로그인 자격이 없는 시드 계정에 이 값으로 자격을 만든다(첫 로그인에서 변경 강제). 운영에서는 비워 둔다.
	DevSeedPassword string

	// SetupCode 는 설치 마법사의 설치 코드를 직접 정한다(PL_SETUP_CODE, 비밀, 16자 이상). 비우면 기동할 때마다 무작위 코드를 만들어 서버 로그에만 출력한다.
	// 값은 로그에 찍지 않는다. 설치(pending) 상태가 아니면 쓰이지 않는다.
	SetupCode string

	// Rehearsal 은 복구 리허설 모드다(PL_REHEARSAL=1, B38 D15). erp-admin restore --rehearse 와 scripts/restore-rehearsal.sh 가 임시 위치의 복구본에서
	// 서버를 띄울 때만 켠다: 주기 작업·메일/웹훅 발송·감사 앵커 기록을 하지 않는다. 임시 경로(os.TempDir() 아래 또는 .rehearsal 마커가 있는 디렉터리)의
	// SQLite 에서만 허용하고 운영 경로·PG 에서는 기동을 거부한다(internal/rehearsal).
	Rehearsal bool

	// NotifyInsecureLocal 은 알림 웹훅이 루프백 주소(와 http)로 가는 것을 허용한다(PL_NOTIFY_INSECURE_LOCAL=1). 로컬 시험(모의 웹훅 서버) 전용이며 운영에서는 비운다.
	NotifyInsecureLocal bool

	// BaseURL 은 브라우저가 서버에 접속하는 주소다(PL_BASE_URL). OIDC 리다이렉트·dev IdP 발급자 주소의 기준.
	// 비우면 Addr 에서 http://host:port 를 만든다.
	BaseURL string
	// CookieSecure 는 세션 쿠키에 Secure 속성을 강제한다(PL_COOKIE_SECURE=1). BaseURL 이 https 이면 자동으로 켜진다.
	CookieSecure bool

	// OIDC 는 외부 IdP 설정 자리다(PL_OIDC_ISSUER, PL_OIDC_CLIENT_ID, PL_OIDC_CLIENT_SECRET, PL_OIDC_REDIRECT_URL).
	// 담당: platform.
	OIDCIssuer       string
	OIDCClientID     string
	OIDCClientSecret string
	OIDCRedirectURL  string

	// 감사 체인 외부 앵커(B4). 키가 비면 기능이 꺼진 채로 기동한다. 키는 환경변수로만 받고 코드·DB·저장소에 두지 않는다.
	// 키가 있으면 AuditAnchorPath(DB 와 다른 볼륨·권한 권장)가 필수이고, 쓸 수 없으면 기동이 거부된다.
	AuditAnchorKey      string        // PL_AUDIT_ANCHOR_KEY (16바이트 이상)
	AccountSecretKey    string        // PL_ACCOUNT_SECRET_KEY: 계좌번호 저장 암호화용 32바이트(base64) 키(비밀). 비면 계좌번호 저장을 거부한다(B32 D1 후속)
	AuditAnchorPath     string        // PL_AUDIT_ANCHOR_PATH
	AuditAnchorInterval time.Duration // PL_AUDIT_ANCHOR_INTERVAL (Go 기간 형식, 기본 5m, 0 이면 주기 기록 없음, 형식 오류면 음수)

	// MCPRatePerMin 은 개인 토큰 하나의 분당 MCP 호출 한도다(PL_MCP_RATE_PER_MIN, B13). 0 이면 한도 없음(기본).
	// 음수나 숫자가 아닌 값은 -1 로 읽고 조립 단계가 기동을 거부한다.
	MCPRatePerMin int

	// MCPRatePerUserPerMin 은 사용자 한 명의 모든 개인 토큰을 합친 분당 MCP 호출 한도다(PL_MCP_RATE_PER_USER_PER_MIN, B17).
	// 0 이면 사용자 합산 한도 없음(기본). 음수나 숫자가 아닌 값은 -1 로 읽고 조립 단계가 기동을 거부한다.
	MCPRatePerUserPerMin int

	// MaxActiveTokens 는 사용자당 유효(폐기·만료 아님) 개인 토큰 개수 상한이다(PL_MAX_ACTIVE_TOKENS, B17).
	// 기본 10, 0 이면 상한 없음. 음수나 숫자가 아닌 값은 -1 로 읽고 조립 단계가 기동을 거부한다.
	MaxActiveTokens int

	// SCIM 연동(B40). 같은 출처의 유효 토큰 상한(기본 2, 회전 중첩용), 토큰당 분당 호출 한도(기본 600, 0 이면 끔), 동기화 기록 보존 일수(기본 365).
	// 음수나 숫자가 아닌 값은 -1 로 읽고 조립 단계가 기동을 거부한다.
	SCIMMaxActiveTokens int // PL_SCIM_MAX_ACTIVE_TOKENS
	SCIMRatePerMin      int // PL_SCIM_RATE_PER_MIN
	SCIMLogKeepDays     int // PL_SCIM_LOG_KEEP_DAYS
	// SCIMAllowCIDR 은 SCIM 요청을 받을 출발지 주소 목록이다(PL_SCIM_ALLOW_CIDR, 쉼표로 구분한 CIDR 또는 IP). 비면 제한 없음. 연결 상대 주소(RemoteAddr)로만 판정한다.
	SCIMAllowCIDR string
	// 연동 API(B42). 허용 출발지(PL_API_ALLOW_CIDR, 비면 제한 없음), 유효 토큰 상한(PL_API_MAX_ACTIVE_TOKENS, 기본 5), 서버 전체 동시 요청 상한(PL_API_MAX_CONCURRENT, 기본 32),
	// 토큰당 분당 읽기 한도의 기본값(PL_API_RATE_PER_MIN, 기본 600: 화면 설정이 없을 때). 0 이하·잘못된 값은 -1 로 읽어 조립이 기동을 거부한다(0 은 기본값을 쓴다).
	APIAllowCIDR       string
	APIMaxActiveTokens int
	APIMaxConcurrent   int
	APIRatePerMin      int

	// 첨부 상한(B23, 명세 3.15·D16). 기본 파일당 10MB·문서당 20개·합계 50MB. 0 이하이거나 숫자가 아닌 값은 -1 로 읽고 조립 단계가 기동을 거부한다.
	AttachMaxFileBytes  int64 // PL_ATTACH_MAX_FILE_BYTES
	AttachMaxFiles      int   // PL_ATTACH_MAX_FILES
	AttachMaxTotalBytes int64 // PL_ATTACH_MAX_TOTAL_BYTES

	// ExportMaxRows 는 목록·보고서 내보내기의 1회 상한 행 수다(PL_EXPORT_MAX_ROWS, B28). 기본 10000, 허용 100~100000. 범위 밖이거나 숫자가 아니면 -1 로 읽고 조립 단계가 기동을 거부한다.
	ExportMaxRows int

	// Auth 는 로그인 방식 확장(B30)의 환경변수다. 비밀값은 환경변수로만 받는다.
	Auth AuthEnv

	// LLM 은 게이트웨이 설정이다. 모델 ID 는 코드에 두지 않고 PL_LLM_MODEL 로만 받는다. 담당: ai.
	LLM llm.Config
}

// AuthEnv 는 B30 로그인 방식의 환경변수다(.env.example 에 모두 적는다).
type AuthEnv struct {
	SMTPHost, SMTPPort, SMTPUser, SMTPPassword, SMTPFrom string
	SMTPInsecure                                         bool // PL_SMTP_INSECURE=1: 로컬 PL_BASE_URL 에서만 TLS 없는 SMTP 허용
	// Argon 비용(0 이면 기본). 형식 오류는 -1 로 읽고 조립 단계가 기동을 거부한다.
	ArgonMemKiB, ArgonTime, ArgonPar int
	// SecretKey 는 TOTP 비밀키 암호화용 32바이트(base64) 키다(PL_AUTH_SECRET_KEY, 비밀). 비면 2단계 인증 기능이 꺼진다.
	SecretKey string
	// SAML SP 인증서·키 파일 경로(PL_SAML_SP_CERT_PATH, PL_SAML_SP_KEY_PATH). 키 파일은 저장소 밖에 두고 권한 0600.
	SAMLCertPath, SAMLKeyPath string
	// 구글·MS OAuth 클라이언트(PL_AUTH_GOOGLE_CLIENT_ID/SECRET, PL_AUTH_MS_CLIENT_ID/SECRET). 비밀값은 환경변수로만 받는다.
	GoogleClientID, GoogleClientSecret, MSClientID, MSClientSecret string
}

// Load 는 환경변수에서 설정을 읽는다.
func Load() Config {
	c := load()
	c.AuditAnchorInterval = anchorInterval(env("PL_AUDIT_ANCHOR_INTERVAL", "5m"))
	c.MCPRatePerMin = ratePerMin(env("PL_MCP_RATE_PER_MIN", "0"))
	c.MCPRatePerUserPerMin = ratePerMin(env("PL_MCP_RATE_PER_USER_PER_MIN", "0"))
	c.MaxActiveTokens = ratePerMin(env("PL_MAX_ACTIVE_TOKENS", "10"))
	c.SCIMMaxActiveTokens = ratePerMin(env("PL_SCIM_MAX_ACTIVE_TOKENS", "2"))
	c.SCIMRatePerMin = ratePerMin(env("PL_SCIM_RATE_PER_MIN", "600"))
	c.SCIMLogKeepDays = ratePerMin(env("PL_SCIM_LOG_KEEP_DAYS", "365"))
	c.SCIMAllowCIDR = strings.TrimSpace(env("PL_SCIM_ALLOW_CIDR", ""))
	c.APIAllowCIDR = strings.TrimSpace(env("PL_API_ALLOW_CIDR", ""))
	c.APIMaxActiveTokens = ratePerMin(env("PL_API_MAX_ACTIVE_TOKENS", "5"))
	c.APIMaxConcurrent = ratePerMin(env("PL_API_MAX_CONCURRENT", "32"))
	c.APIRatePerMin = ratePerMin(env("PL_API_RATE_PER_MIN", "600"))
	c.DBMaxConns = dbMaxConns(env("PL_DB_MAX_CONNS", "10"))
	c.ExportMaxRows = export.ParseMaxRows(strings.TrimSpace(env("PL_EXPORT_MAX_ROWS", "")))
	c.AttachMaxFileBytes = positiveInt64(env("PL_ATTACH_MAX_FILE_BYTES", "10485760"))
	c.AttachMaxFiles = int(positiveInt64(env("PL_ATTACH_MAX_FILES", "20")))
	c.AttachMaxTotalBytes = positiveInt64(env("PL_ATTACH_MAX_TOTAL_BYTES", "52428800"))
	c.Auth = AuthEnv{
		SMTPHost: env("PL_SMTP_HOST", ""), SMTPPort: env("PL_SMTP_PORT", ""), SMTPUser: env("PL_SMTP_USER", ""),
		SMTPPassword: env("PL_SMTP_PASSWORD", ""), SMTPFrom: env("PL_SMTP_FROM", ""), SMTPInsecure: env("PL_SMTP_INSECURE", "") == "1",
		ArgonMemKiB: ratePerMin(env("PL_AUTH_PASSWORD_ARGON_MEM_KIB", "0")), ArgonTime: ratePerMin(env("PL_AUTH_PASSWORD_ARGON_TIME", "0")),
		ArgonPar: ratePerMin(env("PL_AUTH_PASSWORD_ARGON_PAR", "0")), SecretKey: env("PL_AUTH_SECRET_KEY", ""), SAMLCertPath: env("PL_SAML_SP_CERT_PATH", ""), SAMLKeyPath: env("PL_SAML_SP_KEY_PATH", ""),
		GoogleClientID: env("PL_AUTH_GOOGLE_CLIENT_ID", ""), GoogleClientSecret: env("PL_AUTH_GOOGLE_CLIENT_SECRET", ""),
		MSClientID: env("PL_AUTH_MS_CLIENT_ID", ""), MSClientSecret: env("PL_AUTH_MS_CLIENT_SECRET", ""),
	}
	c.BaseURL = strings.TrimRight(env("PL_BASE_URL", defaultBaseURL(c.Addr)), "/")
	c.CookieSecure = env("PL_COOKIE_SECURE", "") == "1" || strings.HasPrefix(c.BaseURL, "https://")
	return c
}

// anchorInterval 은 앵커 기록 간격을 읽는다. 형식이 틀리면 음수를 돌려주고 조립 단계가 기동을 거부한다.
func anchorInterval(s string) time.Duration {
	if s == "0" {
		return 0
	}
	d, err := time.ParseDuration(s)
	if err != nil || d < 0 {
		return -1
	}
	return d
}

// ratePerMin 은 분당 한도를 읽는다. 형식이 틀리거나 음수면 -1 이다.
// dbMaxConns 는 PostgreSQL 연결 풀 상한(2~100)을 읽는다. 범위 밖이거나 숫자가 아니면 -1 이다.
func dbMaxConns(s string) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || n < 2 || n > 100 {
		return -1
	}
	return n
}

func ratePerMin(s string) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || n < 0 {
		return -1
	}
	return n
}

// positiveInt64 는 1 이상의 정수를 읽는다. 형식이 틀리거나 0 이하이면 -1 이다.
func positiveInt64(s string) int64 {
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil || n <= 0 || n > 1<<50 {
		return -1
	}
	return n
}

// defaultBaseURL 은 ":8080" → "http://localhost:8080", "127.0.0.1:9" → "http://127.0.0.1:9".
func defaultBaseURL(addr string) string {
	if strings.HasPrefix(addr, ":") {
		return "http://localhost" + addr
	}
	return "http://" + addr
}

func load() Config {
	return Config{
		Addr:                env("PL_ADDR", ":8080"),
		DBPath:              env("PL_DB_PATH", "data/erp.db"),
		DBURL:               strings.TrimSpace(env("PL_DB_URL", "")),
		DBAllowInsecure:     env("PL_DB_ALLOW_INSECURE", "") == "1",
		DevIDP:              env("PL_DEV_IDP", "") == "1",
		DevSeedPassword:     env("PL_DEV_SEED_PASSWORD", ""),
		SetupCode:           env("PL_SETUP_CODE", ""),
		Rehearsal:           env("PL_REHEARSAL", "") == "1",
		NotifyInsecureLocal: env("PL_NOTIFY_INSECURE_LOCAL", "") == "1",
		AuditAnchorKey:      env("PL_AUDIT_ANCHOR_KEY", ""),
		AccountSecretKey:    env("PL_ACCOUNT_SECRET_KEY", ""),
		AuditAnchorPath:     env("PL_AUDIT_ANCHOR_PATH", ""),
		OIDCIssuer:          env("PL_OIDC_ISSUER", ""),
		OIDCClientID:        env("PL_OIDC_CLIENT_ID", ""),
		OIDCClientSecret:    env("PL_OIDC_CLIENT_SECRET", ""),
		OIDCRedirectURL:     env("PL_OIDC_REDIRECT_URL", ""),
		LLM: llm.Config{
			Provider:     strings.ToLower(env("PL_LLM_PROVIDER", "mock")),
			DefaultModel: env("PL_LLM_MODEL", ""),
			APIKeyEnv:    env("PL_LLM_API_KEY_ENV", "ANTHROPIC_API_KEY"),
			Timeout:      30 * time.Second,
		},
	}
}

func env(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}
