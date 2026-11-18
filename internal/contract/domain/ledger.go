package domain

import (
	"context"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
)

// B31: 계정과목·전표·매입 채무(). 기존 시그니처는 바꾸지 않고 더하기만 한다. 금액은 모두 정수 원이다.

// 계정 구분.
const (
	AccountAsset     = "asset"
	AccountLiability = "liability"
	AccountEquity    = "equity"
	AccountRevenue   = "revenue"
	AccountExpense   = "expense"
)

// AccountKindLabels 는 구분의 한국어 이름이다.
var AccountKindLabels = map[string]string{AccountAsset: "자산", AccountLiability: "부채", AccountEquity: "자본", AccountRevenue: "수익", AccountExpense: "비용"}

// Account 는 계정과목이다. 코드·구분은 바꿀 수 없다.
type Account struct {
	ID     int64
	Code   string
	Name   string
	Kind   string
	Active bool
	System bool
}

// AccountInput 은 계정 추가·이름 변경 입력이다. 새 계정의 구분은 코드의 앞 숫자로 정해진다(1 자산·2 부채·3 자본·4 수익·5 비용).
type AccountInput struct {
	Code string
	Name string
	Kind string // 비면 코드 앞자리로 정한다. 있으면 코드 앞자리의 구분과 같아야 한다
}

// AccountRule 은 자동 전표 규칙 한 줄이다(이벤트 → 계정).
type AccountRule struct {
	Key         string
	Label       string
	AccountCode string
	AccountName string
	Active      bool // 규칙이 가리키는 계정이 사용 중인가(중지면 자동 전표가 전표 대기로 간다)
}

// 전표 종류·상태.
const (
	JournalGeneral      = "general"
	JournalAutoPurchase = "auto_purchase"
	JournalAutoExpense  = "auto_expense"
	JournalAutoPayment  = "auto_payment"
	JournalAutoCard     = "auto_card"
	JournalAutoAICost   = "auto_aicost" // B49: 부서별 AI 비용 배부 확정이 만드는 임시 비용 전표
	JournalReversal     = "reversal"
	JournalOpening      = "opening"

	JournalDraft     = "draft"
	JournalRequested = "requested"
	JournalApproved  = "approved"
	JournalCanceled  = "canceled"
)

// JournalKindLabels·JournalStatusLabels 는 한국어 이름이다(화면에 영문 코드를 보이지 않는다).
var JournalKindLabels = map[string]string{JournalGeneral: "일반", JournalAutoPurchase: "자동 매입", JournalAutoExpense: "자동 비용", JournalAutoPayment: "자동 지급",
	JournalAutoCard: "자동 카드", JournalAutoAICost: "자동 AI 비용", JournalReversal: "역분개", JournalOpening: "기초"}
var JournalStatusLabels = map[string]string{JournalDraft: "임시", JournalRequested: "승인 요청", JournalApproved: "승인", JournalCanceled: "취소"}

// JournalLine 은 전표 줄이다. Side 는 "D"(차변)·"C"(대변).
type JournalLine struct {
	Seq          int
	AccountID    int64
	AccountCode  string
	AccountName  string
	Side         string
	Amount       int64
	VendorID     int64
	VendorName   string
	Memo         string
	DepartmentID int64 // 부서 차원(B49, 0 = 없음)
}

// JournalEvidence 는 전표에 연결된 증빙이다.
type JournalEvidence struct {
	Kind  string // verdict | document | attachment
	RefID int64
}

// Journal 은 전표 한 건이다. ReversedByNo 가 있으면 "정정됨"이다(승인된 역분개 전표 번호, 파생 값).
type Journal struct {
	ID           int64
	No           string
	Kind         string
	Status       string
	PostingDate  string
	TradeDate    string
	Memo         string
	SourceType   string
	SourceID     int64
	ReversesNo   string
	ReversedByNo string
	Corrected    bool
	DocID        int64
	DocNo        string
	TotalAmount  int64
	Lines        []JournalLine
	Evidence     []JournalEvidence
	CreatedByID  int64
	CreatedBy    string
	CreatedAt    time.Time
	ApprovedBy   string
	ApprovedAt   *time.Time
	CancelReason string
	SiteID       int64  // 사업장(0 = 주사업장, B44)
	SiteName     string // 사업장 이름(주사업장이면 빈 값)
	Intercompany string // 연결 거래처가 있는 줄이 있으면 상대 법인 이름(법인 간 거래 배지, B44 11.1)
}

// JournalLineInput·JournalInput 은 전표 입력이다. Kind 가 비면 일반 전표, "opening" 이면 기초 전표다.
type JournalLineInput struct {
	AccountCode string
	Side        string
	Amount      int64
	VendorID    int64
	Memo        string
}

type JournalInput struct {
	Kind        string
	PostingDate string
	TradeDate   string
	Memo        string
	SiteID      int64 // 사업장(0 = 주사업장). 현재 법인의 활성 사업장이어야 한다(B44)
	Lines       []JournalLineInput
	Evidence    []JournalEvidence
}

// JournalFilter 는 전표 목록 조건이다(Limit 기본 50, 최대 200).
type JournalFilter struct {
	Status      string
	Kind        string
	From, To    string // 전기일
	AccountCode string
	VendorID    int64
	Limit       int
	Offset      int
}

// JournalPage 는 목록 한 쪽과 조건에 맞는 전체 건수다.
type JournalPage struct {
	Rows  []Journal
	Total int
}

// AccountService 는 계정과목·자동 전표 규칙이다. 조회 ledger:read, 변경 account:manage.
type AccountService interface {
	List(ctx context.Context, includeInactive bool) ([]Account, error)
	Create(ctx context.Context, in AccountInput) (Account, error)
	Rename(ctx context.Context, code, name string) (Account, error)
	SetActive(ctx context.Context, code string, active bool) (Account, error)
	Rules(ctx context.Context) ([]AccountRule, error)
	SetRule(ctx context.Context, key, accountCode string) error
}

// JournalService 는 전표다. 조회 ledger:read, 쓰기 ledger:write(재무만).
type JournalService interface {
	List(ctx context.Context, f JournalFilter) (JournalPage, error)
	Get(ctx context.Context, no string) (Journal, error)
	Create(ctx context.Context, in JournalInput) (Journal, error)
	Update(ctx context.Context, no string, in JournalInput) (Journal, error)
	RequestApproval(ctx context.Context, no string) (Journal, error)
	Cancel(ctx context.Context, no, reason string) (Journal, error)
	Reverse(ctx context.Context, no, postingDate string) (Journal, error)
	// RequestApprovalBatch 는 임시 전표 여러 건(20건 상한)을 건별로 승인 요청한다. 한 건의 실패가 다른 건을 막지 않는다.
	RequestApprovalBatch(ctx context.Context, nos []string) ([]JournalBatchResult, error)
}

// ---- 전표 대기(B31 3.3.8) ----

// 전표 대기 사유 코드(화면에는 QueueReasonLabels 의 한국어를 보인다).
const (
	QueueMismatchConfirmed = "mismatch_confirmed"    // 불일치로 확정
	QueueHoldConfirmed     = "hold_confirmed"        // 보류로 확정
	QueueRuleAccount       = "rule_account_inactive" // 규칙이 없거나 규칙 계정을 쓸 수 없음
	QueueTotalDiffers      = "total_differs"         // 증빙 합계와 입고 금액이 다름
	QueueNoReceipt         = "no_receipt"            // 입고가 없는 판정
	QueueVendorMissing     = "vendor_missing"        // 발주 거래처를 찾을 수 없음
	QueueMonthClosed       = "month_closed"          // 전기일이 마감된 달
	QueuePreAdoption       = "pre_adoption"          // 도입 전 확정
	QueueNoAmount          = "no_amount"             // 전표 금액을 정할 수 없음
)

// QueueReasonLabels 는 사유 코드의 한국어 이름이다.
var QueueReasonLabels = map[string]string{
	QueueMismatchConfirmed: "불일치 확정", QueueHoldConfirmed: "보류 확정", QueueRuleAccount: "규칙 계정 사용 불가",
	QueueTotalDiffers: "증빙 합계와 입고 금액이 다름", QueueNoReceipt: "입고 없음", QueueVendorMissing: "거래처 없음",
	QueueMonthClosed: "마감된 월", QueuePreAdoption: "도입 전 확정", QueueNoAmount: "금액 없음",
}

// 전표 대기 상태.
const (
	QueuePending = "pending"
	QueueCreated = "created"
	QueueManual  = "manual"
	QueueSkipped = "skipped"
)

// QueueStatusLabels 는 처리 상태의 한국어 이름이다.
var QueueStatusLabels = map[string]string{QueuePending: "처리 전", QueueCreated: "전표 생성", QueueManual: "수동 입력", QueueSkipped: "전표 불필요"}

// QueueItem 은 전표 대기 한 행이다.
type QueueItem struct {
	ID          int64
	SourceType  string // verdict | approval_doc
	SourceID    int64
	SourceLabel string // 판정 번호 또는 결재 문서 번호(화면용)
	Reason      string
	ReasonText  string
	PONo        string
	VendorName  string
	Amount      *int64
	Status      string
	JournalNo   string
	HandledBy   string
	HandledAt   *time.Time
	Note        string
	CreatedAt   time.Time
}

// JournalBatchResult 는 일괄 승인 요청의 전표 한 건 결과다.
type JournalBatchResult struct {
	No    string
	OK    bool
	Error string // 실패 사유(한국어)
}

// JournalQueueService 는 전표 대기 처리다. 조회 ledger:read, 처리 ledger:write.
type JournalQueueService interface {
	List(ctx context.Context, status string) ([]QueueItem, error)
	// Retry 는 같은 입력으로 전표를 다시 만든다. postingDate 가 비면 오늘(KST)이고, 도입 전 확정 건은 전기일을 직접 지정한다(열린 달만).
	Retry(ctx context.Context, id int64, postingDate string) (QueueItem, error)
	Manual(ctx context.Context, id int64, journalNo string) (QueueItem, error)
	Skip(ctx context.Context, id int64, note string) (QueueItem, error)
}

// ---- 매입 채무(B31 3.5) ----

// PayableVendorRow 는 거래처별 채무 한 줄이다. 미지급 잔액 = 금액 − 지급 누계(예약은 빼지 않고 "지급 예정"으로 따로 보인다).
type PayableVendorRow struct {
	VendorID      int64
	VendorName    string
	Count         int
	Outstanding   int64
	Reserved      int64
	EarliestDue   string // 미지급 항목 중 가장 이른 만기(없으면 "")
	OverdueAmount int64  // 기준일에 만기가 지난 미지급 금액
	PaymentDays   int
	Intercompany  string // 거래처가 그룹의 다른 법인이면 그 법인 이름(법인 간 거래 배지, B44 11.1)
}

// PayableReport 는 거래처별 채무 표와 계정별 원장의 외상매입금 잔액 검산이다.
type PayableReport struct {
	AsOf          string
	Rows          []PayableVendorRow
	Total         int64
	LedgerBalance int64 // 외상매입금(2110) 계정별 원장의 기준일 잔액(승인된 전표)
	Matches       bool  // Total == LedgerBalance
}

// PayableItem 은 채무 항목 한 건이다.
type PayableItem struct {
	ID          int64
	JournalNo   string
	TradeDate   string
	DueDate     string
	Amount      int64
	Paid        int64
	Reserved    int64
	Outstanding int64
	OverdueDays int // 기준일 − 만기(만기 전이면 0)
}

// PayableService 는 매입 채무다. 조회 ledger:read, 결제 조건 수정 account:manage.
type PayableService interface {
	ByVendor(ctx context.Context, asOf string) (PayableReport, error)
	Items(ctx context.Context, vendorID int64, asOf string) ([]PayableItem, error)
	SetPaymentDays(ctx context.Context, vendorID int64, days int) (int, error)
}

// ---- 계정별 원장·시산표(B31 3.6) ----

// AccountLedgerFilter 는 계정별 원장 조건이다. From·To 가 비면 이번 달 1일~오늘이다.
type AccountLedgerFilter struct {
	Account  string
	From, To string
	VendorID int64
}

// AccountLedgerRow 는 원장 한 줄이다. Balance 는 계정의 정상 방향 부호로 맞춘 누계다.
type AccountLedgerRow struct {
	PostingDate string
	JournalNo   string
	Memo        string
	Counter     string // 상대 계정(코드 이름, 여러 개면 쉼표)
	Debit       int64
	Credit      int64
	Balance     int64
}

// AccountLedger 는 계정별 원장이다(승인된 전표만 센다).
type AccountLedger struct {
	Account       Account
	From, To      string
	Opening       int64
	Rows          []AccountLedgerRow
	TotalDebit    int64
	TotalCredit   int64
	Closing       int64
	ExcludedCount int // 기간 안에 이 계정을 쓴 승인 대기(임시·승인 요청) 전표 수
}

// TrialRow 는 시산표 한 줄이다(기초·기말은 정상 방향 부호).
type TrialRow struct {
	Code, Name, Kind string
	Opening          int64
	Debit, Credit    int64
	Closing          int64
}

// TrialBalance 는 시산표다. Balanced 가 거짓이면 데이터 이상 신호다.
type TrialBalance struct {
	From, To      string
	Rows          []TrialRow
	TotalDebit    int64
	TotalCredit   int64
	Balanced      bool
	ExcludedCount int
}

// LedgerReportService 는 계정별 원장·시산표다(ledger:read).
type LedgerReportService interface {
	AccountLedger(ctx context.Context, f AccountLedgerFilter) (AccountLedger, error)
	TrialBalance(ctx context.Context, from, to string, includeZero bool) (TrialBalance, error)
}

// ---- 부서별 AI 비용 배부 전표(B49 3단계) ----

// AICostJournalLine 은 부서 한 곳의 비용 줄이다(차변).
type AICostJournalLine struct {
	DepartmentID int64
	Amount       int64
	Memo         string
}

// AICostJournalInput 은 배부 확정이 만드는 임시 비용 전표의 입력이다. 차변은 부서별 비용 줄, 대변은 미지급금 한 줄(합계)이다.
type AICostJournalInput struct {
	RunID       int64
	PostingDate string // 그 달의 마지막 날
	TradeDate   string
	Memo        string
	ExpenseCode string // 비용 계정 코드(설정 aicost.expense_account)
	Lines       []AICostJournalLine
}

// AICostJournalHook 은 회계 서비스가 배부 실행에 내주는 입구다(구현: *service.JournalService). 모두 호출자의 트랜잭션 안에서 부른다.
type AICostJournalHook interface {
	// CreateAICostDraft 는 임시 전표(종류 auto_aicost, 출처 aicost_run)를 만든다. 전기일이 마감된 달이면 ErrInvalidState.
	CreateAICostDraft(ctx context.Context, tx contract.Tx, in AICostJournalInput) (string, error)
	// AICostJournalStatus 는 전표 번호의 상태다(없으면 ErrNotFound).
	AICostJournalStatus(ctx context.Context, tx contract.Tx, no string) (string, error)
	// CancelAICostDraft 는 임시 전표를 취소한다. 승인 요청 중이거나 승인된 전표는 ErrInvalidState.
	CancelAICostDraft(ctx context.Context, tx contract.Tx, no, reason string) error
}
