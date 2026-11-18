package repository

import "testing"

// 이 값을 적기 전에 상신된 옛 스냅샷(financeAdded 키 없음)은 "알 수 없음"(nil)으로 읽는다.
func TestParseBudgetSnapshotFinanceAdded(t *testing.T) {
	old := ParseBudgetSnapshot("over", `{"month":"2026-11","budget":100,"over":5}`)
	if old == nil || old.FinanceAdded != nil || old.Over != 5 {
		t.Fatalf("키 없는 스냅샷: %+v", old)
	}
	yes := ParseBudgetSnapshot("over", `{"month":"2026-11","financeAdded":true}`)
	if yes.FinanceAdded == nil || !*yes.FinanceAdded {
		t.Fatalf("true: %+v", yes)
	}
	no := ParseBudgetSnapshot("over", `{"month":"2026-11","financeAdded":false}`)
	if no.FinanceAdded == nil || *no.FinanceAdded {
		t.Fatalf("false: %+v", no)
	}
	null := ParseBudgetSnapshot("over", `{"month":"2026-11","financeAdded":null}`)
	if null.FinanceAdded != nil {
		t.Fatalf("null: %+v", null)
	}
}
