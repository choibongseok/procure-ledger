// pgbaseline 은 SQLite 마이그레이션의 최종 스키마와 시드 데이터를 PostgreSQL 문법으로 옮겨 PG 기준 마이그레이션을 만든다(B37 6절).
// 한 번 만든 뒤에는 migrations/pg/ 의 SQL 이 정본이다. 이 도구는 (1) 기준 파일 재생성(SQLite 번호가 올라갔을 때)과
// (2) 새 SQLite 마이그레이션을 PG 로 옮길 때의 초안(-translate)을 위한 것이다. 초안은 사람이 검토한다.
//
//	go run ./internal/tools/pgbaseline -through 00NN > migrations/pg/00NN_baseline.sql   # 기준 파일(NN 은 기준 번호)
//	go run ./internal/tools/pgbaseline -translate migrations/0048_x.sql       # 새 마이그레이션 초안
package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/choibongseok/procure-ledger/internal/dbutil"
	"github.com/choibongseok/procure-ledger/internal/dbutil/pgdialect"
	"github.com/choibongseok/procure-ledger/migrations"
)

func main() {
	through := flag.String("through", "", "기준 파일에 담을 SQLite 마이그레이션의 마지막 번호(예: 0050). 비우면 전부. 번호가 더 큰 마이그레이션은 migrations/pg 에 쌍 파일로 둔다")
	tr := flag.String("translate", "", "이 SQLite 마이그레이션 파일을 PG 문법 초안으로 바꿔 출력한다")
	flag.Parse()
	if *tr != "" {
		b, err := os.ReadFile(*tr)
		if err != nil {
			fatal(err)
		}
		out, err := pgdialect.TranslateFile(string(b))
		if err != nil {
			fatal(err)
		}
		fmt.Print(out)
		return
	}
	if err := baseline(os.Stdout, *through); err != nil {
		fatal(err)
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "pgbaseline:", err)
	os.Exit(1)
}

type obj struct{ typ, name, tbl, sql string }

func baseline(w *os.File, through string) error {
	ctx := context.Background()
	db, err := dbutil.Open(":memory:")
	if err != nil {
		return err
	}
	defer db.Close()
	if through == "" {
		if err := migrations.Apply(ctx, db); err != nil {
			return err
		}
	} else {
		names, err := filepath.Glob(filepath.Join("migrations", through+"_*.sql"))
		if err != nil || len(names) != 1 {
			return fmt.Errorf("번호 %s 의 SQLite 마이그레이션을 migrations/ 에서 찾지 못했습니다(저장소 루트에서 실행하세요)", through)
		}
		if err := migrations.ApplyThrough(ctx, db, filepath.Base(names[0])); err != nil {
			return err
		}
	}
	rows, err := db.QueryContext(ctx, `SELECT type, name, tbl_name, sql FROM sqlite_master WHERE sql IS NOT NULL AND name NOT LIKE 'sqlite_%' AND name <> 'schema_migrations' ORDER BY rowid`)
	if err != nil {
		return err
	}
	var objs []obj
	for rows.Next() {
		var o obj
		if err := rows.Scan(&o.typ, &o.name, &o.tbl, &o.sql); err != nil {
			return err
		}
		objs = append(objs, o)
	}
	rows.Close()
	var last string
	if err := db.QueryRowContext(ctx, `SELECT MAX(name) FROM schema_migrations`).Scan(&last); err != nil {
		return err
	}
	num := strings.SplitN(last, "_", 2)[0]

	var tables, indexes, views, triggers []obj
	for _, o := range objs {
		switch o.typ {
		case "table":
			tables = append(tables, o)
		case "index":
			indexes = append(indexes, o)
		case "view":
			views = append(views, o)
		case "trigger":
			triggers = append(triggers, o)
		}
	}
	sorted, err := pgdialect.SortTables(namesAndSQL(tables))
	if err != nil {
		return err
	}
	ordered := sorted.Tables

	fmt.Fprintf(w, "-- ProcureLedger PostgreSQL 기준 마이그레이션 (B37). SQLite 0001~%s 의 최종 스키마와 시드 데이터를 옮긴 것이다.\n", num)
	fmt.Fprintf(w, "-- equivalent: sqlite %s\n", num)
	fmt.Fprintln(w, "-- 생성: go run ./internal/tools/pgbaseline (이후에는 이 파일이 정본이다. 새 마이그레이션은 migrations/NNNN_x.sql 과 migrations/pg/NNNN_x.sql 을 쌍으로 만든다)")
	fmt.Fprintln(w, "-- 순서: 표 → 시드 데이터 → 식별자 시퀀스 → 색인 → 뷰 → 트리거(데이터 적재 중에는 보호 장치가 없다)")
	fmt.Fprintln(w)

	fmt.Fprintln(w, "-- ============================ 표 ============================")
	var alters []string
	for _, t := range ordered {
		s, al, err := pgdialect.TranslateCreateTable(strings.TrimSpace(pgdialect.StripComments(t.SQL)), sorted.Deferred[t.Name])
		if err != nil {
			return fmt.Errorf("표 %s: %w", t.Name, err)
		}
		fmt.Fprintf(w, "%s;\n\n", s)
		alters = append(alters, al...)
	}
	if len(alters) > 0 {
		fmt.Fprintln(w, "-- 외래 키 순환을 끊으려고 표 생성 뒤에 거는 참조")
		for _, a := range alters {
			fmt.Fprintf(w, "%s;\n", a)
		}
		fmt.Fprintln(w)
	}

	fmt.Fprintln(w, "-- ============================ 시드 데이터 ============================")
	var identity []string
	for _, t := range ordered {
		n, ident, err := dumpRows(ctx, db, w, t.Name)
		if err != nil {
			return fmt.Errorf("시드 %s: %w", t.Name, err)
		}
		_ = n
		if ident != "" {
			identity = append(identity, t.Name+"."+ident)
		}
	}
	fmt.Fprintln(w, "\n-- ============================ 식별자 시퀀스 맞추기 ============================")
	for _, ti := range identity {
		p := strings.SplitN(ti, ".", 2)
		fmt.Fprintf(w, "SELECT setval(pg_get_serial_sequence('%s', '%s'), COALESCE((SELECT MAX(%s) FROM %s), 0) + 1, false);\n", p[0], p[1], p[1], p[0])
	}

	fmt.Fprintln(w, "\n-- ============================ 색인 ============================")
	for _, o := range indexes {
		s, err := pgdialect.TranslateStatement(o.sql)
		if err != nil {
			return fmt.Errorf("색인 %s: %w", o.name, err)
		}
		fmt.Fprintf(w, "%s;\n", s)
	}
	fmt.Fprintln(w, "\n-- ============================ 뷰 ============================")
	for _, o := range views {
		s, err := pgdialect.TranslateStatement(o.sql)
		if err != nil {
			return fmt.Errorf("뷰 %s: %w", o.name, err)
		}
		fmt.Fprintf(w, "%s;\n\n", s)
	}
	fmt.Fprintln(w, "-- ============================ 트리거 (함수 + 트리거) ============================")
	for _, o := range triggers {
		s, err := pgdialect.TranslateStatement(o.sql)
		if err != nil {
			return fmt.Errorf("트리거 %s: %w", o.name, err)
		}
		fmt.Fprintf(w, "%s;\n\n", s)
	}
	return nil
}

func namesAndSQL(os []obj) []pgdialect.Table {
	out := make([]pgdialect.Table, len(os))
	for i, o := range os {
		out[i] = pgdialect.Table{Name: o.name, SQL: o.sql}
	}
	return out
}

// dumpRows 는 표의 행을 INSERT 문으로 쓴다. 식별자 열 이름(AUTOINCREMENT 로 정의된 PK)이 있으면 돌려준다.
func dumpRows(ctx context.Context, db *sql.DB, w *os.File, table string) (int, string, error) {
	cols, types, err := columns(ctx, db, table)
	if err != nil {
		return 0, "", err
	}
	var cnt int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table).Scan(&cnt); err != nil {
		return 0, "", err
	}
	ident := pgdialect.IdentityColumn(table, tableSQL(ctx, db, table))
	if cnt == 0 {
		return 0, ident, nil
	}
	q := fmt.Sprintf(`SELECT %s FROM %s ORDER BY %s`, strings.Join(cols, ", "), table, orderBy(cols))
	rs, err := db.QueryContext(ctx, q)
	if err != nil {
		return 0, "", err
	}
	defer rs.Close()
	fmt.Fprintf(w, "-- %s (%d행)\n", table, cnt)
	for rs.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rs.Scan(ptrs...); err != nil {
			return 0, "", err
		}
		lits := make([]string, len(cols))
		for i, v := range vals {
			lits[i] = pgdialect.Literal(v, types[i])
		}
		fmt.Fprintf(w, "INSERT INTO %s (%s) VALUES (%s);\n", table, strings.Join(cols, ", "), strings.Join(lits, ", "))
	}
	fmt.Fprintln(w)
	return cnt, ident, rs.Err()
}

func orderBy(cols []string) string {
	if len(cols) > 0 && cols[0] == "id" {
		return "id"
	}
	return "1"
}

func tableSQL(ctx context.Context, db *sql.DB, table string) string {
	var s string
	_ = db.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='table' AND name = ?`, table).Scan(&s)
	return s
}

func columns(ctx context.Context, db *sql.DB, table string) ([]string, []string, error) {
	rs, err := db.QueryContext(ctx, `SELECT name, type FROM pragma_table_info(?) ORDER BY cid`, table)
	if err != nil {
		return nil, nil, err
	}
	defer rs.Close()
	var cols, types []string
	for rs.Next() {
		var n, ty string
		if err := rs.Scan(&n, &ty); err != nil {
			return nil, nil, err
		}
		cols = append(cols, n)
		types = append(types, strings.ToUpper(ty))
	}
	return cols, types, rs.Err()
}

var _ = sort.Strings
