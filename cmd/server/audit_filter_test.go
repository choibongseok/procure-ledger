package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/bootstrap"
	"github.com/choibongseok/procure-ledger/internal/config"
	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/dbtest"
)

// 감사 로그 조회는 사용자 이름(일부)과 경로(web·mcp·llmgw)로 거를 수 있다. 와일드카드 문자는 글자 그대로 취급한다.
// 조회 권한이 없는 역할(직원)과 admin 의 검증 요청은 거부된다(admin 에게 audit:verify 를 주지 않는다).
func TestAuditLogFilterByActorNameAndChannel(t *testing.T) {
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
	h := handlerFor(buildRoutes(cfg, d), d)

	principal := func(email string) contract.Principal {
		var p contract.Principal
		var role string
		if err := d.DB.QueryRowContext(ctx, `SELECT id, name, role, department_id FROM users WHERE email = ?`, email).Scan(&p.UserID, &p.Name, &role, &p.DepartmentID); err != nil {
			t.Fatal(err)
		}
		p.Role = contract.Role(role)
		return p
	}
	record := func(p contract.Principal, ch contract.Channel, action string) {
		p.Channel = ch
		err := d.Txs.WithTx(ctx, func(tx contract.Tx) error {
			return d.Audit.Record(ctx, tx, audit.Entry{Actor: p, Action: action, TargetType: audit.TargetPolicy, TargetID: "x", Result: audit.ResultSuccess})
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	staff, staff2 := principal("staff@pl.test"), principal("staff2@pl.test")
	record(staff, contract.ChannelMCP, "filter.a")
	record(staff, contract.ChannelWeb, "filter.b")
	record(staff2, contract.ChannelWeb, "filter.c")

	token := func(email string) string {
		raw, _, err := d.Tokens.Issue(ctx, principal(email), "t-"+email, 0)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	atok := token("auditor@pl.test")
	get := func(tok, query string) (int, []string) {
		req := httptest.NewRequest("GET", "/api/audit-logs?"+query, nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		var out struct {
			Records []struct {
				Action string `json:"action"`
			} `json:"records"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		var acts []string
		for _, r := range out.Records {
			acts = append(acts, r.Action)
		}
		return w.Code, acts
	}
	has := func(acts []string, a string) bool {
		for _, x := range acts {
			if x == a {
				return true
			}
		}
		return false
	}

	code, acts := get(atok, "actorName="+"%EA%B5%AC%EB%A7%A4%EC%A7%81%EC%9B%90&limit=200") // 구매직원
	if code != http.StatusOK || !has(acts, "filter.a") || !has(acts, "filter.b") || has(acts, "filter.c") {
		t.Errorf("이름 필터: %d %v", code, acts)
	}
	code, acts = get(atok, "actorName="+"%EA%B5%AC%EB%A7%A4&channel=mcp&limit=200") // 이름 일부 + 경로
	if code != http.StatusOK || !has(acts, "filter.a") || has(acts, "filter.b") || has(acts, "filter.c") {
		t.Errorf("이름 일부+경로 필터: %d %v", code, acts)
	}
	code, acts = get(atok, "actorName=%25") // '%' 는 글자 그대로라 아무도 맞지 않는다
	if code != http.StatusOK || len(acts) != 0 {
		t.Errorf("와일드카드가 글자 그대로 취급되지 않음: %d %v", code, acts)
	}
	code, acts = get(atok, "actorName=zzzz")
	if code != http.StatusOK || len(acts) != 0 {
		t.Errorf("없는 이름: %d %v", code, acts)
	}

	// 권한 없는 역할은 조회가 거부된다.
	if code, _ := get(token("staff@pl.test"), "actorName=x"); code != http.StatusForbidden {
		t.Errorf("직원의 감사 로그 조회: %d, 기대 403", code)
	}
	// admin 은 열람만 되고 검증은 거부된다.
	adm := token("admin@pl.test")
	if code, _ := get(adm, "channel=web"); code != http.StatusOK {
		t.Errorf("admin 열람: %d", code)
	}
	req := httptest.NewRequest("POST", "/api/audit-logs/verify", nil)
	req.Header.Set("Authorization", "Bearer "+adm)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Errorf("admin 의 검증: %d, 기대 403", w.Code)
	}
}
