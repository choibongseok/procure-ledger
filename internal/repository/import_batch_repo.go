package repository

import (
	"context"
	"fmt"
	"strings"

	"github.com/choibongseok/procure-ledger/internal/contract"
)

// 기초 데이터 가져오기(B35)의 큰 파일(수천~만 행)이 행마다 조회하지 않도록 한 번에 읽는 조회들이다.
// (재고 합 뷰를 행마다 부르면 원장이 커질수록 느려지므로, 품목·로트·기초 입고를 각각 한 번에 맵으로 읽는다.)

// ItemsAll 은 모든 품목(중지 포함)을 SKU(소문자) 키 맵으로 돌려준다. OnHand 는 원장 합이다.
func (SQLImport) ItemsAll(ctx context.Context, q contract.Tx) (map[string]ItemRow, error) {
	rows, err := q.QueryContext(ctx, `SELECT i.id, i.sku, i.name, i.unit, i.safety_stock, i.track_lot, i.active,
		COALESCE((SELECT SUM(l.qty_delta) FROM stock_ledger l WHERE l.item_id = i.id), 0) FROM items i`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]ItemRow{}
	for rows.Next() {
		var r ItemRow
		var lot, active int
		if err := rows.Scan(&r.ID, &r.SKU, &r.Name, &r.Unit, &r.SafetyStock, &lot, &active, &r.OnHand); err != nil {
			return nil, err
		}
		r.TrackLot, r.Active = lot == 1, active == 1
		out[strings.ToLower(r.SKU)] = r
	}
	return out, rows.Err()
}

// LotKey 는 (품목, 로트 번호)의 맵 키다.
func LotKey(itemID int64, lotNo string) string { return fmt.Sprintf("%d|%s", itemID, lotNo) }

// LotsAll 은 모든 로트 번호를 LotKey 키 맵으로 돌려준다.
func (SQLImport) LotsAll(ctx context.Context, q contract.Tx) (map[string]int64, error) {
	rows, err := q.QueryContext(ctx, `SELECT id, item_id, lot_no FROM lots`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var id, item int64
		var no string
		if err := rows.Scan(&id, &item, &no); err != nil {
			return nil, err
		}
		out[LotKey(item, no)] = id
	}
	return out, rows.Err()
}

// OpeningKey 는 (품목, 창고, 로트 번호 0=없음)의 맵 키다.
type OpeningKey struct{ Item, Warehouse, Lot int64 }

// OpeningVal 은 이미 기초로 들어간 수량과 단가다.
type OpeningVal struct{ Qty, UnitCost int64 }

// OpeningStockAll 은 이미 기초로 반영된 (품목, 창고, 로트)별 수량·단가를 한 번에 읽는다.
func (SQLImport) OpeningStockAll(ctx context.Context, q contract.Tx) (map[OpeningKey]OpeningVal, error) {
	rows, err := q.QueryContext(ctx, `SELECT l.item_id, l.warehouse_id, COALESCE(l.lot_id,0), COALESCE(SUM(l.qty_delta),0), COALESCE(MIN(v.in_unit_cost),0)
		FROM stock_ledger l LEFT JOIN stock_valuation v ON v.ledger_id = l.id WHERE l.warehouse_id IN (SELECT id FROM warehouses WHERE `+contract.SQLEntity(ctx, "")+`) AND l.ref_type = ? GROUP BY l.item_id, l.warehouse_id, COALESCE(l.lot_id,0)`, OpeningRef)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[OpeningKey]OpeningVal{}
	for rows.Next() {
		var k OpeningKey
		var v OpeningVal
		if err := rows.Scan(&k.Item, &k.Warehouse, &k.Lot, &v.Qty, &v.UnitCost); err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, rows.Err()
}

// OpeningPayablesAll 은 이미 기초에 들어간(취소되지 않은 전표의) 미지급 항목을 (거래처, 문서 번호)별 잔액으로 한 번에 읽는다.
func (SQLImport) OpeningPayablesAll(ctx context.Context, q contract.Tx) (map[string]int64, error) {
	rows, err := q.QueryContext(ctx, `SELECT o.vendor_id, o.doc_no, l.amount FROM opening_payable_lines o
		JOIN journal_lines l ON l.id = o.line_id JOIN journals j ON j.id = l.journal_id WHERE `+contract.SQLEntity(ctx, "j")+` AND j.status <> 'canceled'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var vendor, amt int64
		var doc string
		if err := rows.Scan(&vendor, &doc, &amt); err != nil {
			return nil, err
		}
		out[fmt.Sprintf("%d|%s", vendor, doc)] += amt
	}
	return out, rows.Err()
}
