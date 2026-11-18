package llmgw

import "testing"

func run(id int64, prov, model, hash string, cases ...EvalCase) EvalRun {
	return EvalRun{ID: id, Provider: prov, Model: model, SuiteHash: hash, Cases: cases, PassRatePct: 100, LatencyMS: id * 10}
}

func c(id string, pass, crit bool) EvalCase {
	return EvalCase{Suite: "docs", ID: id, Pass: pass, Critical: crit}
}

// AC-4512: 공급자·모델별 최신 실행만, 가장 최근 실행과 같은 골든셋만, 가장 오래된 실행이 기준, 기준 대비 회귀(치명 포함)를 센다.
func TestBuildComparison(t *testing.T) {
	all := []EvalRun{ // 최신 순
		run(5, "bedrock", "m1", "h1", c("a", false, true), c("b", true, false)),
		run(4, "anthropic", "x", "OLD", c("a", true, true)), // 골든셋이 다르다 → 제외
		run(3, "mock", "mock-1", "h1", c("a", true, true), c("b", true, false)),
		run(2, "bedrock", "m1", "h1", c("a", true, true), c("b", true, false)), // 같은 공급자·모델의 옛 실행: 무시
		run(1, "legacy", "", "", c("a", true, true)),                           // 해시 없는 옛 실행: 무시
	}
	cmp := buildComparison(all)
	if cmp.SuiteHash != "h1" || len(cmp.Runs) != 2 || cmp.Excluded != 1 {
		t.Fatalf("구성: %+v", cmp)
	}
	if cmp.Runs[0].Provider != "mock" || !cmp.Runs[0].Reference || cmp.Runs[1].Provider != "bedrock" {
		t.Fatalf("기준은 가장 오래된 실행(첫 열): %+v", cmp.Runs)
	}
	if r := cmp.Runs[1]; r.Regressions != 1 || r.CriticalRegress != 1 || r.CriticalFailures != 1 {
		t.Fatalf("회귀: %+v", r)
	}
	if len(cmp.Cases) != 2 || !cmp.Cases[0].Critical || cmp.Cases[0].Results[0] != "pass" || cmp.Cases[0].Results[1] != "fail" {
		t.Fatalf("케이스 행: %+v", cmp.Cases)
	}
	if cmp.Warning == "" {
		t.Fatal("치명 회귀는 경고를 남긴다")
	}
	if got := buildComparison(nil); len(got.Runs) != 0 || got.Warning != "" {
		t.Fatalf("빈 입력: %+v", got)
	}
}

func TestSuiteHashIgnoresResultsAndOrder(t *testing.T) {
	a := SuiteHash([]EvalCase{c("a", true, true), c("b", false, false)})
	b := SuiteHash([]EvalCase{c("b", true, false), c("a", false, true)})
	if a != b || a == "" {
		t.Fatalf("통과 여부·순서와 무관해야 한다: %s %s", a, b)
	}
	if SuiteHash([]EvalCase{c("a", true, false), c("b", true, false)}) == a {
		t.Fatal("치명 여부가 다르면 해시가 달라야 한다")
	}
}
