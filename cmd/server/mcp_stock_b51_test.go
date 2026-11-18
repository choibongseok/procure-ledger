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

	"github.com/choibongseok/procure-ledger/internal/bootstrap"
	"github.com/choibongseok/procure-ledger/internal/config"
	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/dbtest"
)

// B51 5단계(명세 8.3·AC-5143): 재고 관련 MCP 도구 5개(get_stock·list_stock_alerts·get_item_ledger·get_month_close_status·draft_reorder)를
// 두 법인 시드로 cmd/server 실조립·HTTP /mcp·개인 토큰으로 시험한다. 법인 범위가 한 법인(A=MAIN)뿐인 사용자는 B(ABC)의 창고·수량·금액을 한 건도 받지 못한다.
// B 쪽 표식: 창고 이름 "계열창고", 로트 LOT-B, 금액 987654·6993, 단가 777.
func TestStockMCPToolsAreEntityIsolatedB51(t *testing.T) {
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
	must := func(q string, args ...any) {
		t.Helper()
		if _, err := d.DB.ExecContext(ctx, q, args...); err != nil {
			t.Fatalf("시드: %v\n%s", err, q)
		}
	}
	abc := exec(`INSERT INTO entities (code, name, is_default, active, version, created_at, updated_at) VALUES ('ABC', '계열사', 0, 1, 1, 'x', 'x')`)
	whA := exec(`INSERT INTO warehouses (code, name, entity_id, created_at, updated_at) VALUES ('WH-A', '본사창고', 1, 't', 't')`)
	whB := exec(`INSERT INTO warehouses (code, name, entity_id, created_at, updated_at) VALUES ('WH-B', '계열창고', ?, 't', 't')`, abc)
	item := exec(`INSERT INTO items (sku, name, safety_stock, track_lot) VALUES ('B51-MCP', 'B51 교차 품목', 50, 1)`)
	lotA := exec(`INSERT INTO lots (item_id, lot_no) VALUES (?, 'LOT-A')`, item)
	lotB := exec(`INSERT INTO lots (item_id, lot_no) VALUES (?, 'LOT-B')`, item)
	var dept, adminID int64
	if err := d.DB.QueryRowContext(ctx, `SELECT id, department_id FROM users WHERE email = 'admin@pl.test'`).Scan(&adminID, &dept); err != nil {
		t.Fatal(err)
	}
	led := func(lot, wh, qty int64, reason, book string, ent, unit int64, warned int) {
		id := exec(`INSERT INTO stock_ledger (item_id, lot_id, qty_delta, reason, actor_id, warehouse_id, book_date) VALUES (?, ?, ?, ?, ?, ?, ?)`, item, lot, qty, reason, adminID, wh, book)
		must(`INSERT INTO stock_valuation (ledger_id, item_id, book_date, in_unit_cost, unit_cost, amount, bal_qty, bal_amount, avg_cost, warned, entity_id, entity_basis) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1)`,
			id, item, book, unit, unit, qty*unit, qty, qty*unit, unit, warned, ent)
	}
	led(lotA, whA, 4, "receipt", "2026-11-05", 1, 100, 0)
	led(lotB, whB, 9, "receipt", "2026-11-05", abc, 777, 0)
	led(lotB, whB, 1, "adjust", "2026-09-10", abc, 0, 1)                                   // B 만의 평가 경고(열린 달 2026-09)
	for _, c := range []struct{ ent, wh, amt int64 }{{1, whA, 4000}, {abc, whB, 987654}} { // 마감된 2026-10: 기말 금액은 법인마다 다르다
		exec(`INSERT INTO month_close_lines (month, version, item_id, warehouse_id, open_qty, open_amount, in_qty, in_amount, out_qty, out_amount, transfer_qty, transfer_amount, adjust_qty, adjust_amount, close_qty, close_amount, avg_cost, entity_id)
			VALUES ('2026-10', 1, ?, ?, 0,0,0,0,0,0,0,0,0,0, 1, ?, 1, ?)`, item, c.wh, c.amt, c.ent)
		must(`INSERT INTO month_closes (month, status, version, closed_by, closed_at, entity_id) VALUES ('2026-10', 'closed', 1, ?, '2026-11-01T00:00:00Z', ?)`, adminID, c.ent)
	}
	mkUser := func(email string, scopes map[string][]int64) string {
		id := exec(`INSERT INTO users (email, name, role, department_id) VALUES (?, ?, 'staff', ?)`, email, email, dept)
		for role, ents := range scopes {
			for _, e := range ents {
				exec(`INSERT INTO role_grants (role_id, target_type, target_id, source, created_at, scope, scope_entity_id)
					SELECT id, 'user', ?, 'manual', 't', 'entity', ? FROM roles WHERE key = ?`, id, e, role)
			}
		}
		p := contract.Principal{UserID: id, Role: contract.RoleStaff, Name: email, DepartmentID: dept}
		tok, _, err := d.Tokens.Issue(ctx, p, "B51 재고 시험", 0)
		if err != nil {
			t.Fatal(err)
		}
		return tok
	}
	tokA := mkUser("only-a@pl.test", map[string][]int64{"finance": {1}, "staff": {1}}) // A 만, 금액 권한 있음
	tokANoVal := mkUser("only-a-noval@pl.test", map[string][]int64{"staff": {1}})      // A 만, 금액 권한 없음
	tokBoth := mkUser("both@pl.test", map[string][]int64{"admin": {1, abc}})           // 두 법인
	h := handlerFor(buildRoutes(cfg, d), d)
	call := func(tok, tool string, args map[string]any) (string, bool) {
		t.Helper()
		body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": tool, "arguments": args}})
		req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		req.Header.Set("Authorization", "Bearer "+tok)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		raw, _ := io.ReadAll(w.Body)
		s := string(raw)
		if i := strings.Index(s, "data: "); i >= 0 {
			s = s[i+6:]
		}
		var env struct {
			Result struct {
				IsError bool `json:"isError"`
				Content []struct {
					Text string `json:"text"`
				} `json:"content"`
			} `json:"result"`
			Error any `json:"error"`
		}
		if err := json.Unmarshal([]byte(strings.TrimSpace(s)), &env); err != nil {
			t.Fatalf("%s 응답 해석: %v\n%s", tool, err, raw)
		}
		var sb strings.Builder
		for _, c := range env.Result.Content {
			sb.WriteString(c.Text)
		}
		return sb.String(), env.Result.IsError || env.Error != nil
	}
	foreign := []string{"계열창고", "WH-B", "LOT-B", "987654", "6993", "777"}
	noForeign := func(label, text string) {
		t.Helper()
		for _, f := range foreign {
			if strings.Contains(text, f) {
				t.Errorf("%s: 다른 법인 값 %q 이 응답에 있다\n%s", label, f, text)
			}
		}
	}
	itemArg := map[string]any{"item_id": item}
	ledgerArg := map[string]any{"item_id": item, "from": "2026-09-01", "to": "2026-11-30"}
	merge := func(a, b map[string]any) map[string]any {
		m := map[string]any{}
		for k, v := range a {
			m[k] = v
		}
		for k, v := range b {
			m[k] = v
		}
		return m
	}

	// 1) A 만 보는 사용자: 기본 호출·entity=MAIN 모두 A 값만, B 값은 한 건도 없다.
	for _, ent := range []string{"", "MAIN"} {
		extra := map[string]any{}
		if ent != "" {
			extra["entity"] = ent
		}
		label := "A 전용/" + ent
		txt, isErr := call(tokA, "get_stock", merge(itemArg, extra))
		if isErr || !strings.Contains(txt, `"on_hand":4`) {
			t.Errorf("%s get_stock: %v %s", label, isErr, txt)
		}
		noForeign(label+" get_stock", txt)
		txt, isErr = call(tokA, "list_stock_alerts", extra)
		if isErr || !strings.Contains(txt, "B51-MCP") || !strings.Contains(txt, `"shortfall":46`) {
			t.Errorf("%s list_stock_alerts: %v %s", label, isErr, txt)
		}
		noForeign(label+" list_stock_alerts", txt)
		txt, isErr = call(tokA, "get_item_ledger", merge(ledgerArg, extra))
		if isErr || !strings.Contains(txt, "본사창고") {
			t.Errorf("%s get_item_ledger: %v %s", label, isErr, txt)
		}
		noForeign(label+" get_item_ledger", txt)
		txt, isErr = call(tokA, "get_month_close_status", merge(map[string]any{"month": "2026-10"}, extra))
		if isErr || !strings.Contains(txt, `"close_amount":4000`) {
			t.Errorf("%s get_month_close_status(마감 달): %v %s", label, isErr, txt)
		}
		noForeign(label+" get_month_close_status", txt)
		txt, isErr = call(tokA, "get_month_close_status", merge(map[string]any{"month": "2026-09"}, extra))
		if isErr || strings.Contains(txt, `"warnings":1`) {
			t.Errorf("%s get_month_close_status(열린 달): B 의 평가 경고가 섞였다 %v %s", label, isErr, txt)
		}
	}
	// 금액 권한이 없으면 같은 법인이어도 기말 금액이 없다.
	if txt, isErr := call(tokANoVal, "get_month_close_status", map[string]any{"month": "2026-10"}); isErr || strings.Contains(txt, "close_amount") || strings.Contains(txt, "4000") {
		t.Errorf("금액 권한 없음: %v %s", isErr, txt)
	}

	// 2) 범위 밖 법인 코드·모르는 코드·"*" 는 같은 거부 응답이고 어떤 재고 값도 싣지 않는다(명세 8.3: pickEntity 현행 유지).
	var refusal string
	for _, ent := range []string{"ABC", "abc", "NOPE", "*"} {
		for _, c := range []struct {
			tool string
			args map[string]any
		}{{"get_stock", itemArg}, {"list_stock_alerts", map[string]any{}}, {"get_item_ledger", ledgerArg}, {"get_month_close_status", map[string]any{"month": "2026-10"}}, {"draft_reorder", map[string]any{"item_ids": []int64{item}}}} {
			txt, isErr := call(tokA, c.tool, merge(c.args, map[string]any{"entity": ent}))
			if !isErr || !strings.Contains(txt, "찾을 수 없는 법인") {
				t.Errorf("%s entity=%s: 거부가 아니다 %v %s", c.tool, ent, isErr, txt)
			}
			noForeign(c.tool+" entity="+ent, txt)
			if refusal == "" {
				refusal = txt
			} else if txt != refusal {
				t.Errorf("%s entity=%s: 거부 문구가 다르다(존재 여부 노출) %q != %q", c.tool, ent, txt, refusal)
			}
		}
	}
	var drafts int
	_ = d.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM purchase_requests WHERE title = '안전재고 재주문'`).Scan(&drafts)
	if drafts != 0 {
		t.Errorf("거부된 draft_reorder 가 초안을 만들었다: %d", drafts)
	}

	// 3) 재주문 초안은 호출자 법인 기준 부족분(A: 50-4=46)으로 만든다. B 재고(10)로 계산하지 않는다.
	if txt, isErr := call(tokA, "draft_reorder", map[string]any{"item_ids": []int64{item}}); isErr {
		t.Errorf("draft_reorder(A): %s", txt)
	} else {
		var qty int64
		if err := d.DB.QueryRowContext(ctx, `SELECT l.quantity FROM purchase_request_lines l JOIN purchase_requests r ON r.id = l.pr_id WHERE r.title = '안전재고 재주문' AND l.item_id = ?`, item).Scan(&qty); err != nil || qty != 46 {
			t.Errorf("재주문 수량 %d(기대 46) %v", qty, err)
		}
		noForeign("draft_reorder", txt)
	}

	// 4) 두 법인을 다 보는 사용자: entity 로 고른 법인 값만, 법인 코드가 응답에 붙는다.
	for _, w := range []struct {
		ent, onHand, amount, shortfall string
		whName, otherWh, otherLot      string
	}{
		{"MAIN", `"on_hand":4`, `"close_amount":4000`, `"shortfall":46`, "본사창고", "계열창고", "LOT-B"},
		{"ABC", `"on_hand":10`, `"close_amount":987654`, `"shortfall":40`, "계열창고", "본사창고", "LOT-A"},
	} {
		txt, isErr := call(tokBoth, "get_stock", map[string]any{"item_id": item, "entity": w.ent})
		if isErr || !strings.Contains(txt, w.onHand) || !strings.Contains(txt, `"entity":"`+w.ent+`"`) {
			t.Errorf("두 법인/%s get_stock: %s", w.ent, txt)
		}
		if txt, _ = call(tokBoth, "list_stock_alerts", map[string]any{"entity": w.ent}); !strings.Contains(txt, w.shortfall) {
			t.Errorf("두 법인/%s list_stock_alerts: %s", w.ent, txt)
		}
		txt, _ = call(tokBoth, "get_item_ledger", map[string]any{"item_id": item, "from": "2026-09-01", "to": "2026-11-30", "entity": w.ent})
		if !strings.Contains(txt, w.whName) || strings.Contains(txt, w.otherWh) {
			t.Errorf("두 법인/%s get_item_ledger: %s", w.ent, txt)
		}
		txt, _ = call(tokBoth, "get_month_close_status", map[string]any{"month": "2026-10", "entity": w.ent})
		if !strings.Contains(txt, w.amount) {
			t.Errorf("두 법인/%s get_month_close_status: %s", w.ent, txt)
		}
		if w.ent == "ABC" && strings.Contains(txt, `"close_amount":4000`) {
			t.Errorf("기말 금액이 법인을 건너 섞였다: %s", txt)
		}
	}
	// B 의 열린 달(2026-09)에는 B 의 평가 경고가 보이고, A 에는 없다(법인 기준 건수).
	if txt, _ := call(tokBoth, "get_month_close_status", map[string]any{"month": "2026-09", "entity": "ABC"}); !strings.Contains(txt, `"warnings":1`) {
		t.Errorf("ABC 열린 달 경고 건수: %s", txt)
	}
	if txt, _ := call(tokBoth, "get_month_close_status", map[string]any{"month": "2026-09", "entity": "MAIN"}); strings.Contains(txt, `"warnings":1`) {
		t.Errorf("MAIN 열린 달에 ABC 경고가 섞였다: %s", txt)
	}
}
