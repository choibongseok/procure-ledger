package access

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/dbtest"
)

// B44 9절(AC-4412·4414): 역할 부여의 범위(own·entity·group)가 실효 권한 캐시를 거쳐 권한별 효력 법인 집합이 된다.

func (e *env) entity(t *testing.T, code string, active int) int64 {
	t.Helper()
	return dbtest.ExecID(t, e.db, `INSERT INTO entities (code, name, active, created_at, updated_at) VALUES (?, ?, ?, 't', 't')`, code, code, active)
}

func entityPerms(t *testing.T, e *env, uid int64, own ...int64) map[contract.Permission]contract.EntitySet {
	t.Helper()
	m, err := EntityPermsOf(context.Background(), e.db, uid, own)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestGrantScopesResolveToEntitySets(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	s := e.staff(t, "s@x")
	abc := e.entity(t, "ABC", 1)
	xyz := e.entity(t, "XYZ", 1)
	off := e.entity(t, "OFF", 0)
	role, err := e.svc.CreateRole(ctx, e.admin, "구매 조회", "", []string{"purchase:read_all"})
	if err != nil {
		t.Fatal(err)
	}
	target := GrantInput{TargetType: "user", TargetID: s.UserID}

	// 기본(own): 소속·겸직 법인 전체. 기본 템플릿 부여(staff)도 own 이다.
	own := entityPerms(t, e, s.UserID, 1, abc)
	if set := own[contract.PermPurchaseCreate]; !reflect.DeepEqual(set.IDs, []int64{1, abc}) || set.All {
		t.Fatalf("own: %+v", set)
	}
	if got := entityPerms(t, e, s.UserID)[contract.PermPurchaseCreate]; !got.Empty() {
		t.Fatalf("조직 미배치의 own 은 효력이 없다: %+v", got)
	}

	// 특정 법인: 소속이 아니어도 그 법인에서만.
	in := target
	in.Scope, in.ScopeEntityID = contract.ScopeEntity, xyz
	if _, err := e.svc.AddGrant(ctx, e.admin, role.ID, in); err != nil {
		t.Fatalf("법인 부여: %v", err)
	}
	set := entityPerms(t, e, s.UserID, 1)[contract.PermPurchaseReadAll]
	if !reflect.DeepEqual(set.IDs, []int64{xyz}) || set.All {
		t.Fatalf("entity: %+v", set)
	}
	// 같은 역할·같은 대상에게 다른 법인으로 한 번 더 줄 수 있고, 같은 범위의 중복은 거부된다.
	in2 := target
	in2.Scope, in2.ScopeEntityID = contract.ScopeEntity, abc
	if _, err := e.svc.AddGrant(ctx, e.admin, role.ID, in2); err != nil {
		t.Fatalf("다른 법인 부여: %v", err)
	}
	if _, err := e.svc.AddGrant(ctx, e.admin, role.ID, in2); err == nil || !errors.Is(err, contract.ErrInvalidState) {
		t.Fatalf("같은 범위 중복: %v", err)
	}
	set = entityPerms(t, e, s.UserID, 1)[contract.PermPurchaseReadAll]
	if !reflect.DeepEqual(set.IDs, []int64{abc, xyz}) {
		t.Fatalf("법인별 부여의 합집합: %+v", set)
	}

	// 검증: 알 수 없는 범위, 법인 없이 entity, own·group 에 법인, 없는 법인, 비활성 법인.
	for name, g := range map[string]GrantInput{
		"범위":       {TargetType: "user", TargetID: s.UserID, Scope: "world"},
		"법인 없음":    {TargetType: "user", TargetID: s.UserID, Scope: contract.ScopeEntity},
		"own 에 법인": {TargetType: "user", TargetID: s.UserID, ScopeEntityID: abc},
		"없는 법인":    {TargetType: "user", TargetID: s.UserID, Scope: contract.ScopeEntity, ScopeEntityID: 999},
		"비활성 법인":   {TargetType: "user", TargetID: s.UserID, Scope: contract.ScopeEntity, ScopeEntityID: off},
	} {
		if _, err := e.svc.AddGrant(ctx, e.admin, role.ID, g); !errors.Is(err, contract.ErrValidation) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// 부여 목록에 범위의 한국어 이름이 보인다. 감사에는 범위가 남는다.
	list, err := e.svc.ListGrants(ctx, e.admin, role.ID)
	if err != nil || len(list) != 2 {
		t.Fatalf("목록: %+v %v", list, err)
	}
	labels := map[string]bool{}
	for _, g := range list {
		labels[g.ScopeLabel] = true
	}
	if !labels["XYZ 법인"] || !labels["ABC 법인"] {
		t.Fatalf("범위 이름: %v", labels)
	}
	if e.n(t, `SELECT COUNT(*) FROM audit_logs WHERE action = 'role.grant' AND diff LIKE '%grant_scope%'`) != 2 {
		t.Fatal("감사에 적용 범위가 남아야 한다")
	}
}

// 그룹 전체 범위 부여는 모든 법인에서 효력이 있어 위험 부여와 같이 2인 확인을 거친다(D7). 승인하면 모든 법인에서 효력이 생긴다.
func TestGroupScopeGrantNeedsSecondApproval(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	s := e.staff(t, "s@x")
	role, err := e.svc.CreateRole(ctx, e.admin, "구매 조회", "", []string{"purchase:read_all"})
	if err != nil {
		t.Fatal(err)
	}
	in := GrantInput{TargetType: "user", TargetID: s.UserID, Scope: contract.ScopeGroup, Reason: "그룹 구매 감독"}
	prev, err := e.svc.PreviewGrant(ctx, e.admin, role.ID, in)
	if err != nil || !prev.NeedsApproval || len(prev.RiskPerms) == 0 {
		t.Fatalf("미리보기: %+v %v", prev, err)
	}
	res, err := e.svc.AddGrant(ctx, e.admin, role.ID, in)
	if err != nil || res.Request == nil || res.GrantID != 0 {
		t.Fatalf("2인 확인 요청이어야 한다: %+v %v", res, err)
	}
	if got := entityPerms(t, e, s.UserID, 1)[contract.PermPurchaseReadAll]; !got.Empty() {
		t.Fatalf("승인 전에는 효력이 없다: %+v", got)
	}
	noReason := in
	noReason.Reason = ""
	if _, err := e.svc.AddGrant(ctx, e.admin, role.ID, noReason); !errors.Is(err, contract.ErrValidation) {
		t.Fatalf("사유 없는 그룹 부여: %v", err)
	}
	if err := e.svc.Approve(ctx, e.boss, res.Request.ID); err != nil {
		t.Fatalf("승인: %v", err)
	}
	if got := entityPerms(t, e, s.UserID, 1)[contract.PermPurchaseReadAll]; !got.All {
		t.Fatalf("승인 뒤 모든 법인: %+v", got)
	}
	// own 범위의 같은 대상 부여와 따로 존재할 수 있다.
	own := GrantInput{TargetType: "user", TargetID: s.UserID}
	if _, err := e.svc.AddGrant(ctx, e.admin, role.ID, own); err != nil && !errors.Is(err, contract.ErrInvalidState) {
		t.Fatalf("own 부여: %v", err)
	}
}
