package service_test

import (
	"context"
	"testing"
	"time"

	"github.com/choibongseok/procure-ledger/internal/dbtest"
	"github.com/choibongseok/procure-ledger/internal/dbutil"
)

// B37 S3(AC-1721): PG 에서 월 마감 트랜잭션이 월 잠금을 쥐고 있으면 그 달 원장에 쓰려는 트랜잭션은 기다리고, 끝나면 마감 결과를 본다.
// 잠금을 건너뛰는 "틀린 구현"(마감이 잠금을 안 잡음)이면 쓰기가 기다리지 않고 바로 끝나므로 이 시험이 실패한다.
func TestMonthLockMakesLedgerWriteWaitForCloseOnPG(t *testing.T) {
	if !dbtest.PG() {
		t.Skip("PostgreSQL 전용 시험(잠금 동작)")
	}
	e := newEnv(t)
	it := e.item(t, "LOCK-1", 0, false)
	ctx := context.Background()
	closer, err := e.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer closer.Rollback()
	if err := dbutil.AcquireMonthClose(ctx, closer, "2026-09"); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := e.db.ExecContext(ctx, `INSERT INTO stock_ledger (item_id, qty_delta, reason, actor_id, warehouse_id, created_at, book_date) VALUES (?,1,'receipt',1,1,'2026-09-05T00:00:00Z','2026-09-05')`, it.ID)
		done <- err
	}()
	select {
	case err := <-done:
		t.Fatalf("마감이 월 잠금을 쥐고 있는데 그 달 쓰기가 기다리지 않음: %v", err)
	case <-time.After(500 * time.Millisecond):
	}
	if err := closer.Commit(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("마감이 끝난 뒤(열린 달) 쓰기는 통과해야 함: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("마감이 끝났는데도 쓰기가 계속 기다림")
	}
	// 다른 달 쓰기는 기다리지 않는다.
	if _, err := e.db.ExecContext(ctx, `INSERT INTO stock_ledger (item_id, qty_delta, reason, actor_id, warehouse_id, created_at, book_date) VALUES (?,1,'receipt',1,1,'2026-08-05T00:00:00Z','2026-08-05')`, it.ID); err != nil {
		t.Fatal(err)
	}
}
