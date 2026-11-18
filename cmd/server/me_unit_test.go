package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/auth"
	"github.com/choibongseok/procure-ledger/internal/bootstrap"
	"github.com/choibongseok/procure-ledger/internal/config"
	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/dbtest"
	"github.com/choibongseok/procure-ledger/internal/org"
)

// 상단 바의 소속(/api/me 의 unitName)은 조직도 주소속이고, 로그인 화면 개발 계정 목록의 부서(/api/auth/config)와 같은 값이다.
func TestMeUnitNameMatchesOrgChartAndLoginList(t *testing.T) {
	dbtest.SetEnv(t)
	t.Setenv("PL_DEV_IDP", "1")
	t.Setenv("PL_LLM_PROVIDER", "mock")
	cfg := config.Load()
	ctx := context.Background()
	d, closeDB, err := bootstrap.Build(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer closeDB()
	// main 과 같이 개발용 조직도를 넣는다.
	if _, err := d.Org.(*org.Service).SeedDev(ctx); err != nil {
		t.Fatal(err)
	}
	h := handlerFor(buildRoutes(cfg, d), d)

	// 로그인 화면 목록
	req := httptest.NewRequest("GET", "/api/auth/config", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	var conf struct {
		DevAccounts []struct {
			Email      string `json:"email"`
			Department string `json:"department"`
		} `json:"devAccounts"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &conf); err != nil || len(conf.DevAccounts) != len(auth.DevAccounts) {
		t.Fatalf("%v %s", err, w.Body.String())
	}
	for _, a := range conf.DevAccounts {
		var p contract.Principal
		var role string
		if err := d.DB.QueryRowContext(ctx, `SELECT id, name, role, department_id FROM users WHERE email = ?`, a.Email).Scan(&p.UserID, &p.Name, &role, &p.DepartmentID); err != nil {
			t.Fatal(err)
		}
		p.Role = contract.Role(role)
		raw, _, err := d.Tokens.Issue(ctx, p, "unit-"+a.Email, 0)
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest("GET", "/api/me", nil)
		req.Header.Set("Authorization", "Bearer "+raw)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", a.Email, w.Code, w.Body.String())
		}
		var me struct {
			User struct {
				UnitName string `json:"unitName"`
			} `json:"user"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &me); err != nil {
			t.Fatal(err)
		}
		if me.User.UnitName == "" || me.User.UnitName != a.Department {
			t.Errorf("%s: 상단 바 %q, 로그인 목록 %q", a.Email, me.User.UnitName, a.Department)
		}
	}
}
