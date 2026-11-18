package mcp_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
)

// 재고 경보 조회는 안전재고 미달 품목과 부족분을 돌려주고, 결과의 마스킹 대상은 가려진다.
func TestListStockAlertsReturnsBelowSafetyAndMasks(t *testing.T) {
	b, d := newDeps()
	low := b.ERP.AddItem(domain.Item{SKU: "S-1", Name: "특수볼트 (거래처 123-45-67890 전용)", Unit: "EA", SafetyStock: 10})
	b.ERP.SeedStock(low.ID, "L1", "", 3)
	ok := b.ERP.AddItem(domain.Item{SKU: "S-2", Name: "와셔", Unit: "EA", SafetyStock: 2})
	b.ERP.SeedStock(ok.ID, "L1", "", 50)
	cs := connect(t, d, staffP)
	res, text := call(t, cs, contract.ToolListStockAlerts, map[string]any{})
	if res.IsError {
		t.Fatalf("list_stock_alerts: %s", text)
	}
	if !strings.Contains(text, `"shortfall":7`) || strings.Contains(text, "와셔") {
		t.Fatalf("미달 품목과 부족분: %s", text)
	}
	if strings.Contains(text, "123-45-67890") {
		t.Fatalf("마스킹 대상이 그대로 나감: %s", text)
	}
}

// 재주문 초안은 임시저장까지만 한다. 상신·결재 상태로 가지 않고, 서비스 감사와 호출 감사(실행 전)가 남는다.
func TestDraftReorderCreatesDraftOnlyAndIsAudited(t *testing.T) {
	b, d := newDeps()
	low := b.ERP.AddItem(domain.Item{Name: "복사용지", SafetyStock: 10})
	b.ERP.SeedStock(low.ID, "L1", "", 4)
	cs := connect(t, d, staffP)
	res, text := call(t, cs, contract.ToolDraftReorder, map[string]any{"vendor_id": 1, "item_ids": []int64{low.ID}})
	if res.IsError || !strings.Contains(text, `"status":"draft"`) || !strings.Contains(text, `"quantity":6`) {
		t.Fatalf("부족분 수량의 임시저장이어야 함: %v %s", res.IsError, text)
	}
	if !b.Audit.Has("mcp.draft_reorder", "", audit.ResultSuccess) || !b.Audit.Has("purchase_request.create", "", audit.ResultSuccess) {
		t.Fatal("호출 감사(실행 전)와 서비스 감사가 남아야 함")
	}
	prs, _ := b.ERP.List(ctxAs(financeP), domain.PRFilter{})
	for _, p := range prs {
		if p.Status != domain.PRDraft {
			t.Fatalf("상신까지 가면 안 됨: %s", p.Status)
		}
	}
}

// 미달이 아닌 품목은 재주문 초안을 만들지 않는다.
func TestDraftReorderRejectsItemNotBelowSafety(t *testing.T) {
	b, d := newDeps()
	it := b.ERP.AddItem(domain.Item{Name: "와셔", SafetyStock: 2})
	b.ERP.SeedStock(it.ID, "L1", "", 50)
	cs := connect(t, d, staffP)
	res, _ := call(t, cs, contract.ToolDraftReorder, map[string]any{"item_ids": []int64{it.ID}})
	if !res.IsError {
		t.Fatal("미달이 아닌 품목은 거부")
	}
	if n := countPRs(t, b.ERP); n != 0 {
		t.Fatalf("초안이 생김: %d", n)
	}
}

// 권한 없는 역할(감사)은 재주문을 거부당하고 거부가 감사 로그에 남으며 PR 은 생기지 않는다.
func TestDraftReorderDeniedForAuditor(t *testing.T) {
	b, d := newDeps()
	low := b.ERP.AddItem(domain.Item{Name: "복사용지", SafetyStock: 10})
	b.ERP.SeedStock(low.ID, "L1", "", 4)
	cs := connect(t, d, auditorP)
	res, _ := call(t, cs, contract.ToolDraftReorder, map[string]any{"item_ids": []int64{low.ID}})
	if !res.IsError || !b.Audit.Has("mcp.draft_reorder", "", audit.ResultDenied) {
		t.Fatal("거부와 거부 감사가 필요")
	}
	if n := countPRs(t, b.ERP); n != 0 {
		t.Fatalf("PR 이 생김: %d", n)
	}
}

// 호출 감사가 실패하면 재주문 초안은 실행되지 않는다(쓰기 도구 가드).
func TestDraftReorderNotExecutedWhenCallAuditFails(t *testing.T) {
	b, d := newDeps()
	low := b.ERP.AddItem(domain.Item{Name: "복사용지", SafetyStock: 10})
	b.ERP.SeedStock(low.ID, "L1", "", 4)
	cs := connect(t, d, staffP)
	b.Audit.FailWith(errors.New("disk"))
	res, _ := call(t, cs, contract.ToolDraftReorder, map[string]any{"item_ids": []int64{low.ID}})
	b.Audit.FailWith(nil)
	if !res.IsError || countPRs(t, b.ERP) != 0 {
		t.Fatal("감사 실패면 실행하지 않는다")
	}
}
