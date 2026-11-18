package repository

import (
	"context"
	"database/sql"

	"github.com/choibongseok/procure-ledger/internal/contract"
)

// AbsenceSpan 은 사람의 휴직 구간이다(B50 8절). To 는 마지막 날의 다음 날(배타)이고 비어 있으면 아직 끝나지 않았다.
// Kind 는 휴직 구분(질병·육아 등)이다. 민감 정보라 서비스 안의 출근율 판정(출근 간주 표)에만 쓰고 문구·화면·MCP 로 내보내지 않는다.
type AbsenceSpan struct {
	From, To string
	Kind     string
}

// Covers 는 날짜가 구간 안인가다(To 는 배타, 비어 있으면 열려 있다).
func (s AbsenceSpan) Covers(day string) bool { return s.From <= day && (s.To == "" || day < s.To) }

// AbsenceSpans 는 [from, to] 날짜와 겹치는 사람의 휴직 구간이다(조직 서비스가 쓴 구간 표를 읽는다). 휴직 중에는 휴가를 신청할 수 없다.
func (SQLLeave) AbsenceSpans(ctx context.Context, q contract.Tx, userID int64, from, to string) ([]AbsenceSpan, error) {
	rows, err := q.QueryContext(ctx, `SELECT valid_from, COALESCE(valid_to,''), absence_kind FROM org_person_spans
		WHERE user_id = ? AND absence_kind <> '' AND left_flag = 0 AND valid_from <= ? AND (valid_to IS NULL OR valid_to > ?) ORDER BY valid_from`, userID, to, from)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AbsenceSpan
	for rows.Next() {
		var s AbsenceSpan
		if err := rows.Scan(&s.From, &s.To, &s.Kind); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// ApprovedRequestsFrom 은 한 사람의 승인된 휴가 신청 중 잔여 묶음을 쓰고 end 가 from 이후인 건이다(휴직 발령의 연차 복원이 읽는다).
func (SQLLeave) ApprovedRequestsFrom(ctx context.Context, q contract.Tx, userID int64, from string) ([]LeaveReqRow, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+leaveReqCols+leaveReqFrom+` WHERE `+contract.SQLEntity(ctx, "r")+` AND r.user_id = ? AND r.status = 'approved' AND r.balance <> '' AND r.end_date >= ? ORDER BY r.start_date, r.id`, userID, from)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LeaveReqRow
	for rows.Next() {
		r, err := scanLeaveReq(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// AbsenceRestoredMinutes 는 한 휴가 신청에서 휴직 발령으로 이미 복원한 분량의 합이다(원장 줄 ref_type=leave_absence 의 합, 되돌린 분은 빠진다).
func (SQLLeave) AbsenceRestoredMinutes(ctx context.Context, q contract.Tx, requestID int64) (int64, error) {
	var n sql.NullInt64
	err := q.QueryRowContext(ctx, `SELECT SUM(minutes) FROM leave_ledger WHERE `+contract.SQLEntity(ctx, "leave_ledger")+` AND ref_type = 'leave_absence' AND ref_id = ?`, requestID).Scan(&n)
	return n.Int64, err
}

// AbsenceSpansBetween 은 [from, to] 와 겹치는 모든 사람의 휴직 구간이다(팀 부재 표시용, 사람 번호별). 구분은 싣지 않고 날짜만 쓴다.
func (SQLLeave) AbsenceSpansBetween(ctx context.Context, q contract.Tx, from, to string) (map[int64][]AbsenceSpan, error) {
	rows, err := q.QueryContext(ctx, `SELECT user_id, valid_from, COALESCE(valid_to,'') FROM org_person_spans
		WHERE absence_kind <> '' AND left_flag = 0 AND valid_from <= ? AND (valid_to IS NULL OR valid_to > ?) ORDER BY user_id, valid_from`, to, from)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64][]AbsenceSpan{}
	for rows.Next() {
		var u int64
		var s AbsenceSpan
		if err := rows.Scan(&u, &s.From, &s.To); err != nil {
			return nil, err
		}
		out[u] = append(out[u], s)
	}
	return out, rows.Err()
}
