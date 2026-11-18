package service_test

import (
	"strings"
	"testing"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
)

func f1(p *float64) float64 {
	if p == nil {
		return -1
	}
	return *p
}

// AC-730·735: 구매 실적 - 사용액(발주 생성)·결재 진행 중·집행률·예산 없는 부서·월, 합계와 제외 금액, 열람 범위별 숫자, 권한.
func TestReportPurchaseExecution(t *testing.T) {
	e := newEnv(t)
	e.setBudget(t, deptBuy, 500_000)
	a := e.submitted(t, uStaff, 200_000)
	approve(t, e, uLead, a.ID) // 발주 생성(사용액)
	e.submitted(t, uStaff, 100_000)
	s := e.submitted(t, uStaff2, 300_000) // 영업팀(예산 없음)
	approve(t, e, uLead2, s.ID)

	rs := e.reports
	rep, err := rs.Purchase(as(uFinance), domain.PurchaseReportQuery{FromMonth: nov, ToMonth: nov})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Rows) != 2 {
		t.Fatalf("행 %+v", rep.Rows)
	}
	var buy, sales domain.PurchaseReportRow
	for _, r := range rep.Rows {
		if r.DepartmentID == deptBuy {
			buy = r
		} else {
			sales = r
		}
	}
	if buy.Budget == nil || *buy.Budget != 500_000 || buy.Orders != 1 || buy.Used != 200_000 || buy.InProgress != 100_000 || *buy.Balance != 200_000 ||
		f1(buy.ExecutionPct) != 40.0 || f1(buy.ForecastPct) != 60.0 || buy.Badge != domain.BadgeOK {
		t.Fatalf("구매팀 행: %+v", buy)
	}
	if sales.Budget != nil || sales.ExecutionPct != nil || sales.Badge != "none" || sales.Used != 300_000 {
		t.Fatalf("예산 없는 영업팀 행: %+v", sales)
	}
	tt := rep.Totals
	if tt.Used != 500_000 || tt.InProgress != 100_000 || tt.Orders != 2 || tt.Budget != 500_000 || tt.BudgetedUsed != 200_000 || f1(tt.ExecutionPct) != 40.0 || tt.UnbudgetedUsed != 300_000 {
		t.Fatalf("합계: %+v", tt)
	}
	// 열람 범위: 영업팀 팀장은 영업팀 숫자만, 구매팀 직원이 올린 건은 보이지 않는다.
	r2, err := rs.Purchase(as(uLead2), domain.PurchaseReportQuery{FromMonth: nov, ToMonth: nov})
	if err != nil || len(r2.Rows) != 1 || r2.Rows[0].DepartmentID != deptSales || r2.Totals.Used != 300_000 {
		t.Fatalf("영업팀장 범위: %+v %v", r2, err)
	}
	if r3, _ := rs.Purchase(as(uFinance), domain.PurchaseReportQuery{FromMonth: nov, ToMonth: nov, DepartmentID: deptBuy}); len(r3.Rows) != 1 || r3.Totals.Used != 200_000 {
		t.Fatalf("부서 필터: %+v", r3)
	}
	// 직원은 보고서 권한이 없다.
	if _, err := rs.Purchase(as(uStaff), domain.PurchaseReportQuery{FromMonth: nov, ToMonth: nov}); !isErr(err, contract.ErrForbidden) {
		t.Fatalf("직원: %v", err)
	}
	// 기간 오류.
	if _, err := rs.Purchase(as(uFinance), domain.PurchaseReportQuery{FromMonth: "2026-11", ToMonth: "2026-10"}); !isErr(err, contract.ErrValidation) || !strings.Contains(err.Error(), "종료일은 시작일 이후여야 합니다.") {
		t.Fatalf("역전: %v", err)
	}
	if _, err := rs.Purchase(as(uFinance), domain.PurchaseReportQuery{FromMonth: "2025-11", ToMonth: "2026-11"}); !isErr(err, contract.ErrValidation) || !strings.Contains(err.Error(), "조회 기간은 최대 12개월입니다.") {
		t.Fatalf("13개월: %v", err)
	}
}

// B28 검증 D: 보고서 선택지의 부서는 열람 범위 안, 양식은 사람이 기안할 수 있는 양식만이다(시스템 전용 양식 제외).
func TestReportOptionsFollowViewScope(t *testing.T) {
	e := newEnv(t)
	e.submitted(t, uStaff, 100_000) // 구매팀 문서
	all, err := e.reports.Options(as(uFinance))
	if err != nil {
		t.Fatal(err)
	}
	mine, err := e.reports.Options(as(uLead2)) // 영업팀 팀장
	if err != nil {
		t.Fatal(err)
	}
	if len(mine.Departments) >= len(all.Departments) {
		t.Fatalf("조직장 범위가 전체보다 좁아야 함: %v / %v", mine.Departments, all.Departments)
	}
	for _, d := range mine.Departments {
		if d.ID == deptBuy {
			t.Fatalf("범위 밖 부서가 보임: %v", mine.Departments)
		}
	}
	for _, f := range all.Forms {
		if f.Code == "MCR" || f.Code == "ADJ" {
			t.Fatalf("시스템 전용 양식이 선택지에 있음: %v", all.Forms)
		}
	}
	found := false
	for _, f := range all.Forms {
		found = found || f.Code == "PUR"
	}
	if !found {
		t.Fatalf("구매 품의서가 없음: %v", all.Forms)
	}
	if _, err := e.reports.Options(as(uStaff)); !isErr(err, contract.ErrForbidden) {
		t.Fatalf("직원: %v", err)
	}
}

// AC-731: 거래처 탭의 납기 준수율·불일치 비율은 B25 평가와 같은 값이고 발주 금액은 발주일 기준 합이다. 결재자에게는 거래처 탭이 없다(탭 단위 권한).
func TestReportVendorsMatchesRatingAndAddsAmounts(t *testing.T) {
	e := newEnv(t)
	v := e.poVendor(t, "보고서상사", "888-88-88888")
	po := e.orderFor(t, v, "2026-12-31", 1_000, 10)
	e.receiveAll(t, po)
	e.orderFor(t, v, "", 500, 4)
	from, to := contract.Day("2026-11-01"), contract.Day("2026-11-30")
	rows, err := e.reports.Vendors(as(uFinance), from, to)
	if err != nil || len(rows) != 1 {
		t.Fatalf("거래처 보고서: %+v %v", rows, err)
	}
	rating, _ := e.rating.Rate(as(uFinance), from, to)
	if len(rating) != 1 || rows[0].VendorRating != rating[0] {
		t.Fatalf("평가와 같은 값이어야 함: %+v / %+v", rows[0].VendorRating, rating)
	}
	if rows[0].PurchaseOrders != 2 || rows[0].OrderAmount != 12_000 {
		t.Fatalf("발주 건수·금액: %+v", rows[0])
	}
	if _, err := e.reports.Vendors(as(uLead), from, to); !isErr(err, contract.ErrForbidden) {
		t.Fatalf("결재자는 거래처 탭 403: %v", err)
	}
}

func mins(t *testing.T, got, want int64, what string) {
	t.Helper()
	if got != want {
		t.Fatalf("%s: %d분(기대 %d분)", what, got, want)
	}
}

// AC-732~734: 결재 소요 - 단계 시작(상신/직전 단계 처리), 주말을 걸친 달력 시간, 평균·중앙값·최대, 전체 소요, 지연 상위(기간과 무관), 열람 범위.
func TestReportApprovalTimes(t *testing.T) {
	e := newEnv(t)
	base := time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC) // 금요일
	e.now = base
	a := e.submitted(t, uStaff, 200_000)
	e.now = base.Add(90 * time.Minute)
	approve(t, e, uLead, a.ID) // 첫 단계 90분
	// 두 번째 건: 금요일 상신, 월요일 승인(주말 포함 달력 시간 4320분).
	e.now = base
	b := e.submitted(t, uStaff, 150_000)
	e.now = base.Add(72 * time.Hour)
	approve(t, e, uLead, b.ID)
	// 세 번째 건은 진행 중으로 남긴다.
	e.now = base.Add(73 * time.Hour)
	c := e.submitted(t, uStaff, 120_000)
	e.now = base.Add(73*time.Hour + 5*time.Hour)

	q := domain.ApprovalReportQuery{From: "2026-11-01", To: "2026-11-30"}
	rep, err := e.reports.Approvals(as(uFinance), q)
	if err != nil {
		t.Fatal(err)
	}
	var first, total *domain.StepStat
	for i := range rep.Stats {
		s := &rep.Stats[i]
		t.Logf("통계 %+v", *s)
		if s.IsFormTotal {
			total = s
		} else if first == nil {
			first = s
		}
	}
	if first == nil || total == nil {
		t.Fatalf("통계 줄이 없음: %+v", rep.Stats)
	}
	if first.Count != 2 {
		t.Fatalf("첫 단계 처리 건수 %d", first.Count)
	}
	mins(t, first.AvgMinutes, (90+4320)/2, "첫 단계 평균")
	mins(t, first.MedMinutes, (90+4320)/2, "첫 단계 중앙값(짝수 건)")
	mins(t, first.MaxMinutes, 4320, "첫 단계 최대(주말을 걸친 달력 시간)")
	if total.Count != 2 {
		t.Fatalf("전체 소요 건수: %+v", *total)
	}
	mins(t, total.MaxMinutes, 4320, "전체 소요 최대")
	// 지연 상위: 진행 중 건의 현재 단계 경과(5시간)가 기간 필터와 무관하게 보인다.
	far := domain.ApprovalReportQuery{From: "2020-01-01", To: "2020-01-31"}
	rep2, err := e.reports.Approvals(as(uFinance), far)
	if err != nil || len(rep2.Delayed) != 1 || rep2.Delayed[0].DocID != c.ID || rep2.Delayed[0].ElapsedMinutes != 300 {
		t.Fatalf("지연 상위: %+v %v", rep2.Delayed, err)
	}
	if rep2.Delayed[0].NoApprover || rep2.Delayed[0].ApproverName == "" {
		t.Fatalf("현재 결재자: %+v", rep2.Delayed[0])
	}
	if len(rep2.Stats) != 0 {
		t.Fatalf("기간 밖 통계는 비어야 함: %+v", rep2.Stats)
	}
	// 열람 범위: 영업팀 팀장에게는 구매팀 문서가 집계되지 않는다.
	rep3, err := e.reports.Approvals(as(uLead2), q)
	if err != nil || len(rep3.Stats) != 0 || len(rep3.Delayed) != 0 {
		t.Fatalf("영업팀장 범위: %+v %v", rep3, err)
	}
	if _, err := e.reports.Approvals(as(uStaff), q); !isErr(err, contract.ErrForbidden) {
		t.Fatalf("직원: %v", err)
	}
}

// AC-732: 다단계 결재에서 각 단계의 시작은 직전 단계의 처리 시각이다(단계마다 60분 간격이면 모든 단계가 60분, 전체는 단계 수 × 60분).
func TestReportApprovalStepStartIsPreviousDecision(t *testing.T) {
	e := newEnv(t)
	base := time.Date(2026, 11, 10, 1, 0, 0, 0, time.UTC)
	e.now = base
	pr := e.submitted(t, uStaff, 3_000_000)
	who := map[domain.StepKind]int64{domain.StepTeamLead: uLead, domain.StepDeptHead: uHead, domain.StepFinance: uFinance}
	n := len(pr.Steps)
	if n < 2 {
		t.Fatalf("다단계여야 함: %v", kinds(pr))
	}
	for i, s := range pr.Steps {
		e.now = base.Add(time.Duration(i+1) * 60 * time.Minute)
		approve(t, e, who[s.Kind], pr.ID)
	}
	rep, err := e.reports.Approvals(as(uFinance), domain.ApprovalReportQuery{From: "2026-11-01", To: "2026-11-30"})
	if err != nil {
		t.Fatal(err)
	}
	steps := 0
	for _, s := range rep.Stats {
		if s.IsFormTotal {
			mins(t, s.AvgMinutes, int64(n)*60, "전체 소요")
			continue
		}
		steps++
		if s.Count != 1 {
			t.Fatalf("단계 건수: %+v", s)
		}
		mins(t, s.AvgMinutes, 60, "단계 "+s.StepTitle)
	}
	if steps != n {
		t.Fatalf("단계 줄 %d개(기대 %d): %+v", steps, n, rep.Stats)
	}
}
