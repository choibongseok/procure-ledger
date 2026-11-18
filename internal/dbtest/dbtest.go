// Package dbtest 는 시험이 데이터베이스를 얻는 한 곳이다(B37 10절 5항, 시험 전용 패키지).
//
// 기본(PL_TEST_DB 비어 있음)은 시험마다 빈 인메모리 SQLite 이고 시험 동작은 이 패키지가 생기기 전과 같다.
// PL_TEST_DB=pg 이면 PostgreSQL 이다: scripts/verify-pg.sh 가 만든 임시 클러스터(PL_TEST_PG_URL)에 패키지마다 한 번
// 마이그레이션을 적용한 템플릿 데이터베이스를 만들고, 시험마다 그것을 복제한 격리 데이터베이스를 준다(시험이 끝나면 지운다).
//
// Open 이 돌려주는 DB 는 SQLite 는 비어 있고 PG 는 이미 마이그레이션이 적용돼 있다. 시험이 이어서 migrations.Apply 를 불러도
// 두 경우 모두 안전하다(적용된 것은 건너뛴다).
package dbtest

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/config"
	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/dbutil"
	"github.com/choibongseok/procure-ledger/internal/dbutil/pgtest"
	"github.com/choibongseok/procure-ledger/migrations"
)

// PG 는 이번 실행이 PostgreSQL 모드인지다(PL_TEST_DB=pg).
func PG() bool { return os.Getenv("PL_TEST_DB") == "pg" }

// Dialect 는 이번 실행의 방언이다.
func Dialect() dbutil.Dialect {
	if PG() {
		return dbutil.For(dbutil.KindPostgres)
	}
	return dbutil.For(dbutil.KindSQLite)
}

// SkipPG 는 SQLite 전용 시험을 PG 모드에서 건너뛴다. 건너뛴 시험은 사유와 함께 출력되고 허용 목록(상한)에 있어야 한다(AC-1741).
func SkipPG(t testing.TB, reason string) {
	t.Helper()
	if PG() {
		enforceSkipAllowed(t)
		t.Skip("PG 모드에서 건너뜀(SQLite 전용): " + reason)
	}
}

var (
	tplOnce sync.Once
	tplName string
	tplErr  error
)

// template 는 이 프로세스의 템플릿 데이터베이스(마이그레이션 적용 완료)를 한 번 만든다.
func template(t testing.TB) string {
	t.Helper()
	admin := pgtest.Require(t)
	tplOnce.Do(func() {
		ctx := context.Background()
		adb, err := dbutil.OpenPG(ctx, admin, dbutil.PGOptions{MaxConns: 2, AllowInsecure: true})
		if err != nil {
			tplErr = err
			return
		}
		defer adb.Close()
		var b [6]byte
		_, _ = rand.Read(b[:])
		tplName = "pltpl_" + hex.EncodeToString(b[:])
		if _, err := adb.ExecContext(ctx, fmt.Sprintf(`CREATE DATABASE %s ENCODING 'UTF8' LC_COLLATE 'C' LC_CTYPE 'C' TEMPLATE template0`, tplName)); err != nil {
			tplErr = err
			return
		}
		u := withDB(admin, tplName)
		tdb, err := dbutil.OpenPG(ctx, u, dbutil.PGOptions{MaxConns: 2, AllowInsecure: true})
		if err != nil {
			tplErr = err
			return
		}
		if err := migrations.ApplyDialect(ctx, tdb, dbutil.For(dbutil.KindPostgres)); err != nil {
			tplErr = err
		}
		_ = tdb.Close() // 복제하려면 템플릿에 접속이 없어야 한다
	})
	if tplErr != nil {
		t.Fatalf("PG 템플릿 데이터베이스: %v", tplErr)
	}
	return tplName
}

func withDB(raw, name string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	u.Path = "/" + name
	return u.String()
}

// URL 은 PG 모드에서 이 시험 전용의 새 데이터베이스(마이그레이션 적용 완료)의 연결 문자열을 돌려준다. SQLite 모드면 빈 문자열이다.
// bootstrap.Build 처럼 연결 문자열(PL_DB_URL)로 DB 를 여는 시험이 쓴다(Build 의 마이그레이션 적용은 건너뛰어진다).
func URL(t testing.TB) string {
	t.Helper()
	if !PG() {
		return ""
	}
	admin := pgtest.Require(t)
	tpl := template(t)
	ctx := context.Background()
	adb, err := dbutil.OpenPG(ctx, admin, dbutil.PGOptions{MaxConns: 2, AllowInsecure: true})
	if err != nil {
		t.Fatalf("관리 연결: %v", err)
	}
	var b [6]byte
	_, _ = rand.Read(b[:])
	name := "pltest_" + hex.EncodeToString(b[:])
	if _, err := adb.ExecContext(ctx, fmt.Sprintf(`CREATE DATABASE %s TEMPLATE %s`, name, tpl)); err != nil {
		_ = adb.Close()
		t.Fatalf("시험 데이터베이스 복제: %v", err)
	}
	t.Cleanup(func() {
		_, _ = adb.ExecContext(context.Background(), fmt.Sprintf(`DROP DATABASE IF EXISTS %s WITH (FORCE)`, name))
		_ = adb.Close()
	})
	return withDB(admin, name)
}

// Open 은 시험용 DB 를 돌려준다(SQLite: 빈 인메모리, PG: 마이그레이션이 적용된 새 데이터베이스). 시험이 끝나면 닫는다.
func Open(t testing.TB) *sql.DB {
	t.Helper()
	if !PG() {
		db, err := dbutil.Open(":memory:")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = db.Close() })
		return db
	}
	db, err := dbutil.OpenPG(context.Background(), URL(t), dbutil.PGOptions{MaxConns: 10, AllowInsecure: true})
	if err != nil {
		t.Fatalf("PG 연결: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// SetEnv 는 환경변수로 DB 를 고르는 시험(config.Load → bootstrap.Build)이 쓴다: SQLite 는 PL_DB_PATH(임시 파일), PG 는 PL_DB_URL 이다.
func SetEnv(t testing.TB) {
	t.Helper()
	tt, ok := t.(interface{ Setenv(string, string) })
	if !ok {
		t.Fatal("dbtest.SetEnv 는 *testing.T 에서만 쓴다")
	}
	tt.Setenv("PL_DB_PATH", filepath.Join(t.TempDir(), "erp.db"))
	if PG() {
		tt.Setenv("PL_DB_URL", URL(t))
		tt.Setenv("PL_DB_ALLOW_INSECURE", "1")
	}
}

// Config 는 config.Config 로 DB 를 고르는 시험(bootstrap.Build)이 쓴다: SQLite 는 인메모리, PG 는 새 데이터베이스 연결 문자열을 넣는다.
func Config(t testing.TB, c config.Config) config.Config {
	t.Helper()
	if PG() {
		c.DBURL, c.DBAllowInsecure = URL(t), true
		return c
	}
	if c.DBPath == "" {
		c.DBPath = ":memory:"
	}
	return c
}

// Shared 는 같은 DB 를 여러 번 여는(다시 기동하는) 시험이 쓴다. 돌려준 함수가 설정에 같은 DB(SQLite: 임시 파일, PG: 같은 새 데이터베이스)를 가리키게 한다.
func Shared(t testing.TB) func(config.Config) config.Config {
	t.Helper()
	path, u := filepath.Join(t.TempDir(), "shared.db"), URL(t)
	return func(c config.Config) config.Config {
		if PG() {
			c.DBURL, c.DBAllowInsecure = u, true
			return c
		}
		c.DBPath = path
		return c
	}
}

// InsertID 는 INSERT 문(RETURNING 없음)을 실행하고 새 행의 id 를 돌려준다. LastInsertId 는 PG 에서 지원되지 않으므로 시험도 이것을 쓴다.
func InsertID(t testing.TB, q contract.Tx, query string, args ...any) int64 {
	t.Helper()
	id, err := dbutil.InsertID(context.Background(), q, query, args...)
	if err != nil {
		t.Fatalf("삽입: %v", err)
	}
	return id
}

// DropTrigger 는 트리거를 지운다(불변식 보호를 우회해 변조·삭제를 흉내 내는 시험이 쓴다). SQLite 는 `DROP TRIGGER 이름`, PG 는 표 이름이 필요하다.
func DropTrigger(t testing.TB, q contract.Tx, name string) {
	t.Helper()
	ctx := context.Background()
	stmt := "DROP TRIGGER " + name
	if PG() {
		var tbl string
		if err := q.QueryRowContext(ctx, `SELECT c.relname FROM pg_trigger tg JOIN pg_class c ON c.oid = tg.tgrelid WHERE tg.tgname = ?`, name).Scan(&tbl); err != nil {
			t.Fatalf("트리거 %s 를 찾을 수 없음: %v", name, err)
		}
		stmt += " ON " + tbl
	}
	if _, err := q.ExecContext(ctx, stmt); err != nil {
		t.Fatalf("트리거 %s 삭제: %v", name, err)
	}
}

// ExecID 는 문(보통 INSERT)을 실행하고 새 행의 id 를 돌려준다. id 열이 없는 표(연결표 등)에 넣거나 INSERT 가 아니면 0 이다.
// 시험 도우미(맵 형태의 삽입 함수)가 LastInsertId 대신 쓴다.
func ExecID(t testing.TB, q contract.Tx, query string, args ...any) int64 {
	t.Helper()
	ctx := context.Background()
	if !PG() {
		res, err := q.ExecContext(ctx, query, args...)
		if err != nil {
			t.Fatalf("실행: %v", err)
		}
		id, _ := res.LastInsertId()
		return id
	}
	var id int64
	err := q.QueryRowContext(ctx, query+" RETURNING id", args...).Scan(&id)
	if err == nil {
		return id
	}
	if dbutil.IsUndefinedColumn(err) || !isInsert(query) {
		if _, err := q.ExecContext(ctx, query, args...); err != nil {
			t.Fatalf("실행: %v", err)
		}
		return 0
	}
	t.Fatalf("실행: %v", err)
	return 0
}

func isInsert(q string) bool {
	for i := 0; i < len(q); i++ {
		if q[i] == ' ' || q[i] == '\n' || q[i] == '\t' {
			continue
		}
		return len(q) >= i+6 && (q[i:i+6] == "INSERT" || q[i:i+6] == "insert")
	}
	return false
}

// FKOff 는 이 DB 에서 외래 키·트리거 검사를 끈다(구간 표처럼 보호 장치가 있는 표에 시험용 행을 직접 넣을 때). 이후 이 DB 의 모든 문장에 적용된다.
// SQLite 는 `PRAGMA foreign_keys = OFF`, PG 는 연결 하나로 줄여 `session_replication_role = replica`(시험 클러스터의 관리 역할이라 가능)다.
func FKOff(t testing.TB, db *sql.DB) {
	t.Helper()
	if !PG() {
		if _, err := db.Exec(`PRAGMA foreign_keys = OFF`); err != nil {
			t.Fatal(err)
		}
		return
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if _, err := db.Exec(`SET session_replication_role = replica`); err != nil {
		t.Fatalf("PG 외래 키 끄기: %v", err)
	}
}
