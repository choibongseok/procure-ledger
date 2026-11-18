package migrations_test

import "testing"

// 0009: 사용자별 합산 창은 사용자마다 별개 카운터이고, 거부 횟수 칸은 토큰 창·사용자 창 모두 0 에서 시작해 누적된다.
func TestUserRateWindowAndDeniedCount(t *testing.T) {
	db := setup(t)
	const q = `INSERT INTO mcp_user_rate_window (user_id, window_start, calls) VALUES (?,?,1)
		ON CONFLICT(user_id, window_start) DO UPDATE SET calls = calls + 1 WHERE calls < ?`
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
	if res, _ := db.Exec(q, 2, 100, 3); res != nil {
		if n, _ := res.RowsAffected(); n != 1 {
			t.Fatal("다른 사용자는 별개 카운터여야 한다")
		}
	}
	mustExec(t, db, `UPDATE mcp_user_rate_window SET denied_count = denied_count + 1 WHERE user_id=1 AND window_start=100`)
	mustExec(t, db, `INSERT INTO mcp_rate_window (token_id, window_start, calls) VALUES (7, 100, 1)`)
	mustExec(t, db, `UPDATE mcp_rate_window SET denied_count = denied_count + 2 WHERE token_id=7 AND window_start=100`)
	var u, tk int
	_ = db.QueryRow(`SELECT denied_count FROM mcp_user_rate_window WHERE user_id=1 AND window_start=100`).Scan(&u)
	_ = db.QueryRow(`SELECT denied_count FROM mcp_rate_window WHERE token_id=7 AND window_start=100`).Scan(&tk)
	if u != 1 || tk != 2 {
		t.Fatalf("거부 횟수 칸: 사용자 %d(want 1), 토큰 %d(want 2)", u, tk)
	}
	mustExec(t, db, `UPDATE mcp_rate_window SET user_id = 5, summarized = 1 WHERE token_id=7 AND window_start=100`)
	var uid, sum, idx int
	_ = db.QueryRow(`SELECT user_id, summarized FROM mcp_rate_window WHERE token_id=7`).Scan(&uid, &sum)
	_ = db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name='idx_mcp_rate_unsettled'`).Scan(&idx)
	if uid != 5 || sum != 1 || idx != 1 {
		t.Fatalf("user_id=%d summarized=%d idx=%d", uid, sum, idx)
	}
}
