package domain

import (
	"context"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
)

// B28 erp: 업무 보고서 3종( 5~7절). 읽기 전용, 권한 report:read(거래처 탭은 vendor:rating_read 도 필요).
// 계산은 서버가 하고, 집계 대상은 ViewScope 로 줄어든다(같은 보고서를 다른 사람이 열면 숫자가 다르다).

// PurchaseReportQuery 는 구매 실적 조건이다. FromMonth·ToMonth 는 예산 월(YYYY-MM, 양끝 포함, 최대 12개월). DepartmentID 0 이면 열람 범위 안의 전체.
type PurchaseReportQuery struct {
	FromMonth    string
	ToMonth      string
	DepartmentID int64
}

// PurchaseReportRow 는 부서 × 예산 월 한 줄이다. Budget 이 nil 이면 그 부서·월의 예산이 등록되지 않은 것이다(집행률도 nil).
type PurchaseReportRow struct {
	DepartmentID   int64
	DepartmentName string
	Month          string
	Budget         *int64
	Orders         int   // 사용액에 든 건수(발주가 생성된 구매요청 수)
	Used           int64 // 발주가 생성된 구매요청의 공급가액 합
	InProgress     int64 // 상신~결재 중인 구매요청의 공급가액 합
	Balance        *int64
	ExecutionPct   *float64 // 사용액 ÷ 월 예산 × 100(소수 한 자리). 예산이 없거나 0 원이면 nil
	ForecastPct    *float64 // (사용액 + 결재 진행 중) ÷ 월 예산 × 100
	Badge          string   // ok | near | over | none(예산 없음)
}

// PurchaseReportTotals 는 합계 줄이다. 집행률은 예산이 있는 부서·월만 분모·분자에 넣고, 예산이 없는 부서·월의 사용액은 UnbudgetedUsed 로 따로 알린다.
type PurchaseReportTotals struct {
	Orders         int
	Used           int64
	InProgress     int64
	Budget         int64 // 예산이 있는 부서·월의 월 예산 합
	BudgetedUsed   int64 // 그 부서·월의 사용액 합(집행률의 분자)
	ExecutionPct   *float64
	UnbudgetedUsed int64
}

// PurchaseReport 는 구매 실적 보고서다.
type PurchaseReport struct {
	From, To string // 실제로 쓴 기간(요청이 비면 이번 회계연도 시작 월~이번 달)
	Rows     []PurchaseReportRow
	Totals   PurchaseReportTotals
}

// VendorReportRow 는 거래처 한 줄이다: B25 평가(VendorRating)에 발주 건수·발주 금액을 더한다.
type VendorReportRow struct {
	VendorRating
	PurchaseOrders int   // 기간 안에 발주일이 있는 발주 수
	OrderAmount    int64 // 그 발주의 공급가액 합
}

// ApprovalReportQuery 는 결재 소요 시간 조건이다. From·To 는 단계가 처리된 날(KST).
type ApprovalReportQuery struct {
	From         contract.Day
	To           contract.Day
	FormCode     string
	DepartmentID int64
}

// StepStat 는 양식 × 단계 한 줄이다. StepTitle 이 "전체"이면 그 양식의 전체 소요(상신~결재 완료)다. 소요는 분 단위다.
type StepStat struct {
	FormCode    string
	FormName    string
	StepTitle   string
	Count       int
	AvgMinutes  int64
	MedMinutes  int64
	MaxMinutes  int64
	Rejected    int
	Objected    int
	IsFormTotal bool
}

// DelayedDoc 는 지금 진행 중인 문서의 현재 단계 경과(지연 상위)다.
type DelayedDoc struct {
	DocID          int64
	DocNo          string
	Title          string
	FormName       string
	StepTitle      string
	ApproverName   string // 결재자가 없으면 빈 값("결재자 없음"으로 보인다)
	NoApprover     bool
	ElapsedMinutes int64
	StepStart      time.Time
	Link           string // purchase | general: 화면 경로를 정하는 종류
}

// ApprovalReport 는 결재 소요 시간 보고서다. Excluded 는 시작 시각을 역산할 수 없어 평균에서 뺀 단계 수다.
type ApprovalReport struct {
	Stats    []StepStat
	Delayed  []DelayedDoc
	Excluded int
}

// ReportOptions 는 보고서 필터 선택지(부서·양식)다.
type ReportOptions struct {
	Departments []DepartmentRef
	Forms       []FormRef
}

// FormRef 는 양식 선택지다.
type FormRef struct {
	Code string
	Name string
}

// ReportService 는 보고서 3종이다(읽기 전용).
type ReportService interface {
	Options(ctx context.Context) (ReportOptions, error)
	Purchase(ctx context.Context, q PurchaseReportQuery) (PurchaseReport, error)
	Vendors(ctx context.Context, from, to contract.Day) ([]VendorReportRow, error)
	Approvals(ctx context.Context, q ApprovalReportQuery) (ApprovalReport, error)
}
