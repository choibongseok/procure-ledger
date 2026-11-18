package service_test

import (
	"testing"

	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/service"
)

func TestImportBudgetsRulesAndRevisions(t *testing.T) {
	e := newEnv(t) // 2026-11-10
	h := service.NewImportBudget(e.importBase())
	in := domain.ImportPlanInput{Target: domain.ImportBudget, BatchNo: "IMP-2026-0005"}
	rows := []domain.ImportRow{
		irow(2, map[string]string{"dept": "구매팀", "month": "2026-12", "amount": "30000000"}),
		irow(3, map[string]string{"dept": "영업팀", "month": "2026-11", "amount": "5000000", "reason": "연초 확정"}),
		irow(4, map[string]string{"dept": "없는팀", "month": "2026-12", "amount": "1"}),
		irow(5, map[string]string{"dept": "구매팀", "month": "2026-10", "amount": "1"}),
	}
	plan, _, _ := e.importRun(t, h, rows, in)
	wantVerdicts(t, plan, "add", "add", "error", "error")
	ok := rows[:2]
	if _, _, err := e.importRun(t, h, ok, in); err != nil {
		t.Fatal(err)
	}
	if e.col(t, `SELECT amount FROM budgets WHERE month = '2026-12'`) != "30000000" {
		t.Fatal("예산이 저장되지 않음")
	}
	if e.col(t, `SELECT reason FROM budget_revisions ORDER BY id LIMIT 1`) != "기초 데이터 가져오기" {
		t.Error("사유가 비면 기초 데이터 가져오기여야 함")
	}
	again, _, _ := e.importRun(t, h, ok, in)
	wantVerdicts(t, again, "same", "same")
	up := []domain.ImportRow{irow(2, map[string]string{"dept": "구매팀", "month": "2026-12", "amount": "40000000"})}
	plan, _, _ = e.importRun(t, h, up, in)
	wantVerdicts(t, plan, "change")
	if plan.Verdicts[0].Changes[0].Column != "월 예산" || e.col(t, `SELECT COUNT(*) FROM budget_revisions`) != "3" {
		t.Fatalf("변경과 이력: %+v", plan.Verdicts[0])
	}
	if !e.rec.Has("budget.set", audit.TargetBudget, audit.ResultSuccess) {
		t.Error("예산 감사가 없음")
	}
}
