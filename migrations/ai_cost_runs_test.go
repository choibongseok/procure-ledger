package migrations_test

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/dbutil"
	"github.com/choibongseok/procure-ledger/internal/dbutil/pgtest"
	"github.com/choibongseok/procure-ledger/migrations"
)

// B49 3단계 0098: 전표 표를 다시 만든다(journals.kind 에 auto_aicost, source_type 에 aicost_run). 기존 행·번호·시퀀스·색인·트리거·외래키가 그대로 살아 있고,
// 새 종류만 더 받으며, journal_lines 에 부서 열(NULL 허용)이 생긴다. SQLite 는 표를 새로 만드는 경로라 데이터 보존을 직접 확인한다.
func TestAICostRunsJournalRebuildKeepsRowsIndexesAndTriggersSQLite(t *testing.T) {
	ctx := context.Background()
	db, err := dbutil.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := migrations.ApplyThrough(ctx, db, "0097_ai_cost_rules.sql"); err != nil {
		t.Fatal(err)
	}
	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	mustExec(`INSERT INTO departments (id, name) VALUES (900, '운영')`)
	mustExec(`INSERT INTO users (id, email, name, role, department_id) VALUES (1, 'f@x', '재무', 'finance', 900)`)
	mustExec(`INSERT INTO vendors (name) VALUES ('한빛상사')`)
	var acct1, acct2 int64
	_ = db.QueryRow(`SELECT id FROM accounts WHERE code = '5290'`).Scan(&acct1)
	_ = db.QueryRow(`SELECT id FROM accounts WHERE code = '2120'`).Scan(&acct2)
	// 임시 전표 3건(하나는 줄·채무·증빙이 달린 승인 전표), 취소 전표 1건. 마지막 번호를 지워 시퀀스가 최대 id 보다 앞서 가게 한다.
	mustExec(`INSERT INTO journals (id, no, kind, status, posting_date, trade_date, memo, source_type, source_id, total_amount, created_by, created_at, entity_id, site_id) VALUES
		(1, 'JV-2026-00001', 'general', 'draft', '2026-10-05', '2026-10-05', '일반', '', 0, 1000, 1, 't', 1, 0),
		(2, 'JV-2026-00002', 'auto_expense', 'draft', '2026-10-06', '2026-10-06', '비용', 'approval_doc', 77, 2000, 1, 't', 1, 0),
		(3, 'JV-2026-00003', 'general', 'draft', '2026-10-07', '2026-10-07', '승인될 전표', '', 0, 3000, 1, 't', 1, 0),
		(4, 'JV-2026-00004', 'general', 'draft', '2026-10-08', '2026-10-08', '취소될 전표', '', 0, 4000, 1, 't', 1, 0)`)
	mustExec(`INSERT INTO journal_lines (journal_id, seq, account_id, side, amount, memo) VALUES (1,1,?, 'D', 1000, ''), (1,2,?, 'C', 1000, ''), (3,1,?, 'D', 3000, 'a'), (3,2,?, 'C', 3000, 'b')`, acct1, acct2, acct1, acct2)
	mustExec(`UPDATE journals SET status = 'requested' WHERE id = 3`)
	mustExec(`UPDATE journals SET status = 'approved', approved_by = 1, approved_at = 't' WHERE id = 3`)
	mustExec(`UPDATE journals SET status = 'canceled', canceled_by = 1, canceled_at = 't', cancel_reason = '시험' WHERE id = 4`)
	mustExec(`INSERT INTO journals (id, no, kind, status, posting_date, trade_date, source_type, source_id, total_amount, created_by, created_at, entity_id, site_id) VALUES (5, 'JV-2026-00005', 'general', 'draft', '2026-10-09', '2026-10-09', '', 0, 1, 1, 't', 1, 0)`)
	mustExec(`DELETE FROM journal_lines WHERE journal_id = 5`)
	mustExec(`UPDATE journals SET status = 'canceled', canceled_by = 1, canceled_at = 't', cancel_reason = 'x' WHERE id = 5`)
	var vendorID int64
	_ = db.QueryRow(`SELECT id FROM vendors LIMIT 1`).Scan(&vendorID)
	var lineID int64
	_ = db.QueryRow(`SELECT id FROM journal_lines WHERE journal_id = 3 AND seq = 2`).Scan(&lineID)
	mustExec(`INSERT INTO payables (vendor_id, journal_id, line_id, trade_date, due_date, amount, entity_id) VALUES (?, 3, ?, '2026-10-07', '2026-11-06', 3000, 1)`, vendorID, lineID)
	mustExec(`INSERT INTO journal_evidence (journal_id, kind, ref_id, created_at) VALUES (3, 'attachment', 9, 't')`)
	// 옛 제약에서는 새 종류가 들어가지 않는다.
	if _, err := db.Exec(`INSERT INTO journals (no, kind, status, posting_date, trade_date, source_type, source_id, created_by, created_at) VALUES ('X1', 'auto_aicost', 'draft', '2026-10-31', '2026-10-31', 'aicost_run', 1, 1, 't')`); err == nil {
		t.Fatal("0098 전에 auto_aicost 전표가 들어감")
	}

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
	// 행·번호·상태·승인 정보·법인 열이 그대로다.
	if count(`SELECT COUNT(*) FROM journals`) != 5 || count(`SELECT COUNT(*) FROM journal_lines`) != 4 || count(`SELECT COUNT(*) FROM payables`) != 1 || count(`SELECT COUNT(*) FROM journal_evidence`) != 1 {
		t.Fatal("행 수가 달라짐")
	}
	var no, status, kind, src string
	var by sql.NullInt64
	var entity, site int64
	if err := db.QueryRow(`SELECT no, status, kind, source_type, approved_by, entity_id, site_id FROM journals WHERE id = 3`).Scan(&no, &status, &kind, &src, &by, &entity, &site); err != nil ||
		no != "JV-2026-00003" || status != "approved" || kind != "general" || !by.Valid || by.Int64 != 1 || entity != 1 || site != 0 {
		t.Fatalf("전표 3: %s %s %s %v %v %d %d", no, status, kind, by, err, entity, site)
	}
	if count(`SELECT COUNT(*) FROM journals WHERE id = 2 AND kind = 'auto_expense' AND source_type = 'approval_doc' AND source_id = 77`) != 1 {
		t.Fatal("자동 비용 전표의 출처가 달라짐")
	}
	// 줄에는 부서 열이 생겼고 기존 줄은 NULL 이다.
	if count(`SELECT COUNT(*) FROM journal_lines WHERE department_id IS NULL`) != 4 {
		t.Fatal("기존 줄의 부서가 NULL 이 아니다")
	}
	// 시퀀스: 지운 마지막 번호 뒤로 이어진다(번호 재사용 없음).
	mustExec(`INSERT INTO journals (no, kind, status, posting_date, trade_date, source_type, source_id, created_by, created_at) VALUES ('JV-NEW', 'general', 'draft', '2026-10-20', '2026-10-20', '', 0, 1, 't')`)
	if count(`SELECT COUNT(*) FROM journals WHERE no = 'JV-NEW' AND id > 5`) != 1 {
		t.Fatal("시퀀스가 최대 id 뒤로 이어지지 않음")
	}
	// 색인: 자동 전표 출처 색인(취소 아닌 것만 유일)·역분개 색인이 살아 있고 새 종류가 포함된다.
	for _, idx := range []string{"idx_journals_source", "idx_journals_reverses", "idx_journals_posting", "idx_journals_doc", "idx_journals_entity_posting"} {
		if count(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = '`+idx+`'`) != 1 {
			t.Fatalf("색인 %s 가 없다", idx)
		}
	}
	var idxSQL string
	_ = db.QueryRow(`SELECT sql FROM sqlite_master WHERE name = 'idx_journals_source'`).Scan(&idxSQL)
	if !strings.Contains(idxSQL, "auto_aicost") {
		t.Fatalf("출처 색인에 새 종류가 없다: %s", idxSQL)
	}
	// 트리거: 7+3 개가 모두 있고 동작한다.
	for _, trg := range []string{"trg_journals_balanced", "trg_journals_entity_immutable", "trg_journals_final", "trg_journals_month_open_ins", "trg_journals_month_open_upd", "trg_journals_no_delete", "trg_journals_transition",
		"trg_journal_lines_del", "trg_journal_lines_ins", "trg_journal_lines_upd"} {
		if count(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'trigger' AND name = '`+trg+`'`) != 1 {
			t.Fatalf("트리거 %s 가 없다", trg)
		}
	}
	if _, err := db.Exec(`UPDATE journals SET memo = 'x' WHERE id = 3`); err == nil {
		t.Fatal("승인된 전표 수정이 막혀야 한다")
	}
	if _, err := db.Exec(`DELETE FROM journals WHERE id = 1`); err == nil {
		t.Fatal("전표 삭제가 막혀야 한다")
	}
	if _, err := db.Exec(`INSERT INTO journal_lines (journal_id, seq, account_id, side, amount) VALUES (3, 3, 1, 'D', 1)`); err == nil {
		t.Fatal("승인된 전표의 줄 추가가 막혀야 한다")
	}
	if _, err := db.Exec(`UPDATE journals SET status = 'approved' WHERE id = 1`); err == nil {
		t.Fatal("임시에서 바로 승인(자동 지급 전표가 아님)이 막혀야 한다")
	}
	// 외래키: 줄·채무·증빙이 새 표를 가리킨다(고아 없음). 줄이 있는 전표는 지워지지 않는다.
	var fkRows int
	rs, err := db.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	for rs.Next() {
		fkRows++
	}
	rs.Close()
	if fkRows != 0 {
		t.Fatalf("외래키 위반 %d건", fkRows)
	}
	// 새 종류: 자동 전표 출처는 취소가 아닌 한 하나뿐이고, 취소 뒤에는 다시 만들 수 있다. 부서 열이 들어간다.
	mustExec(`INSERT INTO journals (no, kind, status, posting_date, trade_date, source_type, source_id, total_amount, created_by, created_at) VALUES ('AIC-1', 'auto_aicost', 'draft', '2026-10-31', '2026-10-31', 'aicost_run', 1, 100, 1, 't')`)
	if _, err := db.Exec(`INSERT INTO journals (no, kind, status, posting_date, trade_date, source_type, source_id, total_amount, created_by, created_at) VALUES ('AIC-2', 'auto_aicost', 'draft', '2026-10-31', '2026-10-31', 'aicost_run', 1, 100, 1, 't')`); err == nil {
		t.Fatal("같은 실행의 전표가 둘 들어감")
	}
	mustExec(`INSERT INTO journal_lines (journal_id, seq, account_id, side, amount, department_id) VALUES ((SELECT id FROM journals WHERE no = 'AIC-1'), 1, ?, 'D', 100, 901)`, acct1)
	mustExec(`UPDATE journals SET status = 'canceled', canceled_by = 1, canceled_at = 't', cancel_reason = '다시' WHERE no = 'AIC-1'`)
	mustExec(`INSERT INTO journals (no, kind, status, posting_date, trade_date, source_type, source_id, total_amount, created_by, created_at) VALUES ('AIC-3', 'auto_aicost', 'draft', '2026-10-31', '2026-10-31', 'aicost_run', 1, 100, 1, 't')`)
	if _, err := db.Exec(`INSERT INTO journals (no, kind, status, posting_date, trade_date, source_type, source_id, created_by, created_at) VALUES ('BAD', 'zzz', 'draft', '2026-10-31', '2026-10-31', '', 0, 1, 't')`); err == nil {
		t.Fatal("알 수 없는 종류가 들어감")
	}
}

// PG: 표를 다시 만들지 않고 CHECK 두 곳과 색인만 바꾼다. 기준 마이그레이션을 적용한 PG 에서 새 종류·출처·부서 열·색인·기존 트리거가 SQLite 와 같이 동작한다(make verify-pg 에서만 돈다).
func TestAICostRunsJournalConstraintsPG(t *testing.T) {
	pgtest.Require(t)
	db := applyBaseline(t)
	exec := func(q string, args ...any) error {
		_, err := db.Exec(q, args...)
		return err
	}
	must := func(q string, args ...any) {
		t.Helper()
		if err := exec(q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	must(`INSERT INTO departments (id, name) VALUES (900, '운영') ON CONFLICT DO NOTHING`)
	must(`INSERT INTO users (id, email, name, role, department_id) VALUES (1, 'f@x', '재무', 'finance', 900) ON CONFLICT DO NOTHING`)
	var acct int64
	if err := db.QueryRow(`SELECT id FROM accounts WHERE code = '5290'`).Scan(&acct); err != nil {
		t.Fatal(err)
	}
	ins := func(no, kind, src string, srcID int) error {
		return exec(`INSERT INTO journals (no, kind, status, posting_date, trade_date, source_type, source_id, total_amount, created_by, created_at) VALUES (?, ?, 'draft', '2026-10-31', '2026-10-31', ?, ?, 100, 1, 't')`, no, kind, src, srcID)
	}
	if err := ins("AIC-1", "auto_aicost", "aicost_run", 1); err != nil {
		t.Fatalf("새 종류·출처: %v", err)
	}
	if err := ins("AIC-2", "auto_aicost", "aicost_run", 1); err == nil {
		t.Fatal("같은 실행의 전표가 둘 들어감")
	}
	if err := ins("BAD", "zzz", "", 0); err == nil {
		t.Fatal("알 수 없는 종류가 들어감")
	}
	if err := ins("BAD2", "general", "zzz", 0); err == nil {
		t.Fatal("알 수 없는 출처가 들어감")
	}
	must(`INSERT INTO journal_lines (journal_id, seq, account_id, side, amount, department_id) VALUES ((SELECT id FROM journals WHERE no = 'AIC-1'), 1, ?, 'D', 100, 901)`, acct)
	must(`INSERT INTO journal_lines (journal_id, seq, account_id, side, amount) VALUES ((SELECT id FROM journals WHERE no = 'AIC-1'), 2, ?, 'C', 100)`, acct)
	var nullDept int
	if err := db.QueryRow(`SELECT COUNT(*) FROM journal_lines WHERE department_id IS NULL`).Scan(&nullDept); err != nil || nullDept != 1 {
		t.Fatalf("부서 열은 NULL 허용: %d %v", nullDept, err)
	}
	must(`UPDATE journals SET status = 'canceled', canceled_by = 1, canceled_at = 't', cancel_reason = '다시' WHERE no = 'AIC-1'`)
	if err := ins("AIC-3", "auto_aicost", "aicost_run", 1); err != nil {
		t.Fatalf("취소 뒤 다시 만들기: %v", err)
	}
	if err := exec(`UPDATE journals SET memo = 'x' WHERE no = 'AIC-1'`); err == nil {
		t.Fatal("취소된 전표 수정이 막혀야 한다")
	}
	if err := exec(`DELETE FROM journals WHERE no = 'AIC-3'`); err == nil {
		t.Fatal("전표 삭제가 막혀야 한다")
	}
	var idx string
	if err := db.QueryRow(`SELECT indexdef FROM pg_indexes WHERE indexname = 'idx_journals_source'`).Scan(&idx); err != nil || !strings.Contains(idx, "auto_aicost") {
		t.Fatalf("출처 색인: %s %v", idx, err)
	}
}
