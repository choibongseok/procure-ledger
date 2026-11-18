package changes_test

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/choibongseok/procure-ledger/internal/changes"
	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/dbtest"
	"github.com/choibongseok/procure-ledger/internal/dbutil"
	"github.com/choibongseok/procure-ledger/migrations"
)

func setup(t *testing.T) (contract.TxManager, contract.Tx) {
	t.Helper()
	db := dbtest.Open(t)
	if err := migrations.Apply(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	return dbutil.NewTxManager(db, dbutil.Active()), db
}

func note(t *testing.T, txs contract.TxManager, res string, id int64, op, status string) {
	t.Helper()
	if err := txs.WithTx(context.Background(), func(tx contract.Tx) error {
		return changes.Note(context.Background(), tx, res, id, op, status)
	}); err != nil {
		t.Fatal(err)
	}
}

// 롤백되면 줄도 없고, 값이 아니라 자원·번호·동작·상태·바꾼 경로만 남는다.
func TestNoteIsTransactionalAndValueFree(t *testing.T) {
	ctx := context.Background()
	txs, db := setup(t)
	boom := errors.New("롤백")
	err := txs.WithTx(ctx, func(tx contract.Tx) error {
		if err := changes.Note(ctx, tx, changes.ResVendor, 1, changes.OpCreated, ""); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatal(err)
	}
	if h, err := changes.Head(ctx, db); err != nil || h != 0 {
		t.Fatalf("롤백했는데 줄이 남음: head=%d %v", h, err)
	}
	apiCtx := contract.WithPrincipal(ctx, contract.Principal{UserID: 7, Channel: contract.ChannelAPI})
	if err := txs.WithTx(ctx, func(tx contract.Tx) error {
		return changes.Note(apiCtx, tx, changes.ResJournal, 9, changes.OpStatusChanged, "approved")
	}); err != nil {
		t.Fatal(err)
	}
	evs, more, head, err := changes.After(ctx, db, 0, 10, nil)
	if err != nil || more || len(evs) != 1 || head != evs[0].Seq { // PG 는 롤백된 줄이 번호 구멍을 남길 수 있다
		t.Fatalf("읽기: %+v %v %d %v", evs, more, head, err)
	}
	e := evs[0]
	if e.Resource != changes.ResJournal || e.ObjectID != 9 || e.Op != changes.OpStatusChanged || e.Status != "approved" || e.ActorChannel != "api" || e.Seq < 1 {
		t.Fatalf("줄: %+v", e)
	}
	// 알 수 없는 자원·동작·번호는 거부한다.
	for _, bad := range []struct {
		res string
		id  int64
		op  string
	}{{"user", 1, changes.OpCreated}, {changes.ResItem, 1, "deleted"}, {changes.ResItem, 0, changes.OpCreated}} {
		if err := txs.WithTx(ctx, func(tx contract.Tx) error { return changes.Note(ctx, tx, bad.res, bad.id, bad.op, "") }); err == nil {
			t.Errorf("거부되어야 함: %+v", bad)
		}
	}
	// 추가 전용.
	if _, err := db.ExecContext(ctx, `UPDATE change_events SET status = 'x'`); err == nil || !strings.Contains(err.Error(), "추가 전용") {
		t.Errorf("수정이 막혀야 함: %v", err)
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM change_events`); err == nil || !strings.Contains(err.Error(), "추가 전용") {
		t.Errorf("삭제가 막혀야 함: %v", err)
	}
}

func TestAfterCursorFilterLimitAndErrors(t *testing.T) {
	ctx := context.Background()
	txs, db := setup(t)
	for i := int64(1); i <= 7; i++ {
		res := changes.ResItem
		if i%2 == 0 {
			res = changes.ResVendor
		}
		note(t, txs, res, i, changes.OpCreated, "")
	}
	// 쪽 나누기: limit 3 → more, 이어서 마지막 번호부터.
	evs, more, head, err := changes.After(ctx, db, 0, 3, nil)
	if err != nil || !more || head != 7 || len(evs) != 3 || evs[2].Seq != 3 {
		t.Fatalf("첫 쪽: %+v %v %d %v", evs, more, head, err)
	}
	evs, more, _, _ = changes.After(ctx, db, 6, 3, nil)
	if more || len(evs) != 1 || evs[0].Seq != 7 {
		t.Fatalf("끝 쪽: %+v %v", evs, more)
	}
	// 자원 필터: 걸러진 줄은 보이지 않지만 head 는 읽기 시작 때의 값이다.
	evs, more, head, _ = changes.After(ctx, db, 0, 10, []string{changes.ResVendor})
	if more || head != 7 || len(evs) != 3 {
		t.Fatalf("필터: %d %v", len(evs), more)
	}
	for _, e := range evs {
		if e.Resource != changes.ResVendor {
			t.Fatalf("다른 자원이 섞임: %+v", e)
		}
	}
	if _, _, _, err := changes.After(ctx, db, 0, 10, []string{"user"}); !errors.Is(err, contract.ErrValidation) {
		t.Errorf("알 수 없는 자원 필터: %v", err)
	}
	// 미래 커서는 거부, 보존 정리로 사라진 구간은 만료.
	if _, _, _, err := changes.After(ctx, db, 8, 10, nil); !errors.Is(err, changes.ErrBadCursor) {
		t.Errorf("미래 커서: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO app_settings (key, value) VALUES ('changes.purged_through', '3')`); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := changes.After(ctx, db, 2, 10, nil); !errors.Is(err, changes.ErrCursorExpired) {
		t.Errorf("정리된 구간의 커서: %v", err)
	}
	if _, _, _, err := changes.After(ctx, db, 3, 10, nil); err != nil {
		t.Errorf("정리 경계(마지막으로 지운 번호) 이후는 정상: %v", err)
	}
	// 커서 형식.
	for _, c := range []string{"", "7", "p1.7", "c1.x", "c1.-1"} {
		if _, err := changes.DecodeCursor(c); !errors.Is(err, changes.ErrBadCursor) {
			t.Errorf("잘못된 커서 %q: %v", c, err)
		}
	}
	if n, err := changes.DecodeCursor(changes.EncodeCursor(42)); err != nil || n != 42 {
		t.Errorf("왕복: %d %v", n, err)
	}
}

// AC-4210: 여러 트랜잭션이 동시에 쓰는 동안(Note 뒤 늦게 커밋하는 것·롤백하는 것 포함) 커서 소비자가 커밋된 줄을 한 줄도 놓치지 않고,
// 번호 순서대로 한 번씩만 받는다. PG 에서는 피드 전용 잠금이 "번호 순서 = 커밋 순서"를 지켜서 늦게 커밋된 작은 번호가 사라지지 않는다.
func TestCursorConsumerNeverMissesCommittedRows(t *testing.T) {
	ctx := context.Background()
	txs, db := setup(t)
	const writers, perWriter = 8, 12
	var mu sync.Mutex
	committed := map[int64]bool{} // object_id → 커밋됨
	var wg sync.WaitGroup
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			rnd := rand.New(rand.NewSource(int64(w) + 1))
			for i := 0; i < perWriter; i++ {
				id := int64(w*1000 + i + 1)
				rollback := rnd.Intn(5) == 0
				err := txs.WithTx(ctx, func(tx contract.Tx) error {
					if err := changes.Note(ctx, tx, changes.ResItem, id, changes.OpCreated, ""); err != nil {
						return err
					}
					time.Sleep(time.Duration(rnd.Intn(8)) * time.Millisecond) // Note 와 커밋 사이의 지연
					if rollback {
						return errors.New("롤백")
					}
					return nil
				})
				if err == nil {
					mu.Lock()
					committed[id] = true
					mu.Unlock()
				}
			}
		}(w)
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()

	var got []changes.Event
	var cursor int64
	drain := func() {
		for {
			evs, more, head, err := changes.After(ctx, db, cursor, 7, nil)
			if err != nil {
				t.Errorf("읽기: %v", err)
				return
			}
			got = append(got, evs...)
			if len(evs) > 0 {
				cursor = evs[len(evs)-1].Seq
			}
			if !more {
				if len(evs) == 0 && head > cursor {
					cursor = head // 필터가 없으면 일어나지 않지만 규칙대로 head 까지 훑은 것으로 본다
				}
				return
			}
		}
	}
loop:
	for {
		select {
		case <-done:
			break loop
		default:
			drain()
			time.Sleep(time.Millisecond)
		}
	}
	drain()
	seen := map[int64]int{}
	var last int64
	for _, e := range got {
		if e.Seq <= last {
			t.Fatalf("번호가 오름차순이 아님: %d 다음 %d", last, e.Seq)
		}
		last = e.Seq
		seen[e.ObjectID]++
	}
	var missing, dup, extra []string
	for id := range committed {
		if seen[id] == 0 {
			missing = append(missing, fmt.Sprint(id))
		}
	}
	for id, n := range seen {
		if n > 1 {
			dup = append(dup, fmt.Sprint(id))
		}
		if !committed[id] {
			extra = append(extra, fmt.Sprint(id))
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 || len(dup) > 0 || len(extra) > 0 {
		t.Fatalf("소비자가 놓침 %v / 중복 %v / 롤백된 줄 %v (커밋 %d, 받음 %d)", missing, dup, extra, len(committed), len(got))
	}
}
