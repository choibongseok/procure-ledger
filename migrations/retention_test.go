package migrations_test

import "testing"

// B29 AC-840: 결재 이력·알림·수신 건의 불변 트리거 완화는 "폐기 처리된 문서의 의견 칸을 빈 값으로 바꾸는 갱신"에서만 동작한다.
func TestRetentionTriggerRelaxationIsNarrow(t *testing.T) {
	db := setup(t)
	seed(t, db)
	mustExec(t, db, `INSERT INTO purchase_requests (id,no,requester_id,department_id,title,status,created_at,updated_at)
		VALUES (1,'GEN-1',1,1,'기안 제목','approved','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`)
	mustExec(t, db, `INSERT INTO approval_history (pr_id,round,seq,actor_id,action,comment,at) VALUES (1,1,0,1,'submit','','2026-01-01T00:00:00Z')`)
	mustExec(t, db, `INSERT INTO approval_history (pr_id,round,seq,actor_id,action,comment,at) VALUES (1,1,1,2,'approve','확인했습니다','2026-01-01T01:00:00Z')`)
	mustExec(t, db, `INSERT INTO notifications (user_id,kind,title,body,link,ref_type,ref_id,created_at) VALUES (1,'approval_done','결재 완료','「기안 제목」 결재가 완료되었습니다.','/docs/1','purchase_request',1,'2026-01-01T02:00:00Z')`)
	mustExec(t, db, `INSERT INTO notifications (user_id,kind,title,body,link,ref_type,ref_id,created_at) VALUES (1,'x','다른 문서','본문','/docs/2','purchase_request',2,'2026-01-01T02:00:00Z')`)
	mustExec(t, db, `INSERT INTO approval_forms (code,category,name,kind,active,required_permission,created_at) VALUES ('RCV','general','수신 시험','general',1,'draft:create','x')`)
	mustExec(t, db, `INSERT INTO document_receipts (doc_id,round,unit_id,unit_name,status,created_at,comment,reason) VALUES (1,1,10,'재무팀','done','2026-01-01T03:00:00Z','접수 의견','사유')`)

	// 폐기 전에는 어떤 갱신도 막힌다(의견을 비우는 것도).
	wantErr(t, db, "수정할 수 없습니다", `UPDATE approval_history SET comment = '' WHERE action = 'approve'`)
	wantErr(t, db, "읽음 처리", `UPDATE notifications SET title = '비움' WHERE ref_id = 1`)
	wantErr(t, db, "바꿀 수 없습니다", `UPDATE document_receipts SET comment = '', reason = ''`)

	mustExec(t, db, `UPDATE purchase_requests SET disposed_at = '2026-11-12T00:00:00Z', disposed_by = 3, disposal_reason = '보존 만료' WHERE id = 1`)

	// 폐기된 문서: 의견만 빈 값으로 바꿀 수 있다.
	mustExec(t, db, `UPDATE approval_history SET comment = '' WHERE pr_id = 1 AND action = 'approve'`)
	wantErr(t, db, "수정할 수 없습니다", `UPDATE approval_history SET comment = '다시 씀' WHERE pr_id = 1 AND action = 'approve'`)
	wantErr(t, db, "수정할 수 없습니다", `UPDATE approval_history SET actor_id = 3 WHERE pr_id = 1`)
	wantErr(t, db, "수정할 수 없습니다", `UPDATE approval_history SET action = 'reject', comment = '' WHERE pr_id = 1 AND seq = 1`)
	wantErr(t, db, "삭제할 수 없습니다", `DELETE FROM approval_history WHERE pr_id = 1`)

	mustExec(t, db, `UPDATE notifications SET title = '폐기된 문서 관련 알림', body = '폐기됨' WHERE ref_type = 'purchase_request' AND ref_id = 1`)
	wantErr(t, db, "읽음 처리", `UPDATE notifications SET kind = 'y' WHERE ref_id = 1`)
	wantErr(t, db, "읽음 처리", `UPDATE notifications SET title = '다른 문서 제목 지움' WHERE ref_id = 2`)    // 폐기되지 않은 문서의 알림은 그대로
	mustExec(t, db, `UPDATE notifications SET read_at = '2026-11-12T01:00:00Z' WHERE ref_id = 2`) // 읽음 처리는 계속 된다
	wantErr(t, db, "삭제할 수 없습니다", `DELETE FROM notifications WHERE ref_id = 1`)

	mustExec(t, db, `UPDATE document_receipts SET comment = '', reason = '' WHERE doc_id = 1`)
	wantErr(t, db, "바꿀 수 없습니다", `UPDATE document_receipts SET status = 'pending', comment = '' WHERE doc_id = 1`)
	wantErr(t, db, "바꿀 수 없습니다", `UPDATE document_receipts SET comment = '다시 씀' WHERE doc_id = 1`)
	wantErr(t, db, "삭제할 수 없습니다", `DELETE FROM document_receipts WHERE doc_id = 1`)

	// 폐기 기록은 한 번 채워지면 바꿀 수 없다.
	wantErr(t, db, "폐기 기록은 바꿀 수 없습니다", `UPDATE purchase_requests SET disposed_at = NULL WHERE id = 1`)
	wantErr(t, db, "폐기 기록은 바꿀 수 없습니다", `UPDATE purchase_requests SET disposal_reason = '고침' WHERE id = 1`)
}

// 새 열의 기본값: 법정 보존·폐기 보류 아님, 폐기 기록 없음.
func TestRetentionColumnsDefaults(t *testing.T) {
	db := setup(t)
	seed(t, db)
	mustExec(t, db, `INSERT INTO purchase_requests (id,no,requester_id,department_id,title,status,created_at,updated_at)
		VALUES (1,'PR-1',1,1,'t','draft','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`)
	var legal, hold int
	var disposed *string
	if err := db.QueryRow(`SELECT legal_hold, disposal_hold, disposed_at FROM purchase_requests WHERE id = 1`).Scan(&legal, &hold, &disposed); err != nil {
		t.Fatal(err)
	}
	if legal != 0 || hold != 0 || disposed != nil {
		t.Errorf("기본값 legal=%d hold=%d disposed=%v", legal, hold, disposed)
	}
	wantErr(t, db, "CHECK", `UPDATE purchase_requests SET legal_hold = 2 WHERE id = 1`)
}
