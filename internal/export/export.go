// Package export 는 목록·보고서를 CSV·XLSX 로 내려받는 공통 부품이다(B28, 3절).
// 외부 의존 없이 표준 라이브러리만 쓴다. 이 패키지는 서비스 계층 위(핸들러 쪽)에 놓이며 저장소·DB 를 직접 부르지 않는다:
// 행은 소유 영역이 만든 Source 가 서비스 함수를 불러(열람 범위·마스킹 적용) 돌려준다.
package export

import (
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

// Kind 는 열의 값 종류다. 서비스가 화면용으로 다듬은 값(열거 값의 한글 이름, 조직 경로 문자열 등)을 주고 내보내기는 형식만 입힌다.
type Kind int

const (
	KindText     Kind = iota // 문자열
	KindInt                  // 정수(수량 등)
	KindMoney                // 정수 원 단위
	KindPercent              // 백분율 숫자(85.2 는 85.2%)
	KindDate                 // 날짜(YYYY-MM-DD 문자열 또는 time.Time)
	KindDateTime             // 날짜·시각(KST, time.Time 또는 RFC3339 문자열)
	KindBool                 // 예 / 아니오
)

// Column 은 열 정의다. Label 은 화면 표의 글자 그대로이고 단위가 있으면 이름에 넣는다("금액(원)").
// Sensitive 가 참이면 이메일·휴대전화·사업자번호·계좌 같은 마스킹 대상 열이다(원문 포함 선택이 없으면 Source 가 가린 값을 준다).
type Column struct {
	Label     string
	Kind      Kind
	Sensitive bool
}

// Dataset 은 내보낼 수 있는 목록 한 종류다. Code 는 영문 소문자 코드, Label 은 한글 이름(파일 이름·시트 이름·감사)이다.
type Dataset struct {
	Code    string
	Label   string
	Columns []Column
	// Privacy 는 민감 열이 있는 데이터 세트가 원문 포함으로 내보내질 때 남길 개인정보 조회 기록의 범주·필드다(B41). 민감 열이 있는데 비어 있으면 원문 포함 내보내기가 거부된다.
	Privacy *PrivacySpec
}

// PrivacySpec 은 데이터 세트의 개인정보 조회 기록 명세다.
type PrivacySpec struct {
	Category string   // privacy.CatContact 등
	Fields   []string // privacy.FieldEmail 등
	// ServiceNotes 가 참이면 원문을 만드는 서비스 층(예: org.ListPeople)이 이미 응답 한 번에 한 번 기록하므로 내보내기 핸들러는 다시 기록하지 않는다
	// (한 응답 한 기록, 명세 41 5.2). 서비스가 기록하지 못하면 Rows 가 오류를 돌려줘 파일이 나가지 않는다.
	ServiceNotes bool
}

// HasSensitive 는 민감 열이 하나라도 있는지 알려 준다(있으면 "원문 포함" 선택이 의미가 있다).
func (d Dataset) HasSensitive() bool {
	for _, c := range d.Columns {
		if c.Sensitive {
			return true
		}
	}
	return false
}

// Cell 은 한 칸의 값이다. nil 은 빈 칸이다. 허용 형식은 Kind 별로 다음과 같다.
//
//	KindText: string / KindInt, KindMoney: int·int64 / KindPercent: float64·int·int64 / KindDate: string("2006-01-02")·time.Time
//	KindDateTime: time.Time·string(RFC3339) / KindBool: bool
type Cell = any

// Table 은 파일로 쓸 표다.
type Table struct {
	Dataset Dataset
	Rows    [][]Cell
}

// Meta 는 파일에 함께 적는 내보내기 정보다(XLSX 둘째 시트).
type Meta struct {
	At      time.Time // 내보낸 시각
	By      string    // 내보낸 사람 이름
	Filters string    // 적용한 조건 문장(FilterText)
	Raw     bool      // 원문 포함 여부
}

// KST 는 파일의 날짜·시각 기준 시간대다.
var KST = time.FixedZone("KST", 9*3600)

// FilterKind 는 조건 값의 종류다. 종류가 있는 조건(열거·날짜·번호 등)은 **허용 형식 안의 값만** 감사·정보 시트에 남고,
// 형식 밖의 값은 파일을 만들기 전에 400 으로 거부되며 감사에는 값 대신 "잘못된 값"만 남는다(요청의 임의 문자열이 감사에 새지 않게).
// 종류를 지정하지 않은 조건(FilterHidden)은 자유 입력과 같이 값을 남기지 않고 "입력함"만 적는다(안전한 기본값).
type FilterKind int

const (
	FilterHidden FilterKind = iota // 자유 입력·검증하지 않은 값: "입력함"
	FilterEnum                     // Enum 에 있는 원래 값만(감사에는 한국어 라벨)
	FilterDate                     // YYYY-MM-DD
	FilterMonth                    // YYYY-MM
	FilterTime                     // YYYY-MM-DD 또는 RFC3339
	FilterID                       // 양의 정수 번호
	FilterCode                     // 영문·숫자·_·- 코드(양식 코드 등, 30자 이하)
	FilterBool                     // 1·true·0·false
)

// FilterItem 은 적용한 조건 한 가지다. Value 는 요청의 **원래 값**이다. Free 가 참이거나 Kind 가 FilterHidden 이면
// 자유 입력(검색어·이름)이라 감사·파일에 값을 남기지 않고 "입력함"만 적는다. Enum 은 FilterEnum 의 허용 값(원래 값 → 한국어 라벨)이다.
type FilterItem struct {
	Name  string
	Value string
	Free  bool
	Kind  FilterKind
	Enum  map[string]string
}

// 조건 항목 만들기. 값은 요청의 원래 값을 그대로 넘긴다(라벨로 바꾸지 않는다). 검사와 표기는 export 가 한다.
func Enum(name, v string, labels map[string]string) FilterItem {
	return FilterItem{Name: name, Value: v, Kind: FilterEnum, Enum: labels}
}
func Date(name, v string) FilterItem  { return FilterItem{Name: name, Value: v, Kind: FilterDate} }
func Month(name, v string) FilterItem { return FilterItem{Name: name, Value: v, Kind: FilterMonth} }
func Time(name, v string) FilterItem  { return FilterItem{Name: name, Value: v, Kind: FilterTime} }
func ID(name, v string) FilterItem    { return FilterItem{Name: name, Value: v, Kind: FilterID} }
func Code(name, v string) FilterItem  { return FilterItem{Name: name, Value: v, Kind: FilterCode} }
func Bool(name, v string) FilterItem  { return FilterItem{Name: name, Value: v, Kind: FilterBool} }
func Free(name, v string) FilterItem  { return FilterItem{Name: name, Value: v, Free: true} }

const invalidFilterText = "잘못된 값"

var (
	idRe   = regexp.MustCompile(`^[1-9][0-9]{0,17}$`)
	codeRe = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,60}$`)
)

// valid 는 값이 종류의 허용 형식 안인지 본다. 빈 값은 조건이 없는 것이라 항상 유효하다.
func (it FilterItem) valid() bool {
	v := it.Value
	if v == "" || it.Free {
		return true
	}
	switch it.Kind {
	case FilterEnum:
		_, ok := it.Enum[v]
		return ok
	case FilterDate:
		_, err := time.Parse("2006-01-02", v)
		return err == nil
	case FilterMonth:
		_, err := time.Parse("2006-01", v)
		return err == nil
	case FilterTime:
		if _, err := time.Parse("2006-01-02", v); err == nil {
			return true
		}
		_, err := time.Parse(time.RFC3339, v)
		return err == nil
	case FilterID:
		return v == "0" || idRe.MatchString(v) // 0 은 화면 API 와 같이 "조건 없음"
	case FilterCode:
		return codeRe.MatchString(v)
	case FilterBool:
		return v == "1" || v == "true" || v == "0" || v == "false"
	}
	return true // FilterHidden: 값을 남기지 않으므로 형식은 따지지 않는다
}

// CheckFilters 는 허용 형식 밖의 조건 이름을 돌려준다(없으면 nil). 핸들러가 파일을 만들기 전에 400 으로 거부한다.
func CheckFilters(items []FilterItem) []string {
	var bad []string
	for _, it := range items {
		if !it.valid() {
			bad = append(bad, it.Name)
		}
	}
	return bad
}

// text 는 조건 한 가지를 감사·정보 시트에 남길 글자로 바꾼다. 허용 형식 안의 값만 그대로(열거는 라벨) 남는다.
func (it FilterItem) text() string {
	if it.Free || it.Kind == FilterHidden {
		return "입력함"
	}
	if !it.valid() {
		return invalidFilterText
	}
	switch it.Kind {
	case FilterEnum:
		return it.Enum[it.Value]
	case FilterBool:
		if it.Value == "1" || it.Value == "true" {
			return "예"
		}
		return "아니오"
	}
	return it.Value
}

// MaxFilterText 는 조건 문장의 최대 길이(글자)다.
const MaxFilterText = 500

// FilterText 는 조건 문장을 만든다: "기간=2026-10-01~2026-10-31; 상태=결재중; 검색어=입력함". 값이 빈 조건은 뺀다.
func FilterText(items []FilterItem) string {
	var parts []string
	for _, it := range items {
		if strings.TrimSpace(it.Value) == "" || (it.Kind == FilterID && it.Value == "0") {
			continue
		}
		parts = append(parts, it.Name+"="+cleanLine(it.text()))
	}
	s := strings.Join(parts, "; ")
	if utf8.RuneCountInString(s) > MaxFilterText {
		r := []rune(s)
		s = string(r[:MaxFilterText-1]) + "…"
	}
	return s
}

// cleanLine 은 한 줄 문장에서 줄바꿈·제어문자를 공백으로 바꾼다.
func cleanLine(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, s)
}

// FileName 은 "<데이터셋 한글 이름>_<연월일>_<시분>.<확장자>"(KST)다. 필터 값은 이름에 넣지 않는다.
func FileName(label string, at time.Time, ext string) string {
	return fmt.Sprintf("%s_%s.%s", label, at.In(KST).Format("20060102_1504"), ext)
}

// validate 는 표의 각 칸이 열 종류에 맞는지 본다. 맞지 않으면 파일을 만들기 전에 오류로 끝낸다(조용히 이상한 파일을 내지 않는다).
func (t Table) validate() error {
	for i, row := range t.Rows {
		if len(row) != len(t.Dataset.Columns) {
			return fmt.Errorf("export: %d행의 칸 수 %d 가 열 수 %d 와 다름", i+1, len(row), len(t.Dataset.Columns))
		}
		for j, c := range row {
			if err := checkCell(t.Dataset.Columns[j].Kind, c); err != nil {
				return fmt.Errorf("export: %d행 %q 열: %w", i+1, t.Dataset.Columns[j].Label, err)
			}
		}
	}
	return nil
}

func checkCell(k Kind, c Cell) error {
	if c == nil {
		return nil
	}
	switch k {
	case KindText:
		if _, ok := c.(string); ok {
			return nil
		}
	case KindInt, KindMoney:
		switch c.(type) {
		case int, int64:
			return nil
		}
	case KindPercent:
		switch c.(type) {
		case float64, int, int64:
			return nil
		}
	case KindDate:
		switch v := c.(type) {
		case time.Time:
			return nil
		case string:
			if v == "" {
				return nil
			}
			if _, err := time.Parse("2006-01-02", v); err == nil {
				return nil
			}
		}
	case KindDateTime:
		switch v := c.(type) {
		case time.Time:
			return nil
		case string:
			if v == "" {
				return nil
			}
			if _, err := time.Parse(time.RFC3339, v); err == nil {
				return nil
			}
		}
	case KindBool:
		if _, ok := c.(bool); ok {
			return nil
		}
	}
	return fmt.Errorf("값 %T 이(가) 열 종류에 맞지 않음", c)
}
