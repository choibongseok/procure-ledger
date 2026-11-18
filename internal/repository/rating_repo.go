package repository

import (
	"context"

	"github.com/choibongseok/procure-ledger/internal/contract"
)

// SQLRating 은 거래처 평가 원자료(B25 ④)다. 계산은 서비스가 한다(조회 때 계산, 저장·캐시 없음). 날짜는 KST 날짜(YYYY-MM-DD)다.
type SQLRating struct{}

// RatedOrder 는 기간 안에 입고가 끝난 발주 한 건이다.
type RatedOrder struct {
	VendorID   int64
	VendorName string
	POID       int64
	PONo       string
	DueDate    string // 없으면 빈 값
	ReceivedOn string // 마지막 입고일
}

// ReceivedOrders 는 마지막 입고일이 [from,to] 안인 입고 완료 발주다.
func (SQLRating) ReceivedOrders(ctx context.Context, q contract.Tx, from, to string) ([]RatedOrder, error) {
	rows, err := q.QueryContext(ctx, `SELECT vendor_id, name, id, no, due, last_day FROM (
			SELECT po.vendor_id AS vendor_id, v.name AS name, po.id AS id, po.no AS no, COALESCE(po.due_date,'') AS due, date(MAX(r.received_at), '+9 hours') AS last_day
			FROM purchase_orders po JOIN vendors v ON v.id = po.vendor_id JOIN receipts r ON r.po_id = po.id
			WHERE `+contract.SQLEntity(ctx, "po")+` AND po.status = 'received'
			GROUP BY po.id, po.vendor_id, po.no, po.due_date, v.name) t
		WHERE last_day >= ? AND last_day <= ? ORDER BY last_day, id`, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RatedOrder
	for rows.Next() {
		var o RatedOrder
		if err := rows.Scan(&o.VendorID, &o.VendorName, &o.POID, &o.PONo, &o.DueDate, &o.ReceivedOn); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// OpenLateOrder 는 납기를 넘겼는데 입고가 끝나지 않은 발주다.
type OpenLateOrder struct {
	VendorID   int64
	VendorName string
	PONo       string
	DueDate    string
}

// OpenLateOrders 는 납기일이 today 보다 앞인 미입고(open·partial) 발주다.
func (SQLRating) OpenLateOrders(ctx context.Context, q contract.Tx, today string) ([]OpenLateOrder, error) {
	rows, err := q.QueryContext(ctx, `SELECT po.vendor_id, v.name, po.no, po.due_date
		FROM purchase_orders po JOIN vendors v ON v.id = po.vendor_id
		WHERE `+contract.SQLEntity(ctx, "po")+` AND po.status IN ('open','partial') AND po.due_date IS NOT NULL AND po.due_date < ? ORDER BY po.due_date, po.id`, today)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []OpenLateOrder
	for rows.Next() {
		var o OpenLateOrder
		if err := rows.Scan(&o.VendorID, &o.VendorName, &o.PONo, &o.DueDate); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// ConfirmedVerdict 는 재무가 확정한 판정 한 건(확정 결과가 일치 또는 불일치)이다.
type ConfirmedVerdict struct {
	VendorID    int64
	VendorName  string
	VerdictID   int64
	DocumentID  int64
	PONo        string
	Mismatch    bool
	ConfirmedOn string
}

// ConfirmedVerdicts 는 확정일(KST)이 [from,to] 안이고 확정 결과가 일치·불일치인 판정이다. 확정 전·판단 보류는 뺀다.
func (SQLRating) ConfirmedVerdicts(ctx context.Context, q contract.Tx, from, to string) ([]ConfirmedVerdict, error) {
	rows, err := q.QueryContext(ctx, `SELECT vendor_id, name, id, document_id, no, confirmed_outcome, day FROM (
			SELECT po.vendor_id AS vendor_id, v.name AS name, dv.id AS id, dv.document_id AS document_id, po.no AS no, dv.confirmed_outcome AS confirmed_outcome, date(dv.confirmed_at, '+9 hours') AS day
			FROM document_verdicts dv JOIN purchase_orders po ON po.id = dv.po_id JOIN vendors v ON v.id = po.vendor_id
			WHERE `+contract.SQLEntity(ctx, "po")+` AND dv.confirmed_at IS NOT NULL AND dv.confirmed_outcome IN ('match','mismatch')) t
		WHERE day >= ? AND day <= ? ORDER BY day, id`, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ConfirmedVerdict
	for rows.Next() {
		var c ConfirmedVerdict
		var outcome string
		if err := rows.Scan(&c.VendorID, &c.VendorName, &c.VerdictID, &c.DocumentID, &c.PONo, &outcome, &c.ConfirmedOn); err != nil {
			return nil, err
		}
		c.Mismatch = outcome == "mismatch"
		out = append(out, c)
	}
	return out, rows.Err()
}

// ItemUnits 는 품목 번호별 단위(발주서 출력용)다.
func (SQLRating) ItemUnits(ctx context.Context, q contract.Tx, ids []int64) (map[int64]string, error) {
	out := map[int64]string{}
	for _, id := range ids {
		var u string
		if err := q.QueryRowContext(ctx, `SELECT unit FROM items WHERE id = ?`, id).Scan(&u); err == nil {
			out[id] = u
		}
	}
	return out, nil
}
