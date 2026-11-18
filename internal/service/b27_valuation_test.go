package service_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/contract/fake"
	"github.com/choibongseok/procure-ledger/internal/service"
)

// receiveAt 은 품목을 단가·수량으로 발주해 승인하고 그 시각(e.now)에 전량 입고한다.
func (e *env) receiveAt(t *testing.T, now time.Time, it domain.Item, price, qty int64) {
	t.Helper()
	e.now = now
	v := e.vendor(t)
	pr, err := e.purchase.CreateDraft(as(uStaff), domain.DraftInput{VendorID: v.ID, Title: "평가 시험", Reason: "x", Lines: []domain.PRLineInput{{ItemID: it.ID, Quantity: qty, UnitPrice: price}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.purchase.Submit(as(uStaff), pr.ID); err != nil {
		t.Fatal(err)
	}
	res := approve(t, e, uLead, pr.ID)
	if res.PurchaseOrder == nil {
		t.Fatal("발주 없음")
	}
	po := *res.PurchaseOrder
	if _, err := e.inventory.Receive(as(uFinance), recv(po.ID, po.Lines[0].ID, qty, "", "")); err != nil {
		t.Fatal(err)
	}
}

func (e *env) issueOn(t *testing.T, it domain.Item, qty int64, day string) (domain.IssueSlip, error) {
	t.Helper()
	return e.stock().is.Issue(as(uFinance), domain.IssueInput{DepartmentID: deptSales, WarehouseID: 1, Method: domain.IssueFIFO, IssueDate: day,
		Lines: []domain.IssueLineInput{{ItemID: it.ID, Qty: qty}}})
}

type valRow struct{ unit, amount, balQty, balAmt, avg int64 }

func (e *env) valRows(t *testing.T, itemID int64) []valRow {
	t.Helper()
	rows, err := e.db.Query(`SELECT unit_cost, amount, bal_qty, bal_amount, avg_cost FROM stock_valuation WHERE item_id = ? ORDER BY book_date, ledger_id`, itemID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []valRow
	for rows.Next() {
		var r valRow
		if err := rows.Scan(&r.unit, &r.amount, &r.balQty, &r.balAmt, &r.avg); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

// 입고 단가가 평균에 반영되고 출고는 그 시점 평균 단가로 금액이 잡힌다. 원장 수량은 변하지 않는다(AC-900·901).
func TestValuationReceiptsAndIssue(t *testing.T) {
	e := newEnv(t)
	it := e.item(t, "VAL-1", 0, false)
	e.receiveAt(t, at(2026, 11, 1, 1), it, 1000, 10)
	e.receiveAt(t, at(2026, 11, 5, 1), it, 2000, 10)
	e.now = at(2026, 11, 10, 1)
	if _, err := e.issueOn(t, it, 5, ""); err != nil {
		t.Fatal(err)
	}
	got := e.valRows(t, it.ID)
	want := []valRow{{1000, 10000, 10, 10000, 1000}, {2000, 20000, 20, 30000, 1500}, {1500, -7500, 15, 22500, 1500}}
	if len(got) != 3 {
		t.Fatalf("평가 줄 %d건: %+v", len(got), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("줄 %d: %+v, 기대 %+v", i, got[i], want[i])
		}
	}
	// 수불부 금액 열은 재고 평가 조회 권한이 있는 역할에게만 나온다(서비스가 거른다).
	ledger := service.NewLedgerService(service.Base{DB: e.db, Txs: contract.SQLTxManager{DB: e.db}, Audit: e.rec, Masker: fake.Masker{}, Now: func() time.Time { return e.now }})
	pg, err := ledger.Ledger(as(uFinance), domain.LedgerFilter{ItemID: it.ID, From: "2026-11-01", To: "2026-11-30"})
	if err != nil || len(pg.Rows) != 3 || !pg.Rows[2].Valued || pg.Rows[2].Amount != -7500 || pg.Rows[2].UnitCost != 1500 || pg.Rows[0].Amount != 10000 {
		t.Fatalf("재무: %+v %v", pg.Rows, err)
	}
	for _, who := range []int64{uStaff, uLead} {
		pg, err := ledger.Ledger(as(who), domain.LedgerFilter{ItemID: it.ID, From: "2026-11-01", To: "2026-11-30"})
		if err != nil || len(pg.Rows) != 3 || pg.Rows[0].Valued || pg.Rows[0].Amount != 0 || pg.Rows[2].UnitCost != 0 || pg.Closing != 15 {
			t.Fatalf("%d 는 금액 없이 수량만: %+v %v", who, pg, err)
		}
	}
	for _, who := range []int64{uAdmin, uAuditor} {
		if pg, err := ledger.Ledger(as(who), domain.LedgerFilter{ItemID: it.ID, From: "2026-11-01", To: "2026-11-30"}); err != nil || !pg.Rows[0].Valued {
			t.Fatalf("%d 는 금액을 본다: %v", who, err)
		}
	}
	// 재고 화면의 평균 단가·재고 금액도 같은 권한이다.
	if st, err := e.inventory.GetStock(as(uFinance), it.ID); err != nil || !st.HasValuation || st.AvgCost != 1500 || st.StockAmount != 22500 || st.OnHand != 15 {
		t.Fatalf("재무 재고: %+v %v", st, err)
	}
	if st, err := e.inventory.GetStock(as(uStaff), it.ID); err != nil || st.HasValuation || st.AvgCost != 0 || st.OnHand != 15 {
		t.Fatalf("직원 재고: %+v %v", st, err)
	}
}

// 소급 출고(열린 과거 달의 출고일): 그 날짜 기준 평균으로 평가하고 뒤쪽 평가 줄을 다시 계산한다. 그 날짜 기준 재고가 부족하면 거부한다(AC-904).
func TestValuationBackdatedIssue(t *testing.T) {
	e := newEnv(t)
	it := e.item(t, "VAL-2", 0, false)
	e.receiveAt(t, at(2026, 11, 1, 1), it, 1000, 10)
	e.receiveAt(t, at(2026, 11, 5, 1), it, 2000, 10)
	e.now = at(2026, 11, 10, 1)
	// 11/03 기준 재고는 10개뿐이라 12개 출고는 현재 재고(20개)가 충분해도 거부한다.
	if _, err := e.issueOn(t, it, 12, "2026-11-03"); err == nil || !isErr(err, contract.ErrValidation) || !strings.Contains(err.Error(), "2026-11-03 기준 재고가 부족합니다") {
		t.Fatalf("소급 부족: %v", err)
	}
	if n := count(t, e.db, `SELECT COUNT(*) FROM issue_slips`); n != 0 {
		t.Fatalf("거부된 출고가 남음: %d", n)
	}
	if _, err := e.issueOn(t, it, 5, "2026-11-03"); err != nil {
		t.Fatal(err)
	}
	got := e.valRows(t, it.ID) // 장부일 순: 11/01 입고, 11/03 출고, 11/05 입고
	want := []valRow{{1000, 10000, 10, 10000, 1000}, {1000, -5000, 5, 5000, 1000}, {2000, 20000, 25, 25000, 1000}}
	_ = want
	if len(got) != 3 || got[1].amount != -5000 || got[1].balQty != 5 || got[1].balAmt != 5000 {
		t.Fatalf("소급 줄: %+v", got)
	}
	// 11/05 입고 줄은 소급 출고 뒤 잔액 위에서 다시 계산된다: 5개 5,000원 + 10개 20,000원 = 15개 25,000원(평균 1,667).
	if got[2].balQty != 15 || got[2].balAmt != 25000 || got[2].avg != 1667 {
		t.Fatalf("뒤쪽 줄 재계산: %+v", got[2])
	}
	// 원장 수량은 그대로 20 - 5 = 15 다.
	if st, _ := e.inventory.GetStock(as(uFinance), it.ID); st.OnHand != 15 {
		t.Fatalf("재고 %d", st.OnHand)
	}
}

// 평가 줄이 없는 기존 원장 행은 기동 때 한 번 재생된다: 입고 단가는 발주 줄 단가, 멱등, 원장은 그대로, 건수가 감사에 남는다(AC-903).
func TestValuationBackfillIsIdempotent(t *testing.T) {
	e := newEnv(t)
	it := e.item(t, "VAL-3", 0, false)
	e.receiveAt(t, at(2026, 11, 1, 1), it, 1000, 10)
	e.receiveAt(t, at(2026, 11, 5, 1), it, 3000, 10)
	e.now = at(2026, 11, 10, 1)
	if _, err := e.issueOn(t, it, 4, ""); err != nil {
		t.Fatal(err)
	}
	before := e.valRows(t, it.ID)
	ledgerBefore := count(t, e.db, `SELECT COUNT(*) FROM stock_ledger`)
	if _, err := e.db.Exec(`DELETE FROM stock_valuation`); err != nil { // 평가 도입 전 DB 를 흉내 낸다
		t.Fatal(err)
	}
	b := service.Base{DB: e.db, Txs: contract.SQLTxManager{DB: e.db}, Audit: e.rec, Now: func() time.Time { return e.now }}
	if err := service.BackfillValuation(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	after := e.valRows(t, it.ID)
	if len(after) != len(before) {
		t.Fatalf("재생 %d줄, 기대 %d", len(after), len(before))
	}
	for i := range before {
		if after[i] != before[i] {
			t.Errorf("줄 %d: 재생 %+v, 원래 %+v", i, after[i], before[i])
		}
	}
	runs := func() int {
		n := 0
		for _, r := range e.rec.Records() {
			if r.Action == "valuation.backfill" && r.Result == audit.ResultSuccess && r.TargetType == audit.TargetMonthClose && r.TargetID == "all" { // 명세 7절: 대상 종류 month_close, 번호 all
				n++
			}
		}
		return n
	}
	if runs() != 1 {
		t.Fatalf("재생 감사 %d건", runs())
	}
	if err := service.BackfillValuation(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	if runs() != 1 || count(t, e.db, `SELECT COUNT(*) FROM stock_ledger`) != ledgerBefore {
		t.Fatalf("두 번째 실행은 아무것도 하지 않아야 함(감사 %d)", runs())
	}
}

// 이동은 평균과 잔액을 바꾸지 않고, 실사 조정(−)은 평균 단가로 금액이 잡힌다. 평가 줄이 없는 새 원장 행은 만들어지지 않는다(AC-902).
func TestValuationTransferAndAdjustKeepRules(t *testing.T) {
	e := newEnv(t)
	it := e.item(t, "VAL-4", 0, false)
	e.receiveAt(t, at(2026, 11, 1, 1), it, 1000, 10)
	e.now = at(2026, 11, 2, 1)
	s := e.stock()
	w2, err := s.wh.Save(as(uFinance), domain.WarehouseInput{Code: "W-2", Name: "둘째 창고"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.tr.Transfer(as(uFinance), domain.TransferInput{FromID: 1, ToID: w2.ID, MoveDate: "2026-11-02", Lines: []domain.TransferLineInput{{ItemID: it.ID, Qty: 4}}}); err != nil {
		t.Fatal(err)
	}
	got := e.valRows(t, it.ID)
	if len(got) != 3 || got[1].amount != -4000 || got[2].amount != 4000 || got[2].balQty != 10 || got[2].balAmt != 10000 || got[2].avg != 1000 {
		t.Fatalf("이동: %+v", got)
	}
	if n := count(t, e.db, `SELECT COUNT(*) FROM stock_ledger l LEFT JOIN stock_valuation v ON v.ledger_id = l.id WHERE v.ledger_id IS NULL`); n != 0 {
		t.Fatalf("평가 줄이 없는 원장 행 %d건", n)
	}
}
