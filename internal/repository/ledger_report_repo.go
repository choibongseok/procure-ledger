package repository

import (
	"context"
	"database/sql"
	"errors"

	"github.com/choibongseok/procure-ledger/internal/changes"
	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
)

// 계정별 원장·시산표·매입 채무 조회(B31 3.5·3.6). 읽기 전용이고 승인된 전표만 센다(임시·승인 요청·취소는 제외).
// 금액은 정수 원이다. 부호는 서비스가 계정의 정상 방향으로 맞춘다(여기서는 차변 − 대변을 돌려준다).

// LedgerOpening 은 기간 시작 전(전기일 < from)의 차변 − 대변 누계다.
func (SQLLedger) LedgerOpening(ctx context.Context, q contract.Tx, accountID int64, from string, vendorID int64) (int64, error) {
	query := `SELECT COALESCE(SUM(CASE l.side WHEN 'D' THEN l.amount ELSE -l.amount END),0) FROM journal_lines l JOIN journals j ON j.id = l.journal_id
		WHERE ` + contract.SQLEntity(ctx, "j") + ` AND l.account_id = ? AND j.status = 'approved' AND j.posting_date < ?`
	args := []any{accountID, from}
	if vendorID != 0 {
		query += ` AND l.vendor_id = ?`
		args = append(args, vendorID)
	}
	var n int64
	err := q.QueryRowContext(ctx, query, args...).Scan(&n)
	return n, err
}

// LedgerLine 은 원장 한 줄의 원자료다.
type LedgerLine struct {
	PostingDate, No, Memo, Counter string
	Side                           string
	Amount                         int64
}

// LedgerLines 는 기간 안의 승인된 전표 줄이다(전기일·전표·줄 순서).
func (SQLLedger) LedgerLines(ctx context.Context, q contract.Tx, accountID int64, from, to string, vendorID int64) ([]LedgerLine, error) {
	query := `SELECT j.posting_date, j.no, j.memo, l.side, l.amount,
		COALESCE((SELECT string_agg(a2.code || ' ' || a2.name, ', ') FROM journal_lines l2 JOIN accounts a2 ON a2.id = l2.account_id WHERE l2.journal_id = j.id AND l2.side <> l.side),'')
		FROM journal_lines l JOIN journals j ON j.id = l.journal_id
		WHERE ` + contract.SQLEntity(ctx, "j") + ` AND l.account_id = ? AND j.status = 'approved' AND j.posting_date >= ? AND j.posting_date <= ?`
	args := []any{accountID, from, to}
	if vendorID != 0 {
		query += ` AND l.vendor_id = ?`
		args = append(args, vendorID)
	}
	rows, err := q.QueryContext(ctx, query+` ORDER BY j.posting_date, j.id, l.seq`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LedgerLine
	for rows.Next() {
		var l LedgerLine
		if err := rows.Scan(&l.PostingDate, &l.No, &l.Memo, &l.Side, &l.Amount, &l.Counter); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// PendingJournalCount 는 기간 안에 그 계정(0 이면 모든 계정)을 쓴 승인 전 전표(임시·승인 요청) 수다("승인 대기 N건 제외" 안내용).
func (SQLLedger) PendingJournalCount(ctx context.Context, q contract.Tx, accountID int64, from, to string) (int, error) {
	query := `SELECT COUNT(DISTINCT j.id) FROM journals j JOIN journal_lines l ON l.journal_id = j.id WHERE ` + contract.SQLEntity(ctx, "j") + ` AND j.status IN ('draft','requested') AND j.posting_date >= ? AND j.posting_date <= ?`
	args := []any{from, to}
	if accountID != 0 {
		query += ` AND l.account_id = ?`
		args = append(args, accountID)
	}
	var n int
	err := q.QueryRowContext(ctx, query, args...).Scan(&n)
	return n, err
}

// TrialRaw 는 시산표 한 계정의 원자료다(Opening 은 차변 − 대변).
type TrialRaw struct {
	Code, Name, Kind string
	Opening          int64
	Debit, Credit    int64
}

// TrialRaws 는 모든 계정의 기초(차변 − 대변)·기간 차변·기간 대변이다(승인된 전표만).
func (SQLLedger) TrialRaws(ctx context.Context, q contract.Tx, from, to string) ([]TrialRaw, error) {
	rows, err := q.QueryContext(ctx, `SELECT a.code, a.name, a.kind,
		COALESCE(SUM(CASE WHEN j.posting_date < ? THEN CASE l.side WHEN 'D' THEN l.amount ELSE -l.amount END END),0),
		COALESCE(SUM(CASE WHEN j.posting_date >= ? AND j.posting_date <= ? AND l.side = 'D' THEN l.amount END),0),
		COALESCE(SUM(CASE WHEN j.posting_date >= ? AND j.posting_date <= ? AND l.side = 'C' THEN l.amount END),0)
		FROM accounts a LEFT JOIN journal_lines l ON l.account_id = a.id LEFT JOIN journals j ON j.id = l.journal_id AND j.status = 'approved' AND `+contract.SQLEntity(ctx, "j")+`
		 GROUP BY a.id ORDER BY a.code`, from, from, to, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TrialRaw
	for rows.Next() {
		var t TrialRaw
		if err := rows.Scan(&t.Code, &t.Name, &t.Kind, &t.Opening, &t.Debit, &t.Credit); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// PayableVendorAgg 는 거래처별 미지급 채무 집계다.
type PayableVendorAgg struct {
	VendorID      int64
	VendorName    string
	Count         int
	Outstanding   int64
	Reserved      int64
	EarliestDue   string
	OverdueAmount int64
	LinkedEntity  int64 // 거래처가 그룹의 다른 법인이면 그 법인 번호(0 = 아님, B44)
}

// PayableByVendor 는 미지급 잔액이 있는 거래처별 채무다(취소된 항목 제외). asOf 는 만기 경과 판정 기준일이다.
func (SQLLedger) PayableByVendor(ctx context.Context, q contract.Tx, asOf string) ([]PayableVendorAgg, error) {
	rows, err := q.QueryContext(ctx, `SELECT p.vendor_id, v.name, COUNT(*), SUM(p.amount - p.paid_amount), SUM(p.reserved_amount), MIN(p.due_date),
		SUM(CASE WHEN p.due_date < ? THEN p.amount - p.paid_amount ELSE 0 END), v.linked_entity_id
		FROM payables p JOIN vendors v ON v.id = p.vendor_id WHERE `+contract.SQLEntity(ctx, "p")+` AND p.canceled = 0 AND p.amount - p.paid_amount > 0
		GROUP BY p.vendor_id, v.name, v.linked_entity_id ORDER BY MIN(p.due_date), v.name`, asOf)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PayableVendorAgg
	for rows.Next() {
		var a PayableVendorAgg
		if err := rows.Scan(&a.VendorID, &a.VendorName, &a.Count, &a.Outstanding, &a.Reserved, &a.EarliestDue, &a.OverdueAmount, &a.LinkedEntity); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// EntityNames 는 법인 번호별 이름이다(법인 간 거래 배지용). 법인은 몇 개뿐이라 한 번에 읽는다.
func (SQLLedger) EntityNames(ctx context.Context, q contract.Tx) (map[int64]string, error) {
	rows, err := q.QueryContext(ctx, `SELECT id, name FROM entities`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]string{}
	for rows.Next() {
		var id int64
		var n string
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		out[id] = n
	}
	return out, rows.Err()
}

// PayableItems 는 거래처의 미지급 채무 항목이다(만기 순).
func (SQLLedger) PayableItems(ctx context.Context, q contract.Tx, vendorID int64) ([]domain.PayableItem, error) {
	rows, err := q.QueryContext(ctx, `SELECT p.id, j.no, p.trade_date, p.due_date, p.amount, p.paid_amount, p.reserved_amount
		FROM payables p JOIN journals j ON j.id = p.journal_id WHERE `+contract.SQLEntity(ctx, "p")+` AND p.vendor_id = ? AND p.canceled = 0 AND p.amount - p.paid_amount > 0 ORDER BY p.due_date, p.id`, vendorID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.PayableItem
	for rows.Next() {
		var it domain.PayableItem
		if err := rows.Scan(&it.ID, &it.JournalNo, &it.TradeDate, &it.DueDate, &it.Amount, &it.Paid, &it.Reserved); err != nil {
			return nil, err
		}
		it.Outstanding = it.Amount - it.Paid
		out = append(out, it)
	}
	return out, rows.Err()
}

// AccountBalanceAsOf 는 계정의 기준일(포함)까지 승인된 전표의 차변 − 대변이다.
func (SQLLedger) AccountBalanceAsOf(ctx context.Context, q contract.Tx, code, asOf string) (int64, error) {
	var n int64
	err := q.QueryRowContext(ctx, `SELECT COALESCE(SUM(CASE l.side WHEN 'D' THEN l.amount ELSE -l.amount END),0) FROM journal_lines l JOIN journals j ON j.id = l.journal_id
		JOIN accounts a ON a.id = l.account_id WHERE `+contract.SQLEntity(ctx, "j")+` AND a.code = ? AND j.status = 'approved' AND j.posting_date <= ?`, code, asOf).Scan(&n)
	return n, err
}

// SetVendorPaymentDays 는 거래처 결제 조건 일수를 바꾼다(이전 값을 돌려준다, 비어 있었으면 설정 기본값).
func (r SQLLedger) SetVendorPaymentDays(ctx context.Context, q contract.Tx, vendorID int64, days int) (old int, err error) {
	var cur sql.NullInt64
	if err := q.QueryRowContext(ctx, `SELECT payment_days FROM vendors WHERE id = ?`, vendorID).Scan(&cur); errors.Is(err, sql.ErrNoRows) {
		return 0, contract.ErrNotFound
	} else if err != nil {
		return 0, err
	}
	old, err = r.VendorPaymentDays(ctx, q, vendorID)
	if err != nil {
		return 0, err
	}
	if _, err := q.ExecContext(ctx, `UPDATE vendors SET payment_days = ? WHERE id = ?`, days, vendorID); err != nil {
		return 0, mapErr(err)
	}
	if err := changes.Note(ctx, q, changes.ResVendor, vendorID, changes.OpUpdated, ""); err != nil {
		return 0, err
	}
	return old, nil
}
