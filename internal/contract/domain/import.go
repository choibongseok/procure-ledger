package domain

// B35 기초 데이터 가져오기(). 이 파일은 ai 가 구현하는 양식 읽기·검증(internal/importx)과
// platform 의 공통 가져오기 부품(미리보기·반영), erp 의 대상별 반영이 맞물리는 도메인 타입·인터페이스다. 추가만 한다.
//
// 불변식: 파서는 순수 함수다(바이트 → 행, DB·시계·네트워크·LLM 없음). 오류 사유에는 원본 값(이름·계좌 조각·금액)을 넣지 않는다.

// 가져오기 대상 코드(명세 3.3 의 ①~⑦).
const (
	ImportAccount        = "account"         // ① 계정과목
	ImportWarehouse      = "warehouse"       // ② 창고
	ImportVendor         = "vendor"          // ③ 거래처
	ImportItem           = "item"            // ④ 품목
	ImportBudget         = "budget"          // ⑤ 부서 예산
	ImportOpeningStock   = "opening_stock"   // ⑥ 기초 재고
	ImportOpeningPayable = "opening_payable" // ⑦ 미지급 잔액
)

// ImportTargets 는 가져오기 순서(명세 3.4)대로의 대상 코드다.
var ImportTargets = []string{ImportAccount, ImportWarehouse, ImportVendor, ImportItem, ImportBudget, ImportOpeningStock, ImportOpeningPayable}

// ImportTargetLabels 는 화면에 보이는 대상 이름이다(영문 코드를 보이지 않는다).
var ImportTargetLabels = map[string]string{
	ImportAccount: "계정과목", ImportWarehouse: "창고", ImportVendor: "거래처", ImportItem: "품목",
	ImportBudget: "부서 예산", ImportOpeningStock: "기초 재고", ImportOpeningPayable: "미지급 잔액",
}

// ImportRow 는 검증을 통과한 정규화 행이다. Cells 는 열 키(importx 의 데이터셋 정의, 예 "sku"·"qty")별 정규화된 문자열이다:
// 정수는 십진 문자열, 날짜는 YYYY-MM-DD, 월은 YYYY-MM, 예·아니오는 "true"·"false", 열거는 정해진 영문 값, 문자열은 공백을 정리한 값이다.
// 비어 있는 선택 칸은 키가 없다. 호출한 쪽(대상 어댑터)은 값을 다시 검증하지 않고 strconv 로 읽으면 된다.
type ImportRow struct {
	RowNo int               // 파일의 실제 행 번호(머리글이 1, 데이터는 2부터)
	Key   string            // 같은 행인지 판정하는 정규화 키(대소문자·하이픈 정리). 대상마다 정의
	Cells map[string]string // 정규화된 값
}

// ImportRowIssue 는 행 하나의 오류 또는 경고다. Reason 은 정해진 한국어 문장이고 원본 값을 담지 않는다.
type ImportRowIssue struct {
	RowNo  int
	Column string // 문제의 열 이름(없으면 "")
	Reason string
}

// ImportParseOptions 는 파일 밖 입력이다. 파서는 시계를 쓰지 않으므로 기준일·오늘은 호출자가 넘긴다(비면 그 검사를 건너뛴다).
type ImportParseOptions struct {
	BaseDate string // 기초 재고·미지급 잔액의 기준일 YYYY-MM-DD(미지급 잔액의 발생일은 기준일 이하여야 한다)
}

// ImportParseResult 는 파일 하나의 읽기·정규화 결과다.
type ImportParseResult struct {
	Target          string
	TemplateVersion int // 파일 정보 시트의 양식 버전(없으면 0)
	TotalRows       int // 빈 줄을 뺀 데이터 행 수
	Rows            []ImportRow
	Errors          []ImportRowIssue // 오류 행(그 행은 Rows 에 없다)
	Warnings        []ImportRowIssue // 반영은 되지만 주의할 값
	FileWarnings    []string         // 파일 전체에 대한 안내(알 수 없는 추가 열 등)
}

// ImportFileParser 는 양식 XLSX 를 읽어 정규화한다(ai, internal/importx). 파일 수준 오류(형식·크기·머리글·양식 버전)는 error 이고
// contract.ErrValidation 을 감싼 한국어 문구다. 행 수준 오류는 결과의 Errors 에 모은다.
type ImportFileParser interface {
	Parse(target string, data []byte, opts ImportParseOptions) (ImportParseResult, error)
}

// ImportTemplates 는 대상별 양식 파일을 만든다(B28 export.WriteXLSX 재사용). 열 정의는 파서와 같은 한 곳이다.
type ImportTemplates interface {
	Template(target string) ([]byte, error)
}
