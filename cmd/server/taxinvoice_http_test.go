package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/choibongseok/procure-ledger/internal/access"
	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/taxaspadmin"
)

// B43 1단계: 조립된 서버의 라우트 + 서비스 + 전송 작업(모의 대행사)을 처음부터 끝까지 돌린다. 권한은 라우트 선언이 판정한다.
func TestTaxInvoiceHTTPFlowWithMockASP(t *testing.T) {
	rt, db, d := fullEnv(t)
	ctx := context.Background()
	principal := func(email string) contract.Principal {
		var p contract.Principal
		var role string
		if err := db.QueryRowContext(ctx, `SELECT id, name, role, department_id FROM users WHERE email = ?`, email).Scan(&p.UserID, &p.Name, &role, &p.DepartmentID); err != nil {
			t.Fatal(err)
		}
		p.Role, p.Channel, p.Auth = contract.Role(role), contract.ChannelWeb, contract.AuthSession
		var err error
		if p.Perms, err = access.PermsOf(ctx, db, p.UserID); err != nil {
			t.Fatal(err)
		}
		return p
	}
	h := rt.Handler()
	do := func(p contract.Principal, method, path string, body any) *httptest.ResponseRecorder {
		var rd *bytes.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rd = bytes.NewReader(b)
		} else {
			rd = bytes.NewReader(nil)
		}
		req := httptest.NewRequest(method, path, rd).WithContext(contract.WithPrincipal(ctx, p))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w
	}
	fin, admin, staff, auditor := principal("finance@pl.test"), principal("admin@pl.test"), principal("staff@pl.test"), principal("auditor@pl.test")
	// 회사 정보가 있어야 발행된다. 대행사는 모의로 켠다(대행사 설정 저장소 tax_asp_settings).
	if _, err := d.Company.Update(contract.WithPrincipal(ctx, admin), domain.CompanyInput{Name: "에이엑스 주식회사", BusinessNo: "123-45-67890", Representative: "홍길동", Address: "서울"}, 0); err != nil {
		t.Fatal(err)
	}
	actx := contract.WithPrincipal(ctx, admin)
	if _, err := d.TaxASP.Save(actx, taxaspadmin.SaveInput{Provider: "mock"}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.TaxASP.SetActive(actx, true); err != nil {
		t.Fatal(err)
	}
	today := time.Now().In(contract.KST).Format("2006-01-02")
	body := map[string]any{"taxType": "taxable", "issueDate": today, "buyer": map[string]any{"bizNo": "220-81-62517", "name": "한빛상사", "rep": "김", "email": "tax@hanbit.example"},
		"lines": []map[string]any{{"name": "복사용지", "qty": 10, "unitPrice": 10000, "supply": 100000}}}
	// 권한: 직원은 읽기·쓰기 모두 403, 감사는 읽기만, 관리자는 읽기만.
	if w := do(staff, "GET", "/api/finance/tax-invoices", nil); w.Code != http.StatusForbidden {
		t.Fatalf("직원 읽기: %d", w.Code)
	}
	for _, p := range []contract.Principal{auditor, admin} {
		if w := do(p, "POST", "/api/finance/tax-invoices", body); w.Code != http.StatusForbidden {
			t.Fatalf("%s 작성: %d", p.Role, w.Code)
		}
		if w := do(p, "GET", "/api/finance/tax-invoices", nil); w.Code != http.StatusOK {
			t.Fatalf("%s 읽기: %d", p.Role, w.Code)
		}
	}
	w := do(fin, "POST", "/api/finance/tax-invoices", body)
	if w.Code != http.StatusCreated {
		t.Fatalf("작성: %d %s", w.Code, w.Body.String())
	}
	var created map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &created)
	id := int64(created["id"].(float64))
	if created["status"] != "draft" || created["statusName"] != "작성 중" || created["taxTotal"].(float64) != 10000 {
		t.Fatalf("작성 결과: %v", created)
	}
	if em := created["buyer"].(map[string]any)["email"].(string); em == "" || strings.Contains(em, "tax@hanbit.example") {
		t.Fatalf("이메일은 가려서 나간다: %q", em)
	}
	base := "/api/finance/tax-invoices/" + itoa(id)
	if w := do(auditor, "POST", base+"/issue", nil); w.Code != http.StatusForbidden {
		t.Fatalf("감사 발행: %d", w.Code)
	}
	if w := do(fin, "POST", base+"/issue", nil); w.Code != http.StatusOK {
		t.Fatalf("발행: %d %s", w.Code, w.Body.String())
	}
	// 전송 작업을 두 번 돌려(전송, 수신 조회) 국세청 수신까지 간다. 작업은 서버가 돌리는 TaxWorker 와 같은 구성이다.
	type ticker interface {
		Tick(context.Context) (int, error)
	}
	worker, ok := d.TaxWorker.(ticker)
	if !ok {
		t.Fatal("전송 작업을 찾을 수 없다")
	}
	for i := 0; i < 2; i++ {
		time.Sleep(1100 * time.Millisecond) // 수신 조회는 전송 1분 뒤로 예약되므로 시험은 예약 시각을 당겨 둔다(아래 UPDATE)
		if _, err := db.Exec(`UPDATE tax_invoices SET next_try_at = '2000-01-01T00:00:00Z' WHERE id = ? AND next_try_at <> ''`, id); err != nil {
			t.Fatal(err)
		}
		if _, err := worker.Tick(ctx); err != nil {
			t.Fatal(err)
		}
	}
	w = do(fin, "GET", base, nil)
	var got map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if got["status"] != "accepted" || got["statusName"] != "국세청 수신" || len(got["approvalNo"].(string)) != 24 {
		t.Fatalf("국세청 수신: %d %v", w.Code, got)
	}
	if tr, _ := got["transmissions"].([]any); len(tr) < 2 {
		t.Fatalf("전송 기록: %v", got["transmissions"])
	}
	// 수기 등록 중복 승인번호는 400.
	recv := map[string]any{"approvalNo": "20261108ABCDEFGH12345678", "issueDate": today, "supplier": map[string]any{"bizNo": "1068123456", "name": "대한물산", "rep": "박"}, "supplyTotal": 5000, "taxTotal": 500}
	if w := do(fin, "POST", "/api/finance/tax-invoices/receive", recv); w.Code != http.StatusCreated {
		t.Fatalf("수기 등록: %d %s", w.Code, w.Body.String())
	}
	if w := do(fin, "POST", "/api/finance/tax-invoices/receive", recv); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "이미 등록된 승인번호") {
		t.Fatalf("중복 승인번호: %d %s", w.Code, w.Body.String())
	}
	// 목록 필터.
	w = do(auditor, "GET", "/api/finance/tax-invoices?direction=purchase", nil)
	var list struct {
		Rows  []map[string]any `json:"rows"`
		Total int              `json:"total"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &list)
	if w.Code != 200 || list.Total != 1 || list.Rows[0]["directionName"] != "매입" {
		t.Fatalf("매입 목록: %d %s", w.Code, w.Body.String())
	}
	// 감사 기록이 남았다(작성·발행·전송·수신·수기 등록).
	for _, a := range []string{"taxinvoice.create", "taxinvoice.issue", "taxinvoice.send", "taxinvoice.accept", "taxinvoice.manual_receive"} {
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM audit_logs WHERE action = ? AND result = 'success'`, a).Scan(&n); err != nil || n == 0 {
			t.Errorf("감사 %s: %d %v", a, n, err)
		}
	}
}
