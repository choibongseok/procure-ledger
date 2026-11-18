package domain

import "context"

// B27 ④ 월별 재고 현황( 3.4). 마감된 달은 저장된 스냅샷, 그 밖의 달은 실시간 계산이라 "잠정"이다.
// 금액은 정수 원(공급가액)이다. 금액 필드는 inventory:valuation_read 가 없으면 서비스가 비운다(HasAmounts=false).

// MonthStockFilter 는 월별 재고 현황 조건이다. Month 가 비면 직전 달이다.
type MonthStockFilter struct {
	Month       string // YYYY-MM
	WarehouseID int64  // 0 이면 전체 창고
	Query       string // 품목 코드·이름 검색
}

// MonthStockRow 는 품목·창고별 한 줄이다. 이동은 순증감(이동 입고 − 이동 출고)이다.
type MonthStockRow struct {
	ItemID          int64
	SKU             string
	ItemName        string
	WarehouseID     int64
	WarehouseName   string
	WarehouseActive bool
	// 전체 모드(그룹 범위)에서만 채운다(B51 D14): 줄을 법인별로 나눠 보인다.
	EntityID       int64
	EntityName     string
	OpenQty        int64
	InQty          int64
	OutQty         int64
	TransferQty    int64
	AdjustQty      int64
	CloseQty       int64
	OpenAmount     int64
	InAmount       int64
	OutAmount      int64
	TransferAmount int64
	AdjustAmount   int64
	CloseAmount    int64
	AvgCost        int64
	// 전월 대비(전월이 마감되어 있고 금액 열람 권한이 있을 때만).
	PrevCloseAmount *int64
	Delta           *int64
	DeltaPct        *int // 증감률(정수 %, 전월 금액이 0 이면 nil)
}

// MonthStockTotal 은 합계 줄이다. RoundDiff 는 품목 잔액 금액과 창고별 기말 금액 합의 반올림 차이(필터가 없을 때만).
type MonthStockTotal struct {
	OpenQty, InQty, OutQty, TransferQty, AdjustQty, CloseQty                               int64
	OpenAmount, InAmount, OutAmount, TransferAmount, AdjustAmount, CloseAmount, PrevAmount int64
	RoundDiff                                                                              *int64
}

// MonthStockEntityTotal 은 전체 모드의 법인별 소계 줄이다.
type MonthStockEntityTotal struct {
	EntityID   int64
	EntityName string
	Total      MonthStockTotal
}

// MonthStockReport 는 월별 재고 현황 한 달이다.
type MonthStockReport struct {
	Month       string
	Provisional bool // 마감 전 잠정 값(실시간 계산)
	Version     int  // 마감된 달의 스냅샷 버전(잠정이면 0)
	HasAmounts  bool
	PrevClosed  bool // 전월이 마감되어 전월 대비를 계산했다
	Rows        []MonthStockRow
	Total       MonthStockTotal
	// 전체 모드(그룹 범위, B51 D14): 줄은 법인별로 나뉘고 단가·평균은 비우며 합계는 수량·금액만 더한다. EntityTotals 는 법인별 소계다.
	AllEntities  bool
	EntityTotals []MonthStockEntityTotal
	// ExcludedLines 는 법인을 나누기 전에 마감한 달의 스냅샷에서 다른 법인 창고라 제외한 줄 수다(B51 5.4). 0 이면 없다.
	ExcludedLines int
}

// MonthStockService 는 월별 재고 현황이다(inventory:read, 금액은 inventory:valuation_read).
type MonthStockService interface {
	Report(ctx context.Context, f MonthStockFilter) (MonthStockReport, error)
}
