package service_test

import (
	"strings"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/service"
)

// B35 마스터 어댑터 시험: 공통 흐름(platform)이 하는 일을 흉내 낸다. Plan 으로 판정하고, add·change 행만 한 트랜잭션에서 Apply 한다.

func irow(no int, cells map[string]string) domain.ImportRow {
	return domain.ImportRow{RowNo: no, Key: cells["code"] + cells["sku"] + cells["name"], Cells: cells}
}

func (e *env) importBase() service.Base { return e.journals.Base }

// importRun 은 Plan → (오류가 없으면) Apply 를 한다. 적용 행만 걸러 넘기는 것까지 공통 흐름과 같다.
func (e *env) importRun(t *testing.T, h domain.ImportTargetHandler, rows []domain.ImportRow, in domain.ImportPlanInput) (domain.ImportPlan, domain.ImportApplyOutcome, error) {
	t.Helper()
	plan, err := h.Plan(as(uFinance), e.db, rows, in)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Verdicts) != len(rows) {
		t.Fatalf("판정 %d건, 행 %d건: 입력과 같은 순서·개수여야 함", len(plan.Verdicts), len(rows))
	}
	for _, v := range plan.Verdicts {
		if v.Verdict == domain.ImportVerdictError {
			return plan, domain.ImportApplyOutcome{}, nil
		}
	}
	var applyRows []domain.ImportRow
	var applyPlan []domain.ImportRowVerdict
	for i, v := range plan.Verdicts {
		if v.Verdict == domain.ImportVerdictAdd || v.Verdict == domain.ImportVerdictChange {
			applyRows, applyPlan = append(applyRows, rows[i]), append(applyPlan, v)
		}
	}
	var out domain.ImportApplyOutcome
	ctx := as(uFinance)
	p, _ := contract.PrincipalFrom(ctx)
	err = contract.SQLTxManager{DB: e.db}.WithTx(ctx, func(tx contract.Tx) error {
		var aerr error
		out, aerr = h.Apply(ctx, tx, p, applyRows, applyPlan, in)
		return aerr
	})
	return plan, out, err
}

func verdicts(p domain.ImportPlan) []string {
	var out []string
	for _, v := range p.Verdicts {
		out = append(out, v.Verdict)
	}
	return out
}

func wantVerdicts(t *testing.T, p domain.ImportPlan, want ...string) {
	t.Helper()
	if got := verdicts(p); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("판정 %v, 기대 %v (%+v)", got, want, p.Verdicts)
	}
}

func TestImportAccountsPlanAndApply(t *testing.T) {
	e := newEnv(t)
	h := service.NewImportAccount(e.importBase())
	in := domain.ImportPlanInput{Target: domain.ImportAccount, BatchNo: "IMP-2026-0001"}
	rows := []domain.ImportRow{
		irow(2, map[string]string{"code": "5310", "name": "광고선전비", "kind": "expense"}),
		irow(3, map[string]string{"code": "5990", "name": "임시비용", "kind": "expense", "active": "inactive"}),
	}
	plan, _, err := e.importRun(t, h, rows, in)
	if err != nil {
		t.Fatal(err)
	}
	wantVerdicts(t, plan, "add", "add")
	if n := e.col(t, `SELECT COUNT(*) FROM accounts WHERE code IN ('5310','5990')`); n != "2" {
		t.Fatalf("계정이 추가되지 않음: %s", n)
	}
	if a := e.col(t, `SELECT active FROM accounts WHERE code = '5990'`); a != "0" {
		t.Errorf("중지로 들어온 계정: %s", a)
	}
	if !e.rec.Has("account.create", audit.TargetAccount, audit.ResultSuccess) {
		t.Error("계정 추가 감사가 없음")
	}
	// 같은 파일을 다시: 동일. 이름·사용 여부만 바뀌면 변경, 구분이 다르면 오류, 시스템 계정 중지는 오류.
	again, _, _ := e.importRun(t, h, rows, in)
	wantVerdicts(t, again, "same", "same")
	chg2 := []domain.ImportRow{
		irow(2, map[string]string{"code": "5310", "name": "광고비", "kind": "expense", "active": "inactive"}),
	}
	plan, _, err = e.importRun(t, h, chg2, in)
	if err != nil {
		t.Fatal(err)
	}
	wantVerdicts(t, plan, "change")
	if len(plan.Verdicts[0].Changes) != 2 || plan.Verdicts[0].Changes[0].Old != "광고선전비" || plan.Verdicts[0].Changes[1].New != "중지" {
		t.Fatalf("바뀌는 칸: %+v", plan.Verdicts[0].Changes)
	}
	if e.col(t, `SELECT name FROM accounts WHERE code = '5310'`) != "광고비" {
		t.Error("이름이 바뀌지 않음")
	}
	bad := []domain.ImportRow{
		irow(2, map[string]string{"code": "5310", "name": "광고비", "kind": "revenue"}),
		irow(3, map[string]string{"code": "1210", "name": "재고자산", "kind": "asset", "active": "inactive"}),
		irow(4, map[string]string{"code": "4100", "name": "x", "kind": "expense"}),
	}
	plan, _, _ = e.importRun(t, h, bad, in)
	wantVerdicts(t, plan, "error", "error", "error")
	for _, v := range plan.Verdicts {
		if v.Reason == "" {
			t.Errorf("오류 사유가 비어 있음: %+v", v)
		}
	}
}

func TestImportWarehousesPlanAndApply(t *testing.T) {
	e := newEnv(t)
	h := service.NewImportWarehouse(e.importBase())
	in := domain.ImportPlanInput{Target: domain.ImportWarehouse, BatchNo: "IMP-2026-0002"}
	rows := []domain.ImportRow{
		irow(2, map[string]string{"code": "wh-10", "name": "물류센터", "location": "이천"}),
		irow(3, map[string]string{"code": "WH-11", "name": "물류센터"}), // 같은 파일의 다른 코드가 같은 이름
	}
	plan, _, _ := e.importRun(t, h, rows, in)
	wantVerdicts(t, plan, "add", "error")
	rows = rows[:1]
	if _, _, err := e.importRun(t, h, rows, in); err != nil {
		t.Fatal(err)
	}
	if e.col(t, `SELECT name FROM warehouses WHERE code = 'WH-10'`) != "물류센터" {
		t.Fatal("창고가 추가되지 않음(코드는 대문자로 저장)")
	}
	again, _, _ := e.importRun(t, h, rows, in)
	wantVerdicts(t, again, "same")
	chg2 := []domain.ImportRow{irow(2, map[string]string{"code": "WH-10", "name": "물류센터", "location": "안성"})}
	plan, _, _ = e.importRun(t, h, chg2, in)
	wantVerdicts(t, plan, "change")
	if plan.Verdicts[0].Changes[0].Column != "위치" || e.col(t, `SELECT location FROM warehouses WHERE code = 'WH-10'`) != "안성" {
		t.Fatalf("위치 변경: %+v", plan.Verdicts[0])
	}
	clash := []domain.ImportRow{irow(2, map[string]string{"code": "WH-12", "name": "물류센터"})}
	plan, _, _ = e.importRun(t, h, clash, in)
	wantVerdicts(t, plan, "error")
}

func TestImportVendorsMaskSensitiveValuesAndMatchByKey(t *testing.T) {
	e := newEnv(t)
	h := service.NewImportVendor(e.importBase())
	in := domain.ImportPlanInput{Target: domain.ImportVendor, BatchNo: "IMP-2026-0003"}
	rows := []domain.ImportRow{
		irow(2, map[string]string{"name": "한빛상사", "business_no": "1234567890", "payment_days": "45", "phone": "02-123-4567", "email": "a@example.com", "account_no": "110-123-456789"}),
		irow(3, map[string]string{"name": "이름만상사"}),
	}
	plan, _, err := e.importRun(t, h, rows, in)
	if err != nil {
		t.Fatal(err)
	}
	wantVerdicts(t, plan, "add", "add")
	if strings.Contains(plan.Verdicts[0].Key, "1234567890") || !strings.Contains(plan.Verdicts[0].Key, "7890") {
		t.Errorf("키에 사업자번호 원문이 나감: %q", plan.Verdicts[0].Key)
	}
	if e.col(t, `SELECT payment_days FROM vendors WHERE name = '한빛상사'`) != "45" {
		t.Error("결제 조건이 저장되지 않음")
	}
	if got := e.col(t, `SELECT account_no FROM vendors WHERE name = '한빛상사'`); !strings.HasPrefix(got, "enc:v1:") {
		t.Errorf("계좌번호는 봉해서 저장: %q", got)
	}
	again, _, _ := e.importRun(t, h, rows, in)
	wantVerdicts(t, again, "same", "same")
	// 사업자번호가 같으면 이름이 달라도 같은 거래처(변경 + 경고). 변경 칸의 민감 값은 끝 4자리만 보인다.
	chg2 := []domain.ImportRow{irow(2, map[string]string{"name": "한빛산업", "business_no": "123-45-67890", "payment_days": "30", "phone": "02-999-8888", "account_no": "110-123-999999"})}
	plan, _, err = e.importRun(t, h, chg2, in)
	if err != nil {
		t.Fatal(err)
	}
	wantVerdicts(t, plan, "change")
	v := plan.Verdicts[0]
	if v.Warn == "" {
		t.Error("이름 변경 경고가 없음")
	}
	for _, c := range v.Changes {
		if strings.Contains(c.Old+c.New, "4567") && c.Column == "계좌번호" || strings.Contains(c.Old+c.New, "110-123") || strings.Contains(c.Old+c.New, "02-999") || strings.Contains(c.Old+c.New, "02-123") {
			t.Errorf("민감 값 원문이 판정에 나감: %+v", c)
		}
	}
	if e.col(t, `SELECT name FROM vendors WHERE business_no LIKE '%67890'`) != "한빛산업" || e.col(t, `SELECT payment_days FROM vendors WHERE name = '한빛산업'`) != "30" {
		t.Error("변경이 반영되지 않음")
	}
	// 같은 이름 둘 이상(사업자번호 없음)은 대상을 정할 수 없다. 새 사업자번호인데 같은 이름이 있으면 오류.
	if _, err := e.db.Exec(`INSERT INTO vendors (name, business_no, account_no, phone, email) VALUES ('중복상사','','','',''),('중복 상사','','','','')`); err != nil {
		t.Fatal(err)
	}
	amb := []domain.ImportRow{irow(2, map[string]string{"name": "중복상사"}), irow(3, map[string]string{"name": "이름만상사", "business_no": "9998887776"})}
	plan, _, _ = e.importRun(t, h, amb, in)
	wantVerdicts(t, plan, "error", "error")
	if !e.rec.Has("vendor.update", audit.TargetVendor, audit.ResultSuccess) || !e.rec.Has("vendor.create", audit.TargetVendor, audit.ResultSuccess) {
		t.Error("거래처 감사가 없음")
	}
}

func TestImportItemsPlanApplyAndLotGuard(t *testing.T) {
	e := newEnv(t)
	h := service.NewImportItem(e.importBase())
	in := domain.ImportPlanInput{Target: domain.ImportItem, BatchNo: "IMP-2026-0004"}
	rows := []domain.ImportRow{
		irow(2, map[string]string{"sku": "PAPER-A4", "name": "복사용지 A4", "unit": "박스", "safety_stock": "100", "lot_tracked": "true"}),
		irow(3, map[string]string{"sku": "BOLT-M8", "name": "볼트 M8", "unit": "EA"}),
	}
	plan, _, err := e.importRun(t, h, rows, in)
	if err != nil {
		t.Fatal(err)
	}
	wantVerdicts(t, plan, "add", "add")
	again, _, _ := e.importRun(t, h, rows, in)
	wantVerdicts(t, again, "same", "same")
	lower := []domain.ImportRow{irow(2, map[string]string{"sku": "paper-a4", "name": "복사용지 A4 80g", "unit": "박스", "safety_stock": "150", "lot_tracked": "true"})}
	plan, _, err = e.importRun(t, h, lower, in)
	if err != nil {
		t.Fatal(err)
	}
	wantVerdicts(t, plan, "change")
	if len(plan.Verdicts[0].Changes) != 2 || e.col(t, `SELECT safety_stock FROM items WHERE sku = 'PAPER-A4'`) != "150" {
		t.Fatalf("SKU 는 대소문자 구분 없이 같은 품목: %+v", plan.Verdicts[0])
	}
	// 재고가 남은 로트 품목의 로트 관리 끄기는 오류, 재고가 없으면 변경이다.
	lotItem := e.item(t, "LOT-X", 0, true)
	e.now = at(2026, 11, 10, 1)
	v := e.vendor(t)
	pr, err := e.purchase.CreateDraft(as(uStaff), domain.DraftInput{VendorID: v.ID, Title: "로트 시험", Reason: "x", Lines: []domain.PRLineInput{{ItemID: lotItem.ID, Quantity: 5, UnitPrice: 100}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.purchase.Submit(as(uStaff), pr.ID); err != nil {
		t.Fatal(err)
	}
	res := approve(t, e, uLead, pr.ID)
	po := *res.PurchaseOrder
	if _, err := e.inventory.Receive(as(uFinance), recv(po.ID, po.Lines[0].ID, 5, "L1", "2027-12-31")); err != nil {
		t.Fatal(err)
	}
	off := []domain.ImportRow{irow(2, map[string]string{"sku": "LOT-X", "name": "품목 LOT-X", "unit": "EA", "lot_tracked": "false"})}
	plan, _, _ = e.importRun(t, h, off, in)
	wantVerdicts(t, plan, "error")
	if !strings.Contains(plan.Verdicts[0].Reason, "로트") {
		t.Errorf("오류 사유: %+v", plan.Verdicts[0])
	}
	empty := e.item(t, "LOT-Y", 0, true)
	offEmpty := []domain.ImportRow{irow(2, map[string]string{"sku": "LOT-Y", "name": "품목 LOT-Y", "unit": "EA", "lot_tracked": "false"})}
	plan, _, _ = e.importRun(t, h, offEmpty, in)
	wantVerdicts(t, plan, "change")
	_ = empty
}
