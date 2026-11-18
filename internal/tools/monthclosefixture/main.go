// 명령: monthclosefixture — e2e 전용 픽스처(B27, 명세 AC-990).
//
//	go run ./internal/tools/monthclosefixture -db <DB 파일>
//
// 서버가 한 번 기동해 시드(사용자·조직)를 만든 뒤 멈춘 DB 에, 지난 두 달의 장부일을 가진 재고 원장(입고·출고)을 **저장소 함수**(AppendLedger, book_date 지정)로 넣는다.
// 실제 시계로는 지난 달 기록을 API 로 만들 수 없어서 마감 흐름을 실제 서버로 돌리려는 시험용이다. 운영 코드에 시계 조작 경로를 두지 않고, 이 도구는 서버 바이너리에 들어가지 않는다.
// 출력(한 줄, 공백 구분): 지지난 달 지난 달 품목번호.
//
//	-block    지지난 달 기준일의 "결재 중" 실사 표를 하나 더 넣는다(마감 전 점검의 차단 항목).
//	-resolve  그 실사 표를 반려로 정리한다(조정 전표 반려의 결과 상태. 결재 API 로 만든 문서가 아니므로 서버를 멈춘 DB 에서 한다).
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/dbutil"
	"github.com/choibongseok/procure-ledger/internal/repository"
)

func main() {
	dbPath := flag.String("db", "", "서버가 시드를 만든 DB 파일")
	block := flag.Bool("block", false, "지지난 달 기준일의 결재 중 실사 표를 더 넣는다")
	resolve := flag.Bool("resolve", false, "-block 으로 넣은 실사 표를 반려로 정리한다")
	flag.Parse()
	blockFlag, resolveFlag = *block, *resolve
	if *dbPath == "" {
		fmt.Fprintln(os.Stderr, "-db 가 필요합니다")
		os.Exit(2)
	}
	if err := run(*dbPath); err != nil {
		fmt.Fprintln(os.Stderr, "픽스처 실패:", err)
		os.Exit(1)
	}
}

var blockFlag, resolveFlag bool

func run(path string) error {
	db, err := dbutil.Open(path)
	if err != nil {
		return err
	}
	defer db.Close()
	ctx := context.Background()
	if resolveFlag {
		_, err := db.ExecContext(ctx, `UPDATE stock_counts SET status='rejected', reject_reason='재무팀장 반려: 수량 재확인', updated_at=? WHERE no='X-FIX-1' AND status='approving'`, time.Now().UTC().Format("2006-01-02T15:04:05Z"))
		return err
	}
	kst := time.FixedZone("KST", 9*3600)
	now := time.Now().In(kst)
	first := time.Date(now.Year(), now.Month(), 1, 12, 0, 0, 0, kst)
	m2, m1 := first.AddDate(0, -2, 0), first.AddDate(0, -1, 0)
	var actor int64
	if err := db.QueryRowContext(ctx, `SELECT id FROM users WHERE email = 'finance@pl.test'`).Scan(&actor); err != nil {
		return fmt.Errorf("재무 계정을 찾을 수 없습니다(서버를 한 번 기동해 시드를 만든 DB 여야 합니다): %w", err)
	}
	res, err := db.ExecContext(ctx, `INSERT INTO items (sku, name, unit, safety_stock, track_lot) VALUES ('MC-FIX-1', '마감 시험품', 'EA', 0, 0)`)
	if err != nil {
		return err
	}
	item, _ := res.LastInsertId()
	inv := repository.SQLInventory{}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var q contract.Tx = tx
	add := func(at time.Time, qty int64, reason string, cost int64) error {
		return inv.AppendLedger(ctx, q, repository.LedgerEntry{WarehouseID: 1, ItemID: item, Qty: qty, Reason: reason, RefType: reason, RefID: 0, ActorID: actor,
			At: at.UTC(), BookDate: at.Format("2006-01-02"), UnitCost: cost})
	}
	// 지지난 달: 입고 10개(개당 1,000원)와 출고 4개 → 기말 6개 6,000원. 지난 달: 입고 10개(개당 2,000원) → 기말 16개 26,000원.
	steps := []struct {
		at     time.Time
		qty    int64
		reason string
		cost   int64
	}{
		{m2.AddDate(0, 0, 4), 10, "receipt", 1000},
		{m2.AddDate(0, 0, 19), -4, "issue", 0},
		{m1.AddDate(0, 0, 4), 10, "receipt", 2000},
	}
	for _, s := range steps {
		if err := add(s.at, s.qty, s.reason, s.cost); err != nil {
			return err
		}
	}
	if blockFlag {
		base := time.Date(m2.Year(), m2.Month()+1, 0, 12, 0, 0, 0, kst).Format("2006-01-02") // 지지난 달 마지막 날
		ts := time.Now().UTC().Format("2006-01-02T15:04:05Z")
		if _, err := tx.ExecContext(ctx, `INSERT INTO stock_counts (no, warehouse_id, base_date, status, created_by, created_at, updated_at, snapshot_ledger_id) VALUES ('X-FIX-1', 1, ?, 'approving', ?, ?, ?, 0)`, base, actor, ts, ts); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	fmt.Printf("%s %s %d\n", m2.Format("2006-01"), m1.Format("2006-01"), item)
	return nil
}
