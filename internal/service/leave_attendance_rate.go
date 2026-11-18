package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/org"
	"github.com/choibongseok/procure-ledger/internal/repository"
)

// 출근율 80% 판정(B50 5.1·5.2): 연차 발생 기준일에 직전 1년의 출근율을 출퇴근 기록과 연결한다.
// 출근한 날 = 체크인이 있는 근무일 + 승인된 유급 휴가일(연차를 쓴 날은 출근한 것으로 본다) + 휴직 구간 중 출근 간주 구분의 소정근로일.
// 휴직 구간: 출근 간주 표(org.AbsenceCountsAsAttended, 운영 전 노무 검토 필요)에 든 구분은 그날을 출근으로 세고, 아닌 구분은 그날을 소정근로일에서 뺀다(분모에서 제외).
// 소정근로일이 모두 빠지면(WorkDays 0) 80% 판정을 하지 않고 충족으로 본다.
// 판정 대상 기간이 출퇴근 기록이 시작된 날(법인의 가장 이른 기록) 이전이면 기록이 없어서 못 채운 것이므로 충족한 것으로 본다(기록 도입 전 연차가 줄지 않게).
// 80% 미만이면 그 기준일의 연차는 "개근한 달 수만큼"(최대 11일)만 발생한다. **운영 전 노무 검토 필요**(법정 요건·발생 시점 해석).

// attendanceRule 은 출근율 판정의 한 줄 결과다.
type attendanceRule struct {
	Tracked    bool    // 기록이 있는 기간이라 판정했다
	Rate       float64 // 출근일 / 소정 근무일
	WorkDays   int
	FullMonths int // 소정 근무일을 모두 채운 달 수
}

// attendanceRate 는 [from, to] 의 출근율을 계산한다.
func (s *LeaveService) attendanceRate(ctx context.Context, tx contract.Tx, userID int64, from, to string) (attendanceRule, error) {
	var r attendanceRule
	att := repository.SQLAttendance{}
	since, err := att.FirstRecordDate(ctx, tx)
	if err != nil {
		return r, err
	}
	if since == "" || from < since {
		return r, nil
	}
	r.Tracked = true
	recs, err := att.RecordDates(ctx, tx, userID, from, to)
	if err != nil {
		return r, err
	}
	periods, err := att.PaidLeavePeriods(ctx, tx, userID, from, to)
	if err != nil {
		return r, err
	}
	onLeave := map[string]bool{}
	for _, p := range periods {
		a, _ := lvParseDay(p[0])
		b, _ := lvParseDay(p[1])
		for d := a; !d.After(b); d = d.AddDate(0, 0, 1) {
			onLeave[lvFmtDay(d)] = true
		}
	}
	spans, err := s.Repo.AbsenceSpans(ctx, tx, userID, from, to)
	if err != nil {
		return r, err
	}
	a, err := lvParseDay(from)
	if err != nil {
		return r, err
	}
	b, err := lvParseDay(to)
	if err != nil {
		return r, err
	}
	attended := 0
	type mon struct{ days, ok int }
	months := map[string]*mon{}
	for d := a; !d.After(b); d = d.AddDate(0, 0, 1) {
		ds := lvFmtDay(d)
		biz, err := s.isBusinessDay(ctx, tx, ds)
		if err != nil {
			return r, err
		}
		if !biz {
			continue
		}
		counted, excluded := absenceDayRule(spans, ds)
		if excluded {
			continue // 출근 간주가 아닌 휴직 구분의 날은 소정근로일에서 뺀다
		}
		r.WorkDays++
		m := months[ds[:7]]
		if m == nil {
			m = &mon{}
			months[ds[:7]] = m
		}
		m.days++
		if recs[ds] || onLeave[ds] || counted {
			attended++
			m.ok++
		}
	}
	if r.WorkDays > 0 {
		r.Rate = float64(attended) / float64(r.WorkDays)
	}
	for k, m := range months {
		// 기간의 처음·끝에 걸친 달은 달 전체가 기간 안에 있을 때만 개근으로 센다.
		first, _ := time.Parse("2006-01", k)
		last := lvAddMonths(first, 1).AddDate(0, 0, -1)
		if first.Before(a) || last.After(b) {
			continue
		}
		if m.days > 0 && m.days == m.ok {
			r.FullMonths++
		}
	}
	return r, nil
}

// applyAttendanceRule 은 기념일 발생(입사일 기준 hire:yNN, 회계연도 기준 fiscal:yYYYY)의 일수를 출근율에 맞춰 줄인다. 줄이지 않으면 ev 를 그대로 돌려준다.
// 규칙이 꺼져 있거나 월 발생·보정 줄이면 건드리지 않는다. 일수가 0 이 되면 ok=false(그 기준일에는 발생이 없다).
func (s *LeaveService) applyAttendanceRule(ctx context.Context, tx contract.Tx, userID int64, hire time.Time, ev accrualEvent, on bool) (accrualEvent, bool, error) {
	if !on || !(strings.HasPrefix(ev.Key, "hire:y") || strings.HasPrefix(ev.Key, "fiscal:y")) {
		return ev, true, nil
	}
	evDay, err := lvParseDay(ev.Date)
	if err != nil {
		return ev, true, err
	}
	from := lvAddMonths(evDay, -12)
	if from.Before(hire) {
		from = hire
	}
	rule, err := s.attendanceRate(ctx, tx, userID, lvFmtDay(from), lvFmtDay(evDay.AddDate(0, 0, -1)))
	if err != nil || !rule.Tracked || rule.WorkDays == 0 || rule.Rate*100 >= float64(legalMinimum.AttendanceRatePercent) {
		return ev, true, err
	}
	days := rule.FullMonths
	if days > legalMinimum.FirstYearMaxDays {
		days = legalMinimum.FirstYearMaxDays
	}
	if days >= ev.Days {
		return ev, true, nil
	}
	if days <= 0 {
		return ev, false, nil
	}
	ev.Days = days
	ev.Reason = fmt.Sprintf("출근율 %.0f%%(80%% 미만): 개근한 달 %d개월", rule.Rate*100, days)
	return ev, true, nil
}

// absenceDayRule 은 그날이 휴직 구간 안일 때 출근 간주(counted)인지 소정근로일에서 뺄 날(excluded)인지다. 휴직 구간 밖이면 둘 다 거짓이다.
func absenceDayRule(spans []repository.AbsenceSpan, day string) (counted, excluded bool) {
	for _, sp := range spans {
		if !sp.Covers(day) {
			continue
		}
		if org.AbsenceCountsAsAttended(sp.Kind) {
			return true, false
		}
		excluded = true
	}
	return false, excluded
}
