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

// 0064(B42): 감사 경로 'api' 와 전표 출처 'api' 를 더하려고 audit_logs·journals 를 다시 만든다.
// 해시 체인·전표·줄·채무가 그대로이고, 외래키·트리거·색인이 살아 있으며, 새 값만 더 받아야 한다.
func TestAPITokensMigrationKeepsChainAndJournals(t *testing.T) {
	ctx := context.Background()
	db, err := dbutil.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := migrations.ApplyThrough(ctx, db, "0063_notification_termspace.sql"); err != nil {
		t.Fatal(err)
	}
	seed(t, db)
	mustExec(t, db, `INSERT INTO vendors (id, name) VALUES (1, '거래처')`)
	var a, b int64
	_ = db.QueryRow(`SELECT MIN(id) FROM accounts`).Scan(&a)
	_ = db.QueryRow(`SELECT MAX(id) FROM accounts`).Scan(&b)
	mustExec(t, db, `INSERT INTO journals (no, kind, status, posting_date, trade_date, created_by, created_at, total_amount) VALUES ('J-1','general','draft','2026-01-02','2026-01-02',1,'t',100), ('J-2','general','draft','2026-01-03','2026-01-03',1,'t',50)`)
	mustExec(t, db, `INSERT INTO journal_lines (journal_id, seq, account_id, side, amount, vendor_id) VALUES (1,1,?,'D',100,NULL), (1,2,?,'C',100,1)`, a, b)
	mustExec(t, db, `UPDATE journals SET status = 'requested' WHERE id = 1`)
	mustExec(t, db, `INSERT INTO payables (vendor_id, journal_id, line_id, trade_date, due_date, amount) VALUES (1, 1, 2, '2026-01-02', '2026-02-01', 100)`)
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
	for i := 0; i < 4; i++ {
		record(actor, "setting.before")
	}
	if _, err := db.Exec(`INSERT INTO journals (no, kind, status, posting_date, trade_date, source_type, created_by, created_at) VALUES ('J-9','general','draft','2026-01-04','2026-01-04','api',1,'t')`); err == nil {
		t.Fatal("0064 전에 source_type api 가 들어감")
	}
	if _, err := db.Exec(`INSERT INTO audit_logs (at, actor_id, actor_role, channel, action, target_type, target_id, result, prev_hash, hash) VALUES ('t',0,'system','api','a','setting','x','success','','h-old')`); err == nil {
		t.Fatal("0064 전에 api 경로가 들어감")
	}
	var headBefore string
	_ = db.QueryRow(`SELECT hash FROM audit_logs ORDER BY seq DESC LIMIT 1`).Scan(&headBefore)
	// 같은 SQLite 연결에서 전표 번호 시퀀스를 앞서 보낸다(번호 재사용이 없어야 한다).
	mustExec(t, db, `INSERT INTO journals (no, kind, status, posting_date, trade_date, created_by, created_at) VALUES ('J-3','general','draft','2026-01-05','2026-01-05',1,'t')`)
	mustExec(t, db, `UPDATE journals SET status = 'canceled' WHERE id = 3`)

	if err := migrations.Apply(ctx, db); err != nil {
		t.Fatal(err)
	}
	count := func(q string) int {
		t.Helper()
		var n int
		if err := db.QueryRow(q).Scan(&n); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		return n
	}
	// 체인.
	var headAfter string
	_ = db.QueryRow(`SELECT hash FROM audit_logs ORDER BY seq DESC LIMIT 1`).Scan(&headAfter)
	if headBefore != headAfter {
		t.Fatal("체인 머리가 바뀜")
	}
	if res, err := store.Verify(ctx); err != nil || !res.ChainOK || res.Checked != 4 {
		t.Fatalf("체인 검증: %+v %v", res, err)
	}
	record(contract.Principal{Role: "api", Channel: contract.ChannelAPI}, "api.read")
	if res, err := store.Verify(ctx); err != nil || !res.ChainOK || res.Checked != 5 {
		t.Fatalf("api 기록 뒤 체인 검증: %+v %v", res, err)
	}
	// 전표·줄·채무가 그대로이고 외래키가 이어져 있다.
	if count(`SELECT COUNT(*) FROM journals`) != 3 || count(`SELECT COUNT(*) FROM journal_lines`) != 2 || count(`SELECT COUNT(*) FROM payables WHERE journal_id = 1 AND line_id = 2`) != 1 {
		t.Fatal("전표·줄·채무가 바뀜")
	}
	if count(`SELECT COUNT(*) FROM journals WHERE id = 1 AND status = 'requested' AND total_amount = 100`) != 1 {
		t.Fatal("전표 내용이 바뀜")
	}
	if rows, err := db.Query(`PRAGMA foreign_key_check`); err != nil {
		t.Fatal(err)
	} else {
		bad := rows.Next()
		rows.Close()
		if bad {
			t.Fatal("외래키 위반")
		}
	}
	// 새 값은 받고 알 수 없는 값은 거부한다. 번호는 이어진다.
	mustExec(t, db, `INSERT INTO journals (no, kind, status, posting_date, trade_date, source_type, source_id, created_by, created_at) VALUES ('J-9','general','draft','2026-01-06','2026-01-06','api',7,1,'t')`)
	var id int64
	_ = db.QueryRow(`SELECT id FROM journals WHERE no = 'J-9'`).Scan(&id)
	if id < 4 {
		t.Fatalf("전표 번호 시퀀스가 이어지지 않음: %d", id)
	}
	if _, err := db.Exec(`INSERT INTO journals (no, kind, status, posting_date, trade_date, source_type, created_by, created_at) VALUES ('J-10','general','draft','2026-01-06','2026-01-06','zzz',1,'t')`); err == nil {
		t.Fatal("알 수 없는 출처가 들어감")
	}
	// 전표 트리거·색인과 줄 표의 트리거가 다시 만들어졌다.
	expectFail := func(q, want string) {
		t.Helper()
		_, err := db.Exec(q)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("%s: 오류 %v (기대 %q)", q, err, want)
		}
	}
	expectFail(`DELETE FROM journals WHERE id = 1`, "지울 수 없습니다")
	expectFail(`UPDATE journals SET memo = 'x' WHERE id = 3`, "수정할 수 없습니다")       // 취소된 전표는 고정
	expectFail(`UPDATE journals SET status = 'approved' WHERE id = 2`, "허용되지 않는") // draft → approved 는 자동 지급 전표만
	expectFail(`INSERT INTO journal_lines (journal_id, seq, account_id, side, amount) VALUES (1,3,1,'D',1)`, "고칠 수 없습니다")
	expectFail(`UPDATE audit_logs SET detail = 'x' WHERE seq = 1`, "추가 전용")
	for _, name := range []string{"trg_journals_balanced", "trg_journals_final", "trg_journals_month_open_ins", "trg_journals_month_open_upd", "trg_journals_no_delete", "trg_journals_transition",
		"trg_journal_lines_del", "trg_journal_lines_ins", "trg_journal_lines_upd", "trg_audit_no_update", "trg_audit_no_delete",
		"idx_journals_source", "idx_journals_reverses", "idx_journals_posting", "idx_journals_doc", "idx_audit_target", "idx_audit_actor", "idx_audit_action", "idx_audit_channel_at"} {
		if count(`SELECT COUNT(*) FROM sqlite_master WHERE name = '`+name+`'`) != 1 {
			t.Errorf("%s 가 없다", name)
		}
	}
	// 새 표가 있다.
	for _, tbl := range []string{"api_tokens", "api_rate_windows", "api_idempotency", "change_events"} {
		if count(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = '`+tbl+`'`) != 1 {
			t.Errorf("표 %s 가 없다", tbl)
		}
	}
	if _, err := db.Exec(`INSERT INTO api_tokens (name, token_prefix, token_hash, scopes, created_by, created_at, expires_at) VALUES ('t','pli_abcd','h1','[]',1,'t','t')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO api_tokens (name, token_prefix, token_hash, scopes, created_by, created_at, expires_at) VALUES ('t2','pli_abcd','h1','[]',1,'t','t')`); err == nil {
		t.Fatal("같은 해시의 토큰이 두 번 들어감")
	}
	if count(`SELECT COUNT(*) FROM sqlite_master WHERE name LIKE '%\_new' ESCAPE '\'`) != 0 {
		t.Error("임시 표가 남음")
	}
}
