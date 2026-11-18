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

// 법인카드 저장소(B32 단계 2). 카드번호는 끝 4자리만 다룬다. 카드 내역의 사용 일시·가맹점·금액은 DB 트리거가 올린 뒤 고치지 못하게 한다.

type SQLCards struct{}

// ---- 카드 ----

const cardSelect = `SELECT c.id, c.alias, c.issuer, c.last4, COALESCE(c.holder_id,0), COALESCE(u.name,''), c.active FROM (SELECT * FROM corporate_cards WHERE {entity_id}) c LEFT JOIN users u ON u.id = c.holder_id`

func scanCard(s rowScanner) (domain.Card, error) {
	var c domain.Card
	var active int
	if err := s.Scan(&c.ID, &c.Alias, &c.Issuer, &c.Last4, &c.HolderID, &c.HolderName, &active); err != nil {
		return c, err
	}
	c.Active = active == 1
	c.IssuerLabel = domain.CardFormatLabels[c.Issuer]
	return c, nil
}

func (SQLCards) Cards(ctx context.Context, q contract.Tx) ([]domain.Card, error) {
	rows, err := q.QueryContext(ctx, contract.EntitySQL(ctx, cardSelect)+` ORDER BY c.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Card
	for rows.Next() {
		c, err := scanCard(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (SQLCards) Card(ctx context.Context, q contract.Tx, id int64) (domain.Card, error) {
	c, err := scanCard(q.QueryRowContext(ctx, contract.EntitySQL(ctx, cardSelect)+` WHERE c.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return c, contract.ErrNotFound
	}
	return c, err
}

// CardByLast4 는 카드사 형식과 끝 4자리로 카드를 찾는다.
func (SQLCards) CardByLast4(ctx context.Context, q contract.Tx, issuer, last4 string) (domain.Card, bool, error) {
	c, err := scanCard(q.QueryRowContext(ctx, contract.EntitySQL(ctx, cardSelect)+` WHERE c.issuer = ? AND c.last4 = ?`, issuer, last4))
	if errors.Is(err, sql.ErrNoRows) {
		return c, false, nil
	}
	return c, err == nil, err
}

func (SQLCards) InsertCard(ctx context.Context, q contract.Tx, in domain.CardInput, now time.Time) (int64, error) {
	id, err := dbutil.InsertID(ctx, q, `INSERT INTO corporate_cards (entity_id, alias, issuer, last4, holder_id, active, created_at) VALUES (`+contract.SQLEntityID(ctx)+`, ?,?,?,?,1,?)`, in.Alias, in.Issuer, in.Last4, nullID(in.HolderID), fmtTime(now))
	if err != nil {
		return 0, mapErr(err)
	}
	return id, nil
}

func (SQLCards) UpdateCardAlias(ctx context.Context, q contract.Tx, id int64, alias string) error {
	_, err := q.ExecContext(ctx, `UPDATE corporate_cards SET alias = ? WHERE `+contract.SQLEntity(ctx, "corporate_cards")+` AND id = ?`, alias, id)
	return mapErr(err)
}

func (SQLCards) SetCardHolder(ctx context.Context, q contract.Tx, id, holderID int64) error {
	_, err := q.ExecContext(ctx, `UPDATE corporate_cards SET holder_id = ? WHERE `+contract.SQLEntity(ctx, "corporate_cards")+` AND id = ?`, nullID(holderID), id)
	return mapErr(err)
}

func (SQLCards) SetCardActive(ctx context.Context, q contract.Tx, id int64, active bool) error {
	_, err := q.ExecContext(ctx, `UPDATE corporate_cards SET active = ? WHERE `+contract.SQLEntity(ctx, "corporate_cards")+` AND id = ?`, boolInt(active), id)
	return mapErr(err)
}

func (SQLCards) AddHolderHistory(ctx context.Context, q contract.Tx, cardID, holderID, by int64, fromDate string, now time.Time) error {
	_, err := q.ExecContext(ctx, `INSERT INTO card_holder_history (card_id, holder_id, from_date, changed_by, created_at) VALUES (?,?,?,?,?)`, cardID, nullID(holderID), fromDate, nullID(by), fmtTime(now))
	return mapErr(err)
}

// HolderHistory 는 카드 소지자 변경 이력(최근 것이 먼저)이다.
func (SQLCards) HolderHistory(ctx context.Context, q contract.Tx, cardID int64) ([]domain.CardHolderChange, error) {
	rows, err := q.QueryContext(ctx, `SELECT COALESCE(u.name,''), h.from_date, COALESCE(c.name,''), h.created_at FROM card_holder_history h
		LEFT JOIN users u ON u.id = h.holder_id LEFT JOIN users c ON c.id = h.changed_by WHERE h.card_id = ? ORDER BY h.id DESC`, cardID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.CardHolderChange
	for rows.Next() {
		var h domain.CardHolderChange
		var at string
		if err := rows.Scan(&h.HolderName, &h.FromDate, &h.ChangedBy, &at); err != nil {
			return nil, err
		}
		h.ChangedAt = parseTime(at)
		out = append(out, h)
	}
	return out, rows.Err()
}

// UserActive 는 사용자가 있고 재직(users.active)인지다.
func (SQLCards) UserInfo(ctx context.Context, q contract.Tx, id int64) (name string, active bool, found bool, err error) {
	var a int
	err = q.QueryRowContext(ctx, `SELECT name, active FROM users WHERE id = ?`, id).Scan(&name, &a)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, false, nil
	}
	return name, a == 1, err == nil, err
}

// ---- 업로드 ----

// ExistingKeys 는 이미 들어와 있는 중복 키 집합이다.
func (SQLCards) ExistingKeys(ctx context.Context, q contract.Tx, keys []string) (map[string]bool, error) {
	out := map[string]bool{}
	for i := 0; i < len(keys); i += 500 {
		end := i + 500
		if end > len(keys) {
			end = len(keys)
		}
		ph, args := make([]string, end-i), make([]any, end-i)
		for j, k := range keys[i:end] {
			ph[j], args[j] = "?", k
		}
		rows, err := q.QueryContext(ctx, `SELECT dedup_key FROM card_transactions WHERE `+contract.SQLEntity(ctx, "card_transactions")+` AND dedup_key IN (`+strings.Join(ph, ",")+`)`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var k string
			if err := rows.Scan(&k); err != nil {
				rows.Close()
				return nil, err
			}
			out[k] = true
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (SQLCards) InsertImport(ctx context.Context, q contract.Tx, issuer, sha string, total, nw, dup, errN int, by int64, now time.Time) (int64, error) {
	id, err := dbutil.InsertID(ctx, q, `INSERT INTO card_imports (entity_id, issuer, file_sha256, rows_total, rows_new, rows_dup, rows_error, imported_by, imported_at) VALUES (`+contract.SQLEntityID(ctx)+`, ?,?,?,?,?,?,?,?)`,
		issuer, sha, total, nw, dup, errN, by, fmtTime(now))
	return idOrErr(id, err)
}

func idOrErr(id int64, err error) (int64, error) {
	if err != nil {
		return 0, mapErr(err)
	}
	return id, nil
}

// TxnInsert 는 카드 내역 입력이다.
type TxnInsert struct {
	CardID, HolderID, ImportID int64
	UsedAt                     time.Time
	Merchant                   string
	Amount                     int64
	Supply, VAT, Tip           *int64
	ApprovalNo, Kind           string
	DedupKey, DueDate, Status  string
	Now                        time.Time
}

func (SQLCards) InsertTxn(ctx context.Context, q contract.Tx, in TxnInsert) (int64, error) {
	id, err := dbutil.InsertID(ctx, q, `INSERT INTO card_transactions (entity_id, card_id, holder_id, used_at, merchant, amount, supply, vat, tip, approval_no, kind, dedup_key, import_id, due_date, status, created_at)
		VALUES (`+contract.SQLEntityID(ctx)+`, ?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, in.CardID, nullID(in.HolderID), fmtTime(in.UsedAt), in.Merchant, in.Amount, nullPtr(in.Supply), nullPtr(in.VAT), nullPtr(in.Tip),
		in.ApprovalNo, in.Kind, in.DedupKey, in.ImportID, in.DueDate, in.Status, fmtTime(in.Now))
	return idOrErr(id, err)
}

// PairCandidate 는 취소 행과 짝이 되는 승인 행(같은 카드·승인번호·금액 합 0)이다. 없으면 0.
func (SQLCards) PairCandidate(ctx context.Context, q contract.Tx, cardID int64, approvalNo string, cancelAmount int64, exceptID int64) (id int64, status string, err error) {
	if approvalNo == "" {
		return 0, "", nil
	}
	err = q.QueryRowContext(ctx, `SELECT id, status FROM card_transactions WHERE `+contract.SQLEntity(ctx, "card_transactions")+` AND card_id = ? AND approval_no = ? AND kind = 'approve' AND amount = ? AND pair_id IS NULL AND id <> ? ORDER BY id LIMIT 1`,
		cardID, approvalNo, -cancelAmount, exceptID).Scan(&id, &status)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, "", nil
	}
	return id, status, err
}

// ApproveCandidateForCancel 는 승인 행이 늦게 들어왔을 때 짝이 없는 취소 행을 찾는다.
func (SQLCards) CancelCandidate(ctx context.Context, q contract.Tx, cardID int64, approvalNo string, approveAmount int64, exceptID int64) (id int64, err error) {
	if approvalNo == "" {
		return 0, nil
	}
	err = q.QueryRowContext(ctx, `SELECT id FROM card_transactions WHERE `+contract.SQLEntity(ctx, "card_transactions")+` AND card_id = ? AND approval_no = ? AND kind = 'cancel' AND amount = ? AND pair_id IS NULL AND id <> ? ORDER BY id LIMIT 1`,
		cardID, approvalNo, -approveAmount, exceptID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return id, err
}

func (SQLCards) SetPair(ctx context.Context, q contract.Tx, id, pairID int64) error {
	_, err := q.ExecContext(ctx, `UPDATE card_transactions SET pair_id = ? WHERE `+contract.SQLEntity(ctx, "card_transactions")+` AND id = ?`, pairID, id)
	return mapErr(err)
}

// ---- 내역 ----

type TxnRow struct {
	domain.CardTxn
	HolderActive bool
	HolderUnit   int64
}

const txnSelect = `SELECT t.id, t.card_id, c.alias, COALESCE(t.holder_id,0), COALESCE(u.name,''), COALESCE(u.active,0), t.used_at, t.merchant, t.amount, t.supply, t.vat, t.tip, t.approval_no, t.kind, t.status, t.due_date, t.refund_note,
	COALESCE((SELECT j.no FROM journals j WHERE j.entity_id = t.entity_id AND j.source_type = 'card_txn' AND j.kind = 'auto_card' AND j.status <> 'canceled' AND j.source_id IN (SELECT id FROM card_explanations WHERE card_txn_id = t.id) ORDER BY j.id DESC LIMIT 1),'')
	FROM (SELECT * FROM card_transactions WHERE {entity_id}) t JOIN corporate_cards c ON c.id = t.card_id LEFT JOIN users u ON u.id = t.holder_id`

func scanTxn(s rowScanner) (TxnRow, error) {
	var r TxnRow
	var used string
	var active int
	var supply, vat, tip sql.NullInt64
	if err := s.Scan(&r.ID, &r.CardID, &r.CardAlias, &r.HolderID, &r.HolderName, &active, &used, &r.Merchant, &r.Amount, &supply, &vat, &tip, &r.ApprovalNo, &r.Kind, &r.Status, &r.DueDate, &r.RefundNote, &r.JournalNo); err != nil {
		return r, err
	}
	r.HolderActive = active == 1
	r.UsedAt = parseTime(used)
	if supply.Valid {
		v := supply.Int64
		r.Supply = &v
	}
	if vat.Valid {
		v := vat.Int64
		r.VAT = &v
	}
	if tip.Valid {
		v := tip.Int64
		r.Tip = &v
	}
	return r, nil
}

func (SQLCards) Txn(ctx context.Context, q contract.Tx, id int64) (TxnRow, error) {
	r, err := scanTxn(q.QueryRowContext(ctx, contract.EntitySQL(ctx, txnSelect)+` WHERE t.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return r, contract.ErrNotFound
	}
	return r, err
}

// TxnQuery 는 내역 조회 조건이다. HolderID 가 0 이 아니면 그 소지자만, HolderIDs 가 있으면 그 소지자들만 본다.
type TxnQuery struct {
	HolderID  int64
	HolderIDs []int64
	Statuses  []string
	From, To  string // 사용일(KST)
	Limit     int
	Offset    int
	NoHolder  bool // 소지자가 없거나 퇴사한 건만
}

func (SQLCards) Txns(ctx context.Context, q contract.Tx, f TxnQuery) ([]TxnRow, int, error) {
	where, args := []string{"1 = 1"}, []any{}
	if f.HolderID != 0 {
		where = append(where, "t.holder_id = ?")
		args = append(args, f.HolderID)
	}
	if len(f.HolderIDs) > 0 {
		ph, a := idArgs(f.HolderIDs)
		where = append(where, "t.holder_id IN ("+ph+")")
		args = append(args, a...)
	}
	if f.NoHolder {
		where = append(where, "(t.holder_id IS NULL OR u.active = 0)")
	}
	if len(f.Statuses) > 0 {
		ph := make([]string, len(f.Statuses))
		for i, s := range f.Statuses {
			ph[i] = "?"
			args = append(args, s)
		}
		where = append(where, "t.status IN ("+strings.Join(ph, ",")+")")
	}
	if f.From != "" {
		where = append(where, "date(t.used_at, '+9 hours') >= ?")
		args = append(args, f.From)
	}
	if f.To != "" {
		where = append(where, "date(t.used_at, '+9 hours') <= ?")
		args = append(args, f.To)
	}
	cond := strings.Join(where, " AND ")
	var total int
	if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM card_transactions t LEFT JOIN users u ON u.id = t.holder_id WHERE `+contract.SQLEntity(ctx, "t")+` AND `+cond, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	limit := f.Limit
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := q.QueryContext(ctx, contract.EntitySQL(ctx, txnSelect)+` WHERE `+cond+` ORDER BY CASE WHEN t.status IN ('unreconciled','needs_fix') THEN 0 ELSE 1 END, t.due_date, t.id DESC LIMIT ? OFFSET ?`, append(args, limit, f.Offset)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []TxnRow
	for rows.Next() {
		r, err := scanTxn(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, r)
	}
	return out, total, rows.Err()
}

func (SQLCards) SetTxnStatus(ctx context.Context, q contract.Tx, id int64, status string) error {
	_, err := q.ExecContext(ctx, `UPDATE card_transactions SET status = ? WHERE `+contract.SQLEntity(ctx, "card_transactions")+` AND id = ?`, status, id)
	return mapErr(err)
}

func (SQLCards) SetTxnRefund(ctx context.Context, q contract.Tx, id int64, status, note string) error {
	_, err := q.ExecContext(ctx, `UPDATE card_transactions SET status = ?, refund_note = ? WHERE `+contract.SQLEntity(ctx, "card_transactions")+` AND id = ?`, status, note, id)
	return mapErr(err)
}

func (SQLCards) SetTxnHolder(ctx context.Context, q contract.Tx, id, holderID int64) error {
	_, err := q.ExecContext(ctx, `UPDATE card_transactions SET holder_id = ? WHERE `+contract.SQLEntity(ctx, "card_transactions")+` AND id = ?`, nullID(holderID), id)
	return mapErr(err)
}

// ---- 소명 ----

type ExplanationInsert struct {
	TxnID, AccountID, SubmittedBy int64
	Purpose, VATReason            string
	VATDeduct, Personal           bool
	Supply, VAT                   int64
	Now                           time.Time
}

func (SQLCards) InsertExplanation(ctx context.Context, q contract.Tx, in ExplanationInsert) (int64, error) {
	id, err := dbutil.InsertID(ctx, q, `INSERT INTO card_explanations (card_txn_id, purpose, account_id, vat_deduct, vat_reason, supply, vat, personal, status, submitted_at, submitted_by)
		VALUES (?,?,?,?,?,?,?,?, 'submitted', ?,?)`, in.TxnID, in.Purpose, nullID(in.AccountID), boolInt(in.VATDeduct), in.VATReason, in.Supply, in.VAT, boolInt(in.Personal), fmtTime(in.Now), in.SubmittedBy)
	return idOrErr(id, err)
}

// Explanations 는 내역의 소명 이력(오래된 것부터)이다.
func (SQLCards) Explanations(ctx context.Context, q contract.Tx, txnID int64) ([]domain.CardExplanation, []int64, error) {
	rows, err := q.QueryContext(ctx, `SELECT e.id, e.purpose, COALESCE(a.code,''), COALESCE(a.name,''), e.vat_deduct, e.vat_reason, e.supply, e.vat, e.personal, e.status, e.submitted_at, COALESCE(cu.name,''), e.return_reason
		FROM card_explanations e LEFT JOIN accounts a ON a.id = e.account_id LEFT JOIN users cu ON cu.id = e.confirmed_by WHERE e.card_txn_id = ? ORDER BY e.id`, txnID)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var out []domain.CardExplanation
	var ids []int64
	for rows.Next() {
		var e domain.CardExplanation
		var ded, per int
		var at string
		if err := rows.Scan(&e.ID, &e.Purpose, &e.AccountCode, &e.AccountName, &ded, &e.VATReason, &e.Supply, &e.VAT, &per, &e.Status, &at, &e.ConfirmedBy, &e.ReturnReason); err != nil {
			return nil, nil, err
		}
		e.VATDeduct, e.Personal, e.SubmittedAt = ded == 1, per == 1, parseTime(at)
		out = append(out, e)
		ids = append(ids, e.ID)
	}
	return out, ids, rows.Err()
}

// ExplanationRow 는 확인·반려에 필요한 소명 원자료다.
type ExplanationRow struct {
	ID, AccountID, SubmittedBy int64
	Purpose                    string
	VATDeduct, Personal        bool
	Supply, VAT                int64
	Status                     string
}

func (SQLCards) LatestSubmitted(ctx context.Context, q contract.Tx, txnID int64) (ExplanationRow, error) {
	var e ExplanationRow
	var acc sql.NullInt64
	var ded, per int
	err := q.QueryRowContext(ctx, `SELECT id, COALESCE(account_id,0), submitted_by, purpose, vat_deduct, personal, supply, vat, status FROM card_explanations WHERE card_txn_id = ? AND status = 'submitted' ORDER BY id DESC LIMIT 1`, txnID).
		Scan(&e.ID, &acc, &e.SubmittedBy, &e.Purpose, &ded, &per, &e.Supply, &e.VAT, &e.Status)
	if errors.Is(err, sql.ErrNoRows) {
		return e, contract.ErrNotFound
	}
	e.AccountID, e.VATDeduct, e.Personal = acc.Int64, ded == 1, per == 1
	return e, err
}

func (SQLCards) SetExplanationStatus(ctx context.Context, q contract.Tx, id int64, status string, by int64, now time.Time, reason string) error {
	switch status {
	case "confirmed":
		_, err := q.ExecContext(ctx, `UPDATE card_explanations SET status = 'confirmed', confirmed_by = ?, confirmed_at = ? WHERE id = ?`, by, fmtTime(now), id)
		return mapErr(err)
	case "returned":
		_, err := q.ExecContext(ctx, `UPDATE card_explanations SET status = 'returned', return_reason = ? WHERE id = ?`, reason, id)
		return mapErr(err)
	}
	_, err := q.ExecContext(ctx, `UPDATE card_explanations SET status = ? WHERE id = ?`, status, id)
	return mapErr(err)
}

// ExplainAccountOK 는 소명에 쓸 수 있는(허용 목록에 있고 사용 중인) 비용 계정인지다.
func (SQLCards) ExplainAccounts(ctx context.Context, q contract.Tx) ([]domain.Account, error) {
	rows, err := q.QueryContext(ctx, `SELECT a.id, a.code, a.name, a.kind, a.active, a.system FROM accounts a JOIN card_explain_accounts x ON x.account_id = a.id WHERE a.active = 1 ORDER BY a.code`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Account
	for rows.Next() {
		a, err := scanAccount(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// ---- 설정·알림·점검 ----

// SettingInt 는 app_settings 의 정수 값이다(없거나 숫자가 아니면 def).
func (SQLCards) SettingInt(ctx context.Context, q contract.Tx, key string, def int) int {
	var v string
	if err := q.QueryRowContext(ctx, `SELECT value FROM app_settings WHERE key = ?`, key).Scan(&v); err != nil {
		return def
	}
	n := 0
	for _, ch := range v {
		if ch < '0' || ch > '9' {
			return def
		}
		n = n*10 + int(ch-'0')
	}
	return n
}

// MarkReminder 는 그날 그 소지자(또는 조직장)에게 알림을 이미 만들었는지 표시한다. 처음이면 true.
func (SQLCards) MarkReminder(ctx context.Context, q contract.Tx, holderID int64, day, kind string) (bool, error) {
	res, err := q.ExecContext(ctx, `INSERT INTO card_reminders (holder_id, day, kind) VALUES (?,?,?) ON CONFLICT DO NOTHING`, holderID, day, kind)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// OpenTxnsForReminder 는 알림 대상(미소명·보완 필요, 소지자 있음)이다.
func (SQLCards) OpenTxnsForReminder(ctx context.Context, q contract.Tx) ([]TxnRow, error) {
	rows, err := q.QueryContext(ctx, contract.EntitySQL(ctx, txnSelect)+` WHERE t.status IN ('unreconciled','needs_fix') AND t.holder_id IS NOT NULL AND u.active = 1 ORDER BY t.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TxnRow
	for rows.Next() {
		r, err := scanTxn(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// UnreconciledCount 는 사용일이 그 달(KST)인 미소명·보완 필요 건수다(월 마감 점검 경고).
func (SQLCards) UnreconciledCount(ctx context.Context, q contract.Tx, month string) (int, error) {
	var n int
	err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM card_transactions WHERE `+contract.SQLEntity(ctx, "card_transactions")+` AND status IN ('unreconciled','needs_fix') AND substr(date(used_at, '+9 hours'),1,7) = ?`, month).Scan(&n)
	return n, err
}

// TxnByExplanationJournal 은 카드 비용 전표(출처 card_txn, source_id = 소명 번호)가 가리키는 내역 번호다.
func (SQLCards) TxnByExplanation(ctx context.Context, q contract.Tx, explanationID int64) (int64, error) {
	var id int64
	err := q.QueryRowContext(ctx, `SELECT card_txn_id FROM card_explanations WHERE id = ?`, explanationID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, contract.ErrNotFound
	}
	return id, err
}
