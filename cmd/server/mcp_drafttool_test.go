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
	"github.com/choibongseok/procure-ledger/internal/mcptool"
)

// AC-4807·4808·4811: 초안 도구 — 2인 확인으로 켜고, MCP 로 호출하면 호출자 권한으로 임시저장 초안만 만들며(상신·승인 없음),
// idempotency_key 로 중복 초안이 없고, 권한이 없는 사람은 거부된다. 호출 감사는 실행 전 요청 기록과 결과 기록이 모두 남는다.
func TestMCPDraftToolEndToEnd(t *testing.T) {
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

	mk := func(email string) (contract.Principal, string) {
		var p contract.Principal
		var role string
		if err := d.DB.QueryRowContext(ctx, `SELECT id, role, department_id FROM users WHERE email = ?`, email).Scan(&p.UserID, &role, &p.DepartmentID); err != nil {
			t.Fatal(err)
		}
		p.Role, p.Name, p.Channel = contract.Role(role), email, contract.ChannelWeb
		tok, _, err := d.Tokens.Issue(ctx, p, "초안 도구 시험", 0)
		if err != nil {
			t.Fatal(err)
		}
		return p, tok
	}
	staff, staffTok := mk("staff@pl.test")
	_, audTok := mk("auditor@pl.test")
	admin1, _ := mk("admin@pl.test")
	// 두 번째 관리자(2인 확인용).
	var a2id int64
	if err := d.DB.QueryRowContext(ctx, `INSERT INTO users (email, name, role, department_id) VALUES ('admin2@pl.test', '관리자2', 'admin', ?) RETURNING id`, admin1.DepartmentID).Scan(&a2id); err != nil {
		t.Fatal(err)
	}
	admin2 := admin1
	admin2.UserID, admin2.Name = a2id, "admin2@pl.test"

	rpc := func(tok, method string, params any) rpcResult {
		body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
		req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		req.Header.Set("Authorization", "Bearer "+tok)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		raw, _ := io.ReadAll(rec.Body)
		var out rpcResult
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatalf("%s 응답 해석 실패: %s", method, raw)
		}
		return out
	}
	listed := func(tok string) bool {
		for _, tl := range rpc(tok, "tools/list", map[string]any{}).Result.Tools {
			if tl.Name == "custom_make_pr" {
				return true
			}
		}
		return false
	}
	call := func(tok string, args any) rpcResult {
		return rpc(tok, "tools/call", map[string]any{"name": "custom_make_pr", "arguments": args})
	}

	if _, err := d.DB.ExecContext(ctx, `INSERT INTO vendors (name) VALUES ('한빛상사')`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.DB.ExecContext(ctx, `INSERT INTO items (sku, name) VALUES ('PAPER', '복사용지')`); err != nil {
		t.Fatal(err)
	}
	var vendorID, itemID int64
	_ = d.DB.QueryRowContext(ctx, `SELECT id FROM vendors LIMIT 1`).Scan(&vendorID)
	_ = d.DB.QueryRowContext(ctx, `SELECT id FROM items LIMIT 1`).Scan(&itemID)

	svc, err := mcptool.New(mcptool.Options{DB: d.DB, Txs: d.Txs, Audit: d.Audit, Catalog: mcptool.BuiltinCatalog(d), Masker: d.Masker})
	if err != nil {
		t.Fatal(err)
	}
	ctxOf := func(p contract.Principal) context.Context {
		p.Perms = contract.NewPermSet(contract.AllPermissions...)
		return contract.WithPrincipal(ctx, p)
	}
	c1, c2 := ctxOf(admin1), ctxOf(admin2)
	def := map[string]any{"format": "pl-mcp-tool/1", "name": "custom_make_pr", "kind": "draft", "description": "복사용지 구매요청 초안을 만든다.", "target": "purchase_request_draft",
		"input": map[string]any{"title": map[string]any{"type": "string", "required": true}, "qty": map[string]any{"type": "int", "min": 1, "max": 100, "required": true}},
		"map": map[string]any{
			"vendor_id": map[string]any{"value": vendorID}, "item_id": map[string]any{"value": itemID}, "unit_price": map[string]any{"value": 20000},
			"title": map[string]any{"from": "title"}, "quantity": map[string]any{"from": "qty"}, "reason": map[string]any{"value": "AI 도구로 만든 초안"},
		}}
	rawDef, _ := json.Marshal(def)
	tool, err := svc.Save(c1, mcptool.SaveInput{Title: "구매요청 만들기", Definition: rawDef})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Test(c1, mcptool.TestInput{Definition: rawDef, Input: map[string]any{"title": "시험", "qty": 2}}); err != nil {
		t.Fatal(err)
	}
	var prs int
	_ = d.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM purchase_requests`).Scan(&prs)
	if prs != 0 {
		t.Fatalf("시험 실행이 초안을 만들었음: %d", prs)
	}
	// 켜지기 전에는 목록에도 없다. 켜기는 요청 → 다른 관리자의 확인이다.
	if listed(staffTok) {
		t.Fatal("꺼진 초안 도구가 목록에 있음")
	}
	if _, err := svc.RequestEnable(c1, tool.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ConfirmEnable(c1, tool.ID, mcptool.EnableOpts{}); err == nil {
		t.Fatal("요청자가 직접 확인함")
	}
	if on, err := svc.ConfirmEnable(c2, tool.ID, mcptool.EnableOpts{}); err != nil || !on.Enabled {
		t.Fatalf("다른 관리자의 확인: %v", err)
	}

	// 목록: purchase:create 가 있는 직원에게만 보인다(감사는 없음).
	if !listed(staffTok) || listed(audTok) {
		t.Fatalf("초안 도구 목록 직원=%v 감사=%v", listed(staffTok), listed(audTok))
	}
	if r := call(audTok, map[string]any{"title": "감사의 시도", "qty": 1}); !r.Result.IsError || !strings.Contains(r.text(), "권한") {
		t.Fatalf("감사의 호출: %+v %s", r.Result.IsError, r.text())
	}
	_ = d.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM purchase_requests`).Scan(&prs)
	if prs != 0 {
		t.Fatalf("권한 없는 호출이 초안을 만들었음: %d", prs)
	}

	// 직원의 호출: 호출자 권한으로 임시저장 초안만 만든다(상신·결재 없음).
	r := call(staffTok, map[string]any{"title": "복사용지 10박스", "qty": 10, "idempotency_key": "k-1"})
	if r.Result.IsError || !strings.Contains(r.text(), `"replay":false`) || !strings.Contains(r.text(), "확정(상신·승인)은 사람이 화면에서") || !strings.Contains(r.text(), `"status":"draft"`) {
		t.Fatalf("직원의 호출: %+v %s", r.Result.IsError, r.text())
	}
	var no, status string
	var requester int64
	var steps int
	if err := d.DB.QueryRowContext(ctx, `SELECT no, status, requester_id FROM purchase_requests WHERE title = '복사용지 10박스'`).Scan(&no, &status, &requester); err != nil {
		t.Fatal(err)
	}
	_ = d.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM approval_steps`).Scan(&steps)
	if status != "draft" || requester != staff.UserID || steps != 0 || !strings.Contains(r.text(), no) {
		t.Fatalf("초안: 상태 %s 작성자 %d 결재 단계 %d", status, requester, steps)
	}
	// 같은 idempotency_key 는 같은 초안을 돌려주고 새로 만들지 않는다. 다른 키는 새 초안.
	r = call(staffTok, map[string]any{"title": "복사용지 10박스", "qty": 10, "idempotency_key": "k-1"})
	if r.Result.IsError || !strings.Contains(r.text(), `"replay":true`) || !strings.Contains(r.text(), no) {
		t.Fatalf("재호출: %+v %s", r.Result.IsError, r.text())
	}
	var same int
	_ = d.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM purchase_requests WHERE title = '복사용지 10박스'`).Scan(&same)
	if same != 1 {
		t.Fatalf("같은 키로 초안 %d건", same)
	}
	if r = call(staffTok, map[string]any{"title": "복사용지 10박스", "qty": 10, "idempotency_key": "k-2"}); r.Result.IsError {
		t.Fatalf("다른 키: %s", r.text())
	}
	_ = d.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM purchase_requests WHERE title = '복사용지 10박스'`).Scan(&same)
	if same != 2 {
		t.Fatalf("다른 키 뒤 초안 %d건", same)
	}
	// 입력 오류·알 수 없는 인자·잘못된 키는 초안을 만들지 않는다.
	for _, args := range []map[string]any{{"title": "x", "qty": 0}, {"title": "x", "qty": 1, "status": "approved"}, {"title": "x"}, {"title": "x", "qty": 1, "idempotency_key": strings.Repeat("k", 41)}} {
		if r = call(staffTok, args); !r.Result.IsError {
			t.Fatalf("잘못된 호출이 통과함: %v", args)
		}
	}
	_ = d.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM purchase_requests WHERE title = 'x'`).Scan(&same)
	if same != 0 {
		t.Fatalf("잘못된 호출이 초안 %d건을 만듦", same)
	}

	// 감사: 실행 전 요청 기록과 결과 기록이 있고 결과 기록에 정의 버전이 있으며 입력값은 없다.
	var requested, resultRows int
	var diff string
	_ = d.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_logs WHERE channel = 'mcp' AND action = 'mcp.custom_make_pr' AND detail LIKE '%requested%'`).Scan(&requested)
	_ = d.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_logs WHERE channel = 'mcp' AND action = 'mcp.custom_make_pr.result' AND result = 'success'`).Scan(&resultRows)
	_ = d.DB.QueryRowContext(ctx, `SELECT diff FROM audit_logs WHERE channel = 'mcp' AND action = 'mcp.custom_make_pr.result' AND result = 'success' ORDER BY seq DESC LIMIT 1`).Scan(&diff)
	if requested < 3 || resultRows != 3 || !strings.Contains(diff, "tool_version") || strings.Contains(diff, "복사용지") {
		t.Fatalf("호출 감사: 요청 %d 결과 %d diff %s", requested, resultRows, diff)
	}
	// 켜진 초안 도구의 새 버전은 도구를 끈다(목록에서 사라진다).
	def["description"] = "복사용지 구매요청 초안을 만든다(수정)."
	rawDef2, _ := json.Marshal(def)
	if _, err := svc.Save(c1, mcptool.SaveInput{ID: tool.ID, Title: "구매요청 만들기", Definition: rawDef2}); err != nil {
		t.Fatal(err)
	}
	if listed(staffTok) {
		t.Fatal("새 버전을 저장했는데 초안 도구가 계속 켜져 있음")
	}
}
