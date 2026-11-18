package repository

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
)

// 부재(B29 ①) 저장소 조각: 위임 표(approval_delegations)의 kind='absence' 행에 대한 조회·알림 표시.

func kindOrDefault(k string) string {
	if k == "absence" {
		return "absence"
	}
	return "delegation"
}

// OverlappingAbsence 는 위임자의 해제되지 않은 부재 중 [from, to) 와 기간이 겹치는 첫 건이다(없으면 false).
func (SQLDelegations) OverlappingAbsence(ctx context.Context, q contract.Tx, delegatorID int64, from, to time.Time) (DelegationRec, bool, error) {
	r, err := scanDelegation(q.QueryRowContext(ctx, delegationSelect+` WHERE d.delegator_id = ? AND d.kind = 'absence' AND d.revoked_at IS NULL AND d.start_at < ? AND d.end_at > ? ORDER BY d.id LIMIT 1`,
		delegatorID, fmtTime(to), fmtTime(from)))
	if errors.Is(err, sql.ErrNoRows) {
		return r, false, nil
	}
	return r, err == nil, err
}

// NoticeStarted 는 예정 부재(등록 뒤에 시작하는 것) 중 시작 시각이 지났고 시작 알림을 아직 만들지 않은 건이다.
func (SQLDelegations) NoticeStarted(ctx context.Context, q contract.Tx, now time.Time) ([]DelegationRec, error) {
	return queryDelegations(ctx, q, delegationSelect+` WHERE d.kind = 'absence' AND d.revoked_at IS NULL AND d.start_at <= ? AND d.start_at > d.created_at AND d.start_notified_at IS NULL ORDER BY d.id`, fmtTime(now))
}

// NoticeEnded 는 종료 시각이 지났고 종료 알림을 아직 만들지 않은 해제되지 않은 부재다.
func (SQLDelegations) NoticeEnded(ctx context.Context, q contract.Tx, now time.Time) ([]DelegationRec, error) {
	return queryDelegations(ctx, q, delegationSelect+` WHERE d.kind = 'absence' AND d.revoked_at IS NULL AND d.end_at <= ? AND d.end_notified_at IS NULL ORDER BY d.id`, fmtTime(now))
}

// MarkNotified 는 시작("start") 또는 종료("end") 알림을 만들었다고 표시한다. 이미 표시됐으면 false(다른 프로세스가 먼저 만들었다).
func (SQLDelegations) MarkNotified(ctx context.Context, q contract.Tx, id int64, which string, at time.Time) (bool, error) {
	col := "start_notified_at"
	if which == "end" {
		col = "end_notified_at"
	}
	res, err := q.ExecContext(ctx, `UPDATE approval_delegations SET `+col+` = ? WHERE id = ? AND `+col+` IS NULL`, fmtTime(at), id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

func queryDelegations(ctx context.Context, q contract.Tx, query string, args ...any) ([]DelegationRec, error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DelegationRec
	for rows.Next() {
		r, err := scanDelegation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
