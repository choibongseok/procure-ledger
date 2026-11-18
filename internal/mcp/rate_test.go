package mcp_test

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/contract/deps"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/contract/fake"
	"github.com/choibongseok/procure-ledger/internal/dbtest"
	"github.com/choibongseok/procure-ledger/internal/mcp"
	"github.com/choibongseok/procure-ledger/internal/mcpusage"
	"github.com/choibongseok/procure-ledger/migrations"
)

type clock struct{ t atomic.Int64 }

func (c *clock) now() time.Time          { return time.Unix(c.t.Load(), 0) }
func (c *clock) advance(d time.Duration) { c.t.Add(int64(d / time.Second)) }

// rateEnv 는 한도 판정에 실제 SQLite 를 쓰는 환경이다(감사는 가짜 기록기).
func rateEnv(t *testing.T, perMin int) (*fake.Bundle, deps.Deps, *clock, *mcpusage.RateLimiter) {
	t.Helper()
	return rateEnvUser(t, perMin, 0)
}

// rateEnvUser 는 사용자 합산 한도(perUser)도 켠 환경이다.
func rateEnvUser(t *testing.T, perMin, perUser int) (*fake.Bundle, deps.Deps, *clock, *mcpusage.RateLimiter) {
	t.Helper()
	db := dbtest.Open(t)
	if err := migrations.Apply(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	b, d := newDeps()
	clk := &clock{}
	clk.t.Store(1_699_999_990) // 분 경계(1_699_999_980)에서 10초 뒤
	var lim *mcpusage.RateLimiter
	if perMin > 0 || perUser > 0 {
		lim = &mcpusage.RateLimiter{DB: db, PerMin: perMin, PerUserMin: perUser, Audit: d.Audit, Now: clk.now}
		d.MCPRate = lim
	}
	d.DB = db
	return b, d, clk, lim
}

func tokenP(p contract.Principal, id int64) contract.Principal {
	p.TokenID = id
	return p
}

func isLimited(res *sdk.CallToolResult, text string) bool {
	return res.IsError && strings.Contains(text, "호출 한도")
}

func countWindowCalls(t *testing.T, d deps.Deps, tokenID int64) int {
	t.Helper()
	var n int
	if err := d.DB.QueryRow(`SELECT COALESCE(SUM(calls),0) FROM mcp_rate_window WHERE token_id = ?`, tokenID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// 한도를 넘으면 도구를 실행하지 않고 한국어 안내로 거부하며 감사에 남긴다. 거부는 카운트를 올리지 않는다.
func TestRateLimitDeniesAndAudits(t *testing.T) {
	b, d, _, _ := rateEnv(t, 2)
	item := b.ERP.AddItem(domain.Item{Name: "복사용지"})
	cs := connect(t, d, tokenP(staffP, 11))
	for i := 0; i < 2; i++ {
		if res, text := call(t, cs, contract.ToolSearchItems, map[string]any{"query": "x"}); res.IsError {
			t.Fatalf("한도 안의 호출 %d: %s", i, text)
		}
	}
	before := b.Audit.Count()
	// 쓰기 도구도 한도를 넘으면 실행되지 않는다.
	res, text := call(t, cs, contract.ToolCreatePRDraft, draftArgs(item.ID))
	if !isLimited(res, text) || !strings.Contains(text, "1분에 2번") {
		t.Fatalf("한도 초과는 한국어 안내로 거부: isError=%v %q", res.IsError, text)
	}
	if n := countPRs(t, b.ERP); n != 0 {
		t.Fatalf("한도 초과인데 쓰기가 실행됨: PR %d건", n)
	}
	for i := 0; i < 5; i++ {
		if res, text := call(t, cs, contract.ToolSearchItems, map[string]any{"query": "x"}); !isLimited(res, text) {
			t.Fatalf("계속 거부여야 함: %q", text)
		}
	}
	// 거부 기록이 한도를 다시 소모하지 않는다: 창의 카운터는 한도(2)에서 멈춘다.
	if n := countWindowCalls(t, d, 11); n != 2 {
		t.Fatalf("거부 호출이 카운트를 올림: %d", n)
	}
	// 거부 호출 6건 중 감사에는 첫 거부 1건만 남고(B17), 시간·토큰 항목이 있다.
	if got := b.Audit.Count() - before; got != 1 {
		t.Fatalf("거부 감사는 첫 거부 1건이어야 함: %d", got)
	}
	last := b.Audit.Records()[b.Audit.Count()-1]
	if last.Result != audit.ResultDenied || !strings.Contains(last.Detail, "rate_limited") || last.Channel != contract.ChannelMCP || last.ActorID != staffP.UserID {
		t.Fatalf("거부 기록: %+v", last)
	}
	hasKey := func(r audit.Record, k string) bool {
		for _, it := range r.Diff {
			if it.Key == k {
				return true
			}
		}
		return false
	}
	if !hasKey(last, audit.DiffKeyDurationMS) || !hasKey(last, audit.DiffKeyTokenID) {
		t.Fatalf("종료 기록에 duration_ms·token_id 가 있어야 함: %+v", last.Diff)
	}
}

// 분이 바뀌면 다시 호출할 수 있다. 같은 분 안에서는 계속 거부된다.
func TestRateLimitMinuteBoundary(t *testing.T) {
	_, d, clk, _ := rateEnv(t, 1)
	cs := connect(t, d, tokenP(staffP, 11))
	if res, text := call(t, cs, contract.ToolSearchItems, map[string]any{"query": "x"}); res.IsError {
		t.Fatalf("첫 호출: %s", text)
	}
	clk.advance(40 * time.Second) // 같은 분(10초 → 50초)
	if res, text := call(t, cs, contract.ToolSearchItems, map[string]any{"query": "x"}); !isLimited(res, text) {
		t.Fatalf("같은 분 안에서는 거부: %q", text)
	}
	clk.advance(10 * time.Second) // 다음 분 0초
	if res, text := call(t, cs, contract.ToolSearchItems, map[string]any{"query": "x"}); res.IsError {
		t.Fatalf("분이 바뀌면 허용: %s", text)
	}
}

// 한도는 토큰별이다: 한 토큰이 소진해도 다른 토큰은 영향이 없다.
func TestRateLimitIsPerToken(t *testing.T) {
	_, d, _, _ := rateEnv(t, 1)
	a := connect(t, d, tokenP(staffP, 11))
	other := connect(t, d, tokenP(managerP, 12))
	if res, _ := call(t, a, contract.ToolSearchItems, map[string]any{"query": "x"}); res.IsError {
		t.Fatal("첫 호출")
	}
	if res, text := call(t, a, contract.ToolSearchItems, map[string]any{"query": "x"}); !isLimited(res, text) {
		t.Fatal("a 는 소진")
	}
	if res, text := call(t, other, contract.ToolSearchItems, map[string]any{"query": "x"}); res.IsError {
		t.Fatalf("다른 토큰은 영향 없음: %s", text)
	}
	// 같은 사용자의 다른 토큰도 따로 센다.
	same := connect(t, d, tokenP(staffP, 13))
	if res, text := call(t, same, contract.ToolSearchItems, map[string]any{"query": "x"}); res.IsError {
		t.Fatalf("같은 사용자의 다른 토큰은 따로 센다: %s", text)
	}
}

// 한도가 설정되지 않았으면(nil) 무제한이다. 토큰 번호가 없는 호출(세션 등)도 한도 대상이 아니다.
func TestRateLimitUnsetMeansUnlimited(t *testing.T) {
	_, d, _, _ := rateEnv(t, 0)
	if d.MCPRate != nil {
		t.Fatal("한도 0 이면 nil")
	}
	cs := connect(t, d, tokenP(staffP, 11))
	for i := 0; i < 60; i++ {
		if res, text := call(t, cs, contract.ToolSearchItems, map[string]any{"query": "x"}); res.IsError {
			t.Fatalf("무제한 %d: %s", i, text)
		}
	}
	_, d2, _, _ := rateEnv(t, 1)
	cs2 := connect(t, d2, staffP) // TokenID 0
	for i := 0; i < 3; i++ {
		if res, text := call(t, cs2, contract.ToolSearchItems, map[string]any{"query": "x"}); res.IsError {
			t.Fatalf("토큰 번호 없는 호출은 한도 대상이 아님: %s", text)
		}
	}
}

// 프로세스가 둘이어도(서버 /mcp 와 erp-mcp) 같은 DB 를 쓰면 한도가 합쳐서 적용된다.
func TestRateLimitSharedAcrossProcessesViaDB(t *testing.T) {
	_, d, clk, lim := rateEnv(t, 3)
	second := &mcpusage.RateLimiter{DB: d.DB, PerMin: 3, Now: clk.now} // 다른 프로세스의 한도기
	d2 := d
	d2.MCPRate = second
	c1 := connect(t, d, tokenP(staffP, 11))
	c2 := connect(t, d2, tokenP(staffP, 11))
	_ = lim
	ok := 0
	for i := 0; i < 6; i++ {
		c := c1
		if i%2 == 1 {
			c = c2
		}
		if res, _ := call(t, c, contract.ToolSearchItems, map[string]any{"query": "x"}); !res.IsError {
			ok++
		}
	}
	if ok != 3 {
		t.Fatalf("두 프로세스 합쳐서 3번만 허용: %d", ok)
	}
}

// stdio 경로(호출마다 토큰 재검증)에도 한도가 적용된다.
func TestRateLimitOnStdioPath(t *testing.T) {
	b, d, _, _ := rateEnv(t, 2)
	b.Auth.Add("t-limited", tokenP(staffP, 21))
	ctx := context.Background()
	srv, err := mcp.NewServerForToken(ctx, d, "t-limited")
	if err != nil {
		t.Fatal(err)
	}
	ct, st := sdk.NewInMemoryTransports()
	ss, _ := srv.Connect(ctx, st, nil)
	defer ss.Close()
	cs, _ := sdk.NewClient(&sdk.Implementation{Name: "t", Version: "0"}, nil).Connect(ctx, ct, nil)
	defer cs.Close()
	for i := 0; i < 2; i++ {
		if res, text := call(t, cs, contract.ToolSearchItems, map[string]any{"query": "x"}); res.IsError {
			t.Fatalf("한도 안 %d: %s", i, text)
		}
	}
	if res, text := call(t, cs, contract.ToolSearchItems, map[string]any{"query": "x"}); !isLimited(res, text) {
		t.Fatalf("stdio 도 한도 초과는 거부: %q", text)
	}
}

// 한도를 세지 못하면(DB 오류) 도구를 실행하지 않는다.
func TestRateLimitFailsClosedWhenCounterBroken(t *testing.T) {
	_, d, _, _ := rateEnv(t, 5)
	if _, err := d.DB.Exec(`DROP TABLE mcp_rate_window`); err != nil {
		t.Fatal(err)
	}
	cs := connect(t, d, tokenP(staffP, 11))
	res, text := call(t, cs, contract.ToolSearchItems, map[string]any{"query": "x"})
	if !res.IsError || !strings.Contains(text, "한도를 확인하지 못") {
		t.Fatalf("세지 못하면 거부: %v %q", res.IsError, text)
	}
}

// 종료 기록에는 소요 시간과 토큰 번호가 있고, 쓰기 도구의 실행 전 기록에는 없다(호출 1건 = 종료 기록 1건).
func TestTerminalRecordsCarryDurationAndToken(t *testing.T) {
	b, d, _, _ := rateEnv(t, 0)
	item := b.ERP.AddItem(domain.Item{Name: "복사용지"})
	cs := connect(t, d, tokenP(staffP, 11))
	call(t, cs, contract.ToolSearchItems, map[string]any{"query": "x"})
	call(t, cs, contract.ToolCreatePRDraft, draftArgs(item.ID))
	call(t, cs, contract.ToolApproveOrReject, map[string]any{"pr_id": 1, "approve": true}) // staff: 권한 거부
	has := func(r audit.Record, k string) (string, bool) {
		for _, it := range r.Diff {
			if it.Key == k {
				return it.New, true
			}
		}
		return "", false
	}
	terminal := 0
	for _, r := range b.Audit.Records() {
		if r.Channel != contract.ChannelMCP || !strings.HasPrefix(r.Action, "mcp.") {
			continue
		}
		_, dur := has(r, audit.DiffKeyDurationMS)
		tok, hasTok := has(r, audit.DiffKeyTokenID)
		if strings.HasSuffix(r.Detail, "requested") {
			if dur || hasTok {
				t.Fatalf("실행 전 기록에는 없어야 함: %+v", r)
			}
			continue
		}
		if !dur || tok != "11" {
			t.Fatalf("종료 기록에 duration_ms·token_id=11 이 있어야 함: %+v", r)
		}
		terminal++
	}
	if terminal != 3 {
		t.Fatalf("호출 3건이면 종료 기록 3건: %d", terminal)
	}
}

func rateRows(b *fake.Bundle, action string) []audit.Record {
	var out []audit.Record
	for _, r := range b.Audit.Records() {
		if r.Action == action {
			out = append(out, r)
		}
	}
	return out
}

// 한 분 안에 거부가 N 회여도 감사 행은 최대 2건(첫 거부 + 창 요약)이고, 요약의 횟수가 N 이다.
func TestRateDenialsCollapseIntoFirstRowAndSummary(t *testing.T) {
	b, d, clk, lim := rateEnv(t, 1)
	cs := connect(t, d, tokenP(staffP, 11))
	if res, text := call(t, cs, contract.ToolSearchItems, map[string]any{"query": "x"}); res.IsError {
		t.Fatal(text)
	}
	const n = 25
	for i := 0; i < n; i++ {
		if res, text := call(t, cs, contract.ToolSearchItems, map[string]any{"query": "x"}); !isLimited(res, text) {
			t.Fatalf("거부여야 함: %q", text)
		}
	}
	denied := 0
	for _, r := range b.Audit.Records() {
		if r.Result == audit.ResultDenied {
			denied++
		}
	}
	if denied != 1 {
		t.Fatalf("창이 닫히기 전 거부 감사는 1건: %d", denied)
	}
	// 같은 창에서는 정산하지 않는다(창이 아직 열려 있다).
	if got, err := lim.Settle(context.Background()); err != nil || got != 0 {
		t.Fatalf("열린 창은 정산 안 함: %d %v", got, err)
	}
	clk.advance(60 * time.Second)
	// 다음 호출이 지난 창을 정산한다.
	if res, text := call(t, cs, contract.ToolSearchItems, map[string]any{"query": "x"}); res.IsError {
		t.Fatalf("새 창의 첫 호출은 허용: %s", text)
	}
	sums := rateRows(b, mcpusage.SummaryAction)
	if len(sums) != 1 {
		t.Fatalf("요약 1건이어야 함: %d", len(sums))
	}
	var cnt, tok string
	for _, it := range sums[0].Diff {
		switch it.Key {
		case audit.DiffKeyDeniedCount:
			cnt = it.New
		case audit.DiffKeyTokenID:
			tok = it.New
		}
	}
	if cnt != "25" || tok != "11" || sums[0].ActorID != staffP.UserID || sums[0].Channel != contract.ChannelMCP {
		t.Fatalf("요약 기록: count=%q token=%q %+v", cnt, tok, sums[0])
	}
	// 두 번 정산되지 않는다.
	clk.advance(60 * time.Second)
	_, _ = call(t, cs, contract.ToolSearchItems, map[string]any{"query": "x"})
	if got := len(rateRows(b, mcpusage.SummaryAction)); got != 1 {
		t.Fatalf("요약은 한 번만: %d", got)
	}
}

// 거부가 1회뿐인 창은 요약이 없다(첫 거부 기록이 전부).
func TestRateSingleDenialHasNoSummary(t *testing.T) {
	b, d, clk, lim := rateEnv(t, 1)
	cs := connect(t, d, tokenP(staffP, 11))
	call(t, cs, contract.ToolSearchItems, map[string]any{"query": "x"})
	call(t, cs, contract.ToolSearchItems, map[string]any{"query": "x"})
	clk.advance(60 * time.Second)
	if got, err := lim.Settle(context.Background()); err != nil || got != 0 {
		t.Fatalf("정산 대상 없음: %d %v", got, err)
	}
	if len(rateRows(b, mcpusage.SummaryAction)) != 0 {
		t.Fatal("거부 1회는 요약 없음")
	}
}

// 사용자 합산 한도: 토큰을 여러 개 만들어도 합산 한도를 넘지 못한다. 한도 판정은 권한 판정보다 먼저다.
func TestRatePerUserAcrossTokens(t *testing.T) {
	_, d, _, _ := rateEnvUser(t, 0, 3)
	var sess []*sdk.ClientSession
	for id := int64(21); id <= 24; id++ {
		sess = append(sess, connect(t, d, tokenP(staffP, id)))
	}
	allowed := 0
	for i := 0; i < 12; i++ {
		res, text := call(t, sess[i%4], contract.ToolSearchItems, map[string]any{"query": "x"})
		if !res.IsError {
			allowed++
		} else if !isLimited(res, text) || !strings.Contains(text, "모든 개인 토큰을 합쳐") {
			t.Fatalf("합산 한도 안내: %q", text)
		}
	}
	if allowed != 3 {
		t.Fatalf("토큰 4개로 합쳐 3번만 허용: %d", allowed)
	}
	// 다른 사용자는 영향이 없다.
	other := connect(t, d, tokenP(managerP, 31))
	if res, text := call(t, other, contract.ToolSearchItems, map[string]any{"query": "x"}); res.IsError {
		t.Fatalf("다른 사용자: %s", text)
	}
	// 사용자 창 카운터는 한도에서 멈춘다(거부는 올리지 않는다).
	var userCalls int
	_ = d.DB.QueryRow(`SELECT calls FROM mcp_user_rate_window WHERE user_id = ?`, staffP.UserID).Scan(&userCalls)
	if userCalls != 3 {
		t.Fatalf("사용자 창 카운터는 한도에서 멈춤: %d", userCalls)
	}
}

// 토큰 한도에서 막힌 호출은 사용자 합산 카운터도 올리지 않는다(롤백).
func TestRateTokenDenialDoesNotConsumeUserWindow(t *testing.T) {
	_, d, _, _ := rateEnvUser(t, 1, 10)
	cs := connect(t, d, tokenP(staffP, 11))
	for i := 0; i < 5; i++ {
		call(t, cs, contract.ToolSearchItems, map[string]any{"query": "x"})
	}
	var n int
	_ = d.DB.QueryRow(`SELECT calls FROM mcp_user_rate_window WHERE user_id = ?`, staffP.UserID).Scan(&n)
	if n != 1 {
		t.Fatalf("거부된 호출이 사용자 카운터를 올림: %d", n)
	}
}
