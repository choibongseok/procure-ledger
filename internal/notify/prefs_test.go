package notify

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
)

// 시각 표기: 시험 시계는 UTC 이고 KST = UTC+9. 2026-11-12(목) 01:00 UTC = 10:00 KST.
func at(day string, h, m int) time.Time {
	t, _ := time.Parse("2006-01-02", day)
	return t.Add(time.Duration(h)*time.Hour + time.Duration(m)*time.Minute)
}

// kstClock 은 KST 시각을 UTC 시계 값으로 바꾼다.
func kstClock(day string, h, m int) time.Time { return at(day, h, m).Add(-9 * time.Hour) }

func (e *env) calendar(nonBusiness ...string) {
	set := map[string]bool{}
	for _, d := range nonBusiness {
		set[d] = true
	}
	e.svc.SetBusinessDay(func(_ context.Context, _ contract.Tx, day string) (bool, error) { return !set[day], nil })
}

func (e *env) setSetting(t *testing.T, key, val string) {
	t.Helper()
	if _, err := e.db.Exec(`INSERT INTO app_settings(key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, val); err != nil {
		t.Fatal(err)
	}
}

func (e *env) deliveries(t *testing.T, user int64, status string) int {
	return e.n(t, `SELECT COUNT(*) FROM notification_deliveries WHERE user_id = ? AND channel = 'email' AND status = ?`, user, status)
}

func TestMyPrefsDefaultsAndOverrides(t *testing.T) {
	e := newEnv(t)
	ctx := as(e.staff)
	p, err := e.svc.MyPrefs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]PrefKind{}
	for _, k := range p.Kinds {
		by[k.Kind] = k
	}
	if len(p.Kinds) != len(Kinds) {
		t.Fatalf("기본값 행이 있는 모든 종류가 보여야 한다: %d/%d", len(p.Kinds), len(Kinds))
	}
	if k := by["approval_pending"]; !k.DefaultEnabled || k.Mode != "default" || !k.Effective {
		t.Errorf("결재 요청 기본값: %+v", k)
	}
	if k := by["approval_done"]; k.DefaultEnabled || k.Effective {
		t.Errorf("결재 완료는 기본 꺼짐: %+v", k)
	}
	if p.Digest.Mode != "default" || p.Digest.Effective || p.WorkHours != "09:00~18:00" || p.HoldMaxHours != 72 {
		t.Errorf("묶음 기본: %+v %s %d", p.Digest, p.WorkHours, p.HoldMaxHours)
	}
	// 켜기·끄기·기본값 따름
	if err := e.svc.SetMyPref(ctx, "approval_done", "on"); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.SetMyPref(ctx, "approval_pending", "off"); err != nil {
		t.Fatal(err)
	}
	e.emit(t, e.uid["staff"], "approval_done", "t", "b", "/docs/1")    // 기본 꺼짐이지만 본인이 켬 → 발송 건
	e.emit(t, e.uid["staff"], "approval_pending", "t", "b", "/docs/2") // 기본 켜짐이지만 본인이 끔 → 발송 건 없음
	e.emit(t, e.uid["admin"], "approval_pending", "t", "b", "/docs/3") // 다른 사람은 기본값 그대로
	if got := e.deliveries(t, e.uid["staff"], "queued"); got != 1 {
		t.Errorf("직원 대기 건 = %d, want 1(결재 완료만)", got)
	}
	if got := e.deliveries(t, e.uid["admin"], "queued"); got != 1 {
		t.Errorf("관리자는 기본값을 따른다: %d", got)
	}
	// 앱 안 알림은 모두 만들어졌다(끌 수 없다, D3).
	if got := e.n(t, `SELECT COUNT(*) FROM notifications WHERE user_id = ?`, e.uid["staff"]); got != 2 {
		t.Errorf("앱 안 알림은 항상 만들어진다: %d", got)
	}
	// 기본값 따름으로 돌리면 기본값이 다시 적용된다.
	if err := e.svc.SetMyPref(ctx, "approval_pending", "default"); err != nil {
		t.Fatal(err)
	}
	e.emit(t, e.uid["staff"], "approval_pending", "t", "b", "/docs/4")
	if got := e.deliveries(t, e.uid["staff"], "queued"); got != 2 {
		t.Errorf("기본값 따름으로 되돌림: %d", got)
	}
	if got := e.n(t, `SELECT COUNT(*) FROM notification_prefs WHERE user_id = ? AND kind = 'approval_pending'`, e.uid["staff"]); got != 0 {
		t.Errorf("기본값 따름은 행을 지운다: %d", got)
	}
}

func TestMyPrefsAreOwnOnlyAndNotAudited(t *testing.T) {
	e := newEnv(t)
	if err := e.svc.SetMyPref(as(e.staff), "approval_done", "on"); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.SetMyDigest(as(e.staff), "on"); err != nil {
		t.Fatal(err)
	}
	// 다른 사람의 설정에는 영향이 없고 보이지도 않는다(관리자도 마찬가지).
	for _, p := range []contract.Principal{e.admin, e.auditr} {
		got, err := e.svc.MyPrefs(as(p))
		if err != nil {
			t.Fatal(err)
		}
		for _, k := range got.Kinds {
			if k.Kind == "approval_done" && k.Mode != "default" {
				t.Errorf("%s 에게 다른 사람의 설정이 보인다", p.Name)
			}
		}
		if got.Digest.Mode != "default" {
			t.Errorf("%s 의 묶음 설정이 직원 것에 영향을 받았다", p.Name)
		}
	}
	// 본인 설정 변경은 개인 상태라 감사에 남기지 않는다.
	if got := e.n(t, `SELECT COUNT(*) FROM audit_logs WHERE action LIKE 'notification.%'`); got != 0 {
		t.Errorf("본인 수신 설정은 감사 대상이 아니다: %d", got)
	}
	// 로그인하지 않은 호출은 거부.
	if _, err := e.svc.MyPrefs(context.Background()); !errors.Is(err, contract.ErrUnauthenticated) {
		t.Errorf("미인증: %v", err)
	}
	// 입력 검증
	for _, c := range []struct{ kind, mode string }{{"nope", "on"}, {"approval_done", "maybe"}} {
		if err := e.svc.SetMyPref(as(e.staff), c.kind, c.mode); !errors.Is(err, contract.ErrValidation) {
			t.Errorf("%+v: %v", c, err)
		}
	}
	if err := e.svc.SetMyDigest(as(e.staff), "x"); !errors.Is(err, contract.ErrValidation) {
		t.Errorf("묶음 입력: %v", err)
	}
}

func TestLockedKindCannotBeTurnedOff(t *testing.T) {
	e := newEnv(t)
	if err := e.svc.SetDefault(as(e.admin), "approval_pending", true, true); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.SetMyPref(as(e.staff), "approval_pending", "off"); !errors.Is(err, contract.ErrInvalidState) {
		t.Fatalf("필수 알림은 끌 수 없다: %v", err)
	}
	// 필수가 되기 전에 꺼 둔 사람의 설정도 무시된다.
	e2 := newEnv(t)
	if err := e2.svc.SetMyPref(as(e2.staff), "approval_pending", "off"); err != nil {
		t.Fatal(err)
	}
	if err := e2.svc.SetDefault(as(e2.admin), "approval_pending", true, true); err != nil {
		t.Fatal(err)
	}
	e2.emit(t, e2.uid["staff"], "approval_pending", "t", "b", "/docs/1")
	if got := e2.deliveries(t, e2.uid["staff"], "queued"); got != 1 {
		t.Errorf("필수 알림은 끄기 설정을 무시한다: %d", got)
	}
	p, _ := e2.svc.MyPrefs(as(e2.staff))
	for _, k := range p.Kinds {
		if k.Kind == "approval_pending" && (!k.Locked || !k.Effective) {
			t.Errorf("화면에는 필수·켜짐으로 보여야 한다: %+v", k)
		}
	}
	// 필수인데 꺼진 기본값은 만들 수 없다.
	if err := e.svc.SetDefault(as(e.admin), "approval_done", false, true); !errors.Is(err, contract.ErrValidation) {
		t.Errorf("필수+꺼짐: %v", err)
	}
}

func TestAdminDefaultsAudit(t *testing.T) {
	e := newEnv(t)
	for _, p := range []contract.Principal{e.staff, e.auditr} {
		if _, err := e.svc.Defaults(as(p)); !errors.Is(err, contract.ErrForbidden) {
			t.Errorf("%s 기본값 조회: %v", p.Name, err)
		}
		if err := e.svc.SetDefault(as(p), "approval_done", true, false); !errors.Is(err, contract.ErrForbidden) {
			t.Errorf("%s 기본값 변경: %v", p.Name, err)
		}
		if err := e.svc.SetSettings(as(p), map[string]string{"notify.hold_max_hours": "48"}); !errors.Is(err, contract.ErrForbidden) {
			t.Errorf("%s 설정 변경: %v", p.Name, err)
		}
	}
	if err := e.svc.SetDefault(as(e.admin), "approval_done", true, false); err != nil {
		t.Fatal(err)
	}
	d, err := e.svc.Defaults(as(e.admin))
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, k := range d.Kinds {
		if k.Kind == "approval_done" {
			found = k.Enabled
		}
	}
	if !found {
		t.Error("바꾼 기본값이 보여야 한다")
	}
	var diff string
	if err := e.db.QueryRow(`SELECT diff FROM audit_logs WHERE action = 'notification.default_set'`).Scan(&diff); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(diff, "꺼짐") || !strings.Contains(diff, "켜짐") || !strings.Contains(diff, "결재 완료") {
		t.Errorf("감사 diff: %s", diff)
	}
	// 기본값 변경은 "기본값 따름" 사람에게만 새 이벤트부터 적용된다.
	if err := e.svc.SetMyPref(as(e.staff), "approval_done", "off"); err != nil {
		t.Fatal(err)
	}
	e.emit(t, e.uid["staff"], "approval_done", "t", "b", "/docs/1")
	e.emit(t, e.uid["admin"], "approval_done", "t", "b", "/docs/2")
	if e.deliveries(t, e.uid["staff"], "queued") != 0 || e.deliveries(t, e.uid["admin"], "queued") != 1 {
		t.Error("기본값은 기본값 따름 사람에게만 적용된다")
	}
}

func TestSettingsValidationAndAudit(t *testing.T) {
	e := newEnv(t)
	ctx := as(e.admin)
	bad := []map[string]string{
		{"notify.hold_max_hours": "0"},
		{"notify.hold_max_hours": "721"},
		{"notify.hold_max_hours": "abc"},
		{"notify.work_start": "9:00"},
		{"notify.work_start": "25:00"},
		{"notify.work_end": "08:00"},   // 시작(09:00)보다 이르다
		{"notify.work_start": "19:00"}, // 끝(18:00)보다 늦다
		{"notify.digest_default": "2"},
		{"notify.payment_due_bd": "11"},
		{"notify.nope": "1"},
		{"notify.max_attempts": "5", "notify.close_check_every_days": "31"}, // 하나라도 어기면 아무것도 저장하지 않는다
	}
	for _, v := range bad {
		if err := e.svc.SetSettings(ctx, v); !errors.Is(err, contract.ErrValidation) {
			t.Errorf("%v: 검증 오류여야 한다: %v", v, err)
		}
	}
	if got := e.n(t, `SELECT COUNT(*) FROM audit_logs WHERE action = 'notification.settings_set'`); got != 0 {
		t.Errorf("거부된 변경은 감사에 남지 않는다: %d", got)
	}
	if got := strSetting(context.Background(), e.db, "notify.max_attempts", "?"); got != "5" {
		t.Errorf("일부만 저장되었다: %s", got)
	}
	if err := e.svc.SetSettings(ctx, map[string]string{"notify.work_start": "08:30", "notify.work_end": "17:30", "notify.hold_max_hours": "48", "notify.payment_due_bd": "3"}); err != nil {
		t.Fatal(err)
	}
	var diff string
	if err := e.db.QueryRow(`SELECT diff FROM audit_logs WHERE action = 'notification.settings_set'`).Scan(&diff); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"업무 시작 시각", "08:30", "09:00", "묶음 보류 상한", "48", "72"} {
		if !strings.Contains(diff, want) {
			t.Errorf("감사에 %q 가 없다: %s", want, diff)
		}
	}
	if strings.Contains(diff, "지급 예정 알림 범위") {
		t.Errorf("바뀌지 않은 항목은 감사에 없다: %s", diff)
	}
	p, _ := e.svc.MyPrefs(as(e.staff))
	if p.WorkHours != "08:30~17:30" || p.HoldMaxHours != 48 {
		t.Errorf("내 화면에 바뀐 값이 보인다: %s %d", p.WorkHours, p.HoldMaxHours)
	}
}

// ---- 업무 시간 외 묶음 발송 ----

func TestDigestHeldAtNightAndReleasedAtWorkStart(t *testing.T) {
	e := newEnv(t)
	e.calendar() // 모든 날 영업일
	if err := e.svc.SetMyDigest(as(e.staff), "on"); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.SetMyPref(as(e.staff), "approval_done", "on"); err != nil {
		t.Fatal(err)
	}
	// 목요일 22:00 KST — 업무 시간 밖이라 보류된다.
	*e.clock = kstClock("2026-11-12", 22, 0)
	e.emit(t, e.uid["staff"], "approval_pending", "t", "b", "/approvals/1")
	e.emit(t, e.uid["staff"], "approval_pending", "t", "b", "/approvals/2")
	e.emit(t, e.uid["staff"], "approval_done", "t", "b", "/docs/9")
	if e.deliveries(t, e.uid["staff"], "held") != 3 || e.deliveries(t, e.uid["staff"], "queued") != 0 {
		t.Fatalf("밤에 생긴 메일 알림은 보류: held=%d queued=%d", e.deliveries(t, e.uid["staff"], "held"), e.deliveries(t, e.uid["staff"], "queued"))
	}
	e.advance(2 * time.Minute)
	e.tick(t)
	if e.mail.count() != 0 {
		t.Fatal("보류 건은 밤에 나가지 않는다")
	}
	// 다음 날 08:59 KST — 아직 업무 시간 전이다(시작 시각 직전).
	*e.clock = kstClock("2026-11-13", 8, 59)
	e.tick(t)
	if e.mail.count() != 0 || e.deliveries(t, e.uid["staff"], "held") != 3 {
		t.Fatal("업무 시작 1분 전에는 풀리지 않는다")
	}
	// 09:00 KST — 시작 시각 정각에 풀려 요약 메일 한 통으로 나간다(경계: 시작 시각 포함).
	*e.clock = kstClock("2026-11-13", 9, 0)
	e.tick(t)
	if e.mail.count() != 1 {
		t.Fatalf("요약 메일은 한 통: %d", e.mail.count())
	}
	m := e.mail.sent[0]
	if m.to != "staff@pl.test" || m.subject != "[ProcureLedger] 알림 요약" {
		t.Errorf("요약 메일: %+v", m)
	}
	for _, want := range []string{"- 결재 요청 2건", "- 결재 완료 1건", "열기: https://erp.example/notifications"} {
		if !strings.Contains(m.body, want) {
			t.Errorf("요약에 %q 가 없다:\n%s", want, m.body)
		}
	}
	if strings.Contains(m.body, "/approvals/1") || strings.Contains(m.body, "/docs/9") {
		t.Errorf("개별 항목(링크)을 나열하면 안 된다:\n%s", m.body)
	}
	if got := e.n(t, `SELECT COUNT(DISTINCT digest_id) FROM notification_deliveries WHERE user_id = ? AND status = 'sent'`, e.uid["staff"]); got != 1 {
		t.Errorf("한 묶음 번호를 공유한다: %d", got)
	}
	if e.deliveries(t, e.uid["staff"], "sent") != 3 {
		t.Errorf("세 건 모두 sent: %d", e.deliveries(t, e.uid["staff"], "sent"))
	}
	// 다시 불러도 더 나가지 않는다.
	e.advance(time.Minute)
	e.tick(t)
	if e.mail.count() != 1 {
		t.Errorf("중복 발송: %d", e.mail.count())
	}
}

func TestDigestNudgeAndWebhookNotHeld(t *testing.T) {
	e := newHookEnv(t)
	e.calendar()
	e.addTarget(t, "결재방", "slack", "approval_pending")
	if err := e.svc.SetMyDigest(as(e.staff), "on"); err != nil {
		t.Fatal(err)
	}
	*e.clock = kstClock("2026-11-12", 23, 0)
	e.emit(t, e.uid["staff"], "approval_nudge", "t", "b", "/approvals/7")
	e.emit(t, e.uid["staff"], "approval_pending", "t", "b", "/approvals/8")
	if got := e.deliveries(t, e.uid["staff"], "queued"); got != 1 {
		t.Errorf("독촉은 묶지 않고 바로 큐에 올린다: queued=%d", got)
	}
	if got := e.deliveries(t, e.uid["staff"], "held"); got != 1 {
		t.Errorf("결재 요청 메일은 보류: held=%d", got)
	}
	if got := e.n(t, `SELECT COUNT(*) FROM notification_deliveries WHERE target_id IS NOT NULL AND status = 'queued'`); got != 1 {
		t.Errorf("웹훅은 묶음 대상이 아니다: %d", got)
	}
	e.advance(2 * time.Minute)
	e.tick(t)
	if e.mail.count() != 1 || !strings.Contains(e.mail.sent[0].subject, "결재 독촉") {
		t.Errorf("독촉 메일만 즉시: %+v", e.mail.sent)
	}
	if e.recv.count() != 1 {
		t.Errorf("웹훅은 즉시 나간다: %d", e.recv.count())
	}
}

func TestDigestBusinessDayBoundaryAndHoldMax(t *testing.T) {
	e := newEnv(t)
	// 2026-11-14(토)·15(일)은 주말, 16(월)은 임시 공휴일로 등록된 날이라고 본다.
	e.calendar("2026-11-14", "2026-11-15", "2026-11-16")
	if err := e.svc.SetMyDigest(as(e.staff), "on"); err != nil {
		t.Fatal(err)
	}
	// 금요일 18:00 KST 정각 = 업무 종료 시각(끝 미포함) → 보류.
	*e.clock = kstClock("2026-11-13", 18, 0)
	e.emit(t, e.uid["staff"], "approval_pending", "t", "b", "/approvals/1")
	if e.deliveries(t, e.uid["staff"], "held") != 1 {
		t.Fatal("업무 종료 시각 정각은 업무 시간 밖이다")
	}
	// 17:59 KST 는 업무 시간 안이라 보류하지 않는다.
	*e.clock = kstClock("2026-11-13", 17, 59)
	e.emit(t, e.uid["staff"], "approval_rejected", "t", "b", "/docs/2")
	if e.deliveries(t, e.uid["staff"], "queued") != 1 {
		t.Fatal("업무 종료 1분 전은 업무 시간이다")
	}
	// 토요일 10:00 KST — 시각은 업무 시간이어도 영업일이 아니라 풀리지 않는다.
	*e.clock = kstClock("2026-11-14", 10, 0)
	e.tick(t)
	if e.deliveries(t, e.uid["staff"], "held") != 1 {
		t.Fatal("주말에는 풀리지 않는다")
	}
	// 월요일(공휴일) 10:00 — 휴일이라 풀리지 않는다.
	*e.clock = kstClock("2026-11-16", 10, 0)
	e.tick(t)
	if e.deliveries(t, e.uid["staff"], "held") != 1 {
		t.Fatal("공휴일에는 풀리지 않는다")
	}
	// 화요일 09:00 — 첫 영업일 업무 시작에 풀린다.
	*e.clock = kstClock("2026-11-17", 9, 0)
	e.tick(t)
	if e.deliveries(t, e.uid["staff"], "held") != 0 || e.deliveries(t, e.uid["staff"], "sent") < 1 {
		t.Fatalf("첫 영업일 업무 시작에 풀려야 한다: held=%d sent=%d", e.deliveries(t, e.uid["staff"], "held"), e.deliveries(t, e.uid["staff"], "sent"))
	}
}

func TestDigestHoldMaxReleasesDuringLongHoliday(t *testing.T) {
	e := newEnv(t)
	e.calendar("2026-11-14", "2026-11-15", "2026-11-16", "2026-11-17", "2026-11-18") // 긴 연휴
	e.setSetting(t, "notify.hold_max_hours", "72")
	if err := e.svc.SetMyDigest(as(e.staff), "on"); err != nil {
		t.Fatal(err)
	}
	*e.clock = kstClock("2026-11-13", 19, 0)
	e.emit(t, e.uid["staff"], "approval_pending", "t", "b", "/approvals/1")
	// 71시간 59분 뒤: 아직 보류
	*e.clock = kstClock("2026-11-13", 19, 0).Add(71*time.Hour + 59*time.Minute)
	e.tick(t)
	if e.deliveries(t, e.uid["staff"], "held") != 1 {
		t.Fatal("상한 직전에는 보류")
	}
	// 정확히 72시간 — 업무 시간이 아니어도(연휴 중) 풀려 나간다.
	*e.clock = kstClock("2026-11-13", 19, 0).Add(72 * time.Hour)
	e.tick(t)
	if e.deliveries(t, e.uid["staff"], "held") != 0 || e.mail.count() != 1 {
		t.Fatalf("보류 상한에 풀려야 한다: held=%d mail=%d", e.deliveries(t, e.uid["staff"], "held"), e.mail.count())
	}
}

func TestDigestDefaultFromAdminAndUserOverride(t *testing.T) {
	e := newEnv(t)
	e.calendar()
	if err := e.svc.SetSettings(as(e.admin), map[string]string{"notify.digest_default": "1"}); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.SetMyDigest(as(e.auditr), "off"); err != nil { // 관리자 기본값이 켜져 있어도 본인이 끄면 따르지 않는다
		t.Fatal(err)
	}
	*e.clock = kstClock("2026-11-12", 21, 0)
	e.emit(t, e.uid["staff"], "approval_pending", "t", "b", "/approvals/1")
	e.emit(t, e.uid["auditor"], "approval_pending", "t", "b", "/approvals/2")
	if e.deliveries(t, e.uid["staff"], "held") != 1 {
		t.Error("관리자 기본값(켜짐)을 따르는 사람은 보류")
	}
	if e.deliveries(t, e.uid["auditor"], "queued") != 1 {
		t.Error("본인이 끈 사람은 보류하지 않는다")
	}
	p, _ := e.svc.MyPrefs(as(e.staff))
	if !p.Digest.DefaultOn || !p.Digest.Effective || p.Digest.Mode != "default" {
		t.Errorf("내 화면: %+v", p.Digest)
	}
}

func TestComposeDigestLimitsLines(t *testing.T) {
	counts := map[string]int{}
	for _, k := range Kinds {
		counts[k.Kind] = 2
	}
	m, err := ComposeDigest(counts, "https://erp.example")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Count(m.Body, "\n- ")
	if lines != 20 || !strings.Contains(m.Body, fmt.Sprintf("외 %d종", len(Kinds)-20)) {
		t.Errorf("종류별 줄은 20줄까지, 나머지는 '외 N종': %d줄\n%s", lines, m.Body)
	}
	if _, err := ComposeDigest(counts, ""); err == nil {
		t.Error("앱 주소가 없으면 만들지 않는다")
	}
}
