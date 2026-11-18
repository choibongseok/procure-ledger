// 목록 내보내기 원천(B28, 4절): 구매요청·발주·입고·재고 현황·수불부·문서함·예산 현황.
// 원천은 화면 목록이 부르는 같은 서비스 함수를 쪽 나누기 없이 부른다(열람 범위·부서 격리는 서비스가 적용한다). 권한은 그 목록을 읽는 권한과 같다.
// 금액 열은 권한이 다른 데이터셋으로 나눈다(수불부 수량/금액): 권한이 없으면 파일에도 열이 없다.
package handler

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/deps"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/export"
)

// RegisterListExports 는 erp 소유 목록의 내보내기 라우트를 등록한다. 호출: cmd/server/routes_erp.go.
func RegisterListExports(r contract.Registrar, d deps.Deps) {
	if d.Export == nil {
		return
	}
	if lister, ok := d.Purchase.(interface {
		ListAll(ctx context.Context, f domain.PRFilter, max int) ([]domain.PurchaseRequest, error)
	}); ok {
		export.Register(r, "/api/purchase-requests/export", contract.PermPurchaseRead, d.Export, prExport(lister))
	}
	if d.ExportLists != nil {
		export.Register(r, "/api/purchase-orders/export", contract.PermInventoryRead, d.Export, poExport(d.ExportLists))
		export.Register(r, "/api/receipts/export", contract.PermInventoryRead, d.Export, receiptExport(d.ExportLists))
		r.HandleFunc("GET", "/api/receipt-history", contract.PermInventoryRead, receiptHistoryHandler{svc: d.ExportLists}.list)
		export.Register(r, "/api/stock/export", contract.PermInventoryRead, d.Export, stockExport(d.ExportLists))
	}
	if d.Ledger != nil {
		export.Register(r, "/api/stock-ledger/export", contract.PermInventoryRead, d.Export, ledgerExport(d.Ledger, false))
		export.Register(r, "/api/stock-ledger/export-valued", contract.PermInventoryValuation, d.Export, ledgerExport(d.Ledger, true))
	}
	if d.Inbox != nil {
		export.Register(r, "/api/inbox/export", contract.PermInboxRead, d.Export, inboxExport(d.Inbox))
	}
	if d.Budget != nil {
		export.Register(r, "/api/budgets/export", contract.PermBudgetRead, d.Export, budgetExport(d.Budget))
	}
}

// listSource 는 한 번에 max+1 행까지 읽어 오는 함수 하나로 Source 를 만든다. 건수 확인과 행 읽기가 같은 함수라 두 값이 어긋나지 않는다.
type listSource struct {
	ds      export.Dataset
	filters func(url.Values) []export.FilterItem
	fetch   func(ctx context.Context, q url.Values, max int) ([][]export.Cell, error)
}

func (s *listSource) Dataset() export.Dataset                  { return s.ds }
func (s *listSource) Filters(q url.Values) []export.FilterItem { return s.filters(q) }

func (s *listSource) Count(ctx context.Context, q url.Values, max int) (int, bool, error) {
	rows, err := s.fetch(ctx, q, max)
	return len(rows), len(rows) > max, err
}

func (s *listSource) Rows(ctx context.Context, q url.Values, _ bool, max int) ([][]export.Cell, error) {
	return s.fetch(ctx, q, max)
}

func idParam(q url.Values, k string) int64 {
	n, _ := strconv.ParseInt(q.Get(k), 10, 64)
	return n
}

// ---- 열 이름·값 표기(화면 web/src/purchase/labels.ts 와 같은 한국어) ----

var prStatusLabel = map[domain.PRStatus]string{
	domain.PRDraft: "임시저장", domain.PRSubmitted: "상신", domain.PRInReview: "결재중", domain.PRRejected: "반려", domain.PRApproved: "승인", domain.PROrdered: "발주완료",
}

var poStatusLabel = map[domain.POStatus]string{domain.POOpen: "입고 전", domain.POPartial: "부분 입고", domain.POReceived: "입고 완료"}

var receiptStatusLabel = map[string]string{"pending": "접수 대기", "received": "접수됨", "assigned": "담당자 지정", "done": "처리 완료", "returned": "반송"}

var budgetBadgeLabel = map[string]string{domain.BadgeOK: "여유", domain.BadgeNear: "한도 근접", domain.BadgeOver: "예산 초과"}

func labelOr(m map[string]string, k string) any {
	if v, ok := m[k]; ok {
		return v
	}
	return "알 수 없음"
}

func dashIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// ---- 구매요청 ----

type prLister interface {
	ListAll(ctx context.Context, f domain.PRFilter, max int) ([]domain.PurchaseRequest, error)
}

func prExport(l prLister) *listSource {
	return &listSource{
		ds: export.Dataset{Code: "purchase_requests", Label: "구매요청", Columns: []export.Column{
			{Label: "번호"}, {Label: "제목"}, {Label: "요청자"}, {Label: "소속"}, {Label: "금액(원)", Kind: export.KindMoney}, {Label: "상태"}, {Label: "작성일", Kind: export.KindDateTime}}},
		filters: func(q url.Values) []export.FilterItem {
			return []export.FilterItem{export.Enum("상태", q.Get("status"), prStatusLabelByString), export.Date("작성일 시작", q.Get("from")), export.Date("작성일 종료", q.Get("to")),
				export.ID("부서", q.Get("departmentId")), export.ID("요청자", q.Get("requesterId")), export.Free("검색어", q.Get("q"))}
		},
		fetch: func(ctx context.Context, q url.Values, max int) ([][]export.Cell, error) {
			f := domain.PRFilter{Status: domain.PRStatus(q.Get("status")), DepartmentID: idParam(q, "departmentId"), RequesterID: idParam(q, "requesterId"),
				From: q.Get("from"), To: q.Get("to"), Query: strings.TrimSpace(q.Get("q"))}
			prs, err := l.ListAll(ctx, f, max)
			if err != nil {
				return nil, err
			}
			out := make([][]export.Cell, len(prs))
			for i, p := range prs {
				out[i] = []export.Cell{p.No, p.Title, p.RequesterName, dashIfEmpty(p.RequesterUnitPath), p.TotalAmount, prStatusLabel[p.Status], p.CreatedAt}
			}
			return out, nil
		},
	}
}

var prStatusLabelByString = func() map[string]string {
	m := map[string]string{}
	for k, v := range prStatusLabel {
		m[string(k)] = v
	}
	return m
}()

// ---- 발주 ----

func poExport(s domain.ExportListService) *listSource {
	return &listSource{
		ds: export.Dataset{Code: "purchase_orders", Label: "발주", Columns: []export.Column{
			{Label: "발주 번호"}, {Label: "상태"}, {Label: "금액(원)", Kind: export.KindMoney}, {Label: "거래처"}, {Label: "발주일", Kind: export.KindDateTime}, {Label: "납기", Kind: export.KindDate}, {Label: "납품처"}}},
		filters: func(q url.Values) []export.FilterItem {
			return []export.FilterItem{export.Enum("상태", q.Get("status"), map[string]string{"open": "입고 전", "partial": "부분 입고", "received": "입고 완료"})}
		},
		fetch: func(ctx context.Context, q url.Values, max int) ([][]export.Cell, error) {
			rows, err := s.PurchaseOrders(ctx, domain.POExportFilter{Status: domain.POStatus(q.Get("status"))}, max+1)
			if err != nil {
				return nil, err
			}
			out := make([][]export.Cell, len(rows))
			for i, x := range rows {
				out[i] = []export.Cell{x.No, poStatusLabel[x.Status], x.TotalAmount, dashIfEmpty(x.VendorName), x.CreatedAt, dashIfEmpty(x.DueDate), x.DeliveryPlace}
			}
			return out, nil
		},
	}
}

// ---- 입고 ----

func receiptExport(s domain.ExportListService) *listSource {
	return &listSource{
		ds: export.Dataset{Code: "receipts", Label: "입고", Columns: []export.Column{
			{Label: "입고 번호"}, {Label: "발주 번호"}, {Label: "품목"}, {Label: "로트 번호"}, {Label: "유효기한", Kind: export.KindDate}, {Label: "수량", Kind: export.KindInt}, {Label: "창고"}, {Label: "입고일", Kind: export.KindDateTime}}},
		filters: func(q url.Values) []export.FilterItem {
			return []export.FilterItem{export.Date("입고일 시작", q.Get("from")), export.Date("입고일 종료", q.Get("to")), export.ID("창고", q.Get("warehouseId")), export.ID("발주", q.Get("poId"))}
		},
		fetch: func(ctx context.Context, q url.Values, max int) ([][]export.Cell, error) {
			rows, err := s.Receipts(ctx, domain.ReceiptFilter{From: q.Get("from"), To: q.Get("to"), WarehouseID: idParam(q, "warehouseId"), POID: idParam(q, "poId")}, max+1)
			if err != nil {
				return nil, err
			}
			out := make([][]export.Cell, len(rows))
			for i, x := range rows {
				out[i] = []export.Cell{x.ReceiptNo, x.PONo, x.ItemName, dashIfEmpty(x.LotNo), dashIfEmpty(x.ExpiryDate), x.Quantity, x.WarehouseName, x.ReceivedAt}
			}
			return out, nil
		},
	}
}

// ---- 재고 현황 ----

func stockExport(s domain.ExportListService) *listSource {
	return &listSource{
		ds: export.Dataset{Code: "stock", Label: "재고현황", Columns: []export.Column{
			{Label: "창고"}, {Label: "품목"}, {Label: "로트"}, {Label: "유효기한", Kind: export.KindDate}, {Label: "수량", Kind: export.KindInt}, {Label: "안전재고", Kind: export.KindInt}, {Label: "부족 수량", Kind: export.KindInt}}},
		filters: func(q url.Values) []export.FilterItem {
			return []export.FilterItem{export.ID("창고", q.Get("warehouseId")), export.Bool("안전재고 미달만", q.Get("belowSafety"))}
		},
		fetch: func(ctx context.Context, q url.Values, max int) ([][]export.Cell, error) {
			rows, err := s.Stock(ctx, domain.StockExportFilter{WarehouseID: idParam(q, "warehouseId"), BelowSafety: q.Get("belowSafety") == "1"}, max+1)
			if err != nil {
				return nil, err
			}
			out := make([][]export.Cell, len(rows))
			for i, x := range rows {
				out[i] = []export.Cell{x.WarehouseName, x.SKU + " " + x.ItemName, dashIfEmpty(x.LotNo), dashIfEmpty(x.ExpiryDate), x.Quantity, x.SafetyStock, x.Shortage}
			}
			return out, nil
		},
	}
}

// ---- 수불부 ----

// ledgerExport 는 품목 하나의 수불부를 기간 전체로 낸다. 기초 줄이 첫 행, 기말 줄이 끝 행이고 구분 열에 "기초"·"기말"이 적힌다(합계가 화면과 같다).
func ledgerExport(l domain.LedgerService, valued bool) *listSource {
	cols := []export.Column{{Label: "일자", Kind: export.KindDate}, {Label: "구분"}, {Label: "창고"}, {Label: "로트"}, {Label: "전표 번호"},
		{Label: "입고", Kind: export.KindInt}, {Label: "출고", Kind: export.KindInt}, {Label: "잔량", Kind: export.KindInt}}
	ds := export.Dataset{Code: "stock_ledger", Label: "수불부", Columns: cols}
	if valued {
		ds = export.Dataset{Code: "stock_ledger_valued", Label: "수불부(금액)", Columns: append(append([]export.Column{}, cols...), export.Column{Label: "단가(원)", Kind: export.KindMoney}, export.Column{Label: "금액(원)", Kind: export.KindMoney})}
	}
	return &listSource{
		ds: ds,
		filters: func(q url.Values) []export.FilterItem {
			return []export.FilterItem{export.ID("품목", q.Get("itemId")), export.ID("창고", q.Get("warehouseId")), export.ID("로트", q.Get("lotId")), export.Date("기간 시작", q.Get("from")), export.Date("기간 종료", q.Get("to"))}
		},
		fetch: func(ctx context.Context, q url.Values, max int) ([][]export.Cell, error) {
			f := domain.LedgerFilter{ItemID: idParam(q, "itemId"), WarehouseID: idParam(q, "warehouseId"), LotID: idParam(q, "lotId"),
				From: contract.Day(q.Get("from")), To: contract.Day(q.Get("to")), PageSize: 200}
			var rows []domain.LedgerRow
			var first domain.LedgerPage
			for page := 1; ; page++ {
				f.Page = page
				pg, err := l.Ledger(ctx, f)
				if err != nil {
					return nil, err
				}
				if page == 1 {
					first = pg
				}
				first.Closing = pg.Closing
				rows = append(rows, pg.Rows...)
				if len(pg.Rows) == 0 || len(rows) >= pg.Total || len(rows) > max {
					break
				}
			}
			pad := func(c []export.Cell) []export.Cell {
				if valued {
					c = append(c, nil, nil)
				}
				return c
			}
			out := make([][]export.Cell, 0, len(rows)+2)
			out = append(out, pad([]export.Cell{nil, "기초", nil, nil, nil, nil, nil, first.Opening}))
			for _, x := range rows {
				row := []export.Cell{x.Date, x.KindLabel, dashIfEmpty(x.Warehouse), dashIfEmpty(x.LotNo), dashIfEmpty(x.DocNo), nilIfZero(x.In), nilIfZero(x.Out), x.Balance}
				if valued {
					if x.Valued {
						row = append(row, x.UnitCost, x.Amount)
					} else {
						row = append(row, nil, nil)
					}
				}
				out = append(out, row)
			}
			out = append(out, pad([]export.Cell{nil, "기말", nil, nil, nil, nil, nil, first.Closing}))
			return out, nil
		},
	}
}

func nilIfZero(n int64) any {
	if n == 0 {
		return nil
	}
	return n
}

// ---- 문서함(보고 있는 함 하나) ----

func inboxExport(s domain.InboxService) *listSource {
	boxLabel := map[string]string{"drafted": "기안함", "pending": "결재 대기함", "in_progress": "결재 진행함", "done": "완료함", "cc": "참조함", "receive": "수신함", "rejected": "반려함"}
	return &listSource{
		ds: export.Dataset{Code: "inbox", Label: "문서함", Columns: []export.Column{
			{Label: "문서 번호"}, {Label: "제목"}, {Label: "양식"}, {Label: "기안자"}, {Label: "소속"}, {Label: "상태"}, {Label: "수신 처리"}, {Label: "최근 변경", Kind: export.KindDateTime}}},
		filters: func(q url.Values) []export.FilterItem {
			return []export.FilterItem{export.Enum("문서함", q.Get("box"), boxLabel), export.Enum("수신 상태", q.Get("tab"), map[string]string{"waiting": "접수 대기", "received": "접수됨", "done": "처리 완료", "returned": "반송"})}
		},
		fetch: func(ctx context.Context, q url.Values, max int) ([][]export.Cell, error) {
			box := domain.InboxBox(q.Get("box"))
			var out [][]export.Cell
			const page = 100
			for off := 0; ; off += page {
				items, err := s.List(ctx, box, domain.InboxFilter{Tab: q.Get("tab"), Limit: page, Offset: off})
				if err != nil {
					return nil, err
				}
				for _, d := range items {
					no := d.No
					if strings.HasPrefix(no, "TMP-") {
						no = "임시저장"
					}
					var recv any
					if d.ReceiptID != 0 {
						recv = d.ReceiptUnit + " · " + labelOr(receiptStatusLabel, d.ReceiptStatus).(string)
					}
					st := prStatusLabel[d.Status]
					out = append(out, []export.Cell{no, d.Title, d.FormName, d.RequesterName, dashIfEmpty(d.UnitPath), st, recv, d.UpdatedAt})
				}
				if len(items) < page || len(out) > max {
					return out, nil
				}
			}
		},
	}
}

// ---- 예산 현황(월 현황 표만, 수정 이력은 내보내지 않는다) ----

func budgetExport(b domain.BudgetService) *listSource {
	return &listSource{
		ds: export.Dataset{Code: "budgets", Label: "예산현황", Columns: []export.Column{
			{Label: "부서"}, {Label: "월 예산(원)", Kind: export.KindMoney}, {Label: "사용액(원)", Kind: export.KindMoney}, {Label: "결재 진행 중(원)", Kind: export.KindMoney},
			{Label: "잔액(원)", Kind: export.KindMoney}, {Label: "사용률(%)", Kind: export.KindPercent}, {Label: "상태"}}},
		filters: func(q url.Values) []export.FilterItem {
			return []export.FilterItem{export.Month("월", q.Get("month"))}
		},
		fetch: func(ctx context.Context, q url.Values, _ int) ([][]export.Cell, error) {
			rows, err := b.List(ctx, q.Get("month"))
			if err != nil {
				return nil, err
			}
			out := make([][]export.Cell, len(rows))
			for i, x := range rows {
				var pct any
				if x.UsagePct >= 0 {
					pct = x.UsagePct
				}
				out[i] = []export.Cell{x.DepartmentName, x.Amount, x.Used, x.InProgress, x.Balance, pct, labelOr(budgetBadgeLabel, x.Badge)}
			}
			return out, nil
		},
	}
}

// receiptHistoryHandler 는 입고 이력 화면의 목록이다(최근 입고 100줄). 내보내기와 같은 서비스 함수를 쓴다.
type receiptHistoryHandler struct{ svc domain.ExportListService }

func (h receiptHistoryHandler) list(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	rows, err := h.svc.Receipts(r.Context(), domain.ReceiptFilter{From: q.Get("from"), To: q.Get("to"), WarehouseID: idParam(q, "warehouseId"), POID: idParam(q, "poId")}, 101)
	if err != nil {
		writeErr(w, err)
		return
	}
	more := len(rows) > 100
	if more {
		rows = rows[:100]
	}
	out := make([]map[string]any, len(rows))
	for i, x := range rows {
		out[i] = map[string]any{"receiptNo": x.ReceiptNo, "poNo": x.PONo, "itemName": x.ItemName, "lotNo": x.LotNo, "expiryDate": x.ExpiryDate,
			"quantity": x.Quantity, "warehouseName": x.WarehouseName, "receivedAt": rfc(x.ReceivedAt)}
	}
	writeJSON(w, http.StatusOK, map[string]any{"rows": out, "more": more})
}
