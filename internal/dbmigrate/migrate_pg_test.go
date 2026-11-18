package dbmigrate_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/audit"
	"github.com/choibongseok/procure-ledger/internal/bootstrap"
	"github.com/choibongseok/procure-ledger/internal/config"
	"github.com/choibongseok/procure-ledger/internal/contract"
	caudit "github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/dbmigrate"
	"github.com/choibongseok/procure-ledger/internal/dbtest"
	"github.com/choibongseok/procure-ledger/internal/dbutil"
)

// seedSource 는 서버가 시드를 만든 SQLite 원본 파일을 만든다(개발용 조직·계정·역할 템플릿 + 감사 기록 몇 건).
func seedSource(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "src.db")
	t.Setenv("PL_DB_PATH", path)
	t.Setenv("PL_DB_URL", "")
	t.Setenv("PL_DEV_IDP", "1")
	t.Setenv("PL_LLM_PROVIDER", "mock")
	ctx := context.Background()
	d, closeDB, err := bootstrap.Build(ctx, config.Load())
	if err != nil {
		t.Fatal(err)
	}
	var p contract.Principal
	var role string
	if err := d.DB.QueryRowContext(ctx, `SELECT id, name, role, department_id FROM users WHERE email = 'staff@pl.test'`).Scan(&p.UserID, &p.Name, &role, &p.DepartmentID); err != nil {
		t.Fatal(err)
	}
	p.Role, p.Channel = contract.Role(role), contract.ChannelWeb
	for i := 0; i < 5; i++ {
		if err := d.Txs.WithTx(ctx, func(tx contract.Tx) error {
			return d.Audit.Record(ctx, tx, caudit.Entry{Actor: p, Action: "migrate.seed", TargetType: caudit.TargetPolicy, TargetID: "x", Result: caudit.ResultSuccess})
		}); err != nil {
			t.Fatal(err)
		}
	}
	closeDB()
	return path
}

func countRows(t *testing.T, url, table string) int64 {
	t.Helper()
	db, err := dbutil.OpenPG(context.Background(), url, dbutil.PGOptions{MaxConns: 2, AllowInsecure: true})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int64
	if err := db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func requirePG(t *testing.T) {
	t.Helper()
	if !dbtest.PG() {
		t.Skip("PG 전용 시험(verify-pg): 이관 대상이 PostgreSQL 이다")
	}
}

// 시드 DB → PG 이관 → 표별 행 수·체크섬·감사 체인·불변식이 모두 맞고 커밋된다. 이관 뒤 같은 데이터로 체인 검증과 시퀀스 이어쓰기가 된다.
func TestMigrateSeedDatabaseToPG(t *testing.T) {
	requirePG(t)
	src := seedSource(t)
	url := dbtest.URL(t)
	var out strings.Builder
	rep, err := dbmigrate.Run(context.Background(), dbmigrate.Options{From: src, To: url, AllowInsecure: true, Out: &out})
	if err != nil {
		t.Fatalf("이관: %v\n%+v", err, rep)
	}
	if !rep.OK() || !rep.Committed {
		t.Fatalf("판정: %+v", rep)
	}
	if rep.Rows() == 0 || rep.SourceSHA256 == "" {
		t.Fatalf("행 수·원본 해시: %+v", rep)
	}
	if countRows(t, url, "users") == 0 || countRows(t, url, "audit_logs") < 5 {
		t.Fatal("이관된 데이터가 대상에 없음")
	}
	// 이관 뒤: 체인 검증과 시퀀스(다음 자동 번호가 이관된 번호와 겹치지 않음).
	db, err := dbutil.OpenPG(context.Background(), url, dbutil.PGOptions{MaxConns: 2, AllowInsecure: true})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	res, err := audit.New(db).Verify(context.Background())
	if err != nil || !res.ChainOK {
		t.Fatalf("이관 뒤 체인 검증: %+v %v", res, err)
	}
	var maxID, nextID int64
	_ = db.QueryRow(`SELECT MAX(id) FROM users`).Scan(&maxID)
	if err := db.QueryRow(`INSERT INTO users (email, name, role, department_id) VALUES ('after@pl.test','이후','staff',(SELECT MIN(id) FROM departments)) RETURNING id`).Scan(&nextID); err != nil {
		t.Fatalf("이관 뒤 새 사용자 등록(시퀀스): %v", err)
	}
	if nextID <= maxID {
		t.Fatalf("시퀀스가 이관된 번호와 겹침: max %d, 다음 %d", maxID, nextID)
	}
	// 보호 트리거는 이관 뒤 다시 켜져 있다(감사 기록은 고칠 수 없다).
	if _, err := db.Exec(`UPDATE audit_logs SET action = 'x' WHERE seq = 1`); err == nil {
		t.Fatal("이관 뒤 감사 기록 수정 방지 트리거가 꺼져 있음")
	}
	// 같은 대상으로 다시 이관하면 비어 있지 않아 거부된다(덮어쓰지 않는다).
	if _, err := dbmigrate.Run(context.Background(), dbmigrate.Options{From: src, To: url, AllowInsecure: true}); err == nil || !strings.Contains(err.Error(), "비어 있지 않습니다") {
		t.Fatalf("두 번째 이관은 거부해야 함: %v", err)
	}
}

// --dry-run 은 모든 검사를 하고 아무것도 남기지 않는다.
func TestMigrateDryRunLeavesTargetEmpty(t *testing.T) {
	requirePG(t)
	src := seedSource(t)
	url := dbtest.URL(t)
	rep, err := dbmigrate.Run(context.Background(), dbmigrate.Options{From: src, To: url, AllowInsecure: true, DryRun: true})
	if err != nil || !rep.OK() || rep.Committed {
		t.Fatalf("dry-run: %+v %v", rep, err)
	}
	if n := countRows(t, url, "audit_logs"); n != 0 {
		t.Fatalf("dry-run 인데 감사 기록 %d건이 남음", n)
	}
	if n := countRows(t, url, "users"); n != 0 {
		t.Fatalf("dry-run 인데 사용자 %d명이 남음", n)
	}
	// 원본은 건드리지 않는다.
	if rep.SourceSHA256 == "" {
		t.Fatal("원본 해시가 없음")
	}
}

// 원본 감사 체인이 변조돼 있으면 이관은 실패하고 대상은 되돌려진다(원본은 그대로).
func TestMigrateTamperedAuditChainRollsBack(t *testing.T) {
	requirePG(t)
	src := seedSource(t)
	sdb, err := dbutil.Open(src)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sdb.Exec(`DROP TRIGGER trg_audit_no_update`); err != nil { // 변조를 흉내 내려고 보호 트리거를 지운다(원본 파일의 시험 복사본)
		t.Fatal(err)
	}
	if _, err := sdb.Exec(`UPDATE audit_logs SET action = 'tampered' WHERE seq = 3`); err != nil {
		t.Fatal(err)
	}
	_ = sdb.Close()
	url := dbtest.URL(t)
	rep, err := dbmigrate.Run(context.Background(), dbmigrate.Options{From: src, To: url, AllowInsecure: true})
	if err == nil || rep.OK() || rep.Committed {
		t.Fatalf("변조된 체인은 이관을 막아야 함: %+v %v", rep, err)
	}
	failed := false
	for _, c := range rep.Checks {
		if strings.HasPrefix(c.Name, "감사 체인(") && !c.OK {
			failed = true
		}
	}
	if !failed {
		t.Fatalf("체인 검증 실패가 판정표에 없음: %+v", rep.Checks)
	}
	if n := countRows(t, url, "audit_logs"); n != 0 {
		t.Fatalf("실패했는데 대상에 감사 기록 %d건이 남음", n)
	}
}

// 값을 옮길 수 없으면(NUL 문자) 표·행 번호와 함께 전체 중단하고 아무것도 남기지 않는다(E10).
func TestMigrateInvalidValueAbortsWholeMigration(t *testing.T) {
	requirePG(t)
	src := seedSource(t)
	sdb, err := dbutil.Open(src)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sdb.Exec(`INSERT INTO vendors (name) VALUES (?)`, "나쁜"+string(rune(0))+"이름"); err != nil {
		t.Skipf("원본이 NUL 문자를 거부함: %v", err)
	}
	_ = sdb.Close()
	url := dbtest.URL(t)
	rep, err := dbmigrate.Run(context.Background(), dbmigrate.Options{From: src, To: url, AllowInsecure: true})
	if err == nil || !strings.Contains(err.Error(), "vendors") || !strings.Contains(err.Error(), "NUL") || rep.Committed {
		t.Fatalf("NUL 문자: %v", err)
	}
	if n := countRows(t, url, "users"); n != 0 {
		t.Fatalf("중단됐는데 대상에 사용자 %d명이 남음", n)
	}
}

// 원본이 PG 기준과 다른 마이그레이션 번호이면 시작하지 않는다.
func TestMigrateRejectsSourceWithDifferentSchemaVersion(t *testing.T) {
	requirePG(t)
	src := seedSource(t)
	sdb, err := dbutil.Open(src)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sdb.Exec(`DELETE FROM schema_migrations WHERE name = (SELECT MAX(name) FROM schema_migrations)`); err != nil {
		t.Fatal(err)
	}
	_ = sdb.Close()
	_, err = dbmigrate.Run(context.Background(), dbmigrate.Options{From: src, To: dbtest.URL(t), AllowInsecure: true})
	if err == nil || !strings.Contains(err.Error(), "최종 번호") {
		t.Fatalf("번호 불일치는 거부해야 함: %v", err)
	}
}

var _ = sql.ErrNoRows
