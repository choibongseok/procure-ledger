package org

import (
	"fmt"
	"strings"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
)

// 이벤트 종류. 조직 변경은 unit, 인사 발령은 person 대상이다(직무대행은 조직 대상).
const (
	KindUnitCreate    = "unit_create"
	KindUnitRename    = "unit_rename"
	KindUnitMove      = "unit_move"
	KindUnitSort      = "unit_sort"
	KindUnitClose     = "unit_close"
	KindUnitReopen    = "unit_reopen"
	KindHeadSet       = "head_set"
	KindHeadClear     = "head_clear"
	KindActingSet     = "acting_set"
	KindHire          = "hire"
	KindTransfer      = "transfer"
	KindRankChange    = "rank_change"
	KindConcurrentAdd = "concurrent_add"
	KindConcurrentEnd = "concurrent_end"
	KindLeave         = "leave"
	KindAbsenceStart  = "absence_start" // 휴직(B50 8절): 재직은 그대로이고 결재 자격만 멈춘다
	KindAbsenceEnd    = "absence_end"   // 복직
	KindCancel        = "cancel"
)

// 대상 종류.
const (
	SubjectUnit   = "unit"
	SubjectPerson = "person"
)

var kindLabels = map[string]string{
	KindUnitCreate: "조직 신설", KindUnitRename: "조직 이름 변경", KindUnitMove: "조직 이동", KindUnitSort: "조직 순서 변경",
	KindUnitClose: "조직 폐지", KindUnitReopen: "조직 재개", KindHeadSet: "조직장 보임", KindHeadClear: "조직장 해임",
	KindActingSet: "직무대행", KindHire: "입사", KindTransfer: "전보", KindRankChange: "직위 변경",
	KindConcurrentAdd: "겸직 발령", KindConcurrentEnd: "겸직 해제", KindLeave: "퇴사", KindAbsenceStart: "휴직", KindAbsenceEnd: "복직", KindCancel: "취소",
}

// KindLabel 은 이벤트 종류의 한국어 이름이다.
func KindLabel(kind string) string {
	if l, ok := kindLabels[kind]; ok {
		return l
	}
	return "기타"
}

// Payload 는 이벤트의 종류별 내용이다(JSON). 쓰지 않는 칸은 비어 있다.
type Payload struct {
	Code     string           `json:"code,omitempty"`      // unit_create
	Name     string           `json:"name,omitempty"`      // unit_create, unit_rename
	UnitKind contract.OrgKind `json:"unit_kind,omitempty"` // unit_create
	Parent   int64            `json:"parent,omitempty"`    // unit_create, unit_move
	Sort     int              `json:"sort,omitempty"`      // unit_create, unit_sort
	Unit     int64            `json:"unit,omitempty"`      // hire, transfer, concurrent_add, concurrent_end
	Rank     int64            `json:"rank,omitempty"`      // hire, rank_change
	User     int64            `json:"user,omitempty"`      // head_set, acting_set
	End      contract.Day     `json:"end,omitempty"`       // acting_set: 종료일(포함)
	LastDay  contract.Day     `json:"last_day,omitempty"`  // leave: 마지막 근무일(효력일은 그 다음 날)
	Leave    string           `json:"leave_kind,omitempty"`
	// 휴직(absence_start): 구분·예정 복직일·사유. 사유는 민감 정보라 이력 목록·감사에 싣지 않고(길이만), 전용 조회로만 읽는다(B41 범주 attendance).
	Absence       string       `json:"absence_kind,omitempty"`
	ReturnDay     contract.Day `json:"return_day,omitempty"`
	AbsenceReason string       `json:"absence_reason,omitempty"`
	// CSV 일괄 반영(B22)으로 만든 이벤트의 출처: 반영 번호와 "파일 종류 N행". 일반 발령에는 없다.
	ImportBatch int64  `json:"import_batch,omitempty"`
	Source      string `json:"source,omitempty"`
}

// Event 는 이벤트 로그 한 줄이다.
type Event struct {
	ID        int64
	Subject   string // SubjectUnit | SubjectPerson
	SubjectID int64
	Kind      string
	Effective contract.Day
	P         Payload
	Reason    string
	Cancels   int64 // 취소하는 이벤트 번호
	Corrects  int64 // 정정하는 이벤트 번호
	CreatedBy int64
}

// Describe 는 오류 문구에 쓰는 한 줄 설명이다("2026. 12. 1. 전보").
func (e Event) Describe() string {
	return fmt.Sprintf("%s %s", DayText(e.Effective), KindLabel(e.Kind))
}

// DayText 는 날짜를 "2026. 3. 1." 로 쓴다(화면과 같은 한국어 형식).
func DayText(d contract.Day) string {
	t, err := time.Parse("2006-01-02", string(d))
	if err != nil {
		return string(d)
	}
	return fmt.Sprintf("%d. %d. %d.", t.Year(), int(t.Month()), t.Day())
}

// ParseDay 는 "2006-01-02" 만 받는다(달력에 없는 날짜는 거부).
func ParseDay(s string) (contract.Day, error) {
	t, err := time.Parse("2006-01-02", strings.TrimSpace(s))
	if err != nil {
		return "", fmt.Errorf("날짜는 연-월-일(예 2026-03-01)로 입력하세요")
	}
	return contract.Day(t.Format("2006-01-02")), nil
}

// AddDays 는 날짜에 일수를 더한다(음수 가능).
func AddDays(d contract.Day, n int) contract.Day {
	t, err := time.Parse("2006-01-02", string(d))
	if err != nil {
		return d
	}
	return contract.Day(t.AddDate(0, 0, n).Format("2006-01-02"))
}

// DaysBetween 은 b - a 를 일수로 돌려준다.
func DaysBetween(a, b contract.Day) int {
	ta, err1 := time.Parse("2006-01-02", string(a))
	tb, err2 := time.Parse("2006-01-02", string(b))
	if err1 != nil || err2 != nil {
		return 0
	}
	return int(tb.Sub(ta).Hours() / 24)
}
