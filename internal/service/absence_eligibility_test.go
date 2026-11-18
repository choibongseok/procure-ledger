package service_test

import (
	"strings"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
)

// B50 AC-5032·5034·5035: 휴직 중인 결재자는 자격(usable)에서 빠져 결재할 수 없고, 후보가 없어진 단계는 멈춘 건으로 드러난다.
// 재지정(장기 부재 원인)으로 처리하고, 복직하면 자격이 돌아온다.
func TestOnLeaveApproverIsNotEligibleAndReassignable(t *testing.T) {
	e := newEnv(t)
	r := e.reassigner()
	pr := e.submitted(t, uStaff, 500_000) // 1단계 uLead
	if e.stalled(t, uStaff, pr.ID) {
		t.Fatal("휴직 전에는 멈춘 건이 아니다")
	}
	e.org.SetOnLeave(uLead, true)
	if !e.stalled(t, uStaff, pr.ID) {
		t.Fatal("결재자가 휴직 중이면 후보가 없어 멈춘 건이어야 한다")
	}
	if p, _ := e.approval.ListPending(as(uLead)); len(p) != 0 {
		t.Fatalf("휴직 중인 결재자의 결재함에 건이 보이면 안 된다: %+v", p)
	}
	if _, err := e.approval.Decide(as(uLead), pr.ID, domain.Decision{Approve: true}); !isErr(err, contract.ErrForbidden) {
		t.Fatalf("휴직 중 결재는 거부되어야 한다: %v", err)
	}
	// 휴직 중인 사람을 새 결재자로 지정할 수 없고 문구가 휴직임을 말한다.
	e.org.SetOnLeave(uHead, true)
	_, err := r.ReassignStep(as(uAdmin), pr.ID, 1, domain.ReassignInput{UserID: uHead, Cause: "absence", Reason: "장기 부재"})
	if !isErr(err, contract.ErrValidation) || !strings.Contains(err.Error(), "휴직 중인 사용자는 결재자가 될 수 없습니다") {
		t.Fatalf("휴직 중인 새 결재자: %v", err)
	}
	e.org.SetOnLeave(uHead, false)
	// 휴직 중인 결재자 단계는 소멸 목록에 "휴직" 으로 나온다.
	orphans, err := e.liner().ListOrphanedSteps(as(uAdmin), "")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, o := range orphans {
		if o.PRID == pr.ID && o.Reason == "휴직" && o.Reassignable {
			found = true
		}
	}
	if !found {
		t.Fatalf("멈춘 단계 목록에 휴직 사유가 있어야 한다: %+v", orphans)
	}
	// 관리자가 absence 원인으로 재지정하면 새 결재자가 결재한다.
	if _, err := r.ReassignStep(as(uAdmin), pr.ID, 1, domain.ReassignInput{UserID: uHead, Cause: "absence", Reason: "휴직으로 결재할 수 없음"}); err != nil {
		t.Fatalf("재지정: %v", err)
	}
	if e.stalled(t, uStaff, pr.ID) {
		t.Fatal("재지정 뒤에는 멈춘 건이 아니다")
	}
	if _, err := e.approval.Decide(as(uHead), pr.ID, domain.Decision{Approve: true}); err != nil {
		t.Fatalf("새 결재자 결재: %v", err)
	}
}

// 복직하면 자격이 돌아온다(AC-5035).
func TestReturnFromLeaveRestoresEligibility(t *testing.T) {
	e := newEnv(t)
	pr := e.submitted(t, uStaff, 500_000)
	e.org.SetOnLeave(uLead, true)
	if !e.stalled(t, uStaff, pr.ID) {
		t.Fatal("휴직 중에는 멈춘 건")
	}
	e.org.SetOnLeave(uLead, false)
	if e.stalled(t, uStaff, pr.ID) {
		t.Fatal("복직하면 멈춘 건이 아니다")
	}
	if _, err := e.approval.Decide(as(uLead), pr.ID, domain.Decision{Approve: true}); err != nil {
		t.Fatalf("복직한 결재자의 결재: %v", err)
	}
}

// 휴직 중인 사람이 수임자인 위임은 후보에서 빠지고, 위임자가 휴직 중이면 위임이 후보에 기여하지 않는다. 위임 행은 남아 복직하면 다시 유효하다(8.3 의 4).
func TestOnLeaveDelegationDropsFromCandidatesAndRecovers(t *testing.T) {
	e := newEnv(t)
	pr := e.submitted(t, uStaff, 500_000) // 1단계 uLead
	e.mustDelegate(t, uLead, uHead)       // 팀장의 위임: 수임자 부서장(팀장보다 높은 층)
	// 수임자 휴직: 팀장 본인은 그대로 결재할 수 있고 수임자는 빠진다.
	e.org.SetOnLeave(uHead, true)
	if p, _ := e.approval.ListPending(as(uHead)); len(p) != 0 {
		t.Fatalf("휴직 중인 수임자의 결재함: %+v", p)
	}
	if p, _ := e.approval.ListPending(as(uLead)); len(p) != 1 {
		t.Fatalf("위임자 본인은 결재할 수 있다: %+v", p)
	}
	e.org.SetOnLeave(uHead, false)
	if p, _ := e.approval.ListPending(as(uHead)); len(p) != 1 {
		t.Fatalf("복직한 수임자는 위임 기간이 남았으면 다시 유효하다: %+v", p)
	}
	// 위임자 휴직: 위임자가 usable 이 아니므로 수임자도 후보가 아니다.
	e.org.SetOnLeave(uLead, true)
	if p, _ := e.approval.ListPending(as(uHead)); len(p) != 0 {
		t.Fatalf("위임자가 휴직 중이면 위임은 후보에 기여하지 않는다: %+v", p)
	}
	_ = pr
}

// 대결자 후보 목록에서 휴직 중인 사람은 부재 중으로 표시되어 고를 수 없다.
func TestDelegateCandidatesMarkOnLeaveAsAway(t *testing.T) {
	e := newEnv(t)
	e.org.SetOnLeave(uHead, true)
	cs, err := e.delegation.Candidates(as(uLead))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cs {
		if c.ID == uHead && !c.Away {
			t.Fatalf("휴직 중인 후보는 부재 표시여야 한다: %+v", c)
		}
	}
}
