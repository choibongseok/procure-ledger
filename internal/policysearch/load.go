package policysearch

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"

	"github.com/choibongseok/procure-ledger/internal/contract"
)

// LoadResult 는 폴더를 읽은 결과다. Problems 의 문서는 색인에서 빠졌다(열람 범위를 알 수 없는 문서는 아무에게도 보이지 않는다).
type LoadResult struct {
	Index       *Index
	Fingerprint string
	Problems    []string
}

// Load 는 fsys 의 루트에서 *.md(README.md 제외)를 읽어 색인을 만든다.
// Fingerprint 는 파일 이름과 내용의 해시라서, 문서가 바뀌었는지 비교해 다시 만들지 정하는 데 쓴다.
func Load(fsys fs.FS) (LoadResult, error) {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return LoadResult{}, fmt.Errorf("규정 폴더 읽기: %w", err)
	}
	h := sha256.New()
	var docs []Doc
	clauses := map[string][]Clause{}
	var problems []string
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || !strings.HasSuffix(n, ".md") || strings.EqualFold(n, "README.md") {
			continue
		}
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		raw, err := fs.ReadFile(fsys, n)
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s: 읽기 실패: %v", n, err))
			continue
		}
		h.Write([]byte(n))
		h.Write([]byte{0})
		h.Write(raw)
		h.Write([]byte{0})
		slug := strings.TrimSuffix(path.Base(n), ".md")
		d, cs, err := ParseDoc(slug, raw)
		if err == nil {
			err = checkRoles(d)
		}
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s: %v", n, err))
			continue
		}
		docs = append(docs, d)
		clauses[slug] = cs
	}
	return LoadResult{Index: NewIndex(docs, clauses), Fingerprint: hex.EncodeToString(h.Sum(nil)), Problems: problems}, nil
}

func checkRoles(d Doc) error {
	known := map[string]bool{}
	for _, r := range contract.Roles {
		known[string(r)] = true
	}
	for _, r := range d.Roles {
		if !known[r] && !(strings.Contains(r, ":") && isKnownPermission(r)) {
			return fmt.Errorf("알 수 없는 역할·권한 %q", r)
		}
	}
	return nil
}
