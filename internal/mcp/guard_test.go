package mcp_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/mcp"
)

// resultFailRecorder 는 ".result" 로 끝나는 동작의 기록만 실패시키는 감사 기록기다.
type resultFailRecorder struct{ audit.Recorder }

func (r resultFailRecorder) Record(ctx context.Context, tx contract.Tx, e audit.Entry) error {
	if strings.HasSuffix(e.Action, ".result") {
		return errors.New("disk")
	}
	return r.Recorder.Record(ctx, tx, e)
}

func draftArgs(itemID int64) map[string]any {
	return map[string]any{"vendor_id": 1, "title": "용지", "lines": []map[string]any{{"item_id": itemID, "quantity": 1, "unit_price": 1000}}}
}

func countPRs(t *testing.T, b interface {
	List(context.Context, domain.PRFilter) ([]domain.PurchaseRequest, error)
}) int {
	t.Helper()
	prs, err := b.List(ctxAs(financeP), domain.PRFilter{})
	if err != nil {
		t.Fatal(err)
	}
	return len(prs)
}

// 쓰기 도구는 호출 감사를 먼저 남기고, 그 기록이 실패하면 도구를 실행하지 않는다("감사 실패인데 쓰기 성공" 방지).
func TestWriteToolIsNotExecutedWhenCallAuditFails(t *testing.T) {
	b, d := newDeps()
	item := b.ERP.AddItem(domain.Item{Name: "복사용지"})
	cs := connect(t, d, staffP)
	b.Audit.FailWith(errors.New("disk"))
	res, _ := call(t, cs, contract.ToolCreatePRDraft, draftArgs(item.ID))
	if !res.IsError {
		t.Fatal("감사 실패면 호출은 실패여야 함")
	}
	b.Audit.FailWith(nil)
	if n := countPRs(t, b.ERP); n != 0 {
		t.Fatalf("감사가 실패했는데 쓰기가 실행됨: PR %d건", n)
	}
}

// 쓰기 뒤 결과 기록이 실패해도 이미 반영된 쓰기를 실패로 돌려주지 않는다(재시도 중복 방지).
func TestWriteToolSuccessNotTurnedIntoFailureByResultAuditFailure(t *testing.T) {
	b, d := newDeps()
	d.Audit = resultFailRecorder{b.Audit}
	item := b.ERP.AddItem(domain.Item{Name: "복사용지"})
	cs := connect(t, d, staffP)
	res, text := call(t, cs, contract.ToolCreatePRDraft, draftArgs(item.ID))
	if res.IsError {
		t.Fatalf("쓰기가 반영됐으면 성공으로 보여야 함: %s", text)
	}
	if n := countPRs(t, b.ERP); n != 1 {
		t.Fatalf("PR 1건이어야 함: %d", n)
	}
	if !b.Audit.Has("mcp.create_purchase_request_draft", "", audit.ResultSuccess) || !b.Audit.Has("purchase_request.create", "", audit.ResultSuccess) {
		t.Fatal("호출 감사(실행 전)와 서비스 감사가 남아야 함")
	}
}

func TestWriteToolRecordsRequestBeforeAndResultAfter(t *testing.T) {
	b, d := newDeps()
	item := b.ERP.AddItem(domain.Item{Name: "복사용지"})
	cs := connect(t, d, staffP)
	call(t, cs, contract.ToolCreatePRDraft, draftArgs(item.ID))
	var actions []string
	for _, r := range b.Audit.Records() {
		actions = append(actions, r.Action)
	}
	want := []string{"mcp.create_purchase_request_draft", "purchase_request.create", "mcp.create_purchase_request_draft.result"}
	if strings.Join(actions, ",") != strings.Join(want, ",") {
		t.Fatalf("감사 순서: %v (기대 %v)", actions, want)
	}
}

// stdio 처럼 오래 연결되는 서버는 도구 호출마다 토큰을 다시 검증한다.
func TestPerCallTokenRevalidationRejectsRevokedToken(t *testing.T) {
	b, d := newDeps()
	ctx := context.Background()
	srv, err := mcp.NewServerForToken(ctx, d, "t-staff")
	if err != nil {
		t.Fatal(err)
	}
	ct, st := sdk.NewInMemoryTransports()
	ss, err := srv.Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	cs, err := sdk.NewClient(&sdk.Implementation{Name: "t", Version: "0"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()

	if res, text := call(t, cs, contract.ToolSearchItems, map[string]any{"query": "x"}); res.IsError {
		t.Fatalf("유효한 토큰은 통과: %s", text)
	}
	b.Auth.Revoke("t-staff")
	res, text := call(t, cs, contract.ToolSearchItems, map[string]any{"query": "x"})
	if !res.IsError || !strings.Contains(text, "유효하지 않") {
		t.Fatalf("폐기된 토큰의 호출은 거부여야 함: isError=%v %q", res.IsError, text)
	}
	denied := false
	for _, r := range b.Audit.Records() {
		if r.Action == "mcp.search_items" && r.Result == audit.ResultDenied && r.ActorID == 0 && r.Channel == contract.ChannelMCP {
			denied = true
		}
	}
	if !denied {
		t.Fatal("토큰 무효 거부가 감사 로그(channel=mcp, denied)에 남아야 함")
	}
}

func TestPerCallRevalidationPicksUpRoleChange(t *testing.T) {
	b, d := newDeps()
	ctx := context.Background()
	srv, err := mcp.NewServerForToken(ctx, d, "t-mgr")
	if err != nil {
		t.Fatal(err)
	}
	ct, st := sdk.NewInMemoryTransports()
	ss, _ := srv.Connect(ctx, st, nil)
	defer ss.Close()
	cs, _ := sdk.NewClient(&sdk.Implementation{Name: "t", Version: "0"}, nil).Connect(ctx, ct, nil)
	defer cs.Close()
	if res, text := call(t, cs, contract.ToolListPendingApprovals, map[string]any{}); res.IsError {
		t.Fatalf("manager 는 결재함 조회 가능: %s", text)
	}
	// 같은 토큰의 소유자가 staff 로 바뀌었다 → 결재 권한이 없어진다.
	b.Auth.Add("t-mgr", contract.Principal{UserID: managerP.UserID, Name: "x", Role: contract.RoleAuditor, DepartmentID: 10})
	res, text := call(t, cs, contract.ToolCreatePRDraft, draftArgs(1))
	if !res.IsError || !strings.Contains(text, "권한") {
		t.Fatalf("역할 변경이 다음 호출에 반영되어야 함: %v %q", res.IsError, text)
	}
}
