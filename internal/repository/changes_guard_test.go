package repository

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// B42 AC-4208(소스 검사): 변경 피드가 추적하는 표(품목·거래처·발주·입고·전표·채무)와 재고 원장을 쓰는 SQL 문은 저장소 함수 안에만 있고,
// 그 함수는 같은 함수에서 changes.Note 를 부른다(또는 사유를 적은 `changes:exempt` 표식이 있다). 새 쓰기 경로가 피드를 빼먹고 생기지 못하게 한다.

var trackedWrite = regexp.MustCompile("(?i)(INSERT INTO|INSERT OR IGNORE INTO|INSERT OR REPLACE INTO|UPDATE|DELETE FROM)\\s+(items|vendors|purchase_orders|receipts|journals|journal_lines|payables|stock_ledger)\\b")

func goFiles(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	err := filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() && (info.Name() == "node_modules" || info.Name() == ".git") {
			return filepath.SkipDir
		}
		if !info.IsDir() && strings.HasSuffix(p, ".go") && !strings.HasSuffix(p, "_test.go") {
			out = append(out, p)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// 저장소의 쓰기 함수는 모두 changes.Note 를 부른다.
func TestTrackedWritesInRepositoryNoteChanges(t *testing.T) {
	funcStart := regexp.MustCompile(`(?m)^func `)
	n := 0
	for _, f := range goFiles(t, ".") {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		src := string(b)
		starts := funcStart.FindAllStringIndex(src, -1)
		for i, st := range starts {
			end := len(src)
			if i+1 < len(starts) {
				end = starts[i+1][0]
			}
			body := src[st[0]:end]
			if m := trackedWrite.FindString(body); m != "" {
				n++
				if !strings.Contains(body, "changes.Note(") && !strings.Contains(body, "changes.NoteIn(") && !strings.Contains(body, "changes:exempt") {
					head := body
					if j := strings.Index(head, "\n"); j > 0 {
						head = head[:j]
					}
					t.Errorf("%s: %q 를 쓰는데 changes.Note 가 없다(피드에 남기거나 `changes:exempt <사유>` 를 적는다): %s", f, m, head)
				}
			}
		}
	}
	if n < 20 {
		t.Fatalf("소스 검사가 쓰기 함수를 %d 개만 찾았다(정규식이 어긋났을 수 있다)", n)
	}
}

// 추적 표를 쓰는 SQL 문은 저장소 밖(서비스·핸들러·도구)에 있으면 안 된다. 시험용 고정 데이터 도구만 예외다.
func TestTrackedWritesOnlyInRepository(t *testing.T) {
	allowed := []string{"internal/repository/", "internal/docverify/kit/", "internal/tools/", "internal/changes/"}
	for _, root := range []string{"../../internal", "../../cmd"} {
		for _, f := range goFiles(t, root) {
			rel := filepath.ToSlash(strings.TrimPrefix(f, "../../"))
			ok := false
			for _, a := range allowed {
				if strings.HasPrefix(rel, a) {
					ok = true
				}
			}
			if ok {
				continue
			}
			b, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			if m := trackedWrite.FindString(string(b)); m != "" {
				t.Errorf("%s: 저장소 밖에서 추적 표를 쓴다(%q). 저장소 함수(changes.Note 포함)를 거쳐야 한다", rel, m)
			}
		}
	}
}
