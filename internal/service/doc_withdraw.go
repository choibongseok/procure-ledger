package service

import (
	"context"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
)

// Withdraw 는 결재자가 없어 멈춘 상신 양식 문서를 기안자가 임시저장으로 되돌린다(회수, D9: "결재자 없음으로 멈춘 건은 기존 회수(B11)대로").
// 구매 품의서의 회수와 같은 본체(PurchaseService.withdrawKind)를 쓰므로 조건(결재할 사람이 있으면 거부)·결재 이력·감사·차수 증가가 같다.
func (s *ApprovalDocService) Withdraw(ctx context.Context, id int64) (domain.PurchaseRequest, error) {
	p, err := s.authorize(ctx, contract.PermDraftCreate, "approval_doc.withdraw", audit.TargetApprovalDoc)
	if err != nil {
		return domain.PurchaseRequest{}, err
	}
	rec, err := NewPurchaseService(s.Base).withdrawKind(ctx, p, id, false, "approval_doc.withdraw", audit.TargetApprovalDoc)
	if err != nil {
		return domain.PurchaseRequest{}, err
	}
	return s.present(ctx, p, rec), nil
}

// StalledIDs 는 docs 중 지금 차례 단계의 결재자가 없는 진행 중 문서의 번호 집합이다(화면의 "결재자 없음" 표시용, 구매 품의서와 같은 판정).
func (s *ApprovalDocService) StalledIDs(ctx context.Context, docs []domain.PurchaseRequest) (map[int64]bool, error) {
	return NewPurchaseService(s.Base).StalledIDs(ctx, docs)
}
