package notify

import (
	"strings"
	"testing"
)

// B36 판정 M1: notify.window_sec=0 은 "합치지 않음"(바로 보낸다)이라는 유효한 값이다. 음수·숫자가 아닌 값은 기본값(60초)이다.
func TestWindowSecZeroMeansNoCoalescing(t *testing.T) {
	e := newEnv(t)
	staff := e.uid["staff"]
	set := func(v string) {
		t.Helper()
		if _, err := e.db.Exec(`INSERT INTO app_settings(key, value) VALUES ('notify.window_sec', ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, v); err != nil {
			t.Fatal(err)
		}
	}
	set("0")
	id := e.emit(t, staff, "approval_pending", "t", "", "/approvals/1")
	if st, _, _, next := e.status(t, id); st != StatusQueued || next != ts(*e.clock) {
		t.Fatalf("0 이면 바로 받을 때가 된다: %s %s(지금 %s)", st, next, ts(*e.clock))
	}
	e.tick(t)
	if e.mail.count() != 1 {
		t.Fatalf("창 0 이면 시간이 지나지 않아도 첫 틱에 나간다: %d통", e.mail.count())
	}
	for _, bad := range []string{"-5", "abc", ""} {
		set(bad)
		nid := e.emit(t, staff, "approval_pending", "t", "", "/approvals/2")
		if _, _, _, next := e.status(t, nid); next != ts(e.clock.Add(60*1e9)) {
			t.Errorf("설정 %q 는 기본 60초여야 함: %s", bad, next)
		}
	}
	set("120")
	nid := e.emit(t, staff, "approval_pending", "t", "", "/approvals/3")
	if _, _, _, next := e.status(t, nid); next != ts(e.clock.Add(120*1e9)) {
		t.Errorf("120초 설정: %s", next)
	}
}

// 다른 설정은 0 이하를 여전히 기본값으로 본다(최소값 1).
func TestIntSettingMinimums(t *testing.T) {
	e := newEnv(t)
	ctx := t.Context()
	put := func(k, v string) {
		t.Helper()
		if _, err := e.db.Exec(`INSERT INTO app_settings(key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, k, v); err != nil {
			t.Fatal(err)
		}
	}
	put("x.a", "0")
	if got := intSetting(ctx, e.db, "x.a", 7); got != 7 {
		t.Errorf("intSetting 은 0 을 기본값으로 본다: %d", got)
	}
	if got := intSettingMin(ctx, e.db, "x.a", 7, 0); got != 0 {
		t.Errorf("최소 0 이면 0 이 유효: %d", got)
	}
	put("x.a", "-1")
	if got := intSettingMin(ctx, e.db, "x.a", 7, 0); got != 7 {
		t.Errorf("음수는 기본값: %d", got)
	}
}

// B36 판정 M2(명세 4.2): 메일 본문 끝에 "알림 설정: <앱 링크>" 안내가 있다. 일반 메일과 요약 메일 모두, 링크는 수신 설정 화면이고 메일 안에서 상태를 바꾸는 링크는 없다.
func TestMailFooterPointsToNotificationSettings(t *testing.T) {
	const want = "알림 설정: https://erp.example/notifications/settings"
	msg, err := Compose("approval_pending", 3, "https://erp.example", "/approvals")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(strings.TrimRight(msg.Body, "\n"), want) {
		t.Errorf("일반 메일 끝에 안내가 없다:\n%s", msg.Body)
	}
	if !strings.Contains(msg.Body, "열기: https://erp.example/approvals") {
		t.Errorf("앱 링크가 없다:\n%s", msg.Body)
	}
	dg, err := ComposeDigest(map[string]int{"approval_pending": 2}, "https://erp.example")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(strings.TrimRight(dg.Body, "\n"), want) {
		t.Errorf("요약 메일 끝에 안내가 없다:\n%s", dg.Body)
	}
	for _, b := range []string{msg.Body, dg.Body} {
		if strings.Contains(strings.ToLower(b), "unsubscribe") || strings.Count(b, "https://") != 2 {
			t.Errorf("링크는 열기·알림 설정 두 개뿐이어야 한다:\n%s", b)
		}
	}
	// 실제 발송 경로에서도 같은 본문이 나간다.
	e := newEnv(t)
	e.emit(t, e.uid["staff"], "approval_nudge", "t", "", "/approvals/1")
	e.tick(t)
	if e.mail.count() != 1 || !strings.Contains(e.mail.sent[0].body, want) {
		t.Fatalf("발송된 메일에 안내가 없다: %+v", e.mail.sent)
	}
}
