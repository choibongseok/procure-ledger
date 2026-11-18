package repository

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
)

// 전표 대기(B31 3.3.8)와 자동 전표 입력 사실 조회. 전표 행 자체의 쓰기는 ledger_repo.go 가 한다.

// QueueRow 는 대기 행의 저장 형태다.
type QueueRow struct {
	ID                           int64
	SourceType                   string
	SourceID                     int64
	Reason, ReasonText           string
	POID, ReceiptID, VendorID    int64 // 0 이면 NULL
	Amount, DocTotal             *int64
	Status                       string
	JournalID, HandledByID       int64
	HandledBy                    string
	HandledAt                    *time.Time
	Note                         string
	CreatedAt                    time.Time
	PONo, VendorName, SourceName string
}

func nullPtr(p *int64) any {
	if p == nil {
		return nil
	}
	return *p
}

// UpsertQueue 는 대기 행을 쓴다. 같은 출처에 처리 전 행이 있으면 사유·후보를 새 값으로 바꾼다(다시 만들기 실패). 처리된 행은 건드리지 않는다(트리거도 막는다).
func (SQLLedger) UpsertQueue(ctx context.Context, q contract.Tx, r QueueRow, now time.Time) error {
	_, err := q.ExecContext(ctx, `INSERT INTO journal_queue (source_type, source_id, reason, reason_text, po_id, receipt_id, vendor_id, amount, doc_total, status, created_at)
		VALUES (?,?,?,?,?,?,?,?,?,'pending',?)
		ON CONFLICT(source_type, source_id) DO UPDATE SET reason = excluded.reason, reason_text = excluded.reason_text, po_id = excluded.po_id,
			receipt_id = excluded.receipt_id, vendor_id = excluded.vendor_id, amount = excluded.amount, doc_total = excluded.doc_total
		WHERE journal_queue.status = 'pending'`,
		r.SourceType, r.SourceID, r.Reason, r.ReasonText, nullID(r.POID), nullID(r.ReceiptID), nullID(r.VendorID), nullPtr(r.Amount), nullPtr(r.DocTotal), fmtTime(now))
	return mapErr(err)
}

const queueSelect = `SELECT g.id, g.source_type, g.source_id, g.reason, g.reason_text, COALESCE(g.po_id,0), COALESCE(g.receipt_id,0), COALESCE(g.vendor_id,0), g.amount, g.doc_total,
	g.status, COALESCE(g.journal_id,0), COALESCE(g.handled_by,0), COALESCE(hu.name,''), g.handled_at, g.note, g.created_at, COALESCE(po.no,''), COALESCE(v.name,''),
	CASE g.source_type WHEN 'card_txn' THEN '카드 소명 ' || g.source_id WHEN 'approval_doc' THEN COALESCE((SELECT p.no FROM purchase_requests p WHERE p.id = g.source_id),'') ELSE '증빙 판정 ' || g.source_id END
	FROM (SELECT * FROM journal_queue WHERE (CASE source_type WHEN 'card_txn' THEN (SELECT t.entity_id FROM card_explanations e JOIN card_transactions t ON t.id = e.card_txn_id WHERE e.id = journal_queue.source_id) WHEN 'approval_doc' THEN (SELECT entity_id FROM purchase_requests WHERE id = journal_queue.source_id) ELSE COALESCE((SELECT entity_id FROM purchase_orders WHERE id = journal_queue.po_id), 1) END) {entity_in}) g LEFT JOIN users hu ON hu.id = g.handled_by LEFT JOIN purchase_orders po ON po.id = g.po_id LEFT JOIN vendors v ON v.id = g.vendor_id`

func scanQueue(s rowScanner) (QueueRow, error) {
	var r QueueRow
	var amount, docTotal sql.NullInt64
	var handledAt sql.NullString
	var created string
	if err := s.Scan(&r.ID, &r.SourceType, &r.SourceID, &r.Reason, &r.ReasonText, &r.POID, &r.ReceiptID, &r.VendorID, &amount, &docTotal,
		&r.Status, &r.JournalID, &r.HandledByID, &r.HandledBy, &handledAt, &r.Note, &created, &r.PONo, &r.VendorName, &r.SourceName); err != nil {
		return r, err
	}
	if amount.Valid {
		v := amount.Int64
		r.Amount = &v
	}
	if docTotal.Valid {
		v := docTotal.Int64
		r.DocTotal = &v
	}
	r.CreatedAt = parseTime(created)
	if handledAt.Valid {
		t := parseTime(handledAt.String)
		r.HandledAt = &t
	}
	return r, nil
}

// QueueBySource 는 같은 출처의 대기 행이다. 없으면 ErrNotFound.
func (SQLLedger) QueueBySource(ctx context.Context, q contract.Tx, sourceType string, sourceID int64) (QueueRow, error) {
	r, err := scanQueue(q.QueryRowContext(ctx, contract.EntitySQL(ctx, queueSelect)+` WHERE g.source_type = ? AND g.source_id = ?`, sourceType, sourceID))
	if errors.Is(err, sql.ErrNoRows) {
		return r, contract.ErrNotFound
	}
	return r, err
}

// QueueByID 는 대기 행 한 건이다. 없으면 ErrNotFound.
func (SQLLedger) QueueByID(ctx context.Context, q contract.Tx, id int64) (QueueRow, error) {
	r, err := scanQueue(q.QueryRowContext(ctx, contract.EntitySQL(ctx, queueSelect)+` WHERE g.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return r, contract.ErrNotFound
	}
	return r, err
}

// ListQueue 는 대기 행 목록이다(status 가 비면 전부, 처리 전이 먼저).
func (SQLLedger) ListQueue(ctx context.Context, q contract.Tx, status string) ([]QueueRow, error) {
	query := contract.EntitySQL(ctx, queueSelect)
	var args []any
	if status != "" {
		query += ` WHERE g.status = ?`
		args = append(args, status)
	}
	rows, err := q.QueryContext(ctx, query+` ORDER BY CASE g.status WHEN 'pending' THEN 0 ELSE 1 END, g.id DESC LIMIT 500`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []QueueRow
	for rows.Next() {
		r, err := scanQueue(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// HandleQueue 는 처리 전 행을 처리 상태로 바꾼다. 이미 처리된 행이면 ErrInvalidState.
func (SQLLedger) HandleQueue(ctx context.Context, q contract.Tx, id int64, status string, journalID, by int64, note string, now time.Time) error {
	res, err := q.ExecContext(ctx, `UPDATE journal_queue SET status = ?, journal_id = ?, handled_by = ?, handled_at = ?, note = ? WHERE id = ? AND status = 'pending'`,
		status, nullID(journalID), by, fmtTime(now), note, id)
	if err != nil {
		return mapErr(err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return contract.ErrInvalidState
	}
	return nil
}

// ReceiptFacts 는 입고 한 건의 금액·거래처·발주·입고일이다(공급가액 = 입고 줄 수량 × 그 발주 줄 단가의 합).
type ReceiptFacts struct {
	Supply     int64
	VendorID   int64
	POID       int64
	PONo       string
	ReceivedAt time.Time
	Lines      int
	SiteID     int64 // 입고한 창고의 사업장(여러 사업장에 걸치면 0 = 주사업장). 부가세 사업장 집계의 기준이다(B44)
}

// ReceiptFacts 는 입고의 공급가액과 발주 거래처를 읽는다. 입고가 없으면 ErrNotFound.
func (SQLLedger) ReceiptFacts(ctx context.Context, q contract.Tx, receiptID int64) (ReceiptFacts, error) {
	var f ReceiptFacts
	var received string
	err := q.QueryRowContext(ctx, `SELECT po.id, po.no, COALESCE(po.vendor_id,0), r.received_at,
		COALESCE((SELECT SUM(rl.quantity * pl.unit_price) FROM receipt_lines rl JOIN po_lines pl ON pl.id = rl.po_line_id WHERE rl.receipt_id = r.id),0),
		(SELECT COUNT(*) FROM receipt_lines rl WHERE rl.receipt_id = r.id)
		FROM receipts r JOIN purchase_orders po ON po.id = r.po_id WHERE `+contract.SQLEntity(ctx, "r")+` AND r.id = ?`, receiptID).Scan(&f.POID, &f.PONo, &f.VendorID, &received, &f.Supply, &f.Lines)
	if errors.Is(err, sql.ErrNoRows) {
		return f, contract.ErrNotFound
	}
	f.ReceivedAt = parseTime(received)
	if err != nil {
		return f, err
	}
	// 입고가 쌓인 창고의 사업장. 한 사업장이면 그 사업장, 아니면 주사업장.
	var sites, minSite, maxSite int64
	if err := q.QueryRowContext(ctx, `SELECT COUNT(DISTINCT w.site_id), COALESCE(MIN(w.site_id),0), COALESCE(MAX(w.site_id),0)
		FROM stock_ledger s JOIN warehouses w ON w.id = s.warehouse_id WHERE `+contract.SQLEntity(ctx, "w")+` AND s.reason = 'receipt' AND s.ref_type = 'receipt' AND s.ref_id = ?`, receiptID).Scan(&sites, &minSite, &maxSite); err != nil {
		return f, err
	}
	if sites == 1 {
		f.SiteID = minSite
	}
	return f, nil
}

// PurchaseJournalForReceipt 는 그 입고에 연결된 취소되지 않은 자동 매입 전표다(한 입고 한 전표, D8).
func (SQLLedger) PurchaseJournalForReceipt(ctx context.Context, q contract.Tx, receiptID int64) (id int64, no string, found bool, err error) {
	// 증빙 판정으로 연결된 전표, 또는 전자 세금계산서 확정이 만든 전표(출처 tax_invoice, 출처 번호 = 입고 번호, B43 D7)다.
	err = q.QueryRowContext(ctx, `SELECT j.id, j.no FROM journals j WHERE `+contract.SQLEntity(ctx, "j")+` AND j.kind = 'auto_purchase' AND j.status <> 'canceled' AND (
		j.id IN (SELECT e.journal_id FROM journal_evidence e JOIN document_verdicts v ON v.id = e.ref_id WHERE e.kind = 'verdict' AND v.receipt_id = ?)
		OR (j.source_type = 'tax_invoice' AND j.source_id = ?)) ORDER BY j.id LIMIT 1`, receiptID, receiptID).Scan(&id, &no)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, "", false, nil
	}
	return id, no, err == nil, err
}

// JournalBySource 는 같은 출처의 취소되지 않은 자동 전표다(유일 색인과 같은 조건).
func (SQLLedger) JournalBySource(ctx context.Context, q contract.Tx, sourceType string, sourceID int64) (id int64, no string, found bool, err error) {
	err = q.QueryRowContext(ctx, `SELECT id, no FROM journals WHERE `+contract.SQLEntity(ctx, "journals")+` AND source_type = ? AND source_id = ? AND status <> 'canceled' AND kind LIKE 'auto_%' ORDER BY id LIMIT 1`, sourceType, sourceID).Scan(&id, &no)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, "", false, nil
	}
	return id, no, err == nil, err
}

// VerdictState 는 재시도 때 판정이 지금도 일치로 확정된 상태인지다.
func (SQLLedger) VerdictConfirmed(ctx context.Context, q contract.Tx, verdictID int64) (domain.Outcome, error) {
	var out sql.NullString
	err := q.QueryRowContext(ctx, `SELECT confirmed_outcome FROM document_verdicts WHERE id = ?`, verdictID).Scan(&out)
	if errors.Is(err, sql.ErrNoRows) {
		return "", contract.ErrNotFound
	}
	return domain.Outcome(out.String), err
}
