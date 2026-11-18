package main

// 감사 체인 외부 앵커(B4) HTTP 통합 테스트.

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/bootstrap"
	"github.com/choibongseok/procure-ledger/internal/config"
	"github.com/choibongseok/procure-ledger/internal/contract/deps"
	"github.com/choibongseok/procure-ledger/internal/dbtest"
	"github.com/choibongseok/procure-ledger/migrations"
)

type verifyBody struct {
	OK                 bool   `json:"ok"`
	ExternallyVerified bool   `json:"externallyVerified"`
	Level              string `json:"level"`
	Summary            string `json:"summary"`
	ChainOK            bool   `json:"chainOk"`
	Anchor             struct {
		State       string `json:"state"`
		Message     string `json:"message"`
		Lines       int    `json:"lines"`
		LostHistory bool   `json:"lostHistory"`
	} `json:"anchor"`
}

func anchorEnv(t *testing.T) (*platformEnv, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "anchor.log")
	e := newPlatformEnvWith(t, true, func(c *config.Config) {
		c.AuditAnchorKey, c.AuditAnchorPath = "http-test-anchor-key-0123", path
	})
	return e, path
}

func doVerify(t *testing.T, e *platformEnv, c *http.Client) verifyBody {
	t.Helper()
	code, body := e.call(c, "POST", "/api/audit-logs/verify", "", map[string]string{"X-CSRF-Token": e.csrf(c)})
	if code != 200 {
		t.Fatalf("verify: %d %s", code, body)
	}
	var v verifyBody
	if err := json.Unmarshal([]byte(body), &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestAnchorDisabledWithoutKey(t *testing.T) {
	e := newPlatformEnv(t, true)
	if e.d.AuditAnchor != nil {
		t.Fatal("키가 없으면 앵커는 꺼져 있어야 함")
	}
	c := e.browser()
	e.login(c, "auditor@pl.test")
	v := doVerify(t, e, c)
	if v.ExternallyVerified || v.Level != "chain_only" || !strings.Contains(v.Summary, "완전 통과가 아닙니다") {
		t.Fatalf("앵커가 꺼지면 최상위에서 완전 통과와 구분되어야 함: %+v", v)
	}
	if !v.OK || v.Anchor.State != "disabled" || !strings.Contains(v.Anchor.Message, "외부 대조를 하지 않았습니다") {
		t.Fatalf("외부 대조 없음을 알려야 함: %+v", v)
	}
	if code, _ := e.call(c, "POST", "/api/audit-logs/anchor", "", map[string]string{"X-CSRF-Token": e.csrf(c)}); code != http.StatusConflict {
		t.Fatalf("앵커가 꺼져 있으면 409: %d", code)
	}
}

func TestAnchorStampAndVerifyOverHTTP(t *testing.T) {
	e, path := anchorEnv(t)
	c := e.browser()
	e.login(c, "auditor@pl.test")
	h := map[string]string{"X-CSRF-Token": e.csrf(c)}
	if v := doVerify(t, e, c); v.Anchor.State != "empty" || v.ExternallyVerified {
		t.Fatalf("앵커 전에는 empty 이고 완전 통과가 아님: %+v", v)
	}
	code, body := e.call(c, "POST", "/api/audit-logs/anchor", "", h)
	if code != 200 || !strings.Contains(body, `"written":true`) {
		t.Fatalf("anchor: %d %s", code, body)
	}
	if e.auditCount("audit_log.anchor", "success") != 1 {
		t.Fatal("앵커 기록이 감사 로그에 남아야 함")
	}
	if v := doVerify(t, e, c); !v.OK || !v.ExternallyVerified || v.Level != "verified" || v.Anchor.State != "ok" || v.Anchor.Lines != 1 {
		t.Fatalf("정상: %+v", v)
	}

	// 꼬리 삭제: 앵커 이후 기록을 지운다(추가 전용 트리거를 우회).
	for _, tr := range []string{"trg_audit_no_update", "trg_audit_no_delete"} {
		dbtest.DropTrigger(t, e.d.DB, tr)
	}
	// 앵커가 가리키는 기록(2번)부터 끝까지 지운다.
	if _, err := e.d.DB.Exec(`DELETE FROM audit_logs WHERE seq > 1`); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
	v := doVerify(t, e, c)
	if v.OK || v.Anchor.State != "tail_deleted" {
		t.Fatalf("꼬리 삭제를 짚어야 함: %+v", v)
	}
}

// 검증·앵커 기록은 audit:verify 만 가진 역할(auditor)에게 열린다. 나머지는 403 이고 파일은 그대로다.
func TestAnchorEndpointsDeniedForOtherRoles(t *testing.T) {
	e, path := anchorEnv(t)
	for _, email := range []string{"staff@pl.test", "lead@pl.test", "finance@pl.test", "admin@pl.test"} {
		c := e.browser()
		e.login(c, email)
		h := map[string]string{"X-CSRF-Token": e.csrf(c)}
		for _, p := range []string{"/api/audit-logs/anchor", "/api/audit-logs/verify"} {
			if code, _ := e.call(c, "POST", p, "", h); code != http.StatusForbidden {
				t.Errorf("%s 의 %s 가 %d", email, p, code)
			}
		}
	}
	if b, _ := os.ReadFile(path); len(b) != 0 {
		t.Fatal("권한 없는 요청이 앵커 파일에 기록함")
	}
	if e.auditCount("access.denied", "denied") < 8 {
		t.Fatal("거부가 감사 로그에 남아야 함")
	}
}

func TestAnchorStartupRefusals(t *testing.T) {
	wire := func(t *testing.T, tweak func(*config.Config)) error {
		t.Helper()
		db := dbtest.Open(t)
		if err := migrations.Apply(context.Background(), db); err != nil {
			t.Fatal(err)
		}
		cfg := config.Config{BaseURL: "http://localhost:8080"}
		tweak(&cfg)
		d := deps.Deps{DB: db}
		return bootstrap.WirePlatform(cfg, &d)
	}
	good := "startup-anchor-key-0123456"
	dir := t.TempDir()
	if err := wire(t, func(c *config.Config) {}); err != nil {
		t.Fatalf("키가 없으면 꺼진 채로 기동: %v", err)
	}
	cases := map[string]func(*config.Config){
		"경로 없음":     func(c *config.Config) { c.AuditAnchorKey = good },
		"쓸 수 없는 경로": func(c *config.Config) { c.AuditAnchorKey, c.AuditAnchorPath = good, filepath.Join(dir, "no", "a.log") },
		"짧은 키":      func(c *config.Config) { c.AuditAnchorKey, c.AuditAnchorPath = "short", filepath.Join(dir, "a.log") },
		"잘못된 간격": func(c *config.Config) {
			c.AuditAnchorKey, c.AuditAnchorPath, c.AuditAnchorInterval = good, filepath.Join(dir, "b.log"), -1
		},
	}
	for name, tweak := range cases {
		if err := wire(t, tweak); err == nil {
			t.Errorf("%s: 기동이 거부되어야 함", name)
		}
	}
}

// B9 ④: 앵커 파일을 지운 뒤 수동 앵커로 조용히 정상 복귀하지 못한다. 유실은 감사 로그에 남고 명시적 재시작만 복귀시킨다.
func TestAnchorFileLossOverHTTP(t *testing.T) {
	e, path := anchorEnv(t)
	c := e.browser()
	e.login(c, "auditor@pl.test")
	h := map[string]string{"X-CSRF-Token": e.csrf(c)}
	if code, body := e.call(c, "POST", "/api/audit-logs/anchor", "", h); code != 200 {
		t.Fatalf("anchor: %d %s", code, body)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	// 수동 앵커: 파일이 새로 생겨 ok 로 돌아오면 안 된다.
	if code, _ := e.call(c, "POST", "/api/audit-logs/anchor", "", h); code != http.StatusConflict {
		t.Fatalf("유실 뒤 수동 앵커는 409: %d", code)
	}
	if e.auditCount("audit_log.anchor_lost", "error") != 1 {
		t.Fatal("유실이 별도 동작으로 감사 로그에 남아야 함")
	}
	if b, _ := os.ReadFile(path); len(b) != 0 {
		t.Fatalf("조용히 새 앵커를 썼음: %q", b)
	}
	if v := doVerify(t, e, c); v.OK || v.Anchor.State != "anchor_lost" || v.ExternallyVerified || v.Level != "failed" {
		t.Fatalf("유실이 검증에 드러나야 함: %+v", v)
	}
	// 다른 역할은 재시작할 수 없다.
	sc := e.browser()
	e.login(sc, "staff@pl.test")
	if code, _ := e.call(sc, "POST", "/api/audit-logs/anchor/restart", "", map[string]string{"X-CSRF-Token": e.csrf(sc)}); code != http.StatusForbidden {
		t.Fatalf("auditor 만 재시작 가능: %d", code)
	}
	if code, body := e.call(c, "POST", "/api/audit-logs/anchor/restart", "", h); code != 200 {
		t.Fatalf("restart: %d %s", code, body)
	}
	if e.auditCount("audit_log.anchor_restart", "success") != 1 {
		t.Fatal("재시작이 감사 로그에 남아야 함")
	}
	v := doVerify(t, e, c)
	if !v.OK || v.Anchor.State != "ok" || !v.Anchor.LostHistory || v.ExternallyVerified || v.Level != "lost_history" {
		t.Fatalf("복귀하되 유실 이력이 계속 표시되어야 함: %+v", v)
	}
	// 유실 상태가 아닐 때 재시작은 거부된다.
	if code, _ := e.call(c, "POST", "/api/audit-logs/anchor/restart", "", h); code != http.StatusConflict {
		t.Fatalf("유실이 아니면 409: %d", code)
	}
}

// B9 ③: 앵커가 꺼진 채 기동하면 서버 로그에 경고가 남는다(키 없이 경로만 준 경우 포함).
func TestAnchorDisabledStartupWarning(t *testing.T) {
	run := func(t *testing.T, tweak func(*config.Config)) string {
		t.Helper()
		db := dbtest.Open(t)
		if err := migrations.Apply(context.Background(), db); err != nil {
			t.Fatal(err)
		}
		var buf strings.Builder
		log.SetOutput(&buf)
		t.Cleanup(func() { log.SetOutput(os.Stderr) })
		cfg := config.Config{BaseURL: "http://localhost:8080"}
		tweak(&cfg)
		d := deps.Deps{DB: db}
		if err := bootstrap.WirePlatform(cfg, &d); err != nil {
			t.Fatal(err)
		}
		return buf.String()
	}
	if out := run(t, func(c *config.Config) {}); strings.Count(out, "외부 앵커가 꺼져 있습니다") != 1 {
		t.Fatalf("키·경로 없음: 경고 한 줄이 필요: %q", out)
	}
	if out := run(t, func(c *config.Config) { c.AuditAnchorPath = filepath.Join(t.TempDir(), "a.log") }); !strings.Contains(out, "PL_AUDIT_ANCHOR_KEY 가 없어") {
		t.Fatalf("경로만 준 경우도 경고: %q", out)
	}
	if out := run(t, func(c *config.Config) {
		c.AuditAnchorKey, c.AuditAnchorPath = "warn-test-anchor-key-0123", filepath.Join(t.TempDir(), "a.log")
	}); strings.Contains(out, "경고") {
		t.Fatalf("앵커가 켜져 있으면 경고가 없어야 함: %q", out)
	}
}
