package service

import (
	"context"
	"errors"
	"fmt"
	"log"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/repository"
)

// 출퇴근 기록·정정·근태 마감·근무 유형·주 근로시간 경고(B50 5단계, 명세 7절·9.4).
// 체크인·체크아웃은 서버 시각만 쓰고 감사에는 남기지 않는다(D5: 기록 표가 정본). 정정·마감·근무 유형 변경은 같은 트랜잭션에서 감사에 남긴다.

// AttendanceService 는 domain.AttendanceService 의 구현이다.
type AttendanceService struct {
	Base
	Repo  repository.SQLAttendance
	Leave repository.SQLLeave
}

var _ domain.AttendanceService = (*AttendanceService)(nil)

// NewAttendanceService 는 서비스를 만든다.
func NewAttendanceService(b Base) *AttendanceService { return &AttendanceService{Base: b} }

func (s *AttendanceService) requireModule(ctx context.Context) error {
	if !s.leaveModuleOn(ctx) {
		return contract.ErrNotFound
	}
	return nil
}

func (s *AttendanceService) leaveSvc() *LeaveService { return leaveService(s.Base) }

const msgClosedMonth = "마감된 달의 기록은 고칠 수 없습니다. 인사 담당에게 마감 해제를 요청하세요."

func (s *AttendanceService) kstToday() string { return string(contract.DayOf(s.now())) }

// guardMonth 는 근무일이 속한 달이 근태 마감됐으면 거부한다(정정·소급 입력의 한 곳).
func (s *AttendanceService) guardMonth(ctx context.Context, q contract.Tx, entity int64, date string) error {
	if len(date) < 7 {
		return nil
	}
	closed, err := s.Repo.MonthClosed(ctx, q, entity, date[:7])
	if err != nil {
		return err
	}
	if closed {
		return validation("%s", msgClosedMonth)
	}
	return nil
}

func (s *AttendanceService) workTypeOn(ctx context.Context, q contract.Tx, userID int64, date string) (domain.WorkType, error) {
	wt, ok, err := s.Repo.WorkTypeOn(ctx, q, userID, date)
	if err != nil {
		return wt, err
	}
	if !ok {
		return defaultWorkType(), nil
	}
	return wt, nil
}

func (s *AttendanceService) userName(ctx context.Context, q contract.Tx, id int64) string {
	var n string
	_ = q.QueryRowContext(ctx, `SELECT name FROM users WHERE id = ?`, id).Scan(&n)
	return n
}

// buildDay 는 한 날의 기록과 판정을 만든다. rec 가 없으면 빈 날이다.
func (s *AttendanceService) buildDay(ctx context.Context, q contract.Tx, userID int64, date string, rec *repository.AttRecord, wt domain.WorkType, today string) (domain.AttendanceDay, error) {
	d := domain.AttendanceDay{Date: date}
	if biz, err := s.isBusinessDay(ctx, q, date); err != nil {
		return d, err
	} else if !biz {
		d.HolidayNote = "휴일"
	}
	onLeave, err := s.Repo.ApprovedLeaveOn(ctx, q, userID, date)
	if err != nil {
		return d, err
	}
	if onLeave {
		d.LeaveNote = "승인된 휴가"
	}
	if id, err := s.Repo.PendingOn(ctx, q, userID, date); err != nil {
		return d, err
	} else {
		d.CorrectionID = id
	}
	if rec == nil {
		return d, nil
	}
	in, out := parseInstant(rec.In), parseInstant(rec.Out)
	d.CheckIn, d.CheckOut, d.Source = clockText(rec.In), clockText(rec.Out), rec.Source
	d.Missing = !in.IsZero() && out.IsZero() && date < today
	d.WorkMinutes = workedMinutes(date, in, out, wt)
	if !onLeave { // 휴가 날은 지각·조퇴를 따지지 않는다
		j := judgeDay(wt, date, in, out)
		d.Late, d.EarlyLeave, d.CoreMiss = j.Late, j.EarlyLeave, j.CoreMiss
	}
	return d, nil
}

// weekOf 는 date 가 속한 주(월~일)의 기록·합계·경고 단계다. 범위 검사는 호출자가 한다.
func (s *AttendanceService) weekOf(ctx context.Context, q contract.Tx, userID int64, date string) (domain.AttendanceWeek, error) {
	var w domain.AttendanceWeek
	start, err := weekStartOf(date)
	if err != nil {
		return w, validation("날짜를 YYYY-MM-DD 로 입력하세요.")
	}
	st, _ := time.Parse("2006-01-02", start)
	end := st.AddDate(0, 0, 6).Format("2006-01-02")
	recs, err := s.Repo.RecordsBetween(ctx, q, userID, start, end)
	if err != nil {
		return w, err
	}
	byDate := map[string]*repository.AttRecord{}
	for i := range recs {
		byDate[recs[i].Date] = &recs[i]
	}
	w.WeekStart, w.WeekEnd = start, end
	w.CautionMinute, w.OverMinute = workHourLimits.CautionMinutes, workHourLimits.OverMinutes
	today := s.kstToday()
	for i := 0; i < 7; i++ {
		ds := st.AddDate(0, 0, i).Format("2006-01-02")
		wt, err := s.workTypeOn(ctx, q, userID, ds)
		if err != nil {
			return w, err
		}
		if i == 0 || ds == today {
			w.WorkTypeName = wt.Name
		}
		day, err := s.buildDay(ctx, q, userID, ds, byDate[ds], wt, today)
		if err != nil {
			return w, err
		}
		w.TotalMinutes += day.WorkMinutes
		if wt.Kind == domain.WorkFlexible {
			w.WeeklyTarget = wt.WeeklyMinutes
		}
		w.Days = append(w.Days, day)
	}
	w.Level = weekLevel(w.TotalMinutes)
	// 선택 근무: 주가 끝났는데 총량에 못 미치면 안내한다(경고가 아니다). 진행 중인 주에는 말하지 않는다.
	w.BelowTarget = w.WeeklyTarget > 0 && end < today && w.TotalMinutes < w.WeeklyTarget
	return w, nil
}

// WeekOf 는 한 사람의 주 기록이다. userID 0 은 본인. 범위 밖 사람은 없는 것과 같다.
func (s *AttendanceService) WeekOf(ctx context.Context, userID int64, day string) (domain.AttendanceWeek, error) {
	p, err := contract.Authorize(ctx, contract.PermAttendanceSelf)
	if err != nil {
		return domain.AttendanceWeek{}, err
	}
	if err := s.requireModule(ctx); err != nil {
		return domain.AttendanceWeek{}, err
	}
	if userID == 0 {
		userID = p.UserID
	}
	if ok, err := s.leaveSvc().canViewUser(ctx, p, userID); err != nil {
		return domain.AttendanceWeek{}, err
	} else if !ok {
		return domain.AttendanceWeek{}, contract.ErrNotFound
	}
	if day == "" {
		day = s.kstToday()
	}
	return s.weekOf(ctx, s.DB, userID, day)
}

// Today 는 내 근태 화면의 첫 줄(오늘 기록·이번 주·안내)이다.
func (s *AttendanceService) Today(ctx context.Context) (domain.AttendanceToday, error) {
	p, err := contract.Authorize(ctx, contract.PermAttendanceSelf)
	if err != nil {
		return domain.AttendanceToday{}, err
	}
	if err := s.requireModule(ctx); err != nil {
		return domain.AttendanceToday{}, err
	}
	return s.today(ctx, p.UserID)
}

func (s *AttendanceService) today(ctx context.Context, userID int64) (domain.AttendanceToday, error) {
	var out domain.AttendanceToday
	today := s.kstToday()
	week, err := s.weekOf(ctx, s.DB, userID, today)
	if err != nil {
		return out, err
	}
	out.Week = week
	for _, d := range week.Days {
		if d.Date == today {
			out.Today = d
		}
	}
	if out.Today.HolidayNote != "" {
		out.Warnings = append(out.Warnings, "오늘은 휴일입니다.")
	}
	if out.Today.LeaveNote != "" {
		out.Warnings = append(out.Warnings, "오늘은 승인된 휴가입니다.")
	}
	t, _ := time.Parse("2006-01-02", today)
	yday := t.AddDate(0, 0, -1).Format("2006-01-02")
	if rec, ok, err := s.Repo.Record(ctx, s.DB, userID, yday); err != nil {
		return out, err
	} else if ok && rec.In != "" && rec.Out == "" {
		out.MissingYesterday = fmt.Sprintf("어제(%s) 퇴근 기록이 없습니다. 정정 요청을 하세요.", t.AddDate(0, 0, -1).Format("1. 2."))
	}
	return out, nil
}

// directHeads 는 사람의 직속 조직장(자기 자신이면 위 조직으로 올린다)이다. 조직 조회는 트랜잭션 밖에서 한다.
func (s *AttendanceService) directHeads(ctx context.Context, userID int64) []int64 {
	if s.Org == nil {
		return nil
	}
	day := contract.DayOf(s.now())
	person, err := s.Org.Person(ctx, userID, day)
	if err != nil {
		return nil
	}
	unit := person.Primary.Unit.ID
	chain := []int64{unit}
	if anc, err := s.Org.Ancestors(ctx, unit, day); err == nil {
		for _, a := range anc {
			chain = append(chain, a.ID)
		}
	}
	for _, u := range chain {
		h, err := s.Org.Head(ctx, u, day)
		if err == nil && !h.Vacant && h.UserID != 0 && h.UserID != userID {
			return []int64{h.UserID}
		}
	}
	return nil
}

// recipients 는 정정 요청·경고를 받을 사람이다: 직속 조직장(없으면 인사 담당 전원). withHR 이면 인사 담당도 더한다.
func (s *AttendanceService) recipients(ctx context.Context, userID int64, withHR bool) []int64 {
	out := s.directHeads(ctx, userID)
	if withHR || len(out) == 0 {
		if hr, err := s.Repo.HRUsers(ctx, s.DB); err == nil {
			out = append(out, hr...)
		}
	}
	return out
}

func (s *AttendanceService) notifyAll(ctx context.Context, tx contract.Tx, ids []int64, skip int64, kind, title, body string, refID int64) error {
	seen := map[int64]bool{skip: true}
	for _, id := range ids {
		if id == 0 || seen[id] {
			continue
		}
		seen[id] = true
		if err := s.notify(ctx, tx, repository.NotificationInsert{UserID: id, Kind: kind, Title: title, Body: body, Link: "/attendance", RefType: "attendance", RefID: refID}); err != nil {
			return err
		}
	}
	return nil
}

// CheckIn 은 출근을 기록한다(서버 시각). 하루에 한 번이다.
func (s *AttendanceService) CheckIn(ctx context.Context) (domain.AttendanceToday, error) {
	p, err := contract.Authorize(ctx, contract.PermAttendanceSelf)
	if err != nil {
		return domain.AttendanceToday{}, err
	}
	if err := s.requireModule(ctx); err != nil {
		return domain.AttendanceToday{}, err
	}
	if err := s.canClockIn(ctx, p); err != nil {
		return domain.AttendanceToday{}, err
	}
	now := s.now()
	date := s.kstToday()
	entity := contract.WriteEntityID(ctx)
	err = s.write(ctx, p, "attendance.check_in", audit.TargetAttendanceRecord, 0, func(tx contract.Tx) error {
		_, ok, err := s.Repo.InsertRecord(ctx, tx, entity, p.UserID, date, now.Format(time.RFC3339), "", "web", now)
		if err != nil {
			return err
		}
		if !ok {
			return validation("이미 출근했습니다.")
		}
		return nil // 체크인은 양이 많아 감사가 아니라 기록 표가 정본이다(D5)
	})
	if err != nil {
		return domain.AttendanceToday{}, err
	}
	return s.today(ctx, p.UserID)
}

// canClockIn 은 체크인·체크아웃 대상인지 본다: 조직 기준 퇴사한 사람과 오늘 휴직 중인 사람(OrgPerson.OnLeave, B50 8.3 의 6)은 기록할 수 없다. 휴직 구분은 문구에 싣지 않는다.
func (s *AttendanceService) canClockIn(ctx context.Context, p contract.Principal) error {
	if s.Org == nil {
		return nil
	}
	person, err := s.Org.Person(ctx, p.UserID, contract.DayOf(s.now()))
	switch {
	case errors.Is(err, contract.ErrNotInOrg), errors.Is(err, contract.ErrNotFound):
		return nil // 조직에 등록되지 않은 사용자는 users 상태만 본다
	case err != nil:
		return err
	}
	if !person.Active {
		return validation("퇴사 처리된 사용자는 출퇴근을 기록할 수 없습니다.")
	}
	if person.OnLeave {
		return validation("휴직 중에는 출퇴근을 기록할 수 없습니다. 복직 처리 뒤에 기록하세요.")
	}
	return nil
}

// CheckOut 은 퇴근을 기록한다(서버 시각). 이번 주 근로시간이 기준을 넘으면 같은 트랜잭션에서 경고 알림을 남긴다(막지 않는다).
func (s *AttendanceService) CheckOut(ctx context.Context) (domain.AttendanceToday, error) {
	p, err := contract.Authorize(ctx, contract.PermAttendanceSelf)
	if err != nil {
		return domain.AttendanceToday{}, err
	}
	if err := s.requireModule(ctx); err != nil {
		return domain.AttendanceToday{}, err
	}
	if err := s.canClockIn(ctx, p); err != nil {
		return domain.AttendanceToday{}, err
	}
	now := s.now()
	date := s.kstToday()
	recipients := s.recipients(ctx, p.UserID, true)
	name := s.userName(ctx, s.DB, p.UserID)
	err = s.write(ctx, p, "attendance.check_out", audit.TargetAttendanceRecord, 0, func(tx contract.Tx) error {
		rec, ok, err := s.Repo.Record(ctx, tx, p.UserID, date)
		if err != nil {
			return err
		}
		if !ok || rec.In == "" {
			return validation("오늘 출근 기록이 없습니다. 출근을 먼저 기록하거나 정정 요청을 하세요.")
		}
		if rec.Out != "" {
			return validation("이미 퇴근했습니다.")
		}
		if _, err := s.Repo.SetCheckOut(ctx, tx, rec.ID, now.Format(time.RFC3339), now); err != nil {
			return err
		}
		return s.warnWeek(ctx, tx, p.UserID, name, date, recipients)
	})
	if err != nil {
		return domain.AttendanceToday{}, err
	}
	return s.today(ctx, p.UserID)
}

// warnWeek 는 date 가 속한 주의 근로시간이 경고 기준에 새로 닿았으면 알림을 남긴다(같은 주 같은 단계는 한 번). 기록·휴가·결재를 막지 않는다.
func (s *AttendanceService) warnWeek(ctx context.Context, tx contract.Tx, userID int64, name, date string, recipients []int64) error {
	w, err := s.weekOf(ctx, tx, userID, date)
	if err != nil {
		return err
	}
	if w.Level == domain.WeekOK {
		return nil
	}
	now := s.now()
	levels := []string{domain.WeekCaution}
	if w.Level == domain.WeekOver {
		levels = append(levels, domain.WeekOver)
	}
	for _, lv := range levels {
		fresh, err := s.Repo.InsertWarning(ctx, tx, userID, w.WeekStart, lv, now)
		if err != nil {
			return err
		}
		if !fresh {
			continue
		}
		h, m := w.TotalMinutes/60, w.TotalMinutes%60
		kind, title, body := "attendance_week_caution", "주 근로시간 주의", fmt.Sprintf("%s님의 이번 주 근로시간이 %d시간에 이르렀습니다(%d시간 %d분).", name, workHourLimits.CautionMinutes/60, h, m)
		if lv == domain.WeekOver {
			kind, title, body = "attendance_week_over", "주 근로시간 경고", fmt.Sprintf("%s님의 이번 주 근로시간이 %d시간을 넘었습니다(%d시간 %d분).", name, workHourLimits.OverMinutes/60, h, m)
		}
		ids := append([]int64{userID}, recipients...)
		if err := s.notifyAll(ctx, tx, ids, 0, kind, title, body, 0); err != nil {
			return err
		}
	}
	return nil
}

// RunWarnings 는 재직자 전원의 이번 주 근로시간을 평가해 새로 기준에 닿은 사람에게 알린다(정정으로 시간이 늘어난 경우도 잡는다). 사람마다 따로 처리한다.
func (s *AttendanceService) RunWarnings(ctx context.Context) (int, error) {
	if !s.leaveModuleOn(ctx) {
		return 0, nil
	}
	day := s.kstToday()
	emps, err := s.activeEmployees(ctx, day)
	if err != nil {
		return 0, err
	}
	n := 0
	for userID, entityID := range emps {
		ectx := contract.WithActiveEntity(ctx, contract.ActiveEntity{ID: entityID})
		w, err := s.weekOf(ectx, s.DB, userID, day)
		if err != nil {
			log.Printf("attendance: 주 근로시간 계산 실패(사용자 %d): %v", userID, err)
			continue
		}
		if w.Level == domain.WeekOK {
			continue
		}
		recipients := s.recipients(ectx, userID, true)
		name := s.userName(ectx, s.DB, userID)
		if err := s.Txs.WithTx(ectx, func(tx contract.Tx) error { return s.warnWeek(ectx, tx, userID, name, day, recipients) }); err != nil {
			log.Printf("attendance: 주 근로시간 경고 실패(사용자 %d): %v", userID, err)
			continue
		}
		n++
	}
	return n, nil
}

// activeEmployees 는 재직자 목록 조회(휴가 저장소의 같은 조회를 쓴다).
func (s *AttendanceService) activeEmployees(ctx context.Context, day string) (map[int64]int64, error) {
	return s.Leave.ActiveEmployees(ctx, s.DB, day)
}

// Run 은 서버가 켜져 있는 동안 기동 때 한 번, 그 뒤 한 시간마다 주 근로시간 경고를 평가한다.
func (s *AttendanceService) Run(ctx context.Context) {
	tick := func() {
		if _, err := s.RunWarnings(ctx); err != nil && ctx.Err() == nil {
			log.Printf("attendance: 주 근로시간 경고 작업 실패: %v", err)
		}
	}
	tick()
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			tick()
		}
	}
}

// ---- 정정 ----

var hmRe = regexp.MustCompile(`^([01][0-9]|2[0-3]):[0-5][0-9]$`)

// mergeTimes 는 정정 입력과 현재 기록으로 새 출근·퇴근 시각(저장용)을 만든다. 입력하지 않은 칸은 현재 값을 유지한다.
func (s *AttendanceService) mergeTimes(in domain.CorrectionInput, rec repository.AttRecord, found bool) (newIn, newOut string, err error) {
	if in.CheckIn == "" && in.CheckOut == "" {
		return "", "", validation("바꿀 출근 또는 퇴근 시각을 입력하세요.")
	}
	newIn, newOut = rec.In, rec.Out
	if !found {
		newIn, newOut = "", ""
	}
	for _, f := range []struct {
		hm  string
		dst *string
		nm  string
	}{{in.CheckIn, &newIn, "출근"}, {in.CheckOut, &newOut, "퇴근"}} {
		if f.hm == "" {
			continue
		}
		if !hmRe.MatchString(f.hm) {
			return "", "", validation("%s 시각을 HH:MM 형식으로 입력하세요.", f.nm)
		}
		v, _ := instantOf(in.WorkDate, f.hm)
		*f.dst = v
	}
	if newIn != "" && newOut != "" && !parseInstant(newOut).After(parseInstant(newIn)) {
		return "", "", validation("퇴근 시각이 출근 시각보다 늦어야 합니다.")
	}
	if newIn == "" && newOut != "" {
		return "", "", validation("출근 시각이 없는 기록에는 퇴근 시각만 넣을 수 없습니다. 출근 시각도 입력하세요.")
	}
	return newIn, newOut, nil
}

func (s *AttendanceService) checkCorrectionInput(in domain.CorrectionInput) (domain.CorrectionInput, error) {
	in.Reason = strings.TrimSpace(in.Reason)
	if n := utf8.RuneCountInString(in.Reason); n < 1 || n > 200 {
		return in, validation("정정 사유를 1~200자로 입력하세요.")
	}
	d, err := time.Parse("2006-01-02", in.WorkDate)
	if err != nil {
		return in, validation("정정할 날짜를 YYYY-MM-DD 로 입력하세요.")
	}
	if in.WorkDate > s.kstToday() {
		return in, validation("오늘 이후 날짜는 정정할 수 없습니다.")
	}
	if d.Before(time.Now().AddDate(-3, 0, 0)) {
		return in, validation("3년이 지난 기록은 정정할 수 없습니다.")
	}
	return in, nil
}

// RequestCorrection 은 본인이 출퇴근 정정을 요청한다(사유 필수). 직속 조직장(또는 인사 담당)이 승인한다.
func (s *AttendanceService) RequestCorrection(ctx context.Context, in domain.CorrectionInput) (domain.Correction, error) {
	p, err := s.authorize(ctx, contract.PermAttendanceSelf, "attendance.correct_request", audit.TargetAttendanceRecord)
	if err != nil {
		return domain.Correction{}, err
	}
	if err := s.requireModule(ctx); err != nil {
		return domain.Correction{}, err
	}
	if in, err = s.checkCorrectionInput(in); err != nil {
		return domain.Correction{}, err
	}
	recipients := s.recipients(ctx, p.UserID, false)
	name := s.userName(ctx, s.DB, p.UserID)
	entity := contract.WriteEntityID(ctx)
	var id int64
	err = s.write(ctx, p, "attendance.correct_request", audit.TargetAttendanceRecord, 0, func(tx contract.Tx) error {
		if err := s.guardMonth(ctx, tx, entity, in.WorkDate); err != nil {
			return err
		}
		if pend, err := s.Repo.PendingOn(ctx, tx, p.UserID, in.WorkDate); err != nil {
			return err
		} else if pend != 0 {
			return validation("이 날짜에 승인 대기 중인 정정 요청이 있습니다.")
		}
		rec, found, err := s.Repo.Record(ctx, tx, p.UserID, in.WorkDate)
		if err != nil {
			return err
		}
		newIn, newOut, err := s.mergeTimes(in, rec, found)
		if err != nil {
			return err
		}
		c := domain.Correction{UserID: p.UserID, WorkDate: in.WorkDate, OldIn: rec.In, OldOut: rec.Out, NewIn: newIn, NewOut: newOut, Reason: in.Reason, RequestedBy: p.UserID, Status: domain.CorrectionRequested}
		if id, err = s.Repo.InsertCorrection(ctx, tx, entity, c, s.now()); err != nil {
			return err
		}
		if err := s.notifyAll(ctx, tx, recipients, p.UserID, "attendance_correction", "출퇴근 정정 요청",
			fmt.Sprintf("%s님이 %s 출퇴근 정정을 요청했습니다.", name, in.WorkDate), id); err != nil {
			return err
		}
		return s.record(ctx, tx, p, "attendance.correct_request", audit.TargetAttendanceRecord, id, correctionDiff(c)...)
	})
	if err != nil {
		return domain.Correction{}, err
	}
	return s.Repo.CorrectionByID(ctx, s.DB, id)
}

func correctionDiff(c domain.Correction) []audit.DiffItem {
	return []audit.DiffItem{{Key: "work_date", New: c.WorkDate}, {Key: "check_in", Old: clockText(c.OldIn), New: clockText(c.NewIn)}, {Key: "check_out", Old: clockText(c.OldOut), New: clockText(c.NewOut)},
		{Key: "reason", New: c.Reason}}
}

// applyCorrection 은 정정 결과를 기록에 반영한다(트랜잭션 안). 요청 이후 기록이 바뀌었어도 요청이 바꾸려던 칸(출근·퇴근)만 덮는다.
func (s *AttendanceService) applyCorrection(ctx context.Context, tx contract.Tx, entity int64, c domain.Correction, source string) error {
	rec, found, err := s.Repo.Record(ctx, tx, c.UserID, c.WorkDate)
	if err != nil {
		return err
	}
	in, out := rec.In, rec.Out
	if c.NewIn != c.OldIn {
		in = c.NewIn
	}
	if c.NewOut != c.OldOut {
		out = c.NewOut
	}
	if in != "" && out != "" && !parseInstant(out).After(parseInstant(in)) {
		return fmt.Errorf("%w: 기록이 바뀌어 이 정정을 반영하면 퇴근이 출근보다 빨라집니다. 새로 요청하세요", contract.ErrInvalidState)
	}
	if !found {
		_, _, err := s.Repo.InsertRecord(ctx, tx, entity, c.UserID, c.WorkDate, in, out, source, s.now())
		return err
	}
	return s.Repo.ApplyTimes(ctx, tx, rec.ID, in, out, source, s.now())
}

// DecideCorrection 은 정정 요청을 승인하거나 반려한다. 직속 조직장(범위 안)이나 인사 담당만 하고 본인 요청은 본인이 처리할 수 없다.
func (s *AttendanceService) DecideCorrection(ctx context.Context, id int64, approve bool, note string) (domain.Correction, error) {
	p, err := s.authorize(ctx, contract.PermAttendanceSelf, "attendance.correct", audit.TargetAttendanceRecord)
	if err != nil {
		return domain.Correction{}, err
	}
	if err := s.requireModule(ctx); err != nil {
		return domain.Correction{}, err
	}
	note = strings.TrimSpace(note)
	if utf8.RuneCountInString(note) > 200 {
		return domain.Correction{}, validation("처리 의견은 200자 이내로 입력하세요.")
	}
	c, err := s.Repo.CorrectionByID(ctx, s.DB, id)
	if err != nil {
		return domain.Correction{}, err
	}
	if c.UserID == p.UserID {
		s.recordDenied(ctx, p, "attendance.correct", audit.TargetAttendanceRecord, id, "본인 정정 승인")
		return domain.Correction{}, fmt.Errorf("%w: 본인의 정정 요청은 본인이 처리할 수 없습니다. 직속 조직장이나 인사 담당이 처리합니다", contract.ErrForbidden)
	}
	if ok, err := s.leaveSvc().canViewUser(ctx, p, c.UserID); err != nil {
		return domain.Correction{}, err
	} else if !ok {
		return domain.Correction{}, contract.ErrNotFound
	}
	entity := contract.WriteEntityID(ctx)
	warnTo, warnName := s.recipients(ctx, c.UserID, true), s.userName(ctx, s.DB, c.UserID)
	err = s.write(ctx, p, "attendance.correct", audit.TargetAttendanceRecord, id, func(tx contract.Tx) error {
		if err := s.guardMonth(ctx, tx, entity, c.WorkDate); err != nil {
			return err
		}
		status := domain.CorrectionRejected
		if approve {
			status = domain.CorrectionApplied
		}
		changed, err := s.Repo.DecideCorrection(ctx, tx, id, status, p.UserID, note, s.now())
		if err != nil {
			return err
		}
		if !changed {
			return fmt.Errorf("%w: 이미 처리된 정정 요청입니다", contract.ErrInvalidState)
		}
		title, body := "출퇴근 정정이 반려되었습니다", fmt.Sprintf("%s 출퇴근 정정 요청이 반려되었습니다.", c.WorkDate)
		if approve {
			if err := s.applyCorrection(ctx, tx, entity, c, "correction"); err != nil {
				return err
			}
			if err := s.warnWeek(ctx, tx, c.UserID, warnName, c.WorkDate, warnTo); err != nil { // 정정으로 주 근로시간이 늘어난 경우도 알린다
				return err
			}
			title, body = "출퇴근 정정이 반영되었습니다", fmt.Sprintf("%s 출퇴근 정정 요청이 승인되어 기록에 반영되었습니다.", c.WorkDate)
		}
		if err := s.notifyAll(ctx, tx, []int64{c.UserID}, p.UserID, "attendance_correction_done", title, body, id); err != nil {
			return err
		}
		diff := append(correctionDiff(c), audit.DiffItem{Key: "decision", New: map[bool]string{true: "승인", false: "반려"}[approve]}, audit.DiffItem{Key: "subject_user", New: fmt.Sprint(c.UserID)})
		return s.record(ctx, tx, p, "attendance.correct", audit.TargetAttendanceRecord, id, diff...)
	})
	if err != nil {
		return domain.Correction{}, err
	}
	return s.Repo.CorrectionByID(ctx, s.DB, id)
}

// DirectCorrect 는 인사 담당이 다른 사람의 기록을 직접 정정한다(사유 필수, 본인 기록은 불가). 정정 줄은 반영된 상태로 남는다.
func (s *AttendanceService) DirectCorrect(ctx context.Context, in domain.CorrectionInput) (domain.Correction, error) {
	p, err := s.authorize(ctx, contract.PermAttendanceHR, "attendance.correct", audit.TargetAttendanceRecord)
	if err != nil {
		return domain.Correction{}, err
	}
	if err := s.requireModule(ctx); err != nil {
		return domain.Correction{}, err
	}
	if in, err = s.checkCorrectionInput(in); err != nil {
		return domain.Correction{}, err
	}
	if in.UserID == 0 || in.UserID == p.UserID {
		s.recordDenied(ctx, p, "attendance.correct", audit.TargetAttendanceRecord, in.UserID, "본인 기록 직접 정정")
		return domain.Correction{}, validation("본인의 기록은 본인이 정정할 수 없습니다. 다른 인사 담당자에게 요청하세요.")
	}
	entity := contract.WriteEntityID(ctx)
	if inEnt, err := s.leaveSvc().userInEntity(ctx, in.UserID, entity); err != nil {
		return domain.Correction{}, err
	} else if !inEnt {
		return domain.Correction{}, contract.ErrNotFound
	}
	var id int64
	warnTo, warnName := s.recipients(ctx, in.UserID, true), s.userName(ctx, s.DB, in.UserID)
	err = s.write(ctx, p, "attendance.correct", audit.TargetAttendanceRecord, in.UserID, func(tx contract.Tx) error {
		if err := s.guardMonth(ctx, tx, entity, in.WorkDate); err != nil {
			return err
		}
		rec, found, err := s.Repo.Record(ctx, tx, in.UserID, in.WorkDate)
		if err != nil {
			return err
		}
		newIn, newOut, err := s.mergeTimes(in, rec, found)
		if err != nil {
			return err
		}
		c := domain.Correction{UserID: in.UserID, WorkDate: in.WorkDate, OldIn: rec.In, OldOut: rec.Out, NewIn: newIn, NewOut: newOut, Reason: in.Reason, RequestedBy: p.UserID, ApprovedBy: p.UserID, Status: domain.CorrectionApplied}
		if id, err = s.Repo.InsertCorrection(ctx, tx, entity, c, s.now()); err != nil {
			return err
		}
		if err := s.applyCorrection(ctx, tx, entity, c, "admin"); err != nil {
			return err
		}
		if err := s.warnWeek(ctx, tx, in.UserID, warnName, in.WorkDate, warnTo); err != nil {
			return err
		}
		if err := s.notifyAll(ctx, tx, []int64{in.UserID}, p.UserID, "attendance_correction_done", "출퇴근 기록이 정정되었습니다", fmt.Sprintf("%s 출퇴근 기록이 인사 담당에 의해 정정되었습니다.", in.WorkDate), id); err != nil {
			return err
		}
		return s.record(ctx, tx, p, "attendance.correct", audit.TargetAttendanceRecord, id, append(correctionDiff(c), audit.DiffItem{Key: "subject_user", New: fmt.Sprint(in.UserID)})...)
	})
	if err != nil {
		return domain.Correction{}, err
	}
	return s.Repo.CorrectionByID(ctx, s.DB, id)
}

// Corrections 는 내 정정 줄과, 내가 처리할 수 있는 승인 대기 요청이다(범위 밖 요청은 보이지 않는다).
func (s *AttendanceService) Corrections(ctx context.Context) (mine, toDecide []domain.Correction, err error) {
	p, err := contract.Authorize(ctx, contract.PermAttendanceSelf)
	if err != nil {
		return nil, nil, err
	}
	if err := s.requireModule(ctx); err != nil {
		return nil, nil, err
	}
	if mine, err = s.Repo.CorrectionsOfUser(ctx, s.DB, p.UserID, 50); err != nil {
		return nil, nil, err
	}
	if !p.Can(contract.PermAttendanceTeam) && !p.Can(contract.PermAttendanceHR) {
		return mine, nil, nil
	}
	pending, err := s.Repo.PendingCorrections(ctx, s.DB)
	if err != nil {
		return nil, nil, err
	}
	for _, c := range pending {
		if c.UserID == p.UserID {
			continue
		}
		if ok, err := s.leaveSvc().canViewUser(ctx, p, c.UserID); err != nil {
			return nil, nil, err
		} else if ok {
			toDecide = append(toDecide, c)
		}
	}
	return mine, toDecide, nil
}

// ---- 근태 마감 ----

var monthRe = regexp.MustCompile(`^[0-9]{4}-(0[1-9]|1[0-2])$`)

// Closes 는 현재 법인의 근태 마감 현황이다(인사 담당).
func (s *AttendanceService) Closes(ctx context.Context) ([]domain.AttendanceClose, error) {
	if _, err := contract.Authorize(ctx, contract.PermAttendanceHR); err != nil {
		return nil, err
	}
	if err := s.requireModule(ctx); err != nil {
		return nil, err
	}
	return s.Repo.Closes(ctx, s.DB)
}

// CloseMonth 는 끝난 달을 마감한다. 마감하면 그 달의 정정과 소급 휴가 입력이 막힌다(인사 담당은 소급 휴가를 입력할 수 있다).
func (s *AttendanceService) CloseMonth(ctx context.Context, month string) error {
	p, err := s.authorize(ctx, contract.PermAttendanceHR, "attendance.close", audit.TargetAttendanceRecord)
	if err != nil {
		return err
	}
	if err := s.requireModule(ctx); err != nil {
		return err
	}
	if !monthRe.MatchString(month) {
		return validation("마감할 달을 YYYY-MM 으로 입력하세요.")
	}
	if month >= s.kstToday()[:7] {
		return validation("끝나지 않은 달은 마감할 수 없습니다.")
	}
	entity := contract.WriteEntityID(ctx)
	return s.write(ctx, p, "attendance.close", audit.TargetAttendanceRecord, 0, func(tx contract.Tx) error {
		ok, err := s.Repo.Close(ctx, tx, entity, month, p.UserID, s.now())
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("%w: 이미 마감된 달입니다", contract.ErrInvalidState)
		}
		return s.record(ctx, tx, p, "attendance.close", audit.TargetAttendanceRecord, 0, audit.DiffItem{Key: "month", New: month})
	})
}

// ReopenMonth 는 마감을 해제한다(사유 필수, 감사).
func (s *AttendanceService) ReopenMonth(ctx context.Context, month, reason string) error {
	p, err := s.authorize(ctx, contract.PermAttendanceHR, "attendance.reopen", audit.TargetAttendanceRecord)
	if err != nil {
		return err
	}
	if err := s.requireModule(ctx); err != nil {
		return err
	}
	reason = strings.TrimSpace(reason)
	if n := utf8.RuneCountInString(reason); n < 1 || n > 200 {
		return validation("해제 사유를 1~200자로 입력하세요.")
	}
	if !monthRe.MatchString(month) {
		return validation("해제할 달을 YYYY-MM 으로 입력하세요.")
	}
	entity := contract.WriteEntityID(ctx)
	return s.write(ctx, p, "attendance.reopen", audit.TargetAttendanceRecord, 0, func(tx contract.Tx) error {
		ok, err := s.Repo.Reopen(ctx, tx, entity, month, p.UserID, reason, s.now())
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("%w: 마감되지 않은 달입니다", contract.ErrInvalidState)
		}
		return s.record(ctx, tx, p, "attendance.reopen", audit.TargetAttendanceRecord, 0, audit.DiffItem{Key: "month", New: month}, audit.DiffItem{Key: "reason", New: reason})
	})
}

// ---- 근무 유형 ----

// WorkTypes 는 근무 유형 목록이다(맨 앞에 배정이 없는 사람에게 적용되는 기본 유형). 설정 담당은 모듈을 켜기 전에도 읽는다.
func (s *AttendanceService) WorkTypes(ctx context.Context) ([]domain.WorkType, error) {
	p, ok := contract.PrincipalFrom(ctx)
	if !ok {
		return nil, contract.ErrUnauthenticated
	}
	if !p.Can(contract.PermAttendancePolicy) {
		if _, err := contract.Authorize(ctx, contract.PermAttendanceSelf); err != nil {
			return nil, err
		}
		if err := s.requireModule(ctx); err != nil {
			return nil, err
		}
	}
	all, err := s.Repo.WorkTypes(ctx, s.DB)
	if err != nil {
		return nil, err
	}
	return append([]domain.WorkType{defaultWorkType()}, all...), nil
}

func (s *AttendanceService) checkWorkType(in domain.WorkTypeInput, creating bool) (domain.WorkTypeInput, error) {
	in.Name, in.Code = strings.TrimSpace(in.Name), strings.TrimSpace(in.Code)
	if n := utf8.RuneCountInString(in.Name); n < 1 || n > 30 {
		return in, validation("근무 유형 이름을 1~30자로 입력하세요.")
	}
	if creating && (!leaveCodeRe.MatchString(in.Code) || in.Code == "default") {
		return in, validation("근무 유형 코드는 영문 소문자로 시작하는 2~30자(영문 소문자·숫자·밑줄)여야 하고 default 는 쓸 수 없습니다.")
	}
	if _, ok := domain.WorkKindLabels[in.Kind]; !ok {
		return in, validation("근무 유형 구분(고정·시차·선택)을 고르세요.")
	}
	if in.WeeklyMinutes == 0 {
		in.WeeklyMinutes = workHourLimits.FlexibleWeeklyMinutes
	}
	if in.WeeklyMinutes < 600 || in.WeeklyMinutes > 3120 {
		return in, validation("주 소정근로는 10시간 이상 52시간 이하로 입력하세요.")
	}
	pair := func(a, b, label string, required bool) error {
		if a == "" && b == "" {
			if required {
				return validation("%s 시각을 입력하세요.", label)
			}
			return nil
		}
		x, ok1 := hmMin(a)
		y, ok2 := hmMin(b)
		if !ok1 || !ok2 || !hmRe.MatchString(a) || !hmRe.MatchString(b) || y <= x {
			return validation("%s 시각을 HH:MM 형식으로, 끝이 시작보다 늦게 입력하세요.", label)
		}
		return nil
	}
	switch in.Kind {
	case domain.WorkFixed:
		if err := pair(in.StartTime, in.EndTime, "출근·퇴근", true); err != nil {
			return in, err
		}
	case domain.WorkStaggered:
		if err := pair(in.StartTime, in.EndTime, "출근 가능 구간", true); err != nil {
			return in, err
		}
	case domain.WorkFlexible:
		if err := pair(in.CoreStart, in.CoreEnd, "코어 타임", true); err != nil {
			return in, err
		}
		in.StartTime, in.EndTime = "", ""
	}
	if err := pair(in.BreakStart, in.BreakEnd, "휴게", false); err != nil {
		return in, err
	}
	if in.Kind != domain.WorkFlexible {
		in.CoreStart, in.CoreEnd = "", ""
	}
	return in, nil
}

// SaveWorkType 은 근무 유형을 추가(id 0)하거나 고친다. 코드는 바꿀 수 없다. 변경은 감사에 남는다.
func (s *AttendanceService) SaveWorkType(ctx context.Context, id int64, in domain.WorkTypeInput) (domain.WorkType, error) {
	p, err := s.authorize(ctx, contract.PermAttendancePolicy, "attendance.policy_set", audit.TargetLeavePolicy)
	if err != nil {
		return domain.WorkType{}, err
	}
	if in, err = s.checkWorkType(in, id == 0); err != nil {
		return domain.WorkType{}, err
	}
	var out domain.WorkType
	err = s.write(ctx, p, "attendance.policy_set", audit.TargetLeavePolicy, id, func(tx contract.Tx) error {
		if id == 0 {
			nid, err := s.Repo.InsertWorkType(ctx, tx, in, s.now())
			if err != nil {
				if isUniqueViolation(err) {
					return validation("이미 있는 근무 유형 코드입니다.")
				}
				return err
			}
			id = nid
		} else {
			if _, err := s.Repo.WorkTypeByID(ctx, tx, id); err != nil {
				return err
			}
			if err := s.Repo.UpdateWorkType(ctx, tx, id, in); err != nil {
				return err
			}
		}
		if out, err = s.Repo.WorkTypeByID(ctx, tx, id); err != nil {
			return err
		}
		return s.record(ctx, tx, p, "attendance.policy_set", audit.TargetLeavePolicy, id, audit.DiffItem{Key: "work_type", New: out.Name},
			audit.DiffItem{Key: "work_kind", New: domain.WorkKindLabels[out.Kind]})
	})
	return out, err
}

// Assign 는 사람에게 근무 유형을 효력일부터 배정한다(인사 담당). 이력은 추가 전용이다.
func (s *AttendanceService) Assign(ctx context.Context, userID, workTypeID int64, validFrom string) error {
	p, err := s.authorize(ctx, contract.PermAttendanceHR, "attendance.assign", audit.TargetAttendanceRecord)
	if err != nil {
		return err
	}
	if err := s.requireModule(ctx); err != nil {
		return err
	}
	if _, err := time.Parse("2006-01-02", validFrom); err != nil {
		return validation("효력일을 YYYY-MM-DD 로 입력하세요.")
	}
	entity := contract.WriteEntityID(ctx)
	if in, err := s.leaveSvc().userInEntity(ctx, userID, entity); err != nil {
		return err
	} else if !in {
		return contract.ErrNotFound
	}
	return s.write(ctx, p, "attendance.assign", audit.TargetAttendanceRecord, userID, func(tx contract.Tx) error {
		wt, err := s.Repo.WorkTypeByID(ctx, tx, workTypeID)
		if err != nil {
			return validation("근무 유형을 찾을 수 없습니다.")
		}
		if !wt.Active {
			return validation("사용 중지한 근무 유형은 배정할 수 없습니다.")
		}
		ok, err := s.Repo.InsertAssignment(ctx, tx, userID, workTypeID, validFrom, p.UserID, s.now())
		if err != nil {
			return err
		}
		if !ok {
			return validation("같은 효력일의 배정이 이미 있습니다. 다른 효력일로 배정하세요.")
		}
		return s.record(ctx, tx, p, "attendance.assign", audit.TargetAttendanceRecord, userID, audit.DiffItem{Key: "work_type", New: wt.Name}, audit.DiffItem{Key: "effective_from", New: validFrom})
	})
}

// Assignments 는 사람의 배정 이력이다(인사 담당).
func (s *AttendanceService) Assignments(ctx context.Context, userID int64) ([]domain.WorkAssignment, error) {
	if _, err := contract.Authorize(ctx, contract.PermAttendanceHR); err != nil {
		return nil, err
	}
	if err := s.requireModule(ctx); err != nil {
		return nil, err
	}
	if in, err := s.leaveSvc().userInEntity(ctx, userID, contract.WriteEntityID(ctx)); err != nil {
		return nil, err
	} else if !in {
		return nil, contract.ErrNotFound
	}
	return s.Repo.Assignments(ctx, s.DB, userID)
}
