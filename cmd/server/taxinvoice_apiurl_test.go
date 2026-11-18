package main

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/access"
	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
)

type taxAPIRow struct {
	ID         int64  `json:"id"`
	No         string `json:"no"`
	ApprovalNo string `json:"approval_no"`
}

// 웹훅 본문의 api_url(/api/v1/tax-invoices?id=N)을 따라가면 그 건이 나온다. 다른 자원도 같은 규칙이다.
func TestAPIv1IDParamReturnsThatOneRecord(t *testing.T) {
	e := newWriteEnv(t, "tax_invoices:read", "items:read")
	ctx := context.Background()
	actx := contract.WithPrincipal(ctx, e.admin)
	if _, err := e.d.Company.Update(actx, domain.CompanyInput{Name: "에이엑스 주식회사", BusinessNo: "123-45-67890", Representative: "홍길동", Address: "서울"}, 0); err != nil {
		t.Fatal(err)
	}
	var p contract.Principal
	var role string
	if err := e.d.DB.QueryRowContext(ctx, `SELECT id, name, role, department_id FROM users WHERE email = 'finance@pl.test'`).Scan(&p.UserID, &p.Name, &role, &p.DepartmentID); err != nil {
		t.Fatal(err)
	}
	p.Role, p.Channel, p.Auth = contract.Role(role), contract.ChannelWeb, contract.AuthSession
	var err error
	if p.Perms, err = access.PermsOf(ctx, e.d.DB, p.UserID); err != nil {
		t.Fatal(err)
	}
	fctx := contract.WithPrincipal(ctx, p)
	var ids []int64
	for i := 1; i <= 3; i++ {
		v, err := e.d.TaxInvoice.RegisterReceived(fctx, domain.TaxReceivedInput{ApprovalNo: fmt.Sprintf("20261110APIURL%010d", i), IssueDate: "2026-11-10",
			Supplier: domain.TaxParty{BizNo: "9998887776", Name: "공급사", Rep: "x"}, SupplyTotal: int64(1000 * i), TaxTotal: int64(100 * i)})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, v.ID)
	}
	w := e.do("GET", fmt.Sprintf("/api/v1/tax-invoices?id=%d", ids[1]), e.tok, nil, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("상태 %d %s", w.Code, w.Body.String())
	}
	r := decode[[]taxAPIRow](t, w)
	if len(r.Data) != 1 || r.Data[0].ID != ids[1] || r.Data[0].ApprovalNo != "20261110APIURL0000000002" {
		t.Fatalf("?id= 는 그 건만 돌려줘야 한다: %s", w.Body.String())
	}
	// 없는 번호는 빈 목록, 잘못된 값은 거부.
	if r := decode[[]taxAPIRow](t, e.do("GET", "/api/v1/tax-invoices?id=999999", e.tok, nil, nil)); len(r.Data) != 0 {
		t.Fatalf("없는 번호: %+v", r.Data)
	}
	if w := e.do("GET", "/api/v1/tax-invoices?id=abc", e.tok, nil, nil); w.Code != http.StatusBadRequest {
		t.Fatalf("잘못된 id: %d", w.Code)
	}
	// 다른 자원(품목)도 같은 규칙: 목록의 두 번째 품목 번호로 한 건만 나온다.
	all := decode[[]struct {
		ID int64 `json:"id"`
	}](t, e.do("GET", "/api/v1/items", e.tok, nil, nil))
	if len(all.Data) < 2 {
		t.Skip("시험할 품목이 둘 미만")
	}
	one := decode[[]struct {
		ID int64 `json:"id"`
	}](t, e.do("GET", fmt.Sprintf("/api/v1/items?id=%d", all.Data[1].ID), e.tok, nil, nil))
	if len(one.Data) != 1 || one.Data[0].ID != all.Data[1].ID {
		t.Fatalf("품목 ?id=: %+v", one.Data)
	}
}
