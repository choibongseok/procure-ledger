package service_test

import (
	"strings"
	"testing"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/service"
)

// B34 단계 3: 공휴일 등록·삭제 규칙, 소명 기한 연장, 휴일 알림 건너뜀(다음 영업일에 합침).
func TestHolidayRegistrationRules(t *testing.T) {
	e := newEnv(t) // 시계 2026-11-10(KST 18시)
	for _, who := range []int64{uStaff, uLead, uFinance, uAuditor} {
		if _, err := e.company.AddHolidays(as(who), []domain.Holiday{{Date: "2026-12-25", Name: "성탄절"}}); !isErr(err, contract.ErrForbidden) {
			t.Errorf("%d 공휴일 등록: %v", who, err)
		}
	}
	for _, who := range []int64{uStaff, uLead} {
		if _, err := e.company.Holidays(as(who), 2026); !isErr(err, contract.ErrForbidden) {
			t.Errorf("%d 공휴일 조회: %v", who, err)
		}
	}
	for _, bad := range [][]domain.Holiday{{}, {{Date: "2026-13-40", Name: "x"}}, {{Date: "2026-12-25", Name: " "}}, {{Date: "2026-12-25", Name: strings.Repeat("가", 51)}}} {
		if _, err := e.company.AddHolidays(as(uAdmin), bad); !isErr(err, contract.ErrValidation) {
			t.Errorf("잘못된 입력 %+v: %v", bad, err)
		}
	}
	n, err := e.company.AddHolidays(as(uAdmin), []domain.Holiday{{Date: "2026-12-25", Name: "성탄절"}, {Date: "2026-11-05", Name: "지난 휴일"}})
	if err != nil || n != 2 {
		t.Fatalf("등록: %d %v", n, err)
	}
	if n, err := e.company.AddHolidays(as(uAdmin), []domain.Holiday{{Date: "2026-12-25", Name: "다시"}}); err != nil || n != 0 {
		t.Fatalf("같은 날짜는 건너뜀: %d %v", n, err)
	}
	for _, who := range []int64{uAdmin, uFinance, uAuditor} {
		if rows, err := e.company.Holidays(as(who), 2026); err != nil || len(rows) != 2 || rows[0].Date != "2026-11-05" {
			t.Errorf("조회(%d): %+v %v", who, rows, err)
		}
	}
	if err := e.company.RemoveHoliday(as(uAdmin), "2026-11-05"); !isErr(err, contract.ErrValidation) {
		t.Errorf("지난 날짜 삭제: %v", err)
	}
	if err := e.company.RemoveHoliday(as(uAdmin), "2026-11-10"); !isErr(err, contract.ErrValidation) {
		t.Errorf("오늘 날짜 삭제: %v", err)
	}
	if _, err := e.db.Exec(`DELETE FROM holidays WHERE date = '2026-11-05'`); err == nil {
		t.Error("지난 날짜 삭제는 DB 에서도 막혀야 함")
	}
	if err := e.company.RemoveHoliday(as(uStaff), "2026-12-25"); !isErr(err, contract.ErrForbidden) {
		t.Errorf("직원의 삭제: %v", err)
	}
	if err := e.company.RemoveHoliday(as(uAdmin), "2026-12-25"); err != nil {
		t.Fatalf("미래 공휴일 삭제: %v", err)
	}
	if err := e.company.RemoveHoliday(as(uAdmin), "2026-12-25"); !isErr(err, contract.ErrNotFound) {
		t.Errorf("없는 날짜: %v", err)
	}
	for _, a := range []string{"company.holiday_add", "company.holiday_remove"} {
		if !e.rec.Has(a, audit.TargetSetting, audit.ResultSuccess) {
			t.Errorf("%s 감사가 없음", a)
		}
	}
	items, problems := service.ParseHolidayLines("2026-12-25 성탄절\n\n2026-12-25 중복\nabc 이름\n2026-01-01\n2027-01-01 신정\r\n")
	if len(items) != 2 || len(problems) != 3 || !strings.Contains(problems[0], "3번째 줄") {
		t.Errorf("일괄 입력 읽기: %+v %v", items, problems)
	}
	var many strings.Builder
	for i := 0; i < 101; i++ {
		many.WriteString("2026-12-25 x\n")
	}
	if _, problems := service.ParseHolidayLines(many.String()); len(problems) == 0 || !strings.Contains(problems[len(problems)-1], "100줄") {
		t.Errorf("100줄 초과: %v", problems)
	}
}

// 소명 기한의 마지막 날이 휴일이면 다음 영업일로 늦춘다: 주말은 자동, 공휴일은 등록한 날.
func TestCardDueDateExtendsOverHolidays(t *testing.T) {
	e := newEnv(t)
	e.card(t)
	e.importCards(t, cardRow("2026-11-08 12:00:00", "일요일만기", 12_500, "S1", "승인")) // 11-15(일) → 11-16(월)
	if got := e.txnByApproval(t, "S1", "approve").DueDate; got != "2026-11-16" {
		t.Errorf("주말 만기는 다음 영업일로: %s", got)
	}
	e.importCards(t, cardRow("2026-11-10 12:00:00", "평일만기", 12_500, "W1", "승인")) // 11-17(화)
	if got := e.txnByApproval(t, "W1", "approve").DueDate; got != "2026-11-17" {
		t.Errorf("영업일 만기는 그대로: %s", got)
	}
	if _, err := e.company.AddHolidays(as(uAdmin), []domain.Holiday{{Date: "2026-11-24", Name: "임시공휴일"}, {Date: "2026-11-25", Name: "임시공휴일 2"}}); err != nil {
		t.Fatal(err)
	}
	e.importCards(t, cardRow("2026-11-17 12:00:00", "공휴일만기", 12_500, "H1", "승인")) // 11-24(공휴일)·25(공휴일) → 26
	if got := e.txnByApproval(t, "H1", "approve").DueDate; got != "2026-11-26" {
		t.Errorf("공휴일 만기는 다음 영업일로: %s", got)
	}
}

// 휴일에는 소명 알림을 만들지 않고, 다음 영업일 첫 실행에서 건너뛴 날의 시점까지 합쳐 한 건으로 만든다.
func TestCardRemindersSkipHolidaysAndCatchUp(t *testing.T) {
	e := newEnv(t)
	e.card(t)
	if _, err := e.company.AddHolidays(as(uAdmin), []domain.Holiday{{Date: "2026-11-16", Name: "임시공휴일"}}); err != nil {
		t.Fatal(err)
	}
	e.importCards(t, cardRow("2026-11-10 12:00:00", "평일만기", 12_500, "W1", "승인")) // 기한 11-17(화), 알림 시점: 11-16(월, 공휴일)
	tickAt := func(y int, m time.Month, d, h int) {
		e.now = time.Date(y, m, d, h, 0, 0, 0, time.UTC)
		if err := e.cards.Tick(as(uAdmin)); err != nil {
			t.Fatal(err)
		}
	}
	tickAt(2026, 11, 16, 1) // KST 10시, 공휴일: 알림 없음
	if n := e.notes(t, uStaff, "card_reminder"); len(n) != 0 {
		t.Fatalf("공휴일에 알림이 만들어짐: %+v", n)
	}
	tickAt(2026, 11, 17, 1) // 다음 영업일: 전날 시점(공휴일에 걸렸던)이 합쳐져 한 건
	if n := e.notes(t, uStaff, "card_reminder"); len(n) != 1 || !strings.Contains(n[0].Body, "내일이 소명 기한") {
		t.Fatalf("다음 영업일에 합쳐진 알림: %+v", n)
	}
	tickAt(2026, 11, 17, 5)
	if n := e.notes(t, uStaff, "card_reminder"); len(n) != 1 {
		t.Fatalf("같은 날 두 번째 실행은 만들지 않음: %d", len(n))
	}
}
