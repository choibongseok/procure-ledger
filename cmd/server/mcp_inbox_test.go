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
)

// 실제 서비스 조립 위에서 문서함 도구를 HTTP /mcp 로 부른다(명세 22 의 3.6.3):
// 안 읽은 문서가 목록에 나오고, MCP 조회(문서함·구매요청 조회)는 읽음 표시를 남기지 않으며, 수신함은 접수 권한이 없으면 비어 있다.
func TestMCPInboxListDoesNotMarkRead(t *testing.T) {
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

	var staff contract.Principal
	var role string
	if err := d.DB.QueryRowContext(ctx, `SELECT id, role, department_id FROM users WHERE email = 'staff@pl.test'`).Scan(&staff.UserID, &role, &staff.DepartmentID); err != nil {
		t.Fatal(err)
	}
	staff.Role, staff.Name, staff.Channel = contract.Role(role), "staff", contract.ChannelWeb
	tok, _, err := d.Tokens.Issue(ctx, staff, "문서함 시험", 0)
	if err != nil {
		t.Fatal(err)
	}

	// 임시저장 구매요청 한 건(기안함에 안 읽음으로 나온다).
	if _, err := d.DB.ExecContext(ctx, `INSERT INTO vendors (name) VALUES ('한빛상사')`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.DB.ExecContext(ctx, `INSERT INTO items (sku, name) VALUES ('PAPER', '복사용지')`); err != nil {
		t.Fatal(err)
	}
	var vendorID, itemID int64
	_ = d.DB.QueryRowContext(ctx, `SELECT id FROM vendors LIMIT 1`).Scan(&vendorID)
	_ = d.DB.QueryRowContext(ctx, `SELECT id FROM items LIMIT 1`).Scan(&itemID)
	webCtx := contract.WithPrincipal(ctx, staff)
	pr, err := d.Purchase.CreateDraft(webCtx, domain.DraftInput{VendorID: vendorID, Title: "복사용지 구매", Reason: "시험",
		Lines: []domain.PRLineInput{{ItemID: itemID, Quantity: 3, UnitPrice: 20000}}})
	if err != nil {
		t.Fatal(err)
	}

	reads := func() int {
		var n int
		if err := d.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM document_reads`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	mcpCall := func(tool string, args any) (bool, map[string]any, string) {
		body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": tool, "arguments": args}})
		req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		req.Header.Set("Authorization", "Bearer "+tok)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		raw, _ := io.ReadAll(w.Body)
		var out struct {
			Result struct {
				IsError bool `json:"isError"`
				Content []struct {
					Text string `json:"text"`
				} `json:"content"`
			} `json:"result"`
		}
		_ = json.Unmarshal(raw, &out)
		text := ""
		for _, c := range out.Result.Content {
			text += c.Text
		}
		m := map[string]any{}
		_ = json.Unmarshal([]byte(text), &m)
		return out.Result.IsError, m, text
	}

	if reads() != 0 {
		t.Fatalf("시작 읽음 기록: %d", reads())
	}
	isErr, m, text := mcpCall("list_my_approval_docs", map[string]any{"box": "drafted"})
	if isErr {
		t.Fatalf("기안함: %s", text)
	}
	docs, _ := m["docs"].([]any)
	if len(docs) != 1 || docs[0].(map[string]any)["unread"] != true || docs[0].(map[string]any)["form"] != "구매 품의서" || docs[0].(map[string]any)["status"] != "임시저장" {
		t.Fatalf("기안함 목록: %s", text)
	}
	if m["total"] != float64(1) || m["unread"] != float64(1) {
		t.Fatalf("건수: %s", text)
	}
	// 건수만, 구매요청 조회, 같은 목록을 다시 불러도 읽음 기록은 늘지 않는다.
	if isErr, _, text = mcpCall("list_my_approval_docs", map[string]any{}); isErr {
		t.Fatalf("건수: %s", text)
	}
	if isErr, _, text = mcpCall("get_purchase_request", map[string]any{"pr_id": pr.ID}); isErr {
		t.Fatalf("구매요청 조회: %s", text)
	}
	if isErr, m, text = mcpCall("list_my_approval_docs", map[string]any{"box": "drafted"}); isErr || m["unread"] != float64(1) {
		t.Fatalf("다시 조회해도 안 읽음: %s", text)
	}
	if n := reads(); n != 0 {
		t.Fatalf("MCP 조회가 읽음 표시를 남겼다: %d행", n)
	}
	// 수신함: 수신 접수 권한이 없으면 빈 목록(서비스가 판정).
	if isErr, m, text = mcpCall("list_my_approval_docs", map[string]any{"box": "receive"}); isErr || len(m["docs"].([]any)) != 0 {
		t.Fatalf("수신함은 비어야 함: %s", text)
	}
	// 같은 사람이 웹 채널로 열면 그때만 읽음이 남고 목록의 안 읽음이 바뀐다(표시는 서비스가 정한다).
	if err := d.Inbox.MarkRead(webCtx, pr.ID); err != nil {
		t.Fatal(err)
	}
	if isErr, m, text = mcpCall("list_my_approval_docs", map[string]any{"box": "drafted"}); isErr || m["unread"] != float64(0) || !strings.Contains(text, `"unread":false`) {
		t.Fatalf("웹에서 연 뒤: %s", text)
	}
	if reads() != 1 {
		t.Fatalf("웹 열람 읽음 1행: %d", reads())
	}
}
