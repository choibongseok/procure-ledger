package domain

import "context"

// B35 기초 데이터 대조 보고서( 3.9, erp). 시스템 값은 코드가 DB 에서 집계하고,
// 사용자가 입력한 원 시스템 수치(저장하지 않는 화면 입력)와 나란히 보인다. 읽기 전용이다.

// 대조 항목 코드.
const (
	ReconAccounts   = "accounts"
	ReconItems      = "items"
	ReconStockQty   = "stock_qty"
	ReconStockValue = "stock_value"
	ReconVendors    = "vendors"
	ReconPayable    = "payable"
	ReconBudget     = "budget"
)

// ReconcileInput 은 원 시스템 수치다. nil 인 항목은 입력하지 않은 것이라 시스템 값만 보인다.
type ReconcileInput struct {
	Original map[string]int64
}

// ReconcileRow 는 대조 한 줄이다. 판정(Verdict)은 원 시스템 값이 있을 때만 match·diff 이고, 없으면 빈 값이다.
type ReconcileRow struct {
	Code     string
	Label    string
	Original *int64
	System   int64
	Diff     *int64 // 시스템 − 원 시스템
	Verdict  string // match(일치) | diff(차이) | ""(원 시스템 값 없음)
	Notes    []string
}

// ReconcileReport 는 대조 보고서다.
type ReconcileReport struct {
	Rows                []ReconcileRow
	PendingOpeningCount int // 승인 전 기초 전표 건수
}

// ReconcileLabels 는 화면에 보이는 항목 이름이다(순서도 이 순서).
var ReconcileCodes = []string{ReconAccounts, ReconVendors, ReconItems, ReconStockQty, ReconStockValue, ReconPayable, ReconBudget}

var ReconcileLabels = map[string]string{
	ReconAccounts: "계정과목 수", ReconVendors: "거래처 수", ReconItems: "품목 수", ReconStockQty: "재고 수량 합", ReconStockValue: "재고 금액 합(원)",
	ReconPayable: "미지급 잔액 합(원)", ReconBudget: "부서 예산 합(원)",
}

// ImportReconcileService 는 대조 보고서다(import:read).
type ImportReconcileService interface {
	Report(ctx context.Context, in ReconcileInput) (ReconcileReport, error)
}
