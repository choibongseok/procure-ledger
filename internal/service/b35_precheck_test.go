package service_test

import (
	"strings"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/service"
)

// 승인 전 기초 전표는 그 달의 마감 전 점검에 경고로 나오고, 승인되면 사라진다.
func TestMonthClosePrecheckWarnsPendingOpeningJournals(t *testing.T) {
	e := newEnv(t)
	e.jvForm(t)
	e.importItemsAndStock(t)
	has := func() bool {
		pc, err := e.monthClose.Precheck(as(uFinance), "2026-11")
		if err != nil {
			t.Fatal(err)
		}
		for _, w := range pc.Warnings {
			if w.Title == "승인 전 기초 전표" && strings.Contains(w.Detail, "1건") {
				return true
			}
		}
		return false
	}
	if has() {
		t.Fatal("기초 전표가 없는데 경고가 나옴")
	}
	h := service.NewImportOpeningStock(e.importBase(), e.journals)
	_, out, err := e.importRun(t, h, []domain.ImportRow{irow(2, map[string]string{"sku": "PAPER-A4", "warehouse": "WH-01", "qty": "10", "unit_cost": "100"})},
		domain.ImportPlanInput{Target: domain.ImportOpeningStock, BaseDate: "2026-11-05", BatchNo: "IMP-2026-0040"})
	if err != nil || len(out.JournalNos) != 1 {
		t.Fatalf("%+v %v", out, err)
	}
	if !has() {
		t.Fatal("승인 전 기초 전표 경고가 없음")
	}
	j, _ := e.journals.Get(as(uFinance), out.JournalNos[0])
	e.approveJV(t, j)
	if has() {
		t.Fatal("승인된 뒤에는 경고가 사라져야 함")
	}
}
