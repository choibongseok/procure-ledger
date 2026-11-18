package service

import (
	"testing"

	"github.com/choibongseok/procure-ledger/internal/contract/domain"
)

// AC-434 실사 표 상태 전이: 표에 있는 전이만 되고 나머지는 모두 거부된다(상태 x 이벤트 전수).
func TestCountTransitionsExhaustive(t *testing.T) {
	allow := map[string]map[countEvent]string{
		"":                    {cevCreate: domain.CountDraft},
		domain.CountDraft:     {cevEdit: domain.CountDraft, cevNoDiff: domain.CountNoDiff, cevSubmit: domain.CountApproving, cevDiscard: domain.CountDiscarded},
		domain.CountRejected:  {cevEdit: domain.CountRejected, cevNoDiff: domain.CountNoDiff, cevSubmit: domain.CountApproving, cevDiscard: domain.CountDiscarded},
		domain.CountApproving: {cevApprove: domain.CountApplied, cevReject: domain.CountRejected, cevReset: domain.CountDraft},
	}
	states := []string{"", domain.CountDraft, domain.CountApproving, domain.CountApplied, domain.CountRejected, domain.CountNoDiff, domain.CountDiscarded}
	events := []countEvent{cevCreate, cevEdit, cevSubmit, cevNoDiff, cevApprove, cevReject, cevReset, cevDiscard}
	for _, from := range states {
		for _, ev := range events {
			to, err := countTransition(from, ev)
			want, ok := allow[from][ev]
			switch {
			case ok && (err != nil || to != want):
				t.Errorf("%q --%s--> %q 이어야 하는데 %q, %v", from, ev, want, to, err)
			case !ok && err == nil:
				t.Errorf("%q --%s--> 는 거부돼야 하는데 %q 로 전이됨", from, ev, to)
			}
		}
	}
	for _, final := range []string{domain.CountApplied, domain.CountNoDiff, domain.CountDiscarded} {
		for _, ev := range events {
			if _, err := countTransition(final, ev); err == nil {
				t.Errorf("끝난 상태 %s 에서 %s 가 허용됨", final, ev)
			}
		}
	}
}
