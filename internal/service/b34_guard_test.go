package service_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// B34 소스 검사(명세 5절): 부가세율·회계연도 연초·요일 판정·설정 SQL 이 지정된 곳 밖에 새로 생기지 않는다.
// 주석은 검사하지 않는다. 예외는 이 파일 한 곳에 사유와 함께 둔다.

type guardRule struct {
	name    string
	re      *regexp.Regexp
	allowed map[string]string // 파일 이름 → 사유
	bad     string            // 자기 검증용으로 심은 위반 줄(규칙이 반드시 잡아야 한다)
}

func nonTestSources(t *testing.T) map[string][]string {
	t.Helper()
	out := map[string][]string{}
	for _, dir := range []string{"../service", "../repository", "../handler"} {
		_ = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			b, _ := os.ReadFile(path)
			var lines []string
			for _, l := range strings.Split(string(b), "\n") {
				if i := strings.Index(l, "//"); i >= 0 && !strings.Contains(l[:i], "\"") {
					l = l[:i]
				}
				lines = append(lines, l)
			}
			out[filepath.ToSlash(path)] = lines
			return nil
		})
	}
	return out
}

func TestB34HardcodedSettingsStayOutOfCode(t *testing.T) {
	rules := []guardRule{
		{"부가세 10% 상수 함수(POVAT)", regexp.MustCompile(`\bPOVAT\(`), nil, "vat := domain.POVAT(x)"},
		{"카드 부가세 1/11 역산 상수", regexp.MustCompile(`/\s*11\b`), nil, "vat = base / 11"},
		{"부가세 10% 상수(/ 10·* 10 / 100, 변수 이름과 무관)", regexp.MustCompile(`/\s*10\b|\*\s*10\s*/\s*100`), map[string]string{"../service/report_service.go": "비율(%)을 소수 첫째 자리로 반올림하는 계산이다(부가세와 무관)"}, "x := s / 10"},
		{"부가세 10% 상수(곱셈 꼴)", regexp.MustCompile(`\*\s*10\s*/\s*100`), nil, "vat := supply * 10 / 100"},
		{"연초(1월 1일) 가정", regexp.MustCompile(`-01-01"|Month\(1\)|"-01"`), map[string]string{"../repository/company_repo.go": "공휴일 목록의 달력 연도 범위(회계연도와 무관)"}, `from := y + "-01-01"`},
		{"요일 판정(영업일은 isBusinessDay 한 곳)", regexp.MustCompile(`\.Weekday\(\)|time\.Saturday|time\.Sunday`), map[string]string{"../service/company_settings.go": "isBusinessDay 의 유일한 정의"}, "if d.Weekday() == time.Sunday {"},
		{"설정 표 SQL(회사 설정 저장소 한 파일)", regexp.MustCompile(`(?i)\b(FROM|JOIN|INTO|UPDATE)\s+(company_settings|company_assets|vat_rates|holidays)\b`), map[string]string{"../repository/company_repo.go": "설정 저장소"}, "SELECT rate FROM vat_rates"},
		// 금액 뒤에 "원"을 직접 붙이는 문구는 moneyLabel 한 함수만 쓴다(열 머리글의 "(원)" 표기는 대상이 아니다).
		{`금액 뒤 "원" 직접 붙이기(moneyLabel 밖)`, regexp.MustCompile(`%[sd]원|\)\s*\+\s*"원`), map[string]string{
			"../service/doc_fields.go":     "moneyLabel 의 유일한 정의",
			"../service/approval_line.go":  "결재선 안내 문구는 명세(B21)가 쉼표 없는 금액(500000원)으로 고정했다",
			"../handler/export_sources.go": "내보내기 셀 표기 함수(commaInt) — 화면 문구가 아니라 파일 셀 값이다",
		}, `msg := "합계 " + formatWon(n) + "원입니다"`},
	}
	src := nonTestSources(t)
	for _, r := range rules {
		for path, lines := range src {
			if _, ok := r.allowed[path]; ok {
				continue
			}
			for i, l := range lines {
				if r.re.MatchString(l) {
					t.Errorf("%s:%d: %s 이(가) 코드에 직접 있습니다: %s", path, i+1, r.name, strings.TrimSpace(l))
				}
			}
		}
	}
	// 자기 검증: 위반을 심은 줄은 규칙이 잡는다.
	for _, r := range rules {
		if !r.re.MatchString(r.bad) {
			t.Errorf("규칙 %q 이(가) 심은 위반을 잡지 못함: %s", r.name, r.bad)
		}
	}
}

// 웹 소스에도 연초 가정(`-01` 로 올해 1월을 직접 만드는 것)이 새로 생기지 않는다. 회계연도 시작은 서버(/api/company/fiscal)가 정한다.
func TestB34WebSourceHasNoFiscalYearAssumption(t *testing.T) {
	re := regexp.MustCompile("slice\\(0,\\s*4\\)\\}?-01|\\}-01[`\"]|\"-01\"|\\}-01-01|\"-01-01\"|[1-9][0-9]{3}-01-01")
	if !re.MatchString("`${kstToday().slice(0, 4)}-01`") || !re.MatchString(`const from = y + "-01-01"`) {
		t.Fatal("규칙이 심은 위반을 잡지 못함")
	}
	_ = filepath.Walk("../../web/src", func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !(strings.HasSuffix(path, ".ts") || strings.HasSuffix(path, ".tsx")) || strings.Contains(path, ".test.") {
			return nil
		}
		if strings.HasSuffix(filepath.ToSlash(path), "finance/VatPage.tsx") {
			return nil // 부가세 신고 기간은 역년(1~3월·4~6월…)이 법정 기준이라 회계연도와 무관하다
		}
		b, _ := os.ReadFile(path)
		for i, l := range strings.Split(string(b), "\n") {
			if re.MatchString(l) {
				t.Errorf("%s:%d: 연초(1월) 가정이 웹 소스에 직접 있습니다: %s", filepath.ToSlash(path), i+1, strings.TrimSpace(l))
			}
		}
		return nil
	})
}
