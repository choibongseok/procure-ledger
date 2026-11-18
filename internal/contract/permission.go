package contract

// Permission 은 "무엇을 할 수 있는가" 한 단위다. 라우트와 MCP 도구는 등록할 때 반드시 하나를 선언한다.
// 프론트의 web/src/auth/permissions.ts 와 값이 같아야 하며 permission_test.go 가 어긋남을 잡는다.
type Permission string

const (
	// PermPublic 은 인증 없이 열린 경로(헬스체크, 로그인, 정적 파일)를 명시적으로 선언할 때 쓴다.
	PermPublic Permission = "public"
	// PermAuthenticated 는 로그인만 되어 있으면 되는 경로(내 정보, 내 토큰)다.
	PermAuthenticated Permission = "authenticated"
	// PermSetup 은 로그인은 했지만 비밀번호 변경·2단계 인증 등록을 마치지 않은 "제한 세션"에서도 허용되는 경로다
	// (내 정보, 비밀번호 변경, 2단계 등록, 로그아웃). 제한 세션은 PermPublic 과 PermSetup 외에는 모두 거부된다(Principal.Can).
	PermSetup Permission = "setup"

	PermPurchaseCreate         Permission = "purchase:create"          // 구매요청 작성·수정·상신
	PermPurchaseRead           Permission = "purchase:read"            // 본인 부서 구매요청 조회
	PermPurchaseReadAll        Permission = "purchase:read_all"        // 타 부서 구매요청 조회
	PermApprovalRead           Permission = "approval:read"            // 결재함 조회
	PermApprovalDecide         Permission = "approval:decide"          // 승인·반려
	PermApprovalDelegate       Permission = "approval:delegate"        // 본인 결재 위임 지정·해제(B3)
	PermInventoryRead          Permission = "inventory:read"           // 품목·재고 조회
	PermInventoryManage        Permission = "inventory:manage"         // 품목 등록, 입고 처리
	PermDocumentVerify         Permission = "document:verify"          // 증빙 업로드·판정 요청
	PermDocumentRead           Permission = "document:read"            // 증빙·판정 조회
	PermDocumentConfirm        Permission = "document:confirm"         // 판정 최종 확정(재무)
	PermDocumentUploadImage    Permission = "document:upload_image"    // 이미지 증빙 업로드(재무만, B2). LLM 으로 이미지가 나간다
	PermAuditRead              Permission = "audit:read"               // 감사 로그 조회
	PermAuditVerify            Permission = "audit:verify"             // 해시 체인 검증
	PermEvalRead               Permission = "eval:read"                // 평가 실행 이력 조회(B6)
	PermKnowledgeSearch        Permission = "knowledge:search"         // 사내 지식 검색(B47): 완료된 결재 문서·첨부·업로드 매뉴얼·규정의 내용 검색. 열람 범위는 서비스가 문서마다 ViewScope 로 다시 판정한다(권한 밖은 존재도 숨김)
	PermKnowledgeManage        Permission = "knowledge:manage"         // 지식 문서 업로드·교체·삭제, 색인 현황·다시 색인, 양식별 내용 검색 포함 설정(B47, 관리자)
	PermPolicySearch           Permission = "policy:search"            // 사내 규정 검색(B12). 열람 범위는 문서 단위로 서비스가 적용한다
	PermUsageRead              Permission = "usage:read"               // AI 연결(MCP) 사용량 모니터링 조회(B13). 조회는 감사 기록을 만들지 않는다
	PermLLMUsageRead           Permission = "llm:usage_read"           // LLM 사용량 조회
	PermLLMPolicyManage        Permission = "llm:policy_manage"        // 모델 허용 목록·한도 관리
	PermTokenManage            Permission = "token:manage"             // 본인 개인 토큰 발급·폐기
	PermUserManage             Permission = "user:manage"              // 사용자·역할·부서 관리
	PermMaskBypass             Permission = "mask:bypass"              // 민감 필드 원문 조회
	PermOrgRead                Permission = "org:read"                 // 조직도·사람 조회(B20). 연락처는 mask:bypass 가 없으면 가린다
	PermOrgManage              Permission = "org:manage"               // 조직 변경·인사 발령 등록·취소·정정, 직위 표 편집(B20)
	PermOrgHistory             Permission = "org:history"              // 인사 발령 이력 상세(사유 포함)와 퇴사자 조회(B20)
	PermApprovalReassign       Permission = "approval:reassign"        // 결재 진행 중 건의 미완료 직책 단계 결재자 재지정(B21, admin 만)
	PermNotificationRead       Permission = "notification:read"        // 내 앱 안 알림 조회·읽음 처리(B21, 전 역할)
	PermFormManage             Permission = "form:manage"              // 결재 양식·양식 버전 관리(B23, admin 만)
	PermDraftCreate            Permission = "draft:create"             // 양식 문서 기안·상신(B23). 구매 품의서는 purchase:create 를 따른다
	PermDraftRead              Permission = "draft:read"               // 양식 문서 조회(B23). 열람 범위는 서비스가 정한다
	PermDocumentReadAll        Permission = "document:read_all"        // 관계 없는 양식 문서 특권 열람(B23, admin·auditor). 열람은 감사 기록. purchase:read_all 은 구매 품의서에만 적용
	PermApprovalAgree          Permission = "approval:agree"           // 합의 단계 처리(동의·이견, B23)
	PermInboxRead              Permission = "inbox:read"               // 문서함 7종 조회·읽음 처리(B23 단계 2, 전 역할)
	PermAuthorityManage        Permission = "authority:manage"         // 전결 규정 표 편집(B23 단계 3, admin)
	PermBudgetRead             Permission = "budget:read"              // 부서·월 예산 현황 조회(B25, 재무·관리자·감사)
	PermBudgetManage           Permission = "budget:manage"            // 예산 등록·수정(B25, 재무만)
	PermReportRead             Permission = "report:read"              // 업무 보고서 3종 조회(B28, 결재자·재무·감사·관리자, 직원 제외)
	PermRetentionRead          Permission = "retention:read"           // 보존 기한이 지난 문서·폐기 보류 목록 조회(B29, 관리자·감사)
	PermRetentionDispose       Permission = "retention:dispose"        // 만료 문서 폐기 승인·폐기 보류(B29, 관리자만)
	PermLedgerRead             Permission = "ledger:read"              // 전표·원장·시산표·매입 채무·전표 대기 조회, 금액 포함(B31, 재무·관리자·감사)
	PermLedgerWrite            Permission = "ledger:write"             // 전표 입력·수정·승인 요청·취소·역분개·전표 대기 처리(B31, 재무만. 관리자는 쓰지 않는다)
	PermAccountManage          Permission = "account:manage"           // 계정과목·자동 전표 규칙 관리, 거래처 결제 조건 수정(B31, 재무·관리자)
	PermPaymentRead            Permission = "payment:read"             // 지급 요청·일정표 조회(B32, 재무·관리자·감사)
	PermPaymentWrite           Permission = "payment:write"            // 지급 요청 작성·승인 요청·취소·지급 처리·지급 취소(B32, 재무만. 관리자는 쓰지 않는다)
	PermFundRead               Permission = "fund:read"                // 자금 계좌·자금 일보 조회(B32, 재무·관리자·감사)
	PermFundManage             Permission = "fund:manage"              // 자금 계좌 관리·입금 기록(B32, 재무·관리자는 계좌만)
	PermCardReadAll            Permission = "card:read_all"            // 카드 내역·소명 현황 전체 조회(B32, 재무·관리자·감사)
	PermCardManage             Permission = "card:manage"              // 카드 등록·소지자 변경·환입 처리(B32, 재무만)
	PermCardImport             Permission = "card:import"              // 카드 내역 CSV 업로드(B32, 재무만)
	PermCardExplain            Permission = "card:explain"             // 본인 카드 내역 소명(B32, 본인 소지분)
	PermRoleManage             Permission = "role:manage"              // 역할 표·부여 관리(B33, 위험 권한, admin 만, 세션으로만)
	PermFinanceStage           Permission = "approval:finance_stage"   // 결재선 "재무" 단계를 맡는 풀의 구성원(B33 구조 권한: 재무 결재함·재무 단계 알림·재무 위임 대상)
	PermCompanyRead            Permission = "company:read"             // 회사 정보(이름·사업자번호·대표자·주소·전화)와 로고·직인 이미지 조회(출력에 필요, 로그인한 모든 역할. B34)
	PermCompanyManage          Permission = "company:manage"           // 회사 설정 변경(admin 만. B34)
	PermNotificationManage     Permission = "notification:manage"      // 알림 채널 관리: 발송 기록·시험 메일·실패 건 다시 보내기(웹훅·기본 설정은 후속 단계, B36, 관리자만. 위험 권한 아님)
	PermImportMaster           Permission = "import:master"            // 기초 데이터 가져오기: 계정과목·창고·거래처·품목 양식·미리보기·반영(B35, 관리자·재무)
	PermImportBalance          Permission = "import:balance"           // 기초 데이터 가져오기: 부서 예산·기초 재고·미지급 잔액(금액성, B35, 재무만)
	PermImportRead             Permission = "import:read"              // 가져오기 이력·묶음 상세·오류 파일 조회(B35, 관리자·재무·감사)
	PermCardHolderManage       Permission = "card:holder_manage"       // 카드 소지자 변경·"소지자 없음" 재지정(B32 후속, 재무·관리자. 환입·카드 등록은 재무만)
	PermCardConfirm            Permission = "card:confirm"             // 소지자 소명 확인·반려(B32, 소지자의 조직장, 조직 범위)
	PermVATRead                Permission = "vat:read"                 // 부가세 매입 집계 조회(B32, 재무·관리자·감사)
	PermInventoryClose         Permission = "inventory:close"          // 월 마감 실행·마감 해제 요청서 작성(B27, 재무만)
	PermInventoryValuation     Permission = "inventory:valuation_read" // 재고 단가·금액·평가 열 조회(B27, 재무·관리자·감사)
	PermVendorRatingRead       Permission = "vendor:rating_read"       // 거래처 평가 조회(B25, 재무·관리자·감사)
	PermAuthManage             Permission = "auth:manage"              // 로그인 방식 설정·사용자 로그인 자격 관리(B30, admin 만, 세션으로만)
	PermOnboardingRead         Permission = "onboarding:read"          // 도입 점검표 조회·"해당 없음" 표시(B38, 관리자만. 위험 권한 아님)
	PermBackupRead             Permission = "backup:read"              // 백업 상태 화면 조회(마지막 백업·리허설 결과, B38, 관리자·감사. 실행은 앱 권한이 아니라 서버 호스트의 erp-admin)
	PermSCIMManage             Permission = "scim:manage"              // SCIM 연동 토큰 발급·폐기·설정·그룹 매핑(B40, 관리자만, 위험 권한: 토큰 발급 = 계정 생성 능력, 세션으로만)
	PermSCIMRead               Permission = "scim:read"                // SCIM 연동 상태·토큰 목록·동기화 기록 조회(B40, 관리자·감사)
	PermAPIManage              Permission = "api:manage"               // 연동 API 토큰 발급·폐기·켜기·끄기·속도 설정(B42, 관리자, 위험 권한: 데이터를 밖으로 내는 통로를 여는 권한, 세션으로만)
	PermTaxInvoiceRead         Permission = "taxinvoice:read"          // 세금계산서 목록·상세·MCP 조회(B43, 재무·관리자·감사)
	PermTaxInvoiceWrite        Permission = "taxinvoice:write"         // 매출 세금계산서 작성·수정·취소, 매입 수기 등록·등록 취소(B43, 재무만. 관리자는 쓰지 않는다)
	PermTaxInvoiceIssue        Permission = "taxinvoice:issue"         // 매출 세금계산서 발행(전송 요청)·다시 전송(B43, 재무만. write 와 분리해 커스텀 역할로 작성·발행 담당을 나눌 수 있다)
	PermConsolidatedRead       Permission = "consolidated:read"        // 연결 재무 집계(법인별 열 + 합계)·법인 간 거래 조회(B44, 재무·감사·관리자. group 범위가 있어야 전체 법인이 보이고 없으면 자기 범위의 법인만 합산한다)
	PermLLMProviderManage      Permission = "llm:provider_manage"      // LLM 공급자 설정(키·자격 증명·주소)·켜기/끄기·시험 연결(B45, 관리자, 위험 권한·세션 전용: 데이터가 나갈 곳과 자격 증명을 정한다)
	PermEvalWrite              Permission = "eval:write"               // 평가 세트·문항·버전·기준선·게이트 관리(B46, 관리자)
	PermMCPToolRead            Permission = "mcptool:read"             // 사용자 정의 MCP 도구 정의·버전·시험 결과 조회(B48, 관리자·감사)
	PermMCPToolManage          Permission = "mcptool:manage"           // 사용자 정의 MCP 도구 작성·게시·되돌리기·켜기/끄기·한도·평가 연결·내보내기·가져오기(B48, 관리자, 위험 권한·세션 전용: AI 에 노출되는 데이터 범위를 정한다)
	PermAttendanceSelf         Permission = "attendance:self"          // 본인 휴가 신청·취소·잔여 조회, 체크인·체크아웃, 본인 정정 요청(B50, 전 역할)
	PermAttendanceRead         Permission = "attendance:read"          // 내 잔여 연차·팀 부재 MCP 읽기 도구의 공통 권한(B50, 전 역할. 범위는 서비스가 판정한다)
	PermAttendanceTeam         Permission = "attendance:team"          // 직속 조직(하위 포함) 소속원의 근태·잔여·달력, 정정 승인(B50, 조직장 확인은 서비스가 한다)
	PermAttendanceHR           Permission = "attendance:hr"            // 현재 법인 전사 근태·잔여 열람, 조정·수동 발생·정정·근태 마감·사유 원문 열람(B50, 사용자 정의 역할 "인사 담당"으로 부여)
	PermAttendanceExport       Permission = "attendance:export"        // 월 근태 내보내기 CSV(B50, 인사 담당·관리자)
	PermAttendancePolicy       Permission = "attendance:policy"        // 휴가 종류·연차 규칙·근무 유형 설정(B50, 관리자)
	PermAttendanceManageModule Permission = "attendance:manage_module" // 근태·휴가 모듈 켜기·끄기(B50, 관리자, 위험 권한·세션 전용: 켜면 조직·결재 흐름과 개인정보 처리가 달라진다)
	PermEvalRun                Permission = "eval:run"                 // 평가 세트 실행(B46, 관리자. 외부 공급자는 비용이 든다)
	PermEntityManage           Permission = "entity:manage"            // 법인·사업장 관리(B44, 관리자, 위험 권한·세션 전용: 법인 표와 조직 루트 연결이 모든 업무 데이터의 범위를 정한다)
	PermTaxASPManage           Permission = "taxasp:manage"            // 전자세금계산서 대행사 설정·키·인증서·시험 연결·켜기/끄기(B43, 관리자, 위험 권한·세션 전용: 인증서와 키가 내용이다)
	PermWebhookManage          Permission = "webhook:manage"           // 변경 웹훅 구독 관리·발송 기록·재전송(B42, 관리자, 위험 권한 아님·세션 전용: 값 없는 이벤트 알림만 나간다)
	PermAPIAccess              Permission = "api:access"               // 연동 API(/api/v1) 호출 자격(B42). 어느 역할 템플릿에도 없고 사용자에게 줄 수 없다: API 주체(토큰)에만 붙는 내부 권한
	PermAPIJournalDraft        Permission = "api:journal_draft"        // 연동 API 로 전표 초안만 제출(B42 3단계). 위험 권한이 아니고 승인 권한이 없다. API 주체에만 붙는 내부 권한
	PermSCIMProvision          Permission = "scim:provision"           // SCIM 엔드포인트 호출(B40). 어느 역할 템플릿에도 없고 사용자에게 줄 수 없다: SCIM 주체(토큰)에만 붙는 내부 권한
	PermPrivacyLogRead         Permission = "privacy:log_read"         // 개인정보 조회 기록 조회·무결성 확인·보고서(B41, 감사만, 위험 권한: 원문을 본 사람의 명단. 관리자에게는 주지 않는다)
	PermPrivacyLogManage       Permission = "privacy:log_manage"       // 조회 기록 보관 기간 설정(B41, 관리자, 위험 권한: 기록을 줄이는 권한, 세션으로만)
	PermPrivacyRequestManage   Permission = "privacy:request_manage"   // 정보주체 열람 요청 승인·반려(B41 2단계, 관리자)
	PermSoDRead                Permission = "sod:read"                 // 권한 분리 규칙 표·위반 목록·예외 이력 조회와 위반 알림 수신(B41, 감사·관리자)
	PermSoDManage              Permission = "sod:manage"               // 권한 분리 규칙 모드 변경·예외 요청(B41, 관리자, 위험 권한: 통제를 푸는 권한, 세션으로만)
	PermSoDExceptionApprove    Permission = "sod:exception_approve"    // 권한 분리 예외 승인(B41, 감사, 위험 권한, 세션으로만)
	PermReceiptHandle          Permission = "receipt:handle"           // 수신 접수·지정·처리·반송과 수신 담당자 관리(B23 단계 2, 전 역할 선언 + 서비스가 조직장·담당자인지 판정)
	PermCostRead               Permission = "cost:read"                // 법인 범위 안 전사 AI 비용 집계·환율·청구액·규칙·실행 조회(B49, 관리자·재무·감사). 부서장은 이 권한 없이 서비스가 자기 부서만 판정한다
	PermCostAllocate           Permission = "cost:allocate"            // AI 비용 환율·실제 청구액 입력, 배부 규칙·예산 설정, 월 확정·해제 요청·전표 재생성(B49, 재무만. 관리자는 쓰지 않는다)
)

// AllPermissions 는 Public·Authenticated 를 뺀 실제 권한 목록이다(역할 매핑·프론트 동기화 검사 기준).
var AllPermissions = []Permission{
	PermPurchaseCreate, PermPurchaseRead, PermPurchaseReadAll,
	PermApprovalRead, PermApprovalDecide, PermApprovalDelegate,
	PermInventoryRead, PermInventoryManage,
	PermDocumentVerify, PermDocumentRead, PermDocumentConfirm, PermDocumentUploadImage,
	PermAuditRead, PermAuditVerify, PermEvalRead, PermPolicySearch, PermKnowledgeSearch, PermKnowledgeManage, PermUsageRead,
	PermLLMUsageRead, PermLLMPolicyManage,
	PermTokenManage, PermUserManage, PermMaskBypass,
	PermOrgRead, PermOrgManage, PermOrgHistory,
	PermApprovalReassign, PermNotificationRead, PermNotificationManage,
	PermFormManage, PermDraftCreate, PermDraftRead, PermDocumentReadAll, PermApprovalAgree,
	PermInboxRead, PermReceiptHandle, PermAuthorityManage,
	PermBudgetRead, PermBudgetManage, PermVendorRatingRead,
	PermInventoryClose, PermInventoryValuation, PermReportRead, PermRetentionRead, PermRetentionDispose,
	PermLedgerRead, PermLedgerWrite, PermAccountManage, PermAuthManage,
	PermPaymentRead, PermPaymentWrite, PermFundRead, PermFundManage, PermCardReadAll, PermCardManage, PermCardImport, PermCardExplain, PermCardConfirm, PermCardHolderManage, PermVATRead,
	PermRoleManage, PermFinanceStage, PermCompanyRead, PermCompanyManage,
	PermImportMaster, PermImportBalance, PermImportRead,
	PermOnboardingRead, PermBackupRead,
	PermSCIMManage, PermSCIMRead,
	PermPrivacyLogRead, PermPrivacyLogManage, PermPrivacyRequestManage,
	PermSoDRead, PermSoDManage, PermSoDExceptionApprove, PermAPIManage, PermWebhookManage,
	PermTaxInvoiceRead, PermTaxInvoiceWrite, PermTaxInvoiceIssue, PermTaxASPManage,
	PermEntityManage, PermConsolidatedRead, PermLLMProviderManage, PermEvalWrite, PermEvalRun,
	PermMCPToolRead, PermMCPToolManage,
	PermCostRead, PermCostAllocate,
	PermAttendanceSelf, PermAttendanceRead, PermAttendanceTeam, PermAttendanceHR, PermAttendanceExport, PermAttendancePolicy, PermAttendanceManageModule,
}

// InternalPermissions 는 라우트가 선언할 수 있지만 역할에 담거나 사용자에게 줄 수 없는 내부 권한이다(AllPermissions 와 역할 선택 목록에 없다).
// SCIM 주체처럼 사람이 아닌 호출자의 Principal.Perms 에만 인증 계층이 넣는다.
var InternalPermissions = []Permission{PermSCIMProvision, PermAPIAccess, PermAPIJournalDraft}

// RolePermissions 는 역할 → 권한 표다. 이 표가 웹·MCP 공통의 유일한 정본이다.
// 표에 없는 역할·권한은 거부된다. 핸들러 안에서 역할을 직접 비교하지 말고 Permission 을 선언한다.
var RolePermissions = map[Role][]Permission{
	RoleAdmin: {
		PermCompanyRead, PermCompanyManage,
		PermImportMaster, PermImportRead,
		PermPurchaseRead, PermPurchaseReadAll,
		PermApprovalRead,
		PermInventoryRead, PermInventoryManage,
		PermDocumentRead,
		PermAuditRead, PermEvalRead, PermUsageRead,
		PermLLMUsageRead, PermLLMPolicyManage,
		PermPolicySearch, PermKnowledgeSearch, PermKnowledgeManage,
		PermTokenManage, PermUserManage, PermMaskBypass,
		PermOrgRead, PermOrgManage, PermOrgHistory,
		PermApprovalReassign, PermNotificationRead, PermNotificationManage,
		PermFormManage, PermDraftRead, PermDocumentReadAll, PermInboxRead, PermReceiptHandle, PermAuthorityManage,
		PermBudgetRead, PermVendorRatingRead, PermInventoryValuation,
		PermReportRead,
		PermRetentionRead, PermRetentionDispose,
		PermLedgerRead, PermAccountManage, PermAuthManage,
		PermPaymentRead, PermFundRead, PermFundManage, PermCardReadAll, PermCardHolderManage, PermVATRead,
		PermRoleManage,
		PermOnboardingRead, PermBackupRead,
		PermSCIMManage, PermSCIMRead,
		PermPrivacyLogManage, PermPrivacyRequestManage, PermSoDRead, PermSoDManage, PermAPIManage, PermWebhookManage,
		PermTaxInvoiceRead, PermTaxASPManage,
		PermEntityManage, PermConsolidatedRead, PermLLMProviderManage, PermEvalWrite, PermEvalRun,
		PermMCPToolRead, PermMCPToolManage,
		PermCostRead,
		PermAttendanceSelf, PermAttendanceRead, PermAttendancePolicy, PermAttendanceExport, PermAttendanceManageModule,
	},
	RoleFinance: {
		PermCompanyRead,
		PermImportMaster, PermImportBalance, PermImportRead,
		PermFinanceStage,
		PermPurchaseCreate, PermPurchaseRead, PermPurchaseReadAll,
		PermApprovalRead, PermApprovalDecide, PermApprovalDelegate,
		PermInventoryRead, PermInventoryManage,
		PermDocumentVerify, PermDocumentRead, PermDocumentConfirm, PermDocumentUploadImage,
		PermLLMUsageRead,
		PermPolicySearch, PermKnowledgeSearch,
		PermTokenManage, PermMaskBypass,
		PermOrgRead, PermNotificationRead,
		PermDraftCreate, PermDraftRead, PermApprovalAgree, PermInboxRead, PermReceiptHandle,
		PermBudgetRead, PermBudgetManage, PermVendorRatingRead, PermInventoryClose, PermInventoryValuation,
		PermReportRead,
		PermLedgerRead, PermLedgerWrite, PermAccountManage,
		PermPaymentRead, PermPaymentWrite, PermFundRead, PermFundManage, PermCardReadAll, PermCardManage, PermCardImport, PermCardExplain, PermCardHolderManage, PermVATRead,
		PermTaxInvoiceRead, PermTaxInvoiceWrite, PermTaxInvoiceIssue,
		PermConsolidatedRead,
		PermCostRead, PermCostAllocate,
		PermAttendanceSelf, PermAttendanceRead,
	},
	RoleManager: {
		PermCompanyRead,
		PermPurchaseCreate, PermPurchaseRead,
		PermApprovalRead, PermApprovalDecide, PermApprovalDelegate,
		PermInventoryRead,
		PermDocumentRead,
		PermPolicySearch, PermKnowledgeSearch,
		PermTokenManage,
		PermOrgRead, PermNotificationRead,
		PermDraftCreate, PermDraftRead, PermApprovalAgree, PermInboxRead, PermReceiptHandle,
		PermReportRead,
		PermCardExplain, PermCardConfirm,
		PermAttendanceSelf, PermAttendanceRead, PermAttendanceTeam,
	},
	RoleStaff: {
		PermCompanyRead,
		PermPurchaseCreate, PermPurchaseRead,
		PermApprovalRead,
		PermInventoryRead,
		PermDocumentVerify, PermDocumentRead,
		PermPolicySearch, PermKnowledgeSearch,
		PermTokenManage,
		PermOrgRead, PermNotificationRead,
		PermDraftCreate, PermDraftRead, PermApprovalAgree, PermInboxRead, PermReceiptHandle,
		PermCardExplain,
		PermAttendanceSelf, PermAttendanceRead,
	},
	RoleAuditor: {
		PermBackupRead, PermConsolidatedRead, PermSCIMRead, PermPrivacyLogRead, PermSoDRead, PermSoDExceptionApprove,
		PermCompanyRead,
		PermImportRead,
		PermPurchaseRead, PermPurchaseReadAll,
		PermApprovalRead,
		PermInventoryRead,
		PermDocumentRead,
		PermAuditRead, PermAuditVerify, PermEvalRead, PermMCPToolRead, PermUsageRead,
		PermLLMUsageRead,
		PermPolicySearch, PermKnowledgeSearch,
		PermTokenManage,
		PermOrgRead, PermOrgHistory, PermNotificationRead,
		PermDraftRead, PermDocumentReadAll, PermInboxRead,
		PermBudgetRead, PermVendorRatingRead, PermInventoryValuation,
		PermReportRead,
		PermRetentionRead,
		PermLedgerRead,
		PermPaymentRead, PermFundRead, PermCardReadAll, PermVATRead,
		PermTaxInvoiceRead,
		PermCostRead,
		PermAttendanceSelf, PermAttendanceRead,
	},
}

// RoleHas 는 역할이 권한을 가졌는지 본다. 알 수 없는 역할·권한은 false.
func RoleHas(r Role, perm Permission) bool {
	for _, p := range RolePermissions[r] {
		if p == perm {
			return true
		}
	}
	return false
}

// IsKnown 은 선언 가능한 권한인지 본다(빈 문자열·오타 방지).
func (p Permission) IsKnown() bool {
	if p == PermPublic || p == PermAuthenticated || p == PermSetup {
		return true
	}
	for _, k := range AllPermissions {
		if k == p {
			return true
		}
	}
	for _, k := range InternalPermissions {
		if k == p {
			return true
		}
	}
	return false
}

// MCP 도구 이름. spec 2절 "AI 연결" 의 도구 7개와 B7 의 2개.
const (
	ToolSearchItems          = "search_items"
	ToolGetStock             = "get_stock"
	ToolListPendingApprovals = "list_pending_approvals"
	ToolGetPurchaseRequest   = "get_purchase_request"
	ToolCreatePRDraft        = "create_purchase_request_draft"
	ToolApproveOrReject      = "approve_or_reject"
	ToolVerifyDocument       = "verify_document"
	ToolListStockAlerts      = "list_stock_alerts"      // B7: 안전재고 미달·유효기한 임박 조회
	ToolDraftReorder         = "draft_reorder"          // B7: 미달 품목 구매요청 초안(임시저장까지만)
	ToolSearchPolicy         = "search_policy"          // B12: 사내 규정 조항 검색(열람 범위 적용)
	ToolSearchKnowledge      = "search_knowledge"       // B47: 사내 지식(완료된 결재 문서·첨부·매뉴얼·규정) 내용 검색. 마스킹된 발췌·출처만, 열람 범위는 웹과 같은 서비스(읽기 전용)
	ToolListApprovalForms    = "list_approval_forms"    // B23 ⑧: 쓸 수 있는 결재 양식과 입력 칸 조회(읽기 전용)
	ToolGetApprovalDoc       = "get_approval_doc"       // B23 ⑧: 양식 문서 한 건 조회(열람 범위·민감 필드 마스킹, 읽기 전용)
	ToolGetPayables          = "get_payables"           // B32: 거래처별 매입 채무 조회(ledger:read, 계좌번호 없음, 읽기 전용)
	ToolGetTaxInvoices       = "get_tax_invoices"       // B43: 세금계산서 조회(taxinvoice:read, 사업자번호 가림·이메일·주소·대표자·품목 이름 없음, 읽기 전용)
	ToolGetCardUnreconciled  = "get_card_unreconciled"  // B32: 본인 소지 카드의 미소명 내역 조회(카드번호 없음, 읽기 전용)
	ToolGetMonthCloseStatus  = "get_month_close_status" // B27: 재고 월 마감 상태 요약(금액은 서비스가 권한으로 거름, 읽기 전용)
	ToolGetAICostSummary     = "get_ai_cost_summary"    // B49: 부서 월 AI 비용 요약(본인 부서 기본, 숫자만, 읽기 전용)
	ToolGetBudgetStatus      = "get_budget_status"      // B25: 부서 월 예산 잔액 조회(본인 부서 기본, 읽기 전용)
	ToolGetItemLedger        = "get_item_ledger"        // B24: 품목 수불부 조회(처리자·금액 없음, 읽기 전용)
	ToolWhoApproves          = "who_approves"           // B22: 결재선 미리보기(구매 품의서·양식 문서, 읽기 전용)
	ToolSearchPeople         = "search_people"          // B22: 사람 검색(연락처·사번 없음, 퇴사자 없음, 읽기 전용)
	ToolGetOrgUnit           = "get_org_unit"           // B22: 조직 경로·조직장·인원 조회(읽기 전용)
	ToolListMyApprovalDocs   = "list_my_approval_docs"  // B23 ⑧: 내 문서함 7종 목록·건수 조회(읽음 표시 불변, 읽기 전용)
	ToolGetMyLeaveBalance    = "get_my_leave_balance"   // B50: 내 잔여 연차·대체휴가(본인만, 사유·증빙 없음, 모듈이 꺼지면 목록에서 빠짐, 읽기 전용)
	ToolListTeamAbsence      = "list_team_absence"      // B50: 팀 부재 일정(웹 달력과 같은 열람 범위, "휴가"/"부재"만, 사유·병가 구분 없음, 읽기 전용)
)

// MCPToolPermissions 는 MCP 도구 → 필요한 권한이다. 웹과 같은 권한 표(RolePermissions)로 판정한다.
// ai 의 도구 등록은 이 표에 없는 도구를 만들 수 없고, 표의 도구가 빠지면 누락 검사가 실패한다.
var MCPToolPermissions = map[string]Permission{
	ToolSearchItems:          PermInventoryRead,
	ToolGetStock:             PermInventoryRead,
	ToolListPendingApprovals: PermApprovalRead,
	ToolGetPurchaseRequest:   PermPurchaseRead,
	ToolCreatePRDraft:        PermPurchaseCreate,
	ToolApproveOrReject:      PermApprovalDecide,
	ToolVerifyDocument:       PermDocumentVerify,
	ToolListStockAlerts:      PermInventoryRead,
	ToolDraftReorder:         PermPurchaseCreate,
	ToolSearchPolicy:         PermPolicySearch,
	ToolSearchKnowledge:      PermKnowledgeSearch,
	ToolListApprovalForms:    PermDraftCreate, // FormService.ListUsable 이 요구하는 권한과 같다(양식 목록은 기안자용)
	ToolGetApprovalDoc:       PermDraftRead,   // 열람 범위는 서비스가 판정한다
	ToolGetPayables:          PermLedgerRead,
	ToolGetTaxInvoices:       PermTaxInvoiceRead, // 열람 판정은 서비스(TaxInvoiceService.List)
	ToolGetCardUnreconciled:  PermCardExplain,    // 본인 소지분·card:explain 판정은 서비스(권한이 없으면 거절 + denied 감사)
	ToolGetMonthCloseStatus:  PermInventoryRead,  // 기말 금액은 서비스가 inventory:valuation_read 로 거른다
	ToolGetBudgetStatus:      PermPurchaseRead,   // 부서·권한 판정은 서비스(타 부서는 budget:read)
	ToolGetAICostSummary:     PermReportRead,     // 본인 부서 기본, 타 부서는 서비스가 cost:read 로 판정
	ToolGetItemLedger:        PermInventoryRead,
	ToolWhoApproves:          PermApprovalRead, // 범위 판정은 서비스(타인 결재선은 열람 범위 안만)
	ToolSearchPeople:         PermOrgRead,      // 조직도는 사내 전원이 본다(B20 6절)
	ToolGetOrgUnit:           PermOrgRead,
	ToolListMyApprovalDocs:   PermInboxRead,      // 문서함 열람 조건은 문서 조회와 같은 ViewScope 한 곳
	ToolGetMyLeaveBalance:    PermAttendanceRead, // 본인 잔여만. 모듈이 꺼져 있으면 서비스가 없는 것처럼 답한다
	ToolListTeamAbsence:      PermAttendanceRead, // 범위(본인 조직·조직장 하위·인사 담당 법인 전체)는 서비스의 attendanceViewScope 한 곳
}

// HighRiskPermissions 는 부여에 2인 확인·경고가 붙는 위험 권한이다(B33 명세 9절). 새 권한을 추가할 때 위험 여부를 이 목록으로 분류한다.
var HighRiskPermissions = []Permission{
	PermRoleManage, PermAuthManage, PermAuditVerify, PermUserManage, PermMaskBypass,
	PermRetentionDispose, PermLLMPolicyManage, PermLedgerWrite, PermPaymentWrite, PermSCIMManage,
	PermPrivacyLogRead, PermPrivacyLogManage, PermSoDManage, PermSoDExceptionApprove, PermAPIManage, PermTaxASPManage,
	PermEntityManage, PermLLMProviderManage, PermMCPToolManage, PermAttendanceManageModule,
}
