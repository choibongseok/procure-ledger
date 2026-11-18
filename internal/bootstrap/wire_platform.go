package bootstrap

import (
	"context"
	"fmt"
	"github.com/choibongseok/procure-ledger/internal/apitoken"
	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/privreport"
	"github.com/choibongseok/procure-ledger/internal/sod"
	"github.com/choibongseok/procure-ledger/internal/webhooks"
	"log"
	"strings"

	"github.com/choibongseok/procure-ledger/internal/access"
	"github.com/choibongseok/procure-ledger/internal/acctsecret"
	"github.com/choibongseok/procure-ledger/internal/audit"
	"github.com/choibongseok/procure-ledger/internal/auth"
	"github.com/choibongseok/procure-ledger/internal/config"
	"github.com/choibongseok/procure-ledger/internal/contract/deps"
	"github.com/choibongseok/procure-ledger/internal/dbutil"
	"github.com/choibongseok/procure-ledger/internal/entity"
	"github.com/choibongseok/procure-ledger/internal/export"
	"github.com/choibongseok/procure-ledger/internal/importflow"
	"github.com/choibongseok/procure-ledger/internal/module"
	"github.com/choibongseok/procure-ledger/internal/notify"
	"github.com/choibongseok/procure-ledger/internal/onboarding"
	"github.com/choibongseok/procure-ledger/internal/org"
	"github.com/choibongseok/procure-ledger/internal/privacy"
	"github.com/choibongseok/procure-ledger/internal/rbac"
	"github.com/choibongseok/procure-ledger/internal/scim"
)

// WirePlatform 은 platform 서비스(감사 기록기·리더, 인증·세션·토큰·사용자, 마스킹)를 만들어 d 에 채운다.
// d.DB 는 main 이 이미 채웠다. dev IdP 가 켜진 경우에만 시드 계정을 넣는다(운영 기본값은 꺼짐).
func WirePlatform(cfg config.Config, d *deps.Deps) error {
	d.Txs = dbutil.NewTxManager(d.DB, d.Dialect)
	store := audit.New(d.DB)
	d.Audit, d.AuditReader, d.AuditScan = store, store, store
	d.Masker = rbac.Masker{}
	if err := wireAnchor(cfg, store, d); err != nil {
		return err
	}

	if cfg.MCPRatePerMin < 0 {
		return fmt.Errorf("PL_MCP_RATE_PER_MIN 은 0 이상의 정수여야 합니다(0 은 한도 없음)")
	}
	if cfg.MCPRatePerUserPerMin < 0 {
		return fmt.Errorf("PL_MCP_RATE_PER_USER_PER_MIN 은 0 이상의 정수여야 합니다(0 은 사용자 합산 한도 없음)")
	}
	if cfg.MaxActiveTokens < 0 {
		return fmt.Errorf("PL_MAX_ACTIVE_TOKENS 는 0 이상의 정수여야 합니다(0 은 개수 상한 없음)")
	}
	if cfg.ExportMaxRows < 0 {
		return fmt.Errorf("PL_EXPORT_MAX_ROWS 는 %d 이상 %d 이하의 정수여야 합니다(기본 %d)", export.MinMaxRows, export.MaxMaxRows, export.DefaultMaxRows)
	}
	mailer, argon, secretKey, err := authFromConfig(cfg)
	if err != nil {
		return err
	}
	svc, err := auth.New(auth.Options{
		DB: d.DB, Txs: d.Txs, Audit: d.Audit, Mailer: mailer, Argon: argon, SecretKey: secretKey, SAML: auth.SAMLEnv{CertPath: cfg.Auth.SAMLCertPath, KeyPath: cfg.Auth.SAMLKeyPath},
		Google:          auth.OAuthEnv{ClientID: cfg.Auth.GoogleClientID, ClientSecret: cfg.Auth.GoogleClientSecret},
		Microsoft:       auth.OAuthEnv{ClientID: cfg.Auth.MSClientID, ClientSecret: cfg.Auth.MSClientSecret},
		MaxActiveTokens: cfg.MaxActiveTokens,
		BaseURL:         cfg.BaseURL, CookieSecure: cfg.CookieSecure, DevIDP: cfg.DevIDP,
		OIDC: auth.OIDCConfig{Issuer: cfg.OIDCIssuer, ClientID: cfg.OIDCClientID, ClientSecret: cfg.OIDCClientSecret, RedirectURL: cfg.OIDCRedirectURL},
	})
	if err != nil {
		return err
	}
	// 설정 검증(auth.New)을 통과한 뒤에만 시드한다.
	if cfg.DevSeedPassword != "" { // 정책에 맞지 않는 값은 기동 거부(값 자체는 메시지에 넣지 않는다)
		if err := auth.DefaultPasswordPolicy().Check(cfg.DevSeedPassword, "", ""); err != nil {
			return fmt.Errorf("PL_DEV_SEED_PASSWORD 가 비밀번호 정책에 맞지 않습니다: %v", err)
		}
	}
	// 기본 역할 템플릿 5개의 권한을 코드와 맞춘다(B33, 멱등). 사용자 직접 부여의 실효 권한 캐시는 트리거가 같은 트랜잭션에서 맞춘다.
	if sr, err := access.SyncTemplates(context.Background(), d.DB); err != nil {
		return fmt.Errorf("역할 템플릿 동기화: %w", err)
	} else if sr.Added+sr.Removed+len(sr.Unknown) > 0 {
		log.Printf("access: 기본 역할 템플릿을 코드에 맞췄습니다(추가 %d, 제거 %d, 코드에 없는 권한 제거 %v)", sr.Added, sr.Removed, sr.Unknown)
	}
	if cfg.DevIDP || cfg.DevSeedPassword != "" {
		if err := auth.SeedDevUsers(context.Background(), d.DB); err != nil {
			return fmt.Errorf("dev 시드: %w", err)
		}
	}
	d.Authenticator, d.Tokens, d.Users, d.AuthHTTP = svc, svc, svc.Users(), svc
	accessSvc := access.New(d.DB, d.Txs, d.Audit, nil)
	d.Access = accessSvc
	d.TokenLookup = svc
	orgSvc, err := org.New(org.Options{DB: d.DB, Txs: d.Txs, Audit: d.Audit, Masker: d.Masker})
	if err != nil {
		return err
	}
	d.Org = orgSvc
	d.Entities = entity.New(entity.Options{DB: d.DB, Txs: d.Txs, Audit: d.Audit, Org: orgSvc}) // 법인·사업장 관리(B44)
	d.Modules = module.New(module.Options{DB: d.DB, Txs: d.Txs, Audit: d.Audit})               // 업무 모듈 켜기·끄기(B50)
	svc.SetModules(d.Modules.EnabledKeys)                                                      // /api/me 가 켜진 모듈을 알린다(B50)
	privSvc, err := privacy.New(privacy.Options{DB: d.DB, Txs: d.Txs, Audit: d.Audit})
	if err != nil {
		return err
	}
	d.Privacy = privSvc
	svc.SetPrivacy(privSvc)    // 로그인 설정 사용자 목록의 이메일 원문 열람 기록(B41)
	orgSvc.SetPrivacy(privSvc) // 조직도 사람 카드·목록의 연락처 원문 열람 기록(B41). org 서비스를 기록기보다 먼저 만드는 조립 순서라 나중에 꽂는다
	d.Export = export.NewEnv(d.Txs, d.Audit, cfg.ExportMaxRows)
	d.Export.Privacy = privSvc
	nopts := notify.Options{DB: d.DB, Txs: d.Txs, Audit: d.Audit, BaseURL: cfg.BaseURL, AllowLocalWebhook: cfg.NotifyInsecureLocal}
	if key, kerr := acctsecret.DecodeKey(cfg.AccountSecretKey); kerr == nil && key != nil { // 웹훅 주소 봉인(D5). 키 형식 오류는 WireERP 가 기동을 거부한다
		if c, cerr := acctsecret.New(key); cerr == nil {
			nopts.Sealer = c
		}
	}
	if mailer != nil { // 메일이 설정되지 않으면 메일 채널은 건너뜀으로 처리된다(B36 E1)
		nopts.Mailer = mailer
	}
	d.Notify = notify.New(nopts)
	privSvc.SetDeliver(d.Notify.Enqueue) // 열람 요청 알림의 메일·웹훅 발송 건(B41)
	if cfg.APIMaxActiveTokens < 0 || cfg.APIMaxConcurrent < 0 || cfg.APIRatePerMin < 0 {
		return fmt.Errorf("PL_API_MAX_ACTIVE_TOKENS·PL_API_MAX_CONCURRENT·PL_API_RATE_PER_MIN 은 0 이상의 정수여야 합니다")
	}
	apiTok, err := apitoken.New(apitoken.Options{DB: d.DB, Txs: d.Txs, Audit: d.Audit, MaxActive: cfg.APIMaxActiveTokens, MaxConcurrent: cfg.APIMaxConcurrent, RatePerMin: cfg.APIRatePerMin,
		AllowCIDR: cfg.APIAllowCIDR, Disabled: cfg.Rehearsal})
	if err != nil {
		return err
	}
	d.APITokens = apiTok
	whOpts := webhooks.Options{DB: d.DB, Txs: d.Txs, Audit: d.Audit, ManagePerm: contract.PermWebhookManage, BaseURL: cfg.BaseURL, AllowLocal: cfg.NotifyInsecureLocal, Deliver: d.Notify.Enqueue}
	if key, kerr := acctsecret.DecodeKey(cfg.AccountSecretKey); kerr == nil && key != nil { // 구독 주소·서명 비밀 봉인. 키가 없으면 등록을 거부한다
		if c, cerr := acctsecret.New(key); cerr == nil {
			whOpts.Sealer = c
		}
	}
	d.Webhooks = webhooks.New(whOpts)
	if cfg.BaseURL == "" {
		log.Printf("webhooks: PL_BASE_URL 이 비어 있어 변경 웹훅 발송 건이 모두 실패합니다(본문의 api_url 을 만들 수 없음). 접속 주소를 설정하세요")
	}
	sodEng, err := sod.New(sod.Options{DB: d.DB, Txs: d.Txs, Audit: d.Audit, Deliver: d.Notify.Enqueue})
	if err != nil {
		return err
	}
	d.SoD = sodEng
	rep, err := privreport.New(privreport.Options{DB: d.DB, Txs: d.Txs, Audit: d.Audit, Priv: privSvc})
	if err != nil {
		return err
	}
	rep.SetDeliver(d.Notify.Enqueue)
	d.PrivReport = rep
	accessSvc.SetSoD(sodEng) // 역할 부여·역할 편집의 hold 규칙·자기 부여·위험 역할 승인 판정(B41)
	d.Onboarding = onboarding.New(onboarding.Options{DB: d.DB, Txs: d.Txs, Audit: d.Audit, Env: onboarding.Env{
		HTTPS: strings.HasPrefix(strings.ToLower(cfg.BaseURL), "https://"), CookieSecure: cfg.CookieSecure,
		AnchorConfigured: d.AuditAnchor != nil, AccountKey: cfg.AccountSecretKey != "", AuthKey: cfg.Auth.SecretKey != "", MailConfigured: d.Notify.MailConfigured(), BaseURLSet: cfg.BaseURL != "", DevIDP: cfg.DevIDP}})
	// SCIM 계정 동기화(B40). 서버만 라우트·정리 작업을 쓴다. 복구 리허설 모드에서는 모든 SCIM 요청을 거부한다.
	if cfg.SCIMMaxActiveTokens < 0 || cfg.SCIMRatePerMin < 0 || cfg.SCIMLogKeepDays < 0 {
		return fmt.Errorf("PL_SCIM_MAX_ACTIVE_TOKENS·PL_SCIM_RATE_PER_MIN·PL_SCIM_LOG_KEEP_DAYS 는 0 이상의 정수여야 합니다")
	}
	scimSvc, err := scim.New(scim.Options{DB: d.DB, Txs: d.Txs, Audit: d.Audit, Org: orgSvc, Access: accessSvc, BaseURL: cfg.BaseURL,
		MaxActiveTokens: cfg.SCIMMaxActiveTokens, RatePerMin: cfg.SCIMRatePerMin, LogKeepDays: cfg.SCIMLogKeepDays, AllowCIDR: cfg.SCIMAllowCIDR, Disabled: cfg.Rehearsal})
	if err != nil {
		return err
	}
	d.SCIM = scimSvc
	d.Import = importflow.New(importflow.Options{DB: d.DB, Txs: d.Txs, Audit: d.Audit}) // 대상별 반영은 WireERP 가 RegisterTarget 으로 붙인다(B35)
	svc.SetUnitLookup(func(ctx context.Context, userID int64) string {
		person, err := orgSvc.Person(ctx, userID, "")
		if err != nil {
			return ""
		}
		return person.Primary.Unit.Name
	})
	d.Settings = svc.Settings()
	return nil
}

// wireAnchor 는 감사 체인 외부 앵커(B4)를 켠다. 키가 없으면 꺼진 채로 둔다(d.AuditAnchor == nil, 검증 결과가
// "외부 대조 없음"을 알린다). 키가 있는데 경로가 없거나 파일을 쓸 수 없으면 기동을 거부한다.
func wireAnchor(cfg config.Config, store *audit.Store, d *deps.Deps) error {
	if cfg.Rehearsal { // 리허설은 운영 앵커 파일을 읽지도 쓰지도 않는다(복구본의 체인 검증은 erp-admin 이 따로 한다)
		return nil
	}
	if cfg.AuditAnchorKey == "" {
		if cfg.AuditAnchorPath != "" {
			log.Printf("경고: PL_AUDIT_ANCHOR_PATH 가 있지만 PL_AUDIT_ANCHOR_KEY 가 없어 감사 로그 외부 앵커가 꺼져 있습니다(경로만으로는 켜지지 않습니다). 마지막 기록 삭제와 체인 재계산을 잡을 수 없습니다")
		} else {
			log.Printf("경고: 감사 로그 외부 앵커가 꺼져 있습니다(PL_AUDIT_ANCHOR_KEY·PL_AUDIT_ANCHOR_PATH 없음). 마지막 기록 삭제와 체인 재계산을 잡을 수 없습니다")
		}
		return nil
	}
	if cfg.AuditAnchorInterval < 0 {
		return fmt.Errorf("PL_AUDIT_ANCHOR_INTERVAL 형식이 올바르지 않습니다(예: 5m, 0 은 주기 기록 없음)")
	}
	a, err := audit.NewAnchor(store, cfg.AuditAnchorPath, []byte(cfg.AuditAnchorKey))
	if err != nil {
		return fmt.Errorf("감사 외부 앵커(PL_AUDIT_ANCHOR_KEY·PL_AUDIT_ANCHOR_PATH): %w", err)
	}
	d.AuditAnchor = a
	return nil
}

// authFromConfig 는 B30 환경변수에서 메일 발송기와 해시 비용을 만든다. 메일이 설정되지 않으면 mailer 는 nil(재설정 링크 없음, 관리자 임시 비밀번호만).
// TLS 없는 SMTP(PL_SMTP_INSECURE=1)는 PL_BASE_URL 이 로컬일 때만 허용한다(그 밖이면 기동 실패).
func authFromConfig(cfg config.Config) (auth.Mailer, auth.ArgonParams, []byte, error) {
	a := cfg.Auth
	if cfg.Rehearsal { // 리허설은 메일을 보내지 않는다(SMTP 설정이 환경에 남아 있어도 발송기를 만들지 않는다)
		a.SMTPHost = ""
	}
	key, err := auth.DecodeSecretKey(a.SecretKey)
	if err != nil {
		return nil, auth.ArgonParams{}, nil, err
	}
	if a.ArgonMemKiB < 0 || a.ArgonTime < 0 || a.ArgonPar < 0 || a.ArgonPar > 255 {
		return nil, auth.ArgonParams{}, nil, fmt.Errorf("PL_AUTH_PASSWORD_ARGON_* 는 0 이상의 정수여야 합니다(0 은 기본값)")
	}
	argon := auth.ArgonParams{MemoryKiB: uint32(a.ArgonMemKiB), Iterations: uint32(a.ArgonTime), Parallelism: uint8(a.ArgonPar)}
	if a.SMTPHost == "" {
		return nil, argon, key, nil
	}
	if a.SMTPInsecure && !auth.IsLoopbackURL(cfg.BaseURL) {
		return nil, argon, nil, fmt.Errorf("PL_SMTP_INSECURE=1 은 로컬 PL_BASE_URL(localhost 등)에서만 쓸 수 있습니다")
	}
	m, err := auth.NewSMTPMailer(auth.SMTPConfig{Host: a.SMTPHost, Port: a.SMTPPort, User: a.SMTPUser, Password: a.SMTPPassword, From: a.SMTPFrom, Insecure: a.SMTPInsecure})
	if err != nil {
		return nil, argon, nil, err
	}
	return m, argon, key, nil
}
