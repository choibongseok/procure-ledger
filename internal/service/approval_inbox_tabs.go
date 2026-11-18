// 결재함 탭(대기·진행·완료) 목록. 대기는 기존 결재함 판정(ListPending) 한 곳을 그대로 쓴다.
package service

import (
	"context"
	"fmt"
	"sort"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/repository"
)

// 결재함 탭 이름.
const (
	InboxTabPending  = "pending"  // 대기: 지금 내 차례
	InboxTabProgress = "progress" // 진행: 내가 결재선에 있고 아직 끝나지 않은 건(내 차례가 아닌 것 포함)
	InboxTabDone     = "done"     // 완료: 내가 결재했고 결재가 끝난 건
)

// ListInbox 는 결재함 탭별 목록이다. 권한(approval:read)과 열람 범위는 기존 결재함과 같다.
// 진행: 내가 현재 결재자인 아직 끝나지 않은 단계(내 차례 아님 포함, 재무는 재무 단계)이거나 내가 이미 결재했지만 결재가 아직 진행 중인 건. 본인 요청과 대기 탭의 건은 뺀다.
// 완료: 내가 결재·합의·반려한 단계가 있고 건이 결재중이 아닌 끝난 상태(반려·승인·발주완료)인 건.
func (s *ApprovalService) ListInbox(ctx context.Context, tab string) ([]domain.PurchaseRequest, error) {
	switch tab {
	case "", InboxTabPending:
		return s.ListPending(ctx)
	case InboxTabProgress, InboxTabDone:
	default:
		return nil, validation("알 수 없는 결재함 탭입니다")
	}
	p, err := contract.Authorize(ctx, contract.PermApprovalRead)
	if err != nil {
		return nil, err
	}
	ids := map[int64]bool{}
	decided, err := s.Approvals.DecidedPRs(ctx, s.DB, p.UserID)
	if err != nil {
		return nil, err
	}
	for _, id := range decided {
		ids[id] = true
	}
	if tab == InboxTabProgress {
		own, err := s.Approvals.AssignedOpenSteps(ctx, s.DB, p.UserID)
		if err != nil {
			return nil, err
		}
		if p.Can(contract.PermFinanceStage) {
			fin, err := s.Approvals.FinanceOpenSteps(ctx, s.DB)
			if err != nil {
				return nil, err
			}
			own = append(own, fin...)
		}
		for _, ref := range own {
			if ref.RequesterID != p.UserID {
				ids[ref.PRID] = true
			}
		}
		pend, err := s.ListPending(ctx)
		if err != nil {
			return nil, err
		}
		for _, pr := range pend {
			delete(ids, pr.ID)
		}
	}
	order := make([]int64, 0, len(ids))
	for id := range ids {
		order = append(order, id)
	}
	sort.Slice(order, func(i, j int) bool { return order[i] < order[j] })
	out := []domain.PurchaseRequest{}
	for _, id := range order {
		rec, err := s.Purchases.GetPR(ctx, s.DB, id, repository.ScopeAll)
		if err != nil {
			return nil, fmt.Errorf("결재함 건 조회: %w", err)
		}
		open := rec.Status == domain.PRSubmitted || rec.Status == domain.PRInReview
		ended := rec.Status == domain.PRRejected || rec.Status == domain.PRApproved || rec.Status == domain.PROrdered
		if tab == InboxTabProgress && !open || tab == InboxTabDone && !ended {
			continue
		}
		pr := toPR(rec)
		if err := s.decorate(ctx, s.DB, &pr); err != nil {
			return nil, err
		}
		out = append(out, pr)
	}
	s.markPRNames(ctx, out)
	return out, nil
}
