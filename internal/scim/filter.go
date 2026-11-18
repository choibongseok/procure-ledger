package scim

import (
	"strings"

	"github.com/choibongseok/procure-ledger/internal/contract"
)

// 필터는 명세 7.3 의 범위만 지원한다: 속성 userName·externalId·id·active·emails.value·displayName, 연산자 eq(전부)·ne(active)·sw·co(userName·displayName),
// 절 연결은 and 뿐이다(or·not·괄호·값 경로 필터는 미지원). 그 밖은 400 invalidFilter.

type clause struct {
	attr, op, val string
	isBool        bool
	b             bool
}

func errFilter(detail string) *apiError {
	return &apiError{Status: 400, Type: "invalidFilter", Detail: detail}
}

// splitAnd 는 따옴표 밖의 " and " 로 절을 나눈다.
func splitAnd(f string) []string {
	var out []string
	var cur strings.Builder
	inQ, esc := false, false
	for i := 0; i < len(f); i++ {
		c := f[i]
		switch {
		case esc:
			esc = false
		case c == '\\' && inQ:
			esc = true
		case c == '"':
			inQ = !inQ
		case !inQ && c == ' ' && i+5 <= len(f) && strings.EqualFold(f[i:i+5], " and "):
			out = append(out, cur.String())
			cur.Reset()
			i += 4
			continue
		}
		cur.WriteByte(c)
	}
	return append(out, cur.String())
}

func parseClause(c string) (clause, *apiError) {
	c = strings.TrimSpace(c)
	sp1 := strings.IndexByte(c, ' ')
	if sp1 <= 0 {
		return clause{}, errFilter("필터 형식이 올바르지 않습니다: 속성 연산자 값")
	}
	attr := c[:sp1]
	rest := strings.TrimSpace(c[sp1+1:])
	sp2 := strings.IndexByte(rest, ' ')
	if sp2 <= 0 {
		return clause{}, errFilter("필터 형식이 올바르지 않습니다: 속성 연산자 값")
	}
	op := strings.ToLower(rest[:sp2])
	val := strings.TrimSpace(rest[sp2+1:])
	if strings.ContainsAny(attr, "[]()") {
		return clause{}, errFilter("괄호·값 경로 필터는 지원하지 않습니다.")
	}
	cl := clause{attr: attr, op: op}
	switch {
	case val == "true" || val == "false":
		cl.isBool, cl.b = true, val == "true"
	case len(val) >= 2 && val[0] == '"' && val[len(val)-1] == '"':
		v := val[1 : len(val)-1]
		v = strings.ReplaceAll(strings.ReplaceAll(v, `\"`, `"`), `\\`, `\`)
		cl.val = v
	default:
		return clause{}, errFilter("값은 따옴표로 감싼 문자열이거나 true·false 여야 합니다.")
	}
	switch strings.ToLower(attr) {
	case "username", "emails.value", "displayname":
		if op != "eq" && op != "sw" && op != "co" {
			return clause{}, errFilter("이 속성은 eq·sw·co 만 지원합니다.")
		}
	case "externalid", "id":
		if op != "eq" {
			return clause{}, errFilter("이 속성은 eq 만 지원합니다.")
		}
	case "active":
		if op != "eq" && op != "ne" {
			return clause{}, errFilter("active 는 eq·ne 만 지원합니다.")
		}
		if !cl.isBool {
			return clause{}, errFilter("active 의 값은 true 또는 false 여야 합니다.")
		}
	default:
		return clause{}, errFilter("지원하지 않는 필터 속성입니다: " + attr)
	}
	if cl.isBool && strings.ToLower(attr) != "active" {
		return clause{}, errFilter("true·false 값은 active 에만 쓸 수 있습니다.")
	}
	return cl, nil
}

func likeEscape(v string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(strings.ToLower(v))
}

// parseFilter 는 필터 문자열을 SQL 조건(users 별칭 u, scim_links 별칭 l)과 인자로 바꾼다. 빈 문자열이면 조건 없음.
func parseFilter(f string) (conds []string, args []any, cls []clause, e *apiError) {
	f = strings.TrimSpace(f)
	if f == "" {
		return nil, nil, nil, nil
	}
	if len(f) > maxFilterLen {
		return nil, nil, nil, errFilter("필터가 너무 깁니다.")
	}
	lf := strings.ToLower(f)
	if strings.Contains(lf, " or ") || strings.HasPrefix(lf, "not ") {
		return nil, nil, nil, errFilter("or·not·괄호는 지원하지 않습니다. and 로만 연결할 수 있습니다.")
	}
	for _, part := range splitAnd(f) {
		cl, err := parseClause(part)
		if err != nil {
			return nil, nil, nil, err
		}
		cls = append(cls, cl)
		switch strings.ToLower(cl.attr) {
		case "username", "emails.value":
			conds, args = likeCond(conds, args, "lower(u.email)", cl)
		case "displayname":
			conds, args = likeCond(conds, args, "lower(u.name)", cl)
		case "externalid":
			conds, args = append(conds, "l.external_id = ?"), append(args, cl.val)
		case "id":
			conds, args = append(conds, "CAST(u.id AS TEXT) = ?"), append(args, cl.val)
		case "active":
			want := cl.b
			if cl.op == "ne" {
				want = !want
			}
			if want {
				conds = append(conds, "u.active = 1")
			} else {
				conds = append(conds, "u.active = 0")
			}
		}
	}
	return conds, args, cls, nil
}

func likeCond(conds []string, args []any, col string, cl clause) ([]string, []any) {
	switch cl.op {
	case "eq":
		return append(conds, col+" = ?"), append(args, strings.ToLower(cl.val))
	case "sw":
		return append(conds, col+` LIKE ? ESCAPE '\'`), append(args, likeEscape(cl.val)+"%")
	}
	return append(conds, col+` LIKE ? ESCAPE '\'`), append(args, "%"+likeEscape(cl.val)+"%")
}

// riskArgs 는 보호 계정 판정에 쓰는 위험 권한 목록의 SQL 자리표시자와 인자다.
func riskArgs() (string, []any) {
	marks := make([]string, 0, len(contract.HighRiskPermissions))
	args := make([]any, 0, len(contract.HighRiskPermissions))
	for _, p := range contract.HighRiskPermissions {
		marks = append(marks, "?")
		args = append(args, string(p))
	}
	return strings.Join(marks, ","), args
}
