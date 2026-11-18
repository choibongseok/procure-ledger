package dbutil

import (
	"strconv"
	"strings"
)

// Rebind 는 SQL 의 `?` 자리표시자를 PostgreSQL 형식 `$1, $2, ...` 로 바꾼다.
// 작은따옴표 문자열(따옴표 두 개 연속은 이스케이프), 큰따옴표 식별자, `--` 줄 주석, `/* */` 블록 주석 안의 `?` 는 건드리지 않는다.
// 자리표시자가 없으면 입력을 그대로 돌려준다(할당 없음).
func Rebind(q string) string {
	q = rewriteKSTDate(q) // date(식, '+9 hours') → PG 식(그 밖의 SQLite 전용 구문은 건드리지 않는다)
	n := 0
	for i := 0; i < len(q); i++ {
		if q[i] == '?' {
			n++
		}
	}
	if n == 0 {
		return q
	}
	out := make([]byte, 0, len(q)+n*2)
	idx := 0
	for i := 0; i < len(q); {
		c := q[i]
		switch {
		case c == '\'' || c == '"':
			j := i + 1
			for j < len(q) {
				if q[j] == c {
					if j+1 < len(q) && q[j+1] == c { // 따옴표 두 개는 이스케이프
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
			out = append(out, q[i:j]...)
			i = j
		case c == '-' && i+1 < len(q) && q[i+1] == '-':
			j := i
			for j < len(q) && q[j] != '\n' {
				j++
			}
			out = append(out, q[i:j]...)
			i = j
		case c == '/' && i+1 < len(q) && q[i+1] == '*':
			j := i + 2
			for j+1 < len(q) && !(q[j] == '*' && q[j+1] == '/') {
				j++
			}
			if j+1 < len(q) {
				j += 2
			} else {
				j = len(q)
			}
			out = append(out, q[i:j]...)
			i = j
		case c == '$' && dollarTagEnd(q, i) > 0:
			// 달러 인용($tag$ ... $tag$, 함수 본문): 안쪽은 건드리지 않는다. 닫는 머리표가 없으면 끝까지 그대로 둔다.
			te := dollarTagEnd(q, i)
			end := len(q)
			if j := strings.Index(q[te:], q[i:te]); j >= 0 {
				end = te + j + (te - i)
			}
			out = append(out, q[i:end]...)
			i = end
		case c == '?' && i+1 < len(q) && q[i+1] >= '0' && q[i+1] <= '9':
			// 번호 붙은 자리표시자(?1, ?2)는 SQLite 와 같은 뜻으로 $1, $2 로 바꾼다(같은 번호를 여러 번 쓸 수 있다).
			j := i + 1
			for j < len(q) && q[j] >= '0' && q[j] <= '9' {
				j++
			}
			out = append(out, '$')
			out = append(out, q[i+1:j]...)
			i = j
		case c == '?':
			idx++
			out = append(out, '$')
			out = strconv.AppendInt(out, int64(idx), 10)
			i++
		default:
			out = append(out, c)
			i++
		}
	}
	return string(out)
}

// dollarTagEnd 는 i 가 달러 인용 머리표($$ 또는 $name$)의 시작이면 머리표 다음 위치를, 아니면 0 을 돌려준다.
// $1 같은 위치 자리표시자(숫자로 시작)는 머리표가 아니다.
func dollarTagEnd(q string, i int) int {
	j := i + 1
	if j < len(q) && q[j] == '$' {
		return j + 1
	}
	if j >= len(q) || !(q[j] == '_' || q[j] >= 'a' && q[j] <= 'z' || q[j] >= 'A' && q[j] <= 'Z') {
		return 0
	}
	for j < len(q) && (q[j] == '_' || q[j] >= 'a' && q[j] <= 'z' || q[j] >= 'A' && q[j] <= 'Z' || q[j] >= '0' && q[j] <= '9') {
		j++
	}
	if j < len(q) && q[j] == '$' {
		return j + 1
	}
	return 0
}
