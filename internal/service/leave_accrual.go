package service

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/repository"
)

// 연차 발생·이월·소멸(B50 5.2). 계산은 순수 함수(accrualPlan)이고 서비스는 그 결과를 원장에 멱등 키로 쓴다.
// 법정 하한은 legalMinimum 한 곳이다. 아래 값은 공개 법령 요약이며 운영 전 노무 검토가 필요하다(명세 5.1).

// legalMinimum 은 연차의 법정 하한 상수 표다(근로기준법 제60조 요약, **운영 전 노무 검토 필요**). 값이 바뀌면 이 표와 명세 5.1 표를 함께 고친다.
var legalMinimum = struct {
	FullYearDays      int // 1년간 80% 이상 출근한 근로자의 연차
	FirstYearMaxDays  int // 계속 근로 1년 미만(또는 80% 미만 출근) 근로자의 월 1일 발생 합계 상한
	TenureStepYears   int // 가산 주기(년)
	TenureAddDays     int // 주기마다 가산 일수
	CapDays           int // 총 한도
	MinExpireMonths   int // 소멸 시효(월): 이보다 짧게 정할 수 없다
	MinTenureYearsAdd int // 가산이 시작되는 계속 근로 연수(3년 이상)
	// AttendanceRatePercent 는 연차 15일 발생의 출근율 하한(%)이다. 미만이면 개근한 달마다 1일(제60조 제2항).
	AttendanceRatePercent int
}{FullYearDays: 15, FirstYearMaxDays: 11, TenureStepYears: 2, TenureAddDays: 1, CapDays: 25, MinExpireMonths: 12, MinTenureYearsAdd: 3, AttendanceRatePercent: 80}

// annualDaysForYears 는 계속 근로 n년을 채운 시점의 연차 일수다: 15일 + (n-1)/2 일 가산(3년 이상부터), 상한 25일.
func annualDaysForYears(n int) int {
	d := legalMinimum.FullYearDays
	if n >= legalMinimum.MinTenureYearsAdd {
		d += (n - 1) / legalMinimum.TenureStepYears * legalMinimum.TenureAddDays
	}
	if d > legalMinimum.CapDays {
		d = legalMinimum.CapDays
	}
	return d
}

// accrualEvent 는 발생해야 할 연차 한 건이다. Key 는 원장의 멱등 키(period_key)다.
type accrualEvent struct {
	Key    string
	Date   string // 발생일(KST YYYY-MM-DD)
	Days   int
	Reason string
}

func lvParseDay(s string) (time.Time, error) { return time.Parse("2006-01-02", s) }
func lvFmtDay(t time.Time) string            { return t.Format("2006-01-02") }

// lvAddMonths 는 월을 더하되 말일을 넘으면 그 달 말일로 맞춘다(1월 31일 + 1개월 = 2월 말일).
func lvAddMonths(t time.Time, n int) time.Time {
	y, m, d := t.Date()
	first := time.Date(y, m+time.Month(n), 1, 0, 0, 0, 0, time.UTC)
	last := time.Date(first.Year(), first.Month()+1, 0, 0, 0, 0, 0, time.UTC).Day()
	if d > last {
		d = last
	}
	return time.Date(first.Year(), first.Month(), d, 0, 0, 0, 0, time.UTC)
}

// lvYears 은 a 에서 b 까지 채운 햇수다(기념일 기준, 2월 29일 입사는 비윤년에 2월 28일로 본다).
func lvYears(a, b time.Time) int {
	n := 0
	for !lvAddMonths(a, 12*(n+1)).After(b) {
		n++
	}
	return n
}

// accrualPlan 은 입사일 hire 부터 until 까지 발생해야 할 연차 목록이다(발생일 순). 규칙의 효력일보다 이른 발생은 호출자가 거른다(이전 잔여는 초기 이관으로 넣는다).
// 출근율 80% 판정은 accrueUser 가 출퇴근 기록과 연결해 적용한다(leave_attendance_rate.go). 이 함수는 출근율을 충족한 경우의 계획이다.
func accrualPlan(hire time.Time, basis, firstYearMode string, fiscalStartMonth int, until time.Time) []accrualEvent {
	var out []accrualEvent
	add := func(key string, date time.Time, days int, reason string) {
		if !date.After(until) && days > 0 {
			out = append(out, accrualEvent{Key: key, Date: lvFmtDay(date), Days: days, Reason: reason})
		}
	}
	switch basis {
	case "fiscal":
		// 첫 회계연도 시작일 F1: 입사일보다 늦은 가장 이른 회계연도 시작일.
		f1 := time.Date(hire.Year(), time.Month(fiscalStartMonth), 1, 0, 0, 0, 0, time.UTC)
		for !f1.After(hire) {
			f1 = lvAddMonths(f1, 12)
		}
		granted := 0
		if firstYearMode == "upfront" {
			add("fiscal:first", hire, legalMinimum.FirstYearMaxDays, "입사 첫해 선지급")
			granted = legalMinimum.FirstYearMaxDays
		} else {
			for m := 1; m <= legalMinimum.FirstYearMaxDays; m++ {
				d := lvAddMonths(hire, m)
				if !d.Before(f1) {
					break
				}
				add(fmt.Sprintf("fiscal:m%02d", m), d, 1, "입사 첫해 월 개근")
				granted++
			}
		}
		if rest := legalMinimum.FirstYearMaxDays - granted; rest > 0 {
			add("fiscal:adj", f1, rest, "회계연도 기준 보정")
		}
		for f := f1; !f.After(until); f = lvAddMonths(f, 12) {
			n := lvYears(hire, lvAddMonths(f, 12)) // 그 회계연도 안에 채우는 근속 햇수(가산이 그 해 안에서 불리하지 않게)
			add("fiscal:y"+fmt.Sprint(f.Year()), f, annualDaysForYears(n), fmt.Sprintf("회계연도 %d 연차(근속 %d년)", f.Year(), n))
		}
	default: // hire: 입사일 기준
		if firstYearMode == "upfront" {
			add("hire:first", hire, legalMinimum.FirstYearMaxDays, "입사 첫해 선지급")
		} else {
			for m := 1; m <= legalMinimum.FirstYearMaxDays; m++ {
				add(fmt.Sprintf("hire:m%02d", m), lvAddMonths(hire, m), 1, "입사 첫해 월 개근")
			}
		}
		for n := 1; ; n++ {
			d := lvAddMonths(hire, 12*n)
			if d.After(until) {
				break
			}
			add(fmt.Sprintf("hire:y%02d", n), d, annualDaysForYears(n), fmt.Sprintf("근속 %d년 연차", n))
		}
	}
	return out
}

// moduleOn 은 근태·휴가 모듈이 켜져 있는지다(주입이 없으면 켜진 것으로 본다: 시험·서비스 직접 호출).
func (b Base) leaveModuleOn(ctx context.Context) bool {
	if b.Modules == nil {
		return true
	}
	return b.Modules.Enabled(ctx, "attendance")
}

// leaveModuleOnTx 는 트랜잭션 안에서 쓰는 모듈 켜짐 판정이다(tx 핸들로 읽는다).
func (b Base) leaveModuleOnTx(ctx context.Context, tx contract.Tx) bool {
	if b.Modules == nil {
		return true
	}
	return b.Modules.EnabledTx(ctx, tx, "attendance")
}

func (s *LeaveService) moduleOn(ctx context.Context) bool { return s.leaveModuleOn(ctx) }

// fifoRemaining 은 소멸 대상 줄(소멸일 순)마다 남은 분량이다. 사용량 consumed 를 소멸일이 이른 줄부터 깎는다(이미 소멸한 분량은 그 줄에서 빠진다).
func fifoRemaining(grants []repository.ExpiringGrant, consumed int64) []int64 {
	out := make([]int64, len(grants))
	for i, g := range grants {
		capacity := g.Minutes - g.Expired
		use := consumed
		if use > capacity {
			use = capacity
		}
		if use < 0 {
			use = 0
		}
		consumed -= use
		out[i] = capacity - use
	}
	return out
}

// AccrueUser 는 한 사람의 발생·이월·소멸을 asOf 까지 따라잡는다(멱등: 이미 쓴 키는 건너뛴다). 한 트랜잭션 안에서 부른다. 새로 쓴 줄 수를 돌려준다.
func (s *LeaveService) accrueUser(ctx context.Context, tx contract.Tx, userID, entityID int64, asOf time.Time) (int, error) {
	day := string(contract.DayOf(asOf))
	first, err := s.Repo.FirstPolicyDate(ctx, tx)
	if err != nil {
		return 0, err
	}
	if first == "" {
		return 0, nil // 규칙이 없으면 발생하지 않는다
	}
	hireStr, err := s.Repo.HireDate(ctx, tx, userID, day)
	if err != nil || hireStr == "" {
		return 0, err
	}
	hire, err := lvParseDay(hireStr)
	if err != nil {
		return 0, err
	}
	until, _ := lvParseDay(day)
	pol, ok, err := s.Repo.PolicyOn(ctx, tx, day)
	if err != nil || !ok {
		return 0, err
	}
	fsm, err := s.Repo.FiscalStartMonth(ctx, tx)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, ev := range accrualPlan(hire, pol.Basis, pol.FirstYearMode, fsm, until) {
		if ev.Date < first {
			continue // 규칙 효력일 이전 발생은 만들지 않는다
		}
		p, ok, err := s.Repo.PolicyOn(ctx, tx, ev.Date)
		if err != nil {
			return n, err
		}
		if !ok {
			p = pol
		}
		ev, ok, err = s.applyAttendanceRule(ctx, tx, userID, hire, ev, p.AttendanceRule)
		if err != nil {
			return n, err
		}
		if !ok {
			continue
		}
		evDay, _ := lvParseDay(ev.Date)
		wrote, err := s.Repo.Append(ctx, tx, repository.LeaveLedgerEntry{EntityID: entityID, UserID: userID, Balance: domain.LeaveBalanceAnnual, Kind: "grant",
			Minutes: int64(ev.Days) * int64(p.WorkdayMinutes), PeriodKey: ev.Key, ExpiresOn: lvFmtDay(lvAddMonths(evDay, p.ExpireMonths)), Reason: ev.Reason, At: s.now()})
		if err != nil {
			return n, err
		}
		if wrote {
			n++
		}
	}
	m, err := s.expireUser(ctx, tx, userID, entityID, day, pol)
	return n + m, err
}

// expireUser 는 소멸일이 지난 줄의 남은 분량을 소멸 줄로 쓰고, 이월이 허용되면 이월 줄을 쓴다(명세 5.2). 소멸 대상 줄을 소멸일 순으로 먼저 쓴 것으로 보고(FIFO) 남은 분량을 센다.
func (s *LeaveService) expireUser(ctx context.Context, tx contract.Tx, userID, entityID int64, today string, pol domain.LeavePolicy) (int, error) {
	n := 0
	for _, balance := range []string{domain.LeaveBalanceAnnual, domain.LeaveBalanceCompensatory} {
		grants, err := s.Repo.ExpiringGrants(ctx, tx, userID, balance)
		if err != nil {
			return n, err
		}
		consumed, err := s.Repo.ConsumedMinutes(ctx, tx, userID, balance)
		if err != nil {
			return n, err
		}
		rem := fifoRemaining(grants, consumed)
		for i, g := range grants {
			remaining := rem[i]
			if g.ExpiresOn > today || remaining <= 0 {
				continue
			}
			ref := fmt.Sprint(g.ID)
			if wrote, err := s.Repo.Append(ctx, tx, repository.LeaveLedgerEntry{EntityID: entityID, UserID: userID, Balance: balance, Kind: "expire", Minutes: -remaining,
				PeriodKey: "exp:" + ref, RefType: "leave_ledger", RefID: g.ID, Reason: "소멸", At: s.now()}); err != nil {
				return n, err
			} else if wrote {
				n++
			}
			carry := pol.CarryMaxMinutes
			if g.Kind != "grant" || balance != domain.LeaveBalanceAnnual || carry == 0 {
				continue
			}
			c := remaining
			if carry > 0 && int64(carry) < c {
				c = int64(carry)
			}
			exp, _ := lvParseDay(g.ExpiresOn)
			if wrote, err := s.Repo.Append(ctx, tx, repository.LeaveLedgerEntry{EntityID: entityID, UserID: userID, Balance: balance, Kind: "carry", Minutes: c,
				PeriodKey: "carry:" + ref, ExpiresOn: lvFmtDay(lvAddMonths(exp, pol.CarryValidMonths)), RefType: "leave_ledger", RefID: g.ID, Reason: "이월", At: s.now()}); err != nil {
				return n, err
			} else if wrote {
				n++
			}
		}
	}
	return n, nil
}

// RunOnce 는 재직 중인 모든 사람의 발생·소멸을 따라잡는다. 사람마다 한 트랜잭션이라 한 사람의 실패가 다른 사람을 막지 않는다. 처리한 줄 수를 돌려준다.
// 법인별로 현재 법인 context 를 씌워 법인 조건이 그 법인의 규칙·원장만 보게 한다. 모듈이 꺼져 있으면 하지 않는다.
func (s *LeaveService) RunOnce(ctx context.Context) (int, error) {
	if !s.moduleOn(ctx) {
		return 0, nil
	}
	asOf := s.now()
	day := string(contract.DayOf(asOf))
	emps, err := s.Repo.ActiveEmployees(ctx, s.DB, day)
	if err != nil {
		return 0, err
	}
	total := 0
	for userID, entityID := range emps {
		wrote, err := s.AccrueUser(ctx, userID, entityID, asOf)
		if err != nil {
			log.Printf("leave: 연차 발생 실패(사용자 %d): %v", userID, err)
			continue
		}
		total += wrote
		if _, err := s.NotifyExpiring(ctx, userID, entityID, asOf); err != nil {
			log.Printf("leave: 소멸 예정 알림 실패(사용자 %d): %v", userID, err)
		}
	}
	if s.OverdueReturns != nil {
		if _, err := s.OverdueReturns(ctx, asOf); err != nil {
			log.Printf("leave: 예정 복직일 경과 알림 실패: %v", err)
		}
	}
	return total, nil
}

// Run 은 서버가 켜져 있는 동안 기동 때 한 번, 그 뒤 한 시간마다 RunOnce 를 돌린다(발생일 하루가 지나면 다음 실행이 따라잡는다).
func (s *LeaveService) Run(ctx context.Context) {
	tick := func() {
		if _, err := s.RunOnce(ctx); err != nil && ctx.Err() == nil {
			log.Printf("leave: 연차 발생 작업 실패: %v", err)
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

// AccrueUser 는 한 사람의 발생·이월·소멸을 asOf 까지 한 트랜잭션에서 따라잡는다(멱등). 새로 쓴 원장 줄 수를 돌려준다. 일일 작업(RunOnce)과 시험이 쓴다.
// 그 사람의 법인으로 현재 법인 context 를 씌워 법인 조건이 그 법인의 규칙·원장만 보게 한다.
func (s *LeaveService) AccrueUser(ctx context.Context, userID, entityID int64, asOf time.Time) (int, error) {
	ectx := contract.WithActiveEntity(ctx, contract.ActiveEntity{ID: entityID})
	day := string(contract.DayOf(asOf))
	var wrote int
	err := s.Txs.WithTx(ectx, func(tx contract.Tx) error {
		if err := s.Repo.LockUser(ectx, tx, userID); err != nil {
			return err
		}
		if err := s.Repo.EnsureTypes(ectx, tx, entityID, asOf); err != nil {
			return err
		}
		n, err := s.accrueUser(ectx, tx, userID, entityID, asOf)
		if err != nil {
			return err
		}
		wrote = n
		if n == 0 {
			return nil
		}
		return s.Audit.Record(ectx, tx, audit.Entry{Actor: contract.Principal{Name: "시스템", Role: contract.RoleAdmin, Channel: contract.ChannelCLI}, Action: "leave.accrue", TargetType: audit.TargetLeaveRequest,
			TargetID: fmt.Sprint(userID), Result: audit.ResultSuccess, Diff: []audit.DiffItem{{Key: "lines", New: fmt.Sprint(n)}, {Key: "as_of", New: day}}})
	})
	return wrote, err
}
