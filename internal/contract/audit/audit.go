// Package audit 는 감사 기록기 계약이다. 구현: platform (internal/audit, 해시 체인).
//
// 불변식
//   - 추가 전용. 기록된 행은 수정·삭제되지 않는다(마이그레이션의 트리거가 막는다).
//   - 각 레코드는 직전 레코드의 해시를 포함한다. Verify 가 변조 지점(Seq)을 짚는다.
//   - 쓰기 요청과 감사 기록은 같은 트랜잭션이다. Record 가 실패하면 서비스는 오류를 그대로 돌려
//     전체를 롤백한다(기록 실패 = 요청 실패).
package audit

import (
	"context"
	"errors"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
)

// Result 는 동작의 결과다.
type Result string

const (
	ResultSuccess Result = "success"
	ResultDenied  Result = "denied" // 권한 거부도 기록한다
	ResultError   Result = "error"
)

// TargetType 은 감사 대상 종류다. 상수 목록으로 고정하고 프론트 목록(web/src/admin/targetTypes.ts)과
// 어긋나면 테스트가 실패한다(spec 7절).
type TargetType string

const (
	TargetUser             TargetType = "user"
	TargetSession          TargetType = "session"
	TargetToken            TargetType = "token"
	TargetPurchaseRequest  TargetType = "purchase_request"
	TargetApproval         TargetType = "approval"
	TargetPurchaseOrder    TargetType = "purchase_order"
	TargetItem             TargetType = "item"
	TargetReceipt          TargetType = "receipt"
	TargetDocument         TargetType = "document"
	TargetVerdict          TargetType = "verdict"
	TargetLLMCall          TargetType = "llm_call"
	TargetLLMPolicy        TargetType = "llm_policy"
	TargetAuditLog         TargetType = "audit_log"
	TargetRoute            TargetType = "route" // 권한 거부된 API 경로
	TargetVendor           TargetType = "vendor"
	TargetDelegation       TargetType = "approval_delegation"  // 결재 위임(B3)
	TargetSetting          TargetType = "setting"              // 관리자 설정(B2)
	TargetEval             TargetType = "eval_run"             // 평가 실행 기록(B6)
	TargetPolicy           TargetType = "policy"               // 사내 규정 검색(B12)
	TargetOrgUnit          TargetType = "org_unit"             // 조직 단위(B20)
	TargetPerson           TargetType = "person"               // 인사 발령 대상 사람(B20)
	TargetOrgRank          TargetType = "org_rank"             // 직위 표(B20)
	TargetImport           TargetType = "import"               // 조직 CSV 일괄 반영(B22), 대상 ID 는 반영 번호
	TargetApprovalForm     TargetType = "approval_form"        // 결재 양식·양식 버전(B23)
	TargetApprovalDoc      TargetType = "approval_doc"         // 구매 품의서 외 양식 문서(B23)
	TargetPersonalLine     TargetType = "personal_line"        // 개인 결재선(B23)
	TargetDocAttachment    TargetType = "doc_attachment"       // 결재 문서 첨부(B23)
	TargetAuthorityRule    TargetType = "authority_rule"       // 전결 규정(B23 단계 3)
	TargetWarehouse        TargetType = "warehouse"            // 창고(B24)
	TargetStockIssue       TargetType = "stock_issue"          // 출고 전표(B24)
	TargetStockTransfer    TargetType = "stock_transfer"       // 창고 이동 전표(B24)
	TargetStockCount       TargetType = "stock_count"          // 재고 실사 표(B24)
	TargetBudget           TargetType = "budget"               // 부서·월 예산(B25)
	TargetExport           TargetType = "export"               // 목록·보고서 파일 내보내기(B28)
	TargetMonthClose       TargetType = "month_close"          // 월 마감·마감 해제·평가 소급 재생(B27)
	TargetJournal          TargetType = "journal"              // 전표·전표 대기(B31)
	TargetAccount          TargetType = "account"              // 계정과목·자동 전표 규칙(B31)
	TargetPayment          TargetType = "payment"              // 지급 요청·지급(B32)
	TargetBankAccount      TargetType = "bank_account"         // 회사 자금 계좌(B32)
	TargetCard             TargetType = "card"                 // 법인카드·카드 내역·소명(B32)
	TargetPurchaseQuote    TargetType = "purchase_quote"       // 구매요청 견적서(B25 단계 2)
	TargetDocReceipt       TargetType = "doc_receipt"          // 양식 문서의 수신 건(B23 단계 2)
	TargetCredential       TargetType = "credential"           // 사용자 로그인 자격: 비밀번호·2단계·외부 신원 연결(B30)
	TargetAuthProvider     TargetType = "auth_provider"        // 로그인 방식 설정(B30)
	TargetRole             TargetType = "role"                 // 역할·2인 확인 요청(B33)
	TargetRoleGrant        TargetType = "role_grant"           // 역할 부여(B33)
	TargetNotification     TargetType = "notification"         // 알림 채널 관리: 발송 건 다시 보내기·시험 메일·보존 정리(B36)
	TargetSetup            TargetType = "setup"                // 설치 마법사(B38)
	TargetPrivacyLog       TargetType = "privacy_log"          // 개인정보 조회 기록 조회·요약·파기·보관 설정(B41)
	TargetPrivacyRequest   TargetType = "privacy_request"      // 정보주체 열람 요청(B41)
	TargetSoDRule          TargetType = "sod_rule"             // 권한 분리 규칙 모드(B41)
	TargetSoDViolation     TargetType = "sod_violation"        // 권한 분리 위반·차단 기록(B41)
	TargetAPIToken         TargetType = "api_token"            // 연동 API 토큰·켜기·속도 설정(B42)
	TargetChangeFeed       TargetType = "change_feed"          // 변경 피드 보존 정리(B42)
	TargetSoDException     TargetType = "sod_exception"        // 권한 분리 예외(B41)
	TargetWebhook          TargetType = "webhook_subscription" // 변경 이벤트 웹훅 구독(B42)
	TargetTaxInvoice       TargetType = "tax_invoice"          // 전자세금계산서(B43)
	TargetEntity           TargetType = "entity"               // 법인(B44)
	TargetEntitySite       TargetType = "entity_site"          // 법인의 사업장(B44)
	TargetKnowledge        TargetType = "knowledge"            // 사내 지식 검색·색인·업로드(B47)
	TargetLLMProvider      TargetType = "llm_provider"         // LLM 공급자 설정(B45)
	TargetMCPTool          TargetType = "mcp_tool"             // 사용자 정의 MCP 도구(B48)
	TargetAICost           TargetType = "ai_cost"              // 부서별 AI 비용 환율·청구액·규칙·예산·조회(B49), 대상 ID 는 월·부서 번호 등
	TargetModule           TargetType = "module"               // 업무 모듈 켜기·끄기(B50)
	TargetLeaveRequest     TargetType = "leave_request"        // 휴가 신청·잔여 원장 조정·연차 발생(B50)
	TargetLeavePolicy      TargetType = "leave_policy"         // 휴가 종류·연차 규칙·근무 유형(B50)
	TargetAttendanceRecord TargetType = "attendance_record"    // 출퇴근 정정·근태 마감·근무 유형 배정(B50)
	TargetEvalSet          TargetType = "eval_set"             // 평가 세트(B46)
	TargetLLMRoute         TargetType = "llm_route"            // LLM 라우팅 규칙(B45)
	TargetTaxASP           TargetType = "tax_asp"              // 전자세금계산서 대행사 설정(B43)
	TargetConnector        TargetType = "connector"            // 연동 커넥터 실행(B42 3단계, erp-admin connector run)
	TargetSystem           TargetType = "system"               // 서버 운영 작업: 백업·복구·키 교체·DB 이관(B38, B37)
)

// TargetTypes 는 허용되는 대상 종류 전체다.
var TargetTypes = []TargetType{
	TargetUser, TargetSession, TargetToken, TargetPurchaseRequest, TargetApproval,
	TargetPurchaseOrder, TargetItem, TargetReceipt, TargetDocument, TargetVerdict,
	TargetLLMCall, TargetLLMPolicy, TargetAuditLog, TargetRoute, TargetVendor,
	TargetDelegation, TargetSetting, TargetEval, TargetPolicy,
	TargetOrgUnit, TargetPerson, TargetOrgRank, TargetImport,
	TargetApprovalForm, TargetApprovalDoc, TargetPersonalLine, TargetDocAttachment, TargetDocReceipt, TargetAuthorityRule, TargetWarehouse, TargetStockIssue, TargetStockTransfer, TargetStockCount, TargetBudget, TargetExport, TargetPurchaseQuote, TargetMonthClose, TargetJournal, TargetAccount, TargetPayment, TargetBankAccount, TargetCard, TargetCredential, TargetAuthProvider, TargetRole, TargetRoleGrant, TargetNotification, TargetSetup, TargetSystem, TargetPrivacyLog, TargetPrivacyRequest, TargetSoDRule, TargetSoDViolation, TargetSoDException, TargetWebhook, TargetConnector, TargetTaxInvoice, TargetEntity, TargetEntitySite, TargetTaxASP, TargetAPIToken, TargetChangeFeed,
	TargetLLMProvider, TargetLLMRoute, TargetEvalSet, TargetKnowledge, TargetMCPTool, TargetAICost, TargetModule, TargetLeaveRequest, TargetLeavePolicy, TargetAttendanceRecord,
}

// DiffItem 은 변경 전후 한 항목이다(spec 7절). UPDATE 는 {Key, Old, New}, CREATE/DELETE 는 Old 또는 New 만 채운다.
type DiffItem struct {
	Key string `json:"key"`
	Old string `json:"old,omitempty"`
	New string `json:"new,omitempty"`
}

// MCP 호출 종료 기록(B13)이 Diff 에 싣는 키다. 해시 규칙은 그대로이고 Diff 항목으로만 담는다.
// 사용량 집계는 채널이 mcp 이고 DiffKeyDurationMS 항목이 있는 기록만 호출 1건으로 센다.
const (
	DiffKeyTokenID    = "token_id"    // 호출에 쓴 개인 토큰 번호(세션이 아니면 없음)
	DiffKeyDurationMS = "duration_ms" // 호출 소요 시간(밀리초)

	// 한도 초과 거부 요약 기록(mcp.rate_limited_summary, B17)이 싣는 키다. 첫 거부는 즉시 1행으로 남기고,
	// 창이 닫힐 때 2회 이상이면 요약 1행을 더 남긴다(감사 행은 고치지 않는다).
	DiffKeyDeniedCount = "denied_count" // 그 창의 거부 총횟수(첫 거부 포함)
	DiffKeyWindowStart = "window_start" // 한도 창 시작(unix 분, 문자열)
)

// Entry 는 기록할 사건 한 건이다. 시각·시퀀스·해시는 기록기가 채운다.
// Actor 는 Principal 로 받는다(ctx 에서 꺼내 넘긴다). LLM 게이트웨이처럼 사용자 없는 사건은 UserID 0.
type Entry struct {
	Actor      contract.Principal
	Action     string // 예: "purchase_request.submit"
	TargetType TargetType
	TargetID   string
	Result     Result
	Diff       []DiffItem
	Detail     string // 거부 사유 등 자유 텍스트(개인정보 금지)
	// EntityID 는 사건이 속한 법인이다(B44). 0 이면 요청 context 의 현재 법인(contract.EntityFrom)을 쓰고, NoEntity 면 법인과 무관한 사건으로 0 을 강제한다.
	EntityID int64
}

// NoEntity 는 Entry.EntityID 에 넣어 "법인과 무관한 사건" 으로 기록하라는 표시다(저장 값은 0).
const NoEntity int64 = -1

// Record 는 저장된 감사 레코드다.
type Record struct {
	Seq        int64
	At         time.Time
	ActorID    int64
	ActorRole  contract.Role
	Channel    contract.Channel
	Action     string
	TargetType TargetType
	TargetID   string
	Result     Result
	Diff       []DiffItem
	Detail     string
	PrevHash   string
	Hash       string
	// EntityID 는 사건이 속한 법인이다(0 = 법인과 무관). 0 이 아닐 때만 해시에 들어간다(이관 전 레코드의 해시가 그대로다, B44 13.1).
	EntityID int64
}

// Filter 는 감사 로그 조회 조건이다. 영값 필드는 무시한다.
type Filter struct {
	ActorID    int64
	ActorIDs   []int64 // 비어 있지 않으면 이 중 하나인 행위자(이름 검색용). ActorID 와 함께 주면 둘 다 만족해야 한다
	Action     string
	TargetType TargetType
	TargetID   string
	Result     Result
	Channel    contract.Channel
	From, To   time.Time
	Limit      int
	AfterSeq   int64
	Desc       bool // true 면 최신(Seq 큰 것)부터
	// Entities 는 법인 읽기 범위다(B44). nil 이거나 All 이면 거르지 않는다. 아니면 범위 안의 법인 사건과 법인과 무관한 사건(0)만 돌려준다.
	Entities *contract.EntityScope
}

// VerifyResult 는 체인 검증 결과다. 변조가 없으면 OK. 있으면 처음 어긋난 Seq 와 이유를 담는다.
type VerifyResult struct {
	OK         bool // 체인과 외부 앵커 대조가 모두 문제없을 때만 true
	ChainOK    bool // DB 안의 해시 체인만 본 결과
	Checked    int64
	BrokenSeq  int64
	BrokenWhy  string
	HeadHash   string
	VerifiedAt time.Time
	Anchor     AnchorStatus // 외부 앵커 대조 결과(B4)
}

// 검증 결과의 요약 등급(VerifyResult.Level). OK 불리언만 보면 "외부 대조 없음"이 통과로 읽히므로 따로 둔다.
const (
	LevelFailed      = "failed"       // 변조 감지
	LevelChainOnly   = "chain_only"   // 체인은 맞지만 외부 대조를 하지 못함(앵커 꺼짐·비어 있음). 완전 통과가 아니다
	LevelLostHistory = "lost_history" // 현재 앵커는 맞지만 앵커 유실 이력이 있음
	LevelVerified    = "verified"     // 체인과 외부 앵커가 모두 일치하고 유실 이력도 없음
)

// Level 은 검증 결과의 요약 등급이다.
func (r VerifyResult) Level() string {
	switch {
	case !r.OK:
		return LevelFailed
	case r.Anchor.State != AnchorOK:
		return LevelChainOnly
	case r.Anchor.LostHistory:
		return LevelLostHistory
	}
	return LevelVerified
}

// ExternallyVerified 는 외부 앵커 대조까지 끝나 완전히 통과했는지다. 앵커가 꺼졌거나 비었거나
// 유실 이력이 있으면 false 다.
func (r VerifyResult) ExternallyVerified() bool { return r.Level() == LevelVerified }

// Summary 는 화면에 그대로 보이는 한국어 한 줄 요약이다.
func (r VerifyResult) Summary() string {
	switch r.Level() {
	case LevelFailed:
		if r.ChainOK {
			return "기록 체인은 이어져 있지만 외부 앵커 대조에서 문제가 감지되었습니다."
		}
		return "기록 체인에서 변조가 감지되었습니다."
	case LevelChainOnly:
		return "기록 체인은 이어져 있지만 외부 대조를 하지 못했습니다(완전 통과가 아닙니다). 외부 앵커가 꺼져 있거나 아직 기록되지 않았습니다."
	case LevelLostHistory:
		return "기록 체인과 현재 외부 앵커는 일치하지만, 앵커 유실 이력이 있어 그 구간은 외부 대조가 끊겼습니다."
	}
	return "기록 체인과 외부 앵커 대조가 모두 일치합니다."
}

// AnchorState 는 외부 앵커 대조 결과의 종류다.
type AnchorState string

const (
	AnchorDisabled    AnchorState = "disabled"           // 키가 없어 외부 대조를 하지 않음(통과가 아니다)
	AnchorEmpty       AnchorState = "empty"              // 앵커가 아직 한 줄도 없어 대조할 것이 없음
	AnchorOK          AnchorState = "ok"                 // 모든 앵커가 서명·연결·DB 해시와 일치
	AnchorFileCorrupt AnchorState = "file_corrupt"       // 앵커 파일 손상·위조·줄 삭제·순서 변경
	AnchorTailDeleted AnchorState = "tail_deleted"       // 앵커가 가리키는 기록이 DB 에 없음(마지막 기록 삭제)
	AnchorRecomputed  AnchorState = "content_recomputed" // 앵커 시점의 해시와 DB 해시가 다름(내용 재계산)
	AnchorLost        AnchorState = "anchor_lost"        // DB 에는 앵커를 찍은 기록이 있는데 파일에 그 줄이 없음(파일 삭제·줄 잘림, B9)
)

// ErrAnchorLost 는 앵커 파일 유실 상태에서 새 앵커를 쓰려 할 때 돌려준다. 유실을 조용히 덮지 않는다.
// 감사자가 명시적으로 앵커를 다시 시작(AnchorRestarter)해야 한다.
var ErrAnchorLost = errors.New("외부 앵커 기록이 유실되어 새 앵커를 쓰지 않았습니다")

// Failed 는 외부 앵커 대조가 변조를 가리키는 상태인지 본다.
func (s AnchorState) Failed() bool {
	return s == AnchorFileCorrupt || s == AnchorTailDeleted || s == AnchorRecomputed || s == AnchorLost
}

// AnchorStatus 는 검증 시 외부 앵커 대조 결과다. Message 는 화면에 그대로 보이는 한국어 문장이다.
type AnchorStatus struct {
	State   AnchorState
	Message string
	Lines   int       // 확인한 앵커 줄 수
	LastSeq int64     // 마지막 앵커가 가리키는 기록 번호
	LastAt  time.Time // 마지막 앵커 시각
	// LostHistory 는 과거에 앵커 유실이 있었고 재시작했다는 표시다. 근거는 DB 라서 재기동해도 유지된다(B9).
	LostHistory bool
}

// StampResult 는 앵커 기록 시도의 결과다. 새 기록이 없으면 Written 이 false 이고 줄은 늘지 않는다.
type StampResult struct {
	Written bool
	Seq     int64
	Hash    string
	At      time.Time
}

// Anchorer 는 체인 머리 해시를 DB 밖 파일에 서명해 남긴다(B4). 구현: internal/audit.Anchor.
// 키가 설정되지 않으면 구현이 만들어지지 않는다(deps.AuditAnchor == nil).
type Anchorer interface {
	Stamp(ctx context.Context) (StampResult, error)
}

// AnchorRestarter 는 유실된 앵커를 명시적으로 다시 시작한다(B9). 유실 상태가 아니면 ErrInvalidState.
// tx 안에서 재시작 행을 남기므로 호출자는 같은 tx 로 감사 레코드(audit_log.anchor_restart)를 기록해야 한다.
// 재시작 뒤에도 검증 결과에는 LostHistory 가 계속 표시된다.
type AnchorRestarter interface {
	Restart(ctx context.Context, tx contract.Tx) error
}

// Recorder 는 감사 기록기다. 구현: platform. 쓰는 쪽: erp·ai 의 서비스(서비스 계층에서 호출, spec 7절).
type Recorder interface {
	// Record 는 tx 안에서 레코드를 이어 붙인다. 호출자는 같은 tx 로 비즈니스 쓰기를 했어야 하고,
	// 오류를 돌려받으면 그 오류로 WithTx 를 끝내 롤백해야 한다.
	Record(ctx context.Context, tx contract.Tx, e Entry) error
}

// Reader 는 조회·검증이다(auditor 용). 구현: platform. 핸들러는 audit:read / audit:verify 를 선언한다.
// Scanner 는 기간 전체를 훑는 읽기 전용 조회다(사용량 집계, B13). Reader.List 의 500건 상한이 없다.
// 감사 레코드를 새로 만들지 않으므로 체인에 영향이 없다. 구현: internal/audit.Store.
type Scanner interface {
	Scan(ctx context.Context, f Filter, fn func(Record) error) error
}

type Reader interface {
	List(ctx context.Context, f Filter) ([]Record, error)
	Verify(ctx context.Context) (VerifyResult, error)
}
