// Package cicheck 는 배포 경로 정의(워크플로·Dockerfile·체크리스트)가 깨지지 않았는지 `go test` 로 확인한다.
// 외부 도구(actionlint 등) 없이 문법과 핵심 불변식만 본다.
package cicheck

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
)

func root(t *testing.T, parts ...string) string {
	t.Helper()
	return filepath.Join(append([]string{"..", ".."}, parts...)...)
}

type step struct {
	Uses string            `yaml:"uses"`
	Run  string            `yaml:"run"`
	If   string            `yaml:"if"`
	With map[string]any    `yaml:"with"`
	Env  map[string]string `yaml:"env"`
}

type job struct {
	RunsOn any    `yaml:"runs-on"` // 문자열(호스트 러너) 또는 라벨 목록(셀프호스트)
	Needs  any    `yaml:"needs"`
	Steps  []step `yaml:"steps"`
}

type workflow struct {
	Name        string         `yaml:"name"`
	On          any            `yaml:"on"`
	Permissions map[string]any `yaml:"permissions"`
	Jobs        map[string]job `yaml:"jobs"`
	Concurrency struct {
		Group  string `yaml:"group"`
		Cancel string `yaml:"cancel-in-progress"`
	} `yaml:"concurrency"`
}

func load(t *testing.T, name string) (workflow, string) {
	t.Helper()
	b, err := os.ReadFile(root(t, ".github", "workflows", name))
	if err != nil {
		t.Fatal(err)
	}
	var w workflow
	if err := yaml.Unmarshal(b, &w); err != nil {
		t.Fatalf("%s YAML 문법 오류: %v", name, err)
	}
	return w, string(b)
}

var usesRe = regexp.MustCompile(`^[\w.-]+/[\w./-]+@[\w.-]+$`)

func TestWorkflowsAreWellFormed(t *testing.T) {
	files, _ := filepath.Glob(root(t, ".github", "workflows", "*.yml"))
	if len(files) < 2 {
		t.Fatalf("워크플로가 %d 개뿐(ci.yml, release.yml 필요)", len(files))
	}
	for _, f := range files {
		name := filepath.Base(f)
		w, raw := load(t, name)
		if w.Name == "" || w.On == nil || len(w.Jobs) == 0 {
			t.Errorf("%s: name·on·jobs 가 필요", name)
		}
		if w.Permissions == nil {
			t.Errorf("%s: 최상위 permissions 를 명시해야 함(최소 권한)", name)
		}
		for id, j := range w.Jobs {
			if j.RunsOn == nil || len(j.Steps) == 0 {
				t.Errorf("%s/%s: runs-on·steps 필요", name, id)
			}
			for i, s := range j.Steps {
				if (s.Uses == "") == (s.Run == "") {
					t.Errorf("%s/%s step %d: uses 와 run 중 정확히 하나여야 함", name, id, i)
				}
				if s.Uses != "" && !usesRe.MatchString(s.Uses) {
					t.Errorf("%s/%s step %d: uses 형식 이상 %q", name, id, i, s.Uses)
				}
			}
			switch n := j.Needs.(type) {
			case string:
				if _, ok := w.Jobs[n]; !ok {
					t.Errorf("%s/%s: needs %q 없음", name, id, n)
				}
			}
		}
		if strings.Contains(raw, "ghp_") || regexp.MustCompile(`(?i)(password|secret|token):\s*['"]?[A-Za-z0-9]{16,}`).MatchString(raw) {
			t.Errorf("%s: 비밀값이 하드코딩된 것으로 보임", name)
		}
	}
}

// runsCmd 는 run 스크립트의 한 줄이 cmd 로 끝나는지 본다(`lane-lock.sh make verify` 처럼 앞에 잠금 래퍼가 붙어도 인정).
func runsCmd(run, cmd string) bool {
	for _, line := range strings.Split(run, "\n") {
		line = strings.TrimSpace(line)
		if line == cmd || strings.HasSuffix(line, "lane-lock.sh\" "+cmd) {
			return true
		}
	}
	return false
}

// main 은 취소하지 않고 run 마다 group 이 달라야 한다(대기 run 이 밀려 취소되지 않게). 다른 ref 는 ref 단위 group 으로 취소한다.
func TestCIConcurrencyKeepsEveryMainRun(t *testing.T) {
	w, _ := load(t, "ci.yml")
	if w.Concurrency.Cancel != "${{ github.ref != 'refs/heads/main' }}" {
		t.Errorf("cancel-in-progress 는 main 에서 false 가 되는 표현식이어야 함: %q", w.Concurrency.Cancel)
	}
	if !strings.Contains(w.Concurrency.Group, "github.ref == 'refs/heads/main'") || !strings.Contains(w.Concurrency.Group, "github.sha") {
		t.Errorf("main 의 concurrency group 에 github.sha 가 들어가야 함: %q", w.Concurrency.Group)
	}
}

func TestCIRunsMakeVerify(t *testing.T) {
	w, _ := load(t, "ci.yml")
	found := false
	for _, s := range w.Jobs["verify"].Steps {
		found = found || runsCmd(s.Run, "make verify")
	}
	if !found {
		t.Error("ci.yml 의 verify 잡이 `make verify` 를 돌려야 함(로컬과 같은 정본)")
	}
}

// 브라우저 화면 스모크(B14)는 verify 와 분리된 별도 잡이다: 실패했을 때 어느 잡인지 분명하고, make verify 는 브라우저 없이 돈다.
func TestCIBrowserSmokeIsASeparateJob(t *testing.T) {
	w, _ := load(t, "ci.yml")
	ui, ok := w.Jobs["ui"]
	if !ok {
		t.Fatal("ci.yml 에 ui 잡이 필요(브라우저 화면 스모크)")
	}
	runs, uploads := false, false
	for _, s := range ui.Steps {
		if runsCmd(s.Run, "make verify-ui") {
			runs = true
		}
		if strings.HasPrefix(s.Uses, "actions/upload-artifact@") {
			uploads = true
			if s.If != "always()" {
				t.Error("스크린샷 업로드는 실패했을 때도 올라가야 함(if: always())")
			}
			if p, _ := s.With["path"].(string); !strings.Contains(p, "ui-screenshots") {
				t.Errorf("업로드 경로가 ui-screenshots 가 아님: %v", s.With["path"])
			}
		}
	}
	if !runs {
		t.Error("ui 잡이 `make verify-ui` 를 돌려야 함")
	}
	if !uploads {
		t.Error("ui 잡이 스크린샷을 아티팩트로 올려야 함")
	}
	for _, s := range w.Jobs["verify"].Steps {
		if strings.Contains(s.Run, "verify-ui") {
			t.Error("verify 잡에 verify-ui 를 넣지 않는다(잡을 분리)")
		}
	}
	if b, _ := os.ReadFile(root(t, "scripts", "verify.sh")); strings.Contains(string(b), "verify-ui") {
		t.Error("make verify(scripts/verify.sh)에 verify-ui 를 넣지 않는다")
	}
	mk, _ := os.ReadFile(root(t, "Makefile"))
	if !regexp.MustCompile(`(?m)^verify-ui:`).Match(mk) {
		t.Error("Makefile 에 verify-ui 타깃이 필요")
	}
	pkg, _ := os.ReadFile(root(t, "web", "package.json"))
	if !regexp.MustCompile(`"playwright": "\d+\.\d+\.\d+"`).Match(pkg) {
		t.Error("web/package.json devDependencies 에 playwright 가 정확한 버전으로 고정돼 있어야 함")
	}
}

func TestReleaseIsTagTriggeredAndVerifiesBeforePush(t *testing.T) {
	w, raw := load(t, "release.yml")
	if !strings.Contains(raw, `tags: ["v*"]`) && !strings.Contains(raw, "tags:\n      - v*") {
		t.Error("release.yml 은 v* 태그에서만 돌아야 함")
	}
	img, ok := w.Jobs["image"]
	if !ok || img.Needs != "verify" {
		t.Fatal("image 잡은 verify 를 needs 해야 함(검증 통과 후에만 이미지 푸시)")
	}
	pushes := false
	for _, s := range img.Steps {
		if strings.HasPrefix(s.Uses, "docker/build-push-action@") && s.With["push"] == true {
			pushes = true
		}
	}
	if !pushes {
		t.Error("image 잡에 push: true 인 build-push-action 이 필요")
	}
}

func TestDockerfileInvariants(t *testing.T) {
	b, err := os.ReadFile(root(t, "Dockerfile"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	if n := strings.Count(s, "\nFROM ") + strings.Count(s[:6], "FROM "); n < 3 {
		t.Errorf("멀티 스테이지(3단계)여야 함: %d", n)
	}
	for _, want := range []string{"CGO_ENABLED=0", "USER nonroot", "npm ci", "ENTRYPOINT"} {
		if !strings.Contains(s, want) {
			t.Errorf("Dockerfile 에 %q 가 없음", want)
		}
	}
	if regexp.MustCompile(`(?m)^\s*(ENV|ARG)[^\n]*PL_DEV_IDP`).MatchString(s) {
		t.Error("Dockerfile 이 PL_DEV_IDP 를 설정함: 운영 이미지는 dev IdP 가 꺼져 있어야 함")
	}
	if regexp.MustCompile(`(?mi)^\s*(ENV|ARG)[^\n]*(KEY|SECRET|PASSWORD)\s*=`).MatchString(s) {
		t.Error("Dockerfile 에 비밀값 설정으로 보이는 줄이 있음")
	}
	ign, err := os.ReadFile(root(t, ".dockerignore"))
	if err != nil || !strings.Contains(string(ign), ".env") {
		t.Error(".dockerignore 가 .env 를 제외해야 함")
	}
}

func TestDeployChecklistCoversOperationalDefaults(t *testing.T) {
	b, err := os.ReadFile(root(t, "docs", "deploy-checklist.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"PL_DEV_IDP", "PL_OIDC_ISSUER", "PL_BASE_URL", "PL_DB_PATH", "make verify", "롤백"} {
		if !strings.Contains(string(b), want) {
			t.Errorf("deploy-checklist.md 에 %q 가 없음", want)
		}
	}
}

// 코드가 읽는 PL_* 환경변수는 전부 .env.example 에 적혀 있어야 하고, 비밀값이 채워져 있으면 안 된다.
func TestEnvExampleListsEveryEnvVar(t *testing.T) {
	example, err := os.ReadFile(root(t, ".env.example"))
	if err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(`"(PL_[A-Z0-9_]+)"`)
	for _, f := range []string{"internal/config/config.go", "internal/bootstrap/wire_ai.go", "cmd/erp-mcp/main.go"} {
		src, err := os.ReadFile(root(t, filepath.FromSlash(f)))
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range re.FindAllStringSubmatch(string(src), -1) {
			if !regexp.MustCompile(`(?m)^` + m[1] + `=`).Match(example) {
				t.Errorf(".env.example 에 %s 가 없음(%s 에서 읽음)", m[1], f)
			}
		}
	}
	for _, secret := range []string{"PL_OIDC_CLIENT_SECRET", "PL_MCP_TOKEN", "PL_AUDIT_ANCHOR_KEY"} {
		if regexp.MustCompile(`(?m)^` + secret + `=\S`).Match(example) {
			t.Errorf(".env.example 에 %s 값이 채워져 있음", secret)
		}
	}
}

// tsMapKeys 는 auditLabels.ts 의 `export const NAME ... = { ... };` 블록의 키 집합이다.
func tsMapKeys(t *testing.T, src, name string) map[string]bool {
	t.Helper()
	i := strings.Index(src, "export const "+name)
	if i < 0 {
		t.Fatalf("auditLabels.ts 에 %s 가 없음", name)
	}
	rest := src[i:]
	j := strings.Index(rest, "\n};")
	if j < 0 {
		t.Fatalf("%s 블록의 끝을 찾지 못함", name)
	}
	keys := map[string]bool{}
	for _, m := range regexp.MustCompile(`(?m)^\s+"?([A-Za-z0-9_.:]+)"?:`).FindAllStringSubmatch(rest[:j], -1) {
		keys[m[1]] = true
	}
	return keys
}

// goSources 는 테스트가 아닌 Go 소스의 (경로, 내용) 목록이다.
func goSources(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, dir := range []string{"internal", "cmd"} {
		_ = filepath.Walk(root(t, dir), func(path string, info os.FileInfo, err error) error {
			if err == nil && !info.IsDir() && strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, "_test.go") {
				b, _ := os.ReadFile(path)
				out[path] = string(b)
			}
			return nil
		})
	}
	return out
}

// 감사 로그 화면은 내부 코드를 보이면 안 된다. 코드에 새 감사 동작·변경 키가 생기면 한국어 라벨도 함께 추가해야 한다.
func TestAuditActionsHaveKoreanLabels(t *testing.T) {
	b, err := os.ReadFile(root(t, "web", "src", "admin", "auditLabels.ts"))
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	actions, diffKeys, tools := tsMapKeys(t, src, "ACTION_LABELS"), tsMapKeys(t, src, "DIFF_KEY_LABELS"), tsMapKeys(t, src, "TOOL_LABELS")

	// 동작 이름은 "<대상>.<동작>" 형식이다. 대상 접두는 감사 대상 종류와 그 밖의 알려진 접두에서 온다.
	prefixes := []string{"llm_quota", "access", "session", "mcp", "delegation", "llm_image", "eval", "org", "hr", "valuation", "auth", "scim", "privacy", "api", "changes"}
	for _, tt := range audit.TargetTypes {
		prefixes = append(prefixes, string(tt))
	}
	exempt := map[string]bool{
		"document.sum": true, "document.arithmetic": true, // 문서 대조 근거 항목 이름(감사 동작이 아님)
		"mcp.select_tool": true,                                                                                                        // LLM 호출 목적 라벨(감사 동작이 아님)
		"card.payable":    true, "card.vat": true, "payment.bank": true, "card.explain_days": true, "card.receipt_required_over": true, // 자동 전표 규칙 키(B32, 감사 동작이 아님)
		"scim.enabled": true, "scim.group_mode": true, "scim.default_unit": true, "scim.default_rank": true, "scim.default_role": true, "scim.adopt_existing": true, // SCIM 설정 키(B40, 감사 동작이 아님)
		"privacy.retention_days": true, "privacy.digest_day": true, // 조회 기록 설정 키(B41, 감사 동작이 아님)
		"api.enabled": true, "api.rate_per_min": true, "api.write_per_min": true, "changes.keep_days": true, "changes.purged_through": true, // 연동 API·변경 피드 설정 키(B42, 감사 동작이 아님)
	}
	actionRe := regexp.MustCompile(`"((?:` + strings.Join(prefixes, "|") + `)\.[a-z_]+)"`)
	for path, text := range goSources(t) {
		for _, m := range actionRe.FindAllStringSubmatch(text, -1) {
			a := m[1]
			if exempt[a] || strings.HasPrefix(a, "mcp.") || actions[a] {
				continue
			}
			t.Errorf("%s: 감사 동작 %q 의 한국어 라벨이 web/src/admin/auditLabels.ts 의 ACTION_LABELS 에 없음", path, a)
		}
		for _, m := range regexp.MustCompile(`\{Key: *"([a-z_]+)"`).FindAllStringSubmatch(text, -1) {
			if !diffKeys[m[1]] {
				t.Errorf("%s: 변경 키 %q 의 한국어 라벨이 DIFF_KEY_LABELS 에 없음", path, m[1])
			}
		}
	}
	for name := range contract.MCPToolPermissions {
		if !tools[name] {
			t.Errorf("MCP 도구 %q 의 한국어 라벨이 TOOL_LABELS 에 없음", name)
		}
	}
	for a := range actions {
		if !strings.Contains(a, ".") {
			t.Errorf("ACTION_LABELS 키 %q 형식 이상", a)
		}
	}
}
