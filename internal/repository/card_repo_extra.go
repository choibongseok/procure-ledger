package repository

import (
	"context"
	"database/sql"
	"errors"

	"github.com/choibongseok/procure-ledger/internal/contract"
)

// StatusSums 는 카드 내역 상태별 건수와 금액 합(절댓값 기준 아님: 취소는 음수)이다. 재무 현황용.
func (SQLCards) StatusSums(ctx context.Context, q contract.Tx) (map[string]int, map[string]int64, error) {
	rows, err := q.QueryContext(ctx, `SELECT status, COUNT(*), COALESCE(SUM(amount),0) FROM card_transactions  WHERE `+contract.SQLEntity(ctx, "card_transactions")+` GROUP BY status`)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	counts, sums := map[string]int{}, map[string]int64{}
	for rows.Next() {
		var s string
		var n int
		var a int64
		if err := rows.Scan(&s, &n, &a); err != nil {
			return nil, nil, err
		}
		counts[s], sums[s] = n, a
	}
	return counts, sums, rows.Err()
}

// ExplanationByID 는 소명 한 건의 원자료와 그 내역 번호다.
func (SQLCards) ExplanationByID(ctx context.Context, q contract.Tx, id int64) (ExplanationRow, int64, error) {
	var e ExplanationRow
	var txn int64
	var acc sql.NullInt64
	var ded, per int
	err := q.QueryRowContext(ctx, `SELECT id, COALESCE(account_id,0), submitted_by, purpose, vat_deduct, personal, supply, vat, status, card_txn_id FROM card_explanations WHERE id = ?`, id).
		Scan(&e.ID, &acc, &e.SubmittedBy, &e.Purpose, &ded, &per, &e.Supply, &e.VAT, &e.Status, &txn)
	if errors.Is(err, sql.ErrNoRows) {
		return e, 0, contract.ErrNotFound
	}
	e.AccountID, e.VATDeduct, e.Personal = acc.Int64, ded == 1, per == 1
	return e, txn, err
}
