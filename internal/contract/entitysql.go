package contract

import (
	"context"
	"fmt"
	"regexp"
	"strings"
)

// 다법인(B44 2단계) SQL 조각. 업무 저장소가 법인 소유 표를 읽고 쓸 때 쓰는 현재 법인 조건이다.
//
//   - 현재 법인이 하나로 정해져 있으면(웹 세션·헤더, MCP·API 의 소속 법인) 그 법인의 행만 읽는다: "별칭.entity_id = 3".
//   - "전체 법인" 모드(그룹 범위)와 현재 법인이 없는 context(서버 내부 작업·시험)는 조건이 없다("1=1"). 전체 모드의 쓰기는 요청 단계에서 막힌다(auth.Wrap).
//   - 법인 번호는 정수라 SQL 에 직접 넣어도 주입이 없다(자리표시자 순서를 건드리지 않으려고 이렇게 한다).
//   - 권한이 법인별로 다른 사람은 contract.Authorize 가 현재 법인에서의 권한을 이미 확인한다.

// SQLEntity 는 법인 열의 SQL 조건 조각이다. alias 가 비면 열 이름만 쓴다.
func SQLEntity(ctx context.Context, alias string) string {
	col := "entity_id"
	if alias != "" {
		col = alias + ".entity_id"
	}
	if a, ok := ActiveEntityFrom(ctx); ok && !a.All && a.ID > 0 {
		return fmt.Sprintf("%s = %d", col, a.ID)
	}
	return "1=1"
}

// SQLEntityIn 은 읽기 범위(EntityScope)로 법인 열을 거르는 조건 조각이다. 연결 재무 집계처럼 현재 법인 하나가 아니라 호출자가 볼 수 있는 법인 집합을 쓰는 읽기 전용 경로가 쓴다.
// 법인 번호는 정수라 SQL 에 직접 넣는다(자리표시자 순서를 건드리지 않는다). 전체 범위는 "1=1", 빈 범위는 "1=0" 이다.
func SQLEntityIn(s EntityScope, alias string) string {
	if s.All {
		return "1=1"
	}
	if len(s.IDs) == 0 {
		return "1=0"
	}
	col := "entity_id"
	if alias != "" {
		col = alias + ".entity_id"
	}
	ids := make([]string, len(s.IDs))
	for i, id := range s.IDs {
		ids[i] = fmt.Sprint(id)
	}
	return col + " IN (" + strings.Join(ids, ",") + ")"
}

// WriteEntityID 는 새 행에 넣을 법인 번호다: 현재 법인이 하나로 정해져 있으면 그 법인, 아니면 기본 법인(서버 내부 작업·시험·이관 전 호환).
func WriteEntityID(ctx context.Context) int64 {
	if a, ok := ActiveEntityFrom(ctx); ok && !a.All && a.ID > 0 {
		return a.ID
	}
	return DefaultEntityID
}

// SQLEntityID 는 INSERT 값 자리에 쓰는 법인 번호 글자다.
func SQLEntityID(ctx context.Context) string { return fmt.Sprint(WriteEntityID(ctx)) }

// EntitySQL 은 공유 SQL 조각의 법인 표시자를 현재 법인 조건으로 바꾼다. 표시자는 상수 SELECT 안에 미리 심어 두며 이 함수를 거치지 않으면 SQL 문법 오류가 나서 빠뜨릴 수 없다.
//
//	{entity_id}      법인 열 이름만 쓴 조건(예: FROM (SELECT * FROM journals WHERE {entity_id}) j)
//	{entity_id:별칭}  별칭.entity_id 조건(JOIN ... ON 절 끝에 AND 로)
//	{entity_in}      IN 목록(현재 법인이 정해져 있으면 그 번호, 아니면 전체 법인)
func EntitySQL(ctx context.Context, s string) string {
	if !strings.Contains(s, "{entity_") {
		return s
	}
	s = strings.ReplaceAll(s, "{entity_id}", SQLEntity(ctx, ""))
	s = strings.ReplaceAll(s, "{entity_in}", sqlEntityIn(ctx))
	for {
		i := strings.Index(s, "{entity_id:")
		if i < 0 {
			return s
		}
		j := strings.Index(s[i:], "}")
		if j < 0 {
			return s
		}
		alias := s[i+len("{entity_id:") : i+j]
		s = s[:i] + SQLEntity(ctx, alias) + s[i+j+1:]
	}
}

func sqlEntityIn(ctx context.Context) string {
	if a, ok := ActiveEntityFrom(ctx); ok && !a.All && a.ID > 0 {
		return fmt.Sprintf("IN (%d)", a.ID)
	}
	return "IN (SELECT id FROM entities)"
}

var scopeTail = regexp.MustCompile(`(?i)^(GROUP\s+BY|ORDER\s+BY|LIMIT|HAVING|UNION|EXCEPT|INTERSECT|RETURNING|FOR\s+UPDATE|OFFSET)\b`)

// ScopeSQL 은 완성된 SELECT/UPDATE/DELETE 문의 가장 바깥 WHERE 에 현재 법인 조건을 붙인다(없으면 만든다). alias 는 바깥 FROM 의 법인 소유 표 별칭이다.
// 조건이 "1=1" 이면 문을 그대로 돌려준다. 따옴표('...') 안의 글자와 괄호 안(하위 질의)은 건드리지 않는다.
// 공유 SELECT 상수에 WHERE 를 덧붙여 쓰는 저장소 함수가 한 곳에서 법인 조건을 붙이게 하려는 도우미다.
func ScopeSQL(ctx context.Context, alias, query string) string {
	cond := SQLEntity(ctx, alias)
	if cond == "1=1" {
		return query
	}
	depth, inQuote := 0, false
	for i := 0; i < len(query); i++ {
		c := query[i]
		switch {
		case inQuote:
			if c == '\'' {
				inQuote = false
			}
		case c == '\'':
			inQuote = true
		case c == '(':
			depth++
		case c == ')':
			depth--
		case depth == 0 && isWordStart(query, i):
			rest := query[i:]
			if len(rest) >= 5 && strings.EqualFold(rest[:5], "WHERE") && !isWordChar(rest, 5) {
				return query[:i+5] + " " + cond + " AND" + query[i+5:]
			}
			if scopeTail.MatchString(rest) {
				return query[:i] + " WHERE " + cond + " " + query[i:]
			}
		}
	}
	return query + " WHERE " + cond
}

func isWordChar(s string, i int) bool {
	if i >= len(s) {
		return false
	}
	c := s[i]
	return c == '_' || (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func isWordStart(s string, i int) bool { return isWordChar(s, i) && (i == 0 || !isWordChar(s, i-1)) }
