package contract

import (
	"context"
	"net/http"
	"time"
)

// Authenticator 는 MCP·API 의 개인 토큰을 Principal 로 바꾼다.
// 구현: platform (internal/auth, 해시 비교·만료·폐기 확인). 쓰는 쪽: ai internal/mcp.
type Authenticator interface {
	AuthenticateToken(ctx context.Context, rawToken string) (Principal, error)
}

// FieldKind 는 구분 저장되는 민감 필드 종류다(migrations 의 vendors 컬럼과 대응).
type FieldKind string

const (
	FieldBusinessNo FieldKind = "business_no"
	FieldAccountNo  FieldKind = "account_no"
	FieldPhone      FieldKind = "phone"
	FieldEmail      FieldKind = "email"
)

// Masker 는 마스킹 헬퍼다. 구현: platform. 쓰는 쪽: erp(응답), ai(LLM 프롬프트: llmgw 가 전송 전 MaskText).
type Masker interface {
	// MaskField 는 역할이 mask:bypass 권한이 없으면 가린 값을, 있으면 원문을 돌려준다.
	MaskField(p Principal, kind FieldKind, value string) string
	// MaskText 는 자유 텍스트 안의 사업자번호·계좌·연락처 패턴을 무조건 가린다(LLM 전송 전).
	MaskText(text string) string
}

// TokenInfo 는 개인 토큰의 표시용 정보다. 원문·해시는 담지 않는다.
type TokenInfo struct {
	ID         int64
	Name       string
	Prefix     string
	CreatedAt  time.Time
	ExpiresAt  time.Time
	RevokedAt  *time.Time
	LastUsedAt *time.Time
	OwnerName  string // TokenLookup 이 채운다(목록 조회는 비어 있다)
}

// TokenManager 는 본인 개인 토큰의 발급·목록·폐기다. 구현: platform (internal/auth).
// 원문은 Issue 가 한 번만 돌려주고 저장하지 않는다. 발급·폐기는 같은 트랜잭션에서 감사 기록을 남긴다.
type TokenManager interface {
	Issue(ctx context.Context, p Principal, name string, ttl time.Duration) (raw string, info TokenInfo, err error)
	List(ctx context.Context, p Principal) ([]TokenInfo, error)
	Revoke(ctx context.Context, p Principal, id int64) error
}

// TokenLookup 은 사용량 화면이 토큰 번호를 이름·앞자리·소유자로 바꾸는 조회다. 원문·해시는 돌려주지 않는다.
// 폐기·만료된 토큰도 조회된다. 권한 검사는 호출하는 쪽(usage:read 라우트)이 한다. 구현: platform (internal/auth).
type TokenLookup interface {
	TokenInfos(ctx context.Context, ids []int64) (map[int64]TokenInfo, error)
}

// UserInfo 는 사용자 관리 화면용 정보다.
type UserInfo struct {
	ID             int64
	Email          string
	Name           string
	Role           Role
	Position       string
	DepartmentID   int64
	DepartmentName string
	Active         bool
}

// UserPatch 는 사용자 수정 항목이다. nil 필드는 바꾸지 않는다. 소속·직책은 여기서 바꿀 수 없다(인사 발령으로만, B20).
type UserPatch struct {
	Role   *Role
	Active *bool
}

// UserDirectory 는 사용자·역할·부서 관리다(user:manage). 구현: platform.
type UserDirectory interface {
	List(ctx context.Context, p Principal) ([]UserInfo, error)
	Update(ctx context.Context, p Principal, id int64, patch UserPatch) (UserInfo, error)
}

// AuthHTTP 는 인증 HTTP 계층이다. 구현: platform (internal/auth).
// Wrap 은 세션 쿠키·개인 토큰으로 Principal 을 context 에 넣고 상태 변경 요청의 CSRF 를 검사한다.
// Register 는 로그인·콜백·로그아웃·내 정보와(설정으로 켜진 경우) dev IdP 라우트를 선언과 함께 등록한다.
type AuthHTTP interface {
	Wrap(next http.Handler) http.Handler
	Register(r Registrar)
}
