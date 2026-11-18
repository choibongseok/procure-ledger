package domain

import (
	"context"
	"time"
)

// 전자세금계산서(B43,). 발행(매출)·수신(매입) 공통 모델이다. 금액은 정수 원, 작성일은 KST 날짜(YYYY-MM-DD)다.
// 서비스 계층 함수는 웹 핸들러·MCP 도구(3단계)·전송 작업이 같이 쓴다. HTTP 요청 객체에 의존하지 않는다.

// 방향.
const (
	TaxSale     = "sale"     // 매출: 우리가 공급자(발행)
	TaxPurchase = "purchase" // 매입: 우리가 공급받는자(수신)
)

// 과세 유형.
const (
	TaxTaxable = "taxable" // 과세
	TaxZero    = "zero"    // 영세율
	TaxExempt  = "exempt"  // 면세 계산서
)

// 상태(명세 5절). 표에 없는 전이는 서비스(taxTransitions)와 DB 트리거가 모두 거부한다.
const (
	TaxDraft    = "draft"    // 작성 중
	TaxIssued   = "issued"   // 발행 대기(전송 대기)
	TaxSent     = "sent"     // 전송됨(대행사 접수)
	TaxAccepted = "accepted" // 국세청 수신(승인번호 있음). 매입은 이 상태로 생긴다
	TaxFailed   = "failed"   // 실패
	TaxCanceled = "canceled" // 취소
)

// 영수/청구.
const (
	TaxReceipt = "receipt"
	TaxClaim   = "claim"
)

// 출처.
const (
	TaxSourceManual = "manual"
	TaxSourceASP    = "asp"
)

// 수정 사유 코드(공개 서식의 수정 사유 구분을 일반화, D5). 사유가 있으면 수정세금계산서이고 음수 금액을 허용한다.
const (
	AmendEntryError     = "entry_error"     // 기재사항 착오 정정
	AmendAmountChange   = "amount_change"   // 공급가액 변동
	AmendReturn         = "return"          // 환입
	AmendContractCancel = "contract_cancel" // 계약 해제
	AmendLCAfter        = "lc_after"        // 내국신용장 사후 개설
	AmendDoubleIssue    = "double_issue"    // 착오에 의한 이중 발급
)

// TaxStatusLabels 는 화면에 보이는 상태 이름이다(영문 코드를 보이지 않는다).
var TaxStatusLabels = map[string]string{
	TaxDraft: "작성 중", TaxIssued: "발행 대기", TaxSent: "전송됨", TaxAccepted: "국세청 수신", TaxFailed: "실패", TaxCanceled: "취소",
}

// TaxTypeLabels 는 과세 유형 이름이다.
var TaxTypeLabels = map[string]string{TaxTaxable: "과세", TaxZero: "영세율", TaxExempt: "면세"}

// TaxDirectionLabels 는 방향 이름이다.
var TaxDirectionLabels = map[string]string{TaxSale: "매출", TaxPurchase: "매입"}

// AmendReasonLabels 는 수정 사유 이름이다.
var AmendReasonLabels = map[string]string{
	AmendEntryError: "기재사항 착오 정정", AmendAmountChange: "공급가액 변동", AmendReturn: "환입",
	AmendContractCancel: "계약 해제", AmendLCAfter: "내국신용장 사후 개설", AmendDoubleIssue: "착오에 의한 이중 발급",
}

// TaxCodeLabels 는 전송 결과·실패 사유 코드의 한국어 문장이다. 응답 본문·주소·키는 어디에도 남기지 않고 코드만 남는다.
var TaxCodeLabels = map[string]string{
	"ok":                    "처리되었습니다.",
	"network":               "대행사에 연결하지 못했습니다.",
	"timeout":               "대행사 응답이 늦어 중단했습니다.",
	"unavailable":           "대행사가 일시적으로 처리하지 못했습니다.",
	"auth":                  "대행사 인증에 실패했습니다. 대행사 설정을 확인하세요.",
	"cert_expired":          "대행사 인증서가 만료되었습니다.",
	"rejected_validation":   "대행사가 내용 검증에서 반려했습니다.",
	"duplicate":             "대행사에 이미 접수된 건입니다.",
	"quota":                 "대행사 호출 한도를 넘었습니다.",
	"provider_rejected":     "대행사 또는 국세청이 반려했습니다.",
	"retry_over":            "재시도 횟수를 넘어 중단했습니다.",
	"status_timeout":        "국세청 수신 확인이 오래 걸려 중단했습니다.",
	"duplicate_approval_no": "이미 등록된 승인번호입니다.",
	"not_configured":        "대행사 설정이 없거나 꺼져 있습니다.",
	"rehearsal":             "복구 리허설 중에는 대행사 호출을 하지 않습니다.",
	"internal":              "내부 오류로 중단했습니다.",
	"pending":               "아직 국세청 수신 전입니다.",
}

// TaxParty 는 공급자·공급받는자 한 쪽이다(발행 시점의 스냅샷).
type TaxParty struct {
	BizNo    string // 사업자번호(10자리 숫자, 하이픈 없이 저장)
	BranchNo string // 종사업장번호(선택, 4자리)
	Name     string // 상호
	Rep      string // 대표자
	Addr     string
	Email    string // 화면에서는 항상 가려서 나간다(1단계)
}

// TaxLine 은 품목 줄이다.
type TaxLine struct {
	Seq       int
	Day       string // MM-DD(선택)
	Name      string
	Spec      string
	Qty       int64
	UnitPrice int64
	Supply    int64
	Tax       int64
	Remark    string
}

// TaxTransmission 은 대행사 호출 한 번의 기록이다(결과 코드만).
type TaxTransmission struct {
	AttemptNo   int
	At          time.Time
	Kind        string // issue | status | sync
	Code        string
	CodeLabel   string // 한국어 문장
	HTTPStatus  int
	ProviderRef string
}

// TaxInvoice 는 세금계산서 한 건이다.
type TaxInvoice struct {
	ID                int64
	EntityID          int64  // 소속 법인(B44)
	Intercompany      string // 상대방 사업자번호가 그룹의 다른 법인과 같으면 그 법인 이름(조회에서 파생, 저장하지 않는다. 법인 간 거래 배지, B44 11.1)
	No                string // TI-000001
	Direction         string
	TaxType           string
	Status            string
	ApprovalNo        string
	IssueDate         string
	Supplier          TaxParty
	Buyer             TaxParty
	SupplyTotal       int64
	TaxTotal          int64
	Total             int64
	AmendOfID         int64
	AmendOfNo         string
	AmendOfApprovalNo string // 정정하는 원 건의 승인번호(대행사에 보낼 때 쓴다)
	AmendedByNo       string // 이 건을 정정한 수정세금계산서 번호(있으면)
	AmendReason       string
	ChargeType        string
	Remark            string
	Source            string
	ProviderRef       string
	Attempts          int
	Resends           int
	NextTryAt         string
	LastCode          string
	LastCodeText      string // 한국어 문장
	CreatedBy         int64
	CreatedAt         time.Time
	IssuedBy          int64
	IssuedAt          string
	SentAt            string
	AcceptedAt        string
	CanceledAt        string
	CancelReason      string
	Lines             []TaxLine
	// MatchStatus·MatchResult 는 매입 세금계산서의 3자 대조 상태 이름(한국어, 상세에서만 채운다).
	MatchStatus string
	MatchResult string
	// LineCount·JournalNos 는 목록에서 WithLinks 를 켰을 때만 채운다(MCP 조회용: 품목 줄 수, 연결된 매입 전표 번호).
	LineCount     int
	JournalNos    []string
	Transmissions []TaxTransmission
	// Warnings 는 저장·발행을 막지 않는 안내다(지연 발급 등, D16).
	Warnings []string
}

// TaxLineInput 은 줄 입력이다. Supply 가 0 이고 수량·단가가 있으면 곱으로 채운다.
type TaxLineInput struct {
	Day       string
	Name      string
	Spec      string
	Qty       int64
	UnitPrice int64
	Supply    int64
	Remark    string
}

// TaxInvoiceInput 은 매출 세금계산서 작성·수정 입력이다. 공급자는 회사 정보(B34)로 채워지고 입력하지 않는다.
type TaxInvoiceInput struct {
	TaxType     string // 비면 과세
	IssueDate   string
	Buyer       TaxParty
	Lines       []TaxLineInput
	ChargeType  string // 비면 청구
	Remark      string
	AmendReason string // 수정 발행(AmendDraft)에서만 채운다
}

// TaxReceivedInput 은 매입 세금계산서 수기 등록 입력이다. 공급받는자는 회사 정보로 채워진다.
type TaxReceivedInput struct {
	ApprovalNo        string
	TaxType           string
	IssueDate         string
	Supplier          TaxParty
	SupplyTotal       int64
	TaxTotal          int64
	Lines             []TaxLineInput // 선택(있으면 공급가액 합이 SupplyTotal 과 같아야 한다)
	AmendOfApprovalNo string         // 수정세금계산서면 원 승인번호
	AmendReason       string
	Remark            string
}

// TaxInvoiceFilter 는 목록 조건이다(Limit 기본 50, 최대 200).
type TaxInvoiceFilter struct {
	Direction    string
	Status       string
	From, To     string // 작성일
	Counterparty string // 상대방 이름 일부
	ApprovalNo   string
	Limit        int
	Offset       int
	WithLinks    bool // 줄 수·연결된 전표 번호까지 채운다(건당 조회가 늘므로 한 쪽이 작은 호출만 켠다)
}

// TaxInvoicePage 는 목록 한 쪽이다.
type TaxInvoicePage struct {
	Rows  []TaxInvoice
	Total int
}

// TaxInvoiceService 는 세금계산서 서비스다. 조회 taxinvoice:read, 작성·수정·취소·수기 등록 taxinvoice:write, 발행·다시 전송 taxinvoice:issue.
type TaxInvoiceService interface {
	Create(ctx context.Context, in TaxInvoiceInput) (TaxInvoice, error)
	Update(ctx context.Context, id int64, in TaxInvoiceInput) (TaxInvoice, error)
	Cancel(ctx context.Context, id int64, reason string) (TaxInvoice, error)
	Issue(ctx context.Context, id int64) (TaxInvoice, error)
	Resend(ctx context.Context, id int64) (TaxInvoice, error)
	AmendDraft(ctx context.Context, id int64, reason string) (TaxInvoice, error)
	RegisterReceived(ctx context.Context, in TaxReceivedInput) (TaxInvoice, error)
	CancelReceived(ctx context.Context, id int64, reason string) (TaxInvoice, error)
	Get(ctx context.Context, id int64) (TaxInvoice, error)
	List(ctx context.Context, f TaxInvoiceFilter) (TaxInvoicePage, error)
}

// ---- 전송 작업(ai internal/taxasp 의 워커)이 쓰는 좁은 계약 ----

// TaxDispatchJob 은 워커가 가져간 일 하나다. Kind 가 issue 면 대행사에 발행을 요청하고, status 면 접수 결과를 조회한다.
type TaxDispatchJob struct {
	ID          int64
	No          string
	Kind        string // issue | status
	ProviderRef string // status 일 때
	Attempt     int    // 지금까지의 발행 시도 횟수(issue)
	SentAt      time.Time
	Invoice     TaxInvoice // issue 일 때(전송할 내용)
}

// TaxDispatchStore 는 전송 작업이 상태를 바꾸는 유일한 입구다. 전이는 서비스의 전이 표를 그대로 탄다(건 상태 + 전송 기록 + 감사를 한 트랜잭션에서).
type TaxDispatchStore interface {
	// ClaimDue 는 보낼 때가 된 일을 조건부 갱신으로 가져간다(다중 프로세스 안전). 15분 넘게 가져가기만 하고 끝나지 않은 건은 다시 가져갈 수 있다.
	ClaimDue(ctx context.Context, now time.Time, limit int) ([]TaxDispatchJob, error)
	// Submitted 는 발행 요청이 접수되어 issued → sent 로 간다.
	Submitted(ctx context.Context, id int64, providerRef string, httpStatus int) error
	// Retry 는 일시 오류로 issued 에 남기고 다음 시도를 예약한다(재시도 한도를 넘으면 failed 와 retry_over).
	Retry(ctx context.Context, id int64, code string, httpStatus int) error
	// Fail 은 영구 오류로 failed 가 된다(issued·sent 에서).
	Fail(ctx context.Context, id int64, code string, httpStatus int) error
	// Accepted 는 국세청 수신이 확인되어 sent → accepted 가 된다. 승인번호가 이미 있으면 failed(duplicate_approval_no)로 간다.
	Accepted(ctx context.Context, id int64, approvalNo string) error
	// StillPending 은 아직 수신 전이라 다음 조회를 예약한다(7일을 넘기면 failed 와 status_timeout).
	StillPending(ctx context.Context, id int64) error
}
