package mcpusage

import (
	"context"
	"testing"
	"time"

	"github.com/choibongseok/procure-ledger/internal/dbtest"
	"github.com/choibongseok/procure-ledger/migrations"
)

func newLimiterDB(t *testing.T) *RateLimiter {
	t.Helper()
	db := dbtest.Open(t)
	if err := migrations.Apply(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	return &RateLimiter{DB: db, PerMin: 3, Now: func() time.Time { return time.Unix(1_699_999_990, 0) }}
}

func TestNewRateLimiterZeroIsNil(t *testing.T) {
	if NewRateLimiter(nil, 0, 0) != nil || NewRateLimiter(nil, -1, -1) != nil {
		t.Fatal("0 이하는 한도 없음(nil)")
	}
	var l *RateLimiter
	if ok, err := l.Allow(context.Background(), 1); !ok || err != nil || l.Limit() != 0 {
		t.Fatal("nil 한도기는 항상 허용")
	}
}

func TestAllowCountsAtomicallyAndStopsAtLimit(t *testing.T) {
	l := newLimiterDB(t)
	ctx := context.Background()
	ok := 0
	for i := 0; i < 10; i++ {
		a, err := l.Allow(ctx, 7)
		if err != nil {
			t.Fatal(err)
		}
		if a {
			ok++
		}
	}
	if ok != 3 {
		t.Fatalf("허용 3번: %d", ok)
	}
	var calls int
	if err := l.DB.QueryRow(`SELECT calls FROM mcp_rate_window WHERE token_id = 7`).Scan(&calls); err != nil || calls != 3 {
		t.Fatalf("카운터는 한도에서 멈춤: %d %v", calls, err)
	}
	// 다른 토큰은 독립
	if a, _ := l.Allow(ctx, 8); !a {
		t.Fatal("다른 토큰은 영향 없음")
	}
}

func TestOldWindowsAreCleaned(t *testing.T) {
	l := newLimiterDB(t)
	ctx := context.Background()
	if _, err := l.DB.Exec(`INSERT INTO mcp_rate_window (token_id, window_start, calls) VALUES (9, 1, 5)`); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Allow(ctx, 7); err != nil {
		t.Fatal(err)
	}
	var n int
	_ = l.DB.QueryRow(`SELECT COUNT(*) FROM mcp_rate_window WHERE token_id = 9`).Scan(&n)
	if n != 0 {
		t.Fatal("오래된 창은 지운다")
	}
}
