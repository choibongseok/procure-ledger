package org

import (
	"testing"

	"github.com/choibongseok/procure-ledger/internal/contract"
)

// B44: 현재 법인이 정해져 있으면 조직도 트리는 그 법인의 루트 조직이다. 정해지지 않았거나 "전체" 모드면 번호가 가장 작은 회사다(법인이 하나뿐일 때와 같다).
func TestTreeFollowsActiveEntityRoot(t *testing.T) {
	e := newEnv(t)
	co, _, _, _, _ := e.baseOrg()
	other := e.reg(Command{Kind: KindUnitCreate, Code: "AB", Name: "계열사 주식회사", UnitKind: contract.OrgCompany, Effective: "2020-01-01", Reason: "설립"}).UnitID
	if _, err := e.db.Exec(`INSERT INTO entities (code, name, business_no, root_unit_id, is_default, active, version, created_at, updated_at) VALUES ('ABC', '계열사', '2208162517', ?, 0, 1, 1, '2026-03-10T00:00:00Z', '2026-03-10T00:00:00Z')`, other); err != nil {
		t.Fatal(err)
	}
	var otherEntity int64
	if err := e.db.QueryRow(`SELECT id FROM entities WHERE code = 'ABC'`).Scan(&otherEntity); err != nil {
		t.Fatal(err)
	}
	// 현재 법인 없음 → 번호가 가장 작은 회사.
	tree, err := e.svc.Tree(e.ctx, "")
	if err != nil || tree == nil || tree.ID != co {
		t.Fatalf("기본: %+v %v", tree, err)
	}
	// 다른 법인을 고르면 그 법인의 루트 트리.
	ctx := contract.WithActiveEntity(e.ctx, contract.ActiveEntity{ID: otherEntity})
	tree, err = e.svc.Tree(ctx, "")
	if err != nil || tree == nil || tree.ID != other || tree.Name != "계열사 주식회사" {
		t.Fatalf("다른 법인: %+v %v", tree, err)
	}
	// 전체 모드는 기본 규칙.
	ctx = contract.WithActiveEntity(e.ctx, contract.ActiveEntity{All: true})
	if tree, err = e.svc.Tree(ctx, ""); err != nil || tree == nil || tree.ID != co {
		t.Fatalf("전체: %+v %v", tree, err)
	}
	// 루트가 연결되지 않은 법인(또는 알 수 없는 법인)은 기본 규칙으로 돌아간다.
	if _, err := e.db.Exec(`UPDATE entities SET root_unit_id = NULL WHERE id = ?`, otherEntity); err != nil {
		t.Fatal(err)
	}
	ctx = contract.WithActiveEntity(e.ctx, contract.ActiveEntity{ID: otherEntity})
	if tree, err = e.svc.Tree(ctx, ""); err != nil || tree == nil || tree.ID != co {
		t.Fatalf("연결 없음: %+v %v", tree, err)
	}
}
