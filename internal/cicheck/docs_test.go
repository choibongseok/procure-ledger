package cicheck

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// 첫 화면 문서(B16). README·docs 가 적은 명령·환경변수·경로가 실재해야 한다.
var docFiles = []string{"README.md", "docs/architecture.md", "docs/mcp-connect.md", "docs/deploy-checklist.md", "docs/operations.md", "docs/integrations.md", "docs/tax-invoice-guide.md"}

func readDoc(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile(root(t, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("%s: %v", rel, err)
	}
	return string(b)
}

// makefileTargets 는 Makefile 의 `이름:` 줄(들여쓰기 없는 규칙)의 이름들이다.
func makefileTargets(t *testing.T) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	for _, m := range regexp.MustCompile(`(?m)^([a-zA-Z][a-zA-Z0-9_-]*):`).FindAllStringSubmatch(readDoc(t, "Makefile"), -1) {
		out[m[1]] = true
	}
	return out
}

// 문서가 `make 타깃` 으로 적은 것은 Makefile 에 있어야 하고, README 는 사용자용 타깃을 빠짐없이 적는다.
func TestDocsMakeTargetsExist(t *testing.T) {
	targets := makefileTargets(t)
	re := regexp.MustCompile("`make ([a-z][a-z0-9-]*)`")
	// 코드블록(```) 안의 줄에서 make 를 부르는 곳: 줄 머리(앞에 환경변수 대입이 있어도 됨)의 `make 타깃`.
	block := regexp.MustCompile(`^\s*(?:[A-Za-z_][A-Za-z0-9_]*=\S*\s+)*make\s+([a-z][a-z0-9-]*)`)
	for _, f := range docFiles {
		doc := readDoc(t, f)
		for _, m := range re.FindAllStringSubmatch(doc, -1) {
			if !targets[m[1]] {
				t.Errorf("%s 가 적은 `make %s` 가 Makefile 에 없음", f, m[1])
			}
		}
		inBlock := false
		for _, line := range strings.Split(doc, "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "```") {
				inBlock = !inBlock
				continue
			}
			if !inBlock {
				continue
			}
			if m := block.FindStringSubmatch(line); m != nil && !targets[m[1]] {
				t.Errorf("%s 의 코드블록이 부르는 make %s 가 Makefile 에 없음", f, m[1])
			}
		}
	}
	readme := readDoc(t, "README.md")
	for name := range targets {
		if name == "web" { // build 가 부르는 내부 단계
			continue
		}
		if !strings.Contains(readme, "`make "+name+"`") {
			t.Errorf("README 에 `make %s` 설명이 없음", name)
		}
	}
}

// 문서가 적은 PL_* 환경변수는 .env.example 에 실재해야 한다.
func TestDocsEnvVarsExist(t *testing.T) {
	example := readDoc(t, ".env.example")
	re := regexp.MustCompile(`\bAX_[A-Z0-9_]+\b`)
	for _, f := range docFiles {
		for _, name := range re.FindAllString(readDoc(t, f), -1) {
			if !regexp.MustCompile(`(?m)^` + name + `=`).MatchString(example) {
				t.Errorf("%s 가 적은 %s 가 .env.example 에 없음", f, name)
			}
		}
	}
	// 비밀값 예시: 문서에 토큰 원문(plp_ 뒤에 긴 문자열)이 있으면 안 된다.
	leak := regexp.MustCompile(`(plp|pli|pls|whsec)_[A-Za-z0-9_-]{12,}`)
	for _, f := range docFiles {
		if leak.MatchString(readDoc(t, f)) {
			t.Errorf("%s 에 개인 토큰 원문처럼 보이는 값이 있음", f)
		}
	}
}

// 문서의 상대 링크(마크다운 링크)와 이미지는 대상 파일이 실재해야 한다.
func TestDocsLinksResolve(t *testing.T) {
	re := regexp.MustCompile(`\]\(([^)#\s]+)(?:#[^)]*)?\)`)
	for _, f := range docFiles {
		dir := filepath.Dir(filepath.FromSlash(f))
		for _, m := range re.FindAllStringSubmatch(readDoc(t, f), -1) {
			target := m[1]
			if strings.Contains(target, "://") || strings.HasPrefix(target, "mailto:") {
				continue
			}
			if _, err := os.Stat(root(t, filepath.Join(dir, filepath.FromSlash(target)))); err != nil {
				t.Errorf("%s 의 링크 %s 대상이 없음", f, target)
			}
		}
	}
}

// 문서가 적은 저장소 경로(`internal/...`, `cmd/...`, `scripts/...`, `docs/...`, `web/...`, `evals/...`, `migrations/...`)가 실재해야 한다.
func TestDocsPathsExist(t *testing.T) {
	re := regexp.MustCompile("`((?:internal|cmd|scripts|docs|web|evals|migrations|\\.github)/[A-Za-z0-9_./*{}-]*[A-Za-z0-9_}])`")
	for _, f := range docFiles {
		for _, m := range re.FindAllStringSubmatch(readDoc(t, f), -1) {
			p := m[1]
			if strings.ContainsAny(p, "*{}") {
				continue
			}
			if _, err := os.Stat(root(t, filepath.FromSlash(p))); err != nil {
				t.Errorf("%s 가 적은 경로 %s 가 없음", f, p)
			}
		}
	}
}

// 연결 문서가 반드시 알려야 할 운영 사실이 빠지지 않게 한다.
func TestMCPConnectDocCoversOperations(t *testing.T) {
	doc := readDoc(t, "docs/mcp-connect.md")
	for _, want := range []string{
		"PL_MCP_RATE_PER_MIN", "PL_MCP_RATE_PER_USER_PER_MIN", "모두 0 이면 한도가 꺼진 채", "PL_MAX_ACTIVE_TOKENS",
		"폐기", "stdio", "다음 호출부터", "Authorization: Bearer", "claude mcp add", "claude_desktop_config.json", "mcpServers",
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("docs/mcp-connect.md 에 %q 설명이 없음", want)
		}
	}
	arch := readDoc(t, "docs/architecture.md")
	for _, want := range []string{"불변 트리거", "2N", "정산", "externallyVerified", "Anthropic 실호출"} {
		if !strings.Contains(arch, want) {
			t.Errorf("docs/architecture.md 알려진 한계에 %q 가 없음", want)
		}
	}
}

// 코드블록 안의 make 검사가 실제로 돈다: README 의 첫 코드블록은 make build 를 부르므로 검사 대상이 하나 이상 있어야 한다.
func TestDocsCodeBlockMakeCallsAreChecked(t *testing.T) {
	n := 0
	inBlock := false
	for _, line := range strings.Split(readDoc(t, "README.md"), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			inBlock = !inBlock
			continue
		}
		if inBlock && strings.HasPrefix(strings.TrimSpace(line), "make ") {
			n++
		}
	}
	if n == 0 {
		t.Fatal("README 코드블록에 make 호출이 없어 코드블록 검사가 아무것도 보지 않음")
	}
}
