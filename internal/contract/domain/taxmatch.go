package domain

import (
	"context"
	"errors"
	"time"
)

// 매입 세금계산서 3자 대조(B43 2단계, 6.2·5.4)와 수신 동기화(6.1)의 계약.
// 대조 엔진은 코드가 결정적으로 제안하고 확정은 사람이 한다(D8). LLM·외부 호출이 없다.

// 대조 상태.
const (
	MatchProposed  = "proposed"
	MatchConfirmed = "confirmed"
	MatchDismissed = "dismissed"
	MatchUnlinked  = "unlinked"
)

// 대조 결과(명세 6.2).
const (
	MatchResultMatch         = "match"          // 입고 합이 공급가액과 정확히 같은 조합이 하나
	MatchResultAmbiguous     = "ambiguous"      // 정확히 같은 조합이 둘 이상(재무가 고른다: 제안은 가장 작고 오래된 조합)
	MatchResultAmountDiff    = "amount_diff"    // 같은 조합이 없다(가장 가까운 조합과 차액을 보인다)
	MatchResultVendorUnknown = "vendor_unknown" // 사업자번호로 거래처를 찾지 못함
	MatchResultNoReceipt     = "no_receipt"     // 연결할 수 있는 입고가 없음
)

// 경고 코드(결과를 막지 않는다).
const (
	MatchWarnTaxDiff    = "tax_diff"
	MatchWarnDateGap    = "date_gap"
	MatchWarnVendorName = "vendor_name_diff"
	MatchWarnMany       = "many_candidates"
)

// MatchStatusLabels·MatchResultLabels·MatchWarnLabels 는 화면에 보이는 한국어 이름이다.
var (
	MatchStatusLabels = map[string]string{MatchProposed: "제안 대기", MatchConfirmed: "연결됨", MatchDismissed: "기각", MatchUnlinked: "해제됨"}
	MatchResultLabels = map[string]string{
		MatchResultMatch: "일치", MatchResultAmbiguous: "후보가 여럿", MatchResultAmountDiff: "금액 불일치",
		MatchResultVendorUnknown: "거래처 미등록", MatchResultNoReceipt: "연결할 입고 없음",
	}
	MatchWarnLabels = map[string]string{
		MatchWarnTaxDiff:    "세액이 공급가액의 부가세율 계산값과 다릅니다.",
		MatchWarnDateGap:    "입고일의 달과 세금계산서 작성일의 달이 다릅니다(귀속 월을 확인하세요).",
		MatchWarnVendorName: "세금계산서 공급자 상호와 등록된 거래처 이름이 다릅니다.",
		MatchWarnMany:       "연결 후보 입고가 많아 가장 오래된 12건만 대조했습니다.",
	}
)

// TaxMatchReceipt 는 대조에 속한 입고 한 건이다.
type TaxMatchReceipt struct {
	ReceiptID  int64
	POID       int64
	PONo       string
	ReceivedAt string // KST 날짜 YYYY-MM-DD
	Supply     int64
	JournalNo  string // 그 입고의 매입 전표(없으면 빈 값)
}

// TaxMatchDiff 는 금액·건수·날짜뿐이다(이름·번호 없음).
type TaxMatchDiff struct {
	InvoiceSupply int64    `json:"invoice_supply"`
	CandidatesSum int64    `json:"candidates_sum"`
	Diff          int64    `json:"diff"`
	ReceiptCount  int      `json:"receipt_count"`
	Alternatives  int      `json:"alternatives"`
	TaxExpected   int64    `json:"tax_expected"`
	TaxActual     int64    `json:"tax_actual"`
	Months        []string `json:"months,omitempty"`
	VerdictIDs    []int64  `json:"verdict_ids,omitempty"`
}

// TaxMatch 는 대조 한 건이다.
type TaxMatch struct {
	ID                 int64
	InvoiceID          int64
	InvoiceNo          string
	ApprovalNo         string
	IssueDate          string
	SupplierName       string
	Supply, Tax, Total int64
	Status             string
	Result             string
	Receipts           []TaxMatchReceipt
	Diff               TaxMatchDiff
	Warnings           []string // 경고 코드
	ProposedAt         string
	DecidedAt          string
	Comment            string
	// Confirmable 은 지금 확정할 수 있는 제안인지(일치 또는 후보가 여럿이고 입고가 있음), Bulk 는 일괄 확정 대상(일치·경고 없음)인지다.
	Confirmable bool
	// JournalNote 는 확정했지만 자동 매입 전표를 만들지 못했을 때의 사유 한 줄이다(설정 taxinvoice.auto_journal 이 켜져 있을 때만).
	JournalNote string
	Bulk        bool
}

// TaxMatchFilter 는 대조 목록 조건이다.
type TaxMatchFilter struct {
	Status string
	Result string
	Limit  int
	Offset int
}

// TaxMatchPage 는 대조 목록 한 쪽이다.
type TaxMatchPage struct {
	Rows  []TaxMatch
	Total int
}

// TaxProposeSummary 는 제안 실행 결과다.
type TaxProposeSummary struct {
	Examined int // 대조 대상이던 수신 세금계산서
	Created  int
	Updated  int
	Skipped  int // 기각한 조합뿐이라 제안하지 않은 건
}

// TaxBulkResult 는 일괄 확정 결과다.
type TaxBulkResult struct {
	Confirmed int
	Skipped   int // 일치·경고 없음이 아니거나 이미 처리됨
}

// TaxMatchService 는 3자 대조 서비스다. 조회 taxinvoice:read, 제안 실행·확정·기각·해제 taxinvoice:write.
type TaxMatchService interface {
	Propose(ctx context.Context) (TaxProposeSummary, error)
	ListMatches(ctx context.Context, f TaxMatchFilter) (TaxMatchPage, error)
	GetMatch(ctx context.Context, id int64) (TaxMatch, error)
	Confirm(ctx context.Context, id int64, comment string) (TaxMatch, error)
	ConfirmBulk(ctx context.Context, ids []int64) (TaxBulkResult, error)
	Dismiss(ctx context.Context, id int64, reason string) (TaxMatch, error)
	Unlink(ctx context.Context, id int64, reason string) (TaxMatch, error)
}

// ---- 수신 동기화(명세 6.1) ----

// TaxReceivedItem 은 대행사 수신 목록의 한 건이다(승인번호가 키).
type TaxReceivedItem struct {
	ApprovalNo        string
	TaxType           string
	IssueDate         string
	Supplier          TaxParty
	Buyer             TaxParty
	Lines             []TaxLine
	SupplyTotal       int64
	TaxTotal          int64
	ChargeType        string
	Remark            string
	AmendReason       string
	AmendOfApprovalNo string
}

// TaxSyncCounts 는 한 번의 동기화 결과다(건수만).
type TaxSyncCounts struct {
	Inserted int `json:"inserted"`
	Same     int `json:"same"`     // 이미 같은 내용으로 있음(건너뜀)
	Conflict int `json:"conflict"` // 같은 승인번호에 내용이 다름(덮어쓰지 않고 기록만)
	Invalid  int `json:"invalid"`  // 형식 오류
	NotOurs  int `json:"not_ours"` // 공급받는자가 우리 회사가 아님
}

// Add 는 다른 결과를 더한다.
func (c *TaxSyncCounts) Add(o TaxSyncCounts) {
	c.Inserted += o.Inserted
	c.Same += o.Same
	c.Conflict += o.Conflict
	c.Invalid += o.Invalid
	c.NotOurs += o.NotOurs
}

// TaxSyncState 는 동기화 진행 상태다. Cursor 가 비어 있지 않으면 WinFrom~WinTo 창을 중간에 멈춘 것이라 같은 창을 이어서 읽는다.
type TaxSyncState struct {
	Cursor     string
	WinFrom    string
	WinTo      string
	LastDone   string // 마지막으로 끝까지 읽은 창의 끝(YYYY-MM-DD)
	LastRunAt  time.Time
	LastResult string // 코드: ok | partial | no_company | not_configured | failed
	LastCounts TaxSyncCounts
}

// ErrTaxSyncOwner 는 자동 동기화로 들어온 건을 기록할 대행사 연동 담당자가 없거나 비활성이라는 뜻이다(동기화를 멈춘다).
var ErrTaxSyncOwner = errors.New("대행사 연동을 켠 담당자가 없거나 비활성입니다")

// TaxSyncResultLabels 는 동기화 결과 코드의 한국어 문장이다.
var TaxSyncResultLabels = map[string]string{
	"ok": "정상적으로 가져왔습니다.", "partial": "한 번에 가져올 수 있는 양을 넘어 일부만 가져왔습니다. 다음 실행에서 이어서 가져옵니다.",
	"no_company": "회사 정보에 사업자번호가 없어 가져오지 않았습니다.", "not_configured": "대행사 설정이 없거나 꺼져 있어 가져오지 않았습니다.",
	"owner_inactive": "대행사 연동을 켠 담당자가 없거나 비활성이라 멈췄습니다. 담당자가 대행사 연동을 다시 켜야 합니다.", "failed": "대행사에서 가져오지 못했습니다. 다음 실행에서 다시 시도합니다.", "rehearsal": "복구 리허설 중에는 가져오지 않습니다.",
}

// TaxSyncStore 는 수신 동기화 작업(internal/taxasp 의 Syncer)이 쓰는 좁은 계약이다. 구현은 TaxInvoiceService.
type TaxSyncStore interface {
	SyncState(ctx context.Context) (TaxSyncState, error)
	SaveSyncState(ctx context.Context, st TaxSyncState) error
	// CompanyBizNo 는 우리 회사 사업자번호(숫자 10자리, 없으면 빈 값)다.
	CompanyBizNo(ctx context.Context) (string, error)
	// SyncEntities 는 대행사 연동이 켜져 있는 법인 번호들이다. 자동 동기화가 법인마다 한 번씩(그 법인 context 로) 돈다.
	SyncEntities(ctx context.Context) ([]int64, error)
	// CheckSyncOwner 는 자동 실행의 작성자(대행사 연동 담당자)가 유효한지 확인한다. 아니면 ErrTaxSyncOwner. 수동 실행이면 통과한다.
	CheckSyncOwner(ctx context.Context) error
	// ApplyReceived 는 수신 건들을 한 트랜잭션에서 반영한다: 새 승인번호는 삽입(accepted), 같은 내용은 건너뜀, 다른 내용은 충돌 기록(덮어쓰지 않음), 형식 오류·타사 건은 센다.
	ApplyReceived(ctx context.Context, items []TaxReceivedItem) (TaxSyncCounts, error)
	// FinishSync 는 한 번의 실행 결과를 상태와 감사(taxinvoice.sync)에 남기고, 새 건이 있으면 대조 제안을 갱신한다.
	FinishSync(ctx context.Context, st TaxSyncState, counts TaxSyncCounts, result string) error
}

// TaxSyncControl 은 수동 동기화·상태 조회의 권한 판정이다(서비스가 구현).
type TaxSyncControl interface {
	AuthorizeSync(ctx context.Context) error // taxinvoice:write
	SyncStatus(ctx context.Context) (TaxSyncState, error)
}

// TaxContact 는 세금계산서 상대방의 이메일 원문이다(B41 기록을 남긴 뒤에만 채워진다). 값이 없으면 Email 은 빈 값이다.
type TaxContact struct {
	Name  string
	Email string
}

// TaxContactService 는 상대방 이메일 원문 열람이다. taxinvoice:read + 원문 허용 권한이 모두 있어야 하고, 열람 기록이 남지 않으면 싣지 않는다.
type TaxContactService interface {
	RevealContact(ctx context.Context, id int64) (TaxContact, error)
}
