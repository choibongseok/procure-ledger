package domain

import (
	"context"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
)

// B25 단계 3: 발주서 출력(③)과 거래처 평가(④) ( 3.3·3.4). 기존 시그니처는 바꾸지 않고 더하기만 한다.

// POPrintLine 은 발주서 품목 한 줄이다.
type POPrintLine struct {
	No        int
	ItemName  string
	Unit      string
	Quantity  int64
	UnitPrice int64
	Amount    int64
}

// POPrint 는 발주서 출력 화면의 내용이다. 금액은 공급가액 기준이고 부가세(10%, 원 미만 버림)는 발주서에서만 계산한다.
type POPrint struct {
	PRID          int64
	PRNo          string
	PONo          string
	PODate        time.Time
	Company       string // 발주처 회사 이름(회사 설정의 회사명, 설정이 없으면 조직도 최상위 조직)
	CompanyBizNo  string // 회사 설정의 사업자등록번호(없으면 빈 값)
	CompanyRep    string // 대표자
	CompanyAddr   string // 주소
	CompanyPhone  string // 대표 전화
	VATRate       int    // 부가세율(%): 발주일에 유효한 세율. 화면 라벨이 이 값을 쓴다
	HasLogo       bool   // 로고 이미지가 있다(/api/company/logo)
	HasSeal       bool   // 직인 이미지가 있다(/api/company/seal, 출력 화면에서만 쓴다)
	Department    string // 요청 부서(요청자 소속 경로)
	Requester     string
	SupplierName  string
	BusinessNo    string // mask:bypass 가 없으면 가려진 값
	Phone         string
	Email         string
	ContactEmpty  bool // 거래처 연락처(전화·이메일)가 비어 있다
	Masked        bool // 사업자번호·연락처가 가려져 나갔다
	DeliveryPlace string
	DueDate       string // 없으면 빈 값("협의"로 출력)
	Lines         []POPrintLine
	Supply        int64
	VAT           int64
	Total         int64
	Note          string
}

// POVAT 는 공급가액의 10% 를 원 미만 버림으로 계산한 부가세다(D10).
func POVAT(supply int64) int64 {
	if supply <= 0 {
		return 0
	}
	return supply / 10
}

// POPrintService 는 발주서 출력이다. 구매요청을 볼 수 있는 사람만 본다(범위 밖은 404). RecordPrint 는 인쇄 버튼을 눌렀을 때 감사(po.print)를 남긴다.
type POPrintService interface {
	Print(ctx context.Context, prID int64) (POPrint, error)
	RecordPrint(ctx context.Context, prID int64) error
}

// 거래처 평가 배지.
const (
	RatingGood  = "good"  // 양호
	RatingWarn  = "warn"  // 주의
	RatingBad   = "bad"   // 지연 많음 / 불일치 많음
	RatingSmall = "small" // 표본 적음(분모 3건 미만)
	RatingNone  = "none"  // 계산할 것이 없음
)

// VendorRating 은 한 거래처의 기간 평가다. 비율은 정수 퍼센트(내림)이고 계산할 수 없으면 -1 이다.
type VendorRating struct {
	VendorID      int64
	VendorName    string
	Orders        int // 기간 안에 입고가 끝난 발주 수
	OnTime        int // 그 중 납기 내 입고(분자)
	WithDue       int // 그 중 납기가 있는 발주(분모)
	NoDue         int // 납기 미지정으로 제외한 발주 수
	OnTimePct     int
	OnTimeBadge   string
	Verdicts      int // 기간 안에 확정된 판정 중 일치·불일치(분모)
	Mismatches    int // 그 중 확정 결과가 불일치(분자)
	MismatchPct   int
	MismatchBadge string
	LateOpen      int // 납기를 넘겼는데 아직 입고가 끝나지 않은 발주 수(점수에는 넣지 않는다, D11)
}

// VendorRatingLate 는 납기를 넘겨 입고된(또는 아직 입고되지 않은) 발주 한 건이다.
type VendorRatingLate struct {
	PONo       string
	DueDate    string
	ReceivedOn string // 마지막 입고일(미입고면 빈 값)
	DaysLate   int
}

// VendorRatingMismatch 는 확정 결과가 불일치인 판정 한 건이다.
type VendorRatingMismatch struct {
	VerdictID   int64
	DocumentID  int64
	PONo        string
	ConfirmedOn string
}

// VendorRatingDetail 은 거래처 상세(지연 입고·미입고·불일치 판정)다.
type VendorRatingDetail struct {
	VendorRating
	Late       []VendorRatingLate
	OpenLate   []VendorRatingLate
	Mismatched []VendorRatingMismatch
}

// VendorRatingService 는 거래처 평가다(읽기 전용, 조회 때 계산). 권한은 vendor:rating_read 다.
type VendorRatingService interface {
	Rate(ctx context.Context, from, to contract.Day) ([]VendorRating, error)
	Detail(ctx context.Context, vendorID int64, from, to contract.Day) (VendorRatingDetail, error)
}
