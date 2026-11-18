package llmgw_test

import (
	"testing"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/llm"
	"github.com/choibongseok/procure-ledger/internal/llmgw"
)

// B46: 평가 실행은 규칙과 무관하게 고른 공급자로 간다. 마스킹·사용량 기록은 그대로이고 용도 라벨은 eval.run 이다.
func TestForcedProviderBypassesRulesAndKeepsAccounting(t *testing.T) {
	e := newEnv(t)
	e.allow(t, "", 0, "*", 1)
	ad := e.admin(t, false)
	ctx := adminCtx(contract.PermLLMProviderManage, contract.PermLLMPolicyManage)
	a, err := ad.SaveProvider(ctx, llmgw.ProviderInput{Code: "mock-a", Kind: "mock", Name: "가", DefaultModel: "a-1"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := ad.SaveProvider(ctx, llmgw.ProviderInput{Code: "mock-b", Kind: "mock", Name: "나", DefaultModel: "b-1"})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{a.ID, b.ID} {
		if _, err := ad.SetActive(ctx, id, true); err != nil {
			t.Fatal(err)
		}
	}
	// eval.* 규칙은 a 로 보내지만, 고정하면 b 로 간다.
	if _, err := ad.SaveRoute(ctx, llmgw.RouteInput{PurposePattern: "eval.*", PrimaryProviderID: a.ID, Active: true}); err != nil {
		t.Fatal(err)
	}
	gw := e.gwWith(t, false)
	req := llm.Request{Purpose: "eval.run", Messages: []llm.Message{{Role: "user", Content: "문의는 010-1234-5678 로"}}}
	if _, err := gw.Complete(as(contract.RoleStaff, 1, 10), req); err != nil {
		t.Fatal(err)
	}
	if p, _ := e.usageProvider(t); p != "mock-a" {
		t.Fatalf("규칙 경로: %s", p)
	}
	resp, err := gw.Complete(llmgw.WithForcedProvider(as(contract.RoleStaff, 1, 10), "mock-b"), req)
	if err != nil {
		t.Fatal(err)
	}
	var model, purpose string
	var masked int
	if err := e.db.QueryRow(`SELECT provider, model, purpose, masked_count FROM llm_usage ORDER BY id DESC LIMIT 1`).Scan(&model, &resp.Model, &purpose, &masked); err != nil {
		t.Fatal(err)
	}
	if model != "mock-b" || resp.Model != "b-1" || purpose != "eval.run" || masked == 0 {
		t.Fatalf("고정 경로: provider=%s model=%s purpose=%s masked=%d", model, resp.Model, purpose, masked)
	}
	if txt := resp.Text; len(txt) == 0 || containsPhone(txt) {
		t.Fatalf("마스킹 전 글이 공급자에 갔다: %q", resp.Text)
	}
	// 없는 공급자·꺼진 공급자는 호출이 실패하고 다른 곳으로 새지 않는다.
	if _, err := gw.Complete(llmgw.WithForcedProvider(as(contract.RoleStaff, 1, 10), "nope"), req); err == nil {
		t.Fatal("없는 공급자로 호출이 갔다")
	}
	if _, err := ad.SetActive(ctx, b.ID, false); err != nil {
		t.Fatal(err)
	}
	if _, err := gw.Complete(llmgw.WithForcedProvider(as(contract.RoleStaff, 1, 10), "mock-b"), req); err == nil {
		t.Fatal("꺼진 공급자로 호출이 갔다")
	}
}

func containsPhone(s string) bool {
	for i := 0; i+12 <= len(s); i++ {
		if s[i:i+12] == "010-1234-567" {
			return true
		}
	}
	return false
}
