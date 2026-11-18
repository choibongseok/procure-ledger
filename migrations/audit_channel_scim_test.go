package migrations_test

import (
	"context"
	"strings"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/audit"
	"github.com/choibongseok/procure-ledger/internal/contract"
	caudit "github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/dbutil"
	"github.com/choibongseok/procure-ledger/migrations"
)

// 0058(B40 D2): 감사 기록 경로에 'scim' 을 더하려고 audit_logs 를 다시 만든다(0053 'cli' 와 같은 방식).
// 기존 행·seq·해시가 그대로여서 체인 검증이 통과하고, 추가 전용 트리거와 색인이 살아 있으며, 이후에도 체인이 이어져야 한다(AC-4021).
func TestAuditChannelSCIMRebuildKeepsChain(t *testing.T) {
	ctx := context.Background()
	db, err := dbutil.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := migrations.ApplyThrough(ctx, db, "0057_scim.sql"); err != nil {
		t.Fatal(err)
	}
	store := audit.New(db)
	txs := dbutil.NewTxManager(db, dbutil.DialectOf(db))
	actor := contract.Principal{UserID: 7, Role: contract.RoleStaff, Channel: contract.ChannelWeb}
	record := func(a contract.Principal, action string) {
		t.Helper()
		if err := txs.WithTx(ctx, func(tx contract.Tx) error {
			return store.Record(ctx, tx, caudit.Entry{Actor: a, Action: action, TargetType: caudit.TargetSetting, TargetID: "x", Result: caudit.ResultSuccess})
		}); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 5; i++ {
		record(actor, "setting.before")
	}
	// 옛 제약에서는 scim 이 들어가지 않는다.
	if _, err := db.Exec(`INSERT INTO audit_logs (at, actor_id, actor_role, channel, action, target_type, target_id, result, prev_hash, hash) VALUES ('t',0,'system','scim','a','setting','x','success','','h-old')`); err == nil {
		t.Fatal("0058 전에 scim 경로가 들어감")
	}
	var before, headBefore string
	_ = db.QueryRow(`SELECT COUNT(*) FROM audit_logs`).Scan(&before)
	_ = db.QueryRow(`SELECT hash FROM audit_logs ORDER BY seq DESC LIMIT 1`).Scan(&headBefore)

	if err := migrations.Apply(ctx, db); err != nil {
		t.Fatal(err)
	}
	var after, headAfter string
	_ = db.QueryRow(`SELECT COUNT(*) FROM audit_logs`).Scan(&after)
	_ = db.QueryRow(`SELECT hash FROM audit_logs ORDER BY seq DESC LIMIT 1`).Scan(&headAfter)
	if before != after || headBefore != headAfter {
		t.Fatalf("행 수·체인 머리가 바뀜: %s→%s, %s→%s", before, after, headBefore, headAfter)
	}
	res, err := store.Verify(ctx)
	if err != nil || !res.ChainOK || res.Checked != 5 {
		t.Fatalf("이관 직후 체인 검증: %+v %v", res, err)
	}

	// 이어서 web 과 scim 기록이 같은 체인에 붙는다.
	record(actor, "setting.after")
	scimActor := contract.Principal{Role: "scim", Channel: contract.ChannelSCIM}
	record(scimActor, "setting.scim")
	res, err = store.Verify(ctx)
	if err != nil || !res.ChainOK || res.Checked != 7 {
		t.Fatalf("scim 기록 뒤 체인 검증: %+v %v", res, err)
	}
	var ch string
	if err := db.QueryRow(`SELECT channel FROM audit_logs ORDER BY seq DESC LIMIT 1`).Scan(&ch); err != nil || ch != "scim" {
		t.Fatalf("마지막 행의 경로 %q %v", ch, err)
	}
	// 알 수 없는 경로는 여전히 거부된다.
	if _, err := db.Exec(`INSERT INTO audit_logs (at, actor_id, actor_role, channel, action, target_type, target_id, result, prev_hash, hash) VALUES ('t',0,'system','ssh','a','setting','x','success','','h-bad')`); err == nil {
		t.Fatal("알 수 없는 경로가 들어감")
	}
	// 추가 전용 트리거와 색인이 다시 만들어졌다.
	if _, err := db.Exec(`UPDATE audit_logs SET detail = 'x' WHERE seq = 1`); err == nil || !strings.Contains(err.Error(), "추가 전용") {
		t.Fatalf("수정이 막히지 않음: %v", err)
	}
	if _, err := db.Exec(`DELETE FROM audit_logs WHERE seq = 1`); err == nil || !strings.Contains(err.Error(), "추가 전용") {
		t.Fatalf("삭제가 막히지 않음: %v", err)
	}
	for _, idx := range []string{"idx_audit_target", "idx_audit_actor", "idx_audit_action", "idx_audit_channel_at"} {
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name=?`, idx).Scan(&n); err != nil || n != 1 {
			t.Errorf("색인 %s 가 없음", idx)
		}
	}
	// 해시 한 곳을 바꾸면(트리거를 우회해) 검증이 그 지점을 짚는다 — 체인이 새 표에서도 실제로 검증되는지 본다.
	if _, err := db.Exec(`DROP TRIGGER trg_audit_no_update`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE audit_logs SET detail = 'tampered' WHERE seq = 3`); err != nil {
		t.Fatal(err)
	}
	res, err = store.Verify(ctx)
	if err != nil || res.ChainOK || res.BrokenSeq != 3 {
		t.Fatalf("변조를 짚지 못함: %+v %v", res, err)
	}
}
