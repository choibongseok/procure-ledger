package service_test

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
)

// B50 3단계 잔여: 부재 설정(B29) 자동 연결(6.7, AC-5015)과 증빙 첨부의 문서 연결(11.4, AC-5043).

func (e *env) absenceRows(t *testing.T, delegator int64) (active, total int) {
	t.Helper()
	if err := e.db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(CASE WHEN revoked_at IS NULL THEN 1 ELSE 0 END),0) FROM approval_delegations WHERE kind = 'absence' AND delegator_id = ?`, delegator).Scan(&total, &active); err != nil {
		t.Fatal(err)
	}
	return
}

func TestLeaveApprovalCreatesAbsenceAndCancelRevokes(t *testing.T) {
	e := newEnv(t)
	l := e.leave(t)
	l.policy(t)
	l.open(t, uLead, 10)
	// 대결자 자격이 없으면(서열이 낮은 사람) 신청 때 거부한다. 결재 단계를 맡지 않은 사람이 대결자를 적어도 거부한다.
	bad := in("annual", "2026-11-16", "2026-11-17")
	bad.DelegateUserID = uStaff
	_, err := l.svc.Request(as(uLead), bad)
	wantValidation(t, err, "대결자로 지정할 수 없습니다")
	_, err = l.svc.Request(as(uStaff), func() domain.LeaveInput {
		b := in("annual", "2026-11-18", "2026-11-18")
		b.DelegateUserID = uLead
		return b
	}())
	wantValidation(t, err, "결재 단계를 맡고 있지 않아")
	// 정상: 팀장이 상위 조직장을 대결자로 적어 신청 → 승인 때 부재 행이 생기고 신청에 번호가 남는다.
	good := in("annual", "2026-11-16", "2026-11-17")
	good.DelegateUserID = uHead
	r, err := l.svc.Request(as(uLead), good)
	if err != nil {
		t.Fatalf("신청: %v", err)
	}
	if a, _ := e.absenceRows(t, uLead); a != 0 {
		t.Fatalf("승인 전에 부재 행이 생김: %d", a)
	}
	decideAs(t, e, uHead, r.DocID, true, "")
	got, _ := l.svc.Get(as(uLead), r.ID)
	if got.Status != domain.LeaveApproved || got.AbsenceDelegationID == 0 || got.Note != "" {
		t.Fatalf("승인 뒤 신청: %+v", got)
	}
	if a, tot := e.absenceRows(t, uLead); a != 1 || tot != 1 {
		t.Fatalf("부재 행 %d/%d", a, tot)
	}
	var kind, reason, from, to string
	if err := e.db.QueryRow(`SELECT kind, reason, start_at, end_at FROM approval_delegations WHERE id = ?`, got.AbsenceDelegationID).Scan(&kind, &reason, &from, &to); err != nil {
		t.Fatal(err)
	}
	// 2026-11-16 0시(KST) = 11-15 15:00Z, 종료는 11-18 0시(KST) = 11-17 15:00Z.
	if kind != "absence" || reason != "vacation" || !strings.HasPrefix(from, "2026-11-15T15:00") || !strings.HasPrefix(to, "2026-11-17T15:00") {
		t.Fatalf("부재 행: %s %s %s %s", kind, reason, from, to)
	}
	if len(e.notes(t, uHead, "absence_assigned")) != 1 {
		t.Fatal("대결자 알림이 없음")
	}
	// 휴가를 취소하면 만든 부재 행도 해제한다.
	if _, err := l.svc.Cancel(as(uLead), r.ID, "일정 변경"); err != nil {
		t.Fatal(err)
	}
	if a, tot := e.absenceRows(t, uLead); a != 0 || tot != 1 {
		t.Fatalf("취소 뒤 부재 행 %d/%d", a, tot)
	}
	if len(e.notes(t, uHead, "absence_revoked")) != 1 {
		t.Fatal("해제 알림이 없음")
	}
}

func TestLeaveApprovalAbsenceFailureDoesNotBlockApproval(t *testing.T) {
	e := newEnv(t)
	l := e.leave(t)
	l.policy(t)
	l.open(t, uLead, 10)
	// 같은 기간에 이미 부재가 있으면 부재 행만 건너뛰고 승인은 된다. 안내 문구가 신청에 남는다.
	now := e.now
	if _, err := e.delegation.Create(as(uLead), e.absenceIn(uHead, now.AddDate(0, 0, 5), 4, "trip")); err != nil {
		t.Fatal(err)
	}
	r, err := l.svc.Request(as(uLead), func() domain.LeaveInput {
		b := in("annual", "2026-11-16", "2026-11-17")
		b.DelegateUserID = uHead
		return b
	}())
	if err != nil {
		t.Fatal(err)
	}
	decideAs(t, e, uHead, r.DocID, true, "")
	got, _ := l.svc.Get(as(uLead), r.ID)
	if got.Status != domain.LeaveApproved || got.AbsenceDelegationID != 0 || !strings.Contains(got.Note, "부재 설정을 만들지 못했습니다") {
		t.Fatalf("부재 실패 뒤 신청: %+v", got)
	}
	if a, tot := e.absenceRows(t, uLead); tot != 1 || a != 1 {
		t.Fatalf("기존 부재만 남아야 함: %d/%d", a, tot)
	}
	// 취소해도 기존 부재는 건드리지 않는다.
	if _, err := l.svc.Cancel(as(uLead), r.ID, ""); err != nil {
		t.Fatal(err)
	}
	if a, _ := e.absenceRows(t, uLead); a != 1 {
		t.Fatalf("기존 부재가 해제됨: %d", a)
	}
}

// 증빙 첨부는 신청서 문서에 붙고, 신청자·직속 조직장·인사 담당·감사자만 받으며(결재자는 거부), 본인이 아닌 사람의 내려받기는 열람 기록과 감사를 남긴다.
func TestLeaveProofAttachmentLinkAndDownloadGate(t *testing.T) {
	e := newEnv(t)
	l := e.leave(t)
	l.policy(t)
	l.open(t, uStaff, 10)
	att := l.att
	up, err := att.Upload(as(uStaff), "진단서.pdf", []byte("%PDF-1.4 test"))
	if err != nil {
		t.Fatalf("업로드: %v", err)
	}
	// 남의 첨부·없는 첨부는 거부, 병가는 증빙이 필수다.
	other, _ := att.Upload(as(uStaff2), "남의.pdf", []byte("%PDF-1.4 other"))
	sick := in("sick", "2026-11-16", "2026-11-16")
	_, err = l.svc.Request(as(uStaff), sick)
	wantValidation(t, err, "증빙 첨부가 필요")
	sick.ProofAttachmentID = other.ID
	_, err = l.svc.Request(as(uStaff), sick)
	wantValidation(t, err, "증빙 첨부를 찾을 수 없습니다")
	sick.ProofAttachmentID = up.ID
	r, err := l.svc.Request(as(uStaff), sick)
	if err != nil || r.ProofAttachmentID != up.ID {
		t.Fatalf("병가 신청: %+v %v", r, err)
	}
	var docID sql.NullInt64
	if err := e.db.QueryRow(`SELECT doc_id FROM document_attachments WHERE id = ?`, up.ID).Scan(&docID); err != nil || docID.Int64 != r.DocID {
		t.Fatalf("첨부가 문서에 붙지 않음: %v %v", docID, err)
	}
	// 이미 붙은 첨부는 다른 신청에 다시 쓸 수 없다.
	sick2 := in("sick", "2026-11-17", "2026-11-17")
	sick2.ProofAttachmentID = up.ID
	_, err = l.svc.Request(as(uStaff), sick2)
	wantValidation(t, err, "증빙 첨부를 찾을 수 없습니다")
	// 본인은 받는다(열람 기록 없음). 결재선의 결재자(uLead 는 직속 조직장이라 받는다)와 달리 관련 없는 사람은 문서를 못 보거나 거부된다.
	if _, err := att.Open(as(uStaff), up.ID); err != nil {
		t.Fatalf("본인 내려받기: %v", err)
	}
	if len(l.noter.got) != 0 {
		t.Fatalf("본인 내려받기에 열람 기록: %d", len(l.noter.got))
	}
	if _, err := att.Open(as(uStaff2), up.ID); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("관련 없는 사람의 내려받기: %v", err)
	}
	// 인사 담당은 받고 열람 기록(범주 attendance, 증빙 파일)과 감사가 남는다.
	if _, err := att.Open(hrReadCtx(), up.ID); err != nil {
		t.Fatalf("인사 담당 내려받기: %v", err)
	}
	if len(l.noter.got) != 1 || !e.rec.Has("leave.proof_download", "leave_request", "success") {
		t.Fatalf("열람 기록 %d, 감사 %v", len(l.noter.got), e.rec.Has("leave.proof_download", "leave_request", "success"))
	}
	// 열람 기록을 남기지 못하면 내려주지 않는다(fail-closed).
	l.noter.fail = errors.New("기록 실패")
	if _, err := att.Open(hrReadCtx(), up.ID); err == nil || errors.Is(err, contract.ErrNotFound) {
		t.Fatalf("기록 실패인데 내려줌: %v", err)
	}
}

// hrReadCtx 는 인사 담당에 첨부 열람 권한(draft:read)을 더한 요청 context 다.
func hrReadCtx() context.Context {
	p := users[uFinance2]
	p.Channel = contract.ChannelWeb
	p.Perms = contract.NewPermSet(contract.PermAttendanceSelf, contract.PermAttendanceRead, contract.PermAttendanceHR, contract.PermDraftRead)
	return contract.WithPrincipal(context.Background(), p)
}
