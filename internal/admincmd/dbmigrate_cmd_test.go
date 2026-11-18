package admincmd

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/audit"
	"github.com/choibongseok/procure-ledger/internal/bootstrap"
	"github.com/choibongseok/procure-ledger/internal/config"
	"github.com/choibongseok/procure-ledger/internal/dbtest"
	"github.com/choibongseok/procure-ledger/internal/dbutil"
)

func TestDBMigrateIsListedAndValidatesArguments(t *testing.T) {
	if !strings.Contains(strings.Join(Names(), ","), "db-migrate") {
		t.Fatalf("db-migrate 가 명령 목록에 없음: %v", Names())
	}
	if code, out, _ := run(t, "help"); code != 0 || !strings.Contains(out, "db-migrate") {
		t.Errorf("도움말에 db-migrate 가 없음: %d %q", code, out)
	}
	t.Setenv("PL_MIGRATE_TO", "")
	code, _, errw := run(t, "db-migrate")
	if code != 1 || !strings.Contains(errw, "--from") || !strings.Contains(errw, "--to") {
		t.Errorf("인자 없음: %d %q", code, errw)
	}
	code, _, errw = run(t, "db-migrate", "--from", filepath.Join(t.TempDir(), "없는.db"), "--to", "postgres://u:secretpw@127.0.0.1:1/x")
	if code != 1 || !strings.Contains(errw, "찾을 수 없습니다") {
		t.Errorf("없는 원본: %d %q", code, errw)
	}
	if strings.Contains(errw, "secretpw") {
		t.Errorf("오류에 비밀번호가 나옴: %q", errw)
	}
	if code, _, _ := run(t, "db-migrate", "--bogus"); code != 1 {
		t.Errorf("알 수 없는 옵션: %d", code)
	}
}

func seedSQLiteSource(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "src.db")
	t.Setenv("PL_DB_PATH", path)
	t.Setenv("PL_DB_URL", "")
	t.Setenv("PL_DEV_IDP", "1")
	t.Setenv("PL_LLM_PROVIDER", "mock")
	d, closeDeps, err := bootstrap.Build(context.Background(), config.Load())
	if err != nil {
		t.Fatal(err)
	}
	_ = d
	closeDeps()
	return path
}

// 이관 명령: 판정표를 한국어로 출력하고 종료 코드 0, 대상 감사 체인 끝에 system.db_migrated(경로 cli)가 한 건 붙고 체인이 이어진다.
// --dry-run 은 아무것도 남기지 않는다.
func TestDBMigrateCommandRecordsAuditAndDryRunLeavesNothing(t *testing.T) {
	if !dbtest.PG() {
		t.Skip("PG 전용 시험(verify-pg): 이관 대상이 PostgreSQL 이다")
	}
	src := seedSQLiteSource(t)
	url := dbtest.URL(t)
	t.Setenv("PL_MIGRATE_TO", url) // 비밀번호가 명령줄에 남지 않게 환경변수로 넘기는 경로도 쓴다

	code, out, errw := run(t, "db-migrate", "--from", src, "--allow-insecure", "--dry-run")
	if code != 0 || !strings.Contains(out, "이관 판정표") || !strings.Contains(out, "리허설") {
		t.Fatalf("dry-run: %d\n%s\n%s", code, out, errw)
	}
	db, err := dbutil.OpenPG(context.Background(), url, dbutil.PGOptions{MaxConns: 2, AllowInsecure: true})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int64
	if err := db.QueryRow(`SELECT COUNT(*) FROM audit_logs`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("dry-run 인데 감사 기록 %d건이 남음(%v)", n, err)
	}

	code, out, errw = run(t, "db-migrate", "--from", src, "--allow-insecure")
	if code != 0 || !strings.Contains(out, "일치") || !strings.Contains(out, "이관을 마쳤습니다") {
		t.Fatalf("이관: %d\n%s\n%s", code, out, errw)
	}
	if strings.Contains(out, url) {
		t.Error("출력에 연결 문자열이 그대로 나옴")
	}
	var channel, actorRole string
	if err := db.QueryRow(`SELECT channel, actor_role FROM audit_logs WHERE action = 'system.db_migrated'`).Scan(&channel, &actorRole); err != nil {
		t.Fatalf("system.db_migrated 기록: %v", err)
	}
	if channel != "cli" || actorRole != "system" {
		t.Errorf("감사 경로 %q·행위자 %q (cli·system 이어야 함)", channel, actorRole)
	}
	res, err := audit.New(db).Verify(context.Background())
	if err != nil || !res.ChainOK {
		t.Fatalf("이관 기록을 붙인 뒤 체인: %+v %v", res, err)
	}
}
