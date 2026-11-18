package service_test

import (
	"strings"
	"testing"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract/domain"
)

// B34 판정 지적 보완 시험: 발주서 세율 표시, 구매 실적 기본 기간, 독촉 휴일 안내, 지급일 휴일 경고, 같은 시작월 재저장.

func TestPOPrintCarriesVATRateForLabel(t *testing.T) {
	e := newEnv(t)
	v := e.poVendor(t, "발주상사", "555-55-55555")
	old := e.orderFor(t, v, "", 12_345, 1)
	if _, err := e.company.AddVATRate(as(uAdmin), "2026-11-12", 20); err != nil {
		t.Fatal(err)
	}
	e.now = at(2026, 11, 20, 1)
	fresh := e.orderFor(t, v, "", 12_345, 1)
	for _, c := range []struct {
		po   domain.PurchaseOrder
		rate int
		vat  int64
	}{{old, 10, 1_234}, {fresh, 20, 2_469}} {
		p, err := e.poPrint.Print(as(uStaff), c.po.PRID)
		if err != nil || p.VATRate != c.rate || p.VAT != c.vat {
			t.Errorf("세율 %d%%: %+v %v", c.rate, p, err)
		}
	}
}

func TestPurchaseReportDefaultPeriodFollowsFiscalYear(t *testing.T) {
	e := newEnv(t) // 2026-11-10
	rep, err := e.reports.Purchase(as(uFinance), domain.PurchaseReportQuery{})
	if err != nil || rep.From != "2026-01" || rep.To != "2026-11" {
		t.Fatalf("시작월 1: %q~%q %v", rep.From, rep.To, err)
	}
	ci, err := e.company.Update(as(uAdmin), domain.CompanyInput{Name: "에이엑스"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.company.SetFiscalStart(as(uAdmin), 4, ci.Version); err != nil {
		t.Fatal(err)
	}
	rep, err = e.reports.Purchase(as(uFinance), domain.PurchaseReportQuery{})
	if err != nil || rep.From != "2026-04" || rep.To != "2026-11" {
		t.Fatalf("시작월 4: %q~%q %v", rep.From, rep.To, err)
	}
	rep, err = e.reports.Purchase(as(uFinance), domain.PurchaseReportQuery{FromMonth: "2026-09", ToMonth: "2026-10"})
	if err != nil || rep.From != "2026-09" || rep.To != "2026-10" {
		t.Fatalf("명시한 기간은 그대로: %q~%q %v", rep.From, rep.To, err)
	}
}

// 독촉: 휴일에는 안내(Status.Holiday)만 보이고 서버는 독촉을 받는다.
func TestNudgeOnHolidayShowsNoticeButIsAccepted(t *testing.T) {
	e := newEnv(t)
	ns := e.nudgeSvc()
	base := time.Date(2026, 11, 10, 14, 0, 0, 0, time.UTC) // KST 23:00
	e.now = base
	pr := e.submitted(t, uStaff, 500_000)
	if _, err := e.company.AddHolidays(as(uAdmin), []domain.Holiday{{Date: "2026-11-12", Name: "임시공휴일"}}); err != nil {
		t.Fatal(err)
	}
	e.now = base.Add(25*time.Hour + 30*time.Minute) // KST 11-12 00:30(공휴일)
	st, err := ns.Status(as(uStaff), pr.ID)
	if err != nil || !st.Delayed || !st.Holiday {
		t.Fatalf("휴일 안내: %+v %v", st, err)
	}
	if _, err := ns.Nudge(as(uStaff), pr.ID); err != nil {
		t.Fatalf("휴일에도 독촉은 받는다: %v", err)
	}
	e.now = base.Add(24*time.Hour + 30*time.Minute) // 11-11(영업일)이던 날은 휴일이 아니다
	if st, err := ns.Status(as(uStaff), pr.ID); err != nil || st.Holiday {
		t.Fatalf("영업일에는 안내가 없음: %+v %v", st, err)
	}
}

// 지급 처리일이 휴일이면 경고만 한다(차단하지 않는다).
func TestSettleOnHolidayWarnsButSucceeds(t *testing.T) {
	e := newEnv(t)
	e.payForm(t)
	v := e.vendor(t)
	bank := e.bank(t, "주거래")
	a := e.payable(t, v.ID, 80_000)
	p := e.requested(t, e.payInput(v, bank.ID, domain.PaymentItemInput{PayableID: a, Amount: 80_000}))
	e.approvePay(t, p)
	// 2026-11-14(토)로 지급일을 정해 처리한다(오늘 이전, 승인일 이후).
	e.now = at(2026, 11, 15, 1)
	done, err := e.payments.Settle(as(uFinance2), p.No, domain.SettleInput{PaidDate: "2026-11-14", TransferRef: "TR-H1"})
	if err != nil {
		t.Fatalf("휴일 지급일도 처리는 된다: %v", err)
	}
	warned := false
	for _, w := range done.Warnings {
		warned = warned || strings.Contains(w, "휴일")
	}
	if !warned || done.PaidDate != "2026-11-14" {
		t.Fatalf("휴일 경고가 없음: %+v", done)
	}
}

// 같은 시작월을 다시 저장하면 바뀐 것이 없으므로 version 도 감사도 늘지 않는다.
func TestSetFiscalStartSameValueIsNoop(t *testing.T) {
	e := newEnv(t)
	ci, err := e.company.Update(as(uAdmin), domain.CompanyInput{Name: "에이엑스"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	before := e.col(t, `SELECT version FROM company_settings`)
	got, err := e.company.SetFiscalStart(as(uAdmin), 1, ci.Version)
	if err != nil || got.Version != ci.Version || e.col(t, `SELECT version FROM company_settings`) != before {
		t.Fatalf("같은 값 재저장: %+v %v", got, err)
	}
	for _, r := range e.rec.Records() {
		if r.Action == "company.fiscal_set" {
			t.Fatalf("변경이 없으면 감사를 남기지 않아야 함: %+v", r)
		}
	}
}
