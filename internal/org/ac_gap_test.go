package org

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/contract"
	caudit "github.com/choibongseok/procure-ledger/internal/contract/audit"
)

// AC-07: 상위 조직이 폐지되어 있으면 하위 조직을 재개할 수 없고 문구가 안내한다. 상위를 먼저 재개하면 된다.
func TestReopenRejectedWhileParentClosed(t *testing.T) {
	e := newEnv(t)
	_, _, dep, t1, _ := e.baseOrg()
	e.reg(Command{Kind: KindUnitClose, UnitID: t1, Effective: "2026-03-11", Reason: "통합"})
	// 하위(팀)가 이미 폐지된 뒤 부서를 폐지하려면 남은 하위가 폐지 상태이거나 없어야 한다. 구매2팀도 폐지한다.
	var t2 int64
	if err := e.db.QueryRow(`SELECT unit_id FROM org_unit_spans WHERE name = '구매2팀' LIMIT 1`).Scan(&t2); err != nil {
		t.Fatal(err)
	}
	e.reg(Command{Kind: KindUnitClose, UnitID: t2, Effective: "2026-03-11", Reason: "통합"})
	e.reg(Command{Kind: KindUnitClose, UnitID: dep, Effective: "2026-03-12", Reason: "통합"})
	err := e.regErr(Command{Kind: KindUnitReopen, UnitID: t1, Effective: "2026-03-13", Reason: "복원"})
	if !errors.Is(err, contract.ErrValidation) || !strings.Contains(err.Error(), "상위 조직이 폐지되어 있어 재개할 수 없습니다") {
		t.Fatalf("부모 폐지 중 재개: %v", err)
	}
	before := e.count(`SELECT COUNT(*) FROM org_events`)
	e.reg(Command{Kind: KindUnitReopen, UnitID: dep, Effective: "2026-03-13", Reason: "복원"})
	e.reg(Command{Kind: KindUnitReopen, UnitID: t1, Effective: "2026-03-14", Reason: "복원"})
	if e.count(`SELECT COUNT(*) FROM org_events`) != before+2 {
		t.Fatal("상위를 먼저 재개한 뒤에는 하위도 재개되어야 함")
	}
}

// AC-52: 진행 중인 결재 건이 있는 사람의 퇴사 미리보기에는 건수가 나온다. 건이 없으면 0 이다.
func TestLeavePreviewCountsPendingApprovals(t *testing.T) {
	e := newEnv(t)
	_, div, dep, t1, t2 := e.baseOrg()
	lead := e.hire("구매팀장", "lead@pl.test", t1, contract.RoleManager, "2021-01-04")
	staff := e.hire("구매직원", "staff@pl.test", t1, contract.RoleStaff, "2021-01-04")
	e.reg(Command{Kind: KindHeadSet, UnitID: t1, UserID: lead, Effective: "2021-02-01"})
	if _, _, pos := userRow(e, lead); pos != "team_lead" {
		t.Fatalf("투영된 직책 %q", pos)
	}
	_ = dep
	pv, err := e.svc.Preview(e.ctx, e.admin, Command{Kind: KindLeave, UserID: lead, LastDay: "2026-03-31", LeaveKind: "자진"})
	if err != nil || pv.PendingApprovals != 0 {
		t.Fatalf("건이 없을 때: %+v %v", pv, err)
	}
	var dept int64
	_ = e.db.QueryRow(`SELECT department_id FROM users WHERE id = ?`, lead).Scan(&dept)
	if _, err := e.db.Exec(`INSERT INTO vendors (id, name) VALUES (1, '거래처')`); err != nil {
		t.Fatalf("거래처: %v", err)
	}
	now := e.now.UTC().Format("2006-01-02T15:04:05Z")
	// 본부장(옛 규칙의 dept_head 후보가 아님)도 배정자이면 세어진다. 재지정된 결재자의 건도 센다.
	head := e.hire("구매본부장", "div@pl.test", div, contract.RoleManager, "2020-01-06")
	other := e.hire("재지정받은사람", "re@pl.test", t2, contract.RoleManager, "2021-01-04")
	for i := 1; i <= 3; i++ {
		if _, err := e.db.Exec(`INSERT INTO purchase_requests (id, no, requester_id, department_id, vendor_id, title, status, total_amount, round, created_at, updated_at)
			VALUES (?, ?, ?, ?, 1, '복사용지', 'in_review', 1000, 1, ?, ?)`, i, fmt.Sprintf("PR-%06d", i), staff, dept, now, now); err != nil {
			t.Fatal(err)
		}
		if _, err := e.db.Exec(`INSERT INTO approval_steps (pr_id, round, seq, step_kind, status) VALUES (?, 1, 1, ?, 'pending')`, i, map[int]string{1: "team_lead", 2: "team_lead", 3: "division_head"}[i]); err != nil {
			t.Fatal(err)
		}
	}
	for pr, u := range map[int]int64{1: lead, 2: lead, 3: head} {
		if _, err := e.db.Exec(`INSERT INTO approval_step_assignees (pr_id, round, seq, user_id, name_at) VALUES (?, 1, 1, ?, 'x')`, pr, u); err != nil {
			t.Fatal(err)
		}
	}
	leave := func(u int64) Preview {
		pv, err := e.svc.Preview(e.ctx, e.admin, Command{Kind: KindLeave, UserID: u, LastDay: "2026-03-31", LeaveKind: "자진"})
		if err != nil {
			t.Fatal(err)
		}
		return pv
	}
	if pv := leave(lead); pv.PendingApprovals != 2 || len(pv.PendingDocs) != 2 || pv.PendingDocs[0].Title == "" || pv.PendingDocs[0].Requester == "" {
		t.Fatalf("팀장 진행 중 2건과 문서 목록: %+v", pv)
	}
	if pv := leave(head); pv.PendingApprovals != 1 {
		t.Fatalf("본부장(구 규칙으로는 0건) 진행 중 1건: %+v", pv)
	}
	if pv := leave(other); pv.PendingApprovals != 0 {
		t.Fatalf("재지정 전 건수 %+v", pv)
	}
	// 관리자가 3번 건의 결재자를 재지정하면 새 결재자의 건이 되고 이전 결재자의 건은 0 이 된다.
	if _, err := e.db.Exec(`INSERT INTO approval_step_reassignments (pr_id, round, seq, from_user_id, from_name_at, to_user_id, to_name_at, cause, reason, actor_id, created_at)
		VALUES (3, 1, 1, ?, 'x', ?, 'y', 'leave', '퇴사', ?, ?)`, head, other, e.admin.UserID, now); err != nil {
		t.Fatal(err)
	}
	if a, b := leave(head).PendingApprovals, leave(other).PendingApprovals; a != 0 || b != 1 {
		t.Fatalf("재지정 뒤: 이전 %d, 새 결재자 %d", a, b)
	}
	// 결재가 끝난 단계는 세지 않는다.
	if _, err := e.db.Exec(`UPDATE approval_steps SET status = 'approved', approver_id = ? WHERE pr_id = 1`, lead); err != nil {
		t.Fatal(err)
	}
	if pv := leave(lead); pv.PendingApprovals != 1 {
		t.Fatalf("끝난 단계 제외: %+v", pv)
	}
	if e.count(`SELECT COUNT(*) FROM org_events WHERE kind = 'leave'`) != 0 {
		t.Fatal("미리보기가 이벤트를 남김")
	}
}

// AC-51: 퇴사한 사람의 과거 감사 기록 행위자 이름에는 "(퇴사)" 가 붙고 재직자·입사 전 사람에게는 붙지 않는다.
func TestLeftUsersMarkedForPastRecords(t *testing.T) {
	e := newEnv(t)
	_, _, _, t1, _ := e.baseOrg()
	gone := e.hire("퇴직자", "gone@pl.test", t1, contract.RoleStaff, "2021-01-04")
	stay := e.hire("재직자", "stay@pl.test", t1, contract.RoleStaff, "2021-01-04")
	future := e.hire("입사예정", "soon@pl.test", t1, contract.RoleStaff, "2026-06-01")
	e.reg(Command{Kind: KindLeave, UserID: gone, LastDay: "2026-03-09", LeaveKind: "자진", Reason: "퇴사 처리"})
	left, err := e.svc.LeftUsers(e.ctx, []int64{gone, stay, future, e.admin.UserID})
	if err != nil {
		t.Fatal(err)
	}
	if !left[gone] || left[stay] || left[future] || left[e.admin.UserID] {
		t.Fatalf("퇴사자 집합 %v", left)
	}
	// 퇴사 효력일 전날(마지막 근무일 당일)에는 아직 재직이다.
	e.reg(Command{Kind: KindLeave, UserID: stay, LastDay: "2026-03-10", LeaveKind: "자진"})
	if left, _ := e.svc.LeftUsers(e.ctx, []int64{stay}); left[stay] {
		t.Fatal("마지막 근무일 당일은 재직")
	}
}

// AC-69a: 감사 기록이 실패하면 발령도 일괄 등록도 반영되지 않는다(같은 트랜잭션).
type failingAudit struct{}

func (failingAudit) Record(context.Context, contract.Tx, caudit.Entry) error {
	return errors.New("감사 기록 실패(시험)")
}

func TestAuditFailureRollsBackRegisterAndBatch(t *testing.T) {
	e := newEnv(t)
	_, _, _, t1, t2 := e.baseOrg()
	u := e.hire("구매직원", "staff@pl.test", t1, contract.RoleStaff, "2021-01-04")
	events := e.count(`SELECT COUNT(*) FROM org_events`)
	audits := len(e.auditActions())
	_, _, _ = userRow(e, u)

	e.svc.audit = failingAudit{}
	if _, err := e.svc.Register(e.ctx, e.admin, Command{Kind: KindTransfer, UserID: u, ToUnit: t2, Effective: "2026-03-10"}); err == nil {
		t.Fatal("감사가 실패했는데 발령이 성공함")
	}
	// 일괄 등록은 호출자의 트랜잭션 안에서 돈다. 감사 오류가 돌아오면 호출자가 그 오류로 끝내 모두 되돌린다.
	if err := (contract.SQLTxManager{DB: e.db}).WithTx(e.ctx, func(tx contract.Tx) error {
		_, err := e.svc.RegisterBatch(e.ctx, tx, e.admin, []Command{
			{Kind: KindTransfer, UserID: u, ToUnit: t2, Effective: "2026-03-10"},
			{Kind: KindHire, Name: "신입", Email: "new@pl.test", ToUnit: t2, Rank: 1, Role: contract.RoleStaff, Effective: "2026-03-10"},
		}, true)
		return err
	}); err == nil {
		t.Fatal("감사가 실패했는데 일괄 등록이 성공함")
	}
	e.svc.audit = e.store
	if e.count(`SELECT COUNT(*) FROM org_events`) != events {
		t.Fatal("감사 실패인데 이벤트가 남음")
	}
	if e.count(`SELECT COUNT(*) FROM users WHERE email = 'new@pl.test'`) != 0 {
		t.Fatal("감사 실패인데 계정이 생김")
	}
	if _, dept, _ := userRow(e, u); dept == t2 {
		t.Fatal("감사 실패인데 투영이 바뀜")
	}
	if len(e.auditActions()) != audits {
		t.Fatal("감사 기록이 늘어남")
	}
	if err := Consistent(e.ctx, e.db); err != nil {
		t.Fatalf("정합성: %v", err)
	}
}

// 입사 효력일 전인 사람(예약 입사)도 인증이 거부된다. 효력일부터는 거부되지 않는다(명세 3.7).
func TestEmploymentDeniedBeforeHireDate(t *testing.T) {
	e := newEnv(t)
	_, _, _, t1, _ := e.baseOrg()
	u := e.hire("입사예정", "soon@pl.test", t1, contract.RoleStaff, "2026-06-01")
	if denied, err := EmploymentDenied(e.ctx, e.db, u, "2026-05-31"); err != nil || !denied {
		t.Fatalf("입사 전날: denied=%v err=%v", denied, err)
	}
	if denied, err := EmploymentDenied(e.ctx, e.db, u, "2026-06-01"); err != nil || denied {
		t.Fatalf("입사 당일: denied=%v err=%v", denied, err)
	}
}

// 직무대행 겹침 문구에 기존 대행자 이름이 들어간다(명세 8.3).
func TestActingOverlapMessageNamesActingUser(t *testing.T) {
	e := newEnv(t)
	_, _, _, t1, _ := e.baseOrg()
	a := e.hire("홍길동", "a@pl.test", t1, contract.RoleStaff, "2021-01-04")
	c := e.hire("김대행", "c@pl.test", t1, contract.RoleStaff, "2021-01-04")
	e.reg(Command{Kind: KindActingSet, UnitID: t1, UserID: a, Effective: "2026-03-10", EndDay: "2026-03-25"})
	err := e.regErr(Command{Kind: KindActingSet, UnitID: t1, UserID: c, Effective: "2026-03-20", EndDay: "2026-03-30"})
	if !strings.Contains(err.Error(), "이미 직무대행이 있습니다(홍길동, 2026. 3. 10.~2026. 3. 25.)") || strings.Contains(err.Error(), "{u:") {
		t.Fatalf("겹침 문구: %v", err)
	}
}

// 재입사: 재직 중인 사람에게 입사를 등록하는 경우와 퇴사 기간 안(퇴사일 이전) 날짜로 재입사하는 경우의 문구가 다르다.
func TestRehireMessagesDiffer(t *testing.T) {
	e := newEnv(t)
	_, _, _, t1, _ := e.baseOrg()
	u := e.hire("구매직원", "s@pl.test", t1, contract.RoleStaff, "2021-01-04")
	if err := e.regErr(Command{Kind: KindHire, UserID: u, ToUnit: t1, Rank: 1, Effective: "2026-03-10"}); !strings.Contains(err.Error(), "이미 재직 중인 사람입니다") {
		t.Fatalf("재직 중 입사: %v", err)
	}
	e.reg(Command{Kind: KindLeave, UserID: u, LastDay: "2026-06-30", LeaveKind: "자진"})
	err := e.regErr(Command{Kind: KindHire, UserID: u, ToUnit: t1, Rank: 1, Effective: "2026-05-01"})
	if !strings.Contains(err.Error(), "퇴사일 이후 날짜로 등록하세요") || strings.Contains(err.Error(), "이미 재직 중") {
		t.Fatalf("퇴사일 이전 재입사: %v", err)
	}
	e.reg(Command{Kind: KindHire, UserID: u, ToUnit: t1, Rank: 1, Effective: "2026-07-02"})
}

// 직위 표 변경 감사에는 바뀐 값만 남는다.
func TestRankAuditDiffHasOnlyChangedValues(t *testing.T) {
	e := newEnv(t)
	ranks, err := e.svc.ListRanks(e.ctx)
	if err != nil || len(ranks) == 0 {
		t.Fatal(err)
	}
	r := ranks[0]
	if err := e.svc.SaveRanks(e.ctx, e.admin, []RankInput{{ID: r.ID, Name: r.Name + "(수정)", Sort: r.Sort, Active: r.Active}}); err != nil {
		t.Fatal(err)
	}
	recs, _ := e.store.List(e.ctx, caudit.Filter{Action: "org.rank_set", Limit: 5})
	if len(recs) != 1 {
		t.Fatalf("감사 %d건", len(recs))
	}
	for _, d := range recs[0].Diff {
		if d.Old == d.New {
			t.Errorf("바뀌지 않은 값이 diff 에 있음: %+v", d)
		}
		if d.Key == "sort_order" {
			t.Errorf("순서는 바뀌지 않았는데 diff 에 있음")
		}
	}
	if len(recs[0].Diff) != 1 || recs[0].Diff[0].Key != "rank_name" {
		t.Fatalf("diff: %+v", recs[0].Diff)
	}
}

// 결재 권한이 없는 역할의 조직장·직무대행자: 직무대행 등록에도 조직장 보임과 같은 경고가 나오고, 점검 목록에 나타난다.
func TestHeadWithoutApprovalRoleWarnedAndListed(t *testing.T) {
	e := newEnv(t)
	_, _, _, t1, t2 := e.baseOrg()
	head := e.hire("직원조직장", "h@pl.test", t1, contract.RoleStaff, "2021-01-04")
	actor := e.hire("직원대행", "a@pl.test", t2, contract.RoleStaff, "2021-01-04")
	ok := e.hire("결재자대행", "m@pl.test", t2, contract.RoleManager, "2021-01-04")
	o := e.reg(Command{Kind: KindHeadSet, UnitID: t1, UserID: head, Effective: "2026-03-10"})
	if len(o.Warnings) != 1 || !strings.Contains(o.Warnings[0], "권한 역할이 '직원'입니다") {
		t.Fatalf("조직장 보임 경고: %v", o.Warnings)
	}
	o = e.reg(Command{Kind: KindActingSet, UnitID: t2, UserID: actor, Effective: "2026-03-10", EndDay: "2026-03-20"})
	if len(o.Warnings) != 1 || !strings.Contains(o.Warnings[0], "권한 역할이 '직원'입니다") {
		t.Fatalf("직무대행 등록 경고: %v", o.Warnings)
	}
	issues, err := e.svc.Issues(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]int{}
	for _, i := range issues {
		if i.Kind == "head_no_approve" {
			got[i.Name]++
			if !strings.Contains(i.Detail, "결재 권한이 없어") {
				t.Errorf("문구: %q", i.Detail)
			}
		}
	}
	if got["구매1팀"] != 1 || got["구매2팀"] != 1 || len(got) != 2 {
		t.Fatalf("점검 목록: %v", got)
	}
	// 결재 권한이 있는 대행자는 경고도 점검 항목도 없다.
	e.reg(Command{Kind: KindActingSet, UnitID: t2, UserID: ok, Effective: "2026-03-21", EndDay: "2026-03-30"})
	_ = ok
}
