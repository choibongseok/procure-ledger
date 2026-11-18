package importx

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/export"
)

// TemplateVersion 은 양식 버전이다. 열 이름·순서·필수·의미를 바꾸면 올린다(오래된 양식은 "최신 양식을 내려받으세요"로 거절된다).
const TemplateVersion = 1

// ColType 은 열의 값 종류다.
type ColType int

const (
	TText   ColType = iota // 문자열(길이 상한, 줄바꿈·제어문자 거부)
	TCode                  // 정해진 문자만 쓰는 코드(Pattern)
	TDigits                // 자리 수가 정해진 숫자 문자열(하이픈 제거, 선행 0 보존)
	TInt                   // 정수(쉼표·"원" 허용)
	TDate                  // YYYY-MM-DD
	TMonth                 // YYYY-MM
	TBool                  // 예·아니오
	TEnum                  // 허용 값(Enum: 입력 별칭 → 정해진 값)
)

// Column 은 양식의 열 하나다. 양식 생성과 파서 검증이 같은 정의를 쓴다(명세 AC-1502).
type Column struct {
	ID       string // ImportRow.Cells 의 키
	Label    string // 머리글 글자(필수 표시 `*` 는 양식을 만들 때 붙는다)
	Required bool
	Type     ColType
	Max      int // 글자 수 상한(TText·TCode)
	Digits   int // TDigits 자리 수
	IntMin   int64
	IntMax   int64
	Pattern  *regexp.Regexp // TCode
	PatMsg   string         // TCode 형식이 틀렸을 때 문구
	Enum     map[string]string
	WarnNum  bool // 숫자 셀로 들어오면 선행 0 소실 경고(코드류)
	Help     string
	Example  string
}

// Target 은 가져오기 대상 하나의 양식 정의다.
type Target struct {
	Code    string
	Label   string
	MaxRows int
	Columns []Column
	// Cross 는 행을 정규화한 뒤 행 사이 규칙(같은 파일 안 중복·일관성)과 키를 만든다. 행 값 검증은 이미 끝난 상태다.
	Cross func(rows []domain.ImportRow, opts domain.ImportParseOptions) (keys []string, problems map[int]string)
}

const maxMoney = int64(1_000_000_000_000)

var (
	reSKU  = regexp.MustCompile(`^[A-Za-z0-9_-]{1,40}$`)
	reWh   = regexp.MustCompile(`^[A-Za-z0-9-]{2,20}$`)
	reLot  = regexp.MustCompile(`^[A-Za-z0-9_./-]{1,40}$`)
	reTel  = regexp.MustCompile(`^[0-9+\-() ]{5,30}$`)
	reMail = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)
	reAcct = regexp.MustCompile(`^[0-9-]{6,30}$`)
)

var yesNo = map[string]string{"예": "true", "아니오": "false", "y": "true", "n": "false", "yes": "true", "no": "false", "true": "true", "false": "false", "1": "true", "0": "false"}

var accountKinds = map[string]string{"자산": "asset", "부채": "liability", "자본": "equity", "수익": "revenue", "비용": "expense"}

// kindOfCodeDigit 은 계정 코드의 앞 숫자가 가리키는 구분이다(1 자산·2 부채·3 자본·4 수익·5 비용).
var kindOfCodeDigit = map[byte]string{'1': "asset", '2': "liability", '3': "equity", '4': "revenue", '5': "expense"}

func intCol(key, label string, required bool, min, max int64, help, example string) Column {
	return Column{ID: key, Label: label, Required: required, Type: TInt, IntMin: min, IntMax: max, Help: help, Example: example}
}

func textCol(key, label string, required bool, max int, help, example string) Column {
	return Column{ID: key, Label: label, Required: required, Type: TText, Max: max, Help: help, Example: example}
}

func vendorRef(v string) string {
	d := strings.ReplaceAll(v, "-", "")
	if len(d) == 10 && allDigits(d) {
		return "biz:" + d
	}
	return "name:" + v
}

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// dupCross 는 키 목록을 만들고 파일 안 같은 키를 문제로 돌려준다(뒤 행이 문제).
func dupCross(keyOf func(c map[string]string) string, extra func(rows []domain.ImportRow, i int) string) func([]domain.ImportRow, domain.ImportParseOptions) ([]string, map[int]string) {
	return func(rows []domain.ImportRow, _ domain.ImportParseOptions) ([]string, map[int]string) {
		keys := make([]string, len(rows))
		problems := map[int]string{}
		first := map[string]int{}
		for i, r := range rows {
			k := keyOf(r.Cells)
			keys[i] = k
			if at, dup := first[k]; dup {
				problems[i] = fmt.Sprintf("파일 안에 같은 키가 두 번 있습니다(%d행과 같음).", rows[at].RowNo)
				continue
			}
			first[k] = i
			if extra != nil {
				if msg := extra(rows, i); msg != "" {
					problems[i] = msg
				}
			}
		}
		return keys, problems
	}
}

// Targets 는 7개 대상의 양식 정의다(명세 3.3). 이 값이 양식 생성과 파서 검증의 유일한 정의다.
var Targets = map[string]Target{
	domain.ImportAccount: {
		Code: domain.ImportAccount, Label: "계정과목", MaxRows: 5000,
		Columns: []Column{
			{ID: "code", Label: "코드", Required: true, Type: TDigits, Digits: 4, Help: "4자리 숫자. 앞 숫자가 구분(1 자산·2 부채·3 자본·4 수익·5 비용)", Example: "5310"},
			textCol("name", "이름", true, 40, "40자 이내", "광고선전비"),
			{ID: "kind", Label: "구분", Required: true, Type: TEnum, Enum: accountKinds, Help: "자산·부채·자본·수익·비용 중 하나(코드의 앞 숫자와 같아야 함)", Example: "비용"},
			{ID: "active", Label: "사용 여부", Type: TEnum, Enum: map[string]string{"사용": "active", "중지": "inactive"}, Help: "사용 또는 중지(비우면 사용)", Example: "사용"},
		},
		Cross: dupCross(func(c map[string]string) string { return c["code"] }, nil),
	},
	domain.ImportWarehouse: {
		Code: domain.ImportWarehouse, Label: "창고", MaxRows: 5000,
		Columns: []Column{
			{ID: "code", Label: "코드", Required: true, Type: TCode, Max: 20, Pattern: reWh, PatMsg: "영문·숫자·하이픈 2~20자여야 합니다.", WarnNum: true, Help: "영문·숫자·하이픈 2~20자(대소문자 구분 없이 같으면 같은 창고)", Example: "WH-02"},
			textCol("name", "이름", true, 40, "40자 이내, 다른 창고와 겹치지 않게", "물류센터"),
			textCol("location", "위치", false, 100, "100자 이내", "경기도 이천시 ○○로 1"),
		},
		Cross: dupCross(func(c map[string]string) string { return strings.ToLower(c["code"]) }, nil),
	},
	domain.ImportVendor: {
		Code: domain.ImportVendor, Label: "거래처", MaxRows: 5000,
		Columns: []Column{
			textCol("name", "이름", true, 60, "60자 이내", "한빛상사"),
			{ID: "business_no", Label: "사업자번호", Type: TDigits, Digits: 10, Help: "10자리(하이픈 허용). 같으면 같은 거래처로 봅니다", Example: "123-45-67890"},
			intCol("payment_days", "결제 조건(일)", false, 0, 365, "0~365 정수. 비우면 기본 30일", "30"),
			{ID: "phone", Label: "전화", Type: TCode, Max: 30, Pattern: reTel, PatMsg: "전화번호 형식이 올바르지 않습니다.", Help: "숫자·하이픈·괄호", Example: "02-123-4567"},
			{ID: "email", Label: "이메일", Type: TCode, Max: 80, Pattern: reMail, PatMsg: "이메일 형식이 올바르지 않습니다.", Help: "name@example.com", Example: "order@hanbit.example"},
			{ID: "account_no", Label: "계좌번호", Type: TCode, Max: 30, Pattern: reAcct, PatMsg: "계좌번호는 숫자와 하이픈 6~30자여야 합니다.", Help: "숫자·하이픈 6~30자. 저장 때 구분 필드로 보관되고 화면에는 끝 4자리만 보입니다", Example: "110-123-456789"},
		},
		Cross: dupCross(func(c map[string]string) string {
			if b := c["business_no"]; b != "" {
				return "biz:" + b
			}
			return "name:" + c["name"]
		}, nil),
	},
	domain.ImportItem: {
		Code: domain.ImportItem, Label: "품목", MaxRows: 5000,
		Columns: []Column{
			{ID: "sku", Label: "SKU", Required: true, Type: TCode, Max: 40, Pattern: reSKU, PatMsg: "영문·숫자·밑줄·하이픈 1~40자여야 합니다.", WarnNum: true, Help: "영문·숫자·_·- 1~40자(대소문자 구분 없이 같으면 같은 품목)", Example: "PAPER-A4"},
			textCol("name", "품목명", true, 80, "80자 이내", "복사용지 A4"),
			textCol("unit", "단위", true, 10, "10자 이내(EA·박스 등)", "박스"),
			intCol("safety_stock", "안전재고", false, 0, 1_000_000_000, "0 이상 정수", "100"),
			{ID: "lot_tracked", Label: "로트 관리", Type: TBool, Enum: yesNo, Help: "예 또는 아니오(비우면 아니오)", Example: "아니오"},
		},
		Cross: dupCross(func(c map[string]string) string { return strings.ToUpper(c["sku"]) }, nil),
	},
	domain.ImportBudget: {
		Code: domain.ImportBudget, Label: "부서 예산", MaxRows: 5000,
		Columns: []Column{
			textCol("dept", "부서", true, 40, "등록된 부서 이름과 같아야 합니다", "구매팀"),
			{ID: "month", Label: "적용 월", Required: true, Type: TMonth, Help: "YYYY-MM(이번 달 이후만)", Example: "2026-12"},
			intCol("amount", "월 예산(원)", true, 0, maxMoney, "0 이상 정수(쉼표 허용)", "30000000"),
			textCol("reason", "사유", false, 200, "비우면 \"기초 데이터 가져오기\"", ""),
		},
		Cross: dupCross(func(c map[string]string) string { return c["dept"] + "|" + c["month"] }, nil),
	},
	domain.ImportOpeningStock: {
		Code: domain.ImportOpeningStock, Label: "기초 재고", MaxRows: 10000,
		Columns: []Column{
			{ID: "sku", Label: "SKU", Required: true, Type: TCode, Max: 40, Pattern: reSKU, PatMsg: "영문·숫자·밑줄·하이픈 1~40자여야 합니다.", WarnNum: true, Help: "등록된 품목의 SKU", Example: "PAPER-A4"},
			{ID: "warehouse", Label: "창고 코드", Required: true, Type: TCode, Max: 20, Pattern: reWh, PatMsg: "영문·숫자·하이픈 2~20자여야 합니다.", WarnNum: true, Help: "등록된 창고 코드", Example: "WH-01"},
			{ID: "lot", Label: "로트 번호", Type: TCode, Max: 40, Pattern: reLot, PatMsg: "로트 번호는 영문·숫자·_ . / - 1~40자여야 합니다.", WarnNum: true, Help: "로트 관리 품목이면 필수, 아니면 비워 둡니다", Example: ""},
			{ID: "expiry", Label: "유효기한", Type: TDate, Help: "YYYY-MM-DD(로트 관리 품목, 선택)", Example: ""},
			intCol("qty", "수량", true, 1, maxMoney, "1 이상 정수", "100"),
			intCol("unit_cost", "단가(원)", true, 0, maxMoney, "0 이상 정수. 같은 SKU 는 모든 행의 단가가 같아야 합니다", "22000"),
		},
		Cross: func(rows []domain.ImportRow, opts domain.ImportParseOptions) ([]string, map[int]string) {
			keys, problems := dupCross(func(c map[string]string) string {
				return strings.ToUpper(c["sku"]) + "|" + strings.ToLower(c["warehouse"]) + "|" + c["lot"]
			}, nil)(rows, opts)
			cost := map[string]int{} // SKU → 처음 단가를 쓴 행 위치
			for i, r := range rows {
				if _, bad := problems[i]; bad {
					continue
				}
				sku := strings.ToUpper(r.Cells["sku"])
				if at, seen := cost[sku]; seen {
					if rows[at].Cells["unit_cost"] != r.Cells["unit_cost"] {
						problems[i] = fmt.Sprintf("같은 품목의 단가가 행마다 다릅니다(%d행과 다름). 품목당 단가는 하나입니다.", rows[at].RowNo)
					}
					continue
				}
				cost[sku] = i
			}
			return keys, problems
		},
	},
	domain.ImportOpeningPayable: {
		Code: domain.ImportOpeningPayable, Label: "미지급 잔액", MaxRows: 5000,
		Columns: []Column{
			textCol("vendor", "거래처", true, 60, "등록된 거래처의 사업자번호 또는 이름", "한빛상사"),
			textCol("doc_no", "문서 번호", true, 40, "원 시스템의 전표·세금계산서 번호(같은 거래처 안에서 유일)", "TI-2026-0101"),
			{ID: "trade_date", Label: "발생일", Required: true, Type: TDate, Help: "YYYY-MM-DD, 기준일 이하", Example: "2026-10-21"},
			{ID: "due_date", Label: "만기일", Type: TDate, Help: "YYYY-MM-DD, 발생일 이후(비우면 발생일 + 결제 조건)", Example: "2026-11-20"},
			intCol("amount", "잔액(원)", true, 1, maxMoney, "1 이상 정수. 일부 지급했다면 남은 금액만", "3500000"),
		},
		Cross: func(rows []domain.ImportRow, opts domain.ImportParseOptions) ([]string, map[int]string) {
			keys, problems := dupCross(func(c map[string]string) string { return vendorRef(c["vendor"]) + "|" + c["doc_no"] }, nil)(rows, opts)
			for i, r := range rows {
				if _, bad := problems[i]; bad {
					continue
				}
				td, due := r.Cells["trade_date"], r.Cells["due_date"]
				switch {
				case due != "" && due < td:
					problems[i] = "만기일은 발생일 이후여야 합니다."
				case opts.BaseDate != "" && td > opts.BaseDate:
					problems[i] = "발생일은 기준일 이하여야 합니다."
				}
			}
			return keys, problems
		},
	},
}

// Header 는 양식 머리글 글자다(필수 열에는 `*` 가 붙는다).
func (c Column) Header() string {
	if c.Required {
		return c.Label + "*"
	}
	return c.Label
}

// guide 는 양식 정보 시트의 작성 안내 한 줄이다.
func (t Target) guide() string {
	parts := make([]string, 0, len(t.Columns))
	for _, c := range t.Columns {
		parts = append(parts, c.Header()+": "+c.Help)
	}
	return strings.Join(parts, " / ")
}

// Templates 는 domain.ImportTemplates 를 구현한다(B28 export.WriteXLSX 재사용). 상태가 없다.
type Templates struct{}

var _ domain.ImportTemplates = Templates{}

// Template 은 대상의 양식 XLSX 를 만든다: 머리글 한 줄과 예시 한 줄, 둘째 시트(정보)에 양식 버전과 작성 안내가 들어간다.
// 모든 칸은 문자열 형식으로 만든다(SKU·사업자번호·계좌번호의 선행 0 이 사라지지 않게).
func (Templates) Template(target string) ([]byte, error) {
	t, ok := Targets[target]
	if !ok {
		return nil, verr("알 수 없는 가져오기 대상입니다.")
	}
	cols := make([]export.Column, len(t.Columns))
	example := make([]export.Cell, len(t.Columns))
	for i, c := range t.Columns {
		cols[i] = export.Column{Label: c.Header(), Kind: export.KindText, Sensitive: false}
		if c.Example != "" {
			example[i] = c.Example
		}
	}
	return export.WriteXLSX(export.Table{Dataset: export.Dataset{Code: t.Code, Label: t.Label, Columns: cols}, Rows: [][]export.Cell{example}},
		export.Meta{At: time.Now(), By: "서버", Filters: fmt.Sprintf("양식 버전 %d | %s", TemplateVersion, t.guide())})
}
