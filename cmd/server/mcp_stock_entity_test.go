package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/choibongseok/procure-ledger/internal/bootstrap"
	"github.com/choibongseok/procure-ledger/internal/config"
	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/dbtest"
)

// B44 후속: 내장 MCP 도구 list_stock_alerts 도 법인별이다. entity 인자(법인 코드)로 고른 법인의 창고 재고만 나온다.
func TestBuiltinListStockAlertsIsEntityScoped(t *testing.T) {
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
	exec := func(q string, args ...any) int64 {
		t.Helper()
		var id int64
		if err := d.DB.QueryRowContext(ctx, q+` RETURNING id`, args...).Scan(&id); err != nil {
			t.Fatalf("시드: %v\n%s", err, q)
		}
		return id
	}
	abc := exec(`INSERT INTO entities (code, name, is_default, active, version, created_at, updated_at) VALUES ('ABC', '계열사', 0, 1, 1, 'x', 'x')`)
	whA := exec(`INSERT INTO warehouses (code, name, entity_id, created_at, updated_at) VALUES ('WH-A', '본사창고', 1, 't', 't')`)
	whB := exec(`INSERT INTO warehouses (code, name, entity_id, created_at, updated_at) VALUES ('WH-B', '계열사창고', ?, 't', 't')`, abc)
	soon := time.Now().AddDate(0, 0, 5).Format("2006-01-02")
	item := exec(`INSERT INTO items (sku, name, safety_stock, track_lot) VALUES ('MCP-STK', 'MCP 교차 품목', 20, 1)`)
	lotA := exec(`INSERT INTO lots (item_id, lot_no, expiry_date) VALUES (?, 'MCP-LOT-A', ?)`, item, soon)
	lotB := exec(`INSERT INTO lots (item_id, lot_no, expiry_date) VALUES (?, 'MCP-LOT-B', ?)`, item, soon)
	var p contract.Principal
	var role string
	if err := d.DB.QueryRowContext(ctx, `SELECT id, role, department_id FROM users WHERE email = 'admin@pl.test'`).Scan(&p.UserID, &role, &p.DepartmentID); err != nil {
		t.Fatal(err)
	}
	p.Role, p.Name = contract.Role(role), "admin"
	for _, r := range []struct{ lot, wh, qty int64 }{{lotA, whA, 4}, {lotB, whB, 9}} {
		exec(`INSERT INTO stock_ledger (item_id, lot_id, qty_delta, reason, actor_id, warehouse_id) VALUES (?, ?, ?, 'receipt', ?, ?)`, item, r.lot, r.qty, p.UserID, r.wh)
	}
	for _, ent := range []int64{1, abc} {
		exec(`INSERT INTO role_grants (role_id, target_type, target_id, source, created_at, scope, scope_entity_id)
			SELECT id, 'user', ?, 'manual', 't', 'entity', ? FROM roles WHERE key = 'admin'`, p.UserID, ent)
	}
	tok, _, err := d.Tokens.Issue(ctx, p, "재고 법인 시험", 0)
	if err != nil {
		t.Fatal(err)
	}
	h := handlerFor(buildRoutes(cfg, d), d)
	call := func(args any) string {
		body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": "list_stock_alerts", "arguments": args}})
		req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		req.Header.Set("Authorization", "Bearer "+tok)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		raw, _ := io.ReadAll(w.Body)
		return string(raw)
	}
	for ent, want := range map[string]struct{ has, not string }{
		"MAIN": {"MCP-LOT-A", "MCP-LOT-B"},
		"ABC":  {"MCP-LOT-B", "MCP-LOT-A"},
	} {
		out := call(map[string]any{"entity": ent, "within_days": 30})
		if strings.Contains(out, `"isError":true`) || !strings.Contains(out, want.has) || strings.Contains(out, want.not) {
			t.Errorf("list_stock_alerts(%s): %s", ent, out)
		}
	}
}
