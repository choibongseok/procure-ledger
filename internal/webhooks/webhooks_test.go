package webhooks_test

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/choibongseok/procure-ledger/internal/acctsecret"
	"github.com/choibongseok/procure-ledger/internal/audit"
	"github.com/choibongseok/procure-ledger/internal/changes"
	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/dbtest"
	"github.com/choibongseok/procure-ledger/internal/dbutil"
	"github.com/choibongseok/procure-ledger/internal/keyrotate"
	"github.com/choibongseok/procure-ledger/internal/netsafe"
	"github.com/choibongseok/procure-ledger/internal/webhooks"
	"github.com/choibongseok/procure-ledger/migrations"
)

// ---- 모의 수신 서버 ----

type rec struct {
	hdr  http.Header
	body []byte
}

type recv struct {
	mu     sync.Mutex
	reqs   []rec
	status func(n int, r rec) int // n: 이 서버가 받은 순번(1부터)
}

func (r *recv) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		b, _ := io.ReadAll(req.Body)
		r.mu.Lock()
		cur := rec{hdr: req.Header.Clone(), body: b}
		r.reqs = append(r.reqs, cur)
		n := len(r.reqs)
		st := r.status
		r.mu.Unlock()
		code := 200
		if st != nil {
			code = st(n, cur)
		}
		w.WriteHeader(code)
	})
}

func (r *recv) all() []rec {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]rec(nil), r.reqs...)
}

// ---- 환경 ----

type env struct {
	t     *testing.T
	db    *sql.DB
	txs   contract.TxManager
	svc   *webhooks.Service
	recv  *recv
	srv   *httptest.Server
	clock *time.Time
	admin contract.Principal
}

type opt func(*webhooks.Options)

func newEnv(t *testing.T, opts ...opt) *env {
	t.Helper()
	ctx := context.Background()
	db := dbtest.Open(t)
	if err := migrations.Apply(ctx, db); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 13, 1, 0, 0, 0, time.UTC)
	e := &env{t: t, db: db, clock: &now, recv: &recv{}}
	e.txs = dbutil.NewTxManager(db, dbutil.DialectOf(db))
	e.srv = httptest.NewServer(e.recv.handler())
	t.Cleanup(e.srv.Close)
	var uid int64
	if _, err := db.Exec(`INSERT INTO departments (name) VALUES ('운영')`); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`INSERT INTO users (email, name, role, department_id) VALUES ('a@x.test','관리자','admin',(SELECT id FROM departments WHERE name='운영')) RETURNING id`).Scan(&uid); err != nil {
		t.Fatal(err)
	}
	e.admin = contract.Principal{UserID: uid, Role: contract.RoleAdmin, Channel: contract.ChannelWeb, Auth: contract.AuthSession}
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	c, err := acctsecret.New(key)
	if err != nil {
		t.Fatal(err)
	}
	o := webhooks.Options{DB: db, Txs: e.txs, Audit: audit.New(db), Sealer: c, ManagePerm: contract.PermNotificationManage, BaseURL: "https://erp.example", AllowLocal: true,
		Now: func() time.Time { return *e.clock }}
	for _, f := range opts {
		f(&o)
	}
	e.svc = webhooks.New(o)
	return e
}

func (e *env) ctx() context.Context { return contract.WithPrincipal(context.Background(), e.admin) }

func (e *env) sub(name string, in func(*webhooks.CreateInput)) webhooks.Created {
	e.t.Helper()
	ci := webhooks.CreateInput{Name: name, URL: e.srv.URL + "/hook", Events: []string{"journal.status_changed", "journal.created", "item.updated", "purchase_order.created"}}
	if in != nil {
		in(&ci)
	}
	c, err := e.svc.Create(e.ctx(), ci)
	if err != nil {
		e.t.Fatal(err)
	}
	return c
}

func (e *env) note(res string, id int64, op, status string) {
	e.t.Helper()
	e.noteAs(context.Background(), res, id, op, status)
}

func (e *env) noteAs(ctx context.Context, res string, id int64, op, status string) {
	e.t.Helper()
	if err := e.txs.WithTx(ctx, func(tx contract.Tx) error { return changes.Note(ctx, tx, res, id, op, status) }); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) advance(d time.Duration) { *e.clock = e.clock.Add(d) }

// tick 은 코알레싱 시간(2초)을 넘긴 뒤 한 주기를 돈다.
func (e *env) tick() int {
	e.t.Helper()
	e.advance(3 * time.Second)
	if err := e.svc.Scan(context.Background()); err != nil {
		e.t.Fatal(err)
	}
	e.advance(3 * time.Second)
	n, err := e.svc.Dispatch(context.Background())
	if err != nil {
		e.t.Fatal(err)
	}
	return n
}

func (e *env) q1(q string, args ...any) string {
	e.t.Helper()
	var v sql.NullString
	if err := e.db.QueryRow(q, args...).Scan(&v); err != nil {
		e.t.Fatal(err)
	}
	return v.String
}

func (e *env) count(q string, args ...any) int {
	n, _ := strconv.Atoi(e.q1(q, args...))
	return n
}

// ---- 시험 ----

func TestSignedValueFreeRequest(t *testing.T) {
	e := newEnv(t)
	c := e.sub("회계 연동", nil)
	if !strings.HasPrefix(c.Secret, "whsec_") {
		t.Fatalf("비밀은 등록 응답에 한 번 보인다: %q", c.Secret)
	}
	e.note(changes.ResJournal, 42, changes.OpStatusChanged, "approved")
	e.note(changes.ResVendor, 9, changes.OpUpdated, "") // 구독하지 않은 종류
	if n := e.tick(); n != 1 {
		t.Fatalf("구독한 이벤트 하나만 나간다: %d", n)
	}
	rs := e.recv.all()
	if len(rs) != 1 {
		t.Fatalf("요청 %d건", len(rs))
	}
	r := rs[0]
	ts, _ := strconv.ParseInt(r.hdr.Get("X-PL-Timestamp"), 10, 64)
	want := "v1=" + webhooks.Sign(c.Secret, ts, r.body)
	if got := r.hdr.Get("X-PL-Signature"); got != want {
		t.Fatalf("서명이 다르다: %s != %s", got, want)
	}
	if r.hdr.Get("X-PL-Event-Type") != "journal.status_changed" || r.hdr.Get("X-PL-Event-Id") == "" || r.hdr.Get("X-PL-Delivery") == "" || r.hdr.Get("Content-Type") != "application/json" {
		t.Fatalf("헤더: %v", r.hdr)
	}
	var m map[string]any
	if err := json.Unmarshal(r.body, &m); err != nil {
		t.Fatal(err)
	}
	allowed := map[string]bool{"id": true, "type": true, "resource": true, "object_id": true, "op": true, "status": true, "occurred_at": true, "api_url": true, "test": true}
	for k := range m {
		if !allowed[k] {
			t.Errorf("본문에 값 칸이 있다: %s", k)
		}
	}
	if m["resource"] != "journal" || m["object_id"].(float64) != 42 || m["status"] != "approved" || m["api_url"] != "https://erp.example/api/v1/journals?id=42" {
		t.Fatalf("본문: %s", r.body)
	}
	if st := e.q1(`SELECT status FROM webhook_deliveries`); st != "sent" {
		t.Fatalf("상태 %s", st)
	}
	// 같은 주기를 또 돌아도 다시 나가지 않는다.
	if n := e.tick(); n != 0 || len(e.recv.all()) != 1 {
		t.Fatalf("중복 발송: %d", n)
	}
}

func TestSecretsNeverStoredOrShownInPlain(t *testing.T) {
	e := newEnv(t)
	c := e.sub("비밀", nil)
	sealed := e.q1(`SELECT secret_sealed FROM webhook_subscriptions`)
	if strings.Contains(sealed, c.Secret) || strings.Contains(sealed, e.srv.URL) {
		t.Fatal("봉인 칸에 평문이 있다")
	}
	vs, err := e.svc.List(e.ctx())
	if err != nil || len(vs) != 1 {
		t.Fatal(err, vs)
	}
	raw, _ := json.Marshal(vs)
	if strings.Contains(string(raw), c.Secret) || strings.Contains(string(raw), e.srv.URL+"/hook") {
		t.Fatalf("목록에 주소·비밀이 있다: %s", raw)
	}
	var diff string
	rows, err := e.db.Query(`SELECT diff FROM audit_logs WHERE action LIKE 'webhook.%'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		_ = rows.Scan(&diff)
		if strings.Contains(diff, c.Secret) || strings.Contains(diff, e.srv.URL) {
			t.Fatalf("감사에 평문이 있다: %s", diff)
		}
	}
}

func TestRetryBackoffThenSuccess(t *testing.T) {
	e := newEnv(t)
	e.recv.status = func(n int, _ rec) int {
		if n <= 2 {
			return 503
		}
		return 200
	}
	e.sub("재시도", nil)
	e.note(changes.ResJournal, 1, changes.OpCreated, "draft")
	e.tick() // 1차 실패 → 1분 뒤
	if got := e.q1(`SELECT status || ':' || attempts || ':' || last_code FROM webhook_deliveries`); got != "queued:1:hook_5xx" {
		t.Fatalf("1차 뒤: %s", got)
	}
	if n, _ := e.svc.Tick(context.Background()); n != 0 {
		t.Fatalf("1분이 지나기 전에는 다시 보내지 않는다: %d", n)
	}
	e.advance(61 * time.Second)
	e.svc.Tick(context.Background()) // 2차 실패 → 5분 뒤
	next := e.q1(`SELECT next_try_at FROM webhook_deliveries`)
	if want := e.clock.Add(5 * time.Minute).Format("2006-01-02T15:04:05Z"); next != want {
		t.Fatalf("2차 뒤 다음 시각 %s, 기대 %s", next, want)
	}
	e.advance(5*time.Minute + time.Second)
	e.svc.Tick(context.Background())
	if got := e.q1(`SELECT status || ':' || attempts FROM webhook_deliveries`); got != "sent:3" {
		t.Fatalf("3차 성공: %s", got)
	}
	if e.count(`SELECT consecutive_failures FROM webhook_subscriptions`) != 0 {
		t.Fatal("성공하면 연속 실패가 지워진다")
	}
}

func TestRetryExhaustedGivesUpAfterEightRetries(t *testing.T) {
	e := newEnv(t)
	e.recv.status = func(int, rec) int { return 500 }
	e.sub("포기", nil)
	e.note(changes.ResJournal, 1, changes.OpCreated, "draft")
	e.tick()
	for i := 0; i < 20 && e.q1(`SELECT status FROM webhook_deliveries`) == "queued"; i++ {
		e.advance(25 * time.Hour)
		e.svc.Tick(context.Background())
	}
	if got := e.q1(`SELECT status || ':' || attempts || ':' || last_code FROM webhook_deliveries`); got != "failed:9:retry_over" {
		t.Fatalf("첫 시도 + 재시도 8번 = 9번: %s", got)
	}
	if len(e.recv.all()) != 9 {
		t.Fatalf("요청 %d건", len(e.recv.all()))
	}
	if e.count(`SELECT consecutive_failures FROM webhook_subscriptions`) != 1 {
		t.Fatal("최종 실패 한 건이 연속 실패에 더해진다")
	}
}

func TestPerObjectOrderingOthersNotBlocked(t *testing.T) {
	e := newEnv(t, func(o *webhooks.Options) { o.Concurrency = 1 }) // 도착 순서로 응답을 가르는 시험이라 한 번에 하나씩
	e.recv.status = func(n int, r rec) int {
		if n == 1 { // 첫 요청(전표 1 생성)만 실패
			return 500
		}
		return 200
	}
	e.sub("순서", nil)
	e.note(changes.ResJournal, 1, changes.OpCreated, "draft")
	e.note(changes.ResJournal, 2, changes.OpCreated, "draft")
	e.tick()
	e.note(changes.ResJournal, 1, changes.OpStatusChanged, "requested") // 합치기 창 밖의 뒤 이벤트
	e.tick()
	rs := e.recv.all()
	if len(rs) != 2 {
		t.Fatalf("전표 1 의 첫 건과 전표 2 만 나간다(전표 1 의 둘째는 기다린다): %d건", len(rs))
	}
	var types []string
	for _, r := range rs {
		var m struct{ ID, ObjectID int64 }
		_ = json.Unmarshal(r.body, &struct {
			ID       *int64 `json:"id"`
			ObjectID *int64 `json:"object_id"`
		}{&m.ID, &m.ObjectID})
		types = append(types, strconv.FormatInt(m.ObjectID, 10)+":"+r.hdr.Get("X-PL-Event-Type"))
	}
	if types[0] != "1:journal.created" || types[1] != "2:journal.created" {
		t.Fatalf("순서: %v", types)
	}
	if got := e.q1(`SELECT status FROM webhook_deliveries WHERE event_type = 'journal.status_changed'`); got != "queued" {
		t.Fatalf("뒤 건은 대기: %s", got)
	}
	// 앞 건이 재시도로 나가야 뒤 건이 나간다.
	e.advance(61 * time.Second)
	e.svc.Tick(context.Background())
	e.advance(time.Second)
	e.svc.Tick(context.Background())
	rs = e.recv.all()
	var order []string
	for _, r := range rs {
		order = append(order, r.hdr.Get("X-PL-Event-Id"))
	}
	if len(rs) != 4 {
		t.Fatalf("4건이어야 한다: %v", order)
	}
	// 전표 1 의 번호 순서: 생성(앞) → 상태 변경(뒤)
	idx := map[string]int{}
	for i, r := range rs {
		if r.hdr.Get("X-PL-Event-Type") == "journal.status_changed" {
			idx["status"] = i
		}
		if r.hdr.Get("X-PL-Event-Type") == "journal.created" && strings.Contains(string(r.body), `"object_id":1`) {
			idx["created1"] = i // 마지막(성공한) 전송
		}
	}
	if idx["created1"] > idx["status"] {
		t.Fatalf("앞 건이 성공하기 전에 뒤 건이 나갔다: %v", idx)
	}
}

func TestAutoPauseAfterFiveConsecutiveFailures(t *testing.T) {
	e := newEnv(t)
	e.recv.status = func(int, rec) int { return 410 } // 즉시 포기(재시도 없음)
	c := e.sub("자동중지", nil)
	for i := int64(1); i <= 6; i++ {
		e.note(changes.ResJournal, i, changes.OpCreated, "draft")
	}
	e.tick()
	vs, _ := e.svc.List(e.ctx())
	if vs[0].PausedReason != "auto" || vs[0].State != "자동 중지(연속 실패)" || vs[0].Failures < 5 {
		t.Fatalf("5건 연속 실패면 자동 중지: %+v", vs[0])
	}
	if got := len(e.recv.all()); got != 6 && got != 5 {
		t.Fatalf("요청 %d건", got)
	}
	before := len(e.recv.all())
	e.note(changes.ResJournal, 99, changes.OpCreated, "draft")
	e.tick()
	if len(e.recv.all()) != before {
		t.Fatal("중지 중에는 보내지 않는다")
	}
	if e.count(`SELECT COUNT(*) FROM audit_logs WHERE action = 'webhook.sub_auto_pause'`) != 1 {
		t.Fatal("자동 중지는 감사에 남는다")
	}
	e.recv.status = nil
	if _, err := e.svc.Resume(e.ctx(), c.View.ID); err != nil {
		t.Fatal(err)
	}
	e.tick()
	if len(e.recv.all()) <= before {
		t.Fatal("재개하면 따라잡아 보낸다")
	}
	if e.count(`SELECT consecutive_failures FROM webhook_subscriptions`) != 0 {
		t.Fatal("재개하면 연속 실패가 지워진다")
	}
}

func TestSuccessResetsConsecutiveFailures(t *testing.T) {
	e := newEnv(t, func(o *webhooks.Options) { o.Concurrency = 1 }) // 도착 순서로 응답을 가르는 시험이라 한 번에 하나씩
	e.recv.status = func(n int, _ rec) int {
		if n == 5 {
			return 200
		}
		return 410
	}
	e.sub("끊김", nil)
	for i := int64(1); i <= 6; i++ {
		e.note(changes.ResJournal, i, changes.OpCreated, "draft")
	}
	e.tick()
	vs, _ := e.svc.List(e.ctx())
	if vs[0].PausedReason != "" || vs[0].Failures != 1 {
		t.Fatalf("성공 한 번이 낀 실패는 이어지지 않는다: %+v", vs[0])
	}
}

func TestSSRFRefusals(t *testing.T) {
	// 등록 때: 내부 주소로 해석되는 호스트·IP·http 는 거부한다(허용 목록이 비어 있어도 공인 IP 만).
	pub := func(context.Context, string) error { return nil }
	priv := func(context.Context, string) error {
		return errors.New(contract.ErrValidation.Error() + ": 내부 주소")
	}
	e := newEnv(t, func(o *webhooks.Options) { o.AllowLocal = false; o.Resolve = priv })
	for _, u := range []string{"https://internal.example.com/h", "https://127.0.0.1/h", "http://example.com/h", "https://10.0.0.5/h", "https://[::1]/h", "https://169.254.169.254/latest"} {
		if _, err := e.svc.Create(e.ctx(), webhooks.CreateInput{Name: "x", URL: u, Events: []string{"item.updated"}}); err == nil {
			t.Errorf("거부되어야 한다: %s", u)
		}
	}
	e2 := newEnv(t, func(o *webhooks.Options) { o.AllowLocal = false; o.Resolve = pub })
	if _, err := e2.svc.Create(e2.ctx(), webhooks.CreateInput{Name: "공인", URL: "https://hooks.example.com/h", Events: []string{"item.updated"}}); err != nil {
		t.Fatalf("허용 목록이 비어 있으면 공인 호스트는 받는다(D5): %v", err)
	}
	// 연결 직전: 등록 검사를 통과한 구독이어도 실제 연결 주소가 루프백이면 서버에 닿지 않는다.
	e3 := newEnv(t, func(o *webhooks.Options) { o.Client = netsafe.NewClient(netsafe.ClientOptions{}) })
	e3.sub("연결검사", nil)
	e3.note(changes.ResItem, 1, changes.OpUpdated, "")
	e3.tick()
	if len(e3.recv.all()) != 0 {
		t.Fatal("차단된 주소로 요청이 나갔다")
	}
	if got := e3.q1(`SELECT status || ':' || last_code FROM webhook_deliveries`); !strings.HasPrefix(got, "failed:hook_blocked") {
		t.Fatalf("연결 직전 검사: %s", got)
	}
}

func TestAllowListStrictMode(t *testing.T) {
	e := newEnv(t, func(o *webhooks.Options) {
		o.AllowLocal = false
		o.Resolve = func(context.Context, string) error { return nil }
	})
	if _, err := e.db.Exec(`UPDATE app_settings SET value = '*.corp.example' WHERE key = 'webhook.allow_hosts'`); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Create(e.ctx(), webhooks.CreateInput{Name: "밖", URL: "https://other.example.com/h", Events: []string{"item.updated"}}); err == nil || !strings.Contains(err.Error(), "허용되지 않은 호스트") {
		t.Fatalf("목록 밖은 거부: %v", err)
	}
	if _, err := e.svc.Create(e.ctx(), webhooks.CreateInput{Name: "안", URL: "https://hook.corp.example/h", Events: []string{"item.updated"}}); err != nil {
		t.Fatalf("목록 안은 허용: %v", err)
	}
}

func TestAPIChannelChangesExcludedByDefault(t *testing.T) {
	e := newEnv(t)
	e.sub("기본", nil)
	e.sub("API 포함", func(c *webhooks.CreateInput) { c.IncludeAPI = true })
	apiCtx := contract.WithPrincipal(context.Background(), contract.Principal{UserID: e.admin.UserID, Channel: contract.ChannelAPI, Auth: contract.AuthAPI})
	e.noteAs(apiCtx, changes.ResJournal, 5, changes.OpCreated, "draft")
	e.note(changes.ResJournal, 6, changes.OpCreated, "draft")
	e.tick()
	if got := e.count(`SELECT COUNT(*) FROM webhook_deliveries d JOIN webhook_subscriptions s ON s.id = d.subscription_id WHERE s.name = '기본'`); got != 1 {
		t.Fatalf("기본 구독은 API 가 만든 변경을 받지 않는다: %d", got)
	}
	if got := e.count(`SELECT COUNT(*) FROM webhook_deliveries d JOIN webhook_subscriptions s ON s.id = d.subscription_id WHERE s.name = 'API 포함'`); got != 2 {
		t.Fatalf("포함 구독은 둘 다: %d", got)
	}
}

func TestStatusFilterAndNewSubscriptionStartsAtHead(t *testing.T) {
	e := newEnv(t)
	e.note(changes.ResJournal, 1, changes.OpCreated, "draft") // 구독 전의 변경은 받지 않는다
	e.sub("승인만", func(c *webhooks.CreateInput) { c.Events = []string{"journal.status_changed:approved"} })
	e.note(changes.ResJournal, 2, changes.OpStatusChanged, "requested")
	e.note(changes.ResJournal, 2, changes.OpStatusChanged, "approved")
	e.tick()
	rs := e.recv.all()
	if len(rs) != 1 || !strings.Contains(string(rs[0].body), `"status":"approved"`) {
		t.Fatalf("승인 상태 변경만: %d건", len(rs))
	}
}

func TestSecretRotationDualSignatureFor24Hours(t *testing.T) {
	e := newEnv(t)
	c := e.sub("교체", nil)
	secret2, err := e.svc.RotateSecret(e.ctx(), c.View.ID)
	if err != nil || secret2 == c.Secret {
		t.Fatal(err)
	}
	e.note(changes.ResItem, 1, changes.OpUpdated, "")
	e.tick()
	sig := e.recv.all()[0].hdr.Get("X-PL-Signature")
	parts := strings.Split(sig, ",")
	if len(parts) != 2 {
		t.Fatalf("24시간 안에는 새·옛 서명이 함께 온다: %s", sig)
	}
	r := e.recv.all()[0]
	ts, _ := strconv.ParseInt(r.hdr.Get("X-PL-Timestamp"), 10, 64)
	if parts[0] != "v1="+webhooks.Sign(secret2, ts, r.body) || parts[1] != "v1="+webhooks.Sign(c.Secret, ts, r.body) {
		t.Fatal("서명 값이 다르다")
	}
	e.advance(25 * time.Hour)
	e.note(changes.ResItem, 2, changes.OpUpdated, "")
	e.tick()
	if got := e.recv.all()[1].hdr.Get("X-PL-Signature"); strings.Contains(got, ",") {
		t.Fatalf("24시간이 지나면 옛 서명은 빠진다: %s", got)
	}
}

func TestTransitionTableAndTriggerAgree(t *testing.T) {
	e := newEnv(t)
	e.sub("전이", nil)
	e.note(changes.ResItem, 1, changes.OpUpdated, "")
	e.tick()
	states := []string{"queued", "sending", "sent", "failed", "skipped"}
	for _, from := range states {
		for _, to := range states {
			if from == to {
				continue
			}
			if _, err := e.db.Exec(`UPDATE webhook_deliveries SET status = ?`, from); err != nil && from != "sent" {
				// 시작 상태로 만들 수 없는 건(표에 없는 전이)은 건너뛴다. 아래에서 표 밖 전이 거부를 따로 본다.
				continue
			}
			_, err := e.db.Exec(`UPDATE webhook_deliveries SET status = ?`, to)
			if (err == nil) != webhooks.CanTransition(from, to) && e.q1(`SELECT status FROM webhook_deliveries`) == from {
				t.Errorf("표와 트리거가 다르다: %s → %s (오류 %v)", from, to, err)
			}
		}
	}
	// 보낸 건은 되돌릴 수 없다.
	if _, err := e.db.Exec(`UPDATE webhook_deliveries SET status = 'sent'`); err != nil {
		t.Log(err)
	}
}

func TestManagementRequiresPermission(t *testing.T) {
	e := newEnv(t)
	staff := contract.WithPrincipal(context.Background(), contract.Principal{UserID: e.admin.UserID, Role: contract.RoleStaff, Channel: contract.ChannelWeb, Auth: contract.AuthSession})
	if _, err := e.svc.Create(staff, webhooks.CreateInput{Name: "x", URL: e.srv.URL + "/h", Events: []string{"item.updated"}}); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("권한 없는 호출: %v", err)
	}
	if _, err := e.svc.List(staff); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("목록도 같다: %v", err)
	}
	unwired := webhooks.New(webhooks.Options{DB: e.db, Txs: e.txs, Audit: audit.New(e.db)})
	if _, err := unwired.List(e.ctx()); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("권한이 연결되지 않으면 모두 거부: %v", err)
	}
}

func TestPauseResumeAndResendAndTestSend(t *testing.T) {
	e := newEnv(t)
	c := e.sub("관리", nil)
	if _, err := e.svc.Pause(e.ctx(), c.View.ID); err != nil {
		t.Fatal(err)
	}
	e.note(changes.ResItem, 1, changes.OpUpdated, "")
	e.tick()
	if len(e.recv.all()) != 0 {
		t.Fatal("중지 중에는 보내지 않는다")
	}
	if _, err := e.svc.Resume(e.ctx(), c.View.ID); err != nil {
		t.Fatal(err)
	}
	e.tick()
	if len(e.recv.all()) != 1 {
		t.Fatal("재개하면 중지 중의 변경도 따라잡는다")
	}
	if msg, err := e.svc.SendTest(e.ctx(), c.View.ID); err != nil || msg != "" {
		t.Fatalf("시험 보내기: %q %v", msg, err)
	}
	if last := e.recv.all()[1]; !strings.Contains(string(last.body), `"test":true`) {
		t.Fatalf("시험 이벤트 표시: %s", last.body)
	}
	if e.count(`SELECT COUNT(*) FROM webhook_deliveries`) != 1 {
		t.Fatal("시험은 발송 건을 만들지 않는다")
	}
	// 실패한 건만 다시 보낼 수 있다.
	ds, _ := e.svc.Deliveries(e.ctx(), c.View.ID, "", 10)
	if err := e.svc.Resend(e.ctx(), ds[0].ID); !errors.Is(err, contract.ErrInvalidState) {
		t.Fatalf("전달된 건은 다시 보낼 수 없다: %v", err)
	}
	e.recv.status = func(int, rec) int { return 410 }
	e.note(changes.ResItem, 2, changes.OpUpdated, "")
	e.tick()
	ds, _ = e.svc.Deliveries(e.ctx(), c.View.ID, "failed", 10)
	if len(ds) != 1 || ds[0].Reason == "" || ds[0].State != "실패(포기)" {
		t.Fatalf("실패 기록: %+v", ds)
	}
	e.recv.status = nil
	if err := e.svc.Resend(e.ctx(), ds[0].ID); err != nil {
		t.Fatal(err)
	}
	e.tick()
	if got := e.q1(`SELECT status FROM webhook_deliveries WHERE id = ?`, ds[0].ID); got != "sent" {
		t.Fatalf("다시 보내기 뒤: %s", got)
	}
}

func TestConcurrentTicksNeverDoubleSend(t *testing.T) {
	e := newEnv(t)
	e.sub("동시", nil)
	for i := int64(1); i <= 8; i++ {
		e.note(changes.ResItem, i, changes.OpUpdated, "")
	}
	e.advance(3 * time.Second)
	if err := e.svc.Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	e.advance(3 * time.Second)
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = e.svc.Dispatch(context.Background())
		}()
	}
	wg.Wait()
	seen := map[string]int{}
	for _, r := range e.recv.all() {
		seen[r.hdr.Get("X-PL-Event-Id")]++
	}
	for id, n := range seen {
		if n != 1 {
			t.Errorf("이벤트 %s 가 %d번 나갔다", id, n)
		}
	}
	if len(seen) != 8 {
		t.Fatalf("8건이 모두 나가야 한다: %d", len(seen))
	}
}

func TestCoalescingMergesConsecutiveChangesOfSameObject(t *testing.T) {
	e := newEnv(t)
	e.sub("합치기", nil)
	e.note(changes.ResJournal, 1, changes.OpCreated, "draft")
	e.note(changes.ResJournal, 1, changes.OpStatusChanged, "requested")
	e.note(changes.ResJournal, 1, changes.OpStatusChanged, "approved")
	e.note(changes.ResJournal, 2, changes.OpCreated, "draft") // 다른 객체는 합쳐지지 않는다
	e.tick()
	rs := e.recv.all()
	if len(rs) != 2 {
		t.Fatalf("전표 1 은 마지막 한 건, 전표 2 는 한 건: %d건", len(rs))
	}
	var last rec
	for _, r := range rs {
		if strings.Contains(string(r.body), `"object_id":1`) {
			last = r
		}
	}
	if !strings.Contains(string(last.body), `"status":"approved"`) || last.hdr.Get("X-PL-Coalesced") != "2" {
		t.Fatalf("마지막 상태와 합친 수 2: %s %v", last.body, last.hdr)
	}
	for _, r := range rs {
		if strings.Contains(string(r.body), `"object_id":2`) && r.hdr.Get("X-PL-Coalesced") != "" {
			t.Fatal("합치지 않은 건에는 헤더가 없다")
		}
	}
	if got := e.count(`SELECT COUNT(*) FROM webhook_deliveries WHERE status = 'skipped' AND last_code = 'coalesced'`); got != 2 {
		t.Fatalf("합쳐진 앞 건은 건너뜀으로 남는다: %d", got)
	}
	// 창이 지난 뒤의 변경은 새 건이다.
	e.note(changes.ResJournal, 1, changes.OpCreated, "draft")
	e.tick()
	rs = e.recv.all()
	if len(rs) != 3 || rs[2].hdr.Get("X-PL-Coalesced") != "" {
		t.Fatalf("창 밖은 별개: %d건", len(rs))
	}
}

func TestPerMinuteLimitDefersWithoutFailing(t *testing.T) {
	e := newEnv(t)
	if _, err := e.db.Exec(`UPDATE app_settings SET value = '3' WHERE key = 'webhook.per_min'`); err != nil {
		t.Fatal(err)
	}
	e.sub("속도", nil)
	e.sub("다른 구독", nil) // 한 구독의 상한이 다른 구독을 막지 않는다
	for i := int64(1); i <= 7; i++ {
		e.note(changes.ResItem, i, changes.OpUpdated, "")
	}
	e.tick()
	if got := len(e.recv.all()); got != 6 {
		t.Fatalf("구독마다 3건씩 6건: %d", got)
	}
	if e.count(`SELECT COUNT(*) FROM webhook_deliveries WHERE status = 'queued'`) != 8 {
		t.Fatal("나머지 8건은 대기")
	}
	if e.count(`SELECT SUM(consecutive_failures) FROM webhook_subscriptions`) != 0 || e.count(`SELECT COALESCE(SUM(attempts),0) FROM webhook_deliveries WHERE status = 'queued'`) != 0 {
		t.Fatal("미룬 건은 실패·시도로 세지 않는다")
	}
	e.advance(61 * time.Second)
	if _, err := e.svc.Dispatch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := len(e.recv.all()); got != 12 {
		t.Fatalf("1분 뒤 다음 3건씩: %d", got)
	}
}

func TestPurgeKeepsUnsentAndFeedFloor(t *testing.T) {
	e := newEnv(t)
	c := e.sub("정리", nil)
	e.note(changes.ResItem, 1, changes.OpUpdated, "")
	e.tick() // sent
	e.recv.status = func(int, rec) int { return 410 }
	e.note(changes.ResItem, 2, changes.OpUpdated, "")
	e.tick() // failed
	e.recv.status = func(int, rec) int { return 200 }
	e.advance(400 * 24 * time.Hour) // 앞의 두 건(전달됨·실패)은 보존 기간(365일)을 넘겼다
	if _, err := e.svc.Pause(e.ctx(), c.View.ID); err != nil {
		t.Fatal(err)
	}
	e.note(changes.ResItem, 3, changes.OpUpdated, "")
	if _, err := e.svc.Resume(e.ctx(), c.View.ID); err != nil {
		t.Fatal(err)
	}
	e.advance(3 * time.Second)
	if err := e.svc.Scan(context.Background()); err != nil { // queued, 아직 안 보냄
		t.Fatal(err)
	}
	if n, err := e.svc.Purge(context.Background()); err != nil || n != 1 {
		t.Fatalf("오래된 전달됨 한 건만 지운다: %d %v", n, err)
	}
	if e.count(`SELECT COUNT(*) FROM webhook_deliveries`) != 2 || e.count(`SELECT COUNT(*) FROM webhook_deliveries WHERE status = 'failed'`) != 1 || e.count(`SELECT COUNT(*) FROM webhook_deliveries WHERE status = 'queued'`) != 1 {
		t.Fatal("실패·대기는 보존")
	}
	e.note(changes.ResItem, 4, changes.OpUpdated, "") // 구독이 아직 읽지 않은 줄
	floor, ok, err := webhooks.FeedKeepFloor(context.Background(), e.db)
	head, _ := changes.Head(context.Background(), e.db)
	if err != nil || !ok || floor == 0 || floor >= head {
		t.Fatalf("읽지 않은 줄(경계 이후)은 지우면 안 된다: 경계 %d head %d %v %v", floor, head, ok, err)
	}
}

func TestAutoPauseNotifiesManagersSameTx(t *testing.T) {
	var delivered []string
	e := newEnv(t, func(o *webhooks.Options) {
		o.Concurrency = 1
		o.Deliver = func(_ context.Context, _ contract.Tx, nid, uid int64, kind string, _ time.Time) error {
			delivered = append(delivered, kind)
			return nil
		}
	})
	if _, err := e.db.Exec(`INSERT INTO user_effective_permissions (user_id, permission, role_id, grant_id) VALUES (?, 'notification:manage', 1, 1)`, e.admin.UserID); err != nil {
		t.Fatal(err)
	}
	e.recv.status = func(int, rec) int { return 410 }
	e.sub("알림", nil)
	for i := int64(1); i <= 5; i++ {
		e.note(changes.ResJournal, i, changes.OpCreated, "draft")
	}
	e.tick()
	if e.count(`SELECT COUNT(*) FROM notifications WHERE kind = 'webhook_paused' AND user_id = ?`, e.admin.UserID) != 1 {
		t.Fatal("자동 중지 알림이 한 건 만들어져야 한다")
	}
	body := e.q1(`SELECT title || body FROM notifications WHERE kind = 'webhook_paused'`)
	if strings.Contains(body, e.srv.URL) || !strings.Contains(body, "알림") {
		t.Fatalf("본문에는 구독 이름만(주소 없음): %s", body)
	}
	if len(delivered) != 1 || delivered[0] != "webhook_paused" {
		t.Fatalf("발송 건 연결: %v", delivered)
	}
	if e.count(`SELECT enabled FROM notification_defaults WHERE kind = 'webhook_paused' AND channel = 'email'`) != 1 {
		t.Fatal("메일 기본값은 켜짐")
	}
}

// 계좌 키를 바꿔(rotate-key) 다시 봉해도 구독의 주소·서명 비밀이 그대로 풀려 발송과 서명 검증이 된다(옛 비밀 병행 서명 포함).
func TestKeyRotationResealsSubscriptionsAndSendingContinues(t *testing.T) {
	oldKey, newKey := make([]byte, 32), make([]byte, 32)
	_, _ = rand.Read(oldKey)
	_, _ = rand.Read(newKey)
	oldC, _ := acctsecret.New(oldKey)
	newC, _ := acctsecret.New(newKey)
	e := newEnv(t, func(o *webhooks.Options) { o.Sealer = oldC })
	c := e.sub("키 교체", nil)
	secret2, err := e.svc.RotateSecret(e.ctx(), c.View.ID) // 옛 비밀 병행 중인 상태로 교체한다
	if err != nil {
		t.Fatal(err)
	}
	if _, err := keyrotate.RotateAccount(context.Background(), e.db, oldKey, newKey); err != nil {
		t.Fatal(err)
	}
	// 옛 키로는 더 못 풀고 새 키 봉인기를 쓰는 서비스는 보낼 수 있다.
	old := webhooks.New(webhooks.Options{DB: e.db, Txs: e.txs, Audit: audit.New(e.db), Sealer: oldC, ManagePerm: contract.PermNotificationManage, BaseURL: "https://erp.example", AllowLocal: true, Now: func() time.Time { return *e.clock }})
	if msg, err := old.SendTest(e.ctx(), c.View.ID); err != nil || msg == "" || len(e.recv.all()) != 0 {
		t.Fatalf("옛 키로는 풀리면 안 된다: %q %v", msg, err)
	}
	e.note(changes.ResItem, 1, changes.OpUpdated, "")
	e.advance(3 * time.Second)
	fresh := webhooks.New(webhooks.Options{DB: e.db, Txs: e.txs, Audit: audit.New(e.db), Sealer: newC, ManagePerm: contract.PermNotificationManage, BaseURL: "https://erp.example", AllowLocal: true, Now: func() time.Time { return *e.clock }})
	if err := fresh.Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	e.advance(3 * time.Second)
	if _, err := fresh.Dispatch(context.Background()); err != nil {
		t.Fatal(err)
	}
	rs := e.recv.all()
	if len(rs) != 1 {
		t.Fatalf("새 키로 발송: %d건", len(rs))
	}
	r := rs[0]
	ts, _ := strconv.ParseInt(r.hdr.Get("X-PL-Timestamp"), 10, 64)
	want := "v1=" + webhooks.Sign(secret2, ts, r.body) + ",v1=" + webhooks.Sign(c.Secret, ts, r.body)
	if got := r.hdr.Get("X-PL-Signature"); got != want {
		t.Fatalf("재봉인 뒤에도 같은 비밀로 서명: %s", got)
	}
}

// B44 12절: 한 법인에 묶인 구독은 그 법인의 변경만 받고, 전체(0) 구독은 모두 받는다.
func TestSubscriptionEntityFiltersEvents(t *testing.T) {
	e := newEnv(t)
	if _, err := e.db.Exec(`INSERT INTO entities (id, code, name, is_default, active, created_at, updated_at) VALUES (2, 'ABC', '계열사', 0, 1, 't', 't')`); err != nil {
		t.Fatal(err)
	}
	e.sub("전체", func(c *webhooks.CreateInput) { c.EntityID = 0 })
	e.sub("법인2", func(c *webhooks.CreateInput) { c.EntityID = 2 })
	ctx2 := contract.WithActiveEntity(context.Background(), contract.ActiveEntity{ID: 2})
	e.noteAs(ctx2, changes.ResJournal, 5, changes.OpCreated, "draft")
	e.note(changes.ResJournal, 6, changes.OpCreated, "draft")
	e.tick()
	cnt := func(name string) int {
		return e.count(`SELECT COUNT(*) FROM webhook_deliveries d JOIN webhook_subscriptions s ON s.id = d.subscription_id WHERE s.name = ?`, name)
	}
	if cnt("전체") != 2 || cnt("법인2") != 1 {
		t.Fatalf("전체 %d(2), 법인2 %d(1)", cnt("전체"), cnt("법인2"))
	}
}
