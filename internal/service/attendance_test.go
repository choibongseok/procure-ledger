package service_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/contract/fake"
	"github.com/choibongseok/procure-ledger/internal/service"
)

// B50 5단계: 체크인·체크아웃(서버 시각), 정정(요청·승인·직접·본인 승인 금지), 근태 마감, 근무 유형, 주 52시간 경고, 출근율 80% 판정.

type attEnv struct {
	*env
	svc   *service.AttendanceService
	leave *leaveEnv
}

func (e *env) attendance(t *testing.T) *attEnv {
	t.Helper()
	l := e.leave(t)
	b := service.Base{DB: e.db, Txs: contract.SQLTxManager{DB: e.db}, Audit: e.rec, Masker: fake.Masker{}, Org: leftAwareOrg{e.org}, Now: func() time.Time { return e.now }, Modules: l.gate, Privacy: l.noter}
	return &attEnv{env: e, svc: service.NewAttendanceService(b), leave: l}
}

// at 은 KST 시각으로 서버 시계를 맞춘다.
func (a *attEnv) at(date, hm string) {
	t, err := time.ParseInLocation("2006-01-02 15:04", date+" "+hm, contract.KST)
	if err != nil {
		panic(err)
	}
	a.now = t.UTC()
}

func attHR() context.Context { return hrCtx() }

func TestAttendanceCheckInOutServerTimeOnceADay(t *testing.T) {
	a := newEnv(t).attendance(t)
	// 출근 전 퇴근은 거부, 모듈이 꺼져 있으면 없는 것과 같다.
	a.at("2026-11-10", "09:05")
	if _, err := a.svc.CheckOut(as(uStaff)); !errors.Is(err, contract.ErrValidation) {
		t.Fatalf("출근 없이 퇴근: %v", err)
	}
	a.leave.gate.set(false)
	if _, err := a.svc.CheckIn(as(uStaff)); !errors.Is(err, contract.ErrNotFound) {
		t.Fatalf("꺼진 모듈: %v", err)
	}
	a.leave.gate.set(true)
	got, err := a.svc.CheckIn(as(uStaff))
	if err != nil || got.Today.CheckIn != "09:05" || !got.Today.Late || got.Today.CheckOut != "" {
		t.Fatalf("출근: %+v %v", got.Today, err)
	}
	if _, err := a.svc.CheckIn(as(uStaff)); !errors.Is(err, contract.ErrValidation) || !strings.Contains(err.Error(), "이미 출근") {
		t.Fatalf("두 번째 출근: %v", err)
	}
	a.at("2026-11-10", "18:30")
	got, err = a.svc.CheckOut(as(uStaff))
	if err != nil || got.Today.CheckOut != "18:30" || got.Today.WorkMinutes != 505 || got.Today.Late != true || got.Today.EarlyLeave {
		t.Fatalf("퇴근: %+v %v", got.Today, err)
	}
	if _, err := a.svc.CheckOut(as(uStaff)); !errors.Is(err, contract.ErrValidation) || !strings.Contains(err.Error(), "이미 퇴근") {
		t.Fatalf("두 번째 퇴근: %v", err)
	}
	// 체크인·체크아웃은 감사가 아니라 기록 표가 정본이다(D5).
	if a.rec.Has("attendance.check_in", "attendance_record", "success") {
		t.Fatal("체크인이 감사에 남음")
	}
	var n int
	_ = a.db.QueryRow(`SELECT COUNT(*) FROM attendance_records WHERE user_id = ? AND work_date = '2026-11-10'`, uStaff).Scan(&n)
	if n != 1 {
		t.Fatalf("기록 행 %d", n)
	}
	// 기록 행은 지울 수 없다.
	if _, err := a.db.Exec(`DELETE FROM attendance_records`); err == nil {
		t.Fatal("기록 삭제가 막히지 않음")
	}
	// 다음 날 첫 체크인에 어제 퇴근 누락 안내가 나온다.
	a.at("2026-11-11", "09:00")
	if _, err := a.db.Exec(`UPDATE attendance_records SET check_out_at = '' WHERE work_date = '2026-11-10'`); err != nil {
		t.Fatal(err)
	}
	got, err = a.svc.CheckIn(as(uStaff))
	if err != nil || !strings.Contains(got.MissingYesterday, "퇴근 기록이 없습니다") {
		t.Fatalf("누락 안내: %+v %v", got, err)
	}
	// 지난 날 출근만 있는 기록은 "기록 누락"으로 표시되고 주 합계에 넣지 않는다.
	for _, d := range got.Week.Days {
		if d.Date == "2026-11-10" && (!d.Missing || d.WorkMinutes != 0) {
			t.Fatalf("누락 표시: %+v", d)
		}
	}
}

func TestAttendanceCorrectionRequestApproveRejectAndSelfApprovalBlocked(t *testing.T) {
	a := newEnv(t).attendance(t)
	a.at("2026-11-10", "10:00")
	// 기록이 없는 날 정정 요청: 사유 필수, 시각 형식, 미래 거부.
	for name, in := range map[string]domain.CorrectionInput{
		"사유 없음": {WorkDate: "2026-11-09", CheckIn: "09:00", CheckOut: "18:00"},
		"시각 없음": {WorkDate: "2026-11-09", Reason: "x"},
		"형식":    {WorkDate: "2026-11-09", CheckIn: "9시", Reason: "x"},
		"미래":    {WorkDate: "2026-11-12", CheckIn: "09:00", CheckOut: "18:00", Reason: "x"},
		"역전":    {WorkDate: "2026-11-09", CheckIn: "18:00", CheckOut: "09:00", Reason: "x"},
	} {
		if _, err := a.svc.RequestCorrection(as(uStaff), in); !errors.Is(err, contract.ErrValidation) {
			t.Errorf("%s: %v", name, err)
		}
	}
	c, err := a.svc.RequestCorrection(as(uStaff), domain.CorrectionInput{WorkDate: "2026-11-09", CheckIn: "09:00", CheckOut: "18:00", Reason: "퇴근 기록을 놓쳤습니다"})
	if err != nil || c.Status != domain.CorrectionRequested || c.NewIn == "" {
		t.Fatalf("요청: %+v %v", c, err)
	}
	if _, err := a.svc.RequestCorrection(as(uStaff), domain.CorrectionInput{WorkDate: "2026-11-09", CheckIn: "09:10", Reason: "again"}); !errors.Is(err, contract.ErrValidation) {
		t.Fatalf("같은 날 중복 요청: %v", err)
	}
	// 승인 대기 중에는 기록이 바뀌지 않는다. 본인 승인은 금지, 범위 밖 사람은 요청이 없는 것과 같다.
	if w, _ := a.svc.WeekOf(as(uStaff), 0, "2026-11-09"); w.Days[0].CheckIn != "" || w.Days[0].CorrectionID != c.ID {
		t.Fatalf("대기 중 기록: %+v", w.Days[0])
	}
	if _, err := a.svc.DecideCorrection(as(uStaff), c.ID, true, ""); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("본인 승인: %v", err)
	}
	if !a.rec.Has("attendance.correct", "attendance_record", "denied") {
		t.Fatal("본인 승인 거부 감사 없음")
	}
	if _, err := a.svc.DecideCorrection(as(uStaff2), c.ID, true, ""); !errors.Is(err, contract.ErrForbidden) && !errors.Is(err, contract.ErrNotFound) {
		t.Fatalf("범위 밖 승인: %v", err)
	}
	mine, toDecide, err := a.svc.Corrections(as(uLead))
	if err != nil || len(mine) != 0 || len(toDecide) != 1 || toDecide[0].ID != c.ID {
		t.Fatalf("팀장의 대기 목록: %+v %+v %v", mine, toDecide, err)
	}
	if _, td, _ := a.svc.Corrections(as(uStaff2)); len(td) != 0 {
		t.Fatalf("범위 밖 사람의 대기 목록: %+v", td)
	}
	done, err := a.svc.DecideCorrection(as(uLead), c.ID, true, "확인")
	if err != nil || done.Status != domain.CorrectionApplied || done.ApprovedBy != uLead {
		t.Fatalf("승인: %+v %v", done, err)
	}
	w, _ := a.svc.WeekOf(as(uStaff), 0, "2026-11-09")
	if d := w.Days[0]; d.CheckIn != "09:00" || d.CheckOut != "18:00" || d.WorkMinutes != 480 || d.Source != "correction" {
		t.Fatalf("반영된 기록: %+v", d)
	}
	if _, err := a.svc.DecideCorrection(as(uLead), c.ID, false, ""); !errors.Is(err, contract.ErrInvalidState) {
		t.Fatalf("두 번 처리: %v", err)
	}
	if !a.rec.Has("attendance.correct", "attendance_record", "success") || !a.rec.Has("attendance.correct_request", "attendance_record", "success") {
		t.Fatal("정정 감사 없음")
	}
	// 정정 줄은 추가 전용이다(지울 수 없고 신청 내용은 고칠 수 없다).
	if _, err := a.db.Exec(`DELETE FROM attendance_corrections`); err == nil {
		t.Fatal("정정 줄 삭제가 막히지 않음")
	}
	if _, err := a.db.Exec(`UPDATE attendance_corrections SET reason = 'x' WHERE id = ?`, c.ID); err == nil {
		t.Fatal("정정 사유 수정이 막히지 않음")
	}
	// 반려: 기록은 그대로다.
	r, err := a.svc.RequestCorrection(as(uStaff), domain.CorrectionInput{WorkDate: "2026-11-09", CheckOut: "19:00", Reason: "야근"})
	if err != nil {
		t.Fatal(err)
	}
	if d, err := a.svc.DecideCorrection(as(uLead), r.ID, false, "근거 없음"); err != nil || d.Status != domain.CorrectionRejected {
		t.Fatalf("반려: %+v %v", d, err)
	}
	if w, _ := a.svc.WeekOf(as(uStaff), 0, "2026-11-09"); w.Days[0].CheckOut != "18:00" {
		t.Fatalf("반려 뒤 기록: %+v", w.Days[0])
	}
}

func TestAttendanceDirectCorrectCloseAndReopen(t *testing.T) {
	a := newEnv(t).attendance(t)
	a.at("2026-11-10", "10:00")
	in := domain.CorrectionInput{UserID: uStaff, WorkDate: "2026-10-15", CheckIn: "09:00", CheckOut: "18:00", Reason: "출입 기록으로 확인"}
	// 인사 권한이 없으면 직접 정정 불가, 본인 기록도 불가.
	if _, err := a.svc.DirectCorrect(as(uStaff), in); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("직원의 직접 정정: %v", err)
	}
	self := in
	self.UserID = uFinance2
	if _, err := a.svc.DirectCorrect(attHR(), self); !errors.Is(err, contract.ErrValidation) || !strings.Contains(err.Error(), "본인의 기록") {
		t.Fatalf("본인 기록 직접 정정: %v", err)
	}
	c, err := a.svc.DirectCorrect(attHR(), in)
	if err != nil || c.Status != domain.CorrectionApplied || c.ApprovedBy != uFinance2 {
		t.Fatalf("직접 정정: %+v %v", c, err)
	}
	if w, _ := a.svc.WeekOf(as(uStaff), 0, "2026-10-15"); w.Days[3].Source != "admin" || w.Days[3].CheckIn != "09:00" {
		t.Fatalf("직접 정정 기록: %+v", w.Days[3])
	}
	// 마감: 끝나지 않은 달은 거부, 지난달은 마감되고 정정·소급이 막힌다. 해제는 사유 필수.
	if err := a.svc.CloseMonth(attHR(), "2026-11"); !errors.Is(err, contract.ErrValidation) {
		t.Fatalf("끝나지 않은 달 마감: %v", err)
	}
	if err := a.svc.CloseMonth(as(uLead), "2026-10"); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("권한 없는 마감: %v", err)
	}
	if err := a.svc.CloseMonth(attHR(), "2026-10"); err != nil {
		t.Fatal(err)
	}
	if err := a.svc.CloseMonth(attHR(), "2026-10"); !errors.Is(err, contract.ErrInvalidState) {
		t.Fatalf("두 번 마감: %v", err)
	}
	if _, err := a.svc.RequestCorrection(as(uStaff), domain.CorrectionInput{WorkDate: "2026-10-16", CheckIn: "09:00", Reason: "x"}); !errors.Is(err, contract.ErrValidation) || !strings.Contains(err.Error(), "마감된 달") {
		t.Fatalf("마감된 달 정정 요청: %v", err)
	}
	if _, err := a.svc.DirectCorrect(attHR(), domain.CorrectionInput{UserID: uStaff, WorkDate: "2026-10-16", CheckIn: "09:00", Reason: "x"}); !errors.Is(err, contract.ErrValidation) {
		t.Fatalf("마감된 달 직접 정정: %v", err)
	}
	// 마감된 달의 소급 휴가는 인사 담당만 신청한다.
	a.leave.policy(t)
	a.leave.open(t, uStaff, 5)
	late := domain.LeaveInput{LeaveCode: "annual", StartDate: "2026-10-20", EndDate: "2026-10-20", Reason: "사후 신청"}
	if _, err := a.leave.svc.Request(as(uStaff), late); !errors.Is(err, contract.ErrValidation) || !strings.Contains(err.Error(), "마감된 달") {
		t.Fatalf("마감된 달 소급 휴가: %v", err)
	}
	cs, err := a.svc.Closes(attHR())
	if err != nil || len(cs) != 1 || !cs[0].Closed {
		t.Fatalf("마감 현황: %+v %v", cs, err)
	}
	if err := a.svc.ReopenMonth(attHR(), "2026-10", " "); !errors.Is(err, contract.ErrValidation) {
		t.Fatalf("사유 없는 해제: %v", err)
	}
	if err := a.svc.ReopenMonth(attHR(), "2026-10", "정정 필요"); err != nil {
		t.Fatal(err)
	}
	if err := a.svc.ReopenMonth(attHR(), "2026-10", "again"); !errors.Is(err, contract.ErrInvalidState) {
		t.Fatalf("두 번 해제: %v", err)
	}
	if _, err := a.svc.RequestCorrection(as(uStaff), domain.CorrectionInput{WorkDate: "2026-10-16", CheckIn: "09:00", Reason: "x"}); err != nil {
		t.Fatalf("해제 뒤 정정 요청: %v", err)
	}
	if !a.rec.Has("attendance.close", "attendance_record", "success") || !a.rec.Has("attendance.reopen", "attendance_record", "success") {
		t.Fatal("마감 감사 없음")
	}
	// 해제 뒤 다시 마감할 수 있다.
	if err := a.svc.CloseMonth(attHR(), "2026-10"); err != nil {
		t.Fatalf("재마감: %v", err)
	}
}

func TestAttendanceWorkTypesAndAssignment(t *testing.T) {
	a := newEnv(t).attendance(t)
	a.at("2026-11-10", "10:00")
	if _, err := a.svc.SaveWorkType(as(uStaff), 0, domain.WorkTypeInput{Code: "early", Name: "조기", Kind: "fixed", StartTime: "08:00", EndTime: "17:00", Active: true}); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("직원의 유형 저장: %v", err)
	}
	for name, in := range map[string]domain.WorkTypeInput{
		"코드":      {Code: "Early", Name: "x", Kind: "fixed", StartTime: "08:00", EndTime: "17:00"},
		"default": {Code: "default", Name: "x", Kind: "fixed", StartTime: "08:00", EndTime: "17:00"},
		"구분":      {Code: "early", Name: "x", Kind: "weird"},
		"시각 역전":   {Code: "early", Name: "x", Kind: "fixed", StartTime: "17:00", EndTime: "08:00"},
		"코어 없음":   {Code: "flex", Name: "x", Kind: "flexible"},
		"휴게 한쪽":   {Code: "early", Name: "x", Kind: "fixed", StartTime: "08:00", EndTime: "17:00", BreakStart: "12:00"},
	} {
		if _, err := a.svc.SaveWorkType(as(uAdmin), 0, in); !errors.Is(err, contract.ErrValidation) {
			t.Errorf("%s: %v", name, err)
		}
	}
	early, err := a.svc.SaveWorkType(as(uAdmin), 0, domain.WorkTypeInput{Code: "early", Name: "조기 근무", Kind: "fixed", StartTime: "08:00", EndTime: "17:00", BreakStart: "12:00", BreakEnd: "13:00", Active: true})
	if err != nil || early.ID == 0 || early.WeeklyMinutes != 2400 {
		t.Fatalf("유형 추가: %+v %v", early, err)
	}
	if _, err := a.svc.SaveWorkType(as(uAdmin), 0, domain.WorkTypeInput{Code: "early", Name: "중복", Kind: "fixed", StartTime: "08:00", EndTime: "17:00"}); !errors.Is(err, contract.ErrValidation) {
		t.Fatalf("코드 중복: %v", err)
	}
	ts, _ := a.svc.WorkTypes(as(uStaff))
	if len(ts) != 2 || !ts[0].Default || ts[1].Code != "early" {
		t.Fatalf("목록: %+v", ts)
	}
	// 배정: 인사 담당만, 효력일 이력, 같은 날 중복 거부. 배정 전에는 기본 유형(9시), 배정 뒤에는 8시 기준으로 판정한다.
	if err := a.svc.Assign(as(uStaff), uStaff, early.ID, "2026-11-10"); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("직원의 배정: %v", err)
	}
	if err := a.svc.Assign(attHR(), uStaff, early.ID, "2026-11-10"); err != nil {
		t.Fatal(err)
	}
	if err := a.svc.Assign(attHR(), uStaff, early.ID, "2026-11-10"); !errors.Is(err, contract.ErrValidation) {
		t.Fatalf("같은 효력일 배정: %v", err)
	}
	if err := a.svc.Assign(attHR(), uStaff, 99999, "2026-11-11"); !errors.Is(err, contract.ErrValidation) {
		t.Fatalf("없는 유형 배정: %v", err)
	}
	a.at("2026-11-10", "08:30")
	got, err := a.svc.CheckIn(as(uStaff))
	if err != nil || !got.Today.Late || got.Week.WorkTypeName != "조기 근무" {
		t.Fatalf("조기 유형 지각 판정: %+v %v", got.Today, err)
	}
	if _, err := a.db.Exec(`UPDATE work_assignments SET valid_from = '2020-01-01'`); err == nil {
		t.Fatal("배정 이력 수정이 막히지 않음")
	}
	as2, err := a.svc.Assignments(attHR(), uStaff)
	if err != nil || len(as2) != 1 {
		t.Fatalf("배정 이력: %+v %v", as2, err)
	}
	if !a.rec.Has("attendance.assign", "attendance_record", "success") || !a.rec.Has("attendance.policy_set", "leave_policy", "success") {
		t.Fatal("근무 유형 감사 없음")
	}
}

// seedRecord 는 기록 표에 직접 하루를 넣는다(주 합계·출근율 시험용).
func (a *attEnv) seedRecord(t *testing.T, user int64, date, in, out string) {
	t.Helper()
	ts := func(hm string) string {
		if hm == "" {
			return ""
		}
		x, _ := time.ParseInLocation("2006-01-02 15:04", date+" "+hm, contract.KST)
		return x.UTC().Format(time.RFC3339)
	}
	if _, err := a.db.Exec(`INSERT INTO attendance_records (entity_id, user_id, work_date, check_in_at, check_out_at, source, created_at, updated_at) VALUES (1,?,?,?,?,'web','t','t')`, user, date, ts(in), ts(out)); err != nil {
		t.Fatalf("기록 시드: %v", err)
	}
}

func (a *attEnv) notes(t *testing.T, user int64, kind string) int {
	return len(a.env.notes(t, user, kind))
}

func TestAttendanceWeek52HoursWarningOncePerLevelAndDoesNotBlock(t *testing.T) {
	a := newEnv(t).attendance(t)
	// 월~목 12시간씩 48시간(휴게 제외). 아직 금요일 전이라 알림은 없다.
	for _, d := range []string{"2026-11-09", "2026-11-10", "2026-11-11", "2026-11-12"} {
		a.seedRecord(t, uStaff, d, "08:00", "21:00")
	}
	a.at("2026-11-13", "09:00")
	if _, err := a.svc.CheckIn(as(uStaff)); err != nil {
		t.Fatal(err)
	}
	if a.notes(t, uStaff, "attendance_week_caution") != 0 {
		t.Fatal("퇴근 전에 알림")
	}
	// 금요일 09:00~10:00 = 1시간 → 49시간: 주의 한 번.
	a.at("2026-11-13", "10:00")
	got, err := a.svc.CheckOut(as(uStaff))
	if err != nil || got.Week.TotalMinutes != 49*60 || got.Week.Level != domain.WeekCaution {
		t.Fatalf("49시간: %+v %v", got.Week, err)
	}
	if a.notes(t, uStaff, "attendance_week_caution") != 1 || a.notes(t, uStaff, "attendance_week_over") != 0 {
		t.Fatal("주의 알림 수")
	}
	// 정정으로 시간이 늘어 52시간을 넘으면 경고가 새로 나가지만, 주의는 다시 나가지 않는다. 막지 않는다(체크아웃·정정이 성공).
	a.at("2026-11-13", "20:00")
	if _, err := a.svc.RequestCorrection(as(uStaff), domain.CorrectionInput{WorkDate: "2026-11-13", CheckOut: "18:00", Reason: "퇴근 시각 정정"}); err != nil {
		t.Fatalf("정정이 막힘: %v", err)
	}
	if _, err := a.svc.RunWarnings(context.Background()); err != nil {
		t.Fatal(err)
	}
	c, _, _ := a.svc.Corrections(as(uStaff))
	if _, err := a.svc.DecideCorrection(as(uLead), c[0].ID, true, ""); err != nil {
		t.Fatal(err)
	}
	w, _ := a.svc.WeekOf(as(uStaff), 0, "2026-11-13")
	if w.TotalMinutes != 56*60 || w.Level != domain.WeekOver {
		t.Fatalf("정정 뒤 주 합계: %+v", w)
	}
	// 승인으로 52시간을 넘으면 경고가 새로 한 번 나가고, 이미 나간 주의는 다시 나가지 않는다(주·단계 유일 키).
	if a.notes(t, uStaff, "attendance_week_caution") != 1 || a.notes(t, uStaff, "attendance_week_over") != 1 {
		t.Fatalf("주의 %d 경고 %d", a.notes(t, uStaff, "attendance_week_caution"), a.notes(t, uStaff, "attendance_week_over"))
	}
	var cnt int
	_ = a.db.QueryRow(`SELECT COUNT(*) FROM attendance_warnings WHERE user_id = ? AND week_start = '2026-11-09'`, uStaff).Scan(&cnt)
	if cnt != 2 {
		t.Fatalf("경고 기록 %d(주의·경고 2건이어야 함)", cnt)
	}
	// 휴가는 근로시간에 더하지 않는다: 같은 주에 휴가가 있어도 합계는 기록의 합이다.
	if w.TotalMinutes != 56*60 {
		t.Fatal("합계가 변함")
	}
}

func TestAttendanceCheckoutWarnsOverOnce(t *testing.T) {
	a := newEnv(t).attendance(t)
	for _, d := range []string{"2026-11-09", "2026-11-10", "2026-11-11", "2026-11-12"} {
		a.seedRecord(t, uStaff, d, "08:00", "21:00")
	}
	a.at("2026-11-13", "09:00")
	if _, err := a.svc.CheckIn(as(uStaff)); err != nil {
		t.Fatal(err)
	}
	a.at("2026-11-13", "14:30") // 5.5시간 - 휴게 1시간 = 4.5시간 → 52.5시간
	got, err := a.svc.CheckOut(as(uStaff))
	if err != nil || got.Week.Level != domain.WeekOver {
		t.Fatalf("퇴근: %+v %v", got.Week, err)
	}
	if a.notes(t, uStaff, "attendance_week_caution") != 1 || a.notes(t, uStaff, "attendance_week_over") != 1 {
		t.Fatalf("주의 %d 경고 %d", a.notes(t, uStaff, "attendance_week_caution"), a.notes(t, uStaff, "attendance_week_over"))
	}
	if !strings.Contains(a.env.notes(t, uStaff, "attendance_week_over")[0].Body, "52시간") {
		t.Fatal("경고 문구")
	}
}

func TestAttendanceRateCutsAnniversaryGrant(t *testing.T) {
	// 입사 2024-03-15, 2026-03-20 에 발생을 따라잡는다. 기록은 2025-03-14(금)부터 시작한다: 근속 1년(2025-03-15) 기준일은 기록 도입 전이라 15일 그대로이고,
	// 근속 2년(2026-03-15)은 직전 1년의 출근율로 판정한다.
	build := func(t *testing.T, fullYear bool) int64 {
		a := newEnv(t).attendance(t)
		first := stdPolicy
		first.EffectiveFrom = "2025-01-01"
		if _, err := a.leave.svc.SavePolicy(as(uAdmin), first); err != nil {
			t.Fatal(err)
		}
		a.hire(t, uStaff, "2024-03-15")
		a.now = time.Date(2026, 3, 20, 3, 0, 0, 0, time.UTC)
		end := "2026-03-14"
		if !fullYear {
			end = "2025-08-31" // 3월 15일~8월 말까지만 출근(약 50%): 개근한 달은 4~8월 5개월
		}
		for d, _ := time.Parse("2006-01-02", "2025-03-14"); !d.After(mustDay(end)); d = d.AddDate(0, 0, 1) {
			if d.Weekday() == time.Saturday || d.Weekday() == time.Sunday {
				continue
			}
			a.seedRecord(t, uStaff, d.Format("2006-01-02"), "09:00", "18:00")
		}
		if _, err := a.leave.svc.AccrueUser(context.Background(), uStaff, contract.DefaultEntityID, a.now); err != nil {
			t.Fatal(err)
		}
		var m int64
		_ = a.db.QueryRow(`SELECT COALESCE(SUM(minutes),0) FROM leave_ledger WHERE user_id = ? AND period_key = 'hire:y02'`, uStaff).Scan(&m)
		var y1 int64
		_ = a.db.QueryRow(`SELECT COALESCE(SUM(minutes),0) FROM leave_ledger WHERE user_id = ? AND period_key = 'hire:y01'`, uStaff).Scan(&y1)
		if y1 != 15*480 {
			t.Fatalf("기록 도입 전 기준일은 줄지 않아야 함: %d", y1)
		}
		return m
	}
	if got := build(t, true); got != 15*480 {
		t.Fatalf("출근율 충족: %d분", got)
	}
	if got := build(t, false); got != 5*480 {
		t.Fatalf("출근율 미달: %d분(기대 개근 5개월 = 5일)", got)
	}
}

func mustDay(s string) time.Time {
	d, err := time.Parse("2006-01-02", s)
	if err != nil {
		panic(fmt.Sprint(err))
	}
	return d
}
