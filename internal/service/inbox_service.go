package service

import (
	"context"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/repository"
)

// InboxService 는 문서함 7종 조회와 읽음 처리다(B23 단계 2, 권한 inbox:read).
// 열람 조건은 구매요청·양식 문서와 같은 ViewScope 한 곳이다. 문서함은 그 위에 관계 조건만 더한다. 저장되는 것은 읽음 상태뿐이다.
type InboxService struct {
	Base
	Inbox    repository.SQLInbox
	Purchase repository.PurchaseRepository
	approval *ApprovalService
}

var _ domain.InboxService = (*InboxService)(nil)

// NewInboxService 는 SQLite 저장소로 문서함 서비스를 만든다. 결재 대기함은 결재함 판정(ListPending) 한 곳을 그대로 쓴다.
func NewInboxService(b Base) *InboxService {
	return &InboxService{Base: b, Purchase: repository.SQLPurchases{}, approval: NewApprovalService(b)}
}

func validBox(b domain.InboxBox) bool {
	for _, x := range domain.InboxBoxes {
		if x == b {
			return true
		}
	}
	return false
}

// pendingIDs 는 지금 내 차례인 문서 번호다(위임분 포함, 결재 후보 판정과 같은 함수).
func (s *InboxService) pendingIDs(ctx context.Context) ([]int64, error) {
	list, err := s.approval.ListPending(ctx)
	if err != nil {
		return nil, err
	}
	ids := make([]int64, len(list))
	for i, p := range list {
		ids[i] = p.ID
	}
	return ids, nil
}

func (s *InboxService) base(ctx context.Context, p contract.Principal) (repository.InboxQuery, error) {
	scope, err := s.viewScope(ctx, p)
	if err != nil {
		return repository.InboxQuery{}, err
	}
	pend, err := s.pendingIDs(ctx)
	if err != nil {
		return repository.InboxQuery{}, err
	}
	return repository.InboxQuery{UserID: p.UserID, Scope: scope, PendingIDs: pend, ReceiveUnits: scope.ReceiveUnits}, nil
}

// List 는 문서함 한 곳의 목록이다.
func (s *InboxService) List(ctx context.Context, box domain.InboxBox, f domain.InboxFilter) ([]domain.InboxItem, error) {
	p, err := contract.Authorize(ctx, contract.PermInboxRead)
	if err != nil {
		return nil, err
	}
	if !validBox(box) {
		return nil, validation("알 수 없는 문서함입니다")
	}
	q, err := s.base(ctx, p)
	if err != nil {
		return nil, err
	}
	q.Box, q.Tab, q.Limit, q.Offset = box, f.Tab, f.Limit, f.Offset
	out, err := s.Inbox.List(ctx, s.DB, q)
	if err != nil {
		return nil, err
	}
	if out == nil {
		out = []domain.InboxItem{}
	}
	return out, nil
}

// Counts 는 문서함별 건수와 안 읽은 건수다(수신함은 처리 전 건만 센다).
func (s *InboxService) Counts(ctx context.Context) (map[domain.InboxBox]domain.InboxCount, error) {
	p, err := contract.Authorize(ctx, contract.PermInboxRead)
	if err != nil {
		return nil, err
	}
	q, err := s.base(ctx, p)
	if err != nil {
		return nil, err
	}
	out := map[domain.InboxBox]domain.InboxCount{}
	for _, box := range domain.InboxBoxes {
		q.Box = box
		c, err := s.Inbox.Count(ctx, s.DB, q)
		if err != nil {
			return nil, err
		}
		out[box] = c
	}
	return out, nil
}

// MarkRead 는 문서를 읽음으로 표시한다. 열람 범위 밖이면 찾을 수 없음이다. 감사 기록은 남기지 않는다(본인 상태 변경).
func (s *InboxService) MarkRead(ctx context.Context, docID int64) error {
	p, err := contract.Authorize(ctx, contract.PermInboxRead)
	if err != nil {
		return err
	}
	scope, err := s.viewScope(ctx, p)
	if err != nil {
		return err
	}
	if _, err := s.Purchase.GetPR(ctx, s.DB, docID, scope); err != nil {
		return err
	}
	return s.Txs.WithTx(ctx, func(tx contract.Tx) error { return s.Inbox.MarkRead(ctx, tx, docID, p.UserID, s.now()) })
}

// touchRead 는 문서 상세를 연 사실을 읽음으로 남긴다(웹 채널만, 실패해도 조회는 계속한다).
func (b Base) touchRead(ctx context.Context, p contract.Principal, docID int64) {
	if p.Channel == contract.ChannelMCP || p.UserID == 0 {
		return
	}
	_ = b.Txs.WithTx(ctx, func(tx contract.Tx) error {
		return repository.SQLInbox{}.MarkRead(ctx, tx, docID, p.UserID, b.now())
	})
}
