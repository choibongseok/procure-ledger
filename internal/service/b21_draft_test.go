package service_test

import (
	"testing"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
)

// 임시저장 건은 요청자 본인과 purchase:read_all 권한자만 본다. 같은 부서 동료·조직장에게 보이지 않는다(명세 3.5, B21 검증 4).
func TestDraftVisibleOnlyToRequesterAndReadAll(t *testing.T) {
	e := newEnv(t)
	d := e.draft(t, uStaff, 500_000)
	for _, who := range []int64{uLead, uHead, uLead3, uStaff2} {
		_, err := e.purchase.Get(as(who), d.ID)
		is(t, err, contract.ErrNotFound)
		list, err := e.purchase.List(as(who), domain.PRFilter{})
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range list {
			if p.ID == d.ID {
				t.Fatalf("사용자 %d 의 목록에 남의 임시저장 건이 보임", who)
			}
		}
	}
	for _, who := range []int64{uStaff, uFinance, uAuditor, uAdmin} {
		if _, err := e.purchase.Get(as(who), d.ID); err != nil {
			t.Fatalf("사용자 %d: %v", who, err)
		}
	}
	// 상신하면 같은 부서·조직장에게 열린다.
	if _, err := e.purchase.Submit(as(uStaff), d.ID); err != nil {
		t.Fatal(err)
	}
	for _, who := range []int64{uLead, uHead} {
		if _, err := e.purchase.Get(as(who), d.ID); err != nil {
			t.Fatalf("상신 뒤 사용자 %d: %v", who, err)
		}
	}
}

// 임시저장 건의 목록 소속 칸은 요청자의 현재 소속 조직 경로다(상신 당시 조직이 아직 없다).
func TestDraftListShowsCurrentUnit(t *testing.T) {
	e := newEnv(t)
	d := e.draft(t, uStaff, 500_000)
	got, err := e.purchase.Get(as(uStaff), d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.RequesterUnitPath == "" {
		t.Fatalf("임시저장 건의 소속이 비었다: %+v", got)
	}
}
