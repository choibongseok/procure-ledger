package domain

import "context"

// B50 5단계: 출퇴근 기록·근무 유형·정정·근태 마감·주 근로시간 경고(명세 7절, 9.4). 웹 핸들러와 MCP 도구(ai)가 같은 서비스를 쓴다.
// 시각은 서버가 KST 로 정하고(클라이언트 시각을 받지 않는다), 정정·마감 입력의 시각은 "HH:MM"(KST)이다.

// 근무 유형.
const (
	WorkFixed     = "fixed"     // 고정: 출근·퇴근 시각 고정
	WorkStaggered = "staggered" // 시차: 출근 가능 구간 중 선택, 하루 소정 시간 고정
	WorkFlexible  = "flexible"  // 선택: 코어 타임만 정하고 주 단위 총량
)

// WorkKindLabels 는 근무 유형의 한국어 이름이다.
var WorkKindLabels = map[string]string{WorkFixed: "고정", WorkStaggered: "시차", WorkFlexible: "선택"}

// 정정 상태.
const (
	CorrectionRequested = "requested"
	CorrectionApplied   = "applied"
	CorrectionRejected  = "rejected"
)

// CorrectionStatusLabels 는 정정 상태의 한국어 이름이다.
var CorrectionStatusLabels = map[string]string{CorrectionRequested: "승인 대기", CorrectionApplied: "반영됨", CorrectionRejected: "반려됨"}

// 주 근로시간 경고 단계.
const (
	WeekOK      = ""
	WeekCaution = "caution" // 48시간 도달(주의)
	WeekOver    = "over"    // 52시간 초과(경고)
)

// WeekLevelLabels 는 경고 단계의 한국어 이름이다.
var WeekLevelLabels = map[string]string{WeekCaution: "주의", WeekOver: "경고"}

// WorkType 은 근무 유형 한 건이다(법인 소유). 시각은 "HH:MM".
type WorkType struct {
	ID            int64
	Code          string
	Name          string
	Kind          string
	StartTime     string // 고정: 출근 시각 / 시차: 출근 가능 구간 시작
	EndTime       string // 고정: 퇴근 시각 / 시차: 출근 가능 구간 끝
	BreakStart    string
	BreakEnd      string
	CoreStart     string // 선택: 코어 타임
	CoreEnd       string
	WeeklyMinutes int // 주 소정근로(분). 하루 소정 = 이 값 / 5
	Active        bool
	Default       bool // 배정이 없는 사람에게 적용되는 기본 유형(저장되지 않는다)
}

// WorkTypeInput 은 근무 유형 추가·수정 입력이다.
type WorkTypeInput struct {
	Code          string
	Name          string
	Kind          string
	StartTime     string
	EndTime       string
	BreakStart    string
	BreakEnd      string
	CoreStart     string
	CoreEnd       string
	WeeklyMinutes int
	Active        bool
}

// WorkAssignment 는 사람의 근무 유형 배정 이력 한 줄이다.
type WorkAssignment struct {
	ID         int64
	UserID     int64
	WorkTypeID int64
	TypeName   string
	ValidFrom  string
	CreatedBy  string
}

// AttendanceDay 는 하루 출퇴근 기록과 판정이다. 시각은 "HH:MM"(KST)이고 비어 있으면 아직 없다.
type AttendanceDay struct {
	Date         string
	CheckIn      string
	CheckOut     string
	Source       string
	WorkMinutes  int  // 체크인·체크아웃이 모두 있을 때 휴게를 뺀 근로 분
	Missing      bool // 출근만 있고 퇴근이 없다(지난 날이면 "기록 누락")
	Late         bool
	EarlyLeave   bool
	CoreMiss     bool // 선택 근무의 코어 타임 미충족
	HolidayNote  string
	LeaveNote    string
	CorrectionID int64 // 승인 대기 중인 정정 요청이 있으면 그 번호
}

// AttendanceWeek 는 한 주(월~일)의 기록과 합계다.
type AttendanceWeek struct {
	WeekStart     string
	WeekEnd       string
	Days          []AttendanceDay
	TotalMinutes  int
	Level         string // WeekOK | WeekCaution | WeekOver
	WeeklyTarget  int    // 선택 근무의 주 소정(분), 아니면 0
	BelowTarget   bool   // 선택 근무의 주 총량 미달 안내(경고가 아니다)
	WorkTypeName  string
	CautionMinute int // 주의 기준(분)
	OverMinute    int // 경고 기준(분)
}

// AttendanceToday 는 내 근태 화면의 첫 줄이다(오늘 기록·주 요약·안내).
type AttendanceToday struct {
	Today            AttendanceDay
	Week             AttendanceWeek
	MissingYesterday string // 어제 퇴근 기록이 없으면 안내 문구
	Warnings         []string
}

// Correction 은 정정 요청·결과 한 줄이다. 사유는 민감하지 않은 업무 사유로 본다(감사에 그대로 남는다).
type Correction struct {
	ID            int64
	UserID        int64
	UserName      string
	WorkDate      string
	OldIn         string
	OldOut        string
	NewIn         string
	NewOut        string
	Reason        string
	RequestedBy   int64
	RequestedName string
	Status        string
	ApprovedBy    int64
	ApprovedName  string
	DecideNote    string
	CreatedAt     string
	DecidedAt     string
}

// CorrectionInput 은 정정 요청·직접 정정 입력이다(시각은 "HH:MM" KST, 바꿀 칸만 채운다).
type CorrectionInput struct {
	UserID   int64 // 직접 정정(인사 담당)에서만 쓴다. 0 이면 본인
	WorkDate string
	CheckIn  string
	CheckOut string
	Reason   string
}

// AttendanceClose 는 근태 마감 한 달이다.
type AttendanceClose struct {
	Month        string
	Closed       bool
	ClosedBy     string
	ClosedAt     string
	ReopenedBy   string
	ReopenedAt   string
	ReopenReason string
}

// AttendanceService 는 출퇴근·정정·근태 마감·근무 유형 서비스다.
type AttendanceService interface {
	Today(ctx context.Context) (AttendanceToday, error)
	WeekOf(ctx context.Context, userID int64, day string) (AttendanceWeek, error) // userID 0 은 본인. 범위 밖은 ErrNotFound
	CheckIn(ctx context.Context) (AttendanceToday, error)
	CheckOut(ctx context.Context) (AttendanceToday, error)
	RequestCorrection(ctx context.Context, in CorrectionInput) (Correction, error)
	DirectCorrect(ctx context.Context, in CorrectionInput) (Correction, error)
	DecideCorrection(ctx context.Context, id int64, approve bool, note string) (Correction, error)
	Corrections(ctx context.Context) (mine, toDecide []Correction, err error)
	Closes(ctx context.Context) ([]AttendanceClose, error)
	CloseMonth(ctx context.Context, month string) error
	ReopenMonth(ctx context.Context, month, reason string) error
	WorkTypes(ctx context.Context) ([]WorkType, error)
	SaveWorkType(ctx context.Context, id int64, in WorkTypeInput) (WorkType, error)
	Assign(ctx context.Context, userID, workTypeID int64, validFrom string) error
	Assignments(ctx context.Context, userID int64) ([]WorkAssignment, error)
	// 6단계: 부서 달력·팀 근태 현황·월 근태 내보내기(열람 범위는 서비스의 attendanceViewScope 한 곳)
	Calendar(ctx context.Context, month string, unitID int64) (AttCalendarView, error)
	TeamStatus(ctx context.Context, unitID int64) (AttTeamStatus, error)
	// 7단계: MCP 팀 부재 조회. 기간(양 끝 포함, 최대 31일)·열람 범위는 달력과 같다. 모든 일정이 "휴가"/"부재" 한 가지 이름이고 사유는 없다
	Absence(ctx context.Context, from, to string, unitID int64) (AttAbsenceView, error)
	MonthReport(ctx context.Context, month string) (AttMonthReport, error)
	MonthScopeCount(ctx context.Context) (int, error)
}
