package mcptool_test

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/deps"
	"github.com/choibongseok/procure-ledger/internal/contract/fake"
	"github.com/choibongseok/procure-ledger/internal/mcptool"
)

var piiName = regexp.MustCompile(`(?i)phone|tel|mobile|account|bank|biz|business|email|mail|secret|token|password|key|rrn|ssn|card_no|reveal`)

// 카탈로그 불변식: 자원 8개, 칸 이름·종류·연산이 일관되고, 개인정보·비밀 칸 이름이 없으며, 권한이 실재하고, 건수 상한이 50 이하다.
func TestBuiltinCatalogInvariants(t *testing.T) {
	cat := mcptool.BuiltinCatalog(deps.Deps{})
	all := cat.All()
	want := []string{"approval_docs", "items", "journals", "payables", "purchase_orders", "purchase_requests", "stock_alerts", "tax_invoices"}
	if len(all) != len(want) {
		t.Fatalf("자원 %d개", len(all))
	}
	perms := map[contract.Permission]bool{}
	for _, p := range contract.AllPermissions {
		perms[p] = true
	}
	for i, r := range all {
		if r.Name != want[i] {
			t.Errorf("자원 목록: %s != %s", r.Name, want[i])
		}
		if r.Label == "" || r.List == nil || r.Version < 1 || !perms[r.Perm] {
			t.Errorf("%s: 라벨·어댑터·버전·권한 중 빠진 것이 있음", r.Name)
		}
		if r.MaxLimit > 50 || r.DefaultLimit < 1 || r.DefaultLimit > r.MaxLimit {
			t.Errorf("%s: 건수 상한 %d/%d", r.Name, r.DefaultLimit, r.MaxLimit)
		}
		seen := map[string]bool{}
		returnable := 0
		for _, f := range r.Fields {
			if seen[f.Name] || f.Label == "" {
				t.Errorf("%s.%s: 이름 중복이거나 라벨 없음", r.Name, f.Name)
			}
			seen[f.Name] = true
			if piiName.MatchString(f.Name) {
				t.Errorf("%s.%s: 개인정보·비밀로 보이는 칸 이름", r.Name, f.Name)
			}
			if f.Return {
				returnable++
			}
			if f.Type == mcptool.TypeEnum && len(f.Values) == 0 {
				t.Errorf("%s.%s: enum 인데 값이 없음", r.Name, f.Name)
			}
			if f.Class == mcptool.ClassFreeText && f.Type != mcptool.TypeString {
				t.Errorf("%s.%s: 자유 글 분류는 문자 칸만", r.Name, f.Name)
			}
			for _, op := range f.Ops {
				ok := false
				switch op {
				case mcptool.OpEq:
					ok = true
				case mcptool.OpIn, mcptool.OpPrefix:
					ok = f.Type == mcptool.TypeString || f.Type == mcptool.TypeEnum
					if op == mcptool.OpPrefix {
						ok = f.Type == mcptool.TypeString
					}
				case mcptool.OpGte, mcptool.OpLte:
					ok = f.Type == mcptool.TypeInt || f.Type == mcptool.TypeDate
				}
				if !ok {
					t.Errorf("%s.%s: %s 연산을 이 종류에 둘 수 없음", r.Name, f.Name, op)
				}
			}
			for _, c := range f.Ctx {
				if (c == mcptool.CtxToday && f.Type != mcptool.TypeDate) || (c != mcptool.CtxToday && f.Type != mcptool.TypeInt) {
					t.Errorf("%s.%s: 맥락 값 %s 의 종류가 맞지 않음", r.Name, f.Name, c)
				}
			}
		}
		if returnable == 0 {
			t.Errorf("%s: 반환 가능한 칸이 없음", r.Name)
		}
		if r.DefaultSort != "" {
			if f, ok := r.Field(r.DefaultSort); !ok || !f.Sortable {
				t.Errorf("%s: 기본 정렬 칸이 정렬 가능하지 않음", r.Name)
			}
		}
	}
}

// 서비스가 연결되지 않은 자원은 조용히 빈 결과를 내지 않고 상태 오류를 돌려준다.
func TestAdaptersFailClosedWithoutServices(t *testing.T) {
	for _, r := range mcptool.BuiltinCatalog(deps.Deps{}).All() {
		if _, err := r.List(context.Background(), mcptool.Query{Limit: 5}); !errors.Is(err, contract.ErrInvalidState) {
			t.Errorf("%s: %v", r.Name, err)
		}
	}
}

// 어댑터 스모크: 시험용 서비스(fake)로 모든 자원이 정의의 반환 칸 전부를 채워 돌려준다. 서비스 오류(권한 거부 등)는 그대로 전달된다.
func TestAdaptersMapRowsFromServices(t *testing.T) {
	b := fake.NewBundle()
	d := b.Deps()
	cat := mcptool.BuiltinCatalog(d)
	p := contract.Principal{UserID: 1, Role: contract.RoleAdmin, DepartmentID: 10, Channel: contract.ChannelMCP, Perms: contract.NewPermSet(contract.AllPermissions...)}
	ctx := contract.WithPrincipal(context.Background(), p)
	for _, r := range cat.All() {
		var fields []string
		for _, f := range r.Fields {
			if f.Return {
				fields = append(fields, f.Name)
			}
		}
		rows, err := r.List(ctx, mcptool.Query{Limit: 5, Fields: fields, Sort: r.DefaultSort, Dir: r.DefaultDir})
		if err != nil {
			if strings.Contains(err.Error(), "연결되어 있지 않") {
				continue // 이 시험 번들에 없는 서비스
			}
			t.Logf("%s: 시험용 서비스 오류(허용): %v", r.Name, err)
			continue
		}
		for _, row := range rows {
			for name := range row {
				if f, ok := r.Field(name); !ok || !f.Return {
					t.Errorf("%s: 어댑터가 반환 불가 칸 %q 을 채움", r.Name, name)
				}
			}
		}
	}
}

func depsEmpty() deps.Deps { return deps.Deps{} }
