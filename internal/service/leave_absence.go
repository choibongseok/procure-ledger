package service

import (
	"context"
	"fmt"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/repository"
)

// 휴가 신청과 부재 설정(B29)의 연결(B50 6.7). 신청서의 대결자는 B29 의 대결자 자격 규칙(DelegationService.canDelegateTo)을 그대로 쓰고,
// 최종 승인 때 훅이 같은 트랜잭션에서 부재 행(approval_delegations kind=absence, 사유 휴가)을 만든다. 휴가를 취소하면 그 부재 행을 해제한다.
// 훅은 결재 트랜잭션 안에서 돌고 DB 연결이 하나라 조직 조회를 할 수 없다. 그래서 직책 서열 검사는 신청 때(트랜잭션 밖)에 하고,
// 훅은 트랜잭션 핸들로만 읽는 검사(기간 90일·겹침·수임자 재직과 결재 권한)만 한다. 어느 검사든 어긋나면 부재 행만 건너뛰고 승인은 막지 않는다.

// checkDelegate 는 신청서에 적은 대결자가 부재 대결자 자격을 갖췄는지 본다(트랜잭션 밖에서 부른다). 대결자가 없으면 아무것도 하지 않는다.
func (s *LeaveService) checkDelegate(ctx context.Context, p contract.Principal, delegateID int64) error {
	if delegateID == 0 {
		return nil
	}
	ds := NewDelegationService(s.Base)
	day := contract.DayOf(s.now())
	from, err := ds.Delegations.User(ctx, s.DB, p.UserID)
	if err != nil {
		return err
	}
	title, err := ds.delegatorTitle(ctx, from, day)
	if err != nil {
		return err
	}
	if title == "" {
		return validation("결재 단계를 맡고 있지 않아 대결자를 지정할 수 없습니다. 대결자 칸을 비워 주세요.")
	}
	to, err := ds.Delegations.User(ctx, s.DB, delegateID)
	if err != nil {
		return validation("이 사람은 대결자로 지정할 수 없습니다(사용자를 찾을 수 없습니다). 후보 목록에서 다른 사람을 고르세요.")
	}
	if ok, err := s.sameEntityTarget(ctx, s.DB, "user", to.ID, day); err != nil {
		return err
	} else if !ok {
		return validation(errDifferentEntity)
	}
	if ok, err := ds.canDelegateTo(ctx, from, to, day); err != nil {
		return err
	} else if !ok {
		return validation("이 사람은 대결자로 지정할 수 없습니다(%s 결재는 같은 직책 이상의 조직장만 맡을 수 있습니다). 후보 목록에서 다른 사람을 고르세요.", title)
	}
	return nil
}

// leavePeriodTimes 는 휴가 기간(KST 날짜 둘)을 부재 행의 시각 구간으로 바꾼다: 시작일 0시부터 종료일 다음 날 0시 전까지.
func leavePeriodTimes(start, end string) (time.Time, time.Time, error) {
	a, err := time.ParseInLocation("2006-01-02", start, contract.KST)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	b, err := time.ParseInLocation("2006-01-02", end, contract.KST)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	return a.UTC(), b.AddDate(0, 0, 1).UTC(), nil
}

// createAbsence 는 승인된 휴가의 대결자로 부재 행을 만든다. 만들었으면 행 번호를, 건너뛰었으면 0 과 이유(문서 안내 문구)를 돌려준다. 오류는 저장 실패뿐이다.
func (s *LeaveService) createAbsence(ctx context.Context, tx contract.Tx, row repository.LeaveReqRow) (int64, string, error) {
	if row.DelegateUserID == 0 {
		return 0, "", nil
	}
	skip := func(why string) (int64, string, error) {
		return 0, "부재 설정을 만들지 못했습니다(" + why + ")", nil
	}
	from, to, err := leavePeriodTimes(row.StartDate, row.EndDate)
	if err != nil {
		return skip("휴가 기간이 올바르지 않습니다")
	}
	if to.Sub(from) > maxAbsenceDays*24*time.Hour {
		return skip("부재는 한 번에 90일까지 등록할 수 있습니다")
	}
	id, why, err := NewDelegationService(s.Base).createLeaveAbsence(ctx, tx, row.UserID, row.DelegateUserID, from, to)
	if err != nil {
		return 0, "", err
	}
	if why != "" {
		return skip(why)
	}
	return id, "", nil
}

// revokeAbsence 는 휴가가 취소되면 승인 때 만든 부재 행을 해제한다(이미 해제됐으면 아무것도 하지 않는다).
func (s *LeaveService) revokeAbsence(ctx context.Context, tx contract.Tx, row repository.LeaveReqRow) error {
	if row.AbsenceDelegationID == 0 {
		return nil
	}
	ds := NewDelegationService(s.Base)
	d, err := ds.Delegations.Get(ctx, tx, row.AbsenceDelegationID)
	if err != nil || d.RevokedAt != nil {
		return nil
	}
	now := s.now()
	if err := ds.Delegations.Revoke(ctx, tx, d.ID, now); err != nil {
		return fmt.Errorf("부재 설정 해제: %w", err)
	}
	return ds.notifyAbsenceRevoked(ctx, tx, d, now)
}
