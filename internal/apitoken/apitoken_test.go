package apitoken

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/choibongseok/procure-ledger/internal/apiv1"
	"github.com/choibongseok/procure-ledger/internal/audit"
	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/dbtest"
	"github.com/choibongseok/procure-ledger/internal/dbutil"
	"github.com/choibongseok/procure-ledger/migrations"
)

type env struct {
	t      *testing.T
	db     *sql.DB
	svc    *Service
	clock  *time.Time
	admin  int64
	issuer contract.Principal
}

func newEnv(t *testing.T, tweak func(*Options)) *env {
	t.Helper()
	ctx := context.Background()
	db := dbtest.Open(t)
	if err := migrations.Apply(ctx, db); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 13, 1, 0, 0, 0, time.UTC)
	e := &env{t: t, db: db, clock: &now}
	o := Options{DB: db, Txs: dbutil.NewTxManager(db, dbutil.DialectOf(db)), Audit: audit.New(db), Now: func() time.Time { return *e.clock }}
	if tweak != nil {
		tweak(&o)
	}
	var err error
	if e.svc, err = New(o); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO departments (name) VALUES ('운영')`); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`INSERT INTO users (email, name, role, department_id) VALUES ('a@x.test', '관리자', 'admin', (SELECT id FROM departments WHERE name = '운영')) RETURNING id`).Scan(&e.admin); err != nil {
		t.Fatal(err)
	}
	// 발급자는 관리 권한과 품목·거래처 읽기 권한을 가진 로그인 세션이다.
	e.issuer = contract.Principal{UserID: e.admin, Name: "관리자", Role: contract.RoleAdmin, Channel: contract.ChannelWeb, Auth: contract.AuthSession,
		Perms: contract.NewPermSet(contract.PermAPIManage, contract.PermInventoryRead, contract.PermPurchaseRead)}
	return e
}

func (e *env) count(q string, args ...any) int {
	e.t.Helper()
	var n int
	if err := e.db.QueryRow(q, args...).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

func (e *env) issue(name string, scopes ...string) (string, TokenInfo) {
	e.t.Helper()
	raw, info, err := e.svc.Issue(context.Background(), e.issuer, IssueInput{Name: name, Scopes: scopes})
	if err != nil {
		e.t.Fatal(err)
	}
	return raw, info
}

func (e *env) enable() {
	e.t.Helper()
	if err := e.svc.SetEnabled(context.Background(), e.issuer, true); err != nil {
		e.t.Fatal(err)
	}
}

func req(token, ip string) *http.Request {
	r := httptest.NewRequest("GET", "/api/v1/items", nil)
	r.RemoteAddr = ip + ":40000"
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	return r
}

func (e *env) auth(token, ip string) (apiv1.Caller, error) {
	return e.svc.AuthenticateAPI(context.Background(), req(token, ip))
}

func TestIssueStoresOnlyHashAndAudits(t *testing.T) {
	e := newEnv(t, nil)
	raw, info := e.issue("재고 연동", "items:read", "vendors:read")
	if !strings.HasPrefix(raw, "pli_") || len(raw) < 40 || info.Prefix != raw[:8] {
		t.Fatalf("원문 형식 %q %+v", raw, info)
	}
	var hash, prefix string
	if err := e.db.QueryRow(`SELECT token_hash, token_prefix FROM api_tokens WHERE id = ?`, info.ID).Scan(&hash, &prefix); err != nil || hash == raw || strings.Contains(hash, raw[4:]) {
		t.Fatalf("해시만 저장해야 한다: %q %v", hash, err)
	}
	if e.count(`SELECT COUNT(*) FROM audit_logs WHERE action = 'api.token_issue' AND result = 'success' AND channel = 'web'`) != 1 {
		t.Fatal("발급 감사가 한 건이어야 한다")
	}
	if e.count(`SELECT COUNT(*) FROM audit_logs WHERE diff LIKE ? OR detail LIKE ?`, "%"+raw[4:]+"%", "%"+raw[4:]+"%") != 0 {
		t.Fatal("감사에 토큰 원문이 있다")
	}
	list, err := e.svc.List(context.Background(), e.issuer)
	if err != nil || len(list) != 1 || list[0].Prefix != raw[:8] || len(list[0].Scopes) != 2 || list[0].Status != "active" {
		t.Fatalf("목록 %+v %v", list, err)
	}
}

func TestIssueRules(t *testing.T) {
	e := newEnv(t, func(o *Options) { o.MaxActive = 2 })
	ctx := context.Background()
	bad := []struct {
		in   IssueInput
		want error
	}{
		{IssueInput{Name: "", Scopes: []string{"items:read"}}, contract.ErrValidation},
		{IssueInput{Name: "x", Scopes: nil}, contract.ErrValidation},
		{IssueInput{Name: "x", Scopes: []string{"nope:read"}}, contract.ErrValidation},
		{IssueInput{Name: "x", Scopes: []string{"items:read"}, Days: 400}, contract.ErrValidation},
		{IssueInput{Name: "x", Scopes: []string{"items:read"}, Days: -3}, contract.ErrValidation},
		{IssueInput{Name: "x", Scopes: []string{"items:read"}, AllowCIDR: "999.1.1.1"}, contract.ErrValidation},
		{IssueInput{Name: strings.Repeat("가", 81), Scopes: []string{"items:read"}}, contract.ErrValidation},
		// 발급자 권한 상한: 발급자는 전표 읽기 권한(ledger:read)이 없다.
		{IssueInput{Name: "x", Scopes: []string{"items:read", "journals:read"}}, contract.ErrForbidden},
	}
	for i, c := range bad {
		if _, _, err := e.svc.Issue(ctx, e.issuer, c.in); !errors.Is(err, c.want) {
			t.Errorf("%d번 입력: %v(기대 %v)", i, err, c.want)
		}
	}
	// 토큰·SCIM·API 로 들어온 요청은 발급·폐기·설정을 못 한다(로그인 세션 필요).
	for _, m := range []contract.AuthMethod{contract.AuthToken, contract.AuthSCIM, contract.AuthAPI} {
		p := e.issuer
		p.Auth = m
		if _, _, err := e.svc.Issue(ctx, p, IssueInput{Name: "x", Scopes: []string{"items:read"}}); !errors.Is(err, contract.ErrForbidden) {
			t.Errorf("%s 발급: %v", m, err)
		}
		if err := e.svc.SetEnabled(ctx, p, true); !errors.Is(err, contract.ErrForbidden) {
			t.Errorf("%s 켜기: %v", m, err)
		}
	}
	// 권한 없는 사람.
	nobody := contract.Principal{UserID: e.admin, Role: contract.RoleStaff, Auth: contract.AuthSession, Perms: contract.NewPermSet()}
	if _, _, err := e.svc.Issue(ctx, nobody, IssueInput{Name: "x", Scopes: []string{"items:read"}}); !errors.Is(err, contract.ErrForbidden) {
		t.Errorf("권한 없는 발급: %v", err)
	}
	// 유효 토큰 상한(2개): 폐기하면 다시 만들 수 있다.
	_, a := e.issue("하나", "items:read")
	e.issue("둘", "items:read")
	if _, _, err := e.svc.Issue(ctx, e.issuer, IssueInput{Name: "셋", Scopes: []string{"items:read"}}); !errors.Is(err, contract.ErrInvalidState) {
		t.Fatalf("상한: %v", err)
	}
	if err := e.svc.Revoke(ctx, e.issuer, a.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.Revoke(ctx, e.issuer, a.ID); !errors.Is(err, contract.ErrInvalidState) {
		t.Fatalf("이중 폐기: %v", err)
	}
	if err := e.svc.Revoke(ctx, e.issuer, 9999); !errors.Is(err, contract.ErrNotFound) {
		t.Fatalf("없는 토큰: %v", err)
	}
	e.issue("셋", "items:read")
	if e.count(`SELECT COUNT(*) FROM audit_logs WHERE action = 'api.token_revoke'`) != 1 {
		t.Fatal("폐기 감사")
	}
}

func TestAuthenticateFlow(t *testing.T) {
	e := newEnv(t, nil)
	raw, info := e.issue("재고 연동", "items:read", "vendors:read")
	// 꺼짐: 토큰이 맞아도 403(켜기 전에는 존재 노출 방지로 토큰 확인 뒤에 판단).
	if _, err := e.auth(raw, "10.0.0.5"); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("꺼진 API: %v", err)
	}
	e.enable()
	c, err := e.auth(raw, "10.0.0.5")
	if err != nil {
		t.Fatal(err)
	}
	p := c.Principal
	if p.UserID != e.admin || p.Channel != contract.ChannelAPI || p.Auth != contract.AuthAPI || p.TokenID != info.ID || p.Name != "API(재고 연동)" || p.DepartmentID != 0 {
		t.Fatalf("고정 주체: %+v", p)
	}
	if !p.Can(contract.PermAPIAccess) || !p.Can(contract.PermInventoryRead) || !p.Can(contract.PermPurchaseRead) {
		t.Fatal("범위가 가리키는 권한과 api:access 가 있어야 한다")
	}
	// 발급자의 권한을 물려받지 않는다: 발급자는 api:manage 가 있으나 API 주체에는 없다. 위험 권한·mask:bypass 도 없다.
	for _, perm := range append([]contract.Permission{contract.PermAPIManage, contract.PermMaskBypass, contract.PermRoleManage}, contract.HighRiskPermissions...) {
		if p.Can(perm) {
			t.Errorf("API 주체에 %s 가 있다", perm)
		}
	}
	if len(c.Scopes) != 2 || c.TokenID != info.ID {
		t.Fatalf("호출자: %+v", c)
	}
	// 실패: 토큰 없음·잘못된 접두·모르는 토큰·개인 토큰 모양.
	for _, tok := range []string{"", "plp_" + raw[4:], "pli_nonexistent", "Bearer"} {
		if _, err := e.auth(tok, "10.0.0.6"); !errors.Is(err, contract.ErrUnauthenticated) {
			t.Errorf("%q: %v", tok, err)
		}
	}
	// 같은 출발지의 인증 실패 감사는 10분당 1행이다.
	if e.count(`SELECT COUNT(*) FROM audit_logs WHERE action = 'api.auth_failed' AND channel = 'api'`) != 1 {
		t.Fatalf("인증 실패 감사 %d행(10분당 1행이어야 한다)", e.count(`SELECT COUNT(*) FROM audit_logs WHERE action = 'api.auth_failed'`))
	}
	*e.clock = e.clock.Add(11 * time.Minute)
	if _, err := e.auth("pli_nonexistent", "10.0.0.6"); !errors.Is(err, contract.ErrUnauthenticated) {
		t.Fatal(err)
	}
	if e.count(`SELECT COUNT(*) FROM audit_logs WHERE action = 'api.auth_failed'`) != 2 {
		t.Fatal("10분이 지나면 다시 남는다")
	}
	// 폐기하면 즉시 401.
	if err := e.svc.Revoke(context.Background(), e.issuer, info.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.auth(raw, "10.0.0.5"); !errors.Is(err, contract.ErrUnauthenticated) {
		t.Fatalf("폐기 뒤: %v", err)
	}
	// 만료되면 401.
	raw2, _ := e.issue("짧은", "items:read")
	if _, err := e.auth(raw2, "10.0.0.5"); err != nil {
		t.Fatal(err)
	}
	*e.clock = e.clock.Add(181 * 24 * time.Hour)
	if _, err := e.auth(raw2, "10.0.0.5"); !errors.Is(err, contract.ErrUnauthenticated) {
		t.Fatalf("만료 뒤: %v", err)
	}
}

func TestIssuerLeftTokenStillWorksWithWarning(t *testing.T) {
	e := newEnv(t, nil)
	e.enable()
	raw, _ := e.issue("퇴사 시험", "items:read")
	if _, err := e.db.Exec(`UPDATE users SET active = 0 WHERE id = ?`, e.admin); err != nil {
		t.Fatal(err)
	}
	if _, err := e.auth(raw, "10.0.0.5"); err != nil {
		t.Fatalf("발급자가 퇴사해도 토큰은 유효하다(D2·6.2): %v", err)
	}
	list, _ := e.svc.List(context.Background(), e.issuer)
	if len(list) != 1 || !contains(list[0].Warnings, "issuer_left") {
		t.Fatalf("퇴사 경고: %+v", list)
	}
}

func contains(l []string, v string) bool {
	for _, x := range l {
		if x == v {
			return true
		}
	}
	return false
}

func TestListWarningsExpiringAndReducedIssuer(t *testing.T) {
	e := newEnv(t, nil)
	ctx := context.Background()
	_, a, err := e.svc.Issue(ctx, e.issuer, IssueInput{Name: "곧 만료", Scopes: []string{"items:read"}, Days: 10})
	if err != nil {
		t.Fatal(err)
	}
	e.issue("권한 축소", "vendors:read")
	list, _ := e.svc.List(ctx, e.issuer)
	var soon TokenInfo
	for _, l := range list {
		if l.ID == a.ID {
			soon = l
		}
	}
	if !contains(soon.Warnings, "expiring") {
		t.Fatalf("만료 14일 전 경고: %+v", soon)
	}
	// 발급자의 실효 권한(DB)에는 purchase:read 가 없다(시험 발급자는 세션 Perms 로만 가졌다) → 축소 경고.
	for _, l := range list {
		if l.Name == "권한 축소" && !contains(l.Warnings, "issuer_reduced") {
			t.Fatalf("발급자 권한 축소 경고: %+v", l)
		}
	}
	// 토큰은 자동 축소되지 않는다(D2): 인증은 그대로 된다.
	e.enable()
	raw, _ := e.issue("그대로", "vendors:read")
	if c, err := e.auth(raw, "10.0.0.5"); err != nil || !c.Principal.Can(contract.PermPurchaseRead) {
		t.Fatalf("축소된 발급자의 토큰도 범위 권한은 유지: %v", err)
	}
}

func TestRateLimitPerTokenWindow(t *testing.T) {
	e := newEnv(t, nil)
	e.enable()
	if err := e.svc.SetRate(context.Background(), e.issuer, 5); err == nil {
		t.Fatal("10 미만은 거부해야 한다")
	}
	if err := e.svc.SetRate(context.Background(), e.issuer, 10); err != nil {
		t.Fatal(err)
	}
	raw, info := e.issue("한도", "items:read")
	for i := 0; i < 10; i++ {
		if _, err := e.auth(raw, "10.0.0.5"); err != nil {
			t.Fatalf("%d번째: %v", i+1, err)
		}
	}
	var rl apiv1.ErrRateLimited
	for i := 0; i < 3; i++ {
		_, err := e.auth(raw, "10.0.0.5")
		if !errors.As(err, &rl) || rl.RetryAfterSeconds < 1 || rl.RetryAfterSeconds > 60 {
			t.Fatalf("한도 초과 %d: %v", i, err)
		}
	}
	if e.count(`SELECT COUNT(*) FROM audit_logs WHERE action = 'api.rate_limited'`) != 1 {
		t.Fatal("창의 첫 거부만 바로 감사에 남아야 한다")
	}
	var calls, denied int
	_ = e.db.QueryRow(`SELECT SUM(calls), SUM(denied) FROM api_rate_windows WHERE token_id = ?`, info.ID).Scan(&calls, &denied)
	if calls != 13 || denied != 3 {
		t.Fatalf("분 창: 호출 %d 거부 %d", calls, denied)
	}
	// 다른 토큰은 영향이 없고, 다음 분이 되면 다시 열린다.
	raw2, _ := e.issue("다른 토큰", "items:read")
	if _, err := e.auth(raw2, "10.0.0.5"); err != nil {
		t.Fatalf("다른 토큰: %v", err)
	}
	*e.clock = e.clock.Add(61 * time.Second)
	if _, err := e.auth(raw, "10.0.0.5"); err != nil {
		t.Fatalf("다음 분: %v", err)
	}
	list, _ := e.svc.List(context.Background(), e.issuer)
	for _, l := range list {
		if l.ID == info.ID && (l.Calls24h != 14 || l.Denied24h != 3 || l.LastUsedAt == nil) {
			t.Fatalf("사용 현황: %+v", l)
		}
	}
}

func TestIPAllowListAndAuthFailureLimiter(t *testing.T) {
	e := newEnv(t, func(o *Options) { o.AllowCIDR = "10.0.0.0/24" })
	e.enable()
	raw, _ := e.issue("ip", "items:read")
	if _, err := e.auth(raw, "10.0.0.7"); err != nil {
		t.Fatalf("허용 출발지: %v", err)
	}
	if _, err := e.auth(raw, "192.168.1.9"); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("전역 허용 밖: %v", err)
	}
	if e.count(`SELECT COUNT(*) FROM audit_logs WHERE action = 'api.denied' AND result = 'denied'`) != 1 {
		t.Fatal("허용 밖 거부 감사")
	}
	// 토큰별 허용 출발지.
	_, tok, err := e.svc.Issue(context.Background(), e.issuer, IssueInput{Name: "좁은", Scopes: []string{"items:read"}, AllowCIDR: "10.0.0.9"})
	if err != nil || tok.AllowCIDR != "10.0.0.9/32" {
		t.Fatalf("토큰 CIDR: %+v %v", tok, err)
	}
	// 인증 실패가 IP 당 분당 20회를 넘으면 429(Retry-After 60).
	var rl apiv1.ErrRateLimited
	for i := 0; i < 20; i++ {
		if _, err := e.auth("pli_bad", "10.0.0.8"); !errors.Is(err, contract.ErrUnauthenticated) {
			t.Fatalf("실패 %d: %v", i, err)
		}
	}
	if _, err := e.auth("pli_bad", "10.0.0.8"); !errors.As(err, &rl) || rl.RetryAfterSeconds != 60 {
		t.Fatalf("실패 폭주: %v", err)
	}
	if _, err := e.auth(raw, "10.0.0.8"); !errors.As(err, &rl) {
		t.Fatal("차단된 출발지는 올바른 토큰도 429(토큰 추측 방지)")
	}
	if _, err := e.auth(raw, "10.0.0.7"); err != nil {
		t.Fatalf("다른 출발지는 영향 없음: %v", err)
	}
}

func TestTokenCIDRDeniesOutsideSource(t *testing.T) {
	e := newEnv(t, nil)
	e.enable()
	raw, _, err := e.svc.Issue(context.Background(), e.issuer, IssueInput{Name: "좁은", Scopes: []string{"items:read"}, AllowCIDR: "10.1.0.0/16"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.auth(raw, "10.1.2.3"); err != nil {
		t.Fatalf("안: %v", err)
	}
	if _, err := e.auth(raw, "10.2.0.1"); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("밖: %v", err)
	}
}

func TestRehearsalModeRefusesEverything(t *testing.T) {
	e := newEnv(t, func(o *Options) { o.Disabled = true })
	raw, _ := e.issue("리허설", "items:read")
	e.enable()
	if _, err := e.auth(raw, "10.0.0.5"); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("리허설 모드: %v", err)
	}
}

func TestChangeFeedPerSecondLimit(t *testing.T) {
	e := newEnv(t, nil)
	e.enable()
	raw, _ := e.issue("피드", "changes:read", "items:read")
	feed := func() error {
		r := httptest.NewRequest("GET", "/api/v1/changes?cursor=c1.0", nil)
		r.RemoteAddr = "10.0.0.5:1"
		r.Header.Set("Authorization", "Bearer "+raw)
		_, err := e.svc.AuthenticateAPI(context.Background(), r)
		return err
	}
	for i := 0; i < 5; i++ {
		if err := feed(); err != nil {
			t.Fatalf("%d: %v", i, err)
		}
	}
	var rl apiv1.ErrRateLimited
	if err := feed(); !errors.As(err, &rl) {
		t.Fatalf("초당 5회 초과: %v", err)
	}
	*e.clock = e.clock.Add(1100 * time.Millisecond)
	if err := feed(); err != nil {
		t.Fatalf("1초 뒤: %v", err)
	}
	if _, err := e.auth(raw, "10.0.0.5"); err != nil { // 피드가 아닌 경로는 초당 제한이 없다
		t.Fatal(err)
	}
}

func TestWrapSummarizesReadsAndAuditsDenied(t *testing.T) {
	e := newEnv(t, nil)
	e.enable()
	raw, info := e.issue("요약", "items:read")
	status := http.StatusOK
	var next http.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := e.svc.AuthenticateAPI(r.Context(), r); err != nil {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(status)
	})
	h := e.svc.Wrap(next)
	call := func(path string) {
		r := httptest.NewRequest("GET", path, nil)
		r.RemoteAddr = "10.0.0.5:1"
		r.Header.Set("Authorization", "Bearer "+raw)
		h.ServeHTTP(httptest.NewRecorder(), r)
	}
	for i := 0; i < 3; i++ {
		call("/api/v1/items?cursor=SECRET-CURSOR&limit=50")
	}
	call("/api/v1/vendors")
	status = http.StatusForbidden
	call("/api/v1/journals")
	call("/api/v1/journals")
	// 인증 실패한 호출은 읽기 요약에 세지 않는다.
	bad := httptest.NewRequest("GET", "/api/v1/items", nil)
	bad.RemoteAddr = "10.0.0.5:1"
	h.ServeHTTP(httptest.NewRecorder(), bad)
	e.svc.Flush(context.Background(), true)
	rows, err := e.db.Query(`SELECT diff FROM audit_logs WHERE action = 'api.read' AND channel = 'api' AND target_id = ? ORDER BY seq`, strconv.FormatInt(info.ID, 10))
	if err != nil {
		t.Fatal(err)
	}
	var diffs []string
	for rows.Next() {
		var d string
		_ = rows.Scan(&d)
		diffs = append(diffs, d)
	}
	rows.Close()
	if len(diffs) != 3 {
		t.Fatalf("토큰·자원·분 단위 요약이 자원 3개(items·vendors·journals)에 1행씩이어야 한다: %v", diffs)
	}
	all := strings.Join(diffs, "")
	if !strings.Contains(all, `"new":"3"`) || strings.Contains(all, "SECRET-CURSOR") || strings.Contains(all, "limit") {
		t.Fatalf("요약에 건수는 있고 쿼리 값은 없어야 한다: %s", all)
	}
	if e.count(`SELECT COUNT(*) FROM audit_logs WHERE action = 'api.denied' AND diff LIKE '%forbidden_scope%'`) != 1 {
		t.Fatal("범위 밖 거부는 토큰·자원별 10분당 1행이다")
	}
	e.svc.Flush(context.Background(), true)
	if e.count(`SELECT COUNT(*) FROM audit_logs WHERE action = 'api.read'`) != 3 {
		t.Fatal("한 번 남긴 요약은 다시 남지 않는다")
	}
}

func TestWrapConcurrencyLimit(t *testing.T) {
	e := newEnv(t, func(o *Options) { o.MaxConcurrent = 1 })
	started, release := make(chan struct{}), make(chan struct{})
	h := e.svc.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-release
	}))
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/api/v1/items", nil))
	}()
	<-started
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/items", nil))
	if w.Code != http.StatusTooManyRequests || w.Header().Get("Retry-After") == "" {
		t.Fatalf("동시 상한 초과: %d", w.Code)
	}
	close(release)
	wg.Wait()
	// /api/v1 이 아닌 경로는 상한을 거치지 않는다.
	if w := httptest.NewRecorder(); true {
		e.svc.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })).ServeHTTP(w, httptest.NewRequest("GET", "/api/me", nil))
		if w.Code != 204 {
			t.Fatalf("다른 경로: %d", w.Code)
		}
	}
}

func TestSettingsAudit(t *testing.T) {
	e := newEnv(t, nil)
	ctx := context.Background()
	st, err := e.svc.GetSettings(ctx, e.issuer)
	if err != nil || st.Enabled || st.RatePerMin != DefaultRatePerMin || st.KeepDays != DefaultKeepDay {
		t.Fatalf("초기 설정: %+v %v", st, err)
	}
	e.enable()
	if err := e.svc.SetRate(ctx, e.issuer, 100); err != nil {
		t.Fatal(err)
	}
	st, _ = e.svc.GetSettings(ctx, e.issuer)
	if !st.Enabled || st.RatePerMin != 100 {
		t.Fatalf("변경 뒤: %+v", st)
	}
	if e.count(`SELECT COUNT(*) FROM audit_logs WHERE action = 'api.enabled_set'`) != 1 || e.count(`SELECT COUNT(*) FROM audit_logs WHERE action = 'api.rate_set'`) != 1 {
		t.Fatal("설정 변경 감사")
	}
	noPerm := contract.Principal{UserID: e.admin, Role: contract.RoleStaff, Auth: contract.AuthSession, Perms: contract.NewPermSet()}
	if _, err := e.svc.GetSettings(ctx, noPerm); !errors.Is(err, contract.ErrForbidden) {
		t.Fatal(err)
	}
	if _, err := e.svc.List(ctx, noPerm); !errors.Is(err, contract.ErrForbidden) {
		t.Fatal(err)
	}
}

// 사용 중인 웹훅 구독이 아직 읽지 않은 줄은 보존 기간이 지나도 지우지 않고, 중지된 구독은 붙잡지 않는다(B42 D12).
// 발급 시점 상한(D2): 내부 권한만으로 열리는 쓰기 범위도 발급자의 사람 권한을 요구한다. 전표 작성 권한이 없는 관리자는 전표 초안 토큰을 발급할 수 없다.
func TestIssueScopeCeilingForWriteScopes(t *testing.T) {
	e := newEnv(t, nil)
	ctx := context.Background()
	admin := e.issuer // api:manage 만 있고 전표 작성·가져오기 권한은 없다
	for _, scope := range []string{"journals:draft", "vendors:write", "items:write"} {
		if _, _, err := e.svc.Issue(ctx, admin, IssueInput{Name: "x-" + scope, Scopes: []string{scope}}); !errors.Is(err, contract.ErrForbidden) {
			t.Fatalf("%s: 사람 권한이 없는 발급자는 거부되어야 한다: %v", scope, err)
		}
	}
	// 범위 목록의 보유 표시도 같은 기준이다.
	infos, err := e.svc.Scopes(ctx, admin)
	if err != nil {
		t.Fatal(err)
	}
	for _, in := range infos {
		if (in.Name == "journals:draft" || in.Name == "vendors:write" || in.Name == "items:write") && in.Held {
			t.Fatalf("%s 는 보유로 표시되면 안 된다", in.Name)
		}
	}
	finance := e.issuer
	finance.Perms = contract.NewPermSet(contract.PermAPIManage, contract.PermLedgerWrite, contract.PermImportMaster)
	for _, scope := range []string{"journals:draft", "vendors:write", "items:write"} {
		if _, _, err := e.svc.Issue(ctx, finance, IssueInput{Name: "ok-" + scope, Scopes: []string{scope}}); err != nil {
			t.Fatalf("%s: 사람 권한이 있으면 발급된다: %v", scope, err)
		}
	}
	// 전표 작성 권한만으로는 가져오기 범위를, 가져오기 권한만으로는 전표 초안 범위를 발급할 수 없다.
	onlyLedger := e.issuer
	onlyLedger.Perms = contract.NewPermSet(contract.PermAPIManage, contract.PermLedgerWrite)
	if _, _, err := e.svc.Issue(ctx, onlyLedger, IssueInput{Name: "y", Scopes: []string{"vendors:write"}}); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("전표 권한만으로 가져오기 범위: %v", err)
	}
}

func TestPurgeChangesKeepsLinesUnreadByActiveWebhookSubscription(t *testing.T) {
	e := newEnv(t, nil)
	ctx := context.Background()
	old := e.clock.AddDate(0, 0, -100).Format(changeTSLayout)
	for i := 0; i < 4; i++ {
		if _, err := e.db.Exec(`INSERT INTO change_events (at, resource, object_id, op, status, actor_channel) VALUES (?, 'item', 1, 'updated', '', 'web')`, old); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := e.db.Exec(`INSERT INTO webhook_subscriptions (name, secret_sealed, cursor_seq, created_at, updated_at) VALUES ('느린 구독', 'x', 2, 't', 't')`); err != nil {
		t.Fatal(err)
	}
	if n, err := e.svc.PurgeChanges(ctx); err != nil || n != 2 {
		t.Fatalf("구독 위치(2)까지만 지운다: %d %v", n, err)
	}
	if e.count(`SELECT COUNT(*) FROM change_events`) != 2 {
		t.Fatal("읽지 않은 줄 2건은 남아야 한다")
	}
	if _, err := e.db.Exec(`UPDATE webhook_subscriptions SET paused_reason = 'manual'`); err != nil {
		t.Fatal(err)
	}
	if n, err := e.svc.PurgeChanges(ctx); err != nil || n != 2 {
		t.Fatalf("중지된 구독은 붙잡지 않는다: %d %v", n, err)
	}
}

func TestPurgeChangesKeepsRecentAndSetsExpiryMarker(t *testing.T) {
	e := newEnv(t, nil)
	ctx := context.Background()
	old := e.clock.AddDate(0, 0, -100).Format(changeTSLayout)
	recent := e.clock.AddDate(0, 0, -10).Format(changeTSLayout)
	for _, at := range []string{old, old, recent} {
		if _, err := e.db.Exec(`INSERT INTO change_events (at, resource, object_id, op, status, actor_channel) VALUES (?, 'item', 1, 'updated', '', 'web')`, at); err != nil {
			t.Fatal(err)
		}
	}
	n, err := e.svc.PurgeChanges(ctx)
	if err != nil || n != 2 {
		t.Fatalf("정리 %d %v", n, err)
	}
	if e.count(`SELECT COUNT(*) FROM change_events`) != 1 {
		t.Fatal("최근 줄은 남아야 한다")
	}
	var marker string
	if err := e.db.QueryRow(`SELECT value FROM app_settings WHERE key = 'changes.purged_through'`).Scan(&marker); err != nil || marker != "2" {
		t.Fatalf("정리 표시 %q %v", marker, err)
	}
	if e.count(`SELECT COUNT(*) FROM audit_logs WHERE action = 'changes.purge' AND channel = 'cli'`) != 1 {
		t.Fatal("정리 감사")
	}
	// 정리 뒤에도 추가 전용이 유지된다: 직접 지우기·고치기는 막힌다.
	if _, err := e.db.Exec(`DELETE FROM change_events`); err == nil {
		t.Fatal("정리 뒤 직접 삭제가 막히지 않음(트리거 복구 실패)")
	}
	if _, err := e.db.Exec(`UPDATE change_events SET op = 'created'`); err == nil {
		t.Fatal("고치기가 막히지 않음")
	}
	// 보존 기간 설정: 7일로 줄이면 10일 전 줄도 정리된다.
	if _, err := e.db.Exec(`INSERT INTO app_settings (key, value, updated_by, updated_at) VALUES ('changes.keep_days', '7', 0, 't')`); err != nil {
		t.Fatal(err)
	}
	if n, err := e.svc.PurgeChanges(ctx); err != nil || n != 1 {
		t.Fatalf("7일 정리 %d %v", n, err)
	}
	// 지울 것이 없으면 감사도 남기지 않는다.
	before := e.count(`SELECT COUNT(*) FROM audit_logs WHERE action = 'changes.purge'`)
	if n, err := e.svc.PurgeChanges(ctx); err != nil || n != 0 || e.count(`SELECT COUNT(*) FROM audit_logs WHERE action = 'changes.purge'`) != before {
		t.Fatalf("빈 정리 %d %v", n, err)
	}
}

// B42 3단계: 전표 초안 범위는 작성자가 토큰 발급자로 기록되므로, 발급자가 전표 승인에 관여하는 권한을 가졌으면 규칙 모드에 맞춰 경고한다(발급은 막지 않는다).
func TestJournalDraftScopeWarnsIssuerWhoCanApprove(t *testing.T) {
	e := newEnv(t, nil)
	ctx := context.Background()
	finance := e.issuer
	finance.Perms = contract.NewPermSet(contract.PermAPIManage, contract.PermLedgerWrite)
	approver := e.issuer
	approver.Perms = contract.NewPermSet(contract.PermAPIManage, contract.PermFinanceStage)
	plain := e.issuer
	plain.Perms = contract.NewPermSet(contract.PermAPIManage)
	if w := e.svc.IssueWarnings(ctx, plain, []string{"journals:draft"}); len(w) != 0 {
		t.Fatalf("승인 권한이 없는 발급자는 경고가 없다: %v", w)
	}
	if w := e.svc.IssueWarnings(ctx, finance, []string{"items:read", "vendors:read"}); len(w) != 0 {
		t.Fatalf("전표 초안 범위가 아니면 경고가 없다: %v", w)
	}
	for _, who := range []contract.Principal{finance, approver} {
		w := e.svc.IssueWarnings(ctx, who, []string{"journals:draft", "items:read"})
		if len(w) != 1 || !strings.Contains(w[0], "권한 분리 위반으로 기록") {
			t.Fatalf("경고 모드(초기): %v", w)
		}
	}
	// 규칙이 차단 모드면 "승인할 수 없다"는 안내로 바뀌고, 꺼져 있으면 경고가 없다.
	if _, err := e.db.Exec(`INSERT INTO sod_rule_settings (rule_code, mode, updated_by, updated_at) VALUES ('jv.create_approve', 'block', ?, 't')`, e.admin); err != nil {
		t.Fatal(err)
	}
	if w := e.svc.IssueWarnings(ctx, finance, []string{"journals:draft"}); len(w) != 1 || !strings.Contains(w[0], "승인할 수 없습니다") {
		t.Fatalf("차단 모드: %v", w)
	}
	if _, err := e.db.Exec(`UPDATE sod_rule_settings SET mode = 'off' WHERE rule_code = 'jv.create_approve'`); err != nil {
		t.Fatal(err)
	}
	if w := e.svc.IssueWarnings(ctx, finance, []string{"journals:draft"}); len(w) != 0 {
		t.Fatalf("끈 규칙: %v", w)
	}
}

func TestWriteScopesComeFromScopeTable(t *testing.T) {
	e := newEnv(t, nil)
	scopes, err := e.svc.Scopes(context.Background(), e.issuer)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]ScopeInfo{}
	for _, sc := range scopes {
		seen[sc.Name] = sc
		if sc.Write != (func() bool { d, _ := apiv1.ScopeByName(sc.Name); return d.Write })() {
			t.Errorf("%s: 쓰기 표시가 범위 표(ScopeDef.Write)와 다름", sc.Name)
		}
	}
	for _, name := range []string{"vendors:write", "items:write", "journals:draft"} {
		sc, ok := seen[name]
		if !ok || !sc.Write || sc.Note == "" {
			t.Errorf("%s: 쓰기 범위 표시·안내가 있어야 한다: %+v", name, sc)
		}
	}
	if !strings.Contains(seen["vendors:write"].Note, "가져오기") || !strings.Contains(seen["journals:draft"].Note, "초안만") {
		t.Error("안내 문구")
	}
	if seen["items:read"].Write || seen["items:read"].Note != "" {
		t.Error("읽기 범위에는 쓰기 표시·안내가 없다")
	}
}

func TestWriteRateSettingBoundsAndAudit(t *testing.T) {
	e := newEnv(t, nil)
	ctx := context.Background()
	st, _ := e.svc.GetSettings(ctx, e.issuer)
	if st.WritePerMin != DefaultWritePerMin {
		t.Fatalf("기본 쓰기 한도 %d", st.WritePerMin)
	}
	for _, bad := range []int{0, -5, 601} {
		if err := e.svc.SetWriteRate(ctx, e.issuer, bad); !errors.Is(err, contract.ErrValidation) {
			t.Errorf("%d: %v", bad, err)
		}
	}
	if err := e.svc.SetWriteRate(ctx, e.issuer, 12); err != nil {
		t.Fatal(err)
	}
	if st, _ := e.svc.GetSettings(ctx, e.issuer); st.WritePerMin != 12 || st.RatePerMin != DefaultRatePerMin {
		t.Fatalf("쓰기 한도만 바뀌어야 한다: %+v", st)
	}
	var v string
	if err := e.db.QueryRow(`SELECT value FROM app_settings WHERE key = 'api.write_per_min'`).Scan(&v); err != nil || v != "12" {
		t.Fatalf("쓰기 API 가 읽는 설정 키: %q %v", v, err)
	}
	if e.count(`SELECT COUNT(*) FROM audit_logs WHERE action = 'api.rate_set' AND diff LIKE '%api_write_rate%'`) != 1 {
		t.Fatal("쓰기 한도 변경 감사")
	}
	pat := e.issuer
	pat.Auth = contract.AuthToken
	if err := e.svc.SetWriteRate(ctx, pat, 20); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("토큰으로는 못 바꾼다: %v", err)
	}
}
