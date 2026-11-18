package domain

import (
	"context"
	"time"
)

// B25 ① 부서·월 예산과 예산 확인(). 기존 시그니처는 바꾸지 않고 더하기만 한다.
// 금액은 모두 정수 원(공급가액, 부가세 별도)이다.

// 예산 판정 상태(구매요청 한 차수마다 하나).
const (
	BudgetNone         = "none"         // 판정 전(임시저장·기존 요청)
	BudgetWithin       = "within"       // 잔액 이내
	BudgetOver         = "over"         // 잔액 초과: 결재선에 재무 단계가 강제로 붙는다
	BudgetUnregistered = "unregistered" // 그 부서·월의 예산이 등록되지 않아 확인하지 않음
)

// 예산 표의 배지(사용률 기준).
const (
	BadgeOK   = "ok"   // 여유(80% 미만)
	BadgeNear = "near" // 한도 근접(80% 이상 100% 이하)
	BadgeOver = "over" // 예산 초과(100% 초과)
)

// BudgetSnapshot 은 상신 때 판정한 예산 값이다(요청에 저장되어 이후 예산이 바뀌어도 그대로다).
type BudgetSnapshot struct {
	State      string
	Month      string // YYYY-MM
	Budget     int64
	Used       int64
	InProgress int64
	Amount     int64 // 이번 요청 금액
	Over       int64 // 초과 금액(초과가 아니면 0)
	// FinanceAdded 는 예산 초과로 결재선에 재무 단계를 실제로 더했는지다. 금액 구간으로 이미 재무 단계가 있었으면 false.
	// nil 은 알 수 없음(이 값을 적기 전에 상신된 옛 스냅샷).
	FinanceAdded *bool
}

// BudgetRow 는 예산 표의 한 줄(부서·월)이다.
type BudgetRow struct {
	DepartmentID   int64
	DepartmentName string
	Month          string
	Amount         int64 // 월 예산
	Used           int64 // 승인 완료 사용액(발주가 만들어진 건)
	InProgress     int64 // 결재 진행 중(상신~결재중)
	Balance        int64 // 잔액 = 월 예산 − 사용액 − 진행 중(음수 가능)
	UsagePct       int   // 정수 퍼센트(내림). 월 예산이 0 이면 -1("-")
	Badge          string
}

// BudgetInput 은 예산 등록·수정 입력이다.
type BudgetInput struct {
	DepartmentID int64
	Month        string // YYYY-MM, 이번 달 이후만
	Amount       int64
	Reason       string
}

// Budget 은 저장된 예산 한 줄이다.
type Budget struct {
	ID             int64
	DepartmentID   int64
	DepartmentName string
	Month          string
	Amount         int64
	UpdatedAt      time.Time
}

// BudgetRevision 은 예산 수정 이력 한 줄이다.
type BudgetRevision struct {
	DepartmentName string
	Month          string
	OldAmount      *int64 // 처음 등록이면 nil
	NewAmount      int64
	Reason         string
	ActorName      string
	At             time.Time
}

// BudgetCheckInput 은 예산 확인 입력이다. DepartmentID 0 이면 호출자 본인 부서, 다른 부서는 budget:read 가 있어야 한다.
type BudgetCheckInput struct {
	Amount       int64
	DepartmentID int64
	Month        string // YYYY-MM, 비면 이번 달(KST)
	PRID         int64  // 이 요청 자신은 진행 중 합계에서 뺀다(0 이면 없음)
}

// BudgetCheck 는 예산 확인 결과다(작성 화면 카드·상신 판정이 같은 계산을 쓴다).
type BudgetCheck struct {
	Month          string
	DepartmentID   int64
	DepartmentName string
	Registered     bool
	Budget         int64
	Used           int64
	InProgress     int64
	Balance        int64
	Amount         int64
	Over           int64 // 초과 금액
	After          int64 // 요청 뒤 잔액
	State          string
}

// BudgetService 는 부서·월 예산이다.
type BudgetService interface {
	List(ctx context.Context, month string) ([]BudgetRow, error) // budget:read
	Set(ctx context.Context, in BudgetInput) (Budget, error)     // budget:manage
	Revisions(ctx context.Context, month string) ([]BudgetRevision, error)
	Check(ctx context.Context, in BudgetCheckInput) (BudgetCheck, error) // purchase:create(본인 부서)
}
