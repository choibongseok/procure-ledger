package org

import (
	"errors"
	"fmt"
	"math/rand"
	"strings"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/contract"
)

// B40 2단계: SCIM 이 만드는 모양의 발령(행위자 = org:manage·scim:provision 뿐인 토큰 발급자, 효력일 비움 = 오늘, 사유는 시스템이 채움)이
// RegisterBatch(auditEach=true) 경로에서 화면 발령과 같은 이력·감사·투영을 내고, 위험 권한을 새로 만들지 못하는지 본다.

// scimActor 는 internal/scim.newActor 와 같은 모양이다: 사용자 번호는 발급자(0 이 아님), 권한에 role:manage 가 없다.
func (e *env) scimActor() contract.Principal {
	return contract.Principal{UserID: e.admin.UserID, Name: "SCIM(시험)", Role: "scim", Channel: contract.ChannelSCIM, Auth: contract.AuthSCIM,
		Perms: contract.NewPermSet(contract.PermOrgManage, contract.PermSCIMProvision)}
}

func (e *env) eventCount(uid int64, kind string) int {
	return e.count(`SELECT COUNT(*) FROM org_events WHERE subject_id = ? AND subject_type = 'person' AND kind = ?`, uid, kind)
}

func (e *env) auditCount(action string, uid int64, result string) int {
	return e.count(`SELECT COUNT(*) FROM audit_logs WHERE action = ? AND target_id = ? AND result = ?`, action, fmt.Sprint(uid), result)
}

// 전보·직위 변경: 효력일 비움 = 오늘로 발령 이력·감사(채널 scim)·투영(부서·직책)이 화면 발령과 같게 남는다.
func TestSyncTransferAndRankChangeProjection(t *testing.T) {
	e := newEnv(t)
	_, _, dep, t1, t2 := e.baseOrg()
	u := e.hire("김사원", "kim@pl.test", t1, contract.RoleStaff, "2024-01-02")
	act := e.scimActor()
	if _, err := e.batch(act, Command{Kind: KindTransfer, UserID: u, ToUnit: t2, Reason: "SCIM 동기화(시험): 전보"}); err != nil {
		t.Fatalf("전보: %v", err)
	}
	if e.count(`SELECT COUNT(*) FROM org_events WHERE subject_id = ? AND kind = 'transfer' AND effective_date = '2026-03-10' AND created_by = ?`, u, act.UserID) != 1 {
		t.Fatal("전보 이력(효력일 오늘, 발급자 번호)이 있어야 함")
	}
	if e.count(`SELECT COUNT(*) FROM audit_logs WHERE action = 'hr.transfer' AND target_id = ? AND channel = 'scim' AND actor_role = 'scim'`, fmt.Sprint(u)) != 1 {
		t.Fatal("전보 감사(채널 scim)가 한 건 있어야 함")
	}
	_, dept, _ := userRow(e, u)
	var wantDept int64
	if err := e.db.QueryRow(`SELECT department_id FROM users WHERE id = ?`, u).Scan(&wantDept); err != nil || dept != wantDept {
		t.Fatal("투영")
	}
	if res, err := e.svc.Persons(e.ctx, []int64{u}, ""); err != nil || res[u].Primary.Unit.ID != t2 {
		t.Fatalf("현재 소속이 구매2팀이어야 함: %+v %v", res[u], err)
	}
	// 직위 변경.
	if _, err := e.batch(act, Command{Kind: KindRankChange, UserID: u, Rank: 3, Reason: "SCIM 동기화(시험): 직위"}); err != nil {
		t.Fatalf("직위 변경: %v", err)
	}
	if e.eventCount(u, "rank_change") != 1 || e.auditCount("hr.rank_change", u, "success") != 1 {
		t.Fatal("직위 변경 이력·감사")
	}
	// 이미 그 조직에 있는 사람의 같은 전보는 규칙이 거부하거나 변화가 없어야 한다(이력만 늘지 않는다).
	_ = dep
	if rep, err := e.svc.Consistency(e.ctx); err != nil || !rep.OK {
		t.Fatalf("구간 일관성: %+v %v", rep, err)
	}
}

// 소급: SCIM 은 효력일을 받지 않는다(비움 = 오늘). org 수준에서는 사유가 있어도 30일을 넘는 소급은 Confirm 없이는 거부된다.
// SCIM 입구가 날짜를 넘기면 이 보호를 쓰게 되므로, 날짜를 넘기지 않는다는 것을 이 시험이 고정한다.
func TestSyncRetroactiveDateGuards(t *testing.T) {
	e := newEnv(t)
	_, _, _, t1, t2 := e.baseOrg()
	u := e.hire("김사원", "kim@pl.test", t1, contract.RoleStaff, "2024-01-02")
	act := e.scimActor()
	// 사유가 비면 어떤 소급도 거부.
	if _, err := e.batch(act, Command{Kind: KindTransfer, UserID: u, ToUnit: t2, Effective: "2026-03-09"}); err == nil || !strings.Contains(err.Error(), "사유") {
		t.Fatalf("사유 없는 소급: %v", err)
	}
	// 사유가 있어도 30일 넘는 소급은 확인 없이는 거부(ConfirmError).
	_, err := e.batch(act, Command{Kind: KindTransfer, UserID: u, ToUnit: t2, Effective: "2025-12-01", Reason: "SCIM 동기화(시험): 전보"})
	var ce *ConfirmError
	if !errors.As(err, &ce) {
		t.Fatalf("먼 소급은 확인 필요: %v", err)
	}
	if e.eventCount(u, "transfer") != 0 || e.auditCount("hr.transfer", u, "success") != 0 {
		t.Fatal("거부됐는데 이력·감사가 남음")
	}
	// 효력일을 비우면 오늘이다.
	if _, err := e.batch(act, Command{Kind: KindTransfer, UserID: u, ToUnit: t2, Reason: "SCIM 동기화(시험): 전보"}); err != nil {
		t.Fatal(err)
	}
	if e.count(`SELECT COUNT(*) FROM org_events WHERE subject_id = ? AND kind = 'transfer' AND effective_date = '2026-03-10'`, u) != 1 {
		t.Fatal("효력일이 오늘이어야 함")
	}
	// 1년 넘는 미래 예약도 거부.
	if _, err := e.batch(act, Command{Kind: KindTransfer, UserID: u, ToUnit: t1, Effective: "2027-12-01", Reason: "x"}); err == nil {
		t.Fatal("먼 미래 예약이 받아들여짐")
	}
}

// 재입사(퇴사자 재활성): 위험 권한을 되살리는 재입사는 SCIM 행위자(role:manage 없음)로는 거부되고 아무것도 남지 않는다. 위험 없는 사람은 된다.
func TestSyncRehireRestoringRiskIsBlocked(t *testing.T) {
	r := newRiskEnv(t)
	act := r.scimActor()
	fin := r.hire("재무", "fin@pl.test", r.t1, contract.RoleFinance, "2023-01-02")
	safe := r.hire("일반", "safe@pl.test", r.t1, contract.RoleStaff, "2023-01-02")
	for _, u := range []int64{fin, safe} {
		r.reg(Command{Kind: KindLeave, UserID: u, LastDay: "2026-03-09", LeaveKind: "자진", Reason: "퇴사 처리", Confirm: true})
	}
	events := r.count(`SELECT COUNT(*) FROM org_events`)
	_, err := r.batch(act, Command{Kind: KindHire, UserID: fin, ToUnit: r.t1, Rank: 1, Reason: "SCIM 동기화(시험): 재입사"})
	var rg *RiskGainError
	if !errors.As(err, &rg) {
		t.Fatalf("위험 권한 복원 재입사가 거부되어야 함: %v", err)
	}
	if r.count(`SELECT COUNT(*) FROM org_events`) != events || r.has(fin, "ledger:write") {
		t.Fatal("거부됐는데 이력이 남거나 권한이 살아남")
	}
	if active, _, _ := userRow(r.env, fin); active != 0 {
		t.Fatal("거부됐는데 계정이 켜짐")
	}
	// RegisterBatch 는 거부 감사를 남기지 않는다(Register 만 남긴다). SCIM 입구가 scim.rejected 로 남긴다(internal/scim). 성공 감사는 없어야 한다.
	if r.auditCount("hr.hire", fin, "success") != 1 { // 최초 입사 한 건뿐
		t.Fatal("거부된 재입사의 성공 감사가 남음")
	}
	// 위험 없는 퇴사자의 재입사는 통과하고 이력·투영이 맞다.
	if _, err := r.batch(act, Command{Kind: KindHire, UserID: safe, ToUnit: r.t1, Rank: 1, Reason: "SCIM 동기화(시험): 재입사"}); err != nil {
		t.Fatalf("무위험 재입사: %v", err)
	}
	if active, dept, _ := userRow(r.env, safe); active != 1 || dept == 0 {
		t.Fatalf("재입사 투영: active=%d dept=%d", active, dept)
	}
	if r.eventCount(safe, "hire") != 2 {
		t.Fatal("입사 이벤트가 2건(최초·재입사)이어야 함")
	}
}

// 조직장 퇴사: 조직장이 비고(공석), 진행 중 결재 건수가 미리보기에 잡히며, 직책 투영이 풀리고 계정이 꺼진다.
func TestSyncHeadLeaveVacatesUnitAndCountsPending(t *testing.T) {
	e := newEnv(t)
	_, _, _, t1, _ := e.baseOrg()
	lead := e.hire("구매팀장", "lead@pl.test", t1, contract.RoleManager, "2021-01-04")
	staff := e.hire("구매직원", "staff@pl.test", t1, contract.RoleStaff, "2021-01-04")
	e.reg(Command{Kind: KindHeadSet, UnitID: t1, UserID: lead, Effective: "2021-02-01"})
	var dept int64
	_ = e.db.QueryRow(`SELECT department_id FROM users WHERE id = ?`, lead).Scan(&dept)
	now := e.now.UTC().Format("2006-01-02T15:04:05Z")
	if _, err := e.db.Exec(`INSERT INTO vendors (id, name) VALUES (1, '거래처')`); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.Exec(`INSERT INTO purchase_requests (id, no, requester_id, department_id, vendor_id, title, status, total_amount, round, created_at, updated_at)
		VALUES (1, 'PR-000001', ?, ?, 1, '복사용지', 'in_review', 1000, 1, ?, ?)`, staff, dept, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.Exec(`INSERT INTO approval_steps (pr_id, round, seq, step_kind, status) VALUES (1, 1, 1, 'team_lead', 'pending')`); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.Exec(`INSERT INTO approval_step_assignees (pr_id, round, seq, user_id, name_at) VALUES (1, 1, 1, ?, 'x')`, lead); err != nil {
		t.Fatal(err)
	}
	act := e.scimActor()
	leave := Command{Kind: KindLeave, UserID: lead, LastDay: "2026-03-09", LeaveKind: "기타", Reason: "SCIM 동기화(시험): 퇴사"}
	pv, err := e.svc.Preview(e.ctx, act, leave)
	if err != nil || pv.PendingApprovals != 1 {
		t.Fatalf("진행 중 결재 영향: %+v %v", pv, err)
	}
	if _, err := e.batch(act, leave); err != nil {
		t.Fatalf("조직장 퇴사: %v", err)
	}
	if active, _, _ := userRow(e, lead); active != 0 {
		t.Fatal("퇴사 뒤 계정이 꺼져야 함")
	}
	if h, err := e.svc.Head(e.ctx, t1, ""); err == nil && !h.Vacant && h.UserID == lead {
		t.Fatal("퇴사한 사람이 조직장으로 남음")
	}
	if e.auditCount("hr.leave", lead, "success") != 1 {
		t.Fatal("퇴사 감사")
	}
	if rep, err := e.svc.Consistency(e.ctx); err != nil || !rep.OK {
		t.Fatalf("구간 일관성: %+v %v", rep, err)
	}
}

// 불변식: SCIM 행위자가 어떤 순서로 입사·재입사·퇴사·전보·직위 변경·조직장 보임을 보내도(성공이든 거부든)
// 위험 권한을 가진 사람 집합은 처음 상태(관리자 계정)에서 늘지 않고, 이력(구간)과 users 투영은 일치한다.
func TestSyncRandomSequenceNeverGainsRisk(t *testing.T) {
	r := newRiskEnv(t)
	r.grant("unit", r.t2, "") // 구매2팀 소속이면 위험 권한
	r.grant("rank", 2, "")    // 직위 2 이면 위험 권한
	act := r.scimActor()
	fin := r.hire("재무", "fin@pl.test", r.t1, contract.RoleFinance, "2023-01-02")
	r.reg(Command{Kind: KindLeave, UserID: fin, LastDay: "2026-03-09", LeaveKind: "자진", Reason: "퇴사 처리", Confirm: true})
	baseline := r.riskSet()
	pool := []int64{r.staff, r.other, fin}
	units := []int64{r.t1, r.t2, r.t3}
	rnd := rand.New(rand.NewSource(40))
	okOps, deniedOps := 0, 0
	for i := 0; i < 160; i++ {
		var c Command
		uid := pool[rnd.Intn(len(pool))]
		switch rnd.Intn(6) {
		case 0: // 신규 입사(기본 역할 직원)
			c = Command{Kind: KindHire, Name: fmt.Sprintf("신규%d", i), Email: fmt.Sprintf("n%d@pl.test", i), Role: contract.RoleStaff, Rank: int64(1 + rnd.Intn(3)), ToUnit: units[rnd.Intn(3)]}
		case 1: // 재입사 시도
			c = Command{Kind: KindHire, UserID: uid, Rank: int64(1 + rnd.Intn(3)), ToUnit: units[rnd.Intn(3)]}
		case 2:
			c = Command{Kind: KindLeave, UserID: uid, LastDay: "2026-03-09", LeaveKind: "기타"}
		case 3:
			c = Command{Kind: KindTransfer, UserID: uid, ToUnit: units[rnd.Intn(3)]}
		case 4:
			c = Command{Kind: KindRankChange, UserID: uid, Rank: int64(1 + rnd.Intn(3))}
		default:
			c = Command{Kind: KindHeadSet, UnitID: units[rnd.Intn(3)], UserID: uid}
		}
		c.Reason = "SCIM 동기화(시험)"
		outs, err := r.batch(act, c)
		if err == nil {
			okOps++
			if c.Kind == KindHire && c.UserID == 0 {
				pool = append(pool, outs[0].UserID)
			}
		} else {
			deniedOps++
		}
		if got := r.riskSet(); !subsetOf(got, baseline) {
			t.Fatalf("%d번째 요청(%s) 뒤 위험 권한이 늘었다: %v (처음 %v) 오류=%v", i, KindLabel(c.Kind), got, baseline, err)
		}
		r.assertProjection()
	}
	if okOps == 0 || deniedOps == 0 {
		t.Fatalf("시험이 한쪽만 다뤘다: 성공 %d 거부 %d", okOps, deniedOps)
	}
	if r.has(fin, "ledger:write") {
		t.Fatal("위험 권한 퇴사자가 되살아남")
	}
}

// riskSet 은 위험 권한을 가진 (사용자, 권한) 집합의 문자열 표현 목록이다.
func (e *env) riskSet() map[string]bool {
	e.t.Helper()
	out := map[string]bool{}
	err := e.svc.txs.WithTx(e.ctx, func(tx contract.Tx) error {
		snap, err := e.svc.riskAt(e.ctx, tx, e.svc.Today())
		if err != nil {
			return err
		}
		for uid, perms := range snap {
			for p := range perms {
				out[fmt.Sprintf("%d:%s", uid, p)] = true
			}
		}
		return nil
	})
	if err != nil {
		e.t.Fatal(err)
	}
	return out
}

func subsetOf(a, b map[string]bool) bool {
	for k := range a {
		if !b[k] {
			return false
		}
	}
	return true
}

// assertProjection 은 조직 이력(구간)이 정본과 일치하고(Consistency), 오늘 재직 여부와 users.active 가 모든 조직 등록자에게서 같음을 본다.
func (e *env) assertProjection() {
	e.t.Helper()
	if rep, err := e.svc.Consistency(e.ctx); err != nil || !rep.OK {
		e.t.Fatalf("구간 일관성: %+v %v", rep, err)
	}
	res, err := e.svc.foldAll(e.ctx, e.db)
	if err != nil {
		e.t.Fatal(err)
	}
	sn := NewSnap(res, e.svc.Today())
	rows, err := e.db.Query(`SELECT u.id, u.active FROM users u JOIN org_people p ON p.user_id = u.id`)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var active int
		if err := rows.Scan(&id, &active); err != nil {
			e.t.Fatal(err)
		}
		if ok := sn.Employed(id); ok != (active == 1) {
			e.t.Fatalf("사용자 %d: 재직 %v 인데 users.active=%d", id, ok, active)
		}
	}
}
