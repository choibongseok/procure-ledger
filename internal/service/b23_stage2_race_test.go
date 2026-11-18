package service_test

import (
	"sync"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/contract/domain"
)

// E27: 상신 취소와 첫 결재가 동시에 들어오면 둘 중 하나만 이긴다(취소됐는데 승인이 남거나, 승인됐는데 취소되는 일이 없다).
func TestStage2CancelSubmitRacesWithDecision(t *testing.T) {
	for i := 0; i < 8; i++ {
		e := newEnv(t)
		_, _, ap := e.stage2()
		pr := e.genSubmitted(t, uStaff, "경합")
		var wg sync.WaitGroup
		var cancelErr, decideErr error
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, cancelErr = e.docs().CancelSubmit(as(uStaff), pr.ID, "경합 시험")
		}()
		go func() {
			defer wg.Done()
			_, decideErr = ap.Decide(as(uLead), pr.ID, domain.Decision{Approve: true})
		}()
		wg.Wait()
		got, _ := e.docs().Get(as(uStaff), pr.ID)
		approved := count(t, e.db, `SELECT COUNT(*) FROM approval_history WHERE pr_id = ? AND action = 'approve'`, pr.ID)
		switch {
		case cancelErr == nil && decideErr == nil:
			t.Fatalf("둘 다 성공함: %+v", got.Status)
		case cancelErr == nil:
			if got.Status != domain.PRDraft || approved != 0 {
				t.Fatalf("취소됐는데 상태 %s, 승인 이력 %d", got.Status, approved)
			}
		case decideErr == nil:
			if got.Status == domain.PRDraft || approved != 1 {
				t.Fatalf("승인됐는데 상태 %s, 승인 이력 %d", got.Status, approved)
			}
		default:
			t.Fatalf("둘 다 실패: %v / %v", cancelErr, decideErr)
		}
	}
}

// AC-201: 읽은 뒤 같은 시각(초 단위로는 구분되지 않는 순간)에 문서가 갱신되어도 다시 안 읽음이 된다(갱신 차수 비교).
func TestStage2UnreadAfterSameSecondUpdate(t *testing.T) {
	e := newEnv(t)
	in, _, _ := e.stage2()
	pr := e.genSubmitted(t, uStaff, "같은 초 갱신", domain.LineEdit{Role: "cc", TargetType: "user", TargetID: uStaff2})
	if err := in.MarkRead(as(uStaff2), pr.ID); err != nil {
		t.Fatal(err)
	}
	if it := inboxIDs(t, in, uStaff2, domain.BoxCC)[pr.ID]; it.Unread {
		t.Fatal("읽음 처리 뒤에도 안 읽음")
	}
	decideAs(t, e, uLead, pr.ID, true, "") // 시험 시계가 멈춰 있어 읽은 시각과 갱신 시각이 같다
	if it := inboxIDs(t, in, uStaff2, domain.BoxCC)[pr.ID]; !it.Unread {
		t.Fatal("읽은 뒤 같은 시각에 갱신됐는데 읽음으로 남음")
	}
	if err := in.MarkRead(as(uStaff2), pr.ID); err != nil {
		t.Fatal(err)
	}
	if it := inboxIDs(t, in, uStaff2, domain.BoxCC)[pr.ID]; it.Unread {
		t.Fatal("다시 읽었는데 안 읽음")
	}
}
