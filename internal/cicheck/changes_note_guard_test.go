package cicheck

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// B44 후속: 변경 피드 기록(changes.Note)은 요청의 현재 법인(context)을 쓴다. 요청 밖에서 도는 배경 작업·도구(저장소 밖)는 현재 법인이 없어
// 레코드가 엉뚱한 법인(기본 법인)으로 기록될 수 있으므로 changes.NoteIn(레코드의 법인)을 써야 한다.
// 저장소(internal/repository)는 서비스가 요청 context 로 부르므로 Note 를 쓸 수 있고, 그 밖의 곳에 새 changes.Note 호출이 생기면 이 시험이 막는다.
func TestChangesNoteOnlyInRequestPaths(t *testing.T) {
	var bad []string
	for _, dir := range []string{"internal", "cmd"} {
		_ = filepath.Walk(root(t, dir), func(p string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
				return nil
			}
			rel, _ := filepath.Rel(root(t, "."), p)
			rel = filepath.ToSlash(rel)
			if strings.HasPrefix(rel, "internal/repository/") || strings.HasPrefix(rel, "internal/changes/") {
				return nil
			}
			if strings.Contains(string(mustRead(t, p)), "changes.Note(") {
				bad = append(bad, rel)
			}
			return nil
		})
	}
	sort.Strings(bad)
	if len(bad) > 0 {
		t.Fatalf("저장소 밖에서 changes.Note 를 쓴다(배경 작업은 changes.NoteIn 으로 레코드의 법인을 넘긴다): %s", strings.Join(bad, ", "))
	}
}
