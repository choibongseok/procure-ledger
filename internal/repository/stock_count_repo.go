package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/dbutil"
)

// SQLStockCount 는 재고 실사 저장소와 수불부 조회다(B24 ③④). 원장 쓰기는 SQLInventory.InsertLedgerWH 한 곳이다.
type SQLStockCount struct{}

const countSelect = `SELECT c.id, c.no, c.warehouse_id, w.name, c.base_date, c.status, COALESCE(c.doc_id,0), COALESCE(p.no,''), c.reject_reason, u.name, c.created_at, c.applied_at, c.snapshot_ledger_id
	FROM stock_counts c JOIN warehouses w ON w.id = c.warehouse_id AND {entity_id:w} JOIN users u ON u.id = c.created_by LEFT JOIN purchase_requests p ON p.id = c.doc_id`

func scanCount(s rowScanner) (domain.StockCount, int64, error) {
	var c domain.StockCount
	var created string
	var applied sql.NullString
	var snap int64
	err := s.Scan(&c.ID, &c.No, &c.WarehouseID, &c.WarehouseName, &c.BaseDate, &c.Status, &c.DocID, &c.DocNo, &c.RejectReason, &c.CreatedByName, &created, &applied, &snap)
	c.CreatedAt = parseTime(created)
	if applied.Valid {
		t := parseTime(applied.String)
		c.AppliedAt = &t
	}
	return c, snap, err
}

// Count 는 실사 표 한 건(줄 포함)이다. 없으면 ErrNotFound. 두 번째 값은 전산 수량을 잡은 시점의 원장 번호다.
func (r SQLStockCount) Count(ctx context.Context, q contract.Tx, id int64) (domain.StockCount, int64, error) {
	c, snap, err := scanCount(q.QueryRowContext(ctx, contract.EntitySQL(ctx, countSelect)+` WHERE c.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return c, 0, contract.ErrNotFound
	}
	if err != nil {
		return c, 0, err
	}
	c.Lines, err = r.Lines(ctx, q, id)
	finishCount(&c)
	return c, snap, err
}

// CountByDoc 는 조정 전표(양식 문서)에 연결된 실사 표다. 없으면 ErrNotFound.
func (r SQLStockCount) CountByDoc(ctx context.Context, q contract.Tx, docID int64) (domain.StockCount, int64, error) {
	var id int64
	err := q.QueryRowContext(ctx, `SELECT id FROM stock_counts WHERE doc_id = ?`, docID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.StockCount{}, 0, contract.ErrNotFound
	}
	if err != nil {
		return domain.StockCount{}, 0, err
	}
	return r.Count(ctx, q, id)
}

func finishCount(c *domain.StockCount) {
	c.DiffLines, c.Uncounted = 0, 0
	for i := range c.Lines {
		l := &c.Lines[i]
		if l.CountedQty == nil {
			c.Uncounted++
			l.Diff = 0
			continue
		}
		l.Diff = *l.CountedQty - l.SystemQty
		if l.Diff != 0 {
			c.DiffLines++
		}
	}
}

// Counts 는 실사 목록(줄 없이)이다. 줄 수와 차이 줄 수는 채운다.
func (r SQLStockCount) Counts(ctx context.Context, q contract.Tx, f domain.CountFilter) ([]domain.StockCount, error) {
	where, args := ` WHERE 1=1`, []any{}
	if f.WarehouseID != 0 {
		where, args = where+` AND c.warehouse_id = ?`, append(args, f.WarehouseID)
	}
	if f.Status != "" {
		where, args = where+` AND c.status = ?`, append(args, f.Status)
	}
	limit := f.Limit
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	args = append(args, limit)
	rows, err := q.QueryContext(ctx, contract.EntitySQL(ctx, countSelect)+where+` ORDER BY c.id DESC LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	var out []domain.StockCount
	for rows.Next() {
		c, _, err := scanCount(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, c)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for i := range out {
		if err := q.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(CASE WHEN counted_qty IS NOT NULL AND counted_qty <> system_qty THEN 1 ELSE 0 END),0) FROM stock_count_lines WHERE count_id = ?`, out[i].ID).Scan(new(int), &out[i].DiffLines); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// Lines 는 실사 표의 줄이다.
func (SQLStockCount) Lines(ctx context.Context, q contract.Tx, countID int64) ([]domain.StockCountLine, error) {
	rows, err := q.QueryContext(ctx, `SELECT l.item_id, i.name, i.sku, COALESCE(l.lot_id,0), COALESCE(t.lot_no,''), l.system_qty, l.counted_qty, l.reason
		FROM stock_count_lines l JOIN items i ON i.id = l.item_id LEFT JOIN lots t ON t.id = l.lot_id WHERE l.count_id = ? ORDER BY i.sku, l.lot_key`, countID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.StockCountLine
	for rows.Next() {
		var l domain.StockCountLine
		var counted sql.NullInt64
		if err := rows.Scan(&l.ItemID, &l.ItemName, &l.SKU, &l.LotID, &l.LotNo, &l.SystemQty, &counted, &l.Reason); err != nil {
			return nil, err
		}
		if counted.Valid {
			v := counted.Int64
			l.CountedQty = &v
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// LedgerMaxID 는 원장의 마지막 번호다(전산 수량 스냅샷 시점 표시).
func (SQLStockCount) LedgerMaxID(ctx context.Context, q contract.Tx) (int64, error) {
	var n int64
	err := q.QueryRowContext(ctx, `SELECT COALESCE(MAX(id),0) FROM stock_ledger`).Scan(&n)
	return n, err
}

// SnapshotQty 는 그 창고의 재고가 있는 (품목, 로트) 전산 수량이다.
func (SQLStockCount) SnapshotQty(ctx context.Context, q contract.Tx, whID int64) ([]domain.StockCountLine, error) {
	rows, err := q.QueryContext(ctx, `SELECT item_id, lot_id, on_hand FROM v_wh_lot_stock WHERE warehouse_id = ? AND on_hand <> 0`, whID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.StockCountLine
	for rows.Next() {
		var l domain.StockCountLine
		if err := rows.Scan(&l.ItemID, &l.LotID, &l.SystemQty); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

func (SQLStockCount) InsertCount(ctx context.Context, q contract.Tx, no string, whID int64, baseDate string, actor, snapshot int64, now time.Time) (int64, error) {
	id, err := dbutil.InsertID(ctx, q, `INSERT INTO stock_counts (no, warehouse_id, base_date, status, created_by, created_at, updated_at, snapshot_ledger_id) VALUES (?,?,?,'draft',?,?,?,?)`,
		no, whID, baseDate, actor, fmtTime(now), fmtTime(now), snapshot)
	if err != nil {
		return 0, mapErr(err)
	}
	return id, nil
}

func (SQLStockCount) InsertLine(ctx context.Context, q contract.Tx, countID int64, l domain.StockCountLine) error {
	_, err := q.ExecContext(ctx, `INSERT INTO stock_count_lines (count_id, item_id, lot_id, lot_key, system_qty, counted_qty, reason) VALUES (?,?,?,?,?,?,?)`,
		countID, l.ItemID, nullID(l.LotID), l.LotID, l.SystemQty, nullCounted(l.CountedQty), l.Reason)
	return mapErr(err)
}

func nullCounted(v *int64) any {
	if v == nil {
		return nil
	}
	return *v
}

// UpdateLine 은 줄의 실제 수량·사유를 고친다. 없으면 false.
func (SQLStockCount) UpdateLine(ctx context.Context, q contract.Tx, countID int64, l domain.CountLineInput) (bool, error) {
	res, err := q.ExecContext(ctx, `UPDATE stock_count_lines SET counted_qty = ?, reason = ? WHERE count_id = ? AND item_id = ? AND lot_key = ?`,
		nullCounted(l.CountedQty), l.Reason, countID, l.ItemID, l.LotID)
	if err != nil {
		return false, mapErr(err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// DeleteLines 는 줄 전부를 지운다(전산 수량을 다시 잡을 때, 작성 중·반려 상태만 호출한다).
func (SQLStockCount) DeleteLines(ctx context.Context, q contract.Tx, countID int64) error {
	_, err := q.ExecContext(ctx, `DELETE FROM stock_count_lines WHERE count_id = ?`, countID)
	return err
}

// SetSnapshot 은 전산 수량 스냅샷 시점을 바꾼다.
func (SQLStockCount) SetSnapshot(ctx context.Context, q contract.Tx, id, snapshot int64, now time.Time) error {
	_, err := q.ExecContext(ctx, `UPDATE stock_counts SET snapshot_ledger_id = ?, updated_at = ? WHERE id = ?`, snapshot, fmtTime(now), id)
	return mapErr(err)
}

// SetStatus 는 실사 표 상태를 바꾼다(doc 가 0 이 아니면 조정 전표를 연결하고, applied 면 반영 시각을 남긴다).
func (SQLStockCount) SetStatus(ctx context.Context, q contract.Tx, id int64, status string, docID int64, reason string, now time.Time) error {
	applied := any(nil)
	if status == domain.CountApplied {
		applied = fmtTime(now)
	}
	_, err := q.ExecContext(ctx, `UPDATE stock_counts SET status = ?, doc_id = COALESCE(?, doc_id), reject_reason = ?, applied_at = COALESCE(?, applied_at), updated_at = ? WHERE id = ?`,
		status, nullID(docID), reason, applied, fmtTime(now), id)
	return mapErr(err)
}

// MovedSince 는 실사 줄의 (품목, 로트)에 전산 수량 스냅샷 뒤 생긴 그 창고의 원장 행 수다.
func (SQLStockCount) MovedSince(ctx context.Context, q contract.Tx, countID, whID, snapshot int64) (int, error) {
	var n int
	err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM stock_ledger s WHERE s.id > ? AND s.warehouse_id = ?
		AND EXISTS (SELECT 1 FROM stock_count_lines l WHERE l.count_id = ? AND l.item_id = s.item_id AND l.lot_key = COALESCE(s.lot_id,0))`, snapshot, whID, countID).Scan(&n)
	return n, err
}

// ---- 수불부 ----

var ledgerKindLabel = map[string]string{
	domain.LedgerReceipt: "입고", domain.LedgerIssue: "출고", domain.LedgerTransferOut: "이동 출고", domain.LedgerTransferIn: "이동 입고", domain.LedgerAdjust: "실사 조정",
}

func ledgerWhere(f domain.LedgerFilter) (string, []any) {
	w, a := ` s.item_id = ?`, []any{f.ItemID}
	if f.WarehouseID != 0 {
		w, a = w+` AND s.warehouse_id = ?`, append(a, f.WarehouseID)
	}
	if f.LotID != 0 {
		w, a = w+` AND s.lot_id = ?`, append(a, f.LotID)
	}
	return w, a
}

// Ledger 는 품목의 수불부다: 기초(시작일 이전 합계) · 기간 행(잔량 포함, 쪽 나누기) · 합계 · 기말. 기초·합계·행은 모두 같은 창고 법인 조건으로 읽는다(B51 판정 후속: 행만 걸러 다른 법인 수량이 섞이던 결함). 날짜는 한국 시간(KST) 기준이다.
func (SQLStockCount) Ledger(ctx context.Context, q contract.Tx, f domain.LedgerFilter, from, to string, limit, offset int) (domain.LedgerPage, error) {
	var page domain.LedgerPage
	where, args := ledgerWhere(f)
	day := `COALESCE(s.book_date, date(s.created_at, '+9 hours'))` // 장부일(B27): 출고 전표는 출고일, 나머지와 기존 행은 기록한 날(KST)
	if err := q.QueryRowContext(ctx, `SELECT COALESCE(SUM(s.qty_delta),0) FROM stock_ledger s JOIN warehouses w ON w.id = s.warehouse_id WHERE `+contract.SQLEntity(ctx, "w")+` AND `+where+` AND `+day+` < ?`, append(append([]any{}, args...), from)...).Scan(&page.Opening); err != nil {
		return page, err
	}
	inArgs := append(append([]any{}, args...), from, to)
	if err := q.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(CASE WHEN s.qty_delta > 0 THEN s.qty_delta ELSE 0 END),0), COALESCE(SUM(CASE WHEN s.qty_delta < 0 THEN -s.qty_delta ELSE 0 END),0)
		FROM stock_ledger s JOIN warehouses w ON w.id = s.warehouse_id WHERE `+contract.SQLEntity(ctx, "w")+` AND `+where+` AND `+day+` BETWEEN ? AND ?`, inArgs...).Scan(&page.Total, &page.TotalIn, &page.TotalOut); err != nil {
		return page, err
	}
	page.Closing = page.Opening + page.TotalIn - page.TotalOut
	rowArgs := append([]any{page.Opening}, inArgs...)
	rowArgs = append(rowArgs, limit, offset)
	rows, err := q.QueryContext(ctx, `SELECT d, reason, wh, lot_no, ref_type, ref_id, qty_delta, bal, doc_no, uc, am, hv, cp FROM (
			SELECT `+day+` AS d, s.id AS sid, s.reason, w.name AS wh, COALESCE(t.lot_no,'') AS lot_no, s.ref_type, s.ref_id, s.qty_delta,
				? + SUM(s.qty_delta) OVER (ORDER BY `+day+`, s.id) AS bal,
				COALESCE(v.unit_cost,0) AS uc, COALESCE(v.amount,0) AS am, CASE WHEN v.ledger_id IS NOT NULL THEN 1 ELSE 0 END AS hv,
				CASE WHEN s.reason IN ('transfer_out','transfer_in') THEN COALESCE((SELECT e.name FROM stock_transfers tr JOIN entities e ON e.id = CASE s.reason WHEN 'transfer_out' THEN tr.to_entity_id ELSE tr.from_entity_id END
					WHERE tr.id = s.ref_id AND tr.from_entity_id <> tr.to_entity_id),'') ELSE '' END AS cp,
				CASE s.reason
					WHEN 'receipt' THEN 'RC-' || substr('000000' || CAST(s.ref_id AS TEXT), length(CAST(s.ref_id AS TEXT)) + 1, 6)
					WHEN 'issue' THEN COALESCE((SELECT no FROM issue_slips WHERE id = s.ref_id),'')
					WHEN 'transfer_out' THEN COALESCE((SELECT no FROM stock_transfers WHERE id = s.ref_id),'')
					WHEN 'transfer_in' THEN COALESCE((SELECT no FROM stock_transfers WHERE id = s.ref_id),'')
					ELSE COALESCE((SELECT p.no FROM stock_counts c JOIN purchase_requests p ON p.id = c.doc_id WHERE `+contract.SQLEntity(ctx, "p")+` AND c.id = s.ref_id AND s.ref_type = 'stock_count'),'') END AS doc_no
			FROM stock_ledger s JOIN warehouses w ON w.id = s.warehouse_id LEFT JOIN lots t ON t.id = s.lot_id LEFT JOIN stock_valuation v ON v.ledger_id = s.id
			WHERE `+contract.SQLEntity(ctx, "w")+` AND `+where+` AND `+day+` BETWEEN ? AND ?
		) ORDER BY d, sid LIMIT ? OFFSET ?`, rowArgs...)
	if err != nil {
		return page, err
	}
	defer rows.Close()
	for rows.Next() {
		var r domain.LedgerRow
		var refType string
		var refID, qty int64
		var hv int
		if err := rows.Scan(&r.Date, &r.Kind, &r.Warehouse, &r.LotNo, &refType, &refID, &qty, &r.Balance, &r.DocNo, &r.UnitCost, &r.Amount, &hv, &r.CounterEntity); err != nil {
			return page, err
		}
		r.KindLabel = ledgerKindLabel[r.Kind]
		if r.CounterEntity != "" { // 법인 간 이동(B51): 상대 법인 이름만 보인다(창고 이름은 숨긴다, D8)
			r.Intercompany = true
			r.KindLabel = "법인 간 " + r.KindLabel + "(" + r.CounterEntity + " 법인)"
		}
		r.Valued = hv == 1
		if qty > 0 {
			r.In = qty
		} else {
			r.Out = -qty
		}
		page.Rows = append(page.Rows, r)
	}
	if page.Rows == nil {
		page.Rows = []domain.LedgerRow{}
	}
	return page, rows.Err()
}

var _ = fmt.Sprint
