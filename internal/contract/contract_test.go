package contract_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/contract"
)

func TestRolePermissionTable(t *testing.T) {
	for _, r := range contract.Roles {
		if len(contract.RolePermissions[r]) == 0 {
			t.Errorf("역할 %s 의 권한 표가 비어 있음", r)
		}
		for _, p := range contract.RolePermissions[r] {
			if !p.IsKnown() {
				t.Errorf("역할 %s 에 알 수 없는 권한 %s", r, p)
			}
		}
	}
	if contract.RoleHas("ghost", contract.PermPurchaseRead) {
		t.Error("알 수 없는 역할은 거부되어야 함(fail-closed)")
	}
	// spec 3절: auditor 는 읽기 전용, 쓰기 권한 없음.
	for _, w := range []contract.Permission{contract.PermPurchaseCreate, contract.PermApprovalDecide, contract.PermInventoryManage, contract.PermDocumentConfirm} {
		if contract.RoleHas(contract.RoleAuditor, w) {
			t.Errorf("auditor 가 쓰기 권한 %s 를 가짐", w)
		}
	}
	// staff 는 결재 불가, finance 만 문서 확정.
	if contract.RoleHas(contract.RoleStaff, contract.PermApprovalDecide) {
		t.Error("staff 는 결재 불가")
	}
	for _, r := range contract.Roles {
		if r != contract.RoleFinance && contract.RoleHas(r, contract.PermDocumentConfirm) {
			t.Errorf("%s 가 문서 확정 권한을 가짐", r)
		}
	}
	// 감사 로그는 admin·auditor 만.
	for _, r := range contract.Roles {
		want := r == contract.RoleAdmin || r == contract.RoleAuditor
		if contract.RoleHas(r, contract.PermAuditRead) != want {
			t.Errorf("%s 의 audit:read 가 예상과 다름", r)
		}
	}
}

func TestMCPToolPermissionTableCoversSpecTools(t *testing.T) {
	want := []string{"search_items", "get_stock", "list_pending_approvals", "get_purchase_request",
		"create_purchase_request_draft", "approve_or_reject", "verify_document", "list_stock_alerts", "draft_reorder", "search_policy",
		"list_approval_forms", "get_approval_doc", "list_my_approval_docs", "search_people", "get_org_unit", "who_approves", "get_item_ledger", "get_budget_status", "get_ai_cost_summary", "get_month_close_status", "get_payables", "get_card_unreconciled", "get_tax_invoices", "search_knowledge", "get_my_leave_balance", "list_team_absence"}
	for _, n := range want {
		p, ok := contract.MCPToolPermissions[n]
		if !ok || !p.IsKnown() {
			t.Errorf("도구 %s 의 권한이 표에 없음", n)
		}
	}
	if len(contract.MCPToolPermissions) != len(want) {
		t.Errorf("표의 도구 수 %d != %d", len(contract.MCPToolPermissions), len(want))
	}
}

func TestPermissionsMirrorFrontend(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "..", "web", "src", "auth", "permissions.ts"))
	if err != nil {
		t.Fatalf("프론트 권한 목록을 읽을 수 없음: %v", err)
	}
	text := string(src)
	for _, p := range contract.AllPermissions {
		if !strings.Contains(text, `"`+string(p)+`"`) {
			t.Errorf("web/src/auth/permissions.ts 에 %q 가 없음", p)
		}
	}
	for r, perms := range contract.RolePermissions {
		if !strings.Contains(text, `"`+string(r)+`"`) {
			t.Errorf("프론트 역할 표에 %q 가 없음", r)
		}
		_ = perms
	}
}

func TestPermissionDeclarationRoutes(t *testing.T) {
	rt := contract.NewRouteTable()
	h := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	for name, perm := range map[string]contract.Permission{"빈 권한": "", "오타": "purchase:creat"} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("%s 선언이 패닉 없이 등록됨", name)
				}
			}()
			rt.HandleFunc("GET", "/x/"+string(perm), perm, h)
		}()
	}
	rt.HandleFunc("GET", "/ok", contract.PermPublic, h)
	func() {
		defer func() {
			if recover() == nil {
				t.Error("중복 등록이 허용됨")
			}
		}()
		rt.HandleFunc("GET", "/ok", contract.PermPublic, h)
	}()
	if got := contract.CheckRoutes(rt.Routes()); len(got) != 0 {
		t.Errorf("문제 없어야 함: %v", got)
	}
	if got := contract.CheckRoutes([]contract.RouteDecl{{Method: "GET", Pattern: "/bad"}}); len(got) != 1 {
		t.Errorf("선언 누락을 잡아야 함: %v", got)
	}
}

func TestPermissionDeclarationGuard(t *testing.T) {
	rt := contract.NewRouteTable()
	called := false
	rt.HandleFunc("POST", "/api/approve", contract.PermApprovalDecide, func(http.ResponseWriter, *http.Request) { called = true })

	do := func(ctx context.Context) int {
		req := httptest.NewRequest("POST", "/api/approve", nil).WithContext(ctx)
		w := httptest.NewRecorder()
		rt.Handler().ServeHTTP(w, req)
		return w.Code
	}
	if code := do(context.Background()); code != http.StatusUnauthorized || called {
		t.Fatalf("미인증 401 이어야 함: %d", code)
	}
	staff := contract.WithPrincipal(context.Background(), contract.Principal{UserID: 1, Role: contract.RoleStaff})
	if code := do(staff); code != http.StatusForbidden || called {
		t.Fatalf("staff 403 이어야 함: %d", code)
	}
	mgr := contract.WithPrincipal(context.Background(), contract.Principal{UserID: 2, Role: contract.RoleManager})
	if code := do(mgr); code != http.StatusOK || !called {
		t.Fatalf("manager 통과해야 함: %d", code)
	}
}

func TestPermissionDeclarationTools(t *testing.T) {
	var tt contract.ToolTable
	for name := range contract.MCPToolPermissions {
		tt.Declare(name)
	}
	if got := contract.CheckTools(tt.Tools()); len(got) != 0 {
		t.Fatalf("전부 선언했는데 문제: %v", got)
	}
	partial := tt.Tools()[1:]
	if got := contract.CheckTools(partial); len(got) != 1 {
		t.Fatalf("누락 도구 1개를 잡아야 함: %v", got)
	}
	bad := append(tt.Tools(), contract.ToolDecl{Name: "drop_database", Permission: contract.PermPublic})
	if got := contract.CheckTools(bad); len(got) != 1 {
		t.Fatalf("표에 없는 도구를 잡아야 함: %v", got)
	}
	func() {
		defer func() {
			if recover() == nil {
				t.Error("표에 없는 도구 선언이 허용됨")
			}
		}()
		tt.Declare("drop_database")
	}()
}
