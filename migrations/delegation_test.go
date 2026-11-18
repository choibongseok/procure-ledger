package migrations_test

import "testing"

const (
	tPast   = "2026-01-01T00:00:00Z"
	tStart  = "2026-02-01T00:00:00Z"
	tInside = "2026-02-05T00:00:00Z"
	tEnd    = "2026-02-10T00:00:00Z"
)

func TestDelegationConstraints(t *testing.T) {
	db := setup(t)
	seed(t, db)
	wantErr(t, db, "CHECK", `INSERT INTO approval_delegations (delegator_id,delegate_id,start_at,end_at,created_at) VALUES (2,2,?,?,?)`, tStart, tEnd, tPast)
	wantErr(t, db, "CHECK", `INSERT INTO approval_delegations (delegator_id,delegate_id,start_at,end_at,created_at) VALUES (2,3,?,?,?)`, tEnd, tStart, tPast)
	mustExec(t, db, `INSERT INTO approval_delegations (id,delegator_id,delegate_id,start_at,end_at,created_at) VALUES (1,2,3,?,?,?)`, tStart, tEnd, tPast)
	wantErr(t, db, "삭제할 수 없습니다", `DELETE FROM approval_delegations`)
	wantErr(t, db, "수정할 수 없고", `UPDATE approval_delegations SET end_at = '2027-01-01T00:00:00Z'`)
	mustExec(t, db, `UPDATE approval_delegations SET revoked_at = ? WHERE id = 1`, tInside)
	wantErr(t, db, "수정할 수 없고", `UPDATE approval_delegations SET revoked_at = ? WHERE id = 1`, tEnd)
}

func TestDelegatedApprovalRules(t *testing.T) {
	db := setup(t)
	seed(t, db)
	mustExec(t, db, `INSERT INTO purchase_requests (id,no,requester_id,department_id,title,status,created_at,updated_at)
		VALUES (1,'PR-1',1,1,'t','in_review',?,?)`, tPast, tPast)
	// 요청자(1)가 위임자인 건: 2 는 팀장, 1 → 3 위임은 자기 결재 우회.
	mustExec(t, db, `INSERT INTO approval_delegations (id,delegator_id,delegate_id,start_at,end_at,created_at) VALUES (1,2,3,?,?,?),(2,1,3,?,?,?)`,
		tStart, tEnd, tPast, tStart, tEnd, tPast)

	ins := `INSERT INTO approval_history (pr_id,round,seq,actor_id,action,at,on_behalf_of) VALUES (1,1,1,?,?,?,?)`
	// 위임 기간 밖
	wantErr(t, db, "유효한 위임이 없습니다", ins, 3, "approve", tPast, 2)
	wantErr(t, db, "유효한 위임이 없습니다", ins, 3, "approve", tEnd, 2)
	// 위임이 없는 사람이 대신 결재
	wantErr(t, db, "유효한 위임이 없습니다", ins, 1, "approve", tInside, 2)
	// 수임자가 요청자: 자기 결재 금지는 위임에도 적용(요청자 1 이 위임자 2 를 대신할 수 없다)
	mustExec(t, db, `INSERT INTO approval_delegations (id,delegator_id,delegate_id,start_at,end_at,created_at) VALUES (3,2,1,?,?,?)`, tStart, tEnd, tPast)
	wantErr(t, db, "본인이 올린 요청", ins, 1, "approve", tInside, 2)
	// 본인을 대신
	wantErr(t, db, "본인을 대신", ins, 3, "approve", tInside, 3)
	// 승인·반려만 가능
	wantErr(t, db, "승인·반려에만", ins, 3, "submit", tInside, 2)
	// 정상: 기간 안
	mustExec(t, db, ins, 3, "approve", tInside, 2)
	var by, on int64
	if err := db.QueryRow(`SELECT actor_id, on_behalf_of FROM approval_history WHERE on_behalf_of IS NOT NULL`).Scan(&by, &on); err != nil || by != 3 || on != 2 {
		t.Fatalf("누가 누구를 대신해 결재했는지 기록: %d %d %v", by, on, err)
	}
	// 불변 트리거는 그대로
	wantErr(t, db, "수정할 수 없습니다", `UPDATE approval_history SET on_behalf_of = NULL`)
}

func TestDelegationRevokedBeforeDecisionBlocks(t *testing.T) {
	db := setup(t)
	seed(t, db)
	mustExec(t, db, `INSERT INTO purchase_requests (id,no,requester_id,department_id,title,status,created_at,updated_at)
		VALUES (1,'PR-1',1,1,'t','in_review',?,?)`, tPast, tPast)
	mustExec(t, db, `INSERT INTO approval_delegations (id,delegator_id,delegate_id,start_at,end_at,created_at,revoked_at) VALUES (1,2,3,?,?,?,?)`,
		tStart, tEnd, tPast, tInside)
	ins := `INSERT INTO approval_history (pr_id,round,seq,actor_id,action,at,on_behalf_of) VALUES (1,1,1,3,'approve',?,2)`
	wantErr(t, db, "유효한 위임이 없습니다", ins, "2026-02-06T00:00:00Z")
	mustExec(t, db, ins, "2026-02-04T00:00:00Z") // 해제 전 시각의 결재는 유효
}

func TestAppSettingsDefaultOff(t *testing.T) {
	db := setup(t)
	var v string
	if err := db.QueryRow(`SELECT value FROM app_settings WHERE key = 'llm.image_transmission_enabled'`).Scan(&v); err != nil || v != "false" {
		t.Fatalf("이미지 전송 기본값은 꺼짐이어야 합니다: %q %v", v, err)
	}
}
