package llmgw

import (
	"context"
	"database/sql"
	"sort"

	"github.com/choibongseok/procure-ledger/internal/contract"
)

// B33: 모델 허용 규칙과 일일 한도의 "역할" 범위는 사용자의 역할 이름 하나(Principal.Role)가 아니라 **그 사람이 가진 역할 키 전체**로 판정한다.
// 역할 키는 기본 템플릿(admin·finance·manager·staff·auditor)이거나 사용자 정의 역할(r_<번호>)이다. 키는 실효 권한 표(user_effective_permissions)의
// 부여 경로(사람·조직·직위·조직장)를 모두 따르고, 호환용 Principal.Role 이 있으면 함께 센다.

// queryer 는 읽기에 필요한 최소 인터페이스다(*sql.DB·contract.Tx 가 만족한다).
type queryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// roleKeys 는 사용자가 가진 역할 키의 정렬된 집합이다. 조회 실패는 오류로 돌려준다(호출자가 fail-closed 로 처리한다).
func roleKeys(ctx context.Context, q queryer, p contract.Principal) ([]string, error) {
	set := map[string]bool{}
	if p.Role != "" {
		set[string(p.Role)] = true
	}
	if p.UserID != 0 {
		rows, err := q.QueryContext(ctx, `SELECT DISTINCT r.key FROM user_effective_permissions e JOIN roles r ON r.id = e.role_id
			WHERE e.user_id = ? AND r.deleted_at IS NULL`, p.UserID)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		for rows.Next() {
			var k string
			if err := rows.Scan(&k); err != nil {
				return nil, err
			}
			set[k] = true
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out, nil
}

func hasKey(keys []string, k string) bool {
	for _, x := range keys {
		if x == k {
			return true
		}
	}
	return false
}

// primaryRole 은 사용량 행(llm_usage.role)에 남기는 대표 역할 키다: 호환용 Principal.Role 이 있으면 그것, 없으면 키 중 첫째(없으면 "").
func primaryRole(p contract.Principal, keys []string) string {
	if p.Role != "" {
		return string(p.Role)
	}
	if len(keys) > 0 {
		return keys[0]
	}
	return ""
}

// roleUsageFilter 는 역할 키 하나의 오늘 사용량을 세는 조건이다: 그 역할을 지금 가진 사람들의 사용량 + 호출 당시 대표 역할이 그 키였던 행.
// (한 사람이 여러 역할을 가져도 대표 역할 한 곳에만 쌓이지 않고, 사용자 정의 역할의 한도도 실제 사용량을 센다.)
const roleUsageFilter = `(role = ? OR user_id IN (SELECT e.user_id FROM user_effective_permissions e JOIN roles r ON r.id = e.role_id WHERE r.key = ? AND r.deleted_at IS NULL))`

// knownRoleKey 는 키가 템플릿 또는 지워지지 않은 사용자 정의 역할인지 본다(규칙·한도 입력 검증용).
func knownRoleKey(ctx context.Context, q interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}, key string) bool {
	for _, r := range contract.Roles {
		if string(r) == key {
			return true
		}
	}
	var one int
	return q.QueryRowContext(ctx, `SELECT 1 FROM roles WHERE key = ? AND deleted_at IS NULL`, key).Scan(&one) == nil
}
