package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/dbtest"
	"github.com/choibongseok/procure-ledger/internal/repository"
	"github.com/choibongseok/procure-ledger/migrations"
)

// B36: Base.notify 는 앱 안 알림을 만든 뒤 같은 트랜잭션에서 발송 건 훅(Deliver)을 부른다. 훅이 실패해도 알림과 업무는 그대로다(AC-1600).
func TestNotifyCallsDeliverHookAndSurvivesItsFailure(t *testing.T) {
	db := dbtest.Open(t)
	ctx := context.Background()
	if err := migrations.Apply(ctx, db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO departments (id, name) VALUES (1, '구매팀')`); err != nil {
		t.Fatal(err)
	}
	uid := dbtest.InsertID(t, db, `INSERT INTO users (email, name, role, department_id) VALUES ('n@pl.test','n','staff',1)`)
	type call struct {
		nid, user int64
		kind      string
	}
	var calls []call
	var hookErr error
	b := Base{DB: db, Txs: contract.SQLTxManager{DB: db}, Deliver: func(_ context.Context, _ contract.Tx, nid, user int64, kind string, at time.Time) error {
		calls = append(calls, call{nid, user, kind})
		if at.IsZero() {
			t.Error("알림 시각이 비어 있음")
		}
		return hookErr
	}}
	in := repository.NotificationInsert{UserID: uid, Kind: "approval_pending", Title: "t", Link: "/approvals/1", RefType: "purchase_request", RefID: 1}
	if err := b.Txs.WithTx(ctx, func(tx contract.Tx) error { return b.notify(ctx, tx, in) }); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || calls[0].user != uid || calls[0].kind != "approval_pending" || calls[0].nid == 0 {
		t.Fatalf("훅 호출: %+v", calls)
	}
	// 훅이 실패해도 알림은 만들어지고 트랜잭션은 성공한다.
	hookErr = errors.New("발송 건 생성 실패")
	if err := b.Txs.WithTx(ctx, func(tx contract.Tx) error { return b.notify(ctx, tx, in) }); err != nil {
		t.Fatalf("훅 실패가 업무를 막음: %v", err)
	}
	var n int
	_ = db.QueryRow(`SELECT COUNT(*) FROM notifications WHERE user_id=?`, uid).Scan(&n)
	if n != 2 {
		t.Fatalf("앱 안 알림 %d건", n)
	}
	// 훅이 없으면 앱 안 알림만 만든다(기존 동작).
	b.Deliver = nil
	if err := b.Txs.WithTx(ctx, func(tx contract.Tx) error { return b.notify(ctx, tx, in) }); err != nil || len(calls) != 2 {
		t.Fatalf("훅 없음: %v %d", err, len(calls))
	}
}
