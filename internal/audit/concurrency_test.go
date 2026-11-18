package audit_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/dbtest"
)

// B37 S1: 여러 트랜잭션이 동시에 감사 기록을 붙여도 체인은 끊기지 않고 seq 는 빈틈·중복이 없다.
func TestConcurrentRecordsKeepChainIntact(t *testing.T) {
	s, txs := setup(t)
	const workers, each = 8, 10
	var wg sync.WaitGroup
	errs := make(chan error, workers*each)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < each; i++ {
				id := fmt.Sprintf("%d-%d", w, i)
				if err := txs.WithTx(context.Background(), func(tx contract.Tx) error {
					return s.Record(context.Background(), tx, entry("purchase_request.submit", id))
				}); err != nil {
					errs <- fmt.Errorf("worker %d: %w", w, err)
				}
			}
		}(w)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	res, err := s.Verify(context.Background())
	if err != nil || !res.ChainOK || res.Checked != workers*each {
		t.Fatalf("체인 검증: %+v %v (기록 %d건이어야 함)", res, err, workers*each)
	}
}

// holdAndRace 는 트랜잭션 A 가 기록한 채 열려 있는 동안 B 가 기록을 시도하게 하고 B 의 결과를 돌려준다. PG 에서만 의미가 있다(연결이 여럿).
func holdAndRace(t *testing.T, skipLock bool) error {
	t.Helper()
	if !dbtest.PG() {
		t.Skip("PG 전용: SQLite 는 연결이 하나라 A 가 열려 있는 동안 B 가 시작되지 않는다(위 병렬 시험이 SQLite 의 직렬성을 본다)")
	}
	s, txs := setup(t)
	if skipLock {
		s.SkipChainLockForTest()
	}
	ctx := context.Background()
	aRecorded := make(chan struct{})
	aCommit := make(chan struct{})
	aDone := make(chan error, 1)
	go func() {
		aDone <- txs.WithTx(ctx, func(tx contract.Tx) error {
			if err := s.Record(ctx, tx, entry("purchase_request.submit", "A")); err != nil {
				return err
			}
			close(aRecorded)
			<-aCommit
			return nil
		})
	}()
	<-aRecorded
	bDone := make(chan error, 1)
	go func() {
		bDone <- txs.WithTx(ctx, func(tx contract.Tx) error {
			return s.Record(ctx, tx, entry("purchase_request.submit", "B"))
		})
	}()
	select {
	case err := <-bDone:
		t.Fatalf("A 가 커밋하기 전에 B 가 끝났다: %v", err)
	case <-time.After(300 * time.Millisecond):
	}
	close(aCommit)
	if err := <-aDone; err != nil {
		t.Fatalf("A: %v", err)
	}
	berr := <-bDone
	if berr == nil {
		if res, err := s.Verify(ctx); err != nil || !res.ChainOK || res.Checked != 2 {
			t.Fatalf("체인 검증: %+v %v", res, err)
		}
	}
	return berr
}

// AC-1720: A 가 체인 꼬리를 쥐고 있으면 B 는 기다렸다가 그 뒤에 붙는다(체인이 갈라지지 않는다).
func TestChainSecondWriterWaitsAndAppends(t *testing.T) {
	if err := holdAndRace(t, false); err != nil {
		t.Fatalf("B 는 A 뒤에 성공해야 함: %v", err)
	}
}

// "틀린 구현" 시험: 잠금을 건너뛰면 B 가 A 와 같은 seq 를 붙이려다 실패한다(PK 충돌).
// 위 시험이 이 구현을 걸러 낸다는 증거다.
func TestChainWithoutLockForks(t *testing.T) {
	if err := holdAndRace(t, true); err == nil {
		t.Fatal("잠금 없이도 B 가 성공했다: 위 시험이 잠금 누락을 잡지 못한다")
	}
}
