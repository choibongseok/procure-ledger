package auth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/choibongseok/procure-ledger/internal/access"
	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
)

const userSelect = `SELECT u.id, u.email, u.name, u.role, COALESCE(u.position,''), u.department_id, d.name, u.active
	FROM users u JOIN departments d ON d.id = u.department_id`

type rowScanner interface{ Scan(dest ...any) error }

func scanUser(r rowScanner) (contract.UserInfo, error) {
	var u contract.UserInfo
	var role string
	var active int
	err := r.Scan(&u.ID, &u.Email, &u.Name, &role, &u.Position, &u.DepartmentID, &u.DepartmentName, &active)
	u.Role, u.Active = contract.Role(role), active == 1
	return u, err
}

func (s *Service) listUsers(ctx context.Context, p contract.Principal) ([]contract.UserInfo, error) {
	if _, err := contract.Authorize(contract.WithPrincipal(ctx, p), contract.PermUserManage); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, userSelect+` ORDER BY u.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []contract.UserInfo{}
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// usersAdapter 는 contract.UserDirectory 를 구현한다(Service 의 List 는 토큰 목록이 쓰고 있다).
type usersAdapter struct{ s *Service }

var _ contract.UserDirectory = usersAdapter{}

// Users 는 사용자 관리 인터페이스를 돌려준다.
func (s *Service) Users() contract.UserDirectory { return usersAdapter{s} }

func (a usersAdapter) List(ctx context.Context, p contract.Principal) ([]contract.UserInfo, error) {
	return a.s.listUsers(ctx, p)
}

// Update 는 역할·부서·활성 여부를 바꾸고 변경 전후 diff 를 감사 로그에 남긴다.
func (a usersAdapter) Update(ctx context.Context, p contract.Principal, id int64, patch contract.UserPatch) (contract.UserInfo, error) {
	s := a.s
	if _, err := contract.Authorize(contract.WithPrincipal(ctx, p), contract.PermUserManage); err != nil {
		return contract.UserInfo{}, err
	}
	if patch.Role != nil {
		// 역할 변경은 권한 체계를 바꾸는 일이다: user:manage 만으로는 못 하고(스스로 올리기 방지) role:manage 와 로그인 세션이 필요하다.
		// 위험 권한이 든 기본 역할로의 지정은 이 호환 경로로 하지 않는다(역할 부여 화면의 2인 확인으로 한다).
		if _, err := contract.Authorize(contract.WithPrincipal(ctx, p), contract.PermRoleManage); err != nil {
			a.s.deniedRole(ctx, p, "role.template_set", id, "역할 관리 권한 없음")
			return contract.UserInfo{}, err
		}
		if p.Auth != contract.AuthSession {
			a.s.deniedRole(ctx, p, "role.template_set", id, "로그인 화면이 아닌 경로")
			return contract.UserInfo{}, fmt.Errorf("%w: 역할 변경은 로그인한 화면에서만 할 수 있습니다", contract.ErrForbidden)
		}
		if err := access.CheckAssignable(ctx, s.db, string(*patch.Role)); err != nil {
			var rr *access.RiskyRoleError
			if errors.As(err, &rr) {
				a.s.deniedRole(ctx, p, "role.template_set", id, "위험 권한이 든 역할 지정 시도")
			}
			return contract.UserInfo{}, err
		}
		// 기본 템플릿 키 또는 역할 관리에서 만든 사용자 정의 역할 키(입사 발령과 같다). 위험 권한이 든 역할은 위에서 이미 걸렀다.
		var rn int
		if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM roles WHERE key = ? AND deleted_at IS NULL`, string(*patch.Role)).Scan(&rn); err != nil {
			return contract.UserInfo{}, err
		}
		if rn == 0 {
			return contract.UserInfo{}, fmt.Errorf("%w: 알 수 없는 역할입니다", contract.ErrValidation)
		}
	}
	if id == p.UserID && patch.Active != nil && !*patch.Active {
		return contract.UserInfo{}, fmt.Errorf("%w: 본인 계정은 비활성화할 수 없습니다", contract.ErrValidation)
	}
	if id == p.UserID && patch.Role != nil {
		// 본인이 역할·부여 관리 권한을 가졌는데 바꾸려는 역할에 그 권한이 없으면 막는다(스스로 관리자를 내려놓는 실수 방지).
		holds, err := access.HasPerm(ctx, s.db, p.UserID, contract.PermRoleManage)
		if err != nil {
			return contract.UserInfo{}, err
		}
		keeps, err := access.RoleHasPermission(ctx, s.db, string(*patch.Role), contract.PermRoleManage)
		if err != nil {
			return contract.UserInfo{}, err
		}
		if holds && !keeps {
			return contract.UserInfo{}, fmt.Errorf("%w: 본인의 관리자 역할은 바꿀 수 없습니다", contract.ErrValidation)
		}
	}
	var out contract.UserInfo
	err := s.txs.WithTx(ctx, func(tx contract.Tx) error {
		before, err := scanUser(tx.QueryRowContext(ctx, userSelect+` WHERE u.id = ?`, id))
		if errors.Is(err, sql.ErrNoRows) {
			return contract.ErrNotFound
		}
		if err != nil {
			return err
		}
		var diff, roleDiff []audit.DiffItem
		after := before
		if patch.Role != nil && *patch.Role != before.Role {
			after.Role = *patch.Role
			roleDiff = append(roleDiff, audit.DiffItem{Key: "role", Old: string(before.Role), New: string(after.Role)})
		}
		if patch.Active != nil && *patch.Active != before.Active {
			after.Active = *patch.Active
			diff = append(diff, audit.DiffItem{Key: "active", Old: fmt.Sprint(before.Active), New: fmt.Sprint(after.Active)})
		}
		out = after
		if len(diff) == 0 && len(roleDiff) == 0 {
			return nil
		}
		active := 0
		if after.Active {
			active = 1
		}
		// 마지막 role:manage·auth:manage 보유자가 없어지는 비활성·역할 변경은 거부한다.
		if err := access.Guard(ctx, tx, contract.DayOf(s.now()), func() error {
			if _, err := tx.ExecContext(ctx, `UPDATE users SET role = ?, active = ? WHERE id = ?`, string(after.Role), active, id); err != nil {
				return err
			}
			if len(roleDiff) > 0 { // 사용자 정의 역할 키는 users 트리거가 부여를 만들지 않으므로 직접 부여를 보장한다(템플릿은 트리거가 만든다)
				if err := access.EnsureCompatGrant(ctx, tx, id, string(after.Role), s.now()); err != nil {
					return err
				}
			}
			if !after.Active { // 비활성 사용자의 실효 권한은 남기지 않는다
				_, err := tx.ExecContext(ctx, `DELETE FROM user_effective_permissions WHERE user_id = ?`, id)
				return err
			}
			if !before.Active && after.Active { // 다시 켜면 부여를 다시 계산한다
				if err := access.Recompute(ctx, tx, contract.DayOf(s.now())); err != nil {
					return err
				}
				// 위험 권한을 되찾는 사용자의 재활성은 역할 관리 권한이 있어야 한다(user:manage 만으로 휴면 계정을 되살려 권한을 얻지 못하게).
				if !p.Can(contract.PermRoleManage) {
					n, err := riskyCount(ctx, tx, id)
					if err != nil {
						return err
					}
					if n > 0 {
						return errReactivateRisky
					}
				}
			}
			return nil
		}); err != nil {
			return err
		}
		if len(roleDiff) > 0 { // 역할 변경은 권한 체계 변경이라 role.* 로 따로 남긴다
			if err := s.record(ctx, tx, p, "role.template_set", audit.TargetUser, idStr(id), audit.ResultSuccess, roleDiff, ""); err != nil {
				return err
			}
		}
		if len(diff) == 0 {
			return nil
		}
		return s.record(ctx, tx, p, "user.update", audit.TargetUser, idStr(id), audit.ResultSuccess, diff, "")
	})
	if errors.Is(err, errReactivateRisky) {
		s.deniedRole(ctx, p, "user.update", id, "위험 권한 보유자 재활성 시도")
	}
	return out, err
}

// errReactivateRisky 는 role:manage 없이 위험 권한 보유자를 다시 켜려는 거부다(트랜잭션이 롤백된다).
var errReactivateRisky = fmt.Errorf("%w: 위험 권한을 가진 사용자를 다시 켜려면 역할 관리 권한이 필요합니다", contract.ErrForbidden)

func riskyCount(ctx context.Context, q interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}, userID int64) (int, error) {
	marks, args := make([]string, 0, len(contract.HighRiskPermissions)), []any{userID}
	for _, r := range contract.HighRiskPermissions {
		marks, args = append(marks, "?"), append(args, string(r))
	}
	var n int
	err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM user_effective_permissions WHERE user_id = ? AND permission IN (`+strings.Join(marks, ",")+`)`, args...).Scan(&n)
	return n, err
}

// deniedRole 은 거부된 역할·활성 변경 시도를 별도 트랜잭션으로 남긴다(롤백된 트랜잭션 밖, 기록 오류는 삼킨다).
func (s *Service) deniedRole(ctx context.Context, p contract.Principal, action string, id int64, detail string) {
	_ = s.txs.WithTx(ctx, func(tx contract.Tx) error {
		return s.record(ctx, tx, p, action, audit.TargetUser, idStr(id), audit.ResultDenied, nil, detail)
	})
}
