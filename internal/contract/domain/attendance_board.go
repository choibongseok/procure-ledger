package domain

// B50 6단계: 부서 달력·팀 근태 현황·월 근태 내보내기(명세 9절). 열람 범위는 서비스의 attendanceViewScope 한 곳이 정한다.
// 휴가·휴직 사유 원문은 이 화면들에 실리지 않는다(종류·일정만). 사유는 휴가 신청 상세(LeaveService.Get)가 열람 기록과 함께 보인다.

// AttCalendarDay 는 달력 한 날이다.
type AttCalendarDay struct {
	Date    string
	Weekend bool
	Holiday string // 등록된 공휴일 이름(없으면 "")
}

// AttCalendarEntry 는 한 사람의 부재 일정 한 건이다. 본인·직속 조직장·인사 담당에게는 종류가, 같은 팀 동료에게는 "휴가"/"부재" 한 가지가 보인다.
type AttCalendarEntry struct {
	Start string
	End   string
	Label string
	Part  string // 종일이 아니면 구간 이름(오전 반차 등). 동료에게는 ""
}

// AttCalendarPerson 은 달력의 한 사람이다.
type AttCalendarPerson struct {
	UserID  int64
	Name    string
	Unit    string
	Entries []AttCalendarEntry
}

// AttCalendarView 는 한 달의 부서 달력이다.
type AttCalendarView struct {
	Month  string
	Days   []AttCalendarDay
	People []AttCalendarPerson
	// Scope 는 어느 범위의 달력인지(조직 이름 또는 "전사")다.
	Scope string
}

// AttAbsencePerson 은 부재 일정 조회(MCP)의 한 사람이다. 일정에는 "휴가"/"부재" 구분만 있다(사유·휴가 종류·병가 구분 없음).
type AttAbsencePerson struct {
	Name    string
	Unit    string
	Entries []AttCalendarEntry
}

// AttAbsenceView 는 기간 안의 부재 일정이다. 열람 범위는 부서 달력과 같은 함수(attendanceViewScope)다.
type AttAbsenceView struct {
	From   string
	To     string
	Scope  string
	People []AttAbsencePerson // 기간 안에 일정이 있는 사람만
}

// AttTeamRow 는 팀 근태 현황의 한 사람(오늘)이다.
type AttTeamRow struct {
	UserID      int64
	Name        string
	Unit        string
	State       string // 출근·미출근·휴가·부재(휴직)·휴일
	CheckIn     string
	CheckOut    string
	Late        bool
	WeekMinutes int
	WeekLevel   string
	Missing     bool // 어제 퇴근 기록이 없다
}

// AttTeamStatus 는 조직장·인사 담당의 오늘 현황이다. 합계와 표는 같은 범위 규칙을 쓴다(범위 밖 사람은 어디에도 없다).
type AttTeamStatus struct {
	Date       string
	Scope      string
	Total      int
	Present    int
	NotYet     int
	OnLeave    int
	Absent     int // 휴직 중인 인원(구분 없이 "부재")
	Pending    int // 승인 대기 정정 요청 수(범위 안)
	WeekOver   int // 이번 주 52시간 초과 인원
	WeekCaut   int // 이번 주 48시간 이상 인원
	Rows       []AttTeamRow
	Truncated  bool
	CautionMin int
	OverMin    int
}

// AttMonthRow 는 월 근태 내보내기의 한 사람이다. 금액·수당은 없다. 사유·휴직 구분 원문도 없다.
type AttMonthRow struct {
	EmployeeNo     string
	Name           string
	Unit           string
	WorkType       string
	WorkDays       int // 소정 근무일 수
	AttendDays     int // 출근 일수
	Late           int
	EarlyLeave     int
	WorkMinutes    int
	AnnualDays     float64
	CompDays       float64
	SickDays       float64
	OtherLeaveDays float64
	LeaveOfAbsence int // 휴직 일수(휴직 기능 전에는 0)
	AbsentDays     int // 결근 일수
	OverWeeks      int // 주 52시간 초과 주 수
}

// AttMonthReport 는 한 달의 근태 집계다.
type AttMonthReport struct {
	Month  string
	Closed bool // 근태 마감 여부(미마감이면 "미확정")
	Rows   []AttMonthRow
}
