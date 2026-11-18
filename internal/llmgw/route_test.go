package llmgw_test

import (
	"math/rand"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/llmgw"
)

func rule(id int64, order int, pattern string, dept int64, role string) llmgw.RouteRule {
	return llmgw.RouteRule{ID: id, SortOrder: order, PurposePattern: pattern, DepartmentID: dept, Role: role, PrimaryProviderID: id, Active: true}
}

// B45 5절: 부서 > 용도 구체성(정확 > 긴 접두 > *) > 역할 > 순서 > 번호. 규칙이 없으면 ok=false(기본 경로).
func TestPickRouteOrder(t *testing.T) {
	subj := llmgw.Subject{Purpose: "docverify.extract", DepartmentID: 10, RoleKeys: []string{"staff"}}
	cases := []struct {
		name  string
		rules []llmgw.RouteRule
		want  int64
	}{
		{"규칙 없음", nil, 0},
		{"전체 하나", []llmgw.RouteRule{rule(1, 0, "*", 0, "")}, 1},
		{"정확 > 접두 > 전체", []llmgw.RouteRule{rule(1, 0, "*", 0, ""), rule(2, 0, "docverify.*", 0, ""), rule(3, 0, "docverify.extract", 0, "")}, 3},
		{"긴 접두가 더 구체적", []llmgw.RouteRule{rule(1, 0, "docverify.*", 0, ""), rule(2, 0, "docverify.ex*", 0, "")}, 1},
		{"부서 지정이 용도 구체성보다 앞", []llmgw.RouteRule{rule(1, 0, "docverify.extract", 0, ""), rule(2, 0, "*", 10, "")}, 2},
		{"다른 부서 규칙은 후보가 아님", []llmgw.RouteRule{rule(1, 0, "*", 20, ""), rule(2, 5, "*", 0, "")}, 2},
		{"역할 지정이 앞(같은 용도)", []llmgw.RouteRule{rule(1, 0, "docverify.*", 0, ""), rule(2, 9, "docverify.*", 0, "staff")}, 2},
		{"가진 역할이 아니면 후보 아님", []llmgw.RouteRule{rule(1, 0, "docverify.*", 0, "finance")}, 0},
		{"순서가 작은 것", []llmgw.RouteRule{rule(1, 5, "docverify.*", 0, ""), rule(2, 1, "docverify.*", 0, "")}, 2},
		{"순서가 같으면 번호가 작은 것", []llmgw.RouteRule{rule(7, 1, "docverify.*", 0, ""), rule(4, 1, "docverify.*", 0, "")}, 4},
		{"용도가 안 맞음", []llmgw.RouteRule{rule(1, 0, "policy.*", 0, "")}, 0},
	}
	for _, c := range cases {
		got, ok := llmgw.PickRoute(c.rules, subj)
		if c.want == 0 {
			if ok {
				t.Errorf("%s: 규칙이 맞으면 안 된다: %+v", c.name, got)
			}
			continue
		}
		if !ok || got.ID != c.want {
			t.Errorf("%s: 기대 %d, 실제 %d(%v)", c.name, c.want, got.ID, ok)
		}
	}
	// 꺼진 규칙은 후보가 아니다.
	off := rule(1, 0, "*", 0, "")
	off.Active = false
	if _, ok := llmgw.PickRoute([]llmgw.RouteRule{off}, subj); ok {
		t.Error("꺼진 규칙이 맞았다")
	}
}

// 속성: 같은 규칙 집합은 입력 순서와 무관하게 같은 결과를 준다.
func TestPickRouteIsOrderIndependent(t *testing.T) {
	rng := rand.New(rand.NewSource(45))
	patterns := []string{"*", "docverify.*", "docverify.extract", "policy.*", "docverify.ex*"}
	for iter := 0; iter < 200; iter++ {
		var rules []llmgw.RouteRule
		for i := 0; i < 1+rng.Intn(7); i++ {
			dept := int64(0)
			if rng.Intn(3) == 0 {
				dept = 10
			}
			role := ""
			if rng.Intn(3) == 0 {
				role = "staff"
			}
			rules = append(rules, rule(int64(i+1), rng.Intn(3), patterns[rng.Intn(len(patterns))], dept, role))
		}
		subj := llmgw.Subject{Purpose: "docverify.extract", DepartmentID: 10, RoleKeys: []string{"staff"}}
		want, wok := llmgw.PickRoute(rules, subj)
		shuf := append([]llmgw.RouteRule(nil), rules...)
		rng.Shuffle(len(shuf), func(i, j int) { shuf[i], shuf[j] = shuf[j], shuf[i] })
		got, ok := llmgw.PickRoute(shuf, subj)
		if ok != wok || got.ID != want.ID {
			t.Fatalf("순서에 따라 결과가 달라졌다: %+v vs %+v", want, got)
		}
	}
}
