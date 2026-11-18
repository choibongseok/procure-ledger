package cicheck

import (
	"regexp"
	"strings"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/apiv1"
)

// 연동 안내(docs/integrations.md)가 적은 API 경로·범위 이름이 실제 라우트 표(apiv1.Routes)·범위 표(apiv1.Scopes)와 일치해야 한다(B42 명세 12절).
// 코드가 바뀌면 문서도 같이 바뀌게 하는 검사다: 문서에만 있는 경로·범위는 실패하고, 코드에 있는 범위는 문서가 빠짐없이 설명해야 한다.

const integrationsDoc = "docs/integrations.md"

func TestIntegrationsDocPathsMatchRoutes(t *testing.T) {
	doc := readDoc(t, integrationsDoc)
	routes := map[string]bool{}
	for _, r := range apiv1.Routes {
		routes[r.Path] = true
	}
	re := regexp.MustCompile(`/api/v1[A-Za-z0-9_/{}.:-]*`)
	seen := 0
	for _, p := range re.FindAllString(doc, -1) {
		if strings.HasSuffix(p, "...") || p == "/api/v1" || p == "/api/v1/" {
			continue // 줄임표·접두 표기
		}
		seen++
		p = strings.TrimRight(p, ".,:")
		if routes[p] {
			continue
		}
		// 접두(예: /api/v1/imports/vendors/batches/)는 어떤 라우트의 앞부분이면 허용한다.
		prefix := false
		for r := range routes {
			if strings.HasPrefix(r, p) && (strings.HasSuffix(p, "/") || strings.HasPrefix(r[len(p):], "/")) {
				prefix = true
				break
			}
		}
		if !prefix {
			t.Errorf("%s 가 적은 경로 %s 가 라우트 표(apiv1.Routes)에 없음", integrationsDoc, p)
		}
	}
	if seen == 0 {
		t.Errorf("%s 에 /api/v1 경로가 하나도 없음", integrationsDoc)
	}
	// 문서의 라우트 중 인증이 필요한 자원(목록·변경 피드·쓰기)은 적어도 하나의 경로로 소개되어야 한다: 변경 피드와 전표는 필수.
	for _, must := range []string{"/api/v1/changes", "/api/v1/journals"} {
		if !strings.Contains(doc, must) {
			t.Errorf("%s 가 %s 를 설명하지 않음", integrationsDoc, must)
		}
	}
}

func TestIntegrationsDocScopesMatchScopeTable(t *testing.T) {
	doc := readDoc(t, integrationsDoc)
	defined := map[string]bool{}
	for _, s := range apiv1.Scopes {
		defined[s.Name] = true
		if !strings.Contains(doc, s.Name) {
			t.Errorf("%s 가 범위 %s 를 설명하지 않음(범위 표에는 있음)", integrationsDoc, s.Name)
		}
	}
	re := regexp.MustCompile(`\b[a-z_]+:(?:read|write|draft)\b`)
	for _, name := range re.FindAllString(doc, -1) {
		if !defined[name] {
			t.Errorf("%s 가 적은 범위 %s 가 범위 표(apiv1.Scopes)에 없음", integrationsDoc, name)
		}
	}
	// 라우트가 요구하는 범위는 모두 범위 표에 정의되어 있다(표에 없는 범위로는 아무도 호출할 수 없다).
	for _, r := range apiv1.Routes {
		if r.Scope != "" && !defined[r.Scope] {
			t.Errorf("라우트 %s %s 가 요구하는 범위 %s 가 범위 표에 없음", r.Method, r.Path, r.Scope)
		}
	}
}
