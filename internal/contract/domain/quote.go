package domain

import (
	"context"
	"time"
)

// B25 ② 견적서 첨부와 거래처별 비교( 3.2). 기존 시그니처는 바꾸지 않고 더하기만 한다.
// 금액은 정수 원(공급가액)이다.

// 견적서 상태. 값을 직접 고치면 Edited 가 켜진다.
const (
	QuoteRead       = "read"       // AI 가 값을 읽음
	QuoteUnreadable = "unreadable" // 읽지 못함(직접 입력해야 한다)
)

// QuoteLine 은 견적서의 품목 한 줄이다.
type QuoteLine struct {
	Name      string
	Quantity  int64
	UnitPrice int64
	Amount    int64
}

// Quote 는 견적서 한 건과 비교 표의 계산 결과다.
type Quote struct {
	ID          int64
	PRID        int64
	VendorID    int64 // 0 이면 거래처를 아직 연결하지 못한 견적
	VendorName  string
	Supplier    string // 견적서에 적힌 거래처 이름(연결 전 표시용)
	Filename    string
	Status      string
	Edited      bool
	Lines       []QuoteLine
	Total       int64 // 줄 금액 합(비교 합계)
	HasDocTotal bool
	DocTotal    int64 // 문서에 적힌 합계
	HasLead     bool
	LeadDays    int
	LeadText    string
	Warnings    []string
	Filled      bool // 비교할 값이 있다(읽었거나 직접 입력함)
	Selected    bool
	Lowest      bool  // 최저가(동률 포함)
	OverLowest  int64 // 최저가보다 비싼 금액(최저가면 0)
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// QuoteInput 은 견적 값 직접 입력·수정이다. 거래처는 목록에서 사람이 고른다.
type QuoteInput struct {
	VendorID int64
	Lines    []QuoteLine
	DocTotal *int64 // 비우면 문서 합계 없음
	LeadDays *int   // 발주 후 일수, 비우면 없음
	LeadText string
}

// QuoteCompare 는 한 구매요청의 견적 비교 결과다.
type QuoteCompare struct {
	Quotes         []Quote
	Editable       bool   // 작성 중·반려일 때만 바꿀 수 있다
	SelectedID     int64  // 0 이면 선택 없음
	Reason         string // 최저가가 아닌 선택의 사유
	ReasonRequired bool   // 견적 2건 이상이고 선택이 최저가가 아님
	LowestTotal    int64
	PRTotal        int64
	TotalMismatch  bool   // 선택한 견적 합계와 요청 금액이 다름(경고만)
	Note           string // 선택한 거래처가 최저가보다 비싼 정도 한 줄 안내(없으면 빈 값)
}

// QuoteService 는 견적서 첨부·비교·선택이다. 읽기는 구매요청 열람 범위, 쓰기는 purchase:create 와 본인 작성 중 요청이다.
type QuoteService interface {
	Compare(ctx context.Context, prID int64) (QuoteCompare, error)
	Attach(ctx context.Context, prID int64, filename, contentType string, data []byte) (Quote, error)
	Update(ctx context.Context, quoteID int64, in QuoteInput) (Quote, error)
	Remove(ctx context.Context, quoteID int64) error
	// Select 는 견적을 고른다(quoteID 0 이면 선택 해제). 고르면 구매요청의 거래처가 그 거래처가 된다.
	Select(ctx context.Context, prID, quoteID int64, reason string) (QuoteCompare, error)
}
