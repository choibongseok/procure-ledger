package admincmd

// 키 교체 명령(B38 단계 4, D8). 구현은 internal/keyrotate. init() 에서 commands 에 덧붙인다.

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/choibongseok/procure-ledger/internal/acctsecret"
	"github.com/choibongseok/procure-ledger/internal/auth"
	"github.com/choibongseok/procure-ledger/internal/backup"
	"github.com/choibongseok/procure-ledger/internal/dbutil"
	"github.com/choibongseok/procure-ledger/internal/keyrotate"
)

func init() {
	commands = append(commands,
		command{"rotate-key", "rotate-key <auth|account> --new-key-env 환경변수이름 [--old-key-env 환경변수이름] [--dir D] [--no-backup]",
			"저장된 암호문을 옛 키로 풀어 새 키로 다시 봉한다(재등록 불필요). 서버를 멈춘 뒤 실행하고, 시작 전에 자동 백업을 만든다. 키 값은 환경변수로만 받는다.", runRotateKey},
	)
}

func runRotateKey(ctx context.Context, args []string, out io.Writer) error {
	usage := errors.New("사용법: rotate-key <auth|account> --new-key-env 환경변수이름 [--old-key-env 환경변수이름] [--dir D] [--no-backup]")
	fs := flag.NewFlagSet("rotate-key", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	oldEnv := fs.String("old-key-env", "", "")
	newEnv := fs.String("new-key-env", "", "")
	dir := fs.String("dir", "", "")
	noBackup := fs.Bool("no-backup", false, "")
	var pos []string
	rest := args
	for len(rest) > 0 {
		if err := fs.Parse(rest); err != nil {
			return usage
		}
		if fs.NArg() == 0 {
			break
		}
		pos = append(pos, fs.Arg(0))
		rest = fs.Args()[1:]
	}
	if len(pos) != 1 || (pos[0] != "auth" && pos[0] != "account") || *newEnv == "" {
		return usage
	}
	name := pos[0]
	defOld := map[string]string{"auth": "PL_AUTH_SECRET_KEY", "account": "PL_ACCOUNT_SECRET_KEY"}[name]
	if *oldEnv == "" {
		*oldEnv = defOld
	}
	if *oldEnv == *newEnv {
		return errors.New("옛 키와 새 키가 같은 환경변수입니다")
	}
	decode := func(env string) ([]byte, error) {
		raw := os.Getenv(env)
		if raw == "" {
			return nil, fmt.Errorf("환경변수 %s 가 비어 있습니다", env)
		}
		var k []byte
		var err error
		if name == "auth" {
			k, err = auth.DecodeSecretKey(raw)
		} else {
			k, err = acctsecret.DecodeKey(raw)
		}
		if err != nil {
			return nil, fmt.Errorf("%s: %v", env, err)
		}
		return k, nil
	}
	oldKey, err := decode(*oldEnv)
	if err != nil {
		return err
	}
	newKey, err := decode(*newEnv)
	if err != nil {
		return err
	}
	if string(oldKey) == string(newKey) {
		return errors.New("새 키가 옛 키와 같은 값입니다")
	}

	e, err := loadBackupEnv(*dir)
	if err != nil {
		return err
	}
	kind, target, err := e.kind()
	if err != nil {
		return err
	}
	if kind == "sqlite" {
		if used, uerr := backup.SQLiteInUse(ctx, target.Path); uerr != nil {
			return uerr
		} else if used {
			return errors.New("서버나 다른 프로세스가 DB 를 사용 중입니다. 서버와 erp-mcp 를 모두 멈춘 뒤 다시 실행하세요")
		}
	} else {
		fmt.Fprintln(out, "안내: PostgreSQL 에서는 서버가 멈춰 있는지 이 도구가 확인하지 못합니다. 서버와 erp-mcp 를 모두 멈췄는지 직접 확인하세요.")
	}
	if !*noBackup {
		res, berr := backup.Run(ctx, backup.Options{Kind: kind, DBPath: target.Path, DBURL: e.cfg.DBURL, PGDump: e.pgDump, AllowInsecure: e.insecure,
			AnchorPath: e.cfg.AuditAnchorPath, AnchorKey: e.anchorKey(), Dir: e.dir, Keep: e.keep, Encrypt: e.keySet, Key: e.key, Label: "pre-rotate-" + name, AppVersion: AppVersion})
		if berr != nil {
			return fmt.Errorf("시작 전 자동 백업에 실패해 교체하지 않았습니다: %w", berr)
		}
		fmt.Fprintf(out, "시작 전 자동 백업: %s\n", res.Name)
	}
	db, _, err := dbutil.OpenTarget(ctx, target, dbutil.Options{PG: dbutil.PGOptions{MaxConns: 2, AllowInsecure: e.insecure}})
	if err != nil {
		return err
	}
	defer db.Close()

	started := time.Now()
	var res keyrotate.Result
	if name == "auth" {
		res, err = keyrotate.RotateAuth(ctx, db, oldKey, newKey)
	} else {
		res, err = keyrotate.RotateAccount(ctx, db, oldKey, newKey)
	}
	if err != nil {
		if aerr := keyrotate.RecordAudit(ctx, db, name, 0, false); aerr != nil {
			fmt.Fprintf(out, "경고: 실패 감사를 남기지 못했습니다: %v\n", aerr)
		}
		return fmt.Errorf("키 교체 실패(아무것도 바뀌지 않았습니다): %w", err)
	}
	if name == "account" {
		if verr := keyrotate.VerifyAccount(ctx, db, oldKey, newKey); verr != nil {
			_ = keyrotate.RecordAudit(ctx, db, name, res.Total, false)
			return fmt.Errorf("교체 뒤 표본 점검에 실패했습니다. 시작 전 자동 백업으로 복구하세요: %w", verr)
		}
	}
	if aerr := keyrotate.RecordAudit(ctx, db, name, res.Total, true); aerr != nil {
		fmt.Fprintf(out, "경고: 감사를 남기지 못했습니다: %v\n", aerr)
	}
	fmt.Fprintf(out, "키 %s 교체 완료(%.1f초): 다시 봉한 값 %d건\n", name, time.Since(started).Seconds(), res.Total)
	for k, n := range res.Resealed {
		fmt.Fprintf(out, "  %s: %d건\n", k, n)
	}
	fmt.Fprintf(out, "다음: 서버 환경의 %s 를 새 키 값으로 바꾸고 다시 기동하세요. 옛 키는 시작 전 백업을 복구할 때 필요하니 그 백업을 지울 때까지 보관하세요.\n", defOld)
	return nil
}
