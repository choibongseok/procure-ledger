// Package dialectscan 은 SQLite 전용 구문이 코드와 마이그레이션 어디에 몇 개 있는지 센다(B37 3.2·3.3 고정점의 재측정 도구).
// 명세의 수치는 이 패키지가 낸 값이다. 같은 계산을 기준선 시험(dbutil.TestDialectRatchet)이 쓴다: 방언 의존 구문은 늘지 않고 줄기만 한다.
package dialectscan

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Pattern 은 세는 구문 하나다.
type Pattern struct {
	Name string
	Re   *regexp.Regexp
	// Filter 가 있으면 Re 의 첫 부분 일치 그룹을 넘겨 true 인 것만 센다.
	Filter func(group string) bool
}

func p(name, re string) Pattern { return Pattern{Name: name, Re: regexp.MustCompile("(?i)" + re)} }

// count 는 text 안에서 패턴이 일치하는 횟수다.
func (pt Pattern) count(text string) int {
	if pt.Filter == nil {
		return len(pt.Re.FindAllString(text, -1))
	}
	n := 0
	for _, m := range pt.Re.FindAllStringSubmatch(text, -1) {
		if len(m) > 1 && pt.Filter(m[1]) {
			n++
		}
	}
	return n
}

func notNull(g string) bool     { u := strings.ToUpper(g); return u != "NULL" && u != "NOT" }
func notNullWord(g string) bool { return !strings.EqualFold(g, "NULL") }

func isValue(name, re string, f func(string) bool) Pattern {
	pt := p(name, re)
	pt.Filter = f
	return pt
}

// MigrationPatterns 는 마이그레이션 SQL(주석 제외)에서 세는 구문이다.
var MigrationPatterns = []Pattern{
	p("CREATE TRIGGER", `CREATE TRIGGER`),
	p("RAISE(", `RAISE\s*\(`),
	p("DROP TRIGGER", `DROP TRIGGER`),
	p("AUTOINCREMENT", `AUTOINCREMENT`),
	p("INTEGER PRIMARY KEY", `INTEGER PRIMARY KEY`),
	p("TEXT PRIMARY KEY", `TEXT PRIMARY KEY`),
	p("strftime(", `strftime\s*\(`),
	p("date(", `\bdate\s*\(`),
	p("datetime(", `\bdatetime\s*\(`),
	p("GLOB", `\bGLOB\b`),
	p("CHECK", `\bCHECK\b`),
	isValue("IS NOT (값 비교)", `\bIS\s+NOT\s+(\w+)`, notNullWord),
	isValue("IS (값 비교)", `\bIS\s+(\w+)`, notNull),
	p("ALTER TABLE ... RENAME", `ALTER TABLE\s+\S+\s+RENAME`),
	p("ALTER TABLE ... ADD COLUMN", `ALTER TABLE\s+\S+\s+ADD COLUMN`),
	p("DROP TABLE", `DROP TABLE`),
	p("CREATE VIEW", `CREATE VIEW`),
	p("PRAGMA", `PRAGMA`),
	p("migrate:fk-off 지시", `-- migrate:fk-off`),
	p("INSERT OR IGNORE", `INSERT OR IGNORE`),
	p("INSERT OR REPLACE", `INSERT OR REPLACE`),
	p("ON CONFLICT", `ON CONFLICT`),
	p("substr(", `\bsubstr\s*\(`),
	p("length(", `\blength\s*\(`),
	p("WITHOUT ROWID", `WITHOUT\s+ROWID`),
	p("BLOB", `\bBLOB\b`),
	p("REAL", `\bREAL\b`),
	p("부분 색인(CREATE INDEX ... WHERE)", `CREATE (?:UNIQUE )?INDEX[^;]*\bWHERE\b`),
	p("INSERT INTO ... SELECT", `INSERT INTO[^;]*\bSELECT\b`),
	p("INSERT INTO", `INSERT INTO`),
	p("CREATE TABLE", `CREATE TABLE`),
	p("CREATE INDEX", `CREATE (?:UNIQUE )?INDEX`),
	p("_at TEXT (시각 열)", `_at\s+TEXT`),
	p("스칼라 max(a,b)·min(a,b)", `\b(?:max|min)\s*\([^()]*,[^()]*\)`),
}

// GoPatterns 는 Go 소스의 백틱 SQL 문 안에서 세는 구문이다.
var GoPatterns = []Pattern{
	p("INSERT OR IGNORE/REPLACE", `INSERT OR (?:IGNORE|REPLACE)`),
	p("ON CONFLICT", `ON CONFLICT`),
	p("strftime(", `strftime\s*\(`),
	p("date(", `\bdate\s*\(`),
	p("datetime(", `\bdatetime\s*\(`),
	p("julianday", `julianday`),
	p("group_concat", `group_concat`),
	p("substr(", `\bsubstr\s*\(`),
	p("instr(", `\binstr\s*\(`),
	p("printf(", `\bprintf\s*\(`),
	p("GLOB", `\bGLOB\b`),
	isValue("IS NOT (값 비교)", `\bIS\s+NOT\s+(\w+)`, notNullWord),
	p("LIKE", `\bLIKE\b`),
	p("스칼라 max(a,b)·min(a,b)", `\b(?:max|min)\s*\([^()]*,[^()]*\)`),
	p("PRAGMA/sqlite_", `PRAGMA|sqlite_`),
}

// LastInsertIDRe 는 Go 소스(SQL 문 밖)에서 세는 호출이다.
var LastInsertIDRe = regexp.MustCompile(`\.LastInsertId\(\)`)

var sqlLeadRe = regexp.MustCompile(`(?i)\b(SELECT|INSERT|UPDATE|DELETE|CREATE)\b`)
var lineComment = regexp.MustCompile(`--[^\n]*`)

// Result 는 구문별 합계와 그것이 든 파일 수다.
type Result struct {
	Counts map[string]int
	Files  map[string]int
	Total  int // 대상 항목 수(마이그레이션 파일 수 또는 SQL 문 수)
}

func newResult() Result { return Result{Counts: map[string]int{}, Files: map[string]int{}} }

// Migrations 는 dir 아래 *.sql(하위 디렉터리 제외)을 센다. 주석(-- ...)은 제외한다.
func Migrations(dir string) (Result, error) {
	r := newResult()
	names, err := filepath.Glob(filepath.Join(dir, "*.sql"))
	if err != nil {
		return r, err
	}
	sort.Strings(names)
	for _, n := range names {
		b, err := os.ReadFile(n)
		if err != nil {
			return r, err
		}
		r.Total++
		text := string(b)
		fkOff := strings.HasPrefix(text, "-- migrate:fk-off")
		text = lineComment.ReplaceAllString(text, "")
		for _, pt := range MigrationPatterns {
			c := pt.count(text)
			if pt.Name == "migrate:fk-off 지시" {
				c = 0
				if fkOff {
					c = 1
				}
			}
			if c > 0 {
				r.Counts[pt.Name] += c
				r.Files[pt.Name]++
			}
		}
	}
	return r, nil
}

// PkgResult 는 패키지(디렉터리)별 결과다.
type PkgResult struct {
	Pkg     string
	Stmts   int
	Counts  map[string]int
	PerFile map[string]map[string]int // 파일 → 구문 → 개수
}

// GoSource 는 root/internal 아래 _test.go 를 뺀 Go 파일의 백틱 SQL 문을 센다.
// 반환은 패키지 경로(root 기준) 순서의 결과다. LastInsertId 는 "LastInsertId" 항목으로 파일 전체에서 센다.
func GoSource(root string) ([]PkgResult, error) {
	byPkg := map[string]*PkgResult{}
	err := filepath.WalkDir(filepath.Join(root, "internal"), func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == "dbutil" || d.Name() == "pgbaseline" || d.Name() == "dialectstat" { // 방언 구현 자체(SQLite 조각 포함)와 이 패키지의 패턴 문자열은 세지 않는다
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		pkg := filepath.ToSlash(filepath.Dir(rel))
		pr := byPkg[pkg]
		if pr == nil {
			pr = &PkgResult{Pkg: pkg, Counts: map[string]int{}, PerFile: map[string]map[string]int{}}
			byPkg[pkg] = pr
		}
		add := func(name string, n int) {
			if n == 0 {
				return
			}
			pr.Counts[name] += n
			f := filepath.ToSlash(rel)
			if pr.PerFile[f] == nil {
				pr.PerFile[f] = map[string]int{}
			}
			pr.PerFile[f][name] += n
		}
		src := string(b)
		add("LastInsertId", len(LastInsertIDRe.FindAllString(src, -1)))
		for _, q := range backtickStrings(src) {
			if !sqlLeadRe.MatchString(q) {
				continue
			}
			pr.Stmts++
			for _, pt := range GoPatterns {
				add(pt.Name, pt.count(q))
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	var out []PkgResult
	for _, v := range byPkg {
		out = append(out, *v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Pkg < out[j].Pkg })
	return out, nil
}

// backtickStrings 는 Go 소스에서 백틱 문자열 내용을 뽑는다(주석 안의 백틱은 구분하지 않는 근사다: 소스 검사 용도로 충분하다).
func backtickStrings(src string) []string {
	var out []string
	for {
		i := strings.IndexByte(src, '`')
		if i < 0 {
			return out
		}
		j := strings.IndexByte(src[i+1:], '`')
		if j < 0 {
			return out
		}
		out = append(out, src[i+1:i+1+j])
		src = src[i+1+j+1:]
	}
}

// Totals 는 패키지 결과를 합친다.
func Totals(pkgs []PkgResult) (stmts int, counts map[string]int) {
	counts = map[string]int{}
	for _, p := range pkgs {
		stmts += p.Stmts
		for k, v := range p.Counts {
			counts[k] += v
		}
	}
	return
}

// Markdown 은 명세 3.2·3.3 에 붙일 수 있는 표 문자열이다.
func Markdown(mig Result, pkgs []PkgResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "마이그레이션 SQL %d개 (주석 제외)\n\n| 구문 | 횟수 | 파일 |\n|---|---|---|\n", mig.Total)
	for _, pt := range MigrationPatterns {
		fmt.Fprintf(&b, "| %s | %d | %d |\n", pt.Name, mig.Counts[pt.Name], mig.Files[pt.Name])
	}
	stmts, counts := Totals(pkgs)
	fmt.Fprintf(&b, "\nGo 소스 백틱 SQL 문 %d개\n\n| 패키지 | SQL 문 | 방언 의존 |\n|---|---|---|\n", stmts)
	for _, pk := range pkgs {
		if pk.Stmts == 0 && len(pk.Counts) == 0 {
			continue
		}
		var parts []string
		var keys []string
		for k := range pk.Counts {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			parts = append(parts, fmt.Sprintf("%s %d", k, pk.Counts[k]))
		}
		fmt.Fprintf(&b, "| %s | %d | %s |\n", pk.Pkg, pk.Stmts, strings.Join(parts, ", "))
	}
	var keys []string
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s %d", k, counts[k]))
	}
	fmt.Fprintf(&b, "| **합계** | **%d** | %s |\n", stmts, strings.Join(parts, ", "))
	return b.String()
}
