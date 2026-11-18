package migrations_test

import (
	"context"
	"strings"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/dbutil"
	"github.com/choibongseok/procure-ledger/migrations"
)

// 0012: 결재 단계 삭제와 상신된 건의 귀속 조직 변경은 DB 가 막고, 임시저장·반려 건의 귀속 조직 갱신(상신·재상신 경로)은 통과한다.
func TestSnapshotGuards(t *testing.T) {
	db, err := dbutil.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := migrations.Apply(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	mustExec(t, db, `INSERT INTO departments (id, name) VALUES (1,'구매팀'),(2,'영업팀')`)
	mustExec(t, db, `INSERT INTO users (id,email,name,role,department_id) VALUES (1,'s@x','직원','staff',1)`)
	mustExec(t, db, `INSERT INTO purchase_requests (id,no,requester_id,department_id,owner_unit_id,title,status,round,created_at,updated_at) VALUES
		(1,'PR-1',1,1,1,'초안','draft',1,?,?),(2,'PR-2',1,1,1,'검토','in_review',1,?,?),(3,'PR-3',1,1,1,'반려','rejected',1,?,?),
		(4,'PR-4',1,1,NULL,'옛 건','approved',1,?,?)`, tPast, tPast, tPast, tPast, tPast, tPast, tPast, tPast)
	mustExec(t, db, `INSERT INTO approval_steps (pr_id,round,seq,step_kind,status) VALUES (2,1,1,'team_lead','pending')`)

	// 정상 경로: 임시저장·반려 건의 갱신, 처음 값이 비어 있던 건 채우기, 같은 값 쓰기.
	mustExec(t, db, `UPDATE purchase_requests SET owner_unit_id = 2 WHERE id = 1`)
	mustExec(t, db, `UPDATE purchase_requests SET owner_unit_id = 2 WHERE id = 3`)
	mustExec(t, db, `UPDATE purchase_requests SET owner_unit_id = 1 WHERE id = 4`)
	mustExec(t, db, `UPDATE purchase_requests SET owner_unit_id = 1, status = 'approved' WHERE id = 2`)

	// 상신된 건: 귀속 조직 변경은 거부, 같은 값으로 쓰기와 다른 열 변경은 통과.
	_, err = db.Exec(`UPDATE purchase_requests SET owner_unit_id = 2 WHERE id = 2`)
	if err == nil || !strings.Contains(err.Error(), "귀속 조직은 바꿀 수 없습니다") {
		t.Fatalf("상신된 건의 귀속 조직 변경이 막히지 않음: %v", err)
	}
	if _, err = db.Exec(`UPDATE purchase_requests SET owner_unit_id = 2 WHERE id = 4`); err == nil {
		t.Fatal("값이 있는 승인 건의 귀속 조직 변경이 막히지 않음")
	}
	mustExec(t, db, `UPDATE purchase_requests SET title = '수정' WHERE id = 2`)

	// 결재 단계 삭제는 거부된다(상태 갱신은 그대로 된다).
	if _, err = db.Exec(`DELETE FROM approval_steps WHERE pr_id = 2`); err == nil || !strings.Contains(err.Error(), "삭제할 수 없습니다") {
		t.Fatalf("결재 단계 삭제가 막히지 않음: %v", err)
	}
	mustExec(t, db, `UPDATE approval_steps SET status = 'approved', approver_id = 1 WHERE pr_id = 2`)
	if n := count(t, db, `SELECT COUNT(*) FROM approval_steps`); n != 1 {
		t.Fatalf("결재 단계 %d건", n)
	}
}
