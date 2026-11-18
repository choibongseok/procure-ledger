package service

import (
	"fmt"
	"testing"
	"time"

	"github.com/choibongseok/procure-ledger/internal/repository"
)

// B50 AC-5001·5002·5055: 연차 발생 계산(순수 함수)과 법정 하한 상수 표.

func day(s string) time.Time {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		panic(err)
	}
	return t
}

func sumDays(ev []accrualEvent) int {
	n := 0
	for _, e := range ev {
		n += e.Days
	}
	return n
}

func TestLegalMinimumTableMatchesSpec(t *testing.T) {
	// 명세 5.1 표: 80% 이상 15일, 첫해 월 1일 최대 11일, 3년 이상 2년마다 +1일, 상한 25일, 소멸 시효 12개월. 값을 바꾸려면 노무 검토 뒤 명세 표와 함께 고친다.
	if legalMinimum.FullYearDays != 15 || legalMinimum.FirstYearMaxDays != 11 || legalMinimum.TenureStepYears != 2 || legalMinimum.TenureAddDays != 1 || legalMinimum.CapDays != 25 || legalMinimum.MinExpireMonths != 12 || legalMinimum.MinTenureYearsAdd != 3 || legalMinimum.AttendanceRatePercent != 80 {
		t.Fatalf("법정 하한 상수가 명세 5.1 표와 다름: %+v", legalMinimum)
	}
	for n, want := range map[int]int{1: 15, 2: 15, 3: 16, 4: 16, 5: 17, 6: 17, 10: 19, 21: 25, 22: 25, 40: 25} {
		if got := annualDaysForYears(n); got != want {
			t.Errorf("근속 %d년: %d일(기대 %d)", n, got, want)
		}
	}
}

func TestAccrualHireBasisBoundaries(t *testing.T) {
	hire := day("2024-03-15")
	ev := accrualPlan(hire, "hire", "monthly", 1, day("2030-12-31"))
	byKey := map[string]accrualEvent{}
	for _, e := range ev {
		byKey[e.Key] = e
	}
	// 첫해: 한 달마다 1일씩 11번(11개월째까지), 12개월째에는 월 발생이 없다.
	for m := 1; m <= 11; m++ {
		e, ok := byKey[fmt.Sprintf("hire:m%02d", m)]
		if !ok || e.Days != 1 || e.Date != lvFmtDay(lvAddMonths(hire, m)) {
			t.Fatalf("월 발생 %d: %+v", m, e)
		}
	}
	if _, ok := byKey["hire:m12"]; ok {
		t.Fatal("12번째 월 발생이 있으면 안 됨")
	}
	for n, want := range map[int]int{1: 15, 2: 15, 3: 16, 4: 16, 5: 17, 6: 17} {
		e := byKey[fmt.Sprintf("hire:y%02d", n)]
		if e.Days != want || e.Date != lvFmtDay(lvAddMonths(hire, 12*n)) {
			t.Errorf("근속 %d년 발생: %+v (기대 %d일)", n, e, want)
		}
	}
	// 오늘 이전까지만: 11개월째 시점에는 월 발생 11건, 연 발생 없음.
	early := accrualPlan(hire, "hire", "monthly", 1, day("2025-02-15"))
	if len(early) != 11 || sumDays(early) != 11 {
		t.Fatalf("입사 11개월 시점: %d건 %d일", len(early), sumDays(early))
	}
	// 선지급: 입사일에 11일 한 번, 이후 월 발생 없음.
	up := accrualPlan(hire, "hire", "upfront", 1, day("2025-03-14"))
	if len(up) != 1 || up[0].Days != 11 || up[0].Date != "2024-03-15" {
		t.Fatalf("선지급: %+v", up)
	}
	// 25일 상한.
	long := accrualPlan(day("2000-01-01"), "hire", "monthly", 1, day("2026-06-01"))
	last := long[len(long)-1]
	if last.Days != 25 {
		t.Fatalf("상한: %+v", last)
	}
	// 2월 29일 입사 기념일은 비윤년에 2월 28일.
	leap := accrualPlan(day("2024-02-29"), "hire", "monthly", 1, day("2026-03-01"))
	for _, e := range leap {
		if e.Key == "hire:y01" && e.Date != "2025-02-28" {
			t.Fatalf("윤일 기념일: %+v", e)
		}
	}
	// 말일 처리: 1월 31일 입사의 한 달 뒤는 2월 말일.
	if d := lvFmtDay(lvAddMonths(day("2025-01-31"), 1)); d != "2025-02-28" {
		t.Fatalf("말일 보정: %s", d)
	}
}

func cumulative(ev []accrualEvent, until string) int {
	n := 0
	for _, e := range ev {
		if e.Date <= until {
			n += e.Days
		}
	}
	return n
}

func TestAccrualFiscalBasisNeverLessThanHireBasis(t *testing.T) {
	// 회계연도 기준은 어떤 입사일·회계연도 시작월 조합에서도 어느 날짜까지의 누적 발생이 입사일 기준보다 적지 않다(법정 이상 보장, 명세 5.2·D3).
	until := day("2036-12-31")
	for _, hireS := range []string{"2024-01-01", "2024-03-15", "2024-06-30", "2024-11-02", "2024-12-31", "2025-02-28"} {
		hire := day(hireS)
		for fsm := 1; fsm <= 12; fsm++ {
			for _, mode := range []string{"monthly", "upfront"} {
				h := accrualPlan(hire, "hire", mode, fsm, until)
				f := accrualPlan(hire, "fiscal", mode, fsm, until)
				for d := hire; !d.After(until); d = d.AddDate(0, 0, 7) {
					ds := lvFmtDay(d)
					if cumulative(f, ds) < cumulative(h, ds) {
						t.Fatalf("입사 %s·회계연도 시작 %d월·%s: %s 까지 회계연도 %d < 입사일 %d", hireS, fsm, mode, ds, cumulative(f, ds), cumulative(h, ds))
					}
				}
			}
		}
	}
}

func TestAccrualFiscalFirstYearAdjustment(t *testing.T) {
	// 3월 15일 입사, 회계연도 1월 시작: 첫 회계연도 시작일은 다음 해 1월 1일. 그 전 월 개근 9번(4/15~12/15), 시작일에 11-9=2일 보정, 같은 날 정식 15일.
	ev := accrualPlan(day("2026-03-15"), "fiscal", "monthly", 1, day("2027-01-02"))
	m := map[string]accrualEvent{}
	for _, e := range ev {
		m[e.Key] = e
	}
	if len(ev) != 9+2 || m["fiscal:adj"].Days != 2 || m["fiscal:adj"].Date != "2027-01-01" || m["fiscal:adj"].Reason != "회계연도 기준 보정" {
		t.Fatalf("보정: %+v", ev)
	}
	if y := m["fiscal:y2027"]; y.Days != 15 || y.Date != "2027-01-01" {
		t.Fatalf("첫 정식 발생: %+v", y)
	}
	// 선지급이면 보정이 없다.
	up := accrualPlan(day("2026-03-15"), "fiscal", "upfront", 1, day("2027-01-02"))
	for _, e := range up {
		if e.Key == "fiscal:adj" {
			t.Fatalf("선지급인데 보정이 있음: %+v", up)
		}
	}
	if sumDays(up) != 11+15 {
		t.Fatalf("선지급 합계: %d", sumDays(up))
	}
}

func TestFifoRemaining(t *testing.T) {
	g := []repository.ExpiringGrant{{ID: 1, Minutes: 480, ExpiresOn: "2026-12-01"}, {ID: 2, Minutes: 960, ExpiresOn: "2027-06-01"}, {ID: 3, Minutes: 480, ExpiresOn: "2027-09-01", Expired: 0}}
	// 사용 600분: 먼저 소멸하는 줄부터 깎는다.
	got := fifoRemaining(g, 600)
	if got[0] != 0 || got[1] != 840 || got[2] != 480 {
		t.Fatalf("FIFO: %v", got)
	}
	// 이미 소멸한 분량은 그 줄에서 빠진다(사용량은 소멸 뒤 남은 줄부터).
	g[0].Expired = 480
	got = fifoRemaining(g, 100)
	if got[0] != 0 || got[1] != 860 || got[2] != 480 {
		t.Fatalf("소멸 반영: %v", got)
	}
	// 사용량이 전체를 넘어도 음수가 되지 않는다.
	for _, v := range fifoRemaining(g, 100000) {
		if v != 0 {
			t.Fatalf("초과 사용: %v", v)
		}
	}
}
