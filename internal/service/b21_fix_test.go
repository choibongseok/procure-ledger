package service_test

import (
	"testing"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
)

// 결재 권한을 잃은 위임자의 수임자는 대신 결재할 수 없다. 결재 가능 여부와 후보 계산(결재자 없음)이 같은 usable 조건을 쓴다(B21 검증 1).
// 위임은 자동 해제하지 않고 무효로만 취급한다(권한이 돌아오면 다시 유효).
func TestDelegateCannotDecideWhenDelegatorLostApprovalRole(t *testing.T) {
	e := newEnv(t)
	e.mustDelegate(t, uLead, uHead)
	pr := e.submitted(t, uStaff, 500_000)
	if _, err := e.db.Exec(`UPDATE users SET role = 'staff' WHERE id = ?`, uLead); err != nil {
		t.Fatal(err)
	}
	if !e.stalled(t, uStaff, pr.ID) {
		t.Fatal("결재 권한을 잃은 뒤에는 결재자 없음으로 표시돼야 함")
	}
	_, err := e.approval.Decide(as(uHead), pr.ID, domain.Decision{Approve: true})
	is(t, err, contract.ErrForbidden)
	if st, _ := e.purchase.Get(as(uStaff), pr.ID); st.Steps[0].Status != domain.StepPending {
		t.Fatalf("단계가 바뀜: %+v", st.Steps)
	}
	if _, err := e.db.Exec(`UPDATE users SET role = 'manager' WHERE id = ?`, uLead); err != nil {
		t.Fatal(err)
	}
	if _, err := e.approval.Decide(as(uHead), pr.ID, domain.Decision{Approve: true}); err != nil {
		t.Fatalf("권한이 돌아오면 위임이 다시 유효해야 함: %v", err)
	}
}
