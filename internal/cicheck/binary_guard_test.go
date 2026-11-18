package cicheck

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// 추적 파일은 실행 파일(빌드 산출물)이 아니고 너무 크지 않다. 루트에서 `go build ./internal/tools/...` 를 돌려 나온 11MB 바이너리가
// 한 번 실수로 커밋된 적이 있어(이력은 다시 쓰지 않는다) 같은 일이 다시 일어나지 않게 한다. 빌드 산출물은 bin/ 에만 둔다(.gitignore).
const maxTrackedBytes = 3 << 20 // 3MB. 이보다 큰 추적 파일은 허용 목록에 이유와 함께 적어야 한다.

// 크기 상한을 넘어도 되는 추적 파일(경로 → 이유). 비어 있다.
var largeTrackedOK = map[string]string{}

var binaryMagics = []struct {
	name  string
	magic []byte
}{
	{"ELF", []byte{0x7f, 'E', 'L', 'F'}},
	{"Mach-O(32)", []byte{0xfe, 0xed, 0xfa, 0xce}},
	{"Mach-O(64)", []byte{0xfe, 0xed, 0xfa, 0xcf}},
	{"Mach-O(32, 역순)", []byte{0xce, 0xfa, 0xed, 0xfe}},
	{"Mach-O(64, 역순)", []byte{0xcf, 0xfa, 0xed, 0xfe}},
	{"Mach-O(universal)", []byte{0xca, 0xfe, 0xba, 0xbe}},
	{"Windows 실행 파일", []byte{'M', 'Z'}},
}

func TestNoBinariesOrHugeFilesTracked(t *testing.T) {
	cmd := exec.Command("git", "ls-files", "-z")
	cmd.Dir = root(t)
	out, err := cmd.Output()
	if err != nil {
		t.Skipf("git 추적 목록을 읽을 수 없음: %v", err)
	}
	for _, rel := range strings.Split(strings.TrimRight(string(out), "\x00"), "\x00") {
		if rel == "" {
			continue
		}
		path := filepath.Join(root(t), filepath.FromSlash(rel))
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() {
			continue // 삭제됐거나(작업 트리) 링크
		}
		if info.Size() > maxTrackedBytes {
			if why, ok := largeTrackedOK[rel]; !ok || why == "" {
				t.Errorf("%s: 추적 파일이 %dMB 로 너무 큼(상한 %dMB). 빌드 산출물이면 bin/ 에 두고 커밋하지 마세요", rel, info.Size()>>20, maxTrackedBytes>>20)
			}
		}
		f, err := os.Open(path)
		if err != nil {
			continue
		}
		head := make([]byte, 4)
		n, _ := f.Read(head)
		f.Close()
		head = head[:n]
		for _, b := range binaryMagics {
			if bytes.HasPrefix(head, b.magic) {
				t.Errorf("%s: 실행 파일(%s)이 추적되고 있음. 빌드 산출물은 bin/ 에 두고 커밋하지 마세요(.gitignore)", rel, b.name)
			}
		}
	}
}

// .gitignore 는 루트에서 `go build` 를 돌렸을 때 나오는 산출물 이름(cmd 와 internal/tools 의 각 명령)을 막는다.
func TestGitignoreBlocksRootBuildOutputs(t *testing.T) {
	text := readDoc(t, ".gitignore")
	var names []string
	for _, dir := range []string{"cmd", "internal/tools"} {
		entries, err := os.ReadDir(root(t, filepath.FromSlash(dir)))
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			if e.IsDir() {
				names = append(names, e.Name())
			}
		}
	}
	for _, n := range names {
		if !strings.Contains(text, "\n/"+n+"\n") && !strings.HasPrefix(text, "/"+n+"\n") {
			t.Errorf(".gitignore 에 루트 빌드 산출물 /%s 가 없음", n)
		}
	}
}
