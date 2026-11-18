package migrations_test

import (
	"database/sql"
	"fmt"
	"os"
	"reflect"
	"sort"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/dbutil"
)

// 0006 은 approval_history 를 다시 만든다. 재생성 전후로 행이 그대로이고 트리거·인덱스가 전부 살아 있어야 한다.
func TestApprovalHistoryRebuildKeepsRowsAndTriggers(t *testing.T) {
	db, err := dbutil.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	run := func(name string) {
		t.Helper()
		b, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(string(b)); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	for _, n := range []string{"0001_init.sql", "0002_delegation_settings.sql", "0003_eval_runs.sql", "0004_eval_runs_append_only.sql", "0005_audit_anchor_stamps.sql"} {
		run(n)
	}
	seed(t, db)
	mustExec(t, db, `INSERT INTO purchase_requests (id,no,requester_id,department_id,title,status,created_at,updated_at)
		VALUES (1,'PR-1',1,1,'t','in_review',?,?)`, tPast, tPast)
	mustExec(t, db, `INSERT INTO approval_delegations (id,delegator_id,delegate_id,start_at,end_at,created_at) VALUES (1,2,3,?,?,?)`, tStart, tEnd, tPast)
	ins := `INSERT INTO approval_history (pr_id,round,seq,actor_id,action,comment,at,on_behalf_of) VALUES (1,?,?,?,?,?,?,?)`
	mustExec(t, db, ins, 1, 0, 1, "submit", "상신", tPast, nil)
	mustExec(t, db, ins, 1, 1, 3, "approve", "대리 승인", tInside, 2)
	mustExec(t, db, ins, 1, 2, 2, "reject", "반려", tInside, nil)
	mustExec(t, db, ins, 2, 0, 1, "resubmit", "재상신", tEnd, nil)

	dump := func() [][]string {
		rows, err := db.Query(`SELECT id,pr_id,round,seq,actor_id,action,comment,at,COALESCE(on_behalf_of,0) FROM approval_history ORDER BY id`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out [][]string
		for rows.Next() {
			v := make([]any, 9)
			p := make([]any, 9)
			for i := range v {
				p[i] = &v[i]
			}
			if err := rows.Scan(p...); err != nil {
				t.Fatal(err)
			}
			line := make([]string, 9)
			for i := range v {
				line[i] = fmt.Sprint(v[i])
			}
			out = append(out, line)
		}
		return out
	}
	names := func(typ string) []string {
		rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type=? AND tbl_name='approval_history' AND name NOT LIKE 'sqlite_%' ORDER BY name`, typ)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []string
		for rows.Next() {
			var n string
			_ = rows.Scan(&n)
			out = append(out, n)
		}
		sort.Strings(out)
		return out
	}
	before, trigBefore, idxBefore := dump(), names("trigger"), names("index")
	if len(before) != 4 || len(trigBefore) != 4 {
		t.Fatalf("준비 상태 이상: 행 %d 트리거 %v", len(before), trigBefore)
	}

	run("0006_approval_history_withdraw.sql")

	if after := dump(); !reflect.DeepEqual(before, after) {
		t.Fatalf("재생성 뒤 행이 달라짐\n전 %v\n후 %v", before, after)
	}
	wantTrig := append(append([]string{}, trigBefore...), "trg_approval_withdraw_by_requester")
	sort.Strings(wantTrig)
	if got := names("trigger"); !reflect.DeepEqual(got, wantTrig) {
		t.Fatalf("트리거 목록 %v, 기대 %v", got, wantTrig)
	}
	if got := names("index"); !reflect.DeepEqual(got, idxBefore) {
		t.Fatalf("인덱스 목록 %v, 기대 %v", got, idxBefore)
	}
	// 재생성한 테이블에서도 기존 규칙이 그대로 막는다.
	wantErr(t, db, "수정할 수 없습니다", `UPDATE approval_history SET comment='x'`)
	wantErr(t, db, "삭제할 수 없습니다", `DELETE FROM approval_history`)
	wantErr(t, db, "본인이 올린 요청", `INSERT INTO approval_history (pr_id,round,seq,actor_id,action,at) VALUES (1,2,1,1,'approve','x')`)
	wantErr(t, db, "유효한 위임이 없습니다", `INSERT INTO approval_history (pr_id,round,seq,actor_id,action,at,on_behalf_of) VALUES (1,2,1,3,'approve',?,2)`, tPast)
	wantErr(t, db, "승인·반려에만", `INSERT INTO approval_history (pr_id,round,seq,actor_id,action,at,on_behalf_of) VALUES (1,2,1,3,'submit',?,2)`, tInside)
	// 새 기능: 요청자 본인의 회수 행은 들어가고, 남의 회수와 알 수 없는 동작은 막힌다.
	wantErr(t, db, "회수는 요청자 본인만", `INSERT INTO approval_history (pr_id,round,seq,actor_id,action,at) VALUES (1,2,0,2,'withdraw',?)`, tEnd)
	wantErr(t, db, "CHECK", `INSERT INTO approval_history (pr_id,round,seq,actor_id,action,at) VALUES (1,2,0,1,'cancel',?)`, tEnd)
	mustExec(t, db, `INSERT INTO approval_history (pr_id,round,seq,actor_id,action,comment,at) VALUES (1,2,0,1,'withdraw','회수',?)`, tEnd)
	assertCount(t, db, 5)
}

func assertCount(t *testing.T, db *sql.DB, want int) {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM approval_history`).Scan(&n); err != nil || n != want {
		t.Fatalf("이력 행 %d, 기대 %d (%v)", n, want, err)
	}
}
