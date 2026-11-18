// Package pgdialect 는 SQLite 문법의 마이그레이션 SQL 을 PostgreSQL 문법으로 옮긴다(B37 6절).(1·2단계).
// 사람이 읽고 검토하는 초안을 만드는 도구용이다. 결과의 정확성은 PG 에 적용해 보는 시험과 스키마 동등성 시험이 보증한다.
package pgdialect

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Table 은 정렬할 표다.
type Table struct{ Name, SQL string }

// NowExpr 는 `strftime('%Y-%m-%dT%H:%M:%SZ','now')` 의 PG 식이다(dbutil.Postgres.NowUTC 와 같은 문자열).
const NowExpr = `to_char(now() AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS"Z"')`

// StripComments 는 `--` 줄 주석과 `/* */` 블록 주석을 지운다(작은따옴표·큰따옴표 안은 그대로).
func StripComments(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == '\'' || c == '"':
			j := skipQuoted(s, i)
			b.WriteString(s[i:j])
			i = j
		case c == '-' && i+1 < len(s) && s[i+1] == '-':
			for i < len(s) && s[i] != '\n' {
				i++
			}
		case c == '/' && i+1 < len(s) && s[i+1] == '*':
			j := strings.Index(s[i+2:], "*/")
			if j < 0 {
				return b.String()
			}
			i += 2 + j + 2
		default:
			b.WriteByte(c)
			i++
		}
	}
	return b.String()
}

func skipQuoted(s string, i int) int {
	q := s[i]
	j := i + 1
	for j < len(s) {
		if s[j] == q {
			if j+1 < len(s) && s[j+1] == q {
				j += 2
				continue
			}
			return j + 1
		}
		j++
	}
	return len(s)
}

// SplitStatements 는 파일을 문장으로 나눈다. 작은따옴표·주석 안의 `;` 와 CREATE TRIGGER ... BEGIN ... END 안의 `;` 는 나누지 않는다.
func SplitStatements(s string) []string {
	s = StripComments(s)
	var out []string
	start := 0
	depthTrig := false
	caseDepth := 0
	for i := 0; i < len(s); {
		c := s[i]
		if c == '\'' || c == '"' {
			i = skipQuoted(s, i)
			continue
		}
		if !depthTrig && i == start || (!depthTrig && strings.TrimSpace(s[start:i]) == "") {
			rest := strings.ToUpper(strings.TrimSpace(s[start:min(len(s), start+200)]))
			if strings.HasPrefix(rest, "CREATE TRIGGER") {
				depthTrig = true
			}
		}
		if depthTrig && isWordAt(s, i, "CASE") {
			caseDepth++
		}
		if depthTrig && caseDepth > 0 && isWordAt(s, i, "END") {
			caseDepth-- // CASE ... END 의 END 는 트리거 끝이 아니다
			i += 3
			continue
		}
		if depthTrig && isWordAt(s, i, "END") {
			// END; 로 끝나는 트리거 본문. CASE ... END 와 구분하려고 뒤가 `;` 인지 본다.
			j := i + 3
			for j < len(s) && (s[j] == ' ' || s[j] == '\n' || s[j] == '\t' || s[j] == '\r') {
				j++
			}
			if j < len(s) && s[j] == ';' {
				out = append(out, strings.TrimSpace(s[start:j]))
				i = j + 1
				start = i
				depthTrig = false
				continue
			}
		}
		if !depthTrig && c == ';' {
			if t := strings.TrimSpace(s[start:i]); t != "" {
				out = append(out, t)
			}
			i++
			start = i
			continue
		}
		i++
	}
	if t := strings.TrimSpace(s[start:]); t != "" {
		out = append(out, t)
	}
	return out
}

func isWordAt(s string, i int, w string) bool {
	if i+len(w) > len(s) || !strings.EqualFold(s[i:i+len(w)], w) {
		return false
	}
	if i > 0 && isIdent(s[i-1]) {
		return false
	}
	return i+len(w) == len(s) || !isIdent(s[i+len(w)])
}

func isIdent(c byte) bool {
	return c == '_' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

// TranslateFile 은 SQLite 마이그레이션 파일 전체를 PG 문장 목록으로 바꾼다.
func TranslateFile(src string) (string, error) {
	var b strings.Builder
	for _, st := range SplitStatements(src) {
		out, err := TranslateStatement(st)
		if err != nil {
			return "", fmt.Errorf("%w\n문장: %.120s", err, st)
		}
		b.WriteString(out)
		b.WriteString(";\n\n")
	}
	return b.String(), nil
}

var quotedIdentRe = regexp.MustCompile(`"(\w+)"`)

// normalizeIdents 는 큰따옴표로 감싼 단순 식별자("users")를 따옴표 없이 쓴다. SQLite 가 표 이름을 바꾼 뒤 저장하는 형태다.
// 작은따옴표 문자열 안은 건드리지 않는다.
func normalizeIdents(s string) string {
	return mapCode(s, func(c string) string { return quotedIdentRe.ReplaceAllString(c, "$1") })
}

// TranslateStatement 는 문장 하나를 옮긴다(끝의 `;` 없이 돌려준다. 트리거는 함수와 트리거 두 문장이다).
func TranslateStatement(stmt string) (string, error) {
	stmt = normalizeIdents(strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(StripComments(stmt)), ";")))
	up := strings.ToUpper(stmt)
	switch {
	case strings.HasPrefix(up, "CREATE TABLE"):
		return translateCreateTable(stmt)
	case strings.HasPrefix(up, "CREATE TRIGGER"):
		return translateTrigger(stmt)
	case strings.HasPrefix(up, "CREATE VIEW"):
		return castSums(fixExpr(stmt)), nil
	case strings.HasPrefix(up, "PRAGMA"):
		return "", fmt.Errorf("PRAGMA 는 PG 에 없습니다(문장을 지우세요)")
	case strings.HasPrefix(up, "INSERT OR IGNORE"):
		s := fixExpr(stmt)
		s = regexp.MustCompile(`(?i)^INSERT\s+OR\s+IGNORE\s+INTO`).ReplaceAllString(s, "INSERT INTO")
		return s + " ON CONFLICT DO NOTHING", nil
	case strings.HasPrefix(up, "INSERT OR REPLACE"):
		return "", fmt.Errorf("INSERT OR REPLACE 는 ON CONFLICT ... DO UPDATE 로 직접 옮기세요")
	case strings.HasPrefix(up, "ALTER TABLE") || strings.HasPrefix(up, "CREATE INDEX") || strings.HasPrefix(up, "CREATE UNIQUE INDEX"):
		return mapTypes(fixExpr(stmt)), nil
	}
	return fixExpr(stmt), nil
}

// ---- 표 ----

var refRe = regexp.MustCompile(`(?i)\bREFERENCES\s+(\w+)`)

// Sorted 는 SortTables 의 결과다. Deferred[표] 는 순환을 끊으려고 CREATE TABLE 에서 빼고 나중에 ALTER TABLE 로 거는 참조 대상 표 이름이다.
type Sorted struct {
	Tables   []Table
	Deferred map[string]map[string]bool
}

// SortTables 는 외래 키 참조 순서(참조되는 표가 먼저)로 표를 정렬한다. 자기 참조는 무시한다.
// 순환이 있으면 순환을 닫는 참조 하나를 Deferred 로 돌려 CREATE TABLE 뒤에 ALTER TABLE 로 걸게 한다.
func SortTables(ts []Table) (Sorted, error) {
	byName := map[string]Table{}
	deps := map[string][]string{}
	var names []string
	for _, t := range ts {
		byName[t.Name] = t
		names = append(names, t.Name)
		seen := map[string]bool{}
		for _, m := range refRe.FindAllStringSubmatch(normalizeIdents(StripComments(t.SQL)), -1) {
			if m[1] != t.Name && !seen[m[1]] {
				seen[m[1]] = true
				deps[t.Name] = append(deps[t.Name], m[1])
			}
		}
	}
	res := Sorted{Deferred: map[string]map[string]bool{}}
	state := map[string]int{}
	var visit func(n string)
	visit = func(n string) {
		if _, ok := byName[n]; !ok {
			return
		}
		state[n] = 1
		ds := append([]string(nil), deps[n]...)
		sort.Strings(ds)
		for _, d := range ds {
			if _, ok := byName[d]; !ok {
				continue
			}
			switch state[d] {
			case 1: // 순환: n 이 d 를 참조하는 간선을 끊는다
				if res.Deferred[n] == nil {
					res.Deferred[n] = map[string]bool{}
				}
				res.Deferred[n][d] = true
			case 0:
				visit(d)
			}
		}
		state[n] = 2
		res.Tables = append(res.Tables, byName[n])
	}
	for _, n := range names {
		if state[n] == 0 {
			visit(n)
		}
	}
	return res, nil
}

func splitTop(s string) []string {
	var parts []string
	depth, start := 0, 0
	for i := 0; i < len(s); {
		switch s[i] {
		case '\'', '"':
			i = skipQuoted(s, i)
			continue
		case '(':
			depth++
		case ')':
			depth--
		case ',':
			if depth == 0 {
				parts = append(parts, strings.TrimSpace(s[start:i]))
				start = i + 1
			}
		}
		i++
	}
	parts = append(parts, strings.TrimSpace(s[start:]))
	return parts
}

var tableHeadRe = regexp.MustCompile(`(?is)^CREATE\s+TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?(\w+)\s*\((.*)\)\s*$`)

func translateCreateTable(stmt string) (string, error) {
	c, _, err := TranslateCreateTable(stmt, nil)
	return c, err
}

var tableOptsRe = regexp.MustCompile(`(?is)\)\s*(?:(?:WITHOUT\s+ROWID|STRICT)\s*,?\s*)+$`)

var inlineRefRe = regexp.MustCompile(`(?is)\s+REFERENCES\s+(\w+)\s*\((\w+)\)((?:\s+ON\s+(?:DELETE|UPDATE)\s+(?:CASCADE|SET\s+NULL|SET\s+DEFAULT|RESTRICT|NO\s+ACTION))*)`)
var colNameRe = regexp.MustCompile(`^(\w+)\s`)

// TranslateCreateTable 은 CREATE TABLE 을 옮긴다. defer 에 든 표를 가리키는 인라인 외래 키는 표 정의에서 빼고
// 표 생성 뒤에 실행할 `ALTER TABLE ... ADD FOREIGN KEY` 문으로 돌려준다(외래 키 순환 끊기).
func TranslateCreateTable(stmt string, defer_ map[string]bool) (string, []string, error) {
	var alters []string
	stmt = normalizeIdents(strings.TrimSpace(StripComments(stmt)))
	stmt = tableOptsRe.ReplaceAllString(stmt, ")") // WITHOUT ROWID·STRICT 는 PG 에 없다
	m := tableHeadRe.FindStringSubmatch(stmt)
	if m == nil {
		return "", nil, fmt.Errorf("CREATE TABLE 을 해석할 수 없습니다")
	}
	name, body := m[1], m[2]
	var elems []string
	for _, e := range splitTop(body) {
		e = strings.TrimSpace(e)
		if e == "" {
			continue
		}
		if len(defer_) > 0 {
			if rm := inlineRefRe.FindStringSubmatch(e); rm != nil && defer_[rm[1]] {
				if cm := colNameRe.FindStringSubmatch(e); cm != nil && !strings.HasPrefix(strings.ToUpper(e), "FOREIGN") {
					alters = append(alters, fmt.Sprintf("ALTER TABLE %s ADD FOREIGN KEY (%s) REFERENCES %s(%s)%s", name, cm[1], rm[1], rm[2], strings.TrimRight(rm[3], " ")))
					e = inlineRefRe.ReplaceAllString(e, "")
				}
			}
		}
		te, err := translateElem(name, e)
		if err != nil {
			return "", nil, err
		}
		elems = append(elems, "    "+te)
	}
	return fmt.Sprintf("CREATE TABLE %s (\n%s\n)", name, strings.Join(elems, ",\n")), alters, nil
}

var identRe = regexp.MustCompile(`(?is)^(\w+)\s+INTEGER\s+PRIMARY\s+KEY(\s+AUTOINCREMENT)?(.*)$`)

func translateElem(table, e string) (string, error) {
	if m := identRe.FindStringSubmatch(e); m != nil {
		col, auto, rest := m[1], m[2] != "", strings.TrimSpace(m[3])
		fixed := fixExpr(rest)
		if isIdentity(table, col, auto, rest) {
			return strings.TrimSpace(fmt.Sprintf("%s BIGINT GENERATED BY DEFAULT AS IDENTITY PRIMARY KEY %s", col, fixed)), nil
		}
		return strings.TrimSpace(fmt.Sprintf("%s BIGINT PRIMARY KEY %s", col, fixed)), nil
	}
	return mapTypes(fixExpr(e)), nil
}

// isIdentity 는 INTEGER PRIMARY KEY 열을 식별자(자동 증가)로 만들지 정한다.
// AUTOINCREMENT 는 식별자다. 단 audit_logs.seq 는 코드가 이전 seq+1 을 직접 넣는 값이라 식별자가 아니다(B37 9.1).
// AUTOINCREMENT 없는 INTEGER PRIMARY KEY 는 SQLite 에서 NULL 을 넣으면 자동 번호가 붙으므로 `id` 열이면 식별자로 보되,
// 값이 1로 고정된 단일 행 표(CHECK (id = 1))와 다른 표를 가리키는 열(REFERENCES: user_id·line_id 같은 1:1 키)은 일반 키다.
func isIdentity(table, col string, auto bool, rest string) bool {
	if table == "audit_logs" && col == "seq" {
		return false
	}
	if auto {
		return true
	}
	if !strings.EqualFold(col, "id") || regexp.MustCompile(`(?i)\bREFERENCES\b`).MatchString(rest) {
		return false
	}
	return !regexp.MustCompile(`(?i)CHECK\s*\(\s*` + col + `\s*=\s*1\s*\)`).MatchString(rest)
}

// IdentityColumn 은 표의 식별자 열 이름을 돌려준다(없으면 빈 문자열).
func IdentityColumn(table, createSQL string) string {
	for _, e := range splitTop(tableHeadBody(createSQL)) {
		if m := identRe.FindStringSubmatch(strings.TrimSpace(StripComments(e))); m != nil && isIdentity(table, m[1], m[2] != "", m[3]) {
			return m[1]
		}
	}
	return ""
}

func tableHeadBody(createSQL string) string {
	m := tableHeadRe.FindStringSubmatch(tableOptsRe.ReplaceAllString(normalizeIdents(strings.TrimSpace(StripComments(createSQL))), ")"))
	if m == nil {
		return ""
	}
	return m[2]
}

var (
	intRe  = regexp.MustCompile(`(?i)\bINTEGER\b`)
	blobRe = regexp.MustCompile(`(?i)\bBLOB\b`)
	realRe = regexp.MustCompile(`(?i)\bREAL\b`)
)

// mapTypes 는 타입 이름을 바꾼다(작은따옴표 문자열 안은 건드리지 않는다).
func mapTypes(s string) string {
	return mapCode(s, func(c string) string {
		c = intRe.ReplaceAllString(c, "BIGINT")
		c = blobRe.ReplaceAllString(c, "BYTEA")
		c = realRe.ReplaceAllString(c, "DOUBLE PRECISION")
		return c
	})
}

// mapCode 는 작은따옴표 문자열을 뺀 코드 조각에만 f 를 적용한다.
func mapCode(s string, f func(string) string) string {
	var b strings.Builder
	start := 0
	for i := 0; i < len(s); {
		if s[i] == '\'' {
			b.WriteString(f(s[start:i]))
			j := skipQuoted(s, i)
			b.WriteString(s[i:j])
			i, start = j, j
			continue
		}
		i++
	}
	b.WriteString(f(s[start:]))
	return b.String()
}

// ---- 식 ----

var (
	nowRe    = regexp.MustCompile(`(?i)strftime\(\s*'%Y-%m-%dT%H:%M:%SZ'\s*,\s*'now'\s*\)`)
	kstNowRe = regexp.MustCompile(`(?i)\bdate\(\s*'now'\s*,\s*'\+9 hours'\s*\)`)
	kstColRe = regexp.MustCompile(`(?i)\bdate\(\s*([A-Za-z_][A-Za-z_0-9.]*)\s*,\s*'\+9 hours'\s*\)`)
	globRe   = regexp.MustCompile(`([A-Za-z_][A-Za-z_0-9.]*)\s+GLOB\s+'([^']*)'`)
	isRe     = regexp.MustCompile(`(?i)\bIS\s+(NOT\s+)?(\w+)`)
	sumRe    = regexp.MustCompile(`(?i)\bSUM\s*\(`)
)

// fixExpr 는 SQLite 함수·연산자를 PG 식으로 바꾼다: strftime 'now', date(.., '+9 hours'), GLOB, IS [NOT] 값 비교.
func fixExpr(s string) string {
	s = nowRe.ReplaceAllString(s, NowExpr)
	s = kstNowRe.ReplaceAllString(s, `to_char(now() AT TIME ZONE 'Asia/Seoul', 'YYYY-MM-DD')`)
	s = kstColRe.ReplaceAllString(s, `to_char(($1)::timestamp AT TIME ZONE 'UTC' AT TIME ZONE 'Asia/Seoul', 'YYYY-MM-DD')`)
	s = globRe.ReplaceAllStringFunc(s, func(m string) string {
		g := globRe.FindStringSubmatch(m)
		return g[1] + " ~ '" + globToRegex(g[2]) + "'"
	})
	return mapCode(s, func(c string) string {
		return isRe.ReplaceAllStringFunc(c, func(m string) string {
			g := isRe.FindStringSubmatch(m)
			word := strings.ToUpper(g[2])
			if word == "NULL" {
				return m // IS [NOT] NULL
			}
			if g[1] != "" {
				return "IS DISTINCT FROM " + g[2]
			}
			if word == "NOT" {
				return m
			}
			return "IS NOT DISTINCT FROM " + g[2]
		})
	})
}

// globToRegex 는 SQLite GLOB 패턴을 앵커가 붙은 POSIX 정규식으로 바꾼다. [..] 는 그대로, * 는 .*, ? 는 . 이다.
func globToRegex(g string) string {
	var b strings.Builder
	b.WriteByte('^')
	for i := 0; i < len(g); i++ {
		c := g[i]
		switch c {
		case '*':
			b.WriteString(".*")
		case '?':
			b.WriteByte('.')
		case '[':
			j := strings.IndexByte(g[i:], ']')
			if j < 0 {
				b.WriteString(`\[`)
				continue
			}
			b.WriteString(g[i : i+j+1])
			i += j
		case '.', '+', '(', ')', '{', '}', '|', '^', '$', '\\':
			b.WriteByte('\\')
			b.WriteByte(c)
		default:
			b.WriteByte(c)
		}
	}
	b.WriteByte('$')
	return b.String()
}

// castSums 는 뷰 안의 SUM(...) 결과를 BIGINT 로 맞춘다(PG 의 SUM(bigint) 는 numeric 이다).
func castSums(s string) string {
	var b strings.Builder
	for {
		loc := sumRe.FindStringIndex(s)
		if loc == nil {
			b.WriteString(s)
			return b.String()
		}
		b.WriteString(s[:loc[1]])
		depth, j := 1, loc[1]
		for j < len(s) && depth > 0 {
			switch s[j] {
			case '(':
				depth++
			case ')':
				depth--
			}
			j++
		}
		b.WriteString(s[loc[1]:j])
		b.WriteString("::BIGINT")
		s = s[j:]
	}
}

// ---- 트리거 ----

var trigHeadRe = regexp.MustCompile(`(?is)^CREATE\s+TRIGGER\s+(\w+)\s+(BEFORE|AFTER)\s+(INSERT|UPDATE|DELETE)(\s+OF\s+[\w\s,]+?)?\s+ON\s+(\w+)\s*(.*)$`)
var raiseRe = regexp.MustCompile(`(?is)^SELECT\s+RAISE\s*\(\s*ABORT\s*,\s*'((?:[^']|'')*)'\s*\)\s*(?:WHERE\s+(.*))?$`)

var monthExprRe = regexp.MustCompile(`(?is)FROM month_closes WHERE status = 'closed' AND month = (substr\(.*?, 1, 7\))\)`)

// triggerPrelude 는 트리거 본문 앞에 넣는 PG 전용 잠금 문장이다(SQLite 는 연결 하나가 쓰기를 직렬화해 필요 없다).
var triggerPrelude = map[string]string{
	"trg_receipt_no_over": "PERFORM 1 FROM po_lines WHERE id = NEW.po_line_id FOR UPDATE;",
}

func translateTrigger(stmt string) (string, error) {
	m := trigHeadRe.FindStringSubmatch(stmt)
	if m == nil {
		return "", fmt.Errorf("CREATE TRIGGER 를 해석할 수 없습니다")
	}
	name, when, ev, ofCols, table, rest := m[1], strings.ToUpper(m[2]), strings.ToUpper(m[3]), strings.TrimSpace(m[4]), m[5], strings.TrimSpace(m[6])
	cond := ""
	bi := regexp.MustCompile(`(?i)\bBEGIN\b`).FindStringIndex(rest)
	if bi == nil {
		return "", fmt.Errorf("트리거 %s: BEGIN 이 없습니다", name)
	}
	head := strings.TrimSpace(rest[:bi[0]])
	if hu := strings.ToUpper(head); strings.HasPrefix(hu, "FOR EACH ROW") {
		head = strings.TrimSpace(head[len("FOR EACH ROW"):])
	}
	if strings.HasPrefix(strings.ToUpper(head), "WHEN") {
		cond = fixExpr(strings.TrimSpace(head[4:]))
	} else if head != "" {
		return "", fmt.Errorf("트리거 %s: 알 수 없는 절 %q", name, head)
	}
	body := strings.TrimSpace(rest[bi[1]:])
	body = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(body), "END"))
	var stmts []string
	for _, p := range splitSemis(body) {
		if p = strings.TrimSpace(p); p != "" {
			stmts = append(stmts, p)
		}
	}
	var fb strings.Builder
	// S3: 월 마감 검사 트리거는 월 행을 읽기 전에 그 달의 공유 잠금을 잡는다(마감 트랜잭션은 같은 키의 배타 잠금: dbutil.AcquireMonthClose).
	// 진행 중인 쓰기는 마감이 기다리고, 마감이 먼저면 쓰기는 마감 결과(닫힘)를 보고 거절된다. 잠금은 커밋까지 유지된다.
	if mm := monthExprRe.FindStringSubmatch(cond); mm != nil {
		fmt.Fprintf(&fb, "  PERFORM pg_advisory_xact_lock_shared(hashtextextended('procure-ledger:month:' || %s, 0));\n", mm[1])
	}
	// S4: 입고 초과 검사 전에 PO 줄 행을 잠가 같은 줄에 대한 동시 입고를 줄 세운다(뒤 입고는 앞 입고가 커밋된 뒤의 합계를 본다).
	if pre, ok := triggerPrelude[name]; ok {
		fb.WriteString("  " + pre + "\n")
	}
	ind := "  "
	if cond != "" {
		fmt.Fprintf(&fb, "  IF %s THEN\n", cond)
		ind = "    "
	}
	for _, s := range stmts {
		if rm := raiseRe.FindStringSubmatch(s); rm != nil {
			msg := rm[1]
			if rm[2] != "" {
				fmt.Fprintf(&fb, "%sIF %s THEN\n%s  RAISE EXCEPTION USING ERRCODE = 'P0001', MESSAGE = '%s';\n%sEND IF;\n", ind, fixExpr(rm[2]), ind, msg, ind)
			} else {
				fmt.Fprintf(&fb, "%sRAISE EXCEPTION USING ERRCODE = 'P0001', MESSAGE = '%s';\n", ind, msg)
			}
			continue
		}
		t, err := TranslateStatement(s)
		if err != nil {
			return "", fmt.Errorf("트리거 %s: %w", name, err)
		}
		fmt.Fprintf(&fb, "%s%s;\n", ind, t)
	}
	if cond != "" {
		fb.WriteString("  END IF;\n")
	}
	ret := "NEW"
	if when == "AFTER" {
		ret = "NULL"
	} else if ev == "DELETE" {
		ret = "OLD"
	}
	fn := name + "_fn"
	of := ""
	if ofCols != "" {
		of = " " + strings.Join(strings.Fields(ofCols), " ")
	}
	return fmt.Sprintf("CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $fn$\nBEGIN\n%s  RETURN %s;\nEND\n$fn$;\nCREATE TRIGGER %s %s %s%s ON %s FOR EACH ROW EXECUTE FUNCTION %s()",
		fn, fb.String(), ret, name, when, ev, of, table, fn), nil
}

// splitSemis 는 트리거 본문을 `;` 로 나눈다(따옴표·괄호 안은 나누지 않는다).
func splitSemis(s string) []string {
	var parts []string
	depth, start := 0, 0
	for i := 0; i < len(s); {
		switch s[i] {
		case '\'', '"':
			i = skipQuoted(s, i)
			continue
		case '(':
			depth++
		case ')':
			depth--
		case ';':
			if depth == 0 {
				parts = append(parts, s[start:i])
				start = i + 1
			}
		}
		i++
	}
	parts = append(parts, s[start:])
	return parts
}

// ---- 값 ----

// Literal 은 SQLite 에서 읽은 값을 PG 리터럴로 쓴다. colType 은 선언 타입(대문자)이다.
func Literal(v any, colType string) string {
	switch x := v.(type) {
	case nil:
		return "NULL"
	case int64:
		return strconv.FormatInt(x, 10)
	case float64:
		return strconv.FormatFloat(x, 'g', -1, 64)
	case bool:
		if x {
			return "1"
		}
		return "0"
	case []byte:
		if colType == "BLOB" {
			return fmt.Sprintf(`'\x%x'::bytea`, x)
		}
		return quote(string(x))
	case string:
		if tsRe.MatchString(x) {
			return NowExpr // 시드 행의 생성 시각은 적용 시각이다(SQLite 마이그레이션이 DEFAULT 로 넣은 값과 같은 의미). 생성 시각을 파일에 박지 않는다.
		}
		return quote(x)
	}
	return quote(fmt.Sprint(v))
}

var tsRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z$`)

func quote(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }
