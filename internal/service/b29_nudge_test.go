package service_test

import (
	"strings"
	"testing"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/service"
)

func (e *env) nudgeSvc() *service.NudgeService {
	return service.NewNudgeService(service.Base{DB: e.db, Txs: contract.SQLTxManager{DB: e.db}, Audit: e.rec, Org: leftAwareOrg{e.org}, Now: func() time.Time { return e.now }})
}

// AC-820~822: 기안자만, 24시간이 지난 단계에만, KST 하루 1회. 받는 사람은 현재 처리자이고 알림 본문에는 제목만 있다. KST 0시를 넘기면 새 하루다.
func TestNudgeRulesAndKSTDay(t *testing.T) {
	e := newEnv(t)
	ns := e.nudgeSvc()
	base := time.Date(2026, 11, 10, 14, 0, 0, 0, time.UTC) // KST 23:00
	e.now = base
	pr := e.submitted(t, uStaff, 500_000) // 팀장(uLead) 한 단계
	// 아직 24시간이 안 됐다.
	e.now = base.Add(23 * time.Hour)
	if _, err := ns.Nudge(as(uStaff), pr.ID); !isErr(err, contract.ErrValidation) || !strings.Contains(err.Error(), "아직 독촉할 단계가 없습니다. 결재 대기가 24시간을 넘으면 독촉할 수 있습니다.") {
		t.Fatalf("24시간 전: %v", err)
	}
	// 기안자가 아니면 거부, 24시간 뒤에는 성공.
	e.now = base.Add(24*time.Hour + 30*time.Minute) // KST 11. 11. 23:30
	if _, err := ns.Nudge(as(uLead), pr.ID); !isErr(err, contract.ErrForbidden) || !strings.Contains(err.Error(), "기안자만 독촉할 수 있습니다.") {
		t.Fatalf("기안자 아님: %v", err)
	}
	st, err := ns.Status(as(uStaff), pr.ID)
	if err != nil || !st.Delayed || st.DoneToday != nil || st.NoApprover {
		t.Fatalf("상태: %+v %v", st, err)
	}
	if _, err := ns.Status(as(uStaff2), pr.ID); !isErr(err, contract.ErrNotFound) {
		t.Fatalf("남의 문서 상태: %v", err)
	}
	res, err := ns.Nudge(as(uStaff), pr.ID)
	if err != nil || res.Recipients != 1 || res.WaitingMinutes < 24*60 {
		t.Fatalf("독촉: %+v %v", res, err)
	}
	notes := e.notes(t, uLead, "approval_nudge")
	if len(notes) != 1 || notes[0].Title != "결재 독촉" || !strings.Contains(notes[0].Body, "「구매 요청」") || !strings.Contains(notes[0].Body, "대기 1일 0시간") {
		t.Fatalf("알림: %+v", notes)
	}
	// 같은 날 두 번째는 거부, 상태에 오늘 보낸 시각이 있다.
	e.now = e.now.Add(10 * time.Minute) // KST 23:40
	if _, err := ns.Nudge(as(uStaff), pr.ID); !isErr(err, contract.ErrInvalidState) || !strings.Contains(err.Error(), "오늘은 이미 독촉했습니다. 내일 다시 독촉할 수 있습니다.") {
		t.Fatalf("같은 날: %v", err)
	}
	if st, _ := ns.Status(as(uStaff), pr.ID); st.DoneToday == nil || len(st.Records) != 1 || st.Records[0].Recipients != 1 || len(st.Records[0].RecipientNames) != 1 {
		t.Fatalf("상태·기록: %+v", st)
	}
	// KST 0시를 넘기면 새 하루(AC-822).
	e.now = e.now.Add(30 * time.Minute) // KST 11. 12. 00:10
	if _, err := ns.Nudge(as(uStaff), pr.ID); err != nil {
		t.Fatalf("다음 날: %v", err)
	}
	if n := count(t, e.db, `SELECT COUNT(*) FROM approval_nudges WHERE doc_id = ?`, pr.ID); n != 2 {
		t.Fatalf("기록 %d건(기대 2)", n)
	}
	// AC-823: 같은 날 같은 단계의 직접 중복 삽입은 UNIQUE 가 막고, 기록은 고치거나 지울 수 없다.
	if _, err := e.db.Exec(`INSERT INTO approval_nudges (doc_id, round, seq, nudged_by, nudged_at, nudge_day, recipients) SELECT doc_id, round, seq, nudged_by, nudged_at, nudge_day, recipients FROM approval_nudges LIMIT 1`); err == nil {
		t.Fatal("같은 날 같은 단계 중복이 들어감")
	}
	if _, err := e.db.Exec(`UPDATE approval_nudges SET recipients = 9`); err == nil {
		t.Fatal("독촉 기록이 수정됨")
	}
	if _, err := e.db.Exec(`DELETE FROM approval_nudges`); err == nil {
		t.Fatal("폐기되지 않은 문서의 독촉 기록이 지워짐")
	}
	if rec, ok := lastRecord(e, "approval.nudge"); !ok || !hasDiff(rec, "nudged_to", "1") || !hasDiff(rec, "step", "팀장") {
		t.Fatalf("독촉 감사: %+v %v", rec, ok)
	}
}

// AC-822: 단계가 바뀌면(앞 단계 처리) 새 단계는 처음부터 24시간을 센다. 진행 중이 아닌 문서는 독촉할 수 없다.
func TestNudgeResetsOnNextStepAndNeedsInProgress(t *testing.T) {
	e := newEnv(t)
	ns := e.nudgeSvc()
	base := time.Date(2026, 11, 10, 1, 0, 0, 0, time.UTC)
	e.now = base
	pr := e.submitted(t, uStaff, 3_000_000) // 팀장 → 부서장 → 재무
	e.now = base.Add(30 * time.Hour)
	if _, err := ns.Nudge(as(uStaff), pr.ID); err != nil {
		t.Fatalf("첫 단계 독촉: %v", err)
	}
	approve(t, e, uLead, pr.ID) // 다음 단계(부서장)가 이 시각에 시작된다
	if _, err := ns.Nudge(as(uStaff), pr.ID); !isErr(err, contract.ErrValidation) {
		t.Fatalf("새 단계는 24시간 전이라 독촉할 수 없어야 함: %v", err)
	}
	e.now = e.now.Add(25 * time.Hour)
	res, err := ns.Nudge(as(uStaff), pr.ID)
	if err != nil || res.Recipients != 1 {
		t.Fatalf("새 단계 독촉: %+v %v", res, err)
	}
	if len(e.notes(t, uHead, "approval_nudge")) != 1 {
		t.Fatal("부서장에게 알림이 가야 함")
	}
	// 임시저장·완료 문서는 독촉할 수 없다.
	d := e.draft(t, uStaff, 100_000)
	if _, err := ns.Nudge(as(uStaff), d.ID); !isErr(err, contract.ErrInvalidState) || !strings.Contains(err.Error(), "결재 진행 중인 문서만 독촉할 수 있습니다.") {
		t.Fatalf("임시저장: %v", err)
	}
}

// 부재(대결) 중이면 원결재자와 유효한 대결자 모두에게 간다(E13). 대결자의 대결자는 후보가 아니다(연쇄 금지, 3.3.1).
func TestNudgeReachesDelegateAndNoChain(t *testing.T) {
	e := newEnv(t)
	ns := e.nudgeSvc()
	pr := e.submitted(t, uStaff2, 500_000) // 영업1팀 팀장(uLead2)의 단계
	if _, err := e.delegation.Create(as(uLead2), e.absenceIn(uHead, e.now, 5, "vacation")); err != nil {
		t.Fatal(err)
	}
	if _, err := e.delegation.Create(as(uHead), e.absenceIn(uCEO, e.now, 5, "trip")); err != nil {
		t.Fatal(err)
	}
	e.now = e.now.Add(25 * time.Hour)
	res, err := ns.Nudge(as(uStaff2), pr.ID)
	if err != nil || res.Recipients != 2 {
		t.Fatalf("원결재자와 대결자 2명이어야 함: %+v %v", res, err)
	}
	if len(e.notes(t, uLead2, "approval_nudge")) != 1 || len(e.notes(t, uHead, "approval_nudge")) != 1 || len(e.notes(t, uCEO, "approval_nudge")) != 0 {
		t.Fatal("받는 사람이 다름(대결자의 대결자는 후보가 아니다)")
	}
	if _, err := e.approval.Decide(as(uCEO), pr.ID, domain.Decision{Approve: true}); err == nil {
		t.Fatal("대결자의 대결자가 결재함(연쇄 금지 위반)")
	}
}
