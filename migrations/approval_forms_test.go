package migrations_test

import (
	"context"
	"strings"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/dbutil"
	"github.com/choibongseok/procure-ledger/migrations"
)

// AC-100 0013 은 기존 구매요청(임시저장·진행 중·완료)이 있는 DB 에 통과하고, 모든 기존 건이 구매 품의서 버전 1 에 묶이며 amount 가 합계 금액이다.
// 진행 중 건의 결재 단계·배정자는 그대로다.
func TestApprovalFormsMigrationBackfillsExistingRequests(t *testing.T) {
	db, err := dbutil.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	ctx := context.Background()
	if err := migrations.ApplyThrough(ctx, db, "0012_snapshot_guards.sql"); err != nil {
		t.Fatal(err)
	}
	mustExec(t, db, `INSERT INTO departments (id, name) VALUES (1,'구매팀')`)
	mustExec(t, db, `INSERT INTO users (id,email,name,role,department_id) VALUES (1,'s@x','직원','staff',1),(2,'m@x','팀장','manager',1)`)
	mustExec(t, db, `INSERT INTO purchase_requests (id,no,requester_id,department_id,title,status,total_amount,round,created_at,updated_at) VALUES
		(1,'PR-1',1,1,'초안','draft',100,1,?,?),(2,'PR-2',1,1,'검토','in_review',2000,1,?,?),(3,'PR-3',1,1,'완료','approved',30000,1,?,?)`,
		tPast, tPast, tPast, tPast, tPast, tPast)
	mustExec(t, db, `INSERT INTO approval_steps (pr_id,round,seq,step_kind,status) VALUES (2,1,1,'team_lead','pending')`)
	mustExec(t, db, `INSERT INTO approval_step_assignees (pr_id,round,seq,user_id,name_at) VALUES (2,1,1,2,'팀장')`)

	if err := migrations.Apply(ctx, db); err != nil {
		t.Fatal(err)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM purchase_requests WHERE form_id = 1 AND form_version_id IS NOT NULL AND amount = total_amount`); n != 3 {
		t.Fatalf("기존 건 %d/3 이 구매 품의서에 묶임", n)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM approval_steps s JOIN approval_step_current c ON c.pr_id = s.pr_id AND c.seq = s.seq WHERE s.pr_id = 2 AND s.status = 'pending' AND c.user_id = 2`); n != 1 {
		t.Fatal("진행 중 건의 결재 단계가 바뀜")
	}
	// 기본 양식 4종과 게시된 버전 1.
	if n := count(t, db, `SELECT COUNT(*) FROM approval_forms f JOIN approval_form_versions v ON v.form_id = f.id AND v.status = 'published' WHERE f.code IN ('PUR','GEN','EXP','COOP')`); n != 4 {
		t.Fatalf("기본 양식 %d/4", n)
	}
	// 새로 들어오는 행(표 밖 INSERT)도 구매 품의서로 채워지고 합계가 바뀌면 amount 가 따라간다.
	mustExec(t, db, `INSERT INTO purchase_requests (id,no,requester_id,department_id,title,status,total_amount,round,created_at,updated_at) VALUES (4,'PR-4',1,1,'새 건','draft',500,1,?,?)`, tPast, tPast)
	mustExec(t, db, `UPDATE purchase_requests SET total_amount = 900 WHERE id = 4`)
	if n := count(t, db, `SELECT COUNT(*) FROM purchase_requests WHERE id = 4 AND form_id = 1 AND amount = 900`); n != 1 {
		t.Fatal("표 밖 INSERT 의 양식 열 기본값 또는 amount 동기화가 없음")
	}
}

// AC-112(DB) 게시된 양식 버전은 내용 수정·삭제가 거부되고, 상태는 draft → published → retired 로만 간다. 초안은 고치고 지울 수 있다.
func TestPublishedFormVersionIsImmutable(t *testing.T) {
	db := setup(t)
	if err := migrations.Apply(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE approval_form_versions SET definition = '{}' WHERE form_id = 2 AND version = 1`); err == nil || !strings.Contains(err.Error(), "수정할 수 없습니다") {
		t.Fatalf("게시된 버전 수정이 막히지 않음: %v", err)
	}
	if _, err := db.Exec(`DELETE FROM approval_form_versions WHERE form_id = 2 AND version = 1`); err == nil || !strings.Contains(err.Error(), "삭제할 수 없습니다") {
		t.Fatalf("게시된 버전 삭제가 막히지 않음: %v", err)
	}
	mustExec(t, db, `INSERT INTO approval_form_versions (form_id, version, status, definition, created_at) VALUES (2, 2, 'draft', '{}', 'x')`)
	mustExec(t, db, `UPDATE approval_form_versions SET definition = '{"a":1}' WHERE form_id = 2 AND version = 2`)
	mustExec(t, db, `UPDATE approval_form_versions SET status = 'published', published_at = 'y' WHERE form_id = 2 AND version = 2`)
	if _, err := db.Exec(`UPDATE approval_form_versions SET status = 'draft' WHERE form_id = 2 AND version = 2`); err == nil || !strings.Contains(err.Error(), "되돌릴 수 없습니다") {
		t.Fatalf("상태 되돌리기가 막히지 않음: %v", err)
	}
	mustExec(t, db, `UPDATE approval_form_versions SET status = 'retired' WHERE form_id = 2 AND version = 1`)
	mustExec(t, db, `INSERT INTO approval_form_versions (form_id, version, status, definition, created_at) VALUES (4, 2, 'draft', '{}', 'x')`)
	mustExec(t, db, `DELETE FROM approval_form_versions WHERE form_id = 4 AND version = 2`)
}
