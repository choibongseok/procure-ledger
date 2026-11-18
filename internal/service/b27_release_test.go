package service_test

import (
	"strings"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
)

// mcrForm 은 마감 해제 요청서 양식의 재무팀 조직 코드(T-FIN)를 시험 조직(FIN)에 맞춘 판을 게시하고, 재무팀장 자리를 재무 담당2(uFinance2)로 정한다(운영도 양식 관리 화면에서 같은 일을 한다).
func (e *env) mcrForm(t *testing.T) {
	t.Helper()
	f := e.forms()
	v, err := f.CreateVersionDraft(as(uAdmin), "MCR")
	if err != nil {
		t.Fatal(err)
	}
	def := v.Definition
	for i := range def.Line.Steps {
		if def.Line.Steps[i].UnitCode == "T-FIN" {
			def.Line.Steps[i].UnitCode = "FIN"
		}
	}
	def.Recipients.CC = nil
	if _, err := f.SaveVersionDraft(as(uAdmin), v.ID, domain.FormVersionInput{Definition: def}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Publish(as(uAdmin), v.ID); err != nil {
		t.Fatal(err)
	}
	e.org.SetHead(unitFin, uFinance2)
}

func (e *env) events(t *testing.T, month string) []string {
	t.Helper()
	rows, err := e.db.Query(`SELECT kind FROM month_close_events WHERE month = ? ORDER BY id`, month)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var k string
		_ = rows.Scan(&k)
		out = append(out, k)
	}
	return out
}

func (e *env) hasAudit(action string) bool {
	for _, r := range e.rec.Records() {
		if r.Action == action && r.Result == audit.ResultSuccess && r.TargetType == audit.TargetMonthClose {
			return true
		}
	}
	return false
}

// AC-930·931: 해제 요청은 가장 최근 마감 달만, 사유 10자 이상, 달당 하나, 일반 문서 작성으로는 못 만든다. 반려면 마감 그대로, 승인되면 같은 트랜잭션에서 열리고 재마감은 버전 2 이며 이전 스냅샷이 보존된다.
func TestMonthReleaseFlow(t *testing.T) {
	e := newEnv(t)
	e.closeFixture(t)
	e.mcrForm(t)
	for _, m := range []string{"2026-10", "2026-11"} {
		if _, err := e.monthClose.Close(as(uFinance), m); err != nil {
			t.Fatal(err)
		}
	}
	const reason = "11월 말 입고 단가를 정정해야 합니다"
	for _, who := range []int64{uStaff, uLead, uAdmin, uAuditor} {
		if _, err := e.monthClose.RequestRelease(as(who), "2026-11", reason); !isErr(err, contract.ErrForbidden) {
			t.Fatalf("%d 는 해제를 요청할 수 없어야 함: %v", who, err)
		}
	}
	if _, err := e.monthClose.RequestRelease(as(uFinance), "2026-11", "짧은 사유"); !isErr(err, contract.ErrValidation) || !strings.Contains(err.Error(), "마감 해제 사유를 10자 이상 입력하세요.") {
		t.Fatalf("사유: %v", err)
	}
	if _, err := e.monthClose.RequestRelease(as(uFinance), "2026-10", reason); !isErr(err, contract.ErrValidation) || !strings.Contains(err.Error(), "마감 해제는 가장 최근에 마감한 달(2026년 11월)부터 차례로 할 수 있습니다.") {
		t.Fatalf("최근 달만: %v", err)
	}
	// 일반 문서 작성 경로로는 해제 요청서를 만들 수 없다.
	if _, err := e.docs().CreateDraft(as(uFinance), domain.DocDraftInput{FormCode: "MCR", Title: "수동", Fields: map[string]any{"month": "2026-11", "reason": reason}}); !isErr(err, contract.ErrValidation) || !strings.Contains(err.Error(), "월 마감 화면에서 해제 요청") {
		t.Fatalf("일반 작성: %v", err)
	}
	doc, err := e.monthClose.RequestRelease(as(uFinance), "2026-11", reason)
	if err != nil || doc == 0 {
		t.Fatalf("해제 요청: %d %v", doc, err)
	}
	if st, _ := e.monthClose.Status(as(uAuditor), "2026-11"); st.State != domain.MonthReleasePending || st.Version != 1 {
		t.Fatalf("결재 중 상태: %+v", st)
	}
	if _, err := e.monthClose.RequestRelease(as(uFinance), "2026-11", reason); !isErr(err, contract.ErrInvalidState) || !strings.Contains(err.Error(), "이 달의 마감 해제 요청이 이미 결재 중입니다.") {
		t.Fatalf("중복 요청: %v", err)
	}
	rows, _ := e.monthClose.List(as(uFinance))
	if rows[0].Month != "2026-11" || rows[0].State != domain.MonthReleasePending || rows[0].Releasable {
		t.Fatalf("목록의 결재 중 표시: %+v", rows[0])
	}
	// 마감 해제 결재 중에도 마감은 그대로라 11월 날짜 출고는 거부된다.
	it := domain.Item{ID: 1}
	if _, err := e.issueOn(t, it, 1, "2026-11-20"); err == nil || err.Error() != closedMsg {
		t.Fatalf("결재 중에도 잠금: %v", err)
	}
	// 반려: 마감 그대로이고 사건·감사가 남으며 다시 요청할 수 있다.
	decideAs(t, e, uFinance2, doc, false, "근거를 보강하세요")
	if st, _ := e.monthClose.Status(as(uAuditor), "2026-11"); st.State != domain.MonthClosed {
		t.Fatalf("반려 뒤 상태: %+v", st)
	}
	if !e.hasAudit("month_close.release_reject") || !e.hasAudit("month_close.release_request") {
		t.Fatal("요청·반려 감사가 없음")
	}
	doc2, err := e.monthClose.RequestRelease(as(uFinance), "2026-11", reason)
	if err != nil {
		t.Fatalf("재요청: %v", err)
	}
	// 승인: 같은 트랜잭션에서 달이 열리고 감사가 남는다.
	decideAs(t, e, uFinance2, doc2, true, "확인")
	st, _ := e.monthClose.Status(as(uAuditor), "2026-11")
	if st.State != domain.MonthOpen || st.Version != 1 || !e.hasAudit("month_close.release") {
		t.Fatalf("승인 뒤: %+v", st)
	}
	if got := strings.Join(e.events(t, "2026-11"), ","); got != "close,release_request,release_reject,release_request,release" {
		t.Fatalf("사건: %s", got)
	}
	// 10월은 여전히 마감이고 11월 날짜 출고는 이제 된다(마감 전 점검 규칙은 그대로).
	if _, err := e.issueOn(t, domain.Item{ID: it.ID}, 1, "2026-10-28"); err == nil || err.Error() != closedMsg {
		t.Fatalf("10월은 마감 그대로: %v", err)
	}
	// 재마감하면 버전 2 이고 이전 버전 스냅샷은 보존된다.
	row, err := e.monthClose.Close(as(uFinance), "2026-11")
	if err != nil || row.Version != 2 || row.State != domain.MonthClosed {
		t.Fatalf("재마감: %+v %v", row, err)
	}
	if n1, n2 := count(t, e.db, `SELECT COUNT(*) FROM month_close_lines WHERE month='2026-11' AND version=1`), count(t, e.db, `SELECT COUNT(*) FROM month_close_lines WHERE month='2026-11' AND version=2`); n1 == 0 || n2 != n1 {
		t.Fatalf("버전별 스냅샷 %d / %d", n1, n2)
	}
}

// AC-932·E12: 해제 결재 중에 이후 달이 마감되면 승인 시점에 승인 전체가 취소된다.
func TestMonthReleaseCancelledWhenLaterMonthClosed(t *testing.T) {
	e := newEnv(t)
	it := e.closeFixture(t)
	e.mcrForm(t)
	for _, m := range []string{"2026-10", "2026-11"} {
		if _, err := e.monthClose.Close(as(uFinance), m); err != nil {
			t.Fatal(err)
		}
	}
	doc, err := e.monthClose.RequestRelease(as(uFinance), "2026-11", "11월 기록을 바로잡기 위한 해제 요청")
	if err != nil {
		t.Fatal(err)
	}
	e.receiveAt(t, at(2026, 12, 3, 1), it, 3000, 5)
	e.now = at(2027, 1, 5, 1)
	if _, err := e.monthClose.Close(as(uFinance), "2026-12"); err != nil {
		t.Fatalf("12월 마감: %v", err)
	}
	if _, err := e.approval.Decide(as(uFinance2), doc, domain.Decision{Approve: true}); err == nil || !strings.Contains(err.Error(), "이후 달이 마감되어 해제할 수 없습니다") {
		t.Fatalf("승인이 취소되어야 함: %v", err)
	}
	if st, _ := e.monthClose.Status(as(uAuditor), "2026-11"); st.State != domain.MonthReleasePending && st.State != domain.MonthClosed {
		t.Fatalf("상태: %+v", st)
	}
	if n := count(t, e.db, `SELECT COUNT(*) FROM month_closes WHERE month='2026-11' AND status='closed'`); n != 1 {
		t.Fatal("11월이 열려 버림")
	}
	if e.hasAudit("month_close.release") {
		t.Fatal("취소된 승인의 해제 감사가 남음")
	}
}
