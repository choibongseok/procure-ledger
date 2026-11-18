package mcpusage

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	auditstore "github.com/choibongseok/procure-ledger/internal/audit"
	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
)

// 거부 횟수는 토큰·분 창에 쌓이고, 닫힌 창을 정산하면 실제 감사 저장소(해시 체인)에 요약 1건이 붙는다.
func TestSettleWritesSummaryToRealAuditChain(t *testing.T) {
	l := newLimiterDB(t)
	l.PerMin = 1
	store := auditstore.New(l.DB)
	l.Audit = store
	ctx := context.Background()
	if d, err := l.Check(ctx, Call{TokenID: 7, UserID: 1}); err != nil || !d.Allowed {
		t.Fatalf("첫 호출: %+v %v", d, err)
	}
	for i := 1; i <= 4; i++ {
		d, err := l.Check(ctx, Call{TokenID: 7, UserID: 1})
		if err != nil || d.Allowed || d.Scope != ScopeToken || d.Denied != i {
			t.Fatalf("거부 %d: %+v %v", i, d, err)
		}
	}
	if n, err := l.Settle(ctx); err != nil || n != 0 {
		t.Fatalf("열린 창은 정산 안 함: %d %v", n, err)
	}
	now := l.Now()
	l.Now = func() time.Time { return now.Add(2 * time.Minute) }
	if n, err := l.Settle(ctx); err != nil || n != 1 {
		t.Fatalf("정산 1건: %d %v", n, err)
	}
	if n, _ := l.Settle(ctx); n != 0 {
		t.Fatal("두 번 정산되지 않는다")
	}
	var recs []audit.Record
	_ = store.Scan(ctx, audit.Filter{Channel: contract.ChannelMCP}, func(r audit.Record) error { recs = append(recs, r); return nil })
	if len(recs) != 1 || recs[0].Action != SummaryAction || recs[0].ActorID != 1 {
		t.Fatalf("요약 1건: %+v", recs)
	}
	if v, err := store.Verify(ctx); err != nil || !v.OK {
		t.Fatalf("체인 검증: %+v %v", v, err)
	}
}

type failRec struct{}

func (failRec) Record(context.Context, contract.Tx, audit.Entry) error {
	return errors.New("감사 실패")
}

// 감사 기록이 실패하면 정산 표시도 되돌아가 다음에 다시 시도한다(요약이 유실되지 않는다).
func TestSettleRetriesWhenAuditFails(t *testing.T) {
	l := newLimiterDB(t)
	l.PerMin = 1
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		_, _ = l.Check(ctx, Call{TokenID: 7, UserID: 1})
	}
	now := l.Now()
	l.Now = func() time.Time { return now.Add(2 * time.Minute) }
	l.Audit = failRec{}
	if _, err := l.Settle(ctx); err == nil {
		t.Fatal("감사 실패는 오류")
	}
	l.Audit = auditstore.New(l.DB)
	if n, err := l.Settle(ctx); err != nil || n != 1 {
		t.Fatalf("다시 시도해 정산: %d %v", n, err)
	}
}

// 사용자 합산 한도는 토큰이 달라도 합쳐 세고, 토큰 한도가 꺼져 있으면 토큰 카운터는 쓰지 않는다.
func TestUserLimitSumsTokens(t *testing.T) {
	l := newLimiterDB(t)
	l.PerMin, l.PerUserMin = 0, 3
	ctx := context.Background()
	ok := 0
	for i := 0; i < 8; i++ {
		d, err := l.Check(ctx, Call{TokenID: int64(10 + i%4), UserID: 5})
		if err != nil {
			t.Fatal(err)
		}
		if d.Allowed {
			ok++
		} else if d.Scope != ScopeUser || d.Limit != 3 {
			t.Fatalf("거부 범위: %+v", d)
		}
	}
	if ok != 3 {
		t.Fatalf("합산 3번: %d", ok)
	}
	var calls int
	_ = l.DB.QueryRow(`SELECT COALESCE(SUM(calls),0) FROM mcp_rate_window`).Scan(&calls)
	if calls != 0 {
		t.Fatalf("토큰 카운터를 쓰지 않아야 함: %d", calls)
	}
}

func TestCleanupKeepsUnsettledWindows(t *testing.T) {
	l := newLimiterDB(t)
	ctx := context.Background()
	old := l.Now().Unix()/60 - 120 // 보관 60분을 넘겼지만 정산 보관 기간 안
	if _, err := l.DB.Exec(`INSERT INTO mcp_rate_window (token_id, window_start, calls, denied_count) VALUES (9, ?, 5, 4), (8, ?, 5, 1)`, old, old); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Check(ctx, Call{TokenID: 7}); err != nil {
		t.Fatal(err)
	}
	var kept, gone int
	_ = l.DB.QueryRow(`SELECT COUNT(*) FROM mcp_rate_window WHERE token_id = 9`).Scan(&kept)
	_ = l.DB.QueryRow(`SELECT COUNT(*) FROM mcp_rate_window WHERE token_id = 8`).Scan(&gone)
	if kept != 1 || gone != 0 {
		t.Fatalf("정산 안 된 요약 대상은 남기고 나머지는 지운다: kept=%d gone=%d", kept, gone)
	}
}

// 창 요약 기록은 첫 거부 1건을 뺀 나머지 거부를 합계·토큰·"도구 미상" 행에 더한다(B17).
func TestReportAddsSummarizedRateDenials(t *testing.T) {
	at := time.Date(2026, 10, 5, 9, 0, 0, 0, time.UTC)
	sum := audit.Record{Channel: contract.ChannelMCP, Action: SummaryAction, Result: audit.ResultDenied, At: at, Diff: []audit.DiffItem{
		{Key: audit.DiffKeyTokenID, New: "5"}, {Key: audit.DiffKeyDeniedCount, New: "10"}, {Key: audit.DiffKeyWindowStart, New: "1"},
	}}
	svc := &Service{Scan: &fakeScan{recs: []audit.Record{
		rec("mcp.search_items", audit.ResultSuccess, "tool=search_items", 10, 5, at),
		rec("mcp.search_items", audit.ResultDenied, "tool=search_items rate_limited scope=token limit=1", 1, 5, at),
		sum,
	}}, Tokens: fakeTokens{}}
	rep, err := svc.Report(context.Background(), at.Add(-time.Hour), at.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if rep.Total.Calls != 11 || rep.Total.RateLimited != 10 || rep.Total.Success != 1 {
		t.Fatalf("합계(성공 1 + 첫 거부 1 + 요약 추가 9): %+v", rep.Total)
	}
	if len(rep.Tokens) != 1 || rep.Tokens[0].RateLimited != 10 {
		t.Fatalf("토큰: %+v", rep.Tokens)
	}
	found := false
	for _, tr := range rep.Tools {
		if tr.Tool == SummaryTool && tr.RateLimited == 9 {
			found = true
		}
	}
	if !found {
		t.Fatalf("도구 미상 행: %+v", rep.Tools)
	}
}

// 거부 뒤 추가 호출이 없어도 Run 이 닫힌 창을 정산해 요약 행을 남기고, 종료(ctx 취소) 때도 정산한 뒤 돌아온다.
func TestRunSettlesWithoutFurtherCalls(t *testing.T) {
	l := newLimiterDB(t)
	l.PerMin = 1
	store := auditstore.New(l.DB)
	l.Audit = store
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		_, _ = l.Check(ctx, Call{TokenID: 7, UserID: 1})
	}
	now := l.Now()
	var clk atomic.Int64
	clk.Store(now.Unix())
	l.Now = func() time.Time { return time.Unix(clk.Load(), 0) }
	rctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { l.Run(rctx, 5*time.Millisecond); close(done) }()
	count := func() int {
		n := 0
		_ = store.Scan(ctx, audit.Filter{Channel: contract.ChannelMCP}, func(r audit.Record) error {
			if r.Action == SummaryAction {
				n++
			}
			return nil
		})
		return n
	}
	time.Sleep(30 * time.Millisecond)
	if count() != 0 {
		t.Fatal("창이 열려 있는 동안은 정산하지 않는다")
	}
	clk.Add(120) // 창이 닫힘. 호출은 더 없다.
	deadline := time.Now().Add(2 * time.Second)
	for count() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if count() != 1 {
		t.Fatalf("호출 없이도 요약 1건: %d", count())
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("ctx 취소 뒤 Run 이 돌아오지 않는다")
	}
}

// 대상이 한 번의 묶음(100건)보다 많아도 한 번의 Settle 이 모두 정산한다.
func TestSettleDrainsMoreThanOneBatch(t *testing.T) {
	l := newLimiterDB(t)
	l.Audit = auditstore.New(l.DB)
	old := l.Now().Unix()/60 - 10
	tx, _ := l.DB.Begin()
	for i := 0; i < 250; i++ {
		if _, err := tx.Exec(`INSERT INTO mcp_rate_window (token_id, window_start, calls, denied_count, user_id) VALUES (?, ?, 1, 3, 1)`, i+1, old); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	n, err := l.Settle(context.Background())
	if err != nil || n != 250 {
		t.Fatalf("250건 모두 정산: %d %v", n, err)
	}
	var left int
	_ = l.DB.QueryRow(`SELECT COUNT(*) FROM mcp_rate_window WHERE denied_count >= 2 AND summarized = 0`).Scan(&left)
	if left != 0 {
		t.Fatalf("미정산 %d", left)
	}
}
