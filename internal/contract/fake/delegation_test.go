package fake_test

import (
	"errors"
	"testing"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/contract/fake"
)

func TestFakeDelegatedApproval(t *testing.T) {
	b := fake.NewBundle()
	item := b.ERP.AddItem(domain.Item{SKU: "A-1", Name: "복사용지", Unit: "박스"})
	staff, mgr := as(contract.RoleStaff, 1, 10), as(contract.RoleManager, 2, 10)
	deputy := as(contract.RoleManager, 4, 10)
	now := time.Now()
	b.ERP.Now = func() time.Time { return now }

	pr, _ := b.ERP.CreateDraft(staff, domain.DraftInput{VendorID: 1, Title: "용지", Lines: []domain.PRLineInput{{ItemID: item.ID, Quantity: 1, UnitPrice: 1000}}})
	if _, err := b.ERP.Submit(staff, pr.ID); err != nil {
		t.Fatal(err)
	}
	// 위임 없이 대신 결재 불가
	if _, err := b.ERP.Decide(deputy, pr.ID, domain.Decision{Approve: true, OnBehalfOfID: 2}); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("위임 없는 대리 결재: %v", err)
	}
	// 권한 없는 역할은 위임을 못 만든다
	if _, err := b.ERP.Create(staff, domain.DelegationInput{DelegateID: 4, From: now, To: now.Add(time.Hour)}); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("staff 위임 지정: %v", err)
	}
	dg, err := b.ERP.Create(mgr, domain.DelegationInput{DelegateID: 4, From: now.Add(-time.Hour), To: now.Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.ERP.Decide(deputy, pr.ID, domain.Decision{Approve: true, OnBehalfOfID: 2}); err != nil {
		t.Fatal(err)
	}
	h, _ := b.ERP.History(deputy, pr.ID)
	last := h[len(h)-1]
	if last.ActorID != 4 || last.OnBehalfOfID != 2 {
		t.Fatalf("이력에 누가 누구를 대신했는지: %+v", last)
	}
	if !b.Audit.Has("delegation.create", "approval_delegation", "success") {
		t.Fatal("위임 지정 감사 누락")
	}
	if err := b.ERP.Revoke(mgr, dg.ID); err != nil {
		t.Fatal(err)
	}
	if err := b.ERP.Revoke(mgr, dg.ID); !errors.Is(err, contract.ErrInvalidState) {
		t.Fatalf("이중 해제: %v", err)
	}
}

func TestFakeDelegationSelfApprovalStillForbidden(t *testing.T) {
	b := fake.NewBundle()
	item := b.ERP.AddItem(domain.Item{SKU: "A-1", Name: "복사용지", Unit: "박스"})
	mgr, deputy := as(contract.RoleManager, 2, 10), as(contract.RoleManager, 4, 10)
	now := time.Now()
	pr, _ := b.ERP.CreateDraft(mgr, domain.DraftInput{VendorID: 1, Title: "용지", Lines: []domain.PRLineInput{{ItemID: item.ID, Quantity: 1, UnitPrice: 1000}}})
	if _, err := b.ERP.Submit(mgr, pr.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := b.ERP.Create(mgr, domain.DelegationInput{DelegateID: 4, From: now.Add(-time.Hour), To: now.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	// 위임자(요청자)의 단계를 수임자가 결재하는 것은 허용된다.
	if _, err := b.ERP.Decide(deputy, pr.ID, domain.Decision{Approve: true, OnBehalfOfID: 2}); err != nil {
		t.Fatalf("요청자가 위임한 단계의 수임자 결재: %v", err)
	}
	// 수임자가 요청자면 거부된다(요청자가 다른 사람의 위임을 받아 자기 건을 결재).
	pr2, _ := b.ERP.CreateDraft(deputy, domain.DraftInput{VendorID: 1, Title: "용지2", Lines: []domain.PRLineInput{{ItemID: item.ID, Quantity: 1, UnitPrice: 1000}}})
	if _, err := b.ERP.Submit(deputy, pr2.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := b.ERP.Create(mgr, domain.DelegationInput{DelegateID: 4, From: now.Add(-time.Hour), To: now.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if _, err := b.ERP.Decide(deputy, pr2.ID, domain.Decision{Approve: true, OnBehalfOfID: 2}); !errors.Is(err, contract.ErrSelfApproval) {
		t.Fatalf("수임자가 요청자인 자기 결재: %v", err)
	}
}
