package service_test

import (
	"testing"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
)

// AC-1117: 도입 전에 확정된 일치 판정은 전표 대기("도입 전 확정")에 올라 있고, 재무가 전기일을 직접 지정해 건별로 전표를 만든다(잘못된 형식은 거부).
func TestPreAdoptionQueueRetryWithPostingDate(t *testing.T) {
	e := newEnv(t)
	q := e.queue()
	po, rc := e.receivedPO(t, 2000, 10)
	vid := e.verdictFor(t, po, rc)
	if _, err := e.db.Exec(`INSERT INTO journal_queue (source_type, source_id, reason, reason_text, po_id, receipt_id, status, created_at) VALUES ('verdict', ?, 'pre_adoption', '도입 전 확정', ?, ?, 'pending', '2026-11-01T00:00:00Z')`, vid, po.ID, rc); err != nil {
		t.Fatal(err)
	}
	if e.col(t, `SELECT COUNT(*) FROM journals WHERE kind = 'auto_purchase'`) != "0" {
		t.Fatal("도입 전 확정 건은 전표가 자동으로 만들어지지 않아야 함")
	}
	items, _ := q.List(as(uFinance), "pending")
	if len(items) != 1 || items[0].Reason != domain.QueuePreAdoption {
		t.Fatalf("대기: %+v", items)
	}
	if _, err := q.Retry(as(uFinance), items[0].ID, "어제"); err == nil {
		t.Fatal("잘못된 전기일 형식이 통과됨")
	} else if !isErr(err, contract.ErrValidation) {
		t.Fatalf("검증 오류가 아님: %v", err)
	}
	done, err := q.Retry(as(uFinance), items[0].ID, "2026-11-03")
	if err != nil || done.Status != domain.QueueCreated || done.JournalNo == "" {
		t.Fatalf("다시 만들기: %+v %v", done, err)
	}
	j, _ := e.journals.Get(as(uFinance), done.JournalNo)
	if j.PostingDate != "2026-11-03" || j.Status != domain.JournalDraft || j.TotalAmount != 22_000 {
		t.Fatalf("전표: %+v", j)
	}
}
