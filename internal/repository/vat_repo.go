package repository

import (
	"context"
	"strconv"
	"strings"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
)

// 부가세 매입 집계 조회(B32 3.4). 읽기 전용이고 승인된 전표만 센다. 기준은 전표의 거래일(trade_date)이다.

type SQLVAT struct{}

// siteCond 는 전표 사업장 조건이다(sites 가 nil 이면 법인 전체라 조건이 없다). 사업장 번호는 정수라 SQL 에 직접 넣는다.
func siteCond(alias string, sites []int64) string {
	if sites == nil {
		return ""
	}
	ids := make([]string, len(sites))
	for i, id := range sites {
		ids[i] = strconv.FormatInt(id, 10)
	}
	return " AND " + alias + ".site_id IN (" + strings.Join(ids, ",") + ")"
}

// 매입 전표 한 건의 거래처·공급가액·부가세와 세금계산서 증빙 여부.
const purchaseJournalSelect = `SELECT j.id, j.no, j.trade_date,
	COALESCE((SELECT SUM(l.amount) FROM journal_lines l JOIN accounts a ON a.id = l.account_id WHERE l.journal_id = j.id AND a.code = '1310' AND l.side = 'D'),0),
	COALESCE((SELECT l.vendor_id FROM journal_lines l WHERE l.journal_id = j.id AND l.side = 'C' AND l.vendor_id IS NOT NULL ORDER BY l.seq LIMIT 1),0),
	j.total_amount,
	CASE WHEN EXISTS (SELECT 1 FROM journal_evidence e JOIN document_verdicts v ON v.id = e.ref_id JOIN documents d ON d.id = v.document_id WHERE e.journal_id = j.id AND e.kind = 'verdict' AND d.kind = 'tax_invoice') OR EXISTS (SELECT 1 FROM journal_evidence e JOIN document_verdicts v ON v.id = e.ref_id JOIN tax_invoice_match_receipts mr ON mr.receipt_id = v.receipt_id AND mr.confirmed = 1 WHERE e.journal_id = j.id AND e.kind = 'verdict') OR EXISTS (SELECT 1 FROM tax_invoice_match_receipts mr2 WHERE mr2.journal_id = j.id AND mr2.confirmed = 1) THEN 1 ELSE 0 END
	FROM (SELECT * FROM journals WHERE {entity_id}) j WHERE j.kind = 'auto_purchase' AND j.status = 'approved' AND j.trade_date >= ? AND j.trade_date <= ?`

type purchaseJ struct {
	no, trade     string
	vat, total    int64
	vendorID      int64
	hasTaxInvoice bool
}

func (SQLVAT) purchases(ctx context.Context, q contract.Tx, from, to string, sites []int64) ([]purchaseJ, error) {
	rows, err := q.QueryContext(ctx, contract.EntitySQL(ctx, purchaseJournalSelect)+siteCond("j", sites)+` ORDER BY j.trade_date, j.id`, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []purchaseJ
	for rows.Next() {
		var p purchaseJ
		var id int64
		var tax int
		if err := rows.Scan(&id, &p.no, &p.trade, &p.vat, &p.vendorID, &p.total, &tax); err != nil {
			return nil, err
		}
		p.hasTaxInvoice = tax == 1
		out = append(out, p)
	}
	return out, rows.Err()
}

type vendorInfo struct {
	name, bizNo string
}

func (SQLVAT) vendors(ctx context.Context, q contract.Tx, ids map[int64]bool) (map[int64]vendorInfo, error) {
	out := map[int64]vendorInfo{}
	rows, err := q.QueryContext(ctx, `SELECT id, name, business_no FROM vendors`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var v vendorInfo
		if err := rows.Scan(&id, &v.name, &v.bizNo); err != nil {
			return nil, err
		}
		if ids[id] {
			out[id] = v
		}
	}
	return out, rows.Err()
}

// Purchase 는 표 1(세금계산서 확정 매입, 거래처별)과 표 2(세금계산서 미수취 전표)를 만든다.
func (r SQLVAT) Purchase(ctx context.Context, q contract.Tx, from, to string, sites []int64) ([]domain.VATVendorRow, []domain.VATUnreceivedRow, error) {
	ps, err := r.purchases(ctx, q, from, to, sites)
	if err != nil {
		return nil, nil, err
	}
	ids := map[int64]bool{}
	for _, p := range ps {
		ids[p.vendorID] = true
	}
	vs, err := r.vendors(ctx, q, ids)
	if err != nil {
		return nil, nil, err
	}
	byVendor := map[int64]*domain.VATVendorRow{}
	var order []int64
	var unreceived []domain.VATUnreceivedRow
	for _, p := range ps {
		supply := p.total - p.vat
		v := vs[p.vendorID]
		if p.hasTaxInvoice {
			row := byVendor[p.vendorID]
			if row == nil {
				row = &domain.VATVendorRow{VendorID: p.vendorID, VendorName: v.name, BusinessNo: v.bizNo}
				byVendor[p.vendorID] = row
				order = append(order, p.vendorID)
			}
			row.Count++
			row.Supply += supply
			row.VAT += p.vat
		} else {
			unreceived = append(unreceived, domain.VATUnreceivedRow{JournalNo: p.no, VendorName: v.name, Supply: supply, VAT: p.vat, TradeDate: p.trade})
		}
	}
	rows := make([]domain.VATVendorRow, 0, len(order))
	for _, id := range order {
		rows = append(rows, *byVendor[id])
	}
	return rows, unreceived, nil
}

// Cards 는 표 3(카드 매입)이다: 승인된 카드 비용 전표(출처 소명)의 공제·불공제, 불공제는 사유별.
func (SQLVAT) Cards(ctx context.Context, q contract.Tx, from, to string, sites []int64) ([]domain.VATCardRow, error) {
	rows, err := q.QueryContext(ctx, `SELECT e.vat_deduct, CASE WHEN e.vat_deduct = 1 THEN '' ELSE e.vat_reason END, COUNT(*), COALESCE(SUM(e.supply),0), COALESCE(SUM(e.vat),0), COALESCE(SUM(j.total_amount),0)
		FROM journals j JOIN card_explanations e ON e.id = j.source_id WHERE `+contract.SQLEntity(ctx, "j")+` AND j.kind = 'auto_card' AND j.source_type = 'card_txn' AND j.status = 'approved' AND e.personal = 0`+siteCond("j", sites)+`
		AND j.trade_date >= ? AND j.trade_date <= ? GROUP BY e.vat_deduct, CASE WHEN e.vat_deduct = 1 THEN '' ELSE e.vat_reason END ORDER BY e.vat_deduct DESC, 2`, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.VATCardRow
	for rows.Next() {
		var c domain.VATCardRow
		var ded int
		if err := rows.Scan(&ded, &c.Reason, &c.Count, &c.Supply, &c.VAT, &c.Total); err != nil {
			return nil, err
		}
		c.Kind = "nondeductible"
		if ded == 1 {
			c.Kind = "deductible"
		} else {
			c.ReasonLabel = domain.CardVATReasons[c.Reason]
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// LedgerVAT 는 계정 1310 의 기간 차변 합계(승인된 전표, 거래일 기준)다.
func (SQLVAT) LedgerVAT(ctx context.Context, q contract.Tx, from, to string, sites []int64) (int64, error) {
	var n int64
	err := q.QueryRowContext(ctx, `SELECT COALESCE(SUM(l.amount),0) FROM journal_lines l JOIN journals j ON j.id = l.journal_id JOIN accounts a ON a.id = l.account_id
		WHERE `+contract.SQLEntity(ctx, "j")+` AND a.code = '1310' AND l.side = 'D' AND j.status = 'approved' AND j.trade_date >= ? AND j.trade_date <= ?`+siteCond("j", sites), from, to).Scan(&n)
	return n, err
}

// PendingPurchaseJournals 는 기간의 승인 전(임시·승인 요청) 매입·카드 비용 전표 수다("승인 대기 N건 제외" 안내).
func (SQLVAT) PendingPurchaseJournals(ctx context.Context, q contract.Tx, from, to string, sites []int64) (int, error) {
	var n int
	err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM journals WHERE `+contract.SQLEntity(ctx, "journals")+` AND kind IN ('auto_purchase','auto_card') AND status IN ('draft','requested') AND trade_date >= ? AND trade_date <= ?`+siteCond("journals", sites), from, to).Scan(&n)
	return n, err
}

// Unlinked 는 표 4 다: 작성일이 기간인 수신(accepted) 매입 세금계산서 중 확정된 연결이 없는 것(수정세금계산서 포함). 대조 상태는 제안 결과 이름이다.
func (SQLVAT) Unlinked(ctx context.Context, q contract.Tx, from, to string) ([]domain.VATUnlinkedRow, error) {
	rows, err := q.QueryContext(ctx, `SELECT t.no, t.approval_no, t.supplier_name, t.issue_date, t.supply_total, t.tax_total, t.amend_reason,
		COALESCE((SELECT m.result FROM tax_invoice_matches m WHERE m.invoice_id = t.id AND m.status = 'proposed'),'')
		FROM tax_invoices t WHERE `+contract.SQLEntity(ctx, "t")+` AND t.direction = 'purchase' AND t.status = 'accepted' AND t.issue_date >= ? AND t.issue_date <= ?
		AND NOT EXISTS (SELECT 1 FROM tax_invoice_matches m WHERE m.invoice_id = t.id AND m.status = 'confirmed') ORDER BY t.issue_date, t.id`, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.VATUnlinkedRow
	for rows.Next() {
		var u domain.VATUnlinkedRow
		var amend, result string
		if err := rows.Scan(&u.InvoiceNo, &u.ApprovalNo, &u.Supplier, &u.IssueDate, &u.Supply, &u.VAT, &amend, &result); err != nil {
			return nil, err
		}
		switch {
		case amend != "":
			u.State = "수정세금계산서(입고와 대조하지 않음)"
		case result != "":
			u.State = domain.MatchResultLabels[result]
		default:
			u.State = "대조 전"
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// Diffs 는 확정된 연결의 세금계산서 세액과 연결된 매입 전표 부가세 합이 다른 건이다(작성일이 기간인 것). 전표가 아직 없으면 비교하지 않는다.
func (SQLVAT) Diffs(ctx context.Context, q contract.Tx, from, to string, sites []int64) ([]domain.VATDiffRow, error) {
	rows, err := q.QueryContext(ctx, `SELECT t.no, t.issue_date, t.tax_total, j.no,
		COALESCE((SELECT SUM(l.amount) FROM journal_lines l JOIN accounts a ON a.id = l.account_id WHERE l.journal_id = j.id AND a.code = '1310' AND l.side = 'D'),0)
		FROM tax_invoice_matches m JOIN tax_invoices t ON t.id = m.invoice_id
		JOIN tax_invoice_match_receipts mr ON mr.match_id = m.id AND mr.confirmed = 1
		JOIN journals j ON j.kind = 'auto_purchase' AND j.status = 'approved' AND j.id IN (
			SELECT e.journal_id FROM journal_evidence e JOIN document_verdicts v ON v.id = e.ref_id AND e.kind = 'verdict' WHERE v.receipt_id = mr.receipt_id
			UNION SELECT mr.journal_id WHERE mr.journal_id IS NOT NULL)
		WHERE `+contract.SQLEntity(ctx, "t")+` AND m.status = 'confirmed' AND t.issue_date >= ? AND t.issue_date <= ?`+siteCond("j", sites)+` ORDER BY t.issue_date, t.id, j.id`, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.VATDiffRow
	idx := map[string]int{}
	seen := map[string]bool{}
	for rows.Next() {
		var no, issue, jno string
		var tax, jvat int64
		if err := rows.Scan(&no, &issue, &tax, &jno, &jvat); err != nil {
			return nil, err
		}
		i, ok := idx[no]
		if !ok {
			out = append(out, domain.VATDiffRow{InvoiceNo: no, IssueDate: issue, InvoiceVAT: tax})
			i = len(out) - 1
			idx[no] = i
		}
		if seen[no+"|"+jno] {
			continue
		}
		seen[no+"|"+jno] = true
		if out[i].JournalNos != "" {
			out[i].JournalNos += ", "
		}
		out[i].JournalNos += jno
		out[i].JournalVAT += jvat
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	kept := out[:0]
	for _, d := range out {
		if d.Diff = d.JournalVAT - d.InvoiceVAT; d.Diff != 0 {
			kept = append(kept, d)
		}
	}
	return kept, nil
}

// SiteVAT 는 사업장별(전표의 site_id) 계정 1310 기간 차변 합계와 매입 전표 수다(승인된 전표, 거래일 기준). 키는 site_id(0 = 지정 없음 → 주사업장).
func (SQLVAT) SiteVAT(ctx context.Context, q contract.Tx, from, to string) (vat map[int64]int64, count map[int64]int, err error) {
	vat, count = map[int64]int64{}, map[int64]int{}
	rows, err := q.QueryContext(ctx, `SELECT j.site_id, COALESCE(SUM(l.amount),0) FROM journal_lines l JOIN journals j ON j.id = l.journal_id JOIN accounts a ON a.id = l.account_id
		WHERE `+contract.SQLEntity(ctx, "j")+` AND a.code = '1310' AND l.side = 'D' AND j.status = 'approved' AND j.trade_date >= ? AND j.trade_date <= ? GROUP BY j.site_id`, from, to)
	if err != nil {
		return nil, nil, err
	}
	for rows.Next() {
		var id, n int64
		if err := rows.Scan(&id, &n); err != nil {
			rows.Close()
			return nil, nil, err
		}
		vat[id] = n
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, nil, err
	}
	rows.Close()
	rows, err = q.QueryContext(ctx, `SELECT site_id, COUNT(*) FROM journals WHERE `+contract.SQLEntity(ctx, "journals")+` AND kind = 'auto_purchase' AND status = 'approved' AND trade_date >= ? AND trade_date <= ? GROUP BY site_id`, from, to)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return nil, nil, err
		}
		count[id] = n
	}
	return vat, count, rows.Err()
}
