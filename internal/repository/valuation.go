package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/dbutil"
)

// 재고 평가(B27 ①, 3.1): (법인, 품목) 단위 이동평균. B51 부터 평가 키는 (법인, 품목)이고 법인은 원장 줄의 창고가 정한다(D1).
// 원장은 바꾸지 않고 평가 표(stock_valuation)에 원장 행당 한 줄을 쓴다. 평가는 (장부일, 원장 번호) 순서로 쌓이는 품목별 잔량·잔액 금액이다.
//   - 입고: 금액 = 수량 × 발주 단가. 잔액 금액에 더한다(평균은 잔액 금액 ÷ 잔량).
//   - 출고·이동 출고·조정(−): 금액 = 반올림(잔액 금액 × 수량 ÷ 잔량). 잔량이 0 이 되면 잔액 금액 전부(잔돈이 남지 않는다).
//   - 이동 입고: 짝이 되는 이동 출고와 같은 금액(품목 전체 잔액·평균은 변하지 않는다).
//   - 조정(+): 금액 = 수량 × 그 시점 평균 단가. 평균을 모르면 직전 평균을 잇고, 한 번도 입고가 없었으면 0 원(warned).
// 금액은 정수 원이고 반올림은 0.5 올림이다.

// valState 는 한 품목의 평가 진행 상태다.
type valState struct {
	balQty, balAmt, avg int64
}

// valInput 은 평가할 원장 한 줄이다.
type valInput struct {
	qty    int64
	reason string
	inCost int64  // 입고 단가(입고 행)
	paired *int64 // 이동 입고의 짝 이동 출고 금액(절대값)
	fixed  *int64 // 법인 간 이동 줄의 동결 금액(부호 있음, 명세 7.3). 있으면 계산하지 않고 이 금액을 쓴다
}

// valOutput 은 한 줄의 평가 결과다.
type valOutput struct {
	unitCost, amount int64
	warned           bool
}

// roundDiv 는 a ÷ b 를 0.5 올림으로 나눈다(a ≥ 0, b > 0).
func roundDiv(a, b int64) int64 { return (2*a + b) / (2 * b) }

// valuate 는 이동평균 규칙으로 한 줄을 평가하고 상태를 갱신한다(순수 함수라 경계값을 표로 시험한다).
func valuate(st *valState, in valInput) valOutput {
	var out valOutput
	switch {
	case in.fixed != nil:
		out.amount = *in.fixed
	case in.reason == "receipt":
		out.amount = in.qty * in.inCost
	case in.reason == "transfer_in":
		if in.paired != nil {
			out.amount = *in.paired
		} else {
			out.amount = in.qty * st.avg
		}
	case in.qty > 0: // 조정(+)
		if st.balQty > 0 {
			st.avg = roundDiv(st.balAmt, st.balQty)
		}
		out.amount = in.qty * st.avg
		out.warned = st.avg == 0
	default: // 출고·이동 출고·조정(−)
		n := -in.qty
		switch {
		case st.balQty <= 0:
			out.amount = 0
		case n >= st.balQty:
			out.amount = -st.balAmt
		default:
			out.amount = -roundDiv(st.balAmt*n, st.balQty)
		}
	}
	st.balQty += in.qty
	st.balAmt += out.amount
	if st.balQty > 0 {
		st.avg = roundDiv(st.balAmt, st.balQty)
	}
	if in.qty != 0 {
		a := out.amount
		if a < 0 {
			a = -a
		}
		q := in.qty
		if q < 0 {
			q = -q
		}
		out.unitCost = roundDiv(a, q)
	}
	return out
}

// ErrBackdatedShortage 는 소급 출고의 장부일 시점 수량이 부족할 때다(명세 E13). 메시지는 서비스가 한국어로 다듬는다.
type ErrBackdatedShortage struct {
	Day      string
	ItemName string
	Have     int64
	Need     int64
}

func (e *ErrBackdatedShortage) Error() string {
	return fmt.Sprintf("%s 기준 재고가 부족합니다(품목 %s: 가능 %d, 요청 %d)", e.Day, e.ItemName, e.Have, e.Need)
}

func (e *ErrBackdatedShortage) Unwrap() error { return contract.ErrValidation }

const bookDay = `COALESCE(s.book_date, date(s.created_at, '+9 hours'))`

// stateBefore 는 (day, id) 바로 앞까지 쌓인 (법인, 품목) 평가 상태다. 앞선 줄이 없으면 0 이다.
// 앞선 줄이 이관 전 품목 전체 기준 값(entity_basis = 0)이면 그 열을 쓰지 않고 그 법인 줄의 합(Σ 수량, Σ 금액)으로 상태를 만든다(명세 5.2).
func stateBefore(ctx context.Context, q contract.Tx, entityID, itemID int64, day string, id int64) (valState, error) {
	var st valState
	var basis int
	err := q.QueryRowContext(ctx, `SELECT bal_qty, bal_amount, avg_cost, entity_basis FROM stock_valuation
		WHERE entity_id = ? AND item_id = ? AND (book_date < ? OR (book_date = ? AND ledger_id < ?)) ORDER BY book_date DESC, ledger_id DESC LIMIT 1`, entityID, itemID, day, day, id).Scan(&st.balQty, &st.balAmt, &st.avg, &basis)
	if errors.Is(err, sql.ErrNoRows) {
		return valState{}, nil
	}
	if err != nil || basis == 1 {
		return st, err
	}
	// 대체 계산: 열은 옛 품목 전체 값이라 이 법인 줄의 합으로 다시 센다. 평균은 잔량이 있으면 잔액÷잔량, 없으면 옛 평균을 잇는다.
	oldAvg := st.avg
	if err := q.QueryRowContext(ctx, `SELECT COALESCE(SUM(l.qty_delta),0), COALESCE(SUM(v.amount),0) FROM stock_valuation v JOIN stock_ledger l ON l.id = v.ledger_id
		WHERE v.entity_id = ? AND v.item_id = ? AND (v.book_date < ? OR (v.book_date = ? AND v.ledger_id < ?))`, entityID, itemID, day, day, id).Scan(&st.balQty, &st.balAmt); err != nil {
		return valState{}, err
	}
	st.avg = oldAvg
	if st.balQty > 0 {
		st.avg = roundDiv(st.balAmt, st.balQty)
	}
	return st, nil
}

type replayRow struct {
	id                 int64
	day, reason, rtype string
	refID, lotID, qty  int64
	hasVal             bool
	inCost             sql.NullInt64
	frozen             sql.NullInt64 // 법인 간 이동 줄의 동결 금액(절대값). 같은 법인 이동·다른 원장 줄은 NULL
}

// replay 는 (법인, 품목)의 (day, id) 이후 원장 줄(그 줄 포함)을 차례로 평가해 평가 표를 쓰거나 고친다.
// newCost 는 아직 평가 줄이 없는 새 입고 줄의 발주 단가다. 잔량이 음수가 되는 줄이 있으면 ErrBackdatedShortage 를 돌려준다.
func replay(ctx context.Context, q contract.Tx, entityID, itemID int64, day string, fromID int64, st valState, newCost map[int64]int64) error {
	rows, err := q.QueryContext(ctx, `SELECT s.id, `+bookDay+` AS d, s.reason, s.ref_type, s.ref_id, COALESCE(s.lot_id,0), s.qty_delta, v.ledger_id IS NOT NULL, v.in_unit_cost,
			CASE WHEN s.reason IN ('transfer_in','transfer_out') THEN (SELECT tl.cost_amount FROM stock_transfers tr JOIN stock_transfer_lines tl ON tl.transfer_id = tr.id
				WHERE tr.id = s.ref_id AND tr.from_entity_id <> tr.to_entity_id AND tl.item_id = s.item_id AND COALESCE(tl.lot_id,0) = COALESCE(s.lot_id,0)) END
		FROM stock_ledger s JOIN warehouses w ON w.id = s.warehouse_id LEFT JOIN stock_valuation v ON v.ledger_id = s.id
		WHERE w.entity_id = ? AND s.item_id = ? AND (`+bookDay+` > ? OR (`+bookDay+` = ? AND s.id >= ?)) ORDER BY d, s.id`, entityID, itemID, day, day, fromID)
	if err != nil {
		return err
	}
	var list []replayRow
	for rows.Next() {
		var r replayRow
		if err := rows.Scan(&r.id, &r.day, &r.reason, &r.rtype, &r.refID, &r.lotID, &r.qty, &r.hasVal, &r.inCost, &r.frozen); err != nil {
			rows.Close()
			return err
		}
		list = append(list, r)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	transferOut := map[[3]int64]int64{} // (이동 번호, 품목, 로트) → 이동 출고 금액(절대값)
	for _, r := range list {
		in := valInput{qty: r.qty, reason: r.reason}
		var inCost sql.NullInt64
		if r.inCost.Valid {
			inCost = r.inCost
		}
		if c, ok := newCost[r.id]; ok {
			inCost = sql.NullInt64{Int64: c, Valid: true}
		}
		in.inCost = inCost.Int64
		if r.frozen.Valid && (r.reason == "transfer_in" || r.reason == "transfer_out") {
			// 법인 간 이동은 이동을 기록한 시점에 동결한 금액을 쓴다(소급 재계산이 도착 법인으로 번지지 않는다, 명세 7.3 D5).
			f := r.frozen.Int64
			if r.qty < 0 {
				f = -f
			}
			in.fixed = &f
		}
		key := [3]int64{r.refID, itemID, r.lotID}
		if r.reason == "transfer_in" {
			if a, ok := transferOut[key]; ok {
				in.paired = &a
			}
		}
		out := valuate(&st, in)
		if r.reason == "transfer_out" {
			transferOut[key] = -out.amount
		}
		if st.balQty < 0 {
			var name string
			_ = q.QueryRowContext(ctx, `SELECT name FROM items WHERE id = ?`, itemID).Scan(&name)
			return &ErrBackdatedShortage{Day: r.day, ItemName: name, Have: st.balQty - r.qty, Need: -r.qty}
		}
		warned := 0
		if out.warned {
			warned = 1
		}
		if r.hasVal {
			_, err = q.ExecContext(ctx, `UPDATE stock_valuation SET book_date=?, unit_cost=?, amount=?, bal_qty=?, bal_amount=?, avg_cost=?, warned=?, entity_basis=1 WHERE entity_id=? AND ledger_id=?`,
				r.day, out.unitCost, out.amount, st.balQty, st.balAmt, st.avg, warned, entityID, r.id)
		} else {
			// 법인은 원장 줄의 창고가 정한다(B51). 새 줄은 항상 법인 기준(entity_basis = 1)이다.
			_, err = q.ExecContext(ctx, `INSERT INTO stock_valuation (ledger_id, item_id, book_date, in_unit_cost, unit_cost, amount, bal_qty, bal_amount, avg_cost, warned, entity_id, entity_basis)
				VALUES (?,?,?,?,?,?,?,?,?,?,?,1)`,
				r.id, itemID, r.day, nullInt(inCost), out.unitCost, out.amount, st.balQty, st.balAmt, st.avg, warned, entityID)
		}
		if err != nil {
			return mapErr(err)
		}
	}
	return nil
}

func nullInt(n sql.NullInt64) any {
	if !n.Valid {
		return nil
	}
	return n.Int64
}

// lastValuation 은 (법인, 품목)의 장부일 upTo 까지 마지막 평가 줄의 평균 단가·잔액 금액이다. 줄이 없으면 ok=false.
// 마지막 줄이 이관 전 품목 전체 기준(entity_basis = 0)이면 열을 쓰지 않고 그 법인 줄의 합으로 센다(명세 5.2).
func lastValuation(ctx context.Context, q contract.Tx, entityID, itemID int64, upTo string) (avg, amount int64, ok bool, err error) {
	var basis int
	err = q.QueryRowContext(ctx, `SELECT avg_cost, bal_amount, entity_basis FROM stock_valuation WHERE entity_id = ? AND item_id = ? AND book_date <= ? ORDER BY book_date DESC, ledger_id DESC LIMIT 1`,
		entityID, itemID, upTo).Scan(&avg, &amount, &basis)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, 0, false, nil
	}
	if err != nil || basis == 1 {
		return avg, amount, err == nil, err
	}
	var qty int64
	if err = q.QueryRowContext(ctx, `SELECT COALESCE(SUM(l.qty_delta),0), COALESCE(SUM(v.amount),0) FROM stock_valuation v JOIN stock_ledger l ON l.id = v.ledger_id
		WHERE v.entity_id = ? AND v.item_id = ? AND v.book_date <= ?`, entityID, itemID, upTo).Scan(&qty, &amount); err != nil {
		return 0, 0, false, err
	}
	if qty > 0 {
		avg = roundDiv(amount, qty)
	}
	return avg, amount, true, nil
}

// avgCostAt 은 (법인, 품목)의 장부일 upTo 까지 마지막 평균 단가다(평가 줄이 없으면 0).
func avgCostAt(ctx context.Context, q contract.Tx, entityID, itemID int64, upTo string) (int64, error) {
	avg, _, _, err := lastValuation(ctx, q, entityID, itemID, upTo)
	return avg, err
}

// entityOfWarehouse 는 창고가 속한 법인이다(원장 줄의 평가 법인). 창고가 없으면 기본 법인.
func entityOfWarehouse(ctx context.Context, q contract.Tx, warehouseID int64) (int64, error) {
	var id int64
	err := q.QueryRowContext(ctx, `SELECT entity_id FROM warehouses WHERE id = ?`, warehouseID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return contract.DefaultEntityID, nil
	}
	return id, err
}

// lockValuation 은 PG 에서 같은 (법인, 품목)의 평가 이어 쓰기를 직렬화한다(D16). 법인이 다르면 키가 달라 서로 기다리지 않는다. SQLite 는 연결이 하나라 할 일이 없다.
func lockValuation(ctx context.Context, q contract.Tx, entityID, itemID int64) error {
	return dbutil.AcquireKeyLock(ctx, q, fmt.Sprintf("valuation:%d:%d", entityID, itemID))
}

// valuateNewRow 는 방금 넣은 원장 줄(ledgerID)을 평가한다. 장부일이 뒤쪽 줄보다 앞서면(소급 출고) 같은 (법인, 품목)의 뒤쪽 평가 줄을 다시 계산한다.
func valuateNewRow(ctx context.Context, q contract.Tx, entityID, itemID, ledgerID int64, day string, inCost int64) error {
	if err := lockValuation(ctx, q, entityID, itemID); err != nil {
		return err
	}
	// 평가 줄이 없는 앞선 원장 행(도입 전 기록)이 남아 있으면 그 (법인, 품목)을 먼저 처음부터 재생한다(기동 때 재생과 같은 규칙).
	var unvalued int
	if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM stock_ledger s JOIN warehouses w ON w.id = s.warehouse_id LEFT JOIN stock_valuation v ON v.ledger_id = s.id
		WHERE w.entity_id = ? AND s.item_id = ? AND s.id <> ? AND v.ledger_id IS NULL`, entityID, itemID, ledgerID).Scan(&unvalued); err != nil {
		return err
	}
	if unvalued > 0 {
		_, _, err := backfillItem(ctx, q, entityID, itemID, map[int64]int64{ledgerID: inCost})
		return err
	}
	st, err := stateBefore(ctx, q, entityID, itemID, day, ledgerID)
	if err != nil {
		return err
	}
	return replay(ctx, q, entityID, itemID, day, ledgerID, st, map[int64]int64{ledgerID: inCost})
}

// BackfillValuation 은 평가 줄이 없는 원장 행을 가진 (법인, 품목)을 처음부터 재생한다(기동 때 한 번, 멱등). 원장은 읽기만 한다.
// 입고 단가는 입고 줄이 가리키는 발주 줄 단가다. 재생한 원장 행 수와 평균을 몰라 0 원으로 평가한 조정 행 수를 돌려준다.
func BackfillValuation(ctx context.Context, q contract.Tx) (rows, warned int, err error) {
	ids, err := q.QueryContext(ctx, `SELECT DISTINCT w.entity_id, s.item_id FROM stock_ledger s JOIN warehouses w ON w.id = s.warehouse_id LEFT JOIN stock_valuation v ON v.ledger_id = s.id WHERE v.ledger_id IS NULL ORDER BY w.entity_id, s.item_id`)
	if err != nil {
		return 0, 0, err
	}
	var items [][2]int64
	for ids.Next() {
		var k [2]int64
		if err := ids.Scan(&k[0], &k[1]); err != nil {
			ids.Close()
			return 0, 0, err
		}
		items = append(items, k)
	}
	if err := ids.Close(); err != nil {
		return 0, 0, err
	}
	for _, k := range items {
		if err := lockValuation(ctx, q, k[0], k[1]); err != nil {
			return 0, 0, err
		}
		n, w, err := backfillItem(ctx, q, k[0], k[1], nil)
		if err != nil {
			return 0, 0, err
		}
		rows, warned = rows+n, warned+w
	}
	return rows, warned, nil
}

// backfillItem 은 한 (법인, 품목)의 평가 줄을 지우고 원장을 처음부터 다시 재생한다. extra 는 입고 줄 단가를 직접 아는 원장 줄(방금 넣은 줄)의 단가다.
func backfillItem(ctx context.Context, q contract.Tx, entityID, itemID int64, extra map[int64]int64) (rows, warned int, err error) {
	if _, err := q.ExecContext(ctx, `DELETE FROM stock_valuation WHERE entity_id = ? AND item_id = ?`, entityID, itemID); err != nil {
		return 0, 0, err
	}
	costs, err := receiptCosts(ctx, q, itemID)
	if err != nil {
		return 0, 0, err
	}
	for id, c := range extra {
		if costs[id] == 0 {
			costs[id] = c
		}
	}
	if err := replay(ctx, q, entityID, itemID, "0000-00-00", 0, valState{}, costs); err != nil {
		return 0, 0, fmt.Errorf("법인 %d 품목 %d 평가 재생: %w", entityID, itemID, err)
	}
	err = q.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(warned),0) FROM stock_valuation WHERE entity_id = ? AND item_id = ?`, entityID, itemID).Scan(&rows, &warned)
	return rows, warned, err
}

// receiptCosts 는 한 품목의 입고 원장 줄별 발주 단가다(원장 번호 → 단가). 입고 줄과 수량·로트가 같은 줄의 발주 단가를 쓴다.
func receiptCosts(ctx context.Context, q contract.Tx, itemID int64) (map[int64]int64, error) {
	rows, err := q.QueryContext(ctx, `SELECT s.id,
			COALESCE((SELECT pl.unit_price FROM receipt_lines rl JOIN po_lines pl ON pl.id = rl.po_line_id
				WHERE rl.receipt_id = s.ref_id AND rl.item_id = s.item_id AND COALESCE(rl.lot_id,0) = COALESCE(s.lot_id,0) AND rl.quantity = s.qty_delta ORDER BY rl.id LIMIT 1),
			(SELECT pl.unit_price FROM receipt_lines rl JOIN po_lines pl ON pl.id = rl.po_line_id WHERE rl.receipt_id = s.ref_id AND rl.item_id = s.item_id ORDER BY rl.id LIMIT 1), 0)
		FROM stock_ledger s WHERE s.item_id = ? AND s.reason = 'receipt'`, itemID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]int64{}
	for rows.Next() {
		var id, cost int64
		if err := rows.Scan(&id, &cost); err != nil {
			return nil, err
		}
		out[id] = cost
	}
	return out, rows.Err()
}

// RevaluedRowsAfter 는 현재 법인의 그 품목에서 장부일이 day 보다 뒤인 평가 줄의 수다: 과거 날짜로 출고를 넣으면 이 줄들이 다시 계산된다(감사 diff 용).
// slipID 는 지금 넣은 출고 전표의 번호이고 그 전표 자신의 줄은 세지 않는다.
func (SQLInventory) RevaluedRowsAfter(ctx context.Context, q contract.Tx, itemID int64, day string, slipID int64) (int, error) {
	var n int
	err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM stock_ledger s JOIN warehouses w ON w.id = s.warehouse_id AND `+contract.SQLEntity(ctx, "w")+` JOIN stock_valuation v ON v.ledger_id = s.id
		WHERE s.item_id = ? AND `+bookDay+` > ? AND NOT (s.ref_type = 'issue_slip' AND s.ref_id = ?)`, itemID, day, slipID).Scan(&n)
	return n, err
}

// LockValuation 은 PG 에서 (법인, 품목)의 평가 이어 쓰기 잠금을 미리 잡는다. 법인 간 이동이 두 법인의 잠금을 법인 번호 순서로 잡아 반대 방향 이동끼리 교착하지 않게 한다(명세 7.6).
func LockValuation(ctx context.Context, q contract.Tx, entityID, itemID int64) error {
	return lockValuation(ctx, q, entityID, itemID)
}

// TransferCostLine 은 동결 금액을 구할 이동 줄(품목·수량)이다.
type TransferCostLine struct {
	ItemID, Qty int64
}

// TransferCosts 는 법인 간 이동 줄마다 이동 시점(장부일 day) 출발 법인 이동평균으로 환산한 금액(절대값)을 구한다(명세 7.3).
// 같은 품목 줄은 앞 줄이 먼저 빠진 상태에서 이어 계산한다. 출발 법인에 그 품목 평균 단가가 없으면 금액은 0 이다(차단하지 않는다, 호출자가 경고한다).
func (SQLInventory) TransferCosts(ctx context.Context, q contract.Tx, entityID int64, day string, lines []TransferCostLine) ([]int64, error) {
	states := map[int64]*valState{}
	out := make([]int64, len(lines))
	for i, l := range lines {
		st, ok := states[l.ItemID]
		if !ok {
			// 새 줄은 장부일 day 의 가장 뒤에 놓이므로 그날 모든 줄을 반영한 상태가 출발점이다.
			base, err := stateBefore(ctx, q, entityID, l.ItemID, day, math.MaxInt64)
			if err != nil {
				return nil, err
			}
			st = &base
			states[l.ItemID] = st
		}
		o := valuate(st, valInput{qty: -l.Qty, reason: "transfer_out"})
		out[i] = -o.amount
	}
	return out, nil
}
