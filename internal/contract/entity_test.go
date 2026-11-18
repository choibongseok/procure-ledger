package contract

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func entityPrincipal(perm Permission, set EntitySet, own ...int64) Principal {
	return Principal{UserID: 1, Role: RoleFinance, Perms: NewPermSet(perm), EntityPerms: map[Permission]EntitySet{perm: set}, OwnEntities: own}
}

func TestEntitySetAndScope(t *testing.T) {
	a, b := NewEntitySet(3, 1, 3), NewEntitySet(2)
	if !reflect.DeepEqual(a.IDs, []int64{1, 3}) {
		t.Fatalf("정렬·중복 제거: %v", a.IDs)
	}
	if !a.Union(b).Has(2) || !a.Has(1) || a.Has(2) || !(EntitySet{All: true}).Has(99) || !(EntitySet{}).Empty() {
		t.Fatal("집합 연산")
	}
	if a.Intersects(b) || !a.Intersects(NewEntitySet(3, 4)) || !a.Intersects(EntitySet{All: true}) || (EntitySet{}).Intersects(EntitySet{All: true}) {
		t.Fatal("겹침 판정(그룹은 모든 법인과 겹치고 빈 집합은 겹치지 않는다)")
	}
	if c, args := (EntityScope{All: true}).Cond("j"); c != "1 = 1" || args != nil {
		t.Fatalf("그룹은 조건이 없다: %s", c)
	}
	if c, _ := (EntityScope{}).Cond("j"); c != "1 = 0" {
		t.Fatalf("빈 범위는 전부 거부: %s", c)
	}
	if c, args := (EntityScope{IDs: []int64{1, 2}}).Cond("j"); c != "j.entity_id IN (?,?)" || len(args) != 2 {
		t.Fatalf("조건: %s %v", c, args)
	}
}

func TestPrincipalEntityPerms(t *testing.T) {
	// 계산되지 않은 Principal(시험·가짜 구현)은 권한이 있으면 전체 법인이다: 법인이 하나뿐인 설치와 같다.
	legacy := Principal{UserID: 1, Role: RoleFinance}
	if !legacy.CanIn(PermLedgerWrite, 7) || !legacy.EntitiesFor(PermLedgerWrite).All {
		t.Fatal("계산되지 않은 Principal")
	}
	if legacy.CanIn(PermUserManage, 1) {
		t.Fatal("권한이 없으면 어느 법인에서도 안 된다")
	}
	p := entityPrincipal(PermLedgerWrite, NewEntitySet(2), 1, 2)
	if !p.CanIn(PermLedgerWrite, 2) || p.CanIn(PermLedgerWrite, 1) || p.CanIn(PermLedgerRead, 2) {
		t.Fatal("권한별 법인 판정")
	}
	if got := p.SelectableEntities(); !reflect.DeepEqual(got.IDs, []int64{1, 2}) || got.All {
		t.Fatalf("고를 수 있는 법인: %+v", got)
	}
	g := entityPrincipal(PermLedgerWrite, EntitySet{All: true}, 1)
	if !g.SelectableEntities().All {
		t.Fatal("그룹 범위가 하나라도 있으면 전체를 고를 수 있다")
	}
}

func TestReadScopeWriteEntityRequireIn(t *testing.T) {
	p := entityPrincipal(PermLedgerWrite, NewEntitySet(1, 2), 1, 2)
	ctxOf := func(p Principal, a *ActiveEntity) context.Context {
		c := WithPrincipal(context.Background(), p)
		if a != nil {
			c = WithActiveEntity(c, *a)
		}
		return c
	}
	if _, err := ReadScope(context.Background(), PermLedgerWrite); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("비로그인: %v", err)
	}
	if sc, err := ReadScope(ctxOf(p, nil), PermLedgerWrite); err != nil || !reflect.DeepEqual(sc.IDs, []int64{1, 2}) {
		t.Fatalf("현재 법인이 없으면 권한 범위 전체: %+v %v", sc, err)
	}
	if sc, err := ReadScope(ctxOf(p, &ActiveEntity{ID: 2}), PermLedgerWrite); err != nil || !reflect.DeepEqual(sc.IDs, []int64{2}) {
		t.Fatalf("현재 법인: %+v %v", sc, err)
	}
	if _, err := ReadScope(ctxOf(p, &ActiveEntity{ID: 3}), PermLedgerWrite); !errors.Is(err, ErrForbidden) {
		t.Fatalf("범위 밖 현재 법인: %v", err)
	}
	if _, err := ReadScope(ctxOf(p, &ActiveEntity{All: true}), PermLedgerWrite); !errors.Is(err, ErrForbidden) {
		t.Fatalf("그룹 범위 없이 전체 모드: %v", err)
	}
	g := entityPrincipal(PermLedgerWrite, EntitySet{All: true}, 1)
	if sc, err := ReadScope(ctxOf(g, &ActiveEntity{All: true}), PermLedgerWrite); err != nil || !sc.All {
		t.Fatalf("그룹 전체 모드: %+v %v", sc, err)
	}
	// 쓰기: 법인 하나가 정해져 있어야 하고 그 법인에서 권한이 있어야 한다.
	if id, err := WriteEntity(ctxOf(p, &ActiveEntity{ID: 2}), PermLedgerWrite); err != nil || id != 2 {
		t.Fatalf("쓰기 법인: %d %v", id, err)
	}
	if id, err := WriteEntity(ctxOf(p, nil), PermLedgerWrite); err != nil || id != DefaultEntityID {
		t.Fatalf("현재 법인이 없으면 기본 법인: %d %v", id, err)
	}
	if _, err := WriteEntity(ctxOf(g, &ActiveEntity{All: true}), PermLedgerWrite); !errors.Is(err, ErrEntityRequired) || !errors.Is(err, ErrValidation) {
		t.Fatalf("전체 모드의 쓰기: %v", err)
	}
	if _, err := WriteEntity(ctxOf(p, &ActiveEntity{ID: 3}), PermLedgerWrite); !errors.Is(err, ErrForbidden) {
		t.Fatalf("범위 밖 법인에 쓰기: %v", err)
	}
	if err := RequireIn(ctxOf(p, nil), PermLedgerWrite, 3); !errors.Is(err, ErrNotFound) {
		t.Fatalf("다른 법인 레코드는 404: %v", err)
	}
	if err := RequireIn(ctxOf(p, nil), PermLedgerWrite, 1); err != nil {
		t.Fatalf("범위 안 레코드: %v", err)
	}
}
