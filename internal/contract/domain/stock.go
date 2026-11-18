package domain

import (
	"context"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
)

// B24 재고 기능 깊이(): 창고·출고·창고 이동. 기존 InventoryService 는 바꾸지 않고 새 서비스로 더한다.

// Warehouse 는 재고가 놓이는 곳이다.
type Warehouse struct {
	ID        int64
	Code      string
	Name      string
	Location  string
	Active    bool
	ItemCount int    // 재고가 남아 있는 품목 종류 수
	SiteID    int64  // 사업장(0 = 주사업장, B44). 이 창고 입고분의 자동 매입 전표가 이 사업장에 속한다
	SiteName  string // 사업장 이름(주사업장이면 빈 값)
}

// WarehouseInput 은 창고 등록·수정 입력이다. ID 가 0 이면 새로 만든다.
type WarehouseInput struct {
	ID       int64
	Code     string
	Name     string
	Location string
	SiteID   int64 // 사업장(0 = 주사업장). 현재 법인의 활성 사업장이어야 한다
}

// WarehouseService 는 창고 마스터다. 조회는 inventory:read, 쓰기는 inventory:manage.
type WarehouseService interface {
	List(ctx context.Context) ([]Warehouse, error)
	Save(ctx context.Context, in WarehouseInput) (Warehouse, error)
	SetActive(ctx context.Context, id int64, active bool) error // 재고가 남은 창고·마지막 사용 창고는 중지 거부
}

// 출고 로트 고르는 방식.
const (
	IssueFIFO   = "fifo"   // 선입선출(기본)
	IssueExpiry = "expiry" // 유효기한 임박 우선
	IssueManual = "manual" // 직접 고르기
)

// IssueAllocation 은 출고 한 줄의 로트 배분이다. LotID 0 은 로트 없는 품목이다.
type IssueAllocation struct {
	ItemID   int64
	LotID    int64
	LotNo    string
	Expiry   string // YYYY-MM-DD
	Qty      int64
	OnHand   int64  // 그 창고의 현재 재고(제안 때 참고용)
	Received string // 첫 입고 시각(선입선출 기준, 참고용)
}

// IssueProposeInput 은 방식별 로트 배분 제안 입력이다(저장하지 않는다).
type IssueProposeInput struct {
	WarehouseID int64
	ItemID      int64
	Qty         int64
	Method      string // fifo | expiry
}

// IssueLineInput 은 출고 전표의 한 줄이다(이미 로트에 나눈 상태).
type IssueLineInput struct {
	ItemID int64
	LotID  int64
	Qty    int64
}

// IssueInput 은 출고 처리 입력이다.
type IssueInput struct {
	DepartmentID int64
	WarehouseID  int64
	IssueDate    string // YYYY-MM-DD, 비면 오늘
	Method       string // fifo | expiry | manual (감사에 남기는 이름)
	Memo         string
	Lines        []IssueLineInput
}

// IssueSlipLine 은 처리된 출고 전표의 한 줄이다.
type IssueSlipLine struct {
	ItemID   int64
	ItemName string
	SKU      string
	LotID    int64
	LotNo    string
	Qty      int64
}

// IssueSlip 은 처리된 출고 전표다.
type IssueSlip struct {
	ID             int64
	No             string
	DepartmentID   int64
	DepartmentName string
	WarehouseID    int64
	WarehouseName  string
	IssueDate      string
	Method         string
	Memo           string
	ActorName      string
	CreatedAt      time.Time
	Lines          []IssueSlipLine
}

// StockIssueService 는 부서 사용 출고다(inventory:manage).
type StockIssueService interface {
	ProposeIssue(ctx context.Context, in IssueProposeInput) ([]IssueAllocation, error)
	Issue(ctx context.Context, in IssueInput) (IssueSlip, error)
	ListRecent(ctx context.Context, limit int) ([]IssueSlip, error)
	Departments(ctx context.Context) ([]DepartmentRef, error) // 출고 부서 선택지
}

// DepartmentRef 는 출고 부서 선택지(부서 마스터)다.
type DepartmentRef struct {
	ID   int64
	Name string
}

// TransferLineInput 은 이동 전표의 한 줄이다.
type TransferLineInput struct {
	ItemID int64
	LotID  int64
	Qty    int64
}

// TransferInput 은 창고 이동 입력이다.
type TransferInput struct {
	FromID   int64
	ToID     int64
	MoveDate string // YYYY-MM-DD, 비면 오늘
	Memo     string
	Lines    []TransferLineInput
}

// LedgerPreviewLine 은 이동이 만들 재고 기록 한 줄(처리 전 미리보기)이다.
type LedgerPreviewLine struct {
	Kind          string // transfer_out | transfer_in
	WarehouseName string
	ItemName      string
	LotNo         string
	Qty           int64 // 부호 있음
	// B51 4단계: 법인 간 이동의 표시. 법인 하나 설치와 같은 법인 이동은 모두 비어 있다(Intercompany=false).
	Intercompany bool
	EntityName   string // 이 줄이 속한 법인 이름(법인 간 이동일 때만)
	UnitCost     int64  // 이동 단가(출발 법인 평균, 법인 간 이동의 이동 줄)
	Amount       int64  // 이동 금액(절대값)
	Valued       bool   // inventory:valuation_read 가 있을 때만 단가·금액이 채워진다
	NoAvg        bool   // 출발 법인에 평균 단가가 없어 0원으로 이동한다(경고)
}

// TransferSlipLine 은 처리된 이동 전표의 한 줄이다.
type TransferSlipLine struct {
	ItemName string
	SKU      string
	LotNo    string
	Qty      int64
}

// TransferSlip 은 처리된 이동 전표다.
type TransferSlip struct {
	ID        int64
	No        string
	FromID    int64
	FromName  string
	ToID      int64
	ToName    string
	MoveDate  string
	Memo      string
	ActorName string
	CreatedAt time.Time
	Lines     []TransferSlipLine
	// B51 4단계: 법인 간 이동(내부 거래). 상대 법인이 호출자의 범위 밖이면 상대 창고 이름·코드는 비우고 법인 이름만 남긴다(D8).
	Intercompany   bool
	FromEntityID   int64
	FromEntityName string
	ToEntityID     int64
	ToEntityName   string
	CostAmount     int64 // 법인 간 이동 줄 동결 금액의 합. Valued 일 때만 의미가 있다
	Valued         bool  // inventory:valuation_read 가 있을 때만 금액을 보인다
}

// TransferTarget 은 다른 법인에서 고를 수 있는 도착 창고다(양쪽 법인에서 inventory:manage 가 효력일 때만).
type TransferTarget struct {
	ID         int64
	Code       string
	Name       string
	EntityID   int64
	EntityName string
}

// StockTransferService 는 창고 간 이동이다(inventory:manage). 출고 한 줄·입고 한 줄이 한 트랜잭션이다.
type StockTransferService interface {
	Preview(ctx context.Context, in TransferInput) ([]LedgerPreviewLine, error)
	Transfer(ctx context.Context, in TransferInput) (TransferSlip, error)
	ListRecent(ctx context.Context, limit int) ([]TransferSlip, error)
	// Targets 는 현재 법인이 아닌 다른 법인의 도착 창고 후보다(B51). 두 법인 모두 inventory:manage 가 효력일 때만 나온다.
	Targets(ctx context.Context) ([]TransferTarget, error)
}

// WarehouseQty 는 창고별 재고 수량이다(GetStock 의 ByWarehouse, 추가만).
type WarehouseQty struct {
	WarehouseID   int64
	WarehouseName string
	OnHand        int64
}

// ---- 재고 실사(B24 ③) ----

// 실사 표 상태.
const (
	CountDraft     = "draft"     // 작성 중
	CountApproving = "approving" // 조정 전표 결재 중
	CountApplied   = "applied"   // 재고 반영 완료
	CountRejected  = "rejected"  // 반려
	CountNoDiff    = "no_diff"   // 차이 없음으로 종료
	CountDiscarded = "discarded" // 폐기
)

// StockCountLine 은 실사 표의 한 줄이다. CountedQty 가 nil 이면 아직 세지 않은 줄이다.
type StockCountLine struct {
	ItemID     int64
	ItemName   string
	SKU        string
	LotID      int64
	LotNo      string
	SystemQty  int64
	CountedQty *int64
	Diff       int64 // 실제 − 전산(세지 않았으면 0)
	Reason     string
}

// StockCount 는 실사 표 한 건이다.
type StockCount struct {
	ID            int64
	No            string
	WarehouseID   int64
	WarehouseName string
	BaseDate      string
	Status        string
	DocID         int64  // 조정 전표(양식 문서) 번호, 상신 전이면 0
	DocNo         string // 조정 전표 문서 번호
	RejectReason  string
	CreatedByName string
	CreatedAt     time.Time
	AppliedAt     *time.Time
	Lines         []StockCountLine
	DiffLines     int // 차이가 있는 줄 수
	Uncounted     int // 아직 세지 않은 줄 수
	MovedSince    int // 전산 수량을 잡은 뒤 이 줄들에 생긴 입출고·이동 기록 수(상신 경고용)
}

// CountLineInput 은 실사 표 저장 입력의 한 줄이다. 없는 (품목, 로트)이면 줄을 더한다(전산 수량은 그 시점의 재고).
type CountLineInput struct {
	ItemID     int64
	LotID      int64
	CountedQty *int64
	Reason     string
}

// CountSaveInput 은 실제 수량·차이 사유 저장 입력이다.
type CountSaveInput struct {
	Lines []CountLineInput
}

// CountFilter 는 실사 목록 조건이다.
type CountFilter struct {
	WarehouseID int64
	Status      string
	Limit       int
}

// StockCountService 는 재고 실사다(inventory:manage). 조정은 조정 전표(양식 문서 ADJ) 결재 뒤 원장에 반영된다.
type StockCountService interface {
	Create(ctx context.Context, warehouseID int64) (StockCount, error)
	Get(ctx context.Context, id int64) (StockCount, error)
	List(ctx context.Context, f CountFilter) ([]StockCount, error)
	Save(ctx context.Context, id int64, in CountSaveInput) (StockCount, error)
	Refresh(ctx context.Context, id int64) (StockCount, error) // 전산 수량을 지금 재고로 다시 잡는다(작성 중·반려 상태)
	Submit(ctx context.Context, id int64) (StockCount, error)  // 차이 없음이면 종료, 있으면 조정 전표를 만들어 상신
	Discard(ctx context.Context, id int64) error
}

// ---- 수불부(B24 ④) ----

// 수불부 구분 코드.
const (
	LedgerReceipt     = "receipt"
	LedgerIssue       = "issue"
	LedgerTransferOut = "transfer_out"
	LedgerTransferIn  = "transfer_in"
	LedgerAdjust      = "adjust"
)

// LedgerFilter 는 수불부 조건이다. From·To 가 비면 이번 달 1일~오늘이다.
type LedgerFilter struct {
	ItemID      int64
	WarehouseID int64 // 0 이면 전체 창고
	LotID       int64 // 0 이면 전체 로트
	From        contract.Day
	To          contract.Day
	Page        int // 1부터
	PageSize    int // 기본 50
}

// LedgerRow 는 수불부의 한 줄이다. In·Out 중 하나만 양수다.
type LedgerRow struct {
	Date      string
	Kind      string // LedgerReceipt …
	KindLabel string
	Warehouse string
	LotNo     string
	DocNo     string
	In        int64
	Out       int64
	Balance   int64 // 그 줄 뒤의 잔량(조회 조건 안에서)
	// B27: 평가 단가·금액 증감(입고 +, 출고 −). 평가 줄이 있을 때만(Valued). inventory:valuation_read 가 없으면 서비스가 비운다(0, Valued=false).
	UnitCost int64
	Amount   int64
	Valued   bool
	// B51 4단계: 법인 간 이동 행의 표시. CounterEntity 는 상대 법인 이름이다(창고 이름은 보이지 않는다, D8).
	Intercompany  bool
	CounterEntity string
}

// LedgerPage 는 수불부 한 쪽과 기초·합계·기말이다. Opening + TotalIn − TotalOut = Closing.
type LedgerPage struct {
	Opening  int64
	Rows     []LedgerRow
	TotalIn  int64
	TotalOut int64
	Closing  int64
	Total    int // 조건에 맞는 전체 행 수
}

// LedgerService 는 품목별 수불부 조회다(inventory:read, 처리자·금액 없음).
type LedgerService interface {
	Ledger(ctx context.Context, f LedgerFilter) (LedgerPage, error)
}
