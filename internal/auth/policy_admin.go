package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/choibongseok/procure-ledger/internal/access"
	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
)

// PolicyInput 은 인증 정책 변경이다. nil 인 항목은 바꾸지 않는다.
type PolicyInput struct {
	Password          *PasswordPolicy `json:"password"`
	EmailLoginEnabled *bool           `json:"emailLoginEnabled"`
	TOTPRequiredPerms *[]string       `json:"totpRequiredPerms"`
	EmergencyAdmins   *[]int64        `json:"emergencyAdmins"`
}

func validPasswordPolicy(p PasswordPolicy) error {
	bad := func(m string) error { return fmt.Errorf("%w: %s", contract.ErrValidation, m) }
	switch {
	case p.MinLength < 8 || p.MinLength > 128:
		return bad("최소 길이는 8~128자여야 합니다")
	case p.MaxLength < p.MinLength || p.MaxLength > 128:
		return bad("최대 길이는 최소 길이 이상 128자 이하여야 합니다")
	case p.RequireClasses < 0 || p.RequireClasses > 4:
		return bad("문자 종류 개수는 0~4 여야 합니다")
	case p.HistoryCount < 0 || p.HistoryCount > historyKeep:
		return bad("재사용 금지 개수는 0~10 이어야 합니다")
	case p.ExpireDays != 0 && p.ExpireDays != 60 && p.ExpireDays != 90 && p.ExpireDays != 180:
		return bad("만료 기간은 끔(0)·60·90·180일 중 하나여야 합니다")
	}
	return nil
}

func idsString(v []int64) string {
	s := make([]string, len(v))
	for i, x := range v {
		s[i] = fmt.Sprint(x)
	}
	return strings.Join(s, ",")
}

// UpdatePolicy 는 비밀번호 정책·2단계 필수 역할·이메일 로그인 켜기/끄기·비상 관리자를 바꾼다(auth:manage, 세션 전용 라우트).
// 이메일 로그인을 끄는 변경(과 그 상태를 깨는 모든 변경)은 guardLockout(11.4)을 통과해야 한다.
func (s *Service) UpdatePolicy(ctx context.Context, p contract.Principal, in PolicyInput) (AuthPolicy, error) {
	if _, err := contract.Authorize(contract.WithPrincipal(ctx, p), contract.PermAuthManage); err != nil {
		return AuthPolicy{}, err
	}
	var out AuthPolicy
	err := s.txs.WithTx(ctx, func(tx contract.Tx) error {
		pol, err := loadPolicy(ctx, tx)
		if err != nil {
			return err
		}
		var diff []audit.DiffItem
		if in.Password != nil {
			if err := validPasswordPolicy(*in.Password); err != nil {
				return err
			}
			if *in.Password != pol.Password {
				o, _ := json.Marshal(pol.Password)
				n, _ := json.Marshal(*in.Password)
				diff = append(diff, audit.DiffItem{Key: "password_policy", Old: string(o), New: string(n)})
				pol.Password = *in.Password
			}
		}
		if in.EmailLoginEnabled != nil && *in.EmailLoginEnabled != pol.EmailLoginEnabled {
			diff = append(diff, audit.DiffItem{Key: "email_login", Old: fmt.Sprint(pol.EmailLoginEnabled), New: fmt.Sprint(*in.EmailLoginEnabled)})
			pol.EmailLoginEnabled = *in.EmailLoginEnabled
		}
		if in.TOTPRequiredPerms != nil {
			perms := []string{}
			seen := map[string]bool{}
			for _, r := range *in.TOTPRequiredPerms {
				known := false
				for _, k := range contract.AllPermissions {
					known = known || string(k) == r
				}
				if !known {
					return fmt.Errorf("%w: 알 수 없는 권한입니다: %q", contract.ErrValidation, r)
				}
				if !seen[r] {
					seen[r] = true
					perms = append(perms, r)
				}
			}
			sort.Strings(perms)
			old := append([]string(nil), pol.TOTPRequiredPerms...)
			sort.Strings(old)
			if strings.Join(perms, ",") != strings.Join(old, ",") {
				diff = append(diff, audit.DiffItem{Key: "totp_required_perms", Old: strings.Join(old, ","), New: strings.Join(perms, ",")})
				pol.TOTPRequiredPerms = perms
			}
		}
		if in.EmergencyAdmins != nil {
			adm := *in.EmergencyAdmins
			if len(adm) > 3 {
				return fmt.Errorf("%w: 비상 관리자는 최대 3명입니다", contract.ErrValidation)
			}
			seen := map[int64]bool{}
			for _, id := range adm {
				var active int
				if seen[id] {
					return fmt.Errorf("%w: 비상 관리자가 중복되었습니다", contract.ErrValidation)
				}
				seen[id] = true
				err := tx.QueryRowContext(ctx, `SELECT active FROM users WHERE id = ?`, id).Scan(&active)
				if ok, perr := access.HasPerm(ctx, tx, id, contract.PermAuthManage); err != nil || perr != nil || !ok || active != 1 {
					return fmt.Errorf("%w: 비상 관리자는 로그인 설정 권한(auth:manage)을 가진 활성 사용자여야 합니다", contract.ErrValidation)
				}
			}
			if idsString(adm) != idsString(pol.EmergencyAdmins) {
				diff = append(diff, audit.DiffItem{Key: "emergency_admins", Old: idsString(pol.EmergencyAdmins), New: idsString(adm)})
				pol.EmergencyAdmins = adm
			}
		}
		out = pol
		if len(diff) == 0 {
			return nil
		}
		cj, _ := json.Marshal(pol)
		if _, err := tx.ExecContext(ctx, `INSERT INTO auth_policy (id, config_json, updated_by, updated_at) VALUES (1,?,?,?)
			ON CONFLICT(id) DO UPDATE SET config_json = excluded.config_json, updated_by = excluded.updated_by, updated_at = excluded.updated_at`,
			string(cj), p.UserID, s.now().UTC().Format(timeLayout)); err != nil {
			return err
		}
		if err := s.guardLockout(ctx, tx); err != nil {
			return err
		}
		return s.record(ctx, tx, p, "auth.policy_update", audit.TargetAuthProvider, "policy", audit.ResultSuccess, diff, "")
	})
	return out, err
}

// UnlinkIdentity 는 관리자가 사용자의 외부 신원 연결을 해제한다. 그 방식의 그 사용자 세션을 폐기하고 감사를 남기며,
// 해제로 "외부 로그인만 허용" 구성이 깨지면(마지막 연결된 관리자 등) 거부한다(E8).
func (s *Service) UnlinkIdentity(ctx context.Context, p contract.Principal, userID int64, provider string) error {
	if _, err := contract.Authorize(contract.WithPrincipal(ctx, p), contract.PermAuthManage); err != nil {
		return err
	}
	return s.txs.WithTx(ctx, func(tx contract.Tx) error {
		res, err := tx.ExecContext(ctx, `DELETE FROM user_identities WHERE user_id = ? AND provider = ?`, userID, provider)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return contract.ErrNotFound
		}
		if _, err := tx.ExecContext(ctx, `UPDATE sessions SET revoked_at = ? WHERE user_id = ? AND auth_method = ? AND revoked_at IS NULL`,
			s.now().UTC().Format(timeLayout), userID, provider); err != nil {
			return err
		}
		if err := s.guardLockout(ctx, tx); err != nil {
			return err
		}
		return s.record(ctx, tx, p, "auth.identity_unlink", audit.TargetCredential, idStr(userID), audit.ResultSuccess,
			[]audit.DiffItem{{Key: "identity_provider", Old: provider}}, "")
	})
}

// RecentEmergencyLogin 은 최근 7일 안의 비상 로그인 사용 시각이다(관리자 화면 상단 알림). 없으면 빈 문자열.
func (s *Service) RecentEmergencyLogin(ctx context.Context) string {
	var at string
	err := s.db.QueryRowContext(ctx, `SELECT at FROM audit_logs WHERE action = 'auth.emergency_login' AND at > ? ORDER BY seq DESC LIMIT 1`,
		s.now().UTC().AddDate(0, 0, -7).Format(timeLayout)).Scan(&at)
	if err != nil {
		return ""
	}
	return at
}
