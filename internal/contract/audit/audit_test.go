package audit_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/contract/audit"
)

// 대상 종류 목록이 프론트(web/src/admin/targetTypes.ts)와 어긋나면 실패한다(spec 7절).
func TestTargetTypesMirrorFrontend(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "..", "..", "web", "src", "admin", "targetTypes.ts"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(src)
	for _, tt := range audit.TargetTypes {
		if !strings.Contains(text, "  "+string(tt)+": ") {
			t.Errorf("targetTypes.ts 에 %q 가 없음", tt)
		}
	}
	entries := 0
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, "  ") && strings.Contains(line, `: "`) {
			entries++
		}
	}
	if entries != len(audit.TargetTypes) {
		t.Errorf("프론트 항목 %d != Go 항목 %d", entries, len(audit.TargetTypes))
	}
}
