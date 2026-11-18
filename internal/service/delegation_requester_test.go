package service_test

import (
	"testing"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
)

// 위임 지정 때 수임자 자격은 서비스가 검사한다: 직원·재무·감사·관리자·본인은 거부되고 행이 남지 않는다.
func TestRequesterDelegationNeedsQualifiedDelegate(t *testing.T) {
	e := newEnv(t)
	for _, bad := range []int64{uStaff, uStaff2, uFinance, uAuditor, uAdmin, uLead2} {
		// uLead2 는 자기 자신이다.
		_, err := e.delegation.Create(as(uLead2), e.delegateIn(bad))
		is(t, err, contract.ErrValidation)
	}
	if n := count(t, e.db, `SELECT COUNT(*) FROM approval_delegations`); n != 0 {
		t.Fatalf("자격 없는 위임이 남음: %d", n)
	}
	// 비활성 사용자는 수임자가 될 수 없다.
	if _, err := e.db.Exec(`UPDATE users SET active = 0 WHERE id = ?`, uLead); err != nil {
		t.Fatal(err)
	}
	_, err := e.delegation.Create(as(uLead2), e.delegateIn(uLead))
	is(t, err, contract.ErrValidation)
}

// 요청자는 위임했더라도 본인 요청을 결재할 수 없고, 수임자가 요청자면 위임으로도 결재할 수 없다.
func TestRequesterStillCannotApproveOwnRequest(t *testing.T) {
	e := newEnv(t)
	e.mustDelegate(t, uLead2, uHead)
	pr := e.submitted(t, uLead2, 500_000) // 대표가 결재
	_, err := e.approval.Decide(as(uLead2), pr.ID, domain.Decision{Approve: true})
	is(t, err, contract.ErrSelfApproval)

	// 수임자가 요청자인 건: uLead 가 올린 구매1팀 건의 1단계는 부서장이다. 위임자 uHead 의 수임자가 uLead 여도 자기 건은 결재할 수 없다.
	e.mustDelegate(t, uLead3, uLead)
	own := e.submitted(t, uLead, 500_000)
	_, err = e.approval.Decide(as(uLead), own.ID, domain.Decision{Approve: true})
	is(t, err, contract.ErrSelfApproval)
	if p, _ := e.approval.ListPending(as(uLead)); len(p) != 0 {
		t.Fatalf("본인 요청은 결재함에 없어야 함: %+v", p)
	}
}
