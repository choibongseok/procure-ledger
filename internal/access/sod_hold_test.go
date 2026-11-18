package access

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/choibongseok/procure-ledger/internal/audit"
	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/sod"
)

func (e *env) withSoD(t *testing.T) *sod.Engine {
	t.Helper()
	eng, err := sod.New(sod.Options{DB: e.db, Txs: contract.SQLTxManager{DB: e.db}, Audit: audit.New(e.db), Now: func() time.Time { return *e.clock }})
	if err != nil {
		t.Fatal(err)
	}
	e.svc.SetSoD(eng)
	return eng
}

func (e *env) setMode(t *testing.T, rule, mode string) {
	t.Helper()
	if _, err := e.db.Exec(`INSERT INTO sod_rule_settings (rule_code, mode, updated_by, updated_at) VALUES (?,?,?,'t') ON CONFLICT(rule_code) DO UPDATE SET mode = excluded.mode`, rule, mode, e.admin.UserID); err != nil {
		t.Fatal(err)
	}
}

// 한 역할에 구매 요청과 결재 두 권한이 함께 있어 hold.purchase_decide 의 쌍이 된다(위험 권한이 아니라 2인 확인이 끼지 않는다).
var pairPerms = []string{string(contract.PermPurchaseCreate), string(contract.PermApprovalDecide)}

func TestHoldWarnRecordsButApplies(t *testing.T) {
	e := newEnv(t)
	e.withSoD(t)
	ctx := context.Background()
	s := e.staff(t, "s@x")
	role, err := e.svc.CreateRole(ctx, e.admin, "구매 결재", "", pairPerms)
	if err != nil {
		t.Fatal(err)
	}
	// 스태프는 이미 구매 요청 권한(템플릿)만 있고 결재는 없다. 이 역할을 주면 새로 쌍이 생긴다.
	res, err := e.svc.AddGrant(ctx, e.admin, role.ID, GrantInput{TargetType: "user", TargetID: s.UserID})
	if err != nil || res.GrantID == 0 {
		t.Fatalf("경고 모드는 적용된다: %+v %v", res, err)
	}
	if e.n(t, `SELECT COUNT(*) FROM sod_violations WHERE rule_code = 'hold.purchase_decide' AND result = 'allowed' AND subject_type = 'user' AND subject_id = ?`, s.UserID) != 1 {
		t.Fatal("위반(경고) 기록이 없다")
	}
	if e.auditCount(t, "sod.violation") != 1 {
		t.Fatal("감사 sod.violation 이 없다")
	}
}

func TestHoldBlockRejectsNewPairAndRecords(t *testing.T) {
	e := newEnv(t)
	e.withSoD(t)
	e.setMode(t, "hold.purchase_decide", "block")
	ctx := context.Background()
	s := e.staff(t, "s@x")
	role, _ := e.svc.CreateRole(ctx, e.admin, "구매 결재", "", pairPerms)
	_, err := e.svc.AddGrant(ctx, e.admin, role.ID, GrantInput{TargetType: "user", TargetID: s.UserID})
	if !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("새 쌍을 만드는 부여는 차단: %v", err)
	}
	if _, ok := sod.IsBlocked(err); !ok {
		t.Fatal("SoD 차단 오류여야 한다")
	}
	if e.n(t, `SELECT COUNT(*) FROM role_grants WHERE role_id = ?`, role.ID) != 0 || e.perms(t, s.UserID).Has(contract.PermApprovalDecide) {
		t.Fatal("차단되면 부여가 롤백되어야 한다")
	}
	if e.n(t, `SELECT COUNT(*) FROM sod_violations WHERE rule_code = 'hold.purchase_decide' AND result = 'blocked' AND subject_id = ?`, s.UserID) != 1 || e.auditCount(t, "sod.blocked") != 1 {
		t.Fatal("롤백 밖에 차단 기록이 남아야 한다")
	}
}

func TestHoldBlockLeavesExistingPairsAlone(t *testing.T) {
	e := newEnv(t)
	// 규칙이 켜지기 전에 이미 쌍을 가진 사용자.
	s := e.staff(t, "s@x")
	ctx := context.Background()
	pair, _ := e.svc.CreateRole(ctx, e.admin, "구매 결재", "", pairPerms)
	if _, err := e.svc.AddGrant(ctx, e.admin, pair.ID, GrantInput{TargetType: "user", TargetID: s.UserID}); err != nil {
		t.Fatal(err)
	}
	e.withSoD(t)
	e.setMode(t, "hold.purchase_decide", "block")
	other, _ := e.svc.CreateRole(ctx, e.admin, "재고 조회", "", []string{string(contract.PermInventoryRead)})
	if _, err := e.svc.AddGrant(ctx, e.admin, other.ID, GrantInput{TargetType: "user", TargetID: s.UserID}); err != nil {
		t.Fatalf("이미 있던 쌍은 무관한 변경을 막지 않는다(D14): %v", err)
	}
	// 같은 쌍을 가진 다른 사람이 새로 생기면 막는다.
	t2 := e.staff(t, "t@x")
	if _, err := e.svc.AddGrant(ctx, e.admin, pair.ID, GrantInput{TargetType: "user", TargetID: t2.UserID}); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("새 사람의 쌍: %v", err)
	}
}

func TestHoldBlockRoleUpdateCreatingPair(t *testing.T) {
	e := newEnv(t)
	e.withSoD(t)
	ctx := context.Background()
	s := e.staff(t, "s@x")
	role, _ := e.svc.CreateRole(ctx, e.admin, "재고 조회", "", []string{string(contract.PermInventoryRead)})
	if _, err := e.svc.AddGrant(ctx, e.admin, role.ID, GrantInput{TargetType: "user", TargetID: s.UserID}); err != nil {
		t.Fatal(err)
	}
	e.setMode(t, "hold.purchase_decide", "block")
	// 이미 부여된 역할에 결재 권한을 더하면 스태프(구매 요청 보유)에게 새 쌍이 생기므로 편집 전체가 거부된다.
	_, err := e.svc.UpdateRole(ctx, e.admin, role.ID, role.Version, "재고 조회", "", []string{string(contract.PermInventoryRead), string(contract.PermApprovalDecide)}, "")
	if !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("새 쌍을 만드는 역할 편집은 차단: %v", err)
	}
	if e.perms(t, s.UserID).Has(contract.PermApprovalDecide) || e.n(t, `SELECT COUNT(*) FROM role_permissions rp JOIN roles r ON r.id = rp.role_id WHERE r.id = ? AND rp.permission = ?`, role.ID, string(contract.PermApprovalDecide)) != 0 {
		t.Fatal("차단되면 역할 편집이 롤백되어야 한다")
	}
	if e.n(t, `SELECT COUNT(*) FROM sod_violations WHERE rule_code = 'hold.purchase_decide' AND result = 'blocked'`) != 1 {
		t.Fatal("차단 기록이 없다")
	}
}

func TestSelfGrantBlockedByDefault(t *testing.T) {
	e := newEnv(t)
	e.withSoD(t)
	ctx := context.Background()
	role, _ := e.svc.CreateRole(ctx, e.admin, "재고 조회", "", []string{string(contract.PermInventoryRead)})
	_, err := e.svc.AddGrant(ctx, e.admin, role.ID, GrantInput{TargetType: "user", TargetID: e.admin.UserID})
	if !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("자기 자신에게 부여는 차단: %v", err)
	}
	if e.n(t, `SELECT COUNT(*) FROM sod_violations WHERE rule_code = 'role_self_grant' AND result = 'blocked'`) != 1 {
		t.Fatal("차단 기록이 없다")
	}
	// 모드를 끄면 허용된다.
	e.setMode(t, "role_self_grant", "off")
	if _, err := e.svc.AddGrant(ctx, e.admin, role.ID, GrantInput{TargetType: "user", TargetID: e.admin.UserID}); err != nil {
		t.Fatalf("끈 규칙: %v", err)
	}
}

func TestRequestApproveByRequesterIsLockedBlocked(t *testing.T) {
	e := newEnv(t)
	e.withSoD(t)
	ctx := context.Background()
	s := e.staff(t, "s@x")
	risky, _ := e.svc.CreateRole(ctx, e.admin, "감사 검증자", "", []string{"audit:verify", "audit:read"})
	res, err := e.svc.AddGrant(ctx, e.admin, risky.ID, GrantInput{TargetType: "user", TargetID: s.UserID, Reason: "감사 담당 지정"})
	if err != nil || res.Request == nil {
		t.Fatalf("%+v %v", res, err)
	}
	if err := e.svc.Approve(ctx, e.admin, res.Request.ID); !errors.Is(err, contract.ErrInvalidState) {
		t.Fatalf("요청자 본인 승인은 잠금 차단: %v", err)
	}
	if e.n(t, `SELECT COUNT(*) FROM sod_violations WHERE rule_code = 'role_request_approve' AND result = 'blocked'`) != 1 {
		t.Fatal("차단 기록이 없다")
	}
	if err := e.svc.Approve(ctx, e.boss, res.Request.ID); err != nil {
		t.Fatalf("다른 관리자는 승인: %v", err)
	}
}

// role_self_grant 우회 방지: 내가 속한 조직(하위 포함)·직위·조직장 대상 부여로 내게 권한이 들어오는 경우도 직접 부여와 같이 판정한다.
func TestSelfGrantViaUnitRankHeadBlocked(t *testing.T) {
	e := newEnv(t)
	e.withSoD(t)
	ctx := context.Background()
	ids := map[string]int64{}
	for _, em := range []string{"lead@x", "m1@x", "m2@x", "act@x", "gone@x"} {
		ids[em] = addUser(t, e.db, em, "")
	}
	e.org(t)
	role, _ := e.svc.CreateRole(ctx, e.admin, "조직 시험 역할", "", []string{string(contract.PermReportRead)})
	// 행위자는 role:manage 를 가진 관리자 템플릿 권한이면서 팀2 소속(m2, 사원)이다. 팀1 의 조직장은 lead@x 다.
	m2 := contract.Principal{UserID: ids["m2@x"], Name: "m2", Role: contract.RoleAdmin, Channel: contract.ChannelWeb, Auth: contract.AuthSession}
	lead := contract.Principal{UserID: ids["lead@x"], Name: "lead", Role: contract.RoleAdmin, Channel: contract.ChannelWeb, Auth: contract.AuthSession}
	blocked := func(who contract.Principal, in GrantInput, what string) {
		t.Helper()
		if _, err := e.svc.AddGrant(ctx, who, role.ID, in); !errors.Is(err, contract.ErrForbidden) {
			t.Fatalf("%s: 자기에게 들어가는 부여는 차단되어야 한다: %v", what, err)
		}
	}
	ok := func(who contract.Principal, in GrantInput, what string) {
		t.Helper()
		if _, err := e.svc.AddGrant(ctx, who, role.ID, in); err != nil {
			t.Fatalf("%s: 나와 무관한 부여는 통과해야 한다: %v", what, err)
		}
	}
	blocked(m2, GrantInput{TargetType: "unit", TargetID: 5}, "내 조직")
	blocked(m2, GrantInput{TargetType: "unit", TargetID: 3, IncludeDescendants: true}, "내 조직의 상위(하위 포함)")
	blocked(m2, GrantInput{TargetType: "rank", TargetID: 1}, "내 직위")
	blocked(lead, GrantInput{TargetType: "unit_head", UnitKind: "team"}, "내가 조직장인 종류")
	if e.n(t, `SELECT COUNT(*) FROM role_grants WHERE role_id = ?`, role.ID) != 0 || e.perms(t, ids["m2@x"]).Has(contract.PermReportRead) {
		t.Fatal("차단되면 부여가 롤백되어야 한다")
	}
	if e.n(t, `SELECT COUNT(*) FROM sod_violations WHERE rule_code = 'role_self_grant' AND result = 'blocked'`) != 4 {
		t.Fatal("차단 기록이 4건이어야 한다")
	}
	// 나를 포함하지 않는 부여(하위 제외 상위 조직, 다른 팀, 다른 직위)는 통과한다.
	ok(m2, GrantInput{TargetType: "unit", TargetID: 3}, "하위 제외 상위 조직")
	ok(m2, GrantInput{TargetType: "unit", TargetID: 4}, "다른 팀")
	ok(m2, GrantInput{TargetType: "rank", TargetID: 3}, "다른 직위")
	// 모드를 끄면 허용된다.
	e.setMode(t, "role_self_grant", "off")
	ok(m2, GrantInput{TargetType: "unit", TargetID: 5}, "규칙이 꺼진 뒤")
}
