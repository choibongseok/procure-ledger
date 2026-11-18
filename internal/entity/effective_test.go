package entity_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/access"
	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/entity"
	"github.com/choibongseok/procure-ledger/internal/org"
)

func (e *env) grantRow(uid int64, perm, scope string, ent int64, grant int64) {
	e.t.Helper()
	if _, err := e.db.Exec(`INSERT INTO user_effective_permissions (user_id, permission, role_id, grant_id, scope, entity_id) VALUES (?, ?, 1, ?, ?, ?)`, uid, perm, grant, scope, ent); err != nil {
		e.t.Fatal(err)
	}
}

// 실효 법인 범위(own 부여)를 지금의 소속·겸직 법인으로 푼다(조회 때마다).
func (e *env) effective(uid int64, perm contract.Permission, day contract.Day) contract.EntitySet {
	e.t.Helper()
	if err := access.Recompute(context.Background(), e.db, day); err != nil {
		e.t.Fatal(err)
	}
	_, own, err := org.EntitiesOf(context.Background(), e.db, uid, day)
	if err != nil {
		e.t.Fatal(err)
	}
	perms, err := access.EntityPermsOf(context.Background(), e.db, uid, own)
	if err != nil {
		e.t.Fatal(err)
	}
	return perms[perm]
}

// AC-4412: own 부여의 효력 법인은 발령(법인 이동)·겸직 추가·종료를 따라 바뀐다. 부여 자체는 다시 만들지 않는다.
func TestOwnGrantFollowsAppointmentAndConcurrentChanges(t *testing.T) {
	e := newEnv(t)
	rootA := e.unit("CO", "본사", contract.OrgCompany, 0)
	deptA := e.unit("DA", "구매팀", contract.OrgDepartment, rootA)
	rootB := e.unit("AB", "계열사 본사", contract.OrgCompany, 0)
	deptB := e.unit("DB", "계열사 영업팀", contract.OrgDepartment, rootB)
	ent, err := e.svc.Create(e.ctx(), entity.Input{Code: "ABC", Name: "계열사", RootUnitID: rootB})
	if err != nil {
		t.Fatal(err)
	}
	uid := e.hire("a@pl.test", deptA)
	perm := contract.PermPurchaseCreate
	if _, err := e.db.Exec(`UPDATE users SET active = 1 WHERE id = ?`, uid); err != nil {
		t.Fatal(err)
	}
	// 템플릿 권한은 서버 기동 때 동기화되므로(access.SyncTemplates) 시험에서는 필요한 한 줄만 넣는다.
	if _, err := e.db.Exec(`INSERT INTO role_permissions (role_id, permission) SELECT id, ? FROM roles WHERE key = 'staff'`, string(perm)); err != nil {
		t.Fatal(err)
	}
	// 기본 템플릿(staff) 호환 부여가 own 범위다. 아래 effective 가 실효 권한 캐시를 그날 기준으로 다시 계산해서 읽는다.
	day := contract.Day("2026-03-10")
	reg := func(c org.Command) {
		t.Helper()
		c.Reason, c.Confirm = "시험", true
		if _, err := e.org.Register(context.Background(), e.admin, c); err != nil {
			t.Fatalf("%s: %v", c.Kind, err)
		}
	}
	if got := e.effective(uid, perm, day); !reflect.DeepEqual(got, contract.NewEntitySet(1)) {
		t.Fatalf("입사 직후: %+v", got)
	}
	reg(org.Command{Kind: org.KindConcurrentAdd, UserID: uid, ToUnit: deptB, Effective: "2026-03-01"})
	if got := e.effective(uid, perm, day); !reflect.DeepEqual(got, contract.NewEntitySet(1, ent.ID)) {
		t.Fatalf("겸직 추가 뒤: %+v", got)
	}
	reg(org.Command{Kind: org.KindConcurrentEnd, UserID: uid, ToUnit: deptB, Effective: "2026-03-05"})
	if got := e.effective(uid, perm, day); !reflect.DeepEqual(got, contract.NewEntitySet(1)) {
		t.Fatalf("겸직 종료 뒤: %+v", got)
	}
	reg(org.Command{Kind: org.KindTransfer, UserID: uid, ToUnit: deptB, Effective: "2026-03-08"})
	if got := e.effective(uid, perm, day); !reflect.DeepEqual(got, contract.NewEntitySet(ent.ID)) {
		t.Fatalf("법인 이동 발령 뒤: %+v", got)
	}
}

// 알림 수신자: 그 법인에서 권한이 효력인 사람만(own 은 소속·겸직 법인, entity 는 그 법인, group 은 모두).
func TestPermissionHoldersByEntity(t *testing.T) {
	e := newEnv(t)
	rootA := e.unit("CO", "본사", contract.OrgCompany, 0)
	deptA := e.unit("DA", "구매팀", contract.OrgDepartment, rootA)
	rootB := e.unit("AB", "계열사 본사", contract.OrgCompany, 0)
	deptB := e.unit("DB", "계열사 영업팀", contract.OrgDepartment, rootB)
	ent, err := e.svc.Create(e.ctx(), entity.Input{Code: "ABC", Name: "계열사", RootUnitID: rootB})
	if err != nil {
		t.Fatal(err)
	}
	a, b := e.hire("a@pl.test", deptA), e.hire("b@pl.test", deptB)
	g := e.hire("g@pl.test", deptA)
	perm := string(contract.PermTaxASPManage)
	e.grantRow(a, perm, "own", 0, 1)
	e.grantRow(b, perm, "own", 0, 2)
	e.grantRow(g, perm, "group", 0, 3)
	day := contract.Day("2026-03-10")
	ctx := context.Background()
	got, err := entity.PermissionHolders(ctx, e.db, contract.PermTaxASPManage, 1, day)
	if err != nil || !reflect.DeepEqual(got, []int64{a, g}) {
		t.Fatalf("기본 법인 수신자: %v %v", got, err)
	}
	got, err = entity.PermissionHolders(ctx, e.db, contract.PermTaxASPManage, ent.ID, day)
	if err != nil || !reflect.DeepEqual(got, []int64{b, g}) {
		t.Fatalf("계열사 수신자: %v %v", got, err)
	}
}
