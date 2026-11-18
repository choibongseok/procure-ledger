package module_test

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/contract/fake"
	"github.com/choibongseok/procure-ledger/internal/dbtest"
	"github.com/choibongseok/procure-ledger/internal/module"
	"github.com/choibongseok/procure-ledger/migrations"
)

// B50 4절: 모듈 켜기·끄기. 기본은 꺼짐, 권한·점검표를 지나야 켜지고, 꺼진 모듈의 라우트는 권한이 있어도 404 다. 변경은 감사에 이전·새 값으로 남는다.

type env struct {
	t     *testing.T
	db    *sql.DB
	rec   *fake.Recorder
	svc   *module.Service
	now   time.Time
	admin contract.Principal
}

func newEnv(t *testing.T) *env {
	t.Helper()
	db := dbtest.Open(t)
	if err := migrations.Apply(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	e := &env{t: t, db: db, rec: fake.NewRecorder(), now: time.Date(2026, 11, 14, 1, 0, 0, 0, time.UTC)}
	e.svc = module.New(module.Options{DB: db, Txs: contract.SQLTxManager{DB: db}, Audit: e.rec, Now: func() time.Time { return e.now }})
	e.admin = contract.Principal{UserID: 1, Name: "관리자", Role: contract.RoleAdmin, Channel: contract.ChannelWeb}
	return e
}

func (e *env) ctx(p contract.Principal) context.Context {
	return contract.WithPrincipal(context.Background(), p)
}

func (e *env) exec(q string, args ...any) {
	e.t.Helper()
	if _, err := e.db.Exec(q, args...); err != nil {
		e.t.Fatalf("%s: %v", q, err)
	}
}

func (e *env) ready() {
	e.exec(`INSERT INTO holidays (date, name, created_at) VALUES ('2026-12-25', '성탄절', 't'), ('2027-01-01', '신정', 't')`)
	e.policy()
}

// policy 는 연차 규칙(점검표 필수 항목)을 한 건 저장한다.
func (e *env) policy() {
	e.exec(`INSERT INTO leave_policies (entity_id, effective_from, basis, workday_minutes, hour_unit_minutes, attendance_rule, carry_max_minutes, carry_valid_months, expire_months, first_year_mode, created_at)
		VALUES (1, '2026-01-01', 'hire', 480, 60, 1, 0, 12, 12, 'monthly', 't')`)
}

func TestModuleDefaultsOffAndNeedsPermission(t *testing.T) {
	e := newEnv(t)
	if e.svc.Enabled(context.Background(), module.KeyAttendance) {
		t.Fatal("기본은 꺼짐이어야 함")
	}
	staff := contract.Principal{UserID: 2, Name: "직원", Role: contract.RoleStaff, Channel: contract.ChannelWeb}
	for name, p := range map[string]contract.Principal{"직원": staff, "감사자": {UserID: 3, Role: contract.RoleAuditor, Channel: contract.ChannelWeb}} {
		if _, err := e.svc.List(e.ctx(p)); !errors.Is(err, contract.ErrForbidden) {
			t.Errorf("%s 목록: %v", name, err)
		}
		if err := e.svc.SetEnabled(e.ctx(p), module.KeyAttendance, true); !errors.Is(err, contract.ErrForbidden) {
			t.Errorf("%s 켜기: %v", name, err)
		}
		if _, err := e.svc.Checklist(e.ctx(p), module.KeyAttendance); !errors.Is(err, contract.ErrForbidden) {
			t.Errorf("%s 점검표: %v", name, err)
		}
	}
	if err := e.svc.SetEnabled(context.Background(), module.KeyAttendance, true); !errors.Is(err, contract.ErrUnauthenticated) {
		t.Fatalf("로그인 없이 켜기: %v", err)
	}
	if err := e.svc.SetEnabled(e.ctx(e.admin), "nope", true); !errors.Is(err, contract.ErrNotFound) {
		t.Fatalf("모르는 모듈: %v", err)
	}
	vs, err := e.svc.List(e.ctx(e.admin))
	if err != nil || len(vs) != 1 || vs[0].Key != module.KeyAttendance || vs[0].Enabled {
		t.Fatalf("관리자 목록: %+v %v", vs, err)
	}
}

func TestModuleChecklistBlocksEnableUntilRequiredItemsPass(t *testing.T) {
	e := newEnv(t)
	ctx := e.ctx(e.admin)
	err := e.svc.SetEnabled(ctx, module.KeyAttendance, true)
	if !errors.Is(err, contract.ErrInvalidState) || !strings.Contains(err.Error(), "2026년 공휴일 등록") || !strings.Contains(err.Error(), "2027년 공휴일 등록") {
		t.Fatalf("공휴일이 없는데 켜짐: %v", err)
	}
	if e.svc.Enabled(ctx, module.KeyAttendance) {
		t.Fatal("거부됐는데 켜져 있음")
	}
	// 규칙이 없으면 막힌다. 그다음 조직에 등록되지 않은 재직 사용자가 있으면 막힌다.
	e.exec(`INSERT INTO holidays (date, name, created_at) VALUES ('2026-12-25', '성탄절', 't'), ('2027-01-01', '신정', 't')`)
	if err := e.svc.SetEnabled(ctx, module.KeyAttendance, true); err == nil || !strings.Contains(err.Error(), "연차 규칙") {
		t.Fatalf("규칙이 없는데 켜짐: %v", err)
	}
	e.policy()
	e.exec(`INSERT INTO departments (name) VALUES ('운영')`)
	e.exec(`INSERT INTO users (email, name, role, department_id) VALUES ('u@x.test', '미등록', 'staff', (SELECT id FROM departments WHERE name = '운영'))`)
	if err := e.svc.SetEnabled(ctx, module.KeyAttendance, true); err == nil || !strings.Contains(err.Error(), "조직 등록") {
		t.Fatalf("조직 미등록 사용자가 있는데 켜짐: %v", err)
	}
	cs, err := e.svc.Checklist(ctx, module.KeyAttendance)
	if err != nil || len(cs) != 4 {
		t.Fatalf("점검표: %+v %v", cs, err)
	}
	for _, c := range cs {
		if !c.Required {
			t.Errorf("기본 항목은 필수여야 함: %+v", c)
		}
	}
	var bad int
	for _, c := range cs {
		if !c.OK {
			bad++
		}
	}
	if bad != 1 {
		t.Fatalf("통과하지 못한 항목 %d (조직 등록 하나여야 함): %+v", bad, cs)
	}
	// 비활성 사용자는 센 대상이 아니다.
	e.exec(`UPDATE users SET active = 0 WHERE email = 'u@x.test'`)
	if err := e.svc.SetEnabled(ctx, module.KeyAttendance, true); err != nil {
		t.Fatalf("점검표를 통과했는데 거부: %v", err)
	}
}

func TestModuleSetEnabledAuditsOnceAndIsIdempotent(t *testing.T) {
	e := newEnv(t)
	e.ready()
	ctx := e.ctx(e.admin)
	if err := e.svc.SetEnabled(ctx, module.KeyAttendance, true); err != nil {
		t.Fatal(err)
	}
	if !e.svc.Enabled(ctx, module.KeyAttendance) {
		t.Fatal("켜지지 않음")
	}
	if err := e.svc.SetEnabled(ctx, module.KeyAttendance, true); err != nil { // 같은 상태: 아무 일도 하지 않는다
		t.Fatal(err)
	}
	if err := e.svc.SetEnabled(ctx, module.KeyAttendance, false); err != nil {
		t.Fatal(err)
	}
	if e.svc.Enabled(ctx, module.KeyAttendance) {
		t.Fatal("꺼지지 않음")
	}
	n := 0
	for _, en := range e.rec.Records() {
		if en.Action != module.ActionSet {
			continue
		}
		n++
		if en.TargetType != audit.TargetModule || en.TargetID != module.KeyAttendance || len(en.Diff) != 1 || en.Diff[0].Key != "enabled" {
			t.Fatalf("감사 항목: %+v", en)
		}
	}
	if n != 2 {
		t.Fatalf("감사 %d건(켜기 1 + 끄기 1 이어야 함)", n)
	}
	last := e.rec.Records()[len(e.rec.Records())-1]
	if last.Diff[0].Old != "켜짐" || last.Diff[0].New != "꺼짐" {
		t.Fatalf("끄기 감사 값: %+v", last.Diff)
	}
	// 끄는 것은 점검표와 무관하게 항상 된다(공휴일을 지워도).
	e.exec(`DELETE FROM holidays WHERE date = '2027-01-01'`) // 미래 날짜라 트리거가 허용
	if err := e.svc.SetEnabled(ctx, module.KeyAttendance, true); err == nil {
		t.Fatal("공휴일이 부족한데 켜짐")
	}
}

func TestModuleGateReturns404WhenOffEvenForAdmin(t *testing.T) {
	e := newEnv(t)
	e.ready()
	called := 0
	h := e.svc.Gate(module.KeyAttendance, func(w http.ResponseWriter, r *http.Request) { called++; w.WriteHeader(http.StatusOK) })
	do := func() int {
		r := httptest.NewRequest("GET", "/x", nil).WithContext(e.ctx(e.admin))
		w := httptest.NewRecorder()
		h(w, r)
		return w.Code
	}
	if code := do(); code != http.StatusNotFound || called != 0 {
		t.Fatalf("꺼진 모듈: %d 호출 %d", code, called)
	}
	if err := e.svc.SetEnabled(e.ctx(e.admin), module.KeyAttendance, true); err != nil {
		t.Fatal(err)
	}
	if code := do(); code != http.StatusOK || called != 1 {
		t.Fatalf("켜진 모듈: %d 호출 %d", code, called)
	}
	// 읽기 실패는 꺼짐으로 본다(fail-closed).
	e.exec(`UPDATE app_settings SET value = '켜' WHERE key = 'module.attendance'`)
	if e.svc.Enabled(context.Background(), module.KeyAttendance) {
		t.Fatal("알 수 없는 값은 꺼짐이어야 함")
	}
}

func TestModuleDefinitionsAreDeclaredWithRiskPermission(t *testing.T) {
	for _, d := range module.Defs() {
		if d.Key == "" || d.Name == "" || d.Permission == "" {
			t.Errorf("선언 누락: %+v", d)
		}
		found := false
		for _, hp := range contract.HighRiskPermissions {
			if hp == d.Permission {
				found = true
			}
		}
		if !found {
			t.Errorf("모듈 %s 의 켜기 권한 %s 가 위험 권한 목록에 없음", d.Key, d.Permission)
		}
	}
	if module.HRRole.Name == "" || len(module.HRRole.Permissions) == 0 {
		t.Fatal("인사 담당 템플릿이 비어 있음")
	}
	known := map[contract.Permission]bool{}
	for _, p := range contract.AllPermissions {
		known[p] = true
	}
	for _, p := range module.HRRole.Permissions {
		if !known[p] {
			t.Errorf("템플릿 권한 %s 가 권한 표에 없음", p)
		}
	}
}
