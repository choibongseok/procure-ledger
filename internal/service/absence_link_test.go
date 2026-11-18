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
)

// B50 8단계(휴직 연동): 휴직 중 출근 거부, 출근율 80% 의 휴직 구간 처리, 승인된 휴가 기간에 휴직 발령 시 연차 복원.

// hrP 는 휴직 발령자(조직 관리자) 역할의 주체다. 감사 기록기가 채널을 요구한다.
func hrP() contract.Principal {
	p := users[uAdmin]
	p.Channel = contract.ChannelWeb
	return p
}

// seedAbsenceSpan 은 조직 서비스가 쓴 것과 같은 모양의 휴직 구간을 넣는다(구간 표는 조직 서비스만 쓰므로 쓰기 보호 행을 잠깐 연다). to 가 빈 값이면 열린 구간이다.
func seedAbsenceSpan(t *testing.T, e *env, user int64, from, to, kind string) {
	t.Helper()
	toSQL := "NULL"
	if to != "" {
		toSQL = "'" + to + "'"
	}
	for _, q := range []string{
		`INSERT INTO org_units (id, code) VALUES (7001, 'ABS') ON CONFLICT DO NOTHING`,
		`INSERT INTO org_write_guard (id) VALUES (1)`,
		fmt.Sprintf(`INSERT INTO org_person_spans (user_id, valid_from, valid_to, left_flag, rank_id, primary_unit_id, absence_kind) VALUES (%d, '%s', %s, 0, 1, 7001, '%s')`, user, from, toSQL, kind),
		`DELETE FROM org_write_guard`,
	} {
		if _, err := e.db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
}

func clearAbsenceSpans(t *testing.T, e *env, user int64) {
	t.Helper()
	for _, q := range []string{`INSERT INTO org_write_guard (id) VALUES (1)`, fmt.Sprintf(`DELETE FROM org_person_spans WHERE user_id = %d`, user), `DELETE FROM org_write_guard`} {
		if _, err := e.db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
}

// AC-5035: 휴직 중인 사람은 체크인·체크아웃이 거부되고 한국어 문구에 휴직 구분이 없다. 복직하면 기록된다.
func TestAttendanceClockBlockedWhileOnLeave(t *testing.T) {
	a := newEnv(t).attendance(t)
	a.at("2026-11-10", "09:00")
	a.org.SetOnLeave(uStaff, true)
	for name, call := range map[string]func() (domain.AttendanceToday, error){
		"체크인":  func() (domain.AttendanceToday, error) { return a.svc.CheckIn(as(uStaff)) },
		"체크아웃": func() (domain.AttendanceToday, error) { return a.svc.CheckOut(as(uStaff)) },
	} {
		_, err := call()
		if !errors.Is(err, contract.ErrValidation) || !strings.Contains(err.Error(), "휴직 중에는 출퇴근을 기록할 수 없습니다") {
			t.Fatalf("%s: 휴직 중 거부 문구: %v", name, err)
		}
		if strings.Contains(err.Error(), "질병") || strings.Contains(err.Error(), "illness") {
			t.Fatalf("%s: 문구에 휴직 구분: %v", name, err)
		}
	}
	var n int
	_ = a.db.QueryRow(`SELECT COUNT(*) FROM attendance_records WHERE user_id = ?`, uStaff).Scan(&n)
	if n != 0 {
		t.Fatalf("거부된 체크인이 기록됨: %d", n)
	}
	a.org.SetOnLeave(uStaff, false)
	if got, err := a.svc.CheckIn(as(uStaff)); err != nil || got.Today.CheckIn != "09:00" {
		t.Fatalf("복직 뒤 출근: %+v %v", got.Today, err)
	}
}

// 출근율 80% 판정의 휴직 구간 처리(표 시험). 출근 간주 구분(육아)은 출근으로 세고, 아닌 구분(질병·가족 돌봄·개인·기타)은 소정근로일에서 뺀다.
func TestAttendanceRateAbsenceKinds(t *testing.T) {
	// 입사 2024-03-15, 근속 2년(2026-03-15) 기준일. 기록은 2025-03-14(금) 부터 8월 말까지만 있고 9월 이후는 휴직(2026-03-15 복직)이다.
	build := func(t *testing.T, kind string) int64 {
		a := newEnv(t).attendance(t)
		first := stdPolicy
		first.EffectiveFrom = "2025-01-01"
		if _, err := a.leave.svc.SavePolicy(as(uAdmin), first); err != nil {
			t.Fatal(err)
		}
		a.hire(t, uStaff, "2024-03-15")
		a.now = time.Date(2026, 3, 20, 3, 0, 0, 0, time.UTC)
		for d, _ := time.Parse("2006-01-02", "2025-03-14"); !d.After(mustDay("2025-08-31")); d = d.AddDate(0, 0, 1) {
			if d.Weekday() == time.Saturday || d.Weekday() == time.Sunday {
				continue
			}
			a.seedRecord(t, uStaff, d.Format("2006-01-02"), "09:00", "18:00")
		}
		if kind != "" {
			seedAbsenceSpan(t, a.env, uStaff, "2025-09-01", "2026-03-15", kind)
		}
		if _, err := a.leave.svc.AccrueUser(context.Background(), uStaff, contract.DefaultEntityID, a.now); err != nil {
			t.Fatal(err)
		}
		var m int64
		_ = a.db.QueryRow(`SELECT COALESCE(SUM(minutes),0) FROM leave_ledger WHERE user_id = ? AND period_key = 'hire:y02'`, uStaff).Scan(&m)
		return m
	}
	cases := []struct {
		name, kind string
		want       int64
	}{
		{"휴직 없음(출근율 미달: 개근한 달 5개월)", "", 5 * 480},
		{"육아휴직(출근 간주)", "childcare", 15 * 480},
		{"질병(소정근로일 제외)", "illness", 15 * 480},
		{"가족 돌봄(소정근로일 제외)", "family_care", 15 * 480},
		{"개인(소정근로일 제외)", "personal", 15 * 480},
		{"기타(소정근로일 제외)", "other", 15 * 480},
	}
	for _, c := range cases {
		if got := build(t, c.kind); got != c.want {
			t.Errorf("%s: %d분(기대 %d)", c.name, got, c.want)
		}
	}
}

// 소정근로일이 휴직으로 모두 빠지면 출근율을 판정하지 않고 충족으로 본다(0 으로 나누어 미달이 되지 않는다).
func TestAttendanceRateAllDaysExcludedCountsAsMet(t *testing.T) {
	a := newEnv(t).attendance(t)
	first := stdPolicy
	first.EffectiveFrom = "2025-01-01"
	if _, err := a.leave.svc.SavePolicy(as(uAdmin), first); err != nil {
		t.Fatal(err)
	}
	a.hire(t, uStaff, "2024-03-15")
	a.now = time.Date(2026, 3, 20, 3, 0, 0, 0, time.UTC)
	a.seedRecord(t, uStaff, "2025-03-14", "09:00", "18:00") // 기록 도입일(이 날 이전 기준일은 판정 대상이 아니다)
	seedAbsenceSpan(t, a.env, uStaff, "2025-03-15", "2026-03-15", "personal")
	if _, err := a.leave.svc.AccrueUser(context.Background(), uStaff, contract.DefaultEntityID, a.now); err != nil {
		t.Fatal(err)
	}
	var m int64
	_ = a.db.QueryRow(`SELECT COALESCE(SUM(minutes),0) FROM leave_ledger WHERE user_id = ? AND period_key = 'hire:y02'`, uStaff).Scan(&m)
	if m != 15*480 {
		t.Fatalf("1년 내내 휴직이어도 줄지 않아야 함: %d분", m)
	}
}

// 16절: 승인된 휴가 기간에 휴직이 발령되면 겹치는 소정근로일 분량을 복원 줄로 되돌린다. 원장은 추가만 하고 합은 맞춘다(멱등, 휴직 변경 시 되돌림, 휴가 취소와 이중 복원 없음).
func TestLeaveRestoredWhenAbsenceOverlapsApprovedLeave(t *testing.T) {
	e := newEnv(t)
	l := e.leave(t)
	l.policy(t)
	l.open(t, uStaff, 10)
	r, err := l.svc.Request(as(uStaff), in("annual", "2026-11-16", "2026-11-20")) // 월~금 5일
	if err != nil || r.Minutes != 5*480 {
		t.Fatalf("신청: %+v %v", r, err)
	}
	decideAs(t, e, uLead, r.DocID, true, "")
	if avail, _ := l.minutes(t, uStaff); avail != 5*480 {
		t.Fatalf("승인 뒤 잔여 %d", avail)
	}
	restore := func() {
		t.Helper()
		err := contract.SQLTxManager{DB: e.db}.WithTx(context.Background(), func(tx contract.Tx) error {
			return l.svc.RestoreForAbsence(context.Background(), tx, hrP(), uStaff)
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	absLines := func() (n int, sum int64) {
		_ = e.db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(minutes),0) FROM leave_ledger WHERE ref_type = 'leave_absence' AND ref_id = ?`, r.ID).Scan(&n, &sum)
		return
	}
	// 휴직이 없으면 아무것도 하지 않는다.
	restore()
	if n, _ := absLines(); n != 0 {
		t.Fatalf("휴직 없이 복원 줄이 생김: %d", n)
	}
	// 11-18(수)부터 열린 휴직: 수·목·금 3일을 돌려준다. 신청은 그대로(승인).
	seedAbsenceSpan(t, e, uStaff, "2026-11-18", "", "illness")
	restore()
	if avail, _ := l.minutes(t, uStaff); avail != 8*480 {
		t.Fatalf("복원 뒤 잔여 %d(기대 8일)", avail)
	}
	if n, sum := absLines(); n != 1 || sum != 3*480 {
		t.Fatalf("복원 줄 %d건 합 %d", n, sum)
	}
	if got, _ := l.svc.Get(as(uStaff), r.ID); got.Status != domain.LeaveApproved || got.Minutes != 5*480 {
		t.Fatalf("신청이 바뀜: %+v", got)
	}
	// 같은 발령을 다시 처리해도 한 번만 복원한다(멱등).
	restore()
	if n, sum := absLines(); n != 1 || sum != 3*480 {
		t.Fatalf("재처리로 줄이 늘어남: %d건 합 %d", n, sum)
	}
	// 복직(11-19 효력, 구간 [11-18, 11-19)) 으로 구간이 줄면 차이만큼 되돌림 줄(사용)을 더한다.
	clearAbsenceSpans(t, e, uStaff)
	seedAbsenceSpan(t, e, uStaff, "2026-11-18", "2026-11-19", "illness")
	restore()
	if avail, _ := l.minutes(t, uStaff); avail != 6*480 {
		t.Fatalf("구간이 줄어든 뒤 잔여 %d(기대 6일)", avail)
	}
	if n, sum := absLines(); n != 2 || sum != 480 {
		t.Fatalf("되돌림 줄 %d건 합 %d", n, sum)
	}
	// 휴가를 취소하면 이미 복원한 하루는 빼고 나머지만 돌려준다(두 번 복원하지 않는다).
	if _, err := l.svc.Cancel(as(uStaff), r.ID, "일정 변경"); err != nil {
		t.Fatalf("취소: %v", err)
	}
	if avail, _ := l.minutes(t, uStaff); avail != 10*480 {
		t.Fatalf("취소 뒤 잔여 %d(기대 10일, 이중 복원 없음)", avail)
	}
	// 취소된 신청은 이후 휴직 변경에도 건드리지 않는다.
	clearAbsenceSpans(t, e, uStaff)
	restore()
	if avail, _ := l.minutes(t, uStaff); avail != 10*480 {
		t.Fatalf("취소된 신청이 다시 움직임: %d", avail)
	}
	// 감사: 복원은 휴가 신청 대상으로 남는다.
	if !e.rec.Has("leave.absence_restore", "leave_request", "success") {
		t.Fatal("복원 감사 없음")
	}
}

// 반차·시간 단위 하루짜리 휴가는 그날이 휴직 구간이면 전부, 아니면 하나도 돌려주지 않는다. 휴직 전에 끝난 휴가는 건드리지 않는다.
func TestLeaveRestoreSingleDayAndBeforeAbsence(t *testing.T) {
	e := newEnv(t)
	l := e.leave(t)
	l.policy(t)
	l.open(t, uStaff, 10)
	half := in("annual", "2026-11-17", "2026-11-17")
	half.Part = domain.LeavePartAM
	r1, err := l.svc.Request(as(uStaff), half)
	if err != nil {
		t.Fatal(err)
	}
	decideAs(t, e, uLead, r1.DocID, true, "")
	r2, err := l.svc.Request(as(uStaff), in("annual", "2026-11-16", "2026-11-16"))
	if err != nil {
		t.Fatal(err)
	}
	decideAs(t, e, uLead, r2.DocID, true, "")
	seedAbsenceSpan(t, e, uStaff, "2026-11-17", "", "personal") // 11-17 부터 휴직: 11-16 휴가는 그대로
	if err := (contract.SQLTxManager{DB: e.db}).WithTx(context.Background(), func(tx contract.Tx) error {
		return l.svc.RestoreForAbsence(context.Background(), tx, hrP(), uStaff)
	}); err != nil {
		t.Fatal(err)
	}
	var s1, s2 int64
	_ = e.db.QueryRow(`SELECT COALESCE(SUM(minutes),0) FROM leave_ledger WHERE ref_type = 'leave_absence' AND ref_id = ?`, r1.ID).Scan(&s1)
	_ = e.db.QueryRow(`SELECT COALESCE(SUM(minutes),0) FROM leave_ledger WHERE ref_type = 'leave_absence' AND ref_id = ?`, r2.ID).Scan(&s2)
	if s1 != 240 || s2 != 0 {
		t.Fatalf("반차 복원 %d(기대 240) 휴직 전 휴가 복원 %d(기대 0)", s1, s2)
	}
}
