package domain

import (
	"context"
	"time"
)

// B32 지급·자금(). 금액은 정수 원이다. 계좌번호는 마스킹 값만 응답에 싣고, 원문은 RevealAccount 로만 나간다(감사).

// 지급 건 상태(4절). 전이 표는 service 의 paymentTransitions 한 곳이다.
const (
	PaymentDraft        = "draft"
	PaymentRequested    = "requested"
	PaymentApproved     = "approved"
	PaymentPaid         = "paid"
	PaymentCanceled     = "canceled"
	PaymentPaidCanceled = "paid_canceled"
)

// PaymentStatusLabels 는 화면에 보이는 한국어 상태 이름이다.
var PaymentStatusLabels = map[string]string{PaymentDraft: "임시", PaymentRequested: "승인 요청", PaymentApproved: "승인(지급 대기)", PaymentPaid: "지급 완료",
	PaymentCanceled: "취소", PaymentPaidCanceled: "지급 취소"}

// 자금 입출금 종류.
const (
	FundPayment       = "payment"
	FundPaymentCancel = "payment_cancel"
	FundDeposit       = "deposit"
)

// FundKindLabels 는 입출금 종류의 한국어 이름이다.
var FundKindLabels = map[string]string{FundPayment: "지급", FundPaymentCancel: "지급 취소", FundDeposit: "입금"}

// PayableCandidate 는 지급 요청에 넣을 수 있는 채무 항목이다(지급 가능액 = 금액 − 지급 누계 − 예약 > 0).
type PayableCandidate struct {
	PayableID    int64
	JournalNo    string
	TradeDate    string
	DueDate      string
	Amount       int64
	Paid         int64
	Reserved     int64
	Available    int64
	OverdueDays  int
	OpenRequests int // 이 항목에 진행 중인 지급 요청 수
}

// PaymentItemInput·PaymentInput 은 지급 요청 입력이다.
type PaymentItemInput struct {
	PayableID int64
	Amount    int64
}

type PaymentInput struct {
	VendorID      int64
	DueDate       string
	BankAccountID int64
	Memo          string
	Items         []PaymentItemInput
}

// PaymentItem 은 지급 요청의 한 줄이다.
type PaymentItem struct {
	PayableID int64
	JournalNo string
	TradeDate string
	DueDate   string
	Amount    int64
}

// Payment 는 지급 건이다. PayeeAccount 는 마스킹 값이다.
type Payment struct {
	ID            int64
	No            string
	VendorID      int64
	VendorName    string
	Status        string
	Total         int64
	DueDate       string
	BankAccountID int64
	BankAlias     string
	PayeeAccount  string
	Memo          string
	DocID         int64
	DocNo         string
	CreatedByID   int64
	CreatedBy     string
	CreatedAt     time.Time
	ApprovedAt    *time.Time
	CancelReason  string
	ReverseReason string
	Items         []PaymentItem
	// 지급 처리 결과(지급 완료·지급 취소 건).
	PaidDate       string
	TransferRef    string
	PaidBy         string
	JournalNo      string
	ReversalNo     string // 지급 취소로 만든 역분개 전표 번호(아직 승인 전이어도 채워진다)
	Warnings       []string
	AccountDiffers bool // 승인된 받는 계좌와 거래처 현재 계좌가 다르다(지급 처리 때 확인이 필요하다)
}

// PaymentFilter 는 목록 조건이다.
type PaymentFilter struct {
	Status   string
	VendorID int64
	Limit    int
	Offset   int
}

// PaymentPage 는 목록 한 쪽이다.
type PaymentPage struct {
	Rows  []Payment
	Total int
}

// SettleInput 은 지급 처리 입력이다. 금액은 승인된 지급액 그대로라 입력이 없다.
type SettleInput struct {
	PaidDate       string // 비면 오늘(KST)
	BankAccountID  int64  // 0 이면 승인 때 계좌
	ChangeReason   string // 계좌를 바꾸면 필수
	TransferRef    string
	Memo           string
	ConfirmAccount bool // 거래처 계좌가 승인 뒤 바뀐 경우 처리자가 확인했다
}

// CalendarEntry 는 지급 일정표의 한 칸 항목이다.
type CalendarEntry struct {
	Date       string
	Kind       string // due(만기 도래 채무) | payment(승인된 지급) | requested(승인 요청 중) | overdue(만기 경과)
	VendorName string
	Amount     int64
	PaymentNo  string
	Status     string
	Delayed    bool // 승인됐는데 예정일이 지나도록 처리되지 않음
	// NextBusinessDay 는 예정일이 휴일(주말·공휴일)일 때 지급할 수 있는 다음 영업일이다. 안내일 뿐 예정일·만기 데이터는 바꾸지 않는다(B34).
	NextBusinessDay string
}

// PaymentCalendar 는 기간의 지급 예정 일정이다.
type PaymentCalendar struct {
	From, To       string
	Entries        []CalendarEntry
	OverdueTotal   int64
	ScheduledTotal int64 // 기간의 승인된 지급 예정 합계
	BalanceTotal   int64 // 사용 중 계좌의 오늘 기말 잔액 합계
	ShortageWarn   bool
}

// PaymentService 는 지급 요청·지급 처리다. 조회 payment:read, 쓰기 payment:write(재무만).
type PaymentService interface {
	Candidates(ctx context.Context, vendorID int64) ([]PayableCandidate, error)
	List(ctx context.Context, f PaymentFilter) (PaymentPage, error)
	Get(ctx context.Context, no string) (Payment, error)
	Create(ctx context.Context, in PaymentInput) (Payment, error)
	Update(ctx context.Context, no string, in PaymentInput) (Payment, error)
	RequestApproval(ctx context.Context, no string) (Payment, error)
	Cancel(ctx context.Context, no, reason string) (Payment, error)
	Settle(ctx context.Context, no string, in SettleInput) (Payment, error)
	Reverse(ctx context.Context, no, reason string) (Payment, error)
	Calendar(ctx context.Context, from, to string) (PaymentCalendar, error)
}

// ---- 자금 계좌·자금 일보 ----

// BankAccount 는 회사 계좌다. AccountNo 는 마스킹 값이다.
type BankAccount struct {
	ID             int64
	Alias          string
	BankName       string
	AccountNo      string
	LedgerCode     string // 연결 계정 코드(1020·1010)
	LedgerName     string
	OpeningBalance int64
	OpeningDate    string
	Active         bool
	Balance        int64 // 오늘 기말 잔액(승인된 전표 기준)
}

// BankAccountInput 은 계좌 입력이다. 수정 때 계좌번호가 비면 바꾸지 않는다. 기초 잔액·기준일·연결 계정은 새 계좌에서만 받는다.
type BankAccountInput struct {
	Alias          string
	BankName       string
	AccountNo      string
	LedgerCode     string
	OpeningBalance int64
	OpeningDate    string
}

// DepositInput 은 입금 기록 입력이다(상대 계정 기본 4910 기타수익).
type DepositInput struct {
	BankAccountID int64
	Date          string
	Amount        int64
	CounterCode   string
	VendorID      int64
	Memo          string
}

// FundDailyFilter 는 자금 일보 조건이다(기간 최대 366일, 계좌 0 이면 전체).
type FundDailyFilter struct {
	From, To      string
	BankAccountID int64
}

// FundDailyRow 는 일자·계좌 한 줄이다.
type FundDailyRow struct {
	Date           string
	BankAccountID  int64
	BankAlias      string
	Opening        int64
	Deposit        int64
	Withdraw       int64 // 출금(양수로 표시)
	Closing        int64
	PendingDeposit int64 // 전표 승인 전 입금(잔액에 들어가지 않는다)
}

// FundMovementRow 는 일자 칸을 눌렀을 때 보이는 입출금 행이다.
type FundMovementRow struct {
	Date       string
	BankAlias  string
	Kind       string
	Amount     int64
	PaymentNo  string
	VendorName string
	Memo       string
	JournalNo  string
	Pending    bool
}

// FundDaily 는 자금 일보다.
type FundDaily struct {
	From, To      string
	Opening       int64 // 이월 잔액(기간 시작 전 누계, 전체 계좌 합)
	Rows          []FundDailyRow
	Movements     []FundMovementRow
	TotalDeposit  int64
	TotalWithdraw int64
	Closing       int64
	LedgerBalance int64 // 총계정원장 1020 잔액(승인된 전표)
	Matches       bool
	PendingCount  int
}

// FundService 는 자금 계좌·자금 일보다. 조회 fund:read, 관리 fund:manage, 원문 보기 mask:bypass.
type FundService interface {
	Accounts(ctx context.Context, includeInactive bool) ([]BankAccount, error)
	SaveAccount(ctx context.Context, id int64, in BankAccountInput) (BankAccount, error) // id 0 이면 새 계좌
	SetActive(ctx context.Context, id int64, active bool) (BankAccount, error)
	Daily(ctx context.Context, f FundDailyFilter) (FundDaily, error)
	RecordDeposit(ctx context.Context, in DepositInput) (Journal, error)
	// RevealAccount 는 계좌번호 원문을 돌려준다(감사 account.reveal 을 같은 트랜잭션에서 남기고, 기록하지 못하면 원문을 주지 않는다).
	RevealAccount(ctx context.Context, bankAccountID int64, screen string) (string, error)
	// RevealPayee 는 지급 건의 받는 계좌 원문이다(지급 처리자용, 같은 감사).
	RevealPayee(ctx context.Context, paymentNo string) (string, error)
}
