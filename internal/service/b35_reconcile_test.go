package service_test

import (
	"strings"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/service"
)

func reconRow(t *testing.T, rep domain.ReconcileReport, code string) domain.ReconcileRow {
	t.Helper()
	for _, r := range rep.Rows {
		if r.Code == code {
			return r
		}
	}
	t.Fatalf("항목 없음: %s", code)
	return domain.ReconcileRow{}
}

// 대조 보고서: 새 시스템 값은 코드가 DB 에서 집계하고, 원 시스템 값과 같으면 일치, 다르면 차이와 원인 후보를 안내한다.
func TestImportReconcileReport(t *testing.T) {
	e := newEnv(t)
	e.jvForm(t)
	e.importItemsAndStock(t)
	rc := service.NewImportReconcile(e.importBase())
	for _, who := range []int64{uStaff, uLead} {
		if _, err := rc.Report(as(who), domain.ReconcileInput{}); !isErr(err, contract.ErrForbidden) {
			t.Errorf("%d 대조 보고서: %v", who, err)
		}
	}
	for _, who := range []int64{uAdmin, uFinance, uAuditor} {
		if _, err := rc.Report(as(who), domain.ReconcileInput{}); err != nil {
			t.Errorf("%d 대조 보고서: %v", who, err)
		}
	}
	h := service.NewImportOpeningStock(e.importBase(), e.journals)
	_, out, err := e.importRun(t, h, []domain.ImportRow{irow(2, map[string]string{"sku": "PAPER-A4", "warehouse": "WH-01", "qty": "100", "unit_cost": "2200"})},
		domain.ImportPlanInput{Target: domain.ImportOpeningStock, BaseDate: "2026-11-05", BatchNo: "IMP-2026-0050"})
	if err != nil || len(out.JournalNos) != 1 {
		t.Fatalf("%+v %v", out, err)
	}
	rep, err := rc.Report(as(uAuditor), domain.ReconcileInput{Original: map[string]int64{domain.ReconItems: 2, domain.ReconStockQty: 100, domain.ReconStockValue: 250_000}})
	if err != nil {
		t.Fatal(err)
	}
	if r := reconRow(t, rep, domain.ReconItems); r.System != 2 || r.Verdict != "match" || r.Diff == nil || *r.Diff != 0 {
		t.Errorf("품목 수: %+v", r)
	}
	if r := reconRow(t, rep, domain.ReconStockQty); r.System != 100 || r.Verdict != "match" {
		t.Errorf("재고 수량: %+v", r)
	}
	r := reconRow(t, rep, domain.ReconStockValue)
	if r.System != 220_000 || r.Verdict != "diff" || r.Diff == nil || *r.Diff != -30_000 || len(r.Notes) != 1 || !strings.Contains(r.Notes[0], "승인 전 기초 전표 1건") {
		t.Errorf("재고 금액 차이와 원인 안내: %+v", r)
	}
	if rep.PendingOpeningCount != 1 {
		t.Errorf("승인 전 기초 전표 건수: %d", rep.PendingOpeningCount)
	}
	if r := reconRow(t, rep, domain.ReconVendors); r.Original != nil || r.Verdict != "" || r.Diff != nil {
		t.Errorf("원 시스템 값이 없으면 시스템 값만: %+v", r)
	}
	// 승인되면 안내가 사라지고, 중지된 품목이 있으면 품목 수 차이의 원인으로 안내한다.
	j, _ := e.journals.Get(as(uFinance), out.JournalNos[0])
	e.approveJV(t, j)
	if _, err := e.db.Exec(`UPDATE items SET active = 0 WHERE sku = 'MILK-1L'`); err != nil {
		t.Fatal(err)
	}
	rep, _ = rc.Report(as(uAuditor), domain.ReconcileInput{Original: map[string]int64{domain.ReconItems: 1, domain.ReconStockValue: 220_000}})
	if rep.PendingOpeningCount != 0 || reconRow(t, rep, domain.ReconStockValue).Verdict != "match" {
		t.Errorf("승인 뒤: %+v", rep)
	}
	if r := reconRow(t, rep, domain.ReconItems); r.Verdict != "diff" || len(r.Notes) != 1 || !strings.Contains(r.Notes[0], "중지된 품목 1종") {
		t.Errorf("중지 품목 안내: %+v", r)
	}
}
