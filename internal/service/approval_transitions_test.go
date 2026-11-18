package service

import (
	"errors"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
)

// 허용 전이 표(정본)를 테스트가 따로 한 번 더 적어 둔다. 표가 바뀌면 이 목록도 의식적으로 바꿔야 한다.
var wantTransitions = []struct {
	name string
	from domain.PRStatus
	ev   prEvent
	to   domain.PRStatus
}{
	{"임시저장 → 상신", domain.PRDraft, evSubmit, domain.PRSubmitted},
	{"반려 → 상신(재상신)", domain.PRRejected, evResubmit, domain.PRSubmitted},
	{"상신 → 결재중(중간 단계 승인)", domain.PRSubmitted, evApprovePartial, domain.PRInReview},
	{"상신 → 승인(단일·마지막 단계 승인)", domain.PRSubmitted, evApproveFinal, domain.PRApproved},
	{"상신 → 반려", domain.PRSubmitted, evReject, domain.PRRejected},
	{"결재중 → 결재중(다음 단계 승인)", domain.PRInReview, evApprovePartial, domain.PRInReview},
	{"결재중 → 승인(마지막 단계 승인)", domain.PRInReview, evApproveFinal, domain.PRApproved},
	{"결재중 → 반려", domain.PRInReview, evReject, domain.PRRejected},
	{"상신 → 임시저장(결재자 없음 회수)", domain.PRSubmitted, evWithdraw, domain.PRDraft},
	{"결재중 → 임시저장(결재자 없음 회수)", domain.PRInReview, evWithdraw, domain.PRDraft},
	{"상신 → 임시저장(처리 전 상신 취소)", domain.PRSubmitted, evCancelSubmit, domain.PRDraft},
	{"결재중 → 임시저장(처리 전 상신 취소)", domain.PRInReview, evCancelSubmit, domain.PRDraft},
	{"승인 → 발주완료", domain.PRApproved, evIssuePO, domain.PROrdered},
}

// 전이마다 테스트 한 건: 허용된 전이는 표대로 이동한다.
func TestTransitionAllowed(t *testing.T) {
	for _, tc := range wantTransitions {
		t.Run(tc.name, func(t *testing.T) {
			got, err := transition(tc.from, tc.ev)
			if err != nil || got != tc.to {
				t.Fatalf("%s --%s--> %s 기대, 실제 %q %v", tc.from, tc.ev, tc.to, got, err)
			}
		})
	}
}

// 표에 없는 (상태, 사건) 조합은 전부 거부한다. 전수 검사.
func TestTransitionEverythingElseRejected(t *testing.T) {
	allowed := map[[2]string]bool{}
	for _, tc := range wantTransitions {
		allowed[[2]string{string(tc.from), string(tc.ev)}] = true
	}
	if len(allowedTransitions) == 0 {
		t.Fatal("전이 표가 비었음")
	}
	n := 0
	for _, from := range allStatuses {
		for _, ev := range allEvents {
			to, err := transition(from, ev)
			if allowed[[2]string{string(from), string(ev)}] {
				continue
			}
			n++
			if err == nil || !errors.Is(err, contract.ErrInvalidState) || to != "" {
				t.Errorf("%s --%s--> 거부되어야 함: %q %v", from, ev, to, err)
			}
		}
	}
	if n == 0 {
		t.Fatal("거부 조합이 하나도 검사되지 않음")
	}
	// 알 수 없는 상태·사건도 거부(fail-closed).
	if _, err := transition("unknown", evSubmit); err == nil {
		t.Error("알 수 없는 상태가 허용됨")
	}
	if _, err := transition(domain.PRDraft, "unknown"); err == nil {
		t.Error("알 수 없는 사건이 허용됨")
	}
}

// 표 자체가 wantTransitions 와 정확히 같다(표에 몰래 추가된 전이가 없다).
func TestTransitionTableMatchesSpec(t *testing.T) {
	total := 0
	for _, m := range allowedTransitions {
		total += len(m)
	}
	if total != len(wantTransitions) {
		t.Fatalf("표의 전이 %d 건, 기대 %d 건", total, len(wantTransitions))
	}
}

func TestStatusLabelsAreKorean(t *testing.T) {
	want := map[domain.PRStatus]string{
		domain.PRDraft: "임시저장", domain.PRSubmitted: "상신", domain.PRInReview: "결재중",
		domain.PRRejected: "반려", domain.PRApproved: "승인", domain.PROrdered: "발주완료",
	}
	for s, l := range want {
		if StatusLabel(s) != l {
			t.Errorf("%s -> %s", s, StatusLabel(s))
		}
	}
	_, err := transition(domain.PRDraft, evReject)
	if err == nil || !contains(err.Error(), "임시저장") {
		t.Errorf("오류 문구에 한국어 상태가 있어야 함: %v", err)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
