package domain

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
)

func key(parts ...any) string {
	s := make([]string, len(parts))
	for i, p := range parts {
		s[i] = fmt.Sprint(p)
	}
	return strings.Join(s, "|")
}

// B32 법인카드( 3.3). 이 파일은 ai 가 구현하는 카드 CSV 정규화(internal/cardimport)와
// MCP 도구 2개(get_payables·get_card_unreconciled)가 의존하는 도메인 타입·인터페이스다. 구현·서비스는 erp 가 같은 파일에 추가만 한다.
//
// 불변식: 카드번호는 끝 4자리만 다룬다(전체 번호를 받지도 저장하지도 않는다). LLM 은 쓰지 않는다. 금액은 정수 원이다.

// 카드사 형식 코드(D2). 정확한 카드사 이름·열 이름은 실제 샘플을 받은 뒤 확정하고, 그때까지 두 가지 성격을 받는 설정형 열 매핑으로 만든다.
const (
	CardFormatA = "format_a" // 승인일시 한 칸형: 승인일시·카드번호(끝 4자리 포함 마스킹)·가맹점명·승인금액·승인번호·할부개월·승인구분(승인/취소)
	CardFormatB = "format_b" // 일·시 분리·공급가 분리형: 이용일(YYYYMMDD)·이용시간(HHMMSS)·카드번호끝4·가맹점·이용금액·공급가액·부가세·봉사료·승인번호·취소여부(Y/N)
)

// CardFormatLabels 는 화면에 보이는 형식 이름이다(영문 코드를 보이지 않는다).
var CardFormatLabels = map[string]string{CardFormatA: "승인일시 한 칸형", CardFormatB: "일·시 분리 공급가형"}

// 카드 내역 구분.
const (
	CardKindApprove = "approve"
	CardKindCancel  = "cancel"
)

// CardRow 는 정규화한 카드 내역 한 행이다(공통 행). 취소는 금액이 음수다.
type CardRow struct {
	RowNo      int       // 원본 파일의 행 번호(머리글 다음이 2)
	Last4      string    // 카드번호 끝 4자리(숫자 4자). 전체 번호는 들어오지 않는다
	UsedAt     time.Time // 사용 일시(KST 기준 시각, UTC 로 표현해도 된다)
	Merchant   string    // 가맹점명(연속 공백 하나, 200자 상한)
	Amount     int64     // 정수 원, 취소는 음수
	Supply     *int64    // 공급가액(CSV 에 있을 때)
	VAT        *int64    // 부가세(CSV 에 있을 때)
	Tip        *int64    // 봉사료(CSV 에 있을 때)
	ApprovalNo string    // 승인번호(없으면 빈 값)
	Kind       string    // CardKindApprove | CardKindCancel
}

// CardRowError 는 오류 행이다(그 행만 제외하고 업로드는 계속한다).
type CardRowError struct {
	RowNo  int
	Reason string // 한국어 한 문장. 원본 행의 값(가맹점명·카드번호 조각)을 넣지 않는다
}

// CardParseResult 는 CSV 한 파일의 정규화 결과다.
type CardParseResult struct {
	Format       string // 인식했거나 호출자가 고른 형식
	Rows         []CardRow
	Errors       []CardRowError
	TotalRows    int  // 머리글을 뺀 데이터 행 수
	TooManyError bool // 오류 행이 20% 를 넘어 형식이 맞지 않는 것으로 본다(호출자가 업로드를 거부한다)
}

// CardParser 는 카드사 CSV 를 공통 행으로 바꾸는 순수 함수 묶음이다(ai, internal/cardimport). 입력은 바이트뿐이고 DB·시계·LLM 을 쓰지 않는다.
type CardParser interface {
	// Detect 는 머리글로 형식을 알아낸다. 알아내지 못하면 ok=false 다("형식을 인식하지 못했습니다. 카드사 형식을 직접 고르세요.").
	Detect(data []byte) (format string, ok bool)
	// Parse 는 형식(비면 Detect)에 따라 정규화한다. 인코딩은 UTF-8·CP949 를 자동 판별한다. 5MB·10,000행 초과는 error(ErrValidation 계열 한국어 문구)다.
	Parse(data []byte, format string) (CardParseResult, error)
}

// CardDedupKey 는 중복 판정 키다(명세 3.3.2 의 4): 승인번호가 있으면 (카드, 승인번호, 사용일, 금액, 구분), 없으면 (카드, 사용 일시, 가맹점명, 금액, 구분).
// 키 규칙의 정의는 이 함수 한 곳이다(erp 저장소와 ai 시험이 같이 쓴다).
func CardDedupKey(cardID int64, r CardRow) string {
	day := r.UsedAt.In(contract.KST).Format("2006-01-02")
	if r.ApprovalNo != "" {
		return key("A", cardID, r.ApprovalNo, day, r.Amount, r.Kind)
	}
	return key("B", cardID, r.UsedAt.In(contract.KST).Format("2006-01-02T15:04:05"), r.Merchant, r.Amount, r.Kind)
}

// ---- MCP 도구가 읽는 조회(ai 가 도구에서 부른다, 읽기 전용) ----

// CardUnreconciledItem 은 본인 소지 카드의 미소명·보완 필요 내역 한 건이다. 카드번호 조각은 없다(카드 별칭만).
type CardUnreconciledItem struct {
	ID         int64
	UsedAt     time.Time
	Merchant   string
	Amount     int64
	DueDate    string // 소명 기한(YYYY-MM-DD)
	Overdue    bool
	CardAlias  string
	NeedsFixed bool // 보완 필요(반려·역분개)
}

// CardReader 는 MCP 도구 get_card_unreconciled 가 쓰는 조회다. 호출자 본인 소지분만(재무도 본인 소지분만) 돌려주고 권한은 card:explain 이다.
type CardReader interface {
	Unreconciled(ctx context.Context, limit int) ([]CardUnreconciledItem, error)
}
