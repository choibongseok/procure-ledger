// Package domain 은 erp(업무)·ai(AI)가 함께 쓰는 도메인 타입과 서비스 인터페이스다.
// (계약). 인터페이스마다 구현 담당 영역을 주석에 적는다.
//
// 규약
//   - 금액은 원 단위 int64, 수량은 정수. 시각은 UTC time.Time, 날짜는 "2006-01-02" 문자열.
//   - 서비스 메서드는 context 의 contract.Principal 로 호출자를 안다. 권한 검사(contract.Authorize)와
//     부서 격리는 서비스가 한다. 웹 핸들러와 MCP 도구는 같은 서비스를 호출한다(우회 경로 금지).
//   - 쓰기 메서드는 비즈니스 변경과 audit.Recorder.Record 를 한 트랜잭션(contract.TxManager)에서 한다.
package domain

import (
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
)

// ---- 조직·거래처 ----

// Department 는 부서다.
type Department struct {
	ID   int64
	Name string
}

// Vendor 는 거래처다. 사업자번호·계좌·연락처는 구분 필드로 저장하고 응답에서 contract.Masker 로 가린다.
type Vendor struct {
	ID         int64
	Name       string
	BusinessNo string
	AccountNo  string
	Phone      string
	Email      string
}

// ---- 품목·재고 ----

// Item 은 품목 마스터다.
type Item struct {
	ID          int64
	SKU         string
	Name        string
	Unit        string
	SafetyStock int64
	TrackLot    bool
}

// LotStock 은 로트 단위 재고다.
type LotStock struct {
	LotID      int64
	LotNo      string
	ExpiryDate string // 없으면 ""
	Quantity   int64
}

// ItemStock 은 품목의 현재 재고다. 수량은 원장(ledger) 합으로만 계산한다. 수량 직접 수정 API 는 없다.
type ItemStock struct {
	Item        Item
	OnHand      int64
	Lots        []LotStock
	BelowSafety bool
	// ByWarehouse 는 창고별 재고(B24, 추가만). 재고가 없는 창고는 빠진다. OnHand 는 모든 창고 합계다.
	ByWarehouse []WarehouseQty
	// WarehouseLots 는 창고·로트별 재고 줄(창고 선택 시 그 창고만, 로트 표의 창고 열용, 추가만). 로트 없는 품목은 LotID 0 한 줄이다.
	WarehouseLots []WarehouseLot
	// B27: 평균 단가·재고 금액(잔액 금액). inventory:valuation_read 가 있고 평가가 있을 때만 채운다(HasValuation).
	AvgCost      int64
	StockAmount  int64
	HasValuation bool
	// 전체 모드(그룹 범위, B51 D14)에서는 법인마다 단가가 달라 합산하지 않고 법인별로 나눈다(AvgCost·StockAmount·HasValuation 은 비운다).
	ValuationByEntity []EntityValuation
}

// EntityValuation 은 한 법인의 품목 평균 단가·재고 금액이다(전체 모드).
type EntityValuation struct {
	EntityID    int64
	EntityName  string
	AvgCost     int64
	StockAmount int64
}

// WarehouseLot 은 한 창고의 한 로트 재고다.
type WarehouseLot struct {
	WarehouseID   int64
	WarehouseName string
	LotID         int64
	LotNo         string
	ExpiryDate    string
	Quantity      int64
}

// ExpiringLot 은 유효기한 임박 로트다.
type ExpiringLot struct {
	Item         Item
	Lot          LotStock
	DaysToExpire int
}

// ---- 구매요청·결재 ----

// PRStatus 는 구매요청 상태다.
type PRStatus string

const (
	PRDraft     PRStatus = "draft"     // 임시저장
	PRSubmitted PRStatus = "submitted" // 상신
	PRInReview  PRStatus = "in_review" // 결재중
	PRRejected  PRStatus = "rejected"  // 반려
	PRApproved  PRStatus = "approved"  // 승인
	PROrdered   PRStatus = "ordered"   // 발주완료
)

// StepKind 는 결재 단계 종류다. 금액 구간별로 선이 달라진다(approval_line_rules).
type StepKind string

const (
	StepTeamLead     StepKind = "team_lead"     // 팀장(층 1)
	StepDeptHead     StepKind = "dept_head"     // 부서장(층 2)
	StepDivisionHead StepKind = "division_head" // 본부장(층 3, B21)
	StepCEO          StepKind = "ceo"           // 대표(층 4, B21)
	StepFinance      StepKind = "finance"       // 재무(역할 풀)
)

// StepStatus 는 결재 단계 상태다.
type StepStatus string

const (
	StepWaiting  StepStatus = "waiting" // 앞 단계 대기
	StepPending  StepStatus = "pending" // 지금 결재할 차례
	StepApproved StepStatus = "approved"
	StepRejected StepStatus = "rejected"
	StepAgreed   StepStatus = "agreed"   // 합의 동의(B23)
	StepObjected StepStatus = "objected" // 합의 이견(B23)
	StepSkipped  StepStatus = "skipped"  // 반려·이견으로 처리하지 않고 건너뜀(B23)
)

// 결재선 단계 역할(B23). 참조·수신은 단계가 아니라 대상 목록(DocRecipient)이다.
const (
	RoleApprove = "approve"
	RoleAgree   = "agree"
)

// DocRecipient 는 상신 때 고정된 참조·수신 대상이다.
type DocRecipient struct {
	Kind       string // cc | receive
	TargetType string // user | unit
	TargetID   int64
	Name       string
	Path       string
	Note       string
}

// PRLine 은 구매요청 품목 줄이다.
type PRLine struct {
	ID        int64
	ItemID    int64
	ItemName  string
	Quantity  int64
	UnitPrice int64
	Amount    int64
}

// ApprovalStep 은 결재선의 한 단계다.
type ApprovalStep struct {
	Seq        int
	Kind       StepKind
	ApproverID int64 // 결재 전에는 0 일 수 있다
	Status     StepStatus
	Comment    string
	DecidedAt  *time.Time
	// B21: 상신 시점 스냅샷과 현재 결재자. 도입 이전 건은 비어 있다.
	UnitID         int64  // 결재자가 조직장인 조직(재무 단계는 0)
	UnitPath       string // 조직 경로 문자열("구매·물류본부 > 구매팀")
	Title          string // 직책 이름(팀장·부서장·본부장·대표·재무)
	AssigneeID     int64  // 현재 결재자(재지정 반영). 재무 단계·도입 이전 다중 후보 단계는 0
	AssigneeName   string // 현재 결재자 이름(상신·재지정 당시 문자열)
	Acting         bool   // 직무대행자가 배정됨
	RaisedNote     string // 올림 사유 문장
	LayerNote      string // 층 없음 비고
	ReassignedFrom string // 재지정되었다면 직전 결재자 이름
	HeadChangedTo  string // 현재 조직장이 상신 당시와 다르면 현재 조직장 이름("공석" 포함)
	// B23: 역할(approve·agree), 단계 묶음(같은 번호의 합의는 병렬), 필수 여부.
	Role     string
	Stage    int
	Required bool
	// B23 단계 3: 전결 최종 결재 단계 표시와 건너뜀 사유("전결").
	FinalAuthority bool
	SkipReason     string
	// B29: 대결로 처리된 단계면 원결재자(OnBehalfOf*)와 실제 처리자 이름. 본인이 처리했거나 아직 처리 전이면 비어 있다.
	OnBehalfOfID   int64
	OnBehalfOfName string
	ApproverName   string
}

// PurchaseRequest 는 구매요청(PR)이다.
type PurchaseRequest struct {
	ID              int64
	No              string
	RequesterID     int64
	RequesterName   string
	DepartmentID    int64
	VendorID        int64
	Title           string
	Reason          string
	Status          PRStatus
	TotalAmount     int64
	RejectReason    string
	Lines           []PRLine
	Steps           []ApprovalStep
	PurchaseOrderID int64 // 발주 전에는 0
	CreatedAt       time.Time
	UpdatedAt       time.Time
	// 위임 결재 대기 건 표시용. ApprovalService.ListPending 결과에서만 채운다(0 이면 본인 차례).
	DelegatedFromID   int64
	DelegatedFromName string
	Round             int // 현재 상신 차수(1부터)
	// B21: 귀속 조직(상신 시점 고정)과 상신 당시 조직 스냅샷. 도입 이전 건은 비어 있다.
	OwnerUnitID       int64
	RequesterUnitPath string // 상신 당시 조직 경로 문자열(없으면 "")
	LineAsOf          string // 결재선 기준일 "2006-01-02"(없으면 "")
	// B23: 문서 양식. 구매요청은 "구매 품의서"(PUR) 양식의 문서다. 결재선 금액(Amount)은 구매 품의서에서 합계 금액과 같다.
	FormCode string
	FormName string
	Amount   int64
	// B23 문서 확장(구매 품의서가 아닌 양식 문서에서 채운다).
	FormKind       string // purchase | general
	FormID         int64
	FormVersionID  int64
	DeptVisible    bool // 부서 동료 열람 허용(양식 설정의 스냅샷)
	Urgent         bool
	RetentionUntil string         // "2006-01-02", 영구·미정이면 ""
	DisposedAt     string         // 보존 만료로 폐기된 문서의 폐기 일시(KST 날짜, B29). 폐기되지 않았으면 ""
	Fields         map[string]any // 양식 필드 값(민감 필드는 열람자에 따라 마스킹)
	Recipients     []DocRecipient // 현재 차수의 참조·수신
	// B25: 희망 납기(YYYY-MM-DD, 없으면 빈 값)·납품처, 상신 때 판정한 예산 스냅샷(없으면 nil).
	DueDate       string
	DeliveryPlace string
	Budget        *BudgetSnapshot
	// B25 단계 2: 선택한 견적서 번호(없으면 0)와 최저가가 아닌 선택의 사유.
	QuoteID         int64
	QuoteReason     string
	Editable        bool           // 요청자 본인의 임시저장·반려 건이면 고칠 수 있다(상세 화면의 수정 단추와 견적 카드 링크가 이 값 하나만 쓴다)
	CanCancelSubmit bool           // 요청자 본인에게만 채운다: 상신/결재중이고 현재 차수에 처리 이력이 없어 상신 취소가 가능하다(없으면 결재자 없음 안내의 회수만 보인다)
	Authority       *AuthorityNote // B23 단계 3: 상신 때 저장한 전결 판정(없으면 nil)
	Attachments     []Attachment   // 문서에 붙은 첨부(이름·크기만)
	FormFields      []FieldDef     // 기안 때 양식 버전의 필드 정의(화면이 라벨·종류로 그린다)
	FormBody        string         // 본문 서식
	DraftEdits      []LineEdit     // 기안자가 저장해 둔 결재선 편집(기안자 본인의 임시저장·반려 문서만). 자격을 잃은 대상은 뺀 것
	EditNotes       []string       // 위에서 뺀 대상 안내 문구
}

// ApprovalAction 은 결재 이력의 동작이다. 이력 행은 수정·삭제되지 않는다.
type ApprovalAction string

const (
	ActionSubmit   ApprovalAction = "submit"
	ActionResubmit ApprovalAction = "resubmit"
	ActionApprove  ApprovalAction = "approve"
	ActionReject   ApprovalAction = "reject"
	ActionReassign ApprovalAction = "reassign" // 관리자 결재자 재지정(B21)
	ActionAgree    ApprovalAction = "agree"    // 합의 동의(B23)
	ActionObject   ApprovalAction = "object"   // 합의 이견(B23)
)

// ApprovalRecord 는 불변 결재 이력 한 줄이다.
type ApprovalRecord struct {
	ID      int64
	PRID    int64
	Seq     int
	ActorID int64
	Action  ApprovalAction
	Comment string
	At      time.Time
	// 위임 결재면 위임자(원래 결재자)의 사용자 ID, 본인 결재면 0. ActorID 는 실제로 누른 수임자.
	OnBehalfOfID int64
	// 화면 표시용 이름. 저장소가 사용자 표에서 채운다(이력 행에는 저장하지 않는다).
	ActorName      string
	OnBehalfOfName string
}

// Delegation 은 결재 위임 한 건이다. 해제는 RevokedAt 설정이며 행은 지워지지 않는다.
type Delegation struct {
	ID            int64
	DelegatorID   int64
	DelegatorName string
	DelegateID    int64
	DelegateName  string
	From, To      time.Time
	RevokedAt     *time.Time
	CreatedAt     time.Time
	// B29: 부재(absence)는 위임의 한 종류다. 위임(delegation, 기존)은 사유·메모가 비어 있다.
	Kind   string
	Reason string // vacation | trip | training | other
	Note   string
}

// 위임 종류와 부재 사유.
const (
	DelegationKindAbsence    = "absence"
	DelegationKindDelegation = "delegation"
)

// AbsenceReasons 는 부재 사유 코드와 화면 이름이다.
var AbsenceReasons = map[string]string{"vacation": "휴가", "trip": "출장", "training": "교육", "other": "기타"}

// Active 는 시각 t 에 위임이 유효한지 본다(기간 안이고 해제되지 않음).
func (d Delegation) Active(t time.Time) bool {
	return !t.Before(d.From) && t.Before(d.To) && (d.RevokedAt == nil || d.RevokedAt.After(t))
}

// DelegateCandidate 는 위임 대상으로 고를 수 있는 사용자다.
type DelegateCandidate struct {
	ID           int64
	Name         string
	Role         contract.Role
	DepartmentID int64
	// B21: 조직도에서 얻은 직책 이름(팀장·부서장·본부장·대표, 재무는 "재무")과 그 조직 이름. users.position 은 읽지 않는다.
	Title    string
	UnitName string
	// Away 는 지금 부재 중(등록된 부재 기간 안)이라 대결자로 고를 수 없는 사람이다(B29 3.3 의 5).
	Away bool
}

// ---- 발주·입고 ----

// POStatus 는 발주 상태다.
type POStatus string

const (
	POOpen     POStatus = "open"
	POPartial  POStatus = "partial"
	POReceived POStatus = "received"
)

// POLine 은 발주 품목 줄이다.
type POLine struct {
	ID          int64
	ItemID      int64
	ItemName    string
	Quantity    int64
	ReceivedQty int64
	UnitPrice   int64
}

// PurchaseOrder 는 발주(PO)다. PR 승인 시 생성된다.
type PurchaseOrder struct {
	ID          int64
	No          string
	PRID        int64
	VendorID    int64
	Status      POStatus
	TotalAmount int64
	Lines       []POLine
	CreatedAt   time.Time
	// B25: 구매요청에서 복사한 희망 납기(YYYY-MM-DD, 없으면 빈 값)·납품처.
	DueDate       string
	DeliveryPlace string
	// Intercompany 는 거래처가 그룹의 다른 법인이면 그 법인 이름이다(법인 간 거래 배지, B44 11.1).
	Intercompany string
}

// ReceiptLine 은 입고 줄이다.
type ReceiptLine struct {
	POLineID   int64
	ItemID     int64
	Quantity   int64
	LotNo      string
	ExpiryDate string
}

// Receipt 는 입고 처리 한 건이다(부분 입고 허용).
type Receipt struct {
	ID         int64
	POID       int64
	ReceivedBy int64
	ReceivedAt time.Time
	Lines      []ReceiptLine
}

// ---- 증빙 문서 판정 ----

// DocKind 는 증빙 종류다.
type DocKind string

const (
	DocTransactionStatement DocKind = "transaction_statement" // 거래명세서
	DocTaxInvoice           DocKind = "tax_invoice"           // 세금계산서
)

// ExtractedLine 은 문서에서 추출한 품목 줄이다.
type ExtractedLine struct {
	Name      string
	Quantity  int64
	UnitPrice int64
	Amount    int64
}

// Extraction 은 LLM 이 문서에서 뽑은 값이다(추출만 LLM 이 한다).
type Extraction struct {
	Kind        DocKind
	Supplier    string
	BusinessNo  string
	Lines       []ExtractedLine
	TotalAmount int64
	// Citations 는 필드별 원문 인용 구간이다(B5). 키는 필드 경로(VerdictItem.Field 표기와 같다):
	// "supplier","business_no","line[0].name","line[0].quantity","line[0].unit_price","line[0].amount","total_amount".
	// 원문(documents.text_content)에 그 구간이 실제로 있어야 인정된다.
	Citations map[string]Citation
}

// Citation 은 원문에서 추출 값의 근거가 된 구간이다. Start·End 는 원문의 룬 오프셋이며 [Start,End) 다.
type Citation struct {
	Start, End int
	Quote      string
}

// Outcome 은 판정 결과다.
type Outcome string

const (
	OutcomeMatch    Outcome = "match"    // 일치
	OutcomeMismatch Outcome = "mismatch" // 불일치
	OutcomeHold     Outcome = "hold"     // 판단 보류
)

// VerdictItem 은 판정 근거 한 항목이다. 대조 규칙은 코드로 결정적으로 돌린다.
type VerdictItem struct {
	Field    string // 예: "total_amount", "line[0].quantity"
	Expected string // PO·입고 쪽 기준 값(입고가 있으면 입고 기준). 호환용으로 유지한다.
	Actual   string // 문서 쪽 값
	PO       string // 발주 쪽 값. 해당 없으면 "-"(비어 있으면 이전 형식으로 저장된 판정)
	Receipt  string // 입고 쪽 값. 입고 없이 판정했거나 해당 없으면 "-"
	Result   Outcome
	Note     string
}

// Verdict 는 문서 판정이다. AI 판정은 제안이고, 최종 확정은 재무(document:confirm)가 한다.
type Verdict struct {
	ID               int64
	DocumentID       int64
	POID             int64
	ReceiptID        int64
	Extraction       Extraction
	Outcome          Outcome
	Items            []VerdictItem
	ConfirmedBy      int64   // 미확정이면 0
	ConfirmedOutcome Outcome // 미확정이면 ""
	SourceText       string  // 응답용 원문(역할별 마스킹 적용). 이미지·원문 없음이면 ""
	CreatedAt        time.Time
}
