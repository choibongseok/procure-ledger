package domain

import (
	"context"
	"time"
)

// 법인카드 서비스(erp, 3.3). 카드 CSV 정규화(CardParser)는 card.go, 이 파일은 업무 서비스의 도메인 타입이다.

// 카드 내역 상태(4절). 전이는 service 의 cardTransitions 표 한 곳이다.
const (
	CardTxnUnreconciled    = "unreconciled"     // 미소명
	CardTxnSubmitted       = "submitted"        // 소명됨(확인 대기)
	CardTxnConfirmed       = "confirmed"        // 확인됨
	CardTxnNeedsFix        = "needs_fix"        // 보완 필요
	CardTxnNotRequired     = "not_required"     // 소명 불필요(취소 상계)
	CardTxnOrphanCancel    = "orphan_cancel"    // 원거래 없는 취소
	CardTxnRefundRequested = "refund_requested" // 환입 요청
	CardTxnRefunded        = "refunded"         // 환입 완료
)

// CardTxnStatusLabels 는 화면에 보이는 한국어 상태 이름이다.
var CardTxnStatusLabels = map[string]string{CardTxnUnreconciled: "미소명", CardTxnSubmitted: "소명됨(확인 대기)", CardTxnConfirmed: "확인됨", CardTxnNeedsFix: "보완 필요",
	CardTxnNotRequired: "소명 불필요(취소됨)", CardTxnOrphanCancel: "원거래 없는 취소", CardTxnRefundRequested: "환입 요청", CardTxnRefunded: "환입 완료"}

// CardVATReasons 는 부가세 불공제 사유다.
var CardVATReasons = map[string]string{"entertainment": "접대비", "personal_suspect": "개인 사용 의심", "other": "기타"}

// Card 는 법인카드다. 끝 4자리만 있다(전체 번호는 저장하지 않는다).
type Card struct {
	ID          int64
	Alias       string
	Issuer      string
	IssuerLabel string
	Last4       string
	HolderID    int64
	HolderName  string
	Active      bool
}

// CardHolderChange 는 카드 소지자 변경 이력 한 줄이다(추가 전용). HolderName 이 비면 소지자 없음으로 바꾼 기록이다.
type CardHolderChange struct {
	HolderName string
	FromDate   string // 이 날짜 이후 업로드되는 내역부터 이 소지자다
	ChangedBy  string
	ChangedAt  time.Time
}

// CardInput 은 카드 등록·수정 입력이다. 수정에서는 카드사·끝 4자리를 바꾸지 않는다.
type CardInput struct {
	Alias    string
	Issuer   string
	Last4    string
	HolderID int64
}

// CardImportPreview 는 업로드 미리보기(저장하지 않음)다.
type CardImportPreview struct {
	Format       string
	FormatLabel  string
	Total        int
	New          int
	Duplicate    int
	Errors       int
	ErrorRows    []CardRowError
	TotalAmount  int64
	Unregistered []string // "…1234 3건"
	Warnings     []string
}

// CardImportResult 는 업로드 확정 결과다.
type CardImportResult struct {
	ImportID  int64
	Total     int
	New       int
	Duplicate int
	Errors    int
}

// CardExplanation 은 소명 한 건(이력 행)이다.
type CardExplanation struct {
	ID           int64
	Purpose      string
	AccountCode  string
	AccountName  string
	VATDeduct    bool
	VATReason    string
	Supply       int64
	VAT          int64
	Personal     bool
	Status       string // submitted | returned | confirmed | withdrawn
	SubmittedAt  time.Time
	ConfirmedBy  string
	ReturnReason string
	Attachments  []Attachment
}

// CardTxn 은 카드 내역 한 건이다. 카드번호 조각은 없다(카드 별칭만).
type CardTxn struct {
	ID          int64
	CardID      int64
	CardAlias   string
	HolderID    int64
	HolderName  string
	UsedAt      time.Time
	Merchant    string
	Amount      int64
	Supply      *int64
	VAT         *int64
	Tip         *int64
	ApprovalNo  string
	Kind        string
	Status      string
	DueDate     string
	DaysLeft    int // 소명 기한까지 남은 일수(음수면 경과)
	Overdue     bool
	Explanation *CardExplanation // 가장 최근 소명(없으면 nil)
	History     []CardExplanation
	JournalNo   string
	RefundNote  string
}

// CardFilter 는 내역 목록 조건이다.
type CardFilter struct {
	Status   string
	From, To string
	Limit    int
	Offset   int
}

// ExplainInput 은 소명 입력이다. VATDeduct 는 nil 이면 공제다.
type ExplainInput struct {
	Purpose       string
	AccountCode   string
	VATDeduct     *bool
	VATReason     string
	Personal      bool
	AttachmentIDs []int64
}

// CardBatchResult 는 일괄 확인의 건별 결과다.
type CardBatchResult struct {
	ID        int64
	OK        bool
	Error     string
	JournalNo string
}

// CardOverview 는 재무 현황이다.
type CardOverview struct {
	Counts          map[string]int
	Amounts         map[string]int64
	Delayed         []CardTxn // 소명 기한 경과 30일 이상 미소명
	NoHolder        []CardTxn // 소지자가 없거나 퇴사한 미소명
	NeedsFinance    []CardTxn // 원거래 없는 취소, 확인된 건의 취소(재무 처리 필요)
	RefundRequested []CardTxn
}

// CardService 는 법인카드 서비스다(권한은 5절 표). 본인 범위는 서비스가 판정한다.
type CardService interface {
	Cards(ctx context.Context) ([]Card, error)
	SaveCard(ctx context.Context, id int64, in CardInput) (Card, error)
	ChangeHolder(ctx context.Context, id, holderID int64) (Card, error)
	HolderHistory(ctx context.Context, id int64) ([]CardHolderChange, error)
	SetCardActive(ctx context.Context, id int64, active bool) (Card, error)
	PreviewImport(ctx context.Context, format string, data []byte) (CardImportPreview, error)
	ConfirmImport(ctx context.Context, format string, data []byte) (CardImportResult, error)
	Mine(ctx context.Context, f CardFilter) ([]CardTxn, error)
	GetTxn(ctx context.Context, id int64) (CardTxn, error)
	// GetMyTxn 은 "내 카드 내역" 경로의 단건 조회다: 호출자 본인 소지분만 열리고 타인 내역은 재무라도 찾을 수 없음이다(전체 조회는 GetTxn 의 card:read_all 경로).
	GetMyTxn(ctx context.Context, id int64) (CardTxn, error)
	Explain(ctx context.Context, txnID int64, in ExplainInput) (CardTxn, error)
	Withdraw(ctx context.Context, txnID int64) (CardTxn, error)
	ToConfirm(ctx context.Context) ([]CardTxn, error)
	Confirm(ctx context.Context, ids []int64) ([]CardBatchResult, error)
	Return(ctx context.Context, id int64, reason string) (CardTxn, error)
	Overview(ctx context.Context) (CardOverview, error)
	All(ctx context.Context, f CardFilter) ([]CardTxn, int, error)
	RequestRefund(ctx context.Context, id int64, reason string) (CardTxn, error)
	CompleteRefund(ctx context.Context, id int64, refundDate, note string) (CardTxn, error)
	ReassignHolder(ctx context.Context, txnID, holderID int64) (CardTxn, error)
	ExplainAccounts(ctx context.Context) ([]Account, error)
}
