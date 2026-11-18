package repository

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/dbutil"
)

// SQLReceipts 는 문서 수신 건과 수신 담당자 저장소다(B23 단계 2).
type SQLReceipts struct{}

const receiptSelect = `SELECT rc.id, rc.doc_id, p.no, p.title, COALESCE(f.name,''), p.requester_id, ru.name, rc.unit_id, rc.unit_name, rc.status, rc.created_at,
 COALESCE(rb.name,''), rc.received_at, COALESCE(rc.assignee_id,0), COALESCE(au.name,''), rc.assigned_at, COALESCE(dn.name,''), rc.done_at, COALESCE(rt.name,''), rc.returned_at, rc.comment, rc.reason
 FROM document_receipts rc JOIN purchase_requests p ON p.id = rc.doc_id AND {entity_id:p} JOIN users ru ON ru.id = p.requester_id
 LEFT JOIN approval_forms f ON f.id = p.form_id
 LEFT JOIN users rb ON rb.id = rc.received_by LEFT JOIN users au ON au.id = rc.assignee_id LEFT JOIN users dn ON dn.id = rc.done_by LEFT JOIN users rt ON rt.id = rc.returned_by`

func scanReceipt(s rowScanner) (domain.DocReceipt, error) {
	var r domain.DocReceipt
	var created string
	var recAt, asAt, doneAt, retAt sql.NullString
	err := s.Scan(&r.ID, &r.DocID, &r.DocNo, &r.DocTitle, &r.FormName, &r.RequesterID, &r.RequesterName, &r.UnitID, &r.UnitName, &r.Status, &created,
		&r.ReceivedBy, &recAt, &r.AssigneeID, &r.AssigneeName, &asAt, &r.DoneBy, &doneAt, &r.ReturnedBy, &retAt, &r.Comment, &r.Reason)
	if err != nil {
		return r, err
	}
	r.CreatedAt = parseTime(created)
	tm := func(n sql.NullString) *time.Time {
		if !n.Valid {
			return nil
		}
		t := parseTime(n.String)
		return &t
	}
	r.ReceivedAt, r.AssignedAt, r.DoneAt, r.ReturnedAt = tm(recAt), tm(asAt), tm(doneAt), tm(retAt)
	return r, nil
}

// Get 은 수신 건 한 개다. 없으면 contract.ErrNotFound.
func (SQLReceipts) Get(ctx context.Context, q contract.Tx, id int64) (domain.DocReceipt, error) {
	r, err := scanReceipt(q.QueryRowContext(ctx, contract.EntitySQL(ctx, receiptSelect)+` WHERE rc.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return r, contract.ErrNotFound
	}
	return r, err
}

// ListForDoc 는 한 문서의 수신 건 전부다(문서 상세).
func (SQLReceipts) ListForDoc(ctx context.Context, q contract.Tx, docID int64) ([]domain.DocReceipt, error) {
	return scanReceipts(q.QueryContext(ctx, contract.EntitySQL(ctx, receiptSelect)+` WHERE rc.doc_id = ? ORDER BY rc.id`, docID))
}

// ListForUnits 는 수신처 목록에 속한 수신 건이다(상태 탭 조건 포함).
func (SQLReceipts) ListForUnits(ctx context.Context, q contract.Tx, units []int64, tab string, limit int) ([]domain.DocReceipt, error) {
	ph, args := idList(units)
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	query := contract.EntitySQL(ctx, receiptSelect) + ` WHERE rc.unit_id IN (` + ph + `)` + receiptTabCond(tab) + ` ORDER BY rc.created_at DESC, rc.id DESC LIMIT ?`
	return scanReceipts(q.QueryContext(ctx, query, append(args, limit)...))
}

func scanReceipts(rows *sql.Rows, err error) ([]domain.DocReceipt, error) {
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.DocReceipt
	for rows.Next() {
		r, err := scanReceipt(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Insert 는 결재 완료 때 수신 건을 만든다(문서·차수·수신처마다 한 건).
func (SQLReceipts) Create(ctx context.Context, q contract.Tx, docID int64, round int, unitID int64, unitName string, now time.Time) (int64, error) {
	id, err := dbutil.InsertID(ctx, q, `INSERT INTO document_receipts (doc_id, round, unit_id, unit_name, status, created_at) VALUES (?,?,?,?,'pending',?)`,
		docID, round, unitID, unitName, fmtTime(now))
	if err != nil {
		return 0, mapErr(err)
	}
	return id, nil
}

// Accept 는 접수 대기 건을 접수한다. 이미 다른 상태면 contract.ErrInvalidState.
func (SQLReceipts) Accept(ctx context.Context, q contract.Tx, id, userID int64, now time.Time) error {
	return oneRow(q.ExecContext(ctx, `UPDATE document_receipts SET status='received', received_by=?, received_at=? WHERE id=? AND status='pending'`, userID, fmtTime(now), id))
}

// Assign 은 접수한 건(또는 이미 지정된 건)의 담당자를 정한다.
func (SQLReceipts) Assign(ctx context.Context, q contract.Tx, id, assignee, by int64, now time.Time) error {
	return oneRow(q.ExecContext(ctx, `UPDATE document_receipts SET status='assigned', assignee_id=?, assigned_by=?, assigned_at=? WHERE id=? AND status IN ('received','assigned')`, assignee, by, fmtTime(now), id))
}

// Complete 는 접수·지정된 건을 처리 완료한다.
func (SQLReceipts) Complete(ctx context.Context, q contract.Tx, id, by int64, comment string, now time.Time) error {
	return oneRow(q.ExecContext(ctx, `UPDATE document_receipts SET status='done', done_by=?, done_at=?, comment=? WHERE id=? AND status IN ('received','assigned')`, by, fmtTime(now), comment, id))
}

// Return 은 아직 끝나지 않은 건을 반송한다.
func (SQLReceipts) Return(ctx context.Context, q contract.Tx, id, by int64, reason string, now time.Time) error {
	return oneRow(q.ExecContext(ctx, `UPDATE document_receipts SET status='returned', returned_by=?, returned_at=?, reason=? WHERE id=? AND status IN ('pending','received','assigned')`, by, fmtTime(now), reason, id))
}

func oneRow(res sql.Result, err error) error {
	if err != nil {
		return mapErr(err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return contract.ErrInvalidState
	}
	return nil
}

// Receivers 는 조직의 수신 담당자 번호 목록이다.
func (SQLReceipts) Receivers(ctx context.Context, q contract.Tx, unitID int64) ([]int64, error) {
	return scanIDs(q.QueryContext(ctx, `SELECT user_id FROM unit_receivers WHERE unit_id = ? ORDER BY user_id`, unitID))
}

// UnitsOf 는 사용자가 수신 담당자인 조직 번호들이다.
func (SQLReceipts) UnitsOf(ctx context.Context, q contract.Tx, userID int64) ([]int64, error) {
	return scanIDs(q.QueryContext(ctx, `SELECT unit_id FROM unit_receivers WHERE user_id = ? ORDER BY unit_id`, userID))
}

func (SQLReceipts) AddReceiver(ctx context.Context, q contract.Tx, unitID, userID, by int64, now time.Time) error {
	res, err := q.ExecContext(ctx, `INSERT INTO unit_receivers (unit_id, user_id, added_by, created_at) VALUES (?,?,?,?) ON CONFLICT DO NOTHING`, unitID, userID, by, fmtTime(now))
	if err != nil {
		return mapErr(err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return contract.ErrInvalidState
	}
	return nil
}

func (SQLReceipts) RemoveReceiver(ctx context.Context, q contract.Tx, unitID, userID int64) error {
	return oneRow(q.ExecContext(ctx, `DELETE FROM unit_receivers WHERE unit_id = ? AND user_id = ?`, unitID, userID))
}
