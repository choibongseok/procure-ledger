package admincmd_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/acctsecret"
	"github.com/choibongseok/procure-ledger/internal/dbutil"
	"github.com/choibongseok/procure-ledger/migrations"
)

func b64(b byte) string { return base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{b}, 32)) }

// rotate-key account: 서버가 멈춰 있어야 하고, 시작 전 자동 백업을 만들고, 새 키로만 풀리게 다시 봉하며, 키 값 없이 감사를 남긴다.
func TestRotateKeyAccountCommand(t *testing.T) {
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
	oldK, _ := acctsecret.DecodeKey(b64(1))
	oldC, _ := acctsecret.New(oldK)
	sealed, _ := oldC.Seal("110-123-456789")
	if _, err := db.Exec(`INSERT INTO vendors (name, business_no, account_no) VALUES ('한빛상사', '123-45-67890', ?)`, sealed); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PL_DB_PATH", dbPath)
	t.Setenv("PL_DB_URL", "")
	t.Setenv("PL_BACKUP_DIR", filepath.Join(root, "backups"))
	t.Setenv("PL_AUDIT_ANCHOR_KEY", "")
	t.Setenv("PL_AUDIT_ANCHOR_PATH", "")
	t.Setenv("PL_BACKUP_KEY", "")
	t.Setenv("PL_ACCOUNT_SECRET_KEY", b64(1))
	t.Setenv("PL_ACCOUNT_SECRET_KEY_NEXT", b64(2))

	// 사용법·입력 오류
	for _, args := range [][]string{{"rotate-key"}, {"rotate-key", "other", "--new-key-env", "X"}, {"rotate-key", "account"},
		{"rotate-key", "account", "--new-key-env", "PL_ACCOUNT_SECRET_KEY"}, {"rotate-key", "account", "--new-key-env", "PL_NO_SUCH_ENV"}} {
		if code, out := runAdmin(t, args...); code == 0 {
			t.Fatalf("%v 는 실패여야 함: %s", args, out)
		}
	}
	// 서버(다른 연결)가 DB 를 쓰는 중이면 거부하고 백업도 만들지 않는다.
	if code, out := runAdmin(t, "rotate-key", "account", "--new-key-env", "PL_ACCOUNT_SECRET_KEY_NEXT"); code != 1 || !strings.Contains(out, "사용 중") {
		t.Fatalf("사용 중 거부: %d %s", code, out)
	}
	if ents, _ := os.ReadDir(filepath.Join(root, "backups")); len(ents) != 0 {
		t.Fatalf("거부됐는데 백업이 생김: %v", ents)
	}
	_ = db.Close()

	code, out := runAdmin(t, "rotate-key", "account", "--new-key-env", "PL_ACCOUNT_SECRET_KEY_NEXT")
	if code != 0 || !strings.Contains(out, "키 account 교체 완료") || !strings.Contains(out, "다시 봉한 값 1건") || !strings.Contains(out, "시작 전 자동 백업") {
		t.Fatalf("교체: %d %s", code, out)
	}
	for _, secret := range []string{b64(1), b64(2), "110-123-456789", sealed} {
		if strings.Contains(out, secret) {
			t.Fatal("출력에 비밀이 있음")
		}
	}
	if sets, _ := os.ReadDir(filepath.Join(root, "backups")); len(sets) < 1 {
		t.Fatal("시작 전 자동 백업이 없음")
	}
	live, err := dbutil.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer live.Close()
	var v, detail, result string
	_ = live.QueryRow(`SELECT account_no FROM vendors`).Scan(&v)
	newK, _ := acctsecret.DecodeKey(b64(2))
	newC, _ := acctsecret.New(newK)
	if got, err := newC.Open(v); err != nil || got != "110-123-456789" {
		t.Fatalf("새 키로 풀기: %q %v", got, err)
	}
	if _, err := oldC.Open(v); err == nil {
		t.Fatal("옛 키로도 풀림")
	}
	_ = live.QueryRow(`SELECT detail, result FROM audit_logs WHERE action = 'system.key_rotated' ORDER BY seq DESC LIMIT 1`).Scan(&detail, &result)
	if result != "success" || !strings.Contains(detail, "키 account") || strings.Contains(detail, b64(2)) {
		t.Fatalf("감사: %s %s", result, detail)
	}
}
