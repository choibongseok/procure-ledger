package main

// B35 공통 가져오기 라우트를 실제 라우트·세션·권한 표로 태운다(대상 어댑터는 erp 가 붙이므로 여기서는 연결 전 상태).

import (
	"net/http"
	"strings"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/contract"
)

func TestImportRoutesPermissionDeclarations(t *testing.T) {
	e := newPlatformEnv(t, false)
	got := map[string]contract.Permission{}
	for _, r := range e.rt.Routes() {
		got[r.Method+" "+r.Pattern] = r.Permission
	}
	want := map[string]contract.Permission{
		"GET /api/imports/targets":                                 contract.PermAuthenticated,
		"GET /api/imports":                                         contract.PermImportRead,
		"GET /api/imports/{batch}":                                 contract.PermImportRead,
		"GET /api/imports/{batch}/errors":                          contract.PermImportRead,
		"GET /api/imports/templates/item":                          contract.PermImportMaster,
		"POST /api/imports/vendor/preview":                         contract.PermImportMaster,
		"POST /api/imports/account/batches/{batch}/apply":          contract.PermImportMaster,
		"POST /api/imports/warehouse/batches/{batch}/cancel":       contract.PermImportMaster,
		"GET /api/imports/budget/batches/{batch}/errors":           contract.PermImportBalance,
		"GET /api/imports/templates/opening_stock":                 contract.PermImportBalance,
		"POST /api/imports/opening_payable/preview":                contract.PermImportBalance,
		"POST /api/imports/opening_stock/batches/{batch}/apply":    contract.PermImportBalance,
		"POST /api/imports/opening_payable/batches/{batch}/cancel": contract.PermImportBalance,
	}
	for k, p := range want {
		if got[k] != p {
			t.Errorf("%s: 선언 %q, 기대 %q", k, got[k], p)
		}
	}
	if problems := contract.CheckRoutes(e.rt.Routes()); len(problems) != 0 {
		t.Fatal(problems)
	}
}

func TestImportRoutesByRole(t *testing.T) {
	e := newPlatformEnv(t, true)
	admin, fin, aud, staff := e.browser(), e.browser(), e.browser(), e.browser()
	e.login(admin, "admin@pl.test")
	e.login(fin, "finance@pl.test")
	e.login(aud, "auditor@pl.test")
	e.login(staff, "staff@pl.test")
	// 양식 내려받기: 마스터는 관리자·재무, 금액성은 재무만, 직원·감사는 불가.
	cases := []struct {
		who  string
		c    *http.Client
		path string
		want int
	}{
		{"admin", admin, "/api/imports/templates/item", 200},
		{"admin", admin, "/api/imports/templates/opening_stock", 403},
		{"finance", fin, "/api/imports/templates/opening_stock", 200},
		{"finance", fin, "/api/imports/templates/account", 200},
		{"auditor", aud, "/api/imports/templates/item", 403},
		{"staff", staff, "/api/imports/templates/item", 403},
		{"auditor", aud, "/api/imports", 200},
		{"staff", staff, "/api/imports", 403},
		{"admin", admin, "/api/imports/targets", 200},
		{"staff", staff, "/api/imports/targets", 403},
	}
	for _, tc := range cases {
		code, body := e.call(tc.c, "GET", tc.path, "", nil)
		if code != tc.want {
			t.Errorf("%s GET %s: %d(기대 %d) %.80s", tc.who, tc.path, code, tc.want, body)
		}
	}
	// 양식은 XLSX 이고 대상 카드에는 7개 대상과 작성 안내가 있다.
	if _, body := e.call(admin, "GET", "/api/imports/targets", "", nil); !strings.Contains(body, `"opening_payable"`) || !strings.Contains(body, `"columns"`) {
		t.Errorf("대상 카드: %.200s", body)
	}
	// 어댑터가 연결되지 않은 대상의 미리보기는 409 안내다.
	hdr := map[string]string{"X-CSRF-Token": e.csrf(admin), "Content-Type": "multipart/form-data; boundary=XX"}
	body := "--XX\r\nContent-Disposition: form-data; name=\"file\"; filename=\"a.xlsx\"\r\n\r\nPK\r\n--XX--\r\n"
	if code, b := e.call(admin, "POST", "/api/imports/item/preview", body, hdr); code != 409 || !strings.Contains(b, "연결되지 않았습니다") {
		t.Errorf("어댑터 없음 미리보기: %d %.100s", code, b)
	}
	if code, _ := e.call(staff, "POST", "/api/imports/item/preview", body, map[string]string{"X-CSRF-Token": e.csrf(staff), "Content-Type": "multipart/form-data; boundary=XX"}); code != 403 {
		t.Errorf("직원 미리보기: %d", code)
	}
}
