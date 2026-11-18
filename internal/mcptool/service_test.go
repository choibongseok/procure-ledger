package mcptool_test

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/fake"
	"github.com/choibongseok/procure-ledger/internal/dbtest"
	"github.com/choibongseok/procure-ledger/internal/mcptool"
	"github.com/choibongseok/procure-ledger/internal/rbac"
	"github.com/choibongseok/procure-ledger/migrations"
)

type env struct {
	t   *testing.T
	db  *sql.DB
	rec *fake.Recorder
	cat *mcptool.Catalog
	fr  *fakeRes
	svc *mcptool.Service
}

func newEnv(t *testing.T) *env {
	t.Helper()
	db := dbtest.Open(t)
	if err := migrations.Apply(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO departments (id, name) VALUES (900, '운영')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO users (id, email, name, role, department_id) VALUES (1, 'admin@pl.test', '관리자', 'admin', 900)`); err != nil {
		t.Fatal(err)
	}
	e := &env{t: t, db: db, rec: fake.NewRecorder()}
	e.cat, e.fr = newFake()
	e.svc = e.mk(e.cat)
	return e
}

func (e *env) mk(cat *mcptool.Catalog) *mcptool.Service {
	svc, err := mcptool.New(mcptool.Options{DB: e.db, Txs: contract.SQLTxManager{DB: e.db}, Audit: e.rec, Catalog: cat, Masker: rbac.Masker{},
		Now: func() time.Time { return time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC) }})
	if err != nil {
		e.t.Fatal(err)
	}
	return svc
}

func as(perms ...contract.Permission) context.Context {
	return contract.WithPrincipal(context.Background(), contract.Principal{UserID: 1, Role: contract.RoleAdmin, DepartmentID: 900, Channel: contract.ChannelWeb, Auth: contract.AuthSession, Perms: contract.NewPermSet(perms...)})
}

var (
	manage = as(contract.PermMCPToolRead, contract.PermMCPToolManage)
	reader = as(contract.PermMCPToolRead)
	nobody = as()
)

func (e *env) save(m map[string]any, id int64) mcptool.Detail {
	e.t.Helper()
	d, err := e.svc.Save(manage, mcptool.SaveInput{ID: id, Title: "열린 항목", Definition: raw(m), Note: "메모"})
	if err != nil {
		e.t.Fatal(err)
	}
	return d
}

func (e *env) count(q string, args ...any) int {
	var n int
	if err := e.db.QueryRow(q, args...).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

// AC-4809·4811: 저장은 꺼진 새 도구(v1)와 값 없는 감사를 같은 트랜잭션에 남기고, 바뀐 정의만 새 버전이 되며, 이름은 바꿀 수 없다.
func TestSaveCreatesDisabledToolAndVersions(t *testing.T) {
	e := newEnv(t)
	d := e.save(baseDef(), 0)
	if d.CurrentVersion != 1 || d.Enabled || d.Kind != "read" || d.ResourceLabel != "시험 자원" || len(d.Versions) != 1 || d.Broken {
		t.Fatalf("새 도구: %+v", d.ToolView)
	}
	if !strings.HasSuffix(d.ModelDescription, mcptool.ModelNote) || !strings.Contains(string(d.Definition), `"custom_open_things"`) {
		t.Fatalf("상세: %q %s", d.ModelDescription, d.Definition)
	}
	found := false
	for _, r := range e.rec.Records() {
		if r.Action != "mcptool.save" {
			continue
		}
		found = true
		keys := map[string]string{}
		for _, df := range r.Diff {
			keys[df.Key] = df.New
			if strings.Contains(df.New, "시험 항목") {
				t.Fatalf("감사에 설명 원문이 있음: %+v", df)
			}
		}
		if keys["tool_name"] != "custom_open_things" || keys["tool_kind"] != "read" || keys["tool_version"] != "1" || keys["tool_resource"] != "things" {
			t.Fatalf("감사 diff: %+v", r.Diff)
		}
	}
	if !found {
		t.Fatal("mcptool.save 감사가 없음")
	}
	// 같은 정의는 새 버전을 만들지 않는다.
	if _, err := e.svc.Save(manage, mcptool.SaveInput{ID: d.ID, Title: "x", Definition: raw(baseDef())}); !errors.Is(err, contract.ErrInvalidState) {
		t.Fatalf("같은 정의: %v", err)
	}
	m := baseDef()
	m["fields"] = []string{"no", "total"}
	d2 := e.save(m, d.ID)
	if d2.CurrentVersion != 2 || len(d2.Versions) != 2 || d2.Versions[0].Version != 2 || d2.Versions[1].Note != "메모" {
		t.Fatalf("v2: %+v", d2)
	}
	// 이름 변경·중복·잘못된 입력.
	m["name"] = "custom_other_name"
	if _, err := e.svc.Save(manage, mcptool.SaveInput{ID: d.ID, Title: "x", Definition: raw(m)}); !errors.Is(err, contract.ErrValidation) {
		t.Fatalf("이름 변경: %v", err)
	}
	if _, err := e.svc.Save(manage, mcptool.SaveInput{Title: "중복", Definition: raw(baseDef())}); err == nil || !errors.Is(err, contract.ErrValidation) || !strings.Contains(err.Error(), "이미 있는 도구 이름") {
		t.Fatalf("이름 중복: %v", err)
	}
	if _, err := e.svc.Save(manage, mcptool.SaveInput{Title: "", Definition: raw(baseDef())}); !errors.Is(err, contract.ErrValidation) {
		t.Fatalf("이름 없음: %v", err)
	}
	var ve *mcptool.ValidationError
	bad := baseDef()
	bad["resource"] = "users"
	if _, err := e.svc.Save(manage, mcptool.SaveInput{Title: "x", Definition: raw(bad)}); !errors.As(err, &ve) || len(ve.Errors) == 0 {
		t.Fatalf("칸별 오류: %v", err)
	}
	if e.count(`SELECT COUNT(*) FROM mcp_tools`) != 1 {
		t.Fatal("거부된 저장이 도구를 만들었음")
	}
	// 버전은 불변(트리거).
	if _, err := e.db.Exec(`UPDATE mcp_tool_versions SET note = 'x' WHERE version = 1`); err == nil || !strings.Contains(err.Error(), "고칠 수 없습니다") {
		t.Fatalf("버전 수정: %v", err)
	}
	if _, err := e.db.Exec(`DELETE FROM mcp_tool_versions WHERE version = 1`); err == nil {
		t.Fatal("버전 삭제가 통과함")
	}
	if _, err := e.db.Exec(`INSERT INTO mcp_tools (name, title, kind, created_at, updated_at) VALUES ('bad_name_here', 't', 'read', 'x', 'x')`); err == nil {
		t.Fatal("custom_ 접두가 아닌 이름이 통과함")
	}
}

// AC-4809: 되돌리기는 옛 정의를 복사한 새 버전이다.
func TestRollbackCreatesNewVersion(t *testing.T) {
	e := newEnv(t)
	d := e.save(baseDef(), 0)
	m := baseDef()
	m["fields"] = []string{"no"}
	e.save(m, d.ID)
	r, err := e.svc.Rollback(manage, d.ID, 1, "")
	if err != nil || r.CurrentVersion != 3 || len(r.Versions) != 3 || r.Versions[0].Note != "v1 로 되돌림" {
		t.Fatalf("되돌리기: %+v %v", r.ToolView, err)
	}
	if r.Versions[0].DefHash != r.Versions[2].DefHash {
		t.Fatal("되돌린 버전의 정의가 원본과 다름")
	}
	if _, err := e.svc.Rollback(manage, d.ID, 3, ""); !errors.Is(err, contract.ErrInvalidState) {
		t.Fatalf("현재 버전으로 되돌리기: %v", err)
	}
	if _, err := e.svc.Rollback(manage, d.ID, 9, ""); !errors.Is(err, contract.ErrNotFound) {
		t.Fatalf("없는 버전: %v", err)
	}
	if _, err := e.svc.Rollback(manage, 999, 1, ""); !errors.Is(err, contract.ErrNotFound) {
		t.Fatalf("없는 도구: %v", err)
	}
	if !e.rec.Has("mcptool.rollback", "mcp_tool", "success") {
		t.Fatal("mcptool.rollback 감사가 없음")
	}
	// 카탈로그가 바뀌어 옛 정의가 맞지 않으면 되돌릴 수 없다.
	cat2, _ := newFake()
	res, _ := cat2.Get("things")
	res.Fields = res.Fields[1:] // "no" 칸이 사라짐
	if _, err := e.mk(cat2).Rollback(manage, d.ID, 1, ""); !errors.Is(err, contract.ErrInvalidState) || !strings.Contains(err.Error(), "맞지 않아") {
		t.Fatalf("카탈로그와 맞지 않는 되돌리기: %v", err)
	}
}

// AC-4815: 권한 — 읽기 권한만 있으면 조회만 되고 쓰기·시험은 거부, 권한이 없으면 조회도 거부.
func TestPermissions(t *testing.T) {
	e := newEnv(t)
	d := e.save(baseDef(), 0)
	if _, err := e.svc.List(reader); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Get(reader, d.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Catalog(reader); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Save(reader, mcptool.SaveInput{Title: "x", Definition: raw(baseDef())}); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("읽기 권한으로 저장: %v", err)
	}
	if _, err := e.svc.Rollback(reader, d.ID, 1, ""); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("읽기 권한으로 되돌리기: %v", err)
	}
	if _, err := e.svc.Test(reader, mcptool.TestInput{Definition: raw(baseDef())}); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("읽기 권한으로 시험: %v", err)
	}
	for _, f := range []func() error{
		func() error { _, err := e.svc.List(nobody); return err },
		func() error { _, err := e.svc.Get(nobody, d.ID); return err },
		func() error { _, err := e.svc.Catalog(nobody); return err },
	} {
		if err := f(); !errors.Is(err, contract.ErrForbidden) {
			t.Fatalf("권한 없는 조회: %v", err)
		}
	}
	if _, err := e.svc.Get(reader, 999); !errors.Is(err, contract.ErrNotFound) {
		t.Fatalf("없는 도구: %v", err)
	}
}

// AC-4812: 시험 실행은 실행자 본인 권한 기준임을 알리고, 입력·결과 내용은 감사에 없으며 호출 가능 역할이 권한 표와 일치한다.
func TestTestRun(t *testing.T) {
	e := newEnv(t)
	in := mcptool.TestInput{Definition: raw(baseDef()), Input: map[string]any{"status": "open"}}
	out, err := e.svc.Test(manage, in)
	if err != nil {
		t.Fatal(err)
	}
	if out.Count != 2 || !strings.Contains(out.Notice, "내 권한") || out.ElapsedMS < 0 {
		t.Fatalf("결과: %+v", out)
	}
	want := map[string]bool{}
	for role, perms := range contract.RolePermissions {
		for _, p := range perms {
			if p == contract.PermPurchaseRead {
				want[string(role)] = true
			}
		}
	}
	if len(out.CallableRoles) != len(want) {
		t.Fatalf("호출 가능 역할 %v != 권한 표 %v", out.CallableRoles, want)
	}
	for _, r := range out.CallableRoles {
		if !want[r] {
			t.Fatalf("권한 표에 없는 역할 %s", r)
		}
	}
	// 정의의 roles 로 좁히기만 한다.
	m := baseDef()
	m["roles"] = []string{"finance"}
	narrow, err := e.svc.Test(manage, mcptool.TestInput{Definition: raw(m), Input: map[string]any{}})
	if err != nil || len(narrow.CallableRoles) != 1 || narrow.CallableRoles[0] != "finance" {
		t.Fatalf("roles 좁히기: %+v %v", narrow.CallableRoles, err)
	}
	// 마스킹: 결과의 연락처가 가려진다. 감사에는 건수·성공 여부만 있다.
	m2 := baseDef()
	m2["fields"] = []string{"no", "note"}
	masked, err := e.svc.Test(manage, mcptool.TestInput{Definition: raw(m2), Input: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	if n := masked.Rows[0]["note"].(string); strings.Contains(n, "010-1234-5678") {
		t.Fatalf("시험 결과에 연락처 원문: %q", n)
	}
	tests := 0
	for _, r := range e.rec.Records() {
		if r.Action != "mcptool.test" {
			continue
		}
		tests++
		for _, d := range r.Diff {
			if strings.Contains(d.New, "A-1") || d.New == "open" || strings.Contains(d.New, "010") {
				t.Fatalf("시험 감사에 값이 있음: %+v", d)
			}
		}
	}
	if tests != 3 {
		t.Fatalf("시험 감사 %d건", tests)
	}
	// 입력 오류·검증 오류는 실행하지 않으므로 감사하지 않는다. 서비스 오류는 성공 아님으로 남기고 오류를 돌려준다.
	if _, err := e.svc.Test(manage, mcptool.TestInput{Definition: raw(baseDef()), Input: map[string]any{"status": "nope"}}); !errors.Is(err, contract.ErrValidation) {
		t.Fatalf("입력 오류: %v", err)
	}
	if _, err := e.svc.Test(manage, mcptool.TestInput{Definition: []byte(`{"x":1}`)}); !errors.Is(err, contract.ErrValidation) {
		t.Fatalf("검증 오류: %v", err)
	}
	e.fr.err = contract.ErrForbidden
	if _, err := e.svc.Test(manage, in); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("서비스 거부: %v", err)
	}
	var okFlags []string
	for _, r := range e.rec.Records() {
		if r.Action == "mcptool.test" {
			for _, d := range r.Diff {
				if d.Key == "test_ok" {
					okFlags = append(okFlags, d.New)
				}
			}
		}
	}
	if len(okFlags) != 4 || okFlags[3] != "아니오" {
		t.Fatalf("성공 여부 기록: %v", okFlags)
	}
}

// 카탈로그 버전이 바뀐 정의는 broken 으로 표시된다(명세 14절).
func TestBrokenWhenCatalogChanges(t *testing.T) {
	e := newEnv(t)
	e.save(baseDef(), 0)
	cat2, _ := newFake()
	res, _ := cat2.Get("things")
	res.Version = 2
	l, err := e.mk(cat2).List(reader)
	if err != nil || len(l) != 1 || !l[0].Broken {
		t.Fatalf("깨짐 표시: %+v %v", l, err)
	}
	l, _ = e.svc.List(reader)
	if l[0].Broken {
		t.Fatal("같은 카탈로그인데 깨짐으로 표시됨")
	}
	// 자원이 사라져도 깨짐이고 목록 조회는 실패하지 않는다.
	l, err = e.mk(mcptool.NewCatalog()).List(reader)
	if err != nil || !l[0].Broken || l[0].ResourceLabel != "" {
		t.Fatalf("자원 없음: %+v %v", l, err)
	}
}

func TestCatalogListsFieldsWithoutAdapters(t *testing.T) {
	e := newEnv(t)
	cs, err := e.svc.Catalog(reader)
	if err != nil || len(cs) != 1 || cs[0].Label != "시험 자원" || len(cs[0].Fields) < 5 {
		t.Fatalf("%+v %v", cs, err)
	}
}
