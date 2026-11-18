// Package migrations 는 SQL 마이그레이션을 바이너리에 임베드하고 순서대로 적용한다.
// 파일명은 NNNN_설명.sql, 적용 내역은 schema_migrations 에 기록한다. 이미 적용된 파일은 건너뛴다.
package migrations

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"sort"
	"strings"

	"github.com/choibongseok/procure-ledger/internal/dbutil"
)

//go:embed *.sql
var files embed.FS

// pgFiles 는 PostgreSQL 용 마이그레이션이다(B37). `NNNN_baseline.sql` 한 개(SQLite 0001~NNNN 의 최종 스키마와 시드)와
// 그 뒤 번호의 쌍 파일(migrations/NNNN_x.sql 과 같은 이름)로 이뤄진다.
//
//go:embed pg/*.sql
var pgFiles embed.FS

// ApplyDialect 는 DB 종류에 맞는 마이그레이션을 적용한다. SQLite 는 Apply 와 같다.
func ApplyDialect(ctx context.Context, db *sql.DB, d dbutil.Dialect) error {
	if d == nil || d.Kind() == dbutil.KindSQLite {
		return ApplyThrough(ctx, db, "")
	}
	return applyPG(ctx, db)
}

// Apply 는 미적용 마이그레이션을 파일 하나당 한 트랜잭션으로 적용한다. DB 의 방언(DialectOf)에 맞는 마이그레이션을 쓴다.
func Apply(ctx context.Context, db *sql.DB) error {
	if dbutil.DialectOf(db).Kind() == dbutil.KindPostgres {
		return applyPG(ctx, db)
	}
	return ApplyThrough(ctx, db, "")
}

// ApplyThrough 는 last(파일 이름, 예 "0010_org_chart.sql")까지만 적용한다. last 가 비면 전부 적용한다.
// 기존 데이터가 있는 DB 에 새 마이그레이션이 통과하는지 시험할 때 쓴다.
func ApplyThrough(ctx context.Context, db *sql.DB, last string) error {
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		name TEXT PRIMARY KEY,
		applied_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')))`); err != nil {
		return fmt.Errorf("schema_migrations 생성: %w", err)
	}
	entries, err := files.ReadDir(".")
	if err != nil {
		return err
	}
	var names []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".sql") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	for _, name := range names {
		if last != "" && name > last {
			break
		}
		var n int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations WHERE name = ?`, name).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			continue
		}
		body, err := files.ReadFile(name)
		if err != nil {
			return err
		}
		if err := applyOne(ctx, db, name, string(body)); err != nil {
			return err
		}
	}
	return nil
}

// fkOffDirective 가 파일 첫 줄이면 외래키 검사를 끄고(표 재작성용) 적용한 뒤 foreign_key_check 로 위반이 없음을 확인하고 다시 켠다.
// PRAGMA foreign_keys 는 트랜잭션 안에서 바뀌지 않으므로 트랜잭션 밖에서 끄고 켠다(연결은 하나다: dbutil.Open).
const fkOffDirective = "-- migrate:fk-off"

func applyOne(ctx context.Context, db *sql.DB, name, body string) error {
	fkOff := strings.HasPrefix(body, fkOffDirective)
	if fkOff {
		if _, err := db.ExecContext(ctx, `PRAGMA foreign_keys = OFF`); err != nil {
			return err
		}
		defer db.ExecContext(context.WithoutCancel(ctx), `PRAGMA foreign_keys = ON`)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, body); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("마이그레이션 %s: %w", name, err)
	}
	if fkOff {
		rows, err := tx.QueryContext(ctx, `PRAGMA foreign_key_check`)
		if err != nil {
			_ = tx.Rollback()
			return err
		}
		bad := rows.Next()
		_ = rows.Close()
		if bad {
			_ = tx.Rollback()
			return fmt.Errorf("마이그레이션 %s: 외래키 위반이 생겨 되돌렸습니다", name)
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations (name) VALUES (?)`, name); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// pgAdvisoryKey 는 PG 마이그레이션 적용을 한 번에 하나만 하게 하는 advisory lock 키다(여러 프로세스가 동시에 기동하는 경우).
const pgAdvisoryKey int64 = 0x504c_4d49_4752 // "PLMIGR"

// applyPG 는 미적용 PG 마이그레이션을 파일 하나당 한 트랜잭션으로 적용한다(PG 는 DDL 도 트랜잭션 안에서 되돌아간다).
// 이미 SQLite 용 기록(다른 방언)이 있는 DB 에는 적용하지 않는다.
func applyPG(ctx context.Context, db *sql.DB) error {
	conn, err := db.Conn(ctx) // 세션 수준 advisory lock 을 쓰므로 연결 하나를 잡고 간다
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `SELECT pg_advisory_lock(?)`, pgAdvisoryKey); err != nil {
		return fmt.Errorf("마이그레이션 잠금: %w", err)
	}
	defer conn.ExecContext(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock(?)`, pgAdvisoryKey)

	if _, err := conn.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		name TEXT PRIMARY KEY,
		applied_at TEXT NOT NULL DEFAULT `+pgNowExpr+`)`); err != nil {
		return fmt.Errorf("schema_migrations 생성: %w", err)
	}
	entries, err := pgFiles.ReadDir("pg")
	if err != nil {
		return err
	}
	known := map[string]bool{}
	var names []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".sql") {
			names = append(names, e.Name())
			known[e.Name()] = true
		}
	}
	sort.Strings(names)

	applied := map[string]bool{}
	rows, err := conn.QueryContext(ctx, `SELECT name FROM schema_migrations`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			rows.Close()
			return err
		}
		applied[n] = true
	}
	rows.Close()
	for n := range applied {
		if !known[n] {
			return fmt.Errorf("마이그레이션: 이 데이터베이스의 적용 기록 %q 은(는) PostgreSQL 용 마이그레이션이 아닙니다(다른 방언이거나 더 새로운 버전의 앱이 적용했습니다)", n)
		}
	}
	for _, name := range names {
		if applied[name] {
			continue
		}
		body, err := pgFiles.ReadFile("pg/" + name)
		if err != nil {
			return err
		}
		tx, err := conn.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, string(body)); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("마이그레이션 pg/%s: %w", name, err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations (name) VALUES (?)`, name); err != nil {
			_ = tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// pgNowExpr 는 dbutil.Postgres.NowUTC() 와 같은 식이다(상수로 두어 마이그레이션 러너가 방언 객체에 기대지 않는다).
const pgNowExpr = `to_char(now() AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS"Z"')`
