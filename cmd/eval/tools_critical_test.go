package main

import (
	"testing"

	"github.com/choibongseok/procure-ledger/evals"
)

// 도구 선택 케이스도 critical 을 가질 수 있고, 틀리면 합격률과 무관하게 불합격이다(문서 제목 속 지시문 케이스).
func TestToolCasesCarryCriticalAndFailTheRun(t *testing.T) {
	cases, err := evals.LoadToolCases()
	if err != nil {
		t.Fatal(err)
	}
	var crit *evals.ToolCase
	for i := range cases {
		if cases[i].ID == "docs-injection-title" {
			crit = &cases[i]
		}
	}
	if crit == nil || !crit.Critical {
		t.Fatalf("critical 케이스가 있어야 함: %+v", crit)
	}
	// 99 건 통과 + critical 1 건 실패는 합격률 99% 여도 불합격이다.
	var rs []CaseResult
	for i := 0; i < 99; i++ {
		rs = append(rs, CaseResult{Suite: "tools", ID: "ok", Pass: true})
	}
	rs = append(rs, CaseResult{Suite: "tools", ID: crit.ID, Pass: false, Critical: true})
	r := Report{Results: rs, only: "tools"}
	if r.Passed() {
		t.Fatal("critical 실패는 합격률과 무관하게 불합격이어야 함")
	}
}
