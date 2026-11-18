package admincmd_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/admincmd"
	"github.com/choibongseok/procure-ledger/internal/backup"
	"github.com/choibongseok/procure-ledger/internal/dbutil"
	"github.com/choibongseok/procure-ledger/migrations"
)

func runAdmin(t *testing.T, args ...string) (int, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := admincmd.Run(context.Background(), args, &out, &errb)
	return code, out.String() + errb.String()
}

// erp-admin 의 백업·복구 명령을 환경변수만으로 돌린다(서버와 같은 설정). 감사·backup_runs 기록과 거부 규칙까지 본다.
func TestBackupCommandsEndToEnd(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "data", "erp.db")
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := dbutil.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := migrations.Apply(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO departments (id, name) VALUES (1, '구매팀')`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	t.Setenv("PL_DB_PATH", dbPath)
	t.Setenv("PL_DB_URL", "")
	t.Setenv("PL_BACKUP_DIR", filepath.Join(root, "backups"))
	t.Setenv("PL_AUDIT_ANCHOR_KEY", "")
	t.Setenv("PL_AUDIT_ANCHOR_PATH", "")
	t.Setenv("PL_BACKUP_KEY", "a-sufficiently-long-secret")

	for _, name := range []string{"backup", "verify-backup", "list-backups", "restore"} {
		found := false
		for _, n := range admincmd.Names() {
			found = found || n == name
		}
		if !found {
			t.Fatalf("명령 %s 가 등록되지 않음", name)
		}
	}
	if code, out := runAdmin(t, "list-backups"); code != 0 || !strings.Contains(out, "백업 세트가 없습니다") {
		t.Fatalf("빈 목록: %d %s", code, out)
	}
	if code, out := runAdmin(t, "backup", "--keep", "1"); code == 0 {
		t.Fatalf("--keep 1 은 거부여야 함: %s", out)
	}
	code, out := runAdmin(t, "backup", "--encrypt", "--label", "nightly")
	if code != 0 || !strings.Contains(out, "백업 세트를 만들었습니다") || !strings.Contains(out, "암호화됨") {
		t.Fatalf("백업: %d %s", code, out)
	}
	if strings.Contains(out, "a-sufficiently-long-secret") {
		t.Fatal("출력에 키가 있음")
	}
	code, out = runAdmin(t, "list-backups")
	if code != 0 || !strings.Contains(out, "-nightly") || !strings.Contains(out, "암호화") {
		t.Fatalf("목록: %d %s", code, out)
	}
	setName := strings.Fields(strings.TrimSpace(out))[0]
	if code, out := runAdmin(t, "verify-backup", setName); code != 0 || !strings.Contains(out, "검증 통과") {
		t.Fatalf("verify-backup: %d %s", code, out)
	}
	// 키가 없으면 암호화 세트는 검증 실패(종료 코드 1).
	t.Setenv("PL_BACKUP_KEY", "")
	if code, out := runAdmin(t, "verify-backup", setName); code != 1 || !strings.Contains(out, "PL_BACKUP_KEY") {
		t.Fatalf("키 없는 검증: %d %s", code, out)
	}
	t.Setenv("PL_BACKUP_KEY", "a-sufficiently-long-secret")

	// 리허설: 운영 DB 를 바꾸지 않는다. --record 면 backup_runs 와 감사에 남는다.
	before, _, _ := fileSum(t, dbPath)
	if code, out := runAdmin(t, "restore", setName, "--rehearse"); code != 0 || !strings.Contains(out, "복구 리허설 통과") {
		t.Fatalf("리허설: %d %s", code, out)
	}
	if after, _, _ := fileSum(t, dbPath); after != before {
		t.Fatal("--record 없는 리허설이 운영 DB 를 바꿈")
	}
	if code, out := runAdmin(t, "restore", setName, "--rehearse", "--record"); code != 0 {
		t.Fatalf("리허설 기록: %d %s", code, out)
	}
	live, err := dbutil.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	var runs, audits, failedAudits int
	// 실패한 백업(--keep 1 거부)도 system.backup 감사에 error 로 남는다.
	_ = live.QueryRow(`SELECT COUNT(*) FROM audit_logs WHERE action = 'system.backup' AND result = 'error'`).Scan(&failedAudits)
	if failedAudits != 1 {
		t.Fatalf("실패한 백업의 감사 %d건", failedAudits)
	}
	_ = live.QueryRow(`SELECT COUNT(*) FROM backup_runs WHERE kind IN ('backup','rehearsal') AND result = 'success'`).Scan(&runs)
	_ = live.QueryRow(`SELECT COUNT(*) FROM audit_logs WHERE action IN ('system.backup','system.restore_rehearsal') AND channel = 'cli' AND result = 'success'`).Scan(&audits)
	_ = live.Close()
	if runs != 2 || audits != 2 {
		t.Fatalf("backup_runs %d, 감사 %d (각각 2건이어야 함)", runs, audits)
	}

	// 복구: 비어 있지 않은 대상은 --replace 없이 거부, --replace 면 보존하고 성공.
	if code, out := runAdmin(t, "restore", setName, "--yes"); code != 1 || !strings.Contains(out, "--replace") {
		t.Fatalf("--replace 없는 복구: %d %s", code, out)
	}
	code, out = runAdmin(t, "restore", "--replace", "--yes", setName)
	if code != 0 || !strings.Contains(out, "복구 통과") || !strings.Contains(out, ".pre-restore-") {
		t.Fatalf("복구: %d %s", code, out)
	}
	if code, out := runAdmin(t, "restore"); code == 0 {
		t.Fatalf("세트 없는 복구는 사용 오류여야 함: %s", out)
	}
	ents, _ := os.ReadDir(filepath.Dir(dbPath))
	pre := false
	for _, e := range ents {
		pre = pre || strings.Contains(e.Name(), ".pre-restore-")
	}
	if !pre {
		t.Fatal("기존 DB 가 .pre-restore 로 보존되지 않음")
	}
}

func fileSum(t *testing.T, p string) (string, int64, error) {
	t.Helper()
	h, n, err := backup.FileSHA256(p)
	if err != nil {
		t.Fatal(err)
	}
	return h, n, nil
}
