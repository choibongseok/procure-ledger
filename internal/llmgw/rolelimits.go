package llmgw

import (
	"context"
	"sort"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
)

// RoleLimit 는 역할 하나의 오늘 사용량과 하루 한도다(AI 사용량 화면의 "오늘 사용량과 일일 한도" 표).
type RoleLimit struct {
	Role string `json:"role"`
	// UsedTokens 는 오늘 성공 호출의 입력+출력 토큰 합이다(게이트웨이의 한도 기준 날짜와 같은 UTC 날짜).
	UsedTokens int64 `json:"usedTokens"`
	// DailyLimit 는 역할 범위 일일 한도다. 설정이 없으면 nil.
	DailyLimit *int64 `json:"dailyTokenLimit"`
}

// RoleLimits 는 역할별 오늘 사용량과 한도다. llm:usage_read 가 필요하다.
// 모든 역할의 행은 한도 설정을 편집하는 권한(llm:policy_manage, 관리자)에게만 준다. 그 밖에는 본인 역할 한 줄이다.
// 한도는 읽기만 한다(편집은 관리자 설정 화면).
func (s *Store) RoleLimits(ctx context.Context) ([]RoleLimit, error) {
	p, err := contract.Authorize(ctx, contract.PermLLMUsageRead)
	if err != nil {
		return nil, err
	}
	n := s.now().UTC()
	start := time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, time.UTC).Format(time.RFC3339)

	byRole := map[string]*RoleLimit{}
	get := func(role string) *RoleLimit {
		if rl, ok := byRole[role]; ok {
			return rl
		}
		rl := &RoleLimit{Role: role}
		byRole[role] = rl
		return rl
	}

	qrows, err := s.DB.QueryContext(ctx, `SELECT scope_value, daily_token_limit FROM llm_daily_quotas WHERE scope_type = 'role'`)
	if err != nil {
		return nil, err
	}
	for qrows.Next() {
		var role string
		var lim int64
		if err := qrows.Scan(&role, &lim); err != nil {
			qrows.Close()
			return nil, err
		}
		l := lim
		get(role).DailyLimit = &l
	}
	if err := qrows.Err(); err != nil {
		qrows.Close()
		return nil, err
	}
	qrows.Close()

	urows, err := s.DB.QueryContext(ctx, `SELECT role FROM llm_usage WHERE at >= ? AND status = 'ok' GROUP BY role`, start)
	if err != nil {
		return nil, err
	}
	for urows.Next() {
		var role string
		if err := urows.Scan(&role); err != nil {
			urows.Close()
			return nil, err
		}
		get(role)
	}
	if err := urows.Err(); err != nil {
		urows.Close()
		return nil, err
	}
	urows.Close()
	// 역할별 사용량은 한도 판정과 같은 규칙으로 센다(그 역할을 가진 사람들의 사용량 + 대표 역할이 그 키였던 행).
	own, err := roleKeys(ctx, s.DB, p)
	if err != nil {
		return nil, err
	}
	for _, k := range own {
		get(k)
	}
	for role, rl := range byRole {
		if err := s.DB.QueryRowContext(ctx, `SELECT COALESCE(SUM(input_tokens + output_tokens),0) FROM llm_usage WHERE at >= ? AND status = 'ok' AND `+roleUsageFilter,
			start, role, role).Scan(&rl.UsedTokens); err != nil {
			return nil, err
		}
	}

	out := []RoleLimit{}
	if p.Can(contract.PermLLMPolicyManage) {
		for _, rl := range byRole {
			// 한도도 오늘 사용도 없는 역할은 화면에 줄 필요가 없다.
			if rl.DailyLimit == nil && rl.UsedTokens == 0 {
				continue
			}
			out = append(out, *rl)
		}
		sort.Slice(out, func(i, j int) bool { return out[i].Role < out[j].Role })
		return out, nil
	}
	for _, k := range own { // 본인이 가진 역할마다 한 줄(사용자 정의 역할 포함)
		out = append(out, *byRole[k])
	}
	if len(out) == 0 {
		out = append(out, RoleLimit{Role: ""})
	}
	return out, nil
}
