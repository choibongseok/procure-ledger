package repository

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
)

// 기초 재고·미지급 잔액(B35)이 쓰는 조회·기록이다. 재고는 원장 합이라는 불변식을 지키려고 수량을 쓰는 새 길은 없다:
// 기초 입고는 AppendLedger(출처 opening_import)로만 들어가고, 여기서는 읽기와 기초 미지급 줄의 문서 번호·날짜 기록만 한다.

// OpeningRef 는 원장 행의 출처 값이다.
const OpeningRef = "opening_import"

// OpeningStock 은 (품목, 창고, 로트)에 이미 기초로 들어간 수량과 단가다. 없으면 found=false.
func (SQLImport) OpeningStock(ctx context.Context, q contract.Tx, itemID, warehouseID, lotID int64) (qty, unitCost int64, found bool, err error) {
	var n int64
	err = q.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(l.qty_delta),0), COALESCE(MIN(v.in_unit_cost),0) FROM stock_ledger l
		LEFT JOIN stock_valuation v ON v.ledger_id = l.id
		WHERE l.warehouse_id IN (SELECT id FROM warehouses WHERE `+contract.SQLEntity(ctx, "")+`) AND l.ref_type = ? AND l.item_id = ? AND l.warehouse_id = ? AND COALESCE(l.lot_id,0) = ?`, OpeningRef, itemID, warehouseID, lotID).Scan(&n, &qty, &unitCost)
	return qty, unitCost, n > 0, err
}

// LotID 는 (품목, 로트 번호)의 로트 번호다. 없으면 0.
func (SQLImport) LotID(ctx context.Context, q contract.Tx, itemID int64, lotNo string) (int64, error) {
	var id int64
	err := q.QueryRowContext(ctx, `SELECT id FROM lots WHERE item_id = ? AND lot_no = ?`, itemID, lotNo).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return id, err
}

// OpeningValuationSum 은 한 묶음이 만든 기초 입고 행의 평가 금액 합이다(전표 차변과 맞춰 보는 검산 값).
func (SQLImport) OpeningValuationSum(ctx context.Context, q contract.Tx, refID int64) (int64, error) {
	var n int64
	err := q.QueryRowContext(ctx, `SELECT COALESCE(SUM(v.amount),0) FROM stock_ledger l JOIN stock_valuation v ON v.ledger_id = l.id WHERE `+contract.SQLEntity(ctx, "v")+` AND l.ref_type = ? AND l.ref_id = ?`, OpeningRef, refID).Scan(&n)
	return n, err
}

// OpeningPayable 은 (거래처, 문서 번호)로 이미 기초에 들어간(취소되지 않은 전표의) 잔액이다. 없으면 found=false.
func (SQLImport) OpeningPayable(ctx context.Context, q contract.Tx, vendorID int64, docNo string) (amount int64, found bool, err error) {
	var n int64
	err = q.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(l.amount),0) FROM opening_payable_lines o
		JOIN journal_lines l ON l.id = o.line_id JOIN journals j ON j.id = l.journal_id
		WHERE `+contract.SQLEntity(ctx, "j")+` AND o.vendor_id = ? AND o.doc_no = ? AND j.status <> 'canceled'`, vendorID, docNo).Scan(&n, &amount)
	return amount, n > 0, err
}

// OpeningPayableLine 은 기초 미지급 줄의 원 시스템 문서 번호·발생일·만기일 기록이다.
type OpeningPayableLine struct {
	LineID, VendorID          int64
	DocNo, TradeDate, DueDate string
	BatchNo                   string
}

// InsertOpeningPayableLine 은 기록을 추가한다(추가 전용).
func (SQLImport) InsertOpeningPayableLine(ctx context.Context, q contract.Tx, l OpeningPayableLine, now time.Time) error {
	_, err := q.ExecContext(ctx, `INSERT INTO opening_payable_lines (line_id, vendor_id, doc_no, trade_date, due_date, batch_no, created_at) VALUES (?,?,?,?,?,?,?)`,
		l.LineID, l.VendorID, l.DocNo, l.TradeDate, l.DueDate, l.BatchNo, fmtTime(now))
	return mapErr(err)
}

// OpeningTerms 는 줄에 기록된 발생일·만기일이다. 기록이 없으면 found=false(일반 전표는 거래일 + 결제 조건).
func (SQLImport) OpeningTerms(ctx context.Context, q contract.Tx, lineID int64) (trade, due string, found bool, err error) {
	err = q.QueryRowContext(ctx, `SELECT trade_date, due_date FROM opening_payable_lines WHERE line_id = ?`, lineID).Scan(&trade, &due)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", false, nil
	}
	return trade, due, err == nil, err
}

// JournalLineIDs 는 전표 줄(순서대로)의 번호다. 방금 만든 전표의 줄에 기록을 붙일 때 쓴다.
func (SQLImport) JournalLineIDs(ctx context.Context, q contract.Tx, journalID int64) ([]int64, error) {
	rows, err := q.QueryContext(ctx, `SELECT id FROM journal_lines WHERE journal_id = ? ORDER BY seq`, journalID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// PendingOpeningJournals 는 전기일이 그 달(YYYY-MM)인 승인 전(임시·승인 요청) 기초 전표 건수다(마감 전 점검 경고).
func (SQLImport) PendingOpeningJournals(ctx context.Context, q contract.Tx, month string) (int, error) {
	var n int
	err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM journals WHERE `+contract.SQLEntity(ctx, "journals")+` AND kind = 'opening' AND status IN ('draft','requested') AND substr(posting_date,1,7) = ?`, month).Scan(&n)
	return n, err
}

// PendingOpeningJournalsAll 은 달과 관계없이 승인 전 기초 전표 건수다(대조 보고서 안내).
func (SQLImport) PendingOpeningJournalsAll(ctx context.Context, q contract.Tx) (int, error) {
	var n int
	err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM journals WHERE `+contract.SQLEntity(ctx, "journals")+` AND kind = 'opening' AND status IN ('draft','requested')`).Scan(&n)
	return n, err
}
