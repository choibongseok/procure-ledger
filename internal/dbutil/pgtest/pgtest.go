// Package pgtest 는 PostgreSQL 용 시험이 격리된 임시 데이터베이스를 얻는 도우미다(B37 10절). 시험 전용 패키지다.
//
// 환경변수 PL_TEST_PG_URL(관리 권한 연결 문자열, 보통 scripts/verify-pg.sh 가 만든 임시 클러스터)이 없으면 시험을 건너뛴다.
// 데이터베이스 이름은 pltest_<무작위> 이고 시험이 끝나면 지운다. 기존 데이터베이스는 건드리지 않는다.
package pgtest

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/dbutil"
)

// AdminURL 은 PL_TEST_PG_URL 이다(없으면 빈 문자열).
func AdminURL() string { return os.Getenv("PL_TEST_PG_URL") }

// Require 는 PG 가 없으면 시험을 건너뛴다.
func Require(t testing.TB) string {
	t.Helper()
	u := AdminURL()
	if u == "" {
		t.Skip("PostgreSQL 이 없어 건너뜁니다(make verify-pg 로 실행)")
	}
	return u
}

// NewDatabase 는 빈 임시 데이터베이스(UTF8, 'C' 정렬)를 만들고 그 연결 문자열을 돌려준다. 시험이 끝나면 지운다.
func NewDatabase(t testing.TB) string {
	t.Helper()
	admin := Require(t)
	ctx := context.Background()
	adb, err := dbutil.OpenPG(ctx, admin, dbutil.PGOptions{MaxConns: 2, AllowInsecure: true})
	if err != nil {
		t.Fatalf("관리 연결: %v", err)
	}
	var b [6]byte
	_, _ = rand.Read(b[:])
	name := "pltest_" + hex.EncodeToString(b[:])
	if _, err := adb.ExecContext(ctx, fmt.Sprintf(`CREATE DATABASE %s ENCODING 'UTF8' LC_COLLATE 'C' LC_CTYPE 'C' TEMPLATE template0`, name)); err != nil {
		_ = adb.Close()
		t.Fatalf("데이터베이스 생성: %v", err)
	}
	t.Cleanup(func() {
		_, _ = adb.ExecContext(context.Background(), fmt.Sprintf(`DROP DATABASE IF EXISTS %s WITH (FORCE)`, name))
		_ = adb.Close()
	})
	u, err := url.Parse(admin)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	return u.String()
}

// Open 은 빈 임시 데이터베이스에 연결한다(연결 수 상한 maxConns, 0 이면 10).
func Open(t testing.TB, maxConns int) *sql.DB {
	t.Helper()
	db, _ := OpenURL(t, maxConns)
	return db
}

// OpenURL 은 Open 과 같고 연결 문자열도 돌려준다(같은 데이터베이스에 다른 설정으로 두 번째 연결을 만들 때).
func OpenURL(t testing.TB, maxConns int) (*sql.DB, string) {
	t.Helper()
	u := NewDatabase(t)
	db, err := dbutil.OpenPG(context.Background(), u, dbutil.PGOptions{MaxConns: maxConns, AllowInsecure: true})
	if err != nil {
		t.Fatalf("연결: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db, u
}
