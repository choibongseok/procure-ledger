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

// B29: 실제 보존 만료 폐기 서비스로 문서를 폐기한 뒤, MCP 도구(get_approval_doc·list_my_approval_docs)가 껍데기만 돌려준다.
func TestMCPDisposedDocShellOverRealStack(t *testing.T) {
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

	var staffID, adminID, dept int64
	if err := d.DB.QueryRowContext(ctx, `SELECT id, department_id FROM users WHERE email = 'staff@pl.test'`).Scan(&staffID, &dept); err != nil {
		t.Fatal(err)
	}
	if err := d.DB.QueryRowContext(ctx, `SELECT id FROM users WHERE email = 'admin@pl.test'`).Scan(&adminID); err != nil {
		t.Fatal(err)
	}
	token := func(email string) string {
		var p contract.Principal
		var role string
		if err := d.DB.QueryRowContext(ctx, `SELECT id, role, department_id FROM users WHERE email = ?`, email).Scan(&p.UserID, &role, &p.DepartmentID); err != nil {
			t.Fatal(err)
		}
		p.Role, p.Name = contract.Role(role), email
		tok, _, err := d.Tokens.Issue(ctx, p, "폐기 시험-"+email, 0)
		if err != nil {
			t.Fatal(err)
		}
		return tok
	}
	mcpCall := func(tok, tool string, args any) (bool, map[string]any, string) {
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

	// 보존 기한이 지난 완료 일반 기안서 한 건(기밀 제목·본문·결재 의견·첨부)을 만든다.
	docID := dbtest.InsertID(t, d.DB, `INSERT INTO purchase_requests (no, requester_id, department_id, title, status, form_id, fields, retention_until, completed_at, created_at, updated_at, legal_hold, total_amount, amount)
		VALUES ('GEN-9100', ?, ?, '기밀 인수합병 검토', 'approved', (SELECT id FROM approval_forms WHERE code = 'GEN'), '{"content":"극비 본문"}', '2025-12-31', '2020-12-31T00:00:00Z', '2020-12-31T00:00:00Z', '2020-12-31T00:00:00Z', 0, 0, 0)`, staffID, dept)
	if _, err := d.DB.ExecContext(ctx, `INSERT INTO document_attachments (doc_id, uploaded_by, filename, size, sha256, content_type, content, created_at) VALUES (?, ?, '극비계약서.pdf', 3, 'abc', 'application/pdf', ?, '2020-12-30T00:00:00Z')`, docID, staffID, []byte{1, 2, 3}); err != nil {
		t.Fatal(err)
	}

	staffTok, auditorTok := token("staff@pl.test"), token("auditor@pl.test")
	// 폐기 전: 요청자는 제목과 본문 칸을 본다(기준 동작).
	isErr, m, text := mcpCall(staffTok, "get_approval_doc", map[string]any{"doc_id": docID})
	if isErr || !strings.Contains(text, "기밀 인수합병 검토") || m["disposed"] != nil {
		t.Fatalf("폐기 전: %s", text)
	}

	actx := contract.WithPrincipal(ctx, contract.Principal{UserID: adminID, Name: "관리자", Role: contract.RoleAdmin, Channel: contract.ChannelWeb})
	if _, err := d.Retention.Dispose(actx, domain.DisposeInput{IDs: []int64{docID}, Reason: "보존 연한 만료 정기 정리"}); err != nil {
		t.Fatalf("폐기: %v", err)
	}

	for name, tok := range map[string]string{"요청자(직원)": staffTok, "감사": auditorTok} {
		isErr, m, text = mcpCall(tok, "get_approval_doc", map[string]any{"doc_id": docID})
		if isErr {
			t.Fatalf("%s: %s", name, text)
		}
		for _, bad := range []string{"기밀 인수합병", "극비 본문", "극비계약서", "abc"} {
			if strings.Contains(text, bad) {
				t.Fatalf("%s: 폐기 문서 응답에 남은 값 %q: %s", name, bad, text)
			}
		}
		doc := m["doc"].(map[string]any)
		if m["disposed"] != true || doc["status"] != "폐기됨" || doc["title"] != "폐기된 문서" || doc["no"] != "GEN-9100" || doc["disposed_at"] == "" || len(m["fields"].([]any)) != 0 || len(m["attachments"].([]any)) != 0 {
			t.Fatalf("%s 껍데기: %s", name, text)
		}
	}
	// 완료함 목록도 껍데기다.
	isErr, m, text = mcpCall(staffTok, "list_my_approval_docs", map[string]any{"box": "done"})
	if isErr || strings.Contains(text, "기밀 인수합병") {
		t.Fatalf("완료함: %s", text)
	}
	found := false
	for _, r := range m["docs"].([]any) {
		row := r.(map[string]any)
		if row["no"] == "GEN-9100" {
			found = true
			if row["status"] != "폐기됨" || row["title"] != "폐기된 문서" {
				t.Fatalf("폐기 행: %+v", row)
			}
		}
	}
	if !found {
		t.Fatalf("완료함에 폐기된 문서 행이 없음: %s", text)
	}
}
