package dbtest

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"net/url"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/dbutil"
	"modernc.org/sqlite"
)

// TxGuard 는 "트랜잭션 안에서 풀 연결을 쓰는" 코드를 잡는 시험용 장치다(B50 판정 후속).
//
// 운영 SQLite 는 쓰기 연결이 하나라, 트랜잭션이 열린 채 다른 연결로 읽으면 영구 대기한다. PG 는 풀이 새 연결을 내줘서 통과하므로 시험이 놓친다.
// OpenGuarded 는 같은 SQLite 를 연결 여러 개로 열되 드라이버를 감싸, 트랜잭션이 열려 있는 동안 그 트랜잭션 밖 연결에서 실행되는
// 모든 질의·준비·새 트랜잭션 시작을 위반으로 기록한다(운영에서는 이 지점에서 멈춘다). 시험은 끝에서 Check 로 위반이 없음을 확인한다.
//
// 한계: 연결 단위로 판정하므로 시험이 순차적이어야 한다(다른 고루틴의 정상 질의가 위반으로 보인다). 파일 DB 라야 연결끼리 같은 DB 를 본다.
type TxGuard struct {
	mu         sync.Mutex
	violations []string
	txOpen     atomic.Int32
}

// OpenGuarded 는 감시되는 파일 SQLite 를 연다. PG 모드에서는 호출하지 않는다(PG 는 이 위험이 없다).
func OpenGuarded(t testing.TB) (*sql.DB, *TxGuard) {
	t.Helper()
	if PG() {
		t.Fatal("OpenGuarded 는 SQLite 전용이다: 호출한 시험이 먼저 SkipPG 로 PG 모드를 건너뛴다")
	}
	q := url.Values{}
	q.Add("_pragma", "foreign_keys(1)")
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "journal_mode(WAL)")
	dsn := "file:" + filepath.Join(t.TempDir(), "guard.db") + "?" + q.Encode()
	dbutil.SetActive(dbutil.SQLite)
	g := &TxGuard{}
	db := sql.OpenDB(&guardConnector{g: g, dsn: dsn})
	db.SetMaxOpenConns(4) // 풀 질의가 막히지 않고 지나가야 위반으로 기록된다
	if err := db.Ping(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db, g
}

// Violations 는 지금까지 기록된 위반이다(발생 위치 스택 포함).
func (g *TxGuard) Violations() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.violations...)
}

// Check 는 위반이 있으면 시험을 실패시킨다.
func (g *TxGuard) Check(t testing.TB) {
	t.Helper()
	for _, v := range g.Violations() {
		t.Errorf("트랜잭션 안에서 풀 연결을 썼다(운영 SQLite 에서는 영구 대기):\n%s", v)
	}
}

func (g *TxGuard) report(kind, query string) error {
	if g.txOpen.Load() == 0 {
		return nil
	}
	q := strings.Join(strings.Fields(query), " ")
	if len(q) > 160 {
		q = q[:160] + "..."
	}
	msg := fmt.Sprintf("%s: %s\n%s", kind, q, debug.Stack())
	g.mu.Lock()
	g.violations = append(g.violations, msg)
	g.mu.Unlock()
	return fmt.Errorf("txguard: 트랜잭션 안에서 풀 연결 사용(%s)", kind)
}

type guardConnector struct {
	g   *TxGuard
	dsn string
}

func (c *guardConnector) Connect(context.Context) (driver.Conn, error) {
	inner, err := (&sqlite.Driver{}).Open(c.dsn)
	if err != nil {
		return nil, err
	}
	return &guardConn{Conn: inner, g: c.g}, nil
}

func (c *guardConnector) Driver() driver.Driver { return &sqlite.Driver{} }

type guardConn struct {
	driver.Conn
	g    *TxGuard
	inTx atomic.Bool
}

func (c *guardConn) check(kind, query string) error {
	if c.inTx.Load() {
		return nil
	}
	return c.g.report(kind, query)
}

func (c *guardConn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	if err := c.check("준비", query); err != nil {
		return nil, err
	}
	return c.Conn.(driver.ConnPrepareContext).PrepareContext(ctx, query)
}

func (c *guardConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if err := c.check("실행", query); err != nil {
		return nil, err
	}
	return c.Conn.(driver.ExecerContext).ExecContext(ctx, query, args)
}

func (c *guardConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if err := c.check("조회", query); err != nil {
		return nil, err
	}
	return c.Conn.(driver.QueryerContext).QueryContext(ctx, query, args)
}

func (c *guardConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	if err := c.g.report("중첩 트랜잭션", "BEGIN"); err != nil {
		return nil, err
	}
	tx, err := c.Conn.(driver.ConnBeginTx).BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	c.inTx.Store(true)
	c.g.txOpen.Add(1)
	return &guardTx{Tx: tx, c: c}, nil
}

func (c *guardConn) Begin() (driver.Tx, error) {
	return c.BeginTx(context.Background(), driver.TxOptions{})
}

func (c *guardConn) Ping(ctx context.Context) error {
	if p, ok := c.Conn.(driver.Pinger); ok {
		return p.Ping(ctx)
	}
	return nil
}

func (c *guardConn) ResetSession(ctx context.Context) error {
	if r, ok := c.Conn.(driver.SessionResetter); ok {
		return r.ResetSession(ctx)
	}
	return nil
}

func (c *guardConn) IsValid() bool {
	if v, ok := c.Conn.(driver.Validator); ok {
		return v.IsValid()
	}
	return true
}

type guardTx struct {
	driver.Tx
	c    *guardConn
	done atomic.Bool
}

func (x *guardTx) finish() {
	if x.done.CompareAndSwap(false, true) {
		x.c.inTx.Store(false)
		x.c.g.txOpen.Add(-1)
	}
}

func (x *guardTx) Commit() error   { defer x.finish(); return x.Tx.Commit() }
func (x *guardTx) Rollback() error { defer x.finish(); return x.Tx.Rollback() }
