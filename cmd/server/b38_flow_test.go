package main

// B38 대표 흐름 e2e(AC-1880 중 서버 쪽): 빈 DB 첫 기동 → 설치 코드로 마법사 완료 → 로그인 → 백업(서버가 켜진 채) → 복구 리허설 →
// 서버 정지 후 복구 → 복구 뒤 체인·앵커 검증, 복구 전 세션 무효, 마법사는 닫힌 채. 실제 조립(WirePlatform → setup → 라우트)과 erp-admin 명령을 쓴다.
// SQLite 파일 DB 위의 흐름이다(PG 의 백업·복구는 internal/backup 의 PG 시험이 본다).

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/admincmd"
	"github.com/choibongseok/procure-ledger/internal/auth"
	"github.com/choibongseok/procure-ledger/internal/backup"
	"github.com/choibongseok/procure-ledger/internal/bootstrap"
	"github.com/choibongseok/procure-ledger/internal/config"
	"github.com/choibongseok/procure-ledger/internal/contract/deps"
	"github.com/choibongseok/procure-ledger/internal/dbtest"
	"github.com/choibongseok/procure-ledger/internal/dbutil"
	"github.com/choibongseok/procure-ledger/internal/setup"
	"github.com/choibongseok/procure-ledger/migrations"
)

const (
	flowBase   = "http://localhost:8080"
	flowEmail  = "owner@acme.test"
	flowPw     = "Wizard-Pass-2026!"
	flowAnchor = "b38-flow-anchor-key-0123456789"
	flowCode   = "flow-setup-code-0123456789"
)

type flowServer struct {
	d deps.Deps
	h http.Handler
}

func startFlowServer(t *testing.T, dbPath, anchorPath string) *flowServer {
	t.Helper()
	db, err := dbutil.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := migrations.Apply(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{BaseURL: flowBase, SetupCode: flowCode, AuditAnchorKey: flowAnchor, AuditAnchorPath: anchorPath}
	d := deps.Deps{DB: db}
	if err := bootstrap.WirePlatform(cfg, &d); err != nil {
		t.Fatal(err)
	}
	if _, err := setup.Reconcile(context.Background(), d.DB, nil); err != nil {
		t.Fatal(err)
	}
	ss, err := setup.New(context.Background(), setup.Options{DB: d.DB, Txs: d.Txs, Audit: d.Audit, Auth: d.Authenticator.(*auth.Service), Code: cfg.SetupCode})
	if err != nil {
		t.Fatal(err)
	}
	d.Setup = ss
	return &flowServer{d: d, h: handlerFor(buildRoutes(cfg, d), d)}
}

func (s *flowServer) do(method, path string, body any, hdr map[string]string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	var r *http.Request
	if body != nil {
		b, _ := json.Marshal(body)
		r = httptest.NewRequest(method, path, bytes.NewReader(b))
		r.Header.Set("Content-Type", "application/json")
	} else {
		r = httptest.NewRequest(method, path, nil)
	}
	if !strings.HasPrefix(path, "/api/setup/") { // 마법사는 출처 헤더 없이도 같은 출처 규칙을 통과한다(t1 시험과 같은 방식)
		r.Header.Set("Origin", flowBase)
	}
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	for _, c := range cookies {
		r.AddCookie(c)
	}
	w := httptest.NewRecorder()
	s.h.ServeHTTP(w, r)
	return w
}

func (s *flowServer) login(t *testing.T) *http.Cookie {
	t.Helper()
	w := s.do("POST", "/api/auth/login/password", map[string]string{"email": flowEmail, "password": flowPw}, nil)
	if w.Code != 200 && w.Code != 204 {
		t.Fatalf("로그인: %d %s", w.Code, w.Body.String())
	}
	for _, c := range w.Result().Cookies() {
		if c.HttpOnly && c.Value != "" {
			return c
		}
	}
	t.Fatalf("세션 쿠키가 없음: %s", w.Body.String())
	return nil
}

func TestB38RepresentativeFlow(t *testing.T) {
	if dbtest.PG() {
		t.Skip("SQLite 파일 DB 흐름(PG 는 internal/backup 의 PG 시험이 본다)")
	}
	ctx := context.Background()
	root := t.TempDir()
	dbPath := filepath.Join(root, "data", "erp.db")
	anchorPath := filepath.Join(root, "anchor", "audit.anchor")
	for _, d := range []string{filepath.Dir(dbPath), filepath.Dir(anchorPath)} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	// erp-admin 은 서버와 같은 환경변수로 같은 DB 를 연다.
	t.Setenv("PL_DB_PATH", dbPath)
	t.Setenv("PL_DB_URL", "")
	t.Setenv("PL_AUDIT_ANCHOR_KEY", flowAnchor)
	t.Setenv("PL_AUDIT_ANCHOR_PATH", anchorPath)
	t.Setenv("PL_BACKUP_DIR", filepath.Join(root, "backups"))
	t.Setenv("PL_BACKUP_KEY", "")

	// 1) 빈 DB 첫 기동 → 마법사 완료 → 로그인.
	srv := startFlowServer(t, dbPath, anchorPath)
	w := srv.do("POST", "/api/setup/complete", map[string]any{"companyName": "에이씨엠이", "adminName": "오너", "adminEmail": flowEmail, "adminPassword": flowPw}, map[string]string{setup.CodeHeader: flowCode})
	if w.Code != 201 {
		t.Fatalf("마법사 완료: %d %s", w.Code, w.Body.String())
	}
	cookie := srv.login(t)
	if w := srv.do("GET", "/api/me", nil, nil, cookie); w.Code != 200 {
		t.Fatalf("로그인 세션: %d", w.Code)
	}
	if _, err := srv.d.AuditAnchor.Stamp(ctx); err != nil { // 앵커에 한 줄
		t.Fatal(err)
	}

	run := func(args ...string) (int, string) {
		var out, errb bytes.Buffer
		code := admincmd.Run(ctx, args, &out, &errb)
		return code, out.String() + errb.String()
	}
	// 2) 백업(서버가 켜진 채). 첫 줄은 세트 이름.
	code, out := run("backup")
	if code != 0 || !strings.Contains(out, "백업 세트를 만들었습니다") {
		t.Fatalf("백업: %d %s", code, out)
	}
	_, list := run("list-backups")
	set := strings.Fields(strings.TrimSpace(list))[0]
	if code, out := run("verify-backup", set); code != 0 {
		t.Fatalf("verify-backup: %d %s", code, out)
	}
	// 3) 복구 리허설: 운영 DB 를 바꾸지 않는다.
	before, _, _ := backup.FileSHA256(dbPath)
	anchorBefore, _, _ := backup.FileSHA256(anchorPath)
	if code, out := run("restore", set, "--rehearse"); code != 0 || !strings.Contains(out, "복구 리허설 통과") {
		t.Fatalf("리허설: %d %s", code, out)
	}
	if a, _, _ := backup.FileSHA256(dbPath); a != before {
		t.Fatal("리허설이 운영 DB 파일을 바꿈")
	}
	if a, _, _ := backup.FileSHA256(anchorPath); a != anchorBefore {
		t.Fatal("리허설이 앵커 파일을 바꿈")
	}
	var auditsBefore, auditsAfter int
	_ = srv.d.DB.QueryRow(`SELECT COUNT(*) FROM audit_logs`).Scan(&auditsBefore)
	// 백업 뒤에 일어난 일(두 번째 로그인 + 앵커 줄)은 복구로 사라진다.
	srv.login(t)
	if _, err := srv.d.AuditAnchor.Stamp(ctx); err != nil {
		t.Fatal(err)
	}
	_ = srv.d.DB.QueryRow(`SELECT COUNT(*) FROM audit_logs`).Scan(&auditsAfter)
	if auditsAfter <= auditsBefore {
		t.Fatal("전제: 백업 뒤 감사 기록이 늘어야 함")
	}

	// 4) 서버가 켜져 있으면 복구는 거부된다.
	if code, out := run("restore", set, "--replace", "--yes"); code != 1 || !strings.Contains(out, "사용 중") {
		t.Fatalf("서버 실행 중 거부: %d %s", code, out)
	}
	// 5) 서버 정지 후 복구.
	_ = srv.d.DB.Close()
	code, out = run("restore", set, "--replace", "--yes")
	if code != 0 || !strings.Contains(out, "복구 통과") || !strings.Contains(out, "손실 구간") {
		t.Fatalf("복구: %d %s", code, out)
	}
	if strings.Contains(out, flowPw) || strings.Contains(out, flowAnchor) {
		t.Fatal("출력에 비밀이 있음")
	}

	// 6) 복구 뒤: 체인·앵커 검증 통과(복사본으로 점검), 복구 기록이 마지막 감사, 옆에 보존된 이전 DB·앵커.
	verifyCopy := filepath.Join(root, "verify-copy.db")
	raw, err := os.ReadFile(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(verifyCopy, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	rep, err := backup.Inspect(ctx, verifyCopy, anchorPath, []byte(flowAnchor))
	if err != nil || !rep.ChainOK || rep.Anchor.State != "ok" || len(rep.Invariants) != 0 {
		t.Fatalf("복구 후 점검: %v %+v", err, rep)
	}
	pre := 0
	ents, _ := os.ReadDir(root)
	for _, d := range []string{filepath.Dir(dbPath), filepath.Dir(anchorPath)} {
		es, _ := os.ReadDir(d)
		for _, e := range es {
			if strings.Contains(e.Name(), ".pre-restore-") {
				pre++
			}
		}
	}
	_ = ents
	if pre < 2 {
		t.Fatalf("이전 DB·앵커가 .pre-restore 로 보존되어야 함: %d개", pre)
	}

	// 7) 복구된 DB 로 서버를 다시 띄운다: 마법사는 닫힌 채, 복구 전 세션은 무효, 같은 비밀번호로 새로 로그인된다.
	srv2 := startFlowServer(t, dbPath, anchorPath)
	defer srv2.d.DB.Close()
	if w := srv2.do("GET", "/api/setup/status", nil, nil); w.Code != 404 {
		t.Errorf("복구 뒤 마법사가 다시 열림: %d", w.Code)
	}
	if w := srv2.do("GET", "/api/me", nil, nil, cookie); w.Code != 401 {
		t.Errorf("복구 전 세션이 살아 있음: %d", w.Code)
	}
	newCookie := srv2.login(t)
	if w := srv2.do("GET", "/api/me", nil, nil, newCookie); w.Code != 200 {
		t.Errorf("복구 뒤 새 로그인: %d", w.Code)
	}
	var restoreAudits int
	_ = srv2.d.DB.QueryRow(`SELECT COUNT(*) FROM audit_logs WHERE action = 'system.restore' AND channel = 'cli'`).Scan(&restoreAudits)
	if restoreAudits != 1 {
		t.Errorf("system.restore 감사 %d건", restoreAudits)
	}
}
