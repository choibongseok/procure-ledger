package llmgw

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
)

// TodayCounts 는 오늘 호출 수와 토큰 합이다. Calls 는 성공 호출(예약 중인 것 포함), Blocked 는 한도·모델 허용으로 막힌 수다.
type TodayCounts struct {
	Calls   int64 `json:"calls"`
	Blocked int64 `json:"blocked"`
	Tokens  int64 `json:"tokens"`
}

// TodayLimit 는 호출자에게 적용되는 가장 좁은 일일 한도(사용자 > 부서 > 역할)와 그 범위의 오늘 사용량이다.
type TodayLimit struct {
	Scope      string `json:"scope"` // user | department | role
	DailyLimit int64  `json:"dailyTokenLimit"`
	// Used 는 한도가 걸린 범위(역할·부서) 전체의 오늘 사용량이다. 다른 사람의 사용량이 드러나므로 llm:usage_read 가 있을 때만 준다.
	Used *int64 `json:"usedTokens,omitempty"`
	// NearLimit 은 그 범위 사용량이 한도의 80% 이상이라는 뜻이다. 권한 없는 호출자에게는 이 한 비트만 알린다.
	NearLimit bool `json:"nearLimit"`
}

// nearLimitPercent 는 "한도에 가까움" 기준이다.
const nearLimitPercent = 80

// Today 는 오늘 AI 사용량 요약이다. Mine 은 본인, All 은 전사 합계로 llm:usage_read 가 있을 때만 채운다.
type Today struct {
	Day   string       `json:"day"`
	Mine  TodayCounts  `json:"mine"`
	All   *TodayCounts `json:"all,omitempty"`
	Limit *TodayLimit  `json:"limit,omitempty"` // 적용되는 한도가 없으면 생략
}

// TodaySummary 는 오늘(UTC 날짜, 게이트웨이의 한도 기준과 같다) 사용량 요약이다. 로그인한 누구나 본인 범위를 보고,
// 전사 합계는 llm:usage_read 가 있을 때만 담는다(범위 강제는 이 서비스에서 한다).
func (s *Store) TodaySummary(ctx context.Context) (Today, error) {
	p, err := contract.Authorize(ctx, contract.PermAuthenticated)
	if err != nil {
		return Today{}, err
	}
	n := s.now().UTC()
	start := time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, time.UTC).Format(time.RFC3339)
	out := Today{Day: start[:10]}

	counts := func(col string, arg any) (TodayCounts, error) {
		q := `SELECT COALESCE(SUM(CASE WHEN status='ok' THEN 1 ELSE 0 END),0),
			COALESCE(SUM(CASE WHEN status LIKE 'blocked%' THEN 1 ELSE 0 END),0),
			COALESCE(SUM(CASE WHEN status='ok' THEN input_tokens + output_tokens ELSE 0 END),0)
			FROM llm_usage WHERE at >= ?`
		args := []any{start}
		if col != "" {
			q += ` AND ` + col + ` = ?`
			args = append(args, arg)
		}
		var c TodayCounts
		err := s.DB.QueryRowContext(ctx, q, args...).Scan(&c.Calls, &c.Blocked, &c.Tokens)
		return c, err
	}
	if p.Can(contract.PermLLMUsageRead) {
		all, err := counts("", nil)
		if err != nil {
			return Today{}, err
		}
		out.All = &all
	}
	if out.Mine, err = counts("user_id", p.UserID); err != nil {
		return Today{}, err
	}

	scopes := []struct {
		typ, val, col string
		arg           any
	}{
		{"user", strconv.FormatInt(p.UserID, 10), "user_id", p.UserID},
		{"department", strconv.FormatInt(p.DepartmentID, 10), "department_id", p.DepartmentID},
	}
	pick := func(typ string, limit int64, used TodayCounts) {
		lim := &TodayLimit{Scope: typ, DailyLimit: limit, NearLimit: limit > 0 && used.Tokens*100 >= limit*nearLimitPercent}
		if p.Can(contract.PermLLMUsageRead) {
			u := used.Tokens
			lim.Used = &u
		}
		out.Limit = lim
	}
	for _, sc := range scopes {
		var limit int64
		err := s.DB.QueryRowContext(ctx, `SELECT daily_token_limit FROM llm_daily_quotas WHERE scope_type = ? AND scope_value = ?`, sc.typ, sc.val).Scan(&limit)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return Today{}, err
		}
		used, err := counts(sc.col, sc.arg)
		if err != nil {
			return Today{}, err
		}
		pick(sc.typ, limit, used)
		return out, nil
	}
	// 역할 한도: 가진 역할 키(사용자 정의 포함) 중 한도가 있는 것들 가운데 가장 빡빡한(남은 양이 가장 적은) 하나를 보여 준다.
	keys, err := roleKeys(ctx, s.DB, p)
	if err != nil {
		return Today{}, err
	}
	bestRemain, have := int64(0), false
	for _, k := range keys {
		var limit int64
		err := s.DB.QueryRowContext(ctx, `SELECT daily_token_limit FROM llm_daily_quotas WHERE scope_type = 'role' AND scope_value = ?`, k).Scan(&limit)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return Today{}, err
		}
		var c TodayCounts
		if err := s.DB.QueryRowContext(ctx, `SELECT COALESCE(SUM(CASE WHEN status='ok' THEN 1 ELSE 0 END),0),
			COALESCE(SUM(CASE WHEN status LIKE 'blocked%' THEN 1 ELSE 0 END),0),
			COALESCE(SUM(CASE WHEN status='ok' THEN input_tokens + output_tokens ELSE 0 END),0)
			FROM llm_usage WHERE at >= ? AND `+roleUsageFilter, start, k, k).Scan(&c.Calls, &c.Blocked, &c.Tokens); err != nil {
			return Today{}, err
		}
		if remain := limit - c.Tokens; !have || remain < bestRemain {
			have, bestRemain = true, remain
			pick("role", limit, c)
		}
	}
	return out, nil
}
