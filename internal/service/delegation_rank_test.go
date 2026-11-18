package service_test

import (
	"testing"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
)

// 요청자 본인이 맡긴 위임은 자기 건의 결재 후보를 만들지 않는다(요청자는 자기 건의 단계를 맡지 않고 결재선이 요청자를 올림으로 뺀다).
// 영업1팀 팀장(uLead2)의 요청은 대표가 결재하고, uLead2 가 누구에게 위임했든 수임자는 결재함·열람·결재가 열리지 않는다.
func TestRequesterDelegationNeverCreatesCandidates(t *testing.T) {
	e := newEnv(t)
	e.mustDelegate(t, uLead2, uLead) // 같은 서열
	e.mustDelegate(t, uLead2, uHead) // 더 높은 서열
	pr := e.submitted(t, uLead2, 500_000)
	if names := e.lineNames(t, uLead2, pr.ID); len(names) != 1 || names[0] != "대표" {
		t.Fatalf("결재선: %v", names)
	}
	for _, by := range []int64{uLead, uHead} {
		if p, _ := e.approval.ListPending(as(by)); len(p) != 0 {
			t.Fatalf("사용자 %d 결재함: %+v", by, p)
		}
		if _, err := e.purchase.Get(as(by), pr.ID); !isErr(err, contract.ErrNotFound) {
			t.Fatalf("사용자 %d 열람: %v", by, err)
		}
		_, err := e.approval.Decide(as(by), pr.ID, domain.Decision{Approve: true})
		is(t, err, contract.ErrNotFound)
	}
	approve(t, e, uCEO, pr.ID)
}

// 요청자가 위임자가 아닌 건에서는 같은 직책 수임자도 현행대로 결재한다: 구매1팀 팀장(uLead)이 같은 직책 uLead2 에게 맡기면
// 구매1팀 직원의 요청을 uLead2 가 결재할 수 있다.
func TestDelegateOfAssigneeDecidesOthersRequests(t *testing.T) {
	e := newEnv(t)
	e.mustDelegate(t, uLead, uLead2)
	other := e.submitted(t, uStaff, 500_000)
	if p, _ := e.approval.ListPending(as(uLead2)); len(p) != 1 || p[0].ID != other.ID {
		t.Fatalf("수임자 결재함: %+v", p)
	}
	if _, err := e.approval.Decide(as(uLead2), other.ID, domain.Decision{Approve: true}); err != nil {
		t.Fatalf("수임자 결재: %v", err)
	}
}

// 부서장이 요청자이면 위로 올라가 대표가 팀장 단계를 맡고(부서장·본부장 층이 없어 이어지는 단계는 생략) 재무 풀이 남는다.
// 재무가 요청자이면 풀에서 요청자가 빠지고, 다른 재무 담당자가 없으면 상신할 수 없다.
func TestRequesterHighRankRaisesToTopAndFinancePoolNeedsOthers(t *testing.T) {
	e := newEnv(t)
	pr := e.submitted(t, uHead, 20_000_000)
	if names := e.lineNames(t, uHead, pr.ID); len(names) != 2 || names[0] != "대표" || names[1] != "재무" {
		t.Fatalf("부서장 요청 결재선: %v", names)
	}

	e2 := newEnv(t)
	e2.deactivate(t, uFinance)
	fpr := e2.draft(t, uFinance2, 20_000_000)
	_, err := e2.purchase.Submit(as(uFinance2), fpr.ID)
	is(t, err, contract.ErrValidation)
}

// 팀장 요청 2천만 원: 팀장 단계는 부서장으로, 부서장 단계는 대표로 올라가고 재무가 마지막이다. 한 사람이 두 단계를 맡지 않는다.
func TestRequesterLeadLongLine(t *testing.T) {
	e := newEnv(t)
	pr := e.submitted(t, uLead, 20_000_000)
	names := e.lineNames(t, uLead, pr.ID)
	if len(names) != 3 || names[0] != "부서장" || names[1] != "대표" || names[2] != "재무" {
		t.Fatalf("결재선: %v", names)
	}
	approve(t, e, uHead, pr.ID)
	approve(t, e, uCEO, pr.ID)
	approve(t, e, uFinance, pr.ID)
}
