package llmgw_test

import (
	"context"
	"errors"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/contract"
)

// 역할별 오늘 사용량·한도: 관리자는 모든 역할 행, 재무는 본인 역할 한 줄, 권한 없으면 거부.
func TestRoleLimitsScopesAndTotals(t *testing.T) {
	e := newEnv(t)
	e.allow(t, "", 0, "mock*", 1)
	staff, staff2 := as(contract.RoleStaff, 1, 10), as(contract.RoleStaff, 2, 20)
	fin := as(contract.RoleFinance, 3, 10)
	for _, c := range []struct {
		ctx  context.Context
		text string
	}{{staff, "안녕"}, {staff2, "반가워"}, {fin, "재무 질문"}} {
		if _, err := e.gw.Complete(c.ctx, req(c.text)); err != nil {
			t.Fatal(err)
		}
	}
	var staffUsed, finUsed int64
	if err := e.db.QueryRow(`SELECT COALESCE(SUM(input_tokens+output_tokens),0) FROM llm_usage WHERE status='ok' AND role='staff'`).Scan(&staffUsed); err != nil || staffUsed == 0 {
		t.Fatalf("전제: %d %v", staffUsed, err)
	}
	if err := e.db.QueryRow(`SELECT COALESCE(SUM(input_tokens+output_tokens),0) FROM llm_usage WHERE status='ok' AND role='finance'`).Scan(&finUsed); err != nil || finUsed == 0 {
		t.Fatalf("전제: %d %v", finUsed, err)
	}
	if _, err := e.db.Exec(`INSERT INTO llm_daily_quotas (scope_type, scope_value, daily_token_limit) VALUES ('role','staff',50000),('role','finance',200000),('role','approver',50000),('user','1',10)`); err != nil {
		t.Fatal(err)
	}

	all, err := e.st.RoleLimits(as(contract.RoleAdmin, 9, 1))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string][2]int64{}
	for _, r := range all {
		if r.DailyLimit == nil {
			t.Fatalf("한도가 있는 역할만 있어야 함: %+v", r)
		}
		got[r.Role] = [2]int64{r.UsedTokens, *r.DailyLimit}
	}
	if len(all) != 3 || got["staff"] != [2]int64{staffUsed, 50000} || got["finance"] != [2]int64{finUsed, 200000} || got["approver"] != [2]int64{0, 50000} {
		t.Fatalf("관리자는 모든 역할(사용자 범위 한도 제외): %+v", all)
	}

	own, err := e.st.RoleLimits(fin)
	if err != nil {
		t.Fatal(err)
	}
	if len(own) != 1 || own[0].Role != "finance" || own[0].UsedTokens != finUsed || own[0].DailyLimit == nil || *own[0].DailyLimit != 200000 {
		t.Fatalf("재무는 본인 역할 한 줄: %+v", own)
	}

	if _, err := e.st.RoleLimits(staff); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("직원은 거부: %v", err)
	}
}
