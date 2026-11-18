package service

import (
	"context"
	"log"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/holders"
	"github.com/choibongseok/procure-ledger/internal/repository"
)

// 세금계산서 알림 종류(명세 11절, 정본은 notify.Kinds). 본문에는 건수·종류 이름·앱 링크뿐이고 금액·상대방·번호·사업자번호는 없다(AC-4317).
const (
	NotifyTaxInvoiceFailed = "tax_invoice_failed"
	NotifyTaxInvoiceSync   = "tax_invoice_sync"
	NotifyTaxCertExpiring  = "tax_cert_expiring"
)

// NotifyPermissionHolders 는 권한 보유자 전원에게 앱 안 알림을 같은 트랜잭션에서 만들고 외부 채널 발송 건을 큐에 올린다.
// 발송 건 생성 실패는 알림 자체를 막지 않는다(Base.Deliver 계약). 받는 사람이 없으면 아무것도 하지 않는다.
func (b Base) NotifyPermissionHolders(ctx context.Context, tx contract.Tx, perm contract.Permission, kind, title, body, link, refType string, refID int64, now time.Time) error {
	// 받는 사람은 그 법인(현재 법인)에서 권한이 효력인 사람과 그룹 범위를 가진 사람이다(B44 10절).
	ids, err := holders.Of(ctx, tx, perm, contract.WriteEntityID(ctx), contract.DayOf(now))
	if err != nil {
		return err
	}
	for _, uid := range ids {
		nid, err := repository.SQLNotifications{}.Insert(ctx, tx, repository.NotificationInsert{UserID: uid, Kind: kind, Title: title, Body: body, Link: link, RefType: refType, RefID: refID, Now: now})
		if err != nil {
			return err
		}
		if b.Deliver != nil {
			if derr := b.Deliver(ctx, tx, nid, uid, kind, now); derr != nil {
				log.Printf("service: %s 알림의 발송 건 생성 실패(앱 안 알림은 만들어짐, 알림 %d): %v", kind, nid, derr)
			}
		}
	}
	return nil
}
