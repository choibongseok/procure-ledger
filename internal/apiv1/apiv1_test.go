package apiv1

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
)

const committedOpenAPI = "../../docs/openapi/v1.json"

// 문서 정본은 라우트 선언표에서 생성한 것이다(D14). 저장소의 docs/openapi/v1.json 은 그 결과여야 한다.
// 갱신은 PL_UPDATE_OPENAPI=1 go test ./internal/apiv1 (make openapi) 로 한다.
func TestOpenAPIMatchesCommitted(t *testing.T) {
	got, err := OpenAPI()
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, '\n')
	again, _ := OpenAPI()
	if !bytes.Equal(got[:len(got)-1], again) {
		t.Fatal("같은 입력인데 문서가 달라진다(결정적이지 않음)")
	}
	if os.Getenv("PL_UPDATE_OPENAPI") == "1" {
		if err := os.MkdirAll("../../docs/openapi", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(committedOpenAPI, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(committedOpenAPI)
	if err != nil {
		t.Fatalf("저장소의 OpenAPI 문서가 없다(make openapi 로 만든다): %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("docs/openapi/v1.json 이 라우트 선언표에서 생성한 결과와 다르다. make openapi 로 다시 만든다")
	}
}

type collectRegistrar struct{ decls []string }

func (c *collectRegistrar) Handle(method, pattern string, perm contract.Permission, h http.Handler) {
	c.decls = append(c.decls, method+" "+pattern+" "+string(perm))
}
func (c *collectRegistrar) HandleFunc(method, pattern string, perm contract.Permission, h http.HandlerFunc) {
	c.Handle(method, pattern, perm, h)
}

type nopRead struct{ domain.APIReadService }

// 라우트·문서가 양방향으로 같다: 등록된 모든 라우트가 문서에 있고 문서의 모든 경로가 등록된다. 인증 라우트는 모두 api:access 를 선언한다.
func TestRoutesAndOpenAPIAgree(t *testing.T) {
	reg := &collectRegistrar{}
	New(Options{Read: nopRead{}}).Register(reg)
	var doc struct {
		Paths map[string]map[string]struct {
			Scope string `json:"x-required-scope"`
		} `json:"paths"`
		XErrorCodes []string `json:"x-error-codes"`
	}
	b, _ := OpenAPI()
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	registered := map[string]bool{}
	for _, d := range reg.decls {
		f := strings.Fields(d)
		registered[strings.ToLower(f[0])+" "+f[1]] = true
		wantPerm := string(contract.PermAPIAccess)
		if f[1] == openAPIPath {
			wantPerm = string(contract.PermPublic)
		}
		if f[2] != wantPerm {
			t.Errorf("%s 의 권한 선언 %s(기대 %s)", d, f[2], wantPerm)
		}
	}
	documented := map[string]bool{}
	for path, ops := range doc.Paths {
		for method := range ops {
			documented[method+" "+path] = true
		}
	}
	for k := range registered {
		if !documented[k] {
			t.Errorf("등록된 라우트가 문서에 없다: %s", k)
		}
	}
	for k := range documented {
		if !registered[k] {
			t.Errorf("문서에만 있는 경로: %s", k)
		}
	}
	if len(registered) != len(Routes) {
		t.Errorf("Routes %d 개 중 %d 개만 등록됐다(Register 의 switch 에 빠진 경로)", len(Routes), len(registered))
	}
	// 문서가 말하는 범위 이름은 코드의 범위 표와 같다.
	docScopes := map[string]bool{}
	for _, ops := range doc.Paths {
		for _, op := range ops {
			if op.Scope != "" {
				docScopes[op.Scope] = true
			}
		}
	}
	for s := range docScopes {
		if _, ok := ScopeByName(s); !ok {
			t.Errorf("문서의 범위 %q 가 코드 범위 표에 없다", s)
		}
	}
	for _, s := range Scopes {
		if s.Name != ScopeChangesRead && !docScopes[s.Name] {
			t.Errorf("범위 %q 를 쓰는 라우트가 없다", s.Name)
		}
	}
}

// 오류 코드 목록이 코드와 문서에서 같다: 코드가 쓰는 모든 코드는 목록에 있고, 문서가 같은 목록을 싣는다.
func TestErrorCodesMatch(t *testing.T) {
	re := regexp.MustCompile(`(?:writeError|fail)\([^)]*?"([a-z_]+)"`)
	known := map[string]bool{}
	for _, c := range ErrorCodes {
		known[c] = true
	}
	for _, f := range []string{"service.go", "openapi.go", "routes.go"} {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range re.FindAllStringSubmatch(string(b), -1) {
			if !known[m[1]] {
				t.Errorf("%s 가 쓰는 오류 코드 %q 가 ErrorCodes 에 없다", f, m[1])
			}
		}
	}
	var doc struct {
		X []string `json:"x-error-codes"`
	}
	b, _ := OpenAPI()
	_ = json.Unmarshal(b, &doc)
	got, want := append([]string(nil), doc.X...), append([]string(nil), ErrorCodes...)
	sort.Strings(got)
	sort.Strings(want)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("문서의 오류 코드 %v 가 코드 목록 %v 와 다르다", got, want)
	}
}

// AC-4204: 어떤 범위 조합으로도 주체가 위험 권한·mask:bypass·role:manage 를 갖지 못하고, 내부 권한 api:access 는 항상 있다.
func TestScopesNeverGrantRiskyPermissions(t *testing.T) {
	var all []string
	for _, s := range Scopes {
		all = append(all, s.Name)
		for _, p := range s.Perms {
			for _, risky := range contract.HighRiskPermissions {
				if p == risky {
					t.Errorf("범위 %s 에 위험 권한 %s 가 붙어 있다", s.Name, p)
				}
			}
			if p == contract.PermMaskBypass || p == contract.PermRoleManage {
				t.Errorf("범위 %s 에 %s 가 붙어 있다", s.Name, p)
			}
			if !p.IsKnown() {
				t.Errorf("범위 %s 의 권한 %s 가 알려진 권한이 아니다", s.Name, p)
			}
		}
	}
	perms, err := PermsFor(all)
	if err != nil {
		t.Fatal(err)
	}
	for _, risky := range contract.HighRiskPermissions {
		if perms.Has(risky) {
			t.Errorf("모든 범위를 합쳐도 위험 권한 %s 가 생기면 안 된다", risky)
		}
	}
	if !perms.Has(contract.PermAPIAccess) {
		t.Error("api:access 가 없다")
	}
	if p, err := PermsFor(nil); err != nil || !p.Has(contract.PermAPIAccess) || len(p) != 1 {
		t.Errorf("범위가 없으면 api:access 뿐이다: %v %v", p, err)
	}
	if _, err := PermsFor([]string{"vendors:write_everything"}); !errors.Is(err, contract.ErrValidation) {
		t.Errorf("알 수 없는 범위: %v", err)
	}
	if got := ResourcesFor([]string{"vendors:read", "changes:read", "vendors:read"}); len(got) != 1 || got[0] != "vendor" {
		t.Errorf("자원: %v", got)
	}
}

type fixedAuth struct {
	c   Caller
	err error
}

func (f fixedAuth) AuthenticateAPI(context.Context, *http.Request) (Caller, error) { return f.c, f.err }

// 인증 미들웨어 자리: /api/v1 만 인증하고 다른 경로는 건드리지 않으며, 인증 실패를 고정 코드로 옮긴다.
func TestWrapAuthentication(t *testing.T) {
	hit := 0
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit++
		if _, ok := contract.PrincipalFrom(r.Context()); ok {
			w.Header().Set("X-Has-Principal", "1")
		}
		w.WriteHeader(200)
	})
	do := func(a Authenticator, path string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		New(Options{Auth: a}).Wrap(next).ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		return w
	}
	if !IsAPIPath("/api/v1/items") || IsAPIPath("/api/vendors") || IsAPIPath("/api/v10/x") || !IsAPIPath("/api/v1") {
		t.Error("IsAPIPath")
	}
	ok := fixedAuth{c: Caller{Principal: contract.Principal{UserID: 7, Channel: contract.ChannelAPI}, Scopes: []string{"items:read"}}}
	if w := do(ok, "/api/v1/items"); w.Code != 200 || w.Header().Get("X-Has-Principal") != "1" {
		t.Errorf("통과: %d", w.Code)
	}
	if w := do(fixedAuth{err: contract.ErrUnauthenticated}, "/api/v1/items"); w.Code != 401 || w.Header().Get("WWW-Authenticate") != "Bearer" {
		t.Errorf("401: %d", w.Code)
	}
	if w := do(fixedAuth{err: contract.ErrForbidden}, "/api/v1/items"); w.Code != 403 {
		t.Errorf("403: %d", w.Code)
	}
	if w := do(fixedAuth{err: ErrRateLimited{RetryAfterSeconds: 9}}, "/api/v1/items"); w.Code != 429 || w.Header().Get("Retry-After") != "9" {
		t.Errorf("429: %d", w.Code)
	}
	if w := do(fixedAuth{err: errors.New("db down: secret detail")}, "/api/v1/items"); w.Code != 500 || strings.Contains(w.Body.String(), "secret") {
		t.Errorf("알 수 없는 오류의 상세를 숨겨야 한다: %d %s", w.Code, w.Body.String())
	}
	if w := do(nil, "/api/v1/items"); w.Code != 503 {
		t.Errorf("인증 구현이 없으면 열리지 않는다(fail-closed): %d", w.Code)
	}
	hit = 0
	if w := do(fixedAuth{err: contract.ErrUnauthenticated}, openAPIPath); w.Code != 200 || hit != 1 {
		t.Errorf("OpenAPI 는 인증 없이: %d", w.Code)
	}
	if w := do(fixedAuth{err: contract.ErrUnauthenticated}, "/api/vendors"); w.Code != 200 {
		t.Errorf("다른 경로는 건드리지 않는다: %d", w.Code)
	}
}
