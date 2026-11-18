package main

import (
	"bytes"
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/admincmd"
	"github.com/choibongseok/procure-ledger/internal/contract/deps"
)

// B42 3단계 커넥터 예시(examples/connector): 실제 서버 위에서 `erp-admin connector run` 으로 거래처·전표를 들이고(in) 거래처를 CSV 로 내보낸다(out).
// `make connector-demo` 가 이 시험을 돌린다.
func TestConnectorDemo(t *testing.T) {
	e := newWriteEnv(t, "vendors:write", "vendors:read", "journals:draft", "journals:read")
	srv := httptest.NewServer(e.h)
	defer srv.Close()
	// 커넥터 명령은 연동 API 만 쓴다: DB 를 열거나 서버 조립(Build)을 거치면 시험이 실패한다.
	oldBuild := admincmd.Build
	admincmd.Build = func(context.Context) (deps.Deps, func(), error) {
		t.Error("erp-admin connector run 이 서버 조립(DB 열기)을 불렀다")
		return e.d, func() {}, nil
	}
	defer func() { admincmd.Build = oldBuild }()
	t.Setenv("PL_DB_PATH", filepath.Join(t.TempDir(), "must-not-be-created.db"))
	t.Setenv("PL_DB_URL", "")
	t.Setenv("PL_CONNECTOR_TOKEN", e.tok)
	t.Setenv("PL_BASE_URL", srv.URL)
	ex := filepath.Join("..", "..", "examples", "connector")
	run := func(args ...string) (string, int) {
		var out, errw bytes.Buffer
		code := admincmd.Run(context.Background(), append([]string{"connector", "run"}, args...), &out, &errw)
		return out.String() + errw.String(), code
	}
	vendorsBefore := e.count(`SELECT COUNT(*) FROM vendors`)
	// 거래처 in: --dry-run 은 아무것도 반영하지 않고, 실행하면 반영하고, 다시 실행해도 같은 결과(동일 행만 → 변경 없음)다.
	out, code := run("--mapping", filepath.Join(ex, "mapping-vendors-in.json"), "--direction", "in", "--dry-run")
	if code != 0 || !strings.Contains(out, "dry_run") || e.count(`SELECT COUNT(*) FROM vendors`) != vendorsBefore {
		t.Fatalf("dry-run: %d %s", code, out)
	}
	if out, code = run("--mapping", filepath.Join(ex, "mapping-vendors-in.json"), "--direction", "in"); code != 0 || !strings.Contains(out, "결과 ok") {
		t.Fatalf("거래처 in: %d %s", code, out)
	}
	if e.count(`SELECT COUNT(*) FROM vendors`) != vendorsBefore+2 {
		t.Fatal("거래처 2건이 반영되어야 한다")
	}
	if out, code = run("--mapping", filepath.Join(ex, "mapping-vendors-in.json"), "--direction", "in"); code != 0 || e.count(`SELECT COUNT(*) FROM vendors`) != vendorsBefore+2 {
		t.Fatalf("같은 파일 두 번째 실행은 같은 결과여야 한다: %d %s", code, out)
	}
	// 전표 in: 두 건이 초안으로 들어가고, 다시 실행해도 늘지 않는다(external_ref 멱등).
	if out, code = run("--mapping", filepath.Join(ex, "mapping-journals-in.json"), "--direction", "in"); code != 0 {
		t.Fatalf("전표 in: %d %s", code, out)
	}
	if n := e.count(`SELECT COUNT(*) FROM journals WHERE source_type = 'api' AND status = 'draft'`); n != 2 {
		t.Fatalf("전표 초안 2건: %d", n)
	}
	if out, code = run("--mapping", filepath.Join(ex, "mapping-journals-in.json"), "--direction", "in"); code != 0 || e.count(`SELECT COUNT(*) FROM journals WHERE source_type = 'api'`) != 2 {
		t.Fatalf("재실행이 전표를 늘렸다: %d %s", code, out)
	}
	// 거래처 out: CSV 로 내보낸다. 쓸 위치는 임시 폴더로 바꾼 사본 매핑을 쓴다.
	dir := t.TempDir()
	raw, err := os.ReadFile(filepath.Join(ex, "mapping-vendors-out.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "m.json"), []byte(strings.Replace(string(raw), "out/vendors.csv", "vendors.csv", 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, code = run("--mapping", filepath.Join(dir, "m.json"), "--direction", "out"); code != 0 {
		t.Fatalf("거래처 out: %d %s", code, out)
	}
	b, err := os.ReadFile(filepath.Join(dir, "vendors.csv"))
	if err != nil || !strings.Contains(string(b), "VendorId,VendorName,Source") || !strings.Contains(string(b), "샘플상사") || !strings.Contains(string(b), "procure-ledger") {
		t.Fatalf("CSV: %v %s", err, b)
	}
	for _, raw := range []string{"555-99-00011", "5559900011", "sales@sample-vendor.example", "02-555-0101"} {
		if strings.Contains(string(b), raw) {
			t.Fatalf("내보낸 CSV 에 원문이 있다(API 는 가린 값만 준다): %s", raw)
		}
	}
	// 서버 쪽 기록은 API 가 남긴다(가져오기 반영·전표 초안 감사). 명령 자신은 DB 에 아무것도 쓰지 않는다.
	if e.count(`SELECT COUNT(*) FROM connector_runs`) != 0 || e.count(`SELECT COUNT(*) FROM audit_logs WHERE action = 'connector.run'`) != 0 {
		t.Fatal("커넥터 명령이 DB 에 직접 기록했다")
	}
	if e.count(`SELECT COUNT(*) FROM audit_logs WHERE action = 'api.import_apply' AND channel = 'api'`) < 1 || e.count(`SELECT COUNT(*) FROM audit_logs WHERE action = 'api.journal_draft' AND channel = 'api'`) < 2 {
		t.Fatal("서버 쪽 감사(API 경로)가 남아야 한다")
	}
	if _, err := os.Stat(os.Getenv("PL_DB_PATH")); err == nil {
		t.Fatal("명령이 DB 파일을 만들었다")
	}
	// 토큰이 없으면 거부된다(파일·인자로 받지 않는다).
	t.Setenv("PL_CONNECTOR_TOKEN", "")
	if _, code = run("--mapping", filepath.Join(ex, "mapping-vendors-in.json"), "--direction", "in"); code == 0 {
		t.Fatal("토큰 없이 실행되면 안 된다")
	}
}
