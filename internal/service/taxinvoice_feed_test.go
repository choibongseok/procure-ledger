package service_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/service"
)

// B43 3단계: 변경 피드(생성·상태 변경 줄, 롤백되면 없음)와 읽기 API 표현(사업자번호 가림, 개인정보 필드 없음).

func (e *env) feedLines(t *testing.T, id int64) []string {
	t.Helper()
	rows, err := e.db.Query(`SELECT op || ':' || status FROM change_events WHERE resource = 'tax_invoice' AND object_id = ? ORDER BY seq`, id)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		out = append(out, s)
	}
	return out
}

func TestTaxInvoiceChangeFeedLines(t *testing.T) {
	e := newEnv(t)
	e.taxCompany(t)
	e.aspOwner(t, uAdmin)
	w, _ := e.taxWorker("none", true)
	v := e.taxDraft(t, 100_000)
	if got := strings.Join(e.feedLines(t, v.ID), ","); got != "created:draft" {
		t.Fatalf("작성: %s", got)
	}
	if _, err := e.tax.Issue(as(uFinance2), v.ID); err != nil {
		t.Fatal(err)
	}
	e.now = e.now.Add(time.Second)
	if _, err := w.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	e.now = e.now.Add(2 * time.Minute)
	if _, err := w.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(e.feedLines(t, v.ID), ","); got != "created:draft,status_changed:issued,status_changed:sent,status_changed:accepted" {
		t.Fatalf("발행~수신: %s", got)
	}
	// 취소도 상태 변경 줄이다.
	c := e.taxDraft(t, 5_000)
	if _, err := e.tax.Cancel(as(uFinance), c.ID, "시험"); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(e.feedLines(t, c.ID), ","); got != "created:draft,status_changed:canceled" {
		t.Fatalf("취소: %s", got)
	}
	// 매입 수기 등록은 국세청 수신 상태로 생성 줄이 남는다. 롤백되면(중복 승인번호) 줄이 없다.
	in := domain.TaxReceivedInput{ApprovalNo: "20261110FEEDRCV000000001", IssueDate: "2026-11-10", Supplier: domain.TaxParty{BizNo: "9998887776", Name: "공급사", Rep: "x"}, SupplyTotal: 1000, TaxTotal: 100}
	r, err := e.tax.RegisterReceived(as(uFinance), in)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(e.feedLines(t, r.ID), ","); got != "created:accepted" {
		t.Fatalf("수기 등록: %s", got)
	}
	var before, after int
	_ = e.db.QueryRow(`SELECT COUNT(*) FROM change_events WHERE resource = 'tax_invoice'`).Scan(&before)
	if _, err := e.tax.RegisterReceived(as(uFinance), in); !errors.Is(err, contract.ErrValidation) {
		t.Fatalf("중복 승인번호: %v", err)
	}
	_ = e.db.QueryRow(`SELECT COUNT(*) FROM change_events WHERE resource = 'tax_invoice'`).Scan(&after)
	if before != after {
		t.Fatalf("거부된 등록이 피드 줄을 남겼다: %d → %d", before, after)
	}
}

func TestTaxInvoiceAPIReadMasksAndHidesPersonalFields(t *testing.T) {
	e := newEnv(t)
	e.taxCompany(t)
	approvalSeq++
	if _, err := e.tax.RegisterReceived(as(uFinance), domain.TaxReceivedInput{ApprovalNo: "20261110APIREAD000000001", IssueDate: "2026-11-10",
		Supplier: domain.TaxParty{BizNo: "9998887776", Name: "공급사", Rep: "대표자", Addr: "서울시 어딘가", Email: "p@x.example"}, SupplyTotal: 2000, TaxTotal: 200,
		Lines: []domain.TaxLineInput{{Name: "비밀 품목", Qty: 1, UnitPrice: 2000, Supply: 2000}}}); err != nil {
		t.Fatal(err)
	}
	read := service.NewAPIReadService(e.journals.Base, e.inventory, e.journals)
	p := users[uFinance]
	p.Channel = contract.ChannelAPI
	rows, more, err := read.TaxInvoices(contract.WithPrincipal(context.Background(), p), 0, 50)
	if err != nil || more || len(rows) != 1 {
		t.Fatalf("읽기: %+v %v", rows, err)
	}
	v := rows[0]
	if strings.Contains(v.SupplierBiz, "9998887776") || !strings.Contains(v.SupplierBiz, "*") || v.LineCount != 1 || v.ApprovalNo == "" || v.SupplyTotal != 2000 {
		t.Fatalf("사업자번호는 가려지고 줄 수만 보인다: %+v", v)
	}
	// 권한 없는 주체(직원)는 거부된다.
	ps := users[uStaff]
	ps.Channel = contract.ChannelAPI
	if _, _, err := read.TaxInvoices(contract.WithPrincipal(context.Background(), ps), 0, 50); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("직원: %v", err)
	}
}
