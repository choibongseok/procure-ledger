package auth

import (
	"context"
	"errors"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/access"
	"github.com/choibongseok/procure-ledger/internal/contract"
)

// B33: 운영 인증 경로(세션·개인 토큰)는 항상 non-nil 실효 권한을 채우고(빈 집합 = 전부 거부), 권한 회수는 다음 요청부터 적용된다.
func TestAuthenticatedPrincipalsAlwaysCarryEffectivePerms(t *testing.T) {
	e := newPwEnv(t, false)
	ctx := context.Background()
	if _, err := access.SyncTemplates(ctx, e.db); err != nil {
		t.Fatal(err)
	}
	e.setPw(t, "staff@pl.test", goodPw, false)
	lr, err := e.login("staff@pl.test", goodPw)
	if err != nil {
		t.Fatal(err)
	}
	p, _, err := e.s.sessionPrincipal(ctx, lr.RawSession)
	if err != nil || p.Perms == nil {
		t.Fatalf("세션 Principal 의 Perms 가 nil: %v", err)
	}
	if !p.Can(contract.PermPurchaseCreate) || p.Can(contract.PermAuditRead) || p.Can(contract.PermRoleManage) {
		t.Fatalf("직원 권한이 이전과 다름: %v", access.SortedStrings(p.Perms))
	}
	if len(p.RoleNames) != 1 || p.RoleNames[0] != "직원" {
		t.Fatalf("역할 이름 %v", p.RoleNames)
	}

	// 개인 토큰도 같다: 발급자의 현재 실효 권한.
	adm := contract.Principal{UserID: e.uid(t, "staff@pl.test"), Role: contract.RoleStaff, Channel: contract.ChannelWeb, Auth: contract.AuthSession}
	raw, _, err := e.s.Issue(ctx, adm, "t", 0)
	if err != nil {
		t.Fatal(err)
	}
	tp, err := e.s.AuthenticateToken(ctx, raw)
	if err != nil || tp.Perms == nil || !tp.Can(contract.PermPurchaseCreate) {
		t.Fatalf("토큰 Principal: %v %v", tp.Perms, err)
	}

	// 부여를 회수하면 같은 세션·같은 토큰의 다음 요청부터 권한이 사라진다. 권한이 없는 사용자는 non-nil 빈 집합(전부 거부)이다.
	if _, err := e.db.Exec(`DELETE FROM role_grants WHERE target_type = 'user' AND target_id = ?`, adm.UserID); err != nil {
		t.Fatal(err)
	}
	p2, _, err := e.s.sessionPrincipal(ctx, lr.RawSession)
	if err != nil || p2.Perms == nil || p2.Can(contract.PermPurchaseCreate) {
		t.Fatalf("회수 뒤에도 세션 권한이 남음: %v %v", access.SortedStrings(p2.Perms), err)
	}
	for _, perm := range contract.AllPermissions {
		if p2.Can(perm) {
			t.Fatalf("부여가 없는 사용자에게 %s 허용(Role=%s)", perm, p2.Role)
		}
	}
	tp2, err := e.s.AuthenticateToken(ctx, raw)
	if err != nil || tp2.Perms == nil || tp2.Can(contract.PermPurchaseCreate) {
		t.Fatalf("회수 뒤에도 토큰 권한이 남음: %v %v", access.SortedStrings(tp2.Perms), err)
	}
	// 제한 세션 규칙은 그대로: setup 이 있으면 setup 경로만.
	rp := p
	rp.Setup = contract.SetupPasswordChange
	if rp.Can(contract.PermPurchaseCreate) || !rp.Can(contract.PermSetup) {
		t.Fatal("제한 세션 판정이 바뀜")
	}
}

// B33: 마지막 role:manage·auth:manage 보유자를 비활성화하는 사용자 수정은 거부되고, 본인의 관리자 권한을 내리는 역할 변경도 거부된다.
func TestUserUpdateProtectsLastAdminHolder(t *testing.T) {
	e := newPwEnv(t, false)
	ctx := context.Background()
	if _, err := access.SyncTemplates(ctx, e.db); err != nil {
		t.Fatal(err)
	}
	actor := contract.Principal{UserID: e.uid(t, "finance@pl.test"), Role: contract.RoleAdmin, Channel: contract.ChannelWeb} // user:manage 만 필요(계산되지 않은 Principal)
	off := false
	if _, err := e.s.Users().Update(ctx, actor, e.admin.UserID, contract.UserPatch{Active: &off}); err == nil || !errors.Is(err, contract.ErrInvalidState) {
		t.Fatalf("마지막 관리자 비활성화가 허용됨: %v", err)
	}
	var active int
	_ = e.db.QueryRow(`SELECT active FROM users WHERE id = ?`, e.admin.UserID).Scan(&active)
	if active != 1 {
		t.Fatal("거부했는데 비활성화됨")
	}
	staffRole := contract.RoleStaff
	if _, err := e.s.Users().Update(ctx, e.admin, e.admin.UserID, contract.UserPatch{Role: &staffRole}); !errors.Is(err, contract.ErrValidation) {
		t.Fatalf("본인 관리자 역할 내리기: %v", err)
	}
	// 다른 사람의 역할은 바꿀 수 있고 호환 부여와 실효 권한이 따라간다.
	mgr := contract.RoleManager
	uid := e.uid(t, "staff@pl.test")
	if _, err := e.s.Users().Update(ctx, e.admin, uid, contract.UserPatch{Role: &mgr}); err != nil {
		t.Fatal(err)
	}
	if p, _ := access.PermsOf(ctx, e.db, uid); !p.Has(contract.PermApprovalDecide) {
		t.Fatalf("역할 변경이 실효 권한에 반영되지 않음: %v", access.SortedStrings(p))
	}
}
