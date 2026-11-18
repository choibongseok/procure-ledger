package repository

import (
	"context"

	"github.com/choibongseok/procure-ledger/internal/contract"
)

// 평가 법인 분리 이관(B51 명세 5.2, 결정 D3). 0104 가 다법인 품목의 기존 평가 줄에 entity_basis = 0(품목 전체 기준)을 표시해 두면
// 부팅 때 (법인, 품목)마다 줄을 (장부일, 원장 번호) 순으로 걷는다.
//   - 마지막 닫힌 달 줄까지는 값 열을 건드리지 않는다(닫힌 달 불변). 걸으면서 잔량 = Σ 원장 수량, 잔액 = Σ 그 줄의 옛 금액만 센다.
//   - 그 뒤 첫 열린 달 줄부터는 센 상태를 시작 상태로 valuate 로 다시 계산해 모든 값 열을 쓰고 entity_basis = 1 로 만든다.
//
// 법인별 시작 잔액의 합은 옛 품목 잔액과 같다(금액은 줄 단위로 어느 한 법인에 속하므로 반올림 배분이 없다).

// SplitGroup 은 법인 기준으로 옮길 (법인, 품목)이다.
type SplitGroup struct{ EntityID, ItemID int64 }

// SplitOutcome 은 한 (법인, 품목) 이관 결과다.
type SplitOutcome struct {
	Recomputed int   // 다시 계산해 쓴 평가 줄 수
	StartQty   int64 // 다시 계산을 시작한 상태의 잔량(Σ 원장 수량)
	StartAmt   int64 // 다시 계산을 시작한 상태의 잔액 금액(Σ 옛 금액)
}

// ValuationSplitGroups 는 품목 전체 기준 줄(entity_basis = 0)이 남은 (법인, 품목) 목록이다.
func ValuationSplitGroups(ctx context.Context, q contract.Tx) ([]SplitGroup, error) {
	rows, err := q.QueryContext(ctx, `SELECT DISTINCT entity_id, item_id FROM stock_valuation WHERE entity_basis = 0 ORDER BY entity_id, item_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SplitGroup
	for rows.Next() {
		var g SplitGroup
		if err := rows.Scan(&g.EntityID, &g.ItemID); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// SplitValuationGroup 은 한 (법인, 품목)을 이관한다. 이미 옮긴 그룹이거나 열린 달 줄이 없으면 아무것도 쓰지 않는다(멱등).
func SplitValuationGroup(ctx context.Context, q contract.Tx, g SplitGroup) (SplitOutcome, error) {
	var out SplitOutcome
	if err := lockValuation(ctx, q, g.EntityID, g.ItemID); err != nil {
		return out, err
	}
	closed, err := SQLMonthClose{}.ClosedMonthsOf(ctx, q, g.EntityID)
	if err != nil {
		return out, err
	}
	type line struct {
		id, qty, amt, avg int64
		day               string
		basis             int
	}
	rs, err := q.QueryContext(ctx, `SELECT v.ledger_id, v.book_date, l.qty_delta, v.amount, v.avg_cost, v.entity_basis
		FROM stock_valuation v JOIN stock_ledger l ON l.id = v.ledger_id
		WHERE v.entity_id = ? AND v.item_id = ? ORDER BY v.book_date, v.ledger_id`, g.EntityID, g.ItemID)
	if err != nil {
		return out, err
	}
	var lines []line
	for rs.Next() {
		var l line
		if err := rs.Scan(&l.id, &l.day, &l.qty, &l.amt, &l.avg, &l.basis); err != nil {
			rs.Close()
			return out, err
		}
		lines = append(lines, l)
	}
	if err := rs.Close(); err != nil {
		return out, err
	}
	// 경계: 마지막 닫힌 달 줄 바로 다음. 그 앞은 읽기만 한다.
	boundary := 0
	for i, l := range lines {
		if len(l.day) >= 7 && closed[l.day[:7]] {
			boundary = i + 1
		}
	}
	pending := false
	for _, l := range lines[boundary:] {
		if l.basis == 0 {
			pending = true
			break
		}
	}
	if !pending {
		return out, nil
	}
	var st valState
	for _, l := range lines[:boundary] {
		st.balQty += l.qty
		st.balAmt += l.amt
	}
	if boundary > 0 {
		st.avg = lines[boundary-1].avg
	}
	if st.balQty > 0 {
		st.avg = roundDiv(st.balAmt, st.balQty)
	}
	out.StartQty, out.StartAmt = st.balQty, st.balAmt
	first := lines[boundary]
	if err := replay(ctx, q, g.EntityID, g.ItemID, first.day, first.id, st, nil); err != nil {
		return SplitOutcome{}, err
	}
	out.Recomputed = len(lines) - boundary
	return out, nil
}
