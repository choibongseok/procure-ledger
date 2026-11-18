package domain

import (
	"context"
	"time"
)

// B27 ② 월 마감( 3.2·3.3·9절). 기존 시그니처는 바꾸지 않고 더하기만 한다.
// 금액은 정수 원(공급가액)이다. 월은 "YYYY-MM" 문자열(KST 달)이다.

// 월 상태. 열림·마감됨은 저장된 값이고 해제 결재 중은 해제 요청서(MCR)의 문서 상태에서 파생한다.
const (
	MonthOpen           = "open"            // 열림(마감한 적 없거나 해제됨)
	MonthClosed         = "closed"          // 마감됨
	MonthReleasePending = "release_pending" // 마감됨 + 마감 해제 요청 결재 중
)

// MonthStatus 는 한 달의 마감 상태 요약이다(MCP 도구 get_month_close_status 와 화면이 같이 쓴다).
type MonthStatus struct {
	Month    string
	State    string    // MonthOpen · MonthClosed · MonthReleasePending
	Version  int       // 마감 이력이 없으면 0, 마감할 때마다 1 씩 오른다
	ClosedAt time.Time // 마감된 달만(열린 달은 zero)
	Blockers int       // 마감 전 점검의 차단 건수
	Warnings int       // 마감 전 점검의 경고 건수
	// CloseAmount 는 기말 금액 합계다. 호출자에게 inventory:valuation_read 가 없으면 서비스가 nil 로 둔다. 열린 달도 nil 일 수 있다.
	CloseAmount *int64
}

// MonthRow 는 월 마감 화면의 월 목록 한 줄이다.
type MonthRow struct {
	Month       string
	State       string
	Version     int
	ClosedAt    time.Time
	ClosedBy    string
	CloseAmount *int64 // inventory:valuation_read 가 없으면 nil
	Closable    bool   // 지금 마감을 시도할 수 있는 달(이번 달 이전, 앞선 달 모두 마감, 마감 안 됨)
	Releasable  bool   // 지금 마감 해제를 요청할 수 있는 달(가장 최근에 마감된 달, 진행 중 요청 없음)
}

// 점검 항목 종류.
const (
	PrecheckBlocker = "blocker" // 있으면 마감할 수 없다
	PrecheckWarning = "warning" // 알리기만 한다
)

// PrecheckItem 은 마감 전 점검 목록의 한 줄이다. 전표 번호와 다음 할 일을 한국어로 담는다.
type PrecheckItem struct {
	Kind   string // PrecheckBlocker · PrecheckWarning
	Title  string // 예: "결재 진행 중인 재고 조정 전표"
	Detail string // 문서 번호·상태·다음 할 일
	DocID  int64  // 연결할 문서 번호(없으면 0)
}

// Precheck 는 마감 전 점검 결과다. Blockers 가 비어 있고 앞선 조건을 만족하면 Closable 이다.
type Precheck struct {
	Month    string
	Closable bool
	Reason   string // 마감할 수 없는 이유(앞선 달 미마감·이번 달 등), 마감할 수 있으면 빈 값
	Blockers []PrecheckItem
	Warnings []PrecheckItem
}

// MonthCloseService 는 월 마감이다. 읽기는 inventory:read, 마감·해제 요청은 inventory:close 다.
type MonthCloseService interface {
	// List 는 마감 이력이 있는 달과 지금 마감할 수 있는 달의 목록이다(최근 달이 먼저).
	List(ctx context.Context) ([]MonthRow, error)
	// Precheck 는 마감 전 점검(차단·경고 목록)이다. 읽기 전용이고 감사를 남기지 않는다.
	Precheck(ctx context.Context, month string) (Precheck, error)
	// Close 는 월을 마감한다(점검 재확인·스냅샷·감사가 한 트랜잭션).
	Close(ctx context.Context, month string) (MonthRow, error)
	// RequestRelease 는 마감 해제 요청서(양식 MCR)를 만들어 상신하고 문서 번호를 돌려준다.
	RequestRelease(ctx context.Context, month, reason string) (docID int64, err error)
	// Status 는 한 달의 상태 요약이다(inventory:read). 월 형식 오류는 한국어 ErrValidation 이다.
	Status(ctx context.Context, month string) (MonthStatus, error)
}
