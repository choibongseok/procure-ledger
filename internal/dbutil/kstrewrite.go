package dbutil

import "strings"

// 업무 SQL 에는 UTC 시각 문자열을 한국 날짜로 바꾸는 `date(식, '+9 hours')` 가 여러 곳에 있다. SQLite 에서는 그대로 돌고,
// PostgreSQL 연결 계층(Rebind)은 이 한 가지 관용구만 방언 객체의 KSTDate 로 바꾼다(그 밖의 SQLite 전용 구문은 코드에서 공통 부분집합이나 Dialect 로 고친다).
// `date(식)`(수정자 없음)이나 다른 수정자는 건드리지 않는다.

// rewriteKSTDate 는 q 안의 `date(expr, '+9 hours')` 를 PG 식으로 바꾼다. 문자열·식별자 따옴표 안은 건드리지 않는다.
func rewriteKSTDate(q string) string {
	if !strings.Contains(q, "+9 hours") {
		return q
	}
	var out strings.Builder
	i := 0
	for i < len(q) {
		c := q[i]
		if c == '\'' || c == '"' {
			j := i + 1
			for j < len(q) {
				if q[j] == c {
					if j+1 < len(q) && q[j+1] == c {
						j += 2
						continue
					}
					break
				}
				j++
			}
			if j < len(q) {
				j++
			}
			out.WriteString(q[i:j])
			i = j
			continue
		}
		if c == 'd' && strings.HasPrefix(q[i:], "date(") && (i == 0 || !isIdentByte(q[i-1])) {
			if end, expr, ok := kstArgs(q, i+len("date(")); ok {
				out.WriteString(Postgres.KSTDate(rewriteKSTDate(expr)))
				i = end
				continue
			}
		}
		out.WriteByte(c)
		i++
	}
	return out.String()
}

func isIdentByte(b byte) bool {
	return b == '_' || b == '.' || (b >= '0' && b <= '9') || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

// kstArgs 는 start(여는 괄호 바로 뒤)부터 짝이 맞는 닫는 괄호까지 읽어, 인자가 정확히 `expr, '+9 hours'` 꼴이면 그 expr 과 끝 위치를 돌려준다.
func kstArgs(q string, start int) (end int, expr string, ok bool) {
	depth, lastComma := 1, -1
	for j := start; j < len(q); j++ {
		switch q[j] {
		case '\'':
			k := j + 1
			for k < len(q) && !(q[k] == '\'' && (k+1 >= len(q) || q[k+1] != '\'')) {
				if q[k] == '\'' {
					k++
				}
				k++
			}
			j = k
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				if lastComma < 0 {
					return 0, "", false
				}
				if strings.TrimSpace(q[lastComma+1:j]) != "'+9 hours'" {
					return 0, "", false
				}
				return j + 1, strings.TrimSpace(q[start:lastComma]), true
			}
		case ',':
			if depth == 1 {
				lastComma = j
			}
		}
	}
	return 0, "", false
}
