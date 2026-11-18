package service

import (
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
)

// 출퇴근 계산(B50 7절). 순수 함수만 둔다(저장소를 읽지 않는다): 휴게 제외 근로 분, 지각·조퇴·코어 타임 판정, 주 경고 단계.

// workHourLimits 는 근로시간 경고 기준 상수 표다(근로기준법 제50조·제53조 요약, **운영 전 노무 검토 필요**). 값이 바뀌면 이 표와 명세 5.1·7.4 를 함께 고친다.
// 경고 기준은 명세 D14 에 따라 설정으로 바꿀 수 있어야 하지만 이번 구현은 이 상수 한 곳이다(설정 화면은 후속).
var workHourLimits = struct {
	CautionMinutes        int // 주의: 주 48시간 도달
	OverMinutes           int // 경고: 주 52시간 초과(1주 소정 40시간 + 연장 12시간)
	FlexibleWeeklyMinutes int // 선택 근무의 주 소정 총량(40시간)
	WeekStartsMonday      bool
}{CautionMinutes: 48 * 60, OverMinutes: 52 * 60, FlexibleWeeklyMinutes: 40 * 60, WeekStartsMonday: true}

// defaultWorkType 은 근무 유형 배정이 없는 사람에게 적용하는 기본 유형이다(고정 9~18시, 휴게 1시간, 명세 4.2·16절).
func defaultWorkType() domain.WorkType {
	return domain.WorkType{Code: "default", Name: "기본 근무(고정 9~18시)", Kind: domain.WorkFixed, StartTime: "09:00", EndTime: "18:00", BreakStart: "12:00", BreakEnd: "13:00",
		WeeklyMinutes: 40 * 60, Active: true, Default: true}
}

// hmMin 은 "HH:MM" 을 하루 안의 분으로 바꾼다.
func hmMin(s string) (int, bool) {
	t, err := time.Parse("15:04", s)
	if err != nil {
		return 0, false
	}
	return t.Hour()*60 + t.Minute(), true
}

// minuteOfDay 는 KST 기준 하루 안의 분이다(초는 버린다: 09:00:59 는 09:00).
func minuteOfDay(t time.Time) int {
	k := t.In(contract.KST)
	return k.Hour()*60 + k.Minute()
}

// breakOverlapMinutes 는 [in, out] 과 근무 유형의 휴게 구간이 겹치는 분이다(휴게는 그날 KST 기준).
func breakOverlapMinutes(date string, in, out time.Time, wt domain.WorkType) int {
	bs, ok1 := hmMin(wt.BreakStart)
	be, ok2 := hmMin(wt.BreakEnd)
	if !ok1 || !ok2 || be <= bs {
		return 0
	}
	day, err := time.ParseInLocation("2006-01-02", date, contract.KST)
	if err != nil {
		return 0
	}
	start, end := day.Add(time.Duration(bs)*time.Minute), day.Add(time.Duration(be)*time.Minute)
	lo, hi := in, out
	if start.After(lo) {
		lo = start
	}
	if end.Before(hi) {
		hi = end
	}
	if !hi.After(lo) {
		return 0
	}
	return int(hi.Sub(lo) / time.Minute)
}

// workedMinutes 는 체크인~체크아웃에서 휴게 겹침을 뺀 근로 분이다. 퇴근이 출근보다 빠르면 0.
func workedMinutes(date string, in, out time.Time, wt domain.WorkType) int {
	if in.IsZero() || out.IsZero() || !out.After(in) {
		return 0
	}
	m := int(out.Sub(in)/time.Minute) - breakOverlapMinutes(date, in, out, wt)
	if m < 0 {
		return 0
	}
	return m
}

// dailyScheduledMinutes 는 하루 소정근로(주 소정 / 5)다.
func dailyScheduledMinutes(wt domain.WorkType) int {
	w := wt.WeeklyMinutes
	if w <= 0 {
		w = workHourLimits.FlexibleWeeklyMinutes
	}
	return w / 5
}

// dayJudge 는 하루 출퇴근의 지각·조퇴·코어 타임 판정이다(명세 7.2). 출근이 없으면 판정하지 않는다(결근은 월 집계가 센다).
type dayJudge struct{ Late, EarlyLeave, CoreMiss bool }

// judgeDay 는 근무 유형에 따라 판정한다.
//   - 고정: 출근 시각을 넘긴 체크인은 지각, 퇴근 시각 이전 체크아웃은 조퇴.
//   - 시차: 출근 가능 구간의 끝을 넘긴 체크인은 지각(구간 시작 이전 출근은 지각이 아니다), 근로 시간이 하루 소정에 못 미친 체크아웃은 조퇴.
//   - 선택: 코어 타임 시작을 넘긴 체크인이거나 코어 타임 끝 이전 체크아웃이면 코어 미충족(지각·조퇴로 세지 않는다).
func judgeDay(wt domain.WorkType, date string, in, out time.Time) dayJudge {
	var j dayJudge
	if in.IsZero() {
		return j
	}
	inM := minuteOfDay(in)
	switch wt.Kind {
	case domain.WorkFixed:
		if s, ok := hmMin(wt.StartTime); ok && inM > s {
			j.Late = true
		}
		if e, ok := hmMin(wt.EndTime); ok && !out.IsZero() && minuteOfDay(out) < e {
			j.EarlyLeave = true
		}
	case domain.WorkStaggered:
		if e, ok := hmMin(wt.EndTime); ok && inM > e {
			j.Late = true
		}
		if !out.IsZero() && workedMinutes(date, in, out, wt) < dailyScheduledMinutes(wt) {
			j.EarlyLeave = true
		}
	case domain.WorkFlexible:
		if s, ok := hmMin(wt.CoreStart); ok && inM > s {
			j.CoreMiss = true
		}
		if e, ok := hmMin(wt.CoreEnd); ok && !out.IsZero() && minuteOfDay(out) < e {
			j.CoreMiss = true
		}
	}
	return j
}

// weekLevel 은 주 근로 분의 경고 단계다: 52시간을 넘으면 경고, 48시간에 이르면 주의(명세 7.4).
func weekLevel(total int) string {
	switch {
	case total > workHourLimits.OverMinutes:
		return domain.WeekOver
	case total >= workHourLimits.CautionMinutes:
		return domain.WeekCaution
	}
	return domain.WeekOK
}

// weekStartOf 는 날짜(YYYY-MM-DD)가 속한 주의 시작일(월요일)이다.
func weekStartOf(date string) (string, error) {
	d, err := time.Parse("2006-01-02", date)
	if err != nil {
		return "", err
	}
	// 1970-01-01 은 목요일이다: 월요일 기준 요일 번호 = (경과 일수 + 3) % 7.
	back := int((d.Unix()/86400 + 3) % 7)
	return d.AddDate(0, 0, -back).Format("2006-01-02"), nil
}

// parseInstant 는 저장된 RFC3339 시각을 읽는다(빈 값은 영시).
func parseInstant(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

// clockText 는 저장된 시각을 KST "HH:MM" 으로 보인다(빈 값은 "").
func clockText(s string) string {
	t := parseInstant(s)
	if t.IsZero() {
		return ""
	}
	return t.In(contract.KST).Format("15:04")
}

// instantOf 는 근무일과 KST "HH:MM" 으로 저장용 시각(RFC3339 UTC)을 만든다.
func instantOf(date, hm string) (string, bool) {
	m, ok := hmMin(hm)
	if !ok {
		return "", false
	}
	day, err := time.ParseInLocation("2006-01-02", date, contract.KST)
	if err != nil {
		return "", false
	}
	return day.Add(time.Duration(m) * time.Minute).UTC().Format(time.RFC3339), true
}
