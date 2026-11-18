package repository_test

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/choibongseok/procure-ledger/internal/dbtest"
	"github.com/choibongseok/procure-ledger/internal/repository"
	"github.com/choibongseok/procure-ledger/migrations"
)

// 법인 하나 설치에서 평가 값이 바뀌지 않음을 잡는 골든 시험이다(B51 명세 16절 위험 1, AC-5106·5120).
// 이 파일의 기대값(testdata/valuation_single_entity.golden)은 평가 엔진을 법인 키로 바꾸기 전(B51 1단계)의 값을 그대로 적은 것이다.
// 엔진·마이그레이션을 바꾼 뒤에도 같은 입력의 평가 줄·월말 스냅샷·품목 평가 읽기가 한 글자도 달라지지 않아야 한다.
// 값이 달라진 것이 의도라면 명세와 함께 PL_UPDATE_VALUATION_GOLDEN=1 로 다시 만든다(그 변경이 단일 법인 값을 바꾼다는 뜻이므로 검토가 필요하다).

const goldenPath = "testdata/valuation_single_entity.golden"

type goldenOp struct {
	wh, item, qty int64
	reason, ref   string
	refID         int64
	day           string
	cost          int64
}

// 입고·출고·창고 간 이동·조정(+, 평균 모름 경고 포함)·소급 입고·소급 출고·전량 출고 뒤 재입고를 섞은 대본이다. 모두 기본 법인 창고 둘.
var goldenScript = []goldenOp{
	{1, 1, 10, "receipt", "receipt", 1, "2026-01-05", 100},
	{1, 1, 10, "receipt", "receipt", 2, "2026-01-10", 130},
	{1, 1, -5, "issue", "issue", 1, "2026-01-12", 0},
	{1, 1, -4, "transfer_out", "transfer", 7, "2026-01-15", 0},
	{2, 1, 4, "transfer_in", "transfer", 7, "2026-01-15", 0},
	{1, 2, 3, "receipt", "receipt", 3, "2026-01-06", 333},
	{1, 2, -1, "issue", "issue", 2, "2026-02-02", 0},
	{1, 2, -2, "issue", "issue", 3, "2026-02-03", 0},     // 전량 출고: 잔액이 모두 빠진다
	{2, 2, 4, "adjust", "count", 5, "2026-02-10", 0},     // 잔량 0 뒤 조정(+): 직전 평균을 이어 쓴다
	{1, 3, 5, "adjust", "count", 6, "2026-01-20", 0},     // 입고가 한 번도 없던 품목: 0원 + 경고
	{2, 1, 6, "receipt", "receipt", 4, "2026-01-08", 90}, // 소급 입고: 뒤쪽 줄 재계산
	{1, 1, -2, "issue", "issue", 4, "2026-01-11", 0},     // 소급 출고
	{1, 1, 5, "receipt", "receipt", 5, "2026-02-20", 111},
	{2, 1, -3, "transfer_out", "transfer", 8, "2026-02-21", 0},
	{1, 1, 3, "transfer_in", "transfer", 8, "2026-02-21", 0},
}

func TestSingleEntityValuationGolden(t *testing.T) {
	ctx := context.Background()
	db := dbtest.Open(t) // SQLite·PG 둘 다 같은 골든과 비교한다
	if err := migrations.Apply(ctx, db); err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{
		`INSERT INTO departments (id, name) VALUES (1,'구매팀')`,
		`INSERT INTO users (id, email, name, role, department_id) VALUES (1,'fin@x','재무','finance',1)`,
		`INSERT INTO items (id, sku, name) VALUES (1,'A','복사용지'),(2,'B','토너'),(3,'C','스테이플러')`,
		`INSERT INTO warehouses (id, code, name, created_at, updated_at) VALUES (2,'WH-02','지점 창고','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`,
	} {
		if _, err := db.ExecContext(ctx, s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
	inv := repository.SQLInventory{}
	at := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	for i, op := range goldenScript {
		e := repository.LedgerEntry{WarehouseID: op.wh, ItemID: op.item, Qty: op.qty, Reason: op.reason, RefType: op.ref, RefID: op.refID, ActorID: 1, At: at, BookDate: op.day, UnitCost: op.cost}
		if err := inv.AppendLedger(ctx, db, e); err != nil {
			t.Fatalf("대본 %d: %v", i, err)
		}
	}

	var b strings.Builder
	rows, err := db.QueryContext(ctx, `SELECT ledger_id, item_id, book_date, COALESCE(in_unit_cost,-1), unit_cost, amount, bal_qty, bal_amount, avg_cost, warned FROM stock_valuation ORDER BY ledger_id`)
	if err != nil {
		t.Fatal(err)
	}
	b.WriteString("# stock_valuation: ledger item day in_cost unit amount bal_qty bal_amount avg warned\n")
	for rows.Next() {
		var ledger, item, inCost, unit, amount, balQty, balAmt, avg, warned int64
		var day string
		if err := rows.Scan(&ledger, &item, &day, &inCost, &unit, &amount, &balQty, &balAmt, &avg, &warned); err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&b, "%d %d %s %d %d %d %d %d %d %d\n", ledger, item, day, inCost, unit, amount, balQty, balAmt, avg, warned)
	}
	rows.Close()

	var snaps repository.SQLMonthClose
	for _, m := range [][2]string{{"2026-01-01", "2026-01-31"}, {"2026-02-01", "2026-02-28"}} {
		lines, err := snaps.BuildSnapshot(ctx, db, m[0], m[1])
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&b, "# snapshot %s\n", m[0][:7])
		for _, l := range lines {
			fmt.Fprintf(&b, "%d %d open=%d/%d in=%d/%d out=%d/%d tr=%d/%d adj=%d/%d close=%d/%d avg=%d\n", l.ItemID, l.WarehouseID,
				l.OpenQty, l.OpenAmount, l.InQty, l.InAmount, l.OutQty, l.OutAmount, l.TransferQty, l.TransferAmount, l.AdjustQty, l.AdjustAmount, l.CloseQty, l.CloseAmount, l.AvgCost)
		}
	}
	b.WriteString("# item valuation: item avg amount\n")
	for item := int64(1); item <= 3; item++ {
		avg, amount, ok, err := inv.ItemValuation(ctx, db, item)
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&b, "%d %d %d %v\n", item, avg, amount, ok)
	}
	n, err := snaps.WarnedAdjustments(ctx, db, "2026-01")
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintf(&b, "# warned adjustments 2026-01: %d\n", n)

	got := b.String()
	if os.Getenv("PL_UPDATE_VALUATION_GOLDEN") == "1" {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(goldenPath, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("골든 파일이 없습니다: %v", err)
	}
	if got != string(want) {
		t.Fatalf("법인 하나 설치의 평가 값이 달라졌습니다.\n--- 기대\n%s\n--- 실제\n%s", want, got)
	}
}
