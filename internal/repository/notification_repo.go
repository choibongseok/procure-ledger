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

// NotificationInsert 는 알림 한 건의 입력이다. Kind 는 열린 문자열이다(B23 문서함이 같은 표를 쓴다).
type NotificationInsert struct {
	UserID  int64
	Kind    string
	Title   string
	Body    string
	Link    string // 앱 안 경로
	RefType string
	RefID   int64
	Now     time.Time
}

// SQLNotifications 는 앱 안 알림 저장소다(B21 3.9). 읽음 처리(read_at) 외에는 바뀌지 않고 삭제 메서드는 없다.
type SQLNotifications struct{}

// Insert 는 알림을 만든다. 호출자는 업무 쓰기와 같은 트랜잭션에서 부른다.
func (SQLNotifications) Insert(ctx context.Context, q contract.Tx, in NotificationInsert) (int64, error) {
	id, err := dbutil.InsertID(ctx, q, `INSERT INTO notifications (user_id, kind, title, body, link, ref_type, ref_id, created_at) VALUES (?,?,?,?,?,?,?,?)`,
		in.UserID, in.Kind, in.Title, in.Body, in.Link, in.RefType, in.RefID, fmtTime(in.Now))
	if err != nil {
		return 0, mapErr(err)
	}
	return id, nil
}

// List 는 userID 의 알림이다(최신순). beforeID 가 0 이 아니면 그보다 오래된 것부터.
func (SQLNotifications) List(ctx context.Context, q contract.Tx, userID int64, unreadOnly bool, limit int, beforeID int64) ([]domain.Notification, error) {
	query := `SELECT id, kind, title, body, link, created_at, read_at FROM notifications WHERE user_id = ?`
	args := []any{userID}
	if unreadOnly {
		query += ` AND read_at IS NULL`
	}
	if beforeID != 0 {
		query += ` AND id < ?`
		args = append(args, beforeID)
	}
	query += fmt.Sprintf(` ORDER BY id DESC LIMIT %d`, limit)
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.Notification{}
	for rows.Next() {
		var n domain.Notification
		var created string
		var read sql.NullString
		if err := rows.Scan(&n.ID, &n.Kind, &n.Title, &n.Body, &n.Link, &created, &read); err != nil {
			return nil, err
		}
		n.CreatedAt = parseTime(created)
		if read.Valid {
			t := parseTime(read.String)
			n.ReadAt = &t
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// UnreadCount 는 안 읽은 알림 수다.
func (SQLNotifications) UnreadCount(ctx context.Context, q contract.Tx, userID int64) (int, error) {
	var n int
	err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM notifications WHERE user_id = ? AND read_at IS NULL`, userID).Scan(&n)
	return n, err
}

// MarkRead 는 본인 알림 한 건을 읽음으로 표시한다. 남의 알림·없는 번호는 ErrNotFound, 이미 읽은 것은 그대로 성공이다.
func (SQLNotifications) MarkRead(ctx context.Context, q contract.Tx, userID, id int64, now time.Time) error {
	var owner int64
	err := q.QueryRowContext(ctx, `SELECT user_id FROM notifications WHERE id = ?`, id).Scan(&owner)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && owner != userID) {
		return contract.ErrNotFound
	}
	if err != nil {
		return err
	}
	_, err = q.ExecContext(ctx, `UPDATE notifications SET read_at = ? WHERE id = ? AND read_at IS NULL`, fmtTime(now), id)
	return err
}

// MarkAllRead 는 본인의 안 읽은 알림을 모두 읽음으로 표시하고 그 건수를 돌려준다.
func (SQLNotifications) MarkAllRead(ctx context.Context, q contract.Tx, userID int64, now time.Time) (int, error) {
	res, err := q.ExecContext(ctx, `UPDATE notifications SET read_at = ? WHERE user_id = ? AND read_at IS NULL`, fmtTime(now), userID)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return int(n), err
}
