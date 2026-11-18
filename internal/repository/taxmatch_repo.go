package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/dbutil"
)

// 매입 세금계산서 3자 대조 저장소(B43 2단계, 명세 4절·6.2). 제안·확정·기각·해제의 상태 전이와 "한 입고는 확정된 연결을 하나만" 은 DB 트리거·부분 유일 색인이 한 번 더 막는다(0071).

// SQLTaxMatch 는 대조 저장소다.
type SQLTaxMatch struct{}

// VendorBiz 는 거래처 번호·이름·사업자번호(숫자만)다. 사업자번호 비교는 숫자만으로 한다.
type VendorBiz struct {
	ID   int64
	Name string
	Biz  string
}

func digitsOf(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// VendorsByBiz 는 사업자번호(숫자 10자리)가 같은 거래처들이다.
func (SQLTaxMatch) VendorsByBiz(ctx context.Context, q contract.Tx, biz string) ([]VendorBiz, error) {
	rows, err := q.QueryContext(ctx, `SELECT id, name, business_no FROM vendors WHERE business_no <> ''`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []VendorBiz
	for rows.Next() {
		var v VendorBiz
		var raw string
		if err := rows.Scan(&v.ID, &v.Name, &raw); err != nil {
			return nil, err
		}
		if v.Biz = digitsOf(raw); v.Biz == biz {
			out = append(out, v)
		}
	}
	return out, rows.Err()
}

// Candidates 는 거래처들의 발주에 대한 입고 중 확정된 연결이 없는 것이다(입고일 KST 날짜가 [from,to]). 공급가액 = 입고 줄 수량 × 발주 줄 단가의 합.
func (SQLTaxMatch) Candidates(ctx context.Context, q contract.Tx, vendorIDs []int64, from, to string) ([]domain.TaxMatchReceipt, error) {
	if len(vendorIDs) == 0 {
		return nil, nil
	}
	ph := strings.TrimSuffix(strings.Repeat("?,", len(vendorIDs)), ",")
	args := make([]any, 0, len(vendorIDs))
	for _, id := range vendorIDs {
		args = append(args, id)
	}
	rows, err := q.QueryContext(ctx, `SELECT r.id, po.id, po.no, r.received_at,
		COALESCE((SELECT SUM(rl.quantity * pl.unit_price) FROM receipt_lines rl JOIN po_lines pl ON pl.id = rl.po_line_id WHERE rl.receipt_id = r.id),0),
		COALESCE((SELECT j.no FROM journals j JOIN journal_evidence e ON e.journal_id = j.id AND e.kind = 'verdict' JOIN document_verdicts v ON v.id = e.ref_id
		          WHERE `+contract.SQLEntity(ctx, "j")+` AND j.kind = 'auto_purchase' AND j.status <> 'canceled' AND v.receipt_id = r.id ORDER BY j.id LIMIT 1),'')
		FROM receipts r JOIN purchase_orders po ON po.id = r.po_id
		WHERE `+contract.SQLEntity(ctx, "r")+` AND po.vendor_id IN (`+ph+`) AND NOT EXISTS (SELECT 1 FROM tax_invoice_match_receipts mr WHERE mr.receipt_id = r.id AND mr.confirmed = 1)
		ORDER BY r.received_at, r.id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.TaxMatchReceipt
	for rows.Next() {
		var c domain.TaxMatchReceipt
		var at string
		if err := rows.Scan(&c.ReceiptID, &c.POID, &c.PONo, &at, &c.Supply, &c.JournalNo); err != nil {
			return nil, err
		}
		c.ReceivedAt = parseTime(at).In(contract.KST).Format("2006-01-02")
		if (from != "" && c.ReceivedAt < from) || (to != "" && c.ReceivedAt > to) || c.Supply <= 0 {
			continue
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// VerdictIDs 는 입고들에 대해 일치로 확정된 증빙 판정 번호다(참고용).
func (SQLTaxMatch) VerdictIDs(ctx context.Context, q contract.Tx, receiptIDs []int64) ([]int64, error) {
	if len(receiptIDs) == 0 {
		return nil, nil
	}
	ph := strings.TrimSuffix(strings.Repeat("?,", len(receiptIDs)), ",")
	args := make([]any, len(receiptIDs))
	for i, id := range receiptIDs {
		args[i] = id
	}
	rows, err := q.QueryContext(ctx, `SELECT id FROM document_verdicts WHERE receipt_id IN (`+ph+`) AND confirmed_outcome = 'match' ORDER BY id`, args...)
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

// PendingInvoices 는 대조 대상(수신 완료, 수정 아님, 양수 금액)이면서 확정된 연결이 없는 매입 세금계산서다.
func (SQLTaxMatch) PendingInvoices(ctx context.Context, q contract.Tx) ([]domain.TaxInvoice, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+taxCols+` FROM tax_invoices t WHERE `+contract.SQLEntity(ctx, "t")+` AND t.direction = 'purchase' AND t.status = 'accepted' AND t.amend_reason = '' AND t.supply_total > 0
		AND NOT EXISTS (SELECT 1 FROM tax_invoice_matches m WHERE m.invoice_id = t.id AND m.status = 'confirmed') ORDER BY t.issue_date, t.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.TaxInvoice
	for rows.Next() {
		v, err := scanTax(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// ProposedFor 는 그 세금계산서의 제안(proposed) 한 건이다(없으면 id 0).
func (SQLTaxMatch) ProposedFor(ctx context.Context, q contract.Tx, invoiceID int64) (id int64, result, key, diff, warn string, err error) {
	err = q.QueryRowContext(ctx, `SELECT id, result, receipt_key, diff_json, warn_json FROM tax_invoice_matches WHERE invoice_id = ? AND status = 'proposed'`, invoiceID).Scan(&id, &result, &key, &diff, &warn)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, "", "", "", "", nil
	}
	return
}

// DismissedKeys 는 그 세금계산서에서 기각한 후보 조합(receipt_key) 집합이다.
func (SQLTaxMatch) DismissedKeys(ctx context.Context, q contract.Tx, invoiceID int64) (map[string]bool, error) {
	rows, err := q.QueryContext(ctx, `SELECT receipt_key FROM tax_invoice_matches WHERE invoice_id = ? AND status = 'dismissed' AND receipt_key <> ''`, invoiceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, err
		}
		out[k] = true
	}
	return out, rows.Err()
}

// Insert 는 제안 한 건과 후보 입고를 쓴다.
func (SQLTaxMatch) Insert(ctx context.Context, tx contract.Tx, invoiceID int64, result, key string, diff domain.TaxMatchDiff, warns []string, recs []domain.TaxMatchReceipt, now time.Time) (int64, error) {
	d, _ := json.Marshal(diff)
	w, _ := json.Marshal(nonNilStr(warns))
	id, err := dbutil.InsertID(ctx, tx, `INSERT INTO tax_invoice_matches (invoice_id, status, result, receipt_key, diff_json, warn_json, proposed_at) VALUES (?,?,?,?,?,?,?)`,
		invoiceID, domain.MatchProposed, result, key, string(d), string(w), fmtTime(now))
	if err != nil {
		return 0, mapErr(err)
	}
	return id, insertReceipts(ctx, tx, id, recs)
}

func nonNilStr(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func insertReceipts(ctx context.Context, tx contract.Tx, id int64, recs []domain.TaxMatchReceipt) error {
	for _, r := range recs {
		if _, err := tx.ExecContext(ctx, `INSERT INTO tax_invoice_match_receipts (match_id, po_id, receipt_id) VALUES (?,?,?)`, id, r.POID, r.ReceiptID); err != nil {
			return mapErr(err)
		}
	}
	return nil
}

// UpdateProposed 는 제안(proposed)의 내용을 다시 계산한 값으로 바꾼다(입고 목록을 통째로 교체).
func (SQLTaxMatch) UpdateProposed(ctx context.Context, tx contract.Tx, id int64, result, key string, diff domain.TaxMatchDiff, warns []string, recs []domain.TaxMatchReceipt) error {
	d, _ := json.Marshal(diff)
	w, _ := json.Marshal(nonNilStr(warns))
	if _, err := tx.ExecContext(ctx, `UPDATE tax_invoice_matches SET result = ?, receipt_key = ?, diff_json = ?, warn_json = ? WHERE id = ? AND status = 'proposed'`, result, key, string(d), string(w), id); err != nil {
		return mapErr(err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM tax_invoice_match_receipts WHERE match_id = ?`, id); err != nil {
		return mapErr(err)
	}
	return insertReceipts(ctx, tx, id, recs)
}

// Decide 는 상태를 from → to 로 바꾼다(조건부 갱신: 이미 바뀌었으면 충돌).
func (SQLTaxMatch) Decide(ctx context.Context, tx contract.Tx, id int64, from, to string, by int64, comment string, now time.Time) error {
	res, err := tx.ExecContext(ctx, `UPDATE tax_invoice_matches SET status = ?, decided_by = ?, decided_at = ?, comment = ? WHERE id = ? AND status = ?`, to, nullID(by), fmtTime(now), comment, id, from)
	if err != nil {
		return mapErr(err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return fmt.Errorf("%w: 대조의 상태가 이미 바뀌었습니다", contract.ErrInvalidState)
	}
	return nil
}

// SetConfirmed 는 대조에 속한 입고들의 확정 표시를 켜거나 끈다. 켜면 한 입고가 이미 다른 확정에 있을 때 유일 색인 위반(충돌)이 난다.
func (SQLTaxMatch) SetConfirmed(ctx context.Context, tx contract.Tx, matchID int64, on bool, journals map[int64]int64) error {
	v := 0
	if on {
		v = 1
	}
	if _, err := tx.ExecContext(ctx, `UPDATE tax_invoice_match_receipts SET confirmed = ? WHERE match_id = ?`, v, matchID); err != nil {
		if dbutil.IsUnique(err) || strings.Contains(err.Error(), "UNIQUE") {
			return fmt.Errorf("%w: 연결하려는 입고 중 이미 다른 세금계산서에 연결된 것이 있습니다", contract.ErrInvalidState)
		}
		return mapErr(err)
	}
	if on {
		for rid, jid := range journals {
			if jid == 0 {
				continue
			}
			if _, err := tx.ExecContext(ctx, `UPDATE tax_invoice_match_receipts SET journal_id = ? WHERE match_id = ? AND receipt_id = ?`, jid, matchID, rid); err != nil {
				return mapErr(err)
			}
		}
	}
	return nil
}

const matchCols = `m.id, m.invoice_id, t.no, t.approval_no, t.issue_date, t.supplier_name, t.supply_total, t.tax_total, t.total, m.status, m.result, m.diff_json, m.warn_json, m.proposed_at, m.decided_at, m.comment`

func scanMatch(s rowScanner) (domain.TaxMatch, error) {
	var m domain.TaxMatch
	var diff, warn, proposed string
	if err := s.Scan(&m.ID, &m.InvoiceID, &m.InvoiceNo, &m.ApprovalNo, &m.IssueDate, &m.SupplierName, &m.Supply, &m.Tax, &m.Total, &m.Status, &m.Result, &diff, &warn, &proposed, &m.DecidedAt, &m.Comment); err != nil {
		return m, err
	}
	_ = json.Unmarshal([]byte(diff), &m.Diff)
	_ = json.Unmarshal([]byte(warn), &m.Warnings)
	m.ProposedAt = proposed
	return m, nil
}

func (SQLTaxMatch) receipts(ctx context.Context, q contract.Tx, matchID int64) ([]domain.TaxMatchReceipt, error) {
	rows, err := q.QueryContext(ctx, `SELECT r.id, po.id, po.no, r.received_at,
		COALESCE((SELECT SUM(rl.quantity * pl.unit_price) FROM receipt_lines rl JOIN po_lines pl ON pl.id = rl.po_line_id WHERE rl.receipt_id = r.id),0),
		COALESCE((SELECT j.no FROM journals j WHERE `+contract.SQLEntity(ctx, "j")+` AND j.id = mr.journal_id),'')
		FROM tax_invoice_match_receipts mr JOIN receipts r ON r.id = mr.receipt_id JOIN purchase_orders po ON po.id = mr.po_id WHERE `+contract.SQLEntity(ctx, "r")+` AND mr.match_id = ? ORDER BY r.received_at, r.id`, matchID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.TaxMatchReceipt{}
	for rows.Next() {
		var c domain.TaxMatchReceipt
		var at string
		if err := rows.Scan(&c.ReceiptID, &c.POID, &c.PONo, &at, &c.Supply, &c.JournalNo); err != nil {
			return nil, err
		}
		c.ReceivedAt = parseTime(at).In(contract.KST).Format("2006-01-02")
		out = append(out, c)
	}
	return out, rows.Err()
}

// Get 은 대조 한 건(입고 포함)이다. 없으면 ErrNotFound.
func (r SQLTaxMatch) Get(ctx context.Context, q contract.Tx, id int64) (domain.TaxMatch, error) {
	m, err := scanMatch(q.QueryRowContext(ctx, `SELECT `+matchCols+` FROM tax_invoice_matches m JOIN tax_invoices t ON t.id = m.invoice_id AND `+contract.SQLEntity(ctx, "t")+` WHERE m.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return m, contract.ErrNotFound
	}
	if err != nil {
		return m, err
	}
	m.Receipts, err = r.receipts(ctx, q, id)
	return m, err
}

// List 는 대조 목록(제안 시각 내림차순)과 전체 건수다.
func (r SQLTaxMatch) List(ctx context.Context, q contract.Tx, f domain.TaxMatchFilter) ([]domain.TaxMatch, int, error) {
	var conds []string
	var args []any
	if f.Status != "" {
		conds = append(conds, `m.status = ?`)
		args = append(args, f.Status)
	}
	if f.Result != "" {
		conds = append(conds, `m.result = ?`)
		args = append(args, f.Result)
	}
	where := ""
	if len(conds) > 0 {
		where = " WHERE " + strings.Join(conds, " AND ")
	}
	var total int
	if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM tax_invoice_matches m JOIN tax_invoices t ON t.id = m.invoice_id AND `+contract.SQLEntity(ctx, "t")+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	limit := f.Limit
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := q.QueryContext(ctx, `SELECT `+matchCols+` FROM tax_invoice_matches m JOIN tax_invoices t ON t.id = m.invoice_id AND `+contract.SQLEntity(ctx, "t")+where+` ORDER BY m.proposed_at DESC, m.id DESC LIMIT ? OFFSET ?`, append(args, limit, f.Offset)...)
	if err != nil {
		return nil, 0, err
	}
	var out []domain.TaxMatch
	for rows.Next() {
		m, err := scanMatch(rows)
		if err != nil {
			rows.Close()
			return nil, 0, err
		}
		out = append(out, m)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	for i := range out {
		if out[i].Receipts, err = r.receipts(ctx, q, out[i].ID); err != nil {
			return nil, 0, err
		}
	}
	return out, total, nil
}

// SortedKey 는 입고 번호를 작은 순으로 이은 키다("12,15").
func SortedKey(ids []int64) string {
	c := append([]int64(nil), ids...)
	sort.Slice(c, func(i, j int) bool { return c[i] < c[j] })
	parts := make([]string, len(c))
	for i, id := range c {
		parts[i] = fmt.Sprint(id)
	}
	return strings.Join(parts, ",")
}

// StateFor 는 세금계산서 한 건의 대조 상태(확정 > 제안 순, 없으면 빈 값)다.
func (SQLTaxMatch) StateFor(ctx context.Context, q contract.Tx, invoiceID int64) (status, result string, err error) {
	err = q.QueryRowContext(ctx, `SELECT status, result FROM tax_invoice_matches WHERE invoice_id = ? AND status IN ('confirmed','proposed') ORDER BY CASE status WHEN 'confirmed' THEN 0 ELSE 1 END LIMIT 1`, invoiceID).Scan(&status, &result)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", nil
	}
	return
}

// Links 는 세금계산서 한 건의 품목 줄 수와 확정된 연결의 매입 전표 번호(입고 → 증빙 판정 → 전표, 취소 제외)다.
func (SQLTaxMatch) Links(ctx context.Context, q contract.Tx, invoiceID int64) (int, []string, error) {
	var lines int
	if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM tax_invoice_lines WHERE invoice_id = ?`, invoiceID).Scan(&lines); err != nil {
		return 0, nil, err
	}
	rows, err := q.QueryContext(ctx, `SELECT DISTINCT j.no FROM tax_invoice_matches m JOIN tax_invoice_match_receipts mr ON mr.match_id = m.id AND mr.confirmed = 1
		JOIN journals j ON j.kind = 'auto_purchase' AND j.status <> 'canceled' AND j.id IN (
			SELECT e.journal_id FROM journal_evidence e JOIN document_verdicts v ON v.id = e.ref_id AND e.kind = 'verdict' WHERE v.receipt_id = mr.receipt_id
			UNION SELECT mr.journal_id WHERE mr.journal_id IS NOT NULL)
		WHERE `+contract.SQLEntity(ctx, "j")+` AND m.invoice_id = ? AND m.status = 'confirmed' ORDER BY j.no`, invoiceID)
	if err != nil {
		return 0, nil, err
	}
	defer rows.Close()
	var nos []string
	for rows.Next() {
		var no string
		if err := rows.Scan(&no); err != nil {
			return 0, nil, err
		}
		nos = append(nos, no)
	}
	return lines, nos, rows.Err()
}
