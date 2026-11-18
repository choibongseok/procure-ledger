package admincmd

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/audit"
	"github.com/choibongseok/procure-ledger/internal/bootstrap"
	"github.com/choibongseok/procure-ledger/internal/config"
	"github.com/choibongseok/procure-ledger/internal/dbtest"
)

func run(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, errw bytes.Buffer
	code := Run(context.Background(), args, &out, &errw)
	return code, out.String(), errw.String()
}

func TestUsageAndUnknownCommand(t *testing.T) {
	if code, out, _ := run(t); code != 2 || !strings.Contains(out, "access-recompute") || !strings.Contains(out, "auth-unlock") {
		t.Errorf("인자 없음: %d %q", code, out)
	}
	if code, out, _ := run(t, "help"); code != 0 || !strings.Contains(out, "사용법: erp-admin") {
		t.Errorf("help: %d %q", code, out)
	}
	code, _, errw := run(t, "no-such")
	if code != 2 || !strings.Contains(errw, "알 수 없는 명령") {
		t.Errorf("알 수 없는 명령: %d %q", code, errw)
	}
	// 기존 서버 하위 명령 이름이 그대로 있다(D12 별칭).
	got := strings.Join(Names(), ",")
	if !strings.Contains(got, "access-recompute") || !strings.Contains(got, "auth-unlock") {
		t.Errorf("명령 목록: %s", got)
	}
}

// 두 명령은 서버와 같은 DB 에서 일하고 감사에 경로 cli 로 남는다(B38 D7). 체인은 그대로 이어진다.
func TestCommandsAuditAsCLI(t *testing.T) {
	dbtest.SetEnv(t)
	ctx := context.Background()
	d, closeDeps, err := bootstrap.Build(ctx, config.Load())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.DB.Exec(`INSERT INTO departments (name) VALUES ('운영') ON CONFLICT DO NOTHING`); err != nil {
		t.Fatal(err)
	}
	var uid int64
	if err := d.DB.QueryRow(`INSERT INTO users (email, name, role, department_id) VALUES ('locked@x.test', '잠김', 'staff', (SELECT id FROM departments WHERE name = '운영')) RETURNING id`).Scan(&uid); err != nil {
		t.Fatal(err)
	}
	if _, err := d.DB.Exec(`INSERT INTO user_credentials (user_id, password_hash, must_change, changed_at, failed_count, locked_until) VALUES (?, 'x', 0, '2026-11-12T00:00:00Z', 5, '2999-01-01T00:00:00Z')`, uid); err != nil {
		t.Fatal(err)
	}
	closeDeps()

	code, out, errw := run(t, "access-recompute")
	if code != 0 || !strings.Contains(out, "다시 계산했습니다") {
		t.Fatalf("access-recompute: %d %q %q", code, out, errw)
	}
	code, out, errw = run(t, "auth-unlock", "locked@x.test")
	if code != 0 || !strings.Contains(out, "잠금을 풀었습니다") {
		t.Fatalf("auth-unlock: %d %q %q", code, out, errw)
	}
	if code, _, errw := run(t, "auth-unlock"); code != 1 || !strings.Contains(errw, "사용법") {
		t.Errorf("인자 없는 auth-unlock: %d %q", code, errw)
	}
	if code, _, errw := run(t, "auth-unlock", "ghost@x.test"); code != 1 || errw == "" {
		t.Errorf("없는 계정: %d %q", code, errw)
	}

	d, closeDeps, err = bootstrap.Build(ctx, config.Load())
	if err != nil {
		t.Fatal(err)
	}
	defer closeDeps()
	for _, action := range []string{"role.recompute", "auth.account_unlock"} {
		var ch, role string
		if err := d.DB.QueryRow(`SELECT channel, actor_role FROM audit_logs WHERE action = ? ORDER BY seq DESC LIMIT 1`, action).Scan(&ch, &role); err != nil {
			t.Fatalf("%s 감사 행: %v", action, err)
		}
		if ch != "cli" || role != "system" {
			t.Errorf("%s: 경로 %q 역할 %q (cli·system 이어야 함)", action, ch, role)
		}
	}
	var failed int
	_ = d.DB.QueryRow(`SELECT failed_count FROM user_credentials WHERE user_id = ?`, uid).Scan(&failed)
	if failed != 0 {
		t.Errorf("잠금이 풀리지 않음: %d", failed)
	}
	store, ok := d.AuditReader.(*audit.Store)
	if !ok {
		t.Fatal("감사 저장소 형식")
	}
	if res, err := store.Verify(ctx); err != nil || !res.ChainOK {
		t.Fatalf("체인: %+v %v", res, err)
	}
}
