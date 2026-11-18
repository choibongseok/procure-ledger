package service_test

import (
	"testing"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
)

// B34 단계 2: 부가세율 계산식은 r=10 에서 이관 전 값과 같고, 효력일 이력은 거래일 기준으로 적용되며 이미 만든 것은 바뀌지 않는다.
func TestVATFormulasMatchLegacyAtTenPercent(t *testing.T) {
	for _, s := range []int64{0, 9, 10, 19, 100, 12_345, 1_999_999, 123_456_789} {
		if got, want := domain.VATFromSupply(s, 10), s/10; got != want {
			t.Errorf("공급가액 %d: %d, 이관 전 %d", s, got, want)
		}
		if got, want := domain.VATFromTotal(s, 10), s/11; got != want {
			t.Errorf("합계 %d 역산: %d, 이관 전 %d", s, got, want)
		}
	}
	for _, c := range []struct {
		supply, total int64
		rate          int
		fromSupply    int64
		fromTotal     int64
	}{{10_000, 12_000, 20, 2_000, 2_000}, {12_345, 13_579, 0, 0, 0}, {999, 1_000, 5, 49, 47}} {
		if got := domain.VATFromSupply(c.supply, c.rate); got != c.fromSupply {
			t.Errorf("공급가액 %d·%d%%: %d, 기대 %d", c.supply, c.rate, got, c.fromSupply)
		}
		if got := domain.VATFromTotal(c.total, c.rate); got != c.fromTotal {
			t.Errorf("합계 %d·%d%% 역산: %d, 기대 %d", c.total, c.rate, got, c.fromTotal)
		}
	}
}

func TestVATRateEffectiveDatesApplyByTransactionDay(t *testing.T) {
	e := newEnv(t) // 시계: 2026-11-10
	cfg := domain.CompanyInput{Name: "에이엑스"}
	if _, err := e.company.Update(as(uAdmin), cfg, 0); err != nil {
		t.Fatal(err)
	}
	// 권한: 등록·취소는 admin 만, 조회는 admin·재무·감사.
	for _, who := range []int64{uStaff, uLead, uFinance, uAuditor} {
		if _, err := e.company.AddVATRate(as(who), "2026-11-12", 20); !isErr(err, contract.ErrForbidden) {
			t.Errorf("%d 세율 등록: %v", who, err)
		}
	}
	for _, who := range []int64{uStaff, uLead} {
		if _, err := e.company.VATRates(as(who)); !isErr(err, contract.ErrForbidden) {
			t.Errorf("%d 세율 조회: %v", who, err)
		}
	}
	for _, who := range []int64{uAdmin, uFinance, uAuditor} {
		if rows, err := e.company.VATRates(as(who)); err != nil || len(rows) != 1 || rows[0].Percent != 10 || rows[0].EffectiveFrom != "0001-01-01" {
			t.Errorf("시드 세율(%d): %+v %v", who, rows, err)
		}
	}
	// 검증: 과거·형식·범위·중복.
	for name, c := range map[string]struct {
		day string
		pct int
	}{"과거": {"2026-11-09", 20}, "형식": {"2026/11/12", 20}, "범위": {"2026-11-12", 101}, "음수": {"2026-11-12", -1}} {
		if _, err := e.company.AddVATRate(as(uAdmin), c.day, c.pct); !isErr(err, contract.ErrValidation) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// 효력일 이전에 만든 발주는 효력일이 지나도 10%, 효력일 뒤에 만든 발주는 20%.
	v := e.poVendor(t, "발주상사", "555-55-55555")
	old := e.orderFor(t, v, "", 12_345, 1) // 2026-11-10 발주
	r20, err := e.company.AddVATRate(as(uAdmin), "2026-11-12", 20)
	if err != nil || !r20.Pending {
		t.Fatalf("세율 등록: %+v %v", r20, err)
	}
	if _, err := e.company.AddVATRate(as(uAdmin), "2026-11-12", 30); !isErr(err, contract.ErrValidation) {
		t.Errorf("같은 효력일 중복: %v", err)
	}
	e.now = at(2026, 11, 20, 1)
	if p, err := e.poPrint.Print(as(uStaff), old.PRID); err != nil || p.VAT != 1_234 {
		t.Fatalf("효력일 전에 발주한 건은 10%% 그대로여야 함: %+v %v", p, err)
	}
	fresh := e.orderFor(t, v, "", 12_345, 1)
	if p, err := e.poPrint.Print(as(uStaff), fresh.PRID); err != nil || p.VAT != 2_469 || p.Total != 14_814 {
		t.Fatalf("효력일 뒤 발주는 20%%(12,345×0.2=2,469): %+v %v", p, err)
	}
	// 효력이 시작된 세율은 취소할 수 없고, 아직 시작되지 않은 세율은 취소할 수 있다.
	if err := e.company.CancelVATRate(as(uAdmin), r20.ID); !isErr(err, contract.ErrInvalidState) {
		t.Errorf("시작된 세율 취소: %v", err)
	}
	r30, err := e.company.AddVATRate(as(uAdmin), "2026-12-01", 30)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.company.CancelVATRate(as(uAdmin), r30.ID); err != nil {
		t.Fatalf("미래 세율 취소: %v", err)
	}
	if rows, _ := e.company.VATRates(as(uAdmin)); len(rows) != 3 || !rows[0].Canceled {
		t.Errorf("취소 표시: %+v", rows)
	}
	if _, err := e.db.Exec(`DELETE FROM vat_rates`); err == nil {
		t.Error("세율 삭제는 막혀야 함")
	}
	if _, err := e.db.Exec(`UPDATE vat_rates SET rate_percent = 99 WHERE id = ?`, r20.ID); err == nil {
		t.Error("세율 수정은 막혀야 함")
	}
	for _, a := range []string{"company.vat_rate_add", "company.vat_rate_cancel"} {
		if !e.rec.Has(a, audit.TargetSetting, audit.ResultSuccess) {
			t.Errorf("%s 감사가 없음", a)
		}
	}
}

// 카드 소명의 부가세는 카드 사용일의 세율로 역산한다. 이미 확정한 소명은 세율이 바뀌어도 그대로다.
func TestCardExplanationVATFollowsUsageDayRate(t *testing.T) {
	e := newEnv(t)
	e.card(t)
	e.importCards(t, cardRow("2026-11-08 12:00:00", "옛가맹점", 12_500, "OLD", "승인"))
	if _, err := e.company.Update(as(uAdmin), domain.CompanyInput{Name: "에이엑스"}, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := e.company.AddVATRate(as(uAdmin), "2026-11-12", 20); err != nil {
		t.Fatal(err)
	}
	e.now = at(2026, 11, 30, 1)
	e.importCards(t, cardRow("2026-11-15 12:00:00", "새가맹점", 12_500, "NEW", "승인"))
	oldTxn := e.explainOK(t, e.txnByApproval(t, "OLD", "approve"), "5210", true)
	newTxn := e.explainOK(t, e.txnByApproval(t, "NEW", "approve"), "5210", true)
	if oldTxn.Explanation.VAT != 1_136 || oldTxn.Explanation.Supply != 11_364 {
		t.Errorf("효력일 전 사용 내역은 10%%(12,500→1,136): %+v", oldTxn.Explanation)
	}
	if newTxn.Explanation.VAT != 2_083 || newTxn.Explanation.Supply != 10_417 { // 12,500×20/120 = 2,083.33
		t.Errorf("효력일 뒤 사용 내역은 20%% 역산(2,083): %+v", newTxn.Explanation)
	}
}

// 회계연도: 시작월을 정하면 시산표 기본 기간과 수익·비용 이월이 따르고, 데이터와 차대 검증은 그대로이며, 마감된 월이 있으면 바꿀 수 없다.
func TestFiscalStartMonthDrivesTrialBalanceAndIsGuarded(t *testing.T) {
	e := newEnv(t)
	e.jvForm(t)
	v := e.vendor(t)
	led, _ := e.ledgerSvcs()
	exp := func(day string, amount int64) {
		t.Helper()
		j, err := e.journals.Create(as(uFinance), domain.JournalInput{PostingDate: day, TradeDate: day, Memo: "비용 시험", Lines: []domain.JournalLineInput{
			{AccountCode: "5210", Side: "D", Amount: amount}, {AccountCode: "2110", Side: "C", Amount: amount, VendorID: v.ID}}})
		if err != nil {
			t.Fatal(err)
		}
		e.approveJV(t, j)
	}
	exp("2026-02-10", 30_000)
	exp("2026-11-10", 12_000)
	if _, err := e.company.SetFiscalStart(as(uAdmin), 4, 0); !isErr(err, contract.ErrValidation) {
		t.Fatalf("회사 정보 저장 전에는 시작월을 정할 수 없음: %v", err)
	}
	ci, err := e.company.Update(as(uAdmin), domain.CompanyInput{Name: "에이엑스"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	// 시작월 1(현행): 기본 기간은 올해 1월 1일부터, 비용 이월은 올해 누계.
	tb, err := led.TrialBalance(as(uFinance), "", "", false)
	if err != nil || tb.From != "2026-01-01" || tb.To != "2026-11-10" || !tb.Balanced {
		t.Fatalf("시작월 1 기본 기간: %+v %v", tb, err)
	}
	if got, _ := led.TrialBalance(as(uFinance), "2026-11-01", "2026-11-30", false); rowOpening(got, "5210") != 30_000 {
		t.Fatalf("시작월 1 이월은 현행과 같아야 함: %+v", got)
	}
	// 시작월 4: 기본 기간은 4월 1일부터, 2월 비용은 지난 회계연도라 이월에서 빠지고 부채 이월은 그대로다.
	if _, err := e.company.SetFiscalStart(as(uStaff), 4, ci.Version); !isErr(err, contract.ErrForbidden) {
		t.Fatalf("직원의 시작월 변경: %v", err)
	}
	for _, bad := range []int{0, 13} {
		if _, err := e.company.SetFiscalStart(as(uAdmin), bad, ci.Version); !isErr(err, contract.ErrValidation) {
			t.Errorf("시작월 %d: %v", bad, err)
		}
	}
	if _, err := e.company.SetFiscalStart(as(uAdmin), 4, ci.Version); err != nil {
		t.Fatal(err)
	}
	fi, err := e.company.Fiscal(as(uAuditor))
	if err != nil || fi.StartMonth != 4 || fi.ThisFrom != "2026-04-01" || fi.ThisTo != "2027-03-31" || fi.Name != 2027 || fi.LastFrom != "2025-04-01" || fi.LastTo != "2026-03-31" {
		t.Fatalf("회계연도 범위: %+v %v", fi, err)
	}
	tb, err = led.TrialBalance(as(uFinance), "", "", false)
	if err != nil || tb.From != "2026-04-01" || !tb.Balanced || rowOpening(tb, "5210") != 0 || tb.TotalDebit != 12_000 {
		t.Fatalf("시작월 4 기본 기간·이월: %+v %v", tb, err)
	}
	if got, _ := led.TrialBalance(as(uFinance), "2026-11-01", "2026-11-30", false); rowOpening(got, "5210") != 0 || rowOpening(got, "2110") != 30_000 || !got.Balanced {
		t.Fatalf("지난 회계연도 비용은 이월에서 빠지고 부채는 그대로: %+v", got)
	}
	if l, _ := led.AccountLedger(as(uFinance), domain.AccountLedgerFilter{Account: "5210", From: "2026-11-01", To: "2026-11-30"}); l.Opening != 0 || l.Closing != 12_000 {
		t.Fatalf("계정별 원장 비용 이월: %+v", l)
	}
	if l, _ := led.AccountLedger(as(uFinance), domain.AccountLedgerFilter{Account: "2110", From: "2026-11-11", To: "2026-11-30"}); l.Opening != 42_000 {
		t.Fatalf("부채 이월은 전체 누계: %+v", l)
	}
	// 마감된 월이 있으면 시작월을 바꿀 수 없다(같은 값 유지는 가능).
	e2 := newEnv(t)
	e2.closeFixture(t)
	c2, err := e2.company.Update(as(uAdmin), domain.CompanyInput{Name: "에이엑스"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e2.monthClose.Close(as(uFinance), "2026-10"); err != nil {
		t.Fatal(err)
	}
	if _, err := e2.company.SetFiscalStart(as(uAdmin), 4, c2.Version); !isErr(err, contract.ErrInvalidState) {
		t.Fatalf("마감된 월이 있으면 거부: %v", err)
	}
	if !e.rec.Has("company.fiscal_set", audit.TargetSetting, audit.ResultSuccess) {
		t.Error("시작월 변경 감사가 없음")
	}
}

func rowOpening(tb domain.TrialBalance, code string) int64 {
	for _, r := range tb.Rows {
		if r.Code == code {
			return r.Opening
		}
	}
	return -1
}
