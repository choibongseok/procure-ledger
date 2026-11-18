package service_test

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/dbutil"
)

const fixReason = "11월 말 입고 단가를 정정해야 합니다"

// 검증자 B2: 같은 달에 해제 요청 4건을 동시에 넣어도 요청서는 한 건만 상신된다.
func TestMonthReleaseConcurrentRequestsOnlyOne(t *testing.T) {
	e := newEnv(t)
	e.closeFixture(t)
	e.mcrForm(t)
	for _, m := range []string{"2026-10", "2026-11"} {
		if _, err := e.monthClose.Close(as(uFinance), m); err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	results := make([]error, 4)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, results[i] = e.monthClose.RequestRelease(as(uFinance), "2026-11", fixReason)
		}(i)
	}
	wg.Wait()
	ok := 0
	for _, err := range results {
		if err == nil {
			ok++
		} else if !isErr(err, contract.ErrInvalidState) {
			t.Fatalf("중복 요청은 상태 오류여야 함: %v", err)
		}
	}
	if ok != 1 {
		t.Fatalf("성공한 요청 %d건(기대 1)", ok)
	}
	if n := count(t, e.db, `SELECT COUNT(*) FROM purchase_requests p JOIN approval_forms f ON f.id = p.form_id WHERE f.code = 'MCR' AND p.status IN ('submitted','in_review')`); n != 1 {
		t.Fatalf("결재 중인 해제 요청서 %d건(기대 1)", n)
	}
	if n := count(t, e.db, `SELECT COUNT(*) FROM month_close_events WHERE kind = 'release_request'`); n != 1 {
		t.Fatalf("요청 사건 %d건(기대 1)", n)
	}
}

// 검증자 B2 파급: 요청 당시와 다른 마감 버전이면(그 사이 해제·재마감) 오래된 요청서의 승인 전체가 취소되고 재마감된 달은 닫혀 있다.
func TestMonthReleaseStaleRequestCancelled(t *testing.T) {
	e := newEnv(t)
	e.closeFixture(t)
	e.mcrForm(t)
	for _, m := range []string{"2026-10", "2026-11"} {
		if _, err := e.monthClose.Close(as(uFinance), m); err != nil {
			t.Fatal(err)
		}
	}
	doc, err := e.monthClose.RequestRelease(as(uFinance), "2026-11", fixReason)
	if err != nil {
		t.Fatal(err)
	}
	// 요청서가 결재 중인 동안 달이 열렸다가 다시 마감된 것을 DB 수준 전이로 만든다(경합으로 오래된 요청서가 남은 상황).
	if _, err := e.db.Exec(`UPDATE month_closes SET status='open', released_at='2026-12-02T00:00:00Z' WHERE month='2026-11'`); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.Exec(`UPDATE month_closes SET status='closed', version=2, closed_at='2026-12-02T01:00:00Z', released_at=NULL WHERE month='2026-11'`); err != nil {
		t.Fatal(err)
	}
	_, err = e.approval.Decide(as(uFinance2), doc, domain.Decision{Approve: true})
	if err == nil || !strings.Contains(err.Error(), "마감이 다시 이루어졌습니다(요청 당시 1차, 현재 2차)") {
		t.Fatalf("오래된 요청서의 승인이 취소되어야 함: %v", err)
	}
	if n := count(t, e.db, `SELECT COUNT(*) FROM month_closes WHERE month='2026-11' AND status='closed' AND version=2`); n != 1 {
		t.Fatal("재마감된 달이 다시 열림")
	}
}

// 검증자 E: 해제 요청서 영향 요약에 스냅샷 품목 수가 들어가고 금액은 들어가지 않는다.
func TestMonthReleaseSummaryHasItemCount(t *testing.T) {
	e := newEnv(t)
	e.closeFixture(t)
	e.mcrForm(t)
	for _, m := range []string{"2026-10", "2026-11"} {
		if _, err := e.monthClose.Close(as(uFinance), m); err != nil {
			t.Fatal(err)
		}
	}
	doc, err := e.monthClose.RequestRelease(as(uFinance), "2026-11", fixReason)
	if err != nil {
		t.Fatal(err)
	}
	var summary string
	if err := e.db.QueryRow(`SELECT `+jsonField("fields", "summary")+` FROM purchase_requests WHERE id = ?`, doc).Scan(&summary); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(summary, "품목 1개·1줄") || strings.Contains(summary, "원") {
		t.Fatalf("영향 요약: %q", summary)
	}
}

// 검증자 D·F: 마감된 달 날짜의 이동 전표와 실사 수량 저장도 같은 문구로 거부한다.
func TestClosedMonthRejectsTransferAndCountSave(t *testing.T) {
	e := newEnv(t)
	it := e.closeFixture(t)
	id, err := dbutil.InsertID(context.Background(), e.db, `INSERT INTO stock_counts (no, warehouse_id, base_date, status, created_by, created_at, updated_at, snapshot_ledger_id) VALUES ('X-9',1,'2026-10-31','draft',1,'2026-12-02T00:00:00Z','2026-12-02T00:00:00Z',0)`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.monthClose.Close(as(uFinance), "2026-10"); err != nil {
		t.Fatal(err)
	}
	_, err = e.stock().tr.Transfer(as(uFinance), domain.TransferInput{FromID: 1, ToID: 2, MoveDate: "2026-10-20", Lines: []domain.TransferLineInput{{ItemID: it.ID, Qty: 1}}})
	if err == nil || err.Error() != closedMsg {
		t.Fatalf("마감월 이동: %v", err)
	}
	cs, _ := e.counts()
	q := int64(3)
	if _, err := cs.Save(as(uFinance), id, domain.CountSaveInput{Lines: []domain.CountLineInput{{ItemID: it.ID, CountedQty: &q}}}); err == nil || err.Error() != closedMsg {
		t.Fatalf("마감월 실사 저장: %v", err)
	}
}

// 검증자 C: 소급 출고의 감사 diff 에 다시 계산한 평가 줄 수가 남는다.
func TestBackdatedIssueAuditHasRevaluedRows(t *testing.T) {
	e := newEnv(t)
	it := e.closeFixture(t)
	e.receiveAt(t, at(2026, 12, 4, 1), it, 3000, 10)
	e.now = at(2026, 12, 5, 1)
	if _, err := e.issueOn(t, it, 1, "2026-12-02"); err != nil {
		t.Fatalf("소급 출고: %v", err)
	}
	found := ""
	for _, r := range e.rec.Records() {
		if r.Action == "stock_issue.create" {
			for _, d := range r.Diff {
				if d.Key == "revalued_rows" {
					found = d.New
				}
			}
		}
	}
	if found != "1" {
		t.Fatalf("revalued_rows=%q(기대 1)", found)
	}
}
