package main

// B36 대표 흐름(AC-1690)을 실제 서비스 조립(bootstrap.Build + 실제 라우트·세션·권한) 위에서 모의 SMTP·모의 웹훅 서버로 태운다.
// 상신 → 결재 요청 메일 도착(제목·금액 없음, 링크 있음) → 결재자가 메일 끄기 → 다음 상신은 메일 없음 → 웹훅 등록·시험·건수 메시지 →
// 모의 웹훅을 죽여 재시도 → 포기 → 발송 기록 → (마지막) 묶음을 켠 결재자에게 상신해도 응답이 막히지 않음(교착 회귀 시험).
// 모든 HTTP 호출에 시간 제한을 둬서 교착이 나면 멈추지 않고 실패한다.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/choibongseok/procure-ledger/internal/auth/authtest"
	"github.com/choibongseok/procure-ledger/internal/bootstrap"
	"github.com/choibongseok/procure-ledger/internal/config"
	"github.com/choibongseok/procure-ledger/internal/dbtest"
	"github.com/choibongseok/procure-ledger/internal/org"
)

type hookCatcher struct {
	mu     sync.Mutex
	bodies []string
}

func (h *hookCatcher) add(b string) {
	h.mu.Lock()
	h.bodies = append(h.bodies, b)
	h.mu.Unlock()
}

func (h *hookCatcher) all() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.bodies...)
}

func newFullEnv(t *testing.T, smtp *authtest.FakeSMTP) *platformEnv {
	t.Helper()
	ts := httptest.NewUnstartedServer(nil)
	base := "http://" + ts.Listener.Addr().String()
	dbtest.SetEnv(t) // SQLite 는 임시 파일, PG(PL_TEST_DB=pg)는 새 데이터베이스
	t.Setenv("PL_DEV_IDP", "1")
	t.Setenv("PL_LLM_PROVIDER", "mock")
	t.Setenv("PL_BASE_URL", base)
	t.Setenv("PL_ACCOUNT_SECRET_KEY", "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=")
	t.Setenv("PL_NOTIFY_INSECURE_LOCAL", "1")
	t.Setenv("PL_SMTP_HOST", smtp.Host)
	t.Setenv("PL_SMTP_PORT", smtp.Port)
	t.Setenv("PL_SMTP_FROM", "erp@pl.test")
	t.Setenv("PL_SMTP_INSECURE", "1")
	cfg := config.Load()
	d, closeDB, err := bootstrap.Build(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(closeDB)
	if _, err := d.Org.(*org.Service).SeedDev(context.Background()); err != nil { // main 이 기동 때 하는 개발용 조직 시드
		t.Fatal(err)
	}
	rt := buildRoutes(cfg, d)
	ts.Config.Handler = handlerFor(rt, d)
	ts.Start()
	t.Cleanup(ts.Close)
	return &platformEnv{t: t, ts: ts, d: d, rt: rt, base: base}
}

func (e *platformEnv) client() *http.Client {
	jar, _ := cookiejar.New(nil)
	return &http.Client{Jar: jar, Timeout: 20 * time.Second} // 교착이 나도 시험이 멈추지 않게
}

func (e *platformEnv) mut(c *http.Client, method, path string, body any) (int, string) {
	e.t.Helper()
	b := ""
	if body != nil {
		raw, _ := json.Marshal(body)
		b = string(raw)
	}
	return e.call(c, method, path, b, jsonHdr(map[string]string{"X-CSRF-Token": e.csrf(c)}))
}

func mustCode(t *testing.T, what string, got, want int, body string) {
	t.Helper()
	if got != want {
		t.Fatalf("%s: %d(기대 %d) %.300s", what, got, want, body)
	}
}

func TestNotifyChannelsEndToEndOnRealAssembly(t *testing.T) {
	// 개발용 조직 시드(org.SeedDev)가 아직 PG 에서 돌지 않는다(INSERT OR IGNORE, LastInsertId; internal/org 는 erp 소유). 조직 시드가 PG 를 지원하면 이 건너뜀을 뺀다.
	dbtest.SkipPG(t, "org.SeedDev 가 PG 미지원(internal/org 의 INSERT OR IGNORE·LastInsertId)")
	smtp := authtest.NewFakeSMTP(t)
	e := newFullEnv(t, smtp)
	ctx := context.Background()
	admin, staff, lead, fin := e.client(), e.client(), e.client(), e.client()
	for c, who := range map[*http.Client]string{admin: "admin@pl.test", staff: "staff@pl.test", lead: "lead@pl.test", fin: "finance@pl.test"} {
		e.login(c, who)
	}
	tick := func() {
		t.Helper()
		if err := e.d.Notify.Tick(ctx); err != nil {
			t.Fatalf("발송 작업: %v", err)
		}
	}
	count := func(q string, args ...any) int {
		t.Helper()
		var n int
		if err := e.d.DB.QueryRowContext(ctx, q, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	mailsTo := func(addr string) []string {
		var out []string
		for _, m := range smtp.Messages() {
			if strings.Contains(m, "To: "+addr) || strings.Contains(m, "To: <"+addr+">") {
				out = append(out, m)
			}
		}
		return out
	}
	// 합치기 창을 없애 시험이 시계를 기다리지 않게 한다(관리자 설정 API 로).
	code, body := e.mut(admin, "PUT", "/api/notify/settings", map[string]any{"values": map[string]string{"notify.window_sec": "0"}})
	mustCode(t, "합치기 창 설정", code, 204, body)

	// 마스터 데이터 준비(finance API).
	code, body = e.mut(fin, "POST", "/api/vendors", map[string]any{"name": "한빛상사", "businessNo": "123-45-67890", "accountNo": "110-123-456789", "phone": "010-1234-5678", "email": "sales@hanbit.example"})
	mustCode(t, "거래처", code, 201, body)
	code, body = e.mut(fin, "POST", "/api/items", map[string]any{"sku": "BOLT-M8", "name": "볼트 M8", "unit": "EA", "safetyStock": 0, "trackLot": false})
	mustCode(t, "품목", code, 201, body)
	var vendorID, itemID int64
	_ = e.d.DB.QueryRowContext(ctx, `SELECT id FROM vendors LIMIT 1`).Scan(&vendorID)
	_ = e.d.DB.QueryRowContext(ctx, `SELECT id FROM items LIMIT 1`).Scan(&itemID)
	var leadID int64
	_ = e.d.DB.QueryRowContext(ctx, `SELECT id FROM users WHERE email = 'lead@pl.test'`).Scan(&leadID)

	const secretTitle = "극비 볼트 구매 9,876,543원"
	submit := func(title string) int64 {
		t.Helper()
		code, body := e.mut(staff, "POST", "/api/purchase-requests", map[string]any{"vendorId": vendorID, "title": title, "reason": "알림 흐름 시험",
			"lines": []map[string]any{{"itemId": itemID, "quantity": 3, "unitPrice": 1000000}}})
		mustCode(t, "구매요청 작성", code, 201, body)
		var pr struct {
			ID int64 `json:"id"`
		}
		_ = json.Unmarshal([]byte(body), &pr)
		code, body = e.mut(staff, "POST", "/api/purchase-requests/"+strconv.FormatInt(pr.ID, 10)+"/submit", nil)
		mustCode(t, "상신", code, 200, body)
		return pr.ID
	}

	// 1) 상신 → 결재자(팀장)에게 결재 요청 메일. 제목·금액·이름 없이 종류·링크만.
	pr1 := submit(secretTitle)
	tick()
	mails := mailsTo("lead@pl.test")
	if len(mails) != 1 {
		rows, _ := e.d.DB.QueryContext(ctx, `SELECT d.id, u.email, d.channel, d.status, d.next_try_at || ' try=' || d.attempts || ' err=' || COALESCE(d.last_error_code,'') FROM notification_deliveries d JOIN users u ON u.id = d.user_id`)
		for rows != nil && rows.Next() {
			var id int64
			var a, b, c, d string
			_ = rows.Scan(&id, &a, &b, &c, &d)
			t.Logf("delivery %d %s %s %s %s", id, a, b, c, d)
		}
		t.Fatalf("결재 요청 메일 1통이 도착해야 함: %d\n%v", len(mails), smtp.Messages())
	}
	m := mails[0]
	if !strings.Contains(m, "결재할 문서가 도착했습니다") || !strings.Contains(m, e.base+"/approvals/"+strconv.FormatInt(pr1, 10)) {
		t.Errorf("메일 본문(틀·링크): %s", m)
	}
	if !strings.Contains(m, "알림 설정: "+e.base+"/notifications/settings") {
		t.Errorf("메일 끝에 알림 설정 안내가 없다: %s", m)
	}
	for _, leak := range []string{"극비", "9,876,543", "9876543", "3000000", "한빛상사", "구매직원", "staff@pl.test"} {
		if strings.Contains(m, leak) {
			t.Errorf("메일에 %q 가 있다: %s", leak, m)
		}
	}

	// 2) 결재자가 수신 설정에서 결재 요청 메일을 끈다 → 다음 상신은 메일이 없다(앱 안 알림은 그대로).
	code, body = e.mut(lead, "PUT", "/api/notifications/prefs/approval_pending", map[string]string{"mode": "off"})
	mustCode(t, "수신 설정 끄기", code, 204, body)
	unreadBefore := count(`SELECT COUNT(*) FROM notifications WHERE user_id = ? AND kind = 'approval_pending'`, leadID)
	submit("두 번째 구매")
	tick()
	if got := len(mailsTo("lead@pl.test")); got != 1 {
		t.Errorf("메일을 끈 뒤에는 새 메일이 없어야 함: %d통", got)
	}
	if got := count(`SELECT COUNT(*) FROM notifications WHERE user_id = ? AND kind = 'approval_pending'`, leadID); got != unreadBefore+1 {
		t.Errorf("앱 안 알림은 그대로 만들어져야 함: %d → %d", unreadBefore, got)
	}
	if code, body := e.call(lead, "GET", "/api/notifications/prefs", "", nil); code != 200 || !strings.Contains(body, `"mode":"off"`) {
		t.Errorf("내 수신 설정: %d %.200s", code, body)
	}
	if code, _ := e.call(staff, "GET", "/api/notify/defaults", "", nil); code != 403 {
		t.Errorf("직원이 관리자 기본값을 봄: %d", code)
	}

	// 3) 웹훅 대상 등록·시험 발송·건수 메시지. 방에는 건수·종류·링크뿐이다.
	hook := &hookCatcher{}
	status := 200
	var smu sync.Mutex
	hsrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		hook.add(string(b))
		smu.Lock()
		st := status
		smu.Unlock()
		w.WriteHeader(st)
		_, _ = io.WriteString(w, "ok")
	}))
	hookURL := hsrv.URL + "/services/T0/B0/SUPERSECRETTOKEN"
	code, body = e.mut(admin, "POST", "/api/notify/targets", map[string]any{"name": "결재 채널", "type": "slack", "url": hookURL, "kinds": []string{"approval_pending"}})
	mustCode(t, "웹훅 대상 등록", code, 201, body)
	if strings.Contains(body, "SUPERSECRETTOKEN") || strings.Contains(body, hsrv.URL) {
		t.Errorf("등록 응답에 주소가 있다: %s", body)
	}
	var tg struct {
		ID int64 `json:"id"`
	}
	_ = json.Unmarshal([]byte(body), &tg)
	code, body = e.mut(admin, "POST", "/api/notify/targets/"+strconv.FormatInt(tg.ID, 10)+"/test", nil)
	mustCode(t, "시험 발송", code, 200, body)
	if got := hook.all(); len(got) != 1 || !strings.Contains(got[0], "연결 시험입니다") {
		t.Fatalf("시험 메시지: %v", got)
	}
	pr3 := submit("세 번째 구매")
	tick()
	got := hook.all()
	if len(got) != 2 {
		t.Fatalf("웹훅으로 건수 메시지 1건이 나가야 함: %v", got)
	}
	want := "[ProcureLedger] 결재 요청 1건이 있습니다. 열기: " + e.base + "/approvals/" + strconv.FormatInt(pr3, 10)
	if !strings.Contains(got[1], want) {
		t.Errorf("웹훅 메시지 = %s, want 포함 %s", got[1], want)
	}
	for _, leak := range []string{"세 번째", "극비", "한빛상사", "3000000", "구매직원"} {
		if strings.Contains(got[1], leak) {
			t.Errorf("웹훅에 %q 가 있다: %s", leak, got[1])
		}
	}

	// 4) 모의 웹훅을 죽이면 재시도하고(1분·5분·30분·2시간·6시간 백오프를 시계 대신 다음 시도 시각을 당겨 진행), 5번 재시도 뒤 포기한다.
	hsrv.Close()
	submit("네 번째 구매")
	for i := 0; i < 8; i++ {
		tick()
		if count(`SELECT COUNT(*) FROM notification_deliveries WHERE target_id = ? AND status = 'failed'`, tg.ID) > 0 {
			break
		}
		if _, err := e.d.DB.ExecContext(ctx, `UPDATE notification_deliveries SET next_try_at = '2000-01-01T00:00:00Z' WHERE target_id = ? AND status = 'queued'`, tg.ID); err != nil {
			t.Fatal(err)
		}
	}
	var st, codeStr string
	var attempts int
	if err := e.d.DB.QueryRowContext(ctx, `SELECT status, last_error_code, attempts FROM notification_deliveries WHERE target_id = ? AND kind = 'approval_pending' ORDER BY id DESC LIMIT 1`, tg.ID).Scan(&st, &codeStr, &attempts); err != nil {
		t.Fatal(err)
	}
	if st != "failed" || codeStr != "retry_exceeded" || attempts != 6 {
		t.Fatalf("5번 재시도 뒤 포기: %s %s %d회", st, codeStr, attempts)
	}

	// 5) 발송 기록(관리자): 실패 사유가 한국어 문장으로 보이고 주소·본문·메일 주소는 없다.
	code, body = e.call(admin, "GET", "/api/notify/deliveries?channel=slack", "", nil)
	mustCode(t, "발송 기록", code, 200, body)
	for _, want := range []string{"재시도 횟수를 넘겼습니다", `"canResend":true`, "결재 채널", "연결 시험"} {
		if !strings.Contains(body, want) {
			t.Errorf("발송 기록에 %q 가 없다: %.400s", want, body)
		}
	}
	for _, leak := range []string{"SUPERSECRETTOKEN", hsrv.URL, "http://", "lead@pl.test", "극비", "retry_exceeded"} {
		if strings.Contains(body, leak) {
			t.Errorf("발송 기록에 %q 가 있다", leak)
		}
	}
	// 감사: 등록·시험이 남고 주소는 없다.
	for _, a := range []string{"notification.target_save", "notification.target_test", "notification.settings_set"} {
		if e.auditCount(a, "success") == 0 && e.auditCount(a, "error") == 0 {
			t.Errorf("감사 %s 가 없다", a)
		}
	}
	var diffs string
	_ = e.d.DB.QueryRowContext(ctx, `SELECT COALESCE(group_concat(diff || detail, ' '), '') FROM audit_logs WHERE action LIKE 'notification.%'`).Scan(&diffs)
	if strings.Contains(diffs, "SUPERSECRETTOKEN") || strings.Contains(diffs, hsrv.URL) {
		t.Error("감사에 웹훅 주소가 있다")
	}

	// 6) 교착 회귀(H1): 묶음 받기를 켠 결재자에게 상신해도 응답이 막히지 않는다. 업무 시간 판정이 업무 쓰기 트랜잭션 안에서 따로 연결을 열면 영원히 기다린다.
	code, body = e.mut(lead, "PUT", "/api/notifications/prefs/approval_pending", map[string]string{"mode": "default"})
	mustCode(t, "수신 설정 되돌리기", code, 204, body)
	code, body = e.mut(lead, "PUT", "/api/notifications/digest", map[string]string{"mode": "on"})
	mustCode(t, "묶음 켜기", code, 204, body)
	before := count(`SELECT COUNT(*) FROM notification_deliveries WHERE user_id = ? AND channel = 'email'`, leadID)
	start := time.Now()
	submit("다섯 번째 구매")
	if time.Since(start) > 15*time.Second {
		t.Errorf("상신 응답이 너무 오래 걸림: %v", time.Since(start))
	}
	if after := count(`SELECT COUNT(*) FROM notification_deliveries WHERE user_id = ? AND channel = 'email'`, leadID); after != before+1 {
		t.Errorf("묶음을 켠 결재자의 메일 발송 건이 1건 생겨야 함(업무 시간이면 대기, 아니면 보류): %d → %d", before, after)
	}
	if n := count(`SELECT COUNT(*) FROM notification_deliveries WHERE user_id = ? AND status IN ('queued','held')`, leadID); n != 1 {
		t.Errorf("대기 또는 보류 1건: %d", n)
	}
}
