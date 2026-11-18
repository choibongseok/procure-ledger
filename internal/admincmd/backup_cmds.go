package admincmd

// 백업·복구 명령(B38 2단계, 명세 6~7절). 구현은 internal/backup 에 있고 여기서는 명령 연결만 한다.
// init() 에서 commands 에 덧붙이므로 admincmd.go 는 건드리지 않는다.

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/choibongseok/procure-ledger/internal/backup"
	"github.com/choibongseok/procure-ledger/internal/config"
	"github.com/choibongseok/procure-ledger/internal/dbutil"
	"github.com/choibongseok/procure-ledger/migrations"
)

// AppVersion 은 매니페스트에 적는 앱 버전이다(빌드 때 -ldflags 로 바꿀 수 있다).
var AppVersion = "dev"

func init() {
	commands = append(commands,
		command{"backup", "backup [--dir D] [--keep N] [--encrypt] [--label L]", "백업 세트 한 개를 만든다(서버가 켜져 있어도 된다). 검증을 통과한 뒤에만 오래된 세트를 지운다.", runBackup},
		command{"verify-backup", "verify-backup <세트>", "세트의 해시·복호화·DB 점검(무결성·감사 체인·마이그레이션 번호)을 판정표로 보인다.", runVerifyBackup},
		command{"list-backups", "list-backups [--dir D]", "백업 세트 목록(시각·크기·DB 종류·체인 머리)을 보인다.", runListBackups},
		command{"restore", "restore <세트> [--replace] [--rehearse [--work D] [--record]] [--revoke-tokens] [--yes]", "세트를 복구한다. 서버를 멈춘 뒤 실행한다. --rehearse 는 임시 위치에서만 시험한다(운영 파일을 바꾸지 않는다).", runRestore},
	)
}

// backupEnv 는 백업 도구가 환경변수에서 읽는 값들이다.
type backupEnv struct {
	cfg      config.Config
	dir      string
	keySet   bool
	key      []byte
	keep     int
	pgDump   string
	insecure bool
}

func loadBackupEnv(dirFlag string) (backupEnv, error) {
	cfg := config.Load()
	e := backupEnv{cfg: cfg, dir: dirFlag, pgDump: os.Getenv("PL_PG_DUMP"), insecure: cfg.DBAllowInsecure}
	if e.dir == "" {
		e.dir = os.Getenv("PL_BACKUP_DIR")
	}
	if e.dir == "" {
		dbPath := cfg.DBPath
		if dbPath == "" {
			dbPath = dbutil.DefaultSQLitePath
		}
		e.dir = filepath.Join(filepath.Dir(dbPath), "backups")
	}
	if k := os.Getenv("PL_BACKUP_KEY"); k != "" {
		key, err := backup.DeriveKey(k)
		if err != nil {
			return e, err
		}
		e.key, e.keySet = key, true
	}
	if v := os.Getenv("PL_BACKUP_KEEP"); v != "" {
		n, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil || n < 2 {
			return e, errors.New("PL_BACKUP_KEEP 는 2 이상의 정수여야 합니다")
		}
		e.keep = n
	}
	return e, nil
}

func (e backupEnv) kind() (kind string, target dbutil.Target, err error) {
	target, err = dbutil.ParseTarget(e.cfg.DBPath, e.cfg.DBURL)
	if err != nil {
		return "", target, err
	}
	if target.Kind == dbutil.KindPostgres {
		return "postgres", target, nil
	}
	return "sqlite", target, nil
}

func (e backupEnv) anchorKey() []byte {
	if e.cfg.AuditAnchorKey == "" {
		return nil
	}
	return []byte(e.cfg.AuditAnchorKey)
}

func printChecks(out io.Writer, checks []backup.Check) {
	for _, c := range checks {
		line := fmt.Sprintf("  %-4s %s", c.Status, c.Item)
		if c.Detail != "" {
			line += ": " + c.Detail
		}
		fmt.Fprintln(out, line)
	}
}

func runBackup(ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("backup", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	dir := fs.String("dir", "", "")
	keep := fs.Int("keep", 0, "")
	encrypt := fs.Bool("encrypt", false, "")
	label := fs.String("label", "", "")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		return errors.New("사용법: backup [--dir D] [--keep N] [--encrypt] [--label L]")
	}
	e, err := loadBackupEnv(*dir)
	if err != nil {
		return err
	}
	if *encrypt && !e.keySet {
		return errors.New("--encrypt 에는 PL_BACKUP_KEY(16자 이상)가 필요합니다")
	}
	if *keep == 0 {
		*keep = e.keep
	}
	kind, target, err := e.kind()
	if err != nil {
		return err
	}
	opt := backup.Options{Kind: kind, DBPath: target.Path, DBURL: e.cfg.DBURL, PGDump: e.pgDump, AllowInsecure: e.insecure,
		AnchorPath: e.cfg.AuditAnchorPath, AnchorKey: e.anchorKey(), Dir: e.dir, Keep: *keep, Encrypt: *encrypt, Key: e.key, Label: *label, AppVersion: AppVersion}
	started := time.Now()
	res, berr := backup.Run(ctx, opt)
	var invWarn []string
	for _, v := range res.Manifest.Invariants {
		invWarn = append(invWarn, "업무 불변식 위반: "+v)
	}
	record(ctx, e, kind, target, backup.RunRecord{Kind: "backup", SetName: res.Name, DBKind: kind, Bytes: res.Size, ChainSeq: res.Manifest.ChainSeq, Started: started,
		Finished: time.Now(), Action: "system.backup", Warnings: invWarn}, berr, out)
	if berr != nil {
		return fmt.Errorf("백업 실패: %w", berr)
	}
	fmt.Fprintf(out, "백업 세트를 만들었습니다: %s\n", res.Name)
	fmt.Fprintf(out, "  위치 %s\n  크기 %d바이트, DB 종류 %s, 마이그레이션 %s, 감사 체인 머리 %d번%s\n", res.Path, res.Size, res.Manifest.DBKind, res.Manifest.Migration, res.Manifest.ChainSeq,
		map[bool]string{true: ", 암호화됨", false: ""}[res.Manifest.Encrypted])
	for _, w := range res.Warnings {
		fmt.Fprintf(out, "  경고: %s\n", w)
	}
	if len(res.Deleted) > 0 {
		fmt.Fprintf(out, "  보존 개수를 넘어 지운 세트: %s\n", strings.Join(res.Deleted, ", "))
	}
	return nil
}

// record 는 backup_runs 와 감사에 결과를 남긴다. 실패해도 도구는 성공·실패를 바꾸지 않고 경고만 낸다.
func record(ctx context.Context, e backupEnv, kind string, target dbutil.Target, r backup.RunRecord, runErr error, out io.Writer) {
	r.Result, r.Detail = "success", fmt.Sprintf("세트 %s, 크기 %d바이트, 체인 머리 %d번", r.SetName, r.Bytes, r.ChainSeq)
	if runErr != nil {
		r.Result, r.ErrorCode = "failed", "failed"
		r.Detail = "실행 실패(원인은 도구 출력 참고)"
	}
	db, _, err := dbutil.OpenTarget(ctx, target, dbutil.Options{PG: dbutil.PGOptions{MaxConns: 2, AllowInsecure: e.insecure}})
	if err != nil {
		fmt.Fprintf(out, "경고: 실행 기록을 남기지 못했습니다(DB 를 열 수 없음)\n")
		return
	}
	defer db.Close()
	if err := backup.RecordRun(ctx, db, r); err != nil {
		fmt.Fprintf(out, "경고: 실행 기록·감사를 남기지 못했습니다: %v\n", err)
	}
}

func resolveSet(dir, arg string) (string, error) {
	if arg == "" {
		return "", errors.New("세트를 지정하세요(디렉터리 경로 또는 세트 이름)")
	}
	if st, err := os.Stat(arg); err == nil && st.IsDir() {
		return arg, nil
	}
	p := filepath.Join(dir, arg)
	if st, err := os.Stat(p); err == nil && st.IsDir() {
		return p, nil
	}
	return "", fmt.Errorf("세트를 찾을 수 없습니다: %s", arg)
}

func runVerifyBackup(ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("verify-backup", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	dir := fs.String("dir", "", "")
	if err := fs.Parse(args); err != nil || fs.NArg() != 1 {
		return errors.New("사용법: verify-backup <세트>")
	}
	e, err := loadBackupEnv(*dir)
	if err != nil {
		return err
	}
	set, err := resolveSet(e.dir, fs.Arg(0))
	if err != nil {
		return err
	}
	rep, err := backup.VerifySet(ctx, set, backup.VerifyOptions{Key: e.key, AnchorKey: e.anchorKey()})
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "세트 %s 점검 결과\n", filepath.Base(set))
	printChecks(out, rep.Checks)
	if !rep.OK {
		return errors.New("검증에 실패했습니다: " + rep.FirstFailure())
	}
	fmt.Fprintln(out, "검증 통과")
	return nil
}

func runListBackups(ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("list-backups", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	dir := fs.String("dir", "", "")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		return errors.New("사용법: list-backups [--dir D]")
	}
	e, err := loadBackupEnv(*dir)
	if err != nil {
		return err
	}
	sets, err := backup.List(e.dir)
	if err != nil {
		return err
	}
	if len(sets) == 0 {
		fmt.Fprintln(out, "백업 세트가 없습니다.")
		return nil
	}
	for _, s := range sets {
		if !s.Complete {
			fmt.Fprintf(out, "  %s  (불완전: 매니페스트를 읽을 수 없음)\n", s.Name)
			continue
		}
		fmt.Fprintf(out, "  %s  %d바이트  %s  마이그레이션 %s  체인 %d번%s\n", s.Name, s.Size, s.Manifest.DBKind, s.Manifest.Migration, s.Manifest.ChainSeq,
			map[bool]string{true: "  암호화", false: ""}[s.Manifest.Encrypted])
	}
	return nil
}

func runRestore(ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("restore", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	dir := fs.String("dir", "", "")
	replace := fs.Bool("replace", false, "")
	rehearse := fs.Bool("rehearse", false, "")
	work := fs.String("work", "", "")
	rec := fs.Bool("record", false, "")
	revoke := fs.Bool("revoke-tokens", false, "")
	yes := fs.Bool("yes", false, "")
	// 세트 인자가 플래그 앞뒤 어디에 와도 받는다.
	var pos []string
	rest := args
	for len(rest) > 0 {
		if err := fs.Parse(rest); err != nil {
			return errors.New("사용법: restore <세트> [--replace] [--rehearse [--work D] [--record]] [--revoke-tokens] [--yes]")
		}
		if fs.NArg() == 0 {
			break
		}
		pos = append(pos, fs.Arg(0))
		rest = fs.Args()[1:]
	}
	if len(pos) != 1 {
		return errors.New("사용법: restore <세트> [--replace] [--rehearse [--work D] [--record]] [--revoke-tokens] [--yes]")
	}
	if *work != "" && !*rehearse {
		return errors.New("--work 는 --rehearse 와 함께만 씁니다")
	}
	e, err := loadBackupEnv(*dir)
	if err != nil {
		return err
	}
	set, err := resolveSet(e.dir, pos[0])
	if err != nil {
		return err
	}
	kind, target, err := e.kind()
	if err != nil {
		return err
	}
	if !*rehearse && !*yes && stdinIsTerminal() {
		name := filepath.Base(target.Path)
		if kind == "postgres" {
			name = pgDatabaseName(e.cfg.DBURL)
		}
		fmt.Fprintf(out, "이 작업은 현재 DB 의 변경을 되돌립니다. 계속하려면 데이터베이스 이름(%s)을 입력하세요: ", name)
		line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		if strings.TrimSpace(line) != name {
			return errors.New("입력이 맞지 않아 중단했습니다")
		}
	}
	started := time.Now()
	res, err := backup.Restore(ctx, backup.RestoreOptions{SetDir: set, Kind: kind, DBPath: target.Path, DBURL: e.cfg.DBURL, AllowInsecure: e.insecure, PGDump: e.pgDump,
		AnchorPath: e.cfg.AuditAnchorPath, AnchorKey: e.anchorKey(), Key: e.key, Replace: *replace, Rehearse: *rehearse, RevokeTokens: *revoke,
		MaxMigration: migrations.Latest(), WorkDir: *work, ToolVersion: backup.ToolVersion})
	if err != nil {
		return fmt.Errorf("복구를 진행하지 못했습니다: %w", err)
	}
	title := "복구"
	if *rehearse {
		title = "복구 리허설"
	}
	fmt.Fprintf(out, "%s 판정표(세트 %s)\n", title, filepath.Base(set))
	printChecks(out, res.Checks)
	for _, n := range res.Notes {
		fmt.Fprintf(out, "  안내: %s\n", n)
	}
	if res.Loss != nil {
		fmt.Fprintf(out, "  손실 구간: 감사 기록 %d번~%d번(%s ~ %s), 앵커 %d줄이 복구로 사라집니다. 이전 앵커 파일은 보존했습니다.\n", res.Loss.FromSeq, res.Loss.ToSeq, res.Loss.FromAt, res.Loss.ToAt, res.Loss.Lines)
	}
	if res.OK && !*rehearse {
		for _, p := range res.PreRestore {
			fmt.Fprintf(out, "  보존: %s\n", p)
		}
		fmt.Fprintf(out, "  주의: 백업 이후에 폐기·정지된 토큰·계정이 되살아났을 수 있습니다. 검토하세요(토큰 %d개 폐기%s).\n", res.TokensRevoked, map[bool]string{true: "", false: ", 기본은 유지"}[*revoke])
		if res.Manifest.Migration < migrations.Latest() {
			fmt.Fprintln(out, "  안내: 첫 기동 때 마이그레이션이 적용됩니다.")
		}
	}
	if *rehearse && res.WorkDB != "" {
		fmt.Fprintf(out, "리허설 DB: %s\n리허설 앵커: %s\n", res.WorkDB, res.WorkAnchor)
	}
	if *rehearse && *rec {
		recordRehearsal(ctx, e, kind, target, set, res, started, out)
	}
	if !res.OK {
		return errors.New(title + "에 실패했습니다(확정하지 않았습니다)")
	}
	fmt.Fprintf(out, "%s 통과\n", title)
	return nil
}

func recordRehearsal(ctx context.Context, e backupEnv, kind string, target dbutil.Target, set string, res backup.RestoreResult, started time.Time, out io.Writer) {
	r := backup.RunRecord{Kind: "rehearsal", SetName: filepath.Base(set), DBKind: kind, ChainSeq: res.Manifest.ChainSeq, Started: started, Finished: time.Now(), Action: "system.restore_rehearsal"}
	var runErr error
	if !res.OK {
		runErr = errors.New("fail")
	}
	record(ctx, e, kind, target, r, runErr, out)
}

func stdinIsTerminal() bool {
	st, err := os.Stdin.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}

func pgDatabaseName(raw string) string {
	if i := strings.LastIndex(raw, "/"); i >= 0 {
		n := raw[i+1:]
		if j := strings.IndexByte(n, '?'); j >= 0 {
			n = n[:j]
		}
		return n
	}
	return ""
}
