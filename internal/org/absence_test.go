package org

import (
	"errors"
	"strings"
	"testing"

	caudit "github.com/choibongseok/procure-ledger/internal/contract/audit"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/privacy"
)

// B50 4단계: 휴직·복직 발령(명세 8절)과 수용 기준 AC-5030~5035·5041.

func (e *env) absentStart(user int64, day string, kind string, extra ...func(*Command)) Command {
	c := Command{Kind: KindAbsenceStart, UserID: user, Effective: contract.Day(day), AbsenceKind: kind, Reason: "휴직 발령"}
	for _, f := range extra {
		f(&c)
	}
	return c
}

func (e *env) onLeaveAt(user int64, day string) bool {
	e.t.Helper()
	p, err := e.svc.Person(e.ctx, user, contract.Day(day))
	if err != nil {
		e.t.Fatal(err)
	}
	return p.OnLeave
}

// 휴직 시작·복직 효력일에 구간이 바뀌고 재직·소속·직위는 그대로이며, 로그인 계정(users.active)은 막히지 않는다(AC-5030·5035).
func TestAbsenceStartEndSpansAndStaysEmployed(t *testing.T) {
	e := newEnv(t)
	_, _, _, t1, _ := e.baseOrg()
	u := e.hire("휴직자", "leave1@pl.test", t1, contract.RoleStaff, "2024-01-02")
	e.reg(e.absentStart(u, "2026-03-10", AbsenceChildcare))
	if !e.onLeaveAt(u, "2026-03-10") || e.onLeaveAt(u, "2026-03-09") {
		t.Fatal("휴직은 효력일부터다")
	}
	p, _ := e.svc.Person(e.ctx, u, "2026-03-10")
	if !p.Active || p.Primary.Unit.ID != t1 || p.AbsenceKind != AbsenceChildcare {
		t.Fatalf("휴직 중에도 재직·소속은 그대로: %+v", p)
	}
	if a, _, _ := userRow(e, u); a != 1 {
		t.Fatal("휴직은 계정을 막지 않는다(로그인 가능)")
	}
	e.reg(Command{Kind: KindAbsenceEnd, UserID: u, Effective: "2026-03-20", Reason: "복직"})
	if e.onLeaveAt(u, "2026-03-20") || !e.onLeaveAt(u, "2026-03-19") {
		t.Fatal("복직은 효력일부터 휴직이 아니다")
	}
	if err := Consistent(e.ctx, e.db); err != nil {
		t.Fatalf("저장된 구간이 이벤트 재계산과 같아야 함: %v", err)
	}
	// 휴직 이벤트가 없는 사람의 구간은 absence_kind 가 비어 있다(AC-5031 재생).
	other := e.hire("재직자", "stay@pl.test", t1, contract.RoleStaff, "2024-01-02")
	if n := e.count(`SELECT COUNT(*) FROM org_person_spans WHERE user_id = ? AND absence_kind <> ''`, other); n != 0 {
		t.Fatalf("휴직이 없는 사람의 구간 %d", n)
	}
}

// 검증 문구: 퇴사자·이미 휴직 중·휴직 중이 아닌데 복직·구분 누락·예정 복직일 순서(AC-5030).
func TestAbsenceValidation(t *testing.T) {
	e := newEnv(t)
	_, _, _, t1, _ := e.baseOrg()
	u := e.hire("휴직자", "leave2@pl.test", t1, contract.RoleStaff, "2024-01-02")
	gone := e.hire("퇴사자", "gone@pl.test", t1, contract.RoleStaff, "2024-01-02")
	e.reg(Command{Kind: KindLeave, UserID: gone, LastDay: "2026-03-01", LeaveKind: "자진", Reason: "퇴사 처리", Confirm: true})

	if err := e.regErr(e.absentStart(gone, "2026-03-10", AbsenceIllness)); !strings.Contains(err.Error(), "퇴사한 사람에게는 발령할 수 없습니다") {
		t.Fatalf("퇴사자: %v", err)
	}
	if err := e.regErr(Command{Kind: KindAbsenceEnd, UserID: u, Effective: "2026-03-10"}); !strings.Contains(err.Error(), "휴직 중이 아닙니다") {
		t.Fatalf("휴직 아닌데 복직: %v", err)
	}
	if err := e.regErr(e.absentStart(u, "2026-03-10", "")); !strings.Contains(err.Error(), "휴직 구분을 고르세요") {
		t.Fatalf("구분 누락: %v", err)
	}
	if err := e.regErr(e.absentStart(u, "2026-03-10", "nonsense")); !strings.Contains(err.Error(), "휴직 구분을 고르세요") {
		t.Fatalf("모르는 구분: %v", err)
	}
	if err := e.regErr(e.absentStart(u, "2026-03-10", AbsenceIllness, func(c *Command) { c.ReturnDay = "2026-03-10" })); !strings.Contains(err.Error(), "예정 복직일은 휴직 시작일 이후") {
		t.Fatalf("예정 복직일: %v", err)
	}
	long := strings.Repeat("가", MaxAbsenceReasonRunes+1)
	if err := e.regErr(e.absentStart(u, "2026-03-10", AbsenceIllness, func(c *Command) { c.AbsenceReason = long })); !strings.Contains(err.Error(), "200자 이내") {
		t.Fatalf("사유 길이: %v", err)
	}
	e.reg(e.absentStart(u, "2026-03-10", AbsenceIllness))
	if err := e.regErr(e.absentStart(u, "2026-03-12", AbsencePersonal)); !strings.Contains(err.Error(), "이미 휴직 중입니다") {
		t.Fatalf("두 번째 휴직: %v", err)
	}
	// 소급은 B20 규칙을 탄다: 사유 없이 과거 날짜는 거부.
	late := e.hire("소급", "retro@pl.test", t1, contract.RoleStaff, "2024-01-02")
	if _, err := e.svc.Register(e.ctx, e.admin, Command{Kind: KindAbsenceStart, UserID: late, Effective: "2026-02-01", AbsenceKind: AbsenceOther}); err == nil || !strings.Contains(err.Error(), "사유") {
		t.Fatalf("사유 없는 소급: %v", err)
	}
}

// 같은 날 휴직+복직은 등록 순서대로 적용되고, 휴직 중 전보·직위 변경은 허용되며 휴직 구간은 그대로이고, 휴직 중 퇴사는 퇴사 효력일에 휴직이 끝난다.
func TestAbsenceSameDayOrderTransferAndLeave(t *testing.T) {
	e := newEnv(t)
	_, _, _, t1, t2 := e.baseOrg()
	u := e.hire("휴직자", "leave3@pl.test", t1, contract.RoleStaff, "2024-01-02")
	e.reg(e.absentStart(u, "2026-03-15", AbsenceFamilyCare))
	e.reg(Command{Kind: KindAbsenceEnd, UserID: u, Effective: "2026-03-15"})
	if e.onLeaveAt(u, "2026-03-15") {
		t.Fatal("같은 날 휴직 뒤 복직이면 휴직이 아니다")
	}
	e.reg(e.absentStart(u, "2026-03-20", AbsenceIllness))
	e.reg(Command{Kind: KindTransfer, UserID: u, ToUnit: t2, Effective: "2026-03-25"})
	p, _ := e.svc.Person(e.ctx, u, "2026-03-25")
	if !p.OnLeave || p.Primary.Unit.ID != t2 {
		t.Fatalf("휴직 중 전보: %+v", p)
	}
	e.reg(Command{Kind: KindLeave, UserID: u, LastDay: "2026-03-30", LeaveKind: "자진"})
	after, _ := e.svc.Person(e.ctx, u, "2026-03-31")
	if after.Active || after.OnLeave {
		t.Fatalf("휴직 중 퇴사는 퇴사 효력일에 휴직이 끝난 것으로 계산한다: %+v", after)
	}
}

// 취소(예약된 휴직)와 정정이 B20 규칙으로 동작한다.
func TestAbsenceCancelAndCorrect(t *testing.T) {
	e := newEnv(t)
	_, _, _, t1, _ := e.baseOrg()
	u := e.hire("휴직자", "leave4@pl.test", t1, contract.RoleStaff, "2024-01-02")
	o := e.reg(e.absentStart(u, "2026-04-01", AbsenceIllness))
	if e.onLeaveAt(u, "2026-04-02") != true {
		t.Fatal("예약된 휴직은 효력일 이후 휴직")
	}
	if err := e.svc.Cancel(e.ctx, e.admin, o.EventIDs[0], "계획 변경", false); err != nil {
		t.Fatal(err)
	}
	if e.onLeaveAt(u, "2026-04-02") {
		t.Fatal("취소하면 휴직이 아니다")
	}
	o2 := e.reg(e.absentStart(u, "2026-04-01", AbsenceIllness))
	if _, err := e.svc.Correct(e.ctx, e.admin, o2.EventIDs[0], e.absentStart(u, "2026-05-01", AbsenceChildcare)); err != nil {
		t.Fatal(err)
	}
	if e.onLeaveAt(u, "2026-04-15") || !e.onLeaveAt(u, "2026-05-02") {
		t.Fatal("정정한 날짜부터 휴직이어야 한다")
	}
}

// 휴직 중인 조직장: 직무대행을 둘 수 있고 대행자가 실효 조직장이 되며, 대행이 없으면 조직장이 그대로 남는다(결재선은 서비스가 올린다). 복직하면 조직장이 돌아온다.
func TestAbsenceHeadActingAndReturn(t *testing.T) {
	e := newEnv(t)
	_, _, _, t1, _ := e.baseOrg()
	head := e.hire("팀장", "head@pl.test", t1, contract.RoleManager, "2024-01-02")
	sub := e.hire("대행자", "sub@pl.test", t1, contract.RoleManager, "2024-01-02")
	e.reg(Command{Kind: KindHeadSet, UnitID: t1, UserID: head, Effective: "2024-02-01"})
	// 조직장이 정상이면 직무대행을 둘 수 없다.
	if err := e.regErr(Command{Kind: KindActingSet, UnitID: t1, UserID: sub, EndDay: "2026-06-30", Effective: "2026-03-10"}); !strings.Contains(err.Error(), "조직장이 있는 기간에는") {
		t.Fatalf("조직장이 재직 중이면 대행 불가: %v", err)
	}
	o := e.reg(e.absentStart(head, "2026-03-10", AbsenceChildcare))
	if len(o.Warnings) == 0 || !strings.Contains(strings.Join(o.Warnings, " "), "직무대행을 지정하세요") {
		t.Fatalf("조직장 휴직 경고: %v", o.Warnings)
	}
	h, _ := e.svc.Head(e.ctx, t1, "2026-03-10")
	if h.UserID != head || h.Acting {
		t.Fatalf("대행이 없으면 조직장이 그대로: %+v", h)
	}
	e.reg(Command{Kind: KindActingSet, UnitID: t1, UserID: sub, EndDay: "2026-06-30", Effective: "2026-03-10"})
	h, _ = e.svc.Head(e.ctx, t1, "2026-03-10")
	if h.UserID != sub || !h.Acting {
		t.Fatalf("휴직 중 조직장의 자리는 대행자가 이어받는다: %+v", h)
	}
	e.reg(Command{Kind: KindAbsenceEnd, UserID: head, Effective: "2026-04-01"})
	h, _ = e.svc.Head(e.ctx, t1, "2026-04-02")
	if h.UserID != head || h.Acting {
		t.Fatalf("복직하면 조직장이 돌아온다: %+v", h)
	}
	if err := Consistent(e.ctx, e.db); err != nil {
		t.Fatal(err)
	}
}

// 이력·감사·미리보기에는 휴직 사유 원문이 없고 길이만 남는다(B50 15절). 알림은 사유·구분 없이 직속 조직장과 인사 담당에게 간다.
func TestAbsenceReasonNeverInAuditListOrNotice(t *testing.T) {
	e := newEnv(t)
	_, _, _, t1, _ := e.baseOrg()
	head := e.hire("팀장", "h@pl.test", t1, contract.RoleManager, "2024-01-02")
	u := e.hire("휴직자", "leave5@pl.test", t1, contract.RoleStaff, "2024-01-02")
	hr := e.hire("인사담당", "hr@pl.test", t1, contract.RoleStaff, "2024-01-02")
	e.reg(Command{Kind: KindHeadSet, UnitID: t1, UserID: head, Effective: "2024-02-01"})
	// 인사 담당: 사용자 정의 역할 "attendance:hr" 를 사람에게 부여한다(발령이 실효 권한을 다시 계산해도 남는다).
	for _, q := range []string{
		`INSERT INTO roles (key, name, kind, created_at, updated_at) VALUES ('r_hr','인사 담당','custom','a','a')`,
		`INSERT INTO role_permissions (role_id, permission) SELECT id, 'attendance:hr' FROM roles WHERE key='r_hr'`,
	} {
		if _, err := e.db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := e.db.Exec(`INSERT INTO role_grants (role_id, target_type, target_id, unit_kind, include_descendants, source, reason, created_at) SELECT id, 'user', ?, '', 0, 'manual', '시험', 'a' FROM roles WHERE key = 'r_hr'`, hr); err != nil {
		t.Fatal(err)
	}
	const secret = "독감과 입원 치료 때문에 쉽니다"
	pv, err := e.svc.Preview(e.ctx, e.admin, e.absentStart(u, "2026-03-12", AbsenceIllness, func(c *Command) { c.AbsenceReason = secret }))
	if err != nil || strings.Contains(strings.Join(pv.Effects, " "), "독감") {
		t.Fatalf("미리보기: %v %v", pv, err)
	}
	e.reg(e.absentStart(u, "2026-03-12", AbsenceIllness, func(c *Command) { c.AbsenceReason = secret; c.ReturnDay = "2026-04-30" }))

	recs, err := e.store.List(e.ctx, caudit.Filter{Limit: 100, Desc: true})
	if err != nil {
		t.Fatal(err)
	}
	var hit bool
	for _, r := range recs {
		if r.Action != "org.absence_start" {
			continue
		}
		hit = true
		var gotLen bool
		for _, d := range r.Diff {
			if strings.Contains(d.New+d.Old, "독감") {
				t.Fatalf("감사에 사유 원문: %+v", d)
			}
			if d.Key == "absence_reason_length" && d.New == "17" {
				gotLen = true
			}
		}
		if !gotLen {
			t.Fatalf("사유 길이만 남아야 함: %+v", r.Diff)
		}
	}
	if !hit {
		t.Fatal("휴직 감사 기록이 없다")
	}
	page, err := e.svc.ListEventsPage(e.ctx, EventFilter{Kind: KindAbsenceStart})
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("이력: %+v %v", page, err)
	}
	if v := page.Items[0]; strings.Contains(v.Summary+v.Reason, "독감") || !v.HasAbsenceReason || !strings.Contains(v.Summary, "사유 있음") {
		t.Fatalf("이력 요약: %+v", v)
	}
	// 알림 대상: 직속 조직장과 인사 담당. 내용에 사유·구분이 없다.
	for _, uid := range []int64{head, hr} {
		var title, body string
		if err := e.db.QueryRow(`SELECT title, body FROM notifications WHERE user_id = ? AND kind = 'hr_absence_start'`, uid).Scan(&title, &body); err != nil {
			t.Fatalf("알림 대상 %d: %v", uid, err)
		}
		if strings.Contains(title+body, "독감") || strings.Contains(title+body, "질병") || !strings.Contains(title, "휴직합니다") {
			t.Fatalf("알림 내용: %q %q", title, body)
		}
	}
	if n := e.count(`SELECT COUNT(*) FROM notifications WHERE user_id = ? AND kind = 'hr_absence_start'`, u); n != 0 {
		t.Fatal("본인에게는 알림이 없다")
	}
}

// AC-5041: 사유 원문 열람은 본인은 기록 없이, 직속 조직장·인사 담당은 기록 뒤에, 그 밖은 없는 것과 같다. 기록이 실패하면 사유 없이 실패한다.
func TestAbsenceReasonAccessScopeAndPrivacyNote(t *testing.T) {
	e := newEnv(t)
	_, _, _, t1, t2 := e.baseOrg()
	n := &fakeNoter{}
	e.svc.SetPrivacy(n)
	head := e.hire("팀장", "h2@pl.test", t1, contract.RoleManager, "2024-01-02")
	otherHead := e.hire("다른팀장", "oh@pl.test", t2, contract.RoleManager, "2024-01-02")
	u := e.hire("휴직자", "leave6@pl.test", t1, contract.RoleStaff, "2024-01-02")
	peer := e.hire("동료", "peer@pl.test", t1, contract.RoleStaff, "2024-01-02")
	e.reg(Command{Kind: KindHeadSet, UnitID: t1, UserID: head, Effective: "2024-02-01"})
	e.reg(Command{Kind: KindHeadSet, UnitID: t2, UserID: otherHead, Effective: "2024-02-01"})
	o := e.reg(e.absentStart(u, "2026-03-12", AbsenceIllness, func(c *Command) { c.AbsenceReason = "가족 간병" }))
	ev := o.EventIDs[0]
	who := func(uid int64, perms ...contract.Permission) contract.Principal {
		return contract.Principal{UserID: uid, Role: contract.RoleStaff, Channel: contract.ChannelWeb, Perms: contract.NewPermSet(append(perms, contract.PermAttendanceSelf)...)}
	}

	// 본인: 기록 없이 본다.
	v, err := e.svc.AbsenceReason(e.ctx, who(u), ev)
	if err != nil || v.Reason != "가족 간병" || len(n.got) != 0 {
		t.Fatalf("본인: %+v %v 기록 %d", v, err, len(n.got))
	}
	// 직속 조직장: 기록한 뒤 본다.
	v, err = e.svc.AbsenceReason(e.ctx, who(head, contract.PermAttendanceTeam), ev)
	if err != nil || v.Reason != "가족 간병" || len(n.got) != 1 {
		t.Fatalf("직속 조직장: %+v %v 기록 %d", v, err, len(n.got))
	}
	g := n.got[0]
	if g.Category != privacy.CatAttendance || g.Screen != privacy.ScreenAttendanceReason || strings.Join(g.Fields, ",") != privacy.FieldAbsenceReason ||
		len(g.Subjects) != 1 || g.Subjects[0] != (privacy.Subject{Type: privacy.SubjectUser, ID: u}) {
		t.Fatalf("열람 기록 내용: %+v", g)
	}
	// 다른 팀 조직장·동료·관리자·감사자(권한 없는 사람)는 없는 것과 같다.
	for name, p := range map[string]contract.Principal{
		"다른 팀 조직장":     who(otherHead, contract.PermAttendanceTeam),
		"동료":           who(peer),
		"관리자":          {UserID: e.admin.UserID, Role: contract.RoleAdmin, Channel: contract.ChannelWeb, Perms: contract.NewPermSet(contract.PermAttendanceSelf, contract.PermOrgManage)},
		"조직장 권한 없는 팀장": who(head),
	} {
		if _, err := e.svc.AbsenceReason(e.ctx, p, ev); !errors.Is(err, contract.ErrNotFound) {
			t.Fatalf("%s: 없는 것과 같아야 함: %v", name, err)
		}
	}
	if len(n.got) != 1 {
		t.Fatalf("거부된 열람은 기록하지 않는다: %d", len(n.got))
	}
	// 인사 담당(같은 법인): 기록한 뒤 본다.
	v, err = e.svc.AbsenceReason(e.ctx, who(peer, contract.PermAttendanceHR), ev)
	if err != nil || v.Reason != "가족 간병" || len(n.got) != 2 {
		t.Fatalf("인사 담당: %+v %v 기록 %d", v, err, len(n.got))
	}
	// 기록 실패: 사유 없이 실패한다(fail-closed).
	n.fail = errors.New("기록 저장소 장애")
	v, err = e.svc.AbsenceReason(e.ctx, who(head, contract.PermAttendanceTeam), ev)
	if !errors.Is(err, ErrPrivacyNote) || v.Reason != "" {
		t.Fatalf("기록 실패는 사유 없이 실패해야 함: %+v %v", v, err)
	}
	// 기록기가 연결되지 않은 서비스도 본인이 아닌 사람에게 사유를 내지 않는다.
	e.svc.SetPrivacy(nil)
	if v, err := e.svc.AbsenceReason(e.ctx, who(head, contract.PermAttendanceTeam), ev); !errors.Is(err, ErrPrivacyNote) || v.Reason != "" {
		t.Fatalf("기록기 없음: %+v %v", v, err)
	}
	// 휴직 이벤트가 아닌 번호는 404.
	if _, err := e.svc.AbsenceReason(e.ctx, who(u), 1); !errors.Is(err, contract.ErrNotFound) {
		t.Fatalf("휴직이 아닌 이벤트: %v", err)
	}
}

// 조직장이 이 휴직 단계로 사람 카드 DTO 에 휴직 표시가 있다(구분은 없다).
func TestAbsenceCardHasOnLeaveFlagOnly(t *testing.T) {
	e := newEnv(t)
	_, _, _, t1, _ := e.baseOrg()
	u := e.hire("휴직자", "leave7@pl.test", t1, contract.RoleStaff, "2024-01-02")
	e.reg(e.absentStart(u, "2026-03-10", AbsenceIllness))
	c, err := e.svc.Card(e.ctx, e.admin, u, "")
	if err != nil || !c.OnLeave {
		t.Fatalf("카드: %+v %v", c, err)
	}
}
