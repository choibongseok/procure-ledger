package service

import (
	"regexp"
	"sort"
	"strings"
	"testing"
)

// B29 소스 검사(AC-835·AC-814): 문서 검색의 권한 조건이 검색어보다 앞서 WHERE 에 들어가고, 폐기 표시(disposed_at 채우기)와 폐기 호출이 보존 서비스 한 곳뿐이다.

func checkSearchScopeFirst(files map[string]srcFile) []string {
	f, ok := files["internal/repository/search_repo.go"]
	if !ok {
		return []string{"internal/repository/search_repo.go 가 없다"}
	}
	c := strings.Index(f.code, "scope.Cond()")
	l := strings.Index(f.code, "search_text) LIKE")
	if c < 0 || l < 0 || c > l {
		return []string{"문서 검색은 권한 조건(scope.Cond)을 검색어 조건보다 먼저 WHERE 에 넣어야 한다"}
	}
	return nil
}

var disposeCall = regexp.MustCompile(`\.Dispose\(`)
var disposeMark = regexp.MustCompile(`(?i)SET\s+disposed_at\s*=`)

func checkDisposeOnlyByRetention(files map[string]srcFile) []string {
	var bad []string
	for name, f := range files {
		switch name {
		case "internal/repository/retention_repo.go", "internal/service/retention_service.go",
			"internal/handler/retention_register.go", "internal/contract/fake/erp.go":
			continue // 폐기 저장소·보존 서비스·서비스 인터페이스를 부르는 라우트·가짜 구현
		}
		if disposeCall.MatchString(f.code) {
			bad = append(bad, name+": 폐기를 호출한다")
		}
		for _, l := range f.literals {
			if disposeMark.MatchString(l.text) {
				bad = append(bad, name+": 폐기 표시(disposed_at)를 채운다")
			}
		}
	}
	sort.Strings(bad)
	return bad
}

func TestSearchQueryPutsScopeFirst(t *testing.T) {
	if bad := checkSearchScopeFirst(repoSources(t)); len(bad) > 0 {
		t.Fatalf("%v", bad)
	}
}

func TestDisposeIsCalledOnlyByRetention(t *testing.T) {
	if bad := checkDisposeOnlyByRetention(repoSources(t)); len(bad) > 0 {
		t.Fatalf("%v", bad)
	}
}

func TestGuardB29ChecksFailOnViolation(t *testing.T) {
	swapped := parseAll(map[string]string{"internal/repository/search_repo.go": "x := `p.search_text LIKE ?`\n_ = scope.Cond()"})
	if len(checkSearchScopeFirst(swapped)) == 0 {
		t.Fatal("순서가 바뀐 검색을 못 잡는다")
	}
	viol := parseAll(map[string]string{"internal/service/other.go": "func f() { x.Dispose(ctx) }\nvar q = `UPDATE purchase_requests SET disposed_at = ?`"})
	if len(checkDisposeOnlyByRetention(viol)) != 2 {
		t.Fatal("폐기 우회를 못 잡는다")
	}
}
