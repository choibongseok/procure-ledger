package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/choibongseok/procure-ledger/internal/changes"
	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/dbutil"
)

// 지급 요청·지급·자금 계좌·입출금(B32 단계 1). 채무 항목의 paid_amount·reserved_amount 를 바꾸는 SQL 은 이 파일의
// ReservePayable·ReleasePayable·AddPaid·RestorePaid 네 함수에만 있다(소스 검사 AC-1203). 서비스는 지정 함수(reservePayable·releasePayable·settlePayment)에서만 부른다.

type SQLPayments struct{}

// NextPaymentNo 는 연도별 순번의 지급 요청 번호(PAY-2026-0001)를 부여한다. 취소된 번호도 남는다.
func (SQLPayments) NextPaymentNo(ctx context.Context, q contract.Tx, year int) (string, error) {
	// 한 문장으로 올리고 읽는다: 행 잠금이 이 트랜잭션 끝까지 같은 법인·종류·연도의 채번을 줄 세운다(결번 없는 번호, PG 에서도 같다). 기본 법인이 아니면 번호 앞에 법인 코드가 붙는다.
	eid := contract.WriteEntityID(ctx)
	n, err := NextEntityCounter(ctx, q, eid, "PAY", year)
	if err != nil {
		return "", err
	}
	pre, err := EntityNoPrefix(ctx, q, eid)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%sPAY-%d-%04d", pre, year, n), nil
}

// ---- 채무 항목 예약·누계(이 네 함수가 유일한 쓰기 경로) ----

// ReservePayable 은 지급 가능액 안에서만 예약을 올린다. 넘으면 ErrValidation.
func (SQLPayments) ReservePayable(ctx context.Context, q contract.Tx, payableID, amount int64) error {
	res, err := q.ExecContext(ctx, `UPDATE payables SET reserved_amount = reserved_amount + ? WHERE `+contract.SQLEntity(ctx, "payables")+` AND id = ? AND canceled = 0 AND amount - paid_amount - reserved_amount >= ?`, amount, payableID, amount)
	if err != nil {
		return mapErr(err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return contract.ErrValidation
	}
	return changes.Note(ctx, q, changes.ResPayable, payableID, changes.OpUpdated, "")
}

// ReleasePayable 은 예약을 푼다(예약보다 많이 풀 수 없다).
func (SQLPayments) ReleasePayable(ctx context.Context, q contract.Tx, payableID, amount int64) error {
	res, err := q.ExecContext(ctx, `UPDATE payables SET reserved_amount = reserved_amount - ? WHERE `+contract.SQLEntity(ctx, "payables")+` AND id = ? AND reserved_amount >= ?`, amount, payableID, amount)
	if err != nil {
		return mapErr(err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return fmt.Errorf("%w: 풀 수 있는 예약이 부족합니다", contract.ErrInvalidState)
	}
	return changes.Note(ctx, q, changes.ResPayable, payableID, changes.OpUpdated, "")
}

// AddPaid 는 지급 처리에서 예약을 지급 누계로 옮긴다(reserved −, paid +).
func (SQLPayments) AddPaid(ctx context.Context, q contract.Tx, payableID, amount int64) error {
	res, err := q.ExecContext(ctx, `UPDATE payables SET paid_amount = paid_amount + ?, reserved_amount = reserved_amount - ? WHERE `+contract.SQLEntity(ctx, "payables")+` AND id = ? AND canceled = 0 AND reserved_amount >= ?`, amount, amount, payableID, amount)
	if err != nil {
		return mapErr(err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return fmt.Errorf("%w: 지급 대상 채무가 취소되었거나 예약이 부족합니다", contract.ErrInvalidState)
	}
	return changes.Note(ctx, q, changes.ResPayable, payableID, changes.OpUpdated, "")
}

// RestorePaid 는 지급 전표 역분개 승인에서 지급 누계를 되돌린다(paid −).
func (SQLPayments) RestorePaid(ctx context.Context, q contract.Tx, payableID, amount int64) error {
	res, err := q.ExecContext(ctx, `UPDATE payables SET paid_amount = paid_amount - ? WHERE `+contract.SQLEntity(ctx, "payables")+` AND id = ? AND paid_amount >= ?`, amount, payableID, amount)
	if err != nil {
		return mapErr(err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return fmt.Errorf("%w: 되돌릴 지급 누계가 부족합니다", contract.ErrInvalidState)
	}
	return changes.Note(ctx, q, changes.ResPayable, payableID, changes.OpUpdated, "")
}

// ---- 후보 ----

// Candidates 는 거래처의 지급 가능한 채무 항목이다(취소되지 않고 지급 가능액 > 0, 만기 순).
func (SQLPayments) Candidates(ctx context.Context, q contract.Tx, vendorID int64, asOf string) ([]domain.PayableCandidate, error) {
	rows, err := q.QueryContext(ctx, `SELECT p.id, j.no, p.trade_date, p.due_date, p.amount, p.paid_amount, p.reserved_amount,
		(SELECT COUNT(*) FROM payment_request_items i JOIN payment_requests r ON r.id = i.payment_request_id WHERE `+contract.SQLEntity(ctx, "r")+` AND i.payable_id = p.id AND r.status IN ('requested','approved'))
		FROM payables p JOIN journals j ON j.id = p.journal_id WHERE `+contract.SQLEntity(ctx, "p")+` AND p.vendor_id = ? AND p.canceled = 0 AND p.amount - p.paid_amount - p.reserved_amount > 0 ORDER BY p.due_date, p.id`, vendorID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.PayableCandidate
	base, _ := time.Parse("2006-01-02", asOf)
	for rows.Next() {
		var c domain.PayableCandidate
		if err := rows.Scan(&c.PayableID, &c.JournalNo, &c.TradeDate, &c.DueDate, &c.Amount, &c.Paid, &c.Reserved, &c.OpenRequests); err != nil {
			return nil, err
		}
		c.Available = c.Amount - c.Paid - c.Reserved
		if due, err := time.Parse("2006-01-02", c.DueDate); err == nil && base.After(due) {
			c.OverdueDays = int(base.Sub(due).Hours() / 24)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// PayableVendor 는 채무 항목 한 건의 거래처·지급 가능액이다(요청 검증용).
type PayableInfo struct {
	VendorID  int64
	Canceled  bool
	Available int64
	JournalNo string
	TradeDate string
	DueDate   string
}

func (SQLPayments) PayableInfo(ctx context.Context, q contract.Tx, id int64) (PayableInfo, error) {
	var p PayableInfo
	var canceled int
	err := q.QueryRowContext(ctx, `SELECT p.vendor_id, p.canceled, p.amount - p.paid_amount - p.reserved_amount, j.no, p.trade_date, p.due_date FROM payables p JOIN journals j ON j.id = p.journal_id WHERE `+contract.SQLEntity(ctx, "p")+` AND p.id = ?`, id).
		Scan(&p.VendorID, &canceled, &p.Available, &p.JournalNo, &p.TradeDate, &p.DueDate)
	if errors.Is(err, sql.ErrNoRows) {
		return p, contract.ErrNotFound
	}
	p.Canceled = canceled == 1
	return p, err
}

// ---- 지급 요청 ----

// PaymentRow 는 지급 요청의 저장 형태다(원문 계좌 포함: 서비스가 응답에서 가린다).
type PaymentRow struct {
	ID            int64
	No            string
	VendorID      int64
	VendorName    string
	VendorAccount string // 거래처의 현재 계좌(원문, 변경 감지용)
	Status        string
	Total         int64
	DueDate       string
	BankAccountID int64
	BankAlias     string
	// PayeeEnc 는 메모리에서는 풀린 평문이고 DB 에는 암호문(enc:v1:)으로 저장된다(SealAccount/OpenAccount). 응답에는 PayeeMasked 만 싣는다.
	PayeeMasked, PayeeEnc       string
	Memo                        string
	DocID                       int64
	DocNo                       string
	CreatedByID                 int64
	CreatedBy                   string
	CreatedAt                   time.Time
	ApprovedAt                  *time.Time
	ApprovedByID                int64
	CancelReason, ReverseReason string
	PaidDate, TransferRef       string
	PaidBy                      string
	JournalID                   int64
	JournalNo                   string
}

const paymentSelect = `SELECT r.id, r.no, r.vendor_id, v.name, v.account_no, r.status, r.total, r.due_date, r.bank_account_id, b.alias, r.payee_account_masked, r.payee_account_enc, r.memo,
	COALESCE(r.doc_id,0), COALESCE(d.no,''), r.created_by, COALESCE(cu.name,''), r.created_at, r.approved_at, COALESCE(r.approved_by,0), r.cancel_reason, r.reverse_reason,
	COALESCE(pm.paid_date,''), COALESCE(pm.transfer_ref,''), COALESCE(pu.name,''), COALESCE(pm.journal_id,0), COALESCE(jn.no,'')
	FROM (SELECT * FROM payment_requests WHERE {entity_id}) r JOIN vendors v ON v.id = r.vendor_id JOIN bank_accounts b ON b.id = r.bank_account_id
	LEFT JOIN purchase_requests d ON d.id = r.doc_id LEFT JOIN users cu ON cu.id = r.created_by
	LEFT JOIN payments pm ON pm.payment_request_id = r.id LEFT JOIN users pu ON pu.id = pm.paid_by LEFT JOIN journals jn ON jn.id = pm.journal_id`

func scanPayment(s rowScanner) (PaymentRow, error) {
	var r PaymentRow
	var created string
	var approved sql.NullString
	if err := s.Scan(&r.ID, &r.No, &r.VendorID, &r.VendorName, &r.VendorAccount, &r.Status, &r.Total, &r.DueDate, &r.BankAccountID, &r.BankAlias, &r.PayeeMasked, &r.PayeeEnc, &r.Memo,
		&r.DocID, &r.DocNo, &r.CreatedByID, &r.CreatedBy, &created, &approved, &r.ApprovedByID, &r.CancelReason, &r.ReverseReason,
		&r.PaidDate, &r.TransferRef, &r.PaidBy, &r.JournalID, &r.JournalNo); err != nil {
		return r, err
	}
	r.CreatedAt = parseTime(created)
	r.VendorAccount, r.PayeeEnc = OpenAccount(r.VendorAccount), OpenAccount(r.PayeeEnc) // 저장된 암호문을 평문으로 푼다(응답에서는 서비스가 가린다)
	if approved.Valid {
		t := parseTime(approved.String)
		r.ApprovedAt = &t
	}
	return r, nil
}

func (SQLPayments) one(ctx context.Context, q contract.Tx, where string, arg any) (PaymentRow, error) {
	r, err := scanPayment(q.QueryRowContext(ctx, contract.EntitySQL(ctx, paymentSelect)+` WHERE `+where, arg))
	if errors.Is(err, sql.ErrNoRows) {
		return r, contract.ErrNotFound
	}
	return r, err
}

func (p SQLPayments) ByNo(ctx context.Context, q contract.Tx, no string) (PaymentRow, error) {
	return p.one(ctx, q, `r.no = ?`, no)
}
func (p SQLPayments) ByID(ctx context.Context, q contract.Tx, id int64) (PaymentRow, error) {
	return p.one(ctx, q, `r.id = ?`, id)
}
func (p SQLPayments) ByDoc(ctx context.Context, q contract.Tx, docID int64) (PaymentRow, error) {
	return p.one(ctx, q, `r.doc_id = ?`, docID)
}

// PaymentItems 는 지급 요청의 줄이다.
func (SQLPayments) PaymentItems(ctx context.Context, q contract.Tx, id int64) ([]domain.PaymentItem, []int64, error) {
	rows, err := q.QueryContext(ctx, `SELECT i.payable_id, j.no, p.trade_date, p.due_date, i.amount FROM payment_request_items i JOIN payables p ON p.id = i.payable_id JOIN journals j ON j.id = p.journal_id WHERE `+contract.SQLEntity(ctx, "p")+` AND i.payment_request_id = ? ORDER BY i.id`, id)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var out []domain.PaymentItem
	var ids []int64
	for rows.Next() {
		var it domain.PaymentItem
		if err := rows.Scan(&it.PayableID, &it.JournalNo, &it.TradeDate, &it.DueDate, &it.Amount); err != nil {
			return nil, nil, err
		}
		out = append(out, it)
		ids = append(ids, it.PayableID)
	}
	return out, ids, rows.Err()
}

// PaymentInsert 는 지급 요청 헤더 입력이다.
type PaymentInsert struct {
	No                    string
	VendorID              int64
	Total                 int64
	DueDate               string
	BankAccountID         int64
	PayeeMasked, PayeeEnc string
	Memo                  string
	CreatedBy             int64
	Now                   time.Time
}

func (SQLPayments) Insert(ctx context.Context, q contract.Tx, in PaymentInsert) (int64, error) {
	sealed, err := SealAccount(in.PayeeEnc)
	if err != nil {
		return 0, err
	}
	in.PayeeEnc = sealed
	id, err := dbutil.InsertID(ctx, q, `INSERT INTO payment_requests (entity_id, no, vendor_id, status, total, due_date, bank_account_id, payee_account_masked, payee_account_enc, memo, created_by, created_at)
		VALUES (`+contract.SQLEntityID(ctx)+`, ?,?, 'draft', ?,?,?,?,?,?,?,?)`, in.No, in.VendorID, in.Total, in.DueDate, in.BankAccountID, in.PayeeMasked, in.PayeeEnc, in.Memo, in.CreatedBy, fmtTime(in.Now))
	if err != nil {
		return 0, mapErr(err)
	}
	return id, nil
}

// UpdateDraft 는 임시 요청의 헤더를 고친다(거래처는 바꾸지 않는다).
func (SQLPayments) UpdateDraft(ctx context.Context, q contract.Tx, id int64, in PaymentInsert) error {
	sealed, err := SealAccount(in.PayeeEnc)
	if err != nil {
		return err
	}
	in.PayeeEnc = sealed
	_, err = q.ExecContext(ctx, `UPDATE payment_requests SET total = ?, due_date = ?, bank_account_id = ?, payee_account_masked = ?, payee_account_enc = ?, memo = ? WHERE `+contract.SQLEntity(ctx, "payment_requests")+` AND id = ?`,
		in.Total, in.DueDate, in.BankAccountID, in.PayeeMasked, in.PayeeEnc, in.Memo, id)
	return mapErr(err)
}

// ReplaceItems 는 임시 요청의 줄을 통째로 바꾼다(트리거가 임시가 아니면 거부한다).
func (SQLPayments) ReplaceItems(ctx context.Context, q contract.Tx, id int64, items []domain.PaymentItemInput) error {
	if _, err := q.ExecContext(ctx, `DELETE FROM payment_request_items WHERE payment_request_id = ?`, id); err != nil {
		return mapErr(err)
	}
	for _, it := range items {
		if _, err := q.ExecContext(ctx, `INSERT INTO payment_request_items (payment_request_id, payable_id, amount) VALUES (?,?,?)`, id, it.PayableID, it.Amount); err != nil {
			return mapErr(err)
		}
	}
	return nil
}

func (SQLPayments) SetDoc(ctx context.Context, q contract.Tx, id, docID int64) error {
	_, err := q.ExecContext(ctx, `UPDATE payment_requests SET doc_id = ? WHERE `+contract.SQLEntity(ctx, "payment_requests")+` AND id = ?`, nullID(docID), id)
	return mapErr(err)
}

// SetStatus 는 상태를 바꾼다(허용 전이는 서비스 표와 트리거가 지킨다). 승인이면 승인자·시각, 취소·지급 취소면 사유를 함께 쓴다.
func (SQLPayments) SetStatus(ctx context.Context, q contract.Tx, id int64, status string, by int64, now time.Time, reason string) error {
	var err error
	switch status {
	case domain.PaymentApproved:
		_, err = q.ExecContext(ctx, `UPDATE payment_requests SET status = ?, approved_by = ?, approved_at = ? WHERE `+contract.SQLEntity(ctx, "payment_requests")+` AND id = ?`, status, by, fmtTime(now), id)
	case domain.PaymentCanceled:
		_, err = q.ExecContext(ctx, `UPDATE payment_requests SET status = ?, cancel_reason = ? WHERE `+contract.SQLEntity(ctx, "payment_requests")+` AND id = ?`, status, reason, id)
	case domain.PaymentPaidCanceled:
		_, err = q.ExecContext(ctx, `UPDATE payment_requests SET status = ?, reverse_reason = ? WHERE `+contract.SQLEntity(ctx, "payment_requests")+` AND id = ?`, status, reason, id)
	default:
		_, err = q.ExecContext(ctx, `UPDATE payment_requests SET status = ? WHERE `+contract.SQLEntity(ctx, "payment_requests")+` AND id = ?`, status, id)
	}
	return mapErr(err)
}

// SetReverseReason 은 지급 취소 사유를 지급 요청에 남긴다(역분개 승인 전, 상태는 그대로).
func (SQLPayments) SetReverseReason(ctx context.Context, q contract.Tx, id int64, reason string) error {
	_, err := q.ExecContext(ctx, `UPDATE payment_requests SET reverse_reason = ? WHERE `+contract.SQLEntity(ctx, "payment_requests")+` AND id = ?`, reason, id)
	return mapErr(err)
}

// List 는 지급 요청 목록이다(최근 것이 먼저).
func (SQLPayments) List(ctx context.Context, q contract.Tx, f domain.PaymentFilter) ([]PaymentRow, int, error) {
	where, args := "1 = 1", []any{}
	if f.Status != "" {
		where += " AND r.status = ?"
		args = append(args, f.Status)
	}
	if f.VendorID != 0 {
		where += " AND r.vendor_id = ?"
		args = append(args, f.VendorID)
	}
	var total int
	if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM payment_requests r WHERE `+contract.SQLEntity(ctx, "r")+` AND `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	limit := f.Limit
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := q.QueryContext(ctx, contract.EntitySQL(ctx, paymentSelect)+` WHERE `+where+` ORDER BY r.id DESC LIMIT ? OFFSET ?`, append(args, limit, f.Offset)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []PaymentRow
	for rows.Next() {
		r, err := scanPayment(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, r)
	}
	return out, total, rows.Err()
}

// Similar 는 같은 거래처·같은 합계·예정일이 7일 안인 진행 중·완료 지급 건 번호다(중복 의심 경고, 자기 자신 제외).
func (SQLPayments) Similar(ctx context.Context, q contract.Tx, vendorID, total int64, due string, exceptID int64) ([]string, error) {
	// 예정일이 ±7일 안: 날짜 계산은 Go 에서 하고 SQL 은 문자열 비교만 한다(두 방언이 같게 돈다).
	lo, hi := due, due
	if t, perr := time.Parse("2006-01-02", due); perr == nil {
		lo, hi = t.AddDate(0, 0, -7).Format("2006-01-02"), t.AddDate(0, 0, 7).Format("2006-01-02")
	}
	rows, err := q.QueryContext(ctx, `SELECT no FROM payment_requests WHERE `+contract.SQLEntity(ctx, "payment_requests")+` AND vendor_id = ? AND total = ? AND id <> ? AND status IN ('requested','approved','paid')
		AND due_date >= ? AND due_date <= ? ORDER BY id`, vendorID, total, exceptID, lo, hi)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// ---- 지급 행·자금 행 ----

// InsertPayment 는 지급 행을 쓴다(UNIQUE 위반은 이미 지급 처리된 건 또는 참조번호 중복).
func (SQLPayments) InsertPayment(ctx context.Context, q contract.Tx, requestID int64, paidDate string, bankID int64, ref string, by int64, now time.Time, journalID int64, changed bool, memo string) (int64, error) {
	var refArg any
	if ref != "" {
		refArg = ref
	}
	id, err := dbutil.InsertID(ctx, q, `INSERT INTO payments (payment_request_id, paid_date, bank_account_id, transfer_ref, paid_by, paid_at, journal_id, account_changed, memo) VALUES (?,?,?,?,?,?,?,?,?)`,
		requestID, paidDate, bankID, refArg, by, fmtTime(now), journalID, boolInt(changed), memo)
	if err != nil {
		return 0, err
	}
	return id, nil
}

// RefOwner 는 이체 참조번호를 이미 쓴 지급 요청 번호다(없으면 "").
func (SQLPayments) RefOwner(ctx context.Context, q contract.Tx, bankID int64, ref string) (string, error) {
	var no string
	err := q.QueryRowContext(ctx, `SELECT r.no FROM payments p JOIN payment_requests r ON r.id = p.payment_request_id WHERE `+contract.SQLEntity(ctx, "r")+` AND p.bank_account_id = ? AND p.transfer_ref = ?`, bankID, ref).Scan(&no)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return no, err
}

// PaymentIDByRequest 는 지급 요청의 지급 행 번호다(없으면 0).
func (SQLPayments) PaymentIDByRequest(ctx context.Context, q contract.Tx, requestID int64) (int64, error) {
	var id int64
	err := q.QueryRowContext(ctx, `SELECT id FROM payments WHERE payment_request_id = ?`, requestID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return id, err
}

// MovementInsert 는 입출금 기록 입력이다(금액은 입금 +, 출금 −).
type MovementInsert struct {
	BankAccountID, Amount      int64
	Date, Kind, Memo           string
	PaymentID, JournalID       int64
	CounterAccountID, VendorID int64
	CreatedBy                  int64
	Now                        time.Time
}

func (SQLPayments) InsertMovement(ctx context.Context, q contract.Tx, m MovementInsert) error {
	_, err := q.ExecContext(ctx, `INSERT INTO fund_movements (bank_account_id, move_date, kind, amount, payment_id, journal_id, counterpart_account_id, vendor_id, memo, created_by, created_at) VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		m.BankAccountID, m.Date, m.Kind, m.Amount, nullID(m.PaymentID), nullID(m.JournalID), nullID(m.CounterAccountID), nullID(m.VendorID), m.Memo, m.CreatedBy, fmtTime(m.Now))
	return mapErr(err)
}

// ---- 승인된 전표를 바로 만드는 유일한 경로(지급 전표, D6) ----

// ApproveInsertedJournal 은 임시로 넣은 지급 전표를 승인 상태로 바꾼다(DB 트리거는 auto_payment 의 임시 → 승인만 허용한다).
// 호출은 지급 처리 함수 한 곳뿐이다(소스 검사 AC-1105).
func (SQLPayments) ApproveInsertedJournal(ctx context.Context, q contract.Tx, journalID, docID, approver int64, now time.Time) error {
	_, err := q.ExecContext(ctx, `UPDATE journals SET status = 'approved', doc_id = ?, approved_by = ?, approved_at = ? WHERE `+contract.SQLEntity(ctx, "journals")+` AND id = ? AND kind = 'auto_payment' AND status = 'draft'`, nullID(docID), approver, fmtTime(now), journalID)
	if err != nil {
		return mapErr(err)
	}
	return changes.Note(ctx, q, changes.ResJournal, journalID, changes.OpStatusChanged, domain.JournalApproved)
}

// ---- 자금 계좌 ----

type BankRow struct {
	domain.BankAccount
	Enc string // 원문 계좌번호
}

const bankSelect = `SELECT b.id, b.alias, b.bank_name, b.account_no, b.account_no_enc, a.code, a.name, b.opening_balance, b.opening_date, b.active FROM (SELECT * FROM bank_accounts WHERE {entity_id}) b JOIN accounts a ON a.id = b.account_id`

func scanBank(s rowScanner) (BankRow, error) {
	var b BankRow
	var active int
	var legacy, sealed string
	if err := s.Scan(&b.ID, &b.Alias, &b.BankName, &legacy, &sealed, &b.LedgerCode, &b.LedgerName, &b.OpeningBalance, &b.OpeningDate, &active); err != nil {
		return b, err
	}
	b.Active = active == 1
	// 새 행은 account_no_enc 가 암호문이고 account_no 는 마스킹 값이다. 이관 전 행은 account_no 가 평문이라 그대로 읽는다.
	if sealed != "" {
		b.Enc = OpenAccount(sealed)
	} else {
		b.Enc = OpenAccount(legacy)
	}
	return b, nil
}

func (SQLPayments) Banks(ctx context.Context, q contract.Tx, includeInactive bool) ([]BankRow, error) {
	query := contract.EntitySQL(ctx, bankSelect)
	if !includeInactive {
		query += ` WHERE b.active = 1`
	}
	rows, err := q.QueryContext(ctx, query+` ORDER BY b.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []BankRow
	for rows.Next() {
		b, err := scanBank(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

func (SQLPayments) Bank(ctx context.Context, q contract.Tx, id int64) (BankRow, error) {
	b, err := scanBank(q.QueryRowContext(ctx, contract.EntitySQL(ctx, bankSelect)+` WHERE b.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return b, contract.ErrNotFound
	}
	return b, err
}

func (SQLPayments) InsertBank(ctx context.Context, q contract.Tx, in domain.BankAccountInput, accountID int64, now time.Time) (int64, error) {
	sealed, err := SealAccount(in.AccountNo)
	if err != nil {
		return 0, err
	}
	id, err := dbutil.InsertID(ctx, q, `INSERT INTO bank_accounts (entity_id, alias, bank_name, account_no, account_no_enc, account_id, opening_balance, opening_date, active, created_at) VALUES (`+contract.SQLEntityID(ctx)+`, ?,?,?,?,?,?,?,1,?)`,
		in.Alias, in.BankName, MaskAccount(in.AccountNo), sealed, accountID, in.OpeningBalance, in.OpeningDate, fmtTime(now))
	if err != nil {
		return 0, mapErr(err)
	}
	return id, nil
}

func (SQLPayments) UpdateBank(ctx context.Context, q contract.Tx, id int64, alias, bankName, accountNo string) error {
	if accountNo == "" {
		_, err := q.ExecContext(ctx, `UPDATE bank_accounts SET alias = ?, bank_name = ? WHERE `+contract.SQLEntity(ctx, "bank_accounts")+` AND id = ?`, alias, bankName, id)
		return mapErr(err)
	}
	sealed, err := SealAccount(accountNo)
	if err != nil {
		return err
	}
	_, err = q.ExecContext(ctx, `UPDATE bank_accounts SET alias = ?, bank_name = ?, account_no = ?, account_no_enc = ? WHERE `+contract.SQLEntity(ctx, "bank_accounts")+` AND id = ?`, alias, bankName, MaskAccount(accountNo), sealed, id)
	return mapErr(err)
}

func (SQLPayments) SetBankActive(ctx context.Context, q contract.Tx, id int64, active bool) error {
	_, err := q.ExecContext(ctx, `UPDATE bank_accounts SET active = ? WHERE `+contract.SQLEntity(ctx, "bank_accounts")+` AND id = ?`, boolInt(active), id)
	return mapErr(err)
}

// BankBalance 는 계좌의 기준일(포함) 잔액이다: 기초 + 승인된 전표에 묶인 입출금 합. 기초 기준일 이전 기록은 기초에 이미 들어 있다고 본다.
func (SQLPayments) BankBalance(ctx context.Context, q contract.Tx, bankID int64, asOf string) (int64, error) {
	var n int64
	err := q.QueryRowContext(ctx, `SELECT b.opening_balance + COALESCE((SELECT SUM(m.amount) FROM fund_movements m LEFT JOIN journals j ON j.id = m.journal_id
		WHERE m.bank_account_id = b.id AND m.move_date <= ? AND (m.journal_id IS NULL OR j.status = 'approved')),0) FROM bank_accounts b WHERE `+contract.SQLEntity(ctx, "b")+` AND b.id = ?`, asOf, bankID).Scan(&n)
	return n, err
}

// DailyRaw 는 자금 일보 원자료: 일자·계좌별 입금·출금 합과 승인 전 입금 합.
type DailyRaw struct {
	Date           string
	BankID         int64
	Deposit        int64
	Withdraw       int64
	PendingDeposit int64
}

func (SQLPayments) DailyRaws(ctx context.Context, q contract.Tx, from, to string, bankID int64) ([]DailyRaw, error) {
	query := `SELECT m.move_date, m.bank_account_id,
		COALESCE(SUM(CASE WHEN m.amount > 0 AND (m.journal_id IS NULL OR j.status = 'approved') THEN m.amount END),0),
		COALESCE(SUM(CASE WHEN m.amount < 0 AND (m.journal_id IS NULL OR j.status = 'approved') THEN -m.amount END),0),
		COALESCE(SUM(CASE WHEN m.amount > 0 AND j.status IN ('draft','requested') THEN m.amount END),0)
		FROM fund_movements m LEFT JOIN journals j ON j.id = m.journal_id WHERE m.bank_account_id IN (SELECT id FROM bank_accounts WHERE ` + contract.SQLEntity(ctx, "") + `) AND m.move_date >= ? AND m.move_date <= ?`
	args := []any{from, to}
	if bankID != 0 {
		query += ` AND m.bank_account_id = ?`
		args = append(args, bankID)
	}
	rows, err := q.QueryContext(ctx, query+` GROUP BY m.move_date, m.bank_account_id ORDER BY m.move_date, m.bank_account_id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DailyRaw
	for rows.Next() {
		var d DailyRaw
		if err := rows.Scan(&d.Date, &d.BankID, &d.Deposit, &d.Withdraw, &d.PendingDeposit); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// MovementsInRange 는 기간의 입출금 행이다(일자 칸의 상세).
func (SQLPayments) MovementsInRange(ctx context.Context, q contract.Tx, from, to string, bankID int64) ([]domain.FundMovementRow, error) {
	query := `SELECT m.move_date, b.alias, m.kind, m.amount, COALESCE(r.no,''), COALESCE(v.name,''), m.memo, COALESCE(j.no,''), CASE WHEN j.status IN ('draft','requested') THEN 1 ELSE 0 END
		FROM fund_movements m JOIN bank_accounts b ON b.id = m.bank_account_id LEFT JOIN payments p ON p.id = m.payment_id LEFT JOIN payment_requests r ON r.id = p.payment_request_id
		LEFT JOIN vendors v ON v.id = m.vendor_id LEFT JOIN journals j ON j.id = m.journal_id WHERE ` + contract.SQLEntity(ctx, "b") + ` AND m.move_date >= ? AND m.move_date <= ?`
	args := []any{from, to}
	if bankID != 0 {
		query += ` AND m.bank_account_id = ?`
		args = append(args, bankID)
	}
	rows, err := q.QueryContext(ctx, query+` ORDER BY m.move_date, m.id LIMIT 2000`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.FundMovementRow
	for rows.Next() {
		var m domain.FundMovementRow
		var pending int
		if err := rows.Scan(&m.Date, &m.BankAlias, &m.Kind, &m.Amount, &m.PaymentNo, &m.VendorName, &m.Memo, &m.JournalNo, &pending); err != nil {
			return nil, err
		}
		m.Pending = pending == 1
		out = append(out, m)
	}
	return out, rows.Err()
}

// OpeningBefore 는 기간 시작 전(< from) 전체(또는 한 계좌)의 누계다: 기초 잔액 + 승인된 입출금. 기초 기준일이 시작 뒤인 계좌의 기초는 기간 중 그 날 더해진다(서비스가 처리).
func (SQLPayments) OpeningBefore(ctx context.Context, q contract.Tx, bankID int64, before string) (int64, error) {
	var n int64
	query := `SELECT COALESCE(SUM(b.opening_balance),0) + COALESCE((SELECT SUM(m.amount) FROM fund_movements m LEFT JOIN journals j ON j.id = m.journal_id WHERE m.bank_account_id IN (SELECT id FROM bank_accounts WHERE ` + contract.SQLEntity(ctx, "") + `) AND m.move_date < ? AND (m.journal_id IS NULL OR j.status = 'approved')`
	args := []any{before}
	if bankID != 0 {
		query += ` AND m.bank_account_id = ?`
		args = append(args, bankID)
	}
	query += `),0) FROM bank_accounts b WHERE ` + contract.SQLEntity(ctx, "b") + ` AND b.opening_date < ?`
	args = append(args, before)
	if bankID != 0 {
		query += ` AND b.id = ?`
		args = append(args, bankID)
	}
	err := q.QueryRowContext(ctx, query, args...).Scan(&n)
	return n, err
}

// OpeningInRange 는 기초 기준일이 기간 안(from ≤ 기준일 ≤ to)인 계좌의 기초 잔액 합이다(그 날 기초로 더해진다).
func (SQLPayments) OpeningInRange(ctx context.Context, q contract.Tx, bankID int64, from, to string) (map[string]int64, error) {
	query := `SELECT opening_date, SUM(opening_balance) FROM bank_accounts WHERE ` + contract.SQLEntity(ctx, "bank_accounts") + ` AND opening_date >= ? AND opening_date <= ?`
	args := []any{from, to}
	if bankID != 0 {
		query += ` AND id = ?`
		args = append(args, bankID)
	}
	rows, err := q.QueryContext(ctx, query+` GROUP BY opening_date`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var d string
		var n int64
		if err := rows.Scan(&d, &n); err != nil {
			return nil, err
		}
		out[d] = n
	}
	return out, rows.Err()
}

// ---- 일정표 ----

// CalendarPayments 는 기간의 지급 건(승인 요청·승인·지급 완료, 예정일 기준)이다.
func (SQLPayments) CalendarPayments(ctx context.Context, q contract.Tx, from, to string) ([]PaymentRow, error) {
	rows, err := q.QueryContext(ctx, contract.EntitySQL(ctx, paymentSelect)+` WHERE r.status IN ('requested','approved','paid') AND r.due_date >= ? AND r.due_date <= ? ORDER BY r.due_date, r.id`, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PaymentRow
	for rows.Next() {
		r, err := scanPayment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// DelayedApproved 는 예정일이 지났는데 아직 처리하지 않은 승인 건이다(지연 줄).
func (SQLPayments) DelayedApproved(ctx context.Context, q contract.Tx, today string) ([]PaymentRow, error) {
	rows, err := q.QueryContext(ctx, contract.EntitySQL(ctx, paymentSelect)+` WHERE r.status = 'approved' AND r.due_date < ? ORDER BY r.due_date, r.id`, today)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PaymentRow
	for rows.Next() {
		r, err := scanPayment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// DueCandidates 는 기간에 만기가 도래하는 미예약 채무 항목(거래처 이름 포함)이다. 기간 앞 만기 경과분은 Overdue 로 따로 센다.
type DueRow struct {
	Date       string
	VendorName string
	Amount     int64
}

func (SQLPayments) DuePayables(ctx context.Context, q contract.Tx, from, to string) ([]DueRow, error) {
	rows, err := q.QueryContext(ctx, `SELECT p.due_date, v.name, p.amount - p.paid_amount - p.reserved_amount FROM payables p JOIN vendors v ON v.id = p.vendor_id
		WHERE `+contract.SQLEntity(ctx, "p")+` AND p.canceled = 0 AND p.amount - p.paid_amount - p.reserved_amount > 0 AND p.due_date >= ? AND p.due_date <= ? ORDER BY p.due_date, v.name`, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DueRow
	for rows.Next() {
		var d DueRow
		if err := rows.Scan(&d.Date, &d.VendorName, &d.Amount); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (SQLPayments) OverduePayables(ctx context.Context, q contract.Tx, before string) (int64, error) {
	var n int64
	err := q.QueryRowContext(ctx, `SELECT COALESCE(SUM(amount - paid_amount - reserved_amount),0) FROM payables WHERE `+contract.SQLEntity(ctx, "payables")+` AND canceled = 0 AND due_date < ? AND amount - paid_amount - reserved_amount > 0`, before).Scan(&n)
	return n, err
}

// ---- 월 마감 점검 ----

// RequestedPayments 는 예정일이 그 달이고 승인 요청 중인 지급 건(차단)이다. DocID 는 PAY 문서 번호다.
func (SQLPayments) RequestedPayments(ctx context.Context, q contract.Tx, month string) ([]PrecheckDoc, error) {
	return queryDocs(ctx, q, `SELECT COALESCE(r.doc_id,0), COALESCE(d.no,''), r.no, r.status FROM payment_requests r LEFT JOIN purchase_requests d ON d.id = r.doc_id WHERE `+contract.SQLEntity(ctx, "r")+` AND r.status = 'requested' AND substr(r.due_date,1,7) = ? ORDER BY r.id`, month)
}

// ApprovedUnpaid 는 예정일이 그 달이고 승인됐지만 지급 처리 전인 지급 건(경고)이다.
func (SQLPayments) ApprovedUnpaid(ctx context.Context, q contract.Tx, month string) ([]PrecheckDoc, error) {
	return queryDocs(ctx, q, `SELECT 0, '', r.no, r.status FROM payment_requests r WHERE `+contract.SQLEntity(ctx, "r")+` AND r.status = 'approved' AND substr(r.due_date,1,7) = ? ORDER BY r.id`, month)
}
