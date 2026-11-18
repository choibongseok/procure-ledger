package service

import (
	"context"
	"fmt"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/repository"
)

// 연차 소멸 예정 알림(B50 9절·13.1, 앱 안 알림). 소멸일이 30일 안으로 다가온 발생 줄마다 남은 분량이 있으면 그 줄에 대해 한 번만 알린다
// (알림의 ref 가 원장 줄 번호라 일일 작업이 몇 번 돌아도 중복되지 않는다). 사용 촉진(제61조) 서면 통지를 대신하지 않는다: 안내일 뿐이다(명세 20절 위험 1, 노무 검토).

const expiryNoticeDays = 30

// NotifyExpiring 은 한 사람의 소멸 예정 알림을 한 트랜잭션에서 만든다. 만든 알림 수를 돌려준다.
func (s *LeaveService) NotifyExpiring(ctx context.Context, userID, entityID int64, asOf time.Time) (int, error) {
	ectx := contract.WithActiveEntity(ctx, contract.ActiveEntity{ID: entityID})
	today := string(contract.DayOf(asOf))
	soon := lvFmtDay(asOf.In(contract.KST).AddDate(0, 0, expiryNoticeDays))
	n := 0
	err := s.Txs.WithTx(ectx, func(tx contract.Tx) error {
		pol, ok, err := s.Repo.PolicyOn(ectx, tx, today)
		if err != nil || !ok {
			return err
		}
		for _, balance := range []string{domain.LeaveBalanceAnnual, domain.LeaveBalanceCompensatory} {
			grants, err := s.Repo.ExpiringGrants(ectx, tx, userID, balance)
			if err != nil {
				return err
			}
			consumed, err := s.Repo.ConsumedMinutes(ectx, tx, userID, balance)
			if err != nil {
				return err
			}
			rem := fifoRemaining(grants, consumed)
			for i, g := range grants {
				if rem[i] <= 0 || g.ExpiresOn <= today || g.ExpiresOn > soon {
					continue
				}
				var have int
				if err := tx.QueryRowContext(ectx, `SELECT COUNT(*) FROM notifications WHERE user_id = ? AND ref_type = 'leave_expiry' AND ref_id = ?`, userID, g.ID).Scan(&have); err != nil {
					return err
				}
				if have > 0 {
					continue
				}
				if err := s.notify(ectx, tx, repository.NotificationInsert{UserID: userID, Kind: "leave_expiring", Title: "소멸 예정 휴가가 있습니다",
					Body: fmt.Sprintf("%s %s 이(가) %s 에 소멸합니다. 소멸 전에 사용하세요.", domain.LeaveBalanceLabels[balance], fmtMinutes(rem[i], pol.WorkdayMinutes), g.ExpiresOn),
					Link: "/attendance/leave", RefType: "leave_expiry", RefID: g.ID}); err != nil {
					return err
				}
				n++
			}
		}
		return nil
	})
	return n, err
}
