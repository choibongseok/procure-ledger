package service_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
)

// 결재 완료된 일반 기안서(GEN) 한 건: 첨부·의견·읽음 상태가 있다. 보존 기한을 과거로 당겨 만료 상태로 만든다.
func (e *env) completedGen(t *testing.T, title string, expiredUntil string) domain.PurchaseRequest {
	t.Helper()
	att, docs := e.attach()
	ev, err := att.Upload(as(uStaff), "첨부.pdf", pdfBytes)
	if err != nil {
		t.Fatal(err)
	}
	d, err := docs.CreateDraft(as(uStaff), domain.DocDraftInput{FormCode: "GEN", Title: title, Fields: map[string]any{
		"content": "기밀 본문", "attachments": []any{float64(ev.ID)}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := docs.Submit(as(uStaff), d.ID); err != nil {
		t.Fatal(err)
	}
	decideAs(t, e, uLead, d.ID, true, "팀장 의견")
	res := decideAs(t, e, uHead, d.ID, true, "부서장 의견")
	if res.Request.Status != domain.PRApproved {
		t.Fatalf("결재 완료가 아님: %s", res.Request.Status)
	}
	if _, err := e.db.Exec(`INSERT INTO document_reads (doc_id, user_id, read_at) VALUES (?, ?, '2026-11-10T00:00:00Z')`, d.ID, uStaff); err != nil {
		t.Fatal(err)
	}
	if expiredUntil != "" {
		if _, err := e.db.Exec(`UPDATE purchase_requests SET retention_until = ? WHERE id = ?`, expiredUntil, d.ID); err != nil {
			t.Fatal(err)
		}
	}
	return res.Request
}

func (e *env) candidateIDs(t *testing.T, who int64) map[int64]bool {
	t.Helper()
	rows, _, err := e.retention.Candidates(as(who), 200, 0)
	if err != nil {
		t.Fatal(err)
	}
	out := map[int64]bool{}
	for _, r := range rows {
		out[r.ID] = true
	}
	return out
}

func (e *env) col(t *testing.T, q string, args ...any) string {
	t.Helper()
	var v string
	if err := e.db.QueryRow(q, args...).Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

// AC-830·831: 후보는 결재 완료·기한 경과·법정 보존 아님·보류 아님·미폐기 문서뿐이다. 만료일 당일은 후보가 아니고 다음 날부터 후보다.
func TestRetentionCandidateRules(t *testing.T) {
	e := newEnv(t)
	today := "2026-11-10" // 시험 시계(KST 18:00)
	gen := e.completedGen(t, "일반 기안", "2026-11-09")
	if got := e.col(t, `SELECT legal_hold FROM purchase_requests WHERE id = ?`, gen.ID); got != "0" {
		t.Fatalf("일반 기안서는 법정 보존이 아님: %s", got)
	}
	if !e.candidateIDs(t, uAdmin)[gen.ID] {
		t.Fatal("기한이 어제인 일반 기안서가 후보가 아님")
	}
	// 만료일 당일까지는 보존한다.
	if _, err := e.db.Exec(`UPDATE purchase_requests SET retention_until = ? WHERE id = ?`, today, gen.ID); err != nil {
		t.Fatal(err)
	}
	if e.candidateIDs(t, uAdmin)[gen.ID] {
		t.Fatal("만료일 당일인데 후보임")
	}
	if _, err := e.db.Exec(`UPDATE purchase_requests SET retention_until = '2026-11-09' WHERE id = ?`, gen.ID); err != nil {
		t.Fatal(err)
	}
	// 영구 보존(기한 없음)은 후보가 아니다.
	if _, err := e.db.Exec(`UPDATE purchase_requests SET retention_until = NULL WHERE id = ?`, gen.ID); err != nil {
		t.Fatal(err)
	}
	if e.candidateIDs(t, uAdmin)[gen.ID] {
		t.Fatal("영구 보존 문서가 후보임")
	}
	if _, err := e.db.Exec(`UPDATE purchase_requests SET retention_until = '2026-11-09' WHERE id = ?`, gen.ID); err != nil {
		t.Fatal(err)
	}
	// 법정 보존 양식(지출 결의서)은 연한이 지나도 후보가 아니고 완료 때 legal_hold 가 스냅샷된다.
	exp := e.expense(t, uStaff, 15_000_000)
	_, docs := e.attach()
	if _, err := docs.Submit(as(uStaff), exp.ID); err != nil {
		t.Fatal(err)
	}
	decideAs(t, e, uLead, exp.ID, true, "")
	decideAs(t, e, uHead, exp.ID, true, "")
	if got := e.col(t, `SELECT legal_hold FROM purchase_requests WHERE id = ?`, exp.ID); got != "1" {
		t.Fatalf("지출 결의서 legal_hold = %s", got)
	}
	if _, err := e.db.Exec(`UPDATE purchase_requests SET retention_until = '2020-01-01' WHERE id = ?`, exp.ID); err != nil {
		t.Fatal(err)
	}
	// 진행 중·반려 문서는 후보가 아니다.
	_, docs2 := e.attach()
	draft, err := docs2.CreateDraft(as(uStaff), domain.DocDraftInput{FormCode: "GEN", Title: "진행 중", Fields: map[string]any{"content": "본문"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := docs2.Submit(as(uStaff), draft.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.Exec(`UPDATE purchase_requests SET retention_until = '2020-01-01' WHERE id = ?`, draft.ID); err != nil {
		t.Fatal(err)
	}
	c := e.candidateIDs(t, uAdmin)
	if !c[gen.ID] || c[exp.ID] || c[draft.ID] {
		t.Fatalf("후보 %v (기대: 일반 기안서만)", c)
	}
	// 수신 처리가 끝나지 않은 문서는 후보가 아니다.
	if _, err := e.db.Exec(`INSERT INTO document_receipts (doc_id,round,unit_id,unit_name,status,created_at) VALUES (?,1,10,'재무팀','pending','2026-11-10T00:00:00Z')`, gen.ID); err != nil {
		t.Fatal(err)
	}
	if e.candidateIDs(t, uAdmin)[gen.ID] {
		t.Fatal("수신 대기 중인 문서가 후보임")
	}
}

// AC-832: 목록은 관리자·감사가 보고, 폐기·보류는 관리자만 한다. 권한 없는 요청은 거부되고 감사에 남는다.
func TestRetentionPermissions(t *testing.T) {
	e := newEnv(t)
	gen := e.completedGen(t, "권한 시험", "2026-11-09")
	for _, who := range []int64{uAdmin, uAuditor} {
		if !e.candidateIDs(t, who)[gen.ID] {
			t.Errorf("%d 가 목록을 못 봄", who)
		}
		if n, err := e.retention.ExpiredCount(as(who)); err != nil || n != 1 {
			t.Errorf("%d 건수 %d %v", who, n, err)
		}
	}
	for _, who := range []int64{uStaff, uLead, uFinance} {
		if _, _, err := e.retention.Candidates(as(who), 10, 0); !errors.Is(err, contract.ErrForbidden) {
			t.Errorf("%d 의 목록 조회가 거부되지 않음: %v", who, err)
		}
	}
	for _, who := range []int64{uAuditor, uFinance, uStaff} {
		if _, err := e.retention.Dispose(as(who), domain.DisposeInput{IDs: []int64{gen.ID}, Reason: "정리"}); !errors.Is(err, contract.ErrForbidden) {
			t.Errorf("%d 의 폐기가 거부되지 않음: %v", who, err)
		}
		if err := e.retention.Hold(as(who), gen.ID, "보류"); !errors.Is(err, contract.ErrForbidden) {
			t.Errorf("%d 의 보류가 거부되지 않음: %v", who, err)
		}
	}
	if !e.rec.Has("doc.dispose", audit.TargetApprovalDoc, audit.ResultDenied) {
		t.Error("폐기 권한 거부가 감사에 남지 않음")
	}
	if got := e.col(t, `SELECT title FROM purchase_requests WHERE id = ?`, gen.ID); got != "권한 시험" {
		t.Fatalf("거부된 요청이 문서를 바꿈: %q", got)
	}
}

// AC-833·834·835: 폐기는 제목·필드·첨부·의견·읽음을 지우고 껍데기와 결재 행위를 남기며, 문서마다 감사가 남는다. 다시 폐기할 수 없다.
func TestRetentionDisposeLeavesShell(t *testing.T) {
	e := newEnv(t)
	gen := e.completedGen(t, "기밀 제목", "2026-11-09")
	histBefore := e.col(t, `SELECT COUNT(*) FROM approval_history WHERE pr_id = ?`, gen.ID)
	if e.col(t, `SELECT COUNT(*) FROM document_attachments WHERE doc_id = ?`, gen.ID) != "1" {
		t.Fatal("시험 준비: 첨부가 없음")
	}
	if _, err := e.retention.Dispose(as(uAdmin), domain.DisposeInput{IDs: []int64{gen.ID}, Reason: ""}); err == nil || !errors.Is(err, contract.ErrValidation) {
		t.Fatalf("사유 없이 폐기됨: %v", err)
	}
	if _, err := e.retention.Dispose(as(uAdmin), domain.DisposeInput{IDs: nil, Reason: "정리"}); err == nil {
		t.Fatal("선택 없이 폐기됨")
	}
	n, err := e.retention.Dispose(as(uAdmin), domain.DisposeInput{IDs: []int64{gen.ID, gen.ID}, Reason: "보존 연한 만료 정기 정리"})
	if err != nil || n != 1 {
		t.Fatalf("폐기: %d %v", n, err)
	}
	if got := e.col(t, `SELECT title || '|' || fields || '|' || reason || '|' || reject_reason FROM purchase_requests WHERE id = ?`, gen.ID); got != "폐기된 문서|{}||" {
		t.Errorf("지워야 할 칸이 남음: %q", got)
	}
	if got := e.col(t, `SELECT no || '|' || status || '|' || requester_id || '|' || COALESCE(retention_until,'') || '|' || disposal_reason || '|' || disposed_by FROM purchase_requests WHERE id = ?`, gen.ID); got != gen.No+"|approved|1|2026-11-09|보존 연한 만료 정기 정리|7" {
		t.Errorf("껍데기 항목: %q", got)
	}
	if e.col(t, `SELECT COUNT(*) FROM document_attachments WHERE doc_id = ? AND filename <> ''`, gen.ID) != "0" || e.col(t, `SELECT COUNT(*) FROM document_reads WHERE doc_id = ?`, gen.ID) != "0" {
		t.Error("첨부·읽음이 남음")
	}
	if e.col(t, `SELECT COUNT(*) FROM approval_history WHERE pr_id = ?`, gen.ID) != histBefore {
		t.Error("결재 이력 행이 바뀜(누가·언제 기록은 남아야 함)")
	}
	if e.col(t, `SELECT COUNT(*) FROM approval_history WHERE pr_id = ? AND comment <> ''`, gen.ID) != "0" || e.col(t, `SELECT COUNT(*) FROM approval_steps WHERE pr_id = ? AND comment <> ''`, gen.ID) != "0" {
		t.Error("결재 의견이 남음")
	}
	if strings.Contains(e.col(t, `SELECT `+strAgg("title || body")+` FROM notifications WHERE ref_type = 'purchase_request' AND ref_id = ?`, gen.ID), "기밀 제목") {
		t.Error("알림에 문서 제목이 남음")
	}
	if !e.rec.Has("doc.dispose", audit.TargetApprovalDoc, audit.ResultSuccess) {
		t.Error("폐기 감사가 남지 않음")
	}
	// 후보에서 빠지고 다시 폐기할 수 없다.
	if e.candidateIDs(t, uAdmin)[gen.ID] {
		t.Error("폐기된 문서가 후보에 남음")
	}
	if _, err := e.retention.Dispose(as(uAdmin), domain.DisposeInput{IDs: []int64{gen.ID}, Reason: "다시"}); err == nil || !errors.Is(err, contract.ErrValidation) {
		t.Errorf("폐기된 문서를 다시 폐기함: %v", err)
	}
}

// AC-834·836: 요청 전체가 한 트랜잭션이다. 후보가 아닌 문서가 섞이면 아무것도 지우지 않고, 감사 기록이 실패하면 폐기도 취소된다. 보류는 후보에서 빼고 해제하면 돌아온다.
func TestRetentionAtomicAndHold(t *testing.T) {
	e := newEnv(t)
	a := e.completedGen(t, "문서 A", "2026-11-09")
	b := e.completedGen(t, "문서 B", "2026-11-08")
	exp := e.expense(t, uStaff, 15_000_000)
	_, docs := e.attach()
	if _, err := docs.Submit(as(uStaff), exp.ID); err != nil {
		t.Fatal(err)
	}
	decideAs(t, e, uLead, exp.ID, true, "")
	decideAs(t, e, uHead, exp.ID, true, "")
	if _, err := e.db.Exec(`UPDATE purchase_requests SET retention_until = '2020-01-01' WHERE id = ?`, exp.ID); err != nil {
		t.Fatal(err)
	}
	// 법정 보존 문서가 섞이면 전부 취소된다.
	if _, err := e.retention.Dispose(as(uAdmin), domain.DisposeInput{IDs: []int64{a.ID, exp.ID}, Reason: "정리"}); err == nil || !strings.Contains(err.Error(), e.col(t, `SELECT no FROM purchase_requests WHERE id = ?`, exp.ID)) {
		t.Fatalf("법정 보존 문서가 섞인 폐기가 거부되지 않음: %v", err)
	}
	if got := e.col(t, `SELECT title FROM purchase_requests WHERE id = ?`, a.ID); got != "문서 A" {
		t.Fatalf("부분 폐기됨: %q", got)
	}
	// 감사 기록 실패 → 폐기 취소
	e.rec.FailWith(errors.New("저장소 오류"))
	if _, err := e.retention.Dispose(as(uAdmin), domain.DisposeInput{IDs: []int64{a.ID, b.ID}, Reason: "정리"}); err == nil {
		t.Fatal("감사 실패인데 폐기됨")
	}
	e.rec.FailWith(nil)
	if e.col(t, `SELECT COUNT(*) FROM purchase_requests WHERE disposed_at IS NOT NULL`) != "0" || e.col(t, `SELECT COUNT(*) FROM document_attachments`) == "0" {
		t.Fatal("감사 실패 뒤 폐기 흔적이 남음")
	}
	// 보류: 후보에서 빠지고 폐기도 거부되며 해제하면 돌아온다.
	if err := e.retention.Hold(as(uAdmin), a.ID, ""); err == nil || !errors.Is(err, contract.ErrValidation) {
		t.Fatalf("사유 없는 보류: %v", err)
	}
	if err := e.retention.Hold(as(uAdmin), a.ID, "감사 진행 중"); err != nil {
		t.Fatal(err)
	}
	if e.candidateIDs(t, uAdmin)[a.ID] {
		t.Fatal("보류 문서가 후보임")
	}
	holds, err := e.retention.Holds(as(uAuditor))
	if err != nil || len(holds) != 1 || holds[0].ID != a.ID || holds[0].HoldReason != "감사 진행 중" {
		t.Fatalf("보류 목록 %+v %v", holds, err)
	}
	if _, err := e.retention.Dispose(as(uAdmin), domain.DisposeInput{IDs: []int64{a.ID}, Reason: "정리"}); err == nil {
		t.Fatal("보류 문서가 폐기됨")
	}
	if err := e.retention.Hold(as(uAdmin), a.ID, "또"); !errors.Is(err, contract.ErrInvalidState) {
		t.Fatalf("이미 보류인 문서의 보류: %v", err)
	}
	if err := e.retention.ClearHold(as(uAdmin), a.ID, ""); err != nil {
		t.Fatal(err)
	}
	if !e.candidateIDs(t, uAdmin)[a.ID] {
		t.Fatal("보류 해제 뒤 후보로 돌아오지 않음")
	}
	if !e.rec.Has("doc.dispose_hold", audit.TargetApprovalDoc, audit.ResultSuccess) || !e.rec.Has("doc.dispose_hold_clear", audit.TargetApprovalDoc, audit.ResultSuccess) {
		t.Error("보류·해제 감사가 남지 않음")
	}
	// 100건 상한
	ids := make([]int64, 101)
	for i := range ids {
		ids[i] = int64(i + 1000)
	}
	if _, err := e.retention.Dispose(as(uAdmin), domain.DisposeInput{IDs: ids, Reason: "정리"}); err == nil || !errors.Is(err, contract.ErrValidation) || !strings.Contains(err.Error(), "100건") {
		t.Fatalf("100건 상한: %v", err)
	}
	// 폐기 성공(b 만)
	if n, err := e.retention.Dispose(as(uAdmin), domain.DisposeInput{IDs: []int64{b.ID}, Reason: "정리"}); err != nil || n != 1 {
		t.Fatalf("폐기 %d %v", n, err)
	}
	if got := e.col(t, `SELECT title FROM purchase_requests WHERE id = ?`, a.ID); got != "문서 A" {
		t.Fatalf("다른 문서가 바뀜: %q", got)
	}
}
