package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/dbutil"
)

// PRRecord 는 구매요청과 현재 결재 차수(Round)다.
type PRRecord struct {
	domain.PurchaseRequest
	Round int
}

// PRInsert 는 구매요청 신규 입력이다.
type PRInsert struct {
	RequesterID  int64
	DepartmentID int64
	OwnerUnitID  int64 // 귀속 조직(요청자 주소속). 조직에 등록되지 않았으면 0
	VendorID     int64
	Title        string
	Reason       string
	Total        int64
	Lines        []domain.PRLine
	Now          time.Time
}

// PRQuery 는 목록 조건이다. Scope 가 열람 범위(ViewScope)다.
// Only 는 "purchase"(구매 품의서만)·"docs"(그 밖의 양식 문서만)·빈 값(전부, 내부 조회)이고 FormCode 는 양식 코드로 거른다.
type PRQuery struct {
	Filter   domain.PRFilter
	Scope    ViewScope
	Only     string
	FormCode string
	// Max 가 0 보다 크면 쪽 나누기 상한(200) 대신 이 값까지 읽는다(내보내기: 호출한 쪽이 정한 상한+1).
	Max int
}

// PurchaseRepository 는 구매요청 저장소다.
type PurchaseRepository interface {
	InsertPR(ctx context.Context, q contract.Tx, in PRInsert) (int64, error)
	UpdatePR(ctx context.Context, q contract.Tx, id int64, title, reason string, vendorID, total int64, lines []domain.PRLine, now time.Time) error
	GetPR(ctx context.Context, q contract.Tx, id int64, scope ViewScope) (PRRecord, error)
	ListPRs(ctx context.Context, q contract.Tx, f PRQuery) ([]PRRecord, error)
	CountPRs(ctx context.Context, q contract.Tx, f PRQuery) (int, error)
	SetPRState(ctx context.Context, q contract.Tx, id int64, status domain.PRStatus, round int, rejectReason string, now time.Time) error
	SetPurchaseOrder(ctx context.Context, q contract.Tx, prID, poID int64) error
	VendorExists(ctx context.Context, q contract.Tx, id int64) (bool, error)
	ActiveItems(ctx context.Context, q contract.Tx, ids []int64) (map[int64]domain.Item, error)
	LineKindsFor(ctx context.Context, q contract.Tx, amount int64) ([]domain.StepKind, error)
	// LatestUnitPrices 는 품목별 가장 최근 발주 줄의 단가다(발주 이력이 없는 품목은 없다).
	LatestUnitPrices(ctx context.Context, q contract.Tx, itemIDs []int64) (map[int64]int64, error)
	// CountByRequester 는 요청자 본인이 올린 구매요청의 상태별 건수다.
	CountByRequester(ctx context.Context, q contract.Tx, requesterID int64) (map[domain.PRStatus]int, error)
	// DraftNosByItem 은 임시저장 상태의 구매요청 줄에 이미 들어 있는 품목과 그 요청 번호다(열람 범위 안만).
	DraftNosByItem(ctx context.Context, q contract.Tx, itemIDs []int64, scope ViewScope) (map[int64]string, error)
	// SetOwner 는 구매요청의 귀속 조직·귀속 부서를 요청자의 현재 소속으로 갱신한다(임시저장·상신 때).
	SetOwner(ctx context.Context, q contract.Tx, id, ownerUnitID, deptID int64) error
	// B23 양식 문서(구매 품의서 외)
	InsertDoc(ctx context.Context, q contract.Tx, in DocInsert) (int64, error)
	UpdateDoc(ctx context.Context, q contract.Tx, id int64, title, fieldsJSON string, amount int64, urgent bool, now time.Time) error
	SetDocNumber(ctx context.Context, q contract.Tx, id int64, no string) error
	SetCompleted(ctx context.Context, q contract.Tx, id int64, at time.Time, retentionUntil string, legalHold bool) error
	DraftEdits(ctx context.Context, q contract.Tx, id int64) (string, error)
	SetDraftEdits(ctx context.Context, q contract.Tx, id int64, edits string) error
	LineKindsForForm(ctx context.Context, q contract.Tx, formID, amount int64) ([]domain.StepKind, error)
}

// SQLPurchases 는 PurchaseRepository 의 SQLite 구현이다.
type SQLPurchases struct{}

const prSelect = `SELECT p.id, p.no, p.requester_id, u.name, p.department_id, COALESCE(p.vendor_id,0), p.title, p.reason,
 p.status, p.total_amount, p.reject_reason, p.round, COALESCE(p.purchase_order_id,0), p.created_at, p.updated_at,
 COALESCE(p.owner_unit_id,0), COALESCE(l.requester_unit_path,''), COALESCE(l.as_of_day,''),
 COALESCE(f.code,''), COALESCE(f.name,''), COALESCE(p.amount, p.total_amount),
 COALESCE(f.kind,'purchase'), COALESCE(p.form_id,0), COALESCE(p.form_version_id,0), p.urgent, COALESCE(p.retention_until,''), p.fields, p.dept_visible,
 COALESCE(p.due_date,''), p.delivery_place, p.budget_state, p.budget_json, COALESCE(p.quote_id,0), p.quote_reason, COALESCE(p.disposed_at,'')
 FROM (SELECT * FROM purchase_requests WHERE {entity_id}) p JOIN users u ON u.id = p.requester_id
 LEFT JOIN approval_lines l ON l.pr_id = p.id AND l.round = p.round
 LEFT JOIN approval_forms f ON f.id = p.form_id`

type rowScanner interface{ Scan(dest ...any) error }

func scanPR(s rowScanner) (PRRecord, error) {
	var r PRRecord
	var status, created, updated, fieldsJSON, budgetState, budgetJSON string
	var urgent, deptVisible int
	err := s.Scan(&r.ID, &r.No, &r.RequesterID, &r.RequesterName, &r.DepartmentID, &r.VendorID, &r.Title, &r.Reason,
		&status, &r.TotalAmount, &r.RejectReason, &r.Round, &r.PurchaseOrderID, &created, &updated,
		&r.OwnerUnitID, &r.RequesterUnitPath, &r.LineAsOf, &r.FormCode, &r.FormName, &r.Amount, &r.FormKind, &r.FormID, &r.FormVersionID, &urgent, &r.RetentionUntil, &fieldsJSON, &deptVisible, &r.DueDate, &r.DeliveryPlace, &budgetState, &budgetJSON, &r.QuoteID, &r.QuoteReason, &r.DisposedAt)
	if err != nil {
		return r, err
	}
	if t, perr := time.Parse(timeLayout, r.DisposedAt); perr == nil {
		r.DisposedAt = t.In(time.FixedZone("KST", 9*3600)).Format("2006-01-02")
	}
	r.Status = domain.PRStatus(status)
	r.Urgent = urgent == 1
	r.DeptVisible = deptVisible == 1
	r.Budget = ParseBudgetSnapshot(budgetState, budgetJSON)
	if fieldsJSON != "" && fieldsJSON != "{}" {
		_ = json.Unmarshal([]byte(fieldsJSON), &r.Fields)
	}
	r.PurchaseRequest.Round = r.Round
	r.CreatedAt, r.UpdatedAt = parseTime(created), parseTime(updated)
	return r, nil
}

func (SQLPurchases) hydrate(ctx context.Context, q contract.Tx, r *PRRecord) error {
	rows, err := q.QueryContext(ctx, `SELECT l.id, l.item_id, i.name, l.quantity, l.unit_price, l.amount
		FROM purchase_request_lines l JOIN items i ON i.id = l.item_id WHERE l.pr_id = ? ORDER BY l.id`, r.ID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var l domain.PRLine
		if err := rows.Scan(&l.ID, &l.ItemID, &l.ItemName, &l.Quantity, &l.UnitPrice, &l.Amount); err != nil {
			rows.Close()
			return err
		}
		r.Lines = append(r.Lines, l)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	steps, err := loadSteps(ctx, q, r.ID, r.Round)
	if err != nil {
		return err
	}
	r.Steps = steps
	recs, err := SQLApprovals{}.ListRecipients(ctx, q, r.ID, r.Round)
	if err != nil {
		return err
	}
	r.Recipients = recs
	return nil
}

func loadSteps(ctx context.Context, q contract.Tx, prID int64, round int) ([]domain.ApprovalStep, error) {
	rows, err := q.QueryContext(ctx, `SELECT seq, step_kind, status, COALESCE(approver_id,0), comment, decided_at,
		COALESCE(unit_id,0), unit_path, title, acting, raised_note, layer_note, role, stage, required, final_authority, skip_reason,
		COALESCE(on_behalf_of,0), COALESCE((SELECT u.name FROM users u WHERE u.id = approval_steps.on_behalf_of),''), COALESCE((SELECT u.name FROM users u WHERE u.id = approval_steps.approver_id),'')
		FROM approval_steps WHERE pr_id = ? AND round = ? ORDER BY seq`, prID, round)
	if err != nil {
		return nil, err
	}
	var out []domain.ApprovalStep
	for rows.Next() {
		var s domain.ApprovalStep
		var kind, status string
		var decided sql.NullString
		var acting, required, finalAuth int
		if err := rows.Scan(&s.Seq, &kind, &status, &s.ApproverID, &s.Comment, &decided,
			&s.UnitID, &s.UnitPath, &s.Title, &acting, &s.RaisedNote, &s.LayerNote, &s.Role, &s.Stage, &required, &finalAuth, &s.SkipReason, &s.OnBehalfOfID, &s.OnBehalfOfName, &s.ApproverName); err != nil {
			rows.Close()
			return nil, err
		}
		s.Kind, s.Status, s.Acting, s.Required = domain.StepKind(kind), domain.StepStatus(status), acting == 1, required == 1
		s.FinalAuthority = finalAuth == 1
		if decided.Valid {
			t := parseTime(decided.String)
			s.DecidedAt = &t
		}
		out = append(out, s)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return out, nil
	}
	// 현재 결재자(재지정 반영, 뷰). 한 사람이면 채우고 여럿(도입 이전 건)이면 비워 둔다.
	cur, err := q.QueryContext(ctx, `SELECT c.seq, c.user_id, COALESCE(
			(SELECT r.to_name_at FROM approval_step_reassignments r WHERE r.pr_id = c.pr_id AND r.round = c.round AND r.seq = c.seq ORDER BY r.id DESC LIMIT 1),
			(SELECT a.name_at FROM approval_step_assignees a WHERE a.pr_id = c.pr_id AND a.round = c.round AND a.seq = c.seq AND a.user_id = c.user_id), '')
		FROM approval_step_current c WHERE c.pr_id = ? AND c.round = ? ORDER BY c.seq, c.user_id`, prID, round)
	if err != nil {
		return nil, err
	}
	type cu struct {
		id   int64
		name string
	}
	bySeq := map[int][]cu{}
	for cur.Next() {
		var seq int
		var x cu
		if err := cur.Scan(&seq, &x.id, &x.name); err != nil {
			cur.Close()
			return nil, err
		}
		bySeq[seq] = append(bySeq[seq], x)
	}
	if err := cur.Close(); err != nil {
		return nil, err
	}
	// 재지정 직전 결재자 이름(마지막 재지정 기준).
	rr, err := q.QueryContext(ctx, `SELECT r.seq, r.from_name_at FROM approval_step_reassignments r
		WHERE r.pr_id = ? AND r.round = ? AND r.id = (SELECT MAX(r2.id) FROM approval_step_reassignments r2 WHERE r2.pr_id = r.pr_id AND r2.round = r.round AND r2.seq = r.seq)`, prID, round)
	if err != nil {
		return nil, err
	}
	from := map[int]string{}
	for rr.Next() {
		var seq int
		var name string
		if err := rr.Scan(&seq, &name); err != nil {
			rr.Close()
			return nil, err
		}
		from[seq] = name
	}
	if err := rr.Close(); err != nil {
		return nil, err
	}
	for i := range out {
		switch list := bySeq[out[i].Seq]; {
		case len(list) == 1:
			out[i].AssigneeID, out[i].AssigneeName = list[0].id, list[0].name
		case len(list) > 1:
			// 도입 이전 건처럼 후보가 여럿이면 모두 보인다(결재자 한 사람을 정할 수 없다).
			names := make([]string, 0, len(list))
			for _, c := range list {
				names = append(names, c.name)
			}
			out[i].AssigneeName = strings.Join(names, ", ")
		}
		if out[i].Title == "" {
			out[i].Title = stepTitleOf(out[i].Kind) // 스냅샷 열이 없던 시기의 단계는 직책 이름을 단계 종류에서 채운다
		}
		out[i].ReassignedFrom = from[out[i].Seq]
	}
	return out, nil
}

func insertLines(ctx context.Context, q contract.Tx, prID int64, lines []domain.PRLine) error {
	for _, l := range lines {
		if _, err := q.ExecContext(ctx, `INSERT INTO purchase_request_lines (pr_id, item_id, quantity, unit_price, amount) VALUES (?,?,?,?,?)`,
			prID, l.ItemID, l.Quantity, l.UnitPrice, l.Amount); err != nil {
			return mapErr(err)
		}
	}
	return nil
}

// InsertPR 은 PR 헤더·줄을 넣고 번호를 부여한다.
func (SQLPurchases) InsertPR(ctx context.Context, q contract.Tx, in PRInsert) (int64, error) {
	now := fmtTime(in.Now)
	id, err := dbutil.InsertID(ctx, q, `INSERT INTO purchase_requests
		(entity_id, no, requester_id, department_id, owner_unit_id, vendor_id, title, reason, status, total_amount, round, created_at, updated_at)
		VALUES (`+contract.SQLEntityID(ctx)+`, ?,?,?,?,?,?,?, 'draft', ?, 1, ?, ?)`,
		fmt.Sprintf("TMP-%d-%d", in.Now.UnixNano(), in.RequesterID), in.RequesterID, in.DepartmentID, nullID(in.OwnerUnitID), nullID(in.VendorID),
		in.Title, in.Reason, in.Total, now, now)
	if err != nil {
		return 0, mapErr(err)
	}
	if _, err := q.ExecContext(ctx, `UPDATE purchase_requests SET no = ? WHERE `+contract.SQLEntity(ctx, "purchase_requests")+` AND id = ?`, fmt.Sprintf("PR-%06d", id), id); err != nil {
		return 0, err
	}
	if err := insertLines(ctx, q, id, in.Lines); err != nil {
		return 0, err
	}
	return id, SQLPurchases{}.RefreshSearchText(ctx, q, id)
}

// UpdatePR 은 헤더를 고치고 줄을 통째로 바꾼다(결재 이력이 아닌 작성 내용이라 수정 가능).
func (SQLPurchases) UpdatePR(ctx context.Context, q contract.Tx, id int64, title, reason string, vendorID, total int64, lines []domain.PRLine, now time.Time) error {
	if _, err := q.ExecContext(ctx, `UPDATE purchase_requests SET title=?, reason=?, vendor_id=?, total_amount=?, updated_at=? WHERE `+contract.SQLEntity(ctx, "purchase_requests")+` AND id=?`,
		title, reason, nullID(vendorID), total, fmtTime(now), id); err != nil {
		return mapErr(err)
	}
	if _, err := q.ExecContext(ctx, `DELETE FROM purchase_request_lines WHERE pr_id = ?`, id); err != nil {
		return err
	}
	if err := insertLines(ctx, q, id, lines); err != nil {
		return err
	}
	return SQLPurchases{}.RefreshSearchText(ctx, q, id)
}

// GetPR 은 한 건을 줄·현재 차수 결재 단계와 함께 읽는다. 범위 밖이면 ErrNotFound.
func (r SQLPurchases) GetPR(ctx context.Context, q contract.Tx, id int64, scope ViewScope) (PRRecord, error) {
	cond, cargs := scope.Cond()
	query, args := contract.EntitySQL(ctx, prSelect)+` WHERE p.id = ? AND `+cond, append([]any{id}, cargs...)
	rec, err := scanPR(q.QueryRowContext(ctx, query, args...))
	if errors.Is(err, sql.ErrNoRows) {
		return PRRecord{}, contract.ErrNotFound
	}
	if err != nil {
		return PRRecord{}, err
	}
	return rec, r.hydrate(ctx, q, &rec)
}

// prWhere 는 목록·건수가 같이 쓰는 조건절이다. withCursor 가 false 면 afterId 는 뺀다(전체 건수는 커서와 무관하다).
func prWhere(f PRQuery, withCursor bool) (string, []any) {
	var where []string
	var args []any
	add := func(cond string, v any) { where = append(where, cond); args = append(args, v) }
	if !f.Scope.All {
		cond, cargs := f.Scope.Cond()
		where = append(where, cond)
		args = append(args, cargs...)
	} else if f.Scope.LeaveGuard { // 전체 열람자도 휴가 신청서는 관계 범위만(B50 AC-5040)
		cond, cargs := f.Scope.LeaveCond()
		where = append(where, cond)
		args = append(args, cargs...)
	}
	switch f.Only {
	case "purchase":
		where = append(where, "COALESCE(f.kind,'purchase') = 'purchase'")
	case "docs":
		where = append(where, "COALESCE(f.kind,'purchase') <> 'purchase'")
	}
	if f.FormCode != "" {
		add("f.code = ?", f.FormCode)
	}
	if f.Filter.DepartmentID != 0 {
		add("p.department_id = ?", f.Filter.DepartmentID)
	}
	if f.Filter.Status != "" {
		add("p.status = ?", string(f.Filter.Status))
	}
	if f.Filter.RequesterID != 0 {
		add("p.requester_id = ?", f.Filter.RequesterID)
	}
	if withCursor && f.Filter.AfterID != 0 {
		add("p.id > ?", f.Filter.AfterID)
	}
	if f.Filter.From != "" {
		add("date(p.created_at, '+9 hours') >= ?", f.Filter.From)
	}
	if f.Filter.To != "" {
		add("date(p.created_at, '+9 hours') <= ?", f.Filter.To)
	}
	if f.Filter.Query != "" {
		pat := likePattern(f.Filter.Query)
		where = append(where, "(lower(p.title) LIKE lower(?) ESCAPE '\\' OR lower(p.no) LIKE lower(?) ESCAPE '\\')")
		args = append(args, pat, pat)
	}
	if len(where) == 0 {
		return "", args
	}
	return " WHERE " + strings.Join(where, " AND "), args
}

// ListPRs 는 목록이다(id 오름차순). 쪽 이동은 offset+limit 이고 afterId 커서는 호환용으로 남아 있다.
func (r SQLPurchases) ListPRs(ctx context.Context, q contract.Tx, f PRQuery) ([]PRRecord, error) {
	cond, args := prWhere(f, true)
	limit := f.Filter.Limit
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if f.Max > 0 {
		limit = f.Max
	}
	query := contract.EntitySQL(ctx, prSelect) + cond + prOrder(f.Filter) + fmt.Sprintf(" LIMIT %d", limit)
	if f.Filter.Offset > 0 {
		query += fmt.Sprintf(" OFFSET %d", f.Filter.Offset)
	}
	return r.queryPRs(ctx, q, query, args...)
}

// prOrder 는 정렬절이다. 열 이름은 화이트리스트에서만 고르고(서비스가 입력을 먼저 거른다) 사용자 문자열을 이어 붙이지 않는다.
// 정렬 지정이 없으면 id 오름차순(afterId 커서·내보내기 호환). 같은 값은 id 로 이어 순서를 고정한다.
func prOrder(f domain.PRFilter) string {
	col := ""
	switch f.Sort {
	case "created_at":
		col = "p.created_at"
	case "total_amount":
		col = "p.total_amount"
	default:
		return " ORDER BY p.id"
	}
	dir := "ASC"
	if f.Order == "desc" {
		dir = "DESC"
	}
	return " ORDER BY " + col + " " + dir + ", p.id " + dir
}

// CountPRs 는 같은 조건(afterId·limit·offset 제외)의 전체 건수다. 열람 범위는 ListPRs 와 같은 조건절이다.
func (r SQLPurchases) CountPRs(ctx context.Context, q contract.Tx, f PRQuery) (int, error) {
	cond, args := prWhere(f, false)
	var n int
	err := q.QueryRowContext(ctx, "SELECT COUNT(*)"+contract.EntitySQL(ctx, prSelect)[strings.Index(contract.EntitySQL(ctx, prSelect), "\n FROM"):]+cond, args...).Scan(&n)
	return n, err
}

func (r SQLPurchases) queryPRs(ctx context.Context, q contract.Tx, query string, args ...any) ([]PRRecord, error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	var out []PRRecord
	for rows.Next() {
		rec, err := scanPR(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, rec)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for i := range out {
		if err := r.hydrate(ctx, q, &out[i]); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// SetPRState 는 상태·차수·반려 사유를 바꾼다. 상태 전이의 허용 여부는 서비스의 전이 표가 정한다.
func (SQLPurchases) SetPRState(ctx context.Context, q contract.Tx, id int64, status domain.PRStatus, round int, rejectReason string, now time.Time) error {
	_, err := q.ExecContext(ctx, `UPDATE purchase_requests SET status=?, round=?, reject_reason=?, updated_at=? WHERE `+contract.SQLEntity(ctx, "purchase_requests")+` AND id=?`,
		string(status), round, rejectReason, fmtTime(now), id)
	return err
}

// SetPurchaseOrder 는 PR 에 PO 를 연결한다.
func (SQLPurchases) SetPurchaseOrder(ctx context.Context, q contract.Tx, prID, poID int64) error {
	_, err := q.ExecContext(ctx, `UPDATE purchase_requests SET purchase_order_id=? WHERE `+contract.SQLEntity(ctx, "purchase_requests")+` AND id=?`, poID, prID)
	return err
}

// VendorExists 는 거래처 존재 여부다.
func (SQLPurchases) VendorExists(ctx context.Context, q contract.Tx, id int64) (bool, error) {
	var n int
	err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM vendors WHERE id = ?`, id).Scan(&n)
	return n > 0, err
}

// ActiveItems 는 사용 중인 품목만 돌려준다.
func (SQLPurchases) ActiveItems(ctx context.Context, q contract.Tx, ids []int64) (map[int64]domain.Item, error) {
	out := map[int64]domain.Item{}
	for _, id := range ids {
		it, err := getItem(ctx, q, id)
		if errors.Is(err, contract.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		out[id] = it
	}
	return out, nil
}

// LineKindsFor 는 금액 구간의 결재선(approval_line_rules)을 seq 순으로 돌려준다.
func (r SQLPurchases) LineKindsFor(ctx context.Context, q contract.Tx, amount int64) ([]domain.StepKind, error) {
	return r.LineKindsForForm(ctx, q, 1, amount)
}

// LineKindsForForm 은 양식(form_id)의 금액 구간 결재선이다. 구매 품의서는 1, 지출 결의서는 3 이다(규칙 표는 양식별).
func (SQLPurchases) LineKindsForForm(ctx context.Context, q contract.Tx, formID, amount int64) ([]domain.StepKind, error) {
	rows, err := q.QueryContext(ctx, `SELECT step_kind FROM approval_line_rules
		WHERE form_id = ? AND min_amount <= ? AND (max_amount IS NULL OR ? < max_amount) ORDER BY seq`, formID, amount, amount)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.StepKind
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, err
		}
		out = append(out, domain.StepKind(k))
	}
	return out, rows.Err()
}

func idArgs(ids []int64) (string, []any) {
	ph := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		ph[i], args[i] = "?", id
	}
	return strings.Join(ph, ","), args
}

func (SQLPurchases) LatestUnitPrices(ctx context.Context, q contract.Tx, itemIDs []int64) (map[int64]int64, error) {
	out := map[int64]int64{}
	if len(itemIDs) == 0 {
		return out, nil
	}
	ph, args := idArgs(itemIDs)
	rows, err := q.QueryContext(ctx, `SELECT l.item_id, l.unit_price FROM po_lines l
		WHERE l.item_id IN (`+ph+`) AND l.id = (SELECT MAX(l2.id) FROM po_lines l2 WHERE l2.item_id = l.item_id)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, price int64
		if err := rows.Scan(&id, &price); err != nil {
			return nil, err
		}
		out[id] = price
	}
	return out, rows.Err()
}

func (SQLPurchases) DraftNosByItem(ctx context.Context, q contract.Tx, itemIDs []int64, scope ViewScope) (map[int64]string, error) {
	out := map[int64]string{}
	if len(itemIDs) == 0 {
		return out, nil
	}
	ph, args := idArgs(itemIDs)
	query := `SELECT l.item_id, MIN(p.no) FROM purchase_request_lines l JOIN purchase_requests p ON p.id = l.pr_id
		WHERE ` + contract.SQLEntity(ctx, "p") + ` AND p.status = 'draft' AND l.item_id IN (` + ph + `)`
	if !scope.All {
		scope.SeeDrafts = true // 같은 부서의 초안 중복을 막으려면 동료의 초안도 봐야 한다(번호만 돌려준다)
		cond, cargs := scope.Cond()
		query += ` AND ` + cond
		args = append(args, cargs...)
	}
	rows, err := q.QueryContext(ctx, query+` GROUP BY l.item_id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var no string
		if err := rows.Scan(&id, &no); err != nil {
			return nil, err
		}
		out[id] = no
	}
	return out, rows.Err()
}

func (SQLPurchases) CountByRequester(ctx context.Context, q contract.Tx, requesterID int64) (map[domain.PRStatus]int, error) {
	rows, err := q.QueryContext(ctx, `SELECT status, COUNT(*) FROM purchase_requests WHERE `+contract.SQLEntity(ctx, "purchase_requests")+` AND requester_id = ? AND COALESCE(form_id, 1) = 1 GROUP BY status`, requesterID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[domain.PRStatus]int{}
	for rows.Next() {
		var st string
		var n int
		if err := rows.Scan(&st, &n); err != nil {
			return nil, err
		}
		out[domain.PRStatus(st)] = n
	}
	return out, rows.Err()
}

// SetOwner 는 귀속 조직과 귀속 부서를 갱신한다. 상신 뒤 고정되므로 임시저장·상신 시점에만 부른다.
func (SQLPurchases) SetOwner(ctx context.Context, q contract.Tx, id, ownerUnitID, deptID int64) error {
	_, err := q.ExecContext(ctx, `UPDATE purchase_requests SET owner_unit_id = ?, department_id = ? WHERE `+contract.SQLEntity(ctx, "purchase_requests")+` AND id = ?`, nullID(ownerUnitID), deptID, id)
	return mapErr(err)
}

// stepTitleOf 는 단계 종류의 한국어 직책 이름이다(스냅샷에 직책이 없을 때만 쓴다). 서비스의 종류 레지스트리와 같은 이름이다.
func stepTitleOf(k domain.StepKind) string {
	switch k {
	case domain.StepTeamLead:
		return "팀장"
	case domain.StepDeptHead:
		return "부서장"
	case domain.StepDivisionHead:
		return "본부장"
	case domain.StepCEO:
		return "대표"
	case domain.StepFinance:
		return "재무"
	}
	return ""
}

// DocInsert 는 구매 품의서 외 양식 문서의 신규 입력이다(구매요청 표의 양식 열을 쓴다).
type DocInsert struct {
	RequesterID, DepartmentID, OwnerUnitID int64
	FormID, FormVersionID                  int64
	Title                                  string
	FieldsJSON                             string
	Amount                                 int64
	Urgent, DeptVisible                    bool
	Now                                    time.Time
}

// InsertDoc 는 양식 문서 임시저장 행을 만든다. 번호는 상신 때 매긴다(임시 번호 TMP-…).
func (SQLPurchases) InsertDoc(ctx context.Context, q contract.Tx, in DocInsert) (int64, error) {
	now := fmtTime(in.Now)
	id, err := dbutil.InsertID(ctx, q, `INSERT INTO purchase_requests
		(entity_id, no, requester_id, department_id, owner_unit_id, title, reason, status, total_amount, round, created_at, updated_at,
		 form_id, form_version_id, fields, amount, urgent, dept_visible)
		VALUES (`+contract.SQLEntityID(ctx)+`, ?,?,?,?,?, '', 'draft', ?, 1, ?, ?, ?,?,?,?,?,?)`,
		tmpNo(), in.RequesterID, in.DepartmentID, nullID(in.OwnerUnitID), in.Title,
		in.Amount, now, now, in.FormID, in.FormVersionID, in.FieldsJSON, in.Amount, boolInt(in.Urgent), boolInt(in.DeptVisible))
	if err != nil {
		return 0, mapErr(err)
	}
	return id, SQLPurchases{}.RefreshSearchText(ctx, q, id)
}

// UpdateDoc 은 양식 문서 임시저장 내용을 바꾼다.
func (SQLPurchases) UpdateDoc(ctx context.Context, q contract.Tx, id int64, title, fieldsJSON string, amount int64, urgent bool, now time.Time) error {
	_, err := q.ExecContext(ctx, `UPDATE purchase_requests SET title=?, fields=?, amount=?, total_amount=?, urgent=?, updated_at=? WHERE `+contract.SQLEntity(ctx, "purchase_requests")+` AND id=?`,
		title, fieldsJSON, amount, amount, boolInt(urgent), fmtTime(now), id)
	if err != nil {
		return mapErr(err)
	}
	return SQLPurchases{}.RefreshSearchText(ctx, q, id)
}

// SetDocNumber 는 상신 때 문서 번호를 매긴다.
func (SQLPurchases) SetDocNumber(ctx context.Context, q contract.Tx, id int64, no string) error {
	if _, err := q.ExecContext(ctx, `UPDATE purchase_requests SET no = ? WHERE `+contract.SQLEntity(ctx, "purchase_requests")+` AND id = ?`, no, id); err != nil {
		return mapErr(err)
	}
	return SQLPurchases{}.RefreshSearchText(ctx, q, id)
}

// SetCompleted 는 결재 완료 시각과 보존 기한(없으면 ""), 법정 보존 여부(폐기 불가 스냅샷)를 기록한다.
func (SQLPurchases) SetCompleted(ctx context.Context, q contract.Tx, id int64, at time.Time, retentionUntil string, legalHold bool) error {
	hold := 0
	if legalHold {
		hold = 1
	}
	_, err := q.ExecContext(ctx, `UPDATE purchase_requests SET completed_at = ?, retention_until = ?, legal_hold = ? WHERE `+contract.SQLEntity(ctx, "purchase_requests")+` AND id = ?`, fmtTime(at), nullStr(retentionUntil), hold, id)
	return mapErr(err)
}

// DraftEdits 는 임시저장 문서의 결재선 편집(JSON)이다. 없으면 "".
func (SQLPurchases) DraftEdits(ctx context.Context, q contract.Tx, id int64) (string, error) {
	var e string
	err := q.QueryRowContext(ctx, `SELECT draft_edits FROM purchase_requests WHERE `+contract.SQLEntity(ctx, "purchase_requests")+` AND id = ?`, id).Scan(&e)
	return e, err
}

// SetDraftEdits 는 임시저장 문서의 결재선 편집을 저장한다.
func (SQLPurchases) SetDraftEdits(ctx context.Context, q contract.Tx, id int64, edits string) error {
	_, err := q.ExecContext(ctx, `UPDATE purchase_requests SET draft_edits = ? WHERE `+contract.SQLEntity(ctx, "purchase_requests")+` AND id = ?`, edits, id)
	return mapErr(err)
}
