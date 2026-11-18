package org

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/choibongseok/procure-ledger/internal/audit"
	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/dbutil"
	"github.com/choibongseok/procure-ledger/internal/rbac"
	"github.com/choibongseok/procure-ledger/migrations"
)

// 기존 dev 시드와 같은 구성: 부서 3, 사용자 8(팀장 둘, 부서장 하나).
func seedLegacy(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, q := range []string{
		`INSERT INTO departments (id, name) VALUES (1, '구매팀'), (2, '경영지원팀'), (3, '영업팀')`,
		`INSERT INTO users (id, email, name, role, position, department_id, created_at) VALUES
			(1, 'admin@pl.test', '관리자', 'admin', NULL, 2, '2024-01-05T00:00:00Z'),
			(2, 'finance@pl.test', '재무 담당', 'finance', NULL, 2, '2024-01-05T00:00:00Z'),
			(3, 'lead@pl.test', '구매팀장', 'manager', 'team_lead', 1, '2024-01-05T00:00:00Z'),
			(4, 'lead2@pl.test', '구매부팀장', 'manager', 'team_lead', 1, '2024-01-05T00:00:00Z'),
			(5, 'head@pl.test', '구매부서장', 'manager', 'dept_head', 1, '2024-01-05T00:00:00Z'),
			(6, 'staff@pl.test', '구매직원', 'staff', NULL, 1, '2024-01-05T00:00:00Z'),
			(7, 'staff2@pl.test', '영업직원', 'staff', NULL, 3, '2024-01-05T00:00:00Z'),
			(8, 'auditor@pl.test', '감사 담당', 'auditor', NULL, 2, '2024-01-05T00:00:00Z')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	for _, tbl := range []string{"departments", "users"} { // 번호를 직접 넣었으니 PG 시퀀스를 맞춘다
		if err := dbutil.SyncSequence(context.Background(), db, tbl); err != nil {
			t.Fatal(err)
		}
	}
}

type legacyRow struct {
	dept   int64
	pos    string
	role   string
	active int
}

func snapshotUsers(t *testing.T, db *sql.DB) map[int64]legacyRow {
	t.Helper()
	rows, err := db.Query(`SELECT id, department_id, COALESCE(position,''), role, active FROM users`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[int64]legacyRow{}
	for rows.Next() {
		var id int64
		var r legacyRow
		if err := rows.Scan(&id, &r.dept, &r.pos, &r.role, &r.active); err != nil {
			t.Fatal(err)
		}
		out[id] = r
	}
	return out
}

func newSvc(t *testing.T, db *sql.DB, now func() time.Time) *Service {
	t.Helper()
	svc, err := New(Options{DB: db, Txs: contract.SQLTxManager{DB: db}, Audit: audit.New(db), Masker: rbac.Masker{}, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

func fixedNow() time.Time { return time.Date(2026, 3, 10, 3, 0, 0, 0, time.UTC) }

// AC-70: 기존 데이터를 옮기고 다시 계산한 부서·직책·역할이 기존 값과 같다. 두 번째 실행은 아무것도 하지 않는다.
func TestBackfillKeepsLegacyProjection(t *testing.T) {
	db, _ := testDB(t)
	// testDB 가 넣은 임시부서(id 1)는 비운다.
	if _, err := db.Exec(`DELETE FROM departments`); err != nil {
		t.Fatal(err)
	}
	seedLegacy(t, db)
	before := snapshotUsers(t, db)
	svc := newSvc(t, db, fixedNow)
	ctx := context.Background()

	did, err := svc.Backfill(ctx)
	if err != nil || !did {
		t.Fatalf("백필: %v %v", did, err)
	}
	after := snapshotUsers(t, db)
	for id, want := range before {
		if got := after[id]; got != want {
			t.Errorf("사용자 %d: 투영 %+v, 기존 %+v", id, got, want)
		}
	}
	if err := Consistent(ctx, db); err != nil {
		t.Fatal(err)
	}
	// 기존 부서 번호가 조직 번호로 그대로 쓰인다.
	for id, name := range map[int64]string{1: "구매팀", 2: "경영지원팀", 3: "영업팀"} {
		sn, _ := svc.snap(ctx, "2026-03-10")
		if u := sn.Units[id]; u.Name != name || u.Kind != contract.OrgDepartment {
			t.Errorf("부서 %d: %+v", id, u)
		}
	}
	// 팀장마다 팀이 만들어지고 그 팀의 조직장이 된다.
	sn, _ := svc.snap(ctx, "2026-03-10")
	teams := 0
	for _, u := range sn.Units {
		if u.Kind == contract.OrgTeam {
			teams++
			if u.ParentID != 1 || u.HeadUser == 0 {
				t.Errorf("팀 %+v", u)
			}
		}
	}
	if teams != 2 {
		t.Fatalf("팀 수 %d", teams)
	}
	if sn.Units[1].HeadUser != 5 {
		t.Fatalf("구매팀 부서장: %+v", sn.Units[1])
	}
	events := 0
	_ = db.QueryRow(`SELECT COUNT(*) FROM org_events`).Scan(&events)
	did, err = svc.Backfill(ctx)
	if err != nil || did {
		t.Fatalf("두 번째 실행은 아무것도 하지 않아야 함: %v %v", did, err)
	}
	var events2 int
	_ = db.QueryRow(`SELECT COUNT(*) FROM org_events`).Scan(&events2)
	if events2 != events {
		t.Fatalf("이벤트가 늘어남: %d → %d", events, events2)
	}
	// 옮긴 뒤에는 소속·직책을 직접 바꿀 수 없다.
	if _, err := db.Exec(`UPDATE users SET department_id = 2 WHERE id = 6`); err == nil {
		t.Fatal("조직에 옮긴 사용자의 부서를 직접 바꿀 수 있음")
	}
}

// 옮길 것이 없는 새 DB 에서는 아무것도 하지 않고 표지도 남기지 않는다.
func TestBackfillEmptyDatabaseDoesNothing(t *testing.T) {
	db, _ := testDB(t)
	if _, err := db.Exec(`DELETE FROM departments`); err != nil {
		t.Fatal(err)
	}
	svc := newSvc(t, db, fixedNow)
	did, err := svc.Backfill(context.Background())
	if err != nil || did {
		t.Fatalf("%v %v", did, err)
	}
	var n int
	_ = db.QueryRow(`SELECT COUNT(*) FROM org_events`).Scan(&n)
	if n != 0 {
		t.Fatalf("이벤트 %d", n)
	}
}

// 두 프로세스(별도 연결)가 동시에 백필을 불러도 한 번만 옮긴다.
func TestBackfillRunsOnceAcrossProcesses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "erp.db")
	first, err := dbutil.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	if err := migrations.Apply(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	seedLegacy(t, first)

	const procs = 4
	var dbs []*sql.DB
	var svcs []*Service
	for i := 0; i < procs; i++ {
		d, err := dbutil.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		defer d.Close()
		dbs = append(dbs, d)
		svcs = append(svcs, newSvc(t, d, fixedNow))
	}
	var wg sync.WaitGroup
	results := make([]bool, procs)
	errs := make([]error, procs)
	for i := range svcs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = svcs[i].Backfill(context.Background())
		}(i)
	}
	wg.Wait()
	did := 0
	for i := range svcs {
		if errs[i] != nil {
			t.Fatalf("프로세스 %d: %v", i, errs[i])
		}
		if results[i] {
			did++
		}
	}
	if did != 1 {
		t.Fatalf("옮긴 프로세스 수 %d, want 1", did)
	}
	var hires, roots int
	_ = first.QueryRow(`SELECT COUNT(*) FROM org_events WHERE kind = 'hire'`).Scan(&hires)
	_ = first.QueryRow(`SELECT COUNT(*) FROM org_units WHERE code = 'ROOT'`).Scan(&roots)
	if hires != 8 || roots != 1 {
		t.Fatalf("입사 %d(want 8), 루트 %d(want 1)", hires, roots)
	}
	if err := Consistent(context.Background(), first); err != nil {
		t.Fatal(err)
	}
}

// erp-mcp 처럼 백필 전 DB 에 먼저 붙은 프로세스는 기존 사용자를 그대로 다루고, 백필이 끝나면 다음 조회부터 조직을 본다.
func TestAttachBeforeBackfillThenSeesOrg(t *testing.T) {
	path := filepath.Join(t.TempDir(), "erp.db")
	mcpDB, err := dbutil.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer mcpDB.Close()
	if err := migrations.Apply(context.Background(), mcpDB); err != nil {
		t.Fatal(err)
	}
	seedLegacy(t, mcpDB)
	mcp := newSvc(t, mcpDB, fixedNow)
	ctx := context.Background()

	// 백필 전: 조직 조회는 비어 있고, 투영 갱신은 기존 사용자를 건드리지 않는다.
	if sn, err := mcp.snap(ctx, "2026-03-10"); err != nil || len(sn.Units) != 0 || len(sn.People) != 0 {
		t.Fatalf("백필 전 조직: %v %+v", err, sn)
	}
	before := snapshotUsers(t, mcpDB)
	if n, err := mcp.RefreshProjection(ctx); err != nil || n != 0 {
		t.Fatalf("백필 전 갱신: %d %v", n, err)
	}
	for id, want := range before {
		if got := snapshotUsers(t, mcpDB)[id]; got != want {
			t.Fatalf("백필 전 갱신이 사용자 %d 를 바꿈: %+v → %+v", id, want, got)
		}
	}

	// 서버(다른 연결)가 백필한다.
	serverDB, err := dbutil.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer serverDB.Close()
	server := newSvc(t, serverDB, fixedNow)
	if did, err := server.Backfill(ctx); err != nil || !did {
		t.Fatalf("서버 백필: %v %v", did, err)
	}

	// erp-mcp 프로세스의 서비스는 다시 시작하지 않아도 새 조직을 본다(캐시 키 = 이벤트 최대 번호).
	sn, err := mcp.snap(ctx, "2026-03-10")
	if err != nil || len(sn.Units) == 0 || len(sn.People) != 8 {
		t.Fatalf("백필 뒤 조직: %v units=%d people=%d", err, len(sn.Units), len(sn.People))
	}
	// 백필이 이미 끝났으므로 erp-mcp 쪽 백필 호출은 아무것도 하지 않는다.
	if did, err := mcp.Backfill(ctx); err != nil || did {
		t.Fatalf("이미 끝난 백필: %v %v", did, err)
	}
}

// 계정 생성 시각은 UTC 로 저장된다. 백필의 효력일은 KST 날짜여야 한다(UTC 날짜를 쓰면 KST 00:00~09:00 에 만든 계정이 전날로 보인다).
func TestBackfillUsesKSTDateOfCreation(t *testing.T) {
	for in, want := range map[string]contract.Day{
		"2026-11-10T20:30:00Z":        "2026-11-11", // UTC 로는 전날, KST 로는 05:30
		"2026-11-10T14:59:59Z":        "2026-11-10", // KST 23:59:59
		"2026-11-10T15:00:00Z":        "2026-11-11", // KST 00:00:00
		"2026-11-10T20:30:00.123456Z": "2026-11-11",
		"2026-11-10 20:30:00":         "2026-11-11",
	} {
		if got := createdDay(in, "1999-01-01"); got != want {
			t.Errorf("%s → %s, want %s", in, got, want)
		}
	}
	if createdDay("모름", "1999-01-01") != "1999-01-01" {
		t.Error("읽을 수 없으면 대체 날짜")
	}
	db, _ := testDB(t)
	if _, err := db.Exec(`DELETE FROM departments`); err != nil {
		t.Fatal(err)
	}
	seedLegacy(t, db)
	if _, err := db.Exec(`UPDATE users SET created_at = '2026-11-10T20:30:00Z'`); err != nil {
		t.Fatal(err)
	}
	svc := newSvc(t, db, func() time.Time { return time.Date(2026, 11, 10, 21, 0, 0, 0, time.UTC) }) // KST 2026-11-11 06:00
	if did, err := svc.Backfill(context.Background()); err != nil || !did {
		t.Fatalf("%v %v", did, err)
	}
	var bad int
	_ = db.QueryRow(`SELECT COUNT(*) FROM org_events WHERE effective_date <> '2026-11-11'`).Scan(&bad)
	if bad != 0 {
		t.Fatalf("KST 날짜가 아닌 효력일 %d건", bad)
	}
	// 같은 시각의 오늘 판정과 효력일이 같은 날이라 오늘 기준 조직도에 모두 보인다.
	sn, _ := svc.snap(context.Background(), svc.Today())
	if len(sn.People) != 8 {
		t.Fatalf("오늘 기준 재직자 %d명(8명이어야 함, 효력일이 하루 어긋나면 0명)", len(sn.People))
	}
}

// 백필은 이벤트 건수를 상세에 담은 org.projection_apply 한 건만 감사로 남긴다(개별 발령 감사는 없다).
func TestBackfillWritesSingleAuditRowWithEventCount(t *testing.T) {
	db, _ := testDB(t)
	if _, err := db.Exec(`DELETE FROM departments`); err != nil {
		t.Fatal(err)
	}
	seedLegacy(t, db)
	svc := newSvc(t, db, fixedNow)
	if did, err := svc.Backfill(context.Background()); err != nil || !did {
		t.Fatalf("%v %v", did, err)
	}
	var events, rows int
	var detail string
	_ = db.QueryRow(`SELECT COUNT(*) FROM org_events`).Scan(&events)
	_ = db.QueryRow(`SELECT COUNT(*) FROM audit_logs WHERE action LIKE 'org.%' OR action LIKE 'hr.%'`).Scan(&rows)
	_ = db.QueryRow(`SELECT detail FROM audit_logs WHERE action = 'org.projection_apply'`).Scan(&detail)
	if rows != 1 {
		t.Fatalf("백필 감사 %d건(1건이어야 함)", rows)
	}
	if want := fmt.Sprintf("발령 이벤트 %d건", events); !strings.Contains(detail, want) {
		t.Fatalf("상세 %q 에 %q 가 없음", detail, want)
	}
}
