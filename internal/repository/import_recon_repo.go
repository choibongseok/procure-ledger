package repository

import (
	"context"

	"github.com/choibongseok/procure-ledger/internal/contract"
)

// ReconCounts 는 기초 데이터 대조 보고서의 시스템 값이다(코드가 DB 에서 집계한다).
type ReconCounts struct {
	Accounts, InactiveAccounts int64
	Vendors                    int64
	Items, InactiveItems       int64
	StockQty, StockValue       int64
	PayableTotal               int64 // 취소되지 않은 채무 항목의 미지급 잔액(금액 − 지급 누계)
	BudgetTotal                int64
}

// ReconCounts 는 대조 항목별 시스템 집계다. 재고는 현재 법인 창고의 원장 합이고 금액은 (법인, 품목)별 마지막 평가 줄의 잔액 금액 합이다.
func (SQLImport) ReconCounts(ctx context.Context, q contract.Tx) (ReconCounts, error) {
	var c ReconCounts
	for _, one := range []struct {
		query string
		dst   *int64
	}{
		{`SELECT COUNT(*) FROM accounts`, &c.Accounts},
		{`SELECT COUNT(*) FROM accounts WHERE active = 0`, &c.InactiveAccounts},
		{`SELECT COUNT(*) FROM vendors`, &c.Vendors},
		{`SELECT COUNT(*) FROM items`, &c.Items},
		{`SELECT COUNT(*) FROM items WHERE active = 0`, &c.InactiveItems},
		// 재고는 현재 법인의 창고 수량 합과 (법인, 품목) 마지막 평가 줄의 잔액 합이다(B51 4.2). 전체 모드는 법인 합이다.
		{`SELECT COALESCE(SUM(l.qty_delta),0) FROM stock_ledger l JOIN warehouses w ON w.id = l.warehouse_id AND ` + contract.SQLEntity(ctx, "w"), &c.StockQty},
		{`SELECT COALESCE(SUM(CASE WHEN v.entity_basis = 1 THEN v.bal_amount
				ELSE (SELECT COALESCE(SUM(v3.amount),0) FROM stock_valuation v3 WHERE v3.entity_id = v.entity_id AND v3.item_id = v.item_id
					AND (v3.book_date < v.book_date OR (v3.book_date = v.book_date AND v3.ledger_id <= v.ledger_id))) END),0)
			FROM stock_valuation v WHERE ` + contract.SQLEntity(ctx, "v") + ` AND v.ledger_id =
			(SELECT v2.ledger_id FROM stock_valuation v2 WHERE v2.entity_id = v.entity_id AND v2.item_id = v.item_id ORDER BY v2.book_date DESC, v2.ledger_id DESC LIMIT 1)`, &c.StockValue},
		{`SELECT COALESCE(SUM(amount - paid_amount),0) FROM payables WHERE ` + contract.SQLEntity(ctx, "payables") + ` AND canceled = 0`, &c.PayableTotal},
		{`SELECT COALESCE(SUM(amount),0) FROM budgets WHERE ` + contract.SQLEntity(ctx, "budgets") + ` `, &c.BudgetTotal},
	} {
		if err := q.QueryRowContext(ctx, one.query).Scan(one.dst); err != nil {
			return c, err
		}
	}
	return c, nil
}
