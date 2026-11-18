package contract

import (
	"context"
	"testing"
)

func TestScopeSQLInjectsAtOutermostWhere(t *testing.T) {
	ctx := WithActiveEntity(context.Background(), ActiveEntity{ID: 3})
	cases := []struct{ in, want string }{
		{"SELECT 1 FROM journals j WHERE j.id = ?", "SELECT 1 FROM journals j WHERE j.entity_id = 3 AND j.id = ?"},
		{"SELECT 1 FROM journals j ORDER BY j.id", "SELECT 1 FROM journals j  WHERE j.entity_id = 3 ORDER BY j.id"},
		{"SELECT 1 FROM journals j", "SELECT 1 FROM journals j WHERE j.entity_id = 3"},
		// 하위 질의의 WHERE·따옴표 안의 WHERE 는 건드리지 않는다.
		{"SELECT (SELECT 1 FROM x WHERE x.id = j.id), 'where' FROM journals j WHERE j.no = ?", "SELECT (SELECT 1 FROM x WHERE x.id = j.id), 'where' FROM journals j WHERE j.entity_id = 3 AND j.no = ?"},
		{"SELECT 1 FROM journals j LIMIT 5", "SELECT 1 FROM journals j  WHERE j.entity_id = 3 LIMIT 5"},
		{"UPDATE journals SET memo = ? WHERE id = ?", "UPDATE journals SET memo = ? WHERE entity_id = 3 AND id = ?"},
	}
	for _, c := range cases {
		alias := "j"
		if c.in[:6] == "UPDATE" {
			alias = ""
		}
		if got := ScopeSQL(ctx, alias, c.in); got != c.want {
			t.Errorf("%q\n got %q\nwant %q", c.in, got, c.want)
		}
	}
	if got := EntitySQL(ctx, "FROM (SELECT * FROM journals WHERE {entity_id}) j JOIN p ON {entity_id:p} AND x {entity_in}"); got != "FROM (SELECT * FROM journals WHERE entity_id = 3) j JOIN p ON p.entity_id = 3 AND x IN (3)" {
		t.Errorf("표시자: %q", got)
	}
	if got := EntitySQL(context.Background(), "WHERE {entity_id} {entity_in}"); got != "WHERE 1=1 IN (SELECT id FROM entities)" {
		t.Errorf("조건 없음: %q", got)
	}
	// 현재 법인이 없거나 전체 모드면 그대로다.
	for _, cx := range []context.Context{context.Background(), WithActiveEntity(context.Background(), ActiveEntity{All: true})} {
		if got := ScopeSQL(cx, "j", "SELECT 1 FROM journals j WHERE j.id = ?"); got != "SELECT 1 FROM journals j WHERE j.id = ?" {
			t.Errorf("조건이 붙으면 안 된다: %q", got)
		}
	}
	if SQLEntity(ctx, "") != "entity_id = 3" || WriteEntityID(context.Background()) != DefaultEntityID || WriteEntityID(ctx) != 3 {
		t.Fatal("조각·값")
	}
}
