package cicheck

import (
	"regexp"
	"strings"
	"testing"
)

// AC-1860: 운영 매뉴얼이 명세 38 의 9절 목차의 필수 제목을 모두 갖고, README 의 환경변수 표에 있는 이름이 모두 매뉴얼 부록에 있다.
func TestOperationsManualHasRequiredSections(t *testing.T) {
	doc := readDoc(t, "docs/operations.md")
	for _, h := range []string{"## 1. 개요와 구성도", "## 2. 설치", "## 3. 업그레이드", "## 4. 백업", "## 5. 복구", "## 6. 키 관리",
		"## 7. 감사 무결성 점검", "## 8. 장애 대응", "## 9. 보안 체크", "## 10. 부록"} {
		if !strings.Contains(doc, "\n"+h+"\n") {
			t.Errorf("docs/operations.md 에 필수 제목 %q 가 없음", h)
		}
	}
	for _, kw := range []string{"설치 코드", "erp-admin backup", "erp-admin restore", "--rehearse", "rotate-key", "anchor_lost", ".pre-restore"} {
		if !strings.Contains(doc, kw) {
			t.Errorf("docs/operations.md 에 %q 설명이 없음", kw)
		}
	}
	i := strings.Index(doc, "## 10. 부록")
	if i < 0 {
		return
	}
	appendix := doc[i:]
	names := regexp.MustCompile("`(PL_[A-Z0-9_]+)`")
	have := map[string]bool{}
	for _, m := range names.FindAllStringSubmatch(appendix, -1) {
		have[m[1]] = true
	}
	// README 의 환경변수 표 줄의 첫 칸에 있는 이름
	row := regexp.MustCompile("(?m)^\\| (`PL_[^|]*)\\|")
	for _, m := range row.FindAllStringSubmatch(readDoc(t, "README.md"), -1) {
		for _, n := range names.FindAllStringSubmatch(m[1], -1) {
			if !have[n[1]] {
				t.Errorf("README 환경변수 표의 %s 가 docs/operations.md 부록에 없음", n[1])
			}
		}
	}
	// 부록의 모든 이름은 .env.example 에 있어야 한다.
	example := readDoc(t, ".env.example")
	for n := range have {
		if !regexp.MustCompile(`(?m)^` + n + `=`).MatchString(example) {
			t.Errorf("부록의 %s 가 .env.example 에 없음", n)
		}
	}
}
