// 월별 재고 현황 라우트와 내보내기 원천(B27 단계 3, 3.4·9절). 권한은 등록기가 선언하고 서비스가 한 번 더 확인한다.
package handler

import (
	"context"
	"net/http"
	"net/url"
	"strconv"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/deps"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/export"
)

// RegisterMonthStock 은 /api/inventory/month-stock 과 그 내보내기를 등록한다. 호출: cmd/server/routes_erp.go.
// 내보내기는 수량만(inventory:read)과 금액 포함(inventory:valuation_read) 두 데이터셋이다: 금액 열은 권한이 없으면 파일에도 없다.
func RegisterMonthStock(r contract.Registrar, d deps.Deps) {
	if d.MonthStock == nil {
		return
	}
	h := monthStockHandler{svc: d.MonthStock}
	r.HandleFunc("GET", "/api/inventory/month-stock", contract.PermInventoryRead, h.report)
	if d.Export != nil {
		export.Register(r, "/api/inventory/month-stock/export", contract.PermInventoryRead, d.Export, &monthStockSource{svc: d.MonthStock})
		export.Register(r, "/api/inventory/month-stock/export-valued", contract.PermInventoryValuation, d.Export, &monthStockSource{svc: d.MonthStock, valued: true})
	}
}

type monthStockHandler struct{ svc domain.MonthStockService }

func filterOf(q url.Values) domain.MonthStockFilter {
	wh, _ := strconv.ParseInt(q.Get("warehouseId"), 10, 64)
	return domain.MonthStockFilter{Month: q.Get("month"), WarehouseID: wh, Query: q.Get("q")}
}

func ptrOrNil[T any](p *T) any {
	if p == nil {
		return nil
	}
	return *p
}

func (h monthStockHandler) report(w http.ResponseWriter, r *http.Request) {
	rep, err := h.svc.Report(r.Context(), filterOf(r.URL.Query()))
	if err != nil {
		writeErr(w, err)
		return
	}
	rows := make([]map[string]any, len(rep.Rows))
	for i, x := range rep.Rows {
		m := map[string]any{"itemId": x.ItemID, "sku": x.SKU, "itemName": x.ItemName, "warehouseId": x.WarehouseID, "warehouseName": x.WarehouseName, "warehouseActive": x.WarehouseActive,
			"openQty": x.OpenQty, "inQty": x.InQty, "outQty": x.OutQty, "transferQty": x.TransferQty, "adjustQty": x.AdjustQty, "closeQty": x.CloseQty}
		if rep.AllEntities { // 전체 모드(B51 D14): 법인별 줄이고 단가·평균은 내려 보내지 않는다
			m["entityId"], m["entityName"] = x.EntityID, x.EntityName
		}
		if rep.HasAmounts {
			m["openAmount"], m["inAmount"], m["outAmount"], m["transferAmount"], m["adjustAmount"], m["closeAmount"] = x.OpenAmount, x.InAmount, x.OutAmount, x.TransferAmount, x.AdjustAmount, x.CloseAmount
			if !rep.AllEntities {
				m["avgCost"] = x.AvgCost
			}
			if x.Delta != nil {
				m["prevCloseAmount"], m["delta"], m["deltaPct"] = ptrOrNil(x.PrevCloseAmount), ptrOrNil(x.Delta), ptrOrNil(x.DeltaPct)
			}
		}
		rows[i] = m
	}
	t := rep.Total
	total := map[string]any{"openQty": t.OpenQty, "inQty": t.InQty, "outQty": t.OutQty, "transferQty": t.TransferQty, "adjustQty": t.AdjustQty, "closeQty": t.CloseQty}
	if rep.HasAmounts {
		total["openAmount"], total["inAmount"], total["outAmount"], total["transferAmount"], total["adjustAmount"], total["closeAmount"], total["prevAmount"] = t.OpenAmount, t.InAmount, t.OutAmount, t.TransferAmount, t.AdjustAmount, t.CloseAmount, t.PrevAmount
		total["roundDiff"] = ptrOrNil(t.RoundDiff)
	}
	body := map[string]any{"month": rep.Month, "provisional": rep.Provisional, "version": rep.Version, "hasAmounts": rep.HasAmounts, "prevClosed": rep.PrevClosed, "rows": rows, "total": total}
	if rep.AllEntities { // 법인별 소계(수량·금액만)
		body["allEntities"] = true
		subs := make([]map[string]any, len(rep.EntityTotals))
		for i, e := range rep.EntityTotals {
			sub := map[string]any{"entityId": e.EntityID, "entityName": e.EntityName, "openQty": e.Total.OpenQty, "inQty": e.Total.InQty, "outQty": e.Total.OutQty, "transferQty": e.Total.TransferQty, "adjustQty": e.Total.AdjustQty, "closeQty": e.Total.CloseQty}
			if rep.HasAmounts {
				sub["openAmount"], sub["inAmount"], sub["outAmount"], sub["closeAmount"] = e.Total.OpenAmount, e.Total.InAmount, e.Total.OutAmount, e.Total.CloseAmount
			}
			subs[i] = sub
		}
		body["entityTotals"] = subs
	}
	if rep.ExcludedLines > 0 { // 법인을 나누기 전에 마감한 달의 스냅샷에서 제외한 줄 수(B51 5.4)
		body["excludedLines"] = rep.ExcludedLines
	}
	writeJSON(w, http.StatusOK, body)
}

// ---- 내보내기 원천 ----

type monthStockSource struct {
	svc    domain.MonthStockService
	valued bool
}

func (s *monthStockSource) Dataset() export.Dataset { return s.dataset(false) }

// DatasetFor 는 전체 모드(entity=*)의 파일 정의다: 법인 열이 맨 앞에 붙고 평균 단가 열은 비운다(B51 4.3·8.2).
func (s *monthStockSource) DatasetFor(ctx context.Context) export.Dataset {
	a, ok := contract.ActiveEntityFrom(ctx)
	return s.dataset(ok && a.All)
}

func (s *monthStockSource) dataset(all bool) export.Dataset {
	cols := []export.Column{{Label: "품목 코드"}, {Label: "품목"}, {Label: "창고"},
		{Label: "기초 수량", Kind: export.KindInt}, {Label: "입고 수량", Kind: export.KindInt}, {Label: "출고 수량", Kind: export.KindInt},
		{Label: "이동 순증감", Kind: export.KindInt}, {Label: "조정 수량", Kind: export.KindInt}, {Label: "기말 수량", Kind: export.KindInt}}
	if all {
		cols = append([]export.Column{{Label: "법인"}}, cols...)
	}
	if !s.valued {
		return export.Dataset{Code: "month_stock_qty", Label: "월별재고수량", Columns: cols}
	}
	cols = append(cols, export.Column{Label: "평균 단가(원)", Kind: export.KindMoney}, export.Column{Label: "기초 금액(원)", Kind: export.KindMoney}, export.Column{Label: "입고 금액(원)", Kind: export.KindMoney},
		export.Column{Label: "출고 금액(원)", Kind: export.KindMoney}, export.Column{Label: "기말 금액(원)", Kind: export.KindMoney},
		export.Column{Label: "전월 대비 증감(원)", Kind: export.KindMoney}, export.Column{Label: "증감률(%)", Kind: export.KindPercent})
	return export.Dataset{Code: "month_stock", Label: "월별재고현황", Columns: cols}
}

func (s *monthStockSource) Filters(q url.Values) []export.FilterItem {
	return []export.FilterItem{export.Month("월", q.Get("month")), export.ID("창고", q.Get("warehouseId")), export.Free("검색어", q.Get("q"))}
}

func (s *monthStockSource) Count(ctx context.Context, q url.Values, _ int) (int, bool, error) {
	rep, err := s.svc.Report(ctx, filterOf(q))
	return len(rep.Rows), false, err
}

func (s *monthStockSource) Rows(ctx context.Context, q url.Values, _ bool, max int) ([][]export.Cell, error) {
	rep, err := s.svc.Report(ctx, filterOf(q))
	if err != nil {
		return nil, err
	}
	all := rep.AllEntities
	out := make([][]export.Cell, 0, len(rep.Rows)+1)
	for _, x := range rep.Rows {
		if len(out) > max {
			break
		}
		wh := x.WarehouseName
		if !x.WarehouseActive {
			wh += "(중지)"
		}
		row := []export.Cell{x.SKU, x.ItemName, wh, x.OpenQty, x.InQty, x.OutQty, x.TransferQty, x.AdjustQty, x.CloseQty}
		if s.valued {
			var delta, pct any
			if x.Delta != nil {
				delta = *x.Delta
			}
			if x.DeltaPct != nil {
				pct = float64(*x.DeltaPct)
			}
			var avg any = x.AvgCost
			if all { // 법인마다 단가가 달라 전체 모드 파일은 평균 단가를 비운다
				avg = nil
			}
			row = append(row, avg, x.OpenAmount, x.InAmount, x.OutAmount, x.CloseAmount, delta, pct)
		}
		if all {
			row = append([]export.Cell{x.EntityName}, row...)
		}
		out = append(out, row)
	}
	// 합계 줄(화면과 같다).
	t := rep.Total
	tot := []export.Cell{"합계", nil, nil, t.OpenQty, t.InQty, t.OutQty, t.TransferQty, t.AdjustQty, t.CloseQty}
	if s.valued {
		tot = append(tot, nil, t.OpenAmount, t.InAmount, t.OutAmount, t.CloseAmount, nil, nil)
	}
	if all {
		tot = append([]export.Cell{nil}, tot...)
	}
	return append(out, tot), nil
}
