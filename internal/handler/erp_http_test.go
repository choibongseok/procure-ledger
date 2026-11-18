package handler_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/access"
	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/deps"
	"github.com/choibongseok/procure-ledger/internal/contract/fake"
	"github.com/choibongseok/procure-ledger/internal/dbtest"
	"github.com/choibongseok/procure-ledger/internal/handler"
	"github.com/choibongseok/procure-ledger/internal/service"
	"github.com/choibongseok/procure-ledger/migrations"
)

var principals = map[string]contract.Principal{
	"staff":   {UserID: 1, Name: "직원", Role: contract.RoleStaff, DepartmentID: 1},
	"lead":    {UserID: 2, Name: "팀장", Role: contract.RoleManager, DepartmentID: 1},
	"head":    {UserID: 3, Name: "부서장", Role: contract.RoleManager, DepartmentID: 1},
	"finance": {UserID: 4, Name: "재무", Role: contract.RoleFinance, DepartmentID: 3},
	"staff2":  {UserID: 5, Name: "영업직원", Role: contract.RoleStaff, DepartmentID: 2},
	"auditor": {UserID: 6, Name: "감사", Role: contract.RoleAuditor, DepartmentID: 3},
	"admin":   {UserID: 7, Name: "관리자", Role: contract.RoleAdmin, DepartmentID: 3},
	"lead3":   {UserID: 8, Name: "구매팀장2", Role: contract.RoleManager, DepartmentID: 1},
	"ceo":     {UserID: 9, Name: "대표", Role: contract.RoleManager, DepartmentID: 3},
}

// testOrg 는 시드 사용자를 가짜 조직(회사 > 부서 3개 > 팀)에 앉힌다. 구매팀 부서장은 head(3), 대표는 9번이다.
func testOrg() *fake.Org {
	o := fake.NewOrg()
	o.AddUnit(100, "PL", "PL 주식회사", contract.OrgCompany, 0)
	o.AddUnit(1, "BUY", "구매팀", contract.OrgDepartment, 100)
	o.AddUnit(2, "SAL", "영업팀", contract.OrgDepartment, 100)
	o.AddUnit(3, "FIN", "재무팀", contract.OrgDepartment, 100)
	o.AddUnit(11, "BUY1", "구매1팀", contract.OrgTeam, 1)
	o.AddUnit(12, "BUY2", "구매2팀", contract.OrgTeam, 1)
	o.AddUnit(13, "SAL1", "영업1팀", contract.OrgTeam, 2)
	for id, unit := range map[int64]int64{1: 11, 2: 11, 3: 1, 4: 3, 5: 13, 6: 3, 7: 3, 8: 12, 9: 100} {
		o.AddPerson(id, "사용자", "과장", unit, "", "", "")
	}
	o.SetHead(11, 2).SetHead(12, 8).SetHead(1, 3).SetHead(100, 9)
	return o
}

type server struct {
	t   *testing.T
	rt  *contract.RouteTable
	h   http.Handler
	rec *fake.Recorder
	db  *sql.DB
}

// newServer 는 실제 서비스·SQLite 로 라우트를 조립한다. 헤더 X-Test-User 가 인증 미들웨어를 대신한다.
func newServer(t *testing.T) *server {
	t.Helper()
	db := dbtest.Open(t)
	if err := migrations.Apply(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	if _, err := access.SyncTemplates(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO departments (id, name) VALUES (1,'구매팀'),(2,'영업팀'),(3,'재무팀')`,
		`INSERT INTO users (id, email, name, role, position, department_id) VALUES
		 (1,'a@t','직원','staff',NULL,1),(2,'b@t','팀장','manager','team_lead',1),(3,'c@t','부서장','manager','dept_head',1),
		 (4,'d@t','재무','finance',NULL,3),(5,'e@t','영업직원','staff',NULL,2),(6,'f@t','감사','auditor',NULL,3),(7,'g@t','관리자','admin',NULL,3),(8,'h@t','구매팀장2','manager','team_lead',1),(9,'i@t','대표','manager','dept_head',3)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	rec := fake.NewRecorder()
	txs := contract.SQLTxManager{DB: db}
	b := service.Base{DB: db, Txs: txs, Audit: rec, Masker: fake.Masker{}, Org: testOrg()}
	d := deps.Deps{DB: db, Txs: txs, Audit: rec, Masker: fake.Masker{},
		Purchase: service.NewPurchaseService(b), Approval: service.NewApprovalService(b), Inventory: service.NewInventoryService(b), Delegation: service.NewDelegationService(b),
		Line: service.NewLineService(b), Reassign: service.NewReassignService(b), Notification: service.NewNotificationService(b)}
	rt := contract.NewRouteTable()
	handler.RegisterPurchase(rt, d)
	handler.RegisterApproval(rt, d)
	handler.RegisterInventory(rt, d)
	handler.RegisterNotification(rt, d)
	inner := rt.Handler()
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if p, ok := principals[r.Header.Get("X-Test-User")]; ok {
			p.Channel = contract.ChannelWeb
			r = r.WithContext(contract.WithPrincipal(r.Context(), p))
		}
		inner.ServeHTTP(w, r)
	})
	return &server{t: t, rt: rt, h: h, rec: rec, db: db}
}

func (s *server) do(user, method, path string, body any) (int, map[string]any, string) {
	s.t.Helper()
	var rd *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, rd)
	if user != "" {
		req.Header.Set("X-Test-User", user)
	}
	w := httptest.NewRecorder()
	s.h.ServeHTTP(w, req)
	var m map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &m)
	return w.Code, m, w.Body.String()
}

func (s *server) must(user, method, path string, body any, want int) map[string]any {
	s.t.Helper()
	code, m, raw := s.do(user, method, path, body)
	if code != want {
		s.t.Fatalf("%s %s (%s): %d %s (기대 %d)", method, path, user, code, raw, want)
	}
	return m
}

func id(m map[string]any) string { return strconv.FormatInt(int64(m["id"].(float64)), 10) }

// 선언 누락 검사: 등록된 모든 라우트가 알려진 권한을 선언했고, 선언대로 강제된다.
// 미인증은 401, 권한 없는 역할은 핸들러에 닿기 전에 403 이다(핸들러 안 역할 분기 없음).
func TestPermissionDeclarationERPRoutes(t *testing.T) {
	s := newServer(t)
	routes := s.rt.Routes()
	if len(routes) < 15 {
		t.Fatalf("t2 라우트가 %d 개뿐", len(routes))
	}
	if problems := contract.CheckRoutes(routes); len(problems) != 0 {
		t.Fatal(problems)
	}
	roles := map[string]contract.Role{}
	for name, p := range principals {
		roles[name] = p.Role
	}
	for _, r := range routes {
		if r.Permission == contract.PermPublic || r.Permission == contract.PermAuthenticated {
			t.Errorf("%s %s: 업무 라우트는 구체 권한을 선언해야 함(%s)", r.Method, r.Pattern, r.Permission)
		}
		path := strings.NewReplacer("{id}", "1").Replace(r.Pattern)
		if code, _, _ := s.do("", r.Method, path, nil); code != http.StatusUnauthorized {
			t.Errorf("%s %s: 미인증 %d", r.Method, r.Pattern, code)
		}
		for name, role := range roles {
			if contract.RoleHas(role, r.Permission) {
				continue
			}
			if code, _, raw := s.do(name, r.Method, path, map[string]any{}); code != http.StatusForbidden {
				t.Errorf("%s %s: %s(%s) 가 %d %s (403 기대)", r.Method, r.Pattern, name, role, code, raw)
			}
		}
	}
}

func TestHTTPFlowPurchaseToReceipt(t *testing.T) {
	s := newServer(t)
	item := s.must("finance", "POST", "/api/items", map[string]any{"sku": "PAPER", "name": "복사용지", "unit": "박스", "safetyStock": 5, "trackLot": true}, 201)
	vendor := s.must("finance", "POST", "/api/vendors", map[string]any{"name": "대한문구", "businessNo": "123-45-67890"}, 201)
	if vendor["businessNo"] != "123-45-67890" {
		t.Fatalf("재무는 원문: %v", vendor)
	}
	vs := s.must("staff", "GET", "/api/vendors", nil, 200) // 배열이라 m 은 nil
	_ = vs
	_, _, raw := s.do("staff", "GET", "/api/vendors", nil)
	if strings.Contains(raw, "123-45-67890") {
		t.Fatalf("직원 응답에 사업자번호 원문: %s", raw)
	}

	itemID, vendorID := int64(item["id"].(float64)), int64(vendor["id"].(float64))
	pr := s.must("staff", "POST", "/api/purchase-requests", map[string]any{
		"vendorId": vendorID, "title": "용지 구매", "reason": "재고 부족",
		"lines": []map[string]any{{"itemId": itemID, "quantity": 10, "unitPrice": 20000}},
	}, 201)
	if pr["status"] != "draft" || pr["totalAmount"].(float64) != 200000 {
		t.Fatalf("임시저장: %v", pr)
	}
	prID := id(pr)
	if got := s.must("staff", "POST", "/api/purchase-requests/"+prID+"/submit", nil, 200); got["status"] != "submitted" {
		t.Fatalf("상신: %v", got)
	}

	// 결재함: 팀장에게는 보이고 직원에게는 비어 있다.
	if _, _, raw := s.do("lead", "GET", "/api/approvals/pending", nil); !strings.Contains(raw, `"id":`+prID) {
		t.Fatalf("팀장 결재함: %s", raw)
	}
	if _, _, raw := s.do("staff", "GET", "/api/approvals/pending", nil); strings.TrimSpace(raw) != "[]" {
		t.Fatalf("직원 결재함: %s", raw)
	}

	// 반려는 사유 필수(400), 직원은 결재 불가(403), 타 부서는 존재 은닉(404).
	s.must("lead", "POST", "/api/approvals/"+prID+"/decision", map[string]any{"approve": false}, 400)
	s.must("staff", "POST", "/api/approvals/"+prID+"/decision", map[string]any{"approve": true}, 403)
	s.must("staff2", "GET", "/api/purchase-requests/"+prID, nil, 404)

	res := s.must("lead", "POST", "/api/approvals/"+prID+"/decision", map[string]any{"approve": true}, 200)
	po, _ := res["purchaseOrder"].(map[string]any)
	req, _ := res["request"].(map[string]any)
	if po == nil || req["status"] != "ordered" {
		t.Fatalf("승인 결과: %v", res)
	}
	poID := id(po)
	lineID := int64(po["lines"].([]any)[0].(map[string]any)["id"].(float64))

	// 입고: 로트 필수(400), 초과(409), 정상(201), 권한(403).
	s.must("finance", "POST", "/api/receipts", map[string]any{"poId": po["id"], "lines": []map[string]any{{"poLineId": lineID, "quantity": 3}}}, 400)
	s.must("finance", "POST", "/api/receipts", map[string]any{"poId": po["id"], "lines": []map[string]any{{"poLineId": lineID, "quantity": 11, "lotNo": "L1"}}}, 409)
	s.must("staff", "POST", "/api/receipts", map[string]any{"poId": po["id"], "lines": []map[string]any{{"poLineId": lineID, "quantity": 1, "lotNo": "L1"}}}, 403)
	rc := s.must("finance", "POST", "/api/receipts", map[string]any{"poId": po["id"], "lines": []map[string]any{{"poLineId": lineID, "quantity": 4, "lotNo": "L1", "expiryDate": "2030-01-01"}}}, 201)
	if rc["poId"] != po["id"] {
		t.Fatalf("입고: %v", rc)
	}
	if got := s.must("staff", "GET", "/api/purchase-orders/"+poID, nil, 200); got["status"] != "partial" {
		t.Fatalf("PO 상태: %v", got["status"])
	}
	st := s.must("staff", "GET", "/api/items/"+id(item)+"/stock", nil, 200)
	if st["onHand"].(float64) != 4 || st["belowSafety"] != true {
		t.Fatalf("재고: %v", st)
	}
	// 이력은 읽기만 가능: 수정·삭제 메서드는 라우트가 없다.
	if code, _, _ := s.do("lead", "DELETE", "/api/approvals/"+prID+"/history", nil); code == 200 || code == 204 {
		t.Fatalf("이력 삭제 경로가 있음: %d", code)
	}
	if code, _, _ := s.do("finance", "PUT", "/api/items/"+id(item)+"/stock", map[string]any{"onHand": 99}); code != http.StatusMethodNotAllowed && code != http.StatusNotFound {
		t.Fatalf("재고 수량 직접 수정 경로가 있음: %d", code)
	}
}

// 결재함 탭 API: 권한은 /pending 과 같고(미인증 401), 탭 이름이 틀리면 400, 건이 없으면 빈 목록이다.
func TestHTTPApprovalInboxTabs(t *testing.T) {
	s := newServer(t)
	if code, _, _ := s.do("", "GET", "/api/approvals/inbox?tab=progress", nil); code != http.StatusUnauthorized {
		t.Fatalf("미인증: %d", code)
	}
	s.must("lead", "GET", "/api/approvals/inbox?tab=nope", nil, 400)
	for _, tab := range []string{"pending", "progress", "done"} {
		if _, _, raw := s.do("staff", "GET", "/api/approvals/inbox?tab="+tab, nil); strings.TrimSpace(raw) != "[]" {
			t.Fatalf("직원 %s 탭: %s", tab, raw)
		}
	}
}

// 상세 응답의 editable 키는 false 일 때도 내려간다(화면이 "상세 값 우선" 으로 쓴다).
func TestHTTPPurchaseDetailAlwaysCarriesEditable(t *testing.T) {
	s := newServer(t)
	item := s.must("finance", "POST", "/api/items", map[string]any{"sku": "E", "name": "e"}, 201)
	vendor := s.must("finance", "POST", "/api/vendors", map[string]any{"name": "v"}, 201)
	pr := s.must("staff", "POST", "/api/purchase-requests", map[string]any{
		"vendorId": vendor["id"], "title": "수정 가능 표시", "lines": []map[string]any{{"itemId": item["id"], "quantity": 1, "unitPrice": 1000}},
	}, 201)
	got := s.must("staff", "GET", "/api/purchase-requests/"+id(pr), nil, 200)
	if v, ok := got["editable"]; !ok || v != true {
		t.Fatalf("임시저장 본인: editable=%v 키있음=%v", v, ok)
	}
	s.must("staff", "POST", "/api/purchase-requests/"+id(pr)+"/submit", nil, 200)
	got = s.must("staff", "GET", "/api/purchase-requests/"+id(pr), nil, 200)
	if v, ok := got["editable"]; !ok || v != false {
		t.Fatalf("상신 뒤에도 editable 키가 false 로 내려가야 한다: editable=%v 키있음=%v", v, ok)
	}
}

func TestHTTPSelfApprovalIs403(t *testing.T) {
	s := newServer(t)
	item := s.must("finance", "POST", "/api/items", map[string]any{"sku": "S", "name": "s"}, 201)
	vendor := s.must("finance", "POST", "/api/vendors", map[string]any{"name": "v"}, 201)
	pr := s.must("lead", "POST", "/api/purchase-requests", map[string]any{
		"vendorId": vendor["id"], "title": "팀장 구매", "lines": []map[string]any{{"itemId": item["id"], "quantity": 1, "unitPrice": 1000}},
	}, 201)
	s.must("lead", "POST", "/api/purchase-requests/"+id(pr)+"/submit", nil, 200)
	code, _, raw := s.do("lead", "POST", "/api/approvals/"+id(pr)+"/decision", map[string]any{"approve": true})
	if code != 403 || !strings.Contains(raw, "본인이 올린 요청") {
		t.Fatalf("자기 결재: %d %s", code, raw)
	}
}

func TestHTTPErrorsAreKoreanAndHideInternals(t *testing.T) {
	s := newServer(t)
	for _, c := range []struct {
		user, method, path string
		body               any
		want               int
	}{
		{"staff", "GET", "/api/purchase-requests/abc", nil, 400},
		{"staff", "GET", "/api/purchase-requests/999", nil, 404},
		{"staff", "POST", "/api/purchase-requests", map[string]any{"title": ""}, 400},
		{"finance", "POST", "/api/items", map[string]any{"sku": "", "name": ""}, 400},
		{"staff", "GET", "/api/stock/expiring?days=-5", nil, 400},
	} {
		code, _, raw := s.do(c.user, c.method, c.path, c.body)
		if code != c.want {
			t.Errorf("%s %s: %d %s (기대 %d)", c.method, c.path, code, raw, c.want)
		}
		if strings.Contains(raw, "sql") || strings.Contains(raw, "constraint") || strings.Contains(raw, "ErrValidation") {
			t.Errorf("내부 정보 노출: %s", raw)
		}
	}
}

// PO·입고 조회도 원 구매요청의 부서를 따른다: 타 부서 직원은 404/빈 목록, 재무·관리자·감사는 200.
func TestHTTPPurchaseOrderAndReceiptDepartmentIsolation(t *testing.T) {
	s := newServer(t)
	item := s.must("finance", "POST", "/api/items", map[string]any{"sku": "ISO", "name": "격리"}, 201)
	vendor := s.must("finance", "POST", "/api/vendors", map[string]any{"name": "v"}, 201)
	pr := s.must("staff", "POST", "/api/purchase-requests", map[string]any{
		"vendorId": vendor["id"], "title": "구매팀 요청", "lines": []map[string]any{{"itemId": item["id"], "quantity": 5, "unitPrice": 1000}},
	}, 201)
	s.must("staff", "POST", "/api/purchase-requests/"+id(pr)+"/submit", nil, 200)
	res := s.must("lead", "POST", "/api/approvals/"+id(pr)+"/decision", map[string]any{"approve": true}, 200)
	po := res["purchaseOrder"].(map[string]any)
	lineID := po["lines"].([]any)[0].(map[string]any)["id"]
	rc := s.must("finance", "POST", "/api/receipts", map[string]any{"poId": po["id"], "lines": []map[string]any{{"poLineId": lineID, "quantity": 2}}}, 201)

	poPath, rcPath := "/api/purchase-orders/"+id(po), "/api/receipts/"+id(rc)
	s.must("staff2", "GET", poPath, nil, 404)
	s.must("staff2", "GET", rcPath, nil, 404)
	if _, _, raw := s.do("staff2", "GET", "/api/purchase-orders", nil); strings.TrimSpace(raw) != "[]" {
		t.Fatalf("타 부서 PO 목록 노출: %s", raw)
	}
	s.must("staff2", "POST", "/api/receipts", map[string]any{"poId": po["id"], "lines": []map[string]any{{"poLineId": lineID, "quantity": 1}}}, 403)
	for _, u := range []string{"staff", "lead", "finance", "admin", "auditor"} {
		s.must(u, "GET", poPath, nil, 200)
		s.must(u, "GET", rcPath, nil, 200)
		if _, _, raw := s.do(u, "GET", "/api/purchase-orders", nil); !strings.Contains(raw, `"id":`+id(po)) {
			t.Fatalf("%s 의 PO 목록에 없음: %s", u, raw)
		}
	}
}
