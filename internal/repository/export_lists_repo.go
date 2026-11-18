package repository

import (
	"context"
	"fmt"
	"strings"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
)

// 내보내기·입고 이력용 목록 조회(B28 erp). 열람 범위는 구매요청 조건(ViewScope.Cond)을 그대로 쓴다.

// SQLExportLists 는 목록 저장소다.
type SQLExportLists struct{}

// Receipts 는 입고 줄 단위 이력이다(최근 입고가 먼저, limit 행까지).
func (SQLExportLists) Receipts(ctx context.Context, q contract.Tx, f domain.ReceiptFilter, scope ViewScope, limit int) ([]domain.ReceiptRow, error) {
	where := []string{"1=1"}
	var args []any
	if f.From != "" {
		where = append(where, "date(r.received_at, '+9 hours') >= ?")
		args = append(args, f.From)
	}
	if f.To != "" {
		where = append(where, "date(r.received_at, '+9 hours') <= ?")
		args = append(args, f.To)
	}
	if f.WarehouseID != 0 {
		where = append(where, "s.warehouse_id = ?")
		args = append(args, f.WarehouseID)
	}
	if f.POID != 0 {
		where = append(where, "r.po_id = ?")
		args = append(args, f.POID)
	}
	if !scope.All {
		cond, cargs := scope.Cond()
		where = append(where, "po.pr_id IN (SELECT p.id FROM purchase_requests p WHERE p.entity_id = po.entity_id AND "+cond+")")
		args = append(args, cargs...)
	}
	args = append(args, limit)
	rows, err := q.QueryContext(ctx, `SELECT r.id, po.no, i.name, COALESCE(t.lot_no,''), COALESCE(t.expiry_date,''), rl.quantity, COALESCE(w.name,''), r.received_at
		FROM receipt_lines rl JOIN receipts r ON r.id = rl.receipt_id JOIN purchase_orders po ON po.id = r.po_id JOIN items i ON i.id = rl.item_id
		LEFT JOIN lots t ON t.id = rl.lot_id
		LEFT JOIN stock_ledger s ON s.ref_type = 'receipt' AND s.ref_id = r.id AND s.item_id = rl.item_id AND COALESCE(s.lot_id,0) = COALESCE(rl.lot_id,0)
		LEFT JOIN warehouses w ON w.id = s.warehouse_id
		WHERE `+contract.SQLEntity(ctx, "r")+` AND `+strings.Join(where, " AND ")+` ORDER BY r.id DESC, rl.id LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.ReceiptRow{}
	for rows.Next() {
		var x domain.ReceiptRow
		var id int64
		var at string
		if err := rows.Scan(&id, &x.PONo, &x.ItemName, &x.LotNo, &x.ExpiryDate, &x.Quantity, &x.WarehouseName, &at); err != nil {
			return nil, err
		}
		x.ReceiptNo, x.ReceivedAt = fmt.Sprintf("RC-%06d", id), parseTime(at)
		out = append(out, x)
	}
	return out, rows.Err()
}

// PurchaseOrders 는 발주 목록이다(최근 발주가 먼저, limit 행까지, 거래처 이름 포함).
func (SQLExportLists) PurchaseOrders(ctx context.Context, q contract.Tx, status domain.POStatus, scope ViewScope, limit int) ([]domain.POExportRow, error) {
	query := `SELECT po.no, po.status, po.total_amount, COALESCE(v.name,''), po.created_at, COALESCE(po.due_date,''), po.delivery_place
		FROM purchase_orders po LEFT JOIN vendors v ON v.id = po.vendor_id WHERE ` + contract.SQLEntity(ctx, "po") + ` AND 1=1`
	var args []any
	if status != "" {
		query += ` AND po.status = ?`
		args = append(args, string(status))
	}
	if !scope.All {
		cond, cargs := scope.Cond()
		query += ` AND po.pr_id IN (SELECT p.id FROM purchase_requests p WHERE ` + contract.SQLEntity(ctx, "p") + ` AND ` + cond + `)`
		args = append(args, cargs...)
	}
	query += ` ORDER BY po.id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.POExportRow{}
	for rows.Next() {
		var x domain.POExportRow
		var st, at string
		if err := rows.Scan(&x.No, &st, &x.TotalAmount, &x.VendorName, &at, &x.DueDate, &x.DeliveryPlace); err != nil {
			return nil, err
		}
		x.Status, x.CreatedAt = domain.POStatus(st), parseTime(at)
		out = append(out, x)
	}
	return out, rows.Err()
}

// Stock 는 창고·품목·로트별 재고 평평한 목록이다(수량이 0 이 아닌 줄만, 품목·창고 순).
func (SQLExportLists) Stock(ctx context.Context, q contract.Tx, f domain.StockExportFilter, limit int) ([]domain.StockExportRow, error) {
	// 부족량·안전재고 미달 판정의 합계도 현재 법인의 창고만 더한다(품목은 법인 공통, 수량은 법인 소유 창고).
	total := `COALESCE((SELECT SUM(x.on_hand) FROM v_wh_lot_stock x JOIN warehouses xw ON xw.id = x.warehouse_id WHERE ` + contract.SQLEntity(ctx, "xw") + ` AND x.item_id = i.id),0)`
	query := `SELECT w.name, i.sku, i.name, COALESCE(t.lot_no,''), COALESCE(t.expiry_date,''), s.on_hand, i.safety_stock,
			CASE WHEN (i.safety_stock - ` + total + `) > 0 THEN (i.safety_stock - ` + total + `) ELSE 0 END
		FROM v_wh_lot_stock s JOIN items i ON i.id = s.item_id JOIN warehouses w ON w.id = s.warehouse_id LEFT JOIN lots t ON t.id = NULLIF(s.lot_id,0)
		WHERE ` + contract.SQLEntity(ctx, "w") + ` AND s.on_hand <> 0`
	var args []any
	if f.WarehouseID != 0 {
		query += ` AND s.warehouse_id = ?`
		args = append(args, f.WarehouseID)
	}
	if f.BelowSafety {
		query += ` AND i.safety_stock > ` + total
	}
	query += ` ORDER BY i.sku, w.id, COALESCE(t.lot_no,'') LIMIT ?`
	args = append(args, limit)
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.StockExportRow{}
	for rows.Next() {
		var x domain.StockExportRow
		if err := rows.Scan(&x.WarehouseName, &x.SKU, &x.ItemName, &x.LotNo, &x.ExpiryDate, &x.Quantity, &x.SafetyStock, &x.Shortage); err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}
