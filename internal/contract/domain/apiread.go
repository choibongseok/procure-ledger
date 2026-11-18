package domain

import (
	"context"
	"time"
)

// 연동 API(B42, /api/v1)가 읽는 자원 표현과 서비스 계약이다. 구현: internal/service.APIReadService(erp), 쓰는 쪽: internal/apiv1(ai).
// 모든 목록은 번호(id) 오름차순이고 after(마지막으로 받은 번호) 다음부터 limit 개를 돌려주며, 두 번째 값은 더 있는지다.
// 사람 정보(작성자·승인자 이름·사번·연락처)는 어떤 표현에도 없다. 거래처의 사업자번호·계좌·연락처는 호출 채널(API)이 원문을 낼 수 없어 항상 가려서 나간다.

// APIItem 은 품목과 현재 재고(원장 합)다.
type APIItem struct {
	ID          int64
	SKU         string
	Name        string
	Unit        string
	SafetyStock int64
	TrackLot    bool
	OnHand      int64
}

// APIReceipt 는 입고 한 건이다(발주 번호 포함, 입고자 정보 없음).
type APIReceipt struct {
	ID         int64
	POID       int64
	PONo       string
	ReceivedAt time.Time
	Lines      []ReceiptLine
}

// APIPayable 은 채무 항목이다. 상태는 취소·완납·일부 지급·미지급 중 하나로 서비스가 정한다.
type APIPayable struct {
	ID        int64
	VendorID  int64
	JournalNo string
	TradeDate string
	DueDate   string
	Amount    int64
	Paid      int64
	Reserved  int64
	Status    string // canceled | paid | partial | open
}

// 채무 항목 상태 값.
const (
	APIPayableCanceled = "canceled"
	APIPayablePaid     = "paid"
	APIPayablePartial  = "partial"
	APIPayableOpen     = "open"
)

// APITaxInvoice 는 세금계산서 한 건의 연동 API 표현이다(B43 3단계). 이메일·주소·대표자·품목 이름은 없고 사업자번호는 항상 가려서 나간다(API 채널은 원문을 낼 수 없다).
type APITaxInvoice struct {
	ID          int64
	No          string
	Direction   string
	TaxType     string
	Status      string
	ApprovalNo  string
	IssueDate   string
	SupplierBiz string
	SupplierNm  string
	BuyerBiz    string
	BuyerNm     string
	SupplyTotal int64
	TaxTotal    int64
	Total       int64
	AmendOfNo   string
	AmendReason string
	LineCount   int
}

// APIReadService 는 연동 API 읽기 자원의 서비스다. 권한·부서 격리·마스킹은 구현이 호출자(Principal)로 판정한다.
type APIReadService interface {
	Items(ctx context.Context, after int64, limit int) ([]APIItem, bool, error)                // inventory:read
	Vendors(ctx context.Context, after int64, limit int) ([]Vendor, bool, error)               // purchase:read, 민감 필드는 항상 마스킹
	PurchaseOrders(ctx context.Context, after int64, limit int) ([]PurchaseOrder, bool, error) // inventory:read + 열람 범위
	Receipts(ctx context.Context, after int64, limit int) ([]APIReceipt, bool, error)          // inventory:read + 열람 범위
	Journals(ctx context.Context, after int64, limit int) ([]Journal, bool, error)             // ledger:read
	Payables(ctx context.Context, after int64, limit int) ([]APIPayable, bool, error)          // ledger:read
	TaxInvoices(ctx context.Context, after int64, limit int) ([]APITaxInvoice, bool, error)    // taxinvoice:read, 사업자번호는 항상 마스킹
}

// APIJournalDrafter 는 연동 API 의 전표 초안 제출이다(B42 3단계, api:journal_draft 내부 권한). 작성자는 토큰 발급자이고 초안까지만 만든다.
// 같은 토큰의 같은 externalRef 는 멱등이다: 내용이 같으면 기존 전표와 created=false, 다르면 contract.ErrInvalidState(충돌).
type APIJournalDrafter interface {
	CreateDraftFromAPI(ctx context.Context, in JournalInput, externalRef string) (Journal, bool, error)
}
