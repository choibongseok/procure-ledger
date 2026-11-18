package service_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/service"
)

// B35 기초 재고·미지급 잔액: 원장 기초 입고 + 기초 전표(임시), 승인되면 계정별 원장·채무 항목, 반영 한 번 뒤에는 바뀌지 않는다.

func (e *env) importItemsAndStock(t *testing.T) {
	t.Helper()
	items := service.NewImportItem(e.importBase())
	if _, _, err := e.importRun(t, items, []domain.ImportRow{
		irow(2, map[string]string{"sku": "PAPER-A4", "name": "복사용지 A4", "unit": "박스", "safety_stock": "500"}),
		irow(3, map[string]string{"sku": "MILK-1L", "name": "우유", "unit": "EA", "lot_tracked": "true"}),
	}, domain.ImportPlanInput{Target: domain.ImportItem, BatchNo: "IMP-2026-0010"}); err != nil {
		t.Fatal(err)
	}
}

func TestImportOpeningStockLedgerJournalAndImmutability(t *testing.T) {
	e := newEnv(t) // 2026-11-10
	e.jvForm(t)
	e.importItemsAndStock(t)
	h := service.NewImportOpeningStock(e.importBase(), e.journals)
	in := domain.ImportPlanInput{Target: domain.ImportOpeningStock, BaseDate: "2026-11-05", BatchNo: "IMP-2026-0011"}
	rows := []domain.ImportRow{
		irow(2, map[string]string{"sku": "PAPER-A4", "warehouse": "WH-01", "qty": "100", "unit_cost": "2200"}),
		irow(3, map[string]string{"sku": "MILK-1L", "warehouse": "wh-01", "lot": "L1", "expiry": "2027-01-31", "qty": "10", "unit_cost": "0"}),
		irow(4, map[string]string{"sku": "NOPE", "warehouse": "WH-01", "qty": "1", "unit_cost": "1"}),
		irow(5, map[string]string{"sku": "PAPER-A4", "warehouse": "WH-99", "qty": "1", "unit_cost": "2200"}),
		irow(6, map[string]string{"sku": "MILK-1L", "warehouse": "WH-01", "qty": "1", "unit_cost": "1"}),
	}
	plan, out, err := e.importRun(t, h, rows, in)
	if err != nil {
		t.Fatal(err)
	}
	wantVerdicts(t, plan, "add", "add", "error", "error", "error")
	if len(out.JournalNos) != 0 || e.col(t, `SELECT COUNT(*) FROM stock_ledger WHERE ref_type = 'opening_import'`) != "0" {
		t.Fatal("오류 행이 있으면 아무것도 반영되지 않아야 함")
	}
	if plan.Verdicts[1].Warn == "" || !strings.Contains(plan.Verdicts[1].Warn, "0원") {
		t.Errorf("단가 0 경고: %+v", plan.Verdicts[1])
	}
	good := rows[:2]
	plan, out, err = e.importRun(t, h, good, in)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.JournalNos) != 1 || len(plan.Summary) != 1 || !strings.Contains(plan.Summary[0], "품목 2종") || !strings.Contains(plan.Summary[0], "220,000") {
		t.Fatalf("검산 문장·전표: %+v %+v", plan.Summary, out)
	}
	if e.col(t, `SELECT COUNT(*) FROM stock_ledger WHERE ref_type = 'opening_import' AND book_date = '2026-11-05' AND reason = 'receipt'`) != "2" {
		t.Fatal("기초 입고 행(장부일 = 기준일)이 없음")
	}
	if e.col(t, `SELECT SUM(v.amount) FROM stock_valuation v JOIN stock_ledger l ON l.id = v.ledger_id WHERE l.ref_type = 'opening_import'`) != "220000" {
		t.Fatal("평가 줄 금액 합이 다름")
	}
	st, err := e.inventory.GetStock(as(uFinance), itemIDOf(t, e, "PAPER-A4"))
	if err != nil || st.OnHand != 100 {
		t.Fatalf("재고는 원장 합: %+v %v", st, err)
	}
	j, err := e.journals.Get(as(uFinance), out.JournalNos[0])
	if err != nil || j.Kind != domain.JournalOpening || j.Status != domain.JournalDraft || j.PostingDate != "2026-11-05" || len(j.Lines) != 2 ||
		j.Lines[0].AccountCode != "1210" || j.Lines[0].Side != "D" || j.Lines[0].Amount != 220_000 || j.Lines[1].AccountCode != "3210" || j.Lines[1].Side != "C" {
		t.Fatalf("기초 전표: %+v %v", j, err)
	}
	for _, a := range []string{"stock.opening_import", "journal.opening_import"} {
		if !e.rec.Has(a, audit.TargetItem, audit.ResultSuccess) && !e.rec.Has(a, audit.TargetJournal, audit.ResultSuccess) {
			t.Errorf("%s 감사가 없음", a)
		}
	}
	// 승인 전에는 승인된 전표만 세는 시산표에 없고, 승인되면 재고자산과 기초 이월이 들어간다.
	led, _ := e.ledgerSvcs()
	tb, _ := led.TrialBalance(as(uFinance), "2026-11-01", "2026-11-30", false)
	if len(tb.Rows) != 0 {
		t.Fatalf("승인 전 기초 전표가 시산표에 들어감: %+v", tb.Rows)
	}
	e.approveJV(t, j)
	tb, _ = led.TrialBalance(as(uFinance), "2026-11-01", "2026-11-30", false)
	if !tb.Balanced || tb.TotalDebit != 220_000 || len(tb.Rows) != 2 {
		t.Fatalf("승인 뒤 시산표: %+v", tb)
	}
	// 같은 파일은 동일, 값이 다르면 오류(변경 불가), 파일에 없는 키는 건드리지 않는다.
	again, _, _ := e.importRun(t, h, good, in)
	wantVerdicts(t, again, "same", "same")
	diff := []domain.ImportRow{irow(2, map[string]string{"sku": "PAPER-A4", "warehouse": "WH-01", "qty": "120", "unit_cost": "2200"})}
	plan, _, _ = e.importRun(t, h, diff, in)
	wantVerdicts(t, plan, "error")
	if !strings.Contains(plan.Verdicts[0].Reason, "정정은") {
		t.Errorf("정정 경로 안내: %+v", plan.Verdicts[0])
	}
	// 마감된 달이 기준일이면 반영할 수 없다.
	if _, err := h.Plan(as(uFinance), e.db, good, domain.ImportPlanInput{BaseDate: "2026-11-xx"}); !isErr(err, contract.ErrValidation) {
		t.Errorf("기준일 형식: %v", err)
	}
}

func TestImportOpeningStockRejectsClosedMonth(t *testing.T) {
	e := newEnv(t)
	e.closeFixture(t) // 시계 12월 2일, 10월 입출고
	if _, err := e.monthClose.Close(as(uFinance), "2026-10"); err != nil {
		t.Fatal(err)
	}
	e.importItemsAndStock(t)
	h := service.NewImportOpeningStock(e.importBase(), e.journals)
	rows := []domain.ImportRow{irow(2, map[string]string{"sku": "PAPER-A4", "warehouse": "WH-01", "qty": "1", "unit_cost": "1"})}
	_, err := h.Plan(as(uFinance), e.db, rows, domain.ImportPlanInput{BaseDate: "2026-10-15", BatchNo: "IMP-2026-0012"})
	if err == nil || !strings.Contains(err.Error(), "마감된 월입니다") {
		t.Fatalf("마감된 달 기준일: %v", err)
	}
	if _, err := h.Plan(as(uFinance), e.db, rows, domain.ImportPlanInput{BaseDate: "2026-11-15", BatchNo: "IMP-2026-0012"}); err != nil {
		t.Fatalf("열린 달은 가능: %v", err)
	}
}

func itemIDOf(t *testing.T, e *env, sku string) int64 {
	t.Helper()
	var id int64
	if err := e.db.QueryRow(`SELECT id FROM items WHERE sku = ?`, sku).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestImportOpeningPayablesJournalsApprovalAndPayables(t *testing.T) {
	e := newEnv(t)
	e.jvForm(t)
	vh := service.NewImportVendor(e.importBase())
	if _, _, err := e.importRun(t, vh, []domain.ImportRow{
		irow(2, map[string]string{"name": "한빛상사", "business_no": "1234567890", "payment_days": "20"}),
		irow(3, map[string]string{"name": "이름만상사"}),
	}, domain.ImportPlanInput{Target: domain.ImportVendor, BatchNo: "IMP-2026-0020"}); err != nil {
		t.Fatal(err)
	}
	h := service.NewImportOpeningPayable(e.importBase(), e.journals)
	in := domain.ImportPlanInput{Target: domain.ImportOpeningPayable, BaseDate: "2026-11-05", BatchNo: "IMP-2026-0021"}
	rows := []domain.ImportRow{
		irow(2, map[string]string{"vendor": "123-45-67890", "doc_no": "TI-0001", "trade_date": "2026-10-21", "due_date": "2026-11-20", "amount": "3500000"}),
		irow(3, map[string]string{"vendor": "한빛상사", "doc_no": "TI-0002", "trade_date": "2026-10-01", "amount": "100000"}), // 만기 = 10-01 + 20일 = 10-21(기준일 이전)
		irow(4, map[string]string{"vendor": "이름만상사", "doc_no": "X-1", "trade_date": "2026-11-01", "amount": "5000"}),
		irow(5, map[string]string{"vendor": "없는상사", "doc_no": "Z-1", "trade_date": "2026-11-01", "amount": "1"}),
	}
	plan, _, _ := e.importRun(t, h, rows, in)
	wantVerdicts(t, plan, "add", "add", "add", "error")
	if plan.Verdicts[1].Warn == "" {
		t.Error("이미 만기가 지난 항목 경고가 없음")
	}
	if strings.Contains(plan.Verdicts[0].Key, "1234567890") {
		t.Errorf("키에 사업자번호 원문: %q", plan.Verdicts[0].Key)
	}
	good := rows[:3]
	plan, out, err := e.importRun(t, h, good, in)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.JournalNos) != 1 || len(plan.Summary) != 1 || !strings.Contains(plan.Summary[0], "거래처 2곳·항목 3건·잔액 합 3,605,000원") {
		t.Fatalf("검산·전표: %+v %+v", plan.Summary, out)
	}
	j, err := e.journals.Get(as(uFinance), out.JournalNos[0])
	if err != nil || j.Kind != domain.JournalOpening || j.Status != domain.JournalDraft || len(j.Lines) != 4 || j.Lines[0].Side != "D" || j.Lines[0].AccountCode != "3210" || j.Lines[0].Amount != 3_605_000 {
		t.Fatalf("기초 전표: %+v %v", j, err)
	}
	if e.col(t, `SELECT COUNT(*) FROM payables`) != "0" {
		t.Fatal("승인 전에는 채무 항목이 없어야 함")
	}
	e.approveJV(t, j)
	if e.col(t, `SELECT COUNT(*) FROM payables WHERE canceled = 0`) != "3" {
		t.Fatal("승인되면 항목마다 채무 항목이 생겨야 함")
	}
	if e.col(t, `SELECT trade_date || '/' || due_date FROM payables WHERE amount = 3500000`) != "2026-10-21/2026-11-20" {
		t.Error("발생일·만기일은 파일 값이어야 함")
	}
	if e.col(t, `SELECT due_date FROM payables WHERE amount = 100000`) != "2026-10-21" {
		t.Error("만기를 비우면 발생일 + 거래처 결제 조건일")
	}
	if e.col(t, `SELECT due_date FROM payables WHERE amount = 5000`) != "2026-12-01" {
		t.Error("결제 조건이 없으면 기본 30일")
	}
	// 거래처별 합계 = 계정별 원장 외상매입금(B31 검산)
	_, pay := e.ledgerSvcs()
	rep, err := pay.ByVendor(as(uFinance), "2026-11-30")
	if err != nil || !rep.Matches || rep.Total != 3_605_000 {
		t.Fatalf("채무 검산: %+v %v", rep, err)
	}
	// 반영 한 번 뒤: 같은 파일은 동일, 잔액이 다르면 오류.
	again, _, _ := e.importRun(t, h, good, in)
	wantVerdicts(t, again, "same", "same", "same")
	diff := []domain.ImportRow{irow(2, map[string]string{"vendor": "123-45-67890", "doc_no": "TI-0001", "trade_date": "2026-10-21", "amount": "4000000"})}
	plan, _, _ = e.importRun(t, h, diff, in)
	wantVerdicts(t, plan, "error")
}

func TestImportOpeningPayablesSplitJournalsOver200Lines(t *testing.T) {
	e := newEnv(t)
	e.jvForm(t)
	if _, _, err := e.importRun(t, service.NewImportVendor(e.importBase()), []domain.ImportRow{irow(2, map[string]string{"name": "한빛상사", "business_no": "1234567890"})},
		domain.ImportPlanInput{Target: domain.ImportVendor, BatchNo: "IMP-2026-0030"}); err != nil {
		t.Fatal(err)
	}
	h := service.NewImportOpeningPayable(e.importBase(), e.journals)
	var rows []domain.ImportRow
	for i := 0; i < 250; i++ {
		rows = append(rows, irow(i+2, map[string]string{"vendor": "1234567890", "doc_no": fmt.Sprintf("D-%03d", i), "trade_date": "2026-10-20", "due_date": "2026-12-20", "amount": "1000"}))
	}
	in := domain.ImportPlanInput{Target: domain.ImportOpeningPayable, BaseDate: "2026-11-05", BatchNo: "IMP-2026-0031"}
	_, out, err := e.importRun(t, h, rows, in)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.JournalNos) != 2 {
		t.Fatalf("200줄 상한으로 전표가 나뉘어야 함: %v", out.JournalNos)
	}
	var total int64
	for _, no := range out.JournalNos {
		j, err := e.journals.Get(as(uFinance), no)
		if err != nil || len(j.Lines) > 200 {
			t.Fatalf("전표 줄 수: %d %v", len(j.Lines), err)
		}
		var d, c int64
		for _, l := range j.Lines {
			if l.Side == "D" {
				d += l.Amount
			} else {
				c += l.Amount
			}
		}
		if d != c {
			t.Fatalf("어떤 전표도 차대가 맞아야 함: %d %d", d, c)
		}
		total += d
	}
	if total != 250_000 {
		t.Fatalf("합계는 같아야 함: %d", total)
	}
}
