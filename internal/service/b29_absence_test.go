package service_test

import (
	"strings"
	"testing"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
)

func (e *env) absenceIn(to int64, from time.Time, days int, reason string) domain.DelegationInput {
	return domain.DelegationInput{DelegateID: to, From: from, To: from.Add(time.Duration(days) * 24 * time.Hour), Kind: domain.DelegationKindAbsence, Reason: reason}
}

func (e *env) notes(t *testing.T, uid int64, kind string) []domain.Notification {
	t.Helper()
	all, err := e.notification.List(as(uid), domain.NotificationFilter{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	var out []domain.Notification
	for _, n := range all {
		if n.Kind == kind {
			out = append(out, n)
		}
	}
	return out
}

// AC-800: 부재 등록의 입력 규칙과 문구(3.6).
func TestAbsenceValidation(t *testing.T) {
	e := newEnv(t)
	now := e.now
	cases := []struct {
		name string
		in   domain.DelegationInput
		want string
	}{
		{"사유 없음", e.absenceIn(uHead, now, 3, ""), "부재 사유를 고르세요."},
		{"대결자 없음", e.absenceIn(0, now, 3, "vacation"), "대결자를 고르세요."},
		{"과거 시작", e.absenceIn(uHead, now.Add(-2*time.Hour), 3, "vacation"), "부재 기간이 올바르지 않습니다. 시작은 지금 이후, 종료는 시작 이후로 정하세요."},
		{"90일 초과", e.absenceIn(uHead, now, 91, "trip"), "부재는 한 번에 90일까지 등록할 수 있습니다. 더 길면 나누어 등록하세요."},
		{"서열 낮은 대결자", e.absenceIn(uStaff, now, 3, "trip"), "이 사람은 대결자로 지정할 수 없습니다("},
	}
	for _, c := range cases {
		_, err := e.delegation.Create(as(uLead), c.in)
		if !isErr(err, contract.ErrValidation) || !strings.Contains(err.Error(), c.want) {
			t.Fatalf("%s: %v", c.name, err)
		}
	}
	long := e.absenceIn(uHead, now, 3, "training")
	long.Note = strings.Repeat("가", 101)
	if _, err := e.delegation.Create(as(uLead), long); !isErr(err, contract.ErrValidation) {
		t.Fatalf("메모 길이: %v", err)
	}
	// 정상 등록, 겹침 거부, 해제 뒤에는 다시 등록할 수 있다.
	d, err := e.delegation.Create(as(uLead), e.absenceIn(uHead, now.Add(24*time.Hour), 4, "vacation"))
	if err != nil || d.Kind != domain.DelegationKindAbsence || d.Reason != "vacation" {
		t.Fatalf("등록: %+v %v", d, err)
	}
	_, err = e.delegation.Create(as(uLead), e.absenceIn(uCEO, now.Add(48*time.Hour), 2, "trip"))
	if !isErr(err, contract.ErrValidation) || !strings.Contains(err.Error(), "이미 등록된 부재(2026. 11. 11.~11. 15.)와 기간이 겹칩니다. 기간을 바꾸거나 기존 부재를 해제하세요.") {
		t.Fatalf("겹침: %v", err)
	}
	// 대결자가 같은 기간에 부재 중이면 지정할 수 없다.
	if _, err := e.delegation.Create(as(uHead), e.absenceIn(uCEO, now.Add(24*time.Hour), 2, "trip")); err != nil {
		t.Fatalf("부서장 부재: %v", err)
	}
	_, err = e.delegation.Create(as(uLead2), e.absenceIn(uHead, now.Add(30*time.Hour), 1, "other"))
	if !isErr(err, contract.ErrValidation) || !strings.Contains(err.Error(), "같은 기간에 부재 중입니다") {
		t.Fatalf("대결자 부재 중: %v", err)
	}
	if err := e.delegation.Revoke(as(uLead), d.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.delegation.Create(as(uLead), e.absenceIn(uCEO, now.Add(24*time.Hour), 4, "vacation")); err != nil {
		t.Fatalf("해제 뒤 재등록: %v", err)
	}
}

// AC-801·802·805: 부재 시작 전에 이미 도착해 있던 결재도 대결자가 처리하고(on_behalf_of), 원결재자도 처리할 수 있으며, 끝난 뒤에는 대결할 수 없다. 알림이 사건마다 만들어진다.
func TestAbsenceDecideOnBehalfAndExpiry(t *testing.T) {
	e := newEnv(t)
	sales := e.submitted(t, uStaff2, 500_000) // 영업1팀 팀장(uLead2)의 단계
	later := e.submitted(t, uStaff2, 400_000)
	d, err := e.delegation.Create(as(uLead2), e.absenceIn(uHead, e.now, 2, "vacation"))
	if err != nil {
		t.Fatal(err)
	}
	as1 := e.notes(t, uHead, "absence_assigned")
	if len(as1) != 1 || !strings.Contains(as1[0].Body, "지금 대결할 수 있는 결재가 2건 있습니다.") || !strings.Contains(as1[0].Body, "영업팀장") && !strings.Contains(as1[0].Body, "님이") {
		t.Fatalf("등록 알림: %+v", as1)
	}
	// 대결자(부서장)가 대결한다.
	res, err := e.approval.Decide(as(uHead), sales.ID, domain.Decision{Approve: true})
	if err != nil {
		t.Fatalf("대결: %v", err)
	}
	_ = res
	hist, herr := e.approval.History(as(uAuditor), sales.ID)
	if herr != nil {
		t.Fatalf("이력: %v", herr)
	}
	onBehalf := false
	for _, h := range hist {
		if h.OnBehalfOfID == uLead2 && h.ActorID == uHead {
			onBehalf = true
		}
	}
	if !onBehalf {
		t.Fatalf("대결 기록(on_behalf_of)이 없음: %+v", hist)
	}
	// 원결재자도 부재 중에 처리할 수 있다.
	if _, err := e.approval.Decide(as(uLead2), later.ID, domain.Decision{Approve: true}); err != nil {
		t.Fatalf("원결재자 결재: %v", err)
	}
	// 기간이 끝난 뒤에는 대결할 수 없다(자동 종료).
	third := e.submitted(t, uStaff2, 300_000)
	e.now = d.To.Add(time.Minute)
	if _, err := e.approval.Decide(as(uHead), third.ID, domain.Decision{Approve: true}); err == nil {
		t.Fatal("부재가 끝난 뒤 대결이 처리됨")
	}
	if _, err := e.approval.Decide(as(uLead2), third.ID, domain.Decision{Approve: true}); err != nil {
		t.Fatalf("원결재자는 계속 결재: %v", err)
	}
}

// AC-805: 예정 부재의 시작 알림과 종료 알림은 주기 작업(Tick)이 한 번씩만 만든다. 조기 해제는 대결자에게 해제 알림, 원결재자에게 종료 알림.
func TestAbsenceNotificationsTick(t *testing.T) {
	e := newEnv(t)
	start := e.now.Add(24 * time.Hour)
	d, err := e.delegation.Create(as(uLead2), e.absenceIn(uHead, start, 2, "trip"))
	if err != nil {
		t.Fatal(err)
	}
	if err := e.delegation.Tick(t.Context()); err != nil || len(e.notes(t, uHead, "absence_started")) != 0 {
		t.Fatalf("시작 전: %v", err)
	}
	e.now = start.Add(time.Minute)
	for i := 0; i < 2; i++ {
		if err := e.delegation.Tick(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	if n := len(e.notes(t, uHead, "absence_started")); n != 1 {
		t.Fatalf("시작 알림 %d건(기대 1)", n)
	}
	e.now = d.To.Add(time.Minute)
	for i := 0; i < 2; i++ {
		if err := e.delegation.Tick(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	if n := len(e.notes(t, uLead2, "absence_ended")); n != 1 {
		t.Fatalf("종료 알림 %d건(기대 1)", n)
	}
	// 조기 해제.
	e2 := newEnv(t)
	d2, err := e2.delegation.Create(as(uLead2), e2.absenceIn(uHead, e2.now, 3, "other"))
	if err != nil {
		t.Fatal(err)
	}
	if err := e2.delegation.Revoke(as(uLead2), d2.ID); err != nil {
		t.Fatal(err)
	}
	if len(e2.notes(t, uHead, "absence_revoked")) != 1 || len(e2.notes(t, uLead2, "absence_ended")) != 1 {
		t.Fatal("해제 알림이 없음")
	}
	if err := e2.delegation.Tick(t.Context()); err != nil || len(e2.notes(t, uLead2, "absence_ended")) != 1 {
		t.Fatal("해제된 부재의 종료 알림이 중복됨")
	}
}

// AC-806: 결재선 미리보기에 결재자의 부재 종료 시각과 대결자가 붙는다(결재선 자체는 그대로). 부재가 없으면 비어 있다.
func TestAbsenceShownInLinePreview(t *testing.T) {
	e := newEnv(t)
	before, err := e.liner().PreviewLine(as(uStaff), domain.LinePreviewInput{Amount: 500_000})
	if err != nil || len(before.Steps) == 0 || before.Steps[0].AbsentUntil != nil {
		t.Fatalf("부재 전: %+v %v", before.Steps, err)
	}
	d, err := e.delegation.Create(as(uLead), e.absenceIn(uHead, e.now, 3, "trip"))
	if err != nil {
		t.Fatal(err)
	}
	pv, err := e.liner().PreviewLine(as(uStaff), domain.LinePreviewInput{Amount: 500_000})
	if err != nil {
		t.Fatal(err)
	}
	s := pv.Steps[0]
	if s.ApproverID != uLead || s.AbsentUntil == nil || !s.AbsentUntil.Equal(d.To) || s.AbsenceDelegate != "부서장" && s.AbsenceDelegate == "" {
		t.Fatalf("부재 안내: %+v", s)
	}
	if doc, err := e.docs().PreviewLine(as(uStaff), domain.DocLineInput{FormCode: "GEN"}); err != nil || len(doc.Steps) == 0 || doc.Steps[0].AbsentUntil == nil {
		t.Fatalf("양식 문서 미리보기: %+v %v", doc.Steps, err)
	}
}

// AC-803: 부재와 위임이 함께 있어도 같은 사람은 한 번만 후보로 센다(결재는 한 번만 처리된다).
func TestAbsenceAndDelegationUnion(t *testing.T) {
	e := newEnv(t)
	pr := e.submitted(t, uStaff2, 500_000)
	e.mustDelegate(t, uLead2, uHead)
	if _, err := e.delegation.Create(as(uLead2), e.absenceIn(uHead, e.now, 2, "vacation")); err != nil {
		t.Fatal(err)
	}
	if _, err := e.approval.Decide(as(uHead), pr.ID, domain.Decision{Approve: true}); err != nil {
		t.Fatalf("대결: %v", err)
	}
	if _, err := e.approval.Decide(as(uLead2), pr.ID, domain.Decision{Approve: true}); err == nil {
		t.Fatal("이미 처리된 단계가 다시 처리됨")
	}
}
