package service

import "testing"

// 예산 저장소의 집계 SQL 한 곳만 예외다. 같은 파일에 부서 열람 조건이 들어오면 잡힌다.
func TestBudgetRepoExemptionIsOnlyTheAggregate(t *testing.T) {
	agg := "package r\nvar q = `SELECT COALESCE(SUM(CASE WHEN status = 'ordered' THEN total_amount ELSE 0 END),0), COALESCE(SUM(CASE WHEN status IN ('submitted') THEN total_amount ELSE 0 END),0) FROM purchase_requests WHERE department_id = ? AND budget_month = ?`\n"
	if bad := checkViewConditionOnlyInViewScope(parseAll(map[string]string{"internal/repository/budget_repo.go": agg})); len(bad) != 0 {
		t.Fatalf("집계 SQL 이 위반으로 잡힘: %v", bad)
	}
	sneaky := "package r\nvar q = `SELECT id FROM purchase_requests WHERE department_id = ?`\n"
	if bad := checkViewConditionOnlyInViewScope(parseAll(map[string]string{"internal/repository/budget_repo.go": sneaky})); len(bad) != 1 {
		t.Fatalf("budget_repo.go 의 부서 열람 조건이 통과함: %v", bad)
	}
	// 집계처럼 보이게 꾸민 열람 조건(owner_unit_id 추가)도 잡힌다.
	mixed := "package r\nvar q = `SELECT SUM(CASE WHEN status = 'a' THEN 1 END), SUM(CASE WHEN status = 'b' THEN 1 END) FROM purchase_requests WHERE owner_unit_id = ? AND budget_month = ?`\n"
	if bad := checkViewConditionOnlyInViewScope(parseAll(map[string]string{"internal/repository/budget_repo.go": mixed})); len(bad) != 1 {
		t.Fatalf("꾸민 열람 조건이 통과함: %v", bad)
	}
	// 다른 파일은 집계 모양이어도 예외가 아니다.
	if bad := checkViewConditionOnlyInViewScope(parseAll(map[string]string{"internal/repository/other.go": agg})); len(bad) != 1 {
		t.Fatalf("다른 파일이 예외로 통과함: %v", bad)
	}
}
