package service_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/contract/fake"
	"github.com/choibongseok/procure-ledger/internal/privacy"
	"github.com/choibongseok/procure-ledger/internal/service"
)

// B50 2단계: 휴가 종류·연차 규칙(법정 하한)·원장·발생·소멸·이월, 휴가 신청(LEAVE 결재 훅)·전이·취소·열람 범위·동시성.
// e.now = 2026-11-10(화) 18:00 KST. 신청 날짜는 같은 주(11-16 월 ~ 11-20 금)를 쓴다(주말 11-14·15, 21·22).

type leaveEnv struct {
	*env
	svc   *service.LeaveService
	att   *service.AttachmentService
	gate  *stubGate
	noter *fakeNoter
}

type stubGate struct {
	mu sync.Mutex
	on bool
}

func (g *stubGate) Enabled(context.Context, string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.on
}
func (g *stubGate) EnabledTx(ctx context.Context, _ contract.Tx, key string) bool {
	return g.Enabled(ctx, key)
}
func (g *stubGate) set(v bool) { g.mu.Lock(); g.on = v; g.mu.Unlock() }

func (e *env) leave(t *testing.T) *leaveEnv {
	t.Helper()
	g := &stubGate{on: true}
	n := &fakeNoter{}
	b := service.Base{DB: e.db, Txs: contract.SQLTxManager{DB: e.db}, Audit: e.rec, Masker: fake.Masker{}, Org: leftAwareOrg{e.org}, Now: func() time.Time { return e.now }, Modules: g, Privacy: n}
	docs := service.NewApprovalDocService(b)
	return &leaveEnv{env: e, svc: service.NewLeaveService(b, docs), att: service.NewAttachmentService(b, docs), gate: g, noter: n}
}

// hr 는 인사 담당 권한을 가진 사람(재무2)의 요청 context 다.
func hrCtx() context.Context {
	p := users[uFinance2]
	p.Channel = contract.ChannelWeb
	p.Perms = contract.NewPermSet(contract.PermAttendanceSelf, contract.PermAttendanceRead, contract.PermAttendanceHR, contract.PermAttendanceExport)
	return contract.WithPrincipal(context.Background(), p)
}

var stdPolicy = domain.LeavePolicyInput{EffectiveFrom: "2026-11-10", Basis: "hire", WorkdayMinutes: 480, HourUnitMinutes: 60, AttendanceRule: true, CarryMaxMinutes: 0, CarryValidMonths: 12, ExpireMonths: 12, FirstYearMode: "monthly"}

func (l *leaveEnv) policy(t *testing.T) {
	t.Helper()
	if _, err := l.svc.SavePolicy(as(uAdmin), stdPolicy); err != nil {
		t.Fatalf("규칙 저장: %v", err)
	}
}

func (l *leaveEnv) open(t *testing.T, user int64, days int) {
	t.Helper()
	if err := l.svc.Adjust(hrCtx(), domain.LeaveAdjustInput{UserID: user, Balance: "annual", Kind: "opening", Minutes: int64(days) * 480, Reason: "초기 이관"}); err != nil {
		t.Fatalf("초기 이관: %v", err)
	}
}

func (l *leaveEnv) minutes(t *testing.T, user int64) (avail, reserved int64) {
	t.Helper()
	b, err := l.svc.Balance(as(user), 0)
	if err != nil {
		t.Fatalf("잔여: %v", err)
	}
	for _, ln := range b.Lines {
		if ln.Balance == "annual" {
			return ln.Minutes, ln.Reserved
		}
	}
	t.Fatal("연차 줄 없음")
	return
}

func in(code, from, to string) domain.LeaveInput {
	return domain.LeaveInput{LeaveCode: code, StartDate: from, EndDate: to}
}

func wantValidation(t *testing.T, err error, contains string) {
	t.Helper()
	if !errors.Is(err, contract.ErrValidation) || !strings.Contains(err.Error(), contains) {
		t.Fatalf("검증 오류 %q 가 와야 함: %v", contains, err)
	}
}

func TestLeavePolicyLegalFloorAndHistory(t *testing.T) {
	e := newEnv(t)
	l := e.leave(t)
	// 법정 하한보다 불리한 소멸 기한은 거부한다(AC-5003).
	bad := stdPolicy
	bad.ExpireMonths = 6
	if _, err := l.svc.SavePolicy(as(uAdmin), bad); !errors.Is(err, contract.ErrValidation) {
		t.Fatalf("12개월 미만 소멸: %v", err)
	}
	for name, mut := range map[string]func(*domain.LeavePolicyInput){
		"기준":    func(p *domain.LeavePolicyInput) { p.Basis = "x" },
		"하루 분량": func(p *domain.LeavePolicyInput) { p.WorkdayMinutes = 100 },
		"시간 단위": func(p *domain.LeavePolicyInput) { p.HourUnitMinutes = 45 },
		"첫해":    func(p *domain.LeavePolicyInput) { p.FirstYearMode = "x" },
		"이월 기한": func(p *domain.LeavePolicyInput) { p.CarryValidMonths = 0 },
		"효력일":   func(p *domain.LeavePolicyInput) { p.EffectiveFrom = "내일" },
	} {
		p := stdPolicy
		mut(&p)
		if _, err := l.svc.SavePolicy(as(uAdmin), p); !errors.Is(err, contract.ErrValidation) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := l.svc.SavePolicy(as(uStaff), stdPolicy); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("직원의 규칙 저장: %v", err)
	}
	// 처음 저장하는 규칙은 과거 효력일도 된다. 이미 규칙이 있으면 오늘 이후여야 하고 같은 효력일은 거부한다.
	first := stdPolicy
	first.EffectiveFrom = "2026-01-01"
	if _, err := l.svc.SavePolicy(as(uAdmin), first); err != nil {
		t.Fatalf("처음 규칙(과거 효력일): %v", err)
	}
	past := stdPolicy
	past.EffectiveFrom = "2026-06-01"
	wantValidation(t, func() error { _, err := l.svc.SavePolicy(as(uAdmin), past); return err }(), "오늘 이후")
	if _, err := l.svc.SavePolicy(as(uAdmin), first); err == nil {
		t.Fatal("같은 효력일 규칙이 두 번 저장됨")
	}
	future := stdPolicy
	future.EffectiveFrom = "2027-01-01"
	future.Basis = "fiscal"
	p2, err := l.svc.SavePolicy(as(uAdmin), future)
	if err != nil || p2.Basis != "fiscal" {
		t.Fatalf("미래 규칙: %+v %v", p2, err)
	}
	ps, err := l.svc.Policies(as(uAdmin))
	if err != nil || len(ps) != 2 || ps[0].EffectiveFrom != "2027-01-01" {
		t.Fatalf("이력: %+v %v", ps, err)
	}
	// 효력이 시작된 규칙은 취소할 수 없고, 미래 규칙은 취소된다(이력에는 남는다).
	if err := l.svc.CancelPolicy(as(uAdmin), ps[1].ID); !errors.Is(err, contract.ErrInvalidState) {
		t.Fatalf("시작된 규칙 취소: %v", err)
	}
	if err := l.svc.CancelPolicy(as(uAdmin), p2.ID); err != nil {
		t.Fatalf("미래 규칙 취소: %v", err)
	}
	if err := l.svc.CancelPolicy(as(uAdmin), p2.ID); !errors.Is(err, contract.ErrInvalidState) {
		t.Fatalf("두 번 취소: %v", err)
	}
	// 규칙 표는 추가 전용이다: 직접 수정·삭제는 DB 가 막는다.
	if _, err := e.db.Exec(`UPDATE leave_policies SET workday_minutes = 600 WHERE id = ?`, ps[1].ID); err == nil {
		t.Fatal("규칙 수정이 막히지 않음")
	}
	if _, err := e.db.Exec(`DELETE FROM leave_policies WHERE id = ?`, ps[1].ID); err == nil {
		t.Fatal("규칙 삭제가 막히지 않음")
	}
	if !e.rec.Has("leave.policy_set", "leave_policy", "success") || !e.rec.Has("leave.policy_cancel", "leave_policy", "success") {
		t.Fatal("규칙 감사 기록 없음")
	}
}

func TestLeaveTypesRules(t *testing.T) {
	e := newEnv(t)
	l := e.leave(t)
	ts, err := l.svc.AdminTypes(as(uAdmin))
	if err != nil || len(ts) != 6 {
		t.Fatalf("시스템 종류: %d %v", len(ts), err)
	}
	var annual domain.LeaveType
	for _, t := range ts {
		if t.Code == "annual" {
			annual = t
		}
	}
	if _, err := l.svc.AdminTypes(as(uStaff)); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("직원의 설정 목록: %v", err)
	}
	// 회사 종류 추가: 연차를 차감하는 새 종류는 만들 수 없다(법정 연차를 몰래 줄이는 길 차단).
	wantValidation(t, func() error {
		_, err := l.svc.SaveType(as(uAdmin), 0, domain.LeaveTypeInput{Code: "extra", Name: "추가", Unit: "day", Balance: "annual"})
		return err
	}(), "연차를 차감하는 종류")
	wed, err := l.svc.SaveType(as(uAdmin), 0, domain.LeaveTypeInput{Code: "wedding", Name: "결혼휴가", Unit: "day", Paid: true, MaxDays: 5})
	if err != nil || wed.System || !wed.Active || wed.MaxDays != 5 {
		t.Fatalf("종류 추가: %+v %v", wed, err)
	}
	wantValidation(t, func() error {
		_, err := l.svc.SaveType(as(uAdmin), 0, domain.LeaveTypeInput{Code: "wedding", Name: "중복", Unit: "day"})
		return err
	}(), "이미 있습니다")
	wantValidation(t, func() error {
		_, err := l.svc.SaveType(as(uAdmin), 0, domain.LeaveTypeInput{Code: "Bad Code", Name: "x", Unit: "day"})
		return err
	}(), "종류 코드")
	// 시스템 종류는 이름·증빙·유급만 바뀐다(단위·잔여 묶음 변경 시도는 무시되고, 연차는 사용 중지할 수 없다).
	if _, err := l.svc.SaveType(as(uAdmin), annual.ID, domain.LeaveTypeInput{Name: "연차", Unit: "hour", Balance: "", Active: false}); err == nil {
		t.Fatal("연차 사용 중지가 허용됨")
	}
	got, err := l.svc.SaveType(as(uAdmin), annual.ID, domain.LeaveTypeInput{Name: "연차휴가", Unit: "hour", Balance: "", Paid: true, Active: true})
	if err != nil || got.Name != "연차휴가" || got.Unit != "day" || got.Balance != "annual" {
		t.Fatalf("시스템 종류 수정: %+v %v", got, err)
	}
	// 사용 중지한 종류는 신청 화면 목록에서 빠지고 설정 목록에는 남는다. 지우는 길은 없다.
	if _, err := l.svc.SaveType(as(uAdmin), wed.ID, domain.LeaveTypeInput{Name: "결혼휴가", Unit: "day", Paid: true, MaxDays: 5, Active: false}); err != nil {
		t.Fatal(err)
	}
	active, err := l.svc.Types(as(uStaff))
	if err != nil {
		t.Fatal(err)
	}
	for _, t2 := range active {
		if t2.Code == "wedding" {
			t.Fatal("사용 중지한 종류가 신청 목록에 있음")
		}
	}
	if _, err := e.db.Exec(`DELETE FROM leave_types WHERE code = 'wedding'`); err == nil {
		t.Fatal("종류 삭제가 막히지 않음")
	}
	// 모듈이 꺼져 있으면 신청 화면용 목록은 없는 것과 같지만 설정 목록은 읽을 수 있다.
	l.gate.set(false)
	if _, err := l.svc.Types(as(uStaff)); !errors.Is(err, contract.ErrNotFound) {
		t.Fatalf("꺼진 모듈의 종류 목록: %v", err)
	}
	if _, err := l.svc.AdminTypes(as(uAdmin)); err != nil {
		t.Fatalf("꺼진 모듈의 설정 목록: %v", err)
	}
}

func (e *env) hire(t *testing.T, user int64, date string) {
	t.Helper()
	if _, err := e.db.Exec(`INSERT INTO org_events (subject_type, subject_id, kind, effective_date, created_by) VALUES ('person', ?, 'hire', ?, 1)`, user, date); err != nil {
		t.Fatalf("입사 발령 시드: %v", err)
	}
}

func TestLeaveAccrualCatchUpIdempotentExpireAndCarry(t *testing.T) {
	e := newEnv(t)
	l := e.leave(t)
	first := stdPolicy
	first.EffectiveFrom = "2025-01-01"
	if _, err := l.svc.SavePolicy(as(uAdmin), first); err != nil {
		t.Fatal(err)
	}
	e.hire(t, uStaff, "2024-03-15")
	e.now = time.Date(2026, 3, 20, 3, 0, 0, 0, time.UTC) // KST 3월 20일
	n, err := l.svc.AccrueUser(context.Background(), uStaff, contract.DefaultEntityID, e.now)
	if err != nil || n == 0 {
		t.Fatalf("발생: %d %v", n, err)
	}
	// 규칙 효력일(2025-01-01) 이전 월 발생은 만들지 않는다. 이후: 월 2건 + 근속 1년·2년 연차 15일씩. 12개월이 지난 앞의 세 건은 소멸하고(이월 0) 근속 2년 15일만 남는다.
	avail, reserved := l.minutes(t, uStaff)
	if avail != 15*480 || reserved != 0 {
		t.Fatalf("잔여 %d분(기대 %d)", avail, 15*480)
	}
	if n2, err := l.svc.AccrueUser(context.Background(), uStaff, contract.DefaultEntityID, e.now); err != nil || n2 != 0 {
		t.Fatalf("멱등: 두 번째 실행이 %d줄을 더 씀(%v)", n2, err)
	}
	lines, err := l.svc.Ledger(as(uStaff), 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]int{}
	for _, ln := range lines {
		kinds[ln.Kind]++
	}
	if kinds["grant"] != 4 || kinds["expire"] != 3 || kinds["carry"] != 0 {
		t.Fatalf("원장 종류: %v", kinds)
	}
	// 원장은 추가 전용이다.
	if _, err := e.db.Exec(`UPDATE leave_ledger SET minutes = 1 WHERE id = ?`, lines[0].ID); err == nil {
		t.Fatal("원장 수정이 막히지 않음")
	}
	if _, err := e.db.Exec(`DELETE FROM leave_ledger WHERE id = ?`, lines[0].ID); err == nil {
		t.Fatal("원장 삭제가 막히지 않음")
	}

	// 이월: 같은 상황에서 이월 한도 5일이면 만료된 근속 1년분 15일 중 5일이 이월되고(소멸 줄과 이월 줄), 다른 소멸분은 이월되지 않는다(월 발생 2일은 2일 전부 이월 가능).
	e2 := newEnv(t)
	l2 := e2.leave(t)
	carry := stdPolicy
	carry.EffectiveFrom = "2025-01-01"
	carry.CarryMaxMinutes = 5 * 480
	if _, err := l2.svc.SavePolicy(as(uAdmin), carry); err != nil {
		t.Fatal(err)
	}
	e2.hire(t, uStaff, "2024-03-15")
	e2.now = time.Date(2026, 3, 20, 3, 0, 0, 0, time.UTC)
	if _, err := l2.svc.AccrueUser(context.Background(), uStaff, contract.DefaultEntityID, e2.now); err != nil {
		t.Fatal(err)
	}
	avail2, _ := l2.minutes(t, uStaff)
	// 근속 2년 15일 + 이월(월 발생 2건: 각 1일 → 2일, 근속 1년: 5일 한도) = 15 + 1 + 1 + 5 = 22일.
	if avail2 != 22*480 {
		t.Fatalf("이월 뒤 잔여 %d분(기대 %d)", avail2, 22*480)
	}
	bal, _ := l2.svc.Balance(as(uStaff), 0)
	var ann domain.LeaveBalanceLine
	for _, ln := range bal.Lines {
		if ln.Balance == "annual" {
			ann = ln
		}
	}
	if ann.ExpiringMinutes != 0 { // 가장 이른 소멸일(이월분 2027-03-15)이 30일보다 멀다
		t.Fatalf("30일 내 소멸 예정: %+v", ann)
	}
}

func TestLeaveAccrualSkipsWithoutPolicyHireOrModule(t *testing.T) {
	e := newEnv(t)
	l := e.leave(t)
	e.hire(t, uStaff, "2024-03-15")
	if n, err := l.svc.AccrueUser(context.Background(), uStaff, contract.DefaultEntityID, e.now); err != nil || n != 0 {
		t.Fatalf("규칙이 없는데 발생: %d %v", n, err)
	}
	first := stdPolicy
	first.EffectiveFrom = "2025-01-01"
	if _, err := l.svc.SavePolicy(as(uAdmin), first); err != nil {
		t.Fatal(err)
	}
	if n, err := l.svc.AccrueUser(context.Background(), uStaff2, contract.DefaultEntityID, e.now); err != nil || n != 0 {
		t.Fatalf("입사 발령이 없는데 발생: %d %v", n, err)
	}
	// 입사 발령이 취소되면(취소 이벤트가 가리킴) 입사일로 읽지 않는다.
	if _, err := e.db.Exec(`INSERT INTO org_events (subject_type, subject_id, kind, effective_date, cancels_id, created_by) SELECT 'person', subject_id, 'cancel', '2024-03-15', id, 1 FROM org_events WHERE kind = 'hire' AND subject_id = ?`, uStaff); err != nil {
		t.Fatal(err)
	}
	if n, err := l.svc.AccrueUser(context.Background(), uStaff, contract.DefaultEntityID, e.now); err != nil || n != 0 {
		t.Fatalf("취소된 입사로 발생: %d %v", n, err)
	}
	// 일일 작업은 모듈이 꺼져 있으면 아무것도 하지 않는다.
	l.gate.set(false)
	if n, err := l.svc.RunOnce(context.Background()); err != nil || n != 0 {
		t.Fatalf("꺼진 모듈의 일일 작업: %d %v", n, err)
	}
}

func TestLeaveAdjustRules(t *testing.T) {
	e := newEnv(t)
	l := e.leave(t)
	l.policy(t)
	base := domain.LeaveAdjustInput{UserID: uStaff, Balance: "annual", Kind: "adjust", Minutes: 480, Reason: "정정"}
	if err := l.svc.Adjust(as(uStaff), base); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("직원의 조정: %v", err)
	}
	if err := l.svc.Adjust(as(uAdmin), base); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("인사 권한 없는 관리자의 조정: %v", err)
	}
	noReason := base
	noReason.Reason = " "
	wantValidation(t, l.svc.Adjust(hrCtx(), noReason), "사유")
	self := base
	self.UserID = uFinance2
	wantValidation(t, l.svc.Adjust(hrCtx(), self), "본인의 잔여")
	for name, mut := range map[string]func(*domain.LeaveAdjustInput){
		"묶음":  func(a *domain.LeaveAdjustInput) { a.Balance = "x" },
		"종류":  func(a *domain.LeaveAdjustInput) { a.Kind = "x" },
		"0분":  func(a *domain.LeaveAdjustInput) { a.Minutes = 0 },
		"소멸일": func(a *domain.LeaveAdjustInput) { a.ExpiresOn = "내일" },
	} {
		a := base
		mut(&a)
		if err := l.svc.Adjust(hrCtx(), a); !errors.Is(err, contract.ErrValidation) {
			t.Errorf("%s: %v", name, err)
		}
	}
	neg := base
	neg.Minutes, neg.ExpiresOn = -480, "2027-01-01"
	wantValidation(t, l.svc.Adjust(hrCtx(), neg), "소멸일")
	if err := l.svc.Adjust(hrCtx(), base); err != nil {
		t.Fatalf("정상 조정: %v", err)
	}
	if avail, _ := l.minutes(t, uStaff); avail != 480 {
		t.Fatalf("조정 뒤 잔여 %d", avail)
	}
	// 줄이는 조정은 음수 잔여도 그대로 보인다(막지 않는다). 조정 줄에는 사유와 처리한 사람이 남는다.
	down := base
	down.Minutes = -960
	if err := l.svc.Adjust(hrCtx(), down); err != nil {
		t.Fatal(err)
	}
	if avail, _ := l.minutes(t, uStaff); avail != -480 {
		t.Fatalf("음수 잔여 %d", avail)
	}
	lines, _ := l.svc.Ledger(hrCtx(), uStaff, 10)
	if len(lines) != 2 || lines[0].Reason != "정정" || lines[0].CreatedByName == "" {
		t.Fatalf("원장: %+v", lines)
	}
	if !e.rec.Has("leave.adjust", "leave_request", "success") {
		t.Fatal("조정 감사 없음")
	}
	// 같은 법인이 아닌 사람은 없는 것과 같다.
	if err := l.svc.Adjust(hrCtx(), domain.LeaveAdjustInput{UserID: 99999, Balance: "annual", Kind: "adjust", Minutes: 1, Reason: "x"}); !errors.Is(err, contract.ErrNotFound) {
		t.Fatalf("없는 사람: %v", err)
	}
}

func TestLeaveRequestApproveCancelRejectRecallKeepLedgerInvariant(t *testing.T) {
	e := newEnv(t)
	l := e.leave(t)
	l.policy(t)
	l.open(t, uStaff, 10)
	pv, err := l.svc.Preview(as(uStaff), in("annual", "2026-11-16", "2026-11-17"))
	if err != nil || pv.Minutes != 2*480 || pv.BalanceBefore != 10*480 || pv.BalanceAfter != 8*480 || len(pv.Days) != 2 {
		t.Fatalf("미리보기: %+v %v", pv, err)
	}
	if avail, _ := l.minutes(t, uStaff); avail != 10*480 {
		t.Fatalf("미리보기가 잔여를 바꿈: %d", avail)
	}
	sum := func(id int64) int64 {
		var n int64
		if err := e.db.QueryRow(`SELECT COALESCE(SUM(minutes),0) FROM leave_ledger WHERE ref_type = 'leave_request' AND ref_id = ?`, id).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	// 승인: 신청 즉시 예약 차감 → 최종 승인에서 사용. 잔여는 계속 8일이다.
	r1, err := l.svc.Request(as(uStaff), in("annual", "2026-11-16", "2026-11-17"))
	if err != nil || r1.Status != domain.LeavePending || r1.DocNo == "" || !strings.Contains(r1.LeaveName, "연차") {
		t.Fatalf("신청: %+v %v", r1, err)
	}
	if avail, reserved := l.minutes(t, uStaff); avail != 8*480 || reserved != 2*480 {
		t.Fatalf("신청 직후 잔여 %d 예약 %d", avail, reserved)
	}
	if sum(r1.ID) != -2*480 {
		t.Fatalf("대기 중 원장 합 %d", sum(r1.ID))
	}
	decideAs(t, e, uLead, r1.DocID, true, "")
	got, _ := l.svc.Get(as(uStaff), r1.ID)
	if got.Status != domain.LeaveApproved {
		t.Fatalf("승인 뒤 상태: %+v", got)
	}
	if avail, reserved := l.minutes(t, uStaff); avail != 8*480 || reserved != 0 {
		t.Fatalf("승인 뒤 잔여 %d 예약 %d", avail, reserved)
	}
	if sum(r1.ID) != -2*480 {
		t.Fatalf("승인 뒤 원장 합 %d", sum(r1.ID))
	}
	// 시작 전 본인 즉시 취소 → 복원(원장 합 0).
	c1, err := l.svc.Cancel(as(uStaff), r1.ID, "일정 변경")
	if err != nil || c1.Status != domain.LeaveCanceled {
		t.Fatalf("취소: %+v %v", c1, err)
	}
	if avail, _ := l.minutes(t, uStaff); avail != 10*480 || sum(r1.ID) != 0 {
		t.Fatalf("취소 뒤 잔여 %d 합 %d", avail, sum(r1.ID))
	}
	if _, err := l.svc.Cancel(as(uStaff), r1.ID, ""); !errors.Is(err, contract.ErrInvalidState) {
		t.Fatalf("두 번 취소: %v", err)
	}
	// 반려: 예약이 풀리고 원장 합 0.
	r2, err := l.svc.Request(as(uStaff), in("annual", "2026-11-18", "2026-11-18"))
	if err != nil {
		t.Fatal(err)
	}
	decideAs(t, e, uLead, r2.DocID, false, "시기 변경 요청: 12월로 옮겨 주세요")
	got, _ = l.svc.Get(as(uStaff), r2.ID)
	if got.Status != domain.LeaveRejected {
		t.Fatalf("반려 뒤 상태: %+v", got)
	}
	if avail, reserved := l.minutes(t, uStaff); avail != 10*480 || reserved != 0 || sum(r2.ID) != 0 {
		t.Fatalf("반려 뒤 잔여 %d 예약 %d 합 %d", avail, reserved, sum(r2.ID))
	}
	// 회수(상신 취소): 신청 대기에서 신청자가 취소하면 회수 상태, 예약 복원.
	r3, err := l.svc.Request(as(uStaff), in("annual", "2026-11-19", "2026-11-19"))
	if err != nil {
		t.Fatal(err)
	}
	c3, err := l.svc.Cancel(as(uStaff), r3.ID, "")
	if err != nil || c3.Status != domain.LeaveRecalled {
		t.Fatalf("회수: %+v %v", c3, err)
	}
	if avail, reserved := l.minutes(t, uStaff); avail != 10*480 || reserved != 0 || sum(r3.ID) != 0 {
		t.Fatalf("회수 뒤 잔여 %d 예약 %d 합 %d", avail, reserved, sum(r3.ID))
	}
	// 끝난 신청은 다시 살아나지 않는다(DB 트리거까지).
	for _, c := range []struct {
		id int64
		to string
	}{{r1.ID, "pending"}, {r1.ID, "approved"}, {r2.ID, "approved"}, {r3.ID, "pending"}} {
		if _, err := e.db.Exec(`UPDATE leave_requests SET status = ? WHERE id = ?`, c.to, c.id); err == nil {
			t.Errorf("신청 %d 를 %s 로 되돌릴 수 있음", c.id, c.to)
		}
	}
	if _, err := e.db.Exec(`UPDATE leave_requests SET minutes = 1 WHERE id = ?`, r1.ID); err == nil {
		t.Error("신청 내용 수정이 막히지 않음")
	}
	if _, err := e.db.Exec(`DELETE FROM leave_requests WHERE id = ?`, r1.ID); err == nil {
		t.Error("신청 삭제가 막히지 않음")
	}
	for _, a := range []string{"leave.request", "leave.approve", "leave.reject", "leave.recall", "leave.cancel"} {
		if !e.rec.Has(a, "leave_request", "success") {
			t.Errorf("감사 %s 없음", a)
		}
	}
}

func TestLeaveRequestValidationRules(t *testing.T) {
	e := newEnv(t)
	l := e.leave(t)
	// 규칙이 없으면 신청할 수 없다.
	wantValidation(t, func() error {
		_, err := l.svc.Request(as(uStaff), in("annual", "2026-11-16", "2026-11-16"))
		return err
	}(), "규칙이 아직")
	l.policy(t)
	l.open(t, uStaff, 3)
	for name, tc := range map[string]struct {
		in   domain.LeaveInput
		want string
	}{
		"종류 없음":     {in("nope", "2026-11-16", "2026-11-16"), "휴가 종류"},
		"날짜 형식":     {in("annual", "11/16", "11/16"), "YYYY-MM-DD"},
		"종료가 빠름":    {in("annual", "2026-11-17", "2026-11-16"), "종료일"},
		"주말만":       {in("annual", "2026-11-14", "2026-11-15"), "모두 휴일"},
		"잔여 초과":     {in("annual", "2026-11-16", "2026-11-20"), "잔여 연차"},
		"반차는 하루만":   {domain.LeaveInput{LeaveCode: "annual", StartDate: "2026-11-16", EndDate: "2026-11-17", Part: "am"}, "하루만"},
		"시간 단위 위반":  {domain.LeaveInput{LeaveCode: "annual", StartDate: "2026-11-16", EndDate: "2026-11-16", Part: "hours", StartTime: "09:00", EndTime: "10:30"}, "60분 단위"},
		"하루 이상 시간":  {domain.LeaveInput{LeaveCode: "annual", StartDate: "2026-11-16", EndDate: "2026-11-16", Part: "hours", StartTime: "09:00", EndTime: "18:00"}, "종일로 신청"},
		"시간 형식":     {domain.LeaveInput{LeaveCode: "annual", StartDate: "2026-11-16", EndDate: "2026-11-16", Part: "hours", StartTime: "9시", EndTime: "11시"}, "HH:MM"},
		"구간 값":      {domain.LeaveInput{LeaveCode: "annual", StartDate: "2026-11-16", EndDate: "2026-11-16", Part: "x"}, "구간"},
		"병가는 종일만":   {domain.LeaveInput{LeaveCode: "sick", StartDate: "2026-11-16", EndDate: "2026-11-16", Part: "am", ProofAttachmentID: 1}, "종일로만"},
		"병가 증빙":     {in("sick", "2026-11-16", "2026-11-16"), "증빙"},
		"시간 단위 종류":  {in("hourly", "2026-11-16", "2026-11-16"), "시간을 지정"},
		"소급은 사유 필수": {in("annual", "2026-11-09", "2026-11-09"), "사유"},
		"소급 30일 한도": {domain.LeaveInput{LeaveCode: "annual", StartDate: "2026-09-01", EndDate: "2026-09-01", Reason: "누락"}, "30일 전"},
		"90일 초과":    {in("annual", "2026-11-16", "2027-03-30"), "90일"},
	} {
		if _, err := l.svc.Preview(as(uStaff), tc.in); !errors.Is(err, contract.ErrValidation) || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v (기대 %q)", name, err, tc.want)
		}
	}
	// 같은 날 오전·오후 반차 둘은 되고(합 하루), 하루 종일이 겹치면 거부한다.
	am, err := l.svc.Request(as(uStaff), domain.LeaveInput{LeaveCode: "annual", StartDate: "2026-11-16", EndDate: "2026-11-16", Part: "am"})
	if err != nil || am.Minutes != 240 {
		t.Fatalf("오전 반차: %+v %v", am, err)
	}
	if _, err := l.svc.Request(as(uStaff), domain.LeaveInput{LeaveCode: "annual", StartDate: "2026-11-16", EndDate: "2026-11-16", Part: "pm"}); err != nil {
		t.Fatalf("오후 반차: %v", err)
	}
	wantValidation(t, func() error {
		_, err := l.svc.Request(as(uStaff), in("annual", "2026-11-16", "2026-11-16"))
		return err
	}(), "하루 분량을 넘습니다")
	wantValidation(t, func() error {
		_, err := l.svc.Request(as(uStaff), domain.LeaveInput{LeaveCode: "annual", StartDate: "2026-11-16", EndDate: "2026-11-16", Part: "hours", StartTime: "09:00", EndTime: "10:00"})
		return err
	}(), "하루 분량을 넘습니다")
	// 경조휴가의 일수 상한, 사용 중지 종류.
	if _, err := l.svc.SaveType(as(uAdmin), 0, domain.LeaveTypeInput{Code: "wedding", Name: "결혼휴가", Unit: "day", Paid: true, MaxDays: 2}); err != nil {
		t.Fatal(err)
	}
	wantValidation(t, func() error {
		_, err := l.svc.Preview(as(uStaff), in("wedding", "2026-11-18", "2026-11-20"))
		return err
	}(), "최대 2일")
	if _, err := l.svc.Preview(as(uStaff), in("wedding", "2026-11-18", "2026-11-19")); err != nil {
		t.Fatalf("상한 안의 경조휴가: %v", err)
	}
	// 연차를 쓰지 않는 종류는 잔여 검사를 하지 않는다(잔여가 없어도 신청된다).
	ts, _ := l.svc.AdminTypes(as(uAdmin))
	for _, ty := range ts {
		if ty.Code == "wedding" {
			if _, err := l.svc.SaveType(as(uAdmin), ty.ID, domain.LeaveTypeInput{Name: "결혼휴가", Unit: "day", Paid: true, MaxDays: 2, Active: false}); err != nil {
				t.Fatal(err)
			}
		}
	}
	wantValidation(t, func() error {
		_, err := l.svc.Preview(as(uStaff), in("wedding", "2026-11-18", "2026-11-18"))
		return err
	}(), "휴가 종류")
	// 모듈이 꺼져 있으면 신청·미리보기는 없는 것과 같다.
	l.gate.set(false)
	if _, err := l.svc.Request(as(uStaff), in("annual", "2026-11-19", "2026-11-19")); !errors.Is(err, contract.ErrNotFound) {
		t.Fatalf("꺼진 모듈의 신청: %v", err)
	}
	if _, err := l.svc.Balance(as(uStaff), 0); !errors.Is(err, contract.ErrNotFound) {
		t.Fatalf("꺼진 모듈의 잔여: %v", err)
	}
}

func TestLeaveCancelAfterStartOnlyHR(t *testing.T) {
	e := newEnv(t)
	l := e.leave(t)
	l.policy(t)
	l.open(t, uStaff, 5)
	r, err := l.svc.Request(as(uStaff), in("annual", "2026-11-10", "2026-11-10")) // 오늘(화)
	if err != nil {
		t.Fatal(err)
	}
	decideAs(t, e, uLead, r.DocID, true, "")
	if _, err := l.svc.Cancel(as(uStaff), r.ID, "x"); !errors.Is(err, contract.ErrInvalidState) || !strings.Contains(err.Error(), "시작되어") {
		t.Fatalf("시작된 휴가의 본인 취소: %v", err)
	}
	if _, err := l.svc.Cancel(as(uStaff2), r.ID, "x"); !errors.Is(err, contract.ErrNotFound) {
		t.Fatalf("남의 휴가 취소: %v", err)
	}
	if _, err := l.svc.Cancel(hrCtx(), r.ID, ""); !errors.Is(err, contract.ErrValidation) {
		t.Fatalf("사유 없는 인사 취소: %v", err)
	}
	c, err := l.svc.Cancel(hrCtx(), r.ID, "인사 정정")
	if err != nil || c.Status != domain.LeaveCanceled {
		t.Fatalf("인사 정정 취소: %+v %v", c, err)
	}
	if avail, _ := l.minutes(t, uStaff); avail != 5*480 {
		t.Fatalf("복원 뒤 잔여 %d", avail)
	}
}

func TestLeaveReasonVisibilityAndScope(t *testing.T) {
	e := newEnv(t)
	l := e.leave(t)
	l.policy(t)
	l.open(t, uStaff, 5)
	r, err := l.svc.Request(as(uStaff), domain.LeaveInput{LeaveCode: "annual", StartDate: "2026-11-16", EndDate: "2026-11-16", Reason: "가족 행사"})
	if err != nil || r.Reason != "가족 행사" {
		t.Fatalf("본인 신청: %+v %v", r, err)
	}
	// 본인·인사 담당은 사유 원문을 본다. 직속 조직장(팀장)도 본다. 다른 팀 직원·감사자는 존재도 모른다.
	if len(l.noter.got) != 0 {
		t.Fatalf("본인 열람이 기록됨: %+v", l.noter.got)
	}
	if g, err := l.svc.Get(hrCtx(), r.ID); err != nil || g.Reason != "가족 행사" {
		t.Fatalf("인사 담당: %+v %v", g, err)
	}
	if g, err := l.svc.Get(as(uLead), r.ID); err != nil || g.Reason != "가족 행사" {
		t.Fatalf("직속 조직장: %+v %v", g, err)
	}
	// 본인이 아닌 사람에게 사유 원문이 실릴 때마다 B41 열람 기록(범주 attendance, 정보주체 = 신청자, 값 없음)을 응답 전에 남긴다.
	if len(l.noter.got) != 2 {
		t.Fatalf("열람 기록 %d건", len(l.noter.got))
	}
	for i, a := range l.noter.got {
		if a.Category != privacy.CatAttendance || a.Screen != privacy.ScreenAttendanceReason || strings.Join(a.Fields, ",") != "leave_reason" || len(a.Subjects) != 1 || a.Subjects[0].ID != uStaff {
			t.Fatalf("열람 기록 %d: %+v", i, a)
		}
	}
	// 기록하지 못하면 사유 없이 실패한다(fail-closed). 본인은 기록 없이 계속 본다.
	l.noter.fail = errors.New("기록 저장소 장애")
	if g, err := l.svc.Get(hrCtx(), r.ID); !errors.Is(err, service.ErrLeavePrivacyNote) || g.Reason != "" {
		t.Fatalf("기록 실패인데 사유를 줌: %+v %v", g, err)
	}
	if g, err := l.svc.Get(as(uStaff), r.ID); err != nil || g.Reason != "가족 행사" {
		t.Fatalf("본인 열람: %+v %v", g, err)
	}
	l.noter.fail = nil
	for name, who := range map[string]int64{"다른 팀 직원": uStaff2, "다른 부서 팀장": uLead2, "감사자": uAuditor, "재무": uFinance} {
		if _, err := l.svc.Get(as(who), r.ID); !errors.Is(err, contract.ErrNotFound) {
			t.Errorf("%s 가 신청을 봄: %v", name, err)
		}
		if _, err := l.svc.Balance(as(who), uStaff); !errors.Is(err, contract.ErrNotFound) {
			t.Errorf("%s 가 남의 잔여를 봄: %v", name, err)
		}
		if _, err := l.svc.Ledger(as(who), uStaff, 10); !errors.Is(err, contract.ErrNotFound) {
			t.Errorf("%s 가 남의 원장을 봄: %v", name, err)
		}
	}
	if _, err := l.svc.Balance(as(uLead), uStaff); err != nil {
		t.Fatalf("직속 조직장의 잔여 조회: %v", err)
	}
	if _, err := l.svc.Balance(hrCtx(), uStaff); err != nil {
		t.Fatalf("인사 담당의 잔여 조회: %v", err)
	}
	// 휴가 신청서 문서에는 사유가 없다(결재선의 다른 결재자에게 번지지 않는다).
	var fields string
	if err := e.db.QueryRow(`SELECT fields FROM purchase_requests WHERE id = ?`, r.DocID).Scan(&fields); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(fields, "가족 행사") {
		t.Fatalf("문서 본문에 사유가 들어감: %s", fields)
	}
	// 감사에도 사유 원문은 길이만 남는다.
	for _, rec := range e.rec.Records() {
		for _, d := range rec.Diff {
			if strings.Contains(d.New, "가족 행사") || strings.Contains(d.Old, "가족 행사") {
				t.Fatalf("감사에 사유 원문: %+v", rec)
			}
		}
	}
	// LEAVE 양식은 내용 검색에 포함할 수 없다.
	k := e.knowledge(t)
	if err := k.admin.SetFormSearchable(as(uAdmin), "LEAVE", true); !errors.Is(err, contract.ErrValidation) {
		t.Fatalf("LEAVE 검색 포함: %v", err)
	}
}

func TestLeaveSystemFormCannotBeCreatedAsGeneralDraft(t *testing.T) {
	e := newEnv(t)
	_, docs := e.attach()
	if _, err := docs.CreateDraft(as(uStaff), domain.DocDraftInput{FormCode: "LEAVE", Title: "직접 작성", Fields: map[string]any{"leave_name": "연차", "period": "2026-11-16", "amount": "1일"}}); err == nil {
		t.Fatal("휴가 신청서를 일반 기안으로 만들 수 있음")
	}
}

func TestLeaveConcurrentRequestsCannotExceedBalance(t *testing.T) {
	e := newEnv(t)
	l := e.leave(t)
	l.policy(t)
	l.open(t, uStaff, 1) // 하루뿐
	var wg sync.WaitGroup
	results := make([]error, 2)
	for i, d := range []string{"2026-11-16", "2026-11-17"} {
		wg.Add(1)
		go func(i int, d string) {
			defer wg.Done()
			_, results[i] = l.svc.Request(as(uStaff), in("annual", d, d))
		}(i, d)
	}
	wg.Wait()
	ok := 0
	for _, err := range results {
		if err == nil {
			ok++
		}
	}
	if ok != 1 {
		t.Fatalf("잔여 1일에 동시 신청 두 건 중 성공 %d건(%v)", ok, results)
	}
	if avail, reserved := l.minutes(t, uStaff); avail != 0 || reserved != 480 {
		t.Fatalf("잔여 %d 예약 %d", avail, reserved)
	}
}
