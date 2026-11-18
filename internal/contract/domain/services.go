package domain

import (
	"context"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
)

// ---- 입력 타입 ----

// PRLineInput 은 구매요청 줄 입력이다. 금액은 서비스가 수량×단가로 계산한다.
type PRLineInput struct {
	ItemID    int64
	Quantity  int64
	UnitPrice int64
}

// DraftInput 은 구매요청 임시저장 입력이다.
type DraftInput struct {
	VendorID int64
	Title    string
	Reason   string
	Lines    []PRLineInput
	Edits    []LineEdit // B23: 기안자가 더한 참조·수신(구매 품의서는 이 두 역할만)
	Urgent   bool       // B23: 긴급 표시
	// B25: 희망 납기(YYYY-MM-DD, 선택)와 납품처(비면 "본사 창고").
	DueDate       string
	DeliveryPlace string
}

// PRFilter 는 구매요청 목록 조건이다. 부서 격리는 서비스가 적용한다
// (purchase:read_all 이 없으면 호출자 부서로 강제).
type PRFilter struct {
	Status       PRStatus
	DepartmentID int64
	RequesterID  int64
	Limit        int
	AfterID      int64 // 호환용 커서. 화면의 쪽 이동은 Offset 이다
	Offset       int   // 건너뛸 건수(쪽 이동)
	// B28: 작성일(KST) 범위(YYYY-MM-DD, 비면 제한 없음)와 제목 검색어. 화면 목록과 내보내기가 같이 쓴다.
	From, To string
	Query    string
	// 정렬: Sort 는 "created_at"|"total_amount"(비면 id 오름차순 = 예전 동작), Order 는 "asc"|"desc"(비면 asc).
	// 허용 값 판정은 서비스가 한다(그 밖은 contract.ErrValidation). AfterID 커서와 같이 쓰지 않는다.
	Sort, Order string
}

// ReorderInput 은 재고 경보 품목으로 만드는 구매요청 초안 입력이다(B7).
// 수량은 서비스가 안전재고 부족분(안전재고-현재고, 최소 1)으로 정하고, 단가는 그 품목의 가장 최근 발주 단가를
// 쓴다(발주 이력이 없으면 0, 검토자가 채운다). 임시저장까지만 하며 상신은 사람이 한다.
type ReorderInput struct {
	VendorID int64
	ItemIDs  []int64 // 안전재고 미달 품목만 허용한다. 아니면 contract.ErrValidation
	Reason   string  // 비어 있으면 "안전재고 미달 재주문"
}

// Decision 은 결재 결정이다. 반려는 Comment(사유) 필수.
type Decision struct {
	Approve bool
	Comment string
	// OnBehalfOfID 가 0 이 아니면 그 사용자(위임자)를 대신한 결재다. 유효한 위임이 있어야 하고,
	// 위임자가 요청자인 건은 자기 결재 금지로 거부된다(ErrSelfApproval).
	OnBehalfOfID int64
}

// DecisionResult 는 결재 결과다. 마지막 단계 승인이면 PurchaseOrder 가 채워진다.
type DecisionResult struct {
	Request       PurchaseRequest
	PurchaseOrder *PurchaseOrder
}

// ReceiveInput 은 PO 기준 입고 입력이다. PO 잔량을 넘으면 contract.ErrOverReceipt.
type ReceiveInput struct {
	POID        int64
	Lines       []ReceiptLine
	WarehouseID int64 // B24: 입고 창고. 0 이면 기본 창고(본사 창고)
}

// POFilter 는 발주 목록 조건이다.
type POFilter struct {
	Status POStatus
	Limit  int
	Offset int // 건너뛸 건수(쪽 이동)
}

// VerifyInput 은 증빙 판정 요청이다. Text 가 있으면 PDF 텍스트, Image 가 있으면 이미지 증빙.
type VerifyInput struct {
	Filename    string
	ContentType string
	Text        string
	Image       []byte
	POID        int64 // 대조할 발주(필수)
	ReceiptID   int64 // 0 이면 PO 와만 대조
}

// ConfirmInput 은 최종 확정 입력이다.
type ConfirmInput struct {
	Outcome Outcome
	Comment string
}

// ---- 서비스 인터페이스 ----

// PurchaseService 는 구매요청 서비스다.
// 구현: erp (internal/service/purchase*). 쓰는 쪽: erp 핸들러, ai MCP 도구
// (get_purchase_request, create_purchase_request_draft). 임시저장까지만 MCP 에서 허용한다.
type PurchaseService interface {
	CreateDraft(ctx context.Context, in DraftInput) (PurchaseRequest, error) // purchase:create
	UpdateDraft(ctx context.Context, id int64, in DraftInput) (PurchaseRequest, error)
	Submit(ctx context.Context, id int64) (PurchaseRequest, error) // 상신·재상신. 결재선 생성
	Get(ctx context.Context, id int64) (PurchaseRequest, error)    // purchase:read + 부서 격리
	List(ctx context.Context, f PRFilter) ([]PurchaseRequest, error)
	// DraftReorder 는 안전재고 미달 품목으로 구매요청 임시저장 한 건을 만든다(B7). purchase:create + inventory:read,
	// 감사 동작 purchase_request.create(diff 에 source=reorder). MCP 도구 draft_reorder 가 쓴다.
	DraftReorder(ctx context.Context, in ReorderInput) (PurchaseRequest, error)
}

// ApprovalService 는 결재 서비스다.
// 구현: erp (internal/service/approval*). 쓰는 쪽: erp 핸들러, ai MCP 도구
// (list_pending_approvals, approve_or_reject). 자기 결재 금지는 여기서 강제한다.
type ApprovalService interface {
	Agree(ctx context.Context, prID int64, d Decision) (DecisionResult, error) // approval:agree, 합의 단계 동의·이견(B23)
	// DecideBatch 는 내 차례인 건을 한꺼번에 승인·동의한다(최대 50건, 승인만). 건마다 권한·상태 전이·감사가 따로이고 한 건의 실패가 다른 건을 막지 않는다(B23 단계 2).
	DecideBatch(ctx context.Context, items []BatchItem, comment string) ([]BatchResult, error)
	ListPending(ctx context.Context) ([]PurchaseRequest, error)                 // approval:read, 내 차례 건
	Decide(ctx context.Context, prID int64, d Decision) (DecisionResult, error) // approval:decide
	History(ctx context.Context, prID int64) ([]ApprovalRecord, error)          // 불변 이력
}

// DelegationInput 은 위임 지정 입력이다. 위임자는 항상 호출한 본인이다.
type DelegationInput struct {
	DelegateID int64
	From, To   time.Time
	// B29: Kind 가 비면 위임(delegation, 기존 동작)이고 "absence" 면 부재(사유 필수·90일 상한·겹침 검사·알림).
	Kind   string
	Reason string
	Note   string
}

// DelegationService 는 결재 위임 서비스다(B3). 전 메서드 approval:delegate.
// 구현: erp (internal/service/delegation*). 지정·해제는 감사 로그(delegation.create/revoke)에 남는다.
type DelegationService interface {
	Create(ctx context.Context, in DelegationInput) (Delegation, error)
	Revoke(ctx context.Context, id int64) error
	ListMine(ctx context.Context) (given, received []Delegation, err error)
	Candidates(ctx context.Context) ([]DelegateCandidate, error)
}

// InventoryService 는 품목·재고·발주·입고 서비스다.
// 구현: erp (internal/service/inventory*). 쓰는 쪽: erp 핸들러, ai MCP 도구(search_items, get_stock)
// 와 docverify(PO·입고 조회).
type InventoryService interface {
	SearchItems(ctx context.Context, query string, limit int) ([]Item, error) // inventory:read
	GetStock(ctx context.Context, itemID int64) (ItemStock, error)
	ListBelowSafety(ctx context.Context) ([]ItemStock, error)
	ListExpiring(ctx context.Context, withinDays int) ([]ExpiringLot, error)
	GetPurchaseOrder(ctx context.Context, id int64) (PurchaseOrder, error)
	ListPurchaseOrders(ctx context.Context, f POFilter) ([]PurchaseOrder, error)
	GetReceipt(ctx context.Context, id int64) (Receipt, error)
	Receive(ctx context.Context, in ReceiveInput) (Receipt, error) // inventory:manage
}

// DocumentService 는 증빙 문서 판정 서비스다.
// 구현: ai (internal/docverify). 쓰는 쪽: ai 핸들러·MCP 도구(verify_document).
// 추출은 llm.Client 경유, 대조 규칙은 결정적 코드. 조회는 InventoryService 를 사용한다.
type DocumentService interface {
	Verify(ctx context.Context, in VerifyInput) (Verdict, error)                    // document:verify
	GetVerdict(ctx context.Context, id int64) (Verdict, error)                      // document:read
	Confirm(ctx context.Context, verdictID int64, in ConfirmInput) (Verdict, error) // document:confirm
}

// PolicyHit 는 사내 규정 검색 결과 한 조항이다(B12). Text 는 조항 원문이고 Source 는 출처 표기다.
// 예: Source "구매 결재 규정 제3조(금액 구간별 결재선)".
type PolicyHit struct {
	DocSlug  string
	DocTitle string
	ClauseNo string
	Heading  string
	Text     string
	Source   string
	Score    int
	// Reason 은 작성 화면용 관련 조항(Related)에서만 채운다: "tier"(금액 구간 결재선) | "item"(품목 이름이 본문과 맞은 조항). 검색 결과는 비어 있다.
	Reason string
}

// PolicyRelatedInput 은 구매요청 작성 화면이 관련 조항을 찾는 입력이다. Items 는 품목명 목록.
type PolicyRelatedInput struct {
	Amount int64
	Items  []string
}

// PolicyService 는 사내 규정 검색 서비스다(B12). 전 메서드 policy:search.
// 구현: ai (internal/policysearch). 문서마다 열람 범위(역할·부서)가 있고, 호출자가 볼 수 없는 문서는
// 결과에 나오지 않는다. 검색은 감사 로그(policy.search)에 남는다. 쓰는 쪽: ai MCP 도구(search_policy),
// erp 구매요청 작성 화면 API(GET /api/policies/search).
type PolicyService interface {
	Search(ctx context.Context, query string, limit int) ([]PolicyHit, error)
	Related(ctx context.Context, in PolicyRelatedInput) ([]PolicyHit, error)
	// ClauseBy 는 문서 slug 와 조항 번호로 조항 한 건을 읽는다(B23 단계 3 전결 근거 조항 표시). 호출자가 볼 수 있는 문서만이고,
	// 열람 범위 밖이거나 조항이 없으면 같은 contract.ErrNotFound 다(존재 여부를 드러내지 않는다). domain.ClauseReader 와 같은 모양이다.
	ClauseBy(ctx context.Context, slug, no string) (PolicyHit, error)
}

// ---- 조직도 기반 결재선(B21, 8절) ----

// LinePreviewInput 은 결재선 미리보기 입력이다.
type LinePreviewInput struct {
	RequesterID int64        // 0 이면 호출자 본인
	Amount      int64        // 원, 1 이상
	AsOf        contract.Day // 비면 오늘(KST)
	// B23/B22: 양식 문서의 결재선도 같은 입구로 묻는다. FormCode 가 비었거나 PUR 이면 구매 품의서(기존 동작 그대로)다.
	// 양식 문서는 호출자 본인의 결재선만 물을 수 있고(RequesterID 는 0 또는 본인) 권한은 draft:create 다. 도구는 Fields·Edits 를 채우지 않는다.
	FormCode  string
	FieldUnit int64          // 협조 요청 부서 등 field_unit 소스가 쓰는 조직 번호
	Fields    map[string]any // 양식 필드 값(금액 필드·협조 부서 등)
	Edits     []LineEdit     // 기안자 편집(결재·합의·참조·수신)
}

// LineStep 은 만들어진(또는 만들 수 없는) 결재선의 한 단계다.
type LineStep struct {
	Order        int      // 1부터. 생략된 단계(Omitted)는 0
	Kind         StepKind // 화면은 한국어 라벨(Title)을 쓴다
	Title        string   // 한국어 직책 이름: 팀장·부서장·본부장·대표·재무
	UnitID       int64    // 결재자가 조직장인 조직(재무 단계는 0)
	UnitName     string
	UnitPath     string
	ApproverID   int64 // 0 = 못 찾음 또는 재무 풀
	ApproverName string
	Acting       bool   // 직무대행자
	RaisedFrom   string // 올림이 일어났다면 원래 후보 조직 이름, 아니면 빈 값
	RaiseReason  string // 요청자 본인·공석·앞 단계와 같은 사람·재직 중이 아님
	RaisedNote   string // 올림 문장
	LayerNote    string // 층 없음 비고
	Omitted      bool   // 더 올라갈 곳이 없어 생략된 단계
	Missing      bool   // 못 찾아서 비어 있음
	Reason       string // Missing·Omitted 일 때 한국어 한 문장
	PoolSize     int    // 재무 단계의 후보 수
	// B29: 결재자가 지금 부재 중이면 부재 종료 시각(KST 표시용)과 대결자 이름. 결재선(배정자)은 바뀌지 않는다.
	AbsentUntil     *time.Time
	AbsenceDelegate string
	// B23: 역할(approve·agree), 단계 묶음(같은 번호의 합의는 병렬), 필수 단계 여부(기안자가 못 뺀다).
	Role     string
	Stage    int
	Required bool
	// DefIndex 는 양식 기본 결재선의 몇 번째 단계(1부터)인지다. 기안자가 더한 단계는 0. free 양식에서 필수가 아닌 기본 단계를 뺄 때 이 번호를 보낸다.
	DefIndex int
	// B25: 예산 초과로 강제로 붙은 재무 단계(미리보기에 "예산 초과로 추가" 표시).
	Forced bool
	// B23 단계 3: 전결 규정으로 건너뛴 단계(Skipped)와 사유 문구("전결").
	Skipped  bool
	SkipNote string
}

// LineRecipient 는 미리보기의 참조·수신 대상 한 줄이다.
type LineRecipient struct {
	Kind       string // cc | receive
	TargetType string // user | unit
	TargetID   int64
	Name       string
	Path       string
	Note       string // 자동 제외·확장 비고
}

// LinePreview 는 결재선 미리보기 결과다. 조직 사정으로 만들 수 없으면 오류가 아니라 Complete=false 와 Problems 로 돌려준다.
type LinePreview struct {
	RequesterID   int64
	RequesterName string
	UnitPath      string // 요청자 주소속 경로 문자열(조직 미등록이면 "")
	AsOf          contract.Day
	Steps         []LineStep
	Complete      bool            // 상신할 수 있는가
	Problems      []string        // Complete=false 일 때 사유 문장들
	Notes         []string        // 올림·생략·층 없음 비고
	Recipients    []LineRecipient // B23: 참조·수신
	Authority     *AuthorityNote  // B23 단계 3: 적용한 전결 규정(없으면 nil)
}

// OrphanStep 은 결재 진행 중 건에서 후보가 한 명도 없는 단계다(결재자 소멸 목록).
type OrphanStep struct {
	PRID, RequesterID int64
	RequesterName     string // 요청자(기안자) 이름(소멸 목록의 기안자 열)
	PRTitle           string
	Round, Order      int
	Title             string // 단계의 직책 이름
	ApproverID        int64  // 소멸한 배정자
	ApproverName      string // 상신 당시 이름
	Reason            string // "퇴사"·"결재 권한 상실"
	SinceDay          contract.Day
	// 결재자 재지정(ReassignService)으로 이어지는 값.
	Seq          int
	StepKind     StepKind
	Reassignable bool   // 재무 단계는 false
	Role         string // approve | agree(B23). 합의 단계도 같은 재지정 대화상자로 바꾼다
	FormCode     string // 양식 코드(PUR·GEN·EXP·COOP …, B22 소멸 목록의 "양식" 열)
	FormName     string // 양식 이름
	FormKind     string // purchase | general(B23 단계 2): 구매 품의서는 /purchase-requests/{id}, 그 밖은 /docs/{id} 로 연결한다
	Stage        int    // 같은 차수 안의 단계 묶음(병렬 합의면 같은 값이 여럿)
}

// LineService 는 조직도로 결재선을 만들어 보여 주는 읽기 전용 서비스다(쓰기·감사 없음). 권한 approval:read.
// 구현: erp (internal/service/approval_line*). 쓰는 쪽: erp 핸들러(상신 화면 미리보기), ai MCP 도구 who_approves(B22).
type LineService interface {
	// PreviewLine 은 요청자와 금액으로 결재선을 만든다(저장하지 않는다). 상신이 쓰는 BuildLine 과 같은 함수다.
	// 본인(RequesterID 0 포함) 것은 항상, 타인은 요청자의 귀속 조직이 호출자의 열람 범위일 때만 가능하고 아니면 ErrForbidden.
	PreviewLine(ctx context.Context, in LinePreviewInput) (LinePreview, error)
	// ListOrphanedSteps 는 결재 진행 중 건의 결재 전 단계 중 후보가 없는 단계다. 권한 org:manage, 읽기 전용.
	ListOrphanedSteps(ctx context.Context, asOf contract.Day) ([]OrphanStep, error)
}

// ---- 관리자 결재자 재지정(B21 3.8) ----

// ReassignInput 은 결재자 재지정 입력이다.
type ReassignInput struct {
	UserID        int64
	Cause         string // "leave" | "transfer" | "absence" | "other"
	Reason        string // 필수, 500자 이내
	ConfirmActive bool   // 현재 결재자가 재직 중이고 결재할 수 있을 때 필요하다
}

// ReassignCandidate 는 새 결재자로 지정할 수 있는 사람이다(연락처 없음).
type ReassignCandidate struct {
	UserID   int64
	Name     string
	Title    string // 가장 높은 조직장 직책 이름
	Acting   bool   // 직무대행 자격으로 후보가 된 경우
	UnitName string
	UnitPath string
}

// ReassignService 는 관리자 결재자 재지정이다. 권한 approval:reassign(admin). MCP 에 노출하지 않는다.
type ReassignService interface {
	// ReassignStep 은 건 prID 의 현재 차수에서 단계 seq 의 결재자를 바꾼다.
	// 감사 approval.reassign, 결재 이력 reassign, 알림 생성이 모두 같은 트랜잭션이다.
	ReassignStep(ctx context.Context, prID int64, seq int, in ReassignInput) (PurchaseRequest, error)
	// ReassignCandidates 는 새 결재자 자격을 만족하는 사람이다. text 는 이름·조직명 부분 일치, 최대 50명.
	ReassignCandidates(ctx context.Context, prID int64, seq int, text string) ([]ReassignCandidate, error)
}

// ---- 앱 안 알림(B21 3.9) ----

// Notification 은 내 알림 한 건이다. Kind 는 열린 문자열이고 화면에 보이지 않는다.
type Notification struct {
	ID        int64
	Kind      string
	Title     string
	Body      string
	Link      string // 앱 안 경로
	CreatedAt time.Time
	ReadAt    *time.Time
}

// NotificationFilter 는 알림 목록 조건이다. Limit 기본 50·최대 100, BeforeID 가 0 이 아니면 그보다 오래된 것부터.
type NotificationFilter struct {
	UnreadOnly bool
	Limit      int
	BeforeID   int64
}

// NotificationService 는 내 알림 조회·읽음 처리다. 호출자 본인 것만 다룬다. 권한 notification:read(전 역할).
type NotificationService interface {
	List(ctx context.Context, f NotificationFilter) ([]Notification, error)
	UnreadCount(ctx context.Context) (int, error)
	MarkRead(ctx context.Context, id int64) error // 남의 알림·없는 번호는 ErrNotFound
	MarkAllRead(ctx context.Context) (int, error) // 읽음 처리한 건수
}
