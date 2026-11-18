package handler_test

import (
	"strings"
	"testing"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract/audit"
)

// 수임자가 뒤 단계의 유일한 결재자이면 앞 단계 대신 결재는 403 으로 거부되고 건은 멈추지 않는다.
func TestHTTPSamePersonTwoStepsAndWithdraw(t *testing.T) {
	s := newServer(t)
	now := time.Now().UTC()
	s.must("lead", "POST", "/api/approvals/delegations", map[string]any{
		"delegateId": 3, "from": now.Add(-time.Hour).Format(time.RFC3339), "to": now.Add(24 * time.Hour).Format(time.RFC3339)}, 201)
	item := s.must("finance", "POST", "/api/items", map[string]any{"sku": "P", "name": "용지", "unit": "EA"}, 201)
	vendor := s.must("finance", "POST", "/api/vendors", map[string]any{"name": "대한문구", "businessNo": "123-45-67890"}, 201)
	pr := s.must("staff", "POST", "/api/purchase-requests", map[string]any{
		"vendorId": vendor["id"], "title": "대량 구매", "reason": "x",
		"lines": []map[string]any{{"itemId": item["id"], "quantity": 1, "unitPrice": 20000000}},
	}, 201)
	prID := id(pr)
	s.must("staff", "POST", "/api/purchase-requests/"+prID+"/submit", nil, 200)

	// 수임자(부서장)가 뒤 단계(부서장)의 유일한 결재자라 팀장 대신 결재는 결재 시점에 거부된다(B9).
	code, _, raw := s.do("head", "POST", "/api/approvals/"+prID+"/decision", map[string]any{"approve": true})
	if code != 403 || !strings.Contains(raw, "결재가 멈춥니다") {
		t.Fatalf("대신 결재 거부: %d %s", code, raw)
	}
	if !s.rec.Has("approval.decide", audit.TargetApproval, audit.ResultDenied) {
		t.Error("거부 감사 기록 없음")
	}
	got := s.must("staff", "GET", "/api/purchase-requests/"+prID, nil, 200)
	if got["noApprover"] == true || got["status"] != "submitted" {
		t.Fatalf("멈추지 않아야 함: %v", got)
	}
	s.must("staff", "POST", "/api/purchase-requests/"+prID+"/withdraw", nil, 409) // 결재할 사람이 있어 회수 대상 아님
	s.must("lead", "POST", "/api/approvals/"+prID+"/decision", map[string]any{"approve": true}, 200)
	s.must("head", "POST", "/api/approvals/"+prID+"/decision", map[string]any{"approve": true}, 200)
}

// 위임 해제는 건을 멈추게 하지 않는다: 직책 단계는 배정된 결재자가 직접 결재할 수 있다(명세 3.6). 해제 뒤 수임자는 결재할 수 없다.
func TestHTTPRevokeDoesNotStrand(t *testing.T) {
	s := newServer(t)
	now := time.Now().UTC()
	del := s.must("lead", "POST", "/api/approvals/delegations", map[string]any{
		"delegateId": 3, "from": now.Add(-time.Hour).Format(time.RFC3339), "to": now.Add(24 * time.Hour).Format(time.RFC3339)}, 201)
	item := s.must("finance", "POST", "/api/items", map[string]any{"sku": "P", "name": "용지", "unit": "EA"}, 201)
	vendor := s.must("finance", "POST", "/api/vendors", map[string]any{"name": "대한문구", "businessNo": "123-45-67890"}, 201)
	pr := s.must("staff", "POST", "/api/purchase-requests", map[string]any{
		"vendorId": vendor["id"], "title": "소액", "reason": "x",
		"lines": []map[string]any{{"itemId": item["id"], "quantity": 1, "unitPrice": 500000}},
	}, 201)
	s.must("staff", "POST", "/api/purchase-requests/"+id(pr)+"/submit", nil, 200)
	if code, _, raw := s.do("lead", "DELETE", "/api/approvals/delegations/"+id(del), nil); code != 204 {
		t.Fatalf("해제: %d %s", code, raw)
	}
	s.must("head", "POST", "/api/approvals/"+id(pr)+"/decision", map[string]any{"approve": true}, 403)
	s.must("lead", "POST", "/api/approvals/"+id(pr)+"/decision", map[string]any{"approve": true}, 200)
}
