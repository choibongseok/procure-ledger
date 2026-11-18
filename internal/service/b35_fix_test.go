package service_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/service"
)

// B35 판정 지적 보완 시험: 파일 안 중복은 입력 문자열이 아니라 확정된 번호로 판정하고, 큰 파일은 행마다 조회하지 않으며, 마감 월 거부는 감사에 남는다.

func TestImportDuplicatesAreJudgedByResolvedIDsNotInputText(t *testing.T) {
	e := newEnv(t)
	e.jvForm(t)
	vh := service.NewImportVendor(e.importBase())
	if _, _, err := e.importRun(t, vh, []domain.ImportRow{irow(2, map[string]string{"name": "한빛상사", "business_no": "1234567890"})},
		domain.ImportPlanInput{Target: domain.ImportVendor, BatchNo: "IMP-2026-0060"}); err != nil {
		t.Fatal(err)
	}
	// 미지급 잔액: 같은 거래처를 사업자번호와 이름으로 섞어 적고 같은 문서 번호를 넣으면 뒤 행이 오류다(채무 이중 계상 방지).
	ph := service.NewImportOpeningPayable(e.importBase(), e.journals)
	in := domain.ImportPlanInput{Target: domain.ImportOpeningPayable, BaseDate: "2026-11-05", BatchNo: "IMP-2026-0061"}
	rows := []domain.ImportRow{
		irow(2, map[string]string{"vendor": "123-45-67890", "doc_no": "DUP-1", "trade_date": "2026-10-21", "amount": "1000"}),
		irow(3, map[string]string{"vendor": "한빛상사", "doc_no": "DUP-1", "trade_date": "2026-10-21", "amount": "1000"}),
		irow(4, map[string]string{"vendor": "한빛 상사", "doc_no": "DUP-2", "trade_date": "2026-10-21", "amount": "500"}),
	}
	plan, out, _ := e.importRun(t, ph, rows, in)
	wantVerdicts(t, plan, "add", "error", "add")
	if !strings.Contains(plan.Verdicts[1].Reason, "2번째 줄") && !strings.Contains(plan.Verdicts[1].Reason, "같은 거래처·문서 번호") {
		t.Errorf("중복 사유: %+v", plan.Verdicts[1])
	}
	if len(out.JournalNos) != 0 {
		t.Fatal("오류 행이 있는 묶음은 반영되지 않아야 함")
	}
	ok := []domain.ImportRow{rows[0], rows[2]}
	_, out, err := e.importRun(t, ph, ok, in)
	if err != nil || len(out.JournalNos) != 1 {
		t.Fatalf("%+v %v", out, err)
	}
	j, _ := e.journals.Get(as(uFinance), out.JournalNos[0])
	e.approveJV(t, j)
	if e.col(t, `SELECT COUNT(*) FROM payables WHERE canceled = 0`) != "2" || e.col(t, `SELECT SUM(amount) FROM payables`) != "1500" {
		t.Fatal("채무가 한 번씩만 계상되어야 함")
	}
	// 거래처 대상: 같은 새 거래처를 이름만/이름+사업자번호로 두 줄 적으면 뒤 줄이 오류, 기존 거래처를 두 줄로 바꾸는 것도 오류다.
	vrows := []domain.ImportRow{
		irow(2, map[string]string{"name": "새상사", "business_no": "5556667778"}),
		irow(3, map[string]string{"name": "새 상사"}),
		irow(4, map[string]string{"name": "한빛상사", "business_no": "1234567890", "phone": "02-111-2222"}),
		irow(5, map[string]string{"name": "한빛상사", "phone": "02-333-4444"}),
	}
	vplan, _, _ := e.importRun(t, vh, vrows, domain.ImportPlanInput{Target: domain.ImportVendor, BatchNo: "IMP-2026-0062"})
	wantVerdicts(t, vplan, "add", "error", "change", "error")
	// 창고·품목: 대소문자만 다른 같은 코드는 뒤 줄이 오류다.
	wplan, _, _ := e.importRun(t, service.NewImportWarehouse(e.importBase()), []domain.ImportRow{
		irow(2, map[string]string{"code": "WH-77", "name": "일곱창고"}), irow(3, map[string]string{"code": "wh-77", "name": "일곱창고 둘"})},
		domain.ImportPlanInput{Target: domain.ImportWarehouse, BatchNo: "IMP-2026-0063"})
	wantVerdicts(t, wplan, "add", "error")
	iplan, _, _ := e.importRun(t, service.NewImportItem(e.importBase()), []domain.ImportRow{
		irow(2, map[string]string{"sku": "AB-1", "name": "가", "unit": "EA"}), irow(3, map[string]string{"sku": "ab-1", "name": "나", "unit": "EA"})},
		domain.ImportPlanInput{Target: domain.ImportItem, BatchNo: "IMP-2026-0064"})
	wantVerdicts(t, iplan, "add", "error")
	// 기초 재고: SKU 대소문자·창고 코드 대소문자가 달라도 같은 (품목, 창고, 로트)면 뒤 줄이 오류다.
	e.importItemsAndStock(t)
	splan, _, _ := e.importRun(t, service.NewImportOpeningStock(e.importBase(), e.journals), []domain.ImportRow{
		irow(2, map[string]string{"sku": "PAPER-A4", "warehouse": "WH-01", "qty": "10", "unit_cost": "100"}),
		irow(3, map[string]string{"sku": "paper-a4", "warehouse": "wh-01", "qty": "10", "unit_cost": "100"})},
		domain.ImportPlanInput{Target: domain.ImportOpeningStock, BaseDate: "2026-11-05", BatchNo: "IMP-2026-0065"})
	wantVerdicts(t, splan, "add", "error")
}

// 큰 파일: 기초 재고 10,000행의 미리보기·반영이 행마다 조회하지 않아 수 초 안에 끝난다(부하에 덜 민감하도록 상한은 넉넉하다).
func TestImportOpeningStockLargeFileIsFast(t *testing.T) {
	if testing.Short() {
		t.Skip("큰 파일 시험")
	}
	e := newEnv(t)
	e.jvForm(t)
	const n = 10_000
	tx, err := e.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < n; i++ {
		if _, err := tx.Exec(`INSERT INTO items (sku, name, unit, safety_stock, track_lot) VALUES (?,?,?,0,0)`, fmt.Sprintf("BIG-%05d", i), "대량 품목", "EA"); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	rows := make([]domain.ImportRow, n)
	for i := range rows {
		rows[i] = irow(i+2, map[string]string{"sku": fmt.Sprintf("BIG-%05d", i), "warehouse": "WH-01", "qty": "3", "unit_cost": "1000"})
	}
	h := service.NewImportOpeningStock(e.importBase(), e.journals)
	in := domain.ImportPlanInput{Target: domain.ImportOpeningStock, BaseDate: "2026-11-05", BatchNo: "IMP-2026-0070"}
	start := time.Now()
	plan, err := h.Plan(as(uFinance), e.db, rows, in)
	if err != nil {
		t.Fatal(err)
	}
	planTook := time.Since(start)
	for _, v := range plan.Verdicts {
		if v.Verdict != domain.ImportVerdictAdd {
			t.Fatalf("모두 추가여야 함: %+v", v)
		}
	}
	ctx := as(uFinance)
	p, _ := contract.PrincipalFrom(ctx)
	start = time.Now()
	var out domain.ImportApplyOutcome
	if err := (contract.SQLTxManager{DB: e.db}).WithTx(ctx, func(tx contract.Tx) error {
		var aerr error
		out, aerr = h.Apply(ctx, tx, p, rows, plan.Verdicts, in)
		return aerr
	}); err != nil {
		t.Fatal(err)
	}
	applyTook := time.Since(start)
	t.Logf("기초 재고 %d행: 미리보기 %v, 반영 %v", n, planTook, applyTook)
	if planTook > 30*time.Second || applyTook > 60*time.Second {
		t.Errorf("너무 느림: 미리보기 %v, 반영 %v (행마다 조회하면 수십~수백 초가 걸렸다)", planTook, applyTook)
	}
	if len(out.JournalNos) != 1 || e.col(t, `SELECT COUNT(*) FROM stock_ledger WHERE ref_type = 'opening_import'`) != fmt.Sprint(n) {
		t.Fatalf("반영 결과: %+v", out)
	}
}

// 마감된 달이 기준일이면 미리보기를 거부하고 감사(import.denied_closed)에 denied 로 남긴다.
func TestImportClosedBaseDateIsAuditedAsDenied(t *testing.T) {
	e := newEnv(t)
	e.closeFixture(t)
	if _, err := e.monthClose.Close(as(uFinance), "2026-10"); err != nil {
		t.Fatal(err)
	}
	e.importItemsAndStock(t)
	rows := []domain.ImportRow{irow(2, map[string]string{"sku": "PAPER-A4", "warehouse": "WH-01", "qty": "1", "unit_cost": "1"})}
	for _, h := range []domain.ImportTargetHandler{service.NewImportOpeningStock(e.importBase(), e.journals), service.NewImportOpeningPayable(e.importBase(), e.journals)} {
		_, err := h.Plan(as(uFinance), e.db, rows, domain.ImportPlanInput{BaseDate: "2026-10-15", BatchNo: "IMP-2026-0071"})
		if err == nil || !strings.Contains(err.Error(), "마감된 월입니다") {
			t.Fatalf("마감 월 기준일: %v", err)
		}
	}
	if !e.rec.Has("import.denied_closed", audit.TargetImport, audit.ResultDenied) {
		t.Error("마감 월 거부 감사(import.denied_closed)가 없음")
	}
}
