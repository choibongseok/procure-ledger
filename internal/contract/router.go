package contract

import (
	"fmt"
	"net/http"
	"sort"
	"sync"
)

// Registrar 는 HTTP 라우트를 등록하는 유일한 방법이다. Permission 이 필수 인자라서
// 권한 선언 없는 라우트는 코드로 표현할 수 없다. 모든 도메인의 Register* 함수는 http.ServeMux 가 아니라
// 이 인터페이스만 받는다. 핸들러 안에서 역할을 비교하지 않는다.
//
// pattern 은 Go 1.22 ServeMux 패턴에서 메서드를 뺀 경로("/api/purchase-requests/{id}")다.
type Registrar interface {
	Handle(method, pattern string, perm Permission, h http.Handler)
	HandleFunc(method, pattern string, perm Permission, h http.HandlerFunc)
}

// RouteDecl 은 등록된 라우트 한 건의 선언이다. 누락 검사 테스트가 순회한다.
type RouteDecl struct {
	Method     string
	Pattern    string
	Permission Permission
}

// RouteTable 은 Registrar 구현이다. 등록 내역을 보관하고 ServeMux 로 조립한다. (구현: platform)
//
// Guard 가 nil 이면 DefaultGuard(Principal 이 context 에 있고 역할이 권한을 가졌는지)가 쓰인다.
// Principal 을 context 에 넣는 인증 미들웨어는 RouteTable.Handler() 바깥에 둔다(platform).
type RouteTable struct {
	Guard func(perm Permission, next http.Handler) http.Handler

	mu     sync.Mutex
	routes []RouteDecl
	seen   map[string]bool
	mux    *http.ServeMux
}

// NewRouteTable 은 빈 RouteTable 을 만든다.
func NewRouteTable() *RouteTable {
	return &RouteTable{mux: http.NewServeMux(), seen: map[string]bool{}}
}

// Handle 은 Registrar 를 구현한다. 알 수 없거나 빈 권한, 중복 등록은 패닉(기동 실패)이다.
func (t *RouteTable) Handle(method, pattern string, perm Permission, h http.Handler) {
	if method == "" || pattern == "" || h == nil {
		panic("contract: 라우트 등록에 method·pattern·handler 가 필요합니다")
	}
	if !perm.IsKnown() {
		panic(fmt.Sprintf("contract: %s %s 에 알 수 없는 권한 %q 이 선언되었습니다", method, pattern, perm))
	}
	key := method + " " + pattern
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.seen[key] {
		panic("contract: 라우트가 중복 등록되었습니다: " + key)
	}
	t.seen[key] = true
	t.routes = append(t.routes, RouteDecl{Method: method, Pattern: pattern, Permission: perm})

	guard := t.Guard
	if guard == nil {
		guard = DefaultGuard
	}
	t.mux.Handle(key, guard(perm, h))
}

// HandleFunc 은 Registrar 를 구현한다.
func (t *RouteTable) HandleFunc(method, pattern string, perm Permission, h http.HandlerFunc) {
	t.Handle(method, pattern, perm, h)
}

// Routes 는 등록된 선언 목록(정렬됨)이다.
func (t *RouteTable) Routes() []RouteDecl {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := append([]RouteDecl(nil), t.routes...)
	sort.Slice(out, func(i, j int) bool {
		if out[i].Pattern != out[j].Pattern {
			return out[i].Pattern < out[j].Pattern
		}
		return out[i].Method < out[j].Method
	})
	return out
}

// Handler 는 조립된 http.Handler 다.
func (t *RouteTable) Handler() http.Handler { return t.mux }

// DefaultGuard 는 권한 선언을 강제하는 기본 래퍼다. 선언이 Public 이면 그대로 통과,
// 아니면 Principal 이 없을 때 401, 권한이 없을 때 403 을 돌려준다(fail-closed).
func DefaultGuard(perm Permission, next http.Handler) http.Handler {
	if perm == PermPublic {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := Authorize(r.Context(), perm); err != nil {
			code := http.StatusForbidden
			if err == ErrUnauthenticated {
				code = http.StatusUnauthorized
			}
			if err == ErrSetupRequired {
				p, _ := PrincipalFrom(r.Context())
				WriteSetupRequired(w, p.Setup)
				return
			}
			http.Error(w, err.Error(), code)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// WriteSetupRequired 는 제한 세션이 허용되지 않는 경로를 불렀을 때의 403 응답이다: 헤더 X-Auth-Setup 과 본문 코드 auth_setup_required 를 같이 싣는다.
func WriteSetupRequired(w http.ResponseWriter, setup string) {
	w.Header().Set("X-Auth-Setup", setup)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusForbidden)
	_, _ = w.Write([]byte(`{"code":"auth_setup_required","message":"` + ErrSetupRequired.Error() + `","setup":"` + setup + `"}` + "\n"))
}

// ToolDecl 은 MCP 도구 한 건의 권한 선언이다.
type ToolDecl struct {
	Name       string
	Permission Permission
}

// ToolTable 은 MCP 도구의 권한 선언을 모은다. ai 의 도구 등록 함수는 SDK 에 도구를 붙이기 전에
// 반드시 Declare 를 거친다. 권한은 MCPToolPermissions 표에서만 정해지므로 도구 쪽에서 임의로 낮출 수 없다.
type ToolTable struct {
	mu    sync.Mutex
	tools []ToolDecl
}

// Declare 는 도구를 선언하고 표의 권한을 돌려준다. 표에 없는 도구는 패닉(기동 실패)이다.
func (t *ToolTable) Declare(name string) Permission {
	perm, ok := MCPToolPermissions[name]
	if !ok {
		panic("contract: MCPToolPermissions 에 없는 도구입니다: " + name)
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.tools = append(t.tools, ToolDecl{Name: name, Permission: perm})
	return perm
}

// Tools 는 선언된 도구 목록이다.
func (t *ToolTable) Tools() []ToolDecl {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]ToolDecl(nil), t.tools...)
}

// CheckRoutes 는 선언 누락·오타를 찾아 문제 목록을 돌려준다(빈 목록이면 통과).
func CheckRoutes(routes []RouteDecl) []string {
	var problems []string
	for _, r := range routes {
		if !r.Permission.IsKnown() {
			problems = append(problems, fmt.Sprintf("라우트 %s %s: 알 수 없는 권한 %q", r.Method, r.Pattern, r.Permission))
		}
	}
	return problems
}

// CheckTools 는 MCP 도구 선언이 권한 표와 정확히 일치하는지 본다.
// 표의 도구가 빠졌거나, 표에 없는 도구가 있거나, 권한이 다르면 문제로 돌려준다.
func CheckTools(tools []ToolDecl) []string {
	var problems []string
	declared := map[string]Permission{}
	for _, t := range tools {
		declared[t.Name] = t.Permission
		want, ok := MCPToolPermissions[t.Name]
		switch {
		case !ok:
			problems = append(problems, fmt.Sprintf("도구 %s: 권한 표에 없음", t.Name))
		case want != t.Permission:
			problems = append(problems, fmt.Sprintf("도구 %s: 선언 %q != 표 %q", t.Name, t.Permission, want))
		}
	}
	for name := range MCPToolPermissions {
		if _, ok := declared[name]; !ok {
			problems = append(problems, fmt.Sprintf("도구 %s: 표에는 있으나 선언되지 않음", name))
		}
	}
	sort.Strings(problems)
	return problems
}
