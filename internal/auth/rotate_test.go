package auth

import (
	"bytes"
	"context"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/dbtest"
	"github.com/choibongseok/procure-ledger/internal/dbutil"
	"github.com/choibongseok/procure-ledger/migrations"
)

// AC-1850: rotate-key auth 뒤 기존 TOTP 비밀키가 새 키로 풀리고(재등록 없이 검증), 옛 키로는 풀리지 않는다. 옛 키가 틀리면 아무것도 바뀌지 않는다.
func TestReencryptTOTP(t *testing.T) {
	ctx := context.Background()
	db := dbtest.Open(t)
	if !dbtest.PG() {
		if err := migrations.ApplyDialect(ctx, db, dbutil.DialectOf(db)); err != nil {
			t.Fatal(err)
		}
	}
	dbtest.FKOff(t, db)
	oldK, newK := bytes.Repeat([]byte{1}, 32), bytes.Repeat([]byte{2}, 32)
	oldBox, _ := newSecretBox(oldK)
	newBox, _ := newSecretBox(newK)
	for _, id := range []int64{1, 2, 3} {
		active, err := oldBox.seal([]byte("JBSWY3DPEHPK3PXP"), idStr(id))
		if err != nil {
			t.Fatal(err)
		}
		var pending any
		if id == 3 { // 등록 중인 비밀키도 함께 다시 봉한다
			p, _ := oldBox.seal([]byte("PENDINGSECRET234"), idStr(id))
			pending = p
		}
		if _, err := db.Exec(`INSERT INTO user_totp (user_id, secret_enc, pending_secret_enc, confirmed_at, created_at) VALUES (?, ?, ?, '2026-11-01T00:00:00Z', '2026-11-01T00:00:00Z')`, id, active, pending); err != nil {
			t.Fatal(err)
		}
	}
	// 틀린 옛 키: 전체 되돌림.
	tx, _ := db.Begin()
	if _, err := ReencryptTOTP(ctx, tx, bytes.Repeat([]byte{9}, 32), newK); err == nil {
		t.Fatal("틀린 옛 키는 오류여야 함")
	}
	_ = tx.Rollback()
	if tx2, _ := db.Begin(); true {
		ok, n, err := CanOpenTOTP(ctx, tx2, oldK)
		_ = tx2.Rollback()
		if err != nil || !ok || n != 3 {
			t.Fatalf("실패한 교체가 값을 바꿈: %v %v %d", ok, n, err)
		}
	}
	// 정상 교체.
	tx, _ = db.Begin()
	n, err := ReencryptTOTP(ctx, tx, oldK, newK)
	if err != nil || n != 4 {
		t.Fatalf("교체: %d %v", n, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var enc, pend string
	_ = db.QueryRow(`SELECT secret_enc, COALESCE(pending_secret_enc, '') FROM user_totp WHERE user_id = 3`).Scan(&enc, &pend)
	if got, err := newBox.open(enc, "3"); err != nil || string(got) != "JBSWY3DPEHPK3PXP" {
		t.Fatalf("새 키로 풀기: %q %v", got, err)
	}
	if got, err := newBox.open(pend, "3"); err != nil || string(got) != "PENDINGSECRET234" {
		t.Fatalf("등록 중 비밀키: %q %v", got, err)
	}
	if _, err := oldBox.open(enc, "3"); err == nil {
		t.Fatal("옛 키로도 풀림")
	}
	// 다른 사용자 행에 붙여도 풀리지 않는다(aad 가 사용자 번호).
	if _, err := newBox.open(enc, "2"); err == nil {
		t.Fatal("aad 가 사용자에 묶여 있지 않음")
	}
	tx, _ = db.Begin()
	ok, _, err := CanOpenTOTP(ctx, tx, newK)
	_ = tx.Rollback()
	if err != nil || !ok {
		t.Fatalf("표본 점검: %v %v", ok, err)
	}
}
