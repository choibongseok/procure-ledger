// Package deps 는 라우트 등록 함수와 MCP 도구가 받는 의존성 묶음이다.
// 필드 추가가 필요하면 platform 에게 요청한다(세 영역이 같은 파일을 고치지 않게).
package deps

import (
	"context"
	"database/sql"
	"github.com/choibongseok/procure-ledger/internal/apitoken"
	"github.com/choibongseok/procure-ledger/internal/apiv1"
	"github.com/choibongseok/procure-ledger/internal/evalset"
	"github.com/choibongseok/procure-ledger/internal/llmgw"
	"github.com/choibongseok/procure-ledger/internal/module"
	"github.com/choibongseok/procure-ledger/internal/privreport"
	"github.com/choibongseok/procure-ledger/internal/sod"
	"github.com/choibongseok/procure-ledger/internal/taxaspadmin"
	"github.com/choibongseok/procure-ledger/internal/webhooks"
	"net/http"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/contract/llm"
	"github.com/choibongseok/procure-ledger/internal/dbutil"
	"github.com/choibongseok/procure-ledger/internal/export"
	"github.com/choibongseok/procure-ledger/internal/notify"
	"github.com/choibongseok/procure-ledger/internal/privacy"
)

// Deps 는 cmd/server·cmd/erp-mcp 가 조립해 각 Register* 함수에 넘긴다.
// 누가 채우는가는 필드 주석과 cmd/server/wire_*.go 참고.
type Deps struct {
	DB  *sql.DB
	Txs contract.TxManager
	// Dialect 는 DB 종류별 SQL 조각이다(B37). 비어 있으면(시험이 Deps 를 직접 만든 경우) SQLite 로 본다.
	Dialect dbutil.Dialect

	// platform 이 채운다 (cmd/server/wire_platform.go)
	Audit         audit.Recorder
	AuditReader   audit.Reader
	AuditScan     audit.Scanner  // 기간 전체 읽기 전용 순회(B13 집계)
	AuditAnchor   audit.Anchorer // 외부 앵커(B4). 키가 없으면 nil = 기능 꺼짐
	Authenticator contract.Authenticator
	Masker        contract.Masker
	Tokens        contract.TokenManager
	TokenLookup   contract.TokenLookup
	Users         contract.UserDirectory
	Settings      contract.SettingsManager
	AuthHTTP      contract.AuthHTTP
	Onboarding    OnboardingHTTP                            // 도입 점검표·백업 상태 화면(B38 3단계)
	MCPTools      interface{ Register(contract.Registrar) } // 사용자 정의 MCP 도구 관리(B48). 서버만 라우트를 쓴다
	AICost        interface{ Register(contract.Registrar) } // 부서별 AI 비용 집계·환율·청구액(B49). 서버만 라우트를 쓴다
	AICostTool    interface {
		ToolSummary(ctx context.Context, month, department string) (map[string]any, error)
	} // B49 MCP 도구 get_ai_cost_summary 의 읽기 서비스(숫자만)
	EvalSets     *evalset.Service                          // 회사별 평가 세트(B46). 서버만 라우트를 쓴다
	LLMProviders *llmgw.ProviderAdmin                      // LLM 공급자·라우팅 규칙 관리(B45). 서버만 라우트를 쓴다
	TaxASP       *taxaspadmin.Service                      // 전자세금계산서 대행사 설정·시험 연결·인증서 만료 알림(B43). 서버만 라우트·주기 작업을 쓴다
	Entities     interface{ Register(contract.Registrar) } // 법인·사업장 관리(B44). 서버만 라우트를 쓴다
	Leave        domain.LeaveService                       // B50 휴가 종류·연차 규칙·잔여 원장·휴가 신청. 서버가 라우트와 일일 발생 작업(Run)을 쓴다
	Attendance   domain.AttendanceService                  // B50 출퇴근·정정·근태 마감·근무 유형·주 근로시간 경고. 서버가 라우트와 경고 작업(Run)을 쓴다
	Modules      *module.Service                           // 업무 모듈 켜기·끄기(B50). 서버가 라우트를 쓰고, 모듈 라우트·MCP 도구·서버 작업이 Enabled·Gate 로 꺼짐을 따른다
	Webhooks     *webhooks.Service                         // 변경 웹훅 구독·발송(B42 2단계). 서버만 라우트·주기 작업을 쓴다
	APITokens    *apitoken.Service                         // 연동 API 토큰 발급·인증·속도 제한·감사(B42). 서버만 라우트·주기 작업을 쓴다
	APIv1        *apiv1.Service                            // /api/v1 라우트와 인증 미들웨어(B42). WireERP 가 읽기 서비스(APIRead)를 꽂은 뒤 만든다
	PrivReport   *privreport.Service                       // 월별 점검 보고서와 월 알림(B41). 서버만 라우트·주기 작업을 쓴다
	SoD          sod.Evaluator                             // 권한 분리 판정(B41). 구현 *sod.Engine 의 Register·Run 은 서버가 타입 단언으로 쓴다
	Privacy      privacy.Noter                             // 개인정보 조회 기록(B41). 원문이 나가는 경로는 Note 를 부르고, 구현 *privacy.Recorder 의 Register·Run 은 서버가 타입 단언으로 쓴다. 운영 조립에서 nil 이면 원문 경로가 거부된다(fail-closed)
	SCIM         SCIMHTTP                                  // SCIM 2.0 계정 자동 동기화(B40). /scim/v2 와 관리 API. 서버(cmd/server)가 라우트·주기 작업을 쓴다
	Setup        SetupHTTP                                 // 설치 마법사(B38). 서버(cmd/server)만 만든다. nil 이면 마법사 라우트가 없다(erp-mcp·관리 명령)
	Access       AccessHTTP                                // 역할·부여 관리(B33). 구현은 *access.Service 이고 서버 코드는 타입 단언으로 작업(Run)을 꺼낸다
	NotifyEvents interface{ Run(context.Context) }         // 주기 알림 이벤트(지급 예정·월 마감 점검, B36). 서버만 Run 으로 돈다
	Notify       *notify.Service                           // 알림 채널(B36, t1): 발송 큐·메일 발송기·관리. 서버만 Run 으로 발송 작업을 돈다
	Export       *export.Env                               // 목록·보고서 내보내기 공용 환경(B28): 상한·속도 제한·감사. 각 목록의 export.Register 가 쓴다
	Import       domain.ImportService                      // 기초 데이터 가져오기 공통 흐름(B35, t1). 대상별 반영은 t2 가 RegisterTarget 으로 붙인다. 라우트는 구현의 Register 로 등록한다
	Org          contract.OrgDirectory                     // 조직 조회(B20). 구현은 *org.Service 이고 쓰기·작업이 필요한 서버 코드는 타입 단언으로 꺼낸다(deps 가 org 를 가져오면 순환)

	// ai 가 채운다 (cmd/server/wire_ai.go)
	LLM       llm.Client
	Documents domain.DocumentService
	Policy    domain.PolicyService // 사내 규정 검색(B12). 색인이 없으면 nil
	MCPRate   MCPRateLimiter       // 개인 토큰별 분당 MCP 호출 한도(B13). 한도 없음(PL_MCP_RATE_PER_MIN=0)이면 nil

	// erp 가 채운다 (cmd/server/wire_erp.go)
	Purchase   domain.PurchaseService
	Approval   domain.ApprovalService
	Delegation domain.DelegationService
	Inventory  domain.InventoryService
	// B21(조직도 기반 결재선·열람·재지정·알림). erp 가 채운다. 필드 추가는 platform 에게 알렸다.
	Line               domain.LineService
	Reassign           domain.ReassignService
	Notification       domain.NotificationService
	Forms              domain.FormService            // B23 결재 양식
	Attachments        domain.AttachmentService      // B23 첨부
	AttachMaxFileBytes int64                         // B23 첨부 파일당 상한(요청 본문 크기 제한에 쓴다). 조립 단계가 설정에서 채운다
	PersonalLines      domain.PersonalLineService    // B23 개인 결재선
	Docs               domain.ApprovalDocService     // B23 양식 문서(구매 품의서 외)
	Inbox              domain.InboxService           // B23 단계 2 문서함
	Receipts           domain.DocReceiptService      // B23 단계 2 수신 처리
	Authority          domain.AuthorityService       // B23 단계 3 전결 규정 표
	Warehouses         domain.WarehouseService       // B24 창고
	StockIssue         domain.StockIssueService      // B24 출고
	StockTransfer      domain.StockTransferService   // B24 창고 이동
	StockCount         domain.StockCountService      // B24 재고 실사
	Ledger             domain.LedgerService          // B24 수불부
	Budget             domain.BudgetService          // B25 부서·월 예산
	Retention          domain.RetentionService       // B29 보존 만료 문서 폐기
	POPrint            domain.POPrintService         // B25 단계 3 발주서 출력
	VendorRating       domain.VendorRatingService    // B25 단계 3 거래처 평가
	Quotes             domain.QuoteService           // B25 단계 2 견적서·비교
	MonthClose         domain.MonthCloseService      // B27 월 마감(단계 2)
	ExportLists        domain.ExportListService      // B28 t2 입고 이력·발주·재고 현황 목록(내보내기)
	Reports            domain.ReportService          // B28 t2 업무 보고서 3종
	DocSearch          domain.DocSearchService       // B29 t2 문서 통합 검색
	Nudge              domain.NudgeService           // B29 t2 결재 독촉
	Accounts           domain.AccountService         // B31 계정과목·자동 전표 규칙
	Journals           domain.JournalService         // B31 전표
	JournalQueue       domain.JournalQueueService    // B31 전표 대기
	Payables           domain.PayableService         // B31 매입 채무
	APIRead            domain.APIReadService         // B42 연동 API 읽기 자원(품목·거래처·발주·입고·전표·채무). t2 가 채운다. /api/v1 라우트(apiv1)가 쓴다
	Payments           domain.PaymentService         // B32 지급 요청·지급 처리
	Funds              domain.FundService            // B32 자금 계좌·자금 일보
	VAT                domain.VATService             // B32 부가세 매입 집계
	Consolidated       domain.ConsolidatedService    // B44 3단계 연결 재무 집계(합산만, t2)
	Knowledge          domain.KnowledgeService       // B47 사내 지식 검색. 서버·MCP 가 같은 서비스를 쓴다
	KnowledgeAnswer    domain.KnowledgeAnswerService // B47 3단계 질문하기(게이트웨이 용도 knowledge.answer). WireAI 가 채운다
	KnowledgeAdmin     domain.KnowledgeAdminService  // B47 색인 현황·다시 색인·양식별 검색 포함(knowledge:manage)
	KnowledgeWorker    interface {
		Run(context.Context, time.Duration)
	} // B47 색인 대기열 처리 작업(internal/knowledge.Worker). 서버만 돌린다. nil 이면 돌리지 않는다
	Sites           domain.SiteService                // B44 3단계 현재 법인의 사업장 선택기용 목록
	TaxInvoice      domain.TaxInvoiceService          // B43 전자세금계산서(발행·수신 공통 모델). t2 가 채운다
	TaxMatch        domain.TaxMatchService            // B43 2단계 매입 세금계산서 3자 대조
	TaxContact      domain.TaxContactService          // B43 2단계 상대방 이메일 원문 열람(기록 후)
	TaxSyncCtl      domain.TaxSyncControl             // B43 2단계 수신 동기화 상태·수동 실행 권한
	TaxSyncer       TaxSyncer                         // B43 2단계 수신 동기화 작업(internal/taxasp.Syncer). 서버만 돌린다. nil 이면 돌리지 않는다
	TaxWorker       interface{ Run(context.Context) } // B43 대행사 전송·상태 조회 작업(internal/taxasp.Worker). 서버만 돌린다. nil 이면 돌리지 않는다
	Company         domain.CompanyService             // B34 회사 설정(회사 정보·로고·직인)
	ImportReconcile domain.ImportReconcileService     // B35 기초 데이터 대조 보고서
	CardSvc         domain.CardService                // B32 법인카드(소명·확인·재무 현황)
	Cards           domain.CardReader                 // B32 법인카드 조회(MCP get_card_unreconciled, t2 service 가 구현·연결)
	LedgerReport    domain.LedgerReportService        // B31 계정별 원장·시산표
	JournalHook     domain.PurchaseJournalHook        // B31 t2 구현(전표 서비스), t3 가 docverify 확정에서 호출. nil 이면 호출하지 않는다
	MonthStock      domain.MonthStockService          // B27 월별 재고 현황(단계 3)
}

// MCPRateLimiter 는 개인 토큰별 분당 MCP 도구 호출 한도다(B13). 구현: internal/mcpusage.RateLimiter.
// 세는 기준은 DB 라서 서버(/mcp)와 erp-mcp 프로세스가 같은 DB 를 쓰면 한도가 합쳐서 적용된다.
type MCPRateLimiter interface {
	// Allow 는 이번 호출을 세고 한도 안이면 true 다. 한도를 넘은 호출은 세지 않는다(거부가 한도를 다시 소모하지 않는다).
	Allow(ctx context.Context, tokenID int64) (bool, error)
	// Limit 은 분당 한도다. 0 이하면 한도 없음이다.
	Limit() int
}

// OnboardingHTTP 는 도입 점검표·백업 상태 HTTP 계층이다. 구현: internal/onboarding.
type OnboardingHTTP interface {
	Register(r contract.Registrar)
}

// SetupHTTP 는 설치 마법사 HTTP 계층이다. 구현: internal/setup. PageHandler 는 `/setup` 화면 요청을 감싸 설치가 끝났으면 404 를 낸다.
type SetupHTTP interface {
	Register(r contract.Registrar)
	PageHandler(next http.Handler) http.Handler
}

// SCIMHTTP 는 SCIM 동기화 HTTP 계층이다. 구현: internal/scim. Wrap 은 /scim/v2 경로에 SCIM 토큰 인증을 거는 미들웨어고,
// 서버 조립(cmd/server)이 그 경로만 이쪽으로 보낸다(쿠키·개인 토큰 인증과 섞이지 않게). Run 은 보존 기간이 지난 기록을 정리하는 주기 작업이다.
type SCIMHTTP interface {
	Register(r contract.Registrar)
	Wrap(next http.Handler) http.Handler
	Run(ctx context.Context)
}

// AccessHTTP 는 역할·부여 관리 HTTP 계층이다. 구현: internal/access.
type AccessHTTP interface {
	Register(r contract.Registrar)
}

// TaxSyncer 는 수신 동기화 작업이다(internal/taxasp.Syncer). Run 은 하루 한 번 자동으로, RunOnce 는 수동 실행(결과 코드·건수)이다.
type TaxSyncer interface {
	Run(ctx context.Context)
	RunOnce(ctx context.Context) (string, domain.TaxSyncCounts, error)
}
