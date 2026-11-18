package cicheck

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// B28 AC-701: CSV 수식 주입 방지 규칙(= + - @ 탭 CR 로 시작하는 칸 앞에 ')은 내보내기 공용 부품(internal/export.CSVSafe) 한 곳에만 있다.
// 조직 CSV 의 서버 생성 파일(internal/org)은 그 함수를 부르고, 같은 글자 목록을 가진 파일은 두 곳(공용 함수, 입력 검증용 formulaStart)뿐이다.
func TestCSVFormulaRuleHasOneImplementation(t *testing.T) {
	var hits []string
	base := root(t, "internal")
	err := filepath.WalkDir(base, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		b, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		if strings.Contains(string(b), "case '=', '+', '-', '@'") {
			rel, _ := filepath.Rel(base, path)
			hits = append(hits, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"export/csv.go": true, "org/import_csv.go": true}
	for _, h := range hits {
		if !want[h] {
			t.Errorf("CSV 수식 접두 규칙이 %s 에 따로 구현됨(internal/export.CSVSafe 를 부를 것)", h)
		}
	}
	if len(hits) != len(want) {
		t.Errorf("규칙을 가진 파일 %v (기대 %d곳)", hits, len(want))
	}
	b, err := os.ReadFile(root(t, "internal", "org", "import_csv.go"))
	if err != nil || !strings.Contains(string(b), "export.CSVSafe(v)") {
		t.Error("internal/org 의 csvSafe 가 공용 export.CSVSafe 를 부르지 않음")
	}
}
