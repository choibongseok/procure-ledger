package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/bootstrap"
	"github.com/choibongseok/procure-ledger/internal/config"
	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/dbtest"
)

const isolationDoc = "거래명세서\n공급자: 한빛상사\n사업자번호: 123-45-67890\n품목: 볼트 M8 | 수량 10 | 단가 500 | 금액 5000\n합계: 5000"

// 실제 서비스 조립 위에서: 타 부서 PO 로 문서 판정을 요청하면 웹·MCP 모두 404(존재를 숨김)로 끝나고,
// 문서가 저장되지도 LLM 이 호출되지도 않는다. 같은 부서 직원은 정상 판정된다.
func TestDocumentVerifyHidesOtherDepartmentPO(t *testing.T) {
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

	type user struct {
		p   contract.Principal
		tok string
	}
	mk := func(email string) user {
		var p contract.Principal
		var role string
		if err := d.DB.QueryRowContext(ctx, `SELECT id, role, department_id FROM users WHERE email = ?`, email).Scan(&p.UserID, &role, &p.DepartmentID); err != nil {
			t.Fatal(err)
		}
		p.Role, p.Name = contract.Role(role), email
		tok, _, err := d.Tokens.Issue(ctx, p, "iso", 0)
		if err != nil {
			t.Fatal(err)
		}
		return user{p, tok}
	}
	staff, staff2 := mk("staff@pl.test"), mk("staff2@pl.test")
	if staff.p.DepartmentID == staff2.p.DepartmentID {
		t.Fatal("전제: 두 직원은 다른 부서")
	}

	// 구매팀(staff 의 부서) PO 를 직접 시드한다.
	exec := func(q string, args ...any) int64 {
		return dbtest.ExecID(t, d.DB, q, args...)
	}
	now := "2026-11-10T00:00:00Z"
	vendor := exec(`INSERT INTO vendors (name, business_no) VALUES ('한빛상사', '123-45-67890')`)
	item := exec(`INSERT INTO items (sku, name) VALUES ('ISO-1', '볼트 M8')`)
	pr := exec(`INSERT INTO purchase_requests (no, requester_id, department_id, vendor_id, title, status, total_amount, created_at, updated_at)
		VALUES ('PR-ISO-1', ?, ?, ?, '격리', 'ordered', 5000, ?, ?)`, staff.p.UserID, staff.p.DepartmentID, vendor, now, now)
	po := exec(`INSERT INTO purchase_orders (no, pr_id, vendor_id, status, total_amount, created_at) VALUES ('PO-ISO-1', ?, ?, 'open', 5000, ?)`, pr, vendor, now)
	exec(`INSERT INTO po_lines (po_id, item_id, quantity, unit_price) VALUES (?, ?, 10, 500)`, po, item)

	count := func(table string) int {
		var n int
		if err := d.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	verifyWeb := func(u user) *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]any{"filename": "a.txt", "text": isolationDoc, "poId": po})
		req := httptest.NewRequest("POST", "/api/documents/verify", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+u.tok)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w
	}
	verifyMCP := func(u user) (int, map[string]any) {
		body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call",
			"params": map[string]any{"name": "verify_document", "arguments": map[string]any{"po_id": po, "text": isolationDoc}}})
		req := httptest.NewRequest("POST", "/mcp", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		req.Header.Set("Authorization", "Bearer "+u.tok)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		out := map[string]any{}
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out
	}

	// 같은 부서: 정상.
	if w := verifyWeb(staff); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"outcome":"match"`) {
		t.Fatalf("같은 부서 직원은 판정되어야 함: %d %s", w.Code, w.Body.String())
	}
	docs, usage := count("documents"), count("llm_usage")

	// 타 부서(웹): 404, 내용 미노출, 문서·LLM 사용량 불변.
	w := verifyWeb(staff2)
	if w.Code != http.StatusNotFound {
		t.Fatalf("타 부서 PO 는 404 여야 함: %d %s", w.Code, w.Body.String())
	}
	for _, leak := range []string{"5000", "500", "볼트"} {
		if strings.Contains(w.Body.String(), leak) {
			t.Fatalf("응답에 PO 내용이 노출됨(%s): %s", leak, w.Body.String())
		}
	}
	if count("documents") != docs || count("llm_usage") != usage {
		t.Fatalf("404 인데 문서가 저장되었거나 LLM 이 호출됨: documents %d→%d, llm_usage %d→%d", docs, count("documents"), usage, count("llm_usage"))
	}

	// 타 부서(MCP verify_document): 도구 오류(찾을 수 없음), 내용 미노출, 문서·LLM 사용량 불변.
	code, out := verifyMCP(staff2)
	res, _ := out["result"].(map[string]any)
	raw, _ := json.Marshal(res)
	if code != http.StatusOK || res["isError"] != true || !strings.Contains(string(raw), "찾을 수 없습니다") {
		t.Fatalf("MCP 도 PO 존재를 숨겨야 함: %d %s", code, raw)
	}
	if strings.Contains(string(raw), "5000") || strings.Contains(string(raw), "볼트") {
		t.Fatalf("MCP 응답에 PO 내용 노출: %s", raw)
	}
	if count("documents") != docs || count("llm_usage") != usage {
		t.Fatal("MCP 거부인데 문서가 저장되었거나 LLM 이 호출됨")
	}
}
