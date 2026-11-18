package sod

import (
	"context"
	"sort"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
)

// HoldSet 은 hold 규칙별로 두 권한을 함께 가진 사용자 집합이다.
type HoldSet map[string]map[int64]bool

// Guard 는 hold 규칙은 변경 전 Snapshot 후 실효 권한을 다시 계산한 뒤 CheckNew 로 판정한다(명세 8.4, D14): 변경으로 새로 생긴 (규칙, 사용자) 쌍만 거부하고 이미 있던 쌍은 건드리지 않는다.
// Guard 는 access 가 받는 인터페이스다: 거래 판정(자기 부여·위험 역할 승인)과 hold 판정.
type Guard interface {
	Evaluator
	SnapshotHolds(ctx context.Context, tx contract.Tx) (HoldSet, error)
	CheckNewHolds(ctx context.Context, tx contract.Tx, actor contract.Principal, before HoldSet) error
}

var _ Guard = (*Engine)(nil)

// SnapshotHolds 는 지금 두 권한을 함께 가진 사용자를 규칙별로 읽는다(꺼진 규칙은 건너뛴다).
func (e *Engine) SnapshotHolds(ctx context.Context, tx contract.Tx) (HoldSet, error) {
	return Holds(ctx, tx)
}

// Holds 는 모든 hold 규칙의 보유자 집합이다. 두 권한은 **법인 범위가 겹칠 때만** 한 사람에게 함께 있는 것으로 센다(B44 D16):
// 법인 A 에서 payment:write, 법인 B 에서 payment:approve 를 갖는 것은 한 법인 안의 분리 위반이 아니다. group 범위는 모든 법인과 겹친다.
func Holds(ctx context.Context, q querier) (HoldSet, error) {
	out := HoldSet{}
	ownCache := map[int64]map[int64]bool{}
	var active []int64
	activeLoaded := false
	for _, r := range Rules {
		if r.Kind != KindHold {
			continue
		}
		rows, err := q.QueryContext(ctx, `SELECT DISTINCT a.user_id, a.scope, a.entity_id, b.scope, b.entity_id FROM user_effective_permissions a
			JOIN user_effective_permissions b ON b.user_id = a.user_id AND b.permission = ? WHERE a.permission = ?`, string(r.PermB), string(r.PermA))
		if err != nil {
			return nil, err
		}
		type pair struct {
			uid    int64
			sa, sb string
			ea, eb int64
		}
		var pairs []pair
		for rows.Next() {
			var p pair
			if err := rows.Scan(&p.uid, &p.sa, &p.ea, &p.sb, &p.eb); err != nil {
				rows.Close()
				return nil, err
			}
			pairs = append(pairs, p)
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
		set := map[int64]bool{}
		for _, p := range pairs {
			if set[p.uid] {
				continue
			}
			if p.sa == contract.ScopeGroup || p.sb == contract.ScopeGroup {
				set[p.uid] = true
				continue
			}
			if !activeLoaded {
				if active, err = activeEntities(ctx, q); err != nil {
					return nil, err
				}
				activeLoaded = true
			}
			if _, ok := ownCache[p.uid]; !ok {
				if ownCache[p.uid], err = ownEntitiesOf(ctx, q, p.uid, active); err != nil {
					return nil, err
				}
			}
			own := ownCache[p.uid]
			if scopeSet(p.sa, p.ea, own).intersects(scopeSet(p.sb, p.eb, own)) {
				set[p.uid] = true
			}
		}
		out[r.Code] = set
	}
	return out, nil
}

type entSet map[int64]bool

func scopeSet(scope string, entityID int64, own map[int64]bool) entSet {
	if scope == contract.ScopeEntity {
		return entSet{entityID: true}
	}
	return entSet(own)
}

func (a entSet) intersects(b entSet) bool {
	for id := range a {
		if b[id] {
			return true
		}
	}
	return false
}

func activeEntities(ctx context.Context, q querier) ([]int64, error) {
	rows, err := q.QueryContext(ctx, `SELECT id FROM entities WHERE active = 1 ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// ownEntitiesOf 는 own 범위가 풀리는 법인(소속·겸직 법인)이다. 조직에 배치되지 않은 사람은 활성 법인이 하나뿐일 때만 그 법인이다
// (auth 가 Principal 을 만드는 규칙과 같다. sod 는 org 를 import 할 수 없어 같은 조회를 한 번 더 쓴다).
func ownEntitiesOf(ctx context.Context, q querier, userID int64, active []int64) (map[int64]bool, error) {
	d := time.Now().In(kst).Format("2006-01-02")
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
	out := map[int64]bool{}
	for rows.Next() {
		var e int64
		if err := rows.Scan(&e); err != nil {
			return nil, err
		}
		out[e] = true
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 && len(active) == 1 {
		out[active[0]] = true
	}
	return out, nil
}

// CheckNewHolds 는 변경으로 새로 생긴 쌍을 규칙 모드대로 처리한다: 차단이면 *BlockedError(변경 전체 롤백), 경고면 위반 기록.
func (e *Engine) CheckNewHolds(ctx context.Context, tx contract.Tx, actor contract.Principal, before HoldSet) error {
	after, err := Holds(ctx, tx)
	if err != nil {
		return err
	}
	for _, r := range Rules {
		if r.Kind != KindHold {
			continue
		}
		mode := ModeOf(ctx, tx, r)
		if mode == ModeOff {
			continue
		}
		var added []int64
		for id := range after[r.Code] {
			if !before[r.Code][id] {
				added = append(added, id)
			}
		}
		sort.Slice(added, func(i, j int) bool { return added[i] < added[j] })
		for _, uid := range added {
			c := Check{Rule: r.Code, Actor: actor, Subject: Ref{Type: "user", ID: uid}}
			if mode == ModeBlock {
				return &BlockedError{Check: c}
			}
			if err := e.writeViolation(ctx, tx, c, r, "allowed", ModeWarn, 0); err != nil {
				return err
			}
		}
	}
	return nil
}
