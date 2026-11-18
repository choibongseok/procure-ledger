// Package mcpusage 는 AI 연결(MCP) 사용 모니터링(B13)이다.
//
//   - RateLimiter: 개인 토큰별 분당 호출 한도와 사용자별 합산 분당 한도(B17). DB(mcp_rate_window, mcp_user_rate_window)로 센다.
//     한도 초과 거부는 토큰·분 단위로 센다(denied_count). 첫 거부는 감사에 바로 남기고, 창이 닫힌 뒤 거부가 2회 이상이었으면
//     요약 1건을 Settle 이 남긴다(감사 로그는 추가 전용이라 기존 행을 고치지 않는다).
//   - Service: 감사 로그(channel=mcp)에서 도구별·토큰별 호출 수·거부·지연을 집계한다. 읽기 전용이고 감사 레코드를 만들지 않는다.
package mcpusage

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
)

// ScopeToken·ScopeUser 는 거부가 어느 한도에서 났는지다.
const (
	ScopeToken = "token"
	ScopeUser  = "user"
)

// SummaryAction 은 창 요약 감사 기록의 동작 이름이다.
const SummaryAction = "mcp.rate_limited_summary"

// RateLimiter 는 개인 토큰 하나당 분당 PerMin 회, 사용자 한 명당(토큰 합산) 분당 PerUserMin 회로 호출을 제한한다(0 이하는 해당 한도 없음).
// 창은 unix 분 단위 고정 창이고, 카운터는 DB 에 있어 서버(/mcp)와 erp-mcp 가 같은 DB 를 쓰면 한도가 합쳐서 적용된다.
// 한도를 넘은 호출은 카운트를 올리지 않는다.
type RateLimiter struct {
	DB         *sql.DB
	PerMin     int
	PerUserMin int
	Audit      audit.Recorder   // Settle 이 요약 기록을 남길 때 쓴다. nil 이면 Settle 은 아무것도 하지 않는다
	Now        func() time.Time // 테스트용. 비면 time.Now

	lastClean  atomic.Int64 // 마지막으로 오래된 창을 지운 unix 분
	lastSettle atomic.Int64 // 마지막으로 지난 창을 정산한 unix 분
}

// NewRateLimiter 는 두 한도가 모두 0 이하면 nil 을 돌려준다(한도 없음).
func NewRateLimiter(db *sql.DB, perMin, perUserMin int) *RateLimiter {
	if perMin <= 0 && perUserMin <= 0 {
		return nil
	}
	return &RateLimiter{DB: db, PerMin: max(perMin, 0), PerUserMin: max(perUserMin, 0)}
}

// Limit 은 토큰별 분당 한도다.
func (l *RateLimiter) Limit() int {
	if l == nil {
		return 0
	}
	return l.PerMin
}

// UserLimit 은 사용자별 합산 분당 한도다.
func (l *RateLimiter) UserLimit() int {
	if l == nil {
		return 0
	}
	return l.PerUserMin
}

// Enabled 는 어느 한도든 켜져 있는지다.
func (l *RateLimiter) Enabled() bool { return l != nil && (l.PerMin > 0 || l.PerUserMin > 0) }

// Call 은 한도를 세는 호출의 주체다. UserID 가 0 이면 사용자 합산 한도는 적용하지 않는다.
type Call struct {
	TokenID int64
	UserID  int64
}

// Decision 은 한도 판정이다. 거부이면 Scope 가 어느 한도인지, Limit 이 그 한도, Denied 가 이 토큰·분 창의 누적 거부 횟수(이번 포함)다.
// Denied 가 1 이면 이 창의 첫 거부라서 호출자가 감사 기록을 바로 남긴다.
type Decision struct {
	Allowed bool
	Scope   string
	Limit   int
	Denied  int
}

// retainMinutes 보다 오래된 창은 지운다. 현재 창만 의미가 있고 나머지는 운영 카운터라 남길 이유가 없다.
// 정산 안 된 요약 대상 창은 unsettledRetainMinutes 동안 남긴다.
const (
	retainMinutes          = 60
	unsettledRetainMinutes = 7 * 24 * 60
)

func (l *RateLimiter) minute() int64 {
	now := time.Now
	if l.Now != nil {
		now = l.Now
	}
	return now().Unix() / 60
}

// Allow 는 토큰 한도만 보는 옛 인터페이스(deps.MCPRateLimiter)용이다. 사용자 합산 한도는 Check 로만 적용된다.
func (l *RateLimiter) Allow(ctx context.Context, tokenID int64) (bool, error) {
	d, err := l.Check(ctx, Call{TokenID: tokenID})
	return d.Allowed, err
}

// Check 는 이번 호출을 세고 한도 안이면 Allowed 다. 사용자 합산 → 토큰 순으로 한 트랜잭션에서 올리고, 어느 쪽이든 넘으면
// 롤백해서 둘 다 카운트를 올리지 않는다. 여러 프로세스가 같은 DB 를 써도 한도를 넘어 세지 않는다.
func (l *RateLimiter) Check(ctx context.Context, c Call) (Decision, error) {
	if !l.Enabled() {
		return Decision{Allowed: true}, nil
	}
	minute := l.minute()
	denied := Decision{}
	tx, err := l.DB.BeginTx(ctx, nil)
	if err != nil {
		return Decision{}, err
	}
	defer tx.Rollback() //nolint:errcheck
	if l.PerUserMin > 0 && c.UserID != 0 {
		ok, err := bump(ctx, tx, `INSERT INTO mcp_user_rate_window (user_id, window_start, calls) VALUES (?, ?, 1)
			 ON CONFLICT (user_id, window_start) DO UPDATE SET calls = mcp_user_rate_window.calls + 1 WHERE mcp_user_rate_window.calls < ?`, c.UserID, minute, l.PerUserMin)
		if err != nil {
			return Decision{}, err
		}
		if !ok {
			denied = Decision{Scope: ScopeUser, Limit: l.PerUserMin}
		}
	}
	if denied.Scope == "" && l.PerMin > 0 && c.TokenID != 0 {
		ok, err := bump(ctx, tx, `INSERT INTO mcp_rate_window (token_id, window_start, calls, user_id) VALUES (?, ?, 1, ?)
			 ON CONFLICT (token_id, window_start) DO UPDATE SET calls = mcp_rate_window.calls + 1, user_id = excluded.user_id WHERE mcp_rate_window.calls < ?`, c.TokenID, minute, c.UserID, l.PerMin)
		if err != nil {
			return Decision{}, err
		}
		if !ok {
			denied = Decision{Scope: ScopeToken, Limit: l.PerMin}
		}
	}
	if denied.Scope == "" {
		if err := tx.Commit(); err != nil {
			return Decision{}, err
		}
		l.cleanup(ctx, minute)
		return Decision{Allowed: true}, nil
	}
	_ = tx.Rollback() // 거부는 카운트를 올리지 않는다
	if c.TokenID == 0 {
		return denied, nil // 토큰이 없는 호출(세션)은 거부 횟수를 묶을 키가 없다
	}
	n, err := l.recordDenial(ctx, c, minute)
	if err != nil {
		return Decision{}, err
	}
	denied.Denied = n
	l.cleanup(ctx, minute)
	return denied, nil
}

func bump(ctx context.Context, tx *sql.Tx, q string, args ...any) (bool, error) {
	res, err := tx.ExecContext(ctx, q, args...)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// recordDenial 은 토큰·분 창의 거부 횟수를 1 올리고 올린 뒤의 값을 돌려준다.
func (l *RateLimiter) recordDenial(ctx context.Context, c Call, minute int64) (int, error) {
	tx, err := l.DB.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback() //nolint:errcheck
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO mcp_rate_window (token_id, window_start, calls, denied_count, user_id) VALUES (?, ?, 0, 1, ?)
		 ON CONFLICT (token_id, window_start) DO UPDATE SET denied_count = mcp_rate_window.denied_count + 1, user_id = excluded.user_id`,
		c.TokenID, minute, c.UserID); err != nil {
		return 0, err
	}
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT denied_count FROM mcp_rate_window WHERE token_id = ? AND window_start = ?`, c.TokenID, minute).Scan(&n); err != nil {
		return 0, err
	}
	return n, tx.Commit()
}

// cleanup 은 프로세스마다 분당 한 번만 오래된 창을 지운다. 정산 안 된 요약 대상(거부 2회 이상)은 더 오래 남긴다.
// 실패해도 한도 판정에는 영향이 없다.
func (l *RateLimiter) cleanup(ctx context.Context, minute int64) {
	last := l.lastClean.Load()
	if last == minute || !l.lastClean.CompareAndSwap(last, minute) {
		return
	}
	_, _ = l.DB.ExecContext(ctx,
		`DELETE FROM mcp_rate_window WHERE window_start < ? AND (denied_count < 2 OR summarized = 1 OR window_start < ?)`,
		minute-retainMinutes, minute-unsettledRetainMinutes)
	_, _ = l.DB.ExecContext(ctx, `DELETE FROM mcp_user_rate_window WHERE window_start < ?`, minute-retainMinutes)
}

// SettleDue 는 분당 한 번만(프로세스마다) Settle 을 부른다. 호출마다 불러도 싸다.
func (l *RateLimiter) SettleDue(ctx context.Context) {
	if l == nil || l.Audit == nil {
		return
	}
	minute := l.minute()
	last := l.lastSettle.Load()
	if last == minute || !l.lastSettle.CompareAndSwap(last, minute) {
		return
	}
	_, _ = l.Settle(ctx)
}

const settleBatch = 100

// Settle 은 이미 닫힌 창(현재 분 이전) 중 거부가 2회 이상이고 아직 요약하지 않은 토큰·분마다 요약 감사 기록 1건을 남긴다.
// 한 번에 settleBatch 건씩 대상이 없을 때까지 반복하므로, 대상이 많아도 7일 정리에서 요약 없이 지워지지 않는다.
// 요약 표시와 감사 기록은 한 트랜잭션이라 둘 중 하나만 남지 않는다(감사가 실패하면 표시도 되돌아가 다음에 다시 시도한다).
// 거부가 1회뿐인 창은 첫 거부 기록이 전부라 요약이 없다. 처리한 건수를 돌려준다.
func (l *RateLimiter) Settle(ctx context.Context) (int, error) {
	if l == nil || l.Audit == nil {
		return 0, nil
	}
	total := 0
	for {
		n, due, err := l.settleBatch(ctx)
		total += n
		if err != nil {
			return total, err
		}
		if due < settleBatch || n == 0 {
			return total, nil
		}
	}
}

// Run 은 interval 마다 Settle 을 부르고, ctx 가 끝나면 한 번 더 정산하고 돌아온다. 서버 프로세스가 시작 때 고루틴으로 띄우고
// 종료 때 돌아오기를 기다린 뒤 DB 를 닫는다. 그러면 거부 뒤 호출이 더 없어도 요약 감사 행이 남는다.
func (l *RateLimiter) Run(ctx context.Context, interval time.Duration) {
	if l == nil || l.Audit == nil {
		return
	}
	if interval <= 0 {
		interval = time.Minute
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_, _ = l.Settle(sctx)
			cancel()
			return
		case <-t.C:
			_, _ = l.Settle(ctx)
		}
	}
}

// settleBatch 는 대상 최대 settleBatch 건을 정산한다. 처리한 건수와 찾은 대상 수를 돌려준다.
func (l *RateLimiter) settleBatch(ctx context.Context) (done, due int, err error) {
	rows, err := l.DB.QueryContext(ctx,
		`SELECT token_id, window_start, user_id FROM mcp_rate_window
		 WHERE window_start < ? AND denied_count >= 2 AND summarized = 0 ORDER BY window_start LIMIT ?`, l.minute(), settleBatch)
	if err != nil {
		return 0, 0, err
	}
	type key struct{ token, window, user int64 }
	var keys []key
	for rows.Next() {
		var k key
		if err := rows.Scan(&k.token, &k.window, &k.user); err != nil {
			rows.Close()
			return 0, 0, err
		}
		keys = append(keys, k)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, 0, err
	}
	rows.Close()
	for _, k := range keys {
		ok, err := l.settleOne(ctx, k.token, k.window, k.user)
		if err != nil {
			return done, len(keys), err
		}
		if ok {
			done++
		}
	}
	return done, len(keys), nil
}

func (l *RateLimiter) settleOne(ctx context.Context, token, window, user int64) (bool, error) {
	tx, err := l.DB.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback() //nolint:errcheck
	res, err := tx.ExecContext(ctx,
		`UPDATE mcp_rate_window SET summarized = 1 WHERE token_id = ? AND window_start = ? AND summarized = 0`, token, window)
	if err != nil {
		return false, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return false, nil // 다른 프로세스가 먼저 정산했다
	}
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT denied_count FROM mcp_rate_window WHERE token_id = ? AND window_start = ?`, token, window).Scan(&n); err != nil {
		return false, err
	}
	var role string
	if err := tx.QueryRowContext(ctx, `SELECT role FROM users WHERE id = ?`, user).Scan(&role); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	actor := contract.Principal{UserID: user, Role: contract.Role(role), Channel: contract.ChannelMCP, Auth: contract.AuthToken, TokenID: token}
	err = l.Audit.Record(ctx, tx, audit.Entry{
		Actor: actor, Action: SummaryAction, TargetType: audit.TargetItem, TargetID: "-", Result: audit.ResultDenied,
		Detail: "rate_limited_summary 첫 거부 포함 " + strconv.Itoa(n) + "회",
		Diff: []audit.DiffItem{
			{Key: audit.DiffKeyTokenID, New: strconv.FormatInt(token, 10)},
			{Key: audit.DiffKeyDeniedCount, New: strconv.Itoa(n)},
			{Key: audit.DiffKeyWindowStart, New: strconv.FormatInt(window, 10)},
		},
	})
	if err != nil {
		return false, err
	}
	return true, tx.Commit()
}
