package service_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/privacy"
)

// B43 2단계: 상대방 이메일 원문은 원문 허용 권한과 열람 기록이 모두 있을 때만 나가고, 기록하지 못하면 싣지 않는다. 사업자번호 원문 응답은 기록된다(1단계 수정).
func TestTaxContactRevealNotesPrivacyAndFailsClosed(t *testing.T) {
	e := newEnv(t)
	e.taxCompany(t)
	n := &fakeNoter{}
	e.tax.Privacy = n
	v, _ := e.taxVendor(t, 10_000)
	approvalSeq++
	inv, err := e.tax.RegisterReceived(as(uFinance), domain.TaxReceivedInput{ApprovalNo: "20261110REVEAL0000000001", IssueDate: "2026-11-10",
		Supplier: domain.TaxParty{BizNo: digitsForTest(v.BusinessNo), Name: v.Name, Rep: "x", Email: "tax@supplier.example"}, SupplyTotal: 10_000, TaxTotal: 1_000})
	if err != nil {
		t.Fatal(err)
	}
	// 응답은 사업자번호 원문을 싣고 그 열람이 기록된다(account 범주·business_no·세금계산서 상세).
	if len(n.got) == 0 || n.got[len(n.got)-1].Category != privacy.CatAccount || n.got[len(n.got)-1].Screen != privacy.ScreenTaxInvoiceDetail {
		t.Fatalf("사업자번호 열람 기록: %+v", n.got)
	}
	// 응답의 이메일은 항상 가려서 나간다.
	if got, _ := e.tax.Get(as(uFinance), inv.ID); strings.Contains(got.Supplier.Email, "tax@supplier") {
		t.Fatalf("이메일이 가려지지 않았다: %q", got.Supplier.Email)
	}
	// 원문 허용 권한이 없는 감사 담당자는 거부된다.
	if _, err := e.tax.RevealContact(as(uAuditor), inv.ID); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("권한 없음: %v", err)
	}
	// 원문 허용 권한이 있는 주체: 기록 뒤에 원문, 기록 실패면 원문 없이 실패.
	p := users[uFinance]
	p.Channel = contract.ChannelWeb
	p.Perms = contract.PermSet{contract.PermTaxInvoiceRead: {}, contract.PermMaskBypass: {}}
	ctx := contract.WithPrincipal(context.Background(), p)
	before := len(n.got)
	c, err := e.tax.RevealContact(ctx, inv.ID)
	if err != nil || c.Email != "tax@supplier.example" || len(n.got) != before+1 {
		t.Fatalf("원문 열람: %+v %v", c, err)
	}
	if a := n.got[len(n.got)-1]; a.Category != privacy.CatContact || a.Subjects[0].Type != privacy.SubjectVendor || a.Subjects[0].ID != v.ID || a.Fields[0] != privacy.FieldEmail {
		t.Fatalf("기록 내용: %+v", a)
	}
	n.fail = errors.New("디스크 가득")
	if c, err := e.tax.RevealContact(ctx, inv.ID); err == nil || c.Email != "" || strings.Contains(err.Error(), "디스크") {
		t.Fatalf("기록 실패 시 싣지 않는다: %+v %v", c, err)
	}
	n.fail = nil
	// 거래처로 등록되지 않은 상대방도 열람 기록이 남는다: 정보주체는 counterparty, subject_id 는 세금계산서 번호.
	approvalSeq++
	unk, err := e.tax.RegisterReceived(as(uFinance), domain.TaxReceivedInput{ApprovalNo: "20261110REVEAL0000000002", IssueDate: "2026-11-10",
		Supplier: domain.TaxParty{BizNo: "9998887776", Name: "미등록", Rep: "x", Email: "who@unknown.example"}, SupplyTotal: 1_000, TaxTotal: 100})
	if err != nil {
		t.Fatal(err)
	}
	before = len(n.got)
	c, err = e.tax.RevealContact(ctx, unk.ID)
	if err != nil || c.Email != "who@unknown.example" || len(n.got) != before+1 {
		t.Fatalf("미등록 상대방: %+v %v", c, err)
	}
	if sub := n.got[len(n.got)-1].Subjects[0]; sub.Type != privacy.SubjectCounterparty || sub.ID != unk.ID {
		t.Fatalf("정보주체: %+v", sub)
	}
	// 등록 직후 응답의 사업자번호 원문(미등록 공급자)도 counterparty 로 기록되었다.
	found := false
	for _, a := range n.got {
		for _, sub := range a.Subjects {
			if sub.Type == privacy.SubjectCounterparty && sub.ID == unk.ID && a.Category == privacy.CatAccount {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("사업자번호 원문 기록(counterparty)이 없다: %+v", n.got)
	}
}
