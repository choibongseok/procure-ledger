package dbutil

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/dbutil/dialectscan"
)

// 방언 의존 구문 기준선(B37 5.5, 래칫). 저장소·서비스 코드의 SQLite 전용 구문은 파일별로 이 기준선보다 늘 수 없다.
// 3단계(저장소 SQL 정리)에서 줄이면 이 시험이 "기준선을 줄이세요"라고 실패하므로 같은 커밋에서 갱신한다:
//
//	PL_UPDATE_DIALECT_BASELINE=1 go test ./internal/dbutil -run TestDialectRatchet
//
// 새 파일이나 새 구문은 늘 수 없다. 꼭 필요한 예외는 기준선에 올리지 말고 dbutil.Dialect 도우미로 푼다.
const baselineFile = "dialect_baseline.json"

// ratchetIgnored 는 통계에는 세지만 기준선에서는 제외하는 구문이다: 두 DB 가 같은 뜻으로 받거나(ON CONFLICT, substr),
// 방언 차이가 의미 있는 곳이 아니라 검토 대상(LIKE 대소문자)이거나 PG 에서 별도 처리(PRAGMA/sqlite_)라서 래칫으로 막지 않는다.
var ratchetIgnored = map[string]bool{"ON CONFLICT": true, "substr(": true, "LIKE": true, "PRAGMA/sqlite_": true}

func currentCounts(t *testing.T) map[string]map[string]int {
	t.Helper()
	pkgs, err := dialectscan.GoSource(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]map[string]int{}
	for _, p := range pkgs {
		for f, m := range p.PerFile {
			kept := map[string]int{}
			for k, v := range m {
				if !ratchetIgnored[k] {
					kept[k] = v
				}
			}
			if len(kept) > 0 {
				out[f] = kept
			}
		}
	}
	return out
}

func TestDialectRatchet(t *testing.T) {
	cur := currentCounts(t)
	if os.Getenv("PL_UPDATE_DIALECT_BASELINE") == "1" {
		b, err := json.MarshalIndent(cur, "", " ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(baselineFile, append(b, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Skip("기준선을 갱신했습니다")
	}
	raw, err := os.ReadFile(baselineFile)
	if err != nil {
		t.Fatalf("기준선 파일이 없습니다: %v", err)
	}
	var base map[string]map[string]int
	if err := json.Unmarshal(raw, &base); err != nil {
		t.Fatal(err)
	}
	var files []string
	seen := map[string]bool{}
	for f := range cur {
		seen[f] = true
	}
	for f := range base {
		seen[f] = true
	}
	for f := range seen {
		files = append(files, f)
	}
	sort.Strings(files)
	var grew, shrank []string
	for _, f := range files {
		keys := map[string]bool{}
		for k := range cur[f] {
			keys[k] = true
		}
		for k := range base[f] {
			keys[k] = true
		}
		for k := range keys {
			c, b := cur[f][k], base[f][k]
			switch {
			case c > b:
				grew = append(grew, fmt.Sprintf("%s: %s %d개(기준선 %d)", f, k, c, b))
			case c < b:
				shrank = append(shrank, fmt.Sprintf("%s: %s %d개(기준선 %d)", f, k, c, b))
			}
		}
	}
	sort.Strings(grew)
	sort.Strings(shrank)
	if len(grew) > 0 {
		t.Errorf("SQLite 전용 구문이 늘었습니다. dbutil.Dialect 도우미나 공통 부분집합(ON CONFLICT DO NOTHING, IS DISTINCT FROM, RETURNING id, string_agg)을 쓰세요:\n  %v", grew)
	}
	if len(shrank) > 0 {
		t.Errorf("방언 의존 구문이 줄었습니다. 기준선을 갱신하세요(PL_UPDATE_DIALECT_BASELINE=1):\n  %v", shrank)
	}
}
