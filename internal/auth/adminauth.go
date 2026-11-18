package auth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
)

// StartResult 는 "로그인 시작"의 결과다. 임시 비밀번호는 이 응답에서만 한 번 나가고 어디에도 남지 않는다.
type StartResult struct {
	Via          string     `json:"via"` // mail | temp
	TempPassword string     `json:"tempPassword,omitempty"`
	ExpiresAt    *time.Time `json:"expiresAt,omitempty"`
}

// StartLogin 은 관리자가 사용자의 로그인 자격을 시작·초기화한다(명세 4.1·6.2·6.4): 설정 링크 메일(via=mail) 또는
// 임시 비밀번호(via=temp). 관리자는 비밀번호를 직접 정할 수 없고, 자기 자신에게는 임시 비밀번호를 발급할 수 없다.
// 발급하면 대상의 세션을 모두 폐기한다(개인 토큰은 유지, D8).
func (s *Service) StartLogin(ctx context.Context, p contract.Principal, userID int64, via string) (StartResult, error) {
	if _, err := contract.Authorize(contract.WithPrincipal(ctx, p), contract.PermAuthManage); err != nil {
		return StartResult{}, err
	}
	if via != "mail" && via != "temp" {
		return StartResult{}, fmt.Errorf("%w: via 는 mail 또는 temp 여야 합니다", contract.ErrValidation)
	}
	if via == "mail" && s.mailer == nil {
		return StartResult{}, fmt.Errorf("%w: 메일 발송이 설정되지 않았습니다. 임시 비밀번호를 발급하세요", contract.ErrValidation)
	}
	if via == "temp" && userID == p.UserID {
		return StartResult{}, fmt.Errorf("%w: 본인 계정에는 임시 비밀번호를 발급할 수 없습니다. 다른 관리자에게 요청하세요", contract.ErrValidation)
	}
	out := StartResult{Via: via}
	var token, addr string
	var tempHash string
	if via == "temp" {
		pw, err := generateTempPassword()
		if err != nil {
			return StartResult{}, err
		}
		if tempHash, err = s.hasher.Hash(ctx, pw); err != nil {
			return StartResult{}, err
		}
		out.TempPassword = pw
	}
	pol, err := loadPolicy(ctx, s.db)
	if err != nil {
		return StartResult{}, err
	}
	err = s.txs.WithTx(ctx, func(tx contract.Tx) error {
		if _, err := s.resolveUserID(ctx, tx, userID); err != nil {
			if errors.Is(err, contract.ErrForbidden) {
				return fmt.Errorf("%w: 활성이고 재직 중인 계정에만 로그인을 시작할 수 있습니다", contract.ErrValidation)
			}
			return err
		}
		if err := tx.QueryRowContext(ctx, `SELECT email FROM users WHERE id = ?`, userID).Scan(&addr); err != nil {
			return err
		}
		if via == "mail" {
			var err error
			if token, err = s.issueLinkToken(ctx, tx, userID, "setup", setupLinkTTL); err != nil {
				return err
			}
			return s.record(ctx, tx, p, "auth.password_reset_request", audit.TargetCredential, idStr(userID), audit.ResultSuccess,
				[]audit.DiffItem{{Key: "via", New: "mail"}}, "관리자 시작")
		}
		old := ""
		if c, err := scanCred(tx.QueryRowContext(ctx, credSelect+` WHERE c.user_id = ?`, userID)); err == nil {
			old = c.hash
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err := s.setPassword(ctx, tx, userID, tempHash, old, pol.Password, true); err != nil {
			return err
		}
		exp := s.now().UTC().Add(tempPasswordTTL)
		out.ExpiresAt = &exp
		if _, err := tx.ExecContext(ctx, `UPDATE user_credentials SET temp_expires_at = ? WHERE user_id = ?`, exp.Format(timeLayout), userID); err != nil {
			return err
		}
		if err := s.revokeUserSessions(ctx, tx, userID, ""); err != nil {
			return err
		}
		return s.record(ctx, tx, p, "auth.password_temp_issue", audit.TargetCredential, idStr(userID), audit.ResultSuccess,
			[]audit.DiffItem{{Key: "expires_at", New: exp.Format(timeLayout)}}, "")
	})
	if err != nil {
		return StartResult{}, err
	}
	if via == "mail" {
		s.sendLinkMail(userID, addr, "setup", token, "auth.password_reset_request")
	}
	return out, nil
}

// UnlockAccount 는 관리자가 잠금을 즉시 푼다(감사 auth.account_unlock).
func (s *Service) UnlockAccount(ctx context.Context, p contract.Principal, userID int64) error {
	if _, err := contract.Authorize(contract.WithPrincipal(ctx, p), contract.PermAuthManage); err != nil {
		return err
	}
	return s.txs.WithTx(ctx, func(tx contract.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE user_credentials SET failed_count = 0, locked_until = NULL WHERE user_id = ?`, userID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return contract.ErrNotFound
		}
		return s.record(ctx, tx, p, "auth.account_unlock", audit.TargetCredential, idStr(userID), audit.ResultSuccess,
			[]audit.DiffItem{{Key: "by", New: "admin"}}, "")
	})
}

// CredentialStatus 는 사용자 한 명의 로그인 자격 상태다(비밀은 없다).
type CredentialStatus struct {
	UserID      int64      `json:"userId"`
	Email       string     `json:"email"`
	Name        string     `json:"name"`
	Role        string     `json:"role"`
	Active      bool       `json:"active"`
	HasPassword bool       `json:"hasPassword"`
	MustChange  bool       `json:"mustChange"`
	LockedUntil *time.Time `json:"lockedUntil,omitempty"`
	TOTP        bool       `json:"totp"`          // 2단계 인증 등록 여부
	Identities  []string   `json:"identities"`    // 연결된 외부 방식
	CanAuthMgr  bool       `json:"canAuthManage"` // 로그인 설정 권한(auth:manage)을 실효 권한으로 가졌다(비상 관리자 후보)
}

// ListCredentialStatus 는 사용자별 로그인 자격 상태 목록이다(auth:manage).
func (s *Service) ListCredentialStatus(ctx context.Context, p contract.Principal) ([]CredentialStatus, error) {
	if _, err := contract.Authorize(contract.WithPrincipal(ctx, p), contract.PermAuthManage); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT u.id, u.email, u.name, u.role, u.active, c.user_id IS NOT NULL, COALESCE(c.must_change,0), c.locked_until,
		EXISTS (SELECT 1 FROM user_totp t WHERE t.user_id = u.id AND t.confirmed_at IS NOT NULL),
		EXISTS (SELECT 1 FROM user_effective_permissions e WHERE e.user_id = u.id AND e.permission = 'auth:manage')
		FROM users u LEFT JOIN user_credentials c ON c.user_id = u.id ORDER BY u.id`)
	if err != nil {
		return nil, err
	}
	out := []CredentialStatus{}
	idx := map[int64]int{}
	now := s.now().UTC()
	for rows.Next() {
		var c CredentialStatus
		var active, must int
		var has, totp, mgr bool // PG 는 비교식·EXISTS 가 불리언이다(SQLite 의 0/1 도 bool 로 읽힌다)
		var lock sql.NullString
		if err := rows.Scan(&c.UserID, &c.Email, &c.Name, &c.Role, &active, &has, &must, &lock, &totp, &mgr); err != nil {
			rows.Close()
			return nil, err
		}
		c.Active, c.HasPassword, c.MustChange, c.TOTP, c.Identities, c.CanAuthMgr = active == 1, has, must == 1, totp, []string{}, mgr
		if lt := parseTimePtr(lock); lt != nil && now.Before(*lt) {
			c.LockedUntil = lt
		}
		idx[c.UserID] = len(out)
		out = append(out, c)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	ir, err := s.db.QueryContext(ctx, `SELECT user_id, provider FROM user_identities ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer ir.Close()
	for ir.Next() {
		var uid int64
		var prov string
		if err := ir.Scan(&uid, &prov); err != nil {
			return nil, err
		}
		if i, ok := idx[uid]; ok {
			out[i].Identities = append(out[i].Identities, prov)
		}
	}
	return out, ir.Err()
}
