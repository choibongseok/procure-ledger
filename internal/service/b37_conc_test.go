package service_test

import (
	"context"
	"database/sql"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/dbtest"
	"github.com/choibongseok/procure-ledger/internal/dbutil"
	"github.com/choibongseok/procure-ledger/internal/repository"
)

// B37 5절 동시성 시험(AC-1721~1724·1731): 번호 발급·재고 출고·예산 상신이 병렬로 몰려도 중복·음수·초과가 생기지 않는다.
// 잠금을 뺀 "틀린 구현"이 같은 시험 형식에 걸리는지도 함께 보인다(통제 시험). 틀린 구현 통제는 연결이 여럿인 PG 에서만 의미가 있다.

// ---- S2 번호 발급 ----

func inTx(t *testing.T, db *sql.DB, fn func(tx contract.Tx) error) error {
	t.Helper()
	return (contract.SQLTxManager{DB: db}).WithTx(context.Background(), fn)
}

// 여러 고루틴이 동시에 전표·지급·재고 번호를 받아도 번호는 중복 없이 1..N 이다.
func TestConcurrentNumberingHasNoDuplicatesOrGaps(t *testing.T) {
	e := newEnv(t)
	const workers, each = 8, 6
	type got struct{ kind, no string }
	var mu sync.Mutex
	var all []got
	var wg sync.WaitGroup
	errs := make(chan error, workers*each)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < each; i++ {
				err := inTx(t, e.db, func(tx contract.Tx) error {
					ctx := context.Background()
					jv, err := repository.SQLLedger{}.NextJournalNo(ctx, tx, 2099)
					if err != nil {
						return err
					}
					pay, err := repository.SQLPayments{}.NextPaymentNo(ctx, tx, 2099)
					if err != nil {
						return err
					}
					st, err := repository.SQLStock{}.NextNo(ctx, tx, "ISS", 2099)
					if err != nil {
						return err
					}
					mu.Lock()
					all = append(all, got{"JV", jv}, got{"PAY", pay}, got{"ISS", st})
					mu.Unlock()
					return nil
				})
				if err != nil {
					errs <- err
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	by := map[string][]string{}
	for _, g := range all {
		by[g.kind] = append(by[g.kind], g.no)
	}
	for kind, nos := range by {
		if len(nos) != workers*each {
			t.Fatalf("%s: %d건 (기대 %d)", kind, len(nos), workers*each)
		}
		sort.Strings(nos)
		for i := 1; i < len(nos); i++ {
			if nos[i] == nos[i-1] {
				t.Fatalf("%s: 번호 중복 %s", kind, nos[i])
			}
		}
	}
	if e.col(t, `SELECT last FROM inventory_counters WHERE prefix = 'JV' AND year = 2099`) != "48" {
		t.Errorf("마지막 번호가 발급 건수와 다름(결번 또는 중복)")
	}
}

// 번호를 받은 트랜잭션이 열려 있는 동안 다른 트랜잭션은 같은 종류의 번호를 기다린다(PG). 앞 트랜잭션이 끝난 뒤 다음 번호를 받는다.
func TestNumberRowLockQueuesSecondTransactionOnPG(t *testing.T) {
	if !dbtest.PG() {
		t.Skip("PG 전용: SQLite 는 연결이 하나라 앞 트랜잭션이 열려 있는 동안 다른 트랜잭션이 시작되지 않는다")
	}
	e := newEnv(t)
	ctx := context.Background()
	a, err := e.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Rollback()
	first, err := repository.SQLLedger{}.NextJournalNo(ctx, a, 2098)
	if err != nil {
		t.Fatal(err)
	}
	type res struct {
		no  string
		err error
	}
	done := make(chan res, 1)
	go func() {
		var no string
		err := inTx(t, e.db, func(tx contract.Tx) error {
			var err error
			no, err = repository.SQLLedger{}.NextJournalNo(ctx, tx, 2098)
			return err
		})
		done <- res{no, err}
	}()
	select {
	case r := <-done:
		t.Fatalf("앞 트랜잭션이 번호를 쥐고 있는데 뒤 트랜잭션이 기다리지 않음: %+v", r)
	case <-time.After(400 * time.Millisecond):
	}
	if err := a.Commit(); err != nil {
		t.Fatal(err)
	}
	select {
	case r := <-done:
		if r.err != nil || r.no == first {
			t.Fatalf("뒤 트랜잭션: %+v (앞 번호 %s)", r, first)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("앞 트랜잭션이 끝났는데도 기다림")
	}
}

// 통제 시험: "읽고 나서 쓰는" 틀린 채번(SELECT last → UPDATE last = n+1)은 같은 형식의 병렬 실행에서 번호가 겹친다.
func TestWrongReadThenWriteNumberingDuplicatesOnPG(t *testing.T) {
	if !dbtest.PG() {
		t.Skip("PG 전용 통제 시험")
	}
	e := newEnv(t)
	ctx := context.Background()
	if _, err := e.db.Exec(`INSERT INTO inventory_counters (prefix, year, last) VALUES ('WRONG', 2097, 0)`); err != nil {
		t.Fatal(err)
	}
	read := func(tx *sql.Tx) int {
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT last FROM inventory_counters WHERE prefix = 'WRONG' AND year = 2097`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	a, _ := e.db.BeginTx(ctx, nil)
	b, _ := e.db.BeginTx(ctx, nil)
	defer a.Rollback()
	defer b.Rollback()
	na, nb := read(a)+1, read(b)+1 // 둘 다 쓰기 전에 읽는다
	if _, err := a.ExecContext(ctx, `UPDATE inventory_counters SET last = ? WHERE prefix = 'WRONG' AND year = 2097`, na); err != nil {
		t.Fatal(err)
	}
	if err := a.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := b.ExecContext(ctx, `UPDATE inventory_counters SET last = ? WHERE prefix = 'WRONG' AND year = 2097`, nb); err != nil {
		t.Fatal(err)
	}
	if na != nb {
		t.Fatalf("통제 시험이 틀린 구현의 중복을 보이지 못함: %d %d", na, nb)
	}
}

// ---- S5 재고 출고 ----

func (e *env) concurrentIssues(t *testing.T, it domain.Item, workers int) (ok int) {
	t.Helper()
	var wg sync.WaitGroup
	var mu sync.Mutex
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := e.issueOn(t, it, 1, "2026-11-10"); err == nil {
				mu.Lock()
				ok++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	return ok
}

// 재고 3 에서 동시에 8건을 1개씩 출고하면 정확히 3건만 성공하고 재고는 0 이다(음수 불가).
func TestConcurrentIssuesNeverGoNegative(t *testing.T) {
	e := newEnv(t)
	it := e.item(t, "CONC-ISS", 0, false)
	e.seedPlain(t, it.ID, 1, 3)
	ok := e.concurrentIssues(t, it, 8)
	if got := whQty(t, e, 1, it.ID, 0); ok != 3 || got != 0 {
		t.Fatalf("성공 %d건(기대 3), 재고 %d(기대 0)", ok, got)
	}
}

// 재고 줄 키 잠금(S5)은 같은 창고·품목의 다른 트랜잭션을 기다리게 하고, 잠금을 뺀 구현에서는 기다리지 않는다(통제).
// 출고·이동은 전표 번호 행 잠금(S2)이 이미 트랜잭션 전체를 줄 세우므로 서비스 수준 시험만으로는 이 잠금을 따로 검증할 수 없어 잠금 자체를 본다.
func TestStockKeyLockBlocksSecondTransactionOnPG(t *testing.T) {
	if !dbtest.PG() {
		t.Skip("PG 전용 시험(잠금 동작)")
	}
	e := newEnv(t)
	ctx := context.Background()
	waits := func(skip bool) bool {
		if skip {
			defer dbutil.SkipLocksForTest()()
		}
		a, err := e.db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer a.Rollback()
		if err := dbutil.AcquireKeyLock(ctx, a, "stock:1:77"); err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() {
			b, err := e.db.BeginTx(ctx, nil)
			if err != nil {
				done <- err
				return
			}
			defer b.Rollback()
			done <- dbutil.AcquireKeyLock(ctx, b, "stock:1:77")
		}()
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
			return false // 기다리지 않았다
		case <-time.After(400 * time.Millisecond):
		}
		if err := a.Commit(); err != nil {
			t.Fatal(err)
		}
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("앞 트랜잭션이 끝났는데도 기다림")
		}
		return true
	}
	if !waits(false) {
		t.Fatal("재고 키 잠금을 쥔 동안 다른 트랜잭션이 기다리지 않음")
	}
	if waits(true) {
		t.Fatal("통제: 잠금을 뺀 구현에서도 기다림(시험이 잠금을 검증하지 못한다)")
	}
}

// ---- S5 예산 동시 상신 ----

// 통제 시험: 예산 잠금을 뺀 구현은 같은 부서 동시 상신 두 건이 모두 "예산 안"으로 판정될 수 있다(올바른 구현은 TestBudgetConcurrentSubmitJudgedInOrder).
func TestBudgetSubmitWithoutLockBothWithinOnPG(t *testing.T) {
	if !dbtest.PG() {
		t.Skip("PG 전용 통제 시험")
	}
	restore := dbutil.SkipLocksForTest()
	defer restore()
	for round := 0; round < 15; round++ {
		e := newEnv(t)
		e.setBudget(t, deptBuy, 500_000)
		a, b := e.draft(t, uStaff, 300_000), e.draft(t, uLead3, 300_000)
		var wg sync.WaitGroup
		res := make([]domain.PurchaseRequest, 2)
		for i, x := range []struct{ by, id int64 }{{uStaff, a.ID}, {uLead3, b.ID}} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				res[i], _ = e.purchase.Submit(as(x.by), x.id)
			}()
		}
		wg.Wait()
		over := 0
		for _, r := range res {
			if r.Budget != nil && r.Budget.State == domain.BudgetOver {
				over++
			}
		}
		if over == 0 {
			return // 틀린 구현이 걸렸다: 두 건 모두 예산 안으로 통과
		}
	}
	t.Fatal("잠금을 뺀 구현도 두 건을 모두 예산 안으로 판정하지 않았다: 병렬 시험이 예산 잠금을 검증하지 못한다")
}

// ---- 로트 배정·지급 예약 읽고-쓰기 경합 ----

// 같은 로트를 동시에 출고해도 로트 재고(3)를 넘겨 배정되지 않는다: 로트 단위 재고를 키 잠금 뒤에 다시 읽는다.
func TestConcurrentLotIssuesNeverOverAllocate(t *testing.T) {
	e := newEnv(t)
	it := e.item(t, "CONC-LOT", 0, true)
	lot := e.seedLot(t, it.ID, "L-CONC", "2027-12-31", 1, 3, "2026-11-01T00:00:00Z")
	var wg sync.WaitGroup
	var mu sync.Mutex
	ok := 0
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := e.stock().is.Issue(as(uFinance), domain.IssueInput{DepartmentID: deptSales, WarehouseID: 1, Method: domain.IssueManual, IssueDate: "2026-11-10",
				Lines: []domain.IssueLineInput{{ItemID: it.ID, LotID: lot, Qty: 1}}})
			if err == nil {
				mu.Lock()
				ok++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if got := whQty(t, e, 1, it.ID, lot); ok != 3 || got != 0 {
		t.Fatalf("성공 %d건(기대 3), 로트 재고 %d(기대 0)", ok, got)
	}
}

// 채무 항목 예약은 조건부 갱신 한 문장이라 동시에 몰려도 지급 가능액(3)을 넘겨 예약되지 않는다(초과 지급 불가).
func TestConcurrentPayableReservationsNeverExceedAmount(t *testing.T) {
	e := newEnv(t)
	e.addPayable(t, 501, "2026-12-31", 3)
	var wg sync.WaitGroup
	var mu sync.Mutex
	ok := 0
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := inTx(t, e.db, func(tx contract.Tx) error {
				return repository.SQLPayments{}.ReservePayable(context.Background(), tx, 501, 1)
			})
			if err == nil {
				mu.Lock()
				ok++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if reserved := e.col(t, `SELECT reserved_amount FROM payables WHERE id = 501`); reserved != "3" || ok != 3 {
		t.Fatalf("예약 성공 %d건(기대 3), 예약액 %s(기대 3)", ok, reserved)
	}
}
