package service_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
)

// B50 6단계: 부서 달력·팀 근태 현황·월 근태 내보내기. 열람 범위(AC-5040·5050)와 개인정보(AC-5042·5051)를 쌍으로 시험한다.
// 시험 환경의 조직: 구매1팀(팀장 uLead: 직원 uStaff 포함), 구매2팀, 영업1팀(uLead2)… uHead 는 구매본부 부서장(하위 팀 포함).

// approvedLeave 는 휴가 한 건을 신청 → 승인까지 진행한다.
func (a *attEnv) approvedLeave(t *testing.T, user, approver int64, code, from, to string) domain.LeaveRequest {
	t.Helper()
	r, err := a.leave.svc.Request(as(user), domain.LeaveInput{LeaveCode: code, StartDate: from, EndDate: to})
	if err != nil {
		t.Fatalf("신청(%s): %v", code, err)
	}
	decideAs(t, a.env, approver, r.DocID, true, "")
	return r
}

func TestAttendanceCalendarScopeAndSensitiveLabelsHidden(t *testing.T) {
	a := newEnv(t).attendance(t)
	a.leave.policy(t)
	a.leave.open(t, uStaff, 10)
	a.leave.open(t, uLead, 10)
	a.at("2026-11-10", "10:00")
	// 직원: 연차 11-16, 병가 11-18(증빙 필수라 올려서 신청). 팀장: 연차 11-17.
	a.approvedLeave(t, uStaff, uLead, "annual", "2026-11-16", "2026-11-16")
	up, err := a.leave.att.Upload(as(uStaff), "진단서.pdf", []byte("%PDF-1.4 x"))
	if err != nil {
		t.Fatal(err)
	}
	sick, err := a.leave.svc.Request(as(uStaff), domain.LeaveInput{LeaveCode: "sick", StartDate: "2026-11-18", EndDate: "2026-11-18", ProofAttachmentID: up.ID, Reason: "병명은 적지 않음"})
	if err != nil {
		t.Fatal(err)
	}
	decideAs(t, a.env, uLead, sick.DocID, true, "")
	a.approvedLeave(t, uLead, uHead, "annual", "2026-11-17", "2026-11-17")

	byName := func(c domain.AttCalendarView, name string) domain.AttCalendarPerson {
		for _, p := range c.People {
			if p.Name == name {
				return p
			}
		}
		return domain.AttCalendarPerson{}
	}
	// 본인은 자기 일정의 종류를 본다(병가도 병가로).
	mine, err := a.svc.Calendar(as(uStaff), "2026-11", 0)
	if err != nil {
		t.Fatal(err)
	}
	me := byName(mine, "직원")
	if len(me.Entries) != 2 || me.Entries[0].Label != "연차" || me.Entries[1].Label != "병가" {
		t.Fatalf("본인 일정: %+v", me.Entries)
	}
	// 같은 팀 동료는 직원의 일정을 "휴가"/"부재"로만 본다(종류·병가 숨김). 사유는 어디에도 없다.
	a.org.AddPerson(uFinance, "재무", "과장", unitBuy1, "", "", "") // 같은 팀 동료
	peer, err := a.svc.Calendar(as(uFinance), "2026-11", 0)
	if err != nil {
		t.Fatal(err)
	}
	pe := byName(peer, "직원")
	if len(pe.Entries) != 2 || pe.Entries[0].Label != "휴가" || pe.Entries[1].Label != "부재" || pe.Entries[0].Part != "" {
		t.Fatalf("동료가 보는 일정: %+v", pe.Entries)
	}
	for _, p := range peer.People {
		for _, e := range p.Entries {
			if strings.Contains(e.Label, "병가") || strings.Contains(e.Label, "연차") {
				t.Fatalf("동료에게 종류가 새어 나옴: %+v", e)
			}
		}
	}
	// 직속 조직장은 팀원의 종류를 본다. 다른 팀 사람은 달력에 없다.
	head, err := a.svc.Calendar(as(uLead), "2026-11", 0)
	if err != nil {
		t.Fatal(err)
	}
	if ls := byName(head, "직원").Entries; len(ls) != 2 || ls[1].Label != "병가" {
		t.Fatalf("조직장이 보는 일정: %+v", ls)
	}
	for _, p := range peer.People {
		if p.Name == "구매팀장2" || p.Name == "부서장" {
			t.Fatalf("다른 조직 사람이 달력에 있음: %s", p.Name)
		}
	}
	// 범위 밖 조직을 고르면 빈 결과이고(존재를 알리지 않는다), 모듈이 꺼지면 없는 것과 같다.
	out, err := a.svc.Calendar(as(uStaff), "2026-11", unitBuy2)
	if err != nil || len(out.People) != 0 {
		t.Fatalf("범위 밖 조직: %+v %v", out.People, err)
	}
	if _, err := a.svc.Calendar(as(uStaff), "2026-13", 0); !errors.Is(err, contract.ErrValidation) {
		t.Fatalf("잘못된 달: %v", err)
	}
	a.leave.gate.set(false)
	if _, err := a.svc.Calendar(as(uStaff), "2026-11", 0); !errors.Is(err, contract.ErrNotFound) {
		t.Fatalf("꺼진 모듈: %v", err)
	}
}

func TestAttendanceTeamStatusCountsOnlyInScope(t *testing.T) {
	a := newEnv(t).attendance(t)
	a.leave.policy(t)
	a.leave.open(t, uStaff, 10)
	a.at("2026-11-16", "09:20")
	if _, err := a.svc.CheckIn(as(uStaff)); err != nil { // 지각
		t.Fatal(err)
	}
	a.at("2026-11-10", "10:00")
	a.approvedLeave(t, uStaff, uLead, "annual", "2026-11-17", "2026-11-17")
	a.at("2026-11-16", "09:30")
	// 일반 직원은 팀 현황을 볼 수 없다.
	if _, err := a.svc.TeamStatus(as(uStaff), 0); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("직원의 팀 현황: %v", err)
	}
	st, err := a.svc.TeamStatus(as(uLead), 0)
	if err != nil {
		t.Fatal(err)
	}
	if st.Total == 0 || st.Present != 1 || st.Total != len(st.Rows) {
		t.Fatalf("팀장의 현황: %+v", st)
	}
	for _, r := range st.Rows {
		if r.Name == "직원" && (r.State != "출근" || !r.Late || r.CheckIn != "09:20") {
			t.Fatalf("직원 행: %+v", r)
		}
		if r.Name == "구매팀장2" { // 다른 팀
			t.Fatalf("범위 밖 사람이 표에 있음: %+v", r)
		}
	}
	// 합계는 표와 같은 범위다: 인원 수가 범위 밖 사람을 포함하지 않는다.
	if st.Present+st.NotYet+st.OnLeave > st.Total {
		t.Fatalf("합계가 범위를 넘음: %+v", st)
	}
	// 인사 담당은 전사 범위다(조직장 범위보다 넓다).
	all, err := a.svc.TeamStatus(attHR(), 0)
	if err != nil || all.Total <= st.Total || all.Scope != "전사" {
		t.Fatalf("인사 담당 현황: %+v %v", all, err)
	}
}

func TestAttendanceMonthReportColumnsPrivacyAndClosedFlag(t *testing.T) {
	a := newEnv(t).attendance(t)
	first := stdPolicy
	first.EffectiveFrom = "2026-01-01"
	if _, err := a.leave.svc.SavePolicy(as(uAdmin), first); err != nil {
		t.Fatal(err)
	}
	a.leave.open(t, uStaff, 10)
	a.at("2026-11-10", "10:00")
	// 직원: 10-05(월) 정시 근무, 10-06(화) 지각 30분 + 퇴근, 10-07 병가(승인), 10-08 연차 반차는 생략, 10-12~ 결근.
	a.seedRecord(t, uStaff, "2026-10-05", "09:00", "18:00")
	a.seedRecord(t, uStaff, "2026-10-06", "09:30", "18:30")
	a.at("2026-10-01", "09:00")
	up, _ := a.leave.att.Upload(as(uStaff), "진단서.pdf", []byte("%PDF-1.4 x"))
	sick, err := a.leave.svc.Request(as(uStaff), domain.LeaveInput{LeaveCode: "sick", StartDate: "2026-10-07", EndDate: "2026-10-07", ProofAttachmentID: up.ID, Reason: "비밀 사유"})
	if err != nil {
		t.Fatal(err)
	}
	decideAs(t, a.env, uLead, sick.DocID, true, "")
	a.at("2026-11-10", "10:00")
	// 권한 쌍: 직원·팀장은 내보낼 수 없고 인사 담당(내보내기 권한 포함)은 된다.
	if _, err := a.svc.MonthReport(as(uStaff), "2026-10"); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("직원의 내보내기: %v", err)
	}
	if _, err := a.svc.MonthReport(as(uLead), "2026-10"); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("팀장의 내보내기: %v", err)
	}
	if _, err := a.svc.MonthReport(attHR(), "2026-13"); !errors.Is(err, contract.ErrValidation) {
		t.Fatalf("잘못된 달: %v", err)
	}
	rep, err := a.svc.MonthReport(attHR(), "2026-10")
	if err != nil || rep.Closed {
		t.Fatalf("미마감 월: %+v %v", rep.Closed, err)
	}
	var row domain.AttMonthRow
	for _, r := range rep.Rows {
		if r.Name == "직원" {
			row = r
		}
	}
	if row.Name == "" || row.AttendDays != 2 || row.Late != 1 || row.WorkMinutes != 480+480 || row.SickDays != 1 || row.WorkDays == 0 {
		t.Fatalf("직원 월 집계: %+v", row)
	}
	// 결근 = 소정 근무일 - 출근 - 휴가(오늘 이전만). 10월 영업일 22일 중 출근 2·병가 1.
	if row.AbsentDays != row.WorkDays-3 {
		t.Fatalf("결근 %d (소정 %d)", row.AbsentDays, row.WorkDays)
	}
	if len(a.leave.noter.got) == 0 || a.leave.noter.got[len(a.leave.noter.got)-1].Category != "employee_no" || !a.leave.noter.got[len(a.leave.noter.got)-1].Export {
		t.Fatalf("사번 열람 기록: %+v", a.leave.noter.got)
	}
	// 사유·증빙 문구가 집계 결과에 새지 않는다(AC-5042 스윕).
	if s := rowText(rep); strings.Contains(s, "비밀 사유") || strings.Contains(s, "진단서") {
		t.Fatalf("사유·증빙이 내보내기에 있음: %s", s)
	}
	// 열람 기록을 남기지 못하면 내보내지 않는다(fail-closed).
	a.leave.noter.fail = errors.New("기록 실패")
	if _, err := a.svc.MonthReport(attHR(), "2026-10"); err == nil {
		t.Fatal("기록 실패인데 집계를 내줌")
	}
	a.leave.noter.fail = nil
	// 마감된 달은 확정 표시가 된다.
	if err := a.svc.CloseMonth(attHR(), "2026-10"); err != nil {
		t.Fatal(err)
	}
	if rep, _ := a.svc.MonthReport(attHR(), "2026-10"); !rep.Closed {
		t.Fatal("마감 표시 없음")
	}
	if n, err := a.svc.MonthScopeCount(attHR()); err != nil || n == 0 {
		t.Fatalf("대상 인원: %d %v", n, err)
	}
}

func rowText(rep domain.AttMonthReport) string {
	var b strings.Builder
	for _, r := range rep.Rows {
		b.WriteString(r.EmployeeNo + r.Name + r.Unit + r.WorkType)
	}
	return b.String()
}

// 소멸 예정 알림: 30일 안에 소멸하는 발생 줄이 있으면 그 줄에 대해 한 번만 알린다(재실행해도 중복 없음).
func TestLeaveExpiryNoticeOncePerGrant(t *testing.T) {
	a := newEnv(t).attendance(t)
	a.leave.policy(t)
	for _, in := range []domain.LeaveAdjustInput{
		{UserID: uStaff, Balance: "annual", Kind: "opening", Minutes: 3 * 480, ExpiresOn: "2026-11-30", Reason: "초기 이관"},
		{UserID: uStaff, Balance: "annual", Kind: "opening", Minutes: 2 * 480, ExpiresOn: "2027-06-30", Reason: "초기 이관"},
	} {
		if err := a.leave.svc.Adjust(hrCtx(), in); err != nil {
			t.Fatal(err)
		}
	}
	n, err := a.leave.svc.NotifyExpiring(context.Background(), uStaff, contract.DefaultEntityID, a.now)
	if err != nil || n != 1 {
		t.Fatalf("알림 수 %d %v", n, err)
	}
	if n, err = a.leave.svc.NotifyExpiring(context.Background(), uStaff, contract.DefaultEntityID, a.now); err != nil || n != 0 {
		t.Fatalf("재실행 알림 수 %d %v", n, err)
	}
	notes := a.env.notes(t, uStaff, "leave_expiring")
	if len(notes) != 1 || !strings.Contains(notes[0].Body, "3일") || !strings.Contains(notes[0].Body, "2026-11-30") {
		t.Fatalf("알림: %+v", notes)
	}
}
