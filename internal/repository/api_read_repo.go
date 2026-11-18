package repository

import (
	"context"
	"database/sql"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
)

// SQLAPIRead 는 연동 API(B42)의 번호 오름차순 증분 읽기다. 쓰기는 없다.
type SQLAPIRead struct{}

// ItemsAfter 는 품목과 현재 법인 창고의 재고(원장 합)다.
func (SQLAPIRead) ItemsAfter(ctx context.Context, q contract.Tx, after int64, limit int) ([]domain.APIItem, error) {
	rows, err := q.QueryContext(ctx, `SELECT i.id, i.sku, i.name, i.unit, i.safety_stock, i.track_lot,
		COALESCE((SELECT SUM(l.qty_delta) FROM stock_ledger l JOIN warehouses w ON w.id = l.warehouse_id WHERE `+contract.SQLEntity(ctx, "w")+` AND l.item_id = i.id), 0)
		FROM items i WHERE i.active = 1 AND i.id > ? ORDER BY i.id LIMIT ?`, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.APIItem
	for rows.Next() {
		var it domain.APIItem
		var lot int
		if err := rows.Scan(&it.ID, &it.SKU, &it.Name, &it.Unit, &it.SafetyStock, &lot, &it.OnHand); err != nil {
			return nil, err
		}
		it.TrackLot = lot == 1
		out = append(out, it)
	}
	return out, rows.Err()
}

// POsAfter 는 발주(줄 포함)다. scope 는 서비스가 호출자로 정한 열람 범위다.
func (SQLAPIRead) POsAfter(ctx context.Context, q contract.Tx, after int64, limit int, scope ViewScope) ([]domain.PurchaseOrder, error) {
	query, args := contract.EntitySQL(ctx, poSelect)+` WHERE id > ?`, []any{after}
	if c, a := poScopeCond(ctx, scope); c != "" {
		query += c
		args = append(args, a...)
	}
	query += ` ORDER BY id LIMIT ?`
	args = append(args, limit)
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	var out []domain.PurchaseOrder
	for rows.Next() {
		po, err := scanPO(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, po)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for i := range out {
		if out[i].Lines, err = poLines(ctx, q, out[i].ID); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// ReceiptsAfter 는 입고(줄 포함)다. 원 구매요청이 열람 범위 밖인 입고는 뺀다.
func (SQLAPIRead) ReceiptsAfter(ctx context.Context, q contract.Tx, after int64, limit int, scope ViewScope) ([]domain.APIReceipt, error) {
	query, args := `SELECT r.id, r.po_id, o.no, r.received_at FROM receipts r JOIN purchase_orders o ON o.id = r.po_id WHERE `+contract.SQLEntity(ctx, "r")+` AND r.id > ?`, []any{after}
	if !scope.All {
		cond, cargs := scope.Cond()
		query += ` AND o.pr_id IN (SELECT p.id FROM purchase_requests p WHERE ` + contract.SQLEntity(ctx, "p") + ` AND ` + cond + `)`
		args = append(args, cargs...)
	}
	query += ` ORDER BY r.id LIMIT ?`
	args = append(args, limit)
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	var out []domain.APIReceipt
	for rows.Next() {
		var rc domain.APIReceipt
		var at string
		if err := rows.Scan(&rc.ID, &rc.POID, &rc.PONo, &at); err != nil {
			rows.Close()
			return nil, err
		}
		rc.ReceivedAt = parseTime(at)
		out = append(out, rc)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for i := range out {
		lr, err := q.QueryContext(ctx, `SELECT rl.po_line_id, rl.item_id, rl.quantity, COALESCE(l.lot_no,''), COALESCE(l.expiry_date,'')
			FROM receipt_lines rl LEFT JOIN lots l ON l.id = rl.lot_id WHERE rl.receipt_id = ? ORDER BY rl.id`, out[i].ID)
		if err != nil {
			return nil, err
		}
		for lr.Next() {
			var l domain.ReceiptLine
			if err := lr.Scan(&l.POLineID, &l.ItemID, &l.Quantity, &l.LotNo, &l.ExpiryDate); err != nil {
				lr.Close()
				return nil, err
			}
			out[i].Lines = append(out[i].Lines, l)
		}
		if err := lr.Close(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// JournalsAfter 는 전표의 (번호, 전표 번호) 목록이다. 줄과 표현은 서비스가 전표 단건 조회로 채운다.
func (SQLAPIRead) JournalsAfter(ctx context.Context, q contract.Tx, after int64, limit int) ([]string, error) {
	rows, err := q.QueryContext(ctx, `SELECT no FROM journals WHERE `+contract.SQLEntity(ctx, "journals")+` AND id > ? ORDER BY id LIMIT ?`, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var no string
		if err := rows.Scan(&no); err != nil {
			return nil, err
		}
		out = append(out, no)
	}
	return out, rows.Err()
}

// PayablesAfter 는 채무 항목이다(취소된 항목도 상태로 보인다).
func (SQLAPIRead) PayablesAfter(ctx context.Context, q contract.Tx, after int64, limit int) ([]domain.APIPayable, error) {
	rows, err := q.QueryContext(ctx, `SELECT p.id, p.vendor_id, j.no, p.trade_date, p.due_date, p.amount, p.paid_amount, p.reserved_amount, p.canceled
		FROM payables p JOIN journals j ON j.id = p.journal_id WHERE `+contract.SQLEntity(ctx, "p")+` AND p.id > ? ORDER BY p.id LIMIT ?`, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.APIPayable
	for rows.Next() {
		var p domain.APIPayable
		var canceled int
		var due sql.NullString
		if err := rows.Scan(&p.ID, &p.VendorID, &p.JournalNo, &p.TradeDate, &due, &p.Amount, &p.Paid, &p.Reserved, &canceled); err != nil {
			return nil, err
		}
		p.DueDate = due.String
		switch {
		case canceled == 1:
			p.Status = domain.APIPayableCanceled
		case p.Paid >= p.Amount:
			p.Status = domain.APIPayablePaid
		case p.Paid > 0:
			p.Status = domain.APIPayablePartial
		default:
			p.Status = domain.APIPayableOpen
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
