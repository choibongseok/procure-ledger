package service

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/repository"
)

// ⑤ 부서 예산(B35). 키는 (부서 이름, 적용 월). 규칙은 예산 화면(BudgetService.Set)과 같다: 이번 달 이후만, 이미 쓴 금액(사용액+진행 중)보다 적게 줄일 수 없고,
// 변경은 budget_revisions 에 이력이 남는다. 사유가 비면 "기초 데이터 가져오기"다.

const importBudgetReason = "기초 데이터 가져오기"

// ImportBudgetHandler 는 부서 예산 어댑터다.
type ImportBudgetHandler struct {
	Base
	Repo   repository.SQLBudget
	Import repository.SQLImport
}

var _ domain.ImportTargetHandler = (*ImportBudgetHandler)(nil)

func NewImportBudget(b Base) *ImportBudgetHandler { return &ImportBudgetHandler{Base: b} }

func (h *ImportBudgetHandler) Target() string { return domain.ImportBudget }

func (h *ImportBudgetHandler) Plan(ctx context.Context, q contract.Tx, rows []domain.ImportRow, in domain.ImportPlanInput) (domain.ImportPlan, error) {
	var plan domain.ImportPlan
	thisMonth := budgetMonthOf(h.now())
	for _, r := range rows {
		dept, month := r.Cells["dept"], r.Cells["month"]
		amount, _ := strconv.ParseInt(r.Cells["amount"], 10, 64)
		key := dept + " " + month
		deptID, found, err := h.Import.DepartmentByName(ctx, q, dept)
		if err != nil {
			return plan, err
		}
		switch {
		case !found:
			plan.Verdicts = append(plan.Verdicts, verdictOf(r, key, domain.ImportVerdictError, "등록되지 않은 부서입니다. 부서 이름이 정확히 같아야 합니다.", nil))
			continue
		case month < thisMonth:
			plan.Verdicts = append(plan.Verdicts, verdictOf(r, key, domain.ImportVerdictError, "지난 달 예산은 가져올 수 없습니다(이번 달 이후만).", nil))
			continue
		case amount > maxBudgetAmount:
			plan.Verdicts = append(plan.Verdicts, verdictOf(r, key, domain.ImportVerdictError, "예산 금액이 너무 큽니다.", nil))
			continue
		}
		_, cur, registered, err := h.Repo.Amount(ctx, q, deptID, month)
		if err != nil {
			return plan, err
		}
		if !registered {
			plan.Verdicts = append(plan.Verdicts, verdictOf(r, key, domain.ImportVerdictAdd, "", nil))
			continue
		}
		if cur == amount {
			plan.Verdicts = append(plan.Verdicts, verdictOf(r, key, domain.ImportVerdictSame, "", nil))
			continue
		}
		if amount < cur {
			used, inProgress, err := h.Repo.Sums(ctx, q, deptID, month, 0)
			if err != nil {
				return plan, err
			}
			if spent := used + inProgress; amount < spent {
				plan.Verdicts = append(plan.Verdicts, verdictOf(r, key, domain.ImportVerdictError, "사용액과 결재 진행 중 금액의 합("+wonText(spent)+")보다 적게 줄일 수 없습니다.", nil))
				continue
			}
		}
		plan.Verdicts = append(plan.Verdicts, verdictOf(r, key, domain.ImportVerdictChange, "", []domain.ImportChange{chg("월 예산", wonText(cur), wonText(amount))}))
	}
	return plan, nil
}

func (h *ImportBudgetHandler) Apply(ctx context.Context, tx contract.Tx, p contract.Principal, rows []domain.ImportRow, plan []domain.ImportRowVerdict, in domain.ImportPlanInput) (domain.ImportApplyOutcome, error) {
	for i, r := range rows {
		v := plan[i]
		if v.Verdict != domain.ImportVerdictAdd && v.Verdict != domain.ImportVerdictChange {
			continue
		}
		dept, month := r.Cells["dept"], r.Cells["month"]
		amount, _ := strconv.ParseInt(r.Cells["amount"], 10, 64)
		reason := strings.TrimSpace(r.Cells["reason"])
		if reason == "" {
			reason = importBudgetReason
		}
		deptID, found, err := h.Import.DepartmentByName(ctx, tx, dept)
		if err != nil {
			return domain.ImportApplyOutcome{}, err
		}
		if !found {
			return domain.ImportApplyOutcome{}, fmt.Errorf("%w: 미리보기 이후 부서가 바뀌었습니다. 다시 미리보기를 하세요.", contract.ErrInvalidState)
		}
		// 줄이는 변경은 반영 시점에도 다시 확인한다(동시 상신과 경합해도 순서대로 판정).
		if _, cur, registered, err := h.Repo.Amount(ctx, tx, deptID, month); err != nil {
			return domain.ImportApplyOutcome{}, err
		} else if registered && amount < cur {
			used, inProgress, err := h.Repo.Sums(ctx, tx, deptID, month, 0)
			if err != nil {
				return domain.ImportApplyOutcome{}, err
			}
			if spent := used + inProgress; amount < spent {
				return domain.ImportApplyOutcome{}, validation("%s %s 예산은 사용액과 결재 진행 중 금액의 합(%s)보다 적게 줄일 수 없습니다.", dept, monthKorean(month), wonText(spent))
			}
		}
		id, old, err := h.Repo.Upsert(ctx, tx, deptID, month, amount, reason, p.UserID, h.now())
		if err != nil {
			return domain.ImportApplyOutcome{}, err
		}
		oldText := ""
		if old != nil {
			oldText = fmt.Sprint(*old)
		}
		if err := h.Audit.Record(ctx, tx, audit.Entry{Actor: p, Action: "budget.set", TargetType: audit.TargetBudget, TargetID: fmt.Sprint(id), Result: audit.ResultSuccess,
			Detail: "부서 예산 설정" + importSource(in), Diff: []audit.DiffItem{{Key: "department", New: dept}, {Key: "month", New: month},
				{Key: "amount", Old: oldText, New: fmt.Sprint(amount)}, {Key: "reason", New: reason}}}); err != nil {
			return domain.ImportApplyOutcome{}, err
		}
	}
	return domain.ImportApplyOutcome{}, nil
}
