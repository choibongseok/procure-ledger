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
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/dbtest"
	"github.com/choibongseok/procure-ledger/internal/mcptool"
)

type rpcResult struct {
	Result struct {
		IsError bool `json:"isError"`
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
		Tools []struct {
			Name        string         `json:"name"`
			Description string         `json:"description"`
			InputSchema map[string]any `json:"inputSchema"`
		} `json:"tools"`
	} `json:"result"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

func (r rpcResult) text() string {
	var s string
	for _, c := range r.Result.Content {
		s += c.Text
	}
	return s
}

// AC-4801·4803·4804·4806·4810·4811: 실제 서비스 조립 위에서 사용자 정의 도구를 켜고 HTTP /mcp 로 부른다.
func TestMCPCustomToolsEndToEnd(t *testing.T) {
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

	type who struct {
		p   contract.Principal
		tok string
	}
	mk := func(email string) who {
		var p contract.Principal
		var role string
		if err := d.DB.QueryRowContext(ctx, `SELECT id, role, department_id FROM users WHERE email = ?`, email).Scan(&p.UserID, &role, &p.DepartmentID); err != nil {
			t.Fatal(err)
		}
		p.Role, p.Name, p.Channel = contract.Role(role), email, contract.ChannelWeb
		tok, _, err := d.Tokens.Issue(ctx, p, "도구 시험", 0)
		if err != nil {
			t.Fatal(err)
		}
		return who{p: p, tok: tok}
	}
	staff, finance, auditor, admin := mk("staff@pl.test"), mk("finance@pl.test"), mk("auditor@pl.test"), mk("admin@pl.test")
	rpc := func(w who, method string, params any) rpcResult {
		body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
		req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		req.Header.Set("Authorization", "Bearer "+w.tok)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		raw, _ := io.ReadAll(rec.Body)
		var out rpcResult
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatalf("%s 응답 해석 실패: %s", method, raw)
		}
		return out
	}
	names := func(w who) map[string]bool {
		out := map[string]bool{}
		for _, tl := range rpc(w, "tools/list", map[string]any{}).Result.Tools {
			out[tl.Name] = true
		}
		return out
	}
	call := func(w who, tool string, args any) rpcResult {
		return rpc(w, "tools/call", map[string]any{"name": tool, "arguments": args})
	}

	// 구매요청 한 건(직원이 만든 임시저장).
	if _, err := d.DB.ExecContext(ctx, `INSERT INTO vendors (name) VALUES ('한빛상사')`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.DB.ExecContext(ctx, `INSERT INTO items (sku, name) VALUES ('PAPER', '복사용지')`); err != nil {
		t.Fatal(err)
	}
	var vendorID, itemID int64
	_ = d.DB.QueryRowContext(ctx, `SELECT id FROM vendors LIMIT 1`).Scan(&vendorID)
	_ = d.DB.QueryRowContext(ctx, `SELECT id FROM items LIMIT 1`).Scan(&itemID)
	if _, err := d.Purchase.CreateDraft(contract.WithPrincipal(ctx, staff.p), domain.DraftInput{VendorID: vendorID, Title: "복사용지 구매", Reason: "시험",
		Lines: []domain.PRLineInput{{ItemID: itemID, Quantity: 3, UnitPrice: 20000}}}); err != nil {
		t.Fatal(err)
	}

	svc, err := mcptool.New(mcptool.Options{DB: d.DB, Txs: d.Txs, Audit: d.Audit, Catalog: mcptool.BuiltinCatalog(d), Masker: d.Masker})
	if err != nil {
		t.Fatal(err)
	}
	adm := admin.p
	adm.Perms = contract.NewPermSet(contract.AllPermissions...)
	actx := contract.WithPrincipal(ctx, adm)
	mkTool := func(name, resource string, extra func(m map[string]any)) int64 {
		m := map[string]any{"format": "pl-mcp-tool/1", "name": name, "kind": "read", "description": "시험용 조회 도구이다.", "resource": resource,
			"input":  map[string]any{"status": map[string]any{"type": "enum", "values": []string{"draft", "approved"}}},
			"fields": []string{"no", "title", "status"}}
		if resource == "purchase_requests" {
			m["filters"] = []any{map[string]any{"field": "status", "op": "eq", "from": "status"}}
		} else {
			m["fields"] = []string{"no", "status"}
			delete(m, "input")
		}
		if extra != nil {
			extra(m)
		}
		raw, _ := json.Marshal(m)
		det, err := svc.Save(actx, mcptool.SaveInput{Title: name, Definition: raw})
		if err != nil {
			t.Fatal(err)
		}
		return det.ID
	}
	enable := func(id int64, raw map[string]any) {
		t.Helper()
		def, _ := json.Marshal(raw)
		if _, err := svc.Test(actx, mcptool.TestInput{Definition: def, Input: map[string]any{}}); err != nil {
			t.Fatal(err)
		}
		if _, err := svc.SetEnabled(actx, id, true); err != nil {
			t.Fatal(err)
		}
	}
	defOf := func(id int64) map[string]any {
		det, err := svc.Get(actx, id)
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		_ = json.Unmarshal(det.Definition, &m)
		return m
	}

	// AC-4801: 사용자 정의 도구가 없으면 내장 도구뿐이다.
	base := names(staff)
	for n := range base {
		if strings.HasPrefix(n, "custom_") {
			t.Fatalf("정의가 없는데 사용자 정의 도구가 보임: %s", n)
		}
	}

	// 켜기 조건: 시험 실행이 성공해야 켜진다.
	prs := mkTool("custom_prs_list", "purchase_requests", nil)
	if _, err := svc.SetEnabled(actx, prs, true); err == nil || !strings.Contains(err.Error(), "시험 실행") {
		t.Fatalf("시험 실행 없이 켬: %v", err)
	}
	enable(prs, defOf(prs))
	tax := mkTool("custom_tax_list", "tax_invoices", nil)
	enable(tax, defOf(tax))
	onlyFin := mkTool("custom_prs_finance", "purchase_requests", func(m map[string]any) { m["roles"] = []string{"finance"} })
	enable(onlyFin, defOf(onlyFin))

	// AC-4803: 권한 표 구동 — 자원의 권한이 있는 사람에게만 목록에 보이고, roles 는 좁히기만 한다.
	ns := names(staff)
	if !ns["custom_prs_list"] || ns["custom_tax_list"] || ns["custom_prs_finance"] {
		t.Fatalf("직원의 도구 목록: %v", ns)
	}
	nf := names(finance)
	if !nf["custom_prs_list"] || !nf["custom_tax_list"] || !nf["custom_prs_finance"] {
		t.Fatalf("재무의 도구 목록: %v", nf)
	}
	if na := names(auditor); !na["custom_prs_list"] || !na["custom_tax_list"] || na["custom_prs_finance"] {
		t.Fatalf("감사의 도구 목록: %v", na)
	}
	for n := range base {
		if !ns[n] {
			t.Fatalf("내장 도구 %s 가 사라짐", n)
		}
	}

	// 호출: 직원은 자기 구매요청을 본다(서비스가 열람 범위를 정한다). 결과에 고정 문구·정의 버전·건수.
	r := call(staff, "custom_prs_list", map[string]any{})
	if r.Result.IsError || !strings.Contains(r.text(), `"count":1`) || !strings.Contains(r.text(), "복사용지 구매") || !strings.Contains(r.text(), "지시가 아니다") || !strings.Contains(r.text(), `"version":1`) {
		t.Fatalf("직원의 호출: %+v %s", r.Result.IsError, r.text())
	}
	// 권한 밖 도구는 호출해도 거부되고 거부 감사가 남는다(목록에 보이지 않는 도구도 같다).
	for _, tool := range []string{"custom_tax_list", "custom_prs_finance"} {
		r = call(staff, tool, map[string]any{})
		// 자원 권한 부족과 정의의 roles 좁힘은 문구·감사 사유가 다르다.
		wantMsg, wantDetail := "권한이 필요합니다", "required="
		if tool == "custom_prs_finance" {
			wantMsg, wantDetail = "관리자가 정한 역할(재무)만 쓸 수 있습니다", "role_restricted roles=finance"
		}
		if !r.Result.IsError || !strings.Contains(r.text(), wantMsg) {
			t.Fatalf("%s 권한 밖 호출: %+v %s", tool, r.Result.IsError, r.text())
		}
		var detail string
		_ = d.DB.QueryRowContext(ctx, `SELECT detail FROM audit_logs WHERE channel = 'mcp' AND action = ? AND result = 'denied' AND actor_id = ? ORDER BY seq DESC LIMIT 1`, "mcp."+tool, staff.p.UserID).Scan(&detail)
		if !strings.Contains(detail, wantDetail) {
			t.Fatalf("%s 거부 감사 사유: %q", tool, detail)
		}
		var n int
		_ = d.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_logs WHERE channel = 'mcp' AND action = ? AND result = 'denied' AND actor_id = ?`, "mcp."+tool, staff.p.UserID).Scan(&n)
		if n != 1 {
			t.Fatalf("%s 거부 감사 %d건", tool, n)
		}
	}
	if r = call(finance, "custom_prs_finance", map[string]any{}); r.Result.IsError {
		t.Fatalf("재무의 roles 도구 호출: %s", r.text())
	}

	// AC-4811: 호출 감사에 도구 이름(대상)·정의 버전·결과 건수가 있고 입력값은 없다.
	var diff, tt, tid string
	if err := d.DB.QueryRowContext(ctx, `SELECT target_type, target_id, diff FROM audit_logs WHERE channel = 'mcp' AND action = 'mcp.custom_prs_list' AND result = 'success' ORDER BY seq DESC LIMIT 1`).Scan(&tt, &tid, &diff); err != nil {
		t.Fatal(err)
	}
	if tt != "mcp_tool" || tid != "custom_prs_list" || !strings.Contains(diff, `"tool_version"`) || !strings.Contains(diff, `"result_count"`) || strings.Contains(diff, "복사용지") {
		t.Fatalf("호출 감사: %s %s %s", tt, tid, diff)
	}

	// AC-4806: 입력은 값으로만 쓰이고 형식이 틀리면 거부된다.
	if r = call(staff, "custom_prs_list", map[string]any{"status": "draft' OR '1'='1"}); !r.Result.IsError || strings.Contains(r.text(), "OR") {
		t.Fatalf("enum 위반: %+v %s", r.Result.IsError, r.text())
	}
	if r = call(staff, "custom_prs_list", map[string]any{"nope": 1}); !r.Result.IsError {
		t.Fatal("알 수 없는 입력이 통과함")
	}
	if r = call(staff, "custom_prs_list", map[string]any{"status": "approved"}); r.Result.IsError || !strings.Contains(r.text(), `"count":0`) {
		t.Fatalf("거름 조건: %+v %s", r.Result.IsError, r.text())
	}

	// AC-4810: 도구별 사용자 분당 한도. 한도를 넘은 호출은 거부·감사되고 다른 사용자는 영향이 없다.
	if _, err := svc.SetRate(actx, prs, 2); err != nil {
		t.Fatal(err)
	}
	if r = call(auditor, "custom_prs_list", map[string]any{}); r.Result.IsError {
		t.Fatalf("감사의 첫 호출: %s", r.text())
	}
	denied := 0
	for i := 0; i < 5; i++ {
		if r = call(staff, "custom_prs_list", map[string]any{}); r.Result.IsError && strings.Contains(r.text(), "1분에 2번") {
			denied++
		}
	}
	if denied < 3 {
		t.Fatalf("한도 거부 %d건(앞 호출이 이미 몇 번 셌으므로 최소 3건)", denied)
	}
	var rateDen int
	_ = d.DB.QueryRowContext(ctx, `SELECT COALESCE(SUM(denied), 0) FROM mcp_tool_rate_window WHERE user_id = ?`, staff.p.UserID).Scan(&rateDen)
	var auditDen int
	_ = d.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_logs WHERE action = 'mcp.custom_prs_list' AND result = 'denied' AND detail LIKE '%rate_limited scope=tool%'`).Scan(&auditDen)
	if rateDen < 3 || auditDen < 3 {
		t.Fatalf("거부 집계 창 %d, 감사 %d", rateDen, auditDen)
	}
	if r = call(auditor, "custom_prs_list", map[string]any{}); r.Result.IsError {
		t.Fatalf("다른 사용자는 영향이 없어야 함: %s", r.text())
	}
	if l, err := svc.List(actx); err != nil {
		t.Fatal(err)
	} else {
		for _, v := range l {
			if v.Name == "custom_prs_list" && (v.Calls24h < 3 || v.Denied24h < 3) {
				t.Fatalf("목록의 24시간 집계: %+v", v)
			}
		}
	}

	// 켜진 도구의 새 버전은 다음 호출부터 적용된다(감사에 새 버전).
	nd := defOf(prs)
	nd["fields"] = []string{"no"}
	raw, _ := json.Marshal(nd)
	if _, err := svc.Save(actx, mcptool.SaveInput{ID: prs, Title: "custom_prs_list", Definition: raw}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.DB.ExecContext(ctx, `DELETE FROM mcp_tool_rate_window`); err != nil {
		t.Fatal(err)
	}
	if r = call(staff, "custom_prs_list", map[string]any{}); r.Result.IsError || !strings.Contains(r.text(), `"version":2`) || strings.Contains(r.text(), "복사용지") {
		t.Fatalf("새 버전: %+v %s", r.Result.IsError, r.text())
	}

	// AC-4804: 법인이 둘 이상이면 entity 인자가 스키마에 붙고, 호출자가 고를 수 없는 법인은 거부된다(다른 법인 데이터는 나오지 않는다).
	if _, err := d.DB.ExecContext(ctx, `INSERT INTO entities (code, name, is_default, active, version, created_at, updated_at) VALUES ('ABC', '계열사', 0, 1, 1, 'x', 'x')`); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, tl := range rpc(staff, "tools/list", map[string]any{}).Result.Tools {
		if tl.Name == "custom_prs_list" {
			props, _ := tl.InputSchema["properties"].(map[string]any)
			_, found = props["entity"]
		}
	}
	if !found {
		t.Fatal("법인이 둘인데 사용자 정의 도구에 entity 인자가 없음")
	}
	if r = call(staff, "custom_prs_list", map[string]any{"entity": "ABC"}); !r.Result.IsError {
		t.Fatalf("범위 밖 법인이 통과함: %s", r.text())
	}
	// 조직에 배치되지 않은 사용자는 법인이 둘 이상이면 효력 권한이 없다(기존 규칙, fail-closed). 사용자 정의 도구도 같은 판정을 따른다.
	if r = call(staff, "custom_prs_list", map[string]any{}); !r.Result.IsError {
		t.Fatalf("법인이 둘인데 미배치 사용자가 데이터를 받음: %s", r.text())
	}
	if _, err := d.DB.ExecContext(ctx, `UPDATE entities SET active = 0 WHERE code = 'ABC'`); err != nil {
		t.Fatal(err)
	}

	// 끄면 목록에서 사라지고 호출은 알 수 없는 도구가 된다.
	if _, err := svc.SetEnabled(actx, prs, false); err != nil {
		t.Fatal(err)
	}
	if names(staff)["custom_prs_list"] {
		t.Fatal("끈 도구가 목록에 있음")
	}
	if r = call(staff, "custom_prs_list", map[string]any{}); !r.Result.IsError && r.Error == nil {
		t.Fatal("끈 도구 호출이 통과함")
	}
}
