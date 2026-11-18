package service

import (
	"context"
	"fmt"
	"log"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/privacy"
	"github.com/choibongseok/procure-ledger/internal/repository"
)

// leaveProofGate 는 휴가 신청서(LEAVE)에 붙은 첨부를 내려받을 수 있는지 정한다(B50 11.4, AC-5043). 신청자 본인, 직속 조직장(범위 안), 인사 담당(attendance:hr)만 받는다(감사자는 인사 담당 권한을 받아야 한다: 역할 이름으로 판정하지 않는다).
// 결재선의 다른 결재자는 파일 이름만 볼 수 있다. 본인이 아닌 사람이 받으면 응답 전에 B41 열람 기록(범주 attendance, 필드 증빙 파일)과 감사를 남기고,
// 기록하지 못하면 내려주지 않는다(fail-closed). 휴가 문서가 아니면 아무것도 하지 않는다.
func (s *AttachmentService) leaveProofGate(ctx context.Context, p contract.Principal, docID, attachmentID int64) (bool, error) {
	owner, isLeave, err := (repository.SQLLeave{}).LeaveDocUser(ctx, s.DB, docID)
	if err != nil {
		return false, err
	}
	if !isLeave {
		return false, nil
	}
	if owner == p.UserID {
		return true, nil
	}
	allowed, err := leaveService(s.Base).canViewUser(ctx, p, owner)
	if err != nil {
		return true, err
	}
	if !allowed {
		return true, fmt.Errorf("%w: 휴가 증빙은 신청자·직속 조직장·인사 담당만 내려받을 수 있습니다", contract.ErrForbidden)
	}
	if s.Privacy == nil {
		return true, ErrLeavePrivacyNote
	}
	if err := s.Privacy.Note(ctx, p, privacy.Access{Category: privacy.CatAttendance, Screen: privacy.ScreenAttendanceProof, Fields: []string{privacy.FieldProofFile},
		Subjects: []privacy.Subject{{Type: privacy.SubjectUser, ID: owner}}}); err != nil {
		log.Printf("leave: 증빙 열람 기록 실패: %v", err)
		return true, ErrLeavePrivacyNote
	}
	return true, s.write(ctx, p, "leave.proof_download", audit.TargetLeaveRequest, attachmentID, func(tx contract.Tx) error {
		return s.record(ctx, tx, p, "leave.proof_download", audit.TargetLeaveRequest, attachmentID, audit.DiffItem{Key: "subject_user", New: fmt.Sprint(owner)})
	})
}
