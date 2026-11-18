package repository

import (
	"context"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
)

// TaxInvoicesAfter 는 세금계산서를 번호 오름차순으로 읽는다(B43 3단계). 사업자번호 마스킹은 서비스가 한다.
func (SQLAPIRead) TaxInvoicesAfter(ctx context.Context, q contract.Tx, after int64, limit int) ([]domain.APITaxInvoice, error) {
	rows, err := q.QueryContext(ctx, `SELECT t.id, t.no, t.direction, t.tax_type, t.status, t.approval_no, t.issue_date, t.supplier_biz_no, t.supplier_name, t.buyer_biz_no, t.buyer_name,
		t.supply_total, t.tax_total, t.total, COALESCE((SELECT o.no FROM tax_invoices o WHERE `+contract.SQLEntity(ctx, "o")+` AND o.id = t.amend_of_id),''), t.amend_reason,
		(SELECT COUNT(*) FROM tax_invoice_lines l WHERE l.invoice_id = t.id)
		FROM tax_invoices t WHERE `+contract.SQLEntity(ctx, "t")+` AND t.id > ? ORDER BY t.id LIMIT ?`, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.APITaxInvoice
	for rows.Next() {
		var v domain.APITaxInvoice
		if err := rows.Scan(&v.ID, &v.No, &v.Direction, &v.TaxType, &v.Status, &v.ApprovalNo, &v.IssueDate, &v.SupplierBiz, &v.SupplierNm, &v.BuyerBiz, &v.BuyerNm,
			&v.SupplyTotal, &v.TaxTotal, &v.Total, &v.AmendOfNo, &v.AmendReason, &v.LineCount); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
