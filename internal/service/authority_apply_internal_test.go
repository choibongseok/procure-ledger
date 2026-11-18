package service

import (
	"strings"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/contract/domain"
)

func lineOf(kinds ...domain.StepKind) []domain.LineStep {
	var out []domain.LineStep
	for i, k := range kinds {
		role := domain.RoleApprove
		if k == "agree" {
			role = domain.RoleAgree
		}
		out = append(out, domain.LineStep{Order: i + 1, Kind: k, Title: string(k), Role: role, Stage: i + 1, Required: true})
	}
	return out
}

// 전결 규정 적용 표 테스트(AC-300): 최종 결재 단계 판정, 뒤 흐름 단계 건너뜀, 필수 합의와 상충하면 포기·경고.
func TestDecideAuthority(t *testing.T) {
	rule := func(level int) domain.AuthorityRule {
		return domain.AuthorityRule{ID: 7, FinalLevel: level, PolicySlug: "approval-authority", ClauseNo: "제2조"}
	}
	cases := []struct {
		name    string
		level   int
		steps   []domain.LineStep
		mod     func([]domain.LineStep)
		final   int
		skipped []int
		warn    bool
		none    bool
	}{
		{name: "부서장 전결이면 재무가 건너뜀", level: 2, steps: lineOf("team_lead", "dept_head", "finance"), final: 2, skipped: []int{3}},
		{name: "팀장 전결이면 뒤 결재 전부 건너뜀", level: 1, steps: lineOf("team_lead", "dept_head", "finance"), final: 1, skipped: []int{2, 3}},
		{name: "마지막 단계가 최종이면 건너뛸 것이 없음", level: 2, steps: lineOf("team_lead", "dept_head"), final: 2},
		{name: "층 이상의 단계가 없으면 적용 안 함", level: 4, steps: lineOf("team_lead", "dept_head", "finance"), none: true},
		{name: "재무 풀은 최종 결재 단계가 될 수 없음", level: 1, steps: lineOf("finance"), none: true},
		{name: "필수가 아닌 뒤 합의는 건너뜀", level: 2, steps: lineOf("team_lead", "dept_head", "agree"), mod: func(s []domain.LineStep) { s[2].Required = false }, final: 2, skipped: []int{3}},
		{name: "필수 합의가 뒤에 있으면 포기하고 경고", level: 2, steps: lineOf("team_lead", "dept_head", "agree", "finance"), warn: true},
		{name: "앞에 있는 필수 합의는 상관없음", level: 2, steps: lineOf("agree", "team_lead", "dept_head", "finance"), final: 3, skipped: []int{4}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.mod != nil {
				c.mod(c.steps)
			}
			var notes []string
			n := decideAuthority(rule(c.level), c.steps, &notes)
			if c.none {
				if n != nil {
					t.Fatalf("적용되면 안 됨: %+v", n)
				}
				return
			}
			if n == nil {
				t.Fatal("적용돼야 함")
			}
			if c.warn {
				if n.Warning == "" || n.FinalOrder != 0 || len(n.SkippedOrders) != 0 || len(notes) != 1 || !strings.Contains(n.Warning, "전결 규정을 적용하지 않았습니다") {
					t.Fatalf("포기·경고: %+v %v", n, notes)
				}
				for _, s := range c.steps {
					if s.Skipped {
						t.Fatalf("포기했는데 단계가 건너뜀: %+v", s)
					}
				}
				return
			}
			if n.FinalOrder != c.final || n.Slug != "approval-authority" || n.ClauseNo != "제2조" || len(n.SkippedOrders) != len(c.skipped) {
				t.Fatalf("판정: %+v 기대 최종 %d 건너뜀 %v", n, c.final, c.skipped)
			}
			for i, o := range c.skipped {
				if n.SkippedOrders[i] != o || !c.steps[o-1].Skipped || c.steps[o-1].SkipNote != "전결" {
					t.Fatalf("건너뜀 표시: %+v", c.steps)
				}
			}
		})
	}
}
