package repository_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/choibongseok/procure-ledger/internal/backup"
	"github.com/choibongseok/procure-ledger/internal/dbtest"
	"github.com/choibongseok/procure-ledger/internal/dbutil"
	"github.com/choibongseok/procure-ledger/internal/repository"
	"github.com/choibongseok/procure-ledger/migrations"
)

// 법인×품목 평가 엔진과 이관 시험(B51 2단계, AC-5101~5104·5121~5124). 법인 둘: 기본 법인(창고 1·2)과 ABC(창고 10).

func entitySeed(t *testing.T) (context.Context, *sql.DB) {
	t.Helper()
	ctx := context.Background()
	db := dbtest.Open(t)
	if err := migrations.Apply(ctx, db); err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{
		`INSERT INTO departments (id, name) VALUES (1,'구매팀')`,
		`INSERT INTO users (id, email, name, role, department_id) VALUES (1,'fin@x','재무','finance',1)`,
		`INSERT INTO items (id, sku, name) VALUES (1,'A','복사용지'),(2,'B','토너')`,
		`INSERT INTO entities (id, code, name, created_at, updated_at) VALUES (2,'ABC','에이비씨','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`,
		`INSERT INTO warehouses (id, code, name, created_at, updated_at) VALUES (2,'WH-02','지점 창고','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`,
		`INSERT INTO warehouses (id, code, name, created_at, updated_at, entity_id) VALUES (10,'ABC-01','에이비씨 창고','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z',2)`,
	} {
		if _, err := db.ExecContext(ctx, s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
	return ctx, db
}

func appendOp(t *testing.T, ctx context.Context, db *sql.DB, wh, item, qty int64, reason, ref string, refID int64, day string, cost int64) error {
	t.Helper()
	at := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	return repository.SQLInventory{}.AppendLedger(ctx, db, repository.LedgerEntry{WarehouseID: wh, ItemID: item, Qty: qty, Reason: reason, RefType: ref, RefID: refID, ActorID: 1, At: at, BookDate: day, UnitCost: cost})
}

// lastVal 은 (법인, 품목)의 마지막 평가 줄의 평균 단가·잔량·잔액이다.
func lastVal(t *testing.T, ctx context.Context, db *sql.DB, entity, item int64) (avg, qty, amt int64) {
	t.Helper()
	err := db.QueryRowContext(ctx, `SELECT avg_cost, bal_qty, bal_amount FROM stock_valuation WHERE entity_id = ? AND item_id = ? ORDER BY book_date DESC, ledger_id DESC LIMIT 1`, entity, item).Scan(&avg, &qty, &amt)
	if err != nil {
		t.Fatalf("법인 %d 품목 %d 마지막 줄: %v", entity, item, err)
	}
	return
}

func entityRowsDigest(t *testing.T, ctx context.Context, db *sql.DB, entity int64) string {
	t.Helper()
	rows, err := db.QueryContext(ctx, `SELECT ledger_id, item_id, book_date, unit_cost, amount, bal_qty, bal_amount, avg_cost, warned, entity_basis FROM stock_valuation WHERE entity_id = ? ORDER BY ledger_id`, entity)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var b strings.Builder
	for rows.Next() {
		var a, i, u, am, bq, ba, av, w, bs int64
		var d string
		if err := rows.Scan(&a, &i, &d, &u, &am, &bq, &ba, &av, &w, &bs); err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&b, "%d,%d,%s,%d,%d,%d,%d,%d,%d,%d;", a, i, d, u, am, bq, ba, av, w, bs)
	}
	return b.String()
}

func TestValuationIsKeyedByEntityAndItem(t *testing.T) {
	ctx, db := entitySeed(t)
	// 같은 품목을 법인마다 다른 가격에 입고한다(AC-5101).
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(appendOp(t, ctx, db, 1, 1, 10, "receipt", "receipt", 1, "2026-01-05", 100))
	must(appendOp(t, ctx, db, 10, 1, 10, "receipt", "receipt", 2, "2026-01-06", 200))
	must(appendOp(t, ctx, db, 1, 1, -5, "issue", "issue", 1, "2026-01-07", 0))
	must(appendOp(t, ctx, db, 10, 1, -5, "issue", "issue", 2, "2026-01-07", 0))
	if avg, qty, amt := lastVal(t, ctx, db, 1, 1); avg != 100 || qty != 5 || amt != 500 {
		t.Fatalf("기본 법인: 평균 %d 잔량 %d 잔액 %d", avg, qty, amt)
	}
	if avg, qty, amt := lastVal(t, ctx, db, 2, 1); avg != 200 || qty != 5 || amt != 1000 {
		t.Fatalf("ABC: 평균 %d 잔량 %d 잔액 %d", avg, qty, amt)
	}

	// 한 법인의 소급 입고·소급 출고가 다른 법인 평가 줄을 하나도 바꾸지 않는다(AC-5102).
	before := entityRowsDigest(t, ctx, db, 2)
	must(appendOp(t, ctx, db, 1, 1, 4, "receipt", "receipt", 3, "2026-01-01", 50))
	must(appendOp(t, ctx, db, 1, 1, -2, "issue", "issue", 3, "2026-01-02", 0))
	if after := entityRowsDigest(t, ctx, db, 2); after != before {
		t.Fatalf("다른 법인 평가 줄이 바뀌었습니다\n전 %s\n후 %s", before, after)
	}
	if _, qty, _ := lastVal(t, ctx, db, 1, 1); qty != 7 {
		t.Fatalf("기본 법인 잔량 %d, 7 이어야 합니다", qty)
	}

	// 같은 법인 안 창고 간 이동은 법인 평균·잔액을 바꾸지 않는다(AC-5103).
	_, _, amtBefore := lastVal(t, ctx, db, 1, 1)
	must(appendOp(t, ctx, db, 1, 1, -3, "transfer_out", "transfer", 9, "2026-02-01", 0))
	must(appendOp(t, ctx, db, 2, 1, 3, "transfer_in", "transfer", 9, "2026-02-01", 0))
	if _, qty, amt := lastVal(t, ctx, db, 1, 1); qty != 7 || amt < amtBefore-1 || amt > amtBefore+1 { // 이동 입고가 평균 환산이라 반올림 차이 1원까지는 현행 규칙
		t.Fatalf("같은 법인 이동 뒤 잔량 %d 잔액 %d(전 %d)", qty, amt, amtBefore)
	}

	// 수량 부족은 그 법인 수량으로 판정한다: ABC 에는 5 가 남아 있어도 기본 법인에 없는 품목 2 는 거부(AC-5104).
	must(appendOp(t, ctx, db, 10, 2, 50, "receipt", "receipt", 4, "2026-01-05", 10))
	err := appendOp(t, ctx, db, 1, 2, -1, "issue", "issue", 4, "2026-01-10", 0)
	var short *repository.ErrBackdatedShortage
	if !errors.As(err, &short) {
		t.Fatalf("다른 법인에 수량이 있어도 부족으로 거부해야 합니다: %v", err)
	}
}

// legacyize 는 0104 직후의 다법인 품목 모양을 만든다: 모든 줄이 품목 전체 기준 값(entity_basis = 0)이고 entity_id 만 창고 법인이다.
func legacyize(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	if _, err := db.ExecContext(ctx, `UPDATE stock_valuation SET entity_basis = 0`); err != nil {
		t.Fatal(err)
	}
}

func itemTotals(t *testing.T, ctx context.Context, db *sql.DB, item int64, upTo string) (qty, amt int64) {
	t.Helper()
	// 품목 전체 기준 값: 옛 줄의 amount 합과 원장 수량 합.
	if err := db.QueryRowContext(ctx, `SELECT COALESCE(SUM(l.qty_delta),0), COALESCE(SUM(v.amount),0) FROM stock_valuation v JOIN stock_ledger l ON l.id = v.ledger_id WHERE v.item_id = ? AND v.book_date <= ?`, item, upTo).Scan(&qty, &amt); err != nil {
		t.Fatal(err)
	}
	return
}

func TestSplitValuationByEntityKeepsClosedMonthsAndSums(t *testing.T) {
	ctx, db := entitySeed(t)
	// 옛 방식으로 만든 다법인 품목: 두 법인의 입고가 품목 하나의 평균으로 섞여 있었다고 가정한다(모든 줄은 기본 법인 키로 평가한 뒤 법인만 바꾼다).
	for _, op := range []struct {
		wh, qty int64
		reason  string
		id      int64
		day     string
		cost    int64
	}{
		{1, 10, "receipt", 1, "2026-01-05", 100},
		{2, 10, "receipt", 2, "2026-01-06", 300}, // 창고 2 를 나중에 ABC 로 옮긴 것처럼 시험한다
		{1, -4, "issue", 1, "2026-01-20", 0},
		{2, -2, "issue", 2, "2026-01-21", 0},
		{1, 6, "receipt", 3, "2026-02-03", 120},
		{2, -3, "issue", 3, "2026-02-04", 0},
		{1, -2, "issue", 4, "2026-02-10", 0},
	} {
		ref := "receipt"
		if op.reason == "issue" {
			ref = "issue"
		}
		if err := appendOp(t, ctx, db, op.wh, 1, op.qty, op.reason, ref, op.id, op.day, op.cost); err != nil {
			t.Fatal(err)
		}
	}
	// 창고 2 의 줄을 ABC 법인으로 옮기고(원장은 창고로 법인을 안다) 모든 줄을 품목 전체 기준으로 표시한다.
	// 창고의 법인은 만든 뒤 바꿀 수 없다(0105 트리거, D15). 이 시험은 0104 직후의 옛 모양을 만들어야 하므로 이 시험 안에서만 트리거를 끈다.
	allowWarehouseEntityMove(t, ctx, db)
	if _, err := db.ExecContext(ctx, `UPDATE warehouses SET entity_id = 2 WHERE id = 2`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE stock_valuation SET entity_id = 2 WHERE ledger_id IN (SELECT id FROM stock_ledger WHERE warehouse_id = 2)`); err != nil {
		t.Fatal(err)
	}
	legacyize(t, ctx, db)
	// 두 법인 모두 2026-01 이 닫혔다.
	for _, e := range []int{1, 2} {
		if _, err := db.ExecContext(ctx, `INSERT INTO month_closes (entity_id, month, status, version, closed_by, closed_at) VALUES (?, '2026-01', 'closed', 1, 1, '2026-02-01T00:00:00Z')`, e); err != nil {
			t.Fatal(err)
		}
	}
	oldQty, oldAmt := itemTotals(t, ctx, db, 1, "2026-01-31")
	closedBefore := map[int64]string{1: entityRowsDigestClosed(t, ctx, db, 1), 2: entityRowsDigestClosed(t, ctx, db, 2)}

	run := func() int {
		groups, err := repository.ValuationSplitGroups(ctx, db)
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		var sumQty, sumAmt int64
		for _, g := range groups {
			res, err := repository.SplitValuationGroup(ctx, db, g)
			if err != nil {
				t.Fatalf("법인 %d 품목 %d: %v", g.EntityID, g.ItemID, err)
			}
			n += res.Recomputed
			sumQty += res.StartQty
			sumAmt += res.StartAmt
		}
		if n > 0 && (sumQty != oldQty || sumAmt != oldAmt) {
			t.Fatalf("법인별 시작 상태의 합(%d, %d)이 옛 품목 잔량·잔액(%d, %d)과 다릅니다", sumQty, sumAmt, oldQty, oldAmt)
		}
		return n
	}
	if n := run(); n != 3 { // 열린 2월 줄: 기본 법인 2(입고·출고) + ABC 1(출고)
		t.Fatalf("다시 계산한 줄 %d, 3 이어야 합니다", n)
	}
	for e, want := range closedBefore {
		if got := entityRowsDigestClosed(t, ctx, db, e); got != want {
			t.Fatalf("법인 %d 닫힌 달 줄이 바뀌었습니다\n전 %s\n후 %s", e, want, got)
		}
	}
	// 열린 달 줄은 법인 기준이고 잔량이 그 법인 창고 원장 합과 같다. 시작 상태는 기본 법인 6개·200원, ABC 8개·2600원이다.
	if _, qty, amt := lastVal(t, ctx, db, 1, 1); qty != 10 || amt != 767 {
		t.Fatalf("기본 법인 잔량 %d 잔액 %d(10, 767 이어야 합니다)", qty, amt)
	}
	if _, qty, amt := lastVal(t, ctx, db, 2, 1); qty != 5 || amt != 1625 {
		t.Fatalf("ABC 잔량 %d 잔액 %d(5, 1625 여야 합니다)", qty, amt)
	}
	var basis0 int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM stock_valuation WHERE entity_basis = 0 AND substr(book_date,1,7) = '2026-02'`).Scan(&basis0); err != nil || basis0 != 0 {
		t.Fatalf("열린 달에 품목 전체 기준 줄이 남았습니다: %d %v", basis0, err)
	}
	if v := backup.CheckInvariants(ctx, db); len(v) > 0 {
		t.Fatalf("불변식 위반: %v", v)
	}
	// 멱등: 한 번 더 돌려도 줄이 같다.
	d1, d2 := entityRowsDigest(t, ctx, db, 1), entityRowsDigest(t, ctx, db, 2)
	if n := run(); n != 0 {
		t.Fatalf("재실행이 %d 줄을 다시 썼습니다", n)
	}
	if entityRowsDigest(t, ctx, db, 1) != d1 || entityRowsDigest(t, ctx, db, 2) != d2 {
		t.Fatal("재실행으로 값이 바뀌었습니다")
	}
}

func entityRowsDigestClosed(t *testing.T, ctx context.Context, db *sql.DB, entity int64) string {
	t.Helper()
	rows, err := db.QueryContext(ctx, `SELECT ledger_id, unit_cost, amount, bal_qty, bal_amount, avg_cost, entity_basis FROM stock_valuation WHERE entity_id = ? AND substr(book_date,1,7) = '2026-01' ORDER BY ledger_id`, entity)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var b strings.Builder
	for rows.Next() {
		var a, u, am, bq, ba, av, bs int64
		if err := rows.Scan(&a, &u, &am, &bq, &ba, &av, &bs); err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&b, "%d,%d,%d,%d,%d,%d,%d;", a, u, am, bq, ba, av, bs)
	}
	return b.String()
}

// 닫힌 달 줄만 있는 법인(entity_basis = 0)에 새 입고가 들어오면 줄 합으로 시작 상태를 만들어 이어 쓴다(대체 계산 경로, 명세 5.2·12절).
func TestStateBeforeFallsBackToEntitySumsForLegacyRows(t *testing.T) {
	ctx, db := entitySeed(t)
	if err := appendOp(t, ctx, db, 1, 1, 10, "receipt", "receipt", 1, "2026-01-05", 100); err != nil {
		t.Fatal(err)
	}
	if err := appendOp(t, ctx, db, 2, 1, 10, "receipt", "receipt", 2, "2026-01-06", 300); err != nil {
		t.Fatal(err)
	}
	allowWarehouseEntityMove(t, ctx, db)
	if _, err := db.ExecContext(ctx, `UPDATE warehouses SET entity_id = 2 WHERE id = 2`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE stock_valuation SET entity_id = 2 WHERE ledger_id IN (SELECT id FROM stock_ledger WHERE warehouse_id = 2)`); err != nil {
		t.Fatal(err)
	}
	legacyize(t, ctx, db)
	if _, err := db.ExecContext(ctx, `INSERT INTO month_closes (entity_id, month, status, version, closed_by, closed_at) VALUES (2, '2026-01', 'closed', 1, 1, '2026-02-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	// ABC 는 닫힌 줄뿐이다. 2월 입고는 줄 합(10 개, 3000 원)에서 이어진다.
	if err := appendOp(t, ctx, db, 2, 1, 10, "receipt", "receipt", 5, "2026-02-02", 500); err != nil {
		t.Fatal(err)
	}
	if avg, qty, amt := lastVal(t, ctx, db, 2, 1); qty != 20 || amt != 8000 || avg != 400 {
		t.Fatalf("ABC 평균 %d 잔량 %d 잔액 %d(400, 20, 8000 이어야 합니다)", avg, qty, amt)
	}
	var basis int
	if err := db.QueryRowContext(ctx, `SELECT entity_basis FROM stock_valuation WHERE entity_id = 2 AND book_date = '2026-02-02'`).Scan(&basis); err != nil || basis != 1 {
		t.Fatalf("새 줄은 법인 기준이어야 합니다: %d %v", basis, err)
	}
}

// allowWarehouseEntityMove 는 창고의 법인 변경 금지 트리거(0105, D15)를 이 시험 안에서만 끈다: 0104 직후의 옛 모양(창고를 나중에 다른 법인으로 옮긴 것처럼)을 만들어야 하는 시험이 쓴다.
func allowWarehouseEntityMove(t *testing.T, ctx context.Context, db interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}) {
	t.Helper()
	off := `DROP TRIGGER trg_warehouses_entity_immutable`
	if dbutil.Active().Kind() == dbutil.KindPostgres {
		off = `ALTER TABLE warehouses DISABLE TRIGGER trg_warehouses_entity_immutable`
	}
	if _, err := db.ExecContext(ctx, off); err != nil {
		t.Fatal(err)
	}
}
