package onboarding

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/choibongseok/procure-ledger/internal/access"
	"github.com/choibongseok/procure-ledger/internal/audit"
	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/dbtest"
	"github.com/choibongseok/procure-ledger/internal/dbutil"
	"github.com/choibongseok/procure-ledger/migrations"
)

type env struct {
	db    *sql.DB
	svc   *Service
	clock *time.Time
	admin contract.Principal
	ctx   context.Context
}

func newEnv(t *testing.T, e Env) *env {
	t.Helper()
	ctx := context.Background()
	db := dbtest.Open(t)
	if err := migrations.Apply(ctx, db); err != nil {
		t.Fatal(err)
	}
	if _, err := access.SyncTemplates(ctx, db); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC)
	x := &env{db: db, clock: &now}
	txs := dbutil.NewTxManager(db, dbutil.DialectOf(db))
	x.svc = New(Options{DB: db, Txs: txs, Audit: audit.New(db), Env: e, Now: func() time.Time { return *x.clock }})
	var uid int64
	if _, err := db.Exec(`INSERT INTO departments (name) VALUES ('운영') ON CONFLICT DO NOTHING`); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`INSERT INTO users (email, name, role, department_id) VALUES ('admin@x.test', '관리자', 'admin', (SELECT id FROM departments WHERE name = '운영')) RETURNING id`).Scan(&uid); err != nil {
		t.Fatal(err)
	}
	x.admin = contract.Principal{UserID: uid, Name: "관리자", Role: contract.RoleAdmin, Channel: contract.ChannelWeb, Auth: contract.AuthSession}
	x.ctx = contract.WithPrincipal(ctx, x.admin)
	return x
}

func (x *env) item(t *testing.T, id string) Item {
	t.Helper()
	items, _, err := x.svc.List(x.ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range items {
		if it.ID == id {
			return it
		}
	}
	t.Fatalf("항목 %q 가 없음", id)
	return Item{}
}

func (x *env) exec(t *testing.T, q string, args ...any) {
	t.Helper()
	if _, err := x.db.Exec(q, args...); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}

// AC-1810: 21개 항목(B49 AI 비용 배부, B48 AI 도구, B46 평가 세트, B45 LLM 공급자, B40 계정 동기화, B41 개인정보 기록·권한 분리, B42 연동 API, B43 전자세금계산서, B44 법인·사업장 추가)이 DB 상태로 계산되고, 시험 데이터로 완료/미완 전환이 보인다.
func TestTwelveItemsComputedFromDB(t *testing.T) {
	x := newEnv(t, Env{})
	items, sum, err := x.svc.List(x.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 21 || sum.Total != 21 || Count() != 21 {
		t.Fatalf("항목 %d개(21개여야 함) %+v", len(items), sum)
	}
	seen := map[string]bool{}
	for _, it := range items {
		if seen[it.ID] {
			t.Errorf("항목 ID 중복 %s", it.ID)
		}
		seen[it.ID] = true
		if it.State == StateUnknown {
			t.Errorf("%s: 확인하지 못함(%s)", it.ID, it.Evidence)
		}
		if it.Title == "" || it.Evidence == "" {
			t.Errorf("%s: 제목·근거가 비어 있음", it.ID)
		}
	}
	// 새 DB: 회사 정보·조직·백업은 미착수, 시스템 계정과목은 마이그레이션 시드로 완료.
	for id, want := range map[string]State{"company": StateTodo, "org": StateTodo, "backup": StateTodo, "accounts": StateDone, "keys": StateTodo, "notifications": StateTodo} {
		if got := x.item(t, id).State; got != want {
			t.Errorf("새 DB 의 %s: %s(기대 %s)", id, got, want)
		}
	}

	// 회사 정보: 일부만 → 진행 중, 필수 3개 + 로고 → 완료.
	x.exec(t, `INSERT INTO company_settings (id, name, business_no, representative, address, phone, version, updated_at) VALUES (1, '에이씨엠이', '', '', '', '', 1, '2026-11-13T00:00:00Z')`)
	if got := x.item(t, "company").State; got != StateInProgress {
		t.Errorf("회사명만: %s", got)
	}
	x.exec(t, `UPDATE company_settings SET business_no = '123-45-67890', representative = '김대표'`)
	if got := x.item(t, "company").State; got != StateInProgress {
		t.Errorf("로고 없이 3개: %s", got)
	}
	x.exec(t, `INSERT INTO company_assets (kind, content, content_type, size, sha256, width, height, uploaded_at) VALUES ('logo', ?, 'image/png', 1, 'x', 1, 1, '2026-11-13T00:00:00Z')`, []byte{1})
	if got := x.item(t, "company").State; got != StateDone {
		t.Errorf("필수 3개 + 로고: %s", got)
	}

	// 계정과목: 시스템 계정은 중지할 수 없으므로(트리거) 마이그레이션 시드로 항상 완료다.
	if it := x.item(t, "accounts"); it.State != StateDone || !strings.Contains(it.Evidence, "5/5") {
		t.Errorf("계정과목: %s %s", it.State, it.Evidence)
	}

	// 사용자·역할: 비상 관리자·재무·활성 2명이 갖춰져야 완료.
	if got := x.item(t, "users").State; got != StateInProgress {
		t.Errorf("관리자 1명뿐: %s", got)
	}
	x.exec(t, `INSERT INTO users (email, name, role, department_id) VALUES ('fin@x.test', '재무', 'finance', (SELECT id FROM departments WHERE name = '운영'))`)
	x.exec(t, `INSERT INTO auth_policy (id, config_json, updated_at) VALUES (1, ?, '2026-11-13T00:00:00Z') ON CONFLICT(id) DO UPDATE SET config_json = excluded.config_json`, `{"emergencyAdmins":[`+itoa(x.admin.UserID)+`]}`)
	if got := x.item(t, "users").State; got != StateDone {
		t.Errorf("활성 2명·관리자·재무·비상 관리자: %s", got)
	}

	// 양식: 게시본이 없는 사용 중 양식이 있으면 진행 중.
	forms := x.item(t, "forms")
	if forms.State != StateDone {
		t.Fatalf("기본 양식은 게시본이 있어야 함: %s %s", forms.State, forms.Evidence)
	}
	x.exec(t, `INSERT INTO approval_forms (code, category, name, kind, active, created_at) VALUES ('ZZTEST', 'general', '시험 양식', 'general', 1, '2026-11-13T00:00:00Z')`)
	if got := x.item(t, "forms").State; got != StateInProgress {
		t.Errorf("게시본 없는 양식: %s", got)
	}

	// 가져오기: 대상 7종 중 반영한 수만큼.
	x.exec(t, `INSERT INTO import_batches (no, target, status, file_sha256, file_size, row_count, counts_json, plan_sha256, created_by, created_at, applied_at)
		VALUES ('IMP-2026-0001', 'account', 'applied', ?, 1, 1, '{}', 'p', ?, '2026-11-13T00:00:00Z', '2026-11-13T00:00:00Z')`, strings.Repeat("a", 64), x.admin.UserID)
	if it := x.item(t, "opening_data"); it.State != StateInProgress || !strings.Contains(it.Evidence, "1종") {
		t.Errorf("기초 데이터: %s %s", it.State, it.Evidence)
	}

	// 키·로그인 보안·앵커·알림은 환경의 있음/없음으로 계산한다.
	y := newEnv(t, Env{HTTPS: true, CookieSecure: true, AnchorConfigured: true, AccountKey: true, AuthKey: true, MailConfigured: true})
	for id, want := range map[string]State{"keys": StateDone, "notifications": StateInProgress, "audit_anchor": StateInProgress, "login_security": StateInProgress} {
		if got := y.item(t, id).State; got != want {
			t.Errorf("환경 설정 시 %s: %s(기대 %s)", id, got, want)
		}
	}
}

func itoa(n int64) string { b, _ := json.Marshal(n); return string(b) }

// 백업 항목: 지난 7일 안의 성공 백업 + 30일 안의 성공 리허설이 모두 있어야 완료다(backup_runs).
func TestBackupItemUsesBackupRuns(t *testing.T) {
	x := newEnv(t, Env{})
	run := func(kind, result string, ago time.Duration) {
		t.Helper()
		ts := x.clock.Add(-ago).UTC().Format(time.RFC3339)
		x.exec(t, `INSERT INTO backup_runs (kind, started_at, finished_at, result) VALUES (?, ?, ?, ?)`, kind, ts, ts, result)
	}
	if got := x.item(t, "backup").State; got != StateTodo {
		t.Fatalf("기록 없음: %s", got)
	}
	run("backup", "success", 3*24*time.Hour)
	if got := x.item(t, "backup").State; got != StateInProgress {
		t.Errorf("백업만: %s", got)
	}
	run("rehearsal", "success", 10*24*time.Hour)
	if got := x.item(t, "backup").State; got != StateDone {
		t.Errorf("백업 3일 + 리허설 10일: %s", got)
	}
	run("backup", "failed", time.Hour) // 실패 기록은 성공을 대신하지 않는다
	if got := x.item(t, "backup").State; got != StateDone {
		t.Errorf("뒤의 실패 기록이 성공 기록을 가림: %s", got)
	}
	*x.clock = x.clock.Add(5 * 24 * time.Hour) // 백업이 8일 전이 된다
	if got := x.item(t, "backup").State; got != StateInProgress {
		t.Errorf("백업이 8일 전: %s", got)
	}
}

// "해당 없음": 표시할 수 있는 항목만, 사유가 있어야 하고, 감사에 남으며, 해제도 남는다. 완료된 항목은 완료로 보인다.
func TestSkipRulesAndAudit(t *testing.T) {
	x := newEnv(t, Env{})
	if err := x.svc.MarkNotApplicable(x.ctx, "company", "필요 없음"); !errors.Is(err, contract.ErrValidation) {
		t.Errorf("표시할 수 없는 항목: %v", err)
	}
	if err := x.svc.MarkNotApplicable(x.ctx, "nope", "x"); !errors.Is(err, contract.ErrNotFound) {
		t.Errorf("없는 항목: %v", err)
	}
	for _, bad := range []string{"", "   ", strings.Repeat("가", 201), "줄\n바꿈"} {
		if err := x.svc.MarkNotApplicable(x.ctx, "opening_data", bad); !errors.Is(err, contract.ErrValidation) {
			t.Errorf("사유 %q: %v", bad, err)
		}
	}
	if err := x.svc.MarkNotApplicable(x.ctx, "opening_data", "처음부터 새로 시작하는 회사"); err != nil {
		t.Fatal(err)
	}
	it := x.item(t, "opening_data")
	if it.State != StateNA || it.Skip == nil || it.Skip.Reason != "처음부터 새로 시작하는 회사" || it.Skip.By != x.admin.UserID {
		t.Fatalf("표시 뒤: %+v", it)
	}
	_, sum, _ := x.svc.List(x.ctx)
	if sum.NA != 1 {
		t.Errorf("요약 %+v", sum)
	}
	var n int
	_ = x.db.QueryRow(`SELECT COUNT(*) FROM audit_logs WHERE action = 'onboarding.skip' AND target_id = 'opening_data' AND actor_id = ?`, x.admin.UserID).Scan(&n)
	if n != 1 {
		t.Errorf("skip 감사 %d건", n)
	}
	// 해제: 감사 1건, 두 번째 해제는 아무 일도 하지 않는다.
	if err := x.svc.ClearNotApplicable(x.ctx, "opening_data"); err != nil {
		t.Fatal(err)
	}
	if err := x.svc.ClearNotApplicable(x.ctx, "opening_data"); err != nil {
		t.Fatal(err)
	}
	_ = x.db.QueryRow(`SELECT COUNT(*) FROM audit_logs WHERE action = 'onboarding.unskip'`).Scan(&n)
	if n != 1 {
		t.Errorf("unskip 감사 %d건(1건이어야 함)", n)
	}
	if got := x.item(t, "opening_data").State; got == StateNA {
		t.Error("해제했는데 해당 없음으로 남음")
	}
	if res, err := audit.New(x.db).Verify(context.Background()); err != nil || !res.ChainOK {
		t.Fatalf("체인: %+v %v", res, err)
	}
}

// 권한: onboarding:read 가 없는 역할·로그인하지 않은 호출은 점검표·표시·해제 모두 거부된다. 백업 상태는 backup:read(관리자·감사).
func TestPermissions(t *testing.T) {
	x := newEnv(t, Env{})
	for role, wantOnboarding := range map[contract.Role]bool{contract.RoleAdmin: true, contract.RoleFinance: false, contract.RoleManager: false, contract.RoleStaff: false, contract.RoleAuditor: false} {
		ctx := contract.WithPrincipal(context.Background(), contract.Principal{UserID: 99, Role: role, Channel: contract.ChannelWeb})
		_, _, err := x.svc.List(ctx)
		if (err == nil) != wantOnboarding {
			t.Errorf("%s 의 점검표: %v", role, err)
		}
		if wantOnboarding {
			continue
		}
		if err := x.svc.MarkNotApplicable(ctx, "opening_data", "사유"); !errors.Is(err, contract.ErrForbidden) {
			t.Errorf("%s 의 표시: %v", role, err)
		}
		if err := x.svc.ClearNotApplicable(ctx, "opening_data"); !errors.Is(err, contract.ErrForbidden) {
			t.Errorf("%s 의 해제: %v", role, err)
		}
	}
	for role, want := range map[contract.Role]bool{contract.RoleAdmin: true, contract.RoleAuditor: true, contract.RoleFinance: false, contract.RoleManager: false, contract.RoleStaff: false} {
		ctx := contract.WithPrincipal(context.Background(), contract.Principal{UserID: 99, Role: role, Channel: contract.ChannelWeb})
		if _, err := x.svc.BackupStatus(ctx); (err == nil) != want {
			t.Errorf("%s 의 백업 상태: %v", role, err)
		}
	}
	if _, _, err := x.svc.List(context.Background()); !errors.Is(err, contract.ErrUnauthenticated) {
		t.Errorf("미인증: %v", err)
	}
}

// 개별 항목 질의가 실패하면 그 항목만 "확인하지 못했습니다"이고 나머지는 계산된다(SQLite 에서 표를 지워 질의를 깨뜨린다).
func TestOneFailingItemDoesNotHideOthers(t *testing.T) {
	if dbtest.PG() {
		t.Skip("표를 지워 질의를 깨뜨리는 시험은 SQLite 에서만 한다(PG 는 외래 키·의존 표가 있어 지울 수 없다)")
	}
	x := newEnv(t, Env{})
	x.exec(t, `DROP TABLE import_batches`)
	items, _, err := x.svc.List(x.ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range items {
		switch it.ID {
		case "opening_data":
			if it.State != StateUnknown || it.Evidence != "확인하지 못했습니다" {
				t.Errorf("깨진 항목: %+v", it)
			}
		default:
			if it.State == StateUnknown {
				t.Errorf("%s 까지 확인하지 못함으로 번졌다", it.ID)
			}
		}
	}
}

// 판정은 비밀값을 읽지 않는다: 응답 어디에도 키·경로·주소 값이 없고 있음/없음만 있다.
func TestNoSecretsInOutput(t *testing.T) {
	x := newEnv(t, Env{AccountKey: true, AuthKey: true, AnchorConfigured: true, HTTPS: true})
	items, _, err := x.svc.List(x.ctx)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(items)
	for _, bad := range []string{"PL_", "secret", "SECRET", "/data/", "http://", "https://", "password", "argon2id"} {
		if strings.Contains(string(b), bad) {
			t.Errorf("점검표 응답에 %q 가 있음", bad)
		}
	}
}

// 백업 상태: 종류별 마지막 성공·그 뒤의 실패·최근 기록. 읽기 전용이라 감사나 기록을 남기지 않는다.
func TestBackupStatus(t *testing.T) {
	x := newEnv(t, Env{})
	st, err := x.svc.BackupStatus(x.ctx)
	if err != nil || !st.Available || len(st.Last) != 0 || len(st.Recent) != 0 {
		t.Fatalf("기록 없음: %+v %v", st, err)
	}
	ins := func(kind, result, set, errCode string, bytes int64) {
		t.Helper()
		x.exec(t, `INSERT INTO backup_runs (kind, started_at, finished_at, result, set_name, db_kind, bytes, chain_seq, error_code) VALUES (?, '2026-11-12T00:00:00Z', '2026-11-12T00:01:00Z', ?, ?, 'sqlite', ?, 42, ?)`, kind, result, set, bytes, errCode)
	}
	ins("backup", "success", "pl-20261110T000000Z", "", 1000)
	ins("rehearsal", "success", "pl-20261110T000000Z", "", 0)
	ins("backup", "failed", "", "disk_full", 0)
	var before int
	_ = x.db.QueryRow(`SELECT COUNT(*) FROM audit_logs`).Scan(&before)
	st, err = x.svc.BackupStatus(x.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if st.Last["backup"].SetName != "pl-20261110T000000Z" || st.Last["backup"].Bytes != 1000 || st.Last["rehearsal"].Result != "success" {
		t.Errorf("마지막 성공: %+v", st.Last)
	}
	if f, ok := st.LastFailed["backup"]; !ok || f.ErrorCode != "disk_full" {
		t.Errorf("마지막 성공 뒤의 실패: %+v", st.LastFailed)
	}
	if _, ok := st.LastFailed["rehearsal"]; ok {
		t.Error("실패하지 않은 종류에 실패가 표시됨")
	}
	if len(st.Recent) != 3 || st.Recent[0].Result != "failed" {
		t.Errorf("최근 기록: %+v", st.Recent)
	}
	var after int
	_ = x.db.QueryRow(`SELECT COUNT(*) FROM audit_logs`).Scan(&after)
	if before != after {
		t.Error("조회가 감사를 남김")
	}
	b, _ := json.Marshal(st)
	if strings.Contains(string(b), "password") || strings.Contains(string(b), "host") {
		t.Errorf("백업 상태 응답에 비밀·호스트가 있음: %s", b)
	}
}
