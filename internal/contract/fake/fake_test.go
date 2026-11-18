package fake_test

import (
	"context"
	"errors"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/contract/fake"
	"github.com/choibongseok/procure-ledger/internal/contract/llm"
)

func as(role contract.Role, id, dept int64) context.Context {
	return contract.WithPrincipal(context.Background(), contract.Principal{UserID: id, Name: string(role), Role: role, DepartmentID: dept, Channel: contract.ChannelMCP})
}

func TestPurchaseToReceiptFlowRecordsAudit(t *testing.T) {
	b := fake.NewBundle()
	item := b.ERP.AddItem(domain.Item{SKU: "A-1", Name: "복사용지", Unit: "박스", SafetyStock: 10})
	staff, mgr := as(contract.RoleStaff, 1, 10), as(contract.RoleManager, 2, 10)
	fin := as(contract.RoleFinance, 3, 20)

	pr, err := b.ERP.CreateDraft(staff, domain.DraftInput{VendorID: 1, Title: "용지", Lines: []domain.PRLineInput{{ItemID: item.ID, Quantity: 5, UnitPrice: 20000}}})
	if err != nil {
		t.Fatal(err)
	}
	if pr, err = b.ERP.Submit(staff, pr.ID); err != nil || pr.Status != domain.PRSubmitted {
		t.Fatalf("submit: %v %v", pr.Status, err)
	}
	res, err := b.ERP.Decide(mgr, pr.ID, domain.Decision{Approve: true})
	if err != nil || res.PurchaseOrder == nil || res.Request.Status != domain.PROrdered {
		t.Fatalf("decide: %+v %v", res, err)
	}
	po := res.PurchaseOrder
	if _, err := b.ERP.Receive(fin, domain.ReceiveInput{POID: po.ID, Lines: []domain.ReceiptLine{{POLineID: po.Lines[0].ID, Quantity: 6}}}); !errors.Is(err, contract.ErrOverReceipt) {
		t.Fatalf("초과 입고가 거부되어야 함: %v", err)
	}
	if _, err := b.ERP.Receive(fin, domain.ReceiveInput{POID: po.ID, Lines: []domain.ReceiptLine{{POLineID: po.Lines[0].ID, Quantity: 3, LotNo: "L1"}}}); err != nil {
		t.Fatal(err)
	}
	st, _ := b.ERP.GetStock(staff, item.ID)
	if st.OnHand != 3 || !st.BelowSafety {
		t.Fatalf("재고는 원장 합: %+v", st)
	}
	for _, a := range []string{"purchase_request.create", "purchase_request.submit", "approval.decide", "receipt.create"} {
		if !b.Audit.Has(a, "", audit.ResultSuccess) {
			t.Errorf("감사 기록 없음: %s", a)
		}
	}
	if r, _ := b.Audit.Verify(context.Background()); !r.OK {
		t.Fatalf("체인 검증 실패: %+v", r)
	}
}

func TestDeniedAndSelfApproval(t *testing.T) {
	b := fake.NewBundle()
	item := b.ERP.AddItem(domain.Item{Name: "x"})
	mgr := as(contract.RoleManager, 2, 10)
	pr, _ := b.ERP.CreateDraft(mgr, domain.DraftInput{VendorID: 1, Title: "t", Lines: []domain.PRLineInput{{ItemID: item.ID, Quantity: 1, UnitPrice: 1}}})
	_, _ = b.ERP.Submit(mgr, pr.ID)
	if _, err := b.ERP.Decide(mgr, pr.ID, domain.Decision{Approve: true}); !errors.Is(err, contract.ErrSelfApproval) {
		t.Fatalf("자기 결재 금지: %v", err)
	}
	if _, err := b.ERP.Decide(as(contract.RoleStaff, 1, 10), pr.ID, domain.Decision{Approve: true}); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("staff 결재 거부: %v", err)
	}
	if !b.Audit.Has("approval.decide", audit.TargetApproval, audit.ResultDenied) {
		t.Fatal("거부도 감사에 남아야 함")
	}
	if _, err := b.ERP.Get(as(contract.RoleStaff, 9, 99), pr.ID); !errors.Is(err, contract.ErrNotFound) {
		t.Fatalf("타 부서 격리: %v", err)
	}
	if _, err := b.ERP.Get(as(contract.RoleAuditor, 8, 99), pr.ID); err != nil {
		t.Fatalf("auditor 는 타 부서 조회 가능: %v", err)
	}
}

func TestAuditFailureFailsRequest(t *testing.T) {
	b := fake.NewBundle()
	item := b.ERP.AddItem(domain.Item{Name: "x"})
	b.Audit.FailWith(errors.New("disk"))
	_, err := b.ERP.CreateDraft(as(contract.RoleStaff, 1, 10), domain.DraftInput{VendorID: 1, Title: "t", Lines: []domain.PRLineInput{{ItemID: item.ID, Quantity: 1, UnitPrice: 1}}})
	if !errors.Is(err, contract.ErrAuditFailed) {
		t.Fatalf("기록 실패 시 요청이 실패해야 함: %v", err)
	}
	b.Audit.FailWith(nil)
	if list, _ := b.ERP.List(as(contract.RoleStaff, 1, 10), domain.PRFilter{}); len(list) != 0 {
		t.Fatal("실패한 요청의 변경이 남았음")
	}
}

func TestAuditChainTamperDetected(t *testing.T) {
	r := fake.NewRecorder()
	p := contract.Principal{UserID: 1, Role: contract.RoleStaff, Channel: contract.ChannelWeb}
	for i := 0; i < 5; i++ {
		if err := r.Record(context.Background(), nil, audit.Entry{Actor: p, Action: "x", TargetType: audit.TargetItem, TargetID: "1", Result: audit.ResultSuccess}); err != nil {
			t.Fatal(err)
		}
	}
	_ = r.Tamper(3, "변조")
	res, _ := r.Verify(context.Background())
	if res.OK || res.BrokenSeq != 3 {
		t.Fatalf("seq 3 을 짚어야 함: %+v", res)
	}
}

func TestDocumentsPermissionsAndMockLLM(t *testing.T) {
	b := fake.NewBundle()
	v, err := b.Documents.Verify(as(contract.RoleStaff, 1, 10), domain.VerifyInput{POID: 1, Text: "명세서"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.Documents.Confirm(as(contract.RoleStaff, 1, 10), v.ID, domain.ConfirmInput{Outcome: domain.OutcomeMatch}); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("staff 확정 거부: %v", err)
	}
	if _, err := b.Documents.Confirm(as(contract.RoleFinance, 3, 20), v.ID, domain.ConfirmInput{Outcome: domain.OutcomeMatch}); err != nil {
		t.Fatal(err)
	}
	req := llm.Request{System: "s", Messages: []llm.Message{{Role: "user", Content: "hi"}}}
	a, _ := b.LLM.Complete(context.Background(), req)
	c, _ := b.LLM.Complete(context.Background(), req)
	if a.Text != c.Text || len(b.LLM.Calls()) != 2 {
		t.Fatal("mock LLM 은 결정적이어야 함")
	}
}

func TestMasker(t *testing.T) {
	m := fake.Masker{}
	if got := m.MaskText("사업자 123-45-67890 연락 010-1234-5678"); got != "사업자 [MASKED] 연락 [MASKED]" {
		t.Fatal(got)
	}
	if m.MaskField(contract.Principal{UserID: 1, Role: contract.RoleStaff}, contract.FieldBusinessNo, "1234567890") == "1234567890" {
		t.Fatal("staff 는 마스킹")
	}
	if m.MaskField(contract.Principal{UserID: 1, Role: contract.RoleFinance}, contract.FieldBusinessNo, "1234567890") != "1234567890" {
		t.Fatal("finance 는 원문")
	}
}

// 실제 구현과 같은 상태 전이: 상신 직후 submitted, 첫 승인 뒤 다음 단계가 남으면 in_review, 거래처 없는 상신은 거부.
func TestSubmitStatusMatchesRealImplementation(t *testing.T) {
	b := fake.NewBundle()
	item := b.ERP.AddItem(domain.Item{Name: "서버"})
	staff, lead := as(contract.RoleStaff, 1, 10), as(contract.RoleManager, 2, 10)

	noVendor, err := b.ERP.CreateDraft(staff, domain.DraftInput{Title: "거래처 없음", Lines: []domain.PRLineInput{{ItemID: item.ID, Quantity: 1, UnitPrice: 1000}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.ERP.Submit(staff, noVendor.ID); !errors.Is(err, contract.ErrValidation) {
		t.Fatalf("거래처 없는 상신은 ErrValidation: %v", err)
	}

	big, _ := b.ERP.CreateDraft(staff, domain.DraftInput{VendorID: 1, Title: "고가", Lines: []domain.PRLineInput{{ItemID: item.ID, Quantity: 2, UnitPrice: 1000000}}})
	pr, err := b.ERP.Submit(staff, big.ID)
	if err != nil || pr.Status != domain.PRSubmitted {
		t.Fatalf("상신 직후 submitted: %v %v", pr.Status, err)
	}
	res, err := b.ERP.Decide(lead, pr.ID, domain.Decision{Approve: true})
	if err != nil || res.Request.Status != domain.PRInReview {
		t.Fatalf("첫 승인 뒤 in_review: %v %v", res.Request.Status, err)
	}
}
