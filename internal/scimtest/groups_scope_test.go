package scimtest_test

import (
	"context"
	"strings"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/scim"
)

// B44 9.3: 역할 모드 그룹 매핑은 부여 범위(own 기본, entity 법인 하나)를 정하고, SCIM 이 만드는 부여가 그 범위로 생긴다. group 은 SCIM 이 줄 수 없다.
func TestSCIMGroupRoleMapGrantScope(t *testing.T) {
	w := newWorld(t)
	if err := w.setMode("role"); err != nil {
		t.Fatal(err)
	}
	safe := w.role("r_safe", "purchase:create")
	ctx := context.Background()
	if _, err := w.sv().CreateMap(ctx, w.admin, scim.MapInput{Source: "entra", GroupExternalID: "g-grp", RoleID: safe, Scope: "group"}); err == nil || !strings.Contains(err.Error(), "소속 법인 전체 또는 법인 하나") {
		t.Fatalf("group 범위는 거부돼야 한다: %v", err)
	}
	if _, err := w.sv().CreateMap(ctx, w.admin, scim.MapInput{Source: "entra", GroupExternalID: "g-bad", RoleID: safe, Scope: "entity", ScopeEntityID: 999}); err == nil {
		t.Fatal("없는 법인은 거부돼야 한다")
	}
	v, err := w.sv().CreateMap(ctx, w.admin, scim.MapInput{Source: "entra", GroupExternalID: "g-ent", DisplayName: "g-ent", RoleID: safe, Scope: "entity", ScopeEntityID: 1})
	if err != nil {
		t.Fatal(err)
	}
	u := w.scimUser("e1", "u1@pl.test")
	if r := w.addMembers(v.ID, u); r.Status != 200 {
		t.Fatalf("멤버 추가: %d %s", r.Status, r.Raw)
	}
	var scope string
	var ent int64
	if err := w.d.DB.QueryRow(`SELECT scope, scope_entity_id FROM role_grants WHERE role_id = ? AND target_type = 'user' AND target_id = ?`, safe, u).Scan(&scope, &ent); err != nil {
		t.Fatal(err)
	}
	if scope != "entity" || ent != 1 {
		t.Fatalf("SCIM 이 만든 부여의 범위: %s %d", scope, ent)
	}
}
