package backup_test

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"net/url"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/choibongseok/procure-ledger/internal/audit"
	"github.com/choibongseok/procure-ledger/internal/backup"
	caudit "github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/dbtest"
	"github.com/choibongseok/procure-ledger/internal/dbutil"
	"github.com/choibongseok/procure-ledger/internal/dbutil/pgtest"
	"github.com/choibongseok/procure-ledger/migrations"
)

func swapDB(t *testing.T, raw, name string) string {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	return u.String()
}

// AC-1835: PG 에서 백업(pg_dump)·복구(pg_restore)·체인 검증이 같은 판정표로 통과한다. PG 모드에서만 돈다(SQLite 모드는 건너뜀: verify-pg 가 돌린다).
func TestPGBackupRestoreRoundTrip(t *testing.T) {
	if !dbtest.PG() {
		t.Skip("PG 모드(make verify-pg)에서만 돈다")
	}
	if _, err := exec.LookPath("pg_dump"); err != nil {
		t.Fatalf("PG 모드 시험에는 pg_dump 가 필요합니다: %v", err)
	}
	ctx := context.Background()
	srcURL := dbtest.URL(t)
	src, err := dbutil.OpenPG(ctx, srcURL, dbutil.PGOptions{MaxConns: 4, AllowInsecure: true})
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	if _, err := src.Exec(`INSERT INTO departments (name) VALUES ('구매팀')`); err != nil {
		t.Fatal(err)
	}
	st := audit.New(src)
	for i := 0; i < 30; i++ {
		tx, _ := src.Begin()
		if err := st.Record(ctx, tx, caudit.Entry{Actor: backup.Actor, Action: "system.backup", TargetType: caudit.TargetSystem, TargetID: fmt.Sprintf("p%d", i), Result: caudit.ResultSuccess}); err != nil {
			t.Fatal(err)
		}
		_ = tx.Commit()
	}
	dir := t.TempDir()
	res, err := backup.Run(ctx, backup.Options{Kind: "postgres", DBURL: srcURL, AllowInsecure: true, Dir: filepath.Join(dir, "b"), Keep: 14, AppVersion: "test", Now: func() time.Time { return time.Now() }})
	if err != nil {
		t.Fatal(err)
	}
	if res.Manifest.DBKind != "postgres" || res.Manifest.ChainSeq != 30 || res.Manifest.Migration != migrations.Latest() {
		t.Fatalf("매니페스트: %+v", res.Manifest)
	}
	if strings.Contains(fmt.Sprint(res.Manifest), "@") {
		t.Fatalf("매니페스트에 접속 정보가 있음")
	}

	// 리허설: 임시 데이터베이스에 복구하고 지운다. 운영 데이터베이스는 그대로다.
	base := backup.RestoreOptions{SetDir: res.Path, Kind: "postgres", DBURL: srcURL, AllowInsecure: true, MaxMigration: migrations.Latest(), TmpRoot: dir}
	reh := base
	reh.Rehearse = true
	rr, err := backup.Restore(ctx, reh)
	if err != nil || !rr.OK {
		t.Fatalf("리허설: %v %+v", err, rr.Checks)
	}
	// 비어 있지 않은 대상은 거부(덮어쓰기 없음).
	if rr, err := backup.Restore(ctx, base); err != nil || rr.OK || !hasFail(rr.Checks, "대상 상태") {
		t.Fatalf("비어 있지 않은 PG 대상 거부: %v %+v", err, rr.Checks)
	}
	// 비어 있는 새 데이터베이스에는 복구된다.
	admin := pgtest.Require(t)
	adb, err := dbutil.OpenPG(ctx, admin, dbutil.PGOptions{MaxConns: 2, AllowInsecure: true})
	if err != nil {
		t.Fatal(err)
	}
	defer adb.Close()
	var b [5]byte
	_, _ = rand.Read(b[:])
	name := "plrestore_" + hex.EncodeToString(b[:])
	if _, err := adb.Exec(`CREATE DATABASE ` + name); err != nil {
		t.Fatal(err)
	}
	defer adb.Exec(`DROP DATABASE IF EXISTS ` + name + ` WITH (FORCE)`)
	real := base
	real.DBURL = swapDB(t, admin, name)
	rr, err = backup.Restore(ctx, real)
	if err != nil || !rr.OK {
		t.Fatalf("복구: %v %+v", err, rr.Checks)
	}
	dst, err := dbutil.OpenPG(ctx, real.DBURL, dbutil.PGOptions{MaxConns: 2, AllowInsecure: true})
	if err != nil {
		t.Fatal(err)
	}
	defer dst.Close()
	var n int
	var seq int64
	_ = dst.QueryRow(`SELECT COUNT(*) FROM departments`).Scan(&n)
	_ = dst.QueryRow(`SELECT MAX(seq) FROM audit_logs`).Scan(&seq)
	if n != 1 || seq != 31 { // 복구 기록 한 건이 덧붙는다
		t.Fatalf("복구된 내용: 부서 %d, 체인 머리 %d", n, seq)
	}
	vr, err := audit.New(dst).Verify(ctx)
	if err != nil || !vr.ChainOK {
		t.Fatalf("복구 뒤 체인: %v %+v", err, vr)
	}
	_ = sql.ErrNoRows
}

// pg_dump 를 찾지 못하면 명확한 오류로 실패한다(SQLite 모드에서도 돈다).
func TestPGBackupWithoutPGDumpFailsClearly(t *testing.T) {
	_, err := backup.Run(context.Background(), backup.Options{Kind: "postgres", DBURL: "postgres://u:pw@127.0.0.1:1/x", PGDump: "", Dir: t.TempDir(), Keep: 14,
		Now: func() time.Time { return time.Now() }, AllowInsecure: true})
	if err == nil {
		t.Fatal("접속할 수 없는 PG 는 실패여야 함")
	}
	t.Setenv("PATH", t.TempDir())
	_, err = backup.Run(context.Background(), backup.Options{Kind: "postgres", DBURL: "postgres://u:pw@127.0.0.1:1/x", Dir: t.TempDir(), Keep: 14, Now: func() time.Time { return time.Now() }})
	if err == nil || !strings.Contains(err.Error(), "pg_dump 를 찾을 수 없습니다") {
		t.Fatalf("pg_dump 가 없을 때의 안내 오류: %v", err)
	}
	if strings.Contains(err.Error(), "pw") {
		t.Fatalf("오류에 비밀번호가 있음: %v", err)
	}
}
