package service

import (
	"context"
	"fmt"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/repository"
)

// 부가세 매입 집계(B32 ④, 명세 3.4). 승인된 전표만 세고 기준은 거래일(공급 시기)이다. 세금계산서 증빙이 확정된 매입만 공제에 넣고
// 거래명세서뿐인 매입은 "미수취"로 나누어 공제 합계에서 뺀다. 불공제는 카드 소명에서만 나온다(D4). 신고 서식 전송은 하지 않는다.

// VATService 는 domain.VATService 의 구현이다.
type VATService struct {
	Base
	Repo repository.SQLVAT
	Site repository.SQLSites
}

var _ domain.VATService = (*VATService)(nil)

func NewVATService(b Base) *VATService { return &VATService{Base: b} }

// vatPeriod 는 필터를 [from, to] 날짜로 바꾼다(월·분기·사용자 지정 최대 12개월).
func (b Base) vatPeriod(f domain.VATFilter) (string, string, error) {
	today := b.kstDay()
	unit := f.Unit
	if unit == "" && (f.From != "" || f.To != "") {
		unit = "custom" // 단위 없이 시작일·종료일만 오면 사용자 지정 기간으로 읽는다(기본값으로 조용히 바꾸지 않는다)
	}
	switch unit {
	case "", "month":
		m := f.Month
		if m == "" {
			m = today[:7]
		}
		first, err := time.Parse("2006-01", m)
		if err != nil {
			return "", "", validation("월은 YYYY-MM 형식으로 입력하세요.")
		}
		return first.Format("2006-01-02"), first.AddDate(0, 1, -1).Format("2006-01-02"), nil
	case "quarter":
		y, q := f.Year, f.Quarter
		if y == 0 {
			y = b.now().In(contract.KST).Year()
		}
		if q < 1 || q > 4 {
			return "", "", validation("분기는 1~4 로 고르세요.")
		}
		first := time.Date(y, time.Month((q-1)*3+1), 1, 0, 0, 0, 0, time.UTC)
		return first.Format("2006-01-02"), first.AddDate(0, 3, -1).Format("2006-01-02"), nil
	case "custom":
		if !validDate(f.From) || !validDate(f.To) {
			return "", "", validation("시작일·종료일을 YYYY-MM-DD 형식으로 입력하세요.")
		}
		if f.To < f.From {
			return "", "", validation("종료일은 시작일 이후여야 합니다.")
		}
		ft, _ := time.Parse("2006-01-02", f.From)
		if ft.AddDate(1, 0, -1).Format("2006-01-02") < f.To {
			return "", "", validation("사용자 지정 기간은 12개월까지입니다.")
		}
		return f.From, f.To, nil
	}
	return "", "", validation("집계 단위는 월·분기·사용자 지정 중에서 고르세요.")
}

// PurchaseSummary 는 기간의 표 1(매입처별 세금계산서)·표 2(미수취)·표 3(카드 매입)과 1310 검산이다.
func (s *VATService) PurchaseSummary(ctx context.Context, f domain.VATFilter) (domain.VATPurchase, error) {
	var out domain.VATPurchase
	if _, err := contract.Authorize(ctx, contract.PermVATRead); err != nil {
		return out, err
	}
	from, to, err := s.vatPeriod(f)
	if err != nil {
		return out, err
	}
	out.From, out.To = from, to
	// 신고 단위는 사업자(법인)라 "전체" 모드로는 집계하지 않는다(B44 14.2). 연결 재무 집계(consolidated:read)가 따로 있다.
	if a, has := contract.ActiveEntityFrom(ctx); has && a.All {
		return out, validation("부가세 집계는 법인 하나를 골라야 합니다. 상단에서 법인을 고르세요.")
	}
	sc, err := s.siteScope(ctx, f.SiteID)
	if err != nil {
		return out, err
	}
	sites := sc.ids
	out.SiteID, out.SiteName, out.ConsolidatedIn, out.SiteScoped = sc.selected.ID, sc.selected.Name, sc.consolidated, f.SiteID != 0
	if out.Vendors, out.Unreceived, err = s.Repo.Purchase(ctx, s.DB, from, to, sites); err != nil {
		return out, err
	}
	if out.Cards, err = s.Repo.Cards(ctx, s.DB, from, to, sites); err != nil {
		return out, err
	}
	for _, v := range out.Vendors {
		out.VendorVAT += v.VAT
	}
	for _, u := range out.Unreceived {
		out.UnreceivedVAT += u.VAT
	}
	for _, c := range out.Cards {
		if c.Kind == "deductible" {
			out.CardDeductVAT += c.VAT
		}
	}
	// 표 4(수신했지만 연결되지 않은 세금계산서)는 전표가 아직 없어 사업장을 알 수 없다. 사업장 단위 집계에서는 법인 단위로만 보인다.
	if !out.SiteScoped {
		if out.Unlinked, err = s.Repo.Unlinked(ctx, s.DB, from, to); err != nil {
			return out, err
		}
	}
	for _, u := range out.Unlinked {
		out.UnlinkedVAT += u.VAT
	}
	if out.Diffs, err = s.Repo.Diffs(ctx, s.DB, from, to, sites); err != nil {
		return out, err
	}
	out.DeductibleVAT = out.VendorVAT + out.CardDeductVAT
	if out.LedgerVAT, err = s.Repo.LedgerVAT(ctx, s.DB, from, to, sites); err != nil {
		return out, err
	}
	if out.ExcludedCount, err = s.Repo.PendingPurchaseJournals(ctx, s.DB, from, to, sites); err != nil {
		return out, err
	}
	if out.Sites, err = s.siteRows(ctx, sc.all, from, to); err != nil {
		return out, err
	}
	out.Difference = out.LedgerVAT - out.DeductibleVAT
	out.Matches = out.Difference == 0
	if !out.Matches {
		rest := out.Difference
		if out.UnreceivedVAT > 0 && out.UnreceivedVAT <= rest {
			out.Hints = append(out.Hints, fmt.Sprintf("차이 중 %s은 세금계산서를 받지 못한 매입의 부가세입니다. 세금계산서를 받으면 공제에 들어갑니다.", moneyLabel(out.UnreceivedVAT)))
			rest -= out.UnreceivedVAT
		}
		if rest != 0 {
			out.Hints = append(out.Hints, fmt.Sprintf("나머지 %s은 승인 전 전표나 전기일과 거래일의 달 차이, 일반 전표로 입력한 부가세대급금일 수 있습니다.", moneyLabel(rest)))
		}
	}
	return out, nil
}

// vatSiteScope 는 사업장 선택을 전표의 site_id 집합으로 푼 결과다.
type vatSiteScope struct {
	ids          []int64 // nil = 법인 전체
	selected     domain.SiteInfo
	consolidated []string // 주사업장 집계에 합산된 총괄납부 사업장 이름
	all          []domain.SiteInfo
}

// siteScope 는 사업장 선택(0 = 법인 전체)을 푼다. 주사업장을 고르면 지정 없는 전표(0)와 총괄납부 사업장의 전표가 함께 들어온다(명세 14.2).
func (s *VATService) siteScope(ctx context.Context, siteID int64) (vatSiteScope, error) {
	var sc vatSiteScope
	all, err := s.Site.Sites(ctx, s.DB, contract.WriteEntityID(ctx), false)
	if err != nil {
		return sc, err
	}
	sc.all = all
	if siteID == 0 {
		return sc, nil
	}
	var head domain.SiteInfo
	found := false
	for _, t := range all {
		if t.Kind == "head" {
			head = t
		}
		if t.ID == siteID {
			sc.selected, found = t, true
		}
	}
	if !found {
		return sc, validation("사업장을 찾을 수 없습니다.")
	}
	if sc.selected.Kind != "head" {
		sc.ids = []int64{sc.selected.ID}
		return sc, nil
	}
	sc.ids = []int64{0, head.ID}
	for _, t := range all {
		if t.Kind != "head" && t.VATMode == "consolidated" {
			sc.ids = append(sc.ids, t.ID)
			sc.consolidated = append(sc.consolidated, t.Name)
		}
	}
	return sc, nil
}

// siteRows 는 법인 전체 기준의 사업장별 매입 부가세 내역이다(사업장이 둘 이상일 때만).
func (s *VATService) siteRows(ctx context.Context, all []domain.SiteInfo, from, to string) ([]domain.VATSiteRow, error) {
	if len(all) < 2 {
		return nil, nil
	}
	vat, count, err := s.Repo.SiteVAT(ctx, s.DB, from, to)
	if err != nil {
		return nil, err
	}
	rows := make([]domain.VATSiteRow, 0, len(all))
	for _, t := range all {
		r := domain.VATSiteRow{SiteID: t.ID, SiteNo: t.SiteNo, Name: t.Name, Kind: t.Kind, VATMode: t.VATMode, VAT: vat[t.ID], Count: count[t.ID]}
		if t.Kind == "head" { // 사업장이 지정되지 않은 전표(0)는 주사업장에 속한다
			r.VAT += vat[0]
			r.Count += count[0]
		}
		rows = append(rows, r)
	}
	return rows, nil
}
