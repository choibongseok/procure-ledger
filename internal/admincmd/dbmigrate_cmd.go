package admincmd

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/choibongseok/procure-ledger/internal/audit"
	"github.com/choibongseok/procure-ledger/internal/contract"
	caudit "github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/dbmigrate"
)

func init() {
	commands = append(commands, command{"db-migrate", "db-migrate --from <SQLite 파일> --to <postgres URL> [--dry-run]",
		"SQLite 데이터베이스를 빈 PostgreSQL 로 옮기고 행 수·체크섬·감사 체인을 검증한다(서버는 멈춘다, 감사 system.db_migrated).", runDBMigrate})
}

// dbMigrateRun 은 시험이 바꿔 끼우는 이관 실행기다.
var dbMigrateRun = dbmigrate.Run

func runDBMigrate(ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("db-migrate", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	from := fs.String("from", "", "원본 SQLite 파일")
	to := fs.String("to", "", "대상 PostgreSQL 연결 문자열(비밀번호는 환경변수 PL_MIGRATE_TO 로 넘기면 명령줄에 남지 않는다)")
	dry := fs.Bool("dry-run", false, "모든 검사를 하고 마지막에 되돌린다")
	insecure := fs.Bool("allow-insecure", false, "루프백 호스트에서만 TLS 없는 연결을 허용한다(로컬 개발)")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		return errors.New("사용법: db-migrate --from <SQLite 파일> --to <postgres URL> [--dry-run]")
	}
	if *to == "" {
		*to = os.Getenv("PL_MIGRATE_TO")
	}
	if *from == "" || *to == "" {
		return errors.New("--from(원본 SQLite 파일)과 --to(대상 PostgreSQL 연결 문자열, 또는 환경변수 PL_MIGRATE_TO)가 필요합니다")
	}
	fmt.Fprintln(out, "SQLite → PostgreSQL 이관을 시작합니다. 원본은 읽기 전용으로만 열립니다.")
	rep, err := dbMigrateRun(ctx, dbmigrate.Options{From: *from, To: *to, AllowInsecure: *insecure, DryRun: *dry, Out: out, Finalize: recordMigrated})
	printReport(out, rep)
	if err != nil {
		return err
	}
	if !rep.OK() {
		return errors.New("이관 검증에 실패했습니다")
	}
	switch {
	case rep.DryRun:
		fmt.Fprintln(out, "리허설(--dry-run)이 끝났습니다. 대상에는 아무 데이터도 남기지 않았습니다.")
	case rep.Committed:
		fmt.Fprintln(out, "이관을 마쳤습니다. PL_DB_URL 을 설정하고 PL_DB_PATH 를 비워 서버를 띄우세요. 원본 파일은 롤백 수단으로 그대로 두세요.")
	}
	return nil
}

// recordMigrated 는 이관 사실을 대상 감사 체인 끝에 한 건 덧붙인다(같은 트랜잭션). 비밀 값은 넣지 않는다.
func recordMigrated(ctx context.Context, tx contract.Tx, store *audit.Store, rep *dbmigrate.Report) error {
	var parts []string
	for _, t := range rep.Tables {
		if !t.Skipped {
			parts = append(parts, fmt.Sprintf("%s=%d", t.Name, t.Dst))
		}
	}
	sort.Strings(parts)
	return store.Record(ctx, tx, caudit.Entry{Actor: Actor, Action: "system.db_migrated", TargetType: caudit.TargetSystem, TargetID: "db", Result: caudit.ResultSuccess,
		Detail: fmt.Sprintf("SQLite→PostgreSQL 이관: 원본 SHA-256 %s, 마이그레이션 %s, 이관 행 %d건(%s)", rep.SourceSHA256, rep.SchemaVersion, rep.Rows(), strings.Join(parts, ", "))})
}

func printReport(out io.Writer, rep dbmigrate.Report) {
	if len(rep.Tables) == 0 && len(rep.Checks) == 0 {
		return
	}
	fmt.Fprintln(out)
	fmt.Fprintln(out, "=== 이관 판정표 ===")
	fmt.Fprintf(out, "원본 SHA-256 %s\n", rep.SourceSHA256)
	fmt.Fprintf(out, "%-32s %10s %10s  %s\n", "표", "원본 행", "대상 행", "판정")
	for _, t := range rep.Tables {
		switch {
		case t.Skipped:
			fmt.Fprintf(out, "%-32s %10s %10s  제외(%s)\n", t.Name, "-", "-", t.Reason)
		case t.OK():
			fmt.Fprintf(out, "%-32s %10d %10d  일치\n", t.Name, t.Src, t.Dst)
		default:
			fmt.Fprintf(out, "%-32s %10d %10d  불일치(행 수 %v, 내용 %v)\n", t.Name, t.Src, t.Dst, t.Src == t.Dst, t.SrcSum == t.DstSum)
		}
	}
	for _, c := range rep.Checks {
		mark := "통과"
		if !c.OK {
			mark = "실패"
		}
		fmt.Fprintf(out, "%-8s %s: %s\n", mark, c.Name, c.Detail)
	}
}
