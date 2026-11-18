package service

import (
	"context"
	"fmt"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/repository"
)

// 승인된 휴가 기간에 휴직이 발령되면(B50 16절) 휴직 구간과 겹치는 소정근로일 분량은 연차를 쓰지 않은 것으로 되돌린다.
// 휴가 신청 자체는 그대로이고, 원장에 복원 줄(release, ref_type=leave_absence)을 더한다. 줄은 추가만 하고(원장 불변식) 합은 항상 "대상 분량 − 이미 복원한 분량" 만큼만 움직여
// 같은 발령을 다시 처리해도(멱등) 두 번 돌려주지 않고, 휴직을 취소·정정해 구간이 줄면 같은 식으로 되돌림 줄(use)을 더해 맞춘다.

const (
	absenceRestoreRef    = "leave_absence"
	absenceRestoreReason = "휴직 발령: 연차 복원"
	absenceRevertReason  = "휴직 변경: 복원 되돌림"

	// beginningOfTime 은 어떤 날짜보다 앞서는 문자열 비교용 하한이다(날짜 가정이 아니라 "처음부터"를 뜻한다).
	beginningOfTime = "0000-00-00"
)

// absenceRestoreTarget 은 신청 분량 중 휴직 구간이 덮는 소정근로일의 몫이다(biz 는 신청 기간의 소정근로일). 하루짜리 반차·시간 휴가는 그날이 덮이면 전부다.
func absenceRestoreTarget(spans []repository.AbsenceSpan, minutes int64, biz []string) int64 {
	if len(biz) == 0 || minutes <= 0 {
		return 0
	}
	covered := 0
	for _, d := range biz {
		for _, sp := range spans {
			if sp.Covers(d) {
				covered++
				break
			}
		}
	}
	return minutes * int64(covered) / int64(len(biz))
}

// RestoreForAbsence 는 한 사람의 승인된 휴가를 현재 휴직 구간에 맞춰 복원 줄로 맞춘다. 조직 서비스가 휴직·복직·취소·정정과 같은 트랜잭션에서 부른다(훅).
func (s *LeaveService) RestoreForAbsence(ctx context.Context, tx contract.Tx, actor contract.Principal, userID int64) error {
	spans, err := s.Repo.AbsenceSpans(ctx, tx, userID, beginningOfTime, "9999-12-31")
	if err != nil {
		return err
	}
	from := beginningOfTime
	if len(spans) > 0 {
		from = spans[0].From
	}
	all := contractEntityAll(ctx)
	rows, err := s.Repo.ApprovedRequestsFrom(all, tx, userID, from)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		return nil
	}
	// 복원 줄을 쓰는 동안 같은 사람의 신청·승인과 부딪치지 않게 사람 단위로 잠근다(상신·취소와 같은 잠금).
	if err := s.Repo.LockUser(ctx, tx, userID); err != nil {
		return err
	}
	for _, r := range rows {
		ectx := contract.WithActiveEntity(ctx, contract.ActiveEntity{ID: r.EntityID})
		a, err1 := lvParseDay(r.StartDate)
		b, err2 := lvParseDay(r.EndDate)
		if err1 != nil || err2 != nil {
			continue
		}
		var biz []string
		for d := a; !d.After(b); d = d.AddDate(0, 0, 1) {
			ds := lvFmtDay(d)
			ok, err := s.isBusinessDay(ectx, tx, ds)
			if err != nil {
				return err
			}
			if ok {
				biz = append(biz, ds)
			}
		}
		target := absenceRestoreTarget(spans, r.Minutes, biz)
		done, err := s.Repo.AbsenceRestoredMinutes(all, tx, r.ID)
		if err != nil {
			return err
		}
		delta := target - done
		if delta == 0 {
			continue
		}
		e := repository.LeaveLedgerEntry{EntityID: r.EntityID, UserID: userID, Balance: r.Balance, Kind: "release", Minutes: delta, RefType: absenceRestoreRef, RefID: r.ID, Reason: absenceRestoreReason, By: actor.UserID, At: s.now()}
		if delta < 0 {
			e.Kind, e.Reason = "use", absenceRevertReason
		}
		if _, err := s.Repo.Append(ectx, tx, e); err != nil {
			return err
		}
		if err := s.record(ctx, tx, actor, "leave.absence_restore", audit.TargetLeaveRequest, r.ID, audit.DiffItem{Key: "minutes", New: fmt.Sprint(delta)}); err != nil {
			return err
		}
	}
	return nil
}
