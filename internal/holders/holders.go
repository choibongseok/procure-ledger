// Package holders 는 "그 법인에서 권한이 효력인 사용자"(알림 수신자 등)를 고른다(B44 10절). org 를 import 하지 않아(import 순환 방지)
// 소속·겸직 법인 조회를 한 번 더 쓴다.
package holders

import (
	"context"
	"database/sql"
	"sort"

	"github.com/choibongseok/procure-ledger/internal/contract"
)

type holderQuerier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// Of 는 그 법인에서 권한 perm 이 효력을 갖는 활성 사용자 번호다(정렬). 알림을 받는 사람을 고르는 데 쓴다(B44 10절: 법인 A 의 사건은 A 에서 그 권한을 가진 사람과
// 그룹 범위를 가진 사람에게만 간다). 효력 판정은 부여의 범위(own·entity·group)를 풀어서 한다: own 은 그날 소속·겸직 법인에 그 법인이 있을 때,
// 조직에 배치되지 않은 사람은 활성 법인이 그 법인 하나뿐일 때만 효력이 있다(Principal 을 만드는 인증 계층과 같은 규칙).
func Of(ctx context.Context, q holderQuerier, perm contract.Permission, entityID int64, day contract.Day) ([]int64, error) {
	rows, err := q.QueryContext(ctx, `SELECT DISTINCT e.user_id, e.scope, e.entity_id FROM user_effective_permissions e
		JOIN users u ON u.id = e.user_id AND u.active = 1 WHERE e.permission = ? ORDER BY e.user_id`, string(perm))
	if err != nil {
		return nil, err
	}
	type cand struct {
		uid, eid int64
		scope    string
	}
	var cands []cand
	for rows.Next() {
		var c cand
		if err := rows.Scan(&c.uid, &c.scope, &c.eid); err != nil {
			rows.Close()
			return nil, err
		}
		cands = append(cands, c)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	// 활성 법인이 하나뿐인지(조직 미배치 사람의 own 효력 규칙).
	var active int
	var only int64
	erows, err := q.QueryContext(ctx, `SELECT id FROM entities WHERE active = 1 ORDER BY id LIMIT 2`)
	if err != nil {
		return nil, err
	}
	for erows.Next() {
		var id int64
		if err := erows.Scan(&id); err != nil {
			erows.Close()
			return nil, err
		}
		active++
		only = id
	}
	if err := erows.Close(); err != nil {
		return nil, err
	}
	owned := map[int64][]int64{}
	seen := map[int64]bool{}
	var out []int64
	for _, c := range cands {
		if seen[c.uid] {
			continue
		}
		ok := false
		switch c.scope {
		case contract.ScopeGroup:
			ok = true
		case contract.ScopeEntity:
			ok = c.eid == entityID
		default:
			own, has := owned[c.uid]
			if !has {
				own, err = ownEntities(ctx, q, c.uid, day)
				if err != nil {
					return nil, err
				}
				if len(own) == 0 && active == 1 {
					own = []int64{only}
				}
				owned[c.uid] = own
			}
			for _, e := range own {
				if e == entityID {
					ok = true
				}
			}
		}
		if ok {
			seen[c.uid] = true
			out = append(out, c.uid)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out, nil
}

// ownEntities 는 사람의 소속·겸직 법인(그날 KST 기준)이다(org.EntitiesOf 와 같은 조회).
func ownEntities(ctx context.Context, q holderQuerier, userID int64, day contract.Day) ([]int64, error) {
	d := string(day)
	rows, err := q.QueryContext(ctx, `
SELECT s.entity_id FROM org_person_spans p
JOIN org_unit_spans s ON s.unit_id = p.primary_unit_id AND s.valid_from <= ?1 AND (s.valid_to IS NULL OR s.valid_to > ?1)
WHERE p.user_id = ?2 AND p.left_flag = 0 AND p.valid_from <= ?1 AND (p.valid_to IS NULL OR p.valid_to > ?1)
UNION
SELECT s.entity_id FROM org_concurrent_spans c
JOIN org_unit_spans s ON s.unit_id = c.unit_id AND s.valid_from <= ?1 AND (s.valid_to IS NULL OR s.valid_to > ?1)
WHERE c.user_id = ?2 AND c.valid_from <= ?1 AND (c.valid_to IS NULL OR c.valid_to > ?1)`, d, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var e int64
		if err := rows.Scan(&e); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// Member 는 사람이 그날 그 법인에 소속(주 소속 또는 겸직)인지다. 소속 법인이 정해지지 않은 사람(조직에 배치되지 않음)은 다른 법인 사람으로 보지 않는다:
// 그 사람의 권한이 그 법인에서 효력인지는 contract.Authorize 가 따로 막는다. 결재선·합의·참조·수신·위임에 다른 법인 사람을 넣지 못하게 하는 판정이다(B44 11.3).
func Member(ctx context.Context, q holderQuerier, userID, entityID int64, day contract.Day) (bool, error) {
	own, err := ownEntities(ctx, q, userID, day)
	if err != nil {
		return false, err
	}
	if len(own) == 0 {
		return true, nil
	}
	for _, e := range own {
		if e == entityID {
			return true, nil
		}
	}
	return false, nil
}

// UnitIn 은 조직이 그날 그 법인의 조직인지다(조직 구간의 법인 파생 열). 조직 구간이 없으면 참이다.
func UnitIn(ctx context.Context, q holderQuerier, unitID, entityID int64, day contract.Day) (bool, error) {
	rows, err := q.QueryContext(ctx, `SELECT entity_id FROM org_unit_spans WHERE unit_id = ?1 AND valid_from <= ?2 AND (valid_to IS NULL OR valid_to > ?2)`, unitID, string(day))
	if err != nil {
		return false, err
	}
	defer rows.Close()
	found := false
	for rows.Next() {
		var e int64
		if err := rows.Scan(&e); err != nil {
			return false, err
		}
		found = true
		if e == entityID {
			return true, nil
		}
	}
	// 조직 구간이 없는 조직(알 수 없는 조직)은 여기서 거르지 않는다: 조직을 찾지 못하는 오류는 호출부가 따로 낸다.
	return !found, rows.Err()
}
