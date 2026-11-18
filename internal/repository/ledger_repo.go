package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/choibongseok/procure-ledger/internal/changes"
	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/dbutil"
)

// 계정과목·전표·매입 채무 저장소(B31). 전표의 상태·줄·마감 불변식은 DB 트리거가 한 번 더 지킨다(마이그레이션 0037).

// SQLLedger 는 재무 저장소다.
type SQLLedger struct{}

// ---- 계정과목 ----

func scanAccount(s rowScanner) (domain.Account, error) {
	var a domain.Account
	var active, system int
	if err := s.Scan(&a.ID, &a.Code, &a.Name, &a.Kind, &active, &system); err != nil {
		return a, err
	}
	a.Active, a.System = active == 1, system == 1
	return a, nil
}

const accountCols = `SELECT id, code, name, kind, active, system FROM accounts`

// Accounts 는 계정 목록이다(코드 순). includeInactive 가 거짓이면 사용 중인 계정만.
func (SQLLedger) Accounts(ctx context.Context, q contract.Tx, includeInactive bool) ([]domain.Account, error) {
	query := accountCols
	if !includeInactive {
		query += ` WHERE active = 1`
	}
	rows, err := q.QueryContext(ctx, query+` ORDER BY code`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.Account{}
	for rows.Next() {
		a, err := scanAccount(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// AccountByCode 는 코드로 계정 하나를 읽는다(없으면 false).
func (SQLLedger) AccountByCode(ctx context.Context, q contract.Tx, code string) (domain.Account, bool, error) {
	a, err := scanAccount(q.QueryRowContext(ctx, accountCols+` WHERE code = ?`, code))
	if errors.Is(err, sql.ErrNoRows) {
		return a, false, nil
	}
	return a, err == nil, err
}

// AccountByID 는 번호로 계정 하나를 읽는다(없으면 false).
func (SQLLedger) AccountByID(ctx context.Context, q contract.Tx, id int64) (domain.Account, bool, error) {
	a, err := scanAccount(q.QueryRowContext(ctx, accountCols+` WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return a, false, nil
	}
	return a, err == nil, err
}

func (SQLLedger) InsertAccount(ctx context.Context, q contract.Tx, code, name, kind string, now time.Time) error {
	_, err := q.ExecContext(ctx, `INSERT INTO accounts (code, name, kind, active, system, created_at) VALUES (?,?,?,1,0,?)`, code, name, kind, fmtTime(now))
	return mapErr(err)
}

func (SQLLedger) RenameAccount(ctx context.Context, q contract.Tx, code, name string) error {
	_, err := q.ExecContext(ctx, `UPDATE accounts SET name = ? WHERE code = ?`, name, code)
	return mapErr(err)
}

func (SQLLedger) SetAccountActive(ctx context.Context, q contract.Tx, code string, active bool) error {
	a := 0
	if active {
		a = 1
	}
	_, err := q.ExecContext(ctx, `UPDATE accounts SET active = ? WHERE code = ?`, a, code)
	return mapErr(err)
}

// Rules 는 자동 전표 규칙과 그 계정이다(키 순).
func (SQLLedger) Rules(ctx context.Context, q contract.Tx) ([]domain.AccountRule, error) {
	rows, err := q.QueryContext(ctx, `SELECT r.key, a.code, a.name, a.active FROM account_rules r JOIN accounts a ON a.id = r.account_id ORDER BY r.key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.AccountRule{}
	for rows.Next() {
		var r domain.AccountRule
		var active int
		if err := rows.Scan(&r.Key, &r.AccountCode, &r.AccountName, &active); err != nil {
			return nil, err
		}
		r.Active = active == 1
		out = append(out, r)
	}
	return out, rows.Err()
}

// RuleAccount 는 규칙 키가 가리키는 계정이다(없으면 false).
func (SQLLedger) RuleAccount(ctx context.Context, q contract.Tx, key string) (domain.Account, bool, error) {
	a, err := scanAccount(q.QueryRowContext(ctx, `SELECT a.id, a.code, a.name, a.kind, a.active, a.system FROM account_rules r JOIN accounts a ON a.id = r.account_id WHERE r.key = ?`, key))
	if errors.Is(err, sql.ErrNoRows) {
		return a, false, nil
	}
	return a, err == nil, err
}

// SetRule 은 규칙의 계정을 바꾼다. 없는 규칙 키는 ErrNotFound.
func (SQLLedger) SetRule(ctx context.Context, q contract.Tx, key string, accountID, by int64, now time.Time) error {
	res, err := q.ExecContext(ctx, `UPDATE account_rules SET account_id = ?, updated_by = ?, updated_at = ? WHERE key = ?`, accountID, by, fmtTime(now), key)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return contract.ErrNotFound
	}
	return nil
}

// ---- 전표 ----

// NextJournalNo 는 연도별 순번의 전표 번호(JV-2026-00001)를 부여한다. 취소된 전표 번호도 남으므로 결번이 없다.
func (SQLLedger) NextJournalNo(ctx context.Context, q contract.Tx, year int) (string, error) {
	eid := contract.WriteEntityID(ctx)
	n, err := NextEntityCounter(ctx, q, eid, "JV", year)
	if err != nil {
		return "", err
	}
	pre, err := EntityNoPrefix(ctx, q, eid)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%sJV-%d-%05d", pre, year, n), nil
}

// JournalInsert 는 전표 헤더 입력이다.
type JournalInsert struct {
	No, Kind, PostingDate, TradeDate, Memo string
	SourceType                             string
	SourceID                               int64
	ReversesID                             int64
	SiteID                                 int64 // 사업장(0 = 주사업장)
	Total, CreatedBy                       int64
	Now                                    time.Time
}

// LineInsert 는 줄 입력이다(AccountID 는 서비스가 계정 코드에서 찾아 넘긴다).
type LineInsert struct {
	AccountID int64
	Side      string
	Amount    int64
	VendorID  int64
	Memo      string
	// DepartmentID 는 부서 차원이다(B49, 0 = 없음).
	DepartmentID int64
}

func (SQLLedger) InsertJournal(ctx context.Context, q contract.Tx, in JournalInsert) (int64, error) {
	id, err := dbutil.InsertID(ctx, q, `INSERT INTO journals (entity_id, no, kind, status, posting_date, trade_date, memo, source_type, source_id, reverses_id, total_amount, created_by, created_at, site_id)
		VALUES (`+contract.SQLEntityID(ctx)+`, ?,?, 'draft', ?,?,?,?,?,?,?,?,?,?)`, in.No, in.Kind, in.PostingDate, in.TradeDate, in.Memo, in.SourceType, in.SourceID, nullID(in.ReversesID), in.Total, in.CreatedBy, fmtTime(in.Now), in.SiteID)
	if err != nil {
		return 0, mapErr(err)
	}
	if err := changes.Note(ctx, q, changes.ResJournal, id, changes.OpCreated, domain.JournalDraft); err != nil {
		return 0, err
	}
	return id, nil
}

// ReplaceLines 는 임시 전표의 줄을 통째로 바꾼다(트리거가 임시가 아니면 거부한다).
func (SQLLedger) ReplaceLines(ctx context.Context, q contract.Tx, journalID int64, lines []LineInsert) error {
	if _, err := q.ExecContext(ctx, `DELETE FROM journal_lines WHERE journal_id = ?`, journalID); err != nil {
		return mapErr(err)
	}
	for i, l := range lines {
		if _, err := q.ExecContext(ctx, `INSERT INTO journal_lines (journal_id, seq, account_id, side, amount, vendor_id, memo, department_id) VALUES (?,?,?,?,?,?,?,?)`,
			journalID, i+1, l.AccountID, l.Side, l.Amount, nullID(l.VendorID), l.Memo, nullID(l.DepartmentID)); err != nil {
			return mapErr(err)
		}
	}
	return changes.Note(ctx, q, changes.ResJournal, journalID, changes.OpUpdated, "")
}

// UpdateDraft 는 임시 전표의 헤더를 고친다.
func (SQLLedger) UpdateDraft(ctx context.Context, q contract.Tx, id int64, posting, trade, memo string, total, siteID int64) error {
	if _, err := q.ExecContext(ctx, `UPDATE journals SET posting_date = ?, trade_date = ?, memo = ?, total_amount = ?, site_id = ? WHERE `+contract.SQLEntity(ctx, "journals")+` AND id = ?`, posting, trade, memo, total, siteID, id); err != nil {
		return mapErr(err)
	}
	return changes.Note(ctx, q, changes.ResJournal, id, changes.OpUpdated, "")
}

// SiteUsable 은 사업장이 현재(쓰기) 법인의 활성 사업장인지 본다. 0(주사업장)은 늘 쓸 수 있다.
func (SQLLedger) SiteUsable(ctx context.Context, q contract.Tx, siteID int64) (bool, error) {
	if siteID == 0 {
		return true, nil
	}
	var entityID int64
	var active int
	err := q.QueryRowContext(ctx, `SELECT entity_id, active FROM entity_sites WHERE id = ?`, siteID).Scan(&entityID, &active)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return entityID == contract.WriteEntityID(ctx) && active == 1, nil
}

// SetDocID 는 승인 요청서 문서를 전표에 연결한다(임시 상태에서).
func (SQLLedger) SetDocID(ctx context.Context, q contract.Tx, id, docID int64) error {
	if _, err := q.ExecContext(ctx, `UPDATE journals SET doc_id = ? WHERE `+contract.SQLEntity(ctx, "journals")+` AND id = ?`, nullID(docID), id); err != nil {
		return mapErr(err)
	}
	return changes.Note(ctx, q, changes.ResJournal, id, changes.OpUpdated, "")
}

// SetStatus 는 상태를 바꾼다(허용 전이는 서비스 표와 트리거가 지킨다). 승인이면 승인자·시각, 취소면 취소자·시각·사유를 함께 쓴다.
func (SQLLedger) SetStatus(ctx context.Context, q contract.Tx, id int64, status string, by int64, now time.Time, reason string) error {
	var err error
	switch status {
	case domain.JournalApproved:
		_, err = q.ExecContext(ctx, `UPDATE journals SET status = ?, approved_by = ?, approved_at = ? WHERE `+contract.SQLEntity(ctx, "journals")+` AND id = ?`, status, by, fmtTime(now), id)
	case domain.JournalCanceled:
		_, err = q.ExecContext(ctx, `UPDATE journals SET status = ?, canceled_by = ?, canceled_at = ?, cancel_reason = ? WHERE `+contract.SQLEntity(ctx, "journals")+` AND id = ?`, status, by, fmtTime(now), reason, id)
	default:
		_, err = q.ExecContext(ctx, `UPDATE journals SET status = ? WHERE `+contract.SQLEntity(ctx, "journals")+` AND id = ?`, status, id)
	}
	if err != nil {
		return mapErr(err)
	}
	op := changes.OpStatusChanged
	if status == domain.JournalCanceled {
		op = changes.OpCanceled
	}
	return changes.Note(ctx, q, changes.ResJournal, id, op, status)
}

// AddEvidence 는 증빙을 연결한다(같은 증빙은 한 번만).
func (SQLLedger) AddEvidence(ctx context.Context, q contract.Tx, journalID int64, e domain.JournalEvidence, now time.Time) error {
	_, err := q.ExecContext(ctx, `INSERT INTO journal_evidence (journal_id, kind, ref_id, created_at) VALUES (?,?,?,?) ON CONFLICT DO NOTHING`, journalID, e.Kind, e.RefID, fmtTime(now))
	return mapErr(err)
}

const journalSelect = `SELECT j.id, j.no, j.kind, j.status, j.posting_date, j.trade_date, j.memo, j.source_type, j.source_id, COALESCE(rj.no,''), COALESCE(j.doc_id,0), COALESCE(p.no,''), j.total_amount,
	j.created_by, COALESCE(cu.name,''), j.created_at, COALESCE(au.name,''), j.approved_at, j.cancel_reason,
	COALESCE((SELECT x.no FROM journals x WHERE x.reverses_id = j.id AND x.status <> 'canceled' LIMIT 1),''),
	COALESCE((SELECT 1 FROM journals x WHERE x.reverses_id = j.id AND x.status = 'approved' LIMIT 1),0),
	j.site_id, COALESCE((SELECT s.name FROM entity_sites s WHERE s.id = j.site_id),''),
	COALESCE((SELECT e.name FROM journal_lines il JOIN vendors iv ON iv.id = il.vendor_id JOIN entities e ON e.id = iv.linked_entity_id WHERE il.journal_id = j.id AND iv.linked_entity_id > 0 AND iv.linked_entity_id <> j.entity_id ORDER BY il.seq LIMIT 1),'')
 FROM (SELECT * FROM journals WHERE {entity_id}) j LEFT JOIN journals rj ON rj.id = j.reverses_id LEFT JOIN purchase_requests p ON p.id = j.doc_id
 LEFT JOIN users cu ON cu.id = j.created_by LEFT JOIN users au ON au.id = j.approved_by`

func scanJournal(s rowScanner) (domain.Journal, error) {
	var j domain.Journal
	var created string
	var approved sql.NullString
	var corrected int
	if err := s.Scan(&j.ID, &j.No, &j.Kind, &j.Status, &j.PostingDate, &j.TradeDate, &j.Memo, &j.SourceType, &j.SourceID, &j.ReversesNo, &j.DocID, &j.DocNo, &j.TotalAmount,
		&j.CreatedByID, &j.CreatedBy, &created, &j.ApprovedBy, &approved, &j.CancelReason, &j.ReversedByNo, &corrected, &j.SiteID, &j.SiteName, &j.Intercompany); err != nil {
		return j, err
	}
	j.CreatedAt, j.Corrected = parseTime(created), corrected == 1
	if approved.Valid {
		t := parseTime(approved.String)
		j.ApprovedAt = &t
	}
	return j, nil
}

func (SQLLedger) fill(ctx context.Context, q contract.Tx, j *domain.Journal) error {
	rows, err := q.QueryContext(ctx, `SELECT l.seq, l.account_id, a.code, a.name, l.side, l.amount, COALESCE(l.vendor_id,0), COALESCE(v.name,''), l.memo, COALESCE(l.department_id, 0)
		FROM journal_lines l JOIN accounts a ON a.id = l.account_id LEFT JOIN vendors v ON v.id = l.vendor_id WHERE l.journal_id = ? ORDER BY l.seq`, j.ID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var l domain.JournalLine
		if err := rows.Scan(&l.Seq, &l.AccountID, &l.AccountCode, &l.AccountName, &l.Side, &l.Amount, &l.VendorID, &l.VendorName, &l.Memo, &l.DepartmentID); err != nil {
			rows.Close()
			return err
		}
		j.Lines = append(j.Lines, l)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	ev, err := q.QueryContext(ctx, `SELECT kind, ref_id FROM journal_evidence WHERE journal_id = ? ORDER BY id`, j.ID)
	if err != nil {
		return err
	}
	defer ev.Close()
	for ev.Next() {
		var e domain.JournalEvidence
		if err := ev.Scan(&e.Kind, &e.RefID); err != nil {
			return err
		}
		j.Evidence = append(j.Evidence, e)
	}
	return ev.Err()
}

// Journal 은 번호로 전표 한 건(줄·증빙 포함)을 읽는다. 없으면 ErrNotFound.
func (r SQLLedger) Journal(ctx context.Context, q contract.Tx, no string) (domain.Journal, error) {
	return r.one(ctx, q, contract.EntitySQL(ctx, journalSelect)+` WHERE j.no = ?`, no)
}

// JournalByID 는 번호(행 번호)로 전표를 읽는다.
func (r SQLLedger) JournalByID(ctx context.Context, q contract.Tx, id int64) (domain.Journal, error) {
	return r.one(ctx, q, contract.EntitySQL(ctx, journalSelect)+` WHERE j.id = ?`, id)
}

// JournalByDoc 은 승인 요청서 문서에 연결된 전표다(없으면 ErrNotFound).
func (r SQLLedger) JournalByDoc(ctx context.Context, q contract.Tx, docID int64) (domain.Journal, error) {
	return r.one(ctx, q, contract.EntitySQL(ctx, journalSelect)+` WHERE j.doc_id = ?`, docID)
}

func (r SQLLedger) one(ctx context.Context, q contract.Tx, query string, arg any) (domain.Journal, error) {
	j, err := scanJournal(q.QueryRowContext(ctx, query, arg))
	if errors.Is(err, sql.ErrNoRows) {
		return j, contract.ErrNotFound
	}
	if err != nil {
		return j, err
	}
	return j, r.fill(ctx, q, &j)
}

// ListJournals 는 조건에 맞는 전표를 최근 번호 순으로 돌려준다(줄은 채우지 않는다). total 은 전체 건수다.
func (SQLLedger) ListJournals(ctx context.Context, q contract.Tx, f domain.JournalFilter) ([]domain.Journal, int, error) {
	var where []string
	var args []any
	if f.Status != "" {
		where = append(where, "j.status = ?")
		args = append(args, f.Status)
	}
	if f.Kind != "" {
		where = append(where, "j.kind = ?")
		args = append(args, f.Kind)
	}
	if f.From != "" {
		where = append(where, "j.posting_date >= ?")
		args = append(args, f.From)
	}
	if f.To != "" {
		where = append(where, "j.posting_date <= ?")
		args = append(args, f.To)
	}
	if f.AccountCode != "" {
		where = append(where, "EXISTS (SELECT 1 FROM journal_lines l JOIN accounts a ON a.id = l.account_id WHERE l.journal_id = j.id AND a.code = ?)")
		args = append(args, f.AccountCode)
	}
	if f.VendorID != 0 {
		where = append(where, "EXISTS (SELECT 1 FROM journal_lines l WHERE l.journal_id = j.id AND l.vendor_id = ?)")
		args = append(args, f.VendorID)
	}
	cond := ""
	if len(where) > 0 {
		cond = " WHERE " + strings.Join(where, " AND ")
	}
	var total int
	if err := q.QueryRowContext(ctx, contract.EntitySQL(ctx, `SELECT COUNT(*) FROM (SELECT * FROM journals WHERE {entity_id}) j`)+cond, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	limit := f.Limit
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := q.QueryContext(ctx, contract.EntitySQL(ctx, journalSelect)+cond+` ORDER BY j.id DESC LIMIT ? OFFSET ?`, append(args, limit, f.Offset)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []domain.Journal{}
	for rows.Next() {
		j, err := scanJournal(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, j)
	}
	return out, total, rows.Err()
}

// ---- 매입 채무 ----

// PayableLine 은 채무 항목이 될 줄(외상매입금 대변 줄)이다.
type PayableLine struct {
	LineID, VendorID, Amount int64
}

// PayableLines 는 전표의 외상매입금(2110) 대변 줄이다.
func (SQLLedger) PayableLines(ctx context.Context, q contract.Tx, journalID int64) ([]PayableLine, error) {
	rows, err := q.QueryContext(ctx, `SELECT l.id, COALESCE(l.vendor_id,0), l.amount FROM journal_lines l JOIN accounts a ON a.id = l.account_id WHERE l.journal_id = ? AND a.code = '2110' AND l.side = 'C' ORDER BY l.seq`, journalID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PayableLine
	for rows.Next() {
		var p PayableLine
		if err := rows.Scan(&p.LineID, &p.VendorID, &p.Amount); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// InsertPayable 은 채무 항목을 만든다.
func (SQLLedger) InsertPayable(ctx context.Context, q contract.Tx, p PayableLine, journalID int64, trade, due string) error {
	id, err := dbutil.InsertID(ctx, q, `INSERT INTO payables (entity_id, vendor_id, journal_id, line_id, trade_date, due_date, amount) VALUES (`+contract.SQLEntityID(ctx)+`, ?,?,?,?,?,?)`, p.VendorID, journalID, p.LineID, trade, due, p.Amount)
	if err != nil {
		return mapErr(err)
	}
	return changes.Note(ctx, q, changes.ResPayable, id, changes.OpCreated, "")
}

// PayableUse 는 전표의 채무 항목에 지급·예약이 시작됐는지 보는 값이다(역분개 거부 판정).
func (SQLLedger) PayableUse(ctx context.Context, q contract.Tx, journalID int64) (paid, reserved int64, n int, err error) {
	err = q.QueryRowContext(ctx, `SELECT COALESCE(SUM(paid_amount),0), COALESCE(SUM(reserved_amount),0), COUNT(*) FROM payables WHERE `+contract.SQLEntity(ctx, "payables")+` AND journal_id = ? AND canceled = 0`, journalID).Scan(&paid, &reserved, &n)
	return
}

// CancelPayables 는 전표의 채무 항목을 취소한다(역분개 승인). 취소한 건수를 돌려준다.
func (SQLLedger) CancelPayables(ctx context.Context, q contract.Tx, journalID int64) (int, error) {
	ids, err := payableIDs(ctx, q, journalID)
	if err != nil {
		return 0, err
	}
	res, err := q.ExecContext(ctx, `UPDATE payables SET canceled = 1 WHERE `+contract.SQLEntity(ctx, "payables")+` AND journal_id = ? AND canceled = 0`, journalID)
	if err != nil {
		return 0, mapErr(err)
	}
	for _, id := range ids {
		if err := changes.Note(ctx, q, changes.ResPayable, id, changes.OpCanceled, ""); err != nil {
			return 0, err
		}
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// VendorPaymentDays 는 거래처 결제 조건 일수다. 비어 있으면 설정 기본값(기본 30)이다.
func (SQLLedger) VendorPaymentDays(ctx context.Context, q contract.Tx, vendorID int64) (int, error) {
	var d sql.NullInt64
	if err := q.QueryRowContext(ctx, `SELECT payment_days FROM vendors WHERE id = ?`, vendorID).Scan(&d); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	if d.Valid {
		return int(d.Int64), nil
	}
	var v string
	if err := q.QueryRowContext(ctx, `SELECT value FROM app_settings WHERE key = 'ledger.default_payment_days'`).Scan(&v); err != nil {
		return 30, nil
	}
	n := 0
	fmt.Sscanf(v, "%d", &n)
	if n <= 0 {
		n = 30
	}
	return n, nil
}

// VendorExists 는 거래처 번호가 있는지다.
func (SQLLedger) VendorExists(ctx context.Context, q contract.Tx, id int64) (bool, error) {
	var n int
	err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM vendors WHERE id = ?`, id).Scan(&n)
	return n > 0, err
}

// ---- 월 마감 점검(B27 확장) ----

// RequestedJournals 는 전기일이 그 달이고 승인 요청 중인 전표(차단 항목)다. DocID 는 승인 요청서 문서 번호다.
func (SQLLedger) RequestedJournals(ctx context.Context, q contract.Tx, month string) ([]PrecheckDoc, error) {
	return queryDocs(ctx, q, `SELECT COALESCE(j.doc_id,0), COALESCE(p.no,''), j.no, j.status FROM journals j LEFT JOIN purchase_requests p ON p.id = j.doc_id WHERE `+contract.SQLEntity(ctx, "j")+` AND j.status = 'requested' AND substr(j.posting_date,1,7) = ? ORDER BY j.id`, month)
}

// PendingQueueCount 는 그 달(KST)에 올라와 아직 처리하지 않은 전표 대기 건수다(마감 점검 경고용).
func (SQLLedger) PendingQueueCount(ctx context.Context, q contract.Tx, month string) (int, error) {
	var n int
	err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM journal_queue WHERE status = 'pending' AND substr(date(created_at, '+9 hours'),1,7) = ?`, month).Scan(&n)
	return n, err
}

// DraftJournals 는 전기일이 그 달이고 임시인 전표(경고 항목)다.
func (SQLLedger) DraftJournals(ctx context.Context, q contract.Tx, month string) ([]PrecheckDoc, error) {
	return queryDocs(ctx, q, `SELECT 0, '', j.no, j.status FROM journals j WHERE `+contract.SQLEntity(ctx, "j")+` AND j.status = 'draft' AND substr(j.posting_date,1,7) = ? ORDER BY j.id`, month)
}

// payableIDs 는 전표의 취소되지 않은 채무 항목 번호다(취소 전에 읽어 변경 피드에 남긴다).
func payableIDs(ctx context.Context, q contract.Tx, journalID int64) ([]int64, error) {
	rows, err := q.QueryContext(ctx, `SELECT id FROM payables WHERE `+contract.SQLEntity(ctx, "payables")+` AND journal_id = ? AND canceled = 0 ORDER BY id`, journalID)
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
