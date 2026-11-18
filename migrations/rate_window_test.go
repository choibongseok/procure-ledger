package migrations_test

import "testing"

// mcp_rate_window 의 원자 증가: 한도(N)에 닿으면 영향 행이 0 이고 카운트는 오르지 않는다.
func TestRateWindowAtomicIncrement(t *testing.T) {
	db := setup(t)
	const q = `INSERT INTO mcp_rate_window (token_id, window_start, calls) VALUES (?,?,1)
		ON CONFLICT(token_id, window_start) DO UPDATE SET calls = calls + 1 WHERE calls < ?`
	allowed := 0
	for i := 0; i < 5; i++ {
		res, err := db.Exec(q, 1, 100, 3)
		if err != nil {
			t.Fatal(err)
		}
		if n, _ := res.RowsAffected(); n == 1 {
			allowed++
		}
	}
	if allowed != 3 {
		t.Fatalf("허용 %d건, want 3", allowed)
	}
	var calls int
	_ = db.QueryRow(`SELECT calls FROM mcp_rate_window WHERE token_id=1 AND window_start=100`).Scan(&calls)
	if calls != 3 {
		t.Fatalf("거부 호출이 카운트를 올렸다: %d", calls)
	}
	if res, _ := db.Exec(q, 2, 100, 3); res != nil {
		if n, _ := res.RowsAffected(); n != 1 {
			t.Fatal("다른 토큰은 별개 카운터여야 한다")
		}
	}
}
