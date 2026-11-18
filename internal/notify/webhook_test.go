package notify

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/choibongseok/procure-ledger/internal/acctsecret"
	"github.com/choibongseok/procure-ledger/internal/audit"
	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/dbtest"
)

// ---- 주소 검사 ----

func TestValidateWebhookURL(t *testing.T) {
	ok := []struct{ typ, u string }{
		{"slack", "https://hooks.slack.com/services/T000/B000/XXXX"},
		{"teams", "https://prod-12.koreacentral.logic.azure.com:443/workflows/abc/triggers/manual/paths/invoke?api-version=2016-06-01&sig=zz"},
		{"teams", "https://contoso.webhook.office.com/webhookb2/abc"},
		{"teams", "https://x.environment.api.powerplatform.com/powerautomate/automations/direct/workflows/abc"},
		{"kakaowork", "https://api.kakaowork.com/v1/messages.send"},
	}
	for _, c := range ok {
		if _, err := ValidateWebhookURL(c.u, c.typ, nil, false); err != nil {
			t.Errorf("%s %s: %v", c.typ, c.u, err)
		}
	}
	bad := []struct{ typ, u, why string }{
		{"slack", "http://hooks.slack.com/services/a", "http"},
		{"slack", "https://hooks.slack.com.evil.com/services/a", "접미 위장"},
		{"slack", "https://evilhooks.slack.com/x", "하위 이름 아님"},
		{"slack", "https://user:pw" + "@hooks.slack.com/x", "사용자 정보"},
		{"slack", "https://hooks.slack.com:8443/x", "포트"},
		{"slack", "https://127.0.0.1/x", "IP"},
		{"slack", "https://[::1]/x", "IPv6"},
		{"slack", "https://169.254.169.254/latest/meta-data", "메타데이터"},
		{"slack", "https://hooks.slack.com/x#frag", "프래그먼트"},
		{"slack", "https://hooks.slack.com/x y", "공백"},
		{"slack", "https://example.com/x", "허용 호스트 밖"},
		{"teams", "https://logic.azure.com/x", "하위 이름이 없는 도메인 자체"},
		{"kakaowork", "https://api.kakaowork.com/v1/other", "경로"},
		{"kakaowork", "https://hooks.slack.com/services/a", "다른 종류의 호스트"},
		{"slack", "", "빈 값"},
		{"slack", "ftp://hooks.slack.com/x", "스킴"},
	}
	for _, c := range bad {
		if _, err := ValidateWebhookURL(c.u, c.typ, nil, false); err == nil {
			t.Errorf("거부되어야 한다(%s): %s", c.why, c.u)
		}
	}
	// 관리자가 더한 호스트
	if _, err := ValidateWebhookURL("https://chat.corp.example/hook/1", "slack", []string{"*.corp.example"}, false); err != nil {
		t.Errorf("추가 호스트: %v", err)
	}
	// 로컬 시험 예외는 루프백 http 만
	if _, err := ValidateWebhookURL("http://127.0.0.1:9999/x", "slack", nil, true); err != nil {
		t.Errorf("로컬 예외: %v", err)
	}
	if _, err := ValidateWebhookURL("http://10.0.0.5/x", "slack", nil, true); err == nil {
		t.Error("로컬 예외는 루프백만 푼다")
	}
	if _, err := ValidateWebhookURL("http://127.0.0.1:9999/x", "slack", nil, false); err == nil {
		t.Error("예외가 꺼져 있으면 http 거부")
	}
}

func TestValidHostPattern(t *testing.T) {
	for _, h := range []string{"hooks.example.com", "*.example.com", "a-b.example.co.kr"} {
		if !ValidHostPattern(h) {
			t.Errorf("허용: %s", h)
		}
	}
	for _, h := range []string{"", "localhost", "*.com.", "10.0.0.1", "*", "a*.example.com", "http://x.com", "x.com/path", "x..com", "한글.com", "a b.com", "*.*.com"} {
		if ValidHostPattern(h) {
			t.Errorf("거부: %q", h)
		}
	}
}

func TestBlockedIP(t *testing.T) {
	blocked := []string{"127.0.0.1", "::1", "10.1.2.3", "172.16.0.1", "192.168.1.1", "169.254.169.254", "fe80::1", "fd00::1", "100.64.0.1", "0.0.0.0", "::", "224.0.0.1", "ff02::1", "::ffff:127.0.0.1", "::ffff:10.0.0.1", "192.0.2.1", "198.18.0.1", "255.255.255.255", "64:ff9b::1"}
	for _, s := range blocked {
		if !blockedIP(netip.MustParseAddr(s)) {
			t.Errorf("차단되어야 한다: %s", s)
		}
	}
	for _, s := range []string{"8.8.8.8", "52.84.1.1", "2606:4700::1111", "::ffff:8.8.8.8"} {
		if blockedIP(netip.MustParseAddr(s)) {
			t.Errorf("허용되어야 한다: %s", s)
		}
	}
}

// ---- 발송기: 연결 직전 IP 검사, 리다이렉트, 응답 분류 ----

func TestSenderBlocksLoopbackWithoutAllowLocal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("차단된 주소로 요청이 나갔다") }))
	defer srv.Close()
	s := NewHTTPSender(HTTPSenderOptions{})
	// IP 로도, 이름(localhost → 루프백으로 해석)으로도 막힌다 — 등록 때가 아니라 연결 직전에 검사하기 때문이다.
	for _, u := range []string{srv.URL, strings.Replace(srv.URL, "127.0.0.1", "localhost", 1)} {
		err := s.Send(context.Background(), WebhookTarget{Type: "slack", URL: u}, "x")
		var se *SendError
		if !errors.As(err, &se) || se.Code != CodeHookBlocked || se.Retry {
			t.Errorf("%s: %v", u, err)
		}
	}
}

func TestSenderDoesNotFollowRedirects(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("리다이렉트를 따라갔다") }))
	defer target.Close()
	redir := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer redir.Close()
	err := NewHTTPSender(HTTPSenderOptions{AllowLocal: true}).Send(context.Background(), WebhookTarget{Type: "slack", URL: redir.URL}, "x")
	var se *SendError
	if !errors.As(err, &se) || se.Retry {
		t.Fatalf("리다이렉트는 따르지 않고 영구 오류여야 한다: %v", err)
	}
}

func TestClassifyHTTP(t *testing.T) {
	cases := []struct {
		typ    string
		status int
		body   string
		ra     string
		code   string
		retry  bool
		after  time.Duration
	}{
		{"slack", 200, "ok", "", "", false, 0},
		{"teams", 202, "", "", "", false, 0},
		{"kakaowork", 200, `{"success":true}`, "", "", false, 0},
		{"kakaowork", 200, `{"success":false,"error":{"code":"bad_request"}}`, "", CodeHookRejected, false, 0},
		{"slack", 400, "invalid_payload", "", CodeHookPayload, false, 0},
		{"slack", 403, "action_prohibited", "", CodeHookAuth, false, 0},
		{"slack", 404, "no_service", "", CodeHookNoService, false, 0},
		{"slack", 410, "", "", CodeHookNoService, false, 0},
		{"teams", 401, "", "", CodeHookAuth, false, 0},
		{"teams", 429, "", "30", CodeHookRate, true, 30 * time.Second},
		{"teams", 429, "", "", CodeHookRate, true, 0},
		{"slack", 500, "", "", CodeHook5xx, true, 0},
		{"slack", 503, "", "", CodeHook5xx, true, 0},
		{"slack", 302, "", "", CodeHook4xx, false, 0},
		{"slack", 418, "", "", CodeHook4xx, false, 0},
	}
	for _, c := range cases {
		err := classifyHTTP(c.typ, c.status, []byte(c.body), c.ra)
		if c.code == "" {
			if err != nil {
				t.Errorf("%s %d: 성공이어야 한다: %v", c.typ, c.status, err)
			}
			continue
		}
		var se *SendError
		if !errors.As(err, &se) || se.Code != c.code || se.Retry != c.retry || se.After != c.after {
			t.Errorf("%s %d: %+v", c.typ, c.status, err)
		}
	}
}

// ---- 공식 문서 형식 ----

func TestPayloadFormats(t *testing.T) {
	text := "[ProcureLedger] 결재 요청 3건이 있습니다. 열기: https://erp.example/approvals?a=1&b=2"
	b, h, err := Payload(WebhookTarget{Type: "slack"}, text)
	if err != nil || h.Get("Content-Type") != "application/json" {
		t.Fatal(err)
	}
	var sl map[string]string
	_ = json.Unmarshal(b, &sl)
	if len(sl) != 1 || !strings.Contains(sl["text"], "a=1&amp;b=2") {
		t.Errorf("슬랙은 {\"text\"} 하나이고 & 를 이스케이프한다: %s", b)
	}

	b, _, _ = Payload(WebhookTarget{Type: "teams"}, text)
	var tm struct {
		Type        string `json:"type"`
		Attachments []struct {
			ContentType string `json:"contentType"`
			Content     struct {
				Schema  string `json:"$schema"`
				Type    string `json:"type"`
				Version string `json:"version"`
				Body    []struct {
					Type string `json:"type"`
					Text string `json:"text"`
					Wrap bool   `json:"wrap"`
				} `json:"body"`
			} `json:"content"`
		} `json:"attachments"`
	}
	if err := json.Unmarshal(b, &tm); err != nil || tm.Type != "message" || len(tm.Attachments) != 1 {
		t.Fatalf("팀즈 형식: %s", b)
	}
	a := tm.Attachments[0]
	if a.ContentType != "application/vnd.microsoft.card.adaptive" || a.Content.Type != "AdaptiveCard" || len(a.Content.Body) != 1 || a.Content.Body[0].Type != "TextBlock" || a.Content.Body[0].Text != text || a.Content.Schema == "" {
		t.Errorf("팀즈는 Adaptive Card 메시지여야 한다: %s", b)
	}

	b, h, err = Payload(WebhookTarget{Type: "kakaowork", Token: "app-key", Conversation: "12345"}, text)
	if err != nil || h.Get("Authorization") != "Bearer app-key" {
		t.Fatalf("카카오워크 헤더: %v %v", err, h)
	}
	var kk map[string]any
	_ = json.Unmarshal(b, &kk)
	if kk["conversation_id"] != float64(12345) || kk["text"] != text {
		t.Errorf("카카오워크 본문: %s", b)
	}
	if _, _, err := Payload(WebhookTarget{Type: "kakaowork"}, text); err == nil {
		t.Error("앱 키·대화방이 없으면 보내지 않는다")
	}
}

// ---- 서비스 흐름 ----

type hookRecv struct {
	mu     sync.Mutex
	reqs   []hookReq
	status int
	body   string
	ra     string
}

type hookReq struct {
	path, auth, ctype, body string
}

func (h *hookRecv) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		h.mu.Lock()
		h.reqs = append(h.reqs, hookReq{r.URL.Path, r.Header.Get("Authorization"), r.Header.Get("Content-Type"), string(b)})
		st, body, ra := h.status, h.body, h.ra
		h.mu.Unlock()
		if ra != "" {
			w.Header().Set("Retry-After", ra)
		}
		if st == 0 {
			st = 200
		}
		w.WriteHeader(st)
		_, _ = io.WriteString(w, body)
	}
}

func (h *hookRecv) set(status int, body, ra string) {
	h.mu.Lock()
	h.status, h.body, h.ra = status, body, ra
	h.mu.Unlock()
}

func (h *hookRecv) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.reqs)
}

func (h *hookRecv) last() hookReq {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.reqs[len(h.reqs)-1]
}

type hookEnv struct {
	*env
	recv *hookRecv
	srv  *httptest.Server
}

func newHookEnv(t *testing.T) *hookEnv {
	t.Helper()
	e := newEnv(t)
	recv := &hookRecv{}
	srv := httptest.NewServer(recv.handler())
	t.Cleanup(srv.Close)
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	c, err := acctsecret.New(key)
	if err != nil {
		t.Fatal(err)
	}
	e.svc = New(Options{DB: e.db, Txs: contract.SQLTxManager{DB: e.db}, Audit: audit.New(e.db), Mailer: e.mail, BaseURL: "https://erp.example",
		Now: func() time.Time { return *e.clock }, Sealer: c, AllowLocalWebhook: true})
	return &hookEnv{env: e, recv: recv, srv: srv}
}

func (h *hookEnv) addTarget(t *testing.T, name, typ string, kinds ...string) TargetView {
	t.Helper()
	in := TargetInput{Name: name, Type: typ, URL: h.srv.URL + "/services/T0/B0/SECRETSECRET123456", Kinds: kinds}
	if typ == "kakaowork" {
		in.Token, in.Conversation = "kakao-app-key-777", "987654"
		in.URL = h.srv.URL + "/v1/messages.send"
	}
	v, err := h.svc.CreateTarget(as(h.admin), in)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestTargetCreateHidesSecretsEverywhere(t *testing.T) {
	h := newHookEnv(t)
	v := h.addTarget(t, "결재 채널", "slack", "approval_pending")
	b, _ := json.Marshal(v)
	if strings.Contains(string(b), "SECRETSECRET") || strings.Contains(string(b), h.srv.URL) {
		t.Fatalf("응답에 주소가 있다: %s", b)
	}
	u, _ := url.Parse(h.srv.URL)
	if v.Hint != u.Host || strings.ContainsAny(v.Hint, "/…") || strings.Contains(v.Hint, "123456") {
		t.Errorf("힌트는 호스트만(경로의 어떤 부분도 없음): %q (기대 %q)", v.Hint, u.Host)
	}
	k := h.addTarget(t, "카카오", "kakaowork", "payment_due")
	// 모든 표의 모든 글자 열을 훑어 평문이 없음을 확인한다(감사 로그 포함).
	needles := []string{"SECRETSECRET", "kakao-app-key-777", "/services/T0/B0", "123456", "987654"}
	tableQuery, colQuery := `SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'`, `SELECT name FROM pragma_table_info('%s') WHERE type IN ('TEXT','')`
	if dbtest.PG() { // 두 방언 모두 표·글자 열 목록을 얻는다(PG 는 information_schema)
		tableQuery = `SELECT table_name FROM information_schema.tables WHERE table_schema = 'public' AND table_type = 'BASE TABLE'`
		colQuery = `SELECT column_name FROM information_schema.columns WHERE table_schema = 'public' AND table_name = '%s' AND data_type = 'text'`
	}
	rows, err := h.db.Query(tableQuery)
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var n string
		_ = rows.Scan(&n)
		tables = append(tables, n)
	}
	rows.Close()
	for _, tb := range tables {
		cols, err := h.db.Query(fmt.Sprintf(colQuery, tb))
		if err != nil {
			t.Fatal(err)
		}
		var cs []string
		for cols.Next() {
			var c string
			_ = cols.Scan(&c)
			cs = append(cs, c)
		}
		cols.Close()
		for _, c := range cs {
			for _, nd := range needles {
				var n int
				if err := h.db.QueryRow(fmt.Sprintf(`SELECT COUNT(*) FROM %s WHERE %s LIKE '%%' || ? || '%%'`, tb, c), nd).Scan(&n); err != nil {
					t.Fatal(err)
				}
				if n > 0 {
					t.Errorf("%s.%s 에 평문 비밀값(%s)이 있다", tb, c, nd)
				}
			}
		}
	}
	var sealed string
	_ = h.db.QueryRow(`SELECT secret_sealed FROM notification_targets WHERE id = ?`, k.ID).Scan(&sealed)
	if !acctsecret.IsSealed(sealed) {
		t.Errorf("봉인되지 않았다: %q", sealed)
	}
	// 계좌번호 칸(용도 없는 Open)으로는 풀리지 않는다.
	key := make([]byte, 32)
	c2, _ := acctsecret.New(key)
	if _, err := c2.Open(sealed); err == nil {
		t.Error("다른 키로 풀렸다")
	}
}

func TestSealPurposeIsolation(t *testing.T) {
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	c, _ := acctsecret.New(key)
	web, _ := c.SealFor("notify-target", "https://hooks.slack.com/services/x")
	if _, err := c.Open(web); err == nil {
		t.Error("웹훅 주소를 계좌번호 칸으로 풀 수 있으면 안 된다")
	}
	acct, _ := c.Seal("110-123-456789")
	if _, err := c.OpenFor("notify-target", acct); err == nil {
		t.Error("계좌번호를 웹훅 용도로 풀 수 있으면 안 된다")
	}
	if got, err := c.OpenFor("notify-target", web); err != nil || got != "https://hooks.slack.com/services/x" {
		t.Errorf("OpenFor: %q %v", got, err)
	}
	if _, err := c.OpenFor("notify-target", "평문"); err == nil {
		t.Error("봉하지 않은 값은 통과하면 안 된다")
	}
}

func TestTargetValidationAndPermissions(t *testing.T) {
	h := newHookEnv(t)
	ctx := as(h.admin)
	bad := []TargetInput{
		{Name: "", Type: "slack", URL: h.srv.URL, Kinds: nil},
		{Name: strings.Repeat("가", 41), Type: "slack", URL: h.srv.URL},
		{Name: "x", Type: "discord", URL: h.srv.URL},
		{Name: "x", Type: "slack", URL: h.srv.URL, Kinds: []string{"nope"}},
		{Name: "x", Type: "slack", URL: "http://example.com/x"},
		{Name: "x", Type: "kakaowork", URL: h.srv.URL + "/v1/messages.send"},                                  // 앱 키·대화방 없음
		{Name: "x", Type: "slack", URL: h.srv.URL + "/p", Token: "t"},                                         // 슬랙에 앱 키
		{Name: "x", Type: "kakaowork", URL: h.srv.URL + "/v1/messages.send", Token: "a b", Conversation: "1"}, // 공백
	}
	for i, in := range bad {
		if _, err := h.svc.CreateTarget(ctx, in); !errors.Is(err, contract.ErrValidation) {
			t.Errorf("%d: 검증 오류여야 한다: %v", i, err)
		}
	}
	h.addTarget(t, "중복", "slack")
	if _, err := h.svc.CreateTarget(ctx, TargetInput{Name: "중복", Type: "slack", URL: h.srv.URL + "/p"}); !errors.Is(err, contract.ErrValidation) {
		t.Errorf("이름 중복: %v", err)
	}
	// 권한: 직원·감사는 관리 기능을 못 쓴다(발송 기록 조회와 별개).
	for _, p := range []contract.Principal{h.staff, h.auditr} {
		if _, err := h.svc.CreateTarget(as(p), TargetInput{Name: "x", Type: "slack", URL: h.srv.URL}); !errors.Is(err, contract.ErrForbidden) {
			t.Errorf("%s 등록: %v", p.Name, err)
		}
		if _, err := h.svc.Targets(as(p)); !errors.Is(err, contract.ErrForbidden) {
			t.Errorf("%s 목록: %v", p.Name, err)
		}
		if _, err := h.svc.SetTargetActive(as(p), 1, false); !errors.Is(err, contract.ErrForbidden) {
			t.Errorf("%s 중지: %v", p.Name, err)
		}
	}
	// 봉인 키가 없으면 등록할 수 없다.
	e := newEnv(t)
	if _, err := e.svc.CreateTarget(as(e.admin), TargetInput{Name: "x", Type: "slack", URL: h.srv.URL}); !errors.Is(err, contract.ErrInvalidState) {
		t.Errorf("키 없음: %v", err)
	}
}

func TestWebhookMergesIntoOneLineWithoutPersonalData(t *testing.T) {
	h := newHookEnv(t)
	h.addTarget(t, "결재", "slack", "approval_pending", "approval_nudge")
	h.addTarget(t, "재무", "teams", "payment_due") // 종류가 달라 받지 않는다
	secretTitle := "극비 문서 제목 1억 2천만원 홍길동"
	for i := 0; i < 3; i++ {
		h.emit(t, h.uid["staff"], "approval_pending", "결재할 문서가 도착했습니다", secretTitle, fmt.Sprintf("/approvals/%d", 10+i))
	}
	h.emit(t, h.uid["admin"], "receipt_done", "x", secretTitle, "/docs/1") // 대상 종류 밖
	if got := h.n(t, `SELECT COUNT(*) FROM notification_deliveries WHERE target_id IS NOT NULL`); got != 3 {
		t.Fatalf("웹훅 발송 건은 받을 종류 3건뿐이어야 한다: %d", got)
	}
	h.tick(t) // 합치기 창(60초) 전에는 나가지 않는다
	if h.recv.count() != 0 {
		t.Fatal("합치기 창 안에서 나갔다")
	}
	h.advance(61 * time.Second)
	h.tick(t)
	if h.recv.count() != 1 {
		t.Fatalf("같은 대상·종류는 한 줄로 합쳐 한 번만 나가야 한다: %d", h.recv.count())
	}
	r := h.recv.last()
	var body map[string]string
	_ = json.Unmarshal([]byte(r.body), &body)
	want := "[ProcureLedger] 결재 요청 3건이 있습니다. 열기: https://erp.example/approvals"
	if body["text"] != want {
		t.Errorf("메시지 = %q, want %q", body["text"], want)
	}
	for _, bad := range []string{"극비", "1억", "홍길동", "staff", "admin", "10", "결재할 문서가 도착했습니다"} {
		if strings.Contains(r.body, bad) {
			t.Errorf("외부 메시지에 %q 가 있다: %s", bad, r.body)
		}
	}
	if r.ctype != "application/json" {
		t.Errorf("Content-Type = %q", r.ctype)
	}
	if got := h.n(t, `SELECT COUNT(*) FROM notification_deliveries WHERE target_id IS NOT NULL AND status = 'sent' AND digest_id IS NOT NULL`); got != 3 {
		t.Errorf("합친 건은 같은 묶음 번호를 갖는다: %d", got)
	}
	// 한 건이면 그 문서의 링크를 그대로 쓴다. 독촉은 합치기 창 없이 바로 나간다.
	h.emit(t, h.uid["staff"], "approval_nudge", "독촉", "본문", "/approvals/77")
	h.tick(t)
	if h.recv.count() != 2 {
		t.Fatalf("독촉은 바로 나간다: %d", h.recv.count())
	}
	_ = json.Unmarshal([]byte(h.recv.last().body), &body)
	if body["text"] != "[ProcureLedger] 결재 독촉 1건이 있습니다. 열기: https://erp.example/approvals/77" {
		t.Errorf("한 건 링크: %q", body["text"])
	}
}

func TestTeamsAndKakaoSend(t *testing.T) {
	h := newHookEnv(t)
	h.addTarget(t, "팀즈", "teams", "payment_due")
	h.addTarget(t, "카카오", "kakaowork", "payment_due")
	h.emit(t, h.uid["admin"], "payment_due", "지급 예정", "금액 5,000,000원", "/finance/payments")
	h.advance(61 * time.Second)
	h.tick(t)
	if h.recv.count() != 2 {
		t.Fatalf("두 채널로 각 한 번: %d", h.recv.count())
	}
	var sawTeams, sawKakao bool
	for _, r := range h.recv.reqs {
		switch {
		case strings.Contains(r.body, "AdaptiveCard"):
			sawTeams = true
		case r.auth == "Bearer kakao-app-key-777" && strings.Contains(r.body, `"conversation_id":987654`):
			sawKakao = true
		}
		if strings.Contains(r.body, "5,000,000") {
			t.Errorf("금액이 나갔다: %s", r.body)
		}
	}
	if !sawTeams || !sawKakao {
		t.Errorf("팀즈 %v, 카카오워크 %v", sawTeams, sawKakao)
	}
}

func (h *hookEnv) deliveryState(t *testing.T, target int64) (status, code string, attempts int, next string) {
	t.Helper()
	if err := h.db.QueryRow(`SELECT status, last_error_code, attempts, next_try_at FROM notification_deliveries WHERE target_id = ? ORDER BY id DESC LIMIT 1`, target).Scan(&status, &code, &attempts, &next); err != nil {
		t.Fatal(err)
	}
	return
}

func TestWebhookRetryAndGiveUp(t *testing.T) {
	h := newHookEnv(t)
	tg := h.addTarget(t, "결재", "slack", "approval_pending")
	h.recv.set(500, "", "")
	h.emit(t, h.uid["staff"], "approval_pending", "x", "y", "/approvals/1")
	h.advance(61 * time.Second)
	// 재시도 간격: 1분, 5분, 30분, 2시간, 6시간 — 6번째 시도(재시도 5회째)까지 실패하면 포기
	waits := []time.Duration{time.Minute, 5 * time.Minute, 30 * time.Minute, 2 * time.Hour, 6 * time.Hour}
	for i, w := range waits {
		h.tick(t)
		st, code, att, next := h.deliveryState(t, tg.ID)
		if st != StatusQueued || att != i+1 || code != CodeHook5xx {
			t.Fatalf("%d번째 실패 뒤: %s %s %d", i+1, st, code, att)
		}
		if want := ts(h.clock.Add(w)); next != want {
			t.Fatalf("%d번째 다음 시도 %s, want %s", i+1, next, want)
		}
		h.advance(w)
	}
	h.tick(t)
	if st, code, _, _ := h.deliveryState(t, tg.ID); st != StatusFailed || code != CodeRetryOver {
		t.Fatalf("재시도 초과: %s %s", st, code)
	}
	if h.recv.count() != 6 {
		t.Errorf("총 6번 시도: %d", h.recv.count())
	}
}

func TestWebhookPermanentErrorAndAutoPause(t *testing.T) {
	h := newHookEnv(t)
	tg := h.addTarget(t, "결재", "slack", "approval_pending")
	h.recv.set(404, "no_service", "")
	for i := 1; i <= 5; i++ {
		h.emit(t, h.uid["staff"], "approval_pending", "x", "y", fmt.Sprintf("/approvals/%d", i))
		h.advance(61 * time.Second)
		h.tick(t)
		st, code, att, _ := h.deliveryState(t, tg.ID)
		if st != StatusFailed || code != CodeHookNoService || att != 1 {
			t.Fatalf("%d번째: 404 는 즉시 포기여야 한다: %s %s %d", i, st, code, att)
		}
		h.advance(time.Second)
	}
	var paused string
	var active int
	_ = h.db.QueryRow(`SELECT paused_reason, active FROM notification_targets WHERE id = ?`, tg.ID).Scan(&paused, &active)
	if paused != "auto" || active != 1 {
		t.Fatalf("연속 5건 영구 오류면 자동 중지: %q %d", paused, active)
	}
	if h.n(t, `SELECT COUNT(*) FROM audit_logs WHERE action = 'notification.target_auto_pause'`) != 1 {
		t.Error("자동 중지 감사 1건")
	}
	if h.n(t, `SELECT COUNT(*) FROM notifications WHERE kind = 'notify_target_paused' AND user_id = ?`, h.uid["admin"]) != 1 {
		t.Error("관리자에게 앱 안 알림")
	}
	if h.n(t, `SELECT COUNT(*) FROM notifications WHERE kind = 'notify_target_paused' AND user_id = ?`, h.uid["staff"]) != 0 {
		t.Error("권한 없는 직원에게는 알림이 가면 안 된다")
	}
	// 중지된 대상은 새 건을 만들지 않고, 이미 큐에 있던 건은 건너뜀으로 끝난다.
	before := h.n(t, `SELECT COUNT(*) FROM notification_deliveries WHERE target_id IS NOT NULL`)
	h.emit(t, h.uid["staff"], "approval_pending", "x", "y", "/approvals/99")
	if h.n(t, `SELECT COUNT(*) FROM notification_deliveries WHERE target_id IS NOT NULL`) != before {
		t.Error("중지된 대상에는 새 발송 건을 만들지 않는다")
	}
	// 시험 발송이 성공하면 다시 켜진다(기록에는 "연결 시험"으로 남는다).
	h.recv.set(200, "ok", "")
	n0 := h.recv.count()
	v, err := h.svc.TestTarget(as(h.admin), tg.ID)
	if err != nil {
		t.Fatal(err)
	}
	if v.AutoPaused || v.State != "사용 중" || v.Failures != 0 {
		t.Errorf("재개: %+v", v)
	}
	if h.recv.count() != n0+1 {
		t.Error("시험 메시지가 나가야 한다")
	}
	var body map[string]string
	_ = json.Unmarshal([]byte(h.recv.last().body), &body)
	if body["text"] != TestText {
		t.Errorf("시험 문구: %q", body["text"])
	}
	if h.n(t, `SELECT COUNT(*) FROM notification_deliveries WHERE kind = 'connection_test' AND status = 'sent'`) != 1 {
		t.Error("시험 발송 기록")
	}
	// 같은 대상에 시험을 또 보내도 유일 색인에 걸리지 않는다. 실패한 시험은 연속 실패 횟수에 더하지 않는다.
	h.recv.set(500, "", "")
	if _, err := h.svc.TestTarget(as(h.admin), tg.ID); !errors.Is(err, contract.ErrInvalidState) {
		t.Errorf("실패한 시험: %v", err)
	}
	var fails int
	_ = h.db.QueryRow(`SELECT consecutive_failures FROM notification_targets WHERE id = ?`, tg.ID).Scan(&fails)
	if fails != 0 {
		t.Errorf("시험 실패는 연속 실패에 더하지 않는다: %d", fails)
	}
	if h.n(t, `SELECT COUNT(*) FROM notification_deliveries WHERE kind = 'connection_test'`) != 2 {
		t.Error("시험 기록 2건")
	}
}

func TestWebhookRateLimitRetryAfter(t *testing.T) {
	h := newHookEnv(t)
	tg := h.addTarget(t, "결재", "slack", "approval_pending")
	h.recv.set(429, "", "7200") // 2시간 — 상한 1시간으로 줄여 따른다
	h.emit(t, h.uid["staff"], "approval_pending", "x", "y", "/approvals/1")
	h.advance(61 * time.Second)
	h.tick(t)
	st, code, att, next := h.deliveryState(t, tg.ID)
	if st != StatusQueued || code != CodeHookRate || att != 1 || next != ts(h.clock.Add(time.Hour)) {
		t.Fatalf("429: %s %s %d %s", st, code, att, next)
	}
}

func TestWebhookAudit(t *testing.T) {
	h := newHookEnv(t)
	ctx := as(h.admin)
	tg := h.addTarget(t, "결재", "slack", "approval_pending")
	f := false
	if _, err := h.svc.UpdateTarget(ctx, tg.ID, TargetInput{Name: "결재방", Kinds: []string{"approval_pending", "approval_nudge"}, Active: &f}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.SetTargetActive(ctx, tg.ID, false); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.SetTargetActive(ctx, tg.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.SetExtraHosts(ctx, []string{"chat.corp.example", "*.corp2.example", "Chat.Corp.Example"}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.SetExtraHosts(ctx, []string{"10.0.0.1"}); !errors.Is(err, contract.ErrValidation) {
		t.Errorf("IP 허용 호스트: %v", err)
	}
	for _, a := range []string{"notification.target_save", "notification.target_pause", "notification.target_resume", "notification.hosts_set"} {
		if h.n(t, `SELECT COUNT(*) FROM audit_logs WHERE action = ?`, a) == 0 {
			t.Errorf("감사 %s 가 없다", a)
		}
	}
	// 감사에 주소가 없고 url_changed 만 있다.
	rows, _ := h.db.Query(`SELECT diff, detail FROM audit_logs WHERE action LIKE 'notification.%'`)
	defer rows.Close()
	for rows.Next() {
		var d, det string
		_ = rows.Scan(&d, &det)
		if strings.Contains(d+det, "SECRETSECRET") || strings.Contains(d+det, h.srv.URL) {
			t.Errorf("감사에 주소가 있다: %s", d)
		}
	}
	// 새 주소로 바꿔도 비밀 항목은 유지/교체가 구분되고 힌트가 갱신된다.
	v, err := h.svc.UpdateTarget(ctx, tg.ID, TargetInput{Name: "결재방", Kinds: []string{"approval_pending"}, URL: h.srv.URL + "/services/T1/B1/NEWNEWNEW999999"})
	if err != nil || strings.Contains(v.Hint, "999999") || strings.Contains(v.Hint, "/") {
		t.Errorf("주소 교체 뒤에도 힌트에는 호스트만 있어야 한다: %+v %v", v, err)
	}
	if h.n(t, `SELECT COUNT(*) FROM audit_logs WHERE action = 'notification.target_save' AND diff LIKE '%url_changed%'`) < 2 {
		t.Error("주소 변경은 url_changed 로만 남는다")
	}
}

func TestWebhookSkippedWhenNoSealerOrBaseURL(t *testing.T) {
	h := newHookEnv(t)
	tg := h.addTarget(t, "결재", "slack", "approval_pending")
	h.emit(t, h.uid["staff"], "approval_pending", "x", "y", "/approvals/1")
	h.advance(61 * time.Second)
	// 앱 주소가 없으면 건너뜀(알림 자체는 그대로).
	h.svc.o.BaseURL = ""
	h.tick(t)
	if st, code, _, _ := h.deliveryState(t, tg.ID); st != StatusSkipped || code != CodeNoBaseURL {
		t.Fatalf("앱 주소 없음: %s %s", st, code)
	}
	// 키가 없으면 건너뜀.
	h.svc.o.BaseURL = "https://erp.example"
	h.emit(t, h.uid["staff"], "approval_pending", "x", "y", "/approvals/2")
	h.advance(61 * time.Second)
	h.svc.o.Sealer = nil
	h.tick(t)
	if st, code, _, _ := h.deliveryState(t, tg.ID); st != StatusSkipped || code != CodeNoSealer {
		t.Fatalf("키 없음: %s %s", st, code)
	}
	if h.recv.count() != 0 {
		t.Error("건너뛴 건은 나가지 않는다")
	}
}

func TestWebhookRespectsAllowListAtSendTime(t *testing.T) {
	// 로컬 예외가 꺼진 서비스에서는 루프백 주소가 발송 직전에 막힌다(등록 이후 설정이 바뀐 경우).
	h := newHookEnv(t)
	tg := h.addTarget(t, "결재", "slack", "approval_pending")
	h.svc.o.AllowLocalWebhook = false
	h.svc.defaultSender = NewHTTPSender(HTTPSenderOptions{})
	h.emit(t, h.uid["staff"], "approval_pending", "x", "y", "/approvals/1")
	h.advance(61 * time.Second)
	h.tick(t)
	if st, code, _, _ := h.deliveryState(t, tg.ID); st != StatusFailed || code != CodeHookBlocked {
		t.Fatalf("차단: %s %s", st, code)
	}
	if h.recv.count() != 0 {
		t.Error("차단된 주소로 요청이 나갔다")
	}
}

func TestRegistrationRejectsInternalResolution(t *testing.T) {
	h := newHookEnv(t)
	h.svc.o.AllowLocalWebhook = false
	h.svc.o.Resolve = nil
	// 허용 호스트지만 내부 주소로 해석되는 경우를 시험용 해석기로 재현한다.
	h.svc.o.Resolve = func(ctx context.Context, host string) error {
		return valErr("웹훅 주소가 내부 주소로 연결됩니다. 내부 주소는 쓸 수 없습니다.")
	}
	_, err := h.svc.CreateTarget(as(h.admin), TargetInput{Name: "x", Type: "slack", URL: "https://hooks.slack.com/services/a/b/c"})
	if !errors.Is(err, contract.ErrValidation) || !strings.Contains(err.Error(), "내부 주소") {
		t.Fatalf("내부 주소로 해석되면 등록 거부: %v", err)
	}
}

func TestDeliveriesListShowsTargetNameWithoutSecret(t *testing.T) {
	h := newHookEnv(t)
	h.addTarget(t, "결재 채널", "slack", "approval_pending")
	h.emit(t, h.uid["staff"], "approval_pending", "x", "y", "/approvals/1")
	h.advance(61 * time.Second)
	h.tick(t)
	ds, err := h.svc.Deliveries(as(h.admin), DeliveryFilter{Channel: "slack"})
	if err != nil || len(ds) != 1 {
		t.Fatalf("%v %d", err, len(ds))
	}
	if ds[0].Recipient != "결재 채널" || ds[0].ChannelName != "슬랙" || ds[0].Status != StatusSent {
		t.Errorf("%+v", ds[0])
	}
	b, _ := json.Marshal(ds)
	if strings.Contains(string(b), "SECRETSECRET") || strings.Contains(string(b), "http") {
		t.Errorf("기록에 주소가 있다: %s", b)
	}
}

// 이전 버전이 저장한 "호스트…끝6자" 힌트도 표시 단계에서 호스트만 나간다(마이그레이션 없이 경로의 끝 부분을 가린다).
func TestLegacyTargetHintShownAsHostOnly(t *testing.T) {
	h := newHookEnv(t)
	v := h.addTarget(t, "옛 힌트", "slack", "approval_pending")
	if _, err := h.db.Exec(`UPDATE notification_targets SET hint = ? WHERE id = ?`, "hooks.example.com…SECRET", v.ID); err != nil {
		t.Fatal(err)
	}
	tl, err := h.svc.Targets(as(h.admin))
	list := tl.Targets
	if err != nil || len(list) != 1 {
		t.Fatalf("목록 %v %v", list, err)
	}
	b, _ := json.Marshal(list)
	if list[0].Hint != "hooks.example.com" || strings.Contains(string(b), "SECRET") || strings.Contains(string(b), "…") {
		t.Fatalf("저장된 옛 힌트가 그대로 나갔다: %s", b)
	}
}
