package service

import (
	"context"
	"errors"
	"sort"
	"strings"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/repository"
)

// 월별 재고 현황(B27 ④, 3.4).
// 마감된 달은 저장된 스냅샷(최신 버전), 그 밖의 달은 원장·평가 표에서 실시간 계산한 잠정 값이다. 수량은 inventory:read, 금액은 inventory:valuation_read 가 있을 때만 채운다(서비스가 거른다).

// MonthStockService 는 domain.MonthStockService 의 구현이다.
type MonthStockService struct {
	Base
	Repo repository.SQLMonthClose
}

var _ domain.MonthStockService = (*MonthStockService)(nil)

func NewMonthStockService(b Base) *MonthStockService { return &MonthStockService{Base: b} }

func toStockRow(l repository.SnapLine, n repository.StockNames) domain.MonthStockRow {
	it := n.Items[l.ItemID]
	wh := n.Warehouses[l.WarehouseID]
	return domain.MonthStockRow{ItemID: l.ItemID, SKU: it[0], ItemName: it[1], WarehouseID: l.WarehouseID, WarehouseName: wh.Name, WarehouseActive: wh.Active,
		OpenQty: l.OpenQty, InQty: l.InQty, OutQty: l.OutQty, TransferQty: l.TransferQty, AdjustQty: l.AdjustQty, CloseQty: l.CloseQty,
		OpenAmount: l.OpenAmount, InAmount: l.InAmount, OutAmount: l.OutAmount, TransferAmount: l.TransferAmount, AdjustAmount: l.AdjustAmount, CloseAmount: l.CloseAmount, AvgCost: l.AvgCost}
}

// Report 는 한 달의 월별 재고 현황이다. 현재 법인 하나의 값이고, 전체 모드(그룹 범위)는 법인별로 나누어 보인다(B51 D14).
func (s *MonthStockService) Report(ctx context.Context, f domain.MonthStockFilter) (domain.MonthStockReport, error) {
	p, err := contract.Authorize(ctx, contract.PermInventoryRead)
	if err != nil {
		return domain.MonthStockReport{}, err
	}
	if a, has := contract.ActiveEntityFrom(ctx); has && a.All {
		if _, err := contract.ReadScope(ctx, contract.PermInventoryRead); err != nil {
			return domain.MonthStockReport{}, err
		}
		return s.reportAll(ctx, p, f)
	}
	return s.reportOne(ctx, p, f, false)
}

// reportAll 은 전체 모드의 월별 재고 현황이다: 활성 법인마다 그 법인 context 로 보고서를 만들어 줄을 법인 번호 순으로 이어 붙인다.
// 단가·평균은 합산하지 않고 비우며, 합계 줄은 수량·금액만 더한다. 법인이 하나뿐이면 나누지 않는다(그 법인의 보고서 그대로).
func (s *MonthStockService) reportAll(ctx context.Context, p contract.Principal, f domain.MonthStockFilter) (domain.MonthStockReport, error) {
	ents, err := s.Repo.ActiveEntities(ctx, s.DB)
	if err != nil {
		return domain.MonthStockReport{}, err
	}
	if len(ents) == 1 {
		return s.reportOne(contract.WithActiveEntity(ctx, contract.ActiveEntity{ID: ents[0].ID}), p, f, false)
	}
	out := domain.MonthStockReport{AllEntities: true, Rows: []domain.MonthStockRow{}}
	for i, e := range ents {
		rep, err := s.reportOne(contract.WithActiveEntity(ctx, contract.ActiveEntity{ID: e.ID}), p, f, true)
		if err != nil {
			return domain.MonthStockReport{}, err
		}
		if i == 0 {
			out.Month, out.HasAmounts = rep.Month, rep.HasAmounts
		}
		out.Provisional = out.Provisional || rep.Provisional
		out.PrevClosed = out.PrevClosed || rep.PrevClosed
		out.ExcludedLines += rep.ExcludedLines
		for _, r := range rep.Rows {
			r.EntityID, r.EntityName, r.AvgCost = e.ID, e.Name, 0
			out.Rows = append(out.Rows, r)
		}
		t := rep.Total
		out.EntityTotals = append(out.EntityTotals, domain.MonthStockEntityTotal{EntityID: e.ID, EntityName: e.Name, Total: t})
		out.Total.OpenQty += t.OpenQty
		out.Total.InQty += t.InQty
		out.Total.OutQty += t.OutQty
		out.Total.TransferQty += t.TransferQty
		out.Total.AdjustQty += t.AdjustQty
		out.Total.CloseQty += t.CloseQty
		out.Total.OpenAmount += t.OpenAmount
		out.Total.InAmount += t.InAmount
		out.Total.OutAmount += t.OutAmount
		out.Total.TransferAmount += t.TransferAmount
		out.Total.AdjustAmount += t.AdjustAmount
		out.Total.CloseAmount += t.CloseAmount
		out.Total.PrevAmount += t.PrevAmount
		if t.RoundDiff != nil {
			d := *t.RoundDiff
			if out.Total.RoundDiff != nil {
				d += *out.Total.RoundDiff
			}
			out.Total.RoundDiff = &d
		}
	}
	return out, nil
}

// reportOne 은 현재 법인 하나의 월별 재고 현황이다. lenient 는 전체 모드에서 창고 조건이 다른 법인의 창고를 가리킬 때 오류 대신 빈 보고서로 돌려준다.
func (s *MonthStockService) reportOne(ctx context.Context, p contract.Principal, f domain.MonthStockFilter, lenient bool) (domain.MonthStockReport, error) {
	month := strings.TrimSpace(f.Month)
	if month == "" {
		month = addMonths(budgetMonthOf(s.now()), -1)
	}
	t, err := parseMonthText(month)
	if err != nil {
		return domain.MonthStockReport{}, err
	}
	month = t.Format("2006-01")
	if month > budgetMonthOf(s.now()) {
		return domain.MonthStockReport{}, validation("앞으로 올 달은 조회할 수 없습니다")
	}
	if f.WarehouseID != 0 {
		if _, ok := (repository.SQLStock{}).Warehouse(ctx, s.DB, f.WarehouseID); ok != nil {
			if lenient && errors.Is(ok, contract.ErrNotFound) { // 전체 모드: 그 창고는 다른 법인에 있다
				return domain.MonthStockReport{Month: month, HasAmounts: p.Can(contract.PermInventoryValuation), Rows: []domain.MonthStockRow{}}, nil
			}
			return domain.MonthStockReport{}, validation("알 수 없는 창고입니다")
		}
	}
	amounts := p.Can(contract.PermInventoryValuation)
	rep := domain.MonthStockReport{Month: month, HasAmounts: amounts}
	start, end := monthRange(t)
	names, err := s.Repo.Names(ctx, s.DB)
	if err != nil {
		return rep, err
	}
	rec, have, err := s.Repo.Get(ctx, s.DB, month)
	if err != nil {
		return rep, err
	}
	var lines []repository.SnapLine
	if have && rec.Status == "closed" {
		rep.Version = rec.Version
		lines, err = s.Repo.SnapshotLines(ctx, s.DB, month, rec.Version)
		if err == nil {
			rep.ExcludedLines, err = s.Repo.SnapshotForeignLines(ctx, s.DB, month, rec.Version)
		}
	} else {
		rep.Provisional = true
		lines, err = s.Repo.BuildSnapshot(ctx, s.DB, start, end)
	}
	if err != nil {
		return rep, err
	}
	// 전월 대비: 전월이 마감되어 있을 때만.
	prev := map[[2]int64]int64{}
	if amounts {
		pm := addMonths(month, -1)
		if pr, ok, err := s.Repo.Get(ctx, s.DB, pm); err != nil {
			return rep, err
		} else if ok && pr.Status == "closed" {
			rep.PrevClosed = true
			pl, err := s.Repo.SnapshotLines(ctx, s.DB, pm, pr.Version)
			if err != nil {
				return rep, err
			}
			for _, l := range pl {
				prev[[2]int64{l.ItemID, l.WarehouseID}] = l.CloseAmount
			}
		}
	}
	q := strings.ToLower(strings.TrimSpace(f.Query))
	allItems := map[int64]bool{}
	var closeSum int64
	for _, l := range lines {
		row := toStockRow(l, names)
		if f.WarehouseID != 0 && row.WarehouseID != f.WarehouseID {
			continue
		}
		if q != "" && !strings.Contains(strings.ToLower(row.SKU), q) && !strings.Contains(strings.ToLower(row.ItemName), q) {
			continue
		}
		if !amounts {
			row.OpenAmount, row.InAmount, row.OutAmount, row.TransferAmount, row.AdjustAmount, row.CloseAmount, row.AvgCost = 0, 0, 0, 0, 0, 0, 0
		} else if rep.PrevClosed {
			pv := prev[[2]int64{row.ItemID, row.WarehouseID}]
			d := row.CloseAmount - pv
			row.PrevCloseAmount, row.Delta = &pv, &d
			if pv != 0 {
				pct := int(d * 100 / pv)
				row.DeltaPct = &pct
			}
			rep.Total.PrevAmount += pv
		}
		allItems[row.ItemID] = true
		closeSum += row.CloseAmount
		rep.Total.OpenQty += row.OpenQty
		rep.Total.InQty += row.InQty
		rep.Total.OutQty += row.OutQty
		rep.Total.TransferQty += row.TransferQty
		rep.Total.AdjustQty += row.AdjustQty
		rep.Total.CloseQty += row.CloseQty
		rep.Total.OpenAmount += row.OpenAmount
		rep.Total.InAmount += row.InAmount
		rep.Total.OutAmount += row.OutAmount
		rep.Total.TransferAmount += row.TransferAmount
		rep.Total.AdjustAmount += row.AdjustAmount
		rep.Total.CloseAmount += row.CloseAmount
		rep.Rows = append(rep.Rows, row)
	}
	sort.SliceStable(rep.Rows, func(i, j int) bool {
		a, b := rep.Rows[i], rep.Rows[j]
		if a.SKU != b.SKU {
			return a.SKU < b.SKU
		}
		return a.WarehouseID < b.WarehouseID
	})
	if rep.Rows == nil {
		rep.Rows = []domain.MonthStockRow{}
	}
	// 반올림 차이(D12): 필터가 없을 때 품목 잔액 금액과 창고별 기말 금액 합의 차이를 합계 줄에 따로 보인다.
	if amounts && f.WarehouseID == 0 && q == "" {
		var bal int64
		for id := range allItems {
			b, err := s.Repo.ItemBalanceAt(ctx, s.DB, id, end)
			if err != nil {
				return rep, err
			}
			bal += b
		}
		diff := bal - closeSum
		rep.Total.RoundDiff = &diff
	}
	return rep, nil
}
