package domain

import (
	"context"
	"time"
)

// B28 erp: 목록 내보내기·입고 이력용 목록 계약( 4·10절). 기존 시그니처는 바꾸지 않고 더하기만 한다.
// 내보내기 소스는 화면 목록이 부르는 같은 서비스 함수(열람 범위 적용)를 쪽 나누기 없이 부른다. 상한은 호출한 쪽이 max 로 넘기고 max+1 행까지만 읽는다.

// ReceiptFilter 는 입고 이력 조건이다. From·To 는 입고일(KST, YYYY-MM-DD), 비면 제한 없음.
type ReceiptFilter struct {
	From        string
	To          string
	WarehouseID int64
	POID        int64
}

// ReceiptRow 는 입고 이력 한 줄(입고 줄 단위)이다.
type ReceiptRow struct {
	ReceiptNo     string // RC-000012
	PONo          string
	ItemName      string
	LotNo         string
	ExpiryDate    string
	Quantity      int64
	WarehouseName string
	ReceivedAt    time.Time
}

// POExportFilter 는 발주 내보내기 조건이다. Status 가 비면 전체다.
type POExportFilter struct {
	Status POStatus
}

// POExportRow 는 발주 한 줄이다(거래처 이름 포함).
type POExportRow struct {
	No            string
	Status        POStatus
	TotalAmount   int64
	VendorName    string
	CreatedAt     time.Time
	DueDate       string
	DeliveryPlace string
}

// StockExportFilter 는 재고 현황(평평한 창고·품목·로트 표) 조건이다.
type StockExportFilter struct {
	WarehouseID int64
	BelowSafety bool // 안전재고 미달 품목만
}

// StockExportRow 는 창고·품목·로트별 한 줄이다. Shortage 는 품목 전체 재고가 안전재고에 못 미치는 부족 수량(없으면 0)이다.
type StockExportRow struct {
	WarehouseName string
	SKU           string
	ItemName      string
	LotNo         string
	ExpiryDate    string
	Quantity      int64
	SafetyStock   int64
	Shortage      int64
}

// ExportListService 는 내보내기·입고 이력이 쓰는 목록이다(읽기 전용, inventory:read, 열람 범위 적용). max 를 넘는 행은 max+1 번째까지만 읽는다.
type ExportListService interface {
	Receipts(ctx context.Context, f ReceiptFilter, limit int) ([]ReceiptRow, error)
	PurchaseOrders(ctx context.Context, f POExportFilter, limit int) ([]POExportRow, error)
	Stock(ctx context.Context, f StockExportFilter, limit int) ([]StockExportRow, error)
}
