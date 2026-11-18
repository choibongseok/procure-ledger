package llmgw_test

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/contract/fake"
	"github.com/choibongseok/procure-ledger/internal/contract/llm"
	"github.com/choibongseok/procure-ledger/internal/dbtest"
	"github.com/choibongseok/procure-ledger/internal/llmgw"
	"github.com/choibongseok/procure-ledger/migrations"
)

// spyProvider 는 게이트웨이가 공급자에게 실제로 보낸 요청을 기록한다.
type spyProvider struct {
	llmgw.MockProvider
	got   []llm.Request
	calls int
}

func (s *spyProvider) Complete(ctx context.Context, r llm.Request) (llm.Response, error) {
	s.calls++
	s.got = append(s.got, r)
	return s.MockProvider.Complete(ctx, r)
}

type env struct {
	db   *sql.DB
	rec  *fake.Recorder
	gw   *llmgw.Gateway
	spy  *spyProvider
	st   *llmgw.Store
	now  time.Time
	txs  contract.TxManager
	mask contract.Masker
}

func newEnv(t *testing.T) *env {
	t.Helper()
	db := dbtest.Open(t)
	if err := migrations.Apply(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	for _, id := range []int{1, 10, 20} {
		if _, err := db.Exec(`INSERT INTO departments (id, name) VALUES (?, ?)`, id, "부서"+string(rune('A'+id%26))); err != nil {
			t.Fatal(err)
		}
	}
	// 사용자 한도 범위 검증이 실재 사용자만 받으므로 시험용 사용자 1명을 둔다.
	if _, err := db.Exec(`INSERT INTO users (id, email, name, role, department_id) VALUES (1, 'u1@pl.test', '직원', 'staff', 10)`); err != nil {
		t.Fatal(err)
	}
	e := &env{db: db, rec: fake.NewRecorder(), spy: &spyProvider{}, now: time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC), mask: fake.Masker{}}
	e.txs = contract.SQLTxManager{DB: db}
	gw, err := llmgw.New(llmgw.Options{
		Provider: e.spy, DefaultModel: "mock-1", DB: db, Txs: e.txs, Audit: e.rec, Masker: e.mask,
		Prices: llmgw.Prices{InPerMTok: 1_000_000, OutPerMTok: 2_000_000}, Now: func() time.Time { return e.now },
	})
	if err != nil {
		t.Fatal(err)
	}
	e.gw = gw
	e.st = &llmgw.Store{DB: db, Txs: e.txs, Audit: e.rec, Now: func() time.Time { return e.now }}
	return e
}

func (e *env) allow(t *testing.T, role string, dept int64, pattern string, allowed int) {
	t.Helper()
	var r, d any
	if role != "" {
		r = role
	}
	if dept != 0 {
		d = dept
	}
	if _, err := e.db.Exec(`INSERT INTO llm_model_policies (role, department_id, model_pattern, allowed) VALUES (?,?,?,?)`, r, d, pattern, allowed); err != nil {
		t.Fatal(err)
	}
}

func as(role contract.Role, id, dept int64) context.Context {
	return contract.WithPrincipal(context.Background(), contract.Principal{UserID: id, Name: "u", Role: role, DepartmentID: dept, Channel: contract.ChannelWeb})
}

func req(text string) llm.Request {
	return llm.Request{Purpose: "test", Messages: []llm.Message{{Role: "user", Content: text}}}
}

func TestModelPolicyFailClosedAndSpecificity(t *testing.T) {
	e := newEnv(t)
	staff := as(contract.RoleStaff, 1, 10)

	// 규칙이 없으면 전부 거부.
	if _, err := e.gw.Complete(staff, req("안녕")); !errors.Is(err, contract.ErrModelNotAllowed) {
		t.Fatalf("규칙 없음 → 거부여야 함: %v", err)
	}
	if e.spy.calls != 0 {
		t.Fatal("거부된 호출이 공급자에게 갔다")
	}
	// 기본 허용 + staff 에게만 mock-* 금지: 더 구체적인 규칙이 이긴다.
	e.allow(t, "", 0, "mock*", 1)
	e.allow(t, "staff", 0, "mock*", 0)
	if _, err := e.gw.Complete(staff, req("안녕")); !errors.Is(err, contract.ErrModelNotAllowed) {
		t.Fatalf("staff 는 거부: %v", err)
	}
	if _, err := e.gw.Complete(as(contract.RoleFinance, 2, 20), req("안녕")); err != nil {
		t.Fatalf("finance 는 허용: %v", err)
	}
	// 부서 단위 허용(역할 규칙과 같은 구체성 1 → 거부가 이긴다, 구체성 2 는 허용이 이긴다).
	e.allow(t, "staff", 10, "mock*", 1)
	if _, err := e.gw.Complete(staff, req("안녕")); err != nil {
		t.Fatalf("role+dept 규칙(허용)이 이겨야 함: %v", err)
	}
	// 요청 모델이 패턴 밖이면 거부.
	r := req("안녕")
	r.Model = "other-model"
	if _, err := e.gw.Complete(as(contract.RoleFinance, 2, 20), r); !errors.Is(err, contract.ErrModelNotAllowed) {
		t.Fatalf("패턴 밖 모델: %v", err)
	}
}

func smallReq(text string) llm.Request {
	r := req(text)
	r.MaxTokens = 10 // 한도 예약에 쓰는 최대 출력
	return r
}

func TestDailyQuotaReturns429Semantics(t *testing.T) {
	e := newEnv(t)
	e.allow(t, "", 0, "mock*", 1)
	staff := as(contract.RoleStaff, 1, 10)
	admin := as(contract.RoleAdmin, 9, 1)
	if err := e.st.SetQuota(admin, llmgw.Quota{ScopeType: "role", ScopeValue: "staff", DailyLimit: 50}); err != nil {
		t.Fatal(err)
	}
	long := smallReq(strings.Repeat("가나다라 ", 30)) // 예상 입력 38 + 최대 출력 10 = 48 <= 50
	if _, err := e.gw.Complete(staff, long); err != nil {
		t.Fatalf("첫 호출은 한도 이내: %v", err)
	}
	_, err := e.gw.Complete(staff, smallReq("두 번째"))
	if !errors.Is(err, contract.ErrQuotaExceeded) {
		t.Fatalf("한도 초과는 ErrQuotaExceeded 여야 함: %v", err)
	}
	if e.spy.calls != 1 {
		t.Fatalf("차단된 호출은 공급자에게 가면 안 됨: calls=%d", e.spy.calls)
	}
	// 가장 좁은 규칙 우선: 사용자 한도를 크게 주면 다시 허용.
	if err := e.st.SetQuota(admin, llmgw.Quota{ScopeType: "user", ScopeValue: "1", DailyLimit: 1_000_000}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.gw.Complete(staff, smallReq("세 번째")); err != nil {
		t.Fatalf("user 한도가 role 한도보다 우선: %v", err)
	}
	if err := e.st.SetQuota(admin, llmgw.Quota{ScopeType: "user", ScopeValue: "1", DailyLimit: 5}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.gw.Complete(staff, smallReq("네 번째")); !errors.Is(err, contract.ErrQuotaExceeded) {
		t.Fatalf("오늘 사용량 초과: %v", err)
	}
	// 다음 날에는 사용량이 0 에서 다시 시작.
	if err := e.st.SetQuota(admin, llmgw.Quota{ScopeType: "user", ScopeValue: "1", DailyLimit: 100}); err != nil {
		t.Fatal(err)
	}
	e.now = e.now.Add(24 * time.Hour)
	if _, err := e.gw.Complete(staff, smallReq("다음 날")); err != nil {
		t.Fatalf("다음 날은 새로 시작: %v", err)
	}
	var blocked int
	e.db.QueryRow(`SELECT COUNT(*) FROM llm_usage WHERE status='blocked_quota'`).Scan(&blocked)
	if blocked != 2 {
		t.Fatalf("차단 사용량 행: %d", blocked)
	}
	if !e.rec.Has("llm.complete", audit.TargetLLMCall, audit.ResultDenied) {
		t.Fatal("차단도 감사 로그에 남아야 함")
	}
}

// gate 는 공급자 호출을 풀어 줄 때까지 붙잡아 "호출 진행 중" 상태를 만든다.
type gate struct {
	llmgw.MockProvider
	release chan struct{}
}

func (g *gate) Complete(ctx context.Context, r llm.Request) (llm.Response, error) {
	<-g.release
	return g.MockProvider.Complete(ctx, r)
}

// 한도 확인·예약이 원자적이므로, 동시에 몰려도 한도를 넘는 호출은 정확히 거부된다.
func TestConcurrentCallsNeverExceedQuota(t *testing.T) {
	e := newEnv(t)
	e.allow(t, "", 0, "mock*", 1)
	g := &gate{release: make(chan struct{})}
	gw, err := llmgw.New(llmgw.Options{Provider: g, DefaultModel: "mock-1", DB: e.db, Txs: e.txs, Audit: e.rec, Masker: e.mask, Now: func() time.Time { return e.now }})
	if err != nil {
		t.Fatal(err)
	}
	// 호출 하나가 예약하는 양 = 예상 입력 1 + 최대 출력 10 = 11. 한도 55 = 정확히 5건.
	if err := e.st.SetQuota(as(contract.RoleAdmin, 9, 1), llmgw.Quota{ScopeType: "role", ScopeValue: "staff", DailyLimit: 55}); err != nil {
		t.Fatal(err)
	}
	const n = 20
	type result struct{ err error }
	results := make(chan result, n)
	for i := 0; i < n; i++ {
		go func(i int) {
			_, err := gw.Complete(as(contract.RoleStaff, int64(100+i), 10), smallReq("x"))
			results <- result{err}
		}(i)
	}
	// 거부는 호출 전에 즉시 돌아온다. 15건이 거부될 때까지 기다린 뒤 진행 중인 5건을 푼다.
	rejected := 0
	for rejected < n-5 {
		r := <-results
		if !errors.Is(r.err, contract.ErrQuotaExceeded) {
			t.Fatalf("한도를 넘는 호출은 거부여야 함: %v", r.err)
		}
		rejected++
	}
	close(g.release)
	ok := 0
	for i := 0; i < 5; i++ {
		if r := <-results; r.err != nil {
			t.Fatalf("한도 이내 호출은 성공해야 함: %v", r.err)
		}
		ok++
	}
	if ok != 5 || rejected != 15 {
		t.Fatalf("정확히 5건 성공·15건 거부여야 함: ok=%d rejected=%d", ok, rejected)
	}
	var used int64
	e.db.QueryRow(`SELECT COALESCE(SUM(input_tokens+output_tokens),0) FROM llm_usage WHERE status='ok'`).Scan(&used)
	if used > 55 {
		t.Fatalf("실제 사용량이 한도를 넘음: %d", used)
	}
	var reservedLeft int
	e.db.QueryRow(`SELECT COUNT(*) FROM llm_usage WHERE status='ok' AND output_tokens = 10`).Scan(&reservedLeft)
	if reservedLeft != 0 {
		t.Fatal("끝난 호출의 예약이 실제 사용량으로 확정되지 않음")
	}
}

func TestFailedCallReleasesReservation(t *testing.T) {
	e := newEnv(t)
	e.allow(t, "", 0, "mock*", 1)
	if err := e.st.SetQuota(as(contract.RoleAdmin, 9, 1), llmgw.Quota{ScopeType: "role", ScopeValue: "staff", DailyLimit: 11}); err != nil {
		t.Fatal(err)
	}
	fail := &failing{}
	gw, _ := llmgw.New(llmgw.Options{Provider: fail, DefaultModel: "mock-1", DB: e.db, Txs: e.txs, Audit: e.rec, Masker: e.mask, Now: func() time.Time { return e.now }})
	staff := as(contract.RoleStaff, 1, 10)
	if _, err := gw.Complete(staff, smallReq("x")); err == nil {
		t.Fatal("공급자 실패는 오류여야 함")
	}
	fail.err = nil
	if _, err := gw.Complete(staff, smallReq("x")); err != nil {
		t.Fatalf("실패한 호출의 예약은 풀려야 함: %v", err)
	}
}

type failing struct {
	llmgw.MockProvider
	err error
	n   int
}

func (f *failing) Complete(ctx context.Context, r llm.Request) (llm.Response, error) {
	f.n++
	if f.n == 1 {
		return llm.Response{}, errors.New("boom")
	}
	return f.MockProvider.Complete(ctx, r)
}

func TestPromptIsMaskedBeforeProvider(t *testing.T) {
	e := newEnv(t)
	e.allow(t, "", 0, "mock*", 1)
	r := req("공급자 사업자번호 123-45-67890, 연락처 010-1234-5678 확인")
	r.System = "계좌 110-123-456789 는 알리지 말 것"
	if _, err := e.gw.Complete(as(contract.RoleStaff, 1, 10), r); err != nil {
		t.Fatal(err)
	}
	sent := e.spy.got[0]
	all := sent.System + " " + sent.Messages[0].Content
	for _, raw := range []string{"123-45-67890", "010-1234-5678", "110-123-456789"} {
		if strings.Contains(all, raw) {
			t.Fatalf("원문이 공급자에게 전송됨: %s in %q", raw, all)
		}
	}
	var masked int
	e.db.QueryRow(`SELECT masked_count FROM llm_usage`).Scan(&masked)
	if masked < 3 {
		t.Fatalf("마스킹 항목 수 기록: %d", masked)
	}
	// 감사 로그에도 프롬프트 원문이 없다.
	for _, rec := range e.rec.Records() {
		if strings.Contains(rec.Detail, "123-45-67890") {
			t.Fatal("감사 로그에 원문")
		}
	}
}

func TestUsageCostAndAuditRecorded(t *testing.T) {
	e := newEnv(t)
	e.allow(t, "", 0, "mock*", 1)
	resp, err := e.gw.Complete(as(contract.RoleFinance, 7, 20), req("hello"))
	if err != nil {
		t.Fatal(err)
	}
	var in, out, cost int64
	var provider, role, status string
	if err := e.db.QueryRow(`SELECT input_tokens, output_tokens, cost_micro_usd, provider, role, status FROM llm_usage`).
		Scan(&in, &out, &cost, &provider, &role, &status); err != nil {
		t.Fatal(err)
	}
	if int(in) != resp.Usage.InputTokens || int(out) != resp.Usage.OutputTokens || provider != "mock" || role != "finance" || status != "ok" {
		t.Fatalf("사용량 행: %d %d %s %s %s", in, out, provider, role, status)
	}
	if want := (in*1_000_000 + out*2_000_000) / 1_000_000; cost != want {
		t.Fatalf("비용 %d != %d", cost, want)
	}
	recs := e.rec.Records()
	if len(recs) != 1 || recs[0].Channel != contract.ChannelLLMGW || recs[0].TargetType != audit.TargetLLMCall || recs[0].Result != audit.ResultSuccess {
		t.Fatalf("감사 기록: %+v", recs)
	}
	// 집계 조회는 권한이 필요하다.
	if _, err := e.st.UsageSummary(as(contract.RoleStaff, 1, 10), 7); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("staff 는 사용량 조회 불가: %v", err)
	}
	rows, err := e.st.UsageSummary(as(contract.RoleAuditor, 5, 1), 7)
	if err != nil || len(rows) != 1 || rows[0].Calls != 1 || rows[0].Role != "finance" {
		t.Fatalf("집계: %+v %v", rows, err)
	}
}

func TestAuditFailureFailsTheCall(t *testing.T) {
	e := newEnv(t)
	e.allow(t, "", 0, "mock*", 1)
	e.rec.FailWith(errors.New("disk"))
	if _, err := e.gw.Complete(as(contract.RoleStaff, 1, 10), req("x")); !errors.Is(err, contract.ErrAuditFailed) {
		t.Fatalf("기록 실패 = 호출 실패여야 함: %v", err)
	}
	if e.rec.Count() != 0 {
		t.Fatal("감사 실패 시 감사 기록이 남으면 안 됨")
	}
	// 호출은 일어났으므로 사용량은 예약이 아니라 실제 값으로 남는다(과금 누락 방지).
	var out int
	e.db.QueryRow(`SELECT output_tokens FROM llm_usage WHERE status='ok'`).Scan(&out)
	if out == 1024 {
		t.Fatal("예약이 풀리지 않고 남음")
	}
}

func TestRequiresPrincipal(t *testing.T) {
	e := newEnv(t)
	e.allow(t, "", 0, "mock*", 1)
	if _, err := e.gw.Complete(context.Background(), req("x")); !errors.Is(err, contract.ErrUnauthenticated) {
		t.Fatalf("Principal 없는 호출은 거부: %v", err)
	}
}

func TestPolicyManagementNeedsPermissionAndIsAudited(t *testing.T) {
	e := newEnv(t)
	if _, err := e.st.AddPolicy(as(contract.RoleStaff, 1, 10), llmgw.Policy{ModelPattern: "x", Allowed: true}); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("staff 는 정책 변경 불가: %v", err)
	}
	if !e.rec.Has("llm_policy.add", audit.TargetLLMPolicy, audit.ResultDenied) {
		t.Fatal("거부도 감사 로그")
	}
	id, err := e.st.AddPolicy(as(contract.RoleAdmin, 9, 1), llmgw.Policy{Role: "staff", ModelPattern: "mock*", Allowed: true})
	if err != nil || id == 0 {
		t.Fatalf("admin 추가: %v", err)
	}
	if !e.rec.Has("llm_policy.add", audit.TargetLLMPolicy, audit.ResultSuccess) {
		t.Fatal("성공 감사 로그")
	}
	ps, _ := e.st.ListPolicies(context.Background())
	if len(ps) != 1 || ps[0].Role != "staff" {
		t.Fatalf("목록: %+v", ps)
	}
	if err := e.st.DeletePolicy(as(contract.RoleAdmin, 9, 1), id); err != nil {
		t.Fatal(err)
	}
}

func TestEnsureDefaultPolicyOnlyWhenEmpty(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	if err := llmgw.EnsureDefaultPolicy(ctx, e.db, "mock*"); err != nil {
		t.Fatal(err)
	}
	if err := llmgw.EnsureDefaultPolicy(ctx, e.db, "other"); err != nil {
		t.Fatal(err)
	}
	ps, _ := e.st.ListPolicies(ctx)
	if len(ps) != 1 || ps[0].ModelPattern != "mock*" {
		t.Fatalf("기존 정책을 덮어쓰면 안 됨: %+v", ps)
	}
}

func TestMockIsDeterministic(t *testing.T) {
	m := llmgw.MockProvider{}
	r := req("같은 입력")
	a, _ := m.Complete(context.Background(), r)
	b, _ := m.Complete(context.Background(), r)
	if a != b {
		t.Fatalf("mock 은 결정적이어야 함: %+v %+v", a, b)
	}
	r2 := req("다른 입력")
	c, _ := m.Complete(context.Background(), r2)
	if c.Text == a.Text {
		t.Fatal("입력이 다르면 출력도 달라야 함")
	}
}

// B49: 사용 행은 호출 시점의 현재 법인을 기록한다(법인 없는 호출은 0 = 미상).
func TestUsageRecordsActiveEntity(t *testing.T) {
	e := newEnv(t)
	e.allow(t, "", 0, "mock*", 1)
	if _, err := e.gw.Complete(contract.WithActiveEntity(as(contract.RoleFinance, 7, 20), contract.ActiveEntity{ID: 3}), req("hello")); err != nil {
		t.Fatal(err)
	}
	if _, err := e.gw.Complete(as(contract.RoleFinance, 7, 20), req("hello")); err != nil {
		t.Fatal(err)
	}
	rows, err := e.db.Query(`SELECT entity_id FROM llm_usage ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []int64
	for rows.Next() {
		var v int64
		if err := rows.Scan(&v); err != nil {
			t.Fatal(err)
		}
		got = append(got, v)
	}
	if len(got) != 2 || got[0] != 3 || got[1] != 0 {
		t.Fatalf("법인 열 %v (3, 0 이어야 함)", got)
	}
}
