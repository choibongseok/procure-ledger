package access

// 권한 행렬 골든. contract.RolePermissions 와 DB 기본 템플릿이 이것과 같아야 한다(TestTemplatesMatchGoldenAndCode).
// 다시 만들려면 UPDATE_GOLDEN=1 로 돌리되, 그것은 기본 템플릿의 권한을 바꾸는 일이므로 사유가 명세에 있어야 한다.

import (
	"encoding/json"
	"os"
	"sort"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/contract"
)

const goldenPath = "testdata/role_matrix.golden.json"

type golden struct {
	Roles map[string][]string `json:"roles"`
}

func currentMatrix() golden {
	g := golden{Roles: map[string][]string{}}
	for _, r := range contract.Roles {
		var ps []string
		for _, p := range contract.AllPermissions {
			if contract.RoleHas(r, p) {
				ps = append(ps, string(p))
			}
		}
		sort.Strings(ps)
		g.Roles[string(r)] = ps
	}
	return g
}

func TestUpdateGolden(t *testing.T) {
	if os.Getenv("UPDATE_GOLDEN") != "1" {
		t.Skip()
	}
	b, _ := json.MarshalIndent(currentMatrix(), "", "  ")
	if err := os.WriteFile(goldenPath, append(b, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
}
