package auth

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
)

// OAuthEnv 는 구글·MS 클라이언트 자격이다(환경변수). 비밀은 DB·화면·감사·로그 어디에도 나오지 않는다.
type OAuthEnv struct{ ClientID, ClientSecret string }

// 추가 거부 사유 코드.
const (
	ReasonEmailUnverified = "email_unverified"
	ReasonDomainMismatch  = "domain_mismatch"
	ReasonTenantMismatch  = "tenant_mismatch"
	ReasonNoSubject       = "no_subject"
)

// providerConfig 는 방식별 비밀이 아닌 설정이다(DB auth_providers.config_json).
type providerConfig struct {
	AllowedDomains []string `json:"allowedDomains,omitempty"` // 구글: 허용 도메인(hd)
	TenantID       string   `json:"tenantId,omitempty"`       // MS: 단일 테넌트 ID
	DisplayName    string   `json:"displayName,omitempty"`    // 로그인 화면 버튼 이름(SAML 은 "회사 SSO" 등)
	// SAML(단계 5)
	IdPMetadata string `json:"idpMetadata,omitempty"` // IdP 메타데이터 XML(공개 정보)
}

// providerState 는 한 방식의 저장된 상태다.
type providerState struct {
	Provider  string
	Enabled   bool
	TestedAt  *time.Time
	Config    providerConfig
	UpdatedAt time.Time
}

var guidRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

var domainRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$`)

func isOAuthProvider(p string) bool { return p == MethodGoogle || p == MethodMicrosoft }

func (s *Service) loadProvider(ctx context.Context, q queryer, provider string) (providerState, error) {
	st := providerState{Provider: provider}
	var enabled int
	var cfgJSON string
	var tested sql.NullString
	var updated string
	err := q.QueryRowContext(ctx, `SELECT enabled, config_json, tested_at, updated_at FROM auth_providers WHERE provider = ?`, provider).Scan(&enabled, &cfgJSON, &tested, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return st, nil
	}
	if err != nil {
		return st, err
	}
	st.Enabled, st.TestedAt, st.UpdatedAt = enabled == 1, parseTimePtr(tested), parseTime(updated)
	if err := json.Unmarshal([]byte(cfgJSON), &st.Config); err != nil {
		st.Config = providerConfig{} // 깨진 설정은 비운다(fail-closed: 필수 값이 없어 켜지지 않는다)
		st.Enabled = false
	}
	return st, nil
}

// envFor 는 방식의 환경변수 자격이다.
func (s *Service) envFor(provider string) OAuthEnv {
	switch provider {
	case MethodGoogle:
		return s.google
	case MethodMicrosoft:
		return s.microsoft
	}
	return OAuthEnv{}
}

// envReady 는 필요한 환경변수가 설정됐는지다(비밀값 자체는 어디에도 노출하지 않는다).
func (s *Service) envReady(provider string) bool {
	switch provider {
	case MethodGoogle, MethodMicrosoft:
		e := s.envFor(provider)
		return e.ClientID != "" && e.ClientSecret != ""
	case MethodSAML:
		return s.samlEnvReady()
	}
	return false
}

// configProblem 는 필수 설정이 빠졌을 때 사유를 돌려준다(빈 문자열이면 설정 완료). 켜기·연결 시험 전에 쓴다.
func configProblem(provider string, c providerConfig) string {
	switch provider {
	case MethodGoogle:
		if len(c.AllowedDomains) == 0 {
			return "허용 도메인을 하나 이상 입력해야 합니다"
		}
	case MethodMicrosoft:
		if !guidRe.MatchString(c.TenantID) {
			return "테넌트 ID(GUID)를 입력해야 합니다. common·organizations 는 쓸 수 없습니다"
		}
	case MethodSAML:
		if strings.TrimSpace(c.IdPMetadata) == "" {
			return "IdP 메타데이터를 등록해야 합니다"
		}
	}
	return ""
}

// normalizeDomains 는 허용 도메인 목록을 정리·검증한다.
func normalizeDomains(in []string) ([]string, error) {
	seen := map[string]bool{}
	out := []string{}
	for _, d := range in {
		d = strings.ToLower(strings.TrimSpace(d))
		if d == "" {
			continue
		}
		if !domainRe.MatchString(d) {
			return nil, fmt.Errorf("%w: 도메인 형식이 올바르지 않습니다: %q", contract.ErrValidation, d)
		}
		if !seen[d] {
			seen[d] = true
			out = append(out, d)
		}
	}
	return out, nil
}

// SetIssuerForTest 는 시험이 구글·MS 발급자를 모의 IdP 로 돌리는 자리다(통합 시험 전용. 운영 코드는 부르지 않는다).
func (s *Service) SetIssuerForTest(provider, issuer string) {
	s.clientMu.Lock()
	defer s.clientMu.Unlock()
	s.issuerOverride[provider] = issuer
}

// providerIssuer 는 방식의 OIDC 발급자다. 시험은 s.issuerOverride 로 모의 IdP 를 가리킨다.
func (s *Service) providerIssuer(provider string, c providerConfig) string {
	if v, ok := s.issuerOverride[provider]; ok {
		return v
	}
	switch provider {
	case MethodGoogle:
		return "https://accounts.google.com"
	case MethodMicrosoft:
		return "https://login.microsoftonline.com/" + strings.ToLower(c.TenantID) + "/v2.0" // 단일 테넌트 고정
	}
	return ""
}

// clientFor 는 방식의 OIDC 클라이언트를 돌려준다. 범용(oidc·dev)은 기동 때 만든 클라이언트이고, 구글·MS 는 저장된 설정으로
// 만든다. 로그인 목적이면 켜진 방식만 허용한다(꺼진 방식은 ErrLoginNotConfigured). 시험 목적은 설정이 완료됐으면 켜기 전에도 허용한다.
func (s *Service) clientFor(ctx context.Context, method, purpose string) (*oidcClient, providerConfig, error) {
	if !isOAuthProvider(method) {
		if s.oidc == nil {
			return nil, providerConfig{}, ErrLoginNotConfigured
		}
		return s.oidc, providerConfig{}, nil
	}
	st, err := s.loadProvider(ctx, s.db, method)
	if err != nil {
		return nil, providerConfig{}, err
	}
	if purpose != purposeTest && !st.Enabled {
		return nil, providerConfig{}, ErrLoginNotConfigured
	}
	if !s.envReady(method) || configProblem(method, st.Config) != "" {
		return nil, providerConfig{}, ErrLoginNotConfigured
	}
	issuer := s.providerIssuer(method, st.Config)
	env := s.envFor(method)
	key := method + "|" + issuer + "|" + env.ClientID
	s.clientMu.Lock()
	defer s.clientMu.Unlock()
	if c, ok := s.clients[key]; ok {
		return c, st.Config, nil
	}
	c := newOIDCClient(OIDCConfig{Issuer: issuer, ClientID: env.ClientID, ClientSecret: env.ClientSecret, RedirectURL: s.base + "/api/auth/callback"})
	s.clients[key] = c
	return c, st.Config, nil
}

// loginProvider 는 로그인 화면이 그릴 방식 하나다.
type loginProvider struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Kind  string `json:"kind"`
}

// loginProviders 는 로그인 화면에 보일 방식(켜지고 설정이 완료된 것)만 돌려준다. 켜지지 않은 방식은 코드에 있어도 보이지 않는다.
func (s *Service) loginProviders(ctx context.Context) []loginProvider {
	out := []loginProvider{}
	if s.oidc != nil {
		label := "사내 계정으로 로그인"
		out = append(out, loginProvider{ID: MethodOIDC, Label: label, Kind: "oidc"})
	}
	for _, m := range []string{MethodGoogle, MethodMicrosoft, MethodSAML} {
		st, err := s.loadProvider(ctx, s.db, m)
		if err != nil || !st.Enabled || !s.envReady(m) || configProblem(m, st.Config) != "" {
			continue
		}
		label := st.Config.DisplayName
		switch {
		case m == MethodGoogle && label == "":
			label = "Google 로 로그인"
		case m == MethodMicrosoft && label == "":
			label = "Microsoft 로 로그인"
		case m == MethodSAML && label == "":
			label = "회사 SSO"
		}
		out = append(out, loginProvider{ID: m, Label: label, Kind: m})
	}
	return out
}

// ---- 연결 시험 ----

var errTestMismatch = errors.New("시험한 관리자 본인의 계정이 아닙니다")

// TestResult 는 연결 시험의 마지막 결과다(비밀 없음, 클레임은 가린다).
type TestResult struct {
	Provider  string            `json:"provider"`
	OK        bool              `json:"ok"`
	Summary   map[string]string `json:"summary"`
	CreatedAt time.Time         `json:"createdAt"`
}

// finishTest 는 외부 로그인 시험을 마무리한다: 세션은 만들지 않는다. 시험한 관리자 본인 계정으로 판정될 때만 성공이고
// (다른 계정이면 연결도 일어나지 않게 되돌린다), 성공하면 방식의 tested_at 을 갱신해 켤 수 있게 한다.
func (s *Service) finishTest(ctx context.Context, res oidcResult) error {
	ok := res.Denied == nil
	summary := res.Summary
	reason := ""
	if res.Denied != nil {
		reason = denialReason(res.Denied)
	}
	if ok {
		err := s.txs.WithTx(ctx, func(tx contract.Tx) error {
			u, err := s.resolveAccount(ctx, tx, res.Identity)
			if err != nil {
				return err
			}
			if u.UserID != res.TestUser {
				return errTestMismatch
			}
			return nil
		})
		switch {
		case err == nil:
		case errors.Is(err, errTestMismatch):
			ok, reason = false, "not_self"
		case errors.Is(err, contract.ErrForbidden):
			ok, reason = false, denialReason(err)
		default:
			return err
		}
	}
	if summary == nil {
		summary = map[string]string{}
	}
	if !ok {
		summary = map[string]string{"결과": "거부", "사유": reason}
	}
	sj, _ := json.Marshal(summary)
	now := s.now().UTC().Format(timeLayout)
	return s.txs.WithTx(ctx, func(tx contract.Tx) error {
		a, err := loadAccount(ctx, tx, `id = ?`, res.TestUser)
		if err != nil {
			return err
		}
		okInt := 0
		if ok {
			okInt = 1
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO auth_test_results (provider, user_id, ok, summary_json, created_at) VALUES (?,?,?,?,?)`,
			res.Identity.Provider, res.TestUser, okInt, string(sj), now); err != nil {
			return err
		}
		if ok {
			if _, err := tx.ExecContext(ctx, `UPDATE auth_providers SET tested_at = ? WHERE provider = ?`, now, res.Identity.Provider); err != nil {
				return err
			}
		}
		return s.record(ctx, tx, a.p, "auth.provider_test", audit.TargetAuthProvider, res.Identity.Provider, audit.ResultSuccess,
			[]audit.DiffItem{{Key: "auth_provider", New: res.Identity.Provider}, {Key: "ok", New: fmt.Sprint(ok)}}, "")
	})
}

// LastTestResult 는 이 관리자가 한 방식에 대해 가장 최근에 한 시험 결과다.
func (s *Service) LastTestResult(ctx context.Context, p contract.Principal, provider string) (*TestResult, error) {
	if _, err := contract.Authorize(contract.WithPrincipal(ctx, p), contract.PermAuthManage); err != nil {
		return nil, err
	}
	var r TestResult
	var ok int
	var sj, at string
	err := s.db.QueryRowContext(ctx, `SELECT provider, ok, summary_json, created_at FROM auth_test_results WHERE user_id = ? AND provider = ? ORDER BY id DESC LIMIT 1`, p.UserID, provider).
		Scan(&r.Provider, &ok, &sj, &at)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	r.OK, r.CreatedAt = ok == 1, parseTime(at)
	_ = json.Unmarshal([]byte(sj), &r.Summary)
	return &r, nil
}
