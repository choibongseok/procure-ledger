package service_test

import (
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
)

// AC-500: 이미 쓴 금액(사용액 + 결재 진행 중)보다 적게 줄일 수 없다. 같거나 많은 값으로 줄이기와 올리기는 된다.
func TestBudgetCannotBeReducedBelowSpent(t *testing.T) {
	e := newEnv(t)
	e.setBudget(t, deptBuy, 10_000_000)
	e.submitted(t, uStaff, 8_000_000)
	set := func(amount int64) error {
		_, err := e.budget.Set(as(uFinance), domain.BudgetInput{DepartmentID: deptBuy, Month: nov, Amount: amount, Reason: "조정"})
		return err
	}
	for _, ok := range []int64{9_000_000, 8_000_000, 20_000_000} {
		if err := set(ok); err != nil {
			t.Fatalf("%d 로 저장: %v", ok, err)
		}
	}
	e.setBudget(t, deptBuy, 10_000_000) // 올리기
	for _, bad := range []int64{7_999_999, 0} {
		err := set(bad)
		if !isErr(err, contract.ErrValidation) || !strings.Contains(err.Error(), "구매팀 2026년 11월 예산은 사용액과 결재 진행 중 금액의 합(8,000,000원)보다 적게 줄일 수 없습니다. 결재 진행 중인 요청을 정리한 뒤 다시 시도하세요.") {
			t.Fatalf("%d: %v", bad, err)
		}
	}
	if rows, _ := e.budget.List(as(uFinance), nov); rows[0].Amount != 10_000_000 {
		t.Fatalf("거부된 저장이 반영됨: %+v", rows[0])
	}
	// 이미 초과인 달: 사용액 아래로 줄일 수는 없지만 올리기는 항상 된다.
	e2 := newEnv(t)
	e2.setBudget(t, deptBuy, 5_000_000)
	e2.submitted(t, uStaff, 8_000_000) // 초과로 상신(재무 단계 강제)
	if _, err := e2.budget.Set(as(uFinance), domain.BudgetInput{DepartmentID: deptBuy, Month: nov, Amount: 4_000_000, Reason: "x"}); !isErr(err, contract.ErrValidation) {
		t.Fatalf("초과 상태에서 더 줄이기: %v", err)
	}
	if _, err := e2.budget.Set(as(uFinance), domain.BudgetInput{DepartmentID: deptBuy, Month: nov, Amount: 6_000_000, Reason: "x"}); err != nil {
		t.Fatalf("초과 상태에서 올리기: %v", err)
	}
	// 처음 등록은 제한이 없다(줄이는 것이 아니다).
	if _, err := e2.budget.Set(as(uFinance), domain.BudgetInput{DepartmentID: deptSales, Month: nov, Amount: 1, Reason: "처음"}); err != nil {
		t.Fatalf("처음 등록: %v", err)
	}
}

// 줄이기와 상신이 동시에 와도 합계는 같은 트랜잭션에서 다시 읽어 순서대로 판정된다.
func TestBudgetReduceRacesWithSubmit(t *testing.T) {
	for i := 0; i < 5; i++ {
		e := newEnvAt(t, fmt.Sprintf("b25race%d.db", i))
		e.setBudget(t, deptBuy, 10_000_000)
		d := e.draft(t, uStaff, 3_000_000)
		var wg sync.WaitGroup
		var pr domain.PurchaseRequest
		var subErr, setErr error
		wg.Add(2)
		go func() { defer wg.Done(); pr, subErr = e.purchase.Submit(as(uStaff), d.ID) }()
		go func() {
			defer wg.Done()
			_, setErr = e.budget.Set(as(uFinance), domain.BudgetInput{DepartmentID: deptBuy, Month: nov, Amount: 2_500_000, Reason: "삭감"})
		}()
		wg.Wait()
		if subErr != nil {
			t.Fatalf("상신: %v", subErr)
		}
		rows, _ := e.budget.List(as(uFinance), nov)
		switch {
		case setErr == nil && rows[0].Amount == 2_500_000:
			// 삭감이 먼저: 상신은 줄어든 예산으로 판정되어 초과다.
			if pr.Budget.State != domain.BudgetOver {
				t.Fatalf("삭감 뒤 상신은 초과여야 함: %+v", pr.Budget)
			}
		case isErr(setErr, contract.ErrValidation):
			// 상신이 먼저: 쓴 금액(300만) 아래로 줄일 수 없다.
			if rows[0].Amount != 10_000_000 || pr.Budget.State != domain.BudgetWithin {
				t.Fatalf("상신 먼저: 예산 %d, 판정 %+v", rows[0].Amount, pr.Budget)
			}
		default:
			t.Fatalf("예상 밖 결과: setErr=%v 예산=%d", setErr, rows[0].Amount)
		}
	}
}

// 문구는 명세와 같고, 감사 diff 에 이전·이후 월 예산이 같이 남는다(budget.set).
func TestBudgetMessagesAndAuditBeforeAfter(t *testing.T) {
	e := newEnv(t)
	if _, err := e.budget.Set(as(uFinance), domain.BudgetInput{DepartmentID: deptBuy, Month: nov, Amount: 1, Reason: " "}); !isErr(err, contract.ErrValidation) || !strings.Contains(err.Error(), "예산을 바꾸는 사유를 입력하세요.") {
		t.Fatalf("사유 문구: %v", err)
	}
	if _, err := e.budget.Set(as(uFinance), domain.BudgetInput{DepartmentID: deptBuy, Month: nov, Amount: -1, Reason: "x"}); !isErr(err, contract.ErrValidation) || !strings.Contains(err.Error(), "월 예산은 0 이상의 숫자로 입력하세요. 쉼표(,)는 넣어도 됩니다.") {
		t.Fatalf("금액 문구: %v", err)
	}
	e.setBudget(t, deptBuy, 1_000_000)
	if _, err := e.budget.Set(as(uFinance), domain.BudgetInput{DepartmentID: deptBuy, Month: nov, Amount: 2_000_000, Reason: "증액"}); err != nil {
		t.Fatal(err)
	}
	var last audit.Record
	for _, r := range e.rec.Records() {
		if r.Action == "budget.set" && r.Result == audit.ResultSuccess {
			last = r
		}
	}
	found := false
	for _, d := range last.Diff {
		if d.Key == "amount" && d.Old == "1000000" && d.New == "2000000" {
			found = true
		}
	}
	if !found {
		t.Fatalf("diff %+v", last.Diff)
	}
}
