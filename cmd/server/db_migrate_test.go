package main

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/bootstrap"
	"github.com/choibongseok/procure-ledger/internal/config"
	"github.com/choibongseok/procure-ledger/internal/contract"
	caudit "github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/dbmigrate"
	"github.com/choibongseok/procure-ledger/internal/dbtest"
)

// 이관 대표 흐름(B37 5단계): 시드한 SQLite 원본 → PG 이관(표별 검증·감사 체인) → 이관된 데이터베이스로 서버 조립·기동 → 헬스와 로그인된 API 가 응답한다.
func TestMigratedDatabaseBootsServerAndServes(t *testing.T) {
	if !dbtest.PG() {
		t.Skip("PG 전용 시험(verify-pg): 이관 대상이 PostgreSQL 이다")
	}
	ctx := context.Background()
	// 1) SQLite 원본 만들기.
	src := filepath.Join(t.TempDir(), "src.db")
	t.Setenv("PL_DB_PATH", src)
	t.Setenv("PL_DB_URL", "")
	t.Setenv("PL_DEV_IDP", "1")
	t.Setenv("PL_LLM_PROVIDER", "mock")
	scfg := config.Load()
	d0, close0, err := bootstrap.Build(ctx, scfg)
	if err != nil {
		t.Fatal(err)
	}
	var p contract.Principal
	var role string
	if err := d0.DB.QueryRowContext(ctx, `SELECT id, name, role, department_id FROM users WHERE email = 'staff@pl.test'`).Scan(&p.UserID, &p.Name, &role, &p.DepartmentID); err != nil {
		t.Fatal(err)
	}
	p.Role, p.Channel = contract.Role(role), contract.ChannelWeb
	if err := d0.Txs.WithTx(ctx, func(tx contract.Tx) error {
		return d0.Audit.Record(ctx, tx, caudit.Entry{Actor: p, Action: "migrate.boot", TargetType: caudit.TargetPolicy, TargetID: "x", Result: caudit.ResultSuccess})
	}); err != nil {
		t.Fatal(err)
	}
	close0()

	// 2) 이관.
	url := dbtest.URL(t)
	rep, err := dbmigrate.Run(ctx, dbmigrate.Options{From: src, To: url, AllowInsecure: true})
	if err != nil || !rep.OK() || !rep.Committed {
		t.Fatalf("이관: %+v %v", rep, err)
	}

	// 3) 이관된 PG 로 서버 조립·기동.
	t.Setenv("PL_DB_PATH", "")
	t.Setenv("PL_DB_URL", url)
	t.Setenv("PL_DB_ALLOW_INSECURE", "1")
	cfg := config.Load()
	d, closeDB, err := bootstrap.Build(ctx, cfg)
	if err != nil {
		t.Fatalf("이관된 PG 로 서버 조립: %v", err)
	}
	defer closeDB()
	h := handlerFor(buildRoutes(cfg, d), d)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/healthz", nil))
	if w.Code != 200 {
		t.Fatalf("이관된 DB 로 기동한 서버의 /healthz: %d %s", w.Code, w.Body.String())
	}
	// 이관된 사용자·역할로 개인 토큰 발급과 인증 API 가 동작한다.
	var staff contract.Principal
	var r2 string
	if err := d.DB.QueryRowContext(ctx, `SELECT id, name, role, department_id FROM users WHERE email = 'staff@pl.test'`).Scan(&staff.UserID, &staff.Name, &r2, &staff.DepartmentID); err != nil {
		t.Fatal(err)
	}
	staff.Role, staff.Channel = contract.Role(r2), contract.ChannelWeb
	raw, _, err := d.Tokens.Issue(ctx, staff, "migrated", 0)
	if err != nil {
		t.Fatalf("토큰 발급: %v", err)
	}
	req := httptest.NewRequest("GET", "/api/me", nil)
	req.Header.Set("Authorization", "Bearer "+raw)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("이관된 사용자의 /api/me: %d %s", w.Code, w.Body.String())
	}
	// 감사 체인은 이관 뒤 서버가 쓴 기록(토큰 발급)까지 이어진다.
	res, err := d.AuditReader.Verify(ctx)
	if err != nil || !res.ChainOK {
		t.Fatalf("이관 뒤 체인: %+v %v", res, err)
	}
}
