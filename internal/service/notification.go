package service

import (
	"context"
	"log"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/repository"
)

// NotificationService 는 domain.NotificationService 의 구현이다(B21 3.9). 호출자 본인 알림만 다룬다.
// 알림은 앱 안에서만 보인다(메일·메신저 발송 없음). 읽음 처리는 본인 상태 변경이라 감사 로그를 남기지 않는다.
type NotificationService struct {
	Base
	Notes repository.SQLNotifications
}

var _ domain.NotificationService = (*NotificationService)(nil)

// NewNotificationService 는 SQLite 저장소로 알림 서비스를 만든다.
func NewNotificationService(b Base) *NotificationService { return &NotificationService{Base: b} }

const (
	notificationDefaultLimit = 50
	notificationMaxLimit     = 100
)

// notify 는 업무 서비스가 쓰기 트랜잭션 안에서 알림을 만드는 내부 함수다(실패하면 호출자 트랜잭션이 함께 롤백된다).
func (b Base) notify(ctx context.Context, tx contract.Tx, in repository.NotificationInsert) error {
	if in.Now.IsZero() {
		in.Now = b.now()
	}
	id, err := repository.SQLNotifications{}.Insert(ctx, tx, in)
	if err != nil {
		return err
	}
	if b.Deliver != nil { // 메일 채널 발송 건(B36). 실패해도 앱 안 알림과 업무는 그대로다
		if derr := b.Deliver(ctx, tx, id, in.UserID, in.Kind, in.Now); derr != nil {
			log.Printf("notify: 발송 건 생성 실패(앱 안 알림은 만들어짐, 알림 %d): %v", id, derr)
		}
	}
	return nil
}

// List 는 내 알림(최신순)이다.
func (s *NotificationService) List(ctx context.Context, f domain.NotificationFilter) ([]domain.Notification, error) {
	p, err := contract.Authorize(ctx, contract.PermNotificationRead)
	if err != nil {
		return nil, err
	}
	limit := f.Limit
	if limit <= 0 {
		limit = notificationDefaultLimit
	}
	if limit > notificationMaxLimit {
		limit = notificationMaxLimit
	}
	return s.Notes.List(ctx, s.DB, p.UserID, f.UnreadOnly, limit, f.BeforeID)
}

// UnreadCount 는 내 안 읽은 알림 수다.
func (s *NotificationService) UnreadCount(ctx context.Context) (int, error) {
	p, err := contract.Authorize(ctx, contract.PermNotificationRead)
	if err != nil {
		return 0, err
	}
	return s.Notes.UnreadCount(ctx, s.DB, p.UserID)
}

// MarkRead 는 내 알림 한 건을 읽음 처리한다. 남의 알림·없는 번호는 ErrNotFound 다.
func (s *NotificationService) MarkRead(ctx context.Context, id int64) error {
	p, err := contract.Authorize(ctx, contract.PermNotificationRead)
	if err != nil {
		return err
	}
	return s.Txs.WithTx(ctx, func(tx contract.Tx) error {
		return s.Notes.MarkRead(ctx, tx, p.UserID, id, s.now())
	})
}

// MarkAllRead 는 내 안 읽은 알림을 모두 읽음 처리하고 그 건수를 돌려준다.
func (s *NotificationService) MarkAllRead(ctx context.Context) (int, error) {
	p, err := contract.Authorize(ctx, contract.PermNotificationRead)
	if err != nil {
		return 0, err
	}
	n := 0
	err = s.Txs.WithTx(ctx, func(tx contract.Tx) error {
		var e error
		n, e = s.Notes.MarkAllRead(ctx, tx, p.UserID, s.now())
		return e
	})
	return n, err
}
