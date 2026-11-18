package handler_test

import (
	"strings"
	"testing"
	"time"
)

// GET /api/approvals/{id}/acting: 권한은 라우트(approval:decide)가 강제하고, 위임 대신 결재가 아니면 존재 여부와 무관하게 null 이다.
func TestHTTPApprovalActing(t *testing.T) {
	s := newServer(t)
	now := time.Now().UTC()
	item := s.must("finance", "POST", "/api/items", map[string]any{"sku": "P", "name": "용지", "unit": "EA"}, 201)
	vendor := s.must("finance", "POST", "/api/vendors", map[string]any{"name": "대한문구", "businessNo": "123-45-67890"}, 201)
	submit := func(user string) string {
		pr := s.must(user, "POST", "/api/purchase-requests", map[string]any{
			"vendorId": vendor["id"], "title": "소액", "reason": "x",
			"lines": []map[string]any{{"itemId": item["id"], "quantity": 1, "unitPrice": 500000}},
		}, 201)
		s.must(user, "POST", "/api/purchase-requests/"+id(pr)+"/submit", nil, 200)
		return id(pr)
	}
	prID := submit("staff")
	otherDept := submit("staff2")
	path := func(p string) string { return "/api/approvals/" + p + "/acting" }
	isNull := func(user, p string) {
		t.Helper()
		code, _, raw := s.do(user, "GET", path(p), nil)
		if code != 200 || strings.TrimSpace(raw) != "null" {
			t.Fatalf("%s %s: 기대 200 null, 실제 %d %s", user, p, code, raw)
		}
	}

	// 결재 권한이 없는 역할은 403.
	for _, u := range []string{"staff", "auditor", "admin"} {
		s.must(u, "GET", path(prID), nil, 403)
	}
	// 결재 권한이 있어도 위임이 없으면 null: 직접 결재자, 위임받지 않은 결재자, 재무, 없는 번호, 타 부서 건 모두 같다.
	isNull("lead", prID)
	isNull("head", prID)
	isNull("finance", prID)
	isNull("head", "999999")
	isNull("lead", "999999")
	isNull("lead", otherDept)

	// 위임이 생기면 수임자에게만 위임자 이름과 기간이 나온다. 타 부서 건과 없는 번호는 여전히 null.
	from, to := now.Add(-time.Hour).Format(time.RFC3339), now.Add(24*time.Hour).Format(time.RFC3339)
	s.must("lead", "POST", "/api/approvals/delegations", map[string]any{"delegateId": 3, "from": from, "to": to}, 201)
	got := s.must("head", "GET", path(prID), nil, 200)
	if got["delegatorName"] == "" || got["delegatorName"] == nil || got["from"] == "" || got["to"] == "" {
		t.Fatalf("수임자 안내: %+v", got)
	}
	isNull("lead", prID)
	isNull("finance", prID)
	isNull("head", otherDept)
	isNull("head", "999999")
}
