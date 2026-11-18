package service

import (
	"testing"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
)

// B50 5단계 순수 계산 시험(AC-5023 판정 표, AC-5024 경고 단계, 휴게 제외).

func kstAt(date, hm string) time.Time {
	t, err := time.ParseInLocation("2006-01-02 15:04", date+" "+hm, contract.KST)
	if err != nil {
		panic(err)
	}
	return t
}

func TestWorkedMinutesExcludesBreak(t *testing.T) {
	wt := defaultWorkType() // 휴게 12:00~13:00
	cases := []struct {
		in, out string
		want    int
	}{
		{"09:00", "18:00", 480},
		{"09:00", "12:00", 180}, // 휴게 전 퇴근
		{"09:00", "12:30", 180}, // 휴게 중 퇴근: 휴게 겹침을 뺀다
		{"12:30", "18:00", 300}, // 휴게 중 출근
		{"13:00", "18:00", 300}, // 휴게 뒤 출근
		{"08:00", "21:00", 720}, // 13시간 - 휴게 1시간
		{"09:00", "09:00", 0},   // 같은 시각
		{"18:00", "09:00", 0},   // 거꾸로
	}
	for _, c := range cases {
		got := workedMinutes("2026-11-10", kstAt("2026-11-10", c.in), kstAt("2026-11-10", c.out), wt)
		if got != c.want {
			t.Errorf("%s~%s: %d분(기대 %d)", c.in, c.out, got, c.want)
		}
	}
	// 휴게가 없는 유형은 그대로 센다.
	nb := wt
	nb.BreakStart, nb.BreakEnd = "", ""
	if got := workedMinutes("2026-11-10", kstAt("2026-11-10", "09:00"), kstAt("2026-11-10", "18:00"), nb); got != 540 {
		t.Errorf("휴게 없음: %d", got)
	}
}

func TestJudgeDayFixedStaggeredFlexible(t *testing.T) {
	d := "2026-11-10"
	fixed := defaultWorkType()
	stag := domain.WorkType{Kind: domain.WorkStaggered, StartTime: "07:00", EndTime: "11:00", BreakStart: "12:00", BreakEnd: "13:00", WeeklyMinutes: 2400}
	flex := domain.WorkType{Kind: domain.WorkFlexible, CoreStart: "10:00", CoreEnd: "15:00", WeeklyMinutes: 2400}
	cases := []struct {
		name        string
		wt          domain.WorkType
		in, out     string // out 이 "" 이면 아직 퇴근 전
		late, early bool
		coreMiss    bool
	}{
		{"고정 정시", fixed, "09:00", "18:00", false, false, false},
		{"고정 1분 지각", fixed, "09:01", "18:00", true, false, false},
		{"고정 조퇴", fixed, "09:00", "17:59", false, true, false},
		{"고정 지각+조퇴", fixed, "09:30", "16:00", true, true, false},
		{"고정 퇴근 전", fixed, "09:00", "", false, false, false},
		{"시차 구간 안(7시)", stag, "07:00", "16:00", false, false, false},
		{"시차 구간 끝(11시)", stag, "11:00", "20:00", false, false, false},
		{"시차 구간 이후(11:01)", stag, "11:01", "20:30", true, false, false},
		{"시차 구간 이전 출근", stag, "06:30", "15:30", false, false, false},
		{"시차 소정 미달 퇴근", stag, "08:00", "16:30", false, true, false}, // 8:30-1h = 7.5h < 8h
		{"시차 소정 충족 퇴근", stag, "08:00", "17:00", false, false, false},
		{"선택 코어 충족", flex, "10:00", "15:00", false, false, false},
		{"선택 코어 지각", flex, "10:01", "17:00", false, false, true},
		{"선택 코어 조기 퇴근", flex, "09:00", "14:59", false, false, true},
		{"선택 코어 내 출근 없음 판정 아님(퇴근 전)", flex, "09:00", "", false, false, false},
	}
	for _, c := range cases {
		var out time.Time
		if c.out != "" {
			out = kstAt(d, c.out)
		}
		j := judgeDay(c.wt, d, kstAt(d, c.in), out)
		if j.Late != c.late || j.EarlyLeave != c.early || j.CoreMiss != c.coreMiss {
			t.Errorf("%s: %+v (기대 지각 %v 조퇴 %v 코어 %v)", c.name, j, c.late, c.early, c.coreMiss)
		}
	}
	if j := judgeDay(fixed, d, time.Time{}, time.Time{}); j != (dayJudge{}) {
		t.Errorf("출근이 없는 날은 판정하지 않는다: %+v", j)
	}
}

func TestWeekLevelBoundariesAndWeekStart(t *testing.T) {
	cases := map[int]string{0: "", 2879: "", 2880: domain.WeekCaution, 3119: domain.WeekCaution, 3120: domain.WeekCaution, 3121: domain.WeekOver}
	for m, want := range cases {
		if got := weekLevel(m); got != want {
			t.Errorf("%d분: %q(기대 %q)", m, got, want)
		}
	}
	for date, want := range map[string]string{"2026-11-09": "2026-11-09", "2026-11-10": "2026-11-09", "2026-11-15": "2026-11-09", "2026-11-16": "2026-11-16", "2026-01-01": "2025-12-29"} {
		if got, _ := weekStartOf(date); got != want {
			t.Errorf("%s 의 주 시작 %s(기대 %s)", date, got, want)
		}
	}
	// 법정 하한 상수 표(명세 5.1·7.4): 값이 바뀌면 노무 검토 후 표와 함께 고친다.
	if workHourLimits.CautionMinutes != 48*60 || workHourLimits.OverMinutes != 52*60 || workHourLimits.FlexibleWeeklyMinutes != 40*60 {
		t.Fatalf("근로시간 기준 상수가 명세와 다름: %+v", workHourLimits)
	}
}

func TestInstantAndClockRoundTrip(t *testing.T) {
	s, ok := instantOf("2026-11-10", "09:05")
	if !ok || s != "2026-11-10T00:05:00Z" {
		t.Fatalf("instantOf: %q %v", s, ok)
	}
	if clockText(s) != "09:05" {
		t.Fatalf("clockText: %q", clockText(s))
	}
	if _, ok := instantOf("2026-11-10", "25:00"); ok {
		t.Fatal("잘못된 시각 허용")
	}
}
