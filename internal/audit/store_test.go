package audit_test

import (
	"context"
	"errors"
	"strconv"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/audit"
	"github.com/choibongseok/procure-ledger/internal/contract"
	caudit "github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/dbtest"
	"github.com/choibongseok/procure-ledger/migrations"
)

func setup(t *testing.T) (*audit.Store, contract.SQLTxManager) {
	t.Helper()
	db := dbtest.Open(t)
	if err := migrations.Apply(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	return audit.New(db), contract.SQLTxManager{DB: db}
}

var actor = contract.Principal{UserID: 7, Role: contract.RoleStaff, Channel: contract.ChannelWeb}

func entry(action, id string) caudit.Entry {
	return caudit.Entry{Actor: actor, Action: action, TargetType: caudit.TargetPurchaseRequest, TargetID: id,
		Result: caudit.ResultSuccess, Diff: []caudit.DiffItem{{Key: "status", Old: "draft", New: "submitted"}}}
}

func fill(t *testing.T, s *audit.Store, txs contract.SQLTxManager, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		id := strconv.Itoa(i + 1)
		err := txs.WithTx(context.Background(), func(tx contract.Tx) error {
			return s.Record(context.Background(), tx, entry("purchase_request.submit", id))
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestChainLinksAndVerifies(t *testing.T) {
	s, txs := setup(t)
	fill(t, s, txs, 5)
	recs, err := s.List(context.Background(), caudit.Filter{})
	if err != nil || len(recs) != 5 {
		t.Fatalf("list: %v %d", err, len(recs))
	}
	if recs[0].PrevHash != "" {
		t.Error("첫 레코드의 직전 해시는 비어 있어야 함")
	}
	for i := 1; i < len(recs); i++ {
		if recs[i].PrevHash != recs[i-1].Hash {
			t.Errorf("seq %d 가 직전 해시를 포함하지 않음", recs[i].Seq)
		}
	}
	res, err := s.Verify(context.Background())
	if err != nil || !res.OK || res.Checked != 5 || res.HeadHash != recs[4].Hash {
		t.Fatalf("verify: %+v %v", res, err)
	}
}

// 한 행을 바꾸면 검증이 그 지점을 짚는다. 추가 전용 트리거를 우회(DROP)해 변조를 흉내 낸다.
func TestTamperedRowIsDetectedAtThatSeq(t *testing.T) {
	cases := map[string]string{
		"detail 변조":   `UPDATE audit_logs SET detail = 'x' WHERE seq = 3`,
		"결과 변조":       `UPDATE audit_logs SET result = 'denied' WHERE seq = 3`,
		"행위자 변조":      `UPDATE audit_logs SET actor_id = 99 WHERE seq = 3`,
		"diff 변조":     `UPDATE audit_logs SET diff = '[]' WHERE seq = 3`,
		"시각 변조":       `UPDATE audit_logs SET at = '2020-01-01T00:00:00Z' WHERE seq = 3`,
		"해시 위조":       `UPDATE audit_logs SET hash = 'deadbeef' WHERE seq = 3`,
		"직전 해시 위조":    `UPDATE audit_logs SET prev_hash = 'deadbeef' WHERE seq = 3`,
		"형식 깨짐(diff)": `UPDATE audit_logs SET diff = 'not json' WHERE seq = 3`,
	}
	for name, stmt := range cases {
		t.Run(name, func(t *testing.T) {
			s, txs := setup(t)
			fill(t, s, txs, 5)
			if _, err := txs.DB.Exec(stmt); err == nil {
				t.Fatal("추가 전용 트리거가 수정을 막아야 함")
			}
			dbtest.DropTrigger(t, txs.DB, "trg_audit_no_update")
			if _, err := txs.DB.Exec(stmt); err != nil {
				t.Fatal(err)
			}
			res, err := s.Verify(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if res.OK || res.BrokenSeq != 3 {
				t.Fatalf("seq 3 을 짚어야 함: %+v", res)
			}
		})
	}
}

func TestDeletedRowIsDetected(t *testing.T) {
	s, txs := setup(t)
	fill(t, s, txs, 5)
	if _, err := txs.DB.Exec(`DELETE FROM audit_logs WHERE seq = 2`); err == nil {
		t.Fatal("추가 전용 트리거가 삭제를 막아야 함")
	}
	// 트리거를 우회해 삭제하면 이어지지 않는 지점(삭제된 다음 행)을 짚는다.
	dbtest.DropTrigger(t, txs.DB, "trg_audit_no_delete")
	if _, err := txs.DB.Exec(`DELETE FROM audit_logs WHERE seq = 2`); err != nil {
		t.Fatal(err)
	}
	res, _ := s.Verify(context.Background())
	if res.OK || res.BrokenSeq != 3 {
		t.Fatalf("seq 3 에서 끊겨야 함: %+v", res)
	}
}

// 기록이 실패하면 같은 트랜잭션의 쓰기도 롤백된다(기록 실패 = 요청 실패).
func TestRecordFailureRollsBackBusinessWrite(t *testing.T) {
	s, txs := setup(t)
	ctx := context.Background()
	err := txs.WithTx(ctx, func(tx contract.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO departments (name) VALUES ('임시팀')`); err != nil {
			return err
		}
		bad := entry("x.y", "1")
		bad.TargetType = "nope" // 기록 실패 유발
		return s.Record(ctx, tx, bad)
	})
	if !errors.Is(err, contract.ErrAuditFailed) {
		t.Fatalf("ErrAuditFailed 기대: %v", err)
	}
	var n, c int
	_ = txs.DB.QueryRow(`SELECT COUNT(*) FROM departments WHERE name='임시팀'`).Scan(&n)
	_ = txs.DB.QueryRow(`SELECT COUNT(*) FROM audit_logs`).Scan(&c)
	if n != 0 || c != 0 {
		t.Fatalf("롤백되지 않음: 부서 %d 감사 %d", n, c)
	}
}

func TestRecordRejectsInvalidEntries(t *testing.T) {
	s, txs := setup(t)
	ctx := context.Background()
	mut := map[string]func(*caudit.Entry){
		"빈 action":  func(e *caudit.Entry) { e.Action = "" },
		"빈 대상 ID":   func(e *caudit.Entry) { e.TargetID = "" },
		"잘못된 결과":    func(e *caudit.Entry) { e.Result = "ok" },
		"경로 없음":     func(e *caudit.Entry) { e.Actor.Channel = "" },
		"알 수 없는 대상": func(e *caudit.Entry) { e.TargetType = "x" },
	}
	for name, m := range mut {
		e := entry("a.b", "1")
		m(&e)
		err := txs.WithTx(ctx, func(tx contract.Tx) error { return s.Record(ctx, tx, e) })
		if !errors.Is(err, contract.ErrAuditFailed) {
			t.Errorf("%s: 거부되어야 함: %v", name, err)
		}
	}
}

func TestListFilters(t *testing.T) {
	s, txs := setup(t)
	fill(t, s, txs, 4)
	ctx := context.Background()
	recs, _ := s.List(ctx, caudit.Filter{Desc: true, Limit: 2})
	if len(recs) != 2 || recs[0].Seq != 4 {
		t.Fatalf("최신순 2건: %+v", recs)
	}
	recs, _ = s.List(ctx, caudit.Filter{AfterSeq: 3})
	if len(recs) != 1 || recs[0].Seq != 4 {
		t.Fatalf("AfterSeq: %+v", recs)
	}
	recs, _ = s.List(ctx, caudit.Filter{ActorID: 99})
	if len(recs) != 0 {
		t.Fatal("다른 행위자 결과가 섞임")
	}
}
