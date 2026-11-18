package service_test

import (
	"strings"
	"testing"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/contract/fake"
	"github.com/choibongseok/procure-ledger/internal/service"
)

func (e *env) reassigner() *service.ReassignService {
	b := service.Base{DB: e.db, Txs: contract.SQLTxManager{DB: e.db}, Audit: e.rec, Masker: fake.Masker{}, Org: leftAwareOrg{e.org}, Now: func() time.Time { return e.now }}
	return service.NewReassignService(b)
}

func (e *env) liner() *service.LineService {
	b := service.Base{DB: e.db, Txs: contract.SQLTxManager{DB: e.db}, Audit: e.rec, Masker: fake.Masker{}, Org: leftAwareOrg{e.org}, Now: func() time.Time { return e.now }}
	return service.NewLineService(b)
}

func reassignIn(to int64) domain.ReassignInput {
	return domain.ReassignInput{UserID: to, Cause: "leave", Reason: "퇴사로 결재할 수 없음"}
}

// 퇴사한 배정자의 단계를 admin 이 재지정하면 새 결재자가 결재한다. 원 결재자는 그 단계를 못 하고, 이력·감사·알림이 같은 트랜잭션에 남는다(AC-120·127·131·140).
func TestReassignPendingStep(t *testing.T) {
	e := newEnv(t)
	r := e.reassigner()
	pr := e.submitted(t, uStaff, 500_000) // 1단계 uLead
	e.deactivate(t, uLead)
	if !e.stalled(t, uStaff, pr.ID) {
		t.Fatal("배정자 퇴사 뒤 결재자 없음이어야 함")
	}
	got, err := r.ReassignStep(as(uAdmin), pr.ID, 1, reassignIn(uHead))
	if err != nil {
		t.Fatalf("재지정: %v", err)
	}
	st := got.Steps[0]
	if st.AssigneeID != uHead || st.ReassignedFrom != "팀장" || st.Status != domain.StepPending {
		t.Fatalf("재지정 결과: %+v", st)
	}
	if e.stalled(t, uStaff, pr.ID) {
		t.Fatal("재지정 뒤에는 멈춘 건이 아님(AC-134)")
	}
	if p, _ := e.approval.ListPending(as(uHead)); len(p) != 1 || p[0].ID != pr.ID {
		t.Fatalf("새 결재자 결재함: %+v", p)
	}
	// 이력 한 줄과 감사 기록.
	hist, _ := e.approval.History(as(uStaff), pr.ID)
	last := hist[len(hist)-1]
	if last.Action != domain.ActionReassign || last.ActorID != uAdmin || !strings.Contains(last.Comment, "팀장님 → 부서장님") || !strings.Contains(last.Comment, "구분: 퇴사") {
		t.Fatalf("이력: %+v", last)
	}
	rec, ok := lastRecord(e, "approval.reassign")
	if !ok || !hasDiff(rec, "from", "팀장") || !hasDiff(rec, "to", "부서장") || !hasDiff(rec, "cause", "퇴사") || !hasDiff(rec, "notified", "2") || !hasDiff(rec, "confirm_active", "아니오") {
		t.Fatalf("감사 기록: %+v", rec)
	}
	// 알림: 요청자·새 결재자(원 결재자는 퇴사 상태라 없음, E34).
	for _, uid := range []int64{uStaff, uHead} {
		n, _ := e.notification.List(as(uid), domain.NotificationFilter{})
		if len(n) != 1 || n[0].Kind != "approval_reassigned" {
			t.Fatalf("사용자 %d 알림: %+v", uid, n)
		}
	}
	if n, _ := e.notification.List(as(uLead), domain.NotificationFilter{}); len(n) != 1 || n[0].Kind != "approval_pending" {
		t.Fatalf("퇴사한 원 결재자에게는 재지정 알림이 없어야 함(상신 때 받은 결재 알림만): %+v", n)
	}
	if n, _ := e.notification.List(as(uHead), domain.NotificationFilter{}); !strings.Contains(n[0].Body, "지금 결재 차례입니다") {
		t.Fatalf("지금 차례 문구: %s", n[0].Body)
	}
	if _, err := e.approval.Decide(as(uHead), pr.ID, domain.Decision{Approve: true}); err != nil {
		t.Fatalf("새 결재자 결재: %v", err)
	}
}

// 대기 중(waiting) 단계도 재지정되고, 앞 단계가 끝나면 새 결재자가 차례가 된다. 원 결재자의 위임 수임자는 그 단계에서 자격을 잃는다(AC-121·127).
func TestReassignWaitingStepAndOldDelegateLosesEligibility(t *testing.T) {
	e := newEnv(t)
	r := e.reassigner()
	e.mustDelegate(t, uHead, uCEO)          // 부서장의 위임: 수임자 대표
	pr := e.submitted(t, uStaff, 2_000_000) // 팀장 → 부서장
	if p, _ := e.approval.ListPending(as(uCEO)); len(p) != 0 {
		t.Fatalf("아직 부서장 차례가 아님: %+v", p)
	}
	// 부서장 단계(2)를 다른 부서의 부서장으로 바꾼다.
	e.addSalesHead(t)
	got, err := r.ReassignStep(as(uAdmin), pr.ID, 2, domain.ReassignInput{UserID: uSalesHead, Cause: "absence", Reason: "장기 부재", ConfirmActive: true})
	if err != nil {
		t.Fatal(err)
	}
	if got.Steps[1].AssigneeID != uSalesHead || got.Steps[1].Status != domain.StepWaiting {
		t.Fatalf("대기 단계 재지정: %+v", got.Steps[1])
	}
	approve(t, e, uLead, pr.ID) // 1단계가 끝나면 2단계가 새 결재자 차례
	if p, _ := e.approval.ListPending(as(uSalesHead)); len(p) != 1 {
		t.Fatalf("새 결재자 결재함: %+v", p)
	}
	for _, old := range []int64{uHead, uCEO} {
		if p, _ := e.approval.ListPending(as(old)); len(p) != 0 {
			t.Fatalf("원 결재자·그 수임자 %d 결재함: %+v", old, p)
		}
		_, err := e.approval.Decide(as(old), pr.ID, domain.Decision{Approve: true})
		if !isErr(err, contract.ErrForbidden) {
			t.Fatalf("원 결재자·수임자 %d 결재는 거부되어야 함: %v", old, err)
		}
	}
	// 새 결재자도 위임할 수 있다(AC-128).
	if _, err := e.delegation.Create(as(uSalesHead), e.delegateIn(uCEO)); err != nil {
		t.Fatalf("새 결재자의 위임: %v", err)
	}
	// 새 결재자는 타 부서 건이어도 볼 수 있고, 원 결재자는 이 단계 때문에 보던 건을 더는 못 본다(AC-133). 같은 부서(uHead)는 부서 규칙으로 계속 본다.
	if _, err := e.purchase.Get(as(uSalesHead), pr.ID); err != nil {
		t.Fatalf("새 결재자 열람: %v", err)
	}
}

// 재지정 거부 사유: 이미 끝난 단계·재무 단계·진행 중이 아닌 건·사유/구분 누락·권한(AC-122·123·124·E24~E36).
func TestReassignRefusals(t *testing.T) {
	e := newEnv(t)
	r := e.reassigner()
	pr := e.submitted(t, uStaff, 20_000_000) // 팀장 → 부서장 → 재무
	approve(t, e, uLead, pr.ID)
	_, err := r.ReassignStep(as(uAdmin), pr.ID, 1, reassignIn(uLead3))
	is(t, err, contract.ErrInvalidState) // 이미 결재가 끝난 단계
	_, err = r.ReassignStep(as(uAdmin), pr.ID, 3, reassignIn(uCEO))
	is(t, err, contract.ErrValidation) // 재무 단계
	if !strings.Contains(err.Error(), "재무 단계는 재무 담당자 역할") {
		t.Fatalf("재무 문구: %v", err)
	}
	_, err = r.ReassignStep(as(uAdmin), pr.ID, 9, reassignIn(uCEO))
	is(t, err, contract.ErrNotFound)
	_, err = r.ReassignStep(as(uAdmin), pr.ID, 2, domain.ReassignInput{UserID: uCEO, Cause: "leave", Reason: "  "})
	is(t, err, contract.ErrValidation)
	_, err = r.ReassignStep(as(uAdmin), pr.ID, 2, domain.ReassignInput{UserID: uCEO, Cause: "x", Reason: "사유"})
	is(t, err, contract.ErrValidation)
	_, err = r.ReassignStep(as(uAdmin), pr.ID, 2, domain.ReassignInput{UserID: uCEO, Cause: "leave", Reason: strings.Repeat("가", 501)})
	is(t, err, contract.ErrValidation)
	// 재직 중이고 결재 가능한 결재자는 확인이 있어야 바꾼다(E33).
	_, err = r.ReassignStep(as(uAdmin), pr.ID, 2, reassignIn(uCEO))
	is(t, err, contract.ErrValidation)
	if !strings.Contains(err.Error(), "재직 중이며 결재할 수 있습니다") {
		t.Fatalf("확인 문구: %v", err)
	}
	// 권한: admin 만. 그 밖의 역할은 서비스에서도 거부된다.
	for _, by := range []int64{uStaff, uLead, uFinance, uAuditor} {
		_, err = r.ReassignStep(as(by), pr.ID, 2, reassignIn(uCEO))
		is(t, err, contract.ErrForbidden)
	}
	// 승인·반려·발주완료된 건은 대상이 아니다(E35).
	approve(t, e, uHead, pr.ID)
	approve(t, e, uFinance, pr.ID)
	_, err = r.ReassignStep(as(uAdmin), pr.ID, 2, reassignIn(uCEO))
	is(t, err, contract.ErrInvalidState)
	// 감사 실패면 아무것도 남지 않는다(AC-131).
	pr2 := e.submitted(t, uStaff, 500_000)
	e.deactivate(t, uLead)
	notesBefore := count(t, e.db, `SELECT COUNT(*) FROM notifications`)
	e.rec.FailWith(errBoom)
	_, err = r.ReassignStep(as(uAdmin), pr2.ID, 1, reassignIn(uHead))
	is(t, err, contract.ErrAuditFailed)
	e.rec.FailWith(nil)
	if n := count(t, e.db, `SELECT COUNT(*) FROM approval_step_reassignments`); n != 0 {
		t.Fatalf("감사 실패인데 재지정 행 %d", n)
	}
	if n := count(t, e.db, `SELECT COUNT(*) FROM notifications`); n != notesBefore {
		t.Fatalf("감사 실패인데 알림 행이 늘었다: %d -> %d", notesBefore, n)
	}
	if n := count(t, e.db, `SELECT COUNT(*) FROM approval_history WHERE action = 'reassign'`); n != 0 {
		t.Fatalf("감사 실패인데 이력 %d", n)
	}
}

// 새 결재자 자격: 재직·권한·조직장·층·요청자·다른 단계·기결재·현재 결재자(AC-125·126).
func TestReassignNewApproverEligibility(t *testing.T) {
	e := newEnv(t)
	r := e.reassigner()
	pr := e.submitted(t, uStaff, 2_000_000) // 팀장(uLead) → 부서장(uHead)
	approve(t, e, uLead, pr.ID)
	// 2단계(부서장 단계)의 후보로 부적합한 사람들.
	cases := []struct {
		name string
		to   int64
		want string
	}{
		{"직원 역할", uStaff2, "결재 권한이 없는 사용자"},
		{"관리자(결재 권한 없음, 자기 지정 포함)", uAdmin, "결재 권한이 없는 사용자"},
		{"층 미달(팀장은 부서장 이상이어야 함)", uLead3, "부서장 이상의 조직장"},
		{"이미 이 건을 결재한 사람(팀장은 층 미달이 먼저)", uLead, "부서장 이상의 조직장"},
		{"현재 결재자와 같음", uHead, "이미 이 단계의 결재자"},
	}
	for _, c := range cases {
		_, err := r.ReassignStep(as(uAdmin), pr.ID, 2, domain.ReassignInput{UserID: c.to, Cause: "other", Reason: "시험", ConfirmActive: true})
		is(t, err, contract.ErrValidation)
		if !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: 문구 %q 가 %q 를 담아야 함", c.name, err.Error(), c.want)
		}
	}
	// 재직하지 않는 사용자.
	e.deactivate(t, uCEO)
	_, err := r.ReassignStep(as(uAdmin), pr.ID, 2, domain.ReassignInput{UserID: uCEO, Cause: "other", Reason: "시험", ConfirmActive: true})
	is(t, err, contract.ErrValidation)
	if !strings.Contains(err.Error(), "재직 중이 아닌 사용자") {
		t.Fatalf("재직 문구: %v", err)
	}
}

// 후보 조회는 자격을 만족하는 사람만, 이름·직책·조직만 돌려준다(AC-138).
func TestReassignCandidates(t *testing.T) {
	e := newEnv(t)
	r := e.reassigner()
	pr := e.submitted(t, uStaff, 500_000) // 1단계 팀장(uLead)
	got, err := r.ReassignCandidates(as(uAdmin), pr.ID, 1, "")
	if err != nil {
		t.Fatal(err)
	}
	ids := map[int64]domain.ReassignCandidate{}
	for _, c := range got {
		ids[c.UserID] = c
	}
	for _, want := range []int64{uHead, uLead3, uLead2, uCEO} {
		if _, ok := ids[want]; !ok {
			t.Errorf("후보에 %d 가 있어야 함", want)
		}
	}
	for _, not := range []int64{uStaff, uLead, uFinance, uAdmin, uAuditor} {
		if _, ok := ids[not]; ok {
			t.Errorf("후보에 %d 가 없어야 함", not)
		}
	}
	if c := ids[uHead]; c.Title != "부서장" || c.UnitName != "구매팀" {
		t.Fatalf("후보 라벨: %+v", c)
	}
	if f, _ := r.ReassignCandidates(as(uAdmin), pr.ID, 1, "대표"); len(f) != 1 || f[0].UserID != uCEO {
		t.Fatalf("이름 검색: %+v", f)
	}
	if _, err := r.ReassignCandidates(as(uLead), pr.ID, 1, ""); !isErr(err, contract.ErrForbidden) {
		t.Fatalf("admin 만: %v", err)
	}
}

// 같은 단계를 두 번 재지정하고 원 결재자로 복귀한다. 재상신하면 재지정은 이어지지 않는다(AC-129·136).
func TestReassignRepeatAndResubmit(t *testing.T) {
	e := newEnv(t)
	r := e.reassigner()
	pr := e.submitted(t, uStaff, 500_000)
	if _, err := r.ReassignStep(as(uAdmin), pr.ID, 1, domain.ReassignInput{UserID: uLead3, Cause: "transfer", Reason: "전보", ConfirmActive: true}); err != nil {
		t.Fatal(err)
	}
	got, err := r.ReassignStep(as(uAdmin), pr.ID, 1, domain.ReassignInput{UserID: uLead, Cause: "other", Reason: "복귀", ConfirmActive: true})
	if err != nil || got.Steps[0].AssigneeID != uLead || got.Steps[0].ReassignedFrom != "구매팀장2" {
		t.Fatalf("복귀: %+v %v", got.Steps, err)
	}
	if n := count(t, e.db, `SELECT COUNT(*) FROM approval_step_reassignments WHERE pr_id = ?`, pr.ID); n != 2 {
		t.Fatalf("기록 %d", n)
	}
	// 반려 → 재상신: 새 결재선은 조직도로 만들어져 uLead 가 배정되고 재지정은 없다.
	if _, err := e.approval.Decide(as(uLead), pr.ID, domain.Decision{Comment: "보완"}); err != nil {
		t.Fatal(err)
	}
	re, err := e.purchase.Submit(as(uStaff), pr.ID)
	if err != nil || re.Round != 2 || re.Steps[0].AssigneeID != uLead || re.Steps[0].ReassignedFrom != "" {
		t.Fatalf("재상신: %+v %v", re, err)
	}
}

// 결재자 소멸 목록: admin(org:manage)만, 퇴사·권한 상실 사유, 재무 단계는 재지정 불가, 재지정하면 사라진다(AC-33·34·73·135).
func TestListOrphanedSteps(t *testing.T) {
	e := newEnv(t)
	l, r := e.liner(), e.reassigner()
	pr := e.submitted(t, uStaff, 20_000_000)
	if got, err := l.ListOrphanedSteps(as(uAdmin), ""); err != nil || len(got) != 0 {
		t.Fatalf("정상 건은 목록에 없어야 함: %+v %v", got, err)
	}
	e.deactivate(t, uLead)
	// 권한 상실: 부서장의 역할을 staff 로.
	if _, err := e.db.Exec(`UPDATE users SET role = 'staff' WHERE id = ?`, uHead); err != nil {
		t.Fatal(err)
	}
	got, err := l.ListOrphanedSteps(as(uAdmin), "")
	if err != nil || len(got) != 2 {
		t.Fatalf("소멸 목록: %+v %v", got, err)
	}
	byseq := map[int]domain.OrphanStep{}
	for _, o := range got {
		byseq[o.Seq] = o
	}
	if o := byseq[1]; o.Reason != "퇴사" || o.ApproverID != uLead || o.Title != "팀장" || !o.Reassignable || o.PRID != pr.ID || o.Round != 1 {
		t.Fatalf("1단계: %+v", o)
	}
	if o := byseq[2]; o.Reason != "결재 권한 상실" || o.ApproverID != uHead {
		t.Fatalf("2단계: %+v", o)
	}
	for _, by := range []int64{uStaff, uLead, uFinance, uAuditor} {
		if _, err := l.ListOrphanedSteps(as(by), ""); !isErr(err, contract.ErrForbidden) {
			t.Fatalf("admin 만 조회: %v", err)
		}
	}
	if _, err := r.ReassignStep(as(uAdmin), pr.ID, 1, reassignIn(uLead3)); err != nil {
		t.Fatal(err)
	}
	if got, _ := l.ListOrphanedSteps(as(uAdmin), ""); len(got) != 1 || got[0].Seq != 2 {
		t.Fatalf("재지정한 단계는 목록에서 사라져야 함: %+v", got)
	}
	// 위임 수임자가 유효하면 소멸이 아니다: 위임자(2단계 배정자 uHead)는 권한이 없어 위임이 효력이 없으므로 그대로 목록에 있다.
	if _, err := l.ListOrphanedSteps(as(uAdmin), "2026-13-45"); !isErr(err, contract.ErrValidation) {
		t.Fatalf("기준일 형식: %v", err)
	}
}
