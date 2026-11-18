package service

import (
	"context"
	"errors"
	"fmt"
	"log"
	"regexp"
	"sort"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/privacy"
	"github.com/choibongseok/procure-ledger/internal/repository"
)

// 부서 달력·팀 근태 현황·월 근태 내보내기(B50 6단계, 명세 9절).
// 누구의 근태를 볼 수 있는지는 attendanceViewScope 한 곳이 정하고 달력·현황·내보내기가 같은 함수를 쓴다.
// 범위 밖 사람은 목록에도 합계에도 없다. 휴가·휴직 사유 원문은 이 경로의 응답에 실리지 않는다(종류·일정만): 사유는 휴가 신청 상세(LeaveService.Get)가 열람 기록과 함께 보인다.

// viewScope 는 호출자가 볼 수 있는 사람과, 그중 종류까지 보이는 사람(본인·직속 조직장 범위·인사 담당)이다.
type viewScope struct {
	Users []int64
	Full  map[int64]bool
	Label string // 달력·현황 제목에 쓰는 범위 이름
}

const boardRowCap = 300 // 팀 현황 표의 최대 행 수(초과하면 안내한다)

// attendanceViewScope 는 근태 열람 범위의 유일한 판정이다(명세 10.2).
//   - 인사 담당(attendance:hr, 내보내기는 attendance:export 도) = 현재 법인 전 재직자. 조직을 고르면 그 조직(하위 포함)만.
//   - 직속 조직장(attendance:team) = 오늘 기준 자기 조직(하위 포함) 소속원. 직무대행자도 같은 범위이고 대행이 끝나면 즉시 잃는다(HeadedScope 가 그렇게 돌려준다).
//   - 그 밖의 사람 = 자기 조직 소속원(하위 제외)이며 본인 말고는 종류가 숨겨진다(동료).
//   - 결재 위임으로는 범위가 열리지 않는다(위임은 결재만 맡긴다: 이 함수는 위임을 읽지 않는다).
//
// unitID 가 범위 밖이면 빈 범위다(존재를 알리지 않는다).
func (s *AttendanceService) attendanceViewScope(ctx context.Context, p contract.Principal, unitID int64, export bool) (viewScope, error) {
	var err error
	vs := viewScope{Full: map[int64]bool{}}
	day := contract.DayOf(s.now())
	entity := contract.WriteEntityID(ctx)
	inEntityCache := map[int64]bool{}
	inEntity := func(id int64) bool {
		if v, ok := inEntityCache[id]; ok {
			return v
		}
		ok, err := s.leaveSvc().userInEntity(ctx, id, entity)
		inEntityCache[id] = ok && err == nil
		return inEntityCache[id]
	}
	hr := p.Can(contract.PermAttendanceHR) || (export && p.Can(contract.PermAttendanceExport))
	var headed []int64
	if s.Org != nil && p.Can(contract.PermAttendanceTeam) {
		h, err := s.Org.HeadedScope(ctx, p.UserID, day)
		if err != nil && !errors.Is(err, contract.ErrNotInOrg) && !errors.Is(err, contract.ErrNotFound) {
			return vs, err
		}
		headed = h
	}
	headedSet := map[int64]bool{}
	for _, u := range headed {
		headedSet[u] = true
	}
	ownUnit := int64(0)
	if s.Org != nil {
		if person, err := s.Org.Person(ctx, p.UserID, day); err == nil {
			ownUnit = person.Primary.Unit.ID
		}
	}
	members := func(unit int64, desc bool) ([]int64, error) {
		if s.Org == nil {
			return nil, nil
		}
		return s.Org.MembersOf(ctx, unit, day, desc)
	}
	var cand []int64
	switch {
	case unitID != 0:
		if !hr && !headedSet[unitID] && unitID != ownUnit {
			return vs, nil
		}
		if cand, err = members(unitID, hr || headedSet[unitID]); err != nil {
			return vs, err
		}
		vs.Label = s.unitName(ctx, unitID, day)
	case hr:
		rows, err := s.DB.QueryContext(ctx, `SELECT id FROM users WHERE active = 1 ORDER BY id`)
		if err != nil {
			return vs, err
		}
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return vs, err
			}
			cand = append(cand, id)
		}
		rows.Close()
		vs.Label = "전사"
	case len(headed) > 0:
		seen := map[int64]bool{}
		for _, u := range headed {
			ms, err := members(u, false)
			if err != nil {
				return vs, err
			}
			for _, id := range ms {
				if !seen[id] {
					seen[id] = true
					cand = append(cand, id)
				}
			}
		}
		vs.Label = "내 조직"
	case ownUnit != 0:
		if cand, err = members(ownUnit, false); err != nil {
			return vs, err
		}
		vs.Label = s.unitName(ctx, ownUnit, day)
	}
	headMembers := map[int64]bool{}
	if !hr && len(headed) > 0 {
		for _, u := range headed {
			ms, err := members(u, false)
			if err != nil {
				return vs, err
			}
			for _, id := range ms {
				headMembers[id] = true
			}
		}
	}
	seen := map[int64]bool{}
	for _, id := range cand {
		if seen[id] || !inEntity(id) {
			continue
		}
		seen[id] = true
		vs.Users = append(vs.Users, id)
		if hr || id == p.UserID || headMembers[id] {
			vs.Full[id] = true
		}
	}
	return vs, nil
}

func (s *AttendanceService) unitName(ctx context.Context, unit int64, day contract.Day) string {
	if s.Org == nil {
		return ""
	}
	u, err := s.Org.Unit(ctx, unit, day)
	if err != nil {
		return ""
	}
	return u.Name
}

// people 는 이름·소속·사번을 조직 조회에서 한꺼번에 읽는다(재직 중인 사람만 있다).
func (s *AttendanceService) people(ctx context.Context, ids []int64) map[int64]contract.OrgPerson {
	if s.Org == nil || len(ids) == 0 {
		return map[int64]contract.OrgPerson{}
	}
	m, err := s.Org.Persons(ctx, ids, contract.DayOf(s.now()))
	if err != nil {
		return map[int64]contract.OrgPerson{}
	}
	return m
}

func (s *AttendanceService) names(ctx context.Context, ids []int64) map[int64]string {
	out := map[int64]string{}
	for _, id := range ids {
		out[id] = s.userName(ctx, s.DB, id)
	}
	return out
}

var calMonthRe = regexp.MustCompile(`^[0-9]{4}-(0[1-9]|1[0-2])$`)

// Calendar 는 한 달의 부서 달력이다(명세 9.1). 본인·직속 조직장·인사 담당에게는 휴가 종류가, 같은 팀 동료에게는 "휴가" 또는 "부재" 한 가지만 보인다.
func (s *AttendanceService) Calendar(ctx context.Context, month string, unitID int64) (domain.AttCalendarView, error) {
	var out domain.AttCalendarView
	p, err := contract.Authorize(ctx, contract.PermAttendanceSelf)
	if err != nil {
		return out, err
	}
	if err := s.requireModule(ctx); err != nil {
		return out, err
	}
	if month == "" {
		month = s.kstToday()[:7]
	}
	if !calMonthRe.MatchString(month) {
		return out, validation("달을 YYYY-MM 으로 입력하세요.")
	}
	first, _ := time.Parse("2006-01", month)
	last := first.AddDate(0, 1, -1)
	from, to := first.Format("2006-01-02"), last.Format("2006-01-02")
	out.Month = month
	hol := map[string]string{}
	if hs, err := (repository.SQLCompany{}).Holidays(ctx, s.DB, first.Year()); err == nil {
		for _, h := range hs {
			hol[h.Date] = h.Name
		}
	}
	for d := first; !d.After(last); d = d.AddDate(0, 0, 1) {
		ds := d.Format("2006-01-02")
		biz, err := s.isBusinessDay(ctx, s.DB, ds)
		if err != nil {
			return out, err
		}
		out.Days = append(out.Days, domain.AttCalendarDay{Date: ds, Weekend: !biz && hol[ds] == "", Holiday: hol[ds]})
	}
	vs, err := s.attendanceViewScope(ctx, p, unitID, false)
	if err != nil {
		return out, err
	}
	out.Scope = vs.Label
	leaves, err := s.Repo.ApprovedLeaves(ctx, s.DB, from, to)
	if err != nil {
		return out, err
	}
	byUser := map[int64][]repository.LeaveEntry{}
	for _, l := range leaves {
		byUser[l.UserID] = append(byUser[l.UserID], l)
	}
	absent, err := s.absenceEntries(ctx, from, to)
	if err != nil {
		return out, err
	}
	pe := s.people(ctx, vs.Users)
	nm := s.names(ctx, vs.Users)
	for _, id := range vs.Users {
		cp := domain.AttCalendarPerson{UserID: id, Name: nm[id], Unit: pe[id].Primary.Unit.Name}
		for _, l := range byUser[id] {
			e := domain.AttCalendarEntry{Start: l.Start, End: l.End}
			if vs.Full[id] {
				e.Label = l.TypeName
				e.Part = leavePartLabel(l.Part)
			} else if l.Balance != "" {
				e.Label = "휴가" // 연차·대체휴가: 종류를 숨기고 "휴가"만
			} else {
				e.Label = "부재" // 병가·경조·공가 같은 종류는 동료에게 "부재" 한 가지로만
			}
			cp.Entries = append(cp.Entries, e)
		}
		cp.Entries = append(cp.Entries, absent[id]...) // 휴직은 누구에게나 "부재" 한 가지다(구분·사유 없음)
		out.People = append(out.People, cp)
	}
	sort.SliceStable(out.People, func(i, j int) bool { return out.People[i].Name < out.People[j].Name })
	return out, nil
}

// AbsenceMaxDays 는 팀 부재 조회의 최대 기간(일)이다.
const AbsenceMaxDays = 31

// Absence 는 기간 안의 부재 일정이다(MCP 팀 부재 도구가 쓴다). 열람 범위는 달력과 같은 attendanceViewScope 한 곳이다.
// 달력과 달리 본인·직속 조직장·인사 담당에게도 종류를 풀지 않는다: 연차·대체휴가는 "휴가", 그 밖(병가·경조·공가)은 "부재" 한 가지이고 사유·시간 구간은 싣지 않는다(명세 11절 3항).
func (s *AttendanceService) Absence(ctx context.Context, from, to string, unitID int64) (domain.AttAbsenceView, error) {
	var out domain.AttAbsenceView
	p, err := contract.Authorize(ctx, contract.PermAttendanceSelf)
	if err != nil {
		return out, err
	}
	if err := s.requireModule(ctx); err != nil {
		return out, err
	}
	f, ferr := time.Parse("2006-01-02", from)
	t, terr := time.Parse("2006-01-02", to)
	if ferr != nil || terr != nil {
		return out, validation("기간은 연-월-일(예 2026-12-18) 형식으로 입력하세요.")
	}
	if t.Before(f) {
		return out, validation("끝 날짜가 시작 날짜보다 앞섭니다.")
	}
	if int(t.Sub(f).Hours()/24)+1 > AbsenceMaxDays {
		return out, validation("한 번에 %d일까지 조회할 수 있습니다.", AbsenceMaxDays)
	}
	vs, err := s.attendanceViewScope(ctx, p, unitID, false)
	if err != nil {
		return out, err
	}
	out.From, out.To, out.Scope = from, to, vs.Label
	leaves, err := s.Repo.ApprovedLeaves(ctx, s.DB, from, to)
	if err != nil {
		return out, err
	}
	byUser := map[int64][]repository.LeaveEntry{}
	for _, l := range leaves {
		byUser[l.UserID] = append(byUser[l.UserID], l)
	}
	absent, err := s.absenceEntries(ctx, from, to)
	if err != nil {
		return out, err
	}
	pe := s.people(ctx, vs.Users)
	nm := s.names(ctx, vs.Users)
	for _, id := range vs.Users {
		if len(byUser[id]) == 0 && len(absent[id]) == 0 {
			continue
		}
		ap := domain.AttAbsencePerson{Name: nm[id], Unit: pe[id].Primary.Unit.Name}
		for _, l := range byUser[id] {
			label := "부재"
			if l.Balance != "" {
				label = "휴가"
			}
			ap.Entries = append(ap.Entries, domain.AttCalendarEntry{Start: l.Start, End: l.End, Label: label})
		}
		ap.Entries = append(ap.Entries, absent[id]...)
		out.People = append(out.People, ap)
	}
	sort.SliceStable(out.People, func(i, j int) bool { return out.People[i].Name < out.People[j].Name })
	return out, nil
}

// absenceEntries 는 [from, to] 안의 휴직 구간을 "부재" 일정으로 바꾼 사람별 목록이다(B50 8.3 의 6). 구분·사유·예정 복직일은 싣지 않는다.
// 구간의 To 는 배타(복직 효력일)이라 마지막 날은 그 전날이다.
func (s *AttendanceService) absenceEntries(ctx context.Context, from, to string) (map[int64][]domain.AttCalendarEntry, error) {
	spans, err := (repository.SQLLeave{}).AbsenceSpansBetween(ctx, s.DB, from, to)
	if err != nil {
		return nil, err
	}
	out := map[int64][]domain.AttCalendarEntry{}
	for id, list := range spans {
		for _, sp := range list {
			start, end := sp.From, to
			if start < from {
				start = from
			}
			if sp.To != "" {
				if last, perr := time.Parse("2006-01-02", sp.To); perr == nil {
					if d := last.AddDate(0, 0, -1).Format("2006-01-02"); d < end {
						end = d
					}
				}
			}
			if end < start {
				continue
			}
			out[id] = append(out[id], domain.AttCalendarEntry{Start: start, End: end, Label: "부재"})
		}
	}
	return out, nil
}

func leavePartLabel(part string) string {
	return map[string]string{domain.LeavePartAM: "오전 반차", domain.LeavePartPM: "오후 반차", domain.LeavePartHours: "시간 지정"}[part]
}

// weekMinutesOf 는 한 사람의 그 주 근로 분이다(요약 계산: 근무 유형 타임라인과 기록만 읽는다).
func (s *AttendanceService) weekMinutesOf(ctx context.Context, userID int64, date string) (int, error) {
	start, err := weekStartOf(date)
	if err != nil {
		return 0, err
	}
	st, _ := time.Parse("2006-01-02", start)
	recs, err := s.Repo.RecordsBetween(ctx, s.DB, userID, start, st.AddDate(0, 0, 6).Format("2006-01-02"))
	if err != nil {
		return 0, err
	}
	tl, err := s.Repo.Timeline(ctx, s.DB, userID, st.AddDate(0, 0, 6).Format("2006-01-02"))
	if err != nil {
		return 0, err
	}
	total := 0
	for _, r := range recs {
		total += workedMinutes(r.Date, parseInstant(r.In), parseInstant(r.Out), typeAt(tl, r.Date))
	}
	return total, nil
}

// typeAt 는 타임라인(효력일 순)에서 그날의 근무 유형이다. 배정이 없으면 기본 유형.
func typeAt(tl []repository.TypeFrom, date string) domain.WorkType {
	wt := defaultWorkType()
	for _, t := range tl {
		if t.From <= date {
			wt = t.WT
		}
	}
	return wt
}

// TeamStatus 는 조직장·인사 담당의 오늘 현황이다(명세 9.2).
func (s *AttendanceService) TeamStatus(ctx context.Context, unitID int64) (domain.AttTeamStatus, error) {
	var out domain.AttTeamStatus
	p, err := contract.Authorize(ctx, contract.PermAttendanceSelf)
	if err != nil {
		return out, err
	}
	if !p.Can(contract.PermAttendanceTeam) && !p.Can(contract.PermAttendanceHR) {
		return out, fmt.Errorf("%w: 팀 근태 현황은 조직장이나 인사 담당만 볼 수 있습니다", contract.ErrForbidden)
	}
	if err := s.requireModule(ctx); err != nil {
		return out, err
	}
	today := s.kstToday()
	t, _ := time.Parse("2006-01-02", today)
	yday := t.AddDate(0, 0, -1).Format("2006-01-02")
	vs, err := s.attendanceViewScope(ctx, p, unitID, false)
	if err != nil {
		return out, err
	}
	out.Date, out.Scope, out.CautionMin, out.OverMin = today, vs.Label, workHourLimits.CautionMinutes, workHourLimits.OverMinutes
	biz, err := s.isBusinessDay(ctx, s.DB, today)
	if err != nil {
		return out, err
	}
	recs, err := s.Repo.RecordsInRange(ctx, s.DB, yday, today)
	if err != nil {
		return out, err
	}
	rec := map[string]repository.AttRecord{}
	for _, r := range recs {
		rec[fmt.Sprintf("%d|%s", r.UserID, r.Date)] = r
	}
	leaves, err := s.Repo.ApprovedLeaves(ctx, s.DB, today, today)
	if err != nil {
		return out, err
	}
	onLeave := map[int64]bool{}
	for _, l := range leaves {
		onLeave[l.UserID] = true
	}
	absentSpans, err := (repository.SQLLeave{}).AbsenceSpansBetween(ctx, s.DB, today, today) // 오늘 휴직 중인 사람은 "부재"(구분 없음)
	if err != nil {
		return out, err
	}
	pe := s.people(ctx, vs.Users)
	nm := s.names(ctx, vs.Users)
	inScope := map[int64]bool{}
	for _, id := range vs.Users {
		inScope[id] = true
	}
	out.Total = len(vs.Users)
	rows := make([]domain.AttTeamRow, 0, len(vs.Users))
	for _, id := range vs.Users {
		row := domain.AttTeamRow{UserID: id, Name: nm[id], Unit: pe[id].Primary.Unit.Name}
		r, has := rec[fmt.Sprintf("%d|%s", id, today)]
		switch {
		case len(absentSpans[id]) > 0:
			row.State = "부재"
			out.Absent++
		case onLeave[id]:
			row.State = "휴가"
			out.OnLeave++
		case has && r.In != "":
			row.State = "출근"
			out.Present++
		case !biz:
			row.State = "휴일"
		default:
			row.State = "미출근"
			out.NotYet++
		}
		if has {
			row.CheckIn, row.CheckOut = clockText(r.In), clockText(r.Out)
			wt := defaultWorkType()
			if w, ok, err := s.Repo.WorkTypeOn(ctx, s.DB, id, today); err == nil && ok {
				wt = w
			}
			row.Late = judgeDay(wt, today, parseInstant(r.In), parseInstant(r.Out)).Late && !onLeave[id] && len(absentSpans[id]) == 0
		}
		if y, ok := rec[fmt.Sprintf("%d|%s", id, yday)]; ok && y.In != "" && y.Out == "" {
			row.Missing = true
		}
		rows = append(rows, row)
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })
	if len(rows) > boardRowCap {
		rows, out.Truncated = rows[:boardRowCap], true
	}
	for i := range rows {
		m, err := s.weekMinutesOf(ctx, rows[i].UserID, today)
		if err != nil {
			return out, err
		}
		rows[i].WeekMinutes, rows[i].WeekLevel = m, weekLevel(m)
		switch rows[i].WeekLevel {
		case domain.WeekOver:
			out.WeekOver++
		case domain.WeekCaution:
			out.WeekCaut++
		}
	}
	out.Rows = rows
	pending, err := s.Repo.PendingCorrections(ctx, s.DB)
	if err != nil {
		return out, err
	}
	for _, c := range pending {
		if inScope[c.UserID] && c.UserID != p.UserID {
			out.Pending++
		}
	}
	return out, nil
}

// MonthScopeCount 는 월 내보내기 대상 인원 수다(파일을 만들기 전 상한 확인용).
func (s *AttendanceService) MonthScopeCount(ctx context.Context) (int, error) {
	p, err := contract.Authorize(ctx, contract.PermAttendanceExport)
	if err != nil {
		return 0, err
	}
	if err := s.requireModule(ctx); err != nil {
		return 0, err
	}
	vs, err := s.attendanceViewScope(ctx, p, 0, true)
	return len(vs.Users), err
}

// MonthReport 는 한 달의 근태 집계(급여 연동용, 명세 9.4)다. 급여 금액·수당은 계산하지 않고 사유·휴직 구분 원문은 싣지 않는다.
// 사번이 실리므로 응답 전에 B41 열람 기록(범주 employee_no)을 남기고 기록하지 못하면 내보내지 않는다(fail-closed).
func (s *AttendanceService) MonthReport(ctx context.Context, month string) (domain.AttMonthReport, error) {
	var out domain.AttMonthReport
	p, err := contract.Authorize(ctx, contract.PermAttendanceExport)
	if err != nil {
		return out, err
	}
	if err := s.requireModule(ctx); err != nil {
		return out, err
	}
	if !calMonthRe.MatchString(month) {
		return out, validation("달을 YYYY-MM 으로 입력하세요.")
	}
	first, _ := time.Parse("2006-01", month)
	last := first.AddDate(0, 1, -1)
	from, to := first.Format("2006-01-02"), last.Format("2006-01-02")
	today := s.kstToday()
	if from > today {
		return out, validation("아직 시작하지 않은 달은 내보낼 수 없습니다.")
	}
	entity := contract.WriteEntityID(ctx)
	out.Month = month
	if out.Closed, err = s.Repo.MonthClosed(ctx, s.DB, entity, month); err != nil {
		return out, err
	}
	vs, err := s.attendanceViewScope(ctx, p, 0, true)
	if err != nil {
		return out, err
	}
	pol, hasPol, err := s.Leave.PolicyOn(ctx, s.DB, to)
	if err != nil {
		return out, err
	}
	wd := 480
	if hasPol {
		wd = pol.WorkdayMinutes
	}
	// 소정 근무일(영업일) 목록과 주 경계(첫 주 월요일 ~ 마지막 주 일요일).
	var bizDays []string
	for d := first; !d.After(last); d = d.AddDate(0, 0, 1) {
		ds := d.Format("2006-01-02")
		ok, err := s.isBusinessDay(ctx, s.DB, ds)
		if err != nil {
			return out, err
		}
		if ok {
			bizDays = append(bizDays, ds)
		}
	}
	ws, _ := weekStartOf(from)
	weLast, _ := weekStartOf(to)
	weT, _ := time.Parse("2006-01-02", weLast)
	rangeTo := weT.AddDate(0, 0, 6).Format("2006-01-02")
	recs, err := s.Repo.RecordsInRange(ctx, s.DB, ws, rangeTo)
	if err != nil {
		return out, err
	}
	recBy := map[int64][]repository.AttRecord{}
	for _, r := range recs {
		recBy[r.UserID] = append(recBy[r.UserID], r)
	}
	leaves, err := s.Repo.ApprovedLeaves(ctx, s.DB, from, to)
	if err != nil {
		return out, err
	}
	leaveBy := map[int64][]repository.LeaveEntry{}
	for _, l := range leaves {
		leaveBy[l.UserID] = append(leaveBy[l.UserID], l)
	}
	pe := s.people(ctx, vs.Users)
	nm := s.names(ctx, vs.Users)
	for _, id := range vs.Users {
		tl, err := s.Repo.Timeline(ctx, s.DB, id, to)
		if err != nil {
			return out, err
		}
		row := domain.AttMonthRow{EmployeeNo: pe[id].EmployeeNo, Name: nm[id], Unit: pe[id].Primary.Unit.Name, WorkType: typeAt(tl, to).Name}
		hire, err := s.Leave.HireDate(ctx, s.DB, id, to)
		if err != nil {
			return out, err
		}
		attended := map[string]bool{}
		weekTotal := map[string]int{}
		for _, r := range recBy[id] {
			wt := typeAt(tl, r.Date)
			in, outT := parseInstant(r.In), parseInstant(r.Out)
			m := workedMinutes(r.Date, in, outT, wt)
			if wk, err := weekStartOf(r.Date); err == nil {
				weekTotal[wk] += m
			}
			if r.Date < from || r.Date > to {
				continue
			}
			if r.In != "" {
				attended[r.Date] = true
				row.AttendDays++
			}
			row.WorkMinutes += m
			j := judgeDay(wt, r.Date, in, outT)
			if j.Late {
				row.Late++
			}
			if j.EarlyLeave {
				row.EarlyLeave++
			}
		}
		for _, n := range weekTotal {
			if n > workHourLimits.OverMinutes {
				row.OverWeeks++
			}
		}
		fullLeave := map[string]bool{}
		for _, l := range leaveBy[id] {
			days := 0.0
			if l.Part == domain.LeavePartFull {
				n := 0
				for _, b := range bizDays {
					if b >= l.Start && b <= l.End {
						n++
						fullLeave[b] = true
					}
				}
				days = float64(n)
			} else if l.Start >= from && l.Start <= to {
				days = float64(l.Minutes) / float64(wd)
			}
			switch {
			case l.Balance == domain.LeaveBalanceAnnual:
				row.AnnualDays += days
			case l.Balance == domain.LeaveBalanceCompensatory:
				row.CompDays += days
			case l.Code == "sick":
				row.SickDays += days
			default:
				row.OtherLeaveDays += days
			}
		}
		for _, b := range bizDays {
			if hire != "" && b < hire {
				continue
			}
			row.WorkDays++
			if b < today && !attended[b] && !fullLeave[b] {
				row.AbsentDays++
			}
		}
		out.Rows = append(out.Rows, row)
	}
	sort.SliceStable(out.Rows, func(i, j int) bool { return out.Rows[i].Name < out.Rows[j].Name })
	// 사번이 응답에 실리므로 정보주체(본인 제외)를 열람 기록에 남긴다. 기록하지 못하면 내보내지 않는다.
	if s.Privacy == nil {
		return domain.AttMonthReport{}, ErrLeavePrivacyNote
	}
	subs := make([]privacy.Subject, 0, len(vs.Users))
	for _, id := range vs.Users {
		subs = append(subs, privacy.Subject{Type: privacy.SubjectUser, ID: id})
	}
	if err := s.Privacy.Note(ctx, p, privacy.Access{Category: privacy.CatEmployeeNo, Screen: "내보내기 월 근태", Dataset: "attendance_month", Export: true,
		Fields: []string{privacy.FieldEmployeeNo}, Subjects: privacy.ExcludeSelf(p, subs)}); err != nil {
		log.Printf("attendance: 사번 열람 기록 실패: %v", err)
		return domain.AttMonthReport{}, ErrLeavePrivacyNote
	}
	return out, nil
}
