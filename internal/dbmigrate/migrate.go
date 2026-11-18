package dbmigrate

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/choibongseok/procure-ledger/internal/audit"
	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/dbutil"
	"github.com/choibongseok/procure-ledger/migrations"
)

// DefaultMaxSourceBytes 는 한 트랜잭션 이관을 허용하는 원본 파일 크기 상한이다(명세 11.2-1).
const DefaultMaxSourceBytes int64 = 2 << 30

// Options 는 이관 실행 설정이다.
type Options struct {
	From           string // 원본 SQLite 파일(읽기 전용으로만 연다)
	To             string // 대상 PostgreSQL 연결 문자열
	AllowInsecure  bool   // TLS 없는 연결 허용(개발용)
	DryRun         bool   // 모든 검사를 하고 마지막에 롤백한다
	MaxSourceBytes int64  // 0 이면 DefaultMaxSourceBytes
	Out            io.Writer
	// Finalize 는 검증 3 이 끝난 뒤 같은 트랜잭션에서 불린다(감사 체인 끝에 이관 기록을 덧붙이는 자리). 오류면 롤백한다.
	// 끝난 뒤 체인을 한 번 더 검증한다.
	Finalize func(ctx context.Context, tx contract.Tx, store *audit.Store, rep *Report) error
}

// TableResult 는 표 하나의 이관 결과다.
type TableResult struct {
	Name    string
	Skipped bool
	Reason  string
	Src     int64
	Dst     int64
	SrcSum  string
	DstSum  string
}

// OK 는 행 수와 체크섬이 모두 맞는지다(제외 표는 항상 true).
func (t TableResult) OK() bool { return t.Skipped || (t.Src == t.Dst && t.SrcSum == t.DstSum) }

// Check 는 불변식·체인 점검 한 건이다.
type Check struct {
	Name   string
	OK     bool
	Detail string
}

// Report 는 이관 판정표다.
type Report struct {
	SourceSHA256  string
	SchemaVersion string
	Tables        []TableResult
	Checks        []Check
	DryRun        bool
	Committed     bool
}

// OK 는 모든 표와 점검이 통과했는지다.
func (r Report) OK() bool {
	for _, t := range r.Tables {
		if !t.OK() {
			return false
		}
	}
	for _, c := range r.Checks {
		if !c.OK {
			return false
		}
	}
	return true
}

// Rows 는 표별 이관 행 수(제외 표 제외)의 합이다.
func (r Report) Rows() (n int64) {
	for _, t := range r.Tables {
		if !t.Skipped {
			n += t.Dst
		}
	}
	return n
}

func (o Options) say(format string, a ...any) {
	if o.Out != nil {
		fmt.Fprintf(o.Out, format+"\n", a...)
	}
}

// Run 은 이관을 수행한다. 검증이 하나라도 실패하면 대상은 롤백되고(원본은 그대로) 판정표와 오류를 돌려준다.
func Run(ctx context.Context, opt Options) (Report, error) {
	var rep Report
	rep.DryRun = opt.DryRun
	max := opt.MaxSourceBytes
	if max <= 0 {
		max = DefaultMaxSourceBytes
	}
	// 1. 사전 조건: 원본.
	st, err := os.Stat(opt.From)
	if err != nil || st.IsDir() {
		return rep, fmt.Errorf("원본 SQLite 파일을 찾을 수 없습니다: %s", opt.From)
	}
	if st.Size() > max {
		return rep, fmt.Errorf("원본 파일이 %d바이트로 한 번에 이관할 수 있는 크기(%d)를 넘습니다", st.Size(), max)
	}
	sha, err := fileSHA(opt.From)
	if err != nil {
		return rep, err
	}
	rep.SourceSHA256 = sha
	src, err := sql.Open("sqlite", "file:"+opt.From+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		return rep, err
	}
	defer src.Close()
	src.SetMaxOpenConns(1)
	stx, err := src.BeginTx(ctx, &sql.TxOptions{ReadOnly: true}) // 이관 내내 일관된 스냅샷
	if err != nil {
		return rep, fmt.Errorf("원본을 읽기 전용으로 열 수 없습니다(서버가 쓰는 중이면 멈추세요): %w", err)
	}
	defer stx.Rollback()
	var srcVer sql.NullString
	if err := stx.QueryRowContext(ctx, `SELECT MAX(name) FROM schema_migrations`).Scan(&srcVer); err != nil || !srcVer.Valid {
		return rep, fmt.Errorf("원본에서 schema_migrations 를 읽을 수 없습니다: 서버 데이터베이스 파일이 맞나요?")
	}
	rep.SchemaVersion = srcVer.String

	// 2. 사전 조건: 대상.
	dst, err := dbutil.OpenPG(ctx, opt.To, dbutil.PGOptions{MaxConns: 4, AllowInsecure: opt.AllowInsecure})
	if err != nil {
		return rep, err
	}
	defer dst.Close()
	if err := assertTargetEmpty(ctx, dst); err != nil {
		return rep, err
	}
	if err := migrations.ApplyDialect(ctx, dst, dbutil.Postgres); err != nil {
		return rep, fmt.Errorf("대상에 PG 마이그레이션을 적용하지 못했습니다: %w", err)
	}
	var dstVer string
	if err := dst.QueryRowContext(ctx, `SELECT MAX(name) FROM schema_migrations`).Scan(&dstVer); err != nil {
		return rep, err
	}
	if dstVer != srcVer.String {
		return rep, fmt.Errorf("원본의 마이그레이션 최종 번호(%s)가 대상 PG 기준(%s)과 다릅니다. 원본을 먼저 최신으로 올리세요", srcVer.String, dstVer)
	}

	// 3. 계획.
	all, fks, err := catalog(ctx, dst)
	if err != nil {
		return rep, err
	}
	srcTables, err := sqliteTables(ctx, stx)
	if err != nil {
		return rep, err
	}
	plan := map[string]*tableInfo{}
	for n, t := range all {
		if ok, _ := Classified(n); ok {
			plan[n] = t
		}
	}
	for _, n := range srcTables {
		if _, ok := all[n]; !ok {
			if _, skip := Skip[n]; !skip {
				return rep, fmt.Errorf("원본의 표 %s 가 대상 PG 스키마에 없습니다(마이그레이션 번호는 같은데 표가 다릅니다)", n)
			}
		}
	}
	for n := range plan {
		if !contains(srcTables, n) {
			return rep, fmt.Errorf("대상의 표 %s 가 원본에 없습니다", n)
		}
	}
	ordered, err := order(plan, fks)
	if err != nil {
		return rep, err
	}
	for _, n := range ordered {
		if err := sameColumns(ctx, stx, plan[n]); err != nil {
			return rep, err
		}
	}

	// 4. 적재(한 트랜잭션).
	tx, err := dst.BeginTx(ctx, nil)
	if err != nil {
		return rep, err
	}
	defer tx.Rollback()
	for _, n := range ordered {
		if _, err := tx.ExecContext(ctx, `ALTER TABLE `+qi(n)+` DISABLE TRIGGER USER`); err != nil {
			return rep, fmt.Errorf("표 %s 의 보호 트리거를 잠시 끌 수 없습니다(표 소유자 권한이 필요합니다): %w", n, err)
		}
	}
	quoted := make([]string, len(ordered))
	for i, n := range ordered {
		quoted[i] = qi(n)
	}
	if _, err := tx.ExecContext(ctx, `TRUNCATE `+strings.Join(quoted, ", ")+` RESTART IDENTITY CASCADE`); err != nil {
		return rep, fmt.Errorf("대상 시드 데이터를 지울 수 없습니다: %w", err)
	}
	srcSums := map[string]*tableSum{}
	srcCounts := map[string]int64{}
	for _, n := range ordered {
		t := plan[n]
		sum, cnt, err := copyTable(ctx, stx, tx, t)
		if err != nil {
			return rep, fmt.Errorf("표 %s 이관 중단: %w", n, err)
		}
		srcSums[n], srcCounts[n] = sum, cnt
		opt.say("  이관 %-28s %8d행", n, cnt)
	}
	for _, n := range ordered {
		if err := fillDeferred(ctx, stx, tx, plan[n]); err != nil {
			return rep, fmt.Errorf("표 %s 의 순환 외래 키 채우기 실패: %w", n, err)
		}
	}
	for _, n := range ordered {
		if _, err := tx.ExecContext(ctx, `ALTER TABLE `+qi(n)+` ENABLE TRIGGER USER`); err != nil {
			return rep, err
		}
	}
	if err := fixSequences(ctx, tx, plan, ordered); err != nil {
		return rep, err
	}

	// 5. 검증.
	for _, n := range ordered {
		dsum, dcnt, err := readSum(ctx, tx, plan[n])
		if err != nil {
			return rep, err
		}
		rep.Tables = append(rep.Tables, TableResult{Name: n, Src: srcCounts[n], Dst: dcnt, SrcSum: srcSums[n].sum(), DstSum: dsum.sum()})
	}
	skipped := make([]string, 0, len(Skip))
	for n := range Skip {
		skipped = append(skipped, n)
	}
	sort.Strings(skipped)
	for _, n := range skipped {
		rep.Tables = append(rep.Tables, TableResult{Name: n, Skipped: true, Reason: Skip[n]})
	}
	store := audit.New(dst)
	rep.Checks = append(rep.Checks, chainChecks(ctx, store, stx, tx, "이관 직후")...)
	rep.Checks = append(rep.Checks, invariants(ctx, tx)...)
	if opt.Finalize != nil {
		if err := opt.Finalize(ctx, tx, store, &rep); err != nil {
			return rep, fmt.Errorf("이관 기록을 남기지 못했습니다: %w", err)
		}
		res, err := store.VerifyChainIn(ctx, tx)
		rep.Checks = append(rep.Checks, Check{Name: "감사 체인(이관 기록 추가 뒤)", OK: err == nil && res.OK, Detail: fmt.Sprintf("검사 %d건", res.Checked)})
	}
	if !rep.OK() {
		return rep, errors.New("이관 검증에 실패해 대상을 되돌렸습니다(원본은 그대로입니다)")
	}
	if opt.DryRun {
		return rep, nil // 롤백(defer)
	}
	if err := tx.Commit(); err != nil {
		return rep, err
	}
	rep.Committed = true
	return rep, nil
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

func qi(name string) string { return `"` + strings.ReplaceAll(name, `"`, `""`) + `"` }

func fileSHA(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// assertTargetEmpty 는 대상이 비어 있는지 본다: 표가 없거나, 이 도구가 이전에 스키마만 적용해 둔 상태(핵심 사용자 데이터 표가 모두 비어 있음)여야 한다(E9).
func assertTargetEmpty(ctx context.Context, db *sql.DB) error {
	var tables int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = 'public' AND table_type = 'BASE TABLE'`).Scan(&tables); err != nil {
		return err
	}
	if tables == 0 {
		return nil
	}
	for _, t := range []string{"audit_logs", "users", "purchase_requests", "journals", "stock_ledger", "vendors", "items"} {
		var n int64
		err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+qi(t)).Scan(&n)
		if err != nil {
			return fmt.Errorf("대상 데이터베이스가 비어 있지 않고 이 서버의 스키마도 아닙니다: %w", err)
		}
		if n > 0 {
			return fmt.Errorf("대상 데이터베이스가 비어 있지 않습니다(%s 표에 %d행). 덮어쓰지 않습니다. 새 데이터베이스를 만드세요", t, n)
		}
	}
	return nil
}

func sqliteTables(ctx context.Context, q *sql.Tx) ([]string, error) {
	rows, err := q.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// sameColumns 는 원본 표의 열 이름이 대상과 같은지 본다.
func sameColumns(ctx context.Context, q *sql.Tx, t *tableInfo) error {
	rows, err := q.QueryContext(ctx, `SELECT name FROM pragma_table_info('`+strings.ReplaceAll(t.Name, "'", "''")+`')`)
	if err != nil {
		return err
	}
	defer rows.Close()
	have := map[string]bool{}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return err
		}
		have[n] = true
	}
	for _, c := range t.Cols {
		if !have[c.Name] {
			return fmt.Errorf("표 %s: 원본에 열 %s 가 없습니다", t.Name, c.Name)
		}
		delete(have, c.Name)
	}
	if len(have) > 0 {
		var extra []string
		for n := range have {
			extra = append(extra, n)
		}
		sort.Strings(extra)
		return fmt.Errorf("표 %s: 대상에 없는 원본 열이 있습니다(%s)", t.Name, strings.Join(extra, ", "))
	}
	return nil
}

func colList(t *tableInfo) string {
	names := make([]string, len(t.Cols))
	for i, c := range t.Cols {
		names[i] = qi(c.Name)
	}
	return strings.Join(names, ", ")
}

// copyTable 은 원본 표의 모든 행을 대상에 넣는다. 순환 외래 키 열은 NULL 로 넣는다(fillDeferred 가 나중에 채운다).
func copyTable(ctx context.Context, src *sql.Tx, dst *sql.Tx, t *tableInfo) (*tableSum, int64, error) {
	sum := &tableSum{}
	rows, err := src.QueryContext(ctx, `SELECT `+colList(t)+` FROM `+qi(t.Name))
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	def := t.deferredSet()
	n := len(t.Cols)
	perStmt := 60000 / n
	if perStmt > 500 {
		perStmt = 500
	}
	override := ""
	if len(t.identityCols()) > 0 {
		override = " OVERRIDING SYSTEM VALUE"
	}
	var batch [][]any
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		var sb strings.Builder
		args := make([]any, 0, len(batch)*n)
		sb.WriteString(`INSERT INTO ` + qi(t.Name) + ` (` + colList(t) + `)` + override + ` VALUES `)
		for i, r := range batch {
			if i > 0 {
				sb.WriteByte(',')
			}
			sb.WriteByte('(')
			for j := range r {
				if j > 0 {
					sb.WriteByte(',')
				}
				sb.WriteByte('?')
			}
			sb.WriteByte(')')
			args = append(args, r...)
		}
		_, err := dst.ExecContext(ctx, sb.String(), args...)
		batch = batch[:0]
		return err
	}
	var count int64
	for rows.Next() {
		raw := make([]any, n)
		ptr := make([]any, n)
		for i := range raw {
			ptr[i] = &raw[i]
		}
		if err := rows.Scan(ptr...); err != nil {
			return nil, 0, err
		}
		count++
		conv := make([]any, n)
		ins := make([]any, n)
		for i, c := range t.Cols {
			v, err := convert(raw[i], c.Type)
			if err != nil {
				return nil, 0, fmt.Errorf("%d번째 행 열 %s: %w", count, c.Name, err)
			}
			conv[i], ins[i] = v, v
			if def[c.Name] {
				ins[i] = nil
			}
		}
		sum.add(conv)
		batch = append(batch, ins)
		if len(batch) >= perStmt {
			if err := flush(); err != nil {
				return nil, 0, fmt.Errorf("%d번째 행 근처: %w", count, err)
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	if err := flush(); err != nil {
		return nil, 0, err
	}
	return sum, count, nil
}

// fillDeferred 는 NULL 로 넣어 둔 순환 외래 키 열을 원본 값으로 채운다(기본키로 행을 찾는다).
func fillDeferred(ctx context.Context, src *sql.Tx, dst *sql.Tx, t *tableInfo) error {
	if len(t.Deferred) == 0 {
		return nil
	}
	if len(t.PK) == 0 {
		return fmt.Errorf("기본키가 없어 순환 외래 키를 채울 수 없습니다")
	}
	for _, cols := range t.Deferred {
		sel := make([]string, 0, len(cols)+len(t.PK))
		for _, c := range cols {
			sel = append(sel, qi(c))
		}
		for _, c := range t.PK {
			sel = append(sel, qi(c))
		}
		rows, err := src.QueryContext(ctx, `SELECT `+strings.Join(sel, ", ")+` FROM `+qi(t.Name)+` WHERE `+qi(cols[0])+` IS NOT NULL`)
		if err != nil {
			return err
		}
		sets := make([]string, len(cols))
		for i, c := range cols {
			sets[i] = qi(c) + " = ?"
		}
		wh := make([]string, len(t.PK))
		for i, c := range t.PK {
			wh[i] = qi(c) + " = ?"
		}
		upd := `UPDATE ` + qi(t.Name) + ` SET ` + strings.Join(sets, ", ") + ` WHERE ` + strings.Join(wh, " AND ")
		for rows.Next() {
			vals := make([]any, len(sel))
			ptr := make([]any, len(sel))
			for i := range vals {
				ptr[i] = &vals[i]
			}
			if err := rows.Scan(ptr...); err != nil {
				rows.Close()
				return err
			}
			args := make([]any, len(vals))
			for i, v := range vals {
				name := sel[i]
				typ := colType(t, strings.Trim(name, `"`))
				cv, err := convert(v, typ)
				if err != nil {
					rows.Close()
					return err
				}
				args[i] = cv
			}
			if _, err := dst.ExecContext(ctx, upd, args...); err != nil {
				rows.Close()
				return err
			}
		}
		if err := rows.Close(); err != nil {
			return err
		}
	}
	return nil
}

func colType(t *tableInfo, name string) string {
	for _, c := range t.Cols {
		if c.Name == name {
			return c.Type
		}
	}
	return "text"
}

// readSum 은 대상에 들어간 표의 행 수와 체크섬을 읽는다(같은 트랜잭션).
func readSum(ctx context.Context, q *sql.Tx, t *tableInfo) (*tableSum, int64, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+colList(t)+` FROM `+qi(t.Name))
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	sum := &tableSum{}
	n := len(t.Cols)
	var count int64
	for rows.Next() {
		raw := make([]any, n)
		ptr := make([]any, n)
		for i := range raw {
			ptr[i] = &raw[i]
		}
		if err := rows.Scan(ptr...); err != nil {
			return nil, 0, err
		}
		conv := make([]any, n)
		for i, c := range t.Cols {
			v, err := convert(raw[i], c.Type)
			if err != nil {
				return nil, 0, err
			}
			conv[i] = v
		}
		sum.add(conv)
		count++
	}
	return sum, count, rows.Err()
}

// fixSequences 는 적재 뒤 identity 시퀀스를 표의 최대 번호로 맞춘다(다음 자동 번호가 이관된 번호와 겹치지 않게).
func fixSequences(ctx context.Context, tx *sql.Tx, plan map[string]*tableInfo, ordered []string) error {
	for _, n := range ordered {
		for _, c := range plan[n].identityCols() {
			if _, err := tx.ExecContext(ctx, `SELECT setval(pg_get_serial_sequence('`+qi(n)+`', '`+c+`'), COALESCE((SELECT MAX(`+qi(c)+`) FROM `+qi(n)+`), 1), (SELECT MAX(`+qi(c)+`) FROM `+qi(n)+`) IS NOT NULL)`); err != nil {
				return fmt.Errorf("표 %s 의 번호 시퀀스를 맞추지 못했습니다: %w", n, err)
			}
		}
	}
	return nil
}

// chainChecks 는 대상 감사 해시 체인을 검증하고 마지막 번호·해시가 원본과 같은지 본다.
func chainChecks(ctx context.Context, store *audit.Store, src *sql.Tx, dst *sql.Tx, when string) []Check {
	res, err := store.VerifyChainIn(ctx, dst)
	chain := Check{Name: "감사 체인(" + when + ")", OK: err == nil && res.OK, Detail: fmt.Sprintf("검사 %d건", res.Checked)}
	if err != nil {
		chain.Detail = err.Error()
	} else if !res.OK {
		chain.Detail = fmt.Sprintf("seq %d: %s", res.BrokenSeq, res.BrokenWhy)
	}
	head := func(q *sql.Tx) (int64, string) {
		var seq sql.NullInt64
		var h sql.NullString
		_ = q.QueryRowContext(ctx, `SELECT seq, hash FROM audit_logs ORDER BY seq DESC LIMIT 1`).Scan(&seq, &h)
		return seq.Int64, h.String
	}
	ss, sh := head(src)
	ds, dh := head(dst)
	same := Check{Name: "감사 체인 끝(원본과 같음)", OK: ss == ds && sh == dh, Detail: fmt.Sprintf("원본 seq %d, 대상 seq %d", ss, ds)}
	return []Check{chain, same}
}

// invariants 는 이관 결과의 업무 불변식을 점검한다.
func invariants(ctx context.Context, tx *sql.Tx) []Check {
	var out []Check
	var bad int64
	err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM (SELECT journal_id FROM journal_lines GROUP BY journal_id
		HAVING SUM(CASE side WHEN 'D' THEN amount ELSE -amount END) <> 0) t`).Scan(&bad)
	out = append(out, Check{Name: "전표 차대변 합이 0", OK: err == nil && bad == 0, Detail: fmt.Sprintf("어긋난 전표 %d건", bad)})
	var view, ledger int64
	e1 := tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(on_hand), 0) FROM v_wh_lot_stock`).Scan(&view)
	e2 := tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(qty_delta), 0) FROM stock_ledger`).Scan(&ledger)
	out = append(out, Check{Name: "재고 합계가 원장 합과 같음", OK: e1 == nil && e2 == nil && view == ledger, Detail: fmt.Sprintf("재고 %d, 원장 %d", view, ledger)})
	var neg int64
	e3 := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM v_wh_lot_stock WHERE on_hand < 0`).Scan(&neg)
	out = append(out, Check{Name: "음수 재고 없음", OK: e3 == nil && neg == 0, Detail: fmt.Sprintf("음수 재고 줄 %d건", neg)})
	return out
}
