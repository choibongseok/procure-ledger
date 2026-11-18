package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestSplitProvidersKeepsColonsInModelID(t *testing.T) {
	got := splitProviders("mock, bedrock:anthropic.claude-3-haiku-20240307-v1:0 ,vertex:gemini-x,anthropic", "dflt")
	want := []providerSpec{{"mock", "dflt"}, {"bedrock", "anthropic.claude-3-haiku-20240307-v1:0"}, {"vertex", "gemini-x"}, {"anthropic", "dflt"}}
	if len(got) != len(want) {
		t.Fatalf("%v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("%d: %+v != %+v", i, got[i], want[i])
		}
	}
}

// 실제 외부 호출은 --live 와 키가 있을 때만 한다. CI(mock)는 어떤 경우에도 네트워크를 쓰지 않는다.
func TestExternalProvidersAreSkippedWithoutLiveOrKeys(t *testing.T) {
	for _, name := range []string{"bedrock", "vertex", "openai"} {
		_, _, _, skip, err := buildEvalProvider(providerSpec{name, "m"}, "NO_SUCH_KEY_ENV", false)
		if err != nil || !strings.Contains(skip, "--live") {
			t.Errorf("%s: --live 없이는 건너뛰어야 한다: skip=%q err=%v", name, skip, err)
		}
		for _, k := range []string{"AWS_REGION", "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "GOOGLE_CLOUD_PROJECT", "GOOGLE_CLOUD_LOCATION", "GOOGLE_APPLICATION_CREDENTIALS", "OPENAI_COMPAT_BASE_URL", "OPENAI_COMPAT_API_KEY"} {
			t.Setenv(k, "")
		}
		_, _, _, skip, err = buildEvalProvider(providerSpec{name, "m"}, "NO_SUCH_KEY_ENV", true)
		if err != nil || !strings.Contains(skip, "환경변수") {
			t.Errorf("%s: 키가 없으면 건너뛰어야 한다: skip=%q err=%v", name, skip, err)
		}
	}
	if _, _, _, _, err := buildEvalProvider(providerSpec{"nope", ""}, "", false); err == nil {
		t.Error("알 수 없는 공급자는 설정 오류")
	}
}

func rep(cases ...CaseResult) Report { return Report{Results: cases} }

// 비교 표: 기준(첫 공급자)에서 통과하던 케이스가 다른 곳에서 실패하면 회귀로 표시하고 치명 회귀는 경고만 한다.
func TestPrintCompareMarksRegressions(t *testing.T) {
	a := runResult{name: "mock", model: "mock-1", rep: rep(CaseResult{Suite: "docs", ID: "a", Pass: true, Critical: true}, CaseResult{Suite: "docs", ID: "b", Pass: true}), latencyMS: 5, costMicro: 0}
	b := runResult{name: "bedrock", model: "m", rep: rep(CaseResult{Suite: "docs", ID: "a", Pass: false, Critical: true}, CaseResult{Suite: "docs", ID: "b", Pass: true}), latencyMS: 900, costMicro: 42}
	var out bytes.Buffer
	printCompare(&out, []runResult{a, b})
	s := out.String()
	for _, want := range []string{"mock(mock-1)", "bedrock(m)", "docs/a *", "실패(회귀)", "치명 케이스 실패", "경고: 기준 공급자에서 통과하던 치명 케이스", "900", "42"} {
		if !strings.Contains(s, want) {
			t.Errorf("출력에 %q 가 없다:\n%s", want, s)
		}
	}
	// 골든셋이 다르면 비교하지 않는다.
	c := runResult{name: "x", rep: rep(CaseResult{Suite: "docs", ID: "other", Pass: true})}
	out.Reset()
	printCompare(&out, []runResult{a, c})
	if !strings.Contains(out.String(), "골든셋이 달라 비교할 수 없습니다") {
		t.Errorf("골든셋이 다를 때: %s", out.String())
	}
}

// 비교 표 출력은 종료 코드에 영향이 없다(D12 경고만): mock 두 개를 돌려도 종료 코드는 각 실행의 합격 여부만 본다.
func TestMultiProviderRunComparesMocks(t *testing.T) {
	var out, errw bytes.Buffer
	if code := run([]string{"--provider", "mock,mock:mock-2", "--only", "tools"}, &out, &errw); code != 0 {
		t.Fatalf("종료 코드 %d\n%s\n%s", code, out.String(), errw.String())
	}
	if !strings.Contains(out.String(), "공급자별 골든셋 비교") {
		t.Fatalf("비교 표가 없다:\n%s", out.String())
	}
	if code := run([]string{"--provider", "mock,mock", "--update-baseline"}, &out, &errw); code != 2 {
		t.Errorf("--update-baseline 은 mock 한 개일 때만: %d", code)
	}
}
