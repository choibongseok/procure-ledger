package service

import (
	"fmt"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
)

// prEvent 는 구매요청 상태를 바꾸는 사건이다.
type prEvent string

const (
	evSubmit         prEvent = "submit"          // 임시저장 → 상신
	evResubmit       prEvent = "resubmit"        // 반려 → 상신(재상신)
	evApprovePartial prEvent = "approve_partial" // 마지막이 아닌 단계 승인
	evApproveFinal   prEvent = "approve_final"   // 마지막 단계 승인
	evReject         prEvent = "reject"          // 어느 단계에서든 반려
	evIssuePO        prEvent = "issue_po"        // 승인 → 발주완료(PO 생성)
	evWithdraw       prEvent = "withdraw"        // 결재자가 없어 멈춘 상신·결재중 → 임시저장(요청자 회수)
	evCancelSubmit   prEvent = "cancel_submit"   // 아무도 처리하지 않은 상신·결재중 → 임시저장(기안자 상신 취소, D9 확정)
)

var allEvents = []prEvent{evSubmit, evResubmit, evApprovePartial, evApproveFinal, evReject, evIssuePO, evWithdraw, evCancelSubmit}

var allStatuses = []domain.PRStatus{
	domain.PRDraft, domain.PRSubmitted, domain.PRInReview, domain.PRRejected, domain.PRApproved, domain.PROrdered,
}

// allowedTransitions 는 구매요청 상태 전이의 유일한 정의다.
// 표에 없는 (현재 상태, 사건) 조합은 전부 거부한다. 상태를 바꾸는 코드는 반드시 transition 을 거친다.
var allowedTransitions = map[domain.PRStatus]map[prEvent]domain.PRStatus{
	domain.PRDraft: {
		evSubmit: domain.PRSubmitted,
	},
	domain.PRRejected: {
		evResubmit: domain.PRSubmitted,
	},
	domain.PRSubmitted: {
		evApprovePartial: domain.PRInReview,
		evApproveFinal:   domain.PRApproved,
		evReject:         domain.PRRejected,
		evWithdraw:       domain.PRDraft,
		evCancelSubmit:   domain.PRDraft,
	},
	domain.PRInReview: {
		evApprovePartial: domain.PRInReview,
		evApproveFinal:   domain.PRApproved,
		evReject:         domain.PRRejected,
		evWithdraw:       domain.PRDraft,
		evCancelSubmit:   domain.PRDraft,
	},
	domain.PRApproved: {
		evIssuePO: domain.PROrdered,
	},
	// 발주완료는 종점이다.
}

// transition 은 허용된 전이면 다음 상태를, 아니면 ErrInvalidState 를 돌려준다.
func transition(from domain.PRStatus, ev prEvent) (domain.PRStatus, error) {
	if to, ok := allowedTransitions[from][ev]; ok {
		return to, nil
	}
	return "", fmt.Errorf("%w: %s 상태에서는 %s 할 수 없습니다", contract.ErrInvalidState, StatusLabel(from), eventLabel(ev))
}

// canEdit 은 작성 내용(품목·금액)을 고칠 수 있는 상태다. 상태가 바뀌지 않으므로 전이 표 밖이다.
func canEdit(s domain.PRStatus) bool { return s == domain.PRDraft || s == domain.PRRejected }

// StatusLabel 은 화면·오류 문구용 한국어 상태 이름이다.
func StatusLabel(s domain.PRStatus) string {
	switch s {
	case domain.PRDraft:
		return "임시저장"
	case domain.PRSubmitted:
		return "상신"
	case domain.PRInReview:
		return "결재중"
	case domain.PRRejected:
		return "반려"
	case domain.PRApproved:
		return "승인"
	case domain.PROrdered:
		return "발주완료"
	}
	return string(s)
}

func eventLabel(ev prEvent) string {
	switch ev {
	case evSubmit:
		return "상신"
	case evResubmit:
		return "재상신"
	case evApprovePartial, evApproveFinal:
		return "승인"
	case evReject:
		return "반려"
	case evIssuePO:
		return "발주"
	case evWithdraw:
		return "회수"
	case evCancelSubmit:
		return "상신 취소"
	}
	return string(ev)
}
