package repository

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
)

// SQLInbox 는 문서함 저장소다(B23 단계 2). 모든 조회는 열람 범위(ViewScope.Cond) 한 곳을 거친다. 문서함 조건은 그 위에 더하는 관계 조건뿐이다.
type SQLInbox struct{}

// InboxQuery 는 문서함 조회 조건이다.
type InboxQuery struct {
	Box          domain.InboxBox
	UserID       int64
	Scope        ViewScope
	PendingIDs   []int64 // 지금 내 차례인 문서 번호(결재 대기함·진행함 판정). 서비스가 결재 후보 판정 한 곳에서 구해 넘긴다.
	ReceiveUnits []int64 // 내가 접수 권한이 있는 수신처
	Tab          string
	Limit        int
	Offset       int
}

func idList(ids []int64) (string, []any) {
	if len(ids) == 0 {
		return "-1", nil // 빈 목록(NOT IN 이 NULL 을 만나 전부 걸러지지 않게 존재하지 않는 번호를 쓴다)
	}
	ph, args := idArgs(ids)
	return ph, args
}

// 내가 문서에 관여한 관계(기안자·결재선 처리자·참조 대상). 문서함 조건에서 공통으로 쓴다.
const relatedSQL = `(p.requester_id = ? OR EXISTS (SELECT 1 FROM approval_steps s WHERE s.pr_id = p.id AND s.approver_id = ?)
	OR EXISTS (SELECT 1 FROM approval_step_current c WHERE c.pr_id = p.id AND c.round = p.round AND c.user_id = ?)
	OR EXISTS (SELECT 1 FROM document_recipients r WHERE r.doc_id = p.id AND r.round = p.round AND r.kind = 'cc' AND r.target_type = 'user' AND r.target_id = ?)`

// boxCond 는 문서함 하나의 조건 조각과 인자다(alias p, 읽음 join d 와 함께 쓴다).
func boxCond(q InboxQuery) (string, []any) {
	me := q.UserID
	pend, pendArgs := idList(q.PendingIDs)
	recv, recvArgs := idList(q.ReceiveUnits)
	switch q.Box {
	case domain.BoxDrafted:
		return `p.requester_id = ? AND p.status IN ('draft','submitted','in_review')`, []any{me}
	case domain.BoxPending:
		return `p.id IN (` + pend + `)`, pendArgs
	case domain.BoxInProgress:
		args := []any{me, me, me}
		args = append(args, pendArgs...)
		return `p.status IN ('submitted','in_review') AND p.requester_id <> ? AND (EXISTS (SELECT 1 FROM approval_step_current c WHERE c.pr_id = p.id AND c.round = p.round AND c.user_id = ?)
			OR EXISTS (SELECT 1 FROM approval_steps s WHERE s.pr_id = p.id AND s.approver_id = ?)) AND p.id NOT IN (` + pend + `)`, args
	case domain.BoxDone:
		args := []any{me, me, me, me}
		args = append(args, recvArgs...)
		return `p.status IN ('approved','ordered') AND ` + relatedSQL + ` OR EXISTS (SELECT 1 FROM document_receipts rc WHERE rc.doc_id = p.id AND rc.unit_id IN (` + recv + `)))`, args
	case domain.BoxCC:
		return `p.status <> 'draft' AND EXISTS (SELECT 1 FROM document_recipients r WHERE r.doc_id = p.id AND r.round = p.round AND r.kind = 'cc' AND r.target_type = 'user' AND r.target_id = ?)`, []any{me}
	case domain.BoxRejected:
		return `p.status = 'rejected' AND (p.requester_id = ? OR EXISTS (SELECT 1 FROM approval_steps s WHERE s.pr_id = p.id AND (s.approver_id = ? OR EXISTS (SELECT 1 FROM approval_step_assignees a WHERE a.pr_id = s.pr_id AND a.round = s.round AND a.seq = s.seq AND a.user_id = ?))))`, []any{me, me, me}
	}
	return "1 = 0", nil
}

const inboxSelect = `SELECT p.id, p.no, p.title, COALESCE(f.code,''), COALESCE(f.name,''), COALESCE(f.kind,'purchase'), u.name, COALESCE(l.requester_unit_path,''),
 p.status, p.urgent, p.updated_at, COALESCE(p.amount, p.total_amount),
 CASE WHEN d.read_at IS NULL OR d.read_at < p.updated_at OR d.seen_rev < p.rev THEN 1 ELSE 0 END, CASE WHEN COALESCE(p.disposed_at,'') <> '' THEN 1 ELSE 0 END`

const inboxFrom = ` FROM (SELECT * FROM purchase_requests WHERE {entity_id}) p JOIN users u ON u.id = p.requester_id
 LEFT JOIN approval_lines l ON l.pr_id = p.id AND l.round = p.round
 LEFT JOIN approval_forms f ON f.id = p.form_id
 LEFT JOIN document_reads d ON d.doc_id = p.id AND d.user_id = ?`

func scanInboxRow(rows *sql.Rows, receipt bool) (domain.InboxItem, error) {
	var it domain.InboxItem
	var status, updated string
	var urgent, unread, disposed int
	dest := []any{&it.ID, &it.No, &it.Title, &it.FormCode, &it.FormName, &it.FormKind, &it.RequesterName, &it.UnitPath, &status, &urgent, &updated, &it.Amount, &unread, &disposed}
	if receipt {
		dest = append(dest, &it.ReceiptID, &it.ReceiptStatus, &it.ReceiptUnit)
	}
	if err := rows.Scan(dest...); err != nil {
		return it, err
	}
	it.Status, it.Urgent, it.Unread, it.UpdatedAt = domain.PRStatus(status), urgent == 1, unread == 1, parseTime(updated)
	it.Disposed = disposed == 1
	return it, nil
}

func receiptTabCond(tab string) string {
	switch tab {
	case "waiting":
		return " AND rc.status = 'pending'"
	case "received":
		return " AND rc.status IN ('received','assigned')"
	case "done":
		return " AND rc.status = 'done'"
	case "returned":
		return " AND rc.status = 'returned'"
	}
	return ""
}

// List 는 문서함 한 곳의 목록이다. 결재 대기함은 긴급 먼저·오래된 순, 나머지는 최근 갱신순이다.
func (SQLInbox) List(ctx context.Context, tx contract.Tx, q InboxQuery) ([]domain.InboxItem, error) {
	scope, sargs := q.Scope.Cond()
	limit := q.Limit
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	var query string
	args := []any{q.UserID}
	if q.Box == domain.BoxReceive {
		recv, recvArgs := idList(q.ReceiveUnits)
		query = inboxSelect + `, rc.id, rc.status, rc.unit_name` + contract.EntitySQL(ctx, inboxFrom) + ` JOIN document_receipts rc ON rc.doc_id = p.id WHERE ` + scope +
			` AND rc.unit_id IN (` + recv + `)` + receiptTabCond(q.Tab) + ` ORDER BY rc.created_at DESC, rc.id DESC`
		args = append(args, sargs...)
		args = append(args, recvArgs...)
	} else {
		cond, cargs := boxCond(q)
		order := ` ORDER BY p.updated_at DESC, p.id DESC`
		if q.Box == domain.BoxPending {
			order = ` ORDER BY p.urgent DESC, p.updated_at ASC, p.id ASC`
		}
		query = inboxSelect + contract.EntitySQL(ctx, inboxFrom) + ` WHERE ` + scope + ` AND (` + cond + `)` + order
		args = append(args, sargs...)
		args = append(args, cargs...)
	}
	query += fmt.Sprintf(" LIMIT %d OFFSET %d", limit, q.Offset)
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.InboxItem
	for rows.Next() {
		it, err := scanInboxRow(rows, q.Box == domain.BoxReceive)
		if err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// Count 는 문서함 한 곳의 전체 건수와 안 읽은 건수다.
func (SQLInbox) Count(ctx context.Context, tx contract.Tx, q InboxQuery) (domain.InboxCount, error) {
	scope, sargs := q.Scope.Cond()
	args := []any{q.UserID}
	var query string
	if q.Box == domain.BoxReceive {
		recv, recvArgs := idList(q.ReceiveUnits)
		query = `SELECT COUNT(*), COALESCE(SUM(CASE WHEN d.read_at IS NULL OR d.read_at < p.updated_at OR d.seen_rev < p.rev THEN 1 ELSE 0 END),0)` + contract.EntitySQL(ctx, inboxFrom) +
			` JOIN document_receipts rc ON rc.doc_id = p.id WHERE ` + scope + ` AND rc.unit_id IN (` + recv + `) AND rc.status IN ('pending','received','assigned')`
		args = append(args, sargs...)
		args = append(args, recvArgs...)
	} else {
		cond, cargs := boxCond(q)
		query = `SELECT COUNT(*), COALESCE(SUM(CASE WHEN d.read_at IS NULL OR d.read_at < p.updated_at OR d.seen_rev < p.rev THEN 1 ELSE 0 END),0)` + contract.EntitySQL(ctx, inboxFrom) + ` WHERE ` + scope + ` AND (` + cond + `)`
		args = append(args, sargs...)
		args = append(args, cargs...)
	}
	var c domain.InboxCount
	err := tx.QueryRowContext(ctx, query, args...).Scan(&c.Total, &c.Unread)
	return c, err
}

// MarkRead 는 읽음 시각을 지금으로 올린다(없으면 만든다).
func (SQLInbox) MarkRead(ctx context.Context, tx contract.Tx, docID, userID int64, now time.Time) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO document_reads (doc_id, user_id, read_at, seen_rev) VALUES (?,?,?,(SELECT rev FROM purchase_requests WHERE `+contract.SQLEntity(ctx, "purchase_requests")+` AND id = ?))
		ON CONFLICT(doc_id, user_id) DO UPDATE SET read_at = excluded.read_at, seen_rev = excluded.seen_rev`, docID, userID, fmtTime(now), docID)
	return err
}

var _ = strings.Join
