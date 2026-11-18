package contract

import (
	"context"
	"sort"
)

// Role 은 사용자 역할이다. 사용자 한 명은 역할 하나와 부서 하나를 가진다.
type Role string

const (
	RoleAdmin   Role = "admin"
	RoleFinance Role = "finance"
	RoleManager Role = "manager" // 결재자(팀장·부서장)
	RoleStaff   Role = "staff"
	RoleAuditor Role = "auditor" // 읽기 전용 + 감사 로그 조회
)

// Roles 는 알려진 역할 전체다.
var Roles = []Role{RoleAdmin, RoleFinance, RoleManager, RoleStaff, RoleAuditor}

// Channel 은 요청이 들어온 경로다. 감사 로그의 "어떤 경로로"에 그대로 쓴다.
type Channel string

const (
	ChannelWeb   Channel = "web"
	ChannelMCP   Channel = "mcp"
	ChannelLLMGW Channel = "llmgw"
	ChannelAPI   Channel = "api"  // 연동 API(/api/v1, B42)로 들어온 요청. 감사·개인정보 조회 기록의 경로. 마스킹은 MCP 와 같이 어떤 필드도 원문을 내지 않는다
	ChannelSCIM  Channel = "scim" // IdP 의 SCIM 프로비저닝 요청(B40). 감사 경로
	ChannelCLI   Channel = "cli"  // 서버 호스트의 명령줄 도구(erp-admin, 서버 하위 명령). 웹·MCP 가 아닌 운영 작업의 감사 경로(B38 D7)
)

// AuthMethod 는 요청자가 신원을 증명한 수단이다. 장기 자격을 만드는 동작(토큰 발급·폐기)은
// 개인 토큰으로 들어온 요청을 거부해야 하므로, 인증 계층이 요청마다 채운다. 비어 있으면 알 수 없음이다.
type AuthMethod string

const (
	AuthSession AuthMethod = "session" // 브라우저 세션 쿠키(OIDC 로그인)
	AuthToken   AuthMethod = "token"   // 개인 토큰(Bearer, MCP)
	AuthAPI     AuthMethod = "api"     // 연동 API 토큰(Bearer pli_, /api/v1). 사람이 아니라 연동 시스템이 증명한다(B42)
	AuthSCIM    AuthMethod = "scim"    // SCIM 연동 토큰(Bearer, /scim/v2). 사람이 아니라 IdP 가 증명한다(B40)
)

// Principal 은 현재 요청을 보낸 사용자다. 인증 미들웨어(platform)가 context 에 넣고,
// 서비스·MCP 도구는 PrincipalFrom 으로 꺼낸다.
type Principal struct {
	UserID       int64
	Name         string
	Role         Role
	DepartmentID int64
	Channel      Channel
	Auth         AuthMethod
	// TokenID 는 개인 토큰으로 인증된 요청의 토큰 번호다(AuthToken 일 때만 채워진다). 사용량 집계·분당 한도의 키.
	TokenID int64
	// Setup 이 비어 있지 않으면 제한 세션이다(SetupPasswordChange·SetupTOTPEnroll). 인증 계층이 세션 행에서 채우고,
	// Can 이 한 곳에서 강제한다(핸들러 안 분기 없음).
	Setup string
	// Perms 는 인증 계층이 요청마다 계산해 넣는 실효 권한이다(B33). 비어 있지 않은 집합이든 빈 집합이든 non-nil 이면 Can 은 이 집합만으로
	// 판정한다(빈 집합은 전부 거부). nil 은 "계산되지 않은" 값이라 기본 템플릿 표(RolePermissions[Role])로 판정한다: 인증 계층을 거치지 않고
	// Role 만으로 Principal 을 만드는 단위 시험·가짜 구현용이며, 운영 인증 경로는 항상 non-nil 을 채운다(시험으로 지킨다).
	Perms PermSet
	// RoleNames 는 이 사람에게 권한을 준 역할의 이름이다(표시용).
	RoleNames []string
	// EntityPerms 는 권한별로 효력을 갖는 법인 집합이다(B44). 인증 계층이 요청마다 채운다. nil 이면 "계산되지 않은" 값이라 권한이 있으면 전체 법인으로 본다(시험·가짜 구현, 법인이 하나뿐인 설치와 같다).
	EntityPerms map[Permission]EntitySet
	// OwnEntities 는 이 사람의 소속 법인(주소속)과 겸직 법인이다(정렬, 표시·현재 법인 기본값용).
	OwnEntities []int64
}

// PermSet 은 권한 집합이다.
type PermSet map[Permission]struct{}

// Has 는 집합에 권한이 있는지다.
func (s PermSet) Has(p Permission) bool { _, ok := s[p]; return ok }

// NewPermSet 은 목록에서 집합을 만든다(항상 non-nil).
func NewPermSet(perms ...Permission) PermSet {
	s := PermSet{}
	for _, p := range perms {
		s[p] = struct{}{}
	}
	return s
}

// Sorted 는 안정적인 순서의 권한 목록이다.
func (s PermSet) Sorted() []Permission {
	out := make([]Permission, 0, len(s))
	for p := range s {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// 제한 세션 사유.
const (
	SetupPasswordChange = "password_change"
	SetupTOTPEnroll     = "totp_enroll"
)

// Can 은 역할 → 권한 표(RolePermissions)로 판정한다. 알 수 없는 역할은 전부 거부(fail-closed).
func (p Principal) Can(perm Permission) bool {
	if perm == PermPublic {
		return true
	}
	if p.UserID == 0 {
		return false
	}
	if p.Setup != "" { // 제한 세션: 설정 경로만
		return perm == PermSetup
	}
	if perm == PermAuthenticated || perm == PermSetup {
		return true
	}
	if p.Perms != nil {
		return p.Perms.Has(perm)
	}
	return RoleHas(p.Role, perm) // 계산되지 않은 Principal(시험·가짜 구현): 기본 템플릿 표
}

type principalKey struct{}

// WithPrincipal 은 context 에 Principal 을 싣는다.
func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, p)
}

// PrincipalFrom 은 context 의 Principal 을 돌려준다. 없으면 ok=false.
func PrincipalFrom(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(Principal)
	return p, ok
}

// Authorize 는 서비스 계층의 진입 검사다. Principal 이 없으면 ErrUnauthenticated,
// 권한이 없으면 ErrForbidden. 웹 핸들러와 MCP 도구가 같은 서비스를 타므로 같은 검사를 받는다.
func Authorize(ctx context.Context, perm Permission) (Principal, error) {
	p, ok := PrincipalFrom(ctx)
	if !ok {
		return Principal{}, ErrUnauthenticated
	}
	if !p.Can(perm) {
		if p.Setup != "" && p.UserID != 0 {
			return p, ErrSetupRequired
		}
		return p, ErrForbidden
	}
	// 다법인(B44): 현재 법인이 하나로 정해져 있으면 그 법인에서 권한이 효력을 가져야 하고, "전체" 모드면 그룹 범위로 효력이 있어야 한다.
	// 법인마다 권한이 다른 사람이 권한 없는 법인에서 일하지 못하게 모든 서비스 호출이 지나는 이 한 곳에서 막는다. 현재 법인이 없는 context(서버 내부 작업·시험)와
	// 계산되지 않은 Principal(EntityPerms 가 nil)은 영향이 없다.
	if a, has := ActiveEntityFrom(ctx); has && p.EntityPerms != nil {
		set := p.EntitiesFor(perm)
		if (a.All && !set.All) || (!a.All && !set.Has(a.ID)) {
			return p, ErrForbidden
		}
	}
	return p, nil
}
