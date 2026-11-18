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

// AI 도구(MCP) 경로 기록 1건에는 tokenName 이 붙고(폐기된 토큰도), 웹 경로 기록에는 붙지 않는다. 존재하지 않는 토큰 번호는 이름이 비어 있다.
func TestAuditLogTokenNameOnMCPRowsOnly(t *testing.T) {
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
	raw, info, err := d.Tokens.Issue(ctx, staff, "Claude Code 노트북", 0)
	if err != nil {
		t.Fatal(err)
	}
	rec := func(p contract.Principal, action string, diff []audit.DiffItem) {
		t.Helper()
		if err := d.Txs.WithTx(ctx, func(tx contract.Tx) error {
			return d.Audit.Record(ctx, tx, audit.Entry{Actor: p, Action: action, TargetType: audit.TargetPolicy, TargetID: "x", Result: audit.ResultSuccess, Diff: diff})
		}); err != nil {
			t.Fatal(err)
		}
	}
	mcp := staff
	mcp.Channel = contract.ChannelMCP
	rec(mcp, "mcp.tok_named", []audit.DiffItem{{Key: audit.DiffKeyTokenID, New: strconv.FormatInt(info.ID, 10)}})
	rec(mcp, "mcp.tok_unknown", []audit.DiffItem{{Key: audit.DiffKeyTokenID, New: "999999"}})
	web := staff
	web.Channel = contract.ChannelWeb
	rec(web, "policy.web_row", nil)
	if err := d.Tokens.Revoke(ctx, staff, info.ID); err != nil {
		t.Fatal(err)
	}
	atok, _, err := d.Tokens.Issue(ctx, auditor, "auditor", 0)
	if err != nil {
		t.Fatal(err)
	}
	get := func(action string) (name, label, body string) {
		req := httptest.NewRequest("GET", "/api/audit-logs?action="+action, nil)
		req.Header.Set("Authorization", "Bearer "+atok)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("%d %s", w.Code, w.Body.String())
		}
		var out struct {
			Records []struct {
				TokenName  string `json:"tokenName"`
				TokenLabel string `json:"tokenLabel"`
			} `json:"records"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || len(out.Records) != 1 {
			t.Fatalf("%s: %v %s", action, err, w.Body.String())
		}
		return out.Records[0].TokenName, out.Records[0].TokenLabel, w.Body.String()
	}
	if name, _, body := get("mcp.tok_named"); name != "Claude Code 노트북" || strings.Contains(body, raw) {
		t.Errorf("폐기된 토큰 이름 %q (원문 노출 여부 확인)", name)
	}
	if name, label, _ := get("mcp.tok_unknown"); name != "" || label != "" {
		t.Errorf("없는 토큰은 이름이 비어야 함: %q %q", name, label)
	}
	if name, label, _ := get("policy.web_row"); name != "" || label != "" {
		t.Errorf("웹 경로 기록에 토큰 이름이 붙음: %q %q", name, label)
	}
}
