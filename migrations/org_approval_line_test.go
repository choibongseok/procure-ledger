package migrations_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/dbutil"
	"github.com/choibongseok/procure-ledger/migrations"
)

// 0011 은 기존 데이터가 있는 DB 에서도 통과하고, 진행 중 건의 결재 전 직책 단계는 지금 규칙의 후보를 배정자로 고정한다(AC-80·81·82).
func TestOrgApprovalLineMigrationKeepsLegacyData(t *testing.T) {
	db, err := dbutil.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	ctx := context.Background()
	if err := migrations.ApplyThrough(ctx, db, "0010_org_chart.sql"); err != nil {
		t.Fatal(err)
	}
	mustExec(t, db, `INSERT INTO departments (id, name) VALUES (1,'구매팀'),(2,'영업팀')`)
	mustExec(t, db, `INSERT INTO users (id,email,name,role,position,department_id) VALUES
		(1,'staff@x','직원','staff',NULL,1),(2,'lead@x','팀장','manager','team_lead',1),(3,'lead3@x','팀장2','manager','team_lead',1),
		(4,'head@x','부서장','manager','dept_head',1),(5,'fin@x','재무','finance',NULL,2),(6,'lead6@x','영업팀장','manager','team_lead',2),
		(7,'off@x','퇴사 팀장','manager','team_lead',1)`)
	mustExec(t, db, `UPDATE users SET active = 0 WHERE id = 7`)
	// PR-1: 진행 중(팀장 결재 차례), PR-2: 승인 완료, PR-3: 임시저장
	mustExec(t, db, `INSERT INTO purchase_requests (id,no,requester_id,department_id,title,status,round,created_at,updated_at) VALUES
		(1,'PR-1',1,1,'진행','in_review',1,?,?),(2,'PR-2',1,1,'완료','approved',1,?,?),(3,'PR-3',1,1,'초안','draft',1,?,?)`,
		tPast, tPast, tPast, tPast, tPast, tPast)
	mustExec(t, db, `INSERT INTO approval_steps (pr_id,round,seq,step_kind,status,approver_id) VALUES
		(1,1,1,'team_lead','pending',NULL),(1,1,2,'dept_head','waiting',NULL),(1,1,3,'finance','waiting',NULL),
		(2,1,1,'team_lead','approved',2),(2,1,2,'dept_head','approved',4)`)

	if err := migrations.Apply(ctx, db); err != nil {
		t.Fatal(err)
	}

	// 기존 행은 그대로(종류·상태·결재자).
	if n := count(t, db, `SELECT COUNT(*) FROM approval_steps`); n != 5 {
		t.Fatalf("결재 단계 행 %d, 기대 5", n)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM approval_steps WHERE pr_id = 2 AND status = 'approved' AND approver_id IS NOT NULL`); n != 2 {
		t.Fatalf("완료 건의 결재자 보존 %d", n)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM approval_line_rules WHERE form_id = 1`); n != 10 {
		t.Fatalf("규칙 행 %d, 기대 10", n)
	}
	// 진행 중 건: 1단계 후보 = 같은 부서·같은 직책의 재직 manager(요청자 제외, 퇴사자 제외), 2단계 = 부서장, 재무 단계는 행 없음.
	if got := ids(t, db, `SELECT user_id FROM approval_step_assignees WHERE pr_id = 1 AND seq = 1 ORDER BY user_id`); len(got) != 2 || got[0] != 2 || got[1] != 3 {
		t.Fatalf("1단계 배정자 %v, 기대 [2 3]", got)
	}
	if got := ids(t, db, `SELECT user_id FROM approval_step_assignees WHERE pr_id = 1 AND seq = 2`); len(got) != 1 || got[0] != 4 {
		t.Fatalf("2단계 배정자 %v, 기대 [4]", got)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM approval_step_assignees WHERE pr_id = 1 AND seq = 3`); n != 0 {
		t.Fatalf("재무 단계는 배정자 행이 없어야 한다: %d", n)
	}
	// 종료된 건은 스냅샷 행이 없고 귀속 조직은 부서 번호와 같다.
	if n := count(t, db, `SELECT COUNT(*) FROM approval_step_assignees WHERE pr_id IN (2,3)`); n != 0 {
		t.Fatalf("종료·임시저장 건의 배정자 행 %d", n)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM approval_lines`); n != 0 {
		t.Fatalf("스냅샷 행 %d", n)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM purchase_requests WHERE owner_unit_id = department_id`); n != 3 {
		t.Fatalf("귀속 조직 백필 %d, 기대 3", n)
	}
	// 단계의 현재 결재자 뷰는 고정된 후보를 그대로 돌려준다.
	if got := ids(t, db, `SELECT user_id FROM approval_step_current WHERE pr_id = 1 AND seq = 1 ORDER BY user_id`); len(got) != 2 {
		t.Fatalf("현재 결재자 뷰 %v", got)
	}
}

func TestOrgApprovalLineMigrationOnEmptyDB(t *testing.T) {
	db := setup(t)
	if n := count(t, db, `SELECT COUNT(*) FROM approval_line_rules WHERE form_id = 1`); n != 10 {
		t.Fatalf("빈 DB 의 규칙 시드 %d, 기대 10", n)
	}
	// 종류 CHECK 가 닫혀 있지 않아 새 직책 단계를 넣을 수 있다.
	mustExec(t, db, `INSERT INTO approval_line_rules (min_amount, max_amount, seq, step_kind) VALUES (990000000, NULL, 1, 'ceo')`)
}

func TestApprovalSnapshotAndReassignmentAreAppendOnly(t *testing.T) {
	db := setup(t)
	seed(t, db) // 1 직원, 2 팀장, 3 재무 (부서 1)
	mustExec(t, db, `INSERT INTO users (id,email,name,role,position,department_id) VALUES (4,'h@x','부서장','manager','dept_head',1),(5,'a@x','관리자','admin',NULL,1)`)
	mustExec(t, db, `INSERT INTO purchase_requests (id,no,requester_id,department_id,owner_unit_id,title,status,round,created_at,updated_at)
		VALUES (1,'PR-1',1,1,1,'t','in_review',1,?,?)`, tPast, tPast)
	mustExec(t, db, `INSERT INTO approval_steps (pr_id,round,seq,step_kind,status,role,unit_id,unit_path,title,acting)
		VALUES (1,1,1,'team_lead','pending','approve',11,'구매팀 > 구매1팀','팀장',0),(1,1,2,'division_head','waiting','approve',12,'구매·물류본부','본부장',0)`)

	// 스냅샷 행: 기록 삽입만 되고 수정·삭제는 거부. 결재 때 바뀌는 열(status, approver_id 등)은 허용.
	mustExec(t, db, `INSERT INTO approval_lines (pr_id,round,as_of_day,requester_unit_id,requester_unit_path,notes,created_at) VALUES (1,1,'2026-11-11',11,'구매팀 > 구매1팀','[]',?)`, tPast)
	wantErr(t, db, "수정할 수 없습니다", `UPDATE approval_lines SET requester_unit_path = 'x'`)
	wantErr(t, db, "삭제할 수 없습니다", `DELETE FROM approval_lines`)
	mustExec(t, db, `INSERT INTO approval_step_assignees (pr_id,round,seq,user_id,name_at) VALUES (1,1,1,2,'팀장')`)
	wantErr(t, db, "수정할 수 없습니다", `UPDATE approval_step_assignees SET user_id = 3`)
	wantErr(t, db, "삭제할 수 없습니다", `DELETE FROM approval_step_assignees`)
	wantErr(t, db, "요청자 본인은 결재자가 될 수 없습니다", `INSERT INTO approval_step_assignees (pr_id,round,seq,user_id,name_at) VALUES (1,1,2,1,'직원')`)
	wantErr(t, db, "스냅샷은 수정할 수 없습니다", `UPDATE approval_steps SET unit_path = '다른 조직' WHERE seq = 1`)
	wantErr(t, db, "스냅샷은 수정할 수 없습니다", `UPDATE approval_steps SET title = '부서장' WHERE seq = 1`)
	mustExec(t, db, `UPDATE approval_steps SET status = 'approved', approver_id = 2, decided_at = ? WHERE seq = 1`, tInside)

	// 재지정: 추가만 되고, 요청자는 새 결재자가 될 수 없고, 사유가 비면 거부, 뷰는 마지막 재지정을 따른다(AC-130).
	ins := `INSERT INTO approval_step_reassignments (pr_id,round,seq,from_user_id,from_name_at,to_user_id,to_name_at,cause,reason,actor_id,created_at) VALUES (1,1,2,?,?,?,?,?,?,5,?)`
	mustExec(t, db, `INSERT INTO approval_step_assignees (pr_id,round,seq,user_id,name_at) VALUES (1,1,2,4,'부서장')`)
	wantErr(t, db, "요청자 본인은 결재자가 될 수 없습니다", ins, 4, "부서장", 1, "직원", "leave", "사유", tPast)
	wantErr(t, db, "CHECK", ins, 4, "부서장", 3, "재무", "leave", "   ", tPast)
	wantErr(t, db, "CHECK", ins, 4, "부서장", 3, "재무", "vacation", "사유", tPast)
	mustExec(t, db, ins, 4, "부서장", 2, "팀장", "leave", "퇴사", tPast)
	mustExec(t, db, ins, 2, "팀장", 3, "재무", "other", "다시", tInside)
	if got := ids(t, db, `SELECT user_id FROM approval_step_current WHERE pr_id = 1 AND seq = 2`); len(got) != 1 || got[0] != 3 {
		t.Fatalf("현재 결재자 %v, 기대 [3](마지막 재지정)", got)
	}
	wantErr(t, db, "수정할 수 없습니다", `UPDATE approval_step_reassignments SET reason = 'x'`)
	wantErr(t, db, "삭제할 수 없습니다", `DELETE FROM approval_step_reassignments`)
	// 결재 이력에 결재자 변경 한 줄을 남길 수 있다(불변 트리거는 그대로).
	mustExec(t, db, `INSERT INTO approval_history (pr_id,round,seq,actor_id,action,comment,at) VALUES (1,1,2,5,'reassign','결재자 변경',?)`, tInside)
	wantErr(t, db, "수정할 수 없습니다", `UPDATE approval_history SET comment='x'`)
	wantErr(t, db, "삭제할 수 없습니다", `DELETE FROM approval_history`)
	wantErr(t, db, "본인이 올린 요청", `INSERT INTO approval_history (pr_id,round,seq,actor_id,action,at) VALUES (1,1,1,1,'approve',?)`, tInside)

	// 새 직책 단계 종류를 넣을 수 있고(CHECK 가 닫혀 있지 않다), 0007 의 DB 서열 트리거는 없다.
	mustExec(t, db, `INSERT INTO approval_steps (pr_id,round,seq,step_kind,status) VALUES (1,1,3,'ceo','waiting')`)
	if n := count(t, db, `SELECT COUNT(*) FROM sqlite_master WHERE type='trigger' AND name='trg_approval_delegated_requester_rank'`); n != 0 {
		t.Fatalf("요청자 서열 트리거가 남아 있다")
	}
}

func TestNotificationsOnlyReadAtChanges(t *testing.T) {
	db := setup(t)
	seed(t, db)
	mustExec(t, db, `INSERT INTO notifications (user_id,kind,title,body,link,created_at) VALUES (1,'approval.reassigned','제목','본문','/approvals/1',?)`, tPast)
	mustExec(t, db, `UPDATE notifications SET read_at = ? WHERE id = 1`, tInside)
	wantErr(t, db, "읽음 처리", `UPDATE notifications SET body = '바꿈'`)
	wantErr(t, db, "읽음 처리", `UPDATE notifications SET user_id = 2`)
	wantErr(t, db, "삭제할 수 없습니다", `DELETE FROM notifications`)
}

func count(t *testing.T, db *sql.DB, q string, args ...any) int {
	t.Helper()
	var n int
	if err := db.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func ids(t *testing.T, db *sql.DB, q string) []int {
	t.Helper()
	rows, err := db.Query(q)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []int
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			t.Fatal(err)
		}
		out = append(out, v)
	}
	return out
}
