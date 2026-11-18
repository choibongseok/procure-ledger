package service

import (
	"context"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/repository"
)

// 연결 재무 집계(B44 3단계, 명세 11.2). 합산만 한다: 상계·조정 분개·지분법은 없고 전표 원본에서 매번 계산한다(스냅샷 표 없음).
// 호출자가 consolidated:read 를 가진 법인만 열이 되고(그룹 범위면 전체) 현재 법인 선택과는 무관하다. 읽기 전용이라 감사 기록은 남기지 않는다(다른 재무 조회와 같다).

// ConsolidatedService 는 domain.ConsolidatedService 의 구현이다.
type ConsolidatedService struct {
	Base
	Repo  repository.SQLConsolidated
	Close repository.SQLMonthClose
}

var _ domain.ConsolidatedService = (*ConsolidatedService)(nil)

func NewConsolidatedService(b Base) *ConsolidatedService { return &ConsolidatedService{Base: b} }

// consolidatedPeriod 는 필터를 [from, to] 날짜와 그 안의 달 목록으로 바꾼다.
func (b Base) consolidatedPeriod(f domain.ConsolidatedFilter) (from, to string, months []string, err error) {
	var first, last time.Time
	switch f.Unit {
	case "", "month":
		m := f.Month
		if m == "" {
			m = b.kstDay()[:7]
		}
		first, err = time.Parse("2006-01", m)
		if err != nil {
			return "", "", nil, validation("월은 YYYY-MM 형식으로 입력하세요.")
		}
		last = first
	case "quarter":
		y, q := f.Year, f.Quarter
		if y == 0 {
			y = b.now().In(contract.KST).Year()
		}
		if q < 1 || q > 4 {
			return "", "", nil, validation("분기는 1~4 로 고르세요.")
		}
		first = time.Date(y, time.Month((q-1)*3+1), 1, 0, 0, 0, 0, time.UTC)
		last = first.AddDate(0, 2, 0)
	case "year":
		y := f.Year
		if y == 0 {
			y = b.now().In(contract.KST).Year()
		}
		if y < 2000 || y > 2100 {
			return "", "", nil, validation("연도를 확인하세요.")
		}
		first = time.Date(y, 1, 1, 0, 0, 0, 0, time.UTC)
		last = time.Date(y, 12, 1, 0, 0, 0, 0, time.UTC)
	default:
		return "", "", nil, validation("집계 단위는 월·분기·연 중에서 고르세요.")
	}
	for m := first; !m.After(last); m = m.AddDate(0, 1, 0) {
		months = append(months, m.Format("2006-01"))
	}
	return first.Format("2006-01-02"), last.AddDate(0, 1, -1).Format("2006-01-02"), months, nil
}

var consSummaryKinds = []struct{ kind, label string }{
	{"asset", "자산"}, {"liability", "부채"}, {"equity", "자본"}, {"revenue", "수익"}, {"expense", "비용"},
}

// Summary 는 표 1(법인별 시산표)·표 2(재무 요약)·표 3(내부 거래 참고)이다.
func (s *ConsolidatedService) Summary(ctx context.Context, f domain.ConsolidatedFilter) (domain.Consolidated, error) {
	var out domain.Consolidated
	p, err := contract.Authorize(ctx, contract.PermConsolidatedRead)
	if err != nil {
		return out, err
	}
	from, to, months, err := s.consolidatedPeriod(f)
	if err != nil {
		return out, err
	}
	out.From, out.To = from, to
	scope := p.EntitiesFor(contract.PermConsolidatedRead).ScopeOf()
	ents, allActive, err := s.Repo.Entities(ctx, s.DB, scope)
	if err != nil {
		return out, err
	}
	balances, err := s.Repo.Balances(ctx, s.DB, scope, from, to)
	if err != nil {
		return out, err
	}
	hasData := map[int64]bool{}
	for _, b := range balances {
		hasData[b.EntityID] = true
	}
	// 비활성 법인은 기간에 데이터가 있을 때만 열로 둔다(과거 기록은 계속 합산에 들어간다).
	cols := make([]repository.ConsEntity, 0, len(ents))
	idx := map[int64]int{}
	activeSeen := 0
	for _, e := range ents {
		if !e.Active && !hasData[e.ID] {
			continue
		}
		if e.Active {
			activeSeen++
		}
		idx[e.ID] = len(cols)
		cols = append(cols, e)
	}
	out.PartialScope = activeSeen < allActive
	closed, err := s.Close.ClosedMonthsIn(ctx, s.DB, scope, months[0], months[len(months)-1])
	if err != nil {
		return out, err
	}
	out.Entities = make([]domain.ConsolidatedEntity, len(cols))
	for i, e := range cols {
		out.Entities[i] = domain.ConsolidatedEntity{ID: e.ID, Code: e.Code, Name: e.Name, ClosedMonths: closed[e.ID], Months: len(months), Closed: closed[e.ID] == len(months)}
	}
	// 표 1: 계정 × 법인(정상 방향). 계정 순서는 쿼리가 코드 순으로 준다.
	byCode := map[string]int{}
	for _, b := range balances {
		i, ok := idx[b.EntityID]
		if !ok {
			continue
		}
		r, ok := byCode[b.Code]
		if !ok {
			r = len(out.Trial)
			byCode[b.Code] = r
			out.Trial = append(out.Trial, domain.ConsolidatedAccount{Code: b.Code, Name: b.Name, Kind: b.Kind, Amounts: make([]int64, len(cols))})
		}
		v := b.Net
		if b.Kind == "liability" || b.Kind == "equity" || b.Kind == "revenue" {
			v = -v
		}
		out.Trial[r].Amounts[i] = v
		out.Trial[r].Total += v
	}
	// 표 2: 5대 분류와 순손익(수익 − 비용).
	sums := map[string]*domain.ConsolidatedSummaryRow{}
	for _, k := range consSummaryKinds {
		row := &domain.ConsolidatedSummaryRow{Kind: k.kind, Label: k.label, Amounts: make([]int64, len(cols))}
		sums[k.kind] = row
	}
	for _, a := range out.Trial {
		row := sums[a.Kind]
		for i, v := range a.Amounts {
			row.Amounts[i] += v
		}
		row.Total += a.Total
	}
	for _, k := range consSummaryKinds {
		out.Summary = append(out.Summary, *sums[k.kind])
	}
	net := domain.ConsolidatedSummaryRow{Kind: "net", Label: "순손익(수익 − 비용)", Amounts: make([]int64, len(cols))}
	for i := range cols {
		net.Amounts[i] = sums["revenue"].Amounts[i] - sums["expense"].Amounts[i]
	}
	net.Total = sums["revenue"].Total - sums["expense"].Total
	out.Summary = append(out.Summary, net)
	if out.Pending, err = s.Repo.PendingCount(ctx, s.DB, scope, from, to); err != nil {
		return out, err
	}
	// 표 3: 법인 쌍 참고. 상대가 이쪽을 거래처로 기록한 자산 줄은 Receivable 로 맞대어 차이만 보인다(합계·상계에 넣지 않는다).
	lines, err := s.Repo.Intercompany(ctx, s.DB, scope, from, to)
	if err != nil {
		return out, err
	}
	type key struct{ from, to int64 }
	pairIdx := map[key]int{}
	meta := map[int64]repository.ConsEntity{}
	for _, e := range ents {
		meta[e.ID] = e
	}
	assets := map[key]int64{}
	counter := map[key]bool{}
	for _, l := range lines {
		k := key{l.FromID, l.ToID}
		if l.Kind == "asset" {
			assets[k] += l.Debit - l.Credit
		}
		counter[k] = true
	}
	for _, l := range lines {
		k := key{l.FromID, l.ToID}
		i, ok := pairIdx[k]
		if !ok {
			i = len(out.Intercompany)
			pairIdx[k] = i
			fm, tm := meta[l.FromID], meta[l.ToID]
			out.Intercompany = append(out.Intercompany, domain.ConsolidatedPair{FromID: l.FromID, ToID: l.ToID, FromCode: fm.Code, FromName: fm.Name, ToCode: tm.Code, ToName: tm.Name})
		}
		pr := &out.Intercompany[i]
		pr.Lines = append(pr.Lines, domain.ConsolidatedPairLine{Code: l.Code, Name: l.Name, Debit: l.Debit, Credit: l.Credit})
		if l.Kind == "liability" {
			pr.Payable += l.Credit - l.Debit
		}
	}
	for i := range out.Intercompany {
		pr := &out.Intercompany[i]
		rev := key{pr.ToID, pr.FromID}
		pr.CounterpartRecorded = counter[rev]
		pr.Receivable = assets[rev]
		pr.Gap = pr.Payable - pr.Receivable
	}
	return out, nil
}
