package service_test

import (
	"testing"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract/domain"
)

// B34 후속 시험 3건: 지급 일정표의 휴일 안내, 부재 알림의 휴일 건너뜀, 자동 매입 전표 부가세의 입고일 세율.

// 지급 일정표: 예정일이 휴일(주말·공휴일)이면 다음 영업일을 안내하되 예정일 자체는 바꾸지 않고, 영업일 항목에는 안내가 없다.
func TestPaymentCalendarShowsNextBusinessDayOnHolidays(t *testing.T) {
	e := newEnv(t)
	e.payForm(t)
	v := e.vendor(t)
	e.payable(t, v.ID, 80_000) // 만기 2026-12-05(토)
	cal, err := e.payments.Calendar(as(uAuditor), "2026-12-01", "2026-12-10")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, x := range cal.Entries {
		wd := parseTestDay(t, x.Date).Weekday()
		weekend := wd == time.Saturday || wd == time.Sunday
		switch {
		case x.Date == "2026-12-05":
			found = true
			if x.NextBusinessDay != "2026-12-07" {
				t.Errorf("토요일 만기는 월요일을 안내: %+v", x)
			}
		case !weekend && x.NextBusinessDay != "":
			t.Errorf("영업일 항목에는 안내가 없어야 함: %+v", x)
		}
	}
	if !found {
		t.Fatalf("12-05 만기 항목이 없음: %+v", cal.Entries)
	}
	// 월요일이 공휴일이면 화요일을 안내한다. 예정일(만기)은 그대로 12-05 다.
	if _, err := e.company.AddHolidays(as(uAdmin), []domain.Holiday{{Date: "2026-12-07", Name: "임시공휴일"}}); err != nil {
		t.Fatal(err)
	}
	cal, err = e.payments.Calendar(as(uAuditor), "2026-12-01", "2026-12-10")
	if err != nil {
		t.Fatal(err)
	}
	for _, x := range cal.Entries {
		if x.Date == "2026-12-05" && (x.NextBusinessDay != "2026-12-08" || x.Kind != "due") {
			t.Errorf("공휴일 다음 영업일: %+v", x)
		}
	}
	for _, x := range cal.Entries {
		if x.Date == "2026-12-05" {
			return
		}
	}
	t.Error("만기 데이터가 바뀌었음")
}

func parseTestDay(t *testing.T, d string) time.Time {
	t.Helper()
	tm, err := time.Parse("2006-01-02", d)
	if err != nil {
		t.Fatal(err)
	}
	return tm
}

// 부재 알림: 시작일이 휴일이면 그날 알림을 만들지 않고, 다음 영업일 첫 실행에서 한 번만 만든다.
func TestAbsenceNotificationsSkipHolidays(t *testing.T) {
	e := newEnv(t)
	start := e.now.Add(24 * time.Hour) // 2026-11-11(수) 09:00 UTC
	if _, err := e.delegation.Create(as(uLead2), e.absenceIn(uHead, start, 5, "trip")); err != nil {
		t.Fatal(err)
	}
	if _, err := e.company.AddHolidays(as(uAdmin), []domain.Holiday{{Date: "2026-11-11", Name: "임시공휴일"}}); err != nil {
		t.Fatal(err)
	}
	e.now = start.Add(time.Minute) // 시작 시각 이후, 그러나 휴일
	for i := 0; i < 2; i++ {
		if err := e.delegation.Tick(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	if n := len(e.notes(t, uHead, "absence_started")); n != 0 {
		t.Fatalf("공휴일에 부재 시작 알림이 만들어짐: %d", n)
	}
	e.now = start.Add(24*time.Hour + time.Minute) // 11-12(목): 다음 영업일
	for i := 0; i < 2; i++ {
		if err := e.delegation.Tick(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	if n := len(e.notes(t, uHead, "absence_started")); n != 1 {
		t.Fatalf("다음 영업일에 한 번만 만들어져야 함: %d", n)
	}
}

// 자동 매입 전표의 부가세는 입고일(거래일)의 세율이다: 효력일 전 입고는 확정이 효력일 뒤여도 옛 세율, 효력일 뒤 입고는 새 세율.
func TestAutoPurchaseJournalVATFollowsReceiptDayRate(t *testing.T) {
	e := newEnv(t) // 2026-11-10
	if _, err := e.company.AddVATRate(as(uAdmin), "2026-11-12", 20); err != nil {
		t.Fatal(err)
	}
	oldPO, oldRC := e.receivedPO(t, 12_345, 1) // 2026-11-10 입고(10%)
	e.now = at(2026, 11, 20, 1)
	newPO, newRC := e.receivedPO(t, 12_345, 1) // 2026-11-20 입고(20%)
	vatOf := func(po domain.PurchaseOrder, rc int64) (int64, int64) {
		t.Helper()
		total := int64(12_345)
		out, err := e.hook(t, uFinance, matchVerdict(e.verdictFor(t, po, rc), po, rc, &total))
		if err != nil || !out.Created {
			t.Fatalf("전표 생성: %+v %v", out, err)
		}
		j, err := e.journals.Get(as(uFinance), out.JournalNo)
		if err != nil {
			t.Fatal(err)
		}
		var vat, payable int64
		for _, l := range j.Lines {
			switch l.AccountCode {
			case "1310":
				vat = l.Amount
			case "2110":
				payable = l.Amount
			}
		}
		return vat, payable
	}
	// 확정(훅 호출)은 둘 다 효력일 뒤(11-20)에 일어난다. 그래도 옛 입고는 10% 다.
	if vat, pay := vatOf(oldPO, oldRC); vat != 1_234 || pay != 13_579 {
		t.Errorf("효력일 전 입고: 부가세 %d, 외상매입금 %d (기대 1,234 / 13,579)", vat, pay)
	}
	if vat, pay := vatOf(newPO, newRC); vat != 2_469 || pay != 14_814 {
		t.Errorf("효력일 뒤 입고: 부가세 %d, 외상매입금 %d (기대 2,469 / 14,814)", vat, pay)
	}
}
