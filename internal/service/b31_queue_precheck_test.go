package service_test

import (
	"strings"
	"testing"
)

// 명세 3.7 의 3: 처리하지 않은 전표 대기는 월 마감 점검의 경고다(차단 아님). 처리하면 사라진다.
func TestMonthClosePrecheckWarnsPendingJournalQueue(t *testing.T) {
	e := newEnv(t)
	if _, err := e.db.Exec(`INSERT INTO journal_queue (source_type, source_id, reason, reason_text, status, created_at) VALUES ('verdict', 1, 'hold_confirmed', '보류 확정', 'pending', '2026-11-05T01:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	has := func() bool {
		pc, err := e.monthClose.Precheck(as(uFinance), "2026-11")
		if err != nil {
			t.Fatal(err)
		}
		for _, w := range pc.Warnings {
			if w.Title == "처리하지 않은 전표 대기" && strings.Contains(w.Detail, "1건") {
				return true
			}
		}
		return false
	}
	if !has() {
		t.Fatal("전표 대기 경고가 없음")
	}
	if _, err := e.queue().Skip(as(uFinance), 1, "불필요"); err != nil {
		t.Fatal(err)
	}
	if has() {
		t.Fatal("처리한 뒤에도 경고가 남음")
	}
}
