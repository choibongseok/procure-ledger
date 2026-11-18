package org

import (
	"context"
	"database/sql"
	"errors"
	"log"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/privacy"
)

// 휴직 사유 원문 열람(B50 10.2·11.2). 사유는 이력 목록·감사·알림에 싣지 않고 이 조회로만 읽는다.
//
//   - 본인: 기록 없이 본다.
//   - 직속 조직장(attendance:team, 오늘 기준 헤드 범위): 기록한 뒤 본다.
//   - 인사 담당(attendance:hr, 같은 법인): 기록한 뒤 본다.
//   - 그 밖(관리자·감사자·다른 팀·다른 법인): 없는 것과 같다(ErrNotFound).
//
// 열람 기록(B41 범주 attendance)은 응답을 만들기 전에 남기고, 기록하지 못하면 사유 없이 실패한다(fail-closed).
// 기록기가 연결되지 않은 서비스는 사유를 내지 않는다(조용히 통과하지 않는다).

// AbsenceReasonView 는 휴직 발령 한 건의 사유다.
type AbsenceReasonView struct {
	EventID int64
	UserID  int64
	Reason  string
}

// AbsenceReason 은 휴직 발령(이벤트 번호)의 사유 원문을 돌려준다.
func (s *Service) AbsenceReason(ctx context.Context, p contract.Principal, eventID int64) (AbsenceReasonView, error) {
	if _, err := contract.Authorize(contract.WithPrincipal(ctx, p), contract.PermAttendanceSelf); err != nil {
		return AbsenceReasonView{}, err
	}
	e, err := s.loadEvent(ctx, s.db, eventID)
	if err != nil {
		return AbsenceReasonView{}, err
	}
	if e.Kind != KindAbsenceStart || e.Subject != SubjectPerson {
		return AbsenceReasonView{}, contract.ErrNotFound
	}
	self := e.SubjectID == p.UserID
	if !self {
		ok, err := s.canSeeAbsenceReason(ctx, p, e.SubjectID)
		if err != nil {
			return AbsenceReasonView{}, err
		}
		if !ok {
			return AbsenceReasonView{}, contract.ErrNotFound
		}
	}
	out := AbsenceReasonView{EventID: e.ID, UserID: e.SubjectID}
	if e.P.AbsenceReason == "" {
		return out, nil // 내줄 원문이 없으면 기록할 열람도 없다
	}
	if !self {
		if s.priv == nil {
			return AbsenceReasonView{}, ErrPrivacyNote
		}
		if err := s.priv.Note(ctx, p, privacy.Access{Category: privacy.CatAttendance, Screen: privacy.ScreenAttendanceReason,
			Fields: []string{privacy.FieldAbsenceReason}, Subjects: []privacy.Subject{{Type: privacy.SubjectUser, ID: e.SubjectID}}}); err != nil {
			log.Printf("org: 휴직 사유 열람 기록 실패: %v", err)
			return AbsenceReasonView{}, ErrPrivacyNote
		}
	}
	out.Reason = e.P.AbsenceReason
	return out, nil
}

// canSeeAbsenceReason 은 본인이 아닌 호출자가 대상의 휴직 사유를 볼 수 있는지다(인사 담당 또는 직속 조직장).
func (s *Service) canSeeAbsenceReason(ctx context.Context, p contract.Principal, target int64) (bool, error) {
	day := s.Today()
	if p.Can(contract.PermAttendanceHR) {
		var ent int64
		err := s.db.QueryRowContext(ctx, `SELECT us.entity_id FROM org_person_spans ps JOIN org_unit_spans us ON us.unit_id = ps.primary_unit_id
			AND us.valid_from <= ?1 AND (us.valid_to IS NULL OR us.valid_to > ?1)
			WHERE ps.user_id = ?2 AND ps.valid_from <= ?1 AND (ps.valid_to IS NULL OR ps.valid_to > ?1) LIMIT 1`, string(day), target).Scan(&ent)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			return false, nil
		case err != nil:
			return false, err
		}
		return ent == contract.WriteEntityID(ctx), nil
	}
	if p.Can(contract.PermAttendanceTeam) {
		units, err := s.HeadedScope(ctx, p.UserID, day)
		if err != nil {
			return false, err
		}
		person, err := s.Person(ctx, target, day)
		if err != nil {
			return false, nil // 조직에 없는 사람은 볼 수 없다
		}
		for _, u := range units {
			if u == person.Primary.Unit.ID {
				return true, nil
			}
		}
	}
	return false, nil
}
