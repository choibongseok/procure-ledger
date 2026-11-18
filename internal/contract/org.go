package contract

import (
	"context"
	"errors"
	"strings"
	"time"
)

// 조직 조회 계약(B20). 구현은 internal/org, 접근은 deps.Org.
// 12절. 읽기 전용이며 쓰기(조직 변경·발령)는 계약에 없다.
// 모든 조회는 기준일(asOf)을 받고 비어 있으면 오늘(KST)이다. erp(결재선·열람 범위)와 ai(MCP 도구)가 쓴다.

// Day 는 한국 표준시(KST) 기준 날짜("2006-01-02")다. 효력일·기준일에 쓴다. 빈 값은 "오늘"이다.
type Day string

// KST 는 조직 날짜 계산의 기준 시간대다.
var KST = time.FixedZone("KST", 9*60*60)

// DayOf 는 시각이 KST 로 어느 날짜인지다. UTC 15:00 에 날짜가 바뀐다.
func DayOf(t time.Time) Day { return Day(t.In(KST).Format("2006-01-02")) }

// OrgKind 는 조직 단위의 종류다. 직책 이름(대표·본부장·부서장·팀장)이 여기서 정해진다.
type OrgKind string

const (
	OrgCompany    OrgKind = "company"
	OrgDivision   OrgKind = "division"
	OrgDepartment OrgKind = "department"
	OrgTeam       OrgKind = "team"
)

// OrgKinds 는 알려진 조직 종류 전체다.
var OrgKinds = []OrgKind{OrgCompany, OrgDivision, OrgDepartment, OrgTeam}

// IsKnown 은 알려진 조직 종류인지 본다.
func (k OrgKind) IsKnown() bool {
	for _, x := range OrgKinds {
		if x == k {
			return true
		}
	}
	return false
}

// Level 은 조직 종류의 높이다(팀 1, 부서 2, 본부 3, 회사 4). 알 수 없는 종류는 0 이다. 결재선이 "위로 올라가며" 비교할 때 쓴다.
func (k OrgKind) Level() int {
	switch k {
	case OrgTeam:
		return 1
	case OrgDepartment:
		return 2
	case OrgDivision:
		return 3
	case OrgCompany:
		return 4
	}
	return 0
}

// HeadTitle 은 조직 종류별 조직장의 직책 이름이다. 화면과 상신 시점 스냅샷이 같은 말을 쓰게 한다.
func HeadTitle(kind OrgKind) string {
	switch kind {
	case OrgCompany:
		return "대표"
	case OrgDivision:
		return "본부장"
	case OrgDepartment:
		return "부서장"
	case OrgTeam:
		return "팀장"
	}
	return ""
}

// OrgUnit 은 기준일의 조직 단위다.
type OrgUnit struct {
	ID        int64
	Code      string
	Name      string
	Kind      OrgKind
	ParentID  int64 // 0 이면 루트(회사)
	SortOrder int
	Closed    bool // 폐지 상태
}

// OrgPathText 는 회사부터 이어진 경로를 "구매·물류본부 > 구매팀 > 구매1팀" 로 만든다.
// 루트(회사)는 맨 앞에 있어도 생략한다(상신 시점 스냅샷·화면 공용).
func OrgPathText(units []OrgUnit) string {
	names := make([]string, 0, len(units))
	for _, u := range units {
		if u.Kind == OrgCompany {
			continue
		}
		names = append(names, u.Name)
	}
	if len(names) == 0 && len(units) > 0 {
		return units[len(units)-1].Name // 회사 조직 자체(대표)는 회사 이름을 보인다
	}
	return strings.Join(names, " > ")
}

// OrgHead 는 기준일의 조직장이다. 조직장이 있으면 Acting=false, 직무대행이면 Acting=true(UserID 는 대행자),
// 공석이고 대행도 없으면 Vacant=true(UserID 0)다.
type OrgHead struct {
	UserID int64
	Acting bool
	Vacant bool
}

// OrgPlace 는 사람이 속한 조직 하나다.
type OrgPlace struct {
	Unit   OrgUnit
	Path   []OrgUnit // 회사부터 이 조직까지
	IsHead bool      // 이 조직의 조직장(직무대행 제외)
	Acting bool      // 이 조직의 직무대행자
}

// OrgPerson 은 기준일의 사람 소속 정보다.
type OrgPerson struct {
	UserID int64
	Active bool // 기준일에 재직 중인가(퇴사 상태면 false)
	// OnLeave 는 기준일에 휴직 중인가(B50 8절). 휴직은 퇴사가 아니라 Active 는 그대로 true 이고 로그인도 된다.
	// 결재 자격(eligibility.usable)·결재선 자동 구성이 이 값으로 휴직 중인 사람을 뺀다. AbsenceKind(질병·육아 등)는 민감 정보라 화면·MCP 로 내보내지 않는다.
	OnLeave     bool
	AbsenceKind string
	EmployeeNo  string
	Rank        string // 직위 이름
	Primary     OrgPlace
	Concurrent  []OrgPlace // 겸직(조직 순서 고정)
	Acting      []OrgPlace // 직무대행 중인 조직
}

// PeopleQuery 는 사람 검색 조건이다. Text 는 이름·조직명·직책에 부분 일치한다.
type PeopleQuery struct {
	Text        string
	UnitID      int64 // 0 이면 전체. 값이 있으면 그 조직과 하위 조직
	HeadOnly    bool
	IncludeLeft bool // 퇴사자 포함(org:history 권한이 없는 호출자에게는 무시된다)
	AsOf        Day
	Limit       int // 기본 50, 상한 OrgSearchMax. MCP 도구는 20 으로 줄여 쓴다
}

// 검색 개수 한도.
const (
	OrgSearchDefault = 50
	OrgSearchMax     = 200
	OrgFindMax       = 50
)

// PersonCard 는 검색·목록의 한 줄이다. 연락처(Email, Phone)는 호출자 기준으로 이미 가려져 있다.
// Principal.Channel 이 mcp 이면 권한자·본인에게도 가려진다(MaskContact).
type PersonCard struct {
	OrgPerson
	Name      string
	Email     string
	Phone     string
	Extension string // 내선번호는 가리지 않는다
}

// ErrNotInOrg 는 사용자는 있지만 조직에 등록되지 않았다는 뜻이다(ErrNotFound 와 구분한다.
// 도구는 "조직 미등록 사용자" 안내로 바꾼다). 사용자 번호 자체가 없으면 ErrNotFound 다.
var ErrNotInOrg = errors.New("조직에 등록되지 않은 사용자입니다")

// OrgDirectory 는 기준일의 조직 조회다. 쓰기는 없다.
type OrgDirectory interface {
	// 조직
	Unit(ctx context.Context, id int64, asOf Day) (OrgUnit, error)                            // 그날 없던 조직은 ErrNotFound
	Children(ctx context.Context, id int64, asOf Day) ([]OrgUnit, error)                      // 정렬 순서
	Path(ctx context.Context, id int64, asOf Day) ([]OrgUnit, error)                          // 루트 → 조직
	Ancestors(ctx context.Context, id int64, asOf Day) ([]OrgUnit, error)                     // 바로 위 → 루트(상위 순회)
	Descendants(ctx context.Context, id int64, asOf Day, includeSelf bool) ([]OrgUnit, error) // 하위 전체
	FindUnits(ctx context.Context, text string, asOf Day, limit int) ([]OrgUnit, error)       // 이름·코드 부분 일치(번호를 모르는 호출자용), 기본 20·상한 OrgFindMax

	// 조직장
	Head(ctx context.Context, unitID int64, asOf Day) (OrgHead, error)               // 조직장 → 직무대행 → 공석
	Heads(ctx context.Context, unitIDs []int64, asOf Day) (map[int64]OrgHead, error) // 여러 조직의 조직장을 한 번에(목록 화면의 N+1 방지). 없는 조직은 결과에서 빠진다
	UnitsHeadedBy(ctx context.Context, userID int64, asOf Day) ([]OrgUnit, error)    // 겸직 조직장 포함, 직무대행 제외
	HeadedScope(ctx context.Context, userID int64, asOf Day) ([]int64, error)        // 조직장인 조직과 그 하위 조직 번호(열람 범위용, 직무대행 포함)

	// 사람
	Person(ctx context.Context, userID int64, asOf Day) (OrgPerson, error)                        // 소속·겸직·대행. 조직에 등록되지 않았으면 ErrNotInOrg
	Persons(ctx context.Context, userIDs []int64, asOf Day) (map[int64]OrgPerson, error)          // 여러 사람의 소속을 한 번에. 조직에 없거나 그 날 소속이 없는 사람은 결과에서 빠진다(오류 아님)
	MembersOf(ctx context.Context, unitID int64, asOf Day, withDescendants bool) ([]int64, error) // 재직자 번호(주소속·겸직 합집합, 중복 없음)
	CountMembers(ctx context.Context, unitID int64, asOf Day, withDescendants bool) (int, error)  // 인원 수만
	SearchPeople(ctx context.Context, p Principal, q PeopleQuery) ([]PersonCard, error)           // 호출자 기준 마스킹
	Card(ctx context.Context, p Principal, userID int64, asOf Day) (PersonCard, error)            // 한 명의 카드(마스킹 포함)
}

// RawFieldAllowed 는 p 에게 마스킹 대상 필드(계좌·사업자번호·연락처)의 원문을 보일 수 있는지다(B41 D6).
// 원문은 mask:bypass 권한이 있고 MCP·API 채널이 아닐 때만이다: MCP 결과는 외부 AI 로, API 응답은 외부 연동 시스템으로 나가므로 권한자(토큰 발급자)에게도 어떤 필드도 원문을 내지 않는다.
func RawFieldAllowed(p Principal) bool {
	return p.Channel != ChannelMCP && p.Channel != ChannelAPI && p.Can(PermMaskBypass)
}

// ContactRawAllowed 는 p 에게 ownerID 사람의 이메일·휴대전화 원문을 보일 수 있는지다.
// MCP 채널은 결과가 외부 AI 로 나가므로 권한자·본인에게도 허용하지 않는다.
func ContactRawAllowed(p Principal, ownerID int64) bool {
	if p.Channel == ChannelMCP || p.Channel == ChannelAPI {
		return false
	}
	if p.UserID != 0 && p.UserID == ownerID {
		return true
	}
	return RawFieldAllowed(p)
}

// MaskContact 는 연락처 한 값을 호출자 기준으로 가린다(원문은 ContactRawAllowed 일 때만).
func MaskContact(m Masker, p Principal, ownerID int64, kind FieldKind, value string) string {
	if value == "" || ContactRawAllowed(p, ownerID) {
		return value
	}
	// 마스커는 mask:bypass 권한이 있으면 원문을 돌려주므로, 권한 없는 호출자로 바꿔서 부른다.
	return m.MaskField(Principal{UserID: p.UserID, Role: RoleStaff, Channel: p.Channel}, kind, value)
}
