package main

// B38 D15(AC-1834): 복구 리허설 모드의 서버는 주기 작업·시드·메일·앵커 기록을 하지 않고, 운영 경로에서는 기동을 거부한다.

import (
	"context"
	"database/sql"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/choibongseok/procure-ledger/internal/bootstrap"
	"github.com/choibongseok/procure-ledger/internal/config"
	"github.com/choibongseok/procure-ledger/internal/rehearsal"
)

func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().String()
}

func auditRows(t *testing.T, path string) int {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM audit_logs`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// serveOnce 는 dev 시드로 만든 DB 위에서 서버(run)를 잠깐 띄웠다 내리고, 그 사이 늘어난 감사 행 수와 앵커 파일 존재를 돌려준다.
func serveOnce(t *testing.T, rehearsalMode bool) (added int, anchorFile bool) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, rehearsal.Marker), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(dir, "db.sqlite")
	_, closeFn, err := bootstrap.Build(context.Background(), config.Config{DBPath: dbPath, BaseURL: "http://localhost:8080", DevIDP: true}) // 사용자·부서가 있는 복구본 흉내
	if err != nil {
		t.Fatal(err)
	}
	closeFn()
	before := auditRows(t, dbPath)

	addr := freeAddr(t)
	anchorPath := filepath.Join(dir, "anchor.log")
	t.Setenv("PL_DB_PATH", dbPath)
	t.Setenv("PL_ADDR", addr)
	t.Setenv("PL_BASE_URL", "http://"+addr)
	t.Setenv("PL_AUDIT_ANCHOR_KEY", "rehearsal-anchor-key-0123456789")
	t.Setenv("PL_AUDIT_ANCHOR_PATH", anchorPath)
	t.Setenv("PL_AUDIT_ANCHOR_INTERVAL", "1s")
	t.Setenv("PL_SMTP_HOST", "smtp.prod.example")
	t.Setenv("PL_SMTP_PORT", "587")
	t.Setenv("PL_SMTP_FROM", "erp@prod.example")
	t.Setenv("PL_DEV_IDP", "")
	t.Setenv("PL_DEV_SEED_PASSWORD", "")
	if rehearsalMode {
		t.Setenv("PL_REHEARSAL", "1")
	} else {
		t.Setenv("PL_REHEARSAL", "")
	}
	done := make(chan error, 1)
	go func() { done <- run() }()
	up := false
	for i := 0; i < 100 && !up; i++ {
		if resp, err := http.Get("http://" + addr + "/healthz"); err == nil {
			resp.Body.Close()
			up = resp.StatusCode == 200
		}
		select {
		case err := <-done:
			t.Fatalf("서버가 바로 종료됨: %v", err)
		default:
		}
		if !up {
			time.Sleep(100 * time.Millisecond)
		}
	}
	if !up {
		t.Fatal("서버 헬스체크 실패")
	}
	time.Sleep(2500 * time.Millisecond) // 주기 작업(앵커 1초 주기·조직 백필·권한 대조)이 돌 시간을 준다
	_ = syscall.Kill(syscall.Getpid(), syscall.SIGTERM)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("종료: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("서버가 종료되지 않음")
	}
	_, statErr := os.Stat(anchorPath)
	return auditRows(t, dbPath) - before, statErr == nil
}

func TestRehearsalServerDoesNoBackgroundWork(t *testing.T) {
	if testing.Short() {
		t.Skip("서버를 띄워 몇 초 기다리므로 -short 에서는 건너뜀")
	}
	// 대조: 리허설이 꺼져 있으면 같은 구성에서 조직 백필·앵커 기록 같은 작업이 감사 행과 앵커 파일을 만든다.
	added, anchor := serveOnce(t, false)
	if added == 0 || !anchor {
		t.Fatalf("대조 구성에서 주기 작업이 보이지 않음(감사 행 +%d, 앵커 파일 %v): 시험이 의미가 없다", added, anchor)
	}
	// 리허설: 아무것도 쓰지 않는다.
	added, anchor = serveOnce(t, true)
	if added != 0 {
		t.Errorf("리허설 모드가 감사 행을 %d건 만들었다(주기 작업·시드가 돈 것)", added)
	}
	if anchor {
		t.Error("리허설 모드가 앵커 파일을 만들었다")
	}
}

// 운영 경로(임시 위치도 마커도 아님)에서는 서버가 기동을 거부한다.
func TestRehearsalServerRefusesProductionPath(t *testing.T) {
	t.Setenv("PL_DB_PATH", filepath.Join("/nonexistent-pl-prod", "data", "erp.db"))
	t.Setenv("PL_REHEARSAL", "1")
	t.Setenv("PL_ADDR", "127.0.0.1:0")
	err := run()
	if err == nil || !strings.Contains(err.Error(), "PL_REHEARSAL") {
		t.Fatalf("운영 경로에서 기동함: %v", err)
	}
	if _, serr := os.Stat("/nonexistent-pl-prod"); serr == nil {
		t.Error("거부했는데 디렉터리를 만듦")
	}
}
