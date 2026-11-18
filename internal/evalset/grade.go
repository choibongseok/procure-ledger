package evalset

import (
	"math"
	"regexp"
	"strconv"
	"strings"
)

// GradeResult 는 문항 하나의 채점 결과다. Detail 은 실패 사유 한 줄이며 값(기대·응답)을 담지 않는다.
type GradeResult struct {
	Pass   bool
	Score  int // 0~100
	Detail string
}

var (
	spaceRe = regexp.MustCompile(`\s+`)
	numRe   = regexp.MustCompile(`-?\d[\d,]*(?:\.\d+)?`)
	commaRe = regexp.MustCompile(`(\d),(\d)`)
)

// normText 는 앞뒤 공백을 지우고 연속 공백을 하나로 줄이며 숫자 사이 쉼표를 없앤다(채점 규칙 exact·contains).
func normText(s string) string {
	s = spaceRe.ReplaceAllString(strings.TrimSpace(s), " ")
	for {
		t := commaRe.ReplaceAllString(s, "$1$2")
		if t == s {
			return s
		}
		s = t
	}
}

// firstNumber 는 글에서 첫 숫자를 읽는다(쉼표 제거).
func firstNumber(s string) (float64, bool) {
	m := numRe.FindString(s)
	if m == "" {
		return 0, false
	}
	v, err := strconv.ParseFloat(strings.ReplaceAll(m, ",", ""), 64)
	return v, err == nil
}

// Grade 는 한 문항의 응답을 채점한다. 결정적이며 네트워크를 쓰지 않는다(명세 5절).
// llm 채점은 결정적 모의 채점기다: 기준 글의 "필수:" 줄의 낱말이 응답에 모두 있으면 100, "금지:" 줄의 낱말이 있으면 0.
func Grade(it ItemView, response string) GradeResult {
	switch it.Grader {
	case GraderExact:
		if normText(response) == normText(it.Expected) {
			return GradeResult{Pass: true, Score: 100}
		}
		return GradeResult{Detail: "응답이 기대 답과 정확히 같지 않습니다"}
	case GraderContains:
		resp := normText(response)
		missing, total := 0, 0
		for _, line := range strings.Split(it.Expected, "\n") {
			want := normText(line)
			if want == "" {
				continue
			}
			total++
			if !strings.Contains(resp, want) {
				missing++
			}
		}
		if total == 0 {
			return GradeResult{Detail: "기대 글이 비어 있습니다"}
		}
		if missing > 0 {
			return GradeResult{Score: (total - missing) * 100 / total, Detail: "기대한 글 " + strconv.Itoa(total) + "개 중 " + strconv.Itoa(missing) + "개가 응답에 없습니다"}
		}
		return GradeResult{Pass: true, Score: 100}
	case GraderNumber:
		exp, ok := firstNumber(it.Expected)
		if !ok {
			return GradeResult{Detail: "기대 답에서 숫자를 읽지 못했습니다"}
		}
		got, ok := firstNumber(response)
		if !ok {
			return GradeResult{Detail: "응답에서 숫자를 읽지 못했습니다"}
		}
		kind, tol, err := parseTolerance(it.Tolerance)
		if err != nil {
			kind, tol = "abs", 0
		}
		limit := tol
		if kind == "rel" {
			limit = math.Abs(exp) * tol / 100
		}
		if math.Abs(got-exp) <= limit+1e-9 {
			return GradeResult{Pass: true, Score: 100}
		}
		return GradeResult{Detail: "숫자가 허용 오차 밖입니다"}
	case GraderLLM:
		return gradeRubric(it.Rubric, response)
	}
	return GradeResult{Detail: "알 수 없는 채점 기준입니다"}
}

// gradeRubric 은 결정적 모의 채점기다.
func gradeRubric(rubric, response string) GradeResult {
	resp := normText(response)
	var must, ban []string
	for _, line := range strings.Split(rubric, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "필수:"):
			must = append(must, rubricWords(strings.TrimPrefix(line, "필수:"))...)
		case strings.HasPrefix(line, "금지:"):
			ban = append(ban, rubricWords(strings.TrimPrefix(line, "금지:"))...)
		}
	}
	if len(must) == 0 && len(ban) == 0 {
		return GradeResult{Detail: "평가 기준에 필수·금지 줄이 없습니다"}
	}
	for _, w := range ban {
		if strings.Contains(resp, w) {
			return GradeResult{Detail: "금지한 낱말이 응답에 있습니다"}
		}
	}
	missing := 0
	for _, w := range must {
		if !strings.Contains(resp, w) {
			missing++
		}
	}
	if missing > 0 {
		return GradeResult{Detail: "필수 낱말 " + strconv.Itoa(len(must)) + "개 중 " + strconv.Itoa(missing) + "개가 응답에 없습니다"}
	}
	return GradeResult{Pass: true, Score: 100}
}

func rubricWords(s string) []string {
	var out []string
	for _, w := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == '，' || r == ' ' || r == '\t' }) {
		if w = strings.TrimSpace(w); w != "" {
			out = append(out, w)
		}
	}
	return out
}
