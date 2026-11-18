package auth

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/org"
)

// 로그인 방식 이름. 세션·감사·신원 표에 같은 값이 쓰인다.
const (
	MethodPassword  = "password"
	MethodOIDC      = "oidc" // 범용 외부 OIDC(PL_OIDC_*)
	MethodDev       = "dev"  // 로컬 dev IdP(같은 OIDC 코드 경로)
	MethodGoogle    = "google"
	MethodMicrosoft = "microsoft"
	MethodSAML      = "saml"
)

// Identity 는 방식이 증명한 외부 신원이다. 방식별 검사(구글 hd·email_verified, Entra tid, SAML 서명)는 **호출하는 쪽이 끝낸 뒤**
// 넘기고, Email 은 그 방식이 신뢰할 수 있다고 확인한 이메일만 담는다(신뢰할 수 없으면 비운다: 첫 연결을 할 수 없다).
type Identity struct {
	Provider string
	Issuer   string // 발급자·테넌트·IdP entityID(같은 방식 안에서 주체 이름공간)
	Subject  string // sub · oid · NameID
	Email    string
}

// 계정 판정 거부 사유 코드(감사 reason). 입력한 이메일 원문은 남기지 않는다.
const (
	ReasonUnknownAccount = "unknown_account"
	ReasonInactive       = "inactive"
	ReasonNotEmployed    = "not_employed"
	ReasonIdentityBound  = "identity_bound" // 같은 방식에 다른 주체가 이미 묶인 계정
	ReasonNoEmail        = "no_trusted_email"
)

// denial 은 거부 사유를 가진 contract.ErrForbidden 이다(errors.Is(err, contract.ErrForbidden) 가 참).
type denial struct{ reason string }

func (d denial) Error() string        { return contract.ErrForbidden.Error() }
func (d denial) Is(target error) bool { return target == contract.ErrForbidden }

func denialReason(err error) string {
	var d denial
	if errors.As(err, &d) {
		return d.reason
	}
	return ""
}

// normEmail 은 이메일을 비교용으로 정규화한다(앞뒤 공백 제거·소문자).
func normEmail(e string) string { return strings.ToLower(strings.TrimSpace(e)) }

// accountRow 는 resolveAccount 가 읽는 사용자 행이다.
type accountRow struct {
	p      contract.Principal
	active bool
}

func loadAccount(ctx context.Context, tx contract.Tx, where string, arg any) (accountRow, error) {
	var a accountRow
	var role string
	var active int
	err := tx.QueryRowContext(ctx, `SELECT id, name, role, department_id, active FROM users WHERE `+where, arg).
		Scan(&a.p.UserID, &a.p.Name, &role, &a.p.DepartmentID, &active)
	a.p.Role, a.p.Channel, a.active = contract.Role(role), contract.ChannelWeb, active == 1
	return a, err
}

// checkEmployable 은 계정이 활성이고 재직 중인지(B20) 본다. 모든 방식(비밀번호 포함)이 같은 거부 규칙을 쓰는 지점이다.
// 퇴사(또는 입사 전)한 사람은 투영 갱신 전이어도 어떤 방식으로도 로그인할 수 없다.
func (s *Service) checkEmployable(ctx context.Context, tx contract.Tx, a accountRow) error {
	if !a.active {
		return denial{ReasonInactive}
	}
	if denied, err := org.EmploymentDenied(ctx, tx, a.p.UserID, contract.DayOf(s.now())); err != nil {
		return err
	} else if denied {
		return denial{ReasonNotEmployed}
	}
	return nil
}

// resolveUserID 는 이미 신원이 증명된 사용자(비밀번호 로그인 등)에 같은 활성·재직 판정을 건다.
func (s *Service) resolveUserID(ctx context.Context, tx contract.Tx, userID int64) (contract.Principal, error) {
	a, err := loadAccount(ctx, tx, `id = ?`, userID)
	if errors.Is(err, sql.ErrNoRows) {
		return contract.Principal{}, denial{ReasonUnknownAccount}
	}
	if err != nil {
		return contract.Principal{}, err
	}
	if err := s.checkEmployable(ctx, tx, a); err != nil {
		return contract.Principal{}, err
	}
	return a.p, nil
}

// resolveAccount 는 **모든 로그인 방식이 거치는 단 하나의 계정 판정**이다. 방식은 신원만 증명하고,
// 계정 존재·활성·재직(B20)은 여기서 같은 규칙으로 정한다. 등록되지 않은 사용자는 만들지 않는다(fail-closed).
//
//  1. (방식, 발급자, 주체)가 신원 표에 있으면 그 사용자.
//  2. 없으면 신뢰된 이메일이 등록된 계정의 이메일과 같을 때 한 번 연결한다. 같은 방식에 다른 주체가 이미 묶여 있으면 거부한다.
//  3. 비활성이거나 재직이 아니면(퇴사·입사 전) 거부한다. 연결 전에 확인하므로 퇴사자는 신원이 묶이지도 않는다.
//
// 연결이 일어나면 같은 트랜잭션에 auth.identity_link 를 남긴다. 거부는 denial(사유 포함, contract.ErrForbidden)로 돌려준다.
func (s *Service) resolveAccount(ctx context.Context, tx contract.Tx, id Identity) (contract.Principal, error) {
	if id.Provider == "" || id.Subject == "" {
		return contract.Principal{}, denial{ReasonUnknownAccount}
	}
	now := s.now().UTC().Format(timeLayout)
	var identID, userID int64
	err := tx.QueryRowContext(ctx, `SELECT id, user_id FROM user_identities WHERE provider = ? AND issuer = ? AND subject = ?`,
		id.Provider, id.Issuer, id.Subject).Scan(&identID, &userID)
	linked := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return contract.Principal{}, err
	}
	var a accountRow
	if linked {
		a, err = loadAccount(ctx, tx, `id = ?`, userID)
	} else {
		email := normEmail(id.Email)
		if email == "" {
			return contract.Principal{}, denial{ReasonNoEmail}
		}
		a, err = loadAccount(ctx, tx, `lower(email) = ?`, email)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return contract.Principal{}, denial{ReasonUnknownAccount}
	}
	if err != nil {
		return contract.Principal{}, err
	}
	if err := s.checkEmployable(ctx, tx, a); err != nil {
		return contract.Principal{}, err
	}
	if linked {
		if _, err := tx.ExecContext(ctx, `UPDATE user_identities SET last_login_at = ? WHERE id = ?`, now, identID); err != nil {
			return contract.Principal{}, err
		}
		return a.p, nil
	}
	var other int64
	switch err := tx.QueryRowContext(ctx, `SELECT id FROM user_identities WHERE user_id = ? AND provider = ?`, a.p.UserID, id.Provider).Scan(&other); {
	case err == nil:
		return contract.Principal{}, denial{ReasonIdentityBound} // 다른 외부 계정이 이미 묶인 이메일(탈취·이메일 재사용 방지)
	case !errors.Is(err, sql.ErrNoRows):
		return contract.Principal{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO user_identities (user_id, provider, issuer, subject, linked_at, last_login_at) VALUES (?,?,?,?,?,?)`,
		a.p.UserID, id.Provider, id.Issuer, id.Subject, now, now); err != nil {
		return contract.Principal{}, err
	}
	if err := s.record(ctx, tx, a.p, "auth.identity_link", audit.TargetCredential, idStr(a.p.UserID), audit.ResultSuccess,
		[]audit.DiffItem{{Key: "identity_provider", New: id.Provider}}, ""); err != nil {
		return contract.Principal{}, err
	}
	return a.p, nil
}
