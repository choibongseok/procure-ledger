package migrations_test

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/dbutil"
	"github.com/choibongseok/procure-ledger/internal/dbutil/pgtest"
	"github.com/choibongseok/procure-ledger/migrations"
)

// PG 에 기준 마이그레이션을 적용해 SQLite 스키마와 비교한다(B37 AC-1711·1712). make verify-pg 에서만 돈다.

type pgCat struct {
	cols     map[string][]col
	pks      map[string]string
	uniques  map[string][]string
	fks      map[string][]string
	indexes  map[string]bool
	triggers map[string]bool
	views    map[string]bool
}

func q(t *testing.T, db *sql.DB, query string, f func(rs *sql.Rows)) {
	t.Helper()
	rs, err := db.Query(query)
	if err != nil {
		t.Fatalf("%v\n%s", err, query)
	}
	defer rs.Close()
	for rs.Next() {
		f(rs)
	}
	if err := rs.Err(); err != nil {
		t.Fatal(err)
	}
}

func pgCatalog(t *testing.T, db *sql.DB) *pgCat {
	t.Helper()
	c := &pgCat{cols: map[string][]col{}, pks: map[string]string{}, uniques: map[string][]string{}, fks: map[string][]string{}, indexes: map[string]bool{}, triggers: map[string]bool{}, views: map[string]bool{}}
	q(t, db, `SELECT c.table_name, c.column_name, c.data_type, c.is_nullable
		FROM information_schema.columns c JOIN information_schema.tables t ON t.table_schema = c.table_schema AND t.table_name = c.table_name
		WHERE c.table_schema = 'public' AND t.table_type = 'BASE TABLE' AND c.table_name <> 'schema_migrations' ORDER BY c.table_name, c.ordinal_position`, func(rs *sql.Rows) {
		var tb, cn, ty, nl string
		if err := rs.Scan(&tb, &cn, &ty, &nl); err != nil {
			t.Fatal(err)
		}
		c.cols[tb] = append(c.cols[tb], col{Name: cn, Type: strings.ToUpper(ty), NotNull: nl == "NO"})
	})
	// PK·유일 색인: 열 이름을 키 순서대로
	q(t, db, `SELECT t.relname, i.indisprimary,
			string_agg(CASE WHEN k.attnum = 0 THEN '<expr>' ELSE a.attname END, ',' ORDER BY k.ord)
		FROM pg_index i
		JOIN pg_class t ON t.oid = i.indrelid JOIN pg_namespace n ON n.oid = t.relnamespace AND n.nspname = 'public'
		CROSS JOIN LATERAL unnest(i.indkey::int2[]) WITH ORDINALITY AS k(attnum, ord)
		LEFT JOIN pg_attribute a ON a.attrelid = t.oid AND a.attnum = k.attnum
		WHERE (i.indisprimary OR i.indisunique) AND t.relname <> 'schema_migrations'
		GROUP BY t.relname, i.indexrelid, i.indisprimary`, func(rs *sql.Rows) {
		var tb, cols string
		var pk bool
		if err := rs.Scan(&tb, &pk, &cols); err != nil {
			t.Fatal(err)
		}
		if pk {
			c.pks[tb] = cols
		} else {
			c.uniques[tb] = append(c.uniques[tb], cols)
		}
	})
	for k := range c.uniques {
		sort.Strings(c.uniques[k])
	}
	q(t, db, `SELECT t.relname, a.attname, r.relname
		FROM pg_constraint k JOIN pg_class t ON t.oid = k.conrelid JOIN pg_class r ON r.oid = k.confrelid
		JOIN pg_namespace n ON n.oid = t.relnamespace AND n.nspname = 'public'
		CROSS JOIN LATERAL unnest(k.conkey) AS ck(attnum)
		JOIN pg_attribute a ON a.attrelid = t.oid AND a.attnum = ck.attnum
		WHERE k.contype = 'f'`, func(rs *sql.Rows) {
		var tb, cn, ref string
		if err := rs.Scan(&tb, &cn, &ref); err != nil {
			t.Fatal(err)
		}
		c.fks[tb] = append(c.fks[tb], cn+"->"+ref)
	})
	for k := range c.fks {
		sort.Strings(c.fks[k])
	}
	// 명시적 색인: 제약이 만든 색인(PK·UNIQUE 제약)이 아닌 것
	q(t, db, `SELECT i.indexname FROM pg_indexes i WHERE i.schemaname = 'public'
		AND NOT EXISTS (SELECT 1 FROM pg_constraint k WHERE k.conname = i.indexname)`, func(rs *sql.Rows) {
		var n string
		if err := rs.Scan(&n); err != nil {
			t.Fatal(err)
		}
		c.indexes[n] = true
	})
	q(t, db, `SELECT tgname FROM pg_trigger WHERE NOT tgisinternal`, func(rs *sql.Rows) {
		var n string
		if err := rs.Scan(&n); err != nil {
			t.Fatal(err)
		}
		c.triggers[n] = true
	})
	q(t, db, `SELECT table_name FROM information_schema.views WHERE table_schema = 'public'`, func(rs *sql.Rows) {
		var n string
		if err := rs.Scan(&n); err != nil {
			t.Fatal(err)
		}
		c.views[n] = true
	})
	return c
}

// SQLite 선언 타입 ↔ PG 타입 대응(B37 6절 6항).
var typeMap = map[string]string{"INTEGER": "BIGINT", "TEXT": "TEXT", "BLOB": "BYTEA"}

func applyBaseline(t *testing.T) *sql.DB {
	t.Helper()
	db := pgtest.Open(t, 5)
	if err := migrations.ApplyDialect(context.Background(), db, dbutil.Postgres); err != nil {
		t.Fatalf("PG 마이그레이션 적용: %v", err)
	}
	return db
}

func TestPGSchemaEquivalence(t *testing.T) {
	pgtest.Require(t)
	cat, _ := sqliteCatalog(t)
	db := applyBaseline(t)
	pc := pgCatalog(t, db)

	sqliteTables := map[string]bool{}
	for n := range cat.Tables {
		sqliteTables[n] = true
	}
	pgTables := map[string]bool{}
	for n := range pc.cols {
		pgTables[n] = true
	}
	diffSets(t, "표", sqliteTables, pgTables)
	diffSets(t, "명시적 색인", cat.Indexes, pc.indexes)
	diffSets(t, "트리거", cat.Triggers, pc.triggers)
	diffSets(t, "뷰", cat.Views, pc.views)

	for tb, scols := range cat.Tables {
		pcols := pc.cols[tb]
		if len(pcols) != len(scols) {
			t.Errorf("%s: 열 수 SQLite %d, PG %d", tb, len(scols), len(pcols))
			continue
		}
		for i, sc := range scols {
			pcl := pcols[i]
			if sc.Name != pcl.Name {
				t.Errorf("%s: %d번째 열 이름 SQLite %s, PG %s", tb, i+1, sc.Name, pcl.Name)
				continue
			}
			if want := typeMap[sc.Type]; want != pcl.Type {
				t.Errorf("%s.%s: 타입 SQLite %s(→%s), PG %s", tb, sc.Name, sc.Type, want, pcl.Type)
			}
			if sc.PK == 0 && sc.NotNull != pcl.NotNull {
				t.Errorf("%s.%s: NOT NULL SQLite %v, PG %v", tb, sc.Name, sc.NotNull, pcl.NotNull)
			}
		}
		if cat.PKs[tb] != pc.pks[tb] {
			t.Errorf("%s: 기본 키 SQLite %q, PG %q", tb, cat.PKs[tb], pc.pks[tb])
		}
		if strings.Join(cat.Uniques[tb], "|") != strings.Join(pc.uniques[tb], "|") {
			t.Errorf("%s: 유일 제약 SQLite %v, PG %v", tb, cat.Uniques[tb], pc.uniques[tb])
		}
		if strings.Join(cat.FKs[tb], "|") != strings.Join(pc.fks[tb], "|") {
			t.Errorf("%s: 외래 키 SQLite %v, PG %v", tb, cat.FKs[tb], pc.fks[tb])
		}
	}
}

var tsLike = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z$`)

func dumpTable(t *testing.T, db *sql.DB, tb string, orderCols string) []string {
	t.Helper()
	rs, err := db.Query(fmt.Sprintf(`SELECT * FROM %s ORDER BY %s`, tb, orderCols))
	if err != nil {
		t.Fatalf("%s: %v", tb, err)
	}
	defer rs.Close()
	cols, _ := rs.Columns()
	var out []string
	for rs.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rs.Scan(ptrs...); err != nil {
			t.Fatal(err)
		}
		parts := make([]string, len(cols))
		for i, v := range vals {
			switch x := v.(type) {
			case nil:
				parts[i] = "NULL"
			case []byte:
				parts[i] = fmt.Sprintf("%x", x)
			case string:
				if tsLike.MatchString(x) {
					x = "<시각>"
				}
				parts[i] = x
			default:
				parts[i] = fmt.Sprint(x)
			}
		}
		out = append(out, strings.Join(parts, "\x1f"))
	}
	return out
}

func TestPGSeedEquivalence(t *testing.T) {
	pgtest.Require(t)
	cat, sdb := sqliteCatalog(t)
	db := applyBaseline(t)
	for tb, cols := range cat.Tables {
		order := cols[0].Name
		if pk := cat.PKs[tb]; pk != "" {
			order = pk
		}
		a, b := dumpTable(t, sdb, tb, order), dumpTable(t, db, tb, order)
		if len(a) != len(b) {
			t.Errorf("시드 %s: SQLite %d행, PG %d행", tb, len(a), len(b))
			continue
		}
		for i := range a {
			if a[i] != b[i] {
				t.Errorf("시드 %s %d번째 행이 다릅니다:\n  SQLite %q\n  PG     %q", tb, i+1, a[i], b[i])
				break
			}
		}
	}
}

func TestPGApplyIdempotentAndDialectGuard(t *testing.T) {
	pgtest.Require(t)
	db := applyBaseline(t)
	ctx := context.Background()
	var before int
	if err := db.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if err := migrations.ApplyDialect(ctx, db, dbutil.Postgres); err != nil {
		t.Fatalf("두 번째 적용: %v", err)
	}
	var after int
	if err := db.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&after); err != nil || after != before || before != len(listSQL(t, "pg")) {
		t.Fatalf("적용 기록 %d -> %d (PG 파일 수 %d 와 같아야 함): %v", before, after, len(listSQL(t, "pg")), err)
	}
	// 다른 방언의 적용 기록이 섞이면 거부한다.
	if _, err := db.Exec(`INSERT INTO schema_migrations (name) VALUES ('0001_init.sql')`); err != nil {
		t.Fatal(err)
	}
	err := migrations.ApplyDialect(ctx, db, dbutil.Postgres)
	if err == nil || !strings.Contains(err.Error(), "PostgreSQL 용 마이그레이션이 아닙니다") {
		t.Fatalf("다른 방언 기록은 거부되어야 한다: %v", err)
	}
}

// 대표 보호 장치가 SQLite 와 같은 메시지로 중단하고, 식별자 시퀀스가 시드 id 뒤에서 시작하는지 확인한다.
// 모든 트리거의 동작 동등성은 3단계에서 전체 Go 시험을 PG 로 돌려 보증한다. 여기서는 메시지 집합 동등성(정적)과 대표 표본만 본다.
func TestPGGuardSamplesAndIdentity(t *testing.T) {
	pgtest.Require(t)
	db := applyBaseline(t)
	ctx := context.Background()

	// 식별자: 시드가 있는 표(roles)에서 새 행이 시드 id 와 겹치지 않는다.
	var maxSeed int64
	if err := db.QueryRow(`SELECT MAX(id) FROM roles`).Scan(&maxSeed); err != nil {
		t.Fatal(err)
	}
	var id int64
	if err := db.QueryRow(`INSERT INTO roles (key, name, kind, created_at, updated_at) VALUES ('r_test', '시험', 'custom', ?, ?) RETURNING id`, "2026-01-01T00:00:00Z", "2026-01-01T00:00:00Z").Scan(&id); err != nil {
		t.Fatalf("roles 삽입: %v", err)
	}
	if id <= maxSeed {
		t.Fatalf("시퀀스가 시드 뒤에서 시작해야 한다: 새 id %d, 시드 최대 %d", id, maxSeed)
	}

	// 감사 로그는 추가 전용이다(SQLite 와 같은 메시지).
	hash := strings.Repeat("a", 64)
	if _, err := db.ExecContext(ctx, `INSERT INTO audit_logs (seq, at, actor_id, actor_role, channel, action, target_type, target_id, result, prev_hash, hash)
		VALUES (1, '2026-01-01T00:00:00Z', 0, 'admin', 'web', 'x.y', 'setting', '1', 'success', '', ?)`, hash); err != nil {
		t.Fatalf("audit_logs 삽입: %v", err)
	}
	_, err := db.ExecContext(ctx, `UPDATE audit_logs SET detail = 'x' WHERE seq = 1`)
	if !dbutil.IsGuard(err, "audit_logs 는 추가 전용입니다") {
		t.Errorf("audit_logs 수정은 보호 장치가 막아야 한다: %v", err)
	}
	_, err = db.ExecContext(ctx, `DELETE FROM audit_logs WHERE seq = 1`)
	if !dbutil.IsGuard(err, "audit_logs 는 추가 전용입니다") {
		t.Errorf("audit_logs 삭제는 보호 장치가 막아야 한다: %v", err)
	}

	// 월 마감 상태는 처음 마감(버전 1)으로만 만들 수 있다.
	_, err = db.ExecContext(ctx, `INSERT INTO month_closes (month, status, version) VALUES ('2026-01', 'open', 1)`)
	if !dbutil.IsGuard(err, "월 마감 상태는 처음 마감(버전 1)으로만 만들 수 있습니다") {
		t.Errorf("month_closes 보호 장치: %v", err)
	}
	// 유일/CHECK 제약 오류 분류가 PG 에서도 동작한다.
	_, err = db.ExecContext(ctx, `INSERT INTO roles (key, name, kind, created_at, updated_at) VALUES ('r_test', '중복', 'custom', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`)
	if !dbutil.IsUnique(err) {
		t.Errorf("유일 위반: %v", err)
	}
	_, err = db.ExecContext(ctx, `INSERT INTO month_closes (month, status, version, closed_at) VALUES ('2026-13-99', 'closed', 1, '2026-01-01T00:00:00Z')`)
	if !dbutil.IsCheck(err) {
		t.Errorf("GLOB 를 옮긴 CHECK 위반: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO month_closes (month, status, version, closed_at) VALUES ('2026-02', 'closed', 1, '2026-02-01T00:00:00Z')`); err != nil {
		t.Errorf("정상 마감 행: %v", err)
	}
}
