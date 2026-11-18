package mcp_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/contract/deps"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/contract/fake"
	"github.com/choibongseok/procure-ledger/internal/mcp"
)

var (
	staffP   = contract.Principal{UserID: 1, Channel: contract.ChannelMCP, Name: "직원", Role: contract.RoleStaff, DepartmentID: 10}
	managerP = contract.Principal{UserID: 2, Channel: contract.ChannelMCP, Name: "팀장", Role: contract.RoleManager, DepartmentID: 10}
	financeP = contract.Principal{UserID: 3, Channel: contract.ChannelMCP, Name: "재무", Role: contract.RoleFinance, DepartmentID: 20}
	auditorP = contract.Principal{UserID: 4, Channel: contract.ChannelMCP, Name: "감사", Role: contract.RoleAuditor, DepartmentID: 30}
)

func ctxAs(p contract.Principal) context.Context {
	return contract.WithPrincipal(context.Background(), p)
}

func newDeps() (*fake.Bundle, deps.Deps) {
	b := fake.NewBundle()
	for tok, p := range map[string]contract.Principal{"t-staff": staffP, "t-mgr": managerP, "t-fin": financeP, "t-aud": auditorP} {
		b.Auth.Add(tok, p)
	}
	return b, b.Deps()
}

func connect(t *testing.T, d deps.Deps, p contract.Principal) *sdk.ClientSession {
	t.Helper()
	srv, _ := mcp.NewServer(d, p)
	ct, st := sdk.NewInMemoryTransports()
	ctx := context.Background()
	ss, err := srv.Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ss.Close() })
	cs, err := sdk.NewClient(&sdk.Implementation{Name: "test", Version: "0"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func call(t *testing.T, cs *sdk.ClientSession, name string, args any) (*sdk.CallToolResult, string) {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &sdk.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: 프로토콜 오류 %v", name, err)
	}
	var sb strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*sdk.TextContent); ok {
			sb.WriteString(tc.Text)
		}
	}
	return res, sb.String()
}

// ---- 권한 선언 누락 검사(verify.sh 2단계가 이름으로 찾는다) ----

func TestPermissionDeclarationMCPToolsMatchTable(t *testing.T) {
	_, d := newDeps()
	decl := mcp.DeclaredTools(d)
	if problems := contract.CheckTools(decl); len(problems) != 0 {
		t.Fatalf("MCP 도구 권한 선언 문제: %v", problems)
	}
	if len(decl) != 26 {
		t.Fatalf("도구는 26개여야 함: %d", len(decl))
	}
}

func TestPermissionDeclarationRegisteredToolsAreAllDeclared(t *testing.T) {
	_, d := newDeps()
	cs := connect(t, d, staffP)
	var registered []string
	for tool, err := range cs.Tools(context.Background(), nil) {
		if err != nil {
			t.Fatal(err)
		}
		registered = append(registered, tool.Name)
	}
	var declared []string
	for _, dc := range mcp.DeclaredTools(d) {
		declared = append(declared, dc.Name)
	}
	sort.Strings(registered)
	sort.Strings(declared)
	if strings.Join(registered, ",") != strings.Join(declared, ",") {
		t.Fatalf("선언 없이 등록된 도구가 있거나 등록되지 않은 선언이 있음\n등록: %v\n선언: %v", registered, declared)
	}
}

// ---- 권한 거부 + 감사 ----

func seedPR(t *testing.T, b *fake.Bundle) (domain.Item, domain.PurchaseRequest) {
	t.Helper()
	item := b.ERP.AddItem(domain.Item{SKU: "A-1", Name: "복사용지", Unit: "박스", SafetyStock: 10})
	pr, err := b.ERP.CreateDraft(ctxAs(staffP), domain.DraftInput{VendorID: 1, Title: "용지", Lines: []domain.PRLineInput{{ItemID: item.ID, Quantity: 5, UnitPrice: 20000}}})
	if err != nil {
		t.Fatal(err)
	}
	if pr, err = b.ERP.Submit(ctxAs(staffP), pr.ID); err != nil {
		t.Fatal(err)
	}
	return item, pr
}

func TestStaffCannotApproveAndDenialIsAudited(t *testing.T) {
	b, d := newDeps()
	_, pr := seedPR(t, b)
	cs := connect(t, d, staffP)
	before := b.Audit.Count()
	res, text := call(t, cs, contract.ToolApproveOrReject, map[string]any{"pr_id": pr.ID, "approve": true})
	if !res.IsError || !strings.Contains(text, "권한") {
		t.Fatalf("staff 의 승인 호출은 거부여야 함: isError=%v %q", res.IsError, text)
	}
	recs := b.Audit.Records()[before:]
	if len(recs) != 1 {
		t.Fatalf("거부 1건이 감사 로그에 남아야 함: %+v", recs)
	}
	r := recs[0]
	if r.Action != "mcp.approve_or_reject" || r.Result != audit.ResultDenied || r.Channel != contract.ChannelMCP || r.ActorID != staffP.UserID {
		t.Fatalf("감사 기록: %+v", r)
	}
	got, _ := b.ERP.Get(ctxAs(financeP), pr.ID)
	if got.Status != pr.Status || got.Status == domain.PRApproved || got.Status == domain.PROrdered {
		t.Fatalf("거부된 호출이 상태를 바꿨다: %s", got.Status)
	}
}

func TestEveryDisallowedToolIsDeniedForEveryRole(t *testing.T) {
	// 권한 표에 없는 (역할, 도구) 조합은 전부 거부되고 감사 로그에 denied 로 남는다.
	for _, role := range contract.Roles {
		for tool, perm := range contract.MCPToolPermissions {
			if contract.RoleHas(role, perm) {
				continue
			}
			b, d := newDeps()
			cs := connect(t, d, contract.Principal{UserID: 9, Name: "x", Role: role, DepartmentID: 10})
			res, _ := call(t, cs, tool, map[string]any{"pr_id": 1, "approve": true, "item_id": 1, "po_id": 1, "text": "x", "query": "a", "title": "t",
				"lines": []map[string]any{{"item_id": 1, "quantity": 1, "unit_price": 1}}})
			if !res.IsError {
				t.Errorf("%s 역할이 %s 를 호출했는데 거부되지 않음", role, tool)
			}
			if !b.Audit.Has("mcp."+tool, "", audit.ResultDenied) {
				t.Errorf("%s/%s 거부가 감사 로그에 없음", role, tool)
			}
		}
	}
}

func TestManagerApprovesThroughSameService(t *testing.T) {
	b, d := newDeps()
	_, pr := seedPR(t, b)
	cs := connect(t, d, managerP)
	res, text := call(t, cs, contract.ToolApproveOrReject, map[string]any{"pr_id": pr.ID, "approve": true})
	if res.IsError {
		t.Fatalf("manager 승인 실패: %s", text)
	}
	if !strings.Contains(text, "purchase_order_id") {
		t.Fatalf("승인 결과에 발주 정보: %s", text)
	}
	if !b.Audit.Has("approval.decide", "", audit.ResultSuccess) {
		t.Fatal("서비스 계층 감사 로그(approval.decide)가 남아야 함 — 같은 서비스 경로")
	}
	if !b.Audit.Has("mcp.approve_or_reject", "", audit.ResultSuccess) {
		t.Fatal("MCP 호출 감사 로그")
	}
}

func TestSelfApprovalStillBlockedViaMCP(t *testing.T) {
	b, d := newDeps()
	// manager 가 올린 요청을 본인이 승인할 수 없다(서비스 규칙이 MCP 에서도 적용).
	item := b.ERP.AddItem(domain.Item{Name: "x"})
	pr, _ := b.ERP.CreateDraft(ctxAs(managerP), domain.DraftInput{VendorID: 1, Title: "t", Lines: []domain.PRLineInput{{ItemID: item.ID, Quantity: 1, UnitPrice: 1000}}})
	_, _ = b.ERP.Submit(ctxAs(managerP), pr.ID)
	cs := connect(t, d, managerP)
	res, text := call(t, cs, contract.ToolApproveOrReject, map[string]any{"pr_id": pr.ID, "approve": true})
	if !res.IsError {
		t.Fatalf("자기 결재는 거부여야 함: %s", text)
	}
	if !b.Audit.Has("mcp.approve_or_reject.result", "", audit.ResultError) && !b.Audit.Has("mcp.approve_or_reject.result", "", audit.ResultDenied) {
		t.Fatal("실패 호출도 감사 로그")
	}
}

func TestDraftToolCreatesDraftOnly(t *testing.T) {
	b, d := newDeps()
	item := b.ERP.AddItem(domain.Item{Name: "복사용지"})
	cs := connect(t, d, staffP)
	res, text := call(t, cs, contract.ToolCreatePRDraft, map[string]any{"title": "용지 구매", "lines": []map[string]any{{"item_id": item.ID, "quantity": 3, "unit_price": 1000}}})
	if res.IsError || !strings.Contains(text, `"status":"draft"`) {
		t.Fatalf("임시저장만 만들어야 함: %v %s", res.IsError, text)
	}
	if !b.Audit.Has("purchase_request.create", "", audit.ResultSuccess) {
		t.Fatal("서비스 감사 로그")
	}
}

func TestReadToolsAndResultMasking(t *testing.T) {
	b, d := newDeps()
	item := b.ERP.AddItem(domain.Item{SKU: "S-1", Name: "특수볼트 (거래처 123-45-67890 전용)", Unit: "EA", SafetyStock: 5})
	b.ERP.SeedStock(item.ID, "L1", "", 3)
	cs := connect(t, d, staffP)
	res, text := call(t, cs, contract.ToolSearchItems, map[string]any{"query": "볼트"})
	if res.IsError {
		t.Fatalf("search_items: %s", text)
	}
	if strings.Contains(text, "123-45-67890") {
		t.Fatalf("마스킹 대상이 그대로 나감: %s", text)
	}
	res, text = call(t, cs, contract.ToolGetStock, map[string]any{"item_id": item.ID})
	if res.IsError || !strings.Contains(text, `"on_hand":3`) || strings.Contains(text, "123-45-67890") {
		t.Fatalf("get_stock: %v %s", res.IsError, text)
	}
	if res, text = call(t, cs, contract.ToolGetStock, map[string]any{"item_id": 9999}); !res.IsError {
		t.Fatalf("없는 품목은 오류: %s", text)
	}
}

func TestVerifyDocumentToolUsesDocumentService(t *testing.T) {
	b, d := newDeps()
	b.Documents.Outcome = domain.OutcomeMismatch
	cs := connect(t, d, staffP)
	res, text := call(t, cs, contract.ToolVerifyDocument, map[string]any{"po_id": 1, "text": "거래명세서\n합계: 1"})
	if res.IsError || !strings.Contains(text, `"outcome":"mismatch"`) || !strings.Contains(text, "제안") {
		t.Fatalf("verify_document: %v %s", res.IsError, text)
	}
	csA := connect(t, d, auditorP)
	if res, _ := call(t, csA, contract.ToolVerifyDocument, map[string]any{"po_id": 1, "text": "x"}); !res.IsError {
		t.Fatal("auditor 는 verify_document 불가")
	}
}

func TestAuditFailureFailsToolCall(t *testing.T) {
	b, d := newDeps()
	cs := connect(t, d, staffP)
	b.Audit.FailWith(errors.New("disk"))
	res, _ := call(t, cs, contract.ToolSearchItems, map[string]any{"query": "x"})
	if !res.IsError {
		t.Fatal("감사 기록에 실패하면 호출도 실패로 보여야 함")
	}
}

// ---- HTTP: 토큰 → Principal, 요청마다 격리 ----

func rpc(t *testing.T, h http.Handler, token string, name string, args any) (int, map[string]any) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": name, "arguments": args}})
	req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	out := map[string]any{}
	raw, _ := io.ReadAll(rr.Body)
	_ = json.Unmarshal(raw, &out)
	return rr.Code, out
}

func isErr(out map[string]any) bool {
	res, _ := out["result"].(map[string]any)
	e, _ := res["isError"].(bool)
	return e
}

func TestHTTPTokenAuthAndPerRequestIdentity(t *testing.T) {
	b, d := newDeps()
	_, pr := seedPR(t, b)
	h := mcp.NewHTTPHandler(d)

	if code, _ := rpc(t, h, "", contract.ToolSearchItems, map[string]any{"query": "x"}); code != http.StatusUnauthorized {
		t.Fatalf("토큰 없음 → 401: %d", code)
	}
	if code, _ := rpc(t, h, "nope", contract.ToolSearchItems, map[string]any{"query": "x"}); code != http.StatusUnauthorized {
		t.Fatalf("잘못된 토큰 → 401: %d", code)
	}
	b.Auth.Revoke("t-aud")
	if code, _ := rpc(t, h, "t-aud", contract.ToolSearchItems, map[string]any{"query": "x"}); code != http.StatusUnauthorized {
		t.Fatalf("폐기된 토큰 → 401: %d", code)
	}

	// 같은 핸들러에서 staff 토큰은 거부, 직후 manager 토큰은 승인: 사용자가 섞이지 않는다.
	code, out := rpc(t, h, "t-staff", contract.ToolApproveOrReject, map[string]any{"pr_id": pr.ID, "approve": true})
	if code != http.StatusOK || !isErr(out) {
		t.Fatalf("staff 토큰: %d %v", code, out)
	}
	code, out = rpc(t, h, "t-mgr", contract.ToolApproveOrReject, map[string]any{"pr_id": pr.ID, "approve": true})
	if code != http.StatusOK || isErr(out) {
		t.Fatalf("manager 토큰: %d %v", code, out)
	}
	if !b.Audit.Has("mcp.approve_or_reject", "", audit.ResultDenied) || !b.Audit.Has("mcp.approve_or_reject", "", audit.ResultSuccess) {
		t.Fatal("거부·성공이 모두 감사 로그에 남아야 함")
	}
	for _, r := range b.Audit.Records() {
		if strings.HasPrefix(r.Action, "mcp.") && r.Channel != contract.ChannelMCP {
			t.Fatalf("MCP 감사 기록의 채널은 mcp: %+v", r)
		}
	}
}

func TestRunStdioRejectsBadToken(t *testing.T) {
	_, d := newDeps()
	if err := mcp.RunStdio(context.Background(), d, ""); err == nil {
		t.Fatal("토큰 없이 시작하면 안 됨")
	}
	if err := mcp.RunStdio(context.Background(), d, "bad"); !errors.Is(err, contract.ErrUnauthenticated) {
		t.Fatalf("잘못된 토큰: %v", err)
	}
}
