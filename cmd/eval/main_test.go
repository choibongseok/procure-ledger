package main

import (
	"bytes"
	"context"
	"sort"
	"strings"
	"testing"

	"github.com/choibongseok/procure-ledger/evals"
	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/llm"
	"github.com/choibongseok/procure-ledger/internal/llmgw"
)

func TestMockProviderMeetsAcceptanceCriteria(t *testing.T) {
	var out, errw bytes.Buffer
	code := run([]string{"--provider", "mock"}, &out, &errw)
	t.Log("\n" + out.String())
	if code != 0 {
		t.Fatalf("mock 평가가 합격해야 함: exit=%d\n%s\n%s", code, out.String(), errw.String())
	}
	for _, want := range []string{"문서 판정:", "도구 선택:", "규정 검색:", "질문하기:", "결과: 합격"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("표 출력에 %q 가 없음", want)
		}
	}
}

func TestMockRunIsDeterministic(t *testing.T) {
	var a, b bytes.Buffer
	run([]string{"--provider", "mock"}, &a, &bytes.Buffer{})
	run([]string{"--provider", "mock"}, &b, &bytes.Buffer{})
	if a.String() != b.String() {
		t.Fatal("mock 평가 출력이 실행마다 달라짐")
	}
}

func TestGoldenSetSizesAndShape(t *testing.T) {
	set, err := evals.LoadDocSet()
	if err != nil {
		t.Fatal(err)
	}
	if len(set.Cases) < evals.MinDocCases {
		t.Fatalf("문서 판정 골든셋 %d건 < %d", len(set.Cases), evals.MinDocCases)
	}
	kinds := map[string]int{}
	critical := 0
	for _, c := range set.Cases {
		kinds[c.Expect.Outcome]++
		if c.Critical {
			critical++
		}
	}
	for _, k := range []string{"match", "mismatch", "hold"} {
		if kinds[k] < 3 {
			t.Errorf("%s 케이스가 너무 적음: %d", k, kinds[k])
		}
	}
	if critical < 3 {
		t.Errorf("문서 안 지시문(프롬프트 주입) 필수 케이스가 3건 이상이어야 함: %d", critical)
	}
	tools, err := evals.LoadToolCases()
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) < evals.MinToolCases {
		t.Fatalf("도구 선택 케이스 %d건 < %d", len(tools), evals.MinToolCases)
	}
	denied := 0
	for _, c := range tools {
		if !c.ExpectAllowed {
			denied++
		}
		if c.ExpectTool != "" {
			if _, ok := contract.MCPToolPermissions[c.ExpectTool]; !ok {
				t.Errorf("%s: 알 수 없는 도구 %s", c.ID, c.ExpectTool)
			}
		}
	}
	if denied < 2 {
		t.Errorf("권한 거부를 기대하는 도구 케이스가 필요함: %d", denied)
	}
}

func TestPolicyGoldenSetShape(t *testing.T) {
	cases, err := evals.LoadPolicyCases()
	if err != nil {
		t.Fatal(err)
	}
	if len(cases) < evals.MinPolicyCases {
		t.Fatalf("규정 검색 케이스 %d건 < %d", len(cases), evals.MinPolicyCases)
	}
	var hit, none, empty, critical int
	for _, c := range cases {
		hit += len(c.ExpectTop) / max(len(c.ExpectTop), 1)
		if len(c.ExpectNone) > 0 {
			none++
		}
		if c.ExpectEmpty {
			empty++
		}
		if c.Critical {
			critical++
		}
	}
	if hit < 10 || none < 3 || empty < 1 || critical < 3 {
		t.Fatalf("적중 %d, 범위 밖 0건 %d, 빈 결과 %d, 필수 %d", hit, none, empty, critical)
	}
}

func TestPolicyScopeFailureIsCritical(t *testing.T) {
	// 범위 밖 문서가 보이는 케이스(구매팀 직원에게 구매팀 지침이 아니라 재무 문서를 기대하는 식의 역전)는 필수 실패다.
	res, err := runPolicy(context.Background(), []evals.PolicyCase{{ID: "leak", Role: "staff", Dept: "구매팀", Query: "결재 규정",
		ExpectNone: []string{"purchase-approval-line"}, Critical: true}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || res[0].Pass || !res[0].Critical {
		t.Fatalf("보이면 안 되는 문서가 나오면 실패해야 함: %+v", res)
	}
	rep := Report{Results: res}
	if rep.Passed() {
		t.Fatal("필수 케이스 실패는 불합격")
	}
}

func TestToolHelpCoversEveryToolInPermissionTable(t *testing.T) {
	var a, b []string
	for n := range toolHelp {
		a = append(a, n)
	}
	for n := range contract.MCPToolPermissions {
		b = append(b, n)
	}
	sort.Strings(a)
	sort.Strings(b)
	if strings.Join(a, ",") != strings.Join(b, ",") {
		t.Fatalf("도구 설명 목록이 권한 표와 다름\n%v\n%v", a, b)
	}
}

// 깨진 공급자(엉뚱한 출력)에서는 합격률이 기준에 못 미쳐 불합격이어야 한다(0 이 아닌 종료 코드의 근거).
type garbage struct{}

func (garbage) Name() string { return "mock" }
func (garbage) Complete(_ context.Context, r llm.Request) (llm.Response, error) {
	return llm.Response{Text: "모르겠어요", Model: r.Model, Usage: llm.Usage{InputTokens: 1, OutputTokens: 1}}, nil
}

func TestBelowThresholdFails(t *testing.T) {
	rep, err := Run(context.Background(), Config{Provider: garbage{}, Model: "mock-1", PolicyPattern: "mock*"})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Passed() {
		t.Fatal("엉터리 출력이 합격하면 안 됨")
	}
	var out bytes.Buffer
	rep.Print(&out, "garbage")
	if !strings.Contains(out.String(), "결과: 불합격") || !strings.Contains(out.String(), "미달") {
		t.Fatalf("불합격 표기: %s", out.String())
	}
}

// 필수(주입) 케이스가 하나라도 틀리면 합격률이 높아도 불합격이다.
func TestCriticalFailureFailsEvenAtHighPassRate(t *testing.T) {
	rep := Report{}
	for i := 0; i < 99; i++ {
		rep.Results = append(rep.Results, CaseResult{Suite: "docs", ID: "ok", Pass: true})
	}
	rep.Results = append(rep.Results, CaseResult{Suite: "docs", ID: "inj", Pass: false, Critical: true})
	for i := 0; i < 10; i++ {
		rep.Results = append(rep.Results, CaseResult{Suite: "tools", ID: "t", Pass: true})
	}
	if rep.Passed() {
		t.Fatal("필수 케이스 실패는 불합격")
	}
}

func TestTooFewCasesFails(t *testing.T) {
	rep := Report{}
	for i := 0; i < 5; i++ {
		rep.Results = append(rep.Results, CaseResult{Suite: "docs", ID: "ok", Pass: true})
	}
	for i := 0; i < 10; i++ {
		rep.Results = append(rep.Results, CaseResult{Suite: "tools", ID: "t", Pass: true})
	}
	if rep.Passed() {
		t.Fatal("케이스 수가 하한 미만이면 불합격")
	}
}

func TestAnthropicWithoutKeySkipsWithoutFailing(t *testing.T) {
	t.Setenv("PL_EVAL_TEST_KEY_EMPTY", "")
	var out, errw bytes.Buffer
	if code := run([]string{"--provider", "anthropic", "--key-env", "PL_EVAL_TEST_KEY_EMPTY"}, &out, &errw); code != 0 {
		t.Fatalf("키가 없어도 실패하면 안 됨: %d %s", code, errw.String())
	}
	if !strings.Contains(out.String(), "건너뜀") {
		t.Fatalf("건너뜀 안내: %s", out.String())
	}
}

func TestUnknownProviderIsUsageError(t *testing.T) {
	if code := run([]string{"--provider", "nope"}, &bytes.Buffer{}, &bytes.Buffer{}); code != 2 {
		t.Fatalf("exit=%d", code)
	}
	_ = llmgw.MockProvider{}
}

func TestKnowledgeGoldenSet(t *testing.T) {
	var out, errw bytes.Buffer
	code := run([]string{"--provider", "mock", "--only", "knowledge"}, &out, &errw)
	t.Log("\n" + out.String())
	if code != 0 {
		t.Fatalf("질문하기 평가 exit=%d\n%s\n%s", code, out.String(), errw.String())
	}
	set, err := evals.LoadKnowledgeSet()
	if err != nil {
		t.Fatal(err)
	}
	critical, notFound := 0, 0
	for _, c := range set.Cases {
		if c.Critical {
			critical++
		}
		if !c.ExpectFound {
			notFound++
		}
	}
	if len(set.Cases) < evals.MinKnowledgeCases || critical < 3 || notFound < 4 {
		t.Fatalf("케이스 %d, 필수 %d, 찾지 못함 %d", len(set.Cases), critical, notFound)
	}
}
