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

// B49 AC-4909·4913: 실제 서비스 조립 위에서 get_ai_cost_summary 의 노출·권한·내용·감사를 확인한다.
// 직원은 부를 수 없다(report:read 없음, 거부는 감사에 남는다). 재무·관리자·감사는 본인 부서가 기본이고 다른 부서는 cost:read 가 있어 볼 수 있다.
func TestMCPAICostSummaryExposureAndScope(t *testing.T) {
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
		p    contract.Principal
		tok  string
		dept string
	}
	mk := func(email string) who {
		var p contract.Principal
		var role string
		if err := d.DB.QueryRowContext(ctx, `SELECT id, role, department_id FROM users WHERE email = ?`, email).Scan(&p.UserID, &role, &p.DepartmentID); err != nil {
			t.Fatal(err)
		}
		p.Role, p.Name, p.Channel = contract.Role(role), email, contract.ChannelWeb
		tok, _, err := d.Tokens.Issue(ctx, p, "AI 비용 시험", 0)
		if err != nil {
			t.Fatal(err)
		}
		var dept string
		_ = d.DB.QueryRowContext(ctx, `SELECT COALESCE((SELECT s.name FROM org_unit_spans s WHERE s.unit_id = ? ORDER BY s.valid_from DESC LIMIT 1), (SELECT name FROM departments WHERE id = ?))`, p.DepartmentID, p.DepartmentID).Scan(&dept)
		return who{p: p, tok: tok, dept: dept}
	}
	staff, finance, auditor, admin := mk("staff@pl.test"), mk("finance@pl.test"), mk("auditor@pl.test"), mk("admin@pl.test")
	if staff.p.DepartmentID == finance.p.DepartmentID {
		t.Skip("시험 사용자의 부서가 같아 다른 부서 확인을 할 수 없다")
	}
	month := time.Now().In(contract.KST).Format("2006-01")
	at := time.Now().UTC().Format(time.RFC3339)
	add := func(user, dept int64, purpose string, cost int64) {
		if _, err := d.DB.ExecContext(ctx, `INSERT INTO llm_usage (at, user_id, department_id, role, provider, model, purpose, input_tokens, output_tokens, cost_micro_usd, masked_count, status, entity_id)
			VALUES (?,?,?, 'staff', 'anthropic-main', 'm1', ?, 10, 10, ?, 0, 'ok', 1)`, at, user, dept, purpose, cost); err != nil {
			t.Fatal(err)
		}
	}
	add(staff.p.UserID, staff.p.DepartmentID, "docverify.extract", 2_000_000)
	add(staff.p.UserID, staff.p.DepartmentID, "knowledge.answer", 500_000)
	add(finance.p.UserID, finance.p.DepartmentID, "eval.run", 9_000_000)

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
	listed := func(w who) bool {
		for _, tl := range rpc(w, "tools/list", map[string]any{}).Result.Tools {
			if tl.Name == contract.ToolGetAICostSummary {
				return true
			}
		}
		return false
	}
	call := func(w who, args map[string]any) rpcResult {
		return rpc(w, "tools/call", map[string]any{"name": contract.ToolGetAICostSummary, "arguments": args})
	}

	// 내장 도구는 목록에는 모두에게 보이고 호출 때 권한으로 거부된다(사용자 정의 도구만 목록에서 거른다).
	for name, w := range map[string]who{"finance": finance, "auditor": auditor, "admin": admin} {
		if !listed(w) {
			t.Fatalf("%s 에게 AI 비용 도구가 안 보인다", name)
		}
	}
	if r := call(staff, map[string]any{}); !r.Result.IsError || !strings.Contains(r.text(), "권한") {
		t.Fatalf("직원 호출은 거부돼야 한다: %+v %s", r.Result.IsError, r.text())
	}

	// 재무 본인 부서 기본: eval.run 9달러만. 감사인은 본인 부서(비용 없음)도 열람할 수 있다.
	r := call(finance, map[string]any{})
	var out map[string]any
	if r.Result.IsError || json.Unmarshal([]byte(r.text()), &out) != nil || out["cost_usd_micro"] != float64(9_000_000) || out["month"] != month {
		t.Fatalf("재무 본인 부서: %v %s", r.Result.IsError, r.text())
	}
	// cost:read 가 있으면 다른 부서(직원의 부서)도 이름으로 본다: 2.5달러, 용도별 두 줄.
	r = call(finance, map[string]any{"department": staff.dept})
	out = nil
	if r.Result.IsError || json.Unmarshal([]byte(r.text()), &out) != nil || out["cost_usd_micro"] != float64(2_500_000) {
		t.Fatalf("다른 부서(전사 권한): %v %s", r.Result.IsError, r.text())
	}
	if bp, _ := out["by_purpose"].([]any); len(bp) != 2 {
		t.Fatalf("용도별 %v", out["by_purpose"])
	}
	for _, w := range []who{auditor, admin} {
		r = call(w, map[string]any{"department": staff.dept})
		if r.Result.IsError || !strings.Contains(r.text(), "2500000") {
			t.Fatalf("%s 다른 부서: %v %s", w.p.Name, r.Result.IsError, r.text())
		}
	}
	// 없는 부서·잘못된 월.
	if r = call(finance, map[string]any{"department": "없는부서"}); !r.Result.IsError || !strings.Contains(r.text(), "찾을 수 없습니다") {
		t.Fatalf("없는 부서: %s", r.text())
	}
	if r = call(finance, map[string]any{"month": "2026-13"}); !r.Result.IsError {
		t.Fatalf("잘못된 월: %s", r.text())
	}
	// 응답에 프롬프트·사용자 이름·토큰 수가 없다.
	r = call(finance, map[string]any{"department": staff.dept})
	for _, bad := range []string{"input_tokens", "inputTokens", "user", "staff@pl.test", "prompt"} {
		if strings.Contains(r.text(), bad) {
			t.Fatalf("응답에 %q 가 있다: %s", bad, r.text())
		}
	}
	// 감사: 도구 호출은 대상 ai_cost, 상세에 월·부서·금액이 없다.
	var n int
	if err := d.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_logs WHERE action = 'mcp.get_ai_cost_summary' AND target_type = 'ai_cost' AND result = 'success'`).Scan(&n); err != nil || n == 0 {
		t.Fatalf("도구 호출 감사: %d %v", n, err)
	}
	var leaked int
	_ = d.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_logs WHERE action = 'mcp.get_ai_cost_summary' AND (detail LIKE '%2500000%' OR detail LIKE ?)`, "%"+staff.dept+"%").Scan(&leaked)
	if leaked != 0 {
		t.Fatalf("감사 상세에 금액·부서가 있다: %d", leaked)
	}
}
