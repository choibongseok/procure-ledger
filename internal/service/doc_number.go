package service

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract/domain"
)

// 문서 번호 규칙(명세 3.4). 토큰: {form} 양식 코드, {unit} 기안 부서 이름, {unitcode} 그 조직 코드, {yyyy} {yy} 연도(KST),
// {seq:N} 범위 안 일련번호(N자리 0 채움), {id:N} 문서 번호(id)(N자리 0 채움).

var numberToken = regexp.MustCompile(`\{([a-z]+)(?::(\d{1,2}))?\}`)

// NumberParts 는 번호를 만드는 데 쓰는 값이다.
type NumberParts struct {
	FormCode string
	Unit     string
	UnitCode string
	Now      time.Time // KST 로 바꿔 연도를 쓴다
	Seq      int
	ID       int64
}

// kst 는 한국 표준시다. 연도 경계는 이 시각 기준이다.
var kst = time.FixedZone("KST", 9*3600)

// ValidateNumbering 은 번호 규칙이 유효한지 보고 문구 목록을 돌려준다(게시 전 검사).
func ValidateNumbering(n domain.Numbering) []string {
	var problems []string
	if strings.TrimSpace(n.Pattern) == "" {
		return []string{"번호 규칙을 입력하세요."}
	}
	if len([]rune(n.Pattern)) > 60 {
		problems = append(problems, "번호 규칙은 60자 이내여야 합니다.")
	}
	hasUnique := false
	rest := n.Pattern
	for _, m := range numberToken.FindAllStringSubmatch(n.Pattern, -1) {
		name, width := m[1], m[2]
		switch name {
		case "form", "unit", "unitcode", "yyyy", "yy":
			if width != "" {
				problems = append(problems, fmt.Sprintf("번호 규칙의 {%s:%s}을 알 수 없습니다.", name, width))
			}
		case "seq", "id":
			hasUnique = true
			w, _ := strconv.Atoi(width)
			if width == "" || w < 1 || w > 12 {
				problems = append(problems, fmt.Sprintf("번호 규칙의 {%s}에는 자릿수(1~12)가 필요합니다. 예: {%s:4}", name, name))
			}
		default:
			problems = append(problems, fmt.Sprintf("번호 규칙의 {%s}을 알 수 없습니다.", name))
		}
		rest = strings.Replace(rest, m[0], "", 1)
	}
	if strings.ContainsAny(rest, "{}") {
		problems = append(problems, "번호 규칙의 중괄호가 올바르지 않습니다.")
	}
	if !hasUnique {
		problems = append(problems, "번호 규칙에는 {seq:N} 또는 {id:N} 이 있어야 합니다(번호가 겹치지 않게).")
	}
	if n.AssignAt != "submit" && n.AssignAt != "create" {
		problems = append(problems, "번호를 매기는 시점은 상신 또는 작성이어야 합니다.")
	}
	if n.AssignAt == "create" && strings.Contains(n.Pattern, "{seq") {
		problems = append(problems, "작성 때 번호를 매기는 규칙에는 {seq:N} 을 쓸 수 없습니다. {id:N} 을 쓰세요.")
	}
	if n.Reset != "yearly" && n.Reset != "never" {
		problems = append(problems, "일련번호 초기화는 매년 또는 안 함이어야 합니다.")
	}
	return problems
}

// UsesSeq 는 규칙이 일련번호를 쓰는지다.
func UsesSeq(pattern string) bool { return strings.Contains(pattern, "{seq:") }

// UsesUnit 는 규칙이 부서 이름·코드를 쓰는지다(일련번호 범위에 부서가 들어간다).
func UsesUnit(pattern string) bool {
	return strings.Contains(pattern, "{unit}") || strings.Contains(pattern, "{unitcode}")
}

// SeqYear 는 일련번호 범위의 연도다. reset 이 never 면 0 이다.
func SeqYear(n domain.Numbering, now time.Time) int {
	if n.Reset == "never" {
		return 0
	}
	return now.In(kst).Year()
}

// RenderNumber 는 규칙에 값을 채워 번호 문자열을 만든다. 규칙은 ValidateNumbering 을 통과한 것이어야 한다.
func RenderNumber(pattern string, p NumberParts) string {
	t := p.Now.In(kst)
	return numberToken.ReplaceAllStringFunc(pattern, func(tok string) string {
		m := numberToken.FindStringSubmatch(tok)
		w, _ := strconv.Atoi(m[2])
		switch m[1] {
		case "form":
			return p.FormCode
		case "unit":
			return p.Unit
		case "unitcode":
			return p.UnitCode
		case "yyyy":
			return fmt.Sprintf("%04d", t.Year())
		case "yy":
			return fmt.Sprintf("%02d", t.Year()%100)
		case "seq":
			return fmt.Sprintf("%0*d", w, p.Seq)
		case "id":
			return fmt.Sprintf("%0*d", w, p.ID)
		}
		return tok
	})
}
