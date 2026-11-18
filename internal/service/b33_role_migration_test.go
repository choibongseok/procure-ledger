package service_test

import (
	"context"
	"reflect"
	"sort"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/repository"
)

// B33 2단계: 결재 풀·위임 후보가 역할 이름이 아니라 실효 권한으로 정해진다. 기본 템플릿 사용자만 있을 때는 이관 전 결과와 같고,
// 사용자 정의 역할로 권한을 받은 사람은 같은 규칙으로 풀·후보에 들어온다.

func legacyIDs(t *testing.T, e *env, where string) []int64 {
	t.Helper()
	rows, err := e.db.Query(`SELECT id FROM users WHERE active = 1 AND ` + where + ` ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		out = append(out, id)
	}
	return out
}

func idsOf(us []repository.LineUserRec) []int64 {
	var out []int64
	for _, u := range us {
		out = append(out, u.ID)
	}
	return out
}

func sameIDs(t *testing.T, what string, got, want []int64) {
	t.Helper()
	sort.Slice(got, func(i, j int) bool { return got[i] < got[j] })
	sort.Slice(want, func(i, j int) bool { return want[i] < want[j] })
	if len(got) == 0 && len(want) == 0 {
		return
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%s: 실효 권한 %v, 이관 전(역할) %v", what, got, want)
	}
}

func TestApprovalPoolsMatchLegacyRoleSets(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	lines := repository.SQLLines{}
	for _, c := range []struct {
		perm   contract.Permission
		legacy string
	}{
		{contract.PermFinanceStage, `role = 'finance'`},
		{contract.PermApprovalDecide, `role IN ('manager','finance')`},
		{contract.PermApprovalAgree, `role IN ('manager','finance','staff')`},
	} {
		us, err := lines.ActiveByPerm(ctx, e.db, c.perm)
		if err != nil {
			t.Fatal(err)
		}
		sameIDs(t, string(c.perm), idsOf(us), legacyIDs(t, e, c.legacy))
	}
	// 위임 후보: 재무는 재무에게만(자기 제외), 팀장은 조직장끼리만이고 재무·직원은 없다.
	cs, err := e.delegation.Candidates(as(uFinance))
	if err != nil {
		t.Fatal(err)
	}
	var fin []int64
	for _, c := range cs {
		fin = append(fin, c.ID)
	}
	sameIDs(t, "재무의 위임 후보", fin, []int64{uFinance2})
	cs, err = e.delegation.Candidates(as(uLead))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cs {
		if c.ID == uFinance || c.ID == uFinance2 || c.ID == uStaff || c.ID == uAdmin {
			t.Errorf("팀장의 위임 후보에 %d 가 있으면 안 됨", c.ID)
		}
	}
}

// 사용자 정의 역할로 결재 권한·재무 단계 권한을 받은 사람은 풀과 위임 후보에 같은 규칙으로 들어오고, 회수하면 빠진다.
func TestCustomRoleEntersPoolsAndCandidates(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	lines := repository.SQLLines{}
	// 직원(uStaff)에게 "재무 결재" 사용자 정의 역할: 결재 + 재무 단계 + 위임.
	if _, err := e.db.Exec(`INSERT INTO roles (id, key, name, kind, created_at, updated_at) VALUES (100,'r_100','재무 보조','custom','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	for _, p := range []contract.Permission{contract.PermApprovalDecide, contract.PermFinanceStage, contract.PermApprovalDelegate} {
		if _, err := e.db.Exec(`INSERT INTO role_permissions (role_id, permission) VALUES (100, ?)`, string(p)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := e.db.Exec(`INSERT INTO role_grants (role_id, target_type, target_id, source, created_at) VALUES (100,'user',?,'manual','2026-01-01T00:00:00Z')`, uStaff); err != nil {
		t.Fatal(err)
	}
	pool, err := lines.ActiveByPerm(ctx, e.db, contract.PermFinanceStage)
	if err != nil {
		t.Fatal(err)
	}
	sameIDs(t, "재무 풀(사용자 정의 역할 포함)", idsOf(pool), []int64{uFinance, uFinance2, uStaff})
	cs, err := e.delegation.Candidates(as(uFinance))
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, c := range cs {
		found = found || c.ID == uStaff
	}
	if !found {
		t.Error("재무 단계 권한을 받은 사람이 재무의 위임 후보에 있어야 함")
	}
	// 권한을 회수하면 풀에서 빠진다.
	if _, err := e.db.Exec(`DELETE FROM role_grants WHERE role_id = 100`); err != nil {
		t.Fatal(err)
	}
	pool, err = lines.ActiveByPerm(ctx, e.db, contract.PermFinanceStage)
	if err != nil {
		t.Fatal(err)
	}
	sameIDs(t, "회수 뒤 재무 풀", idsOf(pool), []int64{uFinance, uFinance2})
}
