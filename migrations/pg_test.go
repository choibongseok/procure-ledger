package migrations_test

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/dbutil"
	"github.com/choibongseok/procure-ledger/migrations"
)

// 두 방언 마이그레이션의 동등성 시험(B37 6절, AC-1711·1712). 정적 검사는 항상 돌고, PG 에 적용해 비교하는 시험은 make verify-pg 에서 돈다.

func numOf(name string) string { return strings.SplitN(name, "_", 2)[0] }

func listSQL(t *testing.T, dir string) []string {
	t.Helper()
	m, err := filepath.Glob(filepath.Join(dir, "*.sql"))
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, f := range m {
		out = append(out, filepath.Base(f))
	}
	sort.Strings(out)
	return out
}

func baselineName(t *testing.T) string {
	t.Helper()
	var found []string
	for _, n := range listSQL(t, "pg") {
		if strings.HasSuffix(n, "_baseline.sql") {
			found = append(found, n)
		}
	}
	if len(found) != 1 {
		t.Fatalf("PG 기준 파일은 정확히 하나여야 합니다: %v", found)
	}
	return found[0]
}

func TestPGMigrationPairs(t *testing.T) {
	base := baselineName(t)
	baseNum := numOf(base)
	b, err := os.ReadFile(filepath.Join("pg", base))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "-- equivalent: sqlite "+baseNum) {
		t.Errorf("%s 머리말에 `-- equivalent: sqlite %s` 가 있어야 합니다", base, baseNum)
	}
	sqlite := map[string]bool{}
	for _, n := range listSQL(t, ".") {
		sqlite[n] = true
	}
	if !func() bool {
		for n := range sqlite {
			if numOf(n) == baseNum {
				return true
			}
		}
		return false
	}() {
		t.Errorf("SQLite 마이그레이션에 기준 번호 %s 가 없습니다", baseNum)
	}
	pg := map[string]bool{}
	for _, n := range listSQL(t, "pg") {
		if n != base {
			pg[n] = true
		}
	}
	for n := range sqlite {
		if numOf(n) > baseNum && !pg[n] {
			t.Errorf("SQLite 마이그레이션 %s 에 짝인 migrations/pg/%s 가 없습니다. `go run ./internal/tools/pgbaseline -translate migrations/%s` 로 초안을 만드세요", n, n, n)
		}
	}
	for n := range pg {
		if numOf(n) <= baseNum {
			t.Errorf("migrations/pg/%s 는 기준 번호 %s 이하입니다(기준 파일에 이미 들어 있어야 합니다)", n, baseNum)
		}
		if !sqlite[n] {
			t.Errorf("migrations/pg/%s 에 짝인 SQLite 마이그레이션이 없습니다", n)
		}
	}
}

// ---- SQLite 쪽 카탈로그 ----

type col struct {
	Name    string
	Type    string // 대문자 선언 타입
	NotNull bool
	PK      int
}

type catalog struct {
	Tables   map[string][]col
	Uniques  map[string][]string // 표 → 정렬된 "열,열" 목록(PK 제외, 자동 색인 포함)
	PKs      map[string]string   // 표 → "열,열"
	FKs      map[string][]string // 표 → "열->표.열"
	Indexes  map[string]bool     // 명시적 CREATE INDEX 이름
	Triggers map[string]bool
	Views    map[string]bool
}

func sqliteCatalog(t *testing.T) (*catalog, *sql.DB) {
	t.Helper()
	ctx := context.Background()
	db, err := dbutil.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := migrations.Apply(ctx, db); err != nil {
		t.Fatal(err)
	}
	c := &catalog{Tables: map[string][]col{}, Uniques: map[string][]string{}, PKs: map[string]string{}, FKs: map[string][]string{}, Indexes: map[string]bool{}, Triggers: map[string]bool{}, Views: map[string]bool{}}
	rows, err := db.QueryContext(ctx, `SELECT type, name FROM sqlite_master WHERE name NOT LIKE 'sqlite_%' AND name <> 'schema_migrations' AND type IN ('table','index','trigger','view') AND sql IS NOT NULL`)
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var ty, n string
		if err := rows.Scan(&ty, &n); err != nil {
			t.Fatal(err)
		}
		switch ty {
		case "table":
			tables = append(tables, n)
		case "index":
			c.Indexes[n] = true
		case "trigger":
			c.Triggers[n] = true
		case "view":
			c.Views[n] = true
		}
	}
	rows.Close()
	for _, tb := range tables {
		rs, err := db.QueryContext(ctx, `SELECT name, type, "notnull", pk FROM pragma_table_info(?) ORDER BY cid`, tb)
		if err != nil {
			t.Fatal(err)
		}
		var pkCols []struct {
			n  string
			pk int
		}
		for rs.Next() {
			var x col
			var nn int
			if err := rs.Scan(&x.Name, &x.Type, &nn, &x.PK); err != nil {
				t.Fatal(err)
			}
			x.Type, x.NotNull = strings.ToUpper(x.Type), nn == 1
			c.Tables[tb] = append(c.Tables[tb], x)
			if x.PK > 0 {
				pkCols = append(pkCols, struct {
					n  string
					pk int
				}{x.Name, x.PK})
			}
		}
		rs.Close()
		sort.Slice(pkCols, func(i, j int) bool { return pkCols[i].pk < pkCols[j].pk })
		var names []string
		for _, p := range pkCols {
			names = append(names, p.n)
		}
		c.PKs[tb] = strings.Join(names, ",")
		// 유일 색인(PK 자동 색인 제외)
		il, err := db.QueryContext(ctx, `SELECT name, "unique", origin FROM pragma_index_list(?)`, tb)
		if err != nil {
			t.Fatal(err)
		}
		type ix struct {
			name, origin string
			uniq         int
		}
		var ixs []ix
		for il.Next() {
			var x ix
			if err := il.Scan(&x.name, &x.uniq, &x.origin); err != nil {
				t.Fatal(err)
			}
			ixs = append(ixs, x)
		}
		il.Close()
		for _, x := range ixs {
			if x.uniq != 1 || x.origin == "pk" {
				continue
			}
			ic, err := db.QueryContext(ctx, `SELECT name FROM pragma_index_info(?) ORDER BY seqno`, x.name)
			if err != nil {
				t.Fatal(err)
			}
			var cs []string
			for ic.Next() {
				var n sql.NullString
				if err := ic.Scan(&n); err != nil {
					t.Fatal(err)
				}
				if !n.Valid {
					n.String = "<expr>"
				}
				cs = append(cs, n.String)
			}
			ic.Close()
			c.Uniques[tb] = append(c.Uniques[tb], strings.Join(cs, ","))
		}
		sort.Strings(c.Uniques[tb])
		fk, err := db.QueryContext(ctx, `SELECT "from", "table", "to" FROM pragma_foreign_key_list(?)`, tb)
		if err != nil {
			t.Fatal(err)
		}
		for fk.Next() {
			var f, tt string
			var to sql.NullString
			if err := fk.Scan(&f, &tt, &to); err != nil {
				t.Fatal(err)
			}
			c.FKs[tb] = append(c.FKs[tb], fmt.Sprintf("%s->%s", f, tt))
		}
		fk.Close()
		sort.Strings(c.FKs[tb])
	}
	return c, db
}

// ---- PG 파일의 정적 해석 ----

var (
	pgTableRe = regexp.MustCompile(`(?m)^CREATE TABLE (\w+) \(`)
	pgIndexRe = regexp.MustCompile(`(?m)^CREATE (?:UNIQUE )?INDEX\s+(\w+)\s+ON`)
	pgTrigRe  = regexp.MustCompile(`(?m)^CREATE TRIGGER (\w+) `)
	pgViewRe  = regexp.MustCompile(`(?m)^CREATE VIEW (\w+) `)
	pgMsgRe   = regexp.MustCompile(`MESSAGE = '((?:[^']|'')*)'`)
	pgFnRe    = regexp.MustCompile(`(?s)CREATE (?:OR REPLACE )?FUNCTION (\w+)\(\).*?\$fn\$;`)
	pgInsRe   = regexp.MustCompile(`(?m)^INSERT INTO (\w+) `)
)

func names(re *regexp.Regexp, s string) map[string]bool {
	out := map[string]bool{}
	for _, m := range re.FindAllStringSubmatch(s, -1) {
		out[m[1]] = true
	}
	return out
}

func diffSets(t *testing.T, what string, sqlite, pg map[string]bool) {
	t.Helper()
	var only1, only2 []string
	for k := range sqlite {
		if !pg[k] {
			only1 = append(only1, k)
		}
	}
	for k := range pg {
		if !sqlite[k] {
			only2 = append(only2, k)
		}
	}
	sort.Strings(only1)
	sort.Strings(only2)
	if len(only1) > 0 {
		t.Errorf("%s: SQLite 에만 있음: %v", what, only1)
	}
	if len(only2) > 0 {
		t.Errorf("%s: PG 에만 있음: %v", what, only2)
	}
}

var pgObjRe = regexp.MustCompile(`(?m)^(?:CREATE (?:UNIQUE )?(TABLE|INDEX|VIEW|TRIGGER)\s+(\w+)|DROP (TABLE|INDEX|VIEW|TRIGGER)(?: IF EXISTS)?\s+(\w+))`)

// pgObjects 는 PG 마이그레이션 전체(기준 파일 + 쌍 파일, 번호 순)를 문장 순서대로 훑어 지금 있어야 하는 표·색인·뷰·트리거 이름을 모은다.
func pgObjects(files []string) map[string]map[string]bool {
	out := map[string]map[string]bool{"TABLE": {}, "INDEX": {}, "VIEW": {}, "TRIGGER": {}}
	for _, f := range files {
		for _, m := range pgObjRe.FindAllStringSubmatch(f, -1) {
			if m[1] != "" {
				out[m[1]][m[2]] = true
			} else {
				delete(out[m[3]], m[4])
			}
		}
	}
	return out
}

func readPGFiles(t *testing.T) []string {
	t.Helper()
	var out []string
	for _, n := range listSQL(t, "pg") {
		b, err := os.ReadFile(filepath.Join("pg", n))
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, string(b))
	}
	return out
}

// 항상 도는 정적 동등성: 표·색인·트리거·뷰 이름 집합, 트리거 메시지(글자 단위). 시드 INSERT 수는 기준 파일을 기준 번호 시점의 SQLite 와 비교한다.
func TestPGStaticEquivalence(t *testing.T) {
	cat, db := sqliteCatalog(t)
	files := readPGFiles(t)
	pg := strings.Join(files, "\n")
	objs := pgObjects(files)
	tables := map[string]bool{}
	for n := range cat.Tables {
		tables[n] = true
	}
	diffSets(t, "표", tables, objs["TABLE"])
	diffSets(t, "색인", cat.Indexes, objs["INDEX"])
	diffSets(t, "트리거", cat.Triggers, objs["TRIGGER"])
	diffSets(t, "뷰", cat.Views, objs["VIEW"])

	// 트리거 보호 메시지는 글자까지 같아야 한다.
	rows, err := db.Query(`SELECT sql FROM sqlite_master WHERE type='trigger'`)
	if err != nil {
		t.Fatal(err)
	}
	sqliteMsgs := map[string]int{}
	re := regexp.MustCompile(`RAISE\s*\(\s*ABORT\s*,\s*'((?:[^']|'')*)'`)
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		for _, m := range re.FindAllStringSubmatch(s, -1) {
			sqliteMsgs[m[1]]++
		}
	}
	rows.Close()
	// 같은 이름의 함수를 나중 파일이 CREATE OR REPLACE 로 바꾸면 마지막 정의만 센다(B44: 월 마감 잠금 트리거 함수를 법인별로 바꿨다).
	finalFn := map[string]string{}
	for _, m := range pgFnRe.FindAllStringSubmatch(pg, -1) {
		finalFn[m[1]] = m[0]
	}
	pgMsgs := map[string]int{}
	for _, body := range finalFn {
		for _, m := range pgMsgRe.FindAllStringSubmatch(body, -1) {
			pgMsgs[m[1]]++
		}
	}
	for m, n := range sqliteMsgs {
		if pgMsgs[m] != n {
			t.Errorf("트리거 메시지 %q: SQLite %d, PG %d", m, n, pgMsgs[m])
		}
	}
	for m, n := range pgMsgs {
		if sqliteMsgs[m] != n {
			t.Errorf("트리거 메시지 %q: SQLite %d, PG %d", m, sqliteMsgs[m], n)
		}
	}

	// 시드 데이터 행 수: 기준 파일의 INSERT 수 = 기준 번호까지 적용한 SQLite 의 행 수(쌍 파일의 시드는 PG 시험이 행 단위로 비교한다).
	baseNum := numOf(baselineName(t))
	var last string
	for _, n := range listSQL(t, ".") {
		if numOf(n) == baseNum {
			last = n
		}
	}
	bdb, err := dbutil.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer bdb.Close()
	if err := migrations.ApplyThrough(context.Background(), bdb, last); err != nil {
		t.Fatal(err)
	}
	ins := map[string]int{}
	baseText, _ := os.ReadFile(filepath.Join("pg", baselineName(t)))
	for _, m := range pgInsRe.FindAllStringSubmatch(string(baseText), -1) {
		ins[m[1]]++
	}
	rs, err := bdb.Query(`SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' AND name <> 'schema_migrations'`)
	if err != nil {
		t.Fatal(err)
	}
	var tbs []string
	for rs.Next() {
		var n string
		_ = rs.Scan(&n)
		tbs = append(tbs, n)
	}
	rs.Close()
	for _, tb := range tbs {
		var n int
		if err := bdb.QueryRow(`SELECT COUNT(*) FROM ` + tb).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if ins[tb] != n {
			t.Errorf("시드 %s: SQLite(%s 시점) %d행, PG 기준 파일 INSERT %d개", tb, baseNum, n, ins[tb])
		}
	}
}
