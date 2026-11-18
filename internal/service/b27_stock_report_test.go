package service_test

import (
	"testing"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/service"
)

func (e *env) monthStock() *service.MonthStockService {
	return service.NewMonthStockService(service.Base{DB: e.db, Txs: contract.SQLTxManager{DB: e.db}, Audit: e.rec, Now: func() time.Time { return e.now }})
}

// AC-921: 마감된 달은 스냅샷 값, 전월이 마감되어 있으면 전월 대비, 금액은 평가 조회 권한이 있을 때만, 열린 달은 잠정 값이다.
func TestMonthStockReport(t *testing.T) {
	e := newEnv(t)
	it := e.closeFixture(t)
	ms := e.monthStock()
	for _, m := range []string{"2026-10", "2026-11"} {
		if _, err := e.monthClose.Close(as(uFinance), m); err != nil {
			t.Fatal(err)
		}
	}
	rep, err := ms.Report(as(uFinance), domain.MonthStockFilter{Month: "2026-11"})
	if err != nil || rep.Provisional || rep.Version != 1 || !rep.HasAmounts || !rep.PrevClosed || len(rep.Rows) != 1 {
		t.Fatalf("11월: %+v %v", rep, err)
	}
	r := rep.Rows[0]
	if r.ItemID != it.ID || r.OpenQty != 6 || r.OpenAmount != 6000 || r.InQty != 10 || r.InAmount != 20000 || r.CloseQty != 16 || r.CloseAmount != 26000 || r.AvgCost != 1625 {
		t.Fatalf("11월 행: %+v", r)
	}
	if r.Delta == nil || *r.Delta != 20000 || r.DeltaPct == nil || *r.DeltaPct != 333 || r.PrevCloseAmount == nil || *r.PrevCloseAmount != 6000 {
		t.Fatalf("전월 대비: %+v", r)
	}
	if rep.Total.CloseQty != 16 || rep.Total.CloseAmount != 26000 || rep.Total.RoundDiff == nil || *rep.Total.RoundDiff != 0 {
		t.Fatalf("합계: %+v", rep.Total)
	}
	// 10월: 전월(9월)이 마감되지 않아 전월 대비가 없다.
	oct, _ := ms.Report(as(uAuditor), domain.MonthStockFilter{Month: "2026-10"})
	if oct.PrevClosed || oct.Rows[0].Delta != nil || oct.Rows[0].CloseAmount != 6000 || oct.Rows[0].OutQty != 4 || oct.Rows[0].OutAmount != 4000 {
		t.Fatalf("10월: %+v", oct.Rows[0])
	}
	// 직원: 수량만.
	st, _ := ms.Report(as(uStaff), domain.MonthStockFilter{Month: "2026-11"})
	if st.HasAmounts || st.Rows[0].CloseQty != 16 || st.Rows[0].CloseAmount != 0 || st.Rows[0].AvgCost != 0 || st.Rows[0].Delta != nil || st.Total.CloseAmount != 0 || st.Total.RoundDiff != nil {
		t.Fatalf("직원: %+v", st)
	}
	// 열린 달(12월, 이번 달)은 잠정이고 실시간이다.
	e.receiveAt(t, at(2026, 12, 3, 1), it, 3000, 4)
	dec, err := ms.Report(as(uFinance), domain.MonthStockFilter{Month: "2026-12"})
	if err != nil || !dec.Provisional || dec.Version != 0 || dec.Rows[0].OpenQty != 16 || dec.Rows[0].CloseQty != 20 {
		t.Fatalf("12월 잠정: %+v %v", dec, err)
	}
	// 조건: 없는 창고·앞으로 올 달·형식 오류는 400, 검색어가 맞지 않으면 빈 표.
	for name, f := range map[string]domain.MonthStockFilter{"없는 창고": {Month: "2026-11", WarehouseID: 99}, "미래 달": {Month: "2027-03"}, "형식": {Month: "11월"}} {
		if _, err := ms.Report(as(uFinance), f); !isErr(err, contract.ErrValidation) {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if none, _ := ms.Report(as(uFinance), domain.MonthStockFilter{Month: "2026-11", Query: "없는품목"}); len(none.Rows) != 0 || none.Total.RoundDiff != nil {
		t.Fatalf("검색어: %+v", none)
	}
	if one, _ := ms.Report(as(uFinance), domain.MonthStockFilter{Month: "2026-11", WarehouseID: 1, Query: "cl-1"}); len(one.Rows) != 1 {
		t.Fatalf("창고·품목 코드 필터: %+v", one)
	}
}
