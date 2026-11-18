package dbutil

// 방언(B37). SQL 은 지금처럼 손으로 쓰고, 두 DB 가 같은 뜻으로 받는 공통 부분집합을 먼저 쓴다
// (ON CONFLICT DO NOTHING, IS [NOT] DISTINCT FROM, string_agg, RETURNING id).
// 공통 부분집합으로 안 되는 소수 지점만 이 객체가 SQL 조각을 돌려준다.
// 규칙: 조각은 코드 상수와 열 이름 같은 식별자에만 쓰고, 값(사용자 입력)은 반드시 자리표시자로 넘긴다.

import "fmt"

// Kind 는 DB 종류다.
type Kind string

const (
	KindSQLite   Kind = "sqlite"
	KindPostgres Kind = "postgres"
)

// Dialect 는 DB 종류별로 갈리는 SQL 조각이다. 구현은 SQLite 와 Postgres 두 개뿐이다.
type Dialect interface {
	Kind() Kind
	// Rebind 는 `?` 자리표시자를 이 DB 의 형식으로 바꾼다(문자열·식별자·주석 안의 ? 는 그대로).
	Rebind(query string) string
	// NowUTC 는 현재 UTC 시각을 "2006-01-02T15:04:05Z" 형식 문자열로 만드는 SQL 식이다.
	NowUTC() string
	// KSTDate 는 UTC 시각 문자열 식 expr 을 한국 날짜 "YYYY-MM-DD" 로 바꾸는 SQL 식이다(expr 은 코드 상수·열 이름).
	KSTDate(expr string) string
	// DayDiff 는 날짜 문자열("YYYY-MM-DD") 두 식의 차(a - b)를 정수 일수로 만드는 SQL 식이다.
	DayDiff(a, b string) string
	// Greatest 는 스칼라 최댓값 SQL 식이다(SQLite max(a,b), PG GREATEST(a,b)).
	Greatest(a, b string) string
	// RowLock 은 행 잠금 접미사다(PG 는 " FOR UPDATE", SQLite 는 빈 문자열: 연결 하나가 직렬화한다).
	RowLock() string
	// ShareLock 은 공유 행 잠금 접미사다(PG " FOR SHARE", SQLite 빈 문자열).
	ShareLock() string
	// HasExplicitLocks 는 이 DB 가 명시적 잠금을 필요로 하는지(PG 만 참)다.
	HasExplicitLocks() bool
}

// SQLite 와 Postgres 는 방언 값이다.
var (
	SQLite   Dialect = sqliteDialect{}
	Postgres Dialect = pgDialect{}
)

// For 는 종류에 맞는 방언을 돌려준다.
func For(k Kind) Dialect {
	if k == KindPostgres {
		return Postgres
	}
	return SQLite
}

// 공통 부분집합 조각(두 DB 가 같은 문자열을 받는다).
const (
	// OnConflictDoNothing 은 INSERT 뒤에 붙인다(SQLite 3.24+, PG 9.5+). `INSERT OR IGNORE` 를 대신한다.
	OnConflictDoNothing = " ON CONFLICT DO NOTHING"
	// ReturningID 는 INSERT 뒤에 붙여 새 id 를 받는다(SQLite 3.35+, PG). LastInsertId 를 대신한다.
	ReturningID = " RETURNING id"
)

type sqliteDialect struct{}

func (sqliteDialect) Kind() Kind              { return KindSQLite }
func (sqliteDialect) Rebind(q string) string  { return q }
func (sqliteDialect) NowUTC() string          { return `strftime('%Y-%m-%dT%H:%M:%SZ','now')` }
func (sqliteDialect) KSTDate(e string) string { return fmt.Sprintf("date(%s, '+9 hours')", e) }
func (sqliteDialect) DayDiff(a, b string) string {
	return fmt.Sprintf("CAST(julianday(%s) - julianday(%s) AS INTEGER)", a, b)
}
func (sqliteDialect) Greatest(a, b string) string { return fmt.Sprintf("max(%s, %s)", a, b) }
func (sqliteDialect) RowLock() string             { return "" }
func (sqliteDialect) ShareLock() string           { return "" }
func (sqliteDialect) HasExplicitLocks() bool      { return false }

type pgDialect struct{}

func (pgDialect) Kind() Kind             { return KindPostgres }
func (pgDialect) Rebind(q string) string { return Rebind(q) }
func (pgDialect) NowUTC() string {
	return `to_char(now() AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS"Z"')`
}
func (pgDialect) KSTDate(e string) string {
	return fmt.Sprintf("to_char((%s)::timestamp AT TIME ZONE 'UTC' AT TIME ZONE 'Asia/Seoul', 'YYYY-MM-DD')", e)
}
func (pgDialect) DayDiff(a, b string) string  { return fmt.Sprintf("((%s)::date - (%s)::date)", a, b) }
func (pgDialect) Greatest(a, b string) string { return fmt.Sprintf("GREATEST(%s, %s)", a, b) }
func (pgDialect) RowLock() string             { return " FOR UPDATE" }
func (pgDialect) ShareLock() string           { return " FOR SHARE" }
func (pgDialect) HasExplicitLocks() bool      { return true }
