package main

// 라우트 × 기본 역할 허용 행렬 골든(B33 동작 불변 보증). B33 이관 전 코드로 만든 파일과 이관 뒤 계산이 글자 단위로 같아야 한다.
// 다시 만들려면 UPDATE_GOLDEN=1(기본 템플릿 권한을 바꾸는 일이므로 사유가 명세에 있어야 한다).

import (
	"database/sql"

	"context"
	"github.com/choibongseok/procure-ledger/internal/bootstrap"
	"github.com/choibongseok/procure-ledger/internal/config"
	"github.com/choibongseok/procure-ledger/internal/contract/deps"
	"github.com/choibongseok/procure-ledger/internal/dbtest"
	"reflect"

	"encoding/json"
	"fmt"
	"github.com/choibongseok/procure-ledger/internal/access"
	"os"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/contract"
)

// intentionalRouteChanges 는 B33 이관과 무관하게 다른 영역이 의도해서 라우트 권한을 바꾼 경우다(골든의 키 → 지금의 키). 이유를 적는다.
var intentionalRouteChanges = map[string]string{
	// erp(B32 후속): 카드 소지자 변경·재지정을 card:manage(재무)에서 card:holder_manage(재무·관리자)로 넓힘.
	"POST /api/finance/cards/{id}/holder [card:manage]":        "POST /api/finance/cards/{id}/holder [card:holder_manage]",
	"POST /api/finance/cards/txns/{id}/reassign [card:manage]": "POST /api/finance/cards/txns/{id}/reassign [card:holder_manage]",
}

// fullEnv 는 플랫폼·업무·AI 라우트가 모두 붙은 실제 구성(개발 IdP, 시드 포함)을 만든다.
func fullEnv(t *testing.T) (*contract.RouteTable, *sql.DB, deps.Deps) {
	t.Helper()
	dbtest.SetEnv(t)
	t.Setenv("PL_DEV_IDP", "1")
	t.Setenv("PL_LLM_PROVIDER", "mock")
	cfg := config.Load()
	d, closeDB, err := bootstrap.Build(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(closeDB)
	return buildRoutes(cfg, d), d.DB, d
}

const routeGoldenPath = "testdata/route_role_matrix.golden.json"

// routeMatrix 는 "METHOD pattern [권한]" → 기본 역할별 허용 여부다. 허용은 라우트가 선언한 권한을 그 역할이 가졌는지(public·authenticated·setup 은 로그인 역할이면 참).
func routeMatrix(routes []contract.RouteDecl, can func(role contract.Role, perm contract.Permission) bool) map[string]map[string]bool {
	out := map[string]map[string]bool{}
	for _, r := range routes {
		key := fmt.Sprintf("%s %s [%s]", r.Method, r.Pattern, r.Permission)
		row := map[string]bool{}
		for _, role := range contract.Roles {
			row[string(role)] = can(role, r.Permission)
		}
		out[key] = row
	}
	return out
}

func legacyCan(role contract.Role, perm contract.Permission) bool {
	switch perm {
	case contract.PermPublic, contract.PermAuthenticated, contract.PermSetup:
		return true
	}
	return contract.RoleHas(role, perm)
}

func TestUpdateRouteGolden(t *testing.T) {
	if os.Getenv("UPDATE_GOLDEN") != "1" {
		t.Skip()
	}
	rt, _, _ := fullEnv(t)
	b, _ := json.MarshalIndent(routeMatrix(rt.Routes(), legacyCan), "", "  ")
	if err := os.WriteFile(routeGoldenPath, append(b, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
}

// AC-1301·1302: 이관 뒤 계산(DB 의 호환 부여 → 실효 권한 → Principal.Can)이 이관 전 코드로 만든 골든과 라우트별로 글자 단위로 같다.
// 골든에 있는 라우트는 모두 같아야 하고, 그 뒤 새로 생긴 라우트(새 기능)는 비교하지 않는다.
func TestRouteRoleMatrixUnchangedAfterRoleMigration(t *testing.T) {
	rt, db, _ := fullEnv(t)
	users := map[contract.Role]string{
		contract.RoleAdmin: "admin@pl.test", contract.RoleFinance: "finance@pl.test", contract.RoleManager: "lead@pl.test",
		contract.RoleStaff: "staff@pl.test", contract.RoleAuditor: "auditor@pl.test",
	}
	perms := map[contract.Role]contract.PermSet{}
	for role, email := range users {
		var id int64
		if err := db.QueryRow(`SELECT id FROM users WHERE email = ? AND role = ?`, email, string(role)).Scan(&id); err != nil {
			t.Fatalf("%s: %v", email, err)
		}
		ps, err := access.PermsOf(context.Background(), db, id)
		if err != nil {
			t.Fatal(err)
		}
		perms[role] = ps
	}
	now := routeMatrix(rt.Routes(), func(role contract.Role, perm contract.Permission) bool {
		return contract.Principal{UserID: 1, Role: role, Perms: perms[role]}.Can(perm)
	})
	b, err := os.ReadFile(routeGoldenPath)
	if err != nil {
		t.Fatal(err)
	}
	var want map[string]map[string]bool
	if err := json.Unmarshal(b, &want); err != nil {
		t.Fatal(err)
	}
	if len(want) < 100 {
		t.Fatalf("골든이 너무 작음: %d", len(want))
	}
	for key, row := range want {
		if newKey, moved := intentionalRouteChanges[key]; moved {
			if _, ok := now[newKey]; !ok {
				t.Errorf("의도한 변경 %s → %s 의 새 라우트가 없음", key, newKey)
			}
			continue
		}
		got, ok := now[key]
		if !ok {
			t.Errorf("골든의 라우트가 사라짐: %s", key)
			continue
		}
		if !reflect.DeepEqual(got, row) {
			t.Errorf("라우트 %s 의 역할별 허용이 달라짐\n 이관 전 %v\n 이관 뒤 %v", key, row, got)
		}
	}
}
