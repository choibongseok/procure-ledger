package org

import (
	"context"
	"database/sql"
	"sort"

	"github.com/choibongseok/procure-ledger/internal/contract"
)

// 다법인(B44 7절): 사람의 소속 법인과 겸직 법인. 조직의 법인은 구간 계산이 루트에서 물려받아 org_unit_spans.entity_id 에 둔다.

type entityQuerier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// EntitiesOf 는 사람의 소속 법인(주소속 조직의 법인)과 겸직 법인을 그날(KST) 기준으로 돌려준다(정렬·중복 제거).
// 첫 값 own 은 주소속 법인이고 조직에 배치되지 않았으면 0 이다. 조직도에 없는 사람(등록 전·퇴사)은 빈 목록과 0 이다.
func EntitiesOf(ctx context.Context, q entityQuerier, userID int64, day contract.Day) (own int64, all []int64, err error) {
	d := string(day)
	rows, err := q.QueryContext(ctx, `
SELECT 1, s.entity_id FROM org_person_spans p
JOIN org_unit_spans s ON s.unit_id = p.primary_unit_id AND s.valid_from <= ?1 AND (s.valid_to IS NULL OR s.valid_to > ?1)
WHERE p.user_id = ?2 AND p.left_flag = 0 AND p.valid_from <= ?1 AND (p.valid_to IS NULL OR p.valid_to > ?1)
UNION ALL
SELECT 2, s.entity_id FROM org_concurrent_spans c
JOIN org_unit_spans s ON s.unit_id = c.unit_id AND s.valid_from <= ?1 AND (s.valid_to IS NULL OR s.valid_to > ?1)
WHERE c.user_id = ?2 AND c.valid_from <= ?1 AND (c.valid_to IS NULL OR c.valid_to > ?1)`, d, userID)
	if err != nil {
		return 0, nil, err
	}
	defer rows.Close()
	seen := map[int64]bool{}
	for rows.Next() {
		var kind, e int64
		if err := rows.Scan(&kind, &e); err != nil {
			return 0, nil, err
		}
		if kind == 1 && own == 0 {
			own = e
		}
		if !seen[e] {
			seen[e] = true
			all = append(all, e)
		}
	}
	if err := rows.Err(); err != nil {
		return 0, nil, err
	}
	sort.Slice(all, func(i, j int) bool { return all[i] < all[j] })
	return own, all, nil
}
