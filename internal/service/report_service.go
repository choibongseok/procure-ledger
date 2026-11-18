package service

import (
	"context"
	"math"
	"sort"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/repository"
)

// 업무 보고서 3종(B28 erp, 5~7절). 읽기 전용이고 조회 때 계산한다(저장·캐시 없음).
// 열람 범위는 ViewScope 가 집계 대상을 줄이는 것으로 적용한다(같은 보고서를 다른 사람이 열면 숫자가 다르다).

// ReportService 는 domain.ReportService 의 구현이다.
type ReportService struct {
	Base
	Repo   repository.SQLReport
	Rating *VendorRatingService
}

var _ domain.ReportService = (*ReportService)(nil)

func NewReportService(b Base, rating *VendorRatingService) *ReportService {
	return &ReportService{Base: b, Rating: rating}
}

const maxReportMonths = 12

var errReportRange = "종료일은 시작일 이후여야 합니다."

func reportPeriodError(months int) error {
	if months > maxReportMonths {
		return validation("조회 기간은 최대 12개월입니다.")
	}
	return nil
}

// dayRange 는 날짜 구간을 확인한다(종료 ≥ 시작, 최대 12개월).
func dayRange(from, to contract.Day) error {
	f, err1 := time.Parse("2006-01-02", string(from))
	t, err2 := time.Parse("2006-01-02", string(to))
	if err1 != nil || err2 != nil {
		return validation("기간은 YYYY-MM-DD 형식으로 입력하세요.")
	}
	if t.Before(f) {
		return validation("%s", errReportRange)
	}
	if t.After(f.AddDate(0, maxReportMonths, 0)) {
		return validation("조회 기간은 최대 12개월입니다.")
	}
	return nil
}

func pct1(num, den int64) *float64 {
	if den <= 0 {
		return nil
	}
	v := math.Round(float64(num)*1000/float64(den)) / 10
	return &v
}

// Options 는 보고서 필터의 부서·양식 선택지다(이름만, 열람 범위와 무관한 마스터 목록).
func (s *ReportService) Options(ctx context.Context) (domain.ReportOptions, error) {
	p, err := contract.Authorize(ctx, contract.PermReportRead)
	if err != nil {
		return domain.ReportOptions{}, err
	}
	scope, err := s.viewScope(ctx, p)
	if err != nil {
		return domain.ReportOptions{}, err
	}
	return s.Repo.Options(ctx, s.DB, scope, p.DepartmentID)
}

// Purchase 는 부서 × 예산 월 구매 실적과 예산 대비 집행률이다(7.1).
func (s *ReportService) Purchase(ctx context.Context, q domain.PurchaseReportQuery) (domain.PurchaseReport, error) {
	p, err := contract.Authorize(ctx, contract.PermReportRead)
	if err != nil {
		return domain.PurchaseReport{}, err
	}
	if q.FromMonth == "" || q.ToMonth == "" { // 기본 기간: 이번 회계연도 시작 월~이번 달
		start, err := s.fiscalStartMonth(ctx, s.DB)
		if err != nil {
			return domain.PurchaseReport{}, err
		}
		today := s.kstDay()
		if q.FromMonth == "" {
			f, _, _ := fiscalRange(today, start)
			q.FromMonth = f[:7]
		}
		if q.ToMonth == "" {
			q.ToMonth = today[:7]
		}
	}
	fm, err1 := time.Parse("2006-01", q.FromMonth)
	tm, err2 := time.Parse("2006-01", q.ToMonth)
	if err1 != nil || err2 != nil {
		return domain.PurchaseReport{}, validation("조회 월은 YYYY-MM 형식으로 입력하세요.")
	}
	if tm.Before(fm) {
		return domain.PurchaseReport{}, validation("%s", errReportRange)
	}
	if err := reportPeriodError((tm.Year()*12 + int(tm.Month())) - (fm.Year()*12 + int(fm.Month())) + 1); err != nil {
		return domain.PurchaseReport{}, err
	}
	scope, err := s.viewScope(ctx, p)
	if err != nil {
		return domain.PurchaseReport{}, err
	}
	agg, err := s.Repo.PurchaseAgg(ctx, s.DB, q.FromMonth, q.ToMonth, repository.ReportFilter{DepartmentID: q.DepartmentID}, scope)
	if err != nil {
		return domain.PurchaseReport{}, err
	}
	out := domain.PurchaseReport{From: q.FromMonth, To: q.ToMonth, Rows: make([]domain.PurchaseReportRow, 0, len(agg))}
	t := &out.Totals
	for _, a := range agg {
		r := domain.PurchaseReportRow{DepartmentID: a.DepartmentID, DepartmentName: a.DepartmentName, Month: a.Month, Budget: a.Budget,
			Orders: a.Orders, Used: a.Used, InProgress: a.InProgress, Badge: "none"}
		t.Orders += a.Orders
		t.Used += a.Used
		t.InProgress += a.InProgress
		if a.Budget == nil {
			t.UnbudgetedUsed += a.Used
		} else {
			b := *a.Budget
			bal := b - a.Used - a.InProgress
			r.Balance = &bal
			r.ExecutionPct = pct1(a.Used, b)
			r.ForecastPct = pct1(a.Used+a.InProgress, b)
			r.Badge, _ = budgetBadge(b, a.Used+a.InProgress)
			t.Budget += b
			t.BudgetedUsed += a.Used
		}
		out.Rows = append(out.Rows, r)
	}
	t.ExecutionPct = pct1(t.BudgetedUsed, t.Budget)
	return out, nil
}

// Vendors 는 거래처별 실적·평가다(7.2): B25 평가(VendorRatingService.Rate)와 같은 값에 발주 건수·금액을 더한다.
func (s *ReportService) Vendors(ctx context.Context, from, to contract.Day) ([]domain.VendorReportRow, error) {
	p, err := contract.Authorize(ctx, contract.PermReportRead)
	if err != nil {
		return nil, err
	}
	if _, err := contract.Authorize(ctx, contract.PermVendorRatingRead); err != nil { // 거래처 탭은 탭 단위 권한이 더 필요하다
		return nil, err
	}
	if err := dayRange(from, to); err != nil {
		return nil, err
	}
	ratings, err := s.Rating.Rate(ctx, from, to)
	if err != nil {
		return nil, err
	}
	scope, err := s.viewScope(ctx, p)
	if err != nil {
		return nil, err
	}
	orders, err := s.Repo.VendorOrders(ctx, s.DB, string(from), string(to), scope)
	if err != nil {
		return nil, err
	}
	byVendor := map[int64]repository.VendorOrderAgg{}
	for _, o := range orders {
		byVendor[o.VendorID] = o
	}
	out := make([]domain.VendorReportRow, 0, len(ratings)+len(orders))
	seen := map[int64]bool{}
	for _, r := range ratings {
		o := byVendor[r.VendorID]
		out = append(out, domain.VendorReportRow{VendorRating: r, PurchaseOrders: o.Orders, OrderAmount: o.Amount})
		seen[r.VendorID] = true
	}
	for _, o := range orders {
		if seen[o.VendorID] {
			continue
		}
		out = append(out, domain.VendorReportRow{VendorRating: domain.VendorRating{VendorID: o.VendorID, VendorName: o.VendorName, OnTimePct: -1, MismatchPct: -1,
			OnTimeBadge: domain.RatingNone, MismatchBadge: domain.RatingNone}, PurchaseOrders: o.Orders, OrderAmount: o.Amount})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].OrderAmount > out[j].OrderAmount })
	return out, nil
}

// stepStart 는 단계 시작 시각을 역산한다(7.3, 한 곳): 직전에 처리된 단계(더 앞선 단계 번호)의 가장 늦은 처리 시각, 없으면 그 차수의 상신 시각이다.
// 병렬 합의(같은 단계 번호)는 같은 시작을 쓴다. 상신 시각도 없으면 역산할 수 없다(false).
func stepStart(rt repository.RoundTimes, doc int64, round, stage int) (time.Time, bool) {
	k := [2]int64{doc, int64(round)}
	var prev time.Time
	for _, d := range rt.Decided[k] {
		if d.Stage < stage && d.At.After(prev) {
			prev = d.At
		}
	}
	if !prev.IsZero() {
		return prev, true
	}
	if t, ok := rt.Submit[k]; ok && !t.IsZero() {
		return t, true
	}
	return time.Time{}, false
}

var stepKindTitle = map[string]string{"team_lead": "팀장", "dept_head": "부서장", "division_head": "본부장", "ceo": "대표", "finance": "재무"}

func stepTitleOf(title, kind string) string {
	if title != "" {
		return title
	}
	if t, ok := stepKindTitle[kind]; ok {
		return t
	}
	return "결재"
}

func minutes(d time.Duration) int64 {
	if d < 0 {
		return 0
	}
	return int64(d / time.Minute)
}

// Approvals 는 결재 소요 시간(양식·단계별 평균·중앙값·최대, 지연 상위 20건)이다(7.3). 주말·공휴일을 빼지 않은 달력 시간이다.
func (s *ReportService) Approvals(ctx context.Context, q domain.ApprovalReportQuery) (domain.ApprovalReport, error) {
	p, err := contract.Authorize(ctx, contract.PermReportRead)
	if err != nil {
		return domain.ApprovalReport{}, err
	}
	if err := dayRange(q.From, q.To); err != nil {
		return domain.ApprovalReport{}, err
	}
	scope, err := s.viewScope(ctx, p)
	if err != nil {
		return domain.ApprovalReport{}, err
	}
	filter := repository.ReportFilter{DepartmentID: q.DepartmentID}
	steps, err := s.Repo.DecidedSteps(ctx, s.DB, string(q.From), string(q.To), q.FormCode, filter, scope)
	if err != nil {
		return domain.ApprovalReport{}, err
	}
	done, err := s.Repo.CompletedDocs(ctx, s.DB, string(q.From), string(q.To), q.FormCode, filter, scope)
	if err != nil {
		return domain.ApprovalReport{}, err
	}
	pending, err := s.Repo.PendingSteps(ctx, s.DB, q.FormCode, filter, scope)
	if err != nil {
		return domain.ApprovalReport{}, err
	}
	ids := map[int64]bool{}
	for _, x := range steps {
		ids[x.DocID] = true
	}
	for _, x := range done {
		ids[x.DocID] = true
	}
	for _, x := range pending {
		ids[x.DocID] = true
	}
	list := make([]int64, 0, len(ids))
	for id := range ids {
		list = append(list, id)
	}
	rt, err := s.Repo.RoundData(ctx, s.DB, list)
	if err != nil {
		return domain.ApprovalReport{}, err
	}

	type key struct{ form, title string }
	type acc struct {
		stat  domain.StepStat
		times []int64
		stage int
	}
	groups := map[key]*acc{}
	var order []key
	get := func(k key, name, code string, stage int, total bool) *acc {
		a, ok := groups[k]
		if !ok {
			a = &acc{stat: domain.StepStat{FormCode: code, FormName: name, StepTitle: k.title, IsFormTotal: total}, stage: stage}
			groups[k] = a
			order = append(order, k)
		}
		if stage < a.stage {
			a.stage = stage
		}
		return a
	}
	var rep domain.ApprovalReport
	for _, x := range steps {
		start, ok := stepStart(rt, x.DocID, x.Round, x.Stage)
		if !ok {
			rep.Excluded++
			continue
		}
		a := get(key{x.FormCode, stepTitleOf(x.Title, x.StepKind)}, x.FormName, x.FormCode, x.Stage, false)
		a.times = append(a.times, minutes(x.DecidedAt.Sub(start)))
		switch x.Status {
		case "rejected":
			a.stat.Rejected++
		case "objected":
			a.stat.Objected++
		}
	}
	for _, d := range done {
		st, ok := rt.Submit[[2]int64{d.DocID, int64(d.Round)}]
		if !ok {
			rep.Excluded++
			continue
		}
		a := get(key{d.FormCode, "전체"}, d.FormName, d.FormCode, 1<<30, true)
		a.times = append(a.times, minutes(d.DoneAt.Sub(st)))
	}
	sort.SliceStable(order, func(i, j int) bool {
		a, b := groups[order[i]], groups[order[j]]
		if a.stat.FormName != b.stat.FormName {
			return a.stat.FormName < b.stat.FormName
		}
		return a.stage < b.stage
	})
	for _, k := range order {
		a := groups[k]
		ts := a.times
		sort.Slice(ts, func(i, j int) bool { return ts[i] < ts[j] })
		var sum int64
		for _, v := range ts {
			sum += v
		}
		a.stat.Count = len(ts)
		a.stat.AvgMinutes = sum / int64(len(ts))
		a.stat.MaxMinutes = ts[len(ts)-1]
		if n := len(ts); n%2 == 1 {
			a.stat.MedMinutes = ts[n/2]
		} else {
			a.stat.MedMinutes = (ts[n/2-1] + ts[n/2]) / 2
		}
		rep.Stats = append(rep.Stats, a.stat)
	}

	now := s.now()
	for _, x := range pending {
		start, ok := stepStart(rt, x.DocID, x.Round, x.Stage)
		if !ok {
			continue
		}
		d := domain.DelayedDoc{DocID: x.DocID, DocNo: x.DocNo, Title: x.Title, FormName: x.FormName, StepTitle: stepTitleOf(x.StepTitle, x.StepKind),
			NoApprover: x.NoApprover, ElapsedMinutes: minutes(now.Sub(start)), StepStart: start, Link: x.FormKind}
		d.ApproverName = joinNames(x.Approvers)
		rep.Delayed = append(rep.Delayed, d)
	}
	sort.SliceStable(rep.Delayed, func(i, j int) bool { return rep.Delayed[i].ElapsedMinutes > rep.Delayed[j].ElapsedMinutes })
	if len(rep.Delayed) > 20 {
		rep.Delayed = rep.Delayed[:20]
	}
	return rep, nil
}

func joinNames(ns []string) string {
	out := ""
	for i, n := range ns {
		if i > 0 {
			out += ", "
		}
		out += n
	}
	return out
}
