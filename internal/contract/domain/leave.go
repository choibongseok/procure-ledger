package domain

import "context"

// B50 근태·휴가(). 휴가 종류·연차 규칙·잔여 원장·휴가 신청. 웹 핸들러와 MCP 도구(ai)가 같은 서비스를 쓴다.
// 잔여는 원장의 합이다(분 단위, 하루 = 규칙의 workday_minutes). 수량을 직접 고치는 경로는 없다.

// 잔여 묶음.
const (
	LeaveBalanceAnnual       = "annual"       // 연차(반차·시간 단위 포함)
	LeaveBalanceCompensatory = "compensatory" // 대체휴가
)

// 휴가 신청 상태(문서 상태의 투영).
const (
	LeavePending  = "pending"  // 신청 대기(결재 중, 잔여 예약)
	LeaveApproved = "approved" // 승인됨
	LeaveRejected = "rejected" // 반려됨
	LeaveRecalled = "recalled" // 회수됨(상신 취소)
	LeaveCanceled = "canceled" // 승인 뒤 취소됨
)

// LeaveStatusLabels 는 상태의 한국어 이름이다.
var LeaveStatusLabels = map[string]string{LeavePending: "신청 대기", LeaveApproved: "승인됨", LeaveRejected: "반려됨", LeaveRecalled: "회수됨", LeaveCanceled: "취소됨"}

// LeaveBalanceLabels 는 잔여 묶음의 한국어 이름이다.
var LeaveBalanceLabels = map[string]string{LeaveBalanceAnnual: "연차", LeaveBalanceCompensatory: "대체휴가"}

// 신청 구간.
const (
	LeavePartFull  = "full"  // 종일
	LeavePartAM    = "am"    // 오전 반차
	LeavePartPM    = "pm"    // 오후 반차
	LeavePartHours = "hours" // 시간 지정
)

// LeaveType 은 휴가 종류다(법인 소유).
type LeaveType struct {
	ID            int64
	Code          string
	Name          string
	Unit          string // day | hour
	Balance       string // 차감하는 잔여 묶음. 비면 잔여를 쓰지 않는다
	Paid          bool
	ProofRequired bool
	MaxDays       int // 0 = 상한 없음
	System        bool
	Active        bool
}

// LeaveTypeInput 은 휴가 종류 추가·수정 입력이다. 시스템 종류는 이름·증빙·유급만 바뀐다.
type LeaveTypeInput struct {
	Code          string
	Name          string
	Unit          string
	Balance       string
	Paid          bool
	ProofRequired bool
	MaxDays       int
	Active        bool
}

// LeavePolicy 는 연차 발생 규칙 한 건이다(효력일 이력).
type LeavePolicy struct {
	ID               int64
	EffectiveFrom    string
	Basis            string // hire | fiscal
	WorkdayMinutes   int
	HourUnitMinutes  int
	AttendanceRule   bool
	CarryMaxMinutes  int // -1 = 한도 없음, 0 = 이월 없음
	CarryValidMonths int
	ExpireMonths     int
	FirstYearMode    string // monthly | upfront
	CreatedByName    string
	CreatedAt        string
	Canceled         bool
}

// LeavePolicyInput 은 규칙 저장 입력이다. 저장하면 새 이력 행이 되고 효력일은 오늘 이후(오늘 포함)여야 한다.
type LeavePolicyInput struct {
	EffectiveFrom    string
	Basis            string
	WorkdayMinutes   int
	HourUnitMinutes  int
	AttendanceRule   bool
	CarryMaxMinutes  int
	CarryValidMonths int
	ExpireMonths     int
	FirstYearMode    string
}

// LeaveBalanceLine 은 한 잔여 묶음의 현재 상태(분)다.
type LeaveBalanceLine struct {
	Balance         string
	Minutes         int64 // 사용 가능(예약 차감 뒤)
	Reserved        int64 // 신청 대기 중 예약된 분량
	ExpiringMinutes int64 // 30일 안에 소멸하는 분량
	ExpiringOn      string
}

// LeaveBalance 는 한 사람의 잔여다.
type LeaveBalance struct {
	UserID         int64
	WorkdayMinutes int
	Lines          []LeaveBalanceLine
}

// LeaveLedgerLine 은 원장 한 줄이다.
type LeaveLedgerLine struct {
	ID            int64
	Balance       string
	Kind          string
	Minutes       int64
	Reason        string
	RefType       string
	RefID         int64
	ExpiresOn     string
	CreatedAt     string
	CreatedBy     int64
	CreatedByName string
}

// LeaveAdjustInput 은 인사 담당의 조정·수동 발생·초기 이관 입력이다(사유 필수).
type LeaveAdjustInput struct {
	UserID    int64
	Balance   string
	Kind      string // adjust | opening
	Minutes   int64  // 양수 = 늘림, 음수 = 줄임
	ExpiresOn string // 선택(소멸일, 양수 줄에만)
	Reason    string
}

// LeaveInput 은 휴가 신청·미리보기 입력이다.
type LeaveInput struct {
	LeaveCode         string
	StartDate         string
	EndDate           string
	Part              string // full | am | pm | hours
	StartTime         string // hours 일 때 HH:MM
	EndTime           string
	Reason            string // 선택(민감 정보: 문서 본문에 넣지 않는다)
	DelegateUserID    int64
	ProofAttachmentID int64
}

// LeaveDay 는 미리보기의 날짜별 계산이다.
type LeaveDay struct {
	Date    string
	Minutes int64
	Skipped string // 비어 있지 않으면 세지 않은 날과 이유(주말·휴일)
}

// LeavePreview 는 신청 전 계산 결과다(Request 와 같은 함수).
type LeavePreview struct {
	Minutes        int64
	Days           []LeaveDay
	Balance        string
	BalanceBefore  int64
	BalanceAfter   int64
	WorkdayMinutes int
	Warnings       []string
}

// LeaveRequest 는 휴가 신청 한 건이다. Reason 은 본인·직속 조직장·인사 담당에게만 채워진다.
type LeaveRequest struct {
	ID             int64
	DocID          int64
	DocNo          string
	UserID         int64
	UserName       string
	LeaveCode      string
	LeaveName      string
	StartDate      string
	EndDate        string
	Part           string
	StartTime      string
	EndTime        string
	Minutes        int64
	Status         string
	Reason         string
	HasReason      bool
	DelegateUserID int64
	CreatedAt      string
	// Note 는 안내 문구다(부재 설정을 만들지 못한 이유 등). 사유가 아니다.
	Note                string
	AbsenceDelegationID int64 // 승인 때 만든 부재(대결) 행 번호. 0 이면 없다
	ProofAttachmentID   int64 // 증빙 첨부 번호. 0 이면 없다(내려받기는 열람 기록을 남긴다)
}

// LeaveService 는 휴가 종류·규칙·잔여·신청 서비스다.
type LeaveService interface {
	Types(ctx context.Context) ([]LeaveType, error)      // 신청 화면용: 사용 중인 종류만
	AdminTypes(ctx context.Context) ([]LeaveType, error) // 설정 화면용: 사용 중지 포함(모듈이 꺼져 있어도 읽는다)
	SaveType(ctx context.Context, id int64, in LeaveTypeInput) (LeaveType, error)
	Policies(ctx context.Context) ([]LeavePolicy, error)
	SavePolicy(ctx context.Context, in LeavePolicyInput) (LeavePolicy, error)
	CancelPolicy(ctx context.Context, id int64) error
	Balance(ctx context.Context, userID int64) (LeaveBalance, error)
	Ledger(ctx context.Context, userID int64, limit int) ([]LeaveLedgerLine, error)
	Adjust(ctx context.Context, in LeaveAdjustInput) error
	Preview(ctx context.Context, in LeaveInput) (LeavePreview, error)
	Request(ctx context.Context, in LeaveInput) (LeaveRequest, error)
	Cancel(ctx context.Context, id int64, reason string) (LeaveRequest, error)
	MyRequests(ctx context.Context, limit int) ([]LeaveRequest, error)
	Get(ctx context.Context, id int64) (LeaveRequest, error)
}
