package service_test

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
)

const closedMsg = "마감된 월입니다. 재무팀에 마감 해제를 요청하세요."

// closeFixture 는 10월과 11월에 입고·출고가 있는 품목 하나를 만든다(10월: 입고 10×1000, 출고 4 / 11월: 입고 10×2000). 시계는 12월 2일이다.
func (e *env) closeFixture(t *testing.T) domain.Item {
	t.Helper()
	it := e.item(t, "CL-1", 0, false)
	e.receiveAt(t, at(2026, 10, 5, 1), it, 1000, 10)
	e.now = at(2026, 10, 20, 1)
	if _, err := e.issueOn(t, it, 4, ""); err != nil {
		t.Fatal(err)
	}
	e.receiveAt(t, at(2026, 11, 5, 1), it, 2000, 10)
	e.now = at(2026, 12, 2, 1)
	return it
}

// AC-914: 이번 달·앞선 달 미마감·권한·이미 마감을 규칙대로 거부하고, 마감은 감사에 남는다.
func TestMonthCloseOrderPermissionsAndAudit(t *testing.T) {
	e := newEnv(t)
	e.closeFixture(t)
	for _, who := range []int64{uStaff, uLead, uAdmin, uAuditor} {
		if _, err := e.monthClose.Close(as(who), "2026-10"); !isErr(err, contract.ErrForbidden) {
			t.Fatalf("%d 는 마감할 수 없어야 함: %v", who, err)
		}
	}
	if _, err := e.monthClose.Close(as(uFinance), "2026-12"); !isErr(err, contract.ErrValidation) || !strings.Contains(err.Error(), "이번 달이 끝난 뒤에 마감할 수 있습니다.") {
		t.Fatalf("이번 달: %v", err)
	}
	if _, err := e.monthClose.Close(as(uFinance), "2026-11"); !isErr(err, contract.ErrValidation) || !strings.Contains(err.Error(), "앞선 달(2026년 10월)이 아직 마감되지 않았습니다. 순서대로 마감하세요.") {
		t.Fatalf("순서: %v", err)
	}
	if _, err := e.monthClose.Close(as(uFinance), "2026-9x"); !isErr(err, contract.ErrValidation) {
		t.Fatalf("형식: %v", err)
	}
	row, err := e.monthClose.Close(as(uFinance), "2026-10")
	if err != nil || row.State != domain.MonthClosed || row.Version != 1 || row.CloseAmount == nil || *row.CloseAmount != 6000 {
		t.Fatalf("10월 마감: %+v %v", row, err)
	}
	if _, err := e.monthClose.Close(as(uFinance), "2026-10"); !isErr(err, contract.ErrInvalidState) {
		t.Fatalf("이미 마감: %v", err)
	}
	if _, err := e.monthClose.Close(as(uFinance), "2026-11"); err != nil {
		t.Fatalf("11월 마감: %v", err)
	}
	rows, err := e.monthClose.List(as(uAuditor))
	if err != nil || len(rows) != 2 || rows[0].Month != "2026-11" || rows[0].State != domain.MonthClosed || !rows[0].Releasable || rows[1].Releasable || rows[0].Closable {
		t.Fatalf("목록: %+v %v", rows, err)
	}
	if rows[0].CloseAmount == nil {
		t.Fatal("감사는 금액을 본다")
	}
	staffRows, _ := e.monthClose.List(as(uStaff))
	if staffRows[0].CloseAmount != nil {
		t.Fatal("직원에게 기말 금액이 나감")
	}
	n := 0
	for _, r := range e.rec.Records() {
		if r.Action == "month_close.close" && r.Result == audit.ResultSuccess && r.TargetType == audit.TargetMonthClose {
			n++
			okVersion := false
			for _, d := range r.Diff {
				if d.Key == "version" && d.New == "1" {
					okVersion = true
				}
			}
			if !okVersion {
				t.Fatalf("감사 diff: %+v", r.Diff)
			}
		}
	}
	if n != 2 {
		t.Fatalf("감사 %d건", n)
	}
}

// AC-920: 스냅샷은 기초·입고·출고·기말 수량과 금액을 저장하고 기초는 전월 기말이며, 마감 뒤 원장이 늘어도 변하지 않는다.
func TestMonthCloseSnapshot(t *testing.T) {
	e := newEnv(t)
	it := e.closeFixture(t)
	for _, m := range []string{"2026-10", "2026-11"} {
		if _, err := e.monthClose.Close(as(uFinance), m); err != nil {
			t.Fatal(err)
		}
	}
	type snap struct{ openQ, openA, inQ, inA, outQ, outA, closeQ, closeA, avg int64 }
	get := func(month string) snap {
		var s snap
		if err := e.db.QueryRow(`SELECT open_qty, open_amount, in_qty, in_amount, out_qty, out_amount, close_qty, close_amount, avg_cost FROM month_close_lines WHERE month = ? AND item_id = ?`, month, it.ID).
			Scan(&s.openQ, &s.openA, &s.inQ, &s.inA, &s.outQ, &s.outA, &s.closeQ, &s.closeA, &s.avg); err != nil {
			t.Fatal(err)
		}
		return s
	}
	oct, nov := get("2026-10"), get("2026-11")
	if oct != (snap{0, 0, 10, 10000, 4, 4000, 6, 6000, 1000}) {
		t.Fatalf("10월: %+v", oct)
	}
	// 11월: 기초는 10월 기말(6개 6,000원), 입고 10개 20,000원, 평균 (6,000+20,000)/16 = 1,625, 기말 16개 26,000원.
	if nov != (snap{6, 6000, 10, 20000, 0, 0, 16, 26000, 1625}) {
		t.Fatalf("11월: %+v", nov)
	}
	// 마감 뒤(12월) 원장이 늘어도 스냅샷은 그대로이고, 추가 전용이다.
	e.receiveAt(t, at(2026, 12, 3, 1), it, 3000, 5)
	if get("2026-11") != nov {
		t.Fatal("마감 뒤 스냅샷이 바뀜")
	}
	if _, err := e.db.Exec(`UPDATE month_close_lines SET close_qty = 1`); err == nil {
		t.Fatal("스냅샷 수정이 허용됨")
	}
	if _, err := e.db.Exec(`DELETE FROM month_close_lines`); err == nil {
		t.Fatal("스냅샷 삭제가 허용됨")
	}
	if _, err := e.db.Exec(`DELETE FROM month_close_events`); err == nil {
		t.Fatal("마감 사건 삭제가 허용됨")
	}
}

// AC-912·913: 마감된 달 날짜의 출고 전표는 서버가 거부하고(감사 denied), SQL 로 직접 넣어도 트리거가 같은 문구로 중단한다. 열린 달은 영향이 없다.
func TestMonthCloseLocksWrites(t *testing.T) {
	e := newEnv(t)
	it := e.closeFixture(t)
	if _, err := e.monthClose.Close(as(uFinance), "2026-10"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.issueOn(t, it, 1, "2026-10-25"); err == nil || err.Error() != closedMsg || !isErr(err, contract.ErrValidation) {
		t.Fatalf("마감 월 출고: %v", err)
	}
	if n := count(t, e.db, `SELECT COUNT(*) FROM issue_slips WHERE issue_date LIKE '2026-10%'`); n != 1 {
		t.Fatalf("거부된 출고 전표가 남음: %d", n)
	}
	denied := false
	for _, r := range e.rec.Records() {
		if r.Action == "stock_issue.create" && r.Result == audit.ResultDenied && strings.Contains(r.Detail, "마감된 월 2026-10") {
			denied = true
		}
	}
	if !denied {
		t.Fatal("거부 감사가 없음")
	}
	// 같은 달이 열려 있는 11월 날짜는 된다.
	if _, err := e.issueOn(t, it, 1, "2026-11-20"); err != nil {
		t.Fatalf("열린 달 출고: %v", err)
	}
	// 서버 경로를 우회해도 트리거가 막는다.
	for name, q := range map[string]string{
		"원장(장부일)": `INSERT INTO stock_ledger (item_id, qty_delta, reason, actor_id, warehouse_id, created_at, book_date) VALUES (?,1,'receipt',1,1,'2026-12-02T00:00:00Z','2026-10-30')`,
		"원장(기록일)": `INSERT INTO stock_ledger (item_id, qty_delta, reason, actor_id, warehouse_id, created_at) VALUES (?,1,'receipt',1,1,'2026-10-30T00:00:00Z')`,
		"출고 전표":   `INSERT INTO issue_slips (no, department_id, warehouse_id, issue_date, method, actor_id, created_at) VALUES ('X-1',1,1,'2026-10-31','fifo',1,'2026-12-02T00:00:00Z')`,
		"실사 표":    `INSERT INTO stock_counts (no, warehouse_id, base_date, status, created_by, created_at, updated_at, snapshot_ledger_id) VALUES ('X-2',1,'2026-10-31','draft',1,'2026-12-02T00:00:00Z','2026-12-02T00:00:00Z',0)`,
	} {
		if _, err := e.db.Exec(q, argsIf(q, it.ID)...); err == nil || !strings.Contains(err.Error(), closedMsg) {
			t.Fatalf("%s: 트리거가 막지 못함: %v", name, err)
		}
	}
	// 마감된 달의 평가 줄은 다시 계산되지 않는다.
	if _, err := e.db.Exec(`UPDATE stock_valuation SET amount = amount WHERE book_date LIKE '2026-10%'`); err == nil || !strings.Contains(err.Error(), closedMsg) {
		t.Fatalf("마감 월 평가 줄 수정: %v", err)
	}
}

// 마감 전 점검: 결재 진행 중 조정 전표(상신된 실사)는 차단, 작성 중 실사·미입고 발주는 경고다. 차단이 있으면 마감이 거부된다(AC-911).
func TestMonthClosePrecheck(t *testing.T) {
	e := newEnv(t)
	it := e.item(t, "CL-2", 0, false)
	e.receiveAt(t, at(2026, 11, 3, 1), it, 1000, 10)
	v := e.vendor(t)
	e.now = at(2026, 11, 4, 1)
	e.orderFor(t, v, "2026-11-20", 1000, 1) // 납기를 넘겨도 입고하지 않은 발주(경고)
	mk := func(no, status string) {
		t.Helper()
		if _, err := e.db.Exec(`INSERT INTO stock_counts (no, warehouse_id, base_date, status, created_by, created_at, updated_at, snapshot_ledger_id) VALUES (?,1,'2026-11-10',?,1,'2026-11-10T00:00:00Z','2026-11-10T00:00:00Z',0)`, no, status); err != nil {
			t.Fatal(err)
		}
	}
	mk("SC-T-1", "approving")
	e.now = at(2026, 12, 2, 1)
	pc, err := e.monthClose.Precheck(as(uAuditor), "2026-11")
	if err != nil || pc.Closable || len(pc.Blockers) != 1 || len(pc.Warnings) != 1 || !strings.Contains(pc.Warnings[0].Detail, "입고가 끝나지 않은 발주가 1건") {
		t.Fatalf("점검: %+v %v", pc, err)
	}
	if !strings.Contains(pc.Reason, "결재 진행 중인 재고 조정 전표 1건이 있어 마감할 수 없습니다") {
		t.Fatalf("이유: %q", pc.Reason)
	}
	if _, err := e.monthClose.Close(as(uFinance), "2026-11"); !isErr(err, contract.ErrValidation) || !strings.Contains(err.Error(), "결재 진행 중인 재고 조정 전표 1건") {
		t.Fatalf("차단 중 마감: %v", err)
	}
	if n := count(t, e.db, `SELECT COUNT(*) FROM month_closes`); n != 0 {
		t.Fatal("거부된 마감이 저장됨")
	}
	// 반려로 정리하면 차단이 풀리고 작성 중·반려 실사는 경고로 남는다.
	if _, err := e.db.Exec(`UPDATE stock_counts SET status = 'rejected' WHERE no = 'SC-T-1'`); err != nil {
		t.Fatal(err)
	}
	pc, _ = e.monthClose.Precheck(as(uAuditor), "2026-11")
	if !pc.Closable || len(pc.Blockers) != 0 || len(pc.Warnings) != 2 {
		t.Fatalf("정리 뒤: %+v", pc)
	}
	st, err := e.monthClose.Status(as(uStaff), "2026-11")
	if err != nil || st.State != domain.MonthOpen || st.Blockers != 0 || st.Warnings != 2 || st.CloseAmount != nil {
		t.Fatalf("상태: %+v %v", st, err)
	}
	if _, err := e.monthClose.Close(as(uFinance), "2026-11"); err != nil {
		t.Fatalf("마감: %v", err)
	}
	st, _ = e.monthClose.Status(as(uAuditor), "2026-11")
	if st.State != domain.MonthClosed || st.Version != 1 || st.ClosedAt.IsZero() || st.CloseAmount == nil || *st.CloseAmount != 10000 {
		t.Fatalf("마감 뒤 상태: %+v", st)
	}
	if st, _ := e.monthClose.Status(as(uStaff), "2026-11"); st.CloseAmount != nil || st.State != domain.MonthClosed {
		t.Fatalf("직원 상태: %+v", st)
	}
	if _, err := e.monthClose.Status(as(uStaff), "202611"); !isErr(err, contract.ErrValidation) {
		t.Fatalf("형식 오류: %v", err)
	}
}

// AC-915: 마감과 출고가 동시에 와도 순서대로 판정된다. 출고가 먼저면 스냅샷에 포함되고, 마감이 먼저면 출고가 거부된다.
func TestMonthCloseRacesWithIssue(t *testing.T) {
	for i := 0; i < 6; i++ {
		e := newEnvAt(t, fmt.Sprintf("b27race%d.db", i))
		it := e.closeFixture(t) // 10월 기말 6개
		var wg sync.WaitGroup
		var issueErr, closeErr error
		wg.Add(2)
		go func() { defer wg.Done(); _, issueErr = e.issueOn(t, it, 1, "2026-10-28") }()
		go func() { defer wg.Done(); _, closeErr = e.monthClose.Close(as(uFinance), "2026-10") }()
		wg.Wait()
		if closeErr != nil {
			t.Fatalf("마감: %v", closeErr)
		}
		var closeQty int64
		if err := e.db.QueryRow(`SELECT close_qty FROM month_close_lines WHERE month='2026-10' AND item_id = ?`, it.ID).Scan(&closeQty); err != nil {
			t.Fatal(err)
		}
		switch {
		case issueErr == nil && closeQty != 5:
			t.Fatalf("출고가 먼저면 스냅샷 5개여야 함: %d", closeQty)
		case issueErr != nil && (issueErr.Error() != closedMsg || closeQty != 6):
			t.Fatalf("마감이 먼저면 출고 거부·스냅샷 6개: %v %d", issueErr, closeQty)
		}
	}
}

func TestMonthClosePendingReleaseNotReady(t *testing.T) {
	e := newEnv(t)
	e.closeFixture(t)
	if _, err := e.monthClose.RequestRelease(as(uStaff), "2026-10", "사유 열 글자 이상 입력"); !isErr(err, contract.ErrForbidden) {
		t.Fatalf("해제 요청 권한: %v", err)
	}
	_ = time.Now
}
