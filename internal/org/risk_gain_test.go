package org

import (
	"errors"
	"strings"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/access"
	"github.com/choibongseok/procure-ledger/internal/contract"
)

// B33 재판정 3: 발령 결과로 위험 권한이 새로 생기면 발령자는 role:manage 가 있어야 한다(입사·재입사·전보·직위 변경·조직장 보임·정정·미래 효력일).
type riskEnv struct {
	*env
	low            contract.Principal // org:manage·user:manage 만 있고 role:manage 는 없는 발령자
	co, t1, t2, t3 int64
	staff, other   int64
	ledgerRoleKey  string
}

func newRiskEnv(t *testing.T) *riskEnv {
	e := newEnv(t)
	r := &riskEnv{env: e, ledgerRoleKey: "r_ledger"}
	_, _, dep, t1, t2 := e.baseOrg()
	r.t1, r.t2 = t1, t2
	r.t3 = e.reg(Command{Kind: KindUnitCreate, Code: "T3", Name: "구매3팀", UnitKind: contract.OrgTeam, ParentID: dep, Effective: "2020-01-01", Reason: "설립"}).UnitID
	r.staff = e.hire("직원", "s1@pl.test", r.t1, contract.RoleStaff, "2024-01-02")
	r.other = e.hire("동료", "s2@pl.test", r.t1, contract.RoleStaff, "2024-01-02")
	if _, err := e.db.Exec(`INSERT INTO roles (key, name, kind, created_at, updated_at) VALUES ('r_ledger','전표 입력 담당','custom','a','a')`); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.Exec(`INSERT INTO role_permissions (role_id, permission) SELECT id, 'ledger:write' FROM roles WHERE key='r_ledger'`); err != nil {
		t.Fatal(err)
	}
	r.low = contract.Principal{UserID: r.other, Name: "발령 담당", Channel: contract.ChannelWeb, Perms: contract.NewPermSet(contract.PermOrgManage, contract.PermUserManage)}
	return r
}

func (r *riskEnv) grant(typ string, target int64, kind string) {
	r.t.Helper()
	if _, err := r.db.Exec(`INSERT INTO role_grants (role_id, target_type, target_id, unit_kind, include_descendants, source, reason, created_at) SELECT id, ?, ?, ?, 0, 'manual', '시험', 'a' FROM roles WHERE key = ?`, typ, target, kind, r.ledgerRoleKey); err != nil {
		r.t.Fatal(err)
	}
	if err := access.Recompute(r.ctx, r.db, contract.Day("2026-03-10")); err != nil {
		r.t.Fatal(err)
	}
}

func (r *riskEnv) deny(c Command, who contract.Principal) error {
	r.t.Helper()
	_, err := r.svc.Register(r.ctx, who, c)
	var rg *RiskGainError
	if !errors.As(err, &rg) || !errors.Is(err, contract.ErrValidation) || !strings.Contains(err.Error(), "위험 권한이 새로 생기는 발령은 역할 관리 권한이 필요합니다") {
		r.t.Fatalf("거부되어야 함: %v", err)
	}
	return err
}

func (r *riskEnv) has(uid int64, perm string) bool {
	var n int
	_ = r.db.QueryRow(`SELECT COUNT(*) FROM user_effective_permissions WHERE user_id = ? AND permission = ?`, uid, perm).Scan(&n)
	return n > 0
}

func TestTransferGainingRiskNeedsRoleManage(t *testing.T) {
	r := newRiskEnv(t)
	r.grant("unit", r.t2, "") // 구매2팀에 전표 입력 역할(위험 권한 ledger:write)
	c := Command{Kind: KindTransfer, UserID: r.other, ToUnit: r.t2, Effective: "2026-03-10"}
	// 미리보기는 발령자와 무관하게 경고를 보인다.
	pv, err := r.svc.Preview(r.ctx, r.low, c)
	if err != nil || len(pv.Warnings) == 0 || !strings.Contains(strings.Join(pv.Warnings, " "), "위험 권한(전표 입력·승인 요청)이 생깁니다") {
		t.Fatalf("미리보기 경고: %+v %v", pv, err)
	}
	// 자기 자신을 위험 권한이 부여된 조직으로 전보: 거부, 이동하지 않고 권한도 없고 거부 감사가 남는다.
	r.deny(c, r.low)
	if r.has(r.other, "ledger:write") {
		t.Fatal("거부됐는데 권한이 생김")
	}
	var denied int
	if err := r.db.QueryRow(`SELECT COUNT(*) FROM audit_logs WHERE action='hr.transfer' AND result='denied' AND detail LIKE 'gained_risk:%ledger:write%'`).Scan(&denied); err != nil || denied != 1 {
		t.Fatalf("거부 감사 %d건 %v", denied, err)
	}
	// 미래 효력일 발령도 등록 시점에 예상 상태로 판정한다.
	r.deny(Command{Kind: KindTransfer, UserID: r.other, ToUnit: r.t2, Effective: "2026-04-01"}, r.low)
	// role:manage 가 있으면 허용하고 감사 상세에 gained_risk 가 남는다.
	if _, err := r.svc.Register(r.ctx, r.admin, c); err != nil {
		t.Fatalf("role:manage 발령자: %v", err)
	}
	if !r.has(r.other, "ledger:write") {
		t.Fatal("허용된 발령인데 권한이 없음")
	}
	var diff string
	_ = r.db.QueryRow(`SELECT diff FROM audit_logs WHERE action='hr.transfer' AND result='success' ORDER BY seq DESC LIMIT 1`).Scan(&diff)
	if !strings.Contains(diff, "gained_risk") || !strings.Contains(diff, "ledger:write") {
		t.Fatalf("허용 감사의 gained_risk: %s", diff)
	}
	// 위험 권한이 새로 생기지 않는 전보는 role:manage 없이도 된다(이미 가진 권한은 새로 생긴 것이 아니다).
	if _, err := r.svc.Register(r.ctx, r.low, Command{Kind: KindTransfer, UserID: r.staff, ToUnit: r.t3, Effective: "2026-03-10"}); err != nil {
		t.Fatalf("무위험 전보: %v", err)
	}
}

func TestRehireRestoringRiskNeedsRoleManage(t *testing.T) {
	r := newRiskEnv(t)
	fin := r.hire("재무", "fin@pl.test", r.t1, contract.RoleFinance, "2023-01-02") // finance 템플릿에 위험 권한(전표·지급)이 있다
	r.reg(Command{Kind: KindLeave, UserID: fin, LastDay: "2026-03-09", LeaveKind: "자진", Reason: "퇴사 처리", Confirm: true})
	if r.has(fin, "ledger:write") {
		t.Fatal("퇴사 뒤에도 권한이 남음")
	}
	r.deny(Command{Kind: KindHire, UserID: fin, ToUnit: r.t1, Rank: 1, Effective: "2026-03-10", Reason: "재입사", Confirm: true}, r.low)
	if _, err := r.svc.Register(r.ctx, r.admin, Command{Kind: KindHire, UserID: fin, ToUnit: r.t1, Rank: 1, Effective: "2026-03-10", Reason: "재입사", Confirm: true}); err != nil {
		t.Fatalf("role:manage 발령자의 재입사: %v", err)
	}
	if !r.has(fin, "ledger:write") {
		t.Fatal("재입사 뒤 권한이 부활하지 않음")
	}
}

func TestRankChangeAndHeadSetGainingRiskNeedRoleManage(t *testing.T) {
	r := newRiskEnv(t)
	r.grant("rank", 2, "")
	r.deny(Command{Kind: KindRankChange, UserID: r.other, Rank: 2, Effective: "2026-03-10"}, r.low)
	r.grant("unit_head", 0, "team")
	r.deny(Command{Kind: KindHeadSet, UnitID: r.t1, UserID: r.other, Effective: "2026-03-10"}, r.low)
	if r.has(r.other, "ledger:write") {
		t.Fatal("거부됐는데 권한이 생김")
	}
	// 정정(취소 + 새 명령)도 같은 규칙이다: 무위험 전보를 위험 조직 전보로 정정하려 하면 거부된다.
	r.grant("unit", r.t2, "")
	o := r.reg(Command{Kind: KindTransfer, UserID: r.staff, ToUnit: r.t3, Effective: "2026-03-10"})
	_, err := r.svc.Correct(r.ctx, r.low, o.EventIDs[0], Command{Kind: KindTransfer, UserID: r.staff, ToUnit: r.t2, Effective: "2026-03-10", Reason: "정정"})
	var rg *RiskGainError
	if !errors.As(err, &rg) {
		t.Fatalf("정정: %v", err)
	}
	var denied int
	_ = r.db.QueryRow(`SELECT COUNT(*) FROM audit_logs WHERE action='hr.event_correct' AND result='denied'`).Scan(&denied)
	if denied != 1 {
		t.Fatalf("정정 거부 감사 %d건", denied)
	}
}

// 퇴사 발령을 취소해 위험 권한이 되살아나는 것도 같은 규칙이다.
func TestCancelRestoringRiskNeedsRoleManage(t *testing.T) {
	r := newRiskEnv(t)
	fin := r.hire("재무", "fin@pl.test", r.t1, contract.RoleFinance, "2023-01-02")
	o := r.reg(Command{Kind: KindLeave, UserID: fin, LastDay: "2026-03-09", LeaveKind: "자진", Reason: "퇴사 처리", Confirm: true})
	if r.has(fin, "ledger:write") {
		t.Fatal("퇴사 뒤에도 권한이 남음")
	}
	err := r.svc.Cancel(r.ctx, r.low, o.EventIDs[0], "잘못 등록", true)
	var rg *RiskGainError
	if !errors.As(err, &rg) {
		t.Fatalf("취소: %v", err)
	}
	var denied int
	_ = r.db.QueryRow(`SELECT COUNT(*) FROM audit_logs WHERE action='hr.event_cancel' AND result='denied'`).Scan(&denied)
	if denied != 1 {
		t.Fatalf("취소 거부 감사 %d건", denied)
	}
	if err := r.svc.Cancel(r.ctx, r.admin, o.EventIDs[0], "잘못 등록", true); err != nil {
		t.Fatalf("role:manage 발령자의 취소: %v", err)
	}
	if !r.has(fin, "ledger:write") {
		t.Fatal("취소 뒤 권한이 부활하지 않음")
	}
}
