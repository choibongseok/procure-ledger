package notify

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/choibongseok/procure-ledger/internal/contract"
)

// B36 후속: 웹훅 대상 종류 termspace(슬랙처럼 {"text"} 를 받는 수신 웹훅, 주소 https://<호스트>/hooks/tsh_<43자>).

const tsToken = "tsh_abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQ" // "tsh_" + 43자(32바이트 base64url 무패딩)

func TestValidateTermspaceURL(t *testing.T) {
	good := tsToken
	if len(strings.TrimPrefix(good, "tsh_")) != 43 {
		t.Fatal("시험 토큰이 43자가 아니다")
	}
	for _, u := range []string{"https://chat.termspace.io/hooks/" + good, "https://termspace.io/hooks/" + good, "https://a.b.termspace.io:443/hooks/" + good,
		"https://x-Y_z.termspace.io/hooks/tsh_" + strings.Repeat("-_aZ09", 7) + "A"} {
		if _, err := ValidateWebhookURL(u, "termspace", nil, false); err != nil {
			t.Errorf("받아야 한다 %s: %v", u, err)
		}
	}
	bad := []struct{ u, why string }{
		{"https://chat.termspace.io/hooks/" + good[:len(good)-1], "토큰 42자"},
		{"https://chat.termspace.io/hooks/" + good + "A", "토큰 44자"},
		{"https://chat.termspace.io/hooks/tsx_" + strings.TrimPrefix(good, "tsh_"), "접두사"},
		{"https://chat.termspace.io/hooks/" + strings.TrimPrefix(good, "tsh_"), "접두사 없음"},
		{"https://chat.termspace.io/hook/" + good, "경로 이름"},
		{"https://chat.termspace.io/x/hooks/" + good, "앞에 경로가 더 있음"},
		{"https://chat.termspace.io/hooks/" + good + "/", "뒤 슬래시"},
		{"https://chat.termspace.io/hooks/" + good + "/extra", "뒤 경로"},
		{"https://chat.termspace.io/hooks/" + good + "?x=1", "쿼리"},
		{"https://chat.termspace.io/hooks/tsh_" + strings.Repeat("a", 42) + "+", "base64url 밖 문자"},
		{"https://chat.termspace.io/hooks/tsh_" + strings.Repeat("a", 42) + "=", "패딩"},
		{"https://chat.termspace.io/", "경로 없음"},
		{"http://chat.termspace.io/hooks/" + good, "http"},
		{"https://chat.termspace.io.evil.com/hooks/" + good, "접미 위장"},
		{"https://evil-termspace.io/hooks/" + good, "다른 도메인"},
		{"https://hooks.slack.com/hooks/" + good, "다른 종류의 호스트"},
		{"https://chat.termspace.io:8443/hooks/" + good, "포트"},
		{"https://127.0.0.1/hooks/" + good, "IP"},
		{"https://user:pw" + "@chat.termspace.io/hooks/" + good, "사용자 정보"},
	}
	for _, c := range bad {
		if _, err := ValidateWebhookURL(c.u, "termspace", nil, false); err == nil {
			t.Errorf("거부되어야 한다(%s): %s", c.why, c.u)
		}
	}
	// 온프렘 호스트는 관리자가 더한 허용 호스트로만 받고, 경로 모양 검사는 그대로다.
	if _, err := ValidateWebhookURL("https://chat.corp.example/hooks/"+good, "termspace", []string{"chat.corp.example"}, false); err != nil {
		t.Errorf("추가 호스트: %v", err)
	}
	if _, err := ValidateWebhookURL("https://chat.corp.example/hooks/short", "termspace", []string{"chat.corp.example"}, false); err == nil {
		t.Error("추가 호스트여도 경로 모양은 검사한다")
	}
	if _, err := ValidateWebhookURL("https://chat.corp.example/hooks/"+good, "termspace", nil, false); err == nil {
		t.Error("온프렘 호스트는 추가 허용 없이는 거부")
	}
	// 로컬 시험 예외에서도 경로 모양은 검사한다.
	if _, err := ValidateWebhookURL("http://127.0.0.1:9999/hooks/"+good, "termspace", nil, true); err != nil {
		t.Errorf("로컬 예외: %v", err)
	}
	if _, err := ValidateWebhookURL("http://127.0.0.1:9999/services/x", "termspace", nil, true); err == nil {
		t.Error("로컬 예외에서도 termspace 경로 모양을 검사해야 한다")
	}
	if !AllowedHost("termspace", "termspace.io", nil) || !AllowedHost("termspace", "a.termspace.io", nil) || AllowedHost("slack", "a.termspace.io", nil) {
		t.Error("기본 허용 호스트는 termspace 종류에만 termspace.io 와 하위 이름이다")
	}
	if hosts := BuiltinHosts()["termspace"]; len(hosts) != 2 {
		t.Errorf("화면용 기본 허용 호스트: %v", hosts)
	}
}

func TestTermspacePayloadIsPlainText(t *testing.T) {
	text := "[ProcureLedger] 결재 요청 <3>건 & 독촉 1건. 열기: https://erp.example/approvals?a=1&b=2"
	b, h, err := Payload(WebhookTarget{Type: "termspace"}, text)
	if err != nil || h.Get("Content-Type") != "application/json" || h.Get("Authorization") != "" {
		t.Fatalf("헤더: %v %v", err, h)
	}
	var m map[string]string
	if err := json.Unmarshal(b, &m); err != nil || len(m) != 1 || m["text"] != text {
		t.Fatalf("본문은 {\"text\"} 하나이고 이스케이프가 없어야 한다: %s", b)
	}
	if strings.Contains(m["text"], "&amp;") || strings.Contains(m["text"], "&lt;") {
		t.Errorf("슬랙식 이스케이프가 들어감: %q", m["text"])
	}
	if ChannelName("termspace") != "termspace" || !validTargetType("termspace") {
		t.Error("종류 이름·유효 종류")
	}
}

func TestTermspaceTextTruncationKeepsLink(t *testing.T) {
	link := "https://erp.example/approvals?a=1&b=2"
	// 한도 이하는 그대로.
	exact := strings.Repeat("가", termspaceMaxText-len([]rune(" "+link))) + " " + link
	if utf8.RuneCountInString(exact) != termspaceMaxText || termspaceText(exact) != exact {
		t.Fatal("정확히 4000자는 그대로여야 한다")
	}
	// 넘으면 본문을 자르고 링크는 통째로 남긴다(한글은 룬 단위로 자른다).
	long := strings.Repeat("긴문장", 3000) + " 열기: " + link
	got := termspaceText(long)
	if utf8.RuneCountInString(got) > termspaceMaxText || !utf8.ValidString(got) {
		t.Fatalf("4000자 이하의 올바른 UTF-8 이어야 한다: %d", utf8.RuneCountInString(got))
	}
	if !strings.HasSuffix(got, " "+link) || !strings.Contains(got, "…") {
		t.Errorf("링크가 끝에 온전히 남고 생략 표시가 있어야 한다: …%s", got[len(got)-80:])
	}
	// 링크가 없으면 단순히 자르고 생략 표시.
	plain := termspaceText(strings.Repeat("a", 5000))
	if utf8.RuneCountInString(plain) != termspaceMaxText || !strings.HasSuffix(plain, "…") {
		t.Errorf("링크 없는 긴 글: %d", utf8.RuneCountInString(plain))
	}
	// 링크 자체가 비정상적으로 길면 링크도 자르지 않고 앞에서 단순히 자른다(4000자 상한이 우선).
	huge := "열기: https://erp.example/" + strings.Repeat("x", 4500)
	if utf8.RuneCountInString(termspaceText(huge)) != termspaceMaxText {
		t.Error("긴 링크도 4000자를 넘으면 안 된다")
	}
	// Payload 도 같은 규칙으로 보낸다.
	b, _, _ := Payload(WebhookTarget{Type: "termspace"}, long)
	var m map[string]string
	_ = json.Unmarshal(b, &m)
	if utf8.RuneCountInString(m["text"]) > termspaceMaxText || !strings.HasSuffix(m["text"], link) {
		t.Errorf("Payload 의 text: %d자", utf8.RuneCountInString(m["text"]))
	}
}

// ---- 서비스 흐름(모의 웹훅 서버) ----

func (h *hookEnv) addTermspace(t *testing.T, name string, kinds ...string) TargetView {
	t.Helper()
	v, err := h.svc.CreateTarget(as(h.admin), TargetInput{Name: name, Type: "termspace", URL: h.srv.URL + "/hooks/" + tsToken, Kinds: kinds})
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestTermspaceSendRetryAndAutoPause(t *testing.T) {
	h := newHookEnv(t)
	tg := h.addTermspace(t, "termspace 방", "approval_pending")
	if tg.Type != "termspace" || strings.Contains(fmt.Sprintf("%+v", tg), "SECRET") || strings.Contains(fmt.Sprintf("%+v", tg), tsToken) {
		t.Fatalf("대상 표시: %+v", tg)
	}
	// 정상 발송: 형식(경로·본문), 이스케이프 없음, 건수+링크만.
	h.emit(t, h.uid["staff"], "approval_pending", "제목은 나가면 안 됨", "본문", "/approvals?a=1&b=2")
	h.advance(61 * time.Second)
	h.tick(t)
	if h.recv.count() != 1 {
		t.Fatalf("한 번 발송: %d", h.recv.count())
	}
	r := h.recv.last()
	if r.path != "/hooks/"+tsToken || r.ctype != "application/json" || r.auth != "" {
		t.Fatalf("요청 형식: %+v", r)
	}
	var m map[string]string
	if err := json.Unmarshal([]byte(r.body), &m); err != nil || len(m) != 1 {
		t.Fatalf("본문: %s", r.body)
	}
	if !strings.Contains(m["text"], "https://erp.example/approvals?a=1&b=2") || strings.Contains(m["text"], "&amp;") || strings.Contains(m["text"], "제목은 나가면 안 됨") {
		t.Errorf("이스케이프 없이 링크가 온전하고 제목은 없어야 한다: %q", m["text"])
	}
	if st, _, _, _ := h.deliveryState(t, tg.ID); st != StatusSent {
		t.Fatalf("상태: %s", st)
	}
	// 재시도: 5xx 는 대기열로 돌아가 1분 뒤 다시, 429 의 Retry-After 는 상한 1시간 안에서 따른다.
	h.recv.set(500, "", "")
	h.emit(t, h.uid["staff"], "approval_pending", "x", "y", "/approvals/2")
	h.advance(61 * time.Second)
	h.tick(t)
	st, code, att, next := h.deliveryState(t, tg.ID)
	if st != StatusQueued || code != CodeHook5xx || att != 1 || next != ts(h.clock.Add(time.Minute)) {
		t.Fatalf("5xx: %s %s %d %s", st, code, att, next)
	}
	h.recv.set(200, "", "")
	h.advance(time.Minute)
	h.tick(t)
	if st, _, att, _ := h.deliveryState(t, tg.ID); st != StatusSent || att != 1 { // 시도 횟수는 실패만 센다
		t.Fatalf("재시도 성공: %s %d", st, att)
	}
	// 영구 오류(404 폐기)가 연속 5건이면 자동 중지된다.
	h.recv.set(404, "", "")
	for i := 0; i < 5; i++ {
		h.emit(t, h.uid["staff"], "approval_pending", "x", "y", fmt.Sprintf("/approvals/%d", 10+i))
		h.advance(61 * time.Second)
		h.tick(t)
		if st, code, att, _ := h.deliveryState(t, tg.ID); st != StatusFailed || code != CodeHookNoService || att != 1 {
			t.Fatalf("%d번째: %s %s %d", i+1, st, code, att)
		}
		h.advance(time.Second)
	}
	var paused string
	_ = h.db.QueryRow(`SELECT paused_reason FROM notification_targets WHERE id = ?`, tg.ID).Scan(&paused)
	if paused != "auto" {
		t.Fatalf("자동 중지: %q", paused)
	}
	// 시험 보내기가 성공하면 다시 켜지고 시험 문구가 {"text"} 로 나간다.
	h.recv.set(200, "ok", "")
	if v, err := h.svc.TestTarget(as(h.admin), tg.ID); err != nil || v.AutoPaused {
		t.Fatalf("재개: %+v %v", v, err)
	}
	_ = json.Unmarshal([]byte(h.recv.last().body), &m)
	if m["text"] != TestText {
		t.Errorf("시험 문구: %q", m["text"])
	}
}

func TestTermspaceRegistrationRejectsBadPath(t *testing.T) {
	h := newHookEnv(t)
	for _, u := range []string{h.srv.URL + "/hooks/tsh_short", h.srv.URL + "/services/T0/B0/SECRET", h.srv.URL + "/hooks/" + tsToken + "?x=1", h.srv.URL + "/hooks/tsx_" + strings.TrimPrefix(tsToken, "tsh_")} {
		_, err := h.svc.CreateTarget(as(h.admin), TargetInput{Name: "나쁜 주소", Type: "termspace", URL: u, Kinds: []string{"approval_pending"}})
		if !errors.Is(err, contract.ErrValidation) {
			t.Errorf("거부되어야 한다 %s: %v", u, err)
		}
	}
	var n int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM notification_targets WHERE name = '나쁜 주소'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("거부된 등록이 저장됨: %d %v", n, err)
	}
	// 앱 키·대화방은 카카오워크 전용이다.
	if _, err := h.svc.CreateTarget(as(h.admin), TargetInput{Name: "키", Type: "termspace", URL: h.srv.URL + "/hooks/" + tsToken, Token: "k"}); !errors.Is(err, contract.ErrValidation) {
		t.Errorf("termspace 에 앱 키: %v", err)
	}
}
