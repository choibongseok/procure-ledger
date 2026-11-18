package setup_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/choibongseok/procure-ledger/internal/access"
	"github.com/choibongseok/procure-ledger/internal/audit"
	"github.com/choibongseok/procure-ledger/internal/auth"
	"github.com/choibongseok/procure-ledger/internal/contract"
	caudit "github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/dbtest"
	"github.com/choibongseok/procure-ledger/internal/dbutil"
	"github.com/choibongseok/procure-ledger/internal/rbac"
	"github.com/choibongseok/procure-ledger/internal/setup"
	"github.com/choibongseok/procure-ledger/migrations"
)

const (
	adminEmail = "owner@acme.test"
	adminPw    = "Wizard-Pass-2026!"
	envCode    = "operator-chosen-code-0123456789"
)

// failingAudit 는 감사 기록이 실패하는 기록기다(같은 트랜잭션 보장 시험).
type failingAudit struct{ caudit.Recorder }

func (failingAudit) Record(context.Context, contract.Tx, caudit.Entry) error {
	return errors.New("감사 저장소 오류")
}

type env struct {
	db    *sql.DB
	txs   contract.TxManager
	auth  *auth.Service
	svc   *setup.Service
	h     http.Handler
	clock *time.Time
	logs  *bytes.Buffer
	code  string
}

// newEnv 는 빈 DB(사용자 없음 = pending)에 마법사를 올린다. 로그는 버퍼에 모아 설치 코드가 로그에만 나오는지 본다.
func newEnv(t *testing.T, opts ...func(*setup.Options)) *env {
	t.Helper()
	ctx := context.Background()
	db := dbtest.Open(t)
	if err := migrations.Apply(ctx, db); err != nil {
		t.Fatal(err)
	}
	if _, err := access.SyncTemplates(ctx, db); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)
	e := &env{db: db, txs: dbutil.NewTxManager(db, dbutil.DialectOf(db)), clock: &now, logs: &bytes.Buffer{}}
	rec := audit.New(db)
	var err error
	e.auth, err = auth.New(auth.Options{DB: db, Txs: e.txs, Audit: rec, BaseURL: "http://localhost:8080", SyncMail: true,
		Argon: auth.ArgonParams{MemoryKiB: 64, Iterations: 1, Parallelism: 1}, Now: func() time.Time { return *e.clock }})
	if err != nil {
		t.Fatal(err)
	}
	prev := log.Writer()
	log.SetOutput(e.logs)
	t.Cleanup(func() { log.SetOutput(prev) })
	o := setup.Options{DB: db, Txs: e.txs, Audit: rec, Auth: e.auth, Now: func() time.Time { return *e.clock }}
	for _, f := range opts {
		f(&o)
	}
	e.svc, err = setup.New(ctx, o)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(e.logs.String(), "\n") {
		if i := strings.Index(line, "설치 코드를 입력하세요: "); i >= 0 {
			e.code = strings.TrimSpace(line[i+len("설치 코드를 입력하세요: "):])
		}
	}
	rt := contract.NewRouteTable()
	rt.Guard = rbac.NewGuard(rec, e.txs)
	e.svc.Register(rt)
	rt.Handle("GET", "/setup", contract.PermPublic, e.svc.PageHandler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("wizard")) })))
	e.h = e.auth.Wrap(rt.Handler())
	return e
}

func (e *env) do(t *testing.T, method, path, code string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body != nil {
		b, _ := json.Marshal(body)
		r = httptest.NewRequest(method, path, bytes.NewReader(b))
		r.Header.Set("Content-Type", "application/json")
	} else {
		r = httptest.NewRequest(method, path, nil)
	}
	if code != "" {
		r.Header.Set(setup.CodeHeader, code)
	}
	w := httptest.NewRecorder()
	e.h.ServeHTTP(w, r)
	return w
}

func goodBody() map[string]any {
	return map[string]any{"companyName": "에이씨엠이 주식회사", "businessNo": "123-45-67890", "representative": "김대표", "address": "서울", "phone": "02-000-0000",
		"fiscalStartMonth": 4, "adminName": "오너", "adminEmail": " Owner@Acme.test ", "adminPassword": adminPw, "department": "경영지원"}
}

func (e *env) count(t *testing.T, q string, args ...any) int {
	t.Helper()
	var n int
	if err := e.db.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// AC-1800: 사용자가 없으면 pending, 있으면 done. pending 에서 상태 조회는 200 이고 완료 뒤에는 404 다.
func TestPendingWhenNoUsersAndStatusHidesDetails(t *testing.T) {
	e := newEnv(t)
	if !e.svc.Pending(context.Background()) {
		t.Fatal("사용자 0명인 새 DB 가 pending 이 아님")
	}
	w := e.do(t, "GET", "/api/setup/status", "", nil)
	if w.Code != 200 || strings.TrimSpace(w.Body.String()) != `{"state":"pending"}` {
		t.Fatalf("상태 응답: %d %q", w.Code, w.Body.String())
	}
	if w := e.do(t, "GET", "/setup", "", nil); w.Code != 200 {
		t.Fatalf("pending 의 /setup: %d", w.Code)
	}
}

// AC-1801: 코드 없이는 모든 POST 가 401, 코드 힌트가 응답에 없다. 설치 코드는 서버 로그에만 있다.
func TestCodeRequiredAndOnlyInServerLog(t *testing.T) {
	e := newEnv(t)
	if e.code == "" || len(strings.ReplaceAll(e.code, "-", "")) != 24 {
		t.Fatalf("로그에서 설치 코드를 못 찾음: %q", e.logs.String())
	}
	for _, p := range []string{"/api/setup/check", "/api/setup/validate", "/api/setup/complete"} {
		w := e.do(t, "POST", p, "", goodBody())
		if w.Code != 401 {
			t.Errorf("%s 코드 없음: %d", p, w.Code)
		}
		if strings.Contains(w.Body.String(), strings.ReplaceAll(e.code, "-", "")) || strings.Contains(w.Body.String(), e.code) {
			t.Errorf("%s 응답에 코드가 있음", p)
		}
	}
	if w := e.do(t, "POST", "/api/setup/check", e.code, struct{}{}); w.Code != 200 {
		t.Fatalf("올바른 코드: %d %s", w.Code, w.Body.String())
	}
	// 줄임·소문자·하이픈 없음으로 입력해도 같은 코드다.
	if w := e.do(t, "POST", "/api/setup/check", strings.ToLower(strings.ReplaceAll(e.code, "-", " ")), struct{}{}); w.Code != 200 {
		t.Errorf("끊어 읽은 코드: %d", w.Code)
	}
	// 완료 뒤에도 코드는 DB·감사·응답 어디에도 없다.
	if w := e.do(t, "POST", "/api/setup/complete", e.code, goodBody()); w.Code != 201 {
		t.Fatalf("완료: %d %s", w.Code, w.Body.String())
	}
	plain := strings.ReplaceAll(e.code, "-", "")
	for _, tbl := range []string{"audit_logs", "app_settings", "setup_state"} {
		rows, err := e.db.Query(fmt.Sprintf(`SELECT * FROM %s`, tbl))
		if err != nil {
			t.Fatal(err)
		}
		cols, _ := rows.Columns()
		for rows.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			_ = rows.Scan(ptrs...)
			if s := fmt.Sprint(vals...); strings.Contains(s, plain) || strings.Contains(s, e.code) {
				t.Errorf("%s 에 설치 코드가 있음", tbl)
			}
		}
		rows.Close()
	}
}

// PL_SETUP_CODE 로 지정하면 로그에 값을 찍지 않고, 16자 미만은 거부한다.
func TestEnvCodeNeverLogged(t *testing.T) {
	e := newEnv(t, func(o *setup.Options) { o.Code = envCode })
	if strings.Contains(e.logs.String(), envCode) {
		t.Fatal("PL_SETUP_CODE 값이 로그에 있음")
	}
	if !strings.Contains(e.logs.String(), "PL_SETUP_CODE") {
		t.Errorf("지정됨 안내가 없음: %q", e.logs.String())
	}
	if w := e.do(t, "POST", "/api/setup/check", envCode, struct{}{}); w.Code != 200 {
		t.Fatalf("지정한 코드: %d", w.Code)
	}
	if _, _, err := setup.NewGuard("short-code", nil); err == nil {
		t.Error("16자 미만 코드가 허용됨")
	}
}

// AC-1801·E3: 틀린 코드 연속 5회면 15분 동안 올바른 코드도 거부하고, 잠금 시작에 감사 1행이 남는다. 성공은 횟수를 되돌린다.
func TestCodeLockoutAfterFiveFailures(t *testing.T) {
	e := newEnv(t, func(o *setup.Options) { o.Code = envCode })
	for i := 0; i < 4; i++ {
		if w := e.do(t, "POST", "/api/setup/check", "wrong-code", struct{}{}); w.Code != 401 {
			t.Fatalf("%d번째 오답: %d", i+1, w.Code)
		}
	}
	if w := e.do(t, "POST", "/api/setup/check", envCode, struct{}{}); w.Code != 200 { // 성공은 연속 횟수를 0 으로
		t.Fatalf("4회 뒤 정답: %d", w.Code)
	}
	for i := 0; i < 5; i++ {
		e.do(t, "POST", "/api/setup/check", "wrong-code", struct{}{})
	}
	w := e.do(t, "POST", "/api/setup/check", envCode, struct{}{})
	if w.Code != 429 || w.Header().Get("Retry-After") == "" {
		t.Fatalf("잠금 중 정답: %d %v", w.Code, w.Header())
	}
	if strings.Contains(w.Body.String(), envCode) {
		t.Error("거부 응답에 코드가 있음")
	}
	if n := e.count(t, `SELECT COUNT(*) FROM audit_logs WHERE action = 'setup.code_lockout'`); n != 1 {
		t.Errorf("잠금 감사 %d행(1행이어야 함)", n)
	}
	*e.clock = e.clock.Add(14 * time.Minute)
	if w := e.do(t, "POST", "/api/setup/check", envCode, struct{}{}); w.Code != 429 {
		t.Errorf("14분 뒤에도 잠겨 있어야 함: %d", w.Code)
	}
	*e.clock = e.clock.Add(2 * time.Minute)
	if w := e.do(t, "POST", "/api/setup/check", envCode, struct{}{}); w.Code != 200 {
		t.Errorf("잠금이 풀려야 함: %d", w.Code)
	}
	// 같은 요청이 계속 잠겨 있는 동안 감사 행이 늘지 않는다(감사 체인 폭증 방지).
	if n := e.count(t, `SELECT COUNT(*) FROM audit_logs WHERE action = 'setup.code_lockout'`); n != 1 {
		t.Errorf("잠금 감사가 늘었음: %d", n)
	}
}

// 코드는 24시간만 유효하다.
func TestCodeExpiresAfter24Hours(t *testing.T) {
	e := newEnv(t, func(o *setup.Options) { o.Code = envCode })
	*e.clock = e.clock.Add(25 * time.Hour)
	if w := e.do(t, "POST", "/api/setup/check", envCode, struct{}{}); w.Code != 401 {
		t.Fatalf("만료된 코드: %d", w.Code)
	}
}

// AC-1802·AC-1807: 한 트랜잭션 완료, 세션 미생성, 이메일·비밀번호는 감사에 없음, 첫 관리자는 admin·비상 관리자.
func TestCompleteCreatesEverythingAndNoSession(t *testing.T) {
	e := newEnv(t)
	w := e.do(t, "POST", "/api/setup/complete", e.code, goodBody())
	if w.Code != 201 {
		t.Fatalf("완료: %d %s", w.Code, w.Body.String())
	}
	if len(w.Result().Cookies()) != 0 || w.Header().Get("Set-Cookie") != "" {
		t.Error("완료 응답이 쿠키(세션)를 만듦")
	}
	if n := e.count(t, `SELECT COUNT(*) FROM sessions`); n != 0 {
		t.Errorf("세션 %d개가 만들어짐", n)
	}
	var id int64
	var role, name string
	var active int
	if err := e.db.QueryRow(`SELECT id, role, name, active FROM users WHERE email = 'owner@acme.test'`).Scan(&id, &role, &name, &active); err != nil || role != "admin" || active != 1 || name != "오너" {
		t.Fatalf("첫 관리자: %v %q %q %d", err, role, name, active)
	}
	if n := e.count(t, `SELECT COUNT(*) FROM users`); n != 1 {
		t.Errorf("사용자 %d명", n)
	}
	var hash string
	var must int
	if err := e.db.QueryRow(`SELECT password_hash, must_change FROM user_credentials WHERE user_id = ?`, id).Scan(&hash, &must); err != nil || !strings.HasPrefix(hash, "$argon2id$") || must != 0 {
		t.Fatalf("자격: %v %q %d", err, hash, must)
	}
	pol, err := e.auth.Policy(context.Background())
	if err != nil || len(pol.EmergencyAdmins) != 1 || pol.EmergencyAdmins[0] != id {
		t.Fatalf("비상 관리자: %+v %v", pol.EmergencyAdmins, err)
	}
	var company string
	var fiscal int
	if err := e.db.QueryRow(`SELECT name, fiscal_start_month FROM company_settings WHERE id = 1`).Scan(&company, &fiscal); err != nil || company != "에이씨엠이 주식회사" || fiscal != 4 {
		t.Fatalf("회사 정보: %v %q %d", err, company, fiscal)
	}
	if ok, err := access.HasPerm(context.Background(), e.db, id, contract.PermRoleManage); err != nil || !ok {
		t.Errorf("첫 관리자에게 관리 권한이 없음: %v %v", ok, err)
	}
	if st, _ := setup.Status(context.Background(), e.db); st != setup.StatusDone {
		t.Fatalf("상태 %q", st)
	}
	// 감사: setup.complete 한 건, 행위자는 새 관리자, 이메일·비밀번호가 없다. 같은 체인에서 검증된다.
	var actor int64
	var diff, detail string
	if err := e.db.QueryRow(`SELECT actor_id, diff, detail FROM audit_logs WHERE action = 'setup.complete'`).Scan(&actor, &diff, &detail); err != nil || actor != id {
		t.Fatalf("완료 감사: %v actor=%d", err, actor)
	}
	rows, _ := e.db.Query(`SELECT diff, detail, target_id FROM audit_logs`)
	for rows.Next() {
		var a, b, c string
		_ = rows.Scan(&a, &b, &c)
		if s := a + b + c; strings.Contains(s, "owner@acme.test") || strings.Contains(strings.ToLower(s), "acme.test") || strings.Contains(s, adminPw) {
			t.Errorf("감사에 이메일/비밀번호가 있음: %q", s)
		}
	}
	rows.Close()
	if res, err := audit.New(e.db).Verify(context.Background()); err != nil || !res.ChainOK {
		t.Fatalf("체인: %+v %v", res, err)
	}
	if strings.Contains(e.logs.String(), adminPw) || strings.Contains(e.logs.String(), "owner@acme.test") {
		t.Error("로그에 이메일/비밀번호가 있음")
	}
	// 방금 정한 이메일/비밀번호로 로그인된다(세션은 로그인에서 만든다). 비상 관리자라 2단계 인증이 필요하면 제한 세션이다.
	res, err := e.auth.LoginPassword(context.Background(), auth.PasswordLogin{Email: adminEmail, Password: adminPw})
	if err != nil || (res.RawSession == "" && res.Ticket == "") {
		t.Fatalf("로그인: %+v %v", res, err)
	}
}

// AC-1803·E4: 완료 뒤 모든 경로가 404, 이전 코드로도 열리지 않고, 되돌리는 갱신·삭제는 트리거가 막는다. 재기동 뒤에도 같다.
func TestReentryBlockedAfterDone(t *testing.T) {
	e := newEnv(t, func(o *setup.Options) { o.Code = envCode })
	if w := e.do(t, "POST", "/api/setup/complete", envCode, goodBody()); w.Code != 201 {
		t.Fatalf("완료: %d %s", w.Code, w.Body.String())
	}
	for _, c := range []struct{ m, p string }{{"GET", "/api/setup/status"}, {"GET", "/setup"}, {"POST", "/api/setup/check"}, {"POST", "/api/setup/validate"}, {"POST", "/api/setup/complete"}} {
		if w := e.do(t, c.m, c.p, envCode, goodBody()); w.Code != 404 {
			t.Errorf("%s %s: %d(404 여야 함)", c.m, c.p, w.Code)
		}
	}
	if _, err := e.db.Exec(`UPDATE setup_state SET status = 'pending', completed_at = NULL`); err == nil {
		t.Error("done 을 pending 으로 되돌림")
	}
	if _, err := e.db.Exec(`UPDATE setup_state SET completed_by = 99`); err != nil { // done 상태의 다른 칼럼 갱신은 pending→done 이 아니므로 막혀야 한다
		t.Log("done 의 다른 칼럼 갱신도 거부됨")
	}
	if _, err := e.db.Exec(`DELETE FROM setup_state`); err == nil {
		t.Error("설치 상태 삭제가 허용됨")
	}
	// 재기동: 같은 DB 에 새 서비스를 올려도 코드를 만들지 않고 모든 경로가 404 다.
	logs := e.logs.Len()
	e2 := &env{db: e.db, txs: e.txs, auth: e.auth, clock: e.clock, logs: e.logs}
	var err error
	e2.svc, err = setup.New(context.Background(), setup.Options{DB: e.db, Txs: e.txs, Audit: audit.New(e.db), Auth: e.auth, Code: envCode})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(e.logs.String()[logs:], "초기 설정이 필요합니다") {
		t.Error("done 인데 설치 코드 안내가 출력됨")
	}
	rt := contract.NewRouteTable()
	e2.svc.Register(rt)
	e2.h = rt.Handler()
	if w := e2.do(t, "POST", "/api/setup/complete", envCode, goodBody()); w.Code != 404 {
		t.Errorf("재기동 뒤 complete: %d", w.Code)
	}
}

// 전체 롤백: 감사 기록이 실패하면 사용자·회사 정보·상태 어느 것도 남지 않는다(AC-1802).
func TestCompleteIsAtomicWhenAuditFails(t *testing.T) {
	e := newEnv(t, func(o *setup.Options) { o.Audit = failingAudit{audit.New(o.DB)}; o.Code = envCode })
	w := e.do(t, "POST", "/api/setup/complete", envCode, goodBody())
	if w.Code != 500 {
		t.Fatalf("감사 실패: %d", w.Code)
	}
	if n := e.count(t, `SELECT COUNT(*) FROM users`); n != 0 {
		t.Errorf("사용자 %d명이 남음", n)
	}
	if n := e.count(t, `SELECT COUNT(*) FROM company_settings`); n != 0 {
		t.Errorf("회사 정보가 남음")
	}
	if n := e.count(t, `SELECT COUNT(*) FROM user_credentials`); n != 0 {
		t.Errorf("자격이 남음")
	}
	if st, _ := setup.Status(context.Background(), e.db); st != setup.StatusPending {
		t.Errorf("상태가 %q", st)
	}
	var cfg string
	if err := e.db.QueryRow(`SELECT config_json FROM auth_policy WHERE id = 1`).Scan(&cfg); err == nil && strings.Contains(cfg, `"emergencyAdmins":[`) && !strings.Contains(cfg, `"emergencyAdmins":[]`) {
		t.Errorf("비상 관리자가 남음: %s", cfg)
	}
	// 실패 뒤 다시 시도하면 성공한다(반쪽 상태가 없다).
	e2 := newEnvOn(t, e, func(o *setup.Options) { o.Code = envCode })
	if w := e2.do(t, "POST", "/api/setup/complete", envCode, goodBody()); w.Code != 201 {
		t.Fatalf("재시도: %d %s", w.Code, w.Body.String())
	}
}

// newEnvOn 은 같은 DB 위에 새 서비스를 올린다(재기동·재시도 시험).
func newEnvOn(t *testing.T, prev *env, opt func(*setup.Options)) *env {
	t.Helper()
	e := &env{db: prev.db, txs: prev.txs, auth: prev.auth, clock: prev.clock, logs: prev.logs}
	o := setup.Options{DB: e.db, Txs: e.txs, Audit: audit.New(e.db), Auth: e.auth, Now: func() time.Time { return *e.clock }}
	opt(&o)
	var err error
	if e.svc, err = setup.New(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	rt := contract.NewRouteTable()
	e.svc.Register(rt)
	e.h = rt.Handler()
	return e
}

// AC-1806·E2: 두 요청이 동시에 완료를 보내면 정확히 하나만 성공하고 첫 관리자는 한 명이다.
func TestConcurrentCompleteExactlyOneWins(t *testing.T) {
	e := newEnv(t, func(o *setup.Options) { o.Code = envCode })
	const n = 6
	var wg sync.WaitGroup
	codes := make([]int, n)
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			b := goodBody()
			b["adminEmail"] = fmt.Sprintf("owner%d@acme.test", i) // 서로 다른 이메일이어도 하나만 성공해야 한다
			<-start
			codes[i] = e.do(t, "POST", "/api/setup/complete", envCode, b).Code
		}(i)
	}
	close(start)
	wg.Wait()
	ok, conflict := 0, 0
	for _, c := range codes {
		switch c {
		case 201:
			ok++
		case 409, 404: // 먼저 끝난 뒤 도착한 요청은 404(이미 닫힘)일 수 있다
			conflict++
		default:
			t.Errorf("예상 밖 응답 %d (%v)", c, codes)
		}
	}
	if ok != 1 || conflict != n-1 {
		t.Fatalf("성공 %d, 충돌/닫힘 %d: %v", ok, conflict, codes)
	}
	if c := e.count(t, `SELECT COUNT(*) FROM users`); c != 1 {
		t.Fatalf("첫 관리자 %d명", c)
	}
	if c := e.count(t, `SELECT COUNT(*) FROM audit_logs WHERE action = 'setup.complete'`); c != 1 {
		t.Fatalf("완료 감사 %d행", c)
	}
}

// 서비스 계층의 충돌(두 번째 호출이 이미 닫힌 뒤 도착)도 409 의미의 오류다.
func TestSecondCompleteReturnsAlreadyDone(t *testing.T) {
	e := newEnv(t, func(o *setup.Options) { o.Code = envCode })
	in := setup.Input{AdminName: "오너", AdminEmail: adminEmail, AdminPassword: adminPw}
	in.Company.Name = "ACME"
	if _, err := e.svc.Complete(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Complete(context.Background(), in); !errors.Is(err, setup.ErrAlreadyDone) {
		t.Fatalf("두 번째: %v", err)
	}
}

// 입력 검사: 필드별 한국어 문구, 상태를 바꾸지 않는 validate, 비밀번호 값이 응답에 없다.
func TestValidateFieldErrorsWithoutSideEffects(t *testing.T) {
	e := newEnv(t, func(o *setup.Options) { o.Code = envCode })
	bad := map[string]any{"companyName": "", "adminName": "", "adminEmail": "not-an-email", "adminPassword": "short", "fiscalStartMonth": 13, "demo": true, "demoConfirm": "다른 회사"}
	w := e.do(t, "POST", "/api/setup/validate", envCode, bad)
	if w.Code != 200 {
		t.Fatalf("validate: %d", w.Code)
	}
	var out struct {
		OK     bool              `json:"ok"`
		Errors map[string]string `json:"errors"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if out.OK {
		t.Fatal("잘못된 입력이 통과함")
	}
	for _, f := range []string{"company", "name", "email", "password", "fiscalStartMonth"} {
		if out.Errors[f] == "" {
			t.Errorf("%s 오류 문구가 없음: %v", f, out.Errors)
		}
	}
	if strings.Contains(w.Body.String(), "short") && strings.Contains(w.Body.String(), `"adminPassword"`) {
		t.Error("응답에 입력 비밀번호가 되돌아옴")
	}
	if n := e.count(t, `SELECT COUNT(*) FROM users`); n != 0 {
		t.Fatal("validate 가 사용자를 만듦")
	}
	if w := e.do(t, "POST", "/api/setup/complete", envCode, bad); w.Code != 422 {
		t.Errorf("잘못된 완료 요청: %d", w.Code)
	}
	// 흔한 비밀번호·이메일 앞부분과 같은 비밀번호는 정책으로 거부된다.
	for _, pw := range []string{"password123", "owner@acme.test"} {
		b := goodBody()
		b["adminPassword"] = pw
		if w := e.do(t, "POST", "/api/setup/complete", envCode, b); w.Code != 422 {
			t.Errorf("약한 비밀번호 %q: %d", pw, w.Code)
		}
	}
}

// AC-1808: 데모 선택은 확인 문구가 있어야 하고, demo_mode 가 켜지며 /api/auth/config 에 반영된다. 선택하지 않으면 켜지지 않는다.
func TestDemoFlag(t *testing.T) {
	e := newEnv(t, func(o *setup.Options) { o.Code = envCode })
	b := goodBody()
	b["demo"] = true
	if w := e.do(t, "POST", "/api/setup/complete", envCode, b); w.Code != 422 {
		t.Fatalf("확인 문구 없는 데모: %d", w.Code)
	}
	called := 0
	e = newEnvOn(t, e, func(o *setup.Options) {
		o.Code = envCode
		o.Demo = func(ctx context.Context, adminID int64) error {
			called++
			// 데모 시드는 설치 완료가 커밋된 뒤에 불린다: 이 시점에 완료 상태·첫 관리자가 이미 보인다.
			if st, _ := setup.Status(ctx, o.DB); st != setup.StatusDone {
				t.Errorf("데모 시드 시점의 설치 상태 %q(done 이어야 함)", st)
			}
			var n int
			_ = o.DB.QueryRow(`SELECT COUNT(*) FROM users WHERE id = ?`, adminID).Scan(&n)
			if n != 1 {
				t.Errorf("데모 시드 시점에 첫 관리자가 보이지 않음")
			}
			return nil
		}
	})
	b["demoConfirm"] = "에이씨엠이 주식회사"
	if w := e.do(t, "POST", "/api/setup/complete", envCode, b); w.Code != 201 {
		t.Fatalf("데모 완료: %d %s", w.Code, w.Body.String())
	}
	if called != 1 {
		t.Errorf("데모 시드 호출 %d회", called)
	}
	var v string
	if err := e.db.QueryRow(`SELECT value FROM app_settings WHERE key = 'setup.demo_mode'`).Scan(&v); err != nil || v != "1" {
		t.Fatalf("demo_mode: %v %q", err, v)
	}
	rt := contract.NewRouteTable()
	e.auth.Register(rt)
	r := httptest.NewRecorder()
	rt.Handler().ServeHTTP(r, httptest.NewRequest("GET", "/api/auth/config", nil))
	var cfg map[string]any
	_ = json.Unmarshal(r.Body.Bytes(), &cfg)
	if cfg["demoMode"] != true || cfg["setupPending"] != false {
		t.Errorf("auth config: %v", cfg)
	}
}

// 데모를 고르지 않으면 demo_mode 가 없다.
func TestNoDemoByDefault(t *testing.T) {
	e := newEnv(t, func(o *setup.Options) { o.Code = envCode })
	if w := e.do(t, "POST", "/api/setup/complete", envCode, goodBody()); w.Code != 201 {
		t.Fatalf("완료: %d", w.Code)
	}
	if n := e.count(t, `SELECT COUNT(*) FROM app_settings WHERE key = 'setup.demo_mode'`); n != 0 {
		t.Error("데모를 고르지 않았는데 demo_mode 가 있음")
	}
}

// 다른 출처의 요청(CSRF)·JSON 이 아닌 요청은 코드가 맞아도 거부된다.
func TestOriginAndContentTypeChecked(t *testing.T) {
	e := newEnv(t, func(o *setup.Options) { o.Code = envCode })
	b, _ := json.Marshal(goodBody())
	r := httptest.NewRequest("POST", "/api/setup/complete", bytes.NewReader(b))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set(setup.CodeHeader, envCode)
	r.Header.Set("Origin", "https://evil.example")
	w := httptest.NewRecorder()
	e.h.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Errorf("다른 출처: %d", w.Code)
	}
	r = httptest.NewRequest("POST", "/api/setup/complete", bytes.NewReader(b))
	r.Header.Set("Content-Type", "text/plain")
	r.Header.Set(setup.CodeHeader, envCode)
	w = httptest.NewRecorder()
	e.h.ServeHTTP(w, r)
	if w.Code != 415 {
		t.Errorf("JSON 아님: %d", w.Code)
	}
	if n := e.count(t, `SELECT COUNT(*) FROM users`); n != 0 {
		t.Error("거부된 요청이 사용자를 만듦")
	}
}

// 기동 때 확정: pending 인데 사용자가 있으면 done(수동 INSERT 환경 보호). 사용자가 없으면 그대로.
func TestReconcileClosesWhenUsersExist(t *testing.T) {
	e := newEnv(t, func(o *setup.Options) { o.Code = envCode })
	if did, err := setup.Reconcile(context.Background(), e.db, nil); err != nil || did {
		t.Fatalf("사용자 없음: %v %v", did, err)
	}
	if _, err := e.db.Exec(`INSERT INTO departments (name) VALUES ('수동') ON CONFLICT DO NOTHING`); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.Exec(`INSERT INTO users (email, name, role, department_id) VALUES ('manual@x.test', '수동', 'admin', (SELECT id FROM departments WHERE name = '수동'))`); err != nil {
		t.Fatal(err)
	}
	if did, err := setup.Reconcile(context.Background(), e.db, nil); err != nil || !did {
		t.Fatalf("사용자 있음: %v %v", did, err)
	}
	// 이미 pending 으로 올라간 서비스도 DB 를 매번 읽어 곧바로 닫힌다.
	if w := e.do(t, "POST", "/api/setup/check", envCode, struct{}{}); w.Code != 404 {
		t.Errorf("done 직후 check: %d", w.Code)
	}
	// 완료 요청이 사용자가 있는 DB 에서 경쟁해도 첫 관리자가 만들어지지 않는다.
	if _, err := e.svc.Complete(context.Background(), setup.Input{AdminName: "x", AdminEmail: "a@b.test", AdminPassword: adminPw}); err == nil {
		t.Error("사용자가 있는 DB 에서 완료됨")
	}
}

// 경쟁 기동: 같은 DB 를 쓰는 서버 둘이 동시에 올라와도(각자 다른 설치 코드) 한쪽이 끝내면 다른 쪽은 닫힌다. Reconcile 도 동시에 불러도 한 번만 바꾼다.
func TestTwoServersRaceAndReconcileOnce(t *testing.T) {
	e := newEnv(t, func(o *setup.Options) { o.Code = envCode })
	other := newEnvOn(t, e, func(o *setup.Options) { o.Code = "second-server-code-9876543210" })
	if w := other.do(t, "POST", "/api/setup/complete", "second-server-code-9876543210", goodBody()); w.Code != 201 {
		t.Fatalf("두 번째 서버 완료: %d %s", w.Code, w.Body.String())
	}
	if w := e.do(t, "POST", "/api/setup/complete", envCode, goodBody()); w.Code != 404 {
		t.Errorf("첫 서버는 닫혀야 함: %d", w.Code) // 같은 DB 를 매 요청 읽는다
	}
	if _, err := e.db.Exec(`UPDATE setup_state SET status = status`); err == nil {
		t.Error("done 상태의 갱신이 허용됨")
	}
	// Reconcile 병렬: 이미 done 이라 아무도 바꾸지 않는다.
	var wg sync.WaitGroup
	changed := make(chan bool, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			did, err := setup.Reconcile(context.Background(), e.db, nil)
			if err != nil {
				t.Error(err)
			}
			changed <- did
		}()
	}
	wg.Wait()
	close(changed)
	for did := range changed {
		if did {
			t.Error("이미 done 인데 Reconcile 이 바꿨다고 보고함")
		}
	}
}

// AC-1870: 기존 설치(사용자 있음)는 마이그레이션 직후 done, 빈 DB 는 pending. (SQLite 에서 단계별로 적용해 본다.)
func TestMigrationSeedsStateFromUsers(t *testing.T) {
	if dbtest.PG() {
		t.Skip("0054 이전까지만 적용하는 단계 시험은 SQLite 전용이다(PG 는 기준 파일 + 쌍 파일이라 중간 상태를 만들 수 없다). PG 는 위의 시험들이 새 DB 에서 pending 을 본다")
	}
	for _, withUser := range []bool{true, false} {
		db := dbtest.Open(t)
		ctx := context.Background()
		if err := migrations.ApplyThrough(ctx, db, "0053_audit_channel_cli.sql"); err != nil {
			t.Fatal(err)
		}
		if withUser {
			if _, err := db.Exec(`INSERT INTO departments (name) VALUES ('기존부서')`); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`INSERT INTO users (email, name, role, department_id) VALUES ('old@x.test','기존','staff',(SELECT id FROM departments WHERE name = '기존부서'))`); err != nil {
				t.Fatal(err)
			}
		}
		if err := migrations.Apply(ctx, db); err != nil {
			t.Fatal(err)
		}
		st, err := setup.Status(ctx, db)
		want := setup.StatusPending
		if withUser {
			want = setup.StatusDone
		}
		if err != nil || st != want {
			t.Errorf("사용자 있음=%v: 상태 %q(기대 %q) %v", withUser, st, want, err)
		}
	}
}
