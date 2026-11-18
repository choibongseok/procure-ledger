package repository

import (
	"context"

	"github.com/choibongseok/procure-ledger/internal/contract"
)

// 월별 재고 현황 조회(B27 ④). 스냅샷 줄은 마감 때 저장한 값을, 열린 달은 BuildSnapshot 의 실시간 계산을 쓴다.

// StockNames 는 품목·창고 이름 표다.
type StockNames struct {
	Items      map[int64][2]string // 품목 번호 → {코드, 이름}
	Warehouses map[int64]struct {
		Name   string
		Active bool
	}
}

// Names 는 품목과 창고 이름을 한 번에 읽는다.
func (SQLMonthClose) Names(ctx context.Context, q contract.Tx) (StockNames, error) {
	out := StockNames{Items: map[int64][2]string{}, Warehouses: map[int64]struct {
		Name   string
		Active bool
	}{}}
	rows, err := q.QueryContext(ctx, `SELECT id, sku, name FROM items`)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var id int64
		var sku, name string
		if err := rows.Scan(&id, &sku, &name); err != nil {
			rows.Close()
			return out, err
		}
		out.Items[id] = [2]string{sku, name}
	}
	if err := rows.Close(); err != nil {
		return out, err
	}
	wr, err := q.QueryContext(ctx, `SELECT id, name, active FROM warehouses WHERE `+contract.SQLEntity(ctx, "warehouses")+` `)
	if err != nil {
		return out, err
	}
	defer wr.Close()
	for wr.Next() {
		var id int64
		var name string
		var active int
		if err := wr.Scan(&id, &name, &active); err != nil {
			return out, err
		}
		out.Warehouses[id] = struct {
			Name   string
			Active bool
		}{name, active == 1}
	}
	return out, wr.Err()
}

// SnapshotLines 는 마감 버전의 저장된 스냅샷 줄이다. 법인을 나누기 전에 마감한 달은 다른 법인 창고의 줄이 섞여 있을 수 있어(명세 5.4, D4)
// 읽을 때 현재 법인의 창고 줄만 남긴다. 저장된 값은 고치지 않는다(추가 전용).
func (SQLMonthClose) SnapshotLines(ctx context.Context, q contract.Tx, month string, version int) ([]SnapLine, error) {
	rows, err := q.QueryContext(ctx, `SELECT item_id, warehouse_id, open_qty, open_amount, in_qty, in_amount, out_qty, out_amount, transfer_qty, transfer_amount, adjust_qty, adjust_amount, close_qty, close_amount, avg_cost, entity_id
		FROM month_close_lines WHERE `+contract.SQLEntity(ctx, "")+` AND warehouse_id IN (SELECT id FROM warehouses WHERE `+contract.SQLEntity(ctx, "")+`) AND month = ? AND version = ? ORDER BY item_id, warehouse_id`, month, version)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SnapLine
	for rows.Next() {
		var l SnapLine
		if err := rows.Scan(&l.ItemID, &l.WarehouseID, &l.OpenQty, &l.OpenAmount, &l.InQty, &l.InAmount, &l.OutQty, &l.OutAmount, &l.TransferQty, &l.TransferAmount, &l.AdjustQty, &l.AdjustAmount, &l.CloseQty, &l.CloseAmount, &l.AvgCost, &l.EntityID); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// ItemBalanceAt 은 장부일 day 까지의 품목 잔액 금액이다(평가 줄이 없으면 0). 평가 키가 (법인, 품목)이므로 현재 법인의 잔액이고,
// 전체 모드(그룹 범위)에서는 법인별 마지막 줄의 잔액을 더한다. 품목 전체 기준(entity_basis = 0) 줄은 그 법인 줄의 금액 합으로 센다(B51 5.2).
func (SQLMonthClose) ItemBalanceAt(ctx context.Context, q contract.Tx, itemID int64, day string) (int64, error) {
	var a int64
	err := q.QueryRowContext(ctx, `SELECT COALESCE(SUM(CASE WHEN v.entity_basis = 1 THEN v.bal_amount
			ELSE (SELECT COALESCE(SUM(v3.amount),0) FROM stock_valuation v3 WHERE v3.entity_id = v.entity_id AND v3.item_id = v.item_id
				AND (v3.book_date < v.book_date OR (v3.book_date = v.book_date AND v3.ledger_id <= v.ledger_id))) END), 0)
		FROM stock_valuation v WHERE `+contract.SQLEntity(ctx, "v")+` AND v.item_id = ?
		AND v.ledger_id = (SELECT v2.ledger_id FROM stock_valuation v2 WHERE v2.entity_id = v.entity_id AND v2.item_id = v.item_id AND v2.book_date <= ? ORDER BY v2.book_date DESC, v2.ledger_id DESC LIMIT 1)`, itemID, day).Scan(&a)
	return a, err
}

// SnapshotForeignLines 는 마감 버전의 스냅샷에 들어 있지만 현재 법인의 창고가 아니어서 읽을 때 제외한 줄 수다(법인을 나누기 전 마감, 명세 5.4).
func (SQLMonthClose) SnapshotForeignLines(ctx context.Context, q contract.Tx, month string, version int) (int, error) {
	var n int
	err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM month_close_lines WHERE `+contract.SQLEntity(ctx, "")+` AND warehouse_id NOT IN (SELECT id FROM warehouses WHERE `+contract.SQLEntity(ctx, "")+`) AND month = ? AND version = ?`, month, version).Scan(&n)
	return n, err
}

// EntityRef 는 법인 번호와 이름이다.
type EntityRef struct {
	ID   int64
	Name string
}

// ActiveEntities 는 활성 법인을 번호 순으로 돌려준다(전체 모드의 법인별 보고서 나누기용).
func (SQLMonthClose) ActiveEntities(ctx context.Context, q contract.Tx) ([]EntityRef, error) {
	rows, err := q.QueryContext(ctx, `SELECT id, name FROM entities WHERE active = 1 ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []EntityRef
	for rows.Next() {
		var e EntityRef
		if err := rows.Scan(&e.ID, &e.Name); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
