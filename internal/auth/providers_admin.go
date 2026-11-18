package auth

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/choibongseok/procure-ledger/internal/access"
	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
)

// ProviderView 는 설정 화면의 방식 카드 하나다. 비밀값은 없고 환경변수가 설정됐는지(envReady)만 알린다.
type ProviderView struct {
	Provider       string     `json:"provider"`
	Enabled        bool       `json:"enabled"`
	EnvReady       bool       `json:"envReady"`       // 필요한 환경변수가 설정됐는지(값은 노출하지 않는다)
	ConfigProblem  string     `json:"configProblem"`  // 비어 있으면 필수 설정 완료
	TestedAt       *time.Time `json:"testedAt"`       // 마지막 연결 시험 성공(설정을 바꾸면 지워진다)
	AllowedDomains []string   `json:"allowedDomains"` // 구글
	TenantID       string     `json:"tenantId"`       // MS
	DisplayName    string     `json:"displayName"`
	// 읽기 전용 값(IdP 쪽에 등록한다)
	RedirectURI string `json:"redirectUri,omitempty"`
	ACSURL      string `json:"acsUrl,omitempty"`
	EnvProblem  string `json:"envProblem,omitempty"` // SAML SP 인증서·키를 쓸 수 없는 이유(경로·권한 문제, 비밀 아님)
	EntityID    string `json:"entityId,omitempty"`
	// SAML
	IdPEntityID         string     `json:"idpEntityId,omitempty"`
	IdPCertFingerprints []string   `json:"idpCertFingerprints,omitempty"`
	IdPCertNotAfter     *time.Time `json:"idpCertNotAfter,omitempty"`
	LinkedUsers         int        `json:"linkedUsers"`
}

// ProviderInput 은 방식 설정 변경이다. nil 인 항목은 바꾸지 않는다.
type ProviderInput struct {
	Enabled        *bool     `json:"enabled"`
	AllowedDomains *[]string `json:"allowedDomains"`
	TenantID       *string   `json:"tenantId"`
	DisplayName    *string   `json:"displayName"`
	IdPMetadata    *string   `json:"idpMetadata"`
}

func knownProvider(p string) bool {
	return p == MethodGoogle || p == MethodMicrosoft || p == MethodSAML
}

func (s *Service) viewOf(ctx context.Context, q queryer, st providerState) (ProviderView, error) {
	v := ProviderView{Provider: st.Provider, Enabled: st.Enabled, EnvReady: s.envReady(st.Provider), ConfigProblem: configProblem(st.Provider, st.Config),
		TestedAt: st.TestedAt, AllowedDomains: st.Config.AllowedDomains, TenantID: st.Config.TenantID, DisplayName: st.Config.DisplayName}
	if v.AllowedDomains == nil {
		v.AllowedDomains = []string{}
	}
	switch st.Provider {
	case MethodGoogle, MethodMicrosoft:
		v.RedirectURI = s.base + "/api/auth/callback"
	case MethodSAML:
		s.fillSAMLView(&v, st)
	}
	if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM user_identities WHERE provider = ?`, st.Provider).Scan(&v.LinkedUsers); err != nil {
		return v, err
	}
	return v, nil
}

// ListProviders 는 방식 카드 3장의 상태다(auth:manage).
func (s *Service) ListProviders(ctx context.Context, p contract.Principal) ([]ProviderView, error) {
	if _, err := contract.Authorize(contract.WithPrincipal(ctx, p), contract.PermAuthManage); err != nil {
		return nil, err
	}
	out := []ProviderView{}
	for _, m := range []string{MethodGoogle, MethodMicrosoft, MethodSAML} {
		st, err := s.loadProvider(ctx, s.db, m)
		if err != nil {
			return nil, err
		}
		v, err := s.viewOf(ctx, s.db, st)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

// UpdateProvider 는 방식 설정을 바꾼다(auth:manage, 세션 전용 라우트). 규칙
//   - 켜져 있는 방식의 허용 도메인·테넌트·메타데이터는 바꿀 수 없다(먼저 끈다). 바뀌면 시험 성공 기록이 지워진다.
//   - 켜려면 환경변수·필수 설정이 완료되고 연결 시험에 성공한 상태여야 한다.
//   - 끄는 변경은 11.4 보호 검사(외부 로그인만 허용 중이면 관리자가 잠기지 않는지)를 통과해야 한다.
func (s *Service) UpdateProvider(ctx context.Context, p contract.Principal, provider string, in ProviderInput) (ProviderView, error) {
	if _, err := contract.Authorize(contract.WithPrincipal(ctx, p), contract.PermAuthManage); err != nil {
		return ProviderView{}, err
	}
	if !knownProvider(provider) {
		return ProviderView{}, contract.ErrNotFound
	}
	var out ProviderView
	err := s.txs.WithTx(ctx, func(tx contract.Tx) error {
		st, err := s.loadProvider(ctx, tx, provider)
		if err != nil {
			return err
		}
		before := st
		var diff []audit.DiffItem
		cfgChanged := false
		if in.AllowedDomains != nil {
			if provider != MethodGoogle {
				return fmt.Errorf("%w: 허용 도메인은 구글에만 있습니다", contract.ErrValidation)
			}
			ds, err := normalizeDomains(*in.AllowedDomains)
			if err != nil {
				return err
			}
			if strings.Join(ds, ",") != strings.Join(st.Config.AllowedDomains, ",") {
				diff = append(diff, audit.DiffItem{Key: "allowed_domains", Old: strings.Join(st.Config.AllowedDomains, ","), New: strings.Join(ds, ",")})
				st.Config.AllowedDomains, cfgChanged = ds, true
			}
		}
		if in.TenantID != nil {
			if provider != MethodMicrosoft {
				return fmt.Errorf("%w: 테넌트 ID 는 마이크로소프트에만 있습니다", contract.ErrValidation)
			}
			t := strings.ToLower(strings.TrimSpace(*in.TenantID))
			if t != "" && !guidRe.MatchString(t) {
				return fmt.Errorf("%w: 테넌트 ID 는 GUID 여야 합니다(common·organizations 는 쓸 수 없습니다)", contract.ErrValidation)
			}
			if t != st.Config.TenantID {
				diff = append(diff, audit.DiffItem{Key: "tenant_id", Old: st.Config.TenantID, New: t})
				st.Config.TenantID, cfgChanged = t, true
			}
		}
		if in.IdPMetadata != nil {
			if provider != MethodSAML {
				return fmt.Errorf("%w: IdP 메타데이터는 SAML 에만 있습니다", contract.ErrValidation)
			}
			d, err := s.applyIdPMetadata(&st, *in.IdPMetadata)
			if err != nil {
				return err
			}
			diff = append(diff, d...)
			cfgChanged = true
		}
		if in.DisplayName != nil {
			n := strings.TrimSpace(*in.DisplayName)
			if len([]rune(n)) > 40 || strings.ContainsAny(n, "\r\n<>") {
				return fmt.Errorf("%w: 표시 이름은 40자 이하의 글자여야 합니다", contract.ErrValidation)
			}
			if n != st.Config.DisplayName {
				diff = append(diff, audit.DiffItem{Key: "display_name", Old: st.Config.DisplayName, New: n})
				st.Config.DisplayName = n
			}
		}
		if cfgChanged {
			if before.Enabled && (in.Enabled == nil || *in.Enabled) {
				return fmt.Errorf("%w: 켜져 있는 방식의 설정은 바꿀 수 없습니다. 먼저 끄고 바꾼 뒤 연결 시험을 다시 하세요", contract.ErrInvalidState)
			}
			st.TestedAt = nil // 설정이 바뀌면 시험 성공 기록은 무효
		}
		if in.Enabled != nil && *in.Enabled != st.Enabled {
			if *in.Enabled {
				switch {
				case !s.envReady(provider):
					return fmt.Errorf("%w: 필요한 환경변수가 설정되지 않았습니다(서버 환경변수를 확인하세요)", contract.ErrInvalidState)
				case configProblem(provider, st.Config) != "":
					return fmt.Errorf("%w: %s", contract.ErrInvalidState, configProblem(provider, st.Config))
				case st.TestedAt == nil:
					return fmt.Errorf("%w: 연결 시험에 성공해야 켤 수 있습니다", contract.ErrInvalidState)
				}
			}
			diff = append(diff, audit.DiffItem{Key: "enabled", Old: fmt.Sprint(st.Enabled), New: fmt.Sprint(*in.Enabled)})
			st.Enabled = *in.Enabled
		}
		if len(diff) == 0 && cfgChanged == false {
			v, err := s.viewOf(ctx, tx, st)
			out = v
			return err
		}
		cj, _ := json.Marshal(st.Config)
		var tested any
		if st.TestedAt != nil {
			tested = st.TestedAt.UTC().Format(timeLayout)
		}
		en := 0
		if st.Enabled {
			en = 1
		}
		if _, err := tx.ExecContext(ctx, `UPDATE auth_providers SET enabled = ?, config_json = ?, tested_at = ?, updated_by = ?, updated_at = ? WHERE provider = ?`,
			en, string(cj), tested, p.UserID, s.now().UTC().Format(timeLayout), provider); err != nil {
			return err
		}
		// 끄거나 설정이 바뀌어 외부 로그인만 허용하는 구성이 깨지면 되돌린다(관리자 잠금 방지).
		if err := s.guardLockout(ctx, tx); err != nil {
			return err
		}
		if before.Enabled && !st.Enabled { // 끄면 그 방식의 세션은 끊는다
			if _, err := tx.ExecContext(ctx, `UPDATE sessions SET revoked_at = ? WHERE auth_method = ? AND revoked_at IS NULL`, s.now().UTC().Format(timeLayout), provider); err != nil {
				return err
			}
		}
		diff = append([]audit.DiffItem{{Key: "auth_provider", New: provider}}, diff...)
		if err := s.record(ctx, tx, p, "auth.provider_update", audit.TargetAuthProvider, provider, audit.ResultSuccess, diff, ""); err != nil {
			return err
		}
		v, err := s.viewOf(ctx, tx, st)
		out = v
		return err
	})
	return out, err
}

// StartProviderTest 는 연결 시험을 시작한다: 해당 방식의 로그인을 새 창에서 시작해 결과(가린 클레임)를 보여 주되
// 세션은 만들지 않는다. 시험한 관리자 본인 계정으로만 성공한다. 돌려주는 값은 IdP 로 보낼 주소다.
func (s *Service) StartProviderTest(ctx context.Context, p contract.Principal, provider string) (string, error) {
	if _, err := contract.Authorize(contract.WithPrincipal(ctx, p), contract.PermAuthManage); err != nil {
		return "", err
	}
	if !knownProvider(provider) {
		return "", contract.ErrNotFound
	}
	to := "/admin/auth?tested=" + provider
	if provider == MethodSAML {
		return s.beginSAML(ctx, to, purposeTest, p.UserID)
	}
	u, err := s.BeginProviderLogin(ctx, provider, to, "", purposeTest, p.UserID)
	if err == ErrLoginNotConfigured {
		return "", fmt.Errorf("%w: 환경변수와 필수 설정을 마친 뒤에 시험할 수 있습니다", contract.ErrInvalidState)
	}
	return u, err
}

// guardLockout 은 이메일 로그인이 꺼진(외부 로그인만 허용) 구성에서 관리자가 잠기지 않는지 검사한다(명세 11.4).
// 이메일 로그인이 켜져 있으면 통과다. 방식 끄기·설정 변경·연결 해제·이메일 로그인 끄기 모두 같은 검사를 거친다.
func (s *Service) guardLockout(ctx context.Context, tx contract.Tx) error {
	pol, err := loadPolicy(ctx, tx)
	if err != nil {
		return err
	}
	if pol.EmailLoginEnabled {
		return nil
	}
	enabled := []string{}
	for _, m := range []string{MethodGoogle, MethodMicrosoft, MethodSAML} {
		st, err := s.loadProvider(ctx, tx, m)
		if err != nil {
			return err
		}
		if st.Enabled && st.TestedAt != nil {
			enabled = append(enabled, m)
		}
	}
	if len(enabled) == 0 {
		return fmt.Errorf("%w: 이메일 로그인을 끄려면 연결 시험에 성공한 외부 로그인 방식이 하나 이상 켜져 있어야 합니다", contract.ErrInvalidState)
	}
	var linkedAdmins int
	args := make([]any, 0, len(enabled))
	ph := make([]string, 0, len(enabled))
	for _, m := range enabled {
		args = append(args, m)
		ph = append(ph, "?")
	}
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(DISTINCT u.id) FROM users u JOIN user_identities i ON i.user_id = u.id
		WHERE u.active = 1 AND EXISTS (SELECT 1 FROM user_effective_permissions e WHERE e.user_id = u.id AND e.permission = 'auth:manage') AND i.provider IN (`+strings.Join(ph, ",")+`)`, args...).Scan(&linkedAdmins); err != nil {
		return err
	}
	if linkedAdmins == 0 {
		return fmt.Errorf("%w: 켜진 외부 로그인 방식으로 연결된 활성 관리자가 한 명도 없습니다. 관리자 계정이 먼저 그 방식으로 로그인해 연결돼야 합니다", contract.ErrInvalidState)
	}
	if n := len(pol.EmergencyAdmins); n < 1 || n > 3 {
		return fmt.Errorf("%w: 비상 관리자(1~3명)를 지정해야 합니다", contract.ErrInvalidState)
	}
	for _, id := range pol.EmergencyAdmins {
		var active int
		if err := tx.QueryRowContext(ctx, `SELECT active FROM users WHERE id = ?`, id).Scan(&active); err != nil {
			if err == sql.ErrNoRows {
				return fmt.Errorf("%w: 비상 관리자 계정이 없습니다", contract.ErrInvalidState)
			}
			return err
		}
		if ok, err := access.HasPerm(ctx, tx, id, contract.PermAuthManage); err != nil {
			return err
		} else if !ok || active != 1 {
			return fmt.Errorf("%w: 비상 관리자는 로그인 설정 권한(auth:manage)을 가진 활성 사용자여야 합니다", contract.ErrInvalidState)
		}
		if !s.totpConfirmed(ctx, tx, id) {
			return fmt.Errorf("%w: 비상 관리자는 2단계 인증 등록을 마쳐야 합니다", contract.ErrInvalidState)
		}
	}
	return nil
}
