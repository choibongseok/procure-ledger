package service_test

import (
	"context"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
)

// lowItem 은 안전재고 10, 입고 3(부족분 7), 최근 발주 단가 1000 인 품목이다.
func lowItem(t *testing.T, e *env, sku string) domain.Item {
	t.Helper()
	it := e.item(t, sku, 10, false)
	po := orderedPO(t, e, it, 3)
	if _, err := e.inventory.Receive(as(uFinance), recv(po.ID, po.Lines[0].ID, 3, "", "")); err != nil {
		t.Fatal(err)
	}
	return it
}

// 미달 품목의 재주문 초안은 부족분 수량·최근 발주 단가로 임시저장까지만 만들어진다(상신 없음).
func TestDraftReorderCreatesDraftOnly(t *testing.T) {
	e := newEnv(t)
	it := lowItem(t, e, "R1")
	v := e.vendor(t)
	pr, err := e.purchase.DraftReorder(as(uStaff), domain.ReorderInput{VendorID: v.ID, ItemIDs: []int64{it.ID, it.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if pr.Status != domain.PRDraft || len(pr.Steps) != 0 || pr.RequesterID != uStaff || pr.VendorID != v.ID {
		t.Fatalf("초안: %+v", pr)
	}
	if len(pr.Lines) != 1 || pr.Lines[0].Quantity != 7 || pr.Lines[0].UnitPrice != 1000 || pr.TotalAmount != 7000 {
		t.Fatalf("줄: %+v", pr.Lines)
	}
	if pr.Reason != "안전재고 미달 재주문" {
		t.Fatalf("사유: %q", pr.Reason)
	}
	rec, ok := lastRecord(e, "purchase_request.create")
	if !ok || !hasDiff(rec, "source", "reorder") || rec.ActorID != uStaff {
		t.Fatalf("감사: %+v", rec)
	}
	// MCP 채널도 같은 서비스다.
	it2 := lowItem(t, e, "R2")
	mcp := users[uStaff]
	mcp.Channel = contract.ChannelMCP
	if _, err := e.purchase.DraftReorder(contract.WithPrincipal(context.Background(), mcp), domain.ReorderInput{ItemIDs: []int64{it2.ID}}); err != nil {
		t.Fatalf("MCP: %v", err)
	}
}

// 미달이 아닌 품목은 거부되고 아무것도 만들어지지 않는다.
func TestDraftReorderRejectsItemNotBelowSafety(t *testing.T) {
	e := newEnv(t)
	low := lowItem(t, e, "R3")
	fine := e.item(t, "OK", 0, false)
	before := count(t, e.db, `SELECT COUNT(*) FROM purchase_requests`)
	_, err := e.purchase.DraftReorder(as(uStaff), domain.ReorderInput{ItemIDs: []int64{low.ID, fine.ID}})
	is(t, err, contract.ErrValidation)
	_, err = e.purchase.DraftReorder(as(uStaff), domain.ReorderInput{})
	is(t, err, contract.ErrValidation)
	if count(t, e.db, `SELECT COUNT(*) FROM purchase_requests`) != before {
		t.Fatal("거부됐는데 초안이 생김")
	}
}

// 같은 부서에 그 품목의 임시저장 초안이 이미 있으면 새로 만들지 않는다. 초안이 상신되면 다시 만들 수 있다.
func TestDraftReorderRejectsDuplicateDraft(t *testing.T) {
	e := newEnv(t)
	it := lowItem(t, e, "R4")
	first, err := e.purchase.DraftReorder(as(uStaff), domain.ReorderInput{ItemIDs: []int64{it.ID}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.purchase.DraftReorder(as(uLead), domain.ReorderInput{ItemIDs: []int64{it.ID}}) // 같은 부서 다른 사람
	is(t, err, contract.ErrInvalidState)
	// 다른 부서는 그 초안을 볼 수 없으므로 막히지 않는다.
	if _, err := e.purchase.DraftReorder(as(uStaff2), domain.ReorderInput{ItemIDs: []int64{it.ID}}); err != nil {
		t.Fatalf("타 부서: %v", err)
	}
	v := e.vendor(t)
	if _, err := e.purchase.UpdateDraft(as(uStaff), first.ID, domain.DraftInput{VendorID: v.ID, Title: "재주문", Lines: []domain.PRLineInput{{ItemID: it.ID, Quantity: 7, UnitPrice: 1000}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.purchase.Submit(as(uStaff), first.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.purchase.DraftReorder(as(uLead), domain.ReorderInput{ItemIDs: []int64{it.ID}}); err != nil {
		t.Fatalf("상신 뒤: %v", err)
	}
}

// purchase:create 가 없는 역할은 거부되고 거부가 감사에 남는다.
func TestDraftReorderRequiresPermission(t *testing.T) {
	e := newEnv(t)
	it := lowItem(t, e, "R5")
	_, err := e.purchase.DraftReorder(as(uAuditor), domain.ReorderInput{ItemIDs: []int64{it.ID}})
	is(t, err, contract.ErrForbidden)
	if !e.rec.Has("purchase_request.create", audit.TargetPurchaseRequest, audit.ResultDenied) {
		t.Fatal("거부 감사 기록 없음")
	}
	if _, err = e.purchase.DraftReorder(contractNoUser(), domain.ReorderInput{ItemIDs: []int64{it.ID}}); err == nil {
		t.Fatal("비로그인 허용됨")
	}
}
