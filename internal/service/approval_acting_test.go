package service_test

import (
	"testing"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
)

// 결재 화면의 위임 결재 안내(ActingFor)는 실제 결재가 이력에 남기는 위임자와 같은 사람·기간을 알려 준다.
func TestActingForMatchesDecisionHistory(t *testing.T) {
	e := newEnv(t)
	d := e.mustDelegate(t, uLead2, uHead)
	pr := e.submitted(t, uStaff2, 500_000)

	n, ok, err := e.approval.ActingFor(as(uHead), pr.ID)
	if err != nil || !ok {
		t.Fatalf("수임자에게 위임 결재 안내가 있어야 함: ok=%v %v", ok, err)
	}
	if n.DelegatorID != uLead2 || n.DelegatorName == "" || !n.From.Equal(d.From) || !n.To.Equal(d.To) {
		t.Fatalf("안내 값이 위임과 다름: %+v / %+v", n, d)
	}
	// 직접 결재자(위임자 본인)와 요청자에게는 안내가 없다.
	if _, ok, _ := e.approval.ActingFor(as(uLead2), pr.ID); ok {
		t.Fatalf("직접 결재자에게 위임 안내가 나옴")
	}
	if _, ok, _ := e.approval.ActingFor(as(uStaff2), pr.ID); ok {
		t.Fatalf("요청자에게 위임 안내가 나옴")
	}

	if _, err := e.approval.Decide(as(uHead), pr.ID, domain.Decision{Approve: true}); err != nil {
		t.Fatalf("위임 결재: %v", err)
	}
	hist, _ := e.approval.History(as(uStaff2), pr.ID)
	last := hist[len(hist)-1]
	if last.OnBehalfOfID != n.DelegatorID || last.OnBehalfOfName != n.DelegatorName {
		t.Fatalf("이력의 위임자가 안내와 다름: %+v / %+v", last, n)
	}
	// 끝난 건에는 안내가 없다.
	if _, ok, _ := e.approval.ActingFor(as(uHead), pr.ID); ok {
		t.Fatalf("결재가 끝난 건에 위임 안내가 나옴")
	}
}

func TestActingForNeedsDecidePermission(t *testing.T) {
	e := newEnv(t)
	pr := e.submitted(t, uStaff2, 500_000)
	if _, _, err := e.approval.ActingFor(as(uStaff2), pr.ID); !isErr(err, contract.ErrForbidden) {
		t.Fatalf("결재 권한이 없으면 거부해야 함: %v", err)
	}
}
