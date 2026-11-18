package rbac_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/contract/fake"
	"github.com/choibongseok/procure-ledger/internal/rbac"
)

func TestMaskFieldByRole(t *testing.T) {
	m := rbac.Masker{}
	staff := contract.Principal{UserID: 1, Role: contract.RoleStaff}
	fin := contract.Principal{UserID: 2, Role: contract.RoleFinance}
	cases := []struct {
		kind contract.FieldKind
		in   string
		want string
	}{
		{contract.FieldBusinessNo, "123-45-67890", "123-**-*****"},
		{contract.FieldAccountNo, "110-123-456789", "***-***-**6789"},
		{contract.FieldPhone, "010-1234-5678", "010-****-5678"},
		{contract.FieldEmail, "kim@example.com", "k**@example.com"},
	}
	for _, c := range cases {
		if got := m.MaskField(staff, c.kind, c.in); got != c.want {
			t.Errorf("staff %s: %q != %q", c.kind, got, c.want)
		}
		if got := m.MaskField(fin, c.kind, c.in); got != c.in {
			t.Errorf("finance 는 원문을 봐야 함: %q", got)
		}
	}
	if got := m.MaskField(staff, contract.FieldPhone, ""); got != "" {
		t.Errorf("빈 값: %q", got)
	}
	// 알 수 없는 역할·미인증은 가린다(fail-closed).
	if got := m.MaskField(contract.Principal{}, contract.FieldPhone, "010-1234-5678"); got == "010-1234-5678" {
		t.Error("미인증에 원문 노출")
	}
}

func TestMaskTextAlwaysMasks(t *testing.T) {
	m := rbac.Masker{}
	in := "공급자 한빛상사 사업자번호 123-45-67890 연락처 010-1234-5678 계좌 110-123-456789 메일 a.b@corp.example 금액 5000 발행일 2026-11-10"
	out := m.MaskText(in)
	for _, leak := range []string{"123-45-67890", "010-1234-5678", "110-123-456789", "a.b@corp.example"} {
		if strings.Contains(out, leak) {
			t.Errorf("%q 가 남아 있음: %s", leak, out)
		}
	}
	if !strings.Contains(out, "금액 5000") || !strings.Contains(out, "2026-11-10") || !strings.Contains(out, "한빛상사") {
		t.Errorf("일반 텍스트가 훼손됨: %s", out)
	}
	if m.MaskText("수량 10 단가 500 합계 5000") != "수량 10 단가 500 합계 5000" {
		t.Error("숫자만 있는 일반 문장이 가려짐")
	}
}

func TestGuardRecordsDenied(t *testing.T) {
	b := fake.NewBundle()
	rt := contract.NewRouteTable()
	rt.Guard = rbac.NewGuard(b.Audit, b.Txs)
	called := false
	rt.HandleFunc("POST", "/api/approve", contract.PermApprovalDecide, func(http.ResponseWriter, *http.Request) { called = true })
	rt.HandleFunc("GET", "/pub", contract.PermPublic, func(http.ResponseWriter, *http.Request) {})

	do := func(p *contract.Principal) int {
		req := httptest.NewRequest("POST", "/api/approve", nil)
		if p != nil {
			req = req.WithContext(contract.WithPrincipal(req.Context(), *p))
		}
		w := httptest.NewRecorder()
		rt.Handler().ServeHTTP(w, req)
		return w.Code
	}
	if do(nil) != http.StatusUnauthorized || b.Audit.Count() != 0 {
		t.Fatal("미인증은 401, 기록 없음")
	}
	staff := contract.Principal{UserID: 3, Role: contract.RoleStaff, Channel: contract.ChannelWeb}
	if do(&staff) != http.StatusForbidden || called {
		t.Fatal("staff 는 403")
	}
	if !b.Audit.Has("access.denied", audit.TargetRoute, audit.ResultDenied) {
		t.Fatal("거부가 감사 로그에 남아야 함")
	}
	mgr := contract.Principal{UserID: 4, Role: contract.RoleManager, Channel: contract.ChannelWeb}
	if do(&mgr) != http.StatusOK || !called {
		t.Fatal("manager 통과")
	}
}

// B41 D6: MCP 채널은 mask:bypass 를 가진 사용자(토큰 발급자)에게도 어떤 필드도 원문을 내지 않는다. 웹 채널은 그대로다.
func TestMaskFieldMCPChannelNeverReturnsRaw(t *testing.T) {
	m := rbac.Masker{}
	cases := []struct {
		kind contract.FieldKind
		in   string
	}{
		{contract.FieldBusinessNo, "123-45-67890"},
		{contract.FieldAccountNo, "110-123-456789"},
		{contract.FieldPhone, "010-1234-5678"},
		{contract.FieldEmail, "kim@example.com"},
	}
	for _, role := range []contract.Role{contract.RoleFinance, contract.RoleAdmin} {
		web := contract.Principal{UserID: 2, Role: role, Channel: contract.ChannelWeb}
		mcp := contract.Principal{UserID: 2, Role: role, Channel: contract.ChannelMCP}
		if !contract.RawFieldAllowed(web) || contract.RawFieldAllowed(mcp) {
			t.Fatalf("%s: RawFieldAllowed 웹 %v MCP %v", role, contract.RawFieldAllowed(web), contract.RawFieldAllowed(mcp))
		}
		for _, c := range cases {
			if got := m.MaskField(web, c.kind, c.in); got != c.in {
				t.Errorf("%s 웹은 원문이어야 함(%s): %q", role, c.kind, got)
			}
			if got := m.MaskField(mcp, c.kind, c.in); got == c.in || got == "" {
				t.Errorf("%s MCP 는 가려야 함(%s): %q", role, c.kind, got)
			}
		}
	}
}
