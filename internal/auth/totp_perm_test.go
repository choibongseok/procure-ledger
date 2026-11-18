package auth

import (
	"context"
	"strings"
	"testing"
)

// 2단계 인증 필수 대상은 역할 이름이 아니라 실효 권한 기준이다(B33). 이전 형식(역할 목록)은 읽을 때 권한으로 이관된다.
func TestTOTPRequiredByEffectivePermission(t *testing.T) {
	e := newProvEnv(t)
	ctx := context.Background()
	pol, err := loadPolicy(ctx, e.db)
	if err != nil {
		t.Fatal(err)
	}
	admin, staff := e.uid(t, "admin@pl.test"), e.uid(t, "staff@pl.test")
	if !pol.totpRequiredFor(ctx, e.db, admin) || pol.totpRequiredFor(ctx, e.db, staff) {
		t.Fatalf("기본 정책: 관리자 필수, 직원 아님이어야 함 %+v", pol.TOTPRequiredPerms)
	}
	// 직원 계정(users.role=staff)이라도 role:manage 를 실효 권한으로 가졌으면 필수다.
	if _, err := e.db.Exec(`INSERT INTO user_effective_permissions(user_id, permission, role_id, grant_id) VALUES (?, 'role:manage', 0, 0)`, staff); err != nil {
		t.Fatal(err)
	}
	if !pol.totpRequiredFor(ctx, e.db, staff) {
		t.Fatal("role:manage 보유자는 역할 이름과 무관하게 2단계 필수여야 함")
	}
	// 이전 형식 이관: 역할 목록 → 그 기본 역할들의 위험 권한.
	if _, err := e.db.Exec(`INSERT INTO auth_policy (id, config_json, updated_at) VALUES (1, '{"totpRequiredRoles":["finance"],"emergencyAdmins":[]}', '2026-11-12T00:00:00Z')
		ON CONFLICT(id) DO UPDATE SET config_json = excluded.config_json`); err != nil {
		t.Fatal(err)
	}
	old, err := loadPolicy(ctx, e.db)
	if err != nil {
		t.Fatal(err)
	}
	// 옛 기본값 ["admin"] 은 새 기본값으로 바뀐다(재무가 갑자기 필수가 되지 않는다).
	if _, err := e.db.Exec(`UPDATE auth_policy SET config_json = '{"totpRequiredRoles":["admin"],"emergencyAdmins":[]}' WHERE id = 1`); err != nil {
		t.Fatal(err)
	}
	def, err := loadPolicy(ctx, e.db)
	if err != nil || strings.Join(def.TOTPRequiredPerms, ",") != "role:manage,auth:manage" {
		t.Fatalf("옛 기본값 이관: %+v %v", def.TOTPRequiredPerms, err)
	}
	got := strings.Join(old.TOTPRequiredPerms, ",")
	if !strings.Contains(got, "ledger:write") || !strings.Contains(got, "payment:write") || strings.Contains(got, "role:manage") {
		t.Fatalf("이관 결과: %s", got)
	}
}
