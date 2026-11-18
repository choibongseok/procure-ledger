package onboarding

import (
	"strings"
	"testing"
)

// B44 항목 "법인·사업장": 해당 없음 가능. 기본 법인에 사업자번호가 있고 법인마다 주사업장이 있으며 기본 법인이 아닌 법인은 루트 조직이 연결돼 있으면 완료.
func TestEntitiesItem(t *testing.T) {
	x := newEnv(t, Env{})
	it := x.item(t, "entities")
	if it.Title != "법인·사업장" || !it.Skippable || it.State != StateTodo || !strings.Contains(it.Evidence, "사업자번호") {
		t.Fatalf("새 DB(기본 법인 하나, 사업자번호 없음): %+v", it)
	}
	x.exec(t, `UPDATE entities SET business_no = '123-45-67890' WHERE id = 1`)
	if it := x.item(t, "entities"); it.State != StateDone {
		t.Fatalf("사업자번호 입력: %s(%s)", it.State, it.Evidence)
	}
	// 계열사를 만들었지만 사업자번호·루트 조직이 없으면 진행 중이다.
	x.exec(t, `INSERT INTO entities (id, code, name, created_at, updated_at) VALUES (2, 'ABC', '계열사', 't', 't')`)
	x.exec(t, `INSERT INTO entity_sites (entity_id, site_no, name, kind, created_at, updated_at) VALUES (2, '0000', '주사업장', 'head', 't', 't')`)
	it = x.item(t, "entities")
	if it.State != StateInProgress || !strings.Contains(it.Evidence, "사업자번호가 없는 법인 1개") || !strings.Contains(it.Evidence, "루트 조직이 연결되지 않은 법인 1개") {
		t.Fatalf("계열사 미완: %s(%s)", it.State, it.Evidence)
	}
	x.exec(t, `UPDATE entities SET business_no = '220-81-62517' WHERE id = 2`)
	x.exec(t, `INSERT INTO org_units (id, code) VALUES (900, 'AB')`)
	x.exec(t, `UPDATE entities SET root_unit_id = 900 WHERE id = 2`)
	if it := x.item(t, "entities"); it.State != StateDone || !strings.Contains(it.Evidence, "활성 법인 2개") {
		t.Fatalf("계열사 완료: %s(%s)", it.State, it.Evidence)
	}
	// 주사업장이 비활성이면 미완료.
	x.exec(t, `UPDATE entity_sites SET active = 0 WHERE entity_id = 2`)
	if it := x.item(t, "entities"); it.State != StateInProgress || !strings.Contains(it.Evidence, "주사업장이 없는 법인 1개") {
		t.Fatalf("주사업장 없음: %s(%s)", it.State, it.Evidence)
	}
}
