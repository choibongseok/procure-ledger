package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/bootstrap"
	"github.com/choibongseok/procure-ledger/internal/config"
	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/dbtest"
)

// 감사 로그 응답에 표시용 사용자 이름과 토큰 이름·앞자리가 붙는다(해시 대상 필드는 그대로, 토큰 원문·해시는 나가지 않는다).
func TestAuditLogResponseCarriesDisplayNames(t *testing.T) {
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
	staff, auditor := principal("staff@pl.test"), principal("auditor@pl.test")
	raw, info, err := d.Tokens.Issue(ctx, staff, "표시용 토큰", 0)
	if err != nil {
		t.Fatal(err)
	}
	staff.Channel = contract.ChannelMCP
	err = d.Txs.WithTx(ctx, func(tx contract.Tx) error {
		return d.Audit.Record(ctx, tx, audit.Entry{Actor: staff, Action: "mcp.search_policy", TargetType: audit.TargetPolicy, TargetID: "search",
			Result: audit.ResultSuccess, Diff: []audit.DiffItem{{Key: audit.DiffKeyTokenID, New: strconv.FormatInt(info.ID, 10)}, {Key: audit.DiffKeyDurationMS, New: "5"}}})
	})
	if err != nil {
		t.Fatal(err)
	}
	atok, _, err := d.Tokens.Issue(ctx, auditor, "auditor", 0)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("GET", "/api/audit-logs?action=mcp.search_policy", nil)
	req.Header.Set("Authorization", "Bearer "+atok)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	var out struct {
		Records []struct {
			ActorName  string `json:"actorName"`
			TokenLabel string `json:"tokenLabel"`
		} `json:"records"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || len(out.Records) != 1 {
		t.Fatalf("%v %s", err, w.Body.String())
	}
	if out.Records[0].ActorName != staff.Name {
		t.Errorf("사용자 이름 %q, 기대 %q", out.Records[0].ActorName, staff.Name)
	}
	if !strings.Contains(out.Records[0].TokenLabel, "표시용 토큰") || !strings.Contains(out.Records[0].TokenLabel, info.Prefix) {
		t.Errorf("토큰 표시 %q", out.Records[0].TokenLabel)
	}
	if strings.Contains(w.Body.String(), raw) {
		t.Error("토큰 원문이 응답에 있음")
	}
	if res, err := d.AuditReader.Verify(ctx); err != nil || !res.ChainOK {
		t.Fatalf("체인: %+v %v", res, err)
	}
}
