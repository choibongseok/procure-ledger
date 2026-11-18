package dbutil

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

// PGOptions 는 PostgreSQL 연결 풀 설정이다.
type PGOptions struct {
	MaxConns int // 0 이면 10
	// AllowInsecure 는 루프백 TCP 의 평문 연결을 허용한다(PL_DB_ALLOW_INSECURE=1).
	AllowInsecure bool
	// LockTimeout·StatementTimeout 은 연결마다 거는 제한이다(0 이면 10초·60초).
	LockTimeout      time.Duration
	StatementTimeout time.Duration
}

// OpenPG 는 PostgreSQL 에 연결한다. 모든 SQL 의 `?` 는 연결 단계에서 `$n` 으로 바뀐다(Rebind) —
// 그래서 서비스·저장소 코드는 *sql.DB 와 `?` 를 그대로 쓴다.
// 연결마다 UTC 시간대와 잠금 대기·문장 시간 제한을 건다.
func OpenPG(ctx context.Context, rawURL string, o PGOptions) (*sql.DB, error) {
	if err := CheckTLS(rawURL, o.AllowInsecure); err != nil {
		return nil, err
	}
	cfg, err := pgx.ParseConfig(rawURL)
	if err != nil {
		// pgx 의 오류 문구에 연결 문자열 조각이 들어갈 수 있어 값을 가려서 돌려준다.
		return nil, fmt.Errorf("PL_DB_URL 을 해석할 수 없습니다(%s)", Redact(rawURL))
	}
	if o.MaxConns <= 0 {
		o.MaxConns = 10
	}
	if o.LockTimeout <= 0 {
		o.LockTimeout = 10 * time.Second
	}
	if o.StatementTimeout <= 0 {
		o.StatementTimeout = 60 * time.Second
	}
	after := stdlib.OptionAfterConnect(func(ctx context.Context, c *pgx.Conn) error {
		for _, s := range []string{
			"SET TIME ZONE 'UTC'",
			fmt.Sprintf("SET lock_timeout = %d", o.LockTimeout.Milliseconds()),
			fmt.Sprintf("SET statement_timeout = %d", o.StatementTimeout.Milliseconds()),
		} {
			if _, err := c.Exec(ctx, s); err != nil {
				return err
			}
		}
		return nil
	})
	db := sql.OpenDB(rebindConnector{inner: stdlib.GetConnector(*cfg, after)})
	db.SetMaxOpenConns(o.MaxConns)
	db.SetMaxIdleConns((o.MaxConns + 1) / 2)
	db.SetConnMaxLifetime(30 * time.Minute)
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("PostgreSQL 에 연결하지 못했습니다(%s): %s", Redact(rawURL), sanitizeErr(err, rawURL))
	}
	SetActive(Postgres)
	return db, nil
}

// sanitizeErr 는 오류 문자열에서 연결 문자열 원문(비밀번호 포함)을 지운다.
func sanitizeErr(err error, rawURL string) string {
	if rawURL == "" {
		return err.Error()
	}
	return strings.ReplaceAll(err.Error(), rawURL, Redact(rawURL))
}

// rebindConnector 는 pgx 커넥터를 감싸 연결이 보내는 SQL 의 자리표시자를 바꾼다.
type rebindConnector struct{ inner driver.Connector }

func (c rebindConnector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := c.inner.Connect(ctx)
	if err != nil {
		return nil, err
	}
	sc, ok := conn.(*stdlib.Conn)
	if !ok {
		_ = conn.Close()
		return nil, fmt.Errorf("dbutil: 예상하지 못한 드라이버 연결 형식 %T", conn)
	}
	return &rebindConn{Conn: sc}, nil
}

func (c rebindConnector) Driver() driver.Driver { return c.inner.Driver() }

// rebindConn 은 *stdlib.Conn 의 나머지 기능(트랜잭션·핑·세션 재설정·값 변환)을 그대로 쓰고 SQL 을 받는 메서드만 가로챈다.
type rebindConn struct{ *stdlib.Conn }

func (c *rebindConn) ExecContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	return c.Conn.ExecContext(ctx, Rebind(q), args)
}

func (c *rebindConn) QueryContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	return c.Conn.QueryContext(ctx, Rebind(q), args)
}

func (c *rebindConn) PrepareContext(ctx context.Context, q string) (driver.Stmt, error) {
	return c.Conn.PrepareContext(ctx, Rebind(q))
}

func (c *rebindConn) Prepare(q string) (driver.Stmt, error) {
	return c.Conn.Prepare(Rebind(q))
}

// DialectOf 는 열린 DB 의 방언이다: PostgreSQL 드라이버(pgx stdlib)로 열었으면 PostgreSQL, 아니면 SQLite.
// 시험 도우미와 방언을 모르는 호출자(migrations.Apply)가 쓴다.
func DialectOf(db *sql.DB) Dialect {
	if db == nil {
		return For(KindSQLite)
	}
	if _, ok := db.Driver().(*stdlib.Driver); ok {
		return For(KindPostgres)
	}
	return For(KindSQLite)
}
