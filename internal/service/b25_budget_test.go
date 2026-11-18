package service_test

import (
	"strings"
	"sync"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
)

const nov = "2026-11"

func (e *env) setBudget(t *testing.T, dept, amount int64) {
	t.Helper()
	if _, err := e.budget.Set(as(uFinance), domain.BudgetInput{DepartmentID: dept, Month: nov, Amount: amount, Reason: "분기 계획"}); err != nil {
		t.Fatal(err)
	}
}

func stepCount(pr domain.PurchaseRequest, k domain.StepKind) int {
	n := 0
	for _, s := range pr.Steps {
		if s.Kind == k {
			n++
		}
	}
	return n
}

// 예산 이내면 결재선은 금액 구간 그대로이고, 판정은 within 으로 저장된다.
func TestBudgetWithinKeepsLine(t *testing.T) {
	e := newEnv(t)
	e.setBudget(t, deptBuy, 500_000)
	pr := e.submitted(t, uStaff, 400_000)
	if stepCount(pr, domain.StepFinance) != 0 {
		t.Fatalf("이내인데 재무 단계가 있음: %v", kinds(pr))
	}
	if pr.Budget == nil || pr.Budget.State != domain.BudgetWithin || pr.Budget.Month != nov || pr.Budget.Budget != 500_000 {
		t.Fatalf("스냅샷 %+v", pr.Budget)
	}
}

// 잔액과 같은 금액은 이내, 1원 넘으면 초과(경계)이고 초과면 결재선 끝에 재무 단계가 필수로 붙는다.
func TestBudgetBoundaryAndForcedFinance(t *testing.T) {
	e := newEnv(t)
	e.setBudget(t, deptBuy, 500_000)
	same := e.submitted(t, uStaff, 500_000)
	if same.Budget.State != domain.BudgetWithin || stepCount(same, domain.StepFinance) != 0 {
		t.Fatalf("잔액과 같은 금액은 이내: %+v %v", same.Budget, kinds(same))
	}
	// 같은 달 진행 중 500,000 이 잔액을 다 썼으니 1원도 초과다.
	over := e.submitted(t, uStaff, 1)
	if over.Budget.State != domain.BudgetOver || over.Budget.Over != 1 {
		t.Fatalf("초과 판정 %+v", over.Budget)
	}
	ks := kinds(over)
	if len(ks) < 2 || ks[len(ks)-1] != domain.StepFinance {
		t.Fatalf("끝에 재무 단계가 붙어야 함: %v", ks)
	}
	if over.Budget.FinanceAdded == nil || !*over.Budget.FinanceAdded {
		t.Fatalf("예산 초과로 재무를 더했으면 FinanceAdded: %+v", over.Budget)
	}
	if same.Budget.FinanceAdded == nil || *same.Budget.FinanceAdded {
		t.Fatalf("이내인데 FinanceAdded: %+v", same.Budget)
	}
}

// 금액 구간에 재무가 이미 있으면 더하지 않는다(Notes 로만 알린다). 미리보기와 상신이 같은 결과다.
func TestBudgetOverWithFinanceAlreadyInLine(t *testing.T) {
	e := newEnv(t)
	e.setBudget(t, deptBuy, 1_000)
	pv, err := e.line.PreviewLine(as(uStaff), domain.LinePreviewInput{Amount: 12_000_000})
	if err != nil {
		t.Fatal(err)
	}
	fin := 0
	for _, s := range pv.Steps {
		if s.Kind == domain.StepFinance {
			fin++
			if s.Forced {
				t.Fatal("이미 있던 재무 단계가 강제 표시됨")
			}
		}
	}
	if fin != 1 || !strings.Contains(strings.Join(pv.Notes, "|"), "재무 단계는 이미 포함되어 있습니다") {
		t.Fatalf("재무 %d, notes %v", fin, pv.Notes)
	}
	pr := e.submitted(t, uStaff, 12_000_000)
	if stepCount(pr, domain.StepFinance) != 1 || pr.Budget.State != domain.BudgetOver {
		t.Fatalf("상신 결과 %v %+v", kinds(pr), pr.Budget)
	}
	if pr.Budget.FinanceAdded == nil || *pr.Budget.FinanceAdded {
		t.Fatalf("금액 구간으로 이미 있던 재무 단계는 더한 것이 아님: %+v", pr.Budget)
	}
}

// 미리보기는 초과일 때 재무 단계를 강제로 더해 보여 주고, 상신한 결재선과 같다.
func TestBudgetPreviewMatchesSubmit(t *testing.T) {
	e := newEnv(t)
	e.setBudget(t, deptBuy, 100_000)
	pv, err := e.line.PreviewLine(as(uStaff), domain.LinePreviewInput{Amount: 300_000})
	if err != nil {
		t.Fatal(err)
	}
	last := pv.Steps[len(pv.Steps)-1]
	if last.Kind != domain.StepFinance || !last.Forced || !strings.Contains(strings.Join(pv.Notes, "|"), "예산 초과로 재무 단계를 더했습니다") {
		t.Fatalf("미리보기 %+v %v", pv.Steps, pv.Notes)
	}
	pr := e.submitted(t, uStaff, 300_000)
	if len(pr.Steps) != len(pv.Steps) {
		t.Fatalf("미리보기 %d단계, 상신 %d단계", len(pv.Steps), len(pr.Steps))
	}
	// 이내 금액의 미리보기에는 재무가 없다.
	pv2, _ := e.line.PreviewLine(as(uStaff2), domain.LinePreviewInput{Amount: 300_000})
	for _, s := range pv2.Steps {
		if s.Kind == domain.StepFinance {
			t.Fatal("예산 미등록 부서에 재무 단계가 붙음")
		}
	}
}

// 예산이 등록되지 않은 부서·월은 확인하지 않는다.
func TestBudgetUnregistered(t *testing.T) {
	e := newEnv(t)
	pr := e.submitted(t, uStaff, 5_000_000)
	if pr.Budget == nil || pr.Budget.State != domain.BudgetUnregistered || stepCount(pr, domain.StepFinance) != 0 {
		t.Fatalf("%+v %v", pr.Budget, kinds(pr))
	}
}

// 사용액은 발주가 만들어진 건, 진행 중은 상신~결재중 건이고 반려·회수는 빠진다. 승인 뒤에는 월이 바뀌어도 상신한 달에 남는다.
func TestBudgetUsedAndInProgress(t *testing.T) {
	e := newEnv(t)
	e.setBudget(t, deptBuy, 1_000_000)
	a := e.submitted(t, uStaff, 300_000)
	b := e.submitted(t, uStaff, 200_000)
	rows, err := e.budget.List(as(uFinance), nov)
	if err != nil || len(rows) != 1 {
		t.Fatalf("%v %v", rows, err)
	}
	if rows[0].InProgress != 500_000 || rows[0].Used != 0 || rows[0].Balance != 500_000 || rows[0].UsagePct != 50 || rows[0].Badge != domain.BadgeOK {
		t.Fatalf("진행 중 %+v", rows[0])
	}
	approve(t, e, uLead, a.ID)
	if _, err := e.approval.Decide(as(uLead), b.ID, domain.Decision{Approve: false, Comment: "사유 부족"}); err != nil {
		t.Fatal(err)
	}
	e.now = e.now.AddDate(0, 1, 5) // 12월이 되어도 11월 예산 표의 숫자는 그대로다
	rows, _ = e.budget.List(as(uFinance), nov)
	if rows[0].Used != 300_000 || rows[0].InProgress != 0 || rows[0].Balance != 700_000 || rows[0].UsagePct != 30 {
		t.Fatalf("승인·반려 뒤 %+v", rows[0])
	}
}

// 배지 경계: 80% 미만 여유, 80~100% 한도 근접, 100% 초과 예산 초과(0 원 예산은 사용이 있으면 초과).
func TestBudgetBadges(t *testing.T) {
	e := newEnv(t)
	e.setBudget(t, deptBuy, 1_000_000)
	e.submitted(t, uStaff, 799_999)
	rows, _ := e.budget.List(as(uFinance), nov)
	if rows[0].Badge != domain.BadgeOK {
		t.Fatalf("79%%: %+v", rows[0])
	}
	e.submitted(t, uStaff, 1)
	rows, _ = e.budget.List(as(uFinance), nov)
	if rows[0].Badge != domain.BadgeNear || rows[0].UsagePct != 80 {
		t.Fatalf("80%%: %+v", rows[0])
	}
	e.submitted(t, uStaff, 200_000)
	rows, _ = e.budget.List(as(uFinance), nov)
	if rows[0].Badge != domain.BadgeNear || rows[0].Balance != 0 {
		t.Fatalf("100%% 는 한도 근접: %+v", rows[0])
	}
	e.submitted(t, uStaff, 1)
	rows, _ = e.budget.List(as(uFinance), nov)
	if rows[0].Badge != domain.BadgeOver || rows[0].Balance != -1 {
		t.Fatalf("초과: %+v", rows[0])
	}
}

// 예산 0 원이면 모든 요청이 초과다(삭제 없음).
func TestBudgetZeroMakesEveryRequestOver(t *testing.T) {
	e := newEnv(t)
	e.setBudget(t, deptBuy, 0)
	pr := e.submitted(t, uStaff, 1_000)
	if pr.Budget.State != domain.BudgetOver {
		t.Fatalf("%+v", pr.Budget)
	}
}

// 예산 수정은 재무만, 사유 필수, 이번 달 이후만. 이력과 감사가 남고 삭제 경로는 없다.
func TestBudgetSetRules(t *testing.T) {
	e := newEnv(t)
	in := domain.BudgetInput{DepartmentID: deptBuy, Month: nov, Amount: 1_000_000, Reason: "계획"}
	for _, who := range []int64{uStaff, uLead, uAdmin, uAuditor} {
		if _, err := e.budget.Set(as(who), in); !isErr(err, contract.ErrForbidden) {
			t.Fatalf("사용자 %d 는 수정할 수 없어야 함: %v", who, err)
		}
	}
	bad := map[string]domain.BudgetInput{
		"사유 없음":   {DepartmentID: deptBuy, Month: nov, Amount: 1, Reason: " "},
		"지난 달":    {DepartmentID: deptBuy, Month: "2026-10", Amount: 1, Reason: "x"},
		"음수":      {DepartmentID: deptBuy, Month: nov, Amount: -1, Reason: "x"},
		"월 형식":    {DepartmentID: deptBuy, Month: "2026-13", Amount: 1, Reason: "x"},
		"없는 부서":   {DepartmentID: 99, Month: nov, Amount: 1, Reason: "x"},
		"사유 200자": {DepartmentID: deptBuy, Month: nov, Amount: 1, Reason: strings.Repeat("가", 201)},
	}
	for name, b := range bad {
		if _, err := e.budget.Set(as(uFinance), b); !isErr(err, contract.ErrValidation) {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if _, err := e.budget.Set(as(uFinance), in); err != nil {
		t.Fatal(err)
	}
	in.Amount, in.Reason = 2_000_000, "증액"
	if _, err := e.budget.Set(as(uFinance), in); err != nil {
		t.Fatal(err)
	}
	revs, err := e.budget.Revisions(as(uAuditor), nov)
	if err != nil || len(revs) != 2 {
		t.Fatalf("이력 %v %v", revs, err)
	}
	if revs[0].OldAmount == nil || *revs[0].OldAmount != 1_000_000 || revs[0].NewAmount != 2_000_000 || revs[1].OldAmount != nil {
		t.Fatalf("이력 값 %+v", revs)
	}
	if n := count(t, e.db, `SELECT COUNT(*) FROM budgets`); n != 1 {
		t.Fatalf("부서·월당 한 줄: %d", n)
	}
	if _, err := e.db.Exec(`DELETE FROM budgets`); err == nil {
		t.Fatal("예산 행 삭제가 허용됨")
	}
	if _, err := e.db.Exec(`UPDATE budget_revisions SET reason = 'x'`); err == nil {
		t.Fatal("이력 수정이 허용됨")
	}
	if _, err := e.db.Exec(`DELETE FROM budget_revisions`); err == nil {
		t.Fatal("이력 삭제가 허용됨")
	}
	n := 0
	for _, r := range e.rec.Records() {
		if r.Action == "budget.set" && r.Result == audit.ResultSuccess {
			n++
		}
	}
	if n != 2 {
		t.Fatalf("감사 budget.set %d건", n)
	}
	// 조회 권한: 재무·관리자·감사는 보고 직원은 못 본다.
	for _, who := range []int64{uFinance, uAdmin, uAuditor} {
		if _, err := e.budget.List(as(who), nov); err != nil {
			t.Fatalf("%d 조회: %v", who, err)
		}
	}
	if _, err := e.budget.List(as(uStaff), nov); !isErr(err, contract.ErrForbidden) {
		t.Fatalf("직원 조회: %v", err)
	}
}

// 작성 화면의 예산 확인은 본인 부서만(다른 부서는 예산 조회 권한이 있어야 한다).
func TestBudgetCheckScope(t *testing.T) {
	e := newEnv(t)
	e.setBudget(t, deptBuy, 1_000_000)
	c, err := e.budget.Check(as(uStaff), domain.BudgetCheckInput{Amount: 1_200_000})
	if err != nil || c.State != domain.BudgetOver || c.Over != 200_000 || c.Budget != 1_000_000 || c.After != -200_000 {
		t.Fatalf("%+v %v", c, err)
	}
	if _, err := e.budget.Check(as(uStaff), domain.BudgetCheckInput{Amount: 1, DepartmentID: deptSales}); !isErr(err, contract.ErrForbidden) {
		t.Fatalf("타 부서: %v", err)
	}
	if _, err := e.budget.Check(as(uFinance), domain.BudgetCheckInput{Amount: 1, DepartmentID: deptBuy}); err != nil {
		t.Fatalf("재무의 타 부서 확인: %v", err)
	}
	// 영업팀은 예산이 없으니 미등록이다(타 부서 금액이 새지 않는다).
	c2, _ := e.budget.Check(as(uStaff2), domain.BudgetCheckInput{Amount: 5_000_000})
	if c2.State != domain.BudgetUnregistered || c2.Budget != 0 {
		t.Fatalf("%+v", c2)
	}
}

// 같은 부서 두 요청이 동시에 상신되어도 판정은 순서대로다: 잔액 안에 들어가는 건 한 건뿐이다.
func TestBudgetConcurrentSubmitJudgedInOrder(t *testing.T) {
	e := newEnvAt(t, "b25.db")
	e.setBudget(t, deptBuy, 500_000)
	a, b := e.draft(t, uStaff, 300_000), e.draft(t, uLead3, 300_000)
	var wg sync.WaitGroup
	res := make([]domain.PurchaseRequest, 2)
	errs := make([]error, 2)
	for i, x := range []struct{ by, id int64 }{{uStaff, a.ID}, {uLead3, b.ID}} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res[i], errs[i] = e.purchase.Submit(as(x.by), x.id)
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("상신 %d: %v", i, err)
		}
	}
	over := 0
	for _, r := range res {
		if r.Budget != nil && r.Budget.State == domain.BudgetOver {
			over++
			if stepCount(r, domain.StepFinance) != 1 {
				t.Fatalf("초과 건에 재무 단계가 없음: %v", kinds(r))
			}
		}
	}
	if over != 1 {
		t.Fatalf("초과는 정확히 한 건이어야 함: %d", over)
	}
}

// 희망 납기·납품처: 형식·지난 날짜 검사, 비면 기본 납품처, 발주가 만들어질 때 복사된다.
func TestDeliveryFieldsAndPOCopy(t *testing.T) {
	e := newEnv(t)
	it := e.item(t, "D-1", 0, false)
	v := e.vendor(t)
	mk := func(due, place string) (domain.PurchaseRequest, error) {
		return e.purchase.CreateDraft(as(uStaff), domain.DraftInput{VendorID: v.ID, Title: "납기", Reason: "x", DueDate: due, DeliveryPlace: place,
			Lines: []domain.PRLineInput{{ItemID: it.ID, Quantity: 1, UnitPrice: 1000}}})
	}
	if _, err := mk("2026-11-09", ""); !isErr(err, contract.ErrValidation) {
		t.Fatalf("지난 납기: %v", err)
	}
	if _, err := mk("11/20", ""); !isErr(err, contract.ErrValidation) {
		t.Fatalf("형식: %v", err)
	}
	if _, err := mk("", strings.Repeat("가", 101)); !isErr(err, contract.ErrValidation) {
		t.Fatalf("납품처 길이: %v", err)
	}
	def, err := mk("", "")
	if err != nil || def.DueDate != "" || def.DeliveryPlace != "본사 창고" {
		t.Fatalf("기본값 %+v %v", def, err)
	}
	pr, err := mk("2026-11-20", "제2창고")
	if err != nil || pr.DueDate != "2026-11-20" || pr.DeliveryPlace != "제2창고" {
		t.Fatalf("%+v %v", pr, err)
	}
	if _, err := e.purchase.Submit(as(uStaff), pr.ID); err != nil {
		t.Fatal(err)
	}
	approve(t, e, uLead, pr.ID)
	var due, place string
	if err := e.db.QueryRow(`SELECT COALESCE(due_date,''), delivery_place FROM purchase_orders WHERE pr_id = ?`, pr.ID).Scan(&due, &place); err != nil {
		t.Fatal(err)
	}
	if due != "2026-11-20" || place != "제2창고" {
		t.Fatalf("발주 복사 %q %q", due, place)
	}
}
