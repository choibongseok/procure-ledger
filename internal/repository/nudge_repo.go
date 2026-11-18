package repository

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/dbutil"
)

// 결재 독촉(B29 ④) 저장소.

// SQLNudges 는 독촉 저장소다.
type SQLNudges struct{}

// ErrAlreadyNudged 는 같은 문서·차수·단계에 오늘(KST) 이미 독촉한 경우다(UNIQUE 가 막았다).
var ErrAlreadyNudged = errors.New("오늘은 이미 독촉했습니다")

// NudgeDoc 은 독촉 판정에 쓰는 문서 요약이다.
type NudgeDoc struct {
	ID            int64
	No            string
	Title         string
	RequesterID   int64
	RequesterName string
	Status        string
	Round         int
	FormKind      string
	Disposed      bool
}

// Doc 은 문서 한 건의 요약이다(없으면 ErrNotFound).
func (SQLNudges) Doc(ctx context.Context, q contract.Tx, id int64) (NudgeDoc, error) {
	var d NudgeDoc
	var disposed string
	err := q.QueryRowContext(ctx, `SELECT p.id, p.no, p.title, p.requester_id, u.name, p.status, p.round, COALESCE(f.kind,'purchase'), COALESCE(p.disposed_at,'')
		FROM purchase_requests p JOIN users u ON u.id = p.requester_id LEFT JOIN approval_forms f ON f.id = p.form_id WHERE `+contract.SQLEntity(ctx, "p")+` AND p.id = ?`, id).
		Scan(&d.ID, &d.No, &d.Title, &d.RequesterID, &d.RequesterName, &d.Status, &d.Round, &d.FormKind, &disposed)
	if errors.Is(err, sql.ErrNoRows) {
		return d, contract.ErrNotFound
	}
	d.Disposed = disposed != ""
	return d, err
}

// NudgeInsert 는 독촉 한 건의 입력이다.
type NudgeInsert struct {
	DocID, By      int64
	Round, Seq     int
	StepTitle      string
	At             time.Time
	Day            string // KST 날짜
	Recipients     int
	RecipientNames string
}

// AddNudge 는 독촉을 기록한다. 같은 날 같은 단계에 이미 있으면 ErrAlreadyNudged.
func (SQLNudges) AddNudge(ctx context.Context, q contract.Tx, in NudgeInsert) error {
	_, err := q.ExecContext(ctx, `INSERT INTO approval_nudges (doc_id, round, seq, step_title, nudged_by, nudged_at, nudge_day, recipients, recipient_names) VALUES (?,?,?,?,?,?,?,?,?)`,
		in.DocID, in.Round, in.Seq, in.StepTitle, in.By, fmtTime(in.At), in.Day, in.Recipients, in.RecipientNames)
	if err != nil && dbutil.IsUnique(err) {
		return ErrAlreadyNudged
	}
	return err
}

// LastToday 는 그 단계에 오늘(KST) 보낸 독촉의 시각이다(없으면 false).
func (SQLNudges) LastToday(ctx context.Context, q contract.Tx, docID int64, round, seq int, day string) (time.Time, bool, error) {
	var at string
	err := q.QueryRowContext(ctx, `SELECT nudged_at FROM approval_nudges WHERE doc_id = ? AND round = ? AND seq = ? AND nudge_day = ?`, docID, round, seq, day).Scan(&at)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, false, nil
	}
	return parseTime(at), err == nil, err
}

// ListByDoc 은 문서의 독촉 기록이다(최근 것이 먼저).
func (SQLNudges) ListByDoc(ctx context.Context, q contract.Tx, docID int64) ([]domain.NudgeRecord, error) {
	rows, err := q.QueryContext(ctx, `SELECT nudged_at, nudge_day, step_title, recipients, recipient_names FROM approval_nudges WHERE doc_id = ? ORDER BY id DESC`, docID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.NudgeRecord{}
	for rows.Next() {
		var r domain.NudgeRecord
		var at, names string
		if err := rows.Scan(&at, &r.Day, &r.StepTitle, &r.Recipients, &names); err != nil {
			return nil, err
		}
		r.At = parseTime(at)
		if names != "" {
			r.RecipientNames = strings.Split(names, ", ")
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// UserNames 는 사용자 번호들의 이름이다.
func (SQLNudges) UserName(ctx context.Context, q contract.Tx, id int64) string {
	var n string
	_ = q.QueryRowContext(ctx, `SELECT name FROM users WHERE id = ?`, id).Scan(&n)
	return n
}

// StepTitle 은 단계의 직책 이름(상신 당시 스냅샷)이다. 없으면 "".
func (SQLNudges) StepTitle(ctx context.Context, q contract.Tx, docID int64, round, seq int) string {
	var t string
	_ = q.QueryRowContext(ctx, `SELECT title FROM approval_steps WHERE pr_id = ? AND round = ? AND seq = ?`, docID, round, seq).Scan(&t)
	return t
}
