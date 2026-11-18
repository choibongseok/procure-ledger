package sod_test

import (
	"context"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/sod"
)

// B44 D16: hold 규칙은 두 권한의 법인 범위가 겹칠 때만 위반이다. group 은 모든 법인과 겹친다.
func TestHoldsRequireOverlappingEntityScope(t *testing.T) {
	e := newEnv(t)
	put := func(uid int64, perm, scope string, ent int64, grant int64) {
		t.Helper()
		if _, err := e.db.Exec(`INSERT INTO user_effective_permissions (user_id, permission, role_id, grant_id, scope, entity_id) VALUES (?, ?, 1, ?, ?, ?)`, uid, perm, grant, scope, ent); err != nil {
			t.Fatal(err)
		}
	}
	var rule sod.Rule
	for _, r := range sod.Rules {
		if r.Kind == sod.KindHold {
			rule = r
			break
		}
	}
	a, b := string(rule.PermA), string(rule.PermB)
	// 서로 다른 법인에서 나눠 가짐: 위반 아님.
	put(e.staff, a, "entity", 1, 1)
	put(e.staff, b, "entity", 2, 2)
	// 같은 법인에서 함께 가짐: 위반.
	put(e.s2, a, "entity", 2, 3)
	put(e.s2, b, "entity", 2, 4)
	// 그룹 범위와 한 법인: 겹침, 위반.
	put(e.auditor, a, "group", 0, 5)
	put(e.auditor, b, "entity", 1, 6)

	holds, err := sod.Holds(context.Background(), e.db)
	if err != nil {
		t.Fatal(err)
	}
	set := holds[rule.Code]
	if set[e.staff] {
		t.Error("법인을 나눠 가진 사람이 위반으로 잡혔다")
	}
	if !set[e.s2] {
		t.Error("같은 법인에서 두 권한을 가진 사람이 위반으로 안 잡혔다")
	}
	if !set[e.auditor] {
		t.Error("그룹 범위와 한 법인은 겹쳐 위반이어야 한다")
	}
}
