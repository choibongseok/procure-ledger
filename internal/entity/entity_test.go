package entity_test

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/choibongseok/procure-ledger/internal/audit"
	"github.com/choibongseok/procure-ledger/internal/contract"
	caudit "github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/dbtest"
	"github.com/choibongseok/procure-ledger/internal/entity"
	"github.com/choibongseok/procure-ledger/internal/org"
	"github.com/choibongseok/procure-ledger/internal/rbac"
	"github.com/choibongseok/procure-ledger/migrations"
)

type env struct {
	t     *testing.T
	db    *sql.DB
	svc   *entity.Service
	org   *org.Service
	store *audit.Store
	admin contract.Principal
}

func newEnv(t *testing.T) *env {
	t.Helper()
	db := dbtest.Open(t)
	ctx := context.Background()
	if err := migrations.Apply(ctx, db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO departments (id, name) VALUES (900, '임시')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO users (id, email, name, role, department_id) VALUES (900, 'admin@pl.test', '관리자', 'admin', 900)`); err != nil {
		t.Fatal(err)
	}
	store := audit.New(db)
	now := func() time.Time { return time.Date(2026, 3, 10, 3, 0, 0, 0, time.UTC) }
	osvc, err := org.New(org.Options{DB: db, Txs: contract.SQLTxManager{DB: db}, Audit: store, Masker: rbac.Masker{}, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	e := &env{t: t, db: db, org: osvc, store: store, admin: contract.Principal{UserID: 900, Name: "관리자", Role: contract.RoleAdmin, Channel: contract.ChannelWeb, Auth: contract.AuthSession}}
	e.svc = entity.New(entity.Options{DB: db, Txs: contract.SQLTxManager{DB: db}, Audit: store, Org: osvc, Now: now})
	return e
}

func (e *env) ctx() context.Context {
	return contract.WithActiveEntity(contract.WithPrincipal(context.Background(), e.admin), contract.ActiveEntity{ID: 1})
}

func (e *env) unit(code, name string, kind contract.OrgKind, parent int64) int64 {
	e.t.Helper()
	o, err := e.org.Register(context.Background(), e.admin, org.Command{Kind: org.KindUnitCreate, Code: code, Name: name, UnitKind: kind, ParentID: parent, Effective: "2020-01-01", Reason: "설립", Confirm: true})
	if err != nil {
		e.t.Fatalf("조직 %s: %v", code, err)
	}
	return o.UnitID
}

func (e *env) hire(email string, unit int64) int64 {
	e.t.Helper()
	o, err := e.org.Register(context.Background(), e.admin, org.Command{Kind: org.KindHire, Name: email, Email: email, ToUnit: unit, Rank: 1, Role: contract.RoleStaff, Effective: "2021-01-04", Reason: "입사", Confirm: true})
	if err != nil {
		e.t.Fatalf("입사 %s: %v", email, err)
	}
	return o.UserID
}

func (e *env) count(q string, a ...any) int {
	var n int
	if err := e.db.QueryRow(q, a...).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

func TestCreateValidatesAndAudits(t *testing.T) {
	e := newEnv(t)
	for name, in := range map[string]entity.Input{
		"코드 형식":   {Code: "a", Name: "계열사"},
		"이름 없음":   {Code: "ABC"},
		"사업자번호":   {Code: "ABC", Name: "계열사", BusinessNo: "1234"},
		"회계연도 시작": {Code: "ABC", Name: "계열사", FiscalStartMonth: 13},
	} {
		if _, err := e.svc.Create(e.ctx(), in); !errors.Is(err, contract.ErrValidation) {
			t.Errorf("%s: %v", name, err)
		}
	}
	got, err := e.svc.Create(e.ctx(), entity.Input{Code: "abc", Name: "계열사", BusinessNo: "220-81-62517", Representative: "김", FiscalStartMonth: 4})
	if err != nil || got.Code != "ABC" || got.IsDefault || !got.Active || got.Sites != 1 || got.FiscalStartMonth != 4 {
		t.Fatalf("등록: %+v %v", got, err)
	}
	if _, err := e.svc.Create(e.ctx(), entity.Input{Code: "ABC", Name: "또 계열사"}); !errors.Is(err, contract.ErrValidation) || !strings.Contains(err.Error(), "코드") {
		t.Fatalf("코드 중복: %v", err)
	}
	if _, err := e.svc.Create(e.ctx(), entity.Input{Code: "XYZ", Name: "또", BusinessNo: "220-81-62517"}); !errors.Is(err, contract.ErrValidation) || !strings.Contains(err.Error(), "사업자번호") {
		t.Fatalf("사업자번호 중복: %v", err)
	}
	// 감사: 그 법인의 사건으로 남고(entity_id), diff 에 값 이름이 있다.
	recs, _ := e.store.List(context.Background(), caudit.Filter{Action: "entity.create"})
	if len(recs) != 1 || recs[0].EntityID != got.ID || recs[0].TargetType != caudit.TargetEntity {
		t.Fatalf("감사: %+v", recs)
	}
	if res, err := e.store.Verify(context.Background()); err != nil || !res.ChainOK {
		t.Fatalf("체인: %+v %v", res, err)
	}
}

func TestPermissionsAndDefaultEntityRules(t *testing.T) {
	e := newEnv(t)
	staff := contract.WithPrincipal(context.Background(), contract.Principal{UserID: 1, Role: contract.RoleStaff})
	if _, err := e.svc.List(staff); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("직원: %v", err)
	}
	fin := contract.WithPrincipal(context.Background(), contract.Principal{UserID: 1, Role: contract.RoleFinance})
	if _, err := e.svc.Create(fin, entity.Input{Code: "ABC", Name: "x"}); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("재무: %v", err)
	}
	if _, err := e.svc.SetActive(e.ctx(), 1, false); !errors.Is(err, contract.ErrInvalidState) {
		t.Fatalf("기본 법인 비활성화: %v", err)
	}
	if _, err := e.svc.Update(e.ctx(), 1, entity.Input{Name: "다른 이름", Version: 1}); !errors.Is(err, contract.ErrInvalidState) || !strings.Contains(err.Error(), "회사 설정") {
		t.Fatalf("기본 법인 수정은 회사 설정 화면: %v", err)
	}
	if _, err := e.svc.SetRoot(e.ctx(), 1, 5); !errors.Is(err, contract.ErrInvalidState) {
		t.Fatalf("기본 법인 루트: %v", err)
	}
	other, err := e.svc.Create(e.ctx(), entity.Input{Code: "ABC", Name: "계열사"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Update(e.ctx(), other.ID, entity.Input{Name: "새 이름", Version: other.Version + 5}); !errors.Is(err, contract.ErrInvalidState) {
		t.Fatalf("낙관적 잠금: %v", err)
	}
	up, err := e.svc.Update(e.ctx(), other.ID, entity.Input{Name: "새 이름", Version: other.Version, FiscalStartMonth: 7})
	if err != nil || up.Name != "새 이름" || up.Version != other.Version+1 {
		t.Fatalf("수정: %+v %v", up, err)
	}
	off, err := e.svc.SetActive(e.ctx(), other.ID, false)
	if err != nil || off.Active {
		t.Fatalf("비활성: %+v %v", off, err)
	}
	on, err := e.svc.SetActive(e.ctx(), other.ID, true)
	if err != nil || !on.Active {
		t.Fatalf("다시 켜기: %+v %v", on, err)
	}
	if _, err := e.svc.Get(e.ctx(), 999); !errors.Is(err, contract.ErrNotFound) {
		t.Fatalf("없는 법인: %v", err)
	}
}

// 조직의 법인은 루트에서 물려받는다: 루트를 법인에 연결하면 그 아래 조직·사람의 법인이 바뀌고, 겸직은 두 법인 모두에 속한다(B44 7절).
func TestRootLinkDerivesUnitAndPersonEntities(t *testing.T) {
	e := newEnv(t)
	rootA := e.unit("CO", "본사", contract.OrgCompany, 0)
	deptA := e.unit("DA", "구매팀", contract.OrgDepartment, rootA)
	rootB := e.unit("AB", "계열사 본사", contract.OrgCompany, 0)
	deptB := e.unit("DB", "계열사 영업팀", contract.OrgDepartment, rootB)
	staffA := e.hire("a@pl.test", deptA)
	staffB := e.hire("b@pl.test", deptB)

	// 연결 전에는 모든 조직이 기본 법인이다.
	day := contract.Day("2026-03-10")
	if own, all, err := org.EntitiesOf(context.Background(), e.db, staffB, day); err != nil || own != 1 || !reflect.DeepEqual(all, []int64{1}) {
		t.Fatalf("연결 전: %d %v %v", own, all, err)
	}
	// 후보: 부모 없는 조직 둘.
	list, err := e.svc.List(e.ctx())
	if err != nil || len(list.RootCandidates) != 2 {
		t.Fatalf("후보: %+v %v", list.RootCandidates, err)
	}
	ent, err := e.svc.Create(e.ctx(), entity.Input{Code: "ABC", Name: "계열사", RootUnitID: rootB})
	if err != nil || ent.RootUnitID != rootB || ent.Members != 2 {
		t.Fatalf("루트와 함께 등록: %+v %v", ent, err)
	}
	if own, all, err := org.EntitiesOf(context.Background(), e.db, staffB, day); err != nil || own != ent.ID || !reflect.DeepEqual(all, []int64{ent.ID}) {
		t.Fatalf("계열사 직원: %d %v %v", own, all, err)
	}
	if own, _, _ := org.EntitiesOf(context.Background(), e.db, staffA, day); own != 1 {
		t.Fatalf("본사 직원은 기본 법인: %d", own)
	}
	// 겸직: 본사 직원이 계열사 조직을 겸직하면 두 법인에 속한다. 주소속 법인은 그대로다.
	if _, err := e.org.Register(context.Background(), e.admin, org.Command{Kind: org.KindConcurrentAdd, UserID: staffA, ToUnit: deptB, Effective: "2021-02-01", Reason: "겸직", Confirm: true}); err != nil {
		t.Fatalf("겸직: %v", err)
	}
	if own, all, err := org.EntitiesOf(context.Background(), e.db, staffA, day); err != nil || own != 1 || !reflect.DeepEqual(all, []int64{1, ent.ID}) {
		t.Fatalf("겸직: %d %v %v", own, all, err)
	}
	// 이미 연결된 조직은 다른 법인에 연결할 수 없고, 하위 조직(부모 있음)은 루트가 될 수 없다.
	other, _ := e.svc.Create(e.ctx(), entity.Input{Code: "XYZ", Name: "또 다른 계열사"})
	if _, err := e.svc.SetRoot(e.ctx(), other.ID, rootB); !errors.Is(err, contract.ErrValidation) {
		t.Fatalf("중복 연결: %v", err)
	}
	if _, err := e.svc.SetRoot(e.ctx(), other.ID, deptA); !errors.Is(err, contract.ErrValidation) {
		t.Fatalf("하위 조직: %v", err)
	}
	// 연결을 풀면 조직이 기본 법인으로 돌아간다. 감사에 연결·해제가 남는다.
	if _, err := e.svc.SetRoot(e.ctx(), ent.ID, 0); err != nil {
		t.Fatal(err)
	}
	if own, _, _ := org.EntitiesOf(context.Background(), e.db, staffB, day); own != 1 {
		t.Fatalf("연결 해제 뒤: %d", own)
	}
	if n := e.count(`SELECT COUNT(*) FROM audit_logs WHERE action = 'entity.root_set' AND entity_id = ?`, ent.ID); n != 1 {
		t.Fatalf("루트 연결 감사: %d", n)
	}
}

func TestSites(t *testing.T) {
	e := newEnv(t)
	sites, err := e.svc.ListSites(e.ctx(), 1)
	if err != nil || len(sites) != 1 || sites[0].SiteNo != "0000" || sites[0].KindName != "주사업장" {
		t.Fatalf("기본 사업장: %+v %v", sites, err)
	}
	f := false
	for name, in := range map[string]entity.SiteInput{
		"번호 형식":    {SiteNo: "12", Name: "x", Kind: "branch"},
		"종류":       {SiteNo: "0001", Name: "x", Kind: "hq"},
		"주사업장 번호":  {SiteNo: "0001", Name: "x", Kind: "head"},
		"지점이 0000": {SiteNo: "0000", Name: "x", Kind: "branch"},
	} {
		if _, err := e.svc.SaveSite(e.ctx(), 1, 0, in); !errors.Is(err, contract.ErrValidation) {
			t.Errorf("%s: %v", name, err)
		}
	}
	br, err := e.svc.SaveSite(e.ctx(), 1, 0, entity.SiteInput{SiteNo: "0001", Name: "부산 지점", Kind: "branch", VATMode: "consolidated"})
	if err != nil || br.VATName != "주사업장 총괄납부" {
		t.Fatalf("지점: %+v %v", br, err)
	}
	if _, err := e.svc.SaveSite(e.ctx(), 1, 0, entity.SiteInput{SiteNo: "0001", Name: "중복", Kind: "factory"}); !errors.Is(err, contract.ErrValidation) {
		t.Fatalf("번호 중복: %v", err)
	}
	if _, err := e.svc.SaveSite(e.ctx(), 1, 0, entity.SiteInput{SiteNo: "0000", Name: "두 번째 주사업장", Kind: "head"}); !errors.Is(err, contract.ErrValidation) {
		t.Fatalf("주사업장은 하나: %v", err)
	}
	if _, err := e.svc.SaveSite(e.ctx(), 1, br.ID, entity.SiteInput{SiteNo: "0001", Name: "부산 지점", Kind: "factory"}); !errors.Is(err, contract.ErrInvalidState) {
		t.Fatalf("종류 변경: %v", err)
	}
	off, err := e.svc.SaveSite(e.ctx(), 1, br.ID, entity.SiteInput{SiteNo: "0001", Name: "부산 지점", Kind: "branch", Active: &f})
	if err != nil || off.Active {
		t.Fatalf("지점 비활성: %+v %v", off, err)
	}
	if _, err := e.svc.SaveSite(e.ctx(), 1, sites[0].ID, entity.SiteInput{SiteNo: "0000", Name: "주사업장", Kind: "head", Active: &f}); !errors.Is(err, contract.ErrInvalidState) {
		t.Fatalf("주사업장 비활성: %v", err)
	}
	// 다른 법인의 사업장은 건드릴 수 없다.
	other, _ := e.svc.Create(e.ctx(), entity.Input{Code: "ABC", Name: "계열사"})
	if _, err := e.svc.SaveSite(e.ctx(), other.ID, br.ID, entity.SiteInput{SiteNo: "0001", Name: "x", Kind: "branch"}); !errors.Is(err, contract.ErrNotFound) {
		t.Fatalf("다른 법인 사업장: %v", err)
	}
	if n := e.count(`SELECT COUNT(*) FROM audit_logs WHERE action = 'site.save' AND entity_id = 1`); n != 2 {
		t.Fatalf("사업장 감사: %d", n)
	}
}
