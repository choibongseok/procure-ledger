package dbutil

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestRebind(t *testing.T) {
	cases := []struct{ in, want string }{
		{"SELECT 1", "SELECT 1"},
		{"SELECT * FROM t WHERE a = ? AND b = ?", "SELECT * FROM t WHERE a = $1 AND b = $2"},
		{"SELECT '?' , ?", "SELECT '?' , $1"},
		{"SELECT 'it''s ?', ?", "SELECT 'it''s ?', $1"},
		{`SELECT "col?" FROM t WHERE x = ?`, `SELECT "col?" FROM t WHERE x = $1`},
		{"SELECT ? -- 뭐? \n, ?", "SELECT $1 -- 뭐? \n, $2"},
		{"SELECT /* ? */ ?", "SELECT /* ? */ $1"},
		{"INSERT INTO t (a,b) VALUES (?,?)", "INSERT INTO t (a,b) VALUES ($1,$2)"},
		{"SELECT '미완료 따옴표 ?", "SELECT '미완료 따옴표 ?"},
		{"CREATE FUNCTION f() RETURNS int AS $fn$ SELECT ? $fn$; SELECT ?", "CREATE FUNCTION f() RETURNS int AS $fn$ SELECT ? $fn$; SELECT $1"},
		{"SELECT $$a?$$, ?", "SELECT $$a?$$, $1"},
		{"SELECT ?1, ?2, ?1 WHERE x = ?2", "SELECT $1, $2, $1 WHERE x = $2"},
	}
	for _, c := range cases {
		if got := Rebind(c.in); got != c.want {
			t.Errorf("Rebind(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	if Rebind("SELECT 1") != "SELECT 1" {
		t.Fatal("자리표시자가 없으면 그대로여야 한다")
	}
}

func TestParseTarget(t *testing.T) {
	ok := []struct {
		path, url string
		kind      Kind
		wantPath  string
	}{
		{"data/erp.db", "", KindSQLite, "data/erp.db"},
		{"data/erp.db", "postgres://u:p@h/db", KindPostgres, ""},
		{"data/erp.db", "postgresql://u:p@h/db", KindPostgres, ""},
		{"data/erp.db", "POSTGRES://u:p@h/db", KindPostgres, ""},
		{"data/erp.db", "sqlite:/var/x.db", KindSQLite, "/var/x.db"},
		{"data/erp.db", "file:x.db", KindSQLite, "x.db"},
		{"data/erp.db", "/var/lib/erp.db", KindSQLite, "/var/lib/erp.db"},
	}
	for _, c := range ok {
		got, err := ParseTarget(c.path, c.url)
		if err != nil {
			t.Fatalf("%q: %v", c.url, err)
		}
		if got.Kind != c.kind || got.Path != c.wantPath {
			t.Errorf("%q: %+v", c.url, got)
		}
	}
	for _, bad := range []string{"postgre://u@h/db", "pgsql://h/db", "postgress://h/db", "pg://h/db", "sqlite:", "mysql://u:p@h/db", "mongodb://h/db", "http://h/x"} {
		if _, err := ParseTarget("x", bad); err == nil {
			t.Errorf("%q 는 거부되어야 한다", bad)
		}
	}
	tg, _ := ParseTarget("custom.db", "postgres://h/db")
	if !tg.IgnoredPath("custom.db", DefaultSQLitePath) || tg.IgnoredPath(DefaultSQLitePath, DefaultSQLitePath) {
		t.Error("IgnoredPath")
	}
}

func TestRedactHidesPassword(t *testing.T) {
	raw := "postgres://pl:s3cr3t-pw@db.example.com:5432/procure?sslmode=verify-full&password=zzz"
	got := Redact(raw)
	if strings.Contains(got, "s3cr3t-pw") || strings.Contains(got, "zzz") {
		t.Fatalf("비밀번호가 남았다: %s", got)
	}
	if !strings.Contains(got, "db.example.com") || !strings.Contains(got, "procure") {
		t.Fatalf("호스트·DB 이름은 보여야 한다: %s", got)
	}
	tg, _ := ParseTarget("", raw)
	if strings.Contains(tg.Describe(), "s3cr3t-pw") {
		t.Fatal("Describe 에 비밀번호")
	}
	if strings.Contains(Redact("not a url ::: s3cr3t"), "s3cr3t") {
		t.Fatal("해석 불가 값은 전부 가려야 한다")
	}
}

func TestCheckTLS(t *testing.T) {
	bad := []string{
		"postgres://u:p@db.example.com/db",
		"postgres://u:p@db.example.com/db?sslmode=disable",
		"postgres://u:p@db.example.com/db?sslmode=prefer",
		"postgres://u:p@10.0.0.5/db?sslmode=allow",
		"postgres://u:p@127.0.0.1/db",
		"postgres://u:p@localhost/db?sslmode=disable",
	}
	for _, u := range bad {
		if err := CheckTLS(u, false); err == nil {
			t.Errorf("거부되어야 한다: %s", u)
		}
	}
	// 허용 변수는 루프백만 푼다.
	if err := CheckTLS("postgres://u:p@127.0.0.1/db", true); err != nil {
		t.Errorf("루프백 + 허용 변수: %v", err)
	}
	if err := CheckTLS("postgres://u:p@db.example.com/db", true); err == nil {
		t.Error("허용 변수로 원격 평문을 풀 수 없다")
	}
	for _, u := range []string{
		"postgres://u:p@db.example.com/db?sslmode=verify-full",
		"postgres://u:p@db.example.com/db?sslmode=require",
		"postgres://u@/db?host=/tmp/pgsock",
		"postgres://u:p@localhost/db?sslmode=require",
	} {
		if err := CheckTLS(u, false); err != nil {
			t.Errorf("허용되어야 한다: %s: %v", u, err)
		}
	}
	if err := CheckTLS("postgres://u@/db?host=/tmp/a,db.example.com", false); err == nil {
		t.Error("여러 호스트 중 원격 평문이 있으면 거부")
	}
}

func TestPGErrorClassification(t *testing.T) {
	mk := func(code, msg string) error { return fmt.Errorf("wrap: %w", &pgconn.PgError{Code: code, Message: msg}) }
	if !IsUnique(mk("23505", "dup")) || IsUnique(mk("23503", "fk")) {
		t.Error("unique")
	}
	if !IsForeignKey(mk("23503", "")) || !IsCheck(mk("23514", "")) || !IsNotNull(mk("23502", "")) {
		t.Error("constraint codes")
	}
	if !IsGuard(mk("P0001", "audit_logs 는 추가 전용입니다"), "추가 전용") || IsGuard(mk("P0001", "다른 메시지"), "추가 전용") {
		t.Error("guard")
	}
	if !IsRetryable(mk("40001", "")) || !IsRetryable(mk("40P01", "")) || IsRetryable(mk("23505", "")) {
		t.Error("retryable")
	}
	if !IsLockTimeout(mk("55P03", "")) || !IsLockTimeout(mk("57014", "")) {
		t.Error("lock timeout")
	}
	// SQLite 문구
	if !IsUnique(errors.New("constraint failed: UNIQUE constraint failed: users.email (2067)")) {
		t.Error("sqlite unique")
	}
	if !IsGuard(errors.New("audit_logs 는 추가 전용입니다 (19)"), "추가 전용") {
		t.Error("sqlite guard")
	}
	if IsUnique(nil) || IsGuard(nil, "x") || IsRetryable(nil) {
		t.Error("nil")
	}
}

func TestLockKeyStable(t *testing.T) {
	if LockKey(LockAuditChain) == LockKey(LockServerInstance) {
		t.Fatal("서로 다른 이름은 다른 키")
	}
	if LockKey(LockAuditChain) != LockKey(LockAuditChain) {
		t.Fatal("같은 이름은 같은 키")
	}
	// 고정값: 키가 바뀌면 이미 실행 중인 다른 버전의 서버와 잠금이 어긋난다.
	if got := LockKey("procure-ledger:audit-chain"); got != LockKey(LockAuditChain) {
		t.Fatal("상수 불일치")
	}
}

// SQLite 에서 공통 부분집합 조각이 실제로 동작하는지(버전 포함) 확인한다.
func TestSQLiteDialectFragments(t *testing.T) {
	ctx := context.Background()
	db, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	d := SQLite
	var v string
	if err := db.QueryRow(`SELECT sqlite_version()`).Scan(&v); err != nil {
		t.Fatal(err)
	}
	t.Logf("sqlite %s", v)

	var now string
	if err := db.QueryRow(`SELECT ` + d.NowUTC()).Scan(&now); err != nil || len(now) != 20 || !strings.HasSuffix(now, "Z") || now[10] != 'T' {
		t.Fatalf("NowUTC = %q, %v", now, err)
	}
	var kst string
	if err := db.QueryRow(`SELECT ` + d.KSTDate("'2026-01-01T16:00:00Z'")).Scan(&kst); err != nil || kst != "2026-01-02" {
		t.Fatalf("KSTDate = %q, %v", kst, err)
	}
	var diff int
	if err := db.QueryRow(`SELECT ` + d.DayDiff("'2026-03-10'", "'2026-03-01'")).Scan(&diff); err != nil || diff != 9 {
		t.Fatalf("DayDiff = %d, %v", diff, err)
	}
	var g int
	if err := db.QueryRow(`SELECT ` + d.Greatest("3", "7")).Scan(&g); err != nil || g != 7 {
		t.Fatalf("Greatest = %d, %v", g, err)
	}
	if d.RowLock() != "" || d.ShareLock() != "" || d.HasExplicitLocks() {
		t.Fatal("SQLite 는 명시 잠금을 쓰지 않는다")
	}

	if _, err := db.Exec(`CREATE TABLE t (id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT UNIQUE NOT NULL, n INTEGER)`); err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	id, err := InsertID(ctx, tx, `INSERT INTO t (name, n) VALUES (?, ?)`, "a", 1)
	if err != nil || id != 1 {
		t.Fatalf("InsertID = %d, %v", id, err)
	}
	// 공통 부분집합: ON CONFLICT DO NOTHING, IS DISTINCT FROM, string_agg
	res, err := tx.ExecContext(ctx, `INSERT INTO t (name, n) VALUES (?, ?)`+OnConflictDoNothing, "a", 2)
	if err != nil {
		t.Fatal(err)
	}
	if n, _ := res.RowsAffected(); n != 0 {
		t.Fatalf("중복은 무시되어야 한다: %d", n)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO t (name, n) VALUES ('b', NULL)`); err != nil {
		t.Fatal(err)
	}
	var cnt int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM t WHERE n IS DISTINCT FROM 1`).Scan(&cnt); err != nil || cnt != 1 {
		t.Fatalf("IS DISTINCT FROM = %d, %v", cnt, err)
	}
	var agg string
	if err := tx.QueryRowContext(ctx, `SELECT string_agg(name, ',') FROM (SELECT name FROM t ORDER BY id)`).Scan(&agg); err != nil || agg != "a,b" {
		t.Fatalf("string_agg = %q, %v", agg, err)
	}
	// SQLite 에서는 잠금이 필요 없으므로 AcquireTxLock 은 아무 일도 하지 않는다(pg_advisory_xact_lock 이 없는 DB 에서도 성공).
	if err := AcquireTxLock(ctx, tx, SQLite, LockAuditChain); err != nil {
		t.Fatalf("SQLite 잠금 호출은 무동작이어야 한다: %v", err)
	}
}

func TestNewTxManagerSQLiteUnchanged(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	m := NewTxManager(db, SQLite)
	if fmt.Sprintf("%T", m) != "contract.SQLTxManager" {
		t.Fatalf("SQLite 는 기존 SQLTxManager 여야 한다: %T", m)
	}
	if fmt.Sprintf("%T", NewTxManager(db, nil)) != "contract.SQLTxManager" {
		t.Fatal("방언이 없으면 SQLite 로 본다")
	}
	if fmt.Sprintf("%T", NewTxManager(db, Postgres)) != "dbutil.PGTxManager" {
		t.Fatal("PG 는 PGTxManager")
	}
}
