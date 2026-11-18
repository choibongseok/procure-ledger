package service_test

import (
	"context"
	"strings"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/deps"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/contract/fake"
	"github.com/choibongseok/procure-ledger/internal/mcp"
	"github.com/choibongseok/procure-ledger/internal/sod"
)

// B41 3단계(ai): MCP 의 쓰기 도구(approve_or_reject)는 웹과 같은 결재 서비스를 부르므로 결재 후속 훅의 SoD 판정을 우회하지 못한다.
// 같은 서비스 묶음(판정기 포함)에 MCP 서버를 올려, 판정이 규칙 모드대로(차단 → 롤백+롤백 밖 기록 1건, 경고 → 같은 트랜잭션) 일어나는지 본다.

func (e *env) mcpDeps() deps.Deps {
	return deps.Deps{
		DB: e.db, Txs: contract.SQLTxManager{DB: e.db}, Audit: e.rec, Masker: fake.Masker{},
		Purchase: e.purchase, Approval: e.approval, Inventory: e.inventory, Org: leftAwareOrg{e.org},
	}
}

func (e *env) mcpApprove(t *testing.T, who int64, prID int64) (*sdk.CallToolResult, string) {
	t.Helper()
	p := users[who]
	p.Channel = contract.ChannelMCP
	srv, _ := mcp.NewServer(e.mcpDeps(), p)
	ct, st := sdk.NewInMemoryTransports()
	ctx := context.Background()
	ss, err := srv.Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ss.Close() })
	cs, err := sdk.NewClient(&sdk.Implementation{Name: "sod", Version: "0"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	res, err := cs.CallTool(ctx, &sdk.CallToolParams{Name: contract.ToolApproveOrReject, Arguments: map[string]any{"pr_id": prID, "approve": true, "comment": "확인"}})
	if err != nil {
		t.Fatalf("프로토콜 오류: %v", err)
	}
	var sb strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*sdk.TextContent); ok {
			sb.WriteString(tc.Text)
		}
	}
	return res, sb.String()
}

// MCP 는 구매 품의서만 결재할 수 있다(approval_service: 양식 문서는 MCP 채널에서 대상이 없다). 그래서 SoD 판정이 걸린 지급 요청서·전표 승인 요청서는
// MCP 로 최종 승인할 수 없다: 거부되고 아무것도 바뀌지 않으며 판정·차단 기록도 생기지 않는다(우회로가 아니다).
func TestMCPCannotApproveSoDGuardedDocuments(t *testing.T) {
	f := &fakeSoD{force: true, mode: map[string]string{sod.RulePayCreateApprove: sod.ModeBlock}}
	e := newEnvSoD(t, f)
	e.payForm(t)
	v, bank := e.vendor(t), e.bank(t, "주거래")
	pid := e.payable(t, v.ID, 30_000_000)
	p := e.requested(t, e.payInput(v, bank.ID, domain.PaymentItemInput{PayableID: pid, Amount: 30_000_000}))
	decideAs(t, e, uFinance2, p.DocID, true, "확인")
	before := len(f.checks)
	res, text := e.mcpApprove(t, uHead, p.DocID)
	if !res.IsError {
		t.Fatalf("MCP 로 지급 요청서가 승인되면 안 된다: %s", text)
	}
	got, err := e.payments.Get(as(uFinance), p.No)
	if err != nil || got.Status != domain.PaymentRequested {
		t.Fatalf("지급 건이 바뀜: %+v %v", got, err)
	}
	if len(f.checks) != before || len(f.blocked) != 0 {
		t.Fatalf("MCP 시도가 판정 지점까지 가지 않아야 함: 판정 %d→%d 차단 %d", before, len(f.checks), len(f.blocked))
	}
	// 전표 승인 요청서도 같다(준비 단계의 전표 승인이 막히지 않게 이제 규칙을 켠다).
	f.mode[sod.RuleJVCreateApprove] = sod.ModeBlock
	j, err := e.journals.RequestApproval(as(uFinance), e.purchaseJV(t, v.ID, 50_000).No)
	if err != nil {
		t.Fatal(err)
	}
	if res, text := e.mcpApprove(t, uFinance2, j.DocID); !res.IsError {
		t.Fatalf("MCP 로 전표가 승인되면 안 된다: %s", text)
	}
	if got, err := e.journals.Get(as(uFinance), j.No); err != nil || got.Status != domain.JournalRequested {
		t.Fatalf("전표가 바뀜: %+v %v", got, err)
	}
}

// 구매 품의서 승인은 MCP 로 된다. 모든 txn 규칙이 차단이어도(서로 다른 사람) 막히지 않고 발주가 만들어지며 차단 기록은 없다.
func TestMCPPurchaseApprovalPassesWithAllRulesBlocking(t *testing.T) {
	block := map[string]string{}
	for _, r := range sod.Rules {
		if r.Kind == sod.KindTxn {
			block[r.Code] = sod.ModeBlock
		}
	}
	f := &fakeSoD{mode: block}
	e := newEnvSoD(t, f)
	v := e.poVendor(t, "엠씨피상사", "888-88-88888")
	it := e.item(t, "MCP-1", 0, false)
	pr, err := e.purchase.CreateDraft(as(uStaff), domain.DraftInput{VendorID: v.ID, Title: "MCP 승인 시험", Reason: "x",
		Lines: []domain.PRLineInput{{ItemID: it.ID, Quantity: 3, UnitPrice: 1_000}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.purchase.Submit(as(uStaff), pr.ID); err != nil {
		t.Fatal(err)
	}
	res, text := e.mcpApprove(t, uLead, pr.ID)
	if res.IsError || !strings.Contains(text, "purchase_order_no") {
		t.Fatalf("MCP 구매 승인은 발주를 만들어야 함: %s", text)
	}
	if len(f.blocked) != 0 {
		t.Fatalf("차단 기록이 있다: %+v", f.blocked)
	}
}
