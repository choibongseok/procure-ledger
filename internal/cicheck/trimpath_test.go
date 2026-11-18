package cicheck

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// 빌드 산출물에 빌드한 사람의 절대 경로(홈 폴더 이름 등)가 남지 않는다: 빌드 진입점은 모두 -trimpath 를 쓰고,
// 실제로 빌드한 바이너리에 저장소의 절대 경로와 /Users/ 꼴 경로가 없다.
func TestBuildsUseTrimpath(t *testing.T) {
	for _, f := range []string{"Makefile", "Dockerfile"} {
		var any bool
		for _, line := range strings.Split(readDoc(t, f), "\n") {
			l := strings.TrimSpace(line)
			if strings.HasPrefix(l, "#") || !strings.Contains(l, "go build") {
				continue
			}
			any = true
			if !strings.Contains(l, "-trimpath") {
				t.Errorf("%s 의 go build 에 -trimpath 가 없음: %s", f, l)
			}
		}
		if !any {
			t.Errorf("%s 에서 go build 줄을 찾지 못함", f)
		}
	}
	// 워크플로·스크립트가 go build 를 직접 부르면 같은 규칙을 따른다.
	for _, dir := range []string{".github/workflows", "scripts"} {
		_ = filepath.Walk(root(t, filepath.FromSlash(dir)), func(p string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() || strings.HasSuffix(p, "_test.go") {
				return nil
			}
			for _, line := range strings.Split(string(mustRead(t, p)), "\n") {
				l := strings.TrimSpace(line)
				if strings.HasPrefix(l, "#") || !strings.Contains(l, "go build ") || strings.Contains(l, "-trimpath") {
					continue
				}
				if strings.Contains(l, "go build -o bin/") { // e2e.sh 의 설명 주석(make build 를 부른다)
					continue
				}
				t.Errorf("%s 에서 -trimpath 없는 go build: %s", p, l)
			}
			return nil
		})
	}
	if testing.Short() {
		t.Skip("바이너리 빌드는 -short 에서 건너뜀")
	}
	repo := absRoot(t)
	bin := filepath.Join(t.TempDir(), "erp-mcp")
	cmd := exec.Command("go", "build", "-trimpath", "-o", bin, "./cmd/erp-mcp")
	cmd.Dir = repo
	cmd.Env = append(os.Environ(), "GOROOT=")
	if out, err := cmd.CombinedOutput(); err != nil {
		cmd.Env = os.Environ()
		cmd2 := exec.Command("go", "build", "-trimpath", "-o", bin, "./cmd/erp-mcp")
		cmd2.Dir = repo
		if out2, err2 := cmd2.CombinedOutput(); err2 != nil {
			t.Fatalf("빌드 실패: %v\n%s\n%s", err2, out, out2)
		}
	}
	b := mustRead(t, bin)
	if strings.Contains(string(b), repo) {
		t.Errorf("바이너리에 저장소의 절대 경로(%s)가 들어 있음", repo)
	}
	if i := strings.Index(string(b), "/Users/"); i >= 0 {
		end := i + 40
		if end > len(b) {
			end = len(b)
		}
		t.Errorf("바이너리에 /Users/ 꼴 경로가 있음(위치 %d)", i)
	}
}
