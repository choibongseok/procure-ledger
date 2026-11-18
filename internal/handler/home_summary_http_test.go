package handler_test

import (
	"testing"
)

// 내 요청 요약은 본인이 올린 건만 상태별로 세고, 결재자가 없어 멈춘 건 수를 따로 알려 준다.
func TestHTTPMyRequestSummary(t *testing.T) {
	s := newServer(t)
	item := s.must("finance", "POST", "/api/items", map[string]any{"sku": "P", "name": "용지", "unit": "EA"}, 201)
	vendor := s.must("finance", "POST", "/api/vendors", map[string]any{"name": "대한문구", "businessNo": "123-45-67890"}, 201)
	mk := func(user string) map[string]any {
		return s.must(user, "POST", "/api/purchase-requests", map[string]any{
			"vendorId": vendor["id"], "title": "소액", "reason": "x",
			"lines": []map[string]any{{"itemId": item["id"], "quantity": 1, "unitPrice": 500000}},
		}, 201)
	}
	mk("staff") // 임시저장
	sub := mk("staff")
	s.must("staff", "POST", "/api/purchase-requests/"+id(sub)+"/submit", nil, 200)
	other := mk("lead")
	_ = other

	got := s.must("staff", "GET", "/api/purchase-requests/my-summary", nil, 200)
	by, _ := got["byStatus"].(map[string]any)
	if got["total"] != float64(2) || by["draft"] != float64(1) || by["submitted"] != float64(1) || got["noApprover"] != float64(0) {
		t.Fatalf("요약: %v", got)
	}
	// 팀장 단계를 맡을 사람이 없어지면 결재자 없음으로 센다.
	if _, err := s.db.Exec(`UPDATE users SET active = 0 WHERE id IN (2, 8)`); err != nil {
		t.Fatal(err)
	}
	got = s.must("staff", "GET", "/api/purchase-requests/my-summary", nil, 200)
	if got["noApprover"] != float64(1) {
		t.Fatalf("결재자 없음: %v", got)
	}
	// 다른 사람의 요약에는 섞이지 않는다.
	if lead := s.must("finance", "GET", "/api/purchase-requests/my-summary", nil, 200); lead["total"] != float64(0) {
		t.Fatalf("타인 건 포함: %v", lead)
	}
}
