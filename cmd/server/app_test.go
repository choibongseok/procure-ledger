package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/config"
	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/fake"
)

func testRoutes(t *testing.T) (*contract.RouteTable, http.Handler) {
	t.Helper()
	d := fake.NewBundle().Deps()
	rt := buildRoutes(config.Config{}, d)
	return rt, handlerFor(rt, d)
}

// 라우트 권한 선언 누락 검사. 등록기가 Permission 을 필수 인자로 받으므로 컴파일 단계에서 막히고,
// 이 테스트는 등록된 전 라우트가 알려진 권한을 선언했는지, 선언대로 강제되는지를 순회로 확인한다.
func TestPermissionDeclarationsAllRoutes(t *testing.T) {
	rt, h := testRoutes(t)
	routes := rt.Routes()
	if len(routes) == 0 {
		t.Fatal("등록된 라우트가 없음")
	}
	if problems := contract.CheckRoutes(routes); len(problems) != 0 {
		t.Fatalf("권한 선언 문제: %v", problems)
	}
	for _, r := range routes {
		if r.Permission == contract.PermPublic {
			continue
		}
		path := strings.NewReplacer("{id}", "1", "{path...}", "x").Replace(r.Pattern)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(r.Method, path, nil))
		if w.Code != http.StatusUnauthorized {
			t.Errorf("%s %s: 미인증 요청이 %d (401 기대)", r.Method, r.Pattern, w.Code)
		}
	}
}

func TestHealthzIsPublic(t *testing.T) {
	_, h := testRoutes(t)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/healthz", nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"ok"`) {
		t.Fatalf("healthz: %d %s", w.Code, w.Body.String())
	}
}

func TestUnknownAPIPathIs404NotSPA(t *testing.T) {
	_, h := testRoutes(t)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/api/nope", nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("/api/nope: %d", w.Code)
	}
}
