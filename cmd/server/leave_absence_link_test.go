package main

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/choibongseok/procure-ledger/internal/bootstrap"
	"github.com/choibongseok/procure-ledger/internal/config"
	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/deps"
	"github.com/choibongseok/procure-ledger/internal/dbtest"
	"github.com/choibongseok/procure-ledger/internal/org"
)

// B50 8단계(휴직 연동)를 실제 서버 조립(실제 조직·휴가·근태 서비스) 위에서 본다.
//   - 승인된 휴가 기간에 휴직을 발령하면 같은 트랜잭션에서 연차가 복원되고, 휴직을 복직·취소·정정하면 같은 식으로 맞춰진다.
//   - 휴직 중 체크인 거부, 팀 현황·달력·MCP 부재 조회에 "부재"로 합쳐지고(구분·사유 없음), 예정 복직일 경과 알림은 한 번만 간다.

type absenceEnv struct {
	d      deps.Deps
	ctx    context.Context
	osvc   *org.Service
	admin  contract.Principal
	lead   contract.Principal
	staff  contract.Principal
	userID func(email string) int64
}

func newAbsenceEnv(t *testing.T) *absenceEnv {
	t.Helper()
	dbtest.SetEnv(t)
	t.Setenv("PL_DEV_IDP", "1")
	t.Setenv("PL_LLM_PROVIDER", "mock")
	cfg := config.Load()
	ctx := context.Background()
	d, closeDB, err := bootstrap.Build(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(closeDB)
	osvc := d.Org.(*org.Service)
	if did, err := osvc.SeedDev(ctx); err != nil || !did {
		t.Fatalf("개발용 조직 시드: %v %v", did, err)
	}
	if _, err := d.DB.ExecContext(ctx, `INSERT INTO app_settings (key, value) VALUES ('module.attendance', 'on') ON CONFLICT(key) DO UPDATE SET value = 'on'`); err != nil {
		t.Fatal(err)
	}
	e := &absenceEnv{d: d, ctx: ctx, osvc: osvc}
	e.userID = func(email string) int64 {
		var id int64
		if err := d.DB.QueryRowContext(ctx, `SELECT id FROM users WHERE email = ?`, email).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	mk := func(email string, role contract.Role) contract.Principal {
		var dept int64
		if err := d.DB.QueryRowContext(ctx, `SELECT COALESCE(department_id,0) FROM users WHERE email = ?`, email).Scan(&dept); err != nil {
			t.Fatal(err)
		}
		return contract.Principal{UserID: e.userID(email), Name: email, Role: role, DepartmentID: dept, Channel: contract.ChannelWeb}
	}
	e.admin = mk("admin@pl.test", contract.RoleAdmin)
	e.lead = mk("lead@pl.test", contract.RoleManager)
	e.staff = mk("staff@pl.test", contract.RoleStaff)
	return e
}

func (e *absenceEnv) as(p contract.Principal) context.Context {
	return contract.WithPrincipal(e.ctx, p)
}

func (e *absenceEnv) ledgerSum(t *testing.T, user int64) int64 {
	t.Helper()
	var n int64
	if err := e.d.DB.QueryRow(`SELECT COALESCE(SUM(minutes),0) FROM leave_ledger WHERE user_id = ? AND balance = 'annual'`, user).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// 승인된 휴가 기간에 휴직 발령 → 복원 줄, 복직·취소로 구간이 바뀌면 다시 맞춘다(원장은 추가만).
func TestAbsenceRestoresApprovedLeaveThroughOrgService(t *testing.T) {
	e := newAbsenceEnv(t)
	staff := e.staff.UserID
	// 공휴일이 없는 월~금 한 주를 찾는다(소정근로일 5일).
	var mon time.Time
	for off := 30; off < 120; off += 7 {
		d := time.Now().In(contract.KST).AddDate(0, 0, off)
		d = d.AddDate(0, 0, -int((d.Weekday()+6)%7)) // 그 주 월요일
		var n int
		if err := e.d.DB.QueryRow(`SELECT COUNT(*) FROM holidays WHERE date BETWEEN ? AND ?`, d.Format("2006-01-02"), d.AddDate(0, 0, 4).Format("2006-01-02")).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			mon = d
			break
		}
	}
	if mon.IsZero() {
		t.Fatal("공휴일 없는 주를 찾지 못함")
	}
	day := func(i int) string { return mon.AddDate(0, 0, i).Format("2006-01-02") }
	stamp := time.Now().UTC().Format(time.RFC3339)
	var doc int64
	if err := e.d.DB.QueryRow(`INSERT INTO purchase_requests (no, requester_id, department_id, title, status, form_id, fields, created_at, updated_at)
		VALUES ('LV-ABS-1', ?, ?, '휴가 신청서', 'approved', (SELECT id FROM approval_forms WHERE code = 'LEAVE'), '{}', ?, ?) RETURNING id`, staff, e.staff.DepartmentID, stamp, stamp).Scan(&doc); err != nil {
		t.Fatal(err)
	}
	var reqID int64
	if err := e.d.DB.QueryRow(`INSERT INTO leave_requests (user_id, doc_id, leave_code, start_date, end_date, part, minutes, balance, status, reason, note, created_at, updated_at)
		VALUES (?, ?, 'annual', ?, ?, 'full', 2400, 'annual', 'approved', '', '', ?, ?) RETURNING id`, staff, doc, day(0), day(4), stamp, stamp).Scan(&reqID); err != nil {
		t.Fatal(err)
	}
	for _, q := range []struct {
		kind    string
		minutes int64
		ref     int64
	}{{"grant", 4800 * 2, 0}, {"use", -2400, reqID}} {
		refType := ""
		if q.ref != 0 {
			refType = "leave_request"
		}
		if _, err := e.d.DB.Exec(`INSERT INTO leave_ledger (user_id, balance, kind, minutes, ref_type, ref_id, created_at) VALUES (?, 'annual', ?, ?, ?, ?, ?)`, staff, q.kind, q.minutes, refType, q.ref, stamp); err != nil {
			t.Fatal(err)
		}
	}
	base := e.ledgerSum(t, staff) // 9600 - 2400
	if base != 7200 {
		t.Fatalf("시작 잔여 %d", base)
	}
	// 수요일부터 휴직(구분 질병, 열린 구간): 수·목·금 3일(1440분)이 복원된다.
	out, err := e.osvc.Register(e.ctx, e.admin, org.Command{Kind: org.KindAbsenceStart, UserID: staff, Effective: contract.Day(day(2)), AbsenceKind: "illness", AbsenceReason: "비밀사유-수술"})
	if err != nil {
		t.Fatalf("휴직 발령: %v", err)
	}
	if got := e.ledgerSum(t, staff); got != base+1440 {
		t.Fatalf("휴직 발령 뒤 잔여 %d(기대 %d)", got, base+1440)
	}
	var restoreLines int
	_ = e.d.DB.QueryRow(`SELECT COUNT(*) FROM leave_ledger WHERE ref_type = 'leave_absence' AND ref_id = ? AND kind = 'release'`, reqID).Scan(&restoreLines)
	if restoreLines != 1 {
		t.Fatalf("복원 줄 %d건", restoreLines)
	}
	var auditN int
	_ = e.d.DB.QueryRow(`SELECT COUNT(*) FROM audit_logs WHERE action = 'leave.absence_restore' AND result = 'success'`).Scan(&auditN)
	if auditN != 1 {
		t.Fatalf("복원 감사 %d건", auditN)
	}
	var leak int
	_ = e.d.DB.QueryRow(`SELECT COUNT(*) FROM audit_logs WHERE action = 'leave.absence_restore' AND (detail LIKE '%수술%' OR diff LIKE '%수술%' OR diff LIKE '%illness%')`).Scan(&leak)
	if leak != 0 {
		t.Fatal("복원 감사에 휴직 사유·구분이 있다")
	}
	// 목요일 복직: 구간이 [수, 목) 으로 줄어 하루만 남는다 → 960 되돌림.
	end, err := e.osvc.Register(e.ctx, e.admin, org.Command{Kind: org.KindAbsenceEnd, UserID: staff, Effective: contract.Day(day(3))})
	if err != nil {
		t.Fatalf("복직 발령: %v", err)
	}
	if got := e.ledgerSum(t, staff); got != base+480 {
		t.Fatalf("복직 뒤 잔여 %d(기대 %d)", got, base+480)
	}
	// 복직을 취소하면 다시 3일, 휴직까지 취소하면 0 으로 돌아간다. 원장 줄은 지우지 않는다.
	if err := e.osvc.Cancel(e.ctx, e.admin, end.EventIDs[0], "복직 일정 오류", true); err != nil {
		t.Fatalf("복직 취소: %v", err)
	}
	if got := e.ledgerSum(t, staff); got != base+1440 {
		t.Fatalf("복직 취소 뒤 잔여 %d(기대 %d)", got, base+1440)
	}
	if err := e.osvc.Cancel(e.ctx, e.admin, out.EventIDs[0], "발령 오류", true); err != nil {
		t.Fatalf("휴직 취소: %v", err)
	}
	if got := e.ledgerSum(t, staff); got != base {
		t.Fatalf("휴직 취소 뒤 잔여 %d(기대 %d)", got, base)
	}
	var lines int
	_ = e.d.DB.QueryRow(`SELECT COUNT(*) FROM leave_ledger WHERE ref_type = 'leave_absence'`).Scan(&lines)
	if lines != 4 { // 복원, 되돌림, 다시 복원, 되돌림
		t.Fatalf("복원·되돌림 줄 %d건(기대 4)", lines)
	}
	// 원장 불변식: 수정·삭제는 막힌다.
	if _, err := e.d.DB.Exec(`DELETE FROM leave_ledger WHERE ref_type = 'leave_absence'`); err == nil {
		t.Fatal("원장 줄이 지워짐")
	}
}

// 오늘 휴직 중: 체크인 거부, 팀 현황·달력·MCP 부재 조회에는 "부재"(구분·사유 없음), 예정 복직일 경과 알림은 받는 사람마다 한 번.
func TestAbsenceToday_ClockInTeamCalendarAndOverdueNotice(t *testing.T) {
	e := newAbsenceEnv(t)
	staff := e.staff.UserID
	today := time.Now().In(contract.KST)
	start := today.AddDate(0, 0, -3).Format("2006-01-02")
	ret := today.AddDate(0, 0, -1).Format("2006-01-02") // 예정 복직일은 어제
	if _, err := e.osvc.Register(e.ctx, e.admin, org.Command{Kind: org.KindAbsenceStart, UserID: staff, Effective: contract.Day(start), ReturnDay: contract.Day(ret),
		AbsenceKind: "illness", AbsenceReason: "비밀사유-수술", Reason: "휴직 발령", Confirm: true}); err != nil {
		t.Fatalf("휴직 발령: %v", err)
	}
	// 체크인 거부.
	if _, err := e.d.Attendance.CheckIn(e.as(e.staff)); err == nil || !strings.Contains(err.Error(), "휴직 중에는 출퇴근을 기록할 수 없습니다") {
		t.Fatalf("휴직 중 체크인: %v", err)
	}
	// 팀 현황: 부재 1명, 상태 "부재".
	ts, err := e.d.Attendance.TeamStatus(e.as(e.lead), 0)
	if err != nil {
		t.Fatalf("팀 현황: %v", err)
	}
	found := false
	for _, r := range ts.Rows {
		if r.UserID == staff {
			found = true
			if r.State != "부재" {
				t.Fatalf("휴직자의 오늘 상태 %q", r.State)
			}
		}
	}
	if !found || ts.Absent != 1 {
		t.Fatalf("팀 현황: 찾음 %v 부재 %d", found, ts.Absent)
	}
	// 달력: 오늘이 든 부재 일정, 라벨은 "부재" 한 가지.
	cal, err := e.d.Attendance.Calendar(e.as(e.lead), today.Format("2006-01"), 0)
	if err != nil {
		t.Fatalf("달력: %v", err)
	}
	okCal := false
	for _, p := range cal.People {
		if p.UserID != staff {
			continue
		}
		for _, en := range p.Entries {
			if en.Label == "부재" && en.Start <= today.Format("2006-01-02") && en.End >= today.Format("2006-01-02") {
				okCal = true
			}
			if strings.Contains(en.Label, "질병") || strings.Contains(en.Label, "휴직") {
				t.Fatalf("달력 라벨에 구분: %q", en.Label)
			}
		}
	}
	if !okCal {
		t.Fatalf("달력에 휴직 구간이 없다: %+v", cal.People)
	}
	// MCP 팀 부재 조회와 같은 서비스: 오늘부터 이틀.
	ab, err := e.d.Attendance.Absence(e.as(e.lead), today.Format("2006-01-02"), today.AddDate(0, 0, 1).Format("2006-01-02"), 0)
	if err != nil {
		t.Fatalf("팀 부재: %v", err)
	}
	okAb := false
	for _, p := range ab.People {
		for _, en := range p.Entries {
			if p.Name == "구매직원" && en.Label == "부재" {
				okAb = true
			}
		}
	}
	if !okAb {
		t.Fatalf("팀 부재에 휴직자가 없다: %+v", ab.People)
	}
	if strings.Contains(fmt.Sprintf("%+v%+v%+v", ts, cal, ab), "비밀사유") {
		t.Fatal("휴직 사유가 화면 자료에 있다")
	}

	// 예정 복직일 경과 알림: 인사 담당·직속 조직장에게, 사유·구분 없이, 같은 사람·같은 예정일 한 번.
	n, err := e.osvc.NotifyOverdueReturns(e.ctx, time.Now())
	if err != nil || n == 0 {
		t.Fatalf("경과 알림: %d %v", n, err)
	}
	var total int
	_ = e.d.DB.QueryRow(`SELECT COUNT(*) FROM notifications WHERE kind = 'hr_absence_return_overdue'`).Scan(&total)
	if total != n {
		t.Fatalf("알림 %d건(반환 %d)", total, n)
	}
	var leadGot int
	_ = e.d.DB.QueryRow(`SELECT COUNT(*) FROM notifications WHERE kind = 'hr_absence_return_overdue' AND user_id = ?`, e.lead.UserID).Scan(&leadGot)
	if leadGot != 1 {
		t.Fatalf("직속 조직장 알림 %d건", leadGot)
	}
	var selfGot, leak int
	_ = e.d.DB.QueryRow(`SELECT COUNT(*) FROM notifications WHERE kind = 'hr_absence_return_overdue' AND user_id = ?`, staff).Scan(&selfGot)
	_ = e.d.DB.QueryRow(`SELECT COUNT(*) FROM notifications WHERE kind = 'hr_absence_return_overdue' AND (title || body LIKE '%비밀%' OR title || body LIKE '%질병%' OR title || body LIKE '%illness%')`).Scan(&leak)
	if selfGot != 0 || leak != 0 {
		t.Fatalf("본인 알림 %d건, 사유·구분 노출 %d건", selfGot, leak)
	}
	if again, err := e.osvc.NotifyOverdueReturns(e.ctx, time.Now()); err != nil || again != 0 {
		t.Fatalf("같은 예정일 두 번째: %d %v", again, err)
	}
	// 복직하면 더 알리지 않는다.
	if _, err := e.osvc.Register(e.ctx, e.admin, org.Command{Kind: org.KindAbsenceEnd, UserID: staff, Effective: contract.DayOf(today)}); err != nil {
		t.Fatalf("복직: %v", err)
	}
	if n2, err := e.osvc.NotifyOverdueReturns(e.ctx, time.Now()); err != nil || n2 != 0 {
		t.Fatalf("복직 뒤 알림: %d %v", n2, err)
	}
}
