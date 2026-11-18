package llmgw

import (
	"context"
	"database/sql"
	"sort"
	"strings"
)

// RouteRule 는 라우팅 규칙 한 줄이다(llm_routes, 명세 4절).
type RouteRule struct {
	ID                 int64  `json:"id"`
	SortOrder          int    `json:"sortOrder"`
	PurposePattern     string `json:"purposePattern"`
	DepartmentID       int64  `json:"departmentId"`
	Role               string `json:"role"`
	PrimaryProviderID  int64  `json:"primaryProviderId"`
	PrimaryModel       string `json:"primaryModel"`
	FallbackProviderID int64  `json:"fallbackProviderId"`
	FallbackModel      string `json:"fallbackModel"`
	MaxAttempts        int    `json:"maxAttempts"`
	TotalTimeoutMS     int    `json:"totalTimeoutMs"`
	Active             bool   `json:"active"`
}

// Subject 는 라우팅 판정의 입력이다(호출자의 부서·역할 키들과 용도 라벨).
type Subject struct {
	Purpose      string
	DepartmentID int64
	RoleKeys     []string // 호출자가 가진 역할 키(템플릿 키·사용자 정의 r_<번호>)
}

// patternScore 는 용도 패턴이 맞으면 구체성 점수(정확히 같음 > 접두 길이 > 전체)를, 안 맞으면 -1 을 돌려준다.
func patternScore(pattern, purpose string) int {
	switch {
	case pattern == "*":
		return 1
	case pattern == purpose:
		return 100000
	case strings.HasSuffix(pattern, ".*"):
		prefix := strings.TrimSuffix(pattern, "*") // "docverify."
		if strings.HasPrefix(purpose, prefix) {
			return 1000 + len(prefix)
		}
	}
	return -1
}

func hasRole(keys []string, role string) bool {
	for _, k := range keys {
		if k == role {
			return true
		}
	}
	return false
}

// PickRoute 는 명세 5절의 판정 순서로 규칙 하나를 고른다(순수 함수: 입력 순서와 무관).
// 후보: 켜진 규칙 중 용도 패턴이 맞고, 부서가 0 이거나 같고, 역할이 비었거나 호출자가 가진 역할인 것.
// 우선순위: ① 부서 지정 ② 용도 구체성 ③ 역할 지정 ④ sort_order ⑤ id. 후보가 없으면 ok=false(기본 경로).
func PickRoute(rules []RouteRule, s Subject) (RouteRule, bool) {
	type cand struct {
		r     RouteRule
		score int
	}
	var cs []cand
	for _, r := range rules {
		if !r.Active {
			continue
		}
		sc := patternScore(r.PurposePattern, s.Purpose)
		if sc < 0 {
			continue
		}
		if r.DepartmentID != 0 && r.DepartmentID != s.DepartmentID {
			continue
		}
		if r.Role != "" && !hasRole(s.RoleKeys, r.Role) {
			continue
		}
		cs = append(cs, cand{r, sc})
	}
	if len(cs) == 0 {
		return RouteRule{}, false
	}
	sort.SliceStable(cs, func(i, j int) bool {
		a, b := cs[i], cs[j]
		if (a.r.DepartmentID != 0) != (b.r.DepartmentID != 0) {
			return a.r.DepartmentID != 0
		}
		if a.score != b.score {
			return a.score > b.score
		}
		if (a.r.Role != "") != (b.r.Role != "") {
			return a.r.Role != ""
		}
		if a.r.SortOrder != b.r.SortOrder {
			return a.r.SortOrder < b.r.SortOrder
		}
		return a.r.ID < b.r.ID
	})
	return cs[0].r, true
}

type routeQuerier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

const routeCols = `id, sort_order, purpose_pattern, department_id, role, primary_provider_id, primary_model, COALESCE(fallback_provider_id, 0), fallback_model, max_attempts, total_timeout_ms, active`

// LoadRoutes 는 규칙 전체를 읽는다(화면 목록과 판정이 같이 쓴다).
func LoadRoutes(ctx context.Context, q routeQuerier, onlyActive bool) ([]RouteRule, error) {
	query := `SELECT ` + routeCols + ` FROM llm_routes`
	if onlyActive {
		query += ` WHERE active = 1`
	}
	rows, err := q.QueryContext(ctx, query+` ORDER BY sort_order, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RouteRule
	for rows.Next() {
		var r RouteRule
		var active int
		if err := rows.Scan(&r.ID, &r.SortOrder, &r.PurposePattern, &r.DepartmentID, &r.Role, &r.PrimaryProviderID, &r.PrimaryModel, &r.FallbackProviderID, &r.FallbackModel, &r.MaxAttempts, &r.TotalTimeoutMS, &active); err != nil {
			return nil, err
		}
		r.Active = active == 1
		out = append(out, r)
	}
	return out, rows.Err()
}
