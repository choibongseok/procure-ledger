package domain

import "context"

// B23 단계 3: 전결 규정(명세 3.13). 기존 시그니처는 바꾸지 않고 더하기만 한다.

// AuthorityNote 는 상신 때 적용한 전결 판정 결과다(스냅샷으로 저장되어 규정이 바뀌어도 진행 중 문서는 그대로다).
// Warning 이 있으면 필수 합의와 상충해 규정 적용을 포기한 것이고 이때 FinalOrder 는 0 이다.
type AuthorityNote struct {
	RuleID        int64  `json:"ruleId"`
	MinAmount     int64  `json:"minAmount"`
	MaxAmount     int64  `json:"maxAmount"` // 0 = 상한 없음
	FinalOrder    int    `json:"finalOrder"`
	FinalTitle    string `json:"finalTitle"`
	SkippedOrders []int  `json:"skippedOrders"`
	Slug          string `json:"slug"`
	ClauseNo      string `json:"clauseNo"`
	Warning       string `json:"warning,omitempty"`
}

// AuthorityRule 은 전결 규정 표의 한 줄이다.
type AuthorityRule struct {
	ID          int64
	FormCode    string // 빈 값 = 모든 양식
	FormName    string
	MinAmount   int64
	MaxAmount   int64 // 0 = 상한 없음
	FinalLevel  int   // 1 팀장 · 2 부서장 · 3 본부장 · 4 대표
	PolicySlug  string
	PolicyTitle string // 근거 규정 문서의 한국어 제목(화면용, 규정 문서가 없으면 빈 값)
	ClauseNo    string
	Active      bool
}

// AuthorityRuleInput 은 규정 저장 입력이다. ID 가 0 이면 새로 만든다.
type AuthorityRuleInput struct {
	ID         int64
	FormCode   string
	MinAmount  int64
	MaxAmount  int64
	FinalLevel int
	PolicySlug string
	ClauseNo   string
	Active     bool
}

// AuthorityService 는 전결 규정 표 관리다(권한 authority:manage, admin). 변경은 감사 기록이다. MCP 에는 노출하지 않는다.
type AuthorityService interface {
	List(ctx context.Context) ([]AuthorityRule, error)
	Save(ctx context.Context, in AuthorityRuleInput) (AuthorityRule, error)
	Delete(ctx context.Context, id int64) error
	Policies(ctx context.Context) ([]PolicyRef, error) // 근거로 고를 수 있는 규정 문서 목록(권한 authority:manage)
}

// PolicyRef 는 규정 문서의 코드(slug)와 한국어 제목이다.
type PolicyRef struct {
	Slug  string
	Title string
}

// ClauseReader 는 근거 조항 원문을 읽는 규정 검색 서비스(B12, ai)의 모양이다. ai 가 domain.PolicyService.ClauseBy 를 더하면 그대로 연결된다.
type ClauseReader interface {
	ClauseBy(ctx context.Context, slug, no string) (PolicyHit, error)
}
