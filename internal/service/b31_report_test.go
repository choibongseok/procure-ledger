package service_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/service"
)

func (e *env) ledgerSvcs() (*service.LedgerReportService, *service.PayableService) {
	b := e.journals.Base
	return service.NewLedgerReportService(b), service.NewPayableService(b)
}

// AC-1121: 계정별 원장은 승인된 전표만 세고(임시는 제외 안내), 이월 잔액·정상 방향 부호·누계가 맞다.
func TestAccountLedgerCountsApprovedOnly(t *testing.T) {
	e := newEnv(t)
	e.jvForm(t)
	v := e.vendor(t)
	led, _ := e.ledgerSvcs()
	approved := e.approveJV(t, e.purchaseJV(t, v.ID, 100_000))
	if approved.Status != domain.JournalApproved {
		t.Fatalf("승인 준비: %s", approved.Status)
	}
	e.purchaseJV(t, v.ID, 7_000) // 임시로 남긴다
	l, err := led.AccountLedger(as(uFinance), domain.AccountLedgerFilter{Account: "2110", From: "2026-11-01", To: "2026-11-30"})
	if err != nil {
		t.Fatal(err)
	}
	if l.Opening != 0 || len(l.Rows) != 1 || l.Rows[0].Credit != 100_000 || l.Rows[0].Balance != 100_000 || l.Closing != 100_000 || l.ExcludedCount != 1 {
		t.Fatalf("원장: %+v", l)
	}
	if l.Rows[0].Counter == "" || l.Rows[0].JournalNo != approved.No {
		t.Fatalf("상대 계정·전표 번호: %+v", l.Rows[0])
	}
	inv, _ := led.AccountLedger(as(uFinance), domain.AccountLedgerFilter{Account: "1210", From: "2026-11-01", To: "2026-11-30"})
	if inv.Closing != 100_000 || inv.Rows[0].Debit != 100_000 {
		t.Fatalf("자산 계정은 차변이 +: %+v", inv)
	}
	// 기간 시작이 전표 뒤면 이월 잔액으로 나온다.
	after, _ := led.AccountLedger(as(uFinance), domain.AccountLedgerFilter{Account: "2110", From: "2026-11-11", To: "2026-11-30"})
	if after.Opening != 100_000 || len(after.Rows) != 0 || after.Closing != 100_000 {
		t.Fatalf("이월: %+v", after)
	}
	byVendor, _ := led.AccountLedger(as(uFinance), domain.AccountLedgerFilter{Account: "2110", From: "2026-11-01", To: "2026-11-30", VendorID: v.ID + 99})
	if byVendor.Closing != 0 {
		t.Fatalf("다른 거래처: %+v", byVendor)
	}
	for _, c := range []struct {
		f    domain.AccountLedgerFilter
		want string
	}{{domain.AccountLedgerFilter{}, "계정과목을 고르세요"}, {domain.AccountLedgerFilter{Account: "9999"}, "알 수 없는 계정과목"},
		{domain.AccountLedgerFilter{Account: "2110", From: "2026-11-10", To: "2026-11-01"}, "종료일은 시작일 이후"}} {
		if _, err := led.AccountLedger(as(uFinance), c.f); !errors.Is(err, contract.ErrValidation) || !strings.Contains(err.Error(), c.want) {
			t.Fatalf("%+v: %v", c.f, err)
		}
	}
	if _, err := led.AccountLedger(as(uStaff), domain.AccountLedgerFilter{Account: "2110"}); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("권한: %v", err)
	}
	if _, err := led.AccountLedger(as(uAuditor), domain.AccountLedgerFilter{Account: "2110"}); err != nil {
		t.Fatalf("감사는 조회 가능: %v", err)
	}
}

// AC-1121: 시산표는 차변 합계 = 대변 합계이고 0 잔액 계정은 기본으로 숨는다.
func TestTrialBalanceBalancedAndZeroToggle(t *testing.T) {
	e := newEnv(t)
	e.jvForm(t)
	v := e.vendor(t)
	led, _ := e.ledgerSvcs()
	e.approveJV(t, e.purchaseJV(t, v.ID, 100_000))
	tb, err := led.TrialBalance(as(uFinance), "2026-11-01", "2026-11-30", false)
	if err != nil {
		t.Fatal(err)
	}
	if !tb.Balanced || tb.TotalDebit != 100_000 || tb.TotalCredit != 100_000 || len(tb.Rows) != 2 {
		t.Fatalf("시산표: %+v", tb)
	}
	all, _ := led.TrialBalance(as(uFinance), "2026-11-01", "2026-11-30", true)
	if len(all.Rows) != 21 {
		t.Fatalf("0 잔액 포함 %d줄(기대 21)", len(all.Rows))
	}
	later, _ := led.TrialBalance(as(uFinance), "2026-12-01", "2026-12-31", false)
	if len(later.Rows) != 2 || later.Rows[0].Opening == 0 || later.TotalDebit != 0 || !later.Balanced {
		t.Fatalf("다음 달 기초: %+v", later)
	}
}

// AC-1120: 채무 항목은 승인된 매입 전표에서만 생기고 만기 = 거래일 + 결제 조건, 거래처별 합계가 외상매입금 원장 잔액과 같다. 결제 조건 변경은 새 전표부터다.
func TestPayablesByVendorMatchLedger(t *testing.T) {
	e := newEnv(t)
	e.jvForm(t)
	v := e.vendor(t)
	led, pay := e.ledgerSvcs()
	_ = led
	e.purchaseJV(t, v.ID, 1_000) // 임시: 채무 아님
	if rep, err := pay.ByVendor(as(uFinance), "2026-11-10"); err != nil || len(rep.Rows) != 0 || !rep.Matches {
		t.Fatalf("임시 전표는 채무가 아님: %+v %v", rep, err)
	}
	e.approveJV(t, e.purchaseJV(t, v.ID, 100_000))
	rep, err := pay.ByVendor(as(uFinance), "2026-11-10")
	if err != nil || len(rep.Rows) != 1 || rep.Rows[0].Outstanding != 100_000 || rep.Rows[0].EarliestDue != "2026-12-05" || rep.Rows[0].PaymentDays != 30 || !rep.Matches || rep.Total != rep.LedgerBalance {
		t.Fatalf("채무: %+v %v", rep, err)
	}
	items, err := pay.Items(as(uFinance), v.ID, "2026-12-10")
	if err != nil || len(items) != 1 || items[0].DueDate != "2026-12-05" || items[0].OverdueDays != 5 || items[0].Outstanding != 100_000 {
		t.Fatalf("항목: %+v %v", items, err)
	}
	if rep2, _ := pay.ByVendor(as(uFinance), "2026-12-10"); rep2.Rows[0].OverdueAmount != 100_000 {
		t.Fatalf("만기 경과 금액: %+v", rep2.Rows[0])
	}
	if _, err := pay.SetPaymentDays(as(uAuditor), v.ID, 45); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("감사는 결제 조건을 바꿀 수 없음: %v", err)
	}
	for _, bad := range []int{-1, 366} {
		if _, err := pay.SetPaymentDays(as(uFinance), v.ID, bad); !errors.Is(err, contract.ErrValidation) {
			t.Fatalf("%d일: %v", bad, err)
		}
	}
	if _, err := pay.SetPaymentDays(as(uFinance), v.ID, 0); err != nil {
		t.Fatalf("0일(당일 만기)은 허용: %v", err)
	}
	if _, err := pay.SetPaymentDays(as(uFinance), v.ID, 45); err != nil {
		t.Fatal(err)
	}
	e.approveJV(t, e.purchaseJV(t, v.ID, 50_000))
	items, _ = pay.Items(as(uFinance), v.ID, "2026-11-10")
	if len(items) != 2 || items[0].DueDate != "2026-12-05" || items[1].DueDate != "2026-12-20" {
		t.Fatalf("만기(기존 30일, 새 45일): %+v", items)
	}
	if _, err := pay.SetPaymentDays(as(uFinance), 9999, 30); !errors.Is(err, contract.ErrNotFound) {
		t.Fatalf("없는 거래처: %v", err)
	}
	if !e.rec.Has("vendor.payment_days", audit.TargetVendor, audit.ResultSuccess) {
		t.Error("결제 조건 수정 감사가 없음")
	}
	// 역분개가 승인되면 채무 항목이 취소되고 합계는 계속 원장과 같다.
	first := e.col(t, `SELECT no FROM journals WHERE status = 'approved' ORDER BY id LIMIT 1`)
	rev, err := e.journals.Reverse(as(uFinance), first, "2026-11-10")
	if err != nil {
		t.Fatal(err)
	}
	e.approveJV(t, rev)
	after, _ := pay.ByVendor(as(uFinance), "2026-11-10")
	if after.Total != 50_000 || !after.Matches {
		t.Fatalf("역분개 뒤: %+v", after)
	}
}
