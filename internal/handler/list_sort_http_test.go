package handler_test

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

// 목록 정렬: 허용 열만 받고(그 밖은 400), 기본은 작성일 내림차순이며, 건수 헤더·offset·부서 격리와 같이 동작한다.
func TestHTTPPurchaseListSort(t *testing.T) {
	s := newServer(t)
	item := s.must("finance", "POST", "/api/items", map[string]any{"sku": "P", "name": "용지", "unit": "EA"}, 201)
	vendor := s.must("finance", "POST", "/api/vendors", map[string]any{"name": "대한문구", "businessNo": "123-45-67890"}, 201)
	var ids []string
	for _, price := range []int{300000, 100000, 200000} {
		pr := s.must("staff", "POST", "/api/purchase-requests", map[string]any{
			"vendorId": vendor["id"], "title": "소액", "reason": "x",
			"lines": []map[string]any{{"itemId": item["id"], "quantity": 1, "unitPrice": price}},
		}, 201)
		ids = append(ids, id(pr))
	}
	get := func(user, path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", path, nil)
		req.Header.Set("X-Test-User", user)
		w := httptest.NewRecorder()
		s.h.ServeHTTP(w, req)
		return w
	}
	order := func(w *httptest.ResponseRecorder) []string {
		var rows []map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &rows); err != nil {
			t.Fatalf("본문: %v %s", err, w.Body.String())
		}
		var out []string
		for _, r := range rows {
			out = append(out, id(r))
		}
		return out
	}
	eq := func(got []string, want ...string) {
		t.Helper()
		if len(got) != len(want) {
			t.Fatalf("순서 %v (기대 %v)", got, want)
		}
		for i := range got {
			if got[i] != want[i] {
				t.Fatalf("순서 %v (기대 %v)", got, want)
			}
		}
	}
	// 기본: 작성일 내림차순(같은 시각이면 id 내림차순) = 나중에 올린 것이 먼저.
	eq(order(get("staff", "/api/purchase-requests")), ids[2], ids[1], ids[0])
	eq(order(get("staff", "/api/purchase-requests?sort=created_at&order=asc")), ids[0], ids[1], ids[2])
	eq(order(get("staff", "/api/purchase-requests?sort=total_amount&order=desc")), ids[0], ids[2], ids[1])
	eq(order(get("staff", "/api/purchase-requests?sort=total_amount")), ids[1], ids[2], ids[0])
	// 쪽 이동과 건수: 정렬은 offset 앞에 적용되고 건수는 그대로다.
	w := get("staff", "/api/purchase-requests?sort=total_amount&order=desc&limit=1&offset=1")
	eq(order(w), ids[2])
	if w.Header().Get("X-Total-Count") != "3" {
		t.Fatalf("건수 %q", w.Header().Get("X-Total-Count"))
	}
	// 부서 격리: 타 부서는 정렬해도 0건.
	if w := get("staff2", "/api/purchase-requests?sort=total_amount&order=desc"); w.Code != 200 || w.Header().Get("X-Total-Count") != "0" || order(w) != nil {
		t.Fatalf("타 부서: %d %q %s", w.Code, w.Header().Get("X-Total-Count"), w.Body.String())
	}
	// 허용 밖(열 이름 주입 포함)은 400.
	for _, p := range []string{"sort=title", "sort=id%3Bdrop", "sort=total_amount&order=sideways", "order=up"} {
		if w := get("staff", "/api/purchase-requests?"+p); w.Code != 400 {
			t.Fatalf("%s: %d %s", p, w.Code, w.Body.String())
		}
	}
	// 정렬과 afterId 커서는 같이 쓸 수 없다.
	if w := get("staff", "/api/purchase-requests?sort=total_amount&afterId=1"); w.Code != 400 || !strings.Contains(w.Body.String(), "같이 쓸 수 없습니다") {
		t.Fatalf("정렬+커서: %d %s", w.Code, w.Body.String())
	}
}

func TestHTTPStockListSort(t *testing.T) {
	s := newServer(t)
	for _, n := range []string{"나 용지", "가 용지", "다 용지"} {
		s.must("finance", "POST", "/api/items", map[string]any{"sku": n, "name": n, "unit": "EA", "safetyStock": 5}, 201)
	}
	get := func(path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", path, nil)
		req.Header.Set("X-Test-User", "finance")
		w := httptest.NewRecorder()
		s.h.ServeHTTP(w, req)
		return w
	}
	names := func(w *httptest.ResponseRecorder) []string {
		var rows []struct {
			Item struct {
				Name string `json:"name"`
			} `json:"item"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &rows); err != nil {
			t.Fatalf("본문: %v %s", err, w.Body.String())
		}
		var out []string
		for _, r := range rows {
			out = append(out, r.Item.Name)
		}
		return out
	}
	if got := names(get("/api/stock/below-safety?sort=name&order=asc")); len(got) != 3 || got[0] != "가 용지" || got[2] != "다 용지" {
		t.Fatalf("품목명 오름차순: %v", got)
	}
	if got := names(get("/api/stock/below-safety?sort=name&order=desc")); len(got) != 3 || got[0] != "다 용지" {
		t.Fatalf("품목명 내림차순: %v", got)
	}
	for _, p := range []string{"/api/stock/below-safety?sort=sku", "/api/stock/expiring?sort=quantity", "/api/stock/expiring?order=x"} {
		if w := get(p); w.Code != 400 {
			t.Fatalf("%s: %d", p, w.Code)
		}
	}
	if w := get("/api/stock/expiring?sort=expiry_date&order=desc"); w.Code != 200 {
		t.Fatalf("유효기한 정렬: %d %s", w.Code, w.Body.String())
	}
}
