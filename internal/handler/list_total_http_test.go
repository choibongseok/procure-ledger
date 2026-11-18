package handler_test

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

// 목록 응답은 배열 본문 그대로이고 같은 조건의 전체 건수를 X-Total-Count 로 준다(offset 으로 쪽을 넘겨도 같다).
func TestHTTPListTotalCountHeader(t *testing.T) {
	s := newServer(t)
	item := s.must("finance", "POST", "/api/items", map[string]any{"sku": "P", "name": "용지", "unit": "EA"}, 201)
	vendor := s.must("finance", "POST", "/api/vendors", map[string]any{"name": "대한문구", "businessNo": "123-45-67890"}, 201)
	for i := 0; i < 3; i++ {
		s.must("staff", "POST", "/api/purchase-requests", map[string]any{
			"vendorId": vendor["id"], "title": "소액", "reason": "x",
			"lines": []map[string]any{{"itemId": item["id"], "quantity": 1, "unitPrice": 500000}},
		}, 201)
	}
	get := func(user, path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", path, nil)
		req.Header.Set("X-Test-User", user)
		w := httptest.NewRecorder()
		s.h.ServeHTTP(w, req)
		return w
	}
	w := get("staff", "/api/purchase-requests?limit=2&offset=2")
	if w.Code != 200 || w.Header().Get("X-Total-Count") != "3" {
		t.Fatalf("건수 헤더: %d %q %s", w.Code, w.Header().Get("X-Total-Count"), w.Body.String())
	}
	if w.Body.String()[0] != '[' {
		t.Fatalf("본문은 배열이어야 함: %s", w.Body.String())
	}
	if w := get("staff2", "/api/purchase-requests"); w.Header().Get("X-Total-Count") != "0" {
		t.Fatalf("타 부서 건이 건수에 섞임: %q", w.Header().Get("X-Total-Count"))
	}
	// 발주: 소액 두 건을 결재해 발주를 만들고 실제 건수를 본다(여러 상태 값, 쪽 이동, 열람 범위).
	_, _, raw := s.do("staff", "GET", "/api/purchase-requests?limit=2", nil)
	var prs []map[string]any
	if err := json.Unmarshal([]byte(raw), &prs); err != nil || len(prs) != 2 {
		t.Fatalf("구매요청 목록: %v %s", err, raw)
	}
	for _, pr := range prs {
		s.must("staff", "POST", "/api/purchase-requests/"+id(pr)+"/submit", nil, 200)
		s.must("lead", "POST", "/api/approvals/"+id(pr)+"/decision", map[string]any{"approve": true}, 200)
	}
	for path, want := range map[string]string{
		"/api/purchase-orders":                              "2",
		"/api/purchase-orders?status=open,partial":          "2",
		"/api/purchase-orders?status=open":                  "2",
		"/api/purchase-orders?status=partial,received":      "0",
		"/api/purchase-orders?status=open&limit=1&offset=1": "2",
	} {
		if w := get("staff", path); w.Code != 200 || w.Header().Get("X-Total-Count") != want {
			t.Fatalf("%s: %d %q (기대 %s) %s", path, w.Code, w.Header().Get("X-Total-Count"), want, w.Body.String())
		}
	}
	if w := get("staff", "/api/purchase-orders?status=open,partial&limit=1&offset=1"); strings.Count(w.Body.String(), `"no":`) != 1 {
		t.Fatalf("쪽 조각: %s", w.Body.String())
	}
	if w := get("staff2", "/api/purchase-orders"); w.Header().Get("X-Total-Count") != "0" {
		t.Fatalf("타 부서 발주가 건수에 섞임: %q", w.Header().Get("X-Total-Count"))
	}
}
