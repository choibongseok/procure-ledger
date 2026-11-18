package service_test

import (
	"errors"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
)

// AC-230(D9): 결재자 없음으로 멈춘 양식 문서는 처리 이력이 있어도 기안자가 회수한다(구매 품의서 회수와 같은 조건·이력·감사).
func TestDocWithdrawWhenStalled(t *testing.T) {
	e := newEnv(t)
	docs := e.docs()
	pr := e.genSubmitted(t, uStaff, "멈춘 문서")
	if _, err := docs.Withdraw(as(uStaff), pr.ID); !errors.Is(err, contract.ErrInvalidState) {
		t.Fatalf("결재할 수 있는 사람이 있으면 회수 거부: %v", err)
	}
	decideAs(t, e, uLead, pr.ID, true, "")
	if _, err := docs.CancelSubmit(as(uStaff), pr.ID, "x"); !errors.Is(err, contract.ErrInvalidState) {
		t.Fatalf("처리 이력이 있으면 상신 취소는 안 됨: %v", err)
	}
	got, _ := docs.Get(as(uStaff), pr.ID)
	if m, _ := docs.StalledIDs(as(uStaff), []domain.PurchaseRequest{got}); m[pr.ID] {
		t.Fatal("아직 결재자가 있는데 결재자 없음으로 표시됨")
	}
	e.deactivate(t, uHead) // 지금 차례인 부서장이 퇴사
	got, _ = docs.Get(as(uStaff), pr.ID)
	if m, err := docs.StalledIDs(as(uStaff), []domain.PurchaseRequest{got}); err != nil || !m[pr.ID] {
		t.Fatalf("결재자 없음 표시: %v %v", m, err)
	}
	if _, err := docs.Withdraw(as(uStaff2), pr.ID); !errors.Is(err, contract.ErrNotFound) {
		t.Fatalf("남의 문서 회수는 404: %v", err)
	}
	out, err := docs.Withdraw(as(uStaff), pr.ID)
	if err != nil || out.Status != domain.PRDraft || out.Round != got.Round+1 {
		t.Fatalf("회수: %+v %v", out, err)
	}
	if !e.rec.Has("approval_doc.withdraw", "approval_doc", "success") {
		t.Fatal("회수 감사 기록 없음")
	}
	if n := count(t, e.db, `SELECT COUNT(*) FROM approval_history WHERE pr_id = ? AND action = 'withdraw'`, pr.ID); n != 1 {
		t.Fatalf("회수 이력 %d건", n)
	}
	if _, err := docs.Withdraw(as(uStaff), pr.ID); !errors.Is(err, contract.ErrInvalidState) {
		t.Fatalf("임시저장 문서는 회수 대상 아님: %v", err)
	}
	// 회수한 문서는 다시 상신할 수 있다(차수가 오르고 번호는 유지).
	if _, err := docs.UpdateDraft(as(uStaff), pr.ID, domain.DocDraftInput{FormCode: "GEN", Title: "멈춘 문서", Fields: map[string]any{"content": "본문"}}); err != nil {
		t.Fatalf("회수 뒤 수정: %v", err)
	}
	// 구매 API 의 회수는 양식 문서에 닿지 않는다.
	if _, err := e.purchase.Withdraw(as(uStaff), pr.ID); !errors.Is(err, contract.ErrNotFound) {
		t.Fatalf("구매 회수가 양식 문서에 닿음: %v", err)
	}
}
