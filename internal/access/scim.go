package access

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/dbutil"
)

// SCIM 그룹→역할 연동(B40 3단계)이 쓰는 부여·회수. 일반 부여(AddGrant)와 달리 role:manage·2인 확인을 요구하지 않는 대신
// **위험 권한이 든 역할은 어떤 경로로도 거부한다**(B33 규칙: 위험 권한은 역할 관리 화면의 2인 확인으로만 준다). 호출자의 Principal 은 SCIM 주체다.

// SCIMGrant 는 사용자에게 역할의 직접 부여를 만든다. 이미 같은 부여가 있으면(사람이 준 것) 만들지 않고 created=false 로 그 번호를 돌려준다
// (SCIM 이 만들지 않은 부여는 회수도 SCIM 이 하지 않는다). 위험 권한이 든 역할이면 *RiskyRoleError 다. 감사는 role.grant(채널은 호출자의 Principal).
func (s *Service) SCIMGrant(ctx context.Context, tx contract.Tx, actor contract.Principal, roleID, userID int64, reason string, scope string, scopeEntity int64) (grantID int64, created bool, err error) {
	if scope == "" {
		scope = contract.ScopeOwn
	}
	if scope != contract.ScopeOwn && scope != contract.ScopeEntity {
		return 0, false, fmt.Errorf("%w: SCIM 으로는 소속 법인 또는 법인 하나의 범위만 줄 수 있습니다", contract.ErrValidation)
	}
	if scope == contract.ScopeOwn {
		scopeEntity = 0
	}
	role, err := loadRole(ctx, tx, roleID)
	if err != nil {
		return 0, false, err
	}
	if risky := riskOf(role.Permissions); len(risky) > 0 {
		return 0, false, &RiskyRoleError{Perms: risky}
	}
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE id = ?`, userID).Scan(&n); err != nil || n == 0 {
		if err == nil {
			err = contract.ErrNotFound
		}
		return 0, false, err
	}
	err = tx.QueryRowContext(ctx, `SELECT id FROM role_grants WHERE role_id = ? AND target_type = 'user' AND target_id = ? AND unit_kind = '' AND include_descendants = 0 AND scope = ? AND scope_entity_id = ?`, roleID, userID, scope, scopeEntity).Scan(&grantID)
	if err == nil {
		return grantID, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, false, err
	}
	id, err := dbutil.InsertID(ctx, tx, `INSERT INTO role_grants (role_id, target_type, target_id, unit_kind, include_descendants, source, reason, created_by, created_at, scope, scope_entity_id) VALUES (?,?,?,?,?,'manual',?,?,?,?,?)`,
		roleID, "user", userID, "", 0, reason, actor.UserID, s.stamp(), scope, scopeEntity)
	if err != nil {
		return 0, false, err
	}
	diff := []audit.DiffItem{{Key: "role_name", New: role.Name}, {Key: "grant_target_type", New: "user"}, {Key: "grant_target", New: s.targetLabel(ctx, tx, "user", userID, "")},
		{Key: "scim_group", New: reason}}
	if scope != contract.ScopeOwn {
		diff = append(diff, audit.DiffItem{Key: "grant_scope", New: s.scopeLabel(ctx, tx, scope, scopeEntity)})
	}
	return id, true, s.record(ctx, tx, actor, "role.grant", audit.TargetRoleGrant, fmt.Sprint(id), diff, "")
}

// SCIMRevoke 는 SCIMGrant 가 만든 사용자 대상 직접 부여를 회수한다. 사용자 대상이 아니거나 이미 없으면 아무것도 하지 않는다.
// 회수는 권한을 줄이기만 하므로 위험 여부를 따지지 않지만, 마지막 role:manage·auth:manage 보유자가 없어지는 회수는 막는다(Guard).
func (s *Service) SCIMRevoke(ctx context.Context, tx contract.Tx, actor contract.Principal, grantID int64) error {
	var roleID, target int64
	var ttype string
	err := tx.QueryRowContext(ctx, `SELECT role_id, target_type, target_id FROM role_grants WHERE id = ?`, grantID).Scan(&roleID, &ttype, &target)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if ttype != "user" {
		return fmt.Errorf("%w: 사용자 대상이 아닌 부여는 SCIM 이 회수하지 않습니다", contract.ErrValidation)
	}
	role, err := loadRole(ctx, tx, roleID)
	if err != nil {
		return err
	}
	return s.guardHolders(ctx, tx, func() error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM role_grants WHERE id = ?`, grantID); err != nil {
			return err
		}
		diff := []audit.DiffItem{{Key: "role_name", Old: role.Name}, {Key: "grant_target_type", Old: "user"}, {Key: "grant_target", Old: s.targetLabel(ctx, tx, "user", target, "")}}
		return s.record(ctx, tx, actor, "role.revoke", audit.TargetRoleGrant, fmt.Sprint(grantID), diff, "")
	})
}

// RoleRisk 는 역할의 위험 권한 목록이다(없으면 빈 목록). 역할이 없거나 지워졌으면 ErrNotFound.
func RoleRisk(ctx context.Context, tx contract.Tx, roleID int64) ([]string, error) {
	r, err := loadRole(ctx, tx, roleID)
	if err != nil {
		return nil, err
	}
	return riskOf(r.Permissions), nil
}
