package domain

import "context"

// B23 전자결재 표준: 양식(필드·본문·기본 결재선·번호·보존·사용 부서)과 양식 버전.
// 기존 시그니처는 바꾸지 않고 이 파일에 더하기만 한다(platform·ai 가 쓰는 계약과 겹치지 않는다).

// FieldType 은 양식 필드 종류다(명세 3.3).
type FieldType string

const (
	FieldText       FieldType = "text"
	FieldTextarea   FieldType = "textarea"
	FieldNumber     FieldType = "number"
	FieldAmount     FieldType = "amount"
	FieldDate       FieldType = "date"
	FieldSelect     FieldType = "select"
	FieldTable      FieldType = "table"
	FieldAttachment FieldType = "attachment"
	FieldUnit       FieldType = "unit" // 조직 선택(협조 요청 부서). 값은 조직 번호
)

// FieldDef 는 필드 정의 한 개다. 정의는 양식 버전의 JSON 으로 저장된다.
type FieldDef struct {
	Key       string     `json:"key"`
	Label     string     `json:"label"`
	Type      FieldType  `json:"type"`
	Required  bool       `json:"required"`
	Help      string     `json:"help,omitempty"`
	Sensitive bool       `json:"sensitive,omitempty"` // 개인정보·계좌 등. 비관계 열람자·MCP 에는 마스킹한다
	MaxLength int        `json:"maxLength,omitempty"`
	Min       *int64     `json:"min,omitempty"`
	Max       *int64     `json:"max,omitempty"`
	Decimals  int        `json:"decimals,omitempty"`
	Options   []string   `json:"options,omitempty"`
	Columns   []FieldDef `json:"columns,omitempty"`
	MinRows   int        `json:"minRows,omitempty"`
	MaxRows   int        `json:"maxRows,omitempty"`
	MaxFiles  int        `json:"maxFiles,omitempty"`
	Accept    []string   `json:"accept,omitempty"`
	// SumOf 는 금액 필드가 표 필드의 열 합계와 같아야 한다는 규칙이다("표키.열키", 예 items.amount). 상신 때 서버가 검사한다.
	SumOf string `json:"sumOf,omitempty"`
}

// StepSpec 은 양식 기본 결재선의 단계 한 개다(명세 3.6).
type StepSpec struct {
	Role     string `json:"role"`            // approve | agree | cc | receive
	Kind     string `json:"kind,omitempty"`  // 직책 종류(team_lead·dept_head·…)
	Source   string `json:"source"`          // org_level | fixed_user | unit_head | role_pool | drafter_choice | field_unit
	Level    int    `json:"level,omitempty"` // org_level 의 층
	Field    string `json:"field,omitempty"` // field_unit 이 가리키는 조직 선택 필드
	Required bool   `json:"required"`
	Stage    int    `json:"stage,omitempty"`    // 같은 번호의 합의는 병렬
	UserID   int64  `json:"userId,omitempty"`   // fixed_user
	UnitID   int64  `json:"unitId,omitempty"`   // unit_head·수신처
	UnitCode string `json:"unitCode,omitempty"` // 조직 코드로 지정(시드)
	// MinAmount 가 0 보다 크면 문서 금액(양식의 금액 필드)이 이 값 이상일 때만 이 단계가 결재선에 들어간다(B31 D6: 전표 1억원 이상은 재무본부장 추가).
	MinAmount int64 `json:"minAmount,omitempty"`
}

// LineDef 는 기본 결재선이다. Rule 이 "amount_tiers" 면 금액 구간 규칙 표를 쓰고, 아니면 Steps 를 쓴다.
type LineDef struct {
	Rule  string     `json:"rule,omitempty"`
	Steps []StepSpec `json:"steps,omitempty"`
}

// RecipientDef 는 기본 참조·수신 조직이다.
type RecipientDef struct {
	UnitID   int64  `json:"unit_id,omitempty"`
	UnitCode string `json:"unit_code,omitempty"` // 시드는 조직 코드로 지정한다
}

// Recipients 는 양식의 기본 참조처·수신처다.
type Recipients struct {
	CC      []RecipientDef `json:"cc"`
	Receive []RecipientDef `json:"receive"`
}

// Numbering 은 문서 번호 규칙이다(명세 3.4).
type Numbering struct {
	Pattern  string `json:"pattern"`
	AssignAt string `json:"assign_at"` // submit | create
	Reset    string `json:"reset"`     // yearly | never
}

// FormSettings 는 양식별 동작 설정이다.
type FormSettings struct {
	LineEdit        string `json:"line_edit"`        // none | add_only | free
	ObjectionAction string `json:"objection_action"` // record | reject
	AmountField     string `json:"amount_field"`
	DeptVisible     bool   `json:"dept_visible"`
	UrgentAllowed   bool   `json:"urgent_allowed"`
}

// FormDefinition 은 양식 버전의 내용 전체다.
type FormDefinition struct {
	System         bool         `json:"system"`
	Fields         []FieldDef   `json:"fields"`
	Body           string       `json:"body"`
	Line           LineDef      `json:"line"`
	Recipients     Recipients   `json:"recipients"`
	Numbering      Numbering    `json:"numbering"`
	RetentionYears int          `json:"retention_years"` // 0 = 영구
	LegalHold      bool         `json:"legal_hold"`      // 법정 보존: 연한이 지나도 폐기하지 않는다(B29). 시스템 양식 PUR·EXP·ADJ 는 코드가 항상 참으로 본다
	DeptLimit      []int64      `json:"dept_limit"`
	Settings       FormSettings `json:"settings"`
}

// FormKind 는 양식 종류다. purchase 는 완료 때 발주를 만드는 시스템 양식이다.
type FormKind string

const (
	FormKindGeneral  FormKind = "general"
	FormKindPurchase FormKind = "purchase"
)

// FormSummary 는 쓸 수 있는 양식 한 줄이다(최신 게시 버전 기준).
type FormSummary struct {
	ID       int64
	Code     string
	Category string // general | purchase | finance | cooperation | other
	Name     string
	Kind     FormKind
	Version  int
	Active   bool
}

// FormDetail 은 양식과 최신 게시 버전의 정의다.
type FormDetail struct {
	FormSummary
	VersionID  int64
	Definition FormDefinition
}

// FormVersion 은 양식 버전 한 개다.
type FormVersion struct {
	ID          int64
	FormID      int64
	FormCode    string
	Version     int
	Status      string // draft | published | retired
	Definition  FormDefinition
	CreatedAt   string
	PublishedAt string
}

// FormAdminRow 는 관리 화면 목록의 한 줄이다.
type FormAdminRow struct {
	FormSummary
	LatestVersion   int    // 가장 큰 버전 번호
	LatestStatus    string // 그 버전의 상태
	PublishedCount  int
	HasDraftVersion bool
	Searchable      bool // 내용 검색에 포함(B47)
}

// FormVersionInput 은 초안 버전 저장 입력이다. 시스템 양식은 필드·본문을 바꿀 수 없다.
type FormVersionInput struct {
	Definition FormDefinition
}

// FormService 는 양식 읽기(기안 화면·MCP)와 관리(admin)다. 웹 핸들러와 MCP 도구가 같이 쓴다.
type FormService interface {
	ListUsable(ctx context.Context) ([]FormSummary, error)                    // draft:create. 사용 부서 제한 적용
	Get(ctx context.Context, code string) (FormDetail, error)                 // 최신 게시 버전
	List(ctx context.Context) ([]FormAdminRow, error)                         // form:manage
	GetVersion(ctx context.Context, versionID int64) (FormVersion, error)     // form:manage
	CreateVersionDraft(ctx context.Context, code string) (FormVersion, error) // 최신 게시 버전을 복사한 초안
	SaveVersionDraft(ctx context.Context, versionID int64, in FormVersionInput) (FormVersion, error)
	Publish(ctx context.Context, versionID int64) (FormVersion, error)  // 검증 문구는 ErrValidation 메시지
	SetActive(ctx context.Context, code string, active bool) error      // 사용 중지·재개
	PreviewNumber(ctx context.Context, versionID int64) (string, error) // 다음 번호 예
}

// LineEdit 는 기안자가 양식 기본 결재선에 더하는 한 줄이다(명세 3.6). 필수 단계는 뺄 수 없고, 더한 것은 빼면 된다(보내지 않으면 된다).
type LineEdit struct {
	Role       string // agree | cc | receive | approve(free 양식만)
	TargetType string // user | unit
	TargetID   int64
	Group      int    // 같은 0 이 아닌 번호의 합의는 병렬이다
	Name       string // 응답 표시용 이름(저장·입력에는 쓰지 않는다)
}

// DocLineInput 은 양식 문서의 결재선 미리보기 입력이다.
type DocLineInput struct {
	FormCode  string
	Amount    int64
	FieldUnit int64 // 조직 선택 필드(협조 요청 부서 등)의 값
	Fields    map[string]any
	Edits     []LineEdit
}

// DocDraftInput 은 양식 문서 임시저장 입력이다. 구매 품의서는 PurchaseService 가 다룬다.
type DocDraftInput struct {
	FormCode string
	Title    string
	Fields   map[string]any
	Urgent   bool
	Edits    []LineEdit
}

// DocFilter 는 양식 문서 목록 조건이다(구매 품의서는 제외). 열람 범위는 서비스가 적용한다.
type DocFilter struct {
	FormCode    string
	Status      PRStatus
	RequesterID int64
	AfterID     int64
	Limit       int
}

// ApprovalDocService 는 구매 품의서 외 양식 문서의 기안·상신·조회다(B23). 결재·합의 처리는 ApprovalService.Decide 가 한다.
type ApprovalDocService interface {
	PreviewLine(ctx context.Context, in DocLineInput) (LinePreview, error)      // draft:create
	CreateDraft(ctx context.Context, in DocDraftInput) (PurchaseRequest, error) // draft:create
	UpdateDraft(ctx context.Context, id int64, in DocDraftInput) (PurchaseRequest, error)
	Submit(ctx context.Context, id int64) (PurchaseRequest, error)
	Get(ctx context.Context, id int64) (PurchaseRequest, error)                         // draft:read
	CancelSubmit(ctx context.Context, id int64, reason string) (PurchaseRequest, error) // 상신 취소(D9 확정): 아무도 처리하지 않은 문서를 기안자가 임시저장으로 되돌린다
	List(ctx context.Context, f DocFilter) ([]PurchaseRequest, error)
	Withdraw(ctx context.Context, id int64) (PurchaseRequest, error) // 결재자 없음으로 멈춘 상신 문서의 회수(구매 품의서 회수와 같은 조건)
	RecordPrint(ctx context.Context, id int64) error                 // 출력 화면에서 인쇄할 때 감사에 남긴다(document.print, 구매 품의서 포함). 열람 범위 밖이면 ErrNotFound
}

// PersonalLine 은 기안자가 저장한 개인 결재선(편집 부분)이다.
type PersonalLine struct {
	ID       int64
	Name     string
	FormCode string // 비면 모든 양식
	Edits    []LineEdit
}

// PersonalLineInput 은 개인 결재선 저장 입력이다. 같은 이름이면 덮어쓴다.
type PersonalLineInput struct {
	Name     string
	FormCode string
	Edits    []LineEdit
}

// PersonalLineService 는 개인 결재선 저장·불러오기·삭제다(본인 것만).
type PersonalLineService interface {
	List(ctx context.Context) ([]PersonalLine, error)
	Save(ctx context.Context, in PersonalLineInput) (PersonalLine, error)
	Delete(ctx context.Context, id int64) error
	Load(ctx context.Context, id int64) (PersonalLine, []string, error) // 퇴사·없어진 대상은 뺀 줄과 안내 문구
}

// Attachment 는 첨부 한 개의 이름·크기·해시다(내용은 없다).
type Attachment struct {
	ID       int64
	Filename string
	Size     int64
	SHA256   string
}

// AttachmentFile 은 내려받기용 내용이다.
type AttachmentFile struct {
	ID          int64
	Filename    string
	ContentType string
	Size        int64
	SHA256      string
	Data        []byte
}

// AttachmentService 는 양식 문서 첨부다(B23 명세 3.15). 내용은 LLM 게이트웨이·MCP·감사 diff·알림으로 나가지 않는다.
type AttachmentService interface {
	Upload(ctx context.Context, filename string, data []byte) (Attachment, error) // draft:create
	Open(ctx context.Context, id int64) (AttachmentFile, error)                   // draft:read, 문서 열람 조건을 따른다
}
