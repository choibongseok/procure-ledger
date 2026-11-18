package domain

import "context"

// B47 사내 지식 검색(). 완료된 결재 문서의 본문(1단계)·첨부(3단계)와 업로드 매뉴얼·규정(2단계)을 찾는다.
// 결과는 호출자가 원문을 열 수 있는 문서의 조각뿐이다. 권한 밖 문서는 결과·건수·점수 어디에도 드러나지 않는다.

// 지식 문서 종류(KnowledgeHit.Kind).
const (
	KnowledgeApprovalDoc = "approval_doc" // 완료된 결재 문서 본문
	KnowledgeAttachment  = "attachment"   // 결재 문서 첨부
	KnowledgeManual      = "manual"       // 업로드 매뉴얼
	KnowledgeRegulation  = "regulation"   // 업로드 규정
)

// KnowledgeKindLabels 는 종류의 한국어 이름이다.
var KnowledgeKindLabels = map[string]string{
	KnowledgeApprovalDoc: "결재 문서", KnowledgeAttachment: "첨부", KnowledgeManual: "매뉴얼", KnowledgeRegulation: "규정",
}

// KnowledgeFilter 는 검색 조건이다. Limit 기본 5·최대 20, Offset 은 "더 보기"(순위 순서에서 건너뛸 건수)다. Kinds 가 비면 전부.
type KnowledgeFilter struct {
	Query  string
	Limit  int
	Offset int
	Kinds  []string
}

// KnowledgeHit 은 결과 한 건이다. 점수·전체 건수는 없다. Snippet 은 항상 마스킹된 발췌다.
type KnowledgeHit struct {
	Kind        string
	Title       string
	DocNo       string // 결재 문서 번호(첨부도 부모 문서의 번호)
	LocatorKind string // body | page | clause | heading | para
	Locator     string // 쪽 번호·조항·소제목
	Heading     string
	Snippet     string
	Version     int    // 업로드 문서의 버전(결재 문서·첨부는 0)
	Source      string // 출처 한 줄("결재 문서 AB-2026-0012 「제목」 · 본문")
	Link        string // 상대 경로(결재 문서 /docs/{id}, 구매 품의서 /purchase-requests/{id})
}

// KnowledgeResult 는 검색 결과다. More 는 같은 질의로 Offset 을 늘려 이어 볼 결과가 있을 수 있다는 뜻일 뿐 건수가 아니다.
type KnowledgeResult struct {
	Hits []KnowledgeHit
	More bool
}

// KnowledgeService 는 사내 지식 검색이다. 웹 핸들러와 MCP 도구가 같이 쓴다(채널이 달라도 판정 코드는 하나).
type KnowledgeService interface {
	Search(ctx context.Context, f KnowledgeFilter) (KnowledgeResult, error)
	// Open 은 업로드 문서(매뉴얼·규정)의 원문 보기다. 열람 범위 밖이거나 지워졌으면 없는 것과 같은 ErrNotFound. 감사 knowledge.open.
	Open(ctx context.Context, id int64) (KnowledgeDocView, error)
	// File 은 업로드 원본 파일이다(내려받기). 열람 판정은 Open 과 같다. 감사 knowledge.download.
	File(ctx context.Context, id int64) (KnowledgeFile, error)
}

// KnowledgeCitation 은 답변이 인용한 자료 한 건이다. N 은 답 안의 [N] 번호다.
type KnowledgeCitation struct {
	N   int
	Hit KnowledgeHit
}

// KnowledgeAnswer 는 질문하기 결과다. Found 가 false 면 Text 는 "찾지 못함" 고정 문구이고 Sources 는 비어 있다.
type KnowledgeAnswer struct {
	Text    string
	Found   bool
	Sources []KnowledgeCitation
}

// KnowledgeAnswerService 는 근거 조각으로만 답하는 질문하기다(B47 3단계). 근거는 질문자가 열 수 있는 문서뿐이다.
type KnowledgeAnswerService interface {
	Answer(ctx context.Context, question string) (KnowledgeAnswer, error)
}

// KnowledgeSection 은 원문 보기의 한 구역(쪽·조항·소제목)이다.
type KnowledgeSection struct {
	LocatorKind string
	Locator     string
	Heading     string
	Text        string
}

// KnowledgeDocView 는 업로드 문서 원문 보기다.
type KnowledgeDocView struct {
	ID         int64
	Kind       string
	Title      string
	Version    int
	UploadedAt string
	Filename   string
	Sections   []KnowledgeSection
}

// KnowledgeFile 은 내려받을 원본이다.
type KnowledgeFile struct {
	Filename    string
	ContentType string
	Data        []byte
}

// KnowledgeUpload 는 관리 화면의 업로드 문서 한 건이다(내용 없음).
type KnowledgeUpload struct {
	ID             int64
	EntityID       int64 // 0 = 모든 법인 공통
	EntityName     string
	Kind           string
	Title          string
	Version        int
	Roles          string // "all" 또는 쉼표 목록(역할 이름·권한 이름)
	Departments    string // "all" 또는 쉼표 목록(부서 이름)
	Filename       string
	Size           int64
	UploadedByName string
	UploadedAt     string
	State          string // 색인 상태(ready·pending·no_text·unreadable·unsupported·too_long·failed)
	Note           string
	Notice         string   // 색인 대기(pending)일 때의 안내("색인 중입니다…"). 아니면 빈 값
	Warnings       []string // 업로드·새 버전 응답에만: 개인정보 패턴 경고 등(목록에는 없다)
}

// KnowledgeUploadInput 은 업로드 입력이다. Entity 는 법인 코드, 비면 현재 법인, "*" 는 모든 법인 공통(그룹 범위 관리자만).
type KnowledgeUploadInput struct {
	Kind        string // manual | regulation
	Title       string
	Entity      string
	Roles       string // 비면 all
	Departments string // 비면 all
	Filename    string
	Content     []byte
}

// KnowledgeStatus 는 관리 화면의 색인 현황이다(knowledge:manage). 문서 제목은 담지 않는다.
type KnowledgeStatus struct {
	Docs        map[string]int // 종류별 지식 문서 수
	States      map[string]int // 추출 상태별 수(ready·no_text·unreadable·unsupported·too_long·failed·pending)
	Chunks      int
	QueueOpen   int    // 아직 처리하지 않은 대기열 건수
	OldestQueue string // 가장 오래된 대기 줄의 시각(없으면 "")
	Generation  int64
	ChangedAt   string
}

// KnowledgeAdminService 는 지식 색인 관리다. 권한 knowledge:manage.
type KnowledgeAdminService interface {
	Status(ctx context.Context) (KnowledgeStatus, error)
	// Reindex 는 범위(법인 코드·종류)의 색인을 다시 만든다. 내용이 같은 문서는 건너뛴다. 처리한 건수와 건너뛴 건수를 돌려준다.
	Reindex(ctx context.Context, entityCode, kind string) (done, skipped int, err error)
	// SetFormSearchable 은 양식별 "내용 검색에 포함" 을 바꾼다(기본 켬). 그 양식의 기존 문서도 조회 때 즉시 반영된다.
	SetFormSearchable(ctx context.Context, formCode string, searchable bool) error

	// Departments 는 공개 범위에 쓸 수 있는 부서 이름 목록이다.
	Departments(ctx context.Context) ([]string, error)
	// Uploads 는 열려 있는 업로드 문서 목록이다(현재 법인과 모든 법인 공통, 전체 모드는 전부).
	Uploads(ctx context.Context) ([]KnowledgeUpload, error)
	// Upload 는 매뉴얼·규정 파일을 올린다. 저장 전에 글자 추출을 시험하고 읽지 못하면 한국어 사유로 거부한다.
	Upload(ctx context.Context, in KnowledgeUploadInput) (KnowledgeUpload, error)
	// Replace 는 같은 문서의 새 버전을 올린다(이전 버전은 검색에서 빠진다).
	Replace(ctx context.Context, id int64, filename string, content []byte) (KnowledgeUpload, error)
	// Remove 는 업로드 문서를 지운다(표시와 내용 비움, 조각은 바로 삭제).
	Remove(ctx context.Context, id int64) error
	// SetScope 는 열람 범위(역할·부서)를 바꾼다. 색인을 다시 만들지 않고 바로 반영된다.
	SetScope(ctx context.Context, id int64, roles, departments string) (KnowledgeUpload, error)
}
