// Package policysearch 는 사내 규정 검색(B12)이다.
//
// 규정 문서(마크다운)를 조항 단위로 쪼개 로컬 색인(한글 n-gram + BM25)으로 검색한다. 임베딩·외부 서비스·LLM 을 쓰지 않는다.
// 문서는 데이터다: 본문에 지시문처럼 보이는 문장이 있어도 검색 결과 문자열로만 전달되고 시스템 동작을 바꾸지 않는다.
package policysearch

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Doc 은 규정 문서 한 건이다. 열람 범위는 역할 AND 부서다.
type Doc struct {
	Slug     string
	Title    string
	AllRoles bool
	Roles    []string // 소문자 역할 이름. AllRoles 면 비어 있다
	AllDepts bool
	Depts    []string // 부서 이름. AllDepts 면 비어 있다
	// RawRoles·RawDepts 는 머리말 원문이다. 서비스의 두 번째 열람 확인이 위 파싱 결과와 독립으로 여기서 다시 계산한다.
	RawRoles string
	RawDepts string
}

// Clause 는 조항 한 건이다.
type Clause struct {
	No      string // "제2조"
	Heading string // "금액 구간별 결재선"
	Text    string // 조항 본문(머리줄 제외)
}

var clauseHead = regexp.MustCompile(`^##\s*(제\s*\d+\s*조)\s*(?:\(([^)]*)\))?\s*$`)

// ParseDoc 은 머리말(--- 로 감싼 title·roles·departments)과 조항(`## 제N조 (제목)`)을 읽는다.
// 머리말이 없거나 title·roles·departments 가 빠졌거나 조항이 하나도 없으면 오류다(열람 범위를 알 수 없는 문서는 색인하지 않는다).
func ParseDoc(slug string, raw []byte) (Doc, []Clause, error) {
	text := strings.ReplaceAll(string(raw), "\r\n", "\n")
	text = strings.TrimPrefix(text, "\ufeff")
	lines := strings.Split(text, "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return Doc{}, nil, errors.New("머리말(---)이 없습니다")
	}
	meta := map[string]string{}
	i := 1
	closed := false
	for ; i < len(lines); i++ {
		l := strings.TrimSpace(lines[i])
		if l == "---" {
			closed = true
			i++
			break
		}
		if l == "" {
			continue
		}
		k, v, ok := strings.Cut(l, ":")
		if !ok {
			return Doc{}, nil, fmt.Errorf("머리말 줄 형식 오류: %q", l)
		}
		meta[strings.ToLower(strings.TrimSpace(k))] = strings.TrimSpace(v)
	}
	if !closed {
		return Doc{}, nil, errors.New("머리말이 닫히지 않았습니다")
	}
	d := Doc{Slug: slug, Title: meta["title"], RawRoles: meta["roles"], RawDepts: meta["departments"]}
	if d.Title == "" {
		return Doc{}, nil, errors.New("title 이 없습니다")
	}
	var err error
	if d.AllRoles, d.Roles, err = parseScope(meta["roles"], true); err != nil {
		return Doc{}, nil, fmt.Errorf("roles: %w", err)
	}
	if d.AllDepts, d.Depts, err = parseScope(meta["departments"], false); err != nil {
		return Doc{}, nil, fmt.Errorf("departments: %w", err)
	}

	var clauses []Clause
	var cur *Clause
	var body []string
	flush := func() {
		if cur != nil {
			cur.Text = strings.TrimSpace(strings.Join(body, "\n"))
			if cur.Text != "" {
				clauses = append(clauses, *cur)
			}
		}
		cur, body = nil, nil
	}
	for ; i < len(lines); i++ {
		l := strings.TrimRight(lines[i], " \t")
		if m := clauseHead.FindStringSubmatch(l); m != nil {
			flush()
			cur = &Clause{No: strings.Join(strings.Fields(m[1]), ""), Heading: strings.TrimSpace(m[2])}
			continue
		}
		if strings.HasPrefix(l, "#") { // 조항이 아닌 제목줄은 조항을 끝낸다
			flush()
			continue
		}
		if cur != nil {
			body = append(body, l)
		}
	}
	flush()
	if len(clauses) == 0 {
		return Doc{}, nil, errors.New("조항(## 제N조)이 없습니다")
	}
	return d, clauses, nil
}

// parseScope 는 "all" 또는 쉼표 목록이다. 비어 있으면 오류(fail-closed: 범위를 안 적은 문서를 전체 공개로 두지 않는다).
func parseScope(v string, lower bool) (all bool, list []string, err error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return false, nil, errors.New("값이 비어 있습니다")
	}
	seen := map[string]bool{}
	for _, p := range strings.Split(v, ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if lower {
			p = strings.ToLower(p)
		}
		if strings.EqualFold(p, "all") {
			all = true
			continue
		}
		if !seen[p] {
			seen[p] = true
			list = append(list, p)
		}
	}
	if all {
		return true, nil, nil
	}
	if len(list) == 0 {
		return false, nil, errors.New("값이 비어 있습니다")
	}
	sort.Strings(list)
	return false, list, nil
}

// Source 는 출처 표기다. 예: "구매 결재 규정 제3조(자기 결재 금지)".
func (d Doc) Source(c Clause) string {
	if c.Heading == "" {
		return d.Title + " " + c.No
	}
	return d.Title + " " + c.No + "(" + c.Heading + ")"
}
