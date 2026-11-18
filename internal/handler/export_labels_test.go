package handler

import (
	"strings"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/contract/audit"
)

// 감사 로그 내보내기의 한국어 라벨은 화면 정본(web/src/admin/auditLabels.ts·targetTypes.ts)을 임베드해 읽는다.
func TestExportLabelsComeFromFrontendSource(t *testing.T) {
	l := labels()
	for name, m := range map[string]map[string]string{"동작": l.action, "변경 키": l.diffKey, "값": l.value, "대상": l.target, "경로": l.channel, "결과": l.result} {
		if len(m) == 0 {
			t.Fatalf("%s 라벨을 읽지 못함", name)
		}
	}
	if l.action["token.issue"] != "토큰 발급" || l.target["budget"] != "부서 예산" || l.channel["mcp"] != "AI 도구" || l.result["denied"] != "거부" || l.value["csv"] != "CSV" {
		t.Errorf("라벨 값이 기대와 다름: %q %q %q %q %q", l.action["token.issue"], l.target["budget"], l.channel["mcp"], l.result["denied"], l.value["csv"])
	}
	if l.action["export.run"] != "목록 내보내기" || l.target["export"] != "내보내기" {
		t.Error("내보내기 라벨이 없음")
	}
	// 모든 감사 대상 종류에 라벨이 있다(새 종류를 더하고 라벨을 빠뜨리면 파일에 영문 코드가 나온다).
	for _, tt := range audit.TargetTypes {
		if l.target[string(tt)] == "" {
			t.Errorf("대상 종류 %q 의 한국어 라벨이 없음", tt)
		}
	}
}

func TestDiffTextMatchesScreenRules(t *testing.T) {
	l := labels()
	cases := []struct {
		d    audit.DiffItem
		tok  string
		want string
	}{
		{audit.DiffItem{Key: "total_amount", New: "1234567"}, "", "총액: 1,234,567원"},
		{audit.DiffItem{Key: "total_amount", Old: "999", New: "1234"}, "", "총액: 999원 → 1,234원"},
		{audit.DiffItem{Key: "round", Old: "1", New: "1"}, "", "상신 차수: 1"},
		{audit.DiffItem{Key: "sha256", New: "0ca0588a1b2c3d4e"}, "", "문서 지문: 기록됨"},
		{audit.DiffItem{Key: audit.DiffKeyTokenID, New: "5"}, "내 Claude", "개인 토큰: 내 Claude"},
		{audit.DiffItem{Key: "step", New: "3/finance"}, "", "결재 단계: 3단계 재무"},
	}
	for _, c := range cases {
		got := l.diffText(c.d, c.tok)
		if got != c.want {
			t.Errorf("%+v → %q 기대 %q", c.d, got, c.want)
		}
		if strings.Contains(got, "team_lead") || strings.Contains(got, "finance") {
			t.Errorf("내부 코드가 남음: %q", got)
		}
	}
}
