package service

import (
	"context"
	"sort"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/repository"
)

// 거래처 평가(B25 ④, 3.4). 읽기 전용이고 조회 때 SQL 로 원자료를 읽어 계산한다(저장·캐시 없음).
//   - 납기 준수율: 기간 안에 입고가 끝난(마지막 입고일이 기간 안) 발주 중 납기가 있는 것이 분모, 마지막 입고일(KST) ≤ 납기일이 분자.
//   - 불일치 비율: 기간 안에 재무가 확정한 판정 중 일치·불일치가 분모, 불일치가 분자(확정 전·판단 보류 제외).
//   - 배지 임계값은 상수(준수율 90%·70%, 불일치 10%), 분모 3건 미만은 "표본 적음".
//   - 납기를 넘긴 미입고는 점수에 넣지 않고 따로 건수로 드러낸다(D11).

const (
	ratingGoodPct   = 90
	ratingWarnPct   = 70
	ratingMismatch  = 10
	ratingMinSample = 3
)

// onTimeBadge 는 납기 준수 배지다. 정수 연산으로만 비교한다(경계가 흔들리지 않게).
func onTimeBadge(onTime, denom int) (badge string, pct int) {
	switch {
	case denom == 0:
		return domain.RatingNone, -1
	}
	pct = onTime * 100 / denom
	switch {
	case denom < ratingMinSample:
		return domain.RatingSmall, pct
	case onTime*100 >= denom*ratingGoodPct:
		return domain.RatingGood, pct
	case onTime*100 >= denom*ratingWarnPct:
		return domain.RatingWarn, pct
	}
	return domain.RatingBad, pct
}

// mismatchBadge 는 불일치 배지다(10% 이하 양호).
func mismatchBadge(mismatch, n int) (badge string, pct int) {
	if n == 0 {
		return domain.RatingNone, -1
	}
	pct = mismatch * 100 / n
	switch {
	case n < ratingMinSample:
		return domain.RatingSmall, pct
	case mismatch*100 <= n*ratingMismatch:
		return domain.RatingGood, pct
	}
	return domain.RatingBad, pct
}

// VendorRatingService 는 domain.VendorRatingService 의 구현이다.
type VendorRatingService struct {
	Base
	Repo repository.SQLRating
}

var _ domain.VendorRatingService = (*VendorRatingService)(nil)

func NewVendorRatingService(b Base) *VendorRatingService { return &VendorRatingService{Base: b} }

func parseRatingPeriod(from, to contract.Day) error {
	f, err1 := time.Parse("2006-01-02", string(from))
	t, err2 := time.Parse("2006-01-02", string(to))
	if err1 != nil || err2 != nil {
		return validation("기간은 YYYY-MM-DD 형식으로 입력하세요.")
	}
	if t.Before(f) {
		return validation("종료일은 시작일 이후여야 합니다.")
	}
	return nil
}

type ratingData struct {
	orders   []repository.RatedOrder
	verdicts []repository.ConfirmedVerdict
	openLate []repository.OpenLateOrder
}

func (s *VendorRatingService) load(ctx context.Context, from, to contract.Day) (ratingData, error) {
	var d ratingData
	var err error
	if d.orders, err = s.Repo.ReceivedOrders(ctx, s.DB, string(from), string(to)); err != nil {
		return d, err
	}
	if d.verdicts, err = s.Repo.ConfirmedVerdicts(ctx, s.DB, string(from), string(to)); err != nil {
		return d, err
	}
	d.openLate, err = s.Repo.OpenLateOrders(ctx, s.DB, string(contract.DayOf(s.now())))
	return d, err
}

// rate 는 원자료로 거래처별 평가를 계산한다(서비스 밖에서 시험할 수 있게 순수 함수로 둔다).
func rate(d ratingData) []domain.VendorRating {
	by := map[int64]*domain.VendorRating{}
	get := func(id int64, name string) *domain.VendorRating {
		r, ok := by[id]
		if !ok {
			r = &domain.VendorRating{VendorID: id, VendorName: name}
			by[id] = r
		}
		return r
	}
	for _, o := range d.orders {
		r := get(o.VendorID, o.VendorName)
		r.Orders++
		if o.DueDate == "" {
			r.NoDue++
			continue
		}
		r.WithDue++
		if o.ReceivedOn <= o.DueDate { // 납기 당일 입고는 납기 내
			r.OnTime++
		}
	}
	for _, v := range d.verdicts {
		r := get(v.VendorID, v.VendorName)
		r.Verdicts++
		if v.Mismatch {
			r.Mismatches++
		}
	}
	// 납기를 넘긴 미입고 발주는 기간과 무관하게 거래처 행으로 드러낸다(입고 완료·확정 판정이 없어도 행이 있다. 점수는 "—", 미입고 건수만 센다, D11).
	for _, o := range d.openLate {
		get(o.VendorID, o.VendorName).LateOpen++
	}
	out := make([]domain.VendorRating, 0, len(by))
	for _, r := range by {
		r.OnTimeBadge, r.OnTimePct = onTimeBadge(r.OnTime, r.WithDue)
		r.MismatchBadge, r.MismatchPct = mismatchBadge(r.Mismatches, r.Verdicts)
		out = append(out, *r)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].VendorName < out[j].VendorName || (out[i].VendorName == out[j].VendorName && out[i].VendorID < out[j].VendorID)
	})
	return out
}

// Rate 는 기간 안의 거래처별 평가다.
func (s *VendorRatingService) Rate(ctx context.Context, from, to contract.Day) ([]domain.VendorRating, error) {
	if _, err := s.authorize(ctx, contract.PermVendorRatingRead, "vendor_rating.read", audit.TargetVendor); err != nil {
		return nil, err
	}
	if err := parseRatingPeriod(from, to); err != nil {
		return nil, err
	}
	d, err := s.load(ctx, from, to)
	if err != nil {
		return nil, err
	}
	return rate(d), nil
}

func daysBetween(from, to string) int {
	f, err1 := time.Parse("2006-01-02", from)
	t, err2 := time.Parse("2006-01-02", to)
	if err1 != nil || err2 != nil {
		return 0
	}
	return int(t.Sub(f).Hours() / 24)
}

// Detail 은 한 거래처의 상세다: 납기를 넘겨 입고된 발주, 납기를 넘긴 미입고, 확정 결과가 불일치인 판정.
func (s *VendorRatingService) Detail(ctx context.Context, vendorID int64, from, to contract.Day) (domain.VendorRatingDetail, error) {
	if _, err := s.authorize(ctx, contract.PermVendorRatingRead, "vendor_rating.read", audit.TargetVendor); err != nil {
		return domain.VendorRatingDetail{}, err
	}
	if err := parseRatingPeriod(from, to); err != nil {
		return domain.VendorRatingDetail{}, err
	}
	d, err := s.load(ctx, from, to)
	if err != nil {
		return domain.VendorRatingDetail{}, err
	}
	var out domain.VendorRatingDetail
	for _, r := range rate(d) {
		if r.VendorID == vendorID {
			out.VendorRating = r
		}
	}
	if out.VendorID == 0 {
		vs, err := repository.SQLInventory{}.ListVendors(ctx, s.DB)
		if err != nil {
			return out, err
		}
		for _, v := range vs {
			if v.ID == vendorID {
				out.VendorRating = domain.VendorRating{VendorID: v.ID, VendorName: v.Name, OnTimePct: -1, MismatchPct: -1, OnTimeBadge: domain.RatingNone, MismatchBadge: domain.RatingNone}
			}
		}
		if out.VendorID == 0 {
			return out, contract.ErrNotFound
		}
	}
	for _, o := range d.orders {
		if o.VendorID == vendorID && o.DueDate != "" && o.ReceivedOn > o.DueDate {
			out.Late = append(out.Late, domain.VendorRatingLate{PONo: o.PONo, DueDate: o.DueDate, ReceivedOn: o.ReceivedOn, DaysLate: daysBetween(o.DueDate, o.ReceivedOn)})
		}
	}
	today := string(contract.DayOf(s.now()))
	for _, o := range d.openLate {
		if o.VendorID == vendorID {
			out.OpenLate = append(out.OpenLate, domain.VendorRatingLate{PONo: o.PONo, DueDate: o.DueDate, DaysLate: daysBetween(o.DueDate, today)})
		}
	}
	for _, v := range d.verdicts {
		if v.VendorID == vendorID && v.Mismatch {
			out.Mismatched = append(out.Mismatched, domain.VendorRatingMismatch{VerdictID: v.VerdictID, DocumentID: v.DocumentID, PONo: v.PONo, ConfirmedOn: v.ConfirmedOn})
		}
	}
	return out, nil
}
