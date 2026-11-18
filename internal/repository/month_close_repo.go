package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/choibongseok/procure-ledger/internal/dbutil"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
)

// 월 마감 저장소(B27 ②, 3.2·6절). 월은 장부일의 "YYYY-MM"(KST)이다.

// MonthClosedError 는 마감된 월에 쓰려 할 때의 오류다. 메시지는 화면에 그대로 나가고(검증 오류 400), 트리거가 같은 문구로 중단한다.
type MonthClosedError struct{ Month string }

func (e *MonthClosedError) Error() string {
	return "마감된 월입니다. 재무팀에 마감 해제를 요청하세요."
}
func (e *MonthClosedError) Unwrap() error { return contract.ErrValidation }

// SQLMonthClose 는 월 마감 저장소다.
type SQLMonthClose struct{}

// MonthCloseRec 은 월 마감 행이다.
type MonthCloseRec struct {
	Month      string
	Status     string // open · closed
	Version    int
	ClosedBy   int64
	ClosedName string
	ClosedAt   time.Time
}

// Get 은 월 마감 행이다. 없으면 ok=false.
func (SQLMonthClose) Get(ctx context.Context, q contract.Tx, month string) (rec MonthCloseRec, ok bool, err error) {
	var closedAt sql.NullString
	var by sql.NullInt64
	var name sql.NullString
	err = q.QueryRowContext(ctx, `SELECT m.month, m.status, m.version, m.closed_by, u.name, m.closed_at FROM month_closes m LEFT JOIN users u ON u.id = m.closed_by WHERE `+contract.SQLEntity(ctx, "m")+` AND m.month = ?`, month).
		Scan(&rec.Month, &rec.Status, &rec.Version, &by, &name, &closedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return rec, false, nil
	}
	if err != nil {
		return rec, false, err
	}
	rec.ClosedBy, rec.ClosedName = by.Int64, name.String
	if closedAt.Valid {
		rec.ClosedAt = parseTime(closedAt.String)
	}
	return rec, true, nil
}

// AssertOpen 은 그 날짜(YYYY-MM-DD)가 속한 달이 마감되지 않았는지 확인한다. 마감되었으면 *MonthClosedError.
func (r SQLMonthClose) AssertOpen(ctx context.Context, q contract.Tx, day string) error {
	if len(day) < 7 {
		return nil
	}
	rec, ok, err := r.Get(ctx, q, day[:7])
	if err != nil {
		return err
	}
	if ok && rec.Status == "closed" {
		return &MonthClosedError{Month: day[:7]}
	}
	return nil
}

// ClosedFor 는 법인 하나(현재 법인과 무관하게 번호로 지정)의 그 날짜가 속한 달이 마감되었는지다. 법인 간 이동이 도착 법인의 마감을 확인하는 데 쓴다(B51 D9).
func (SQLMonthClose) ClosedFor(ctx context.Context, q contract.Tx, entityID int64, day string) (bool, error) {
	if len(day) < 7 {
		return false, nil
	}
	var n int
	err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM month_closes WHERE entity_id = ? AND month = ? AND status = 'closed'`, entityID, day[:7]).Scan(&n)
	return n > 0, err
}

// All 은 월 마감 행 전부다(월 오름차순).
func (SQLMonthClose) All(ctx context.Context, q contract.Tx) ([]MonthCloseRec, error) {
	rows, err := q.QueryContext(ctx, `SELECT m.month, m.status, m.version, m.closed_by, u.name, m.closed_at FROM month_closes m LEFT JOIN users u ON u.id = m.closed_by  WHERE `+contract.SQLEntity(ctx, "m")+` ORDER BY m.month`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MonthCloseRec
	for rows.Next() {
		var rec MonthCloseRec
		var closedAt, name sql.NullString
		var by sql.NullInt64
		if err := rows.Scan(&rec.Month, &rec.Status, &rec.Version, &by, &name, &closedAt); err != nil {
			return nil, err
		}
		rec.ClosedBy, rec.ClosedName = by.Int64, name.String
		if closedAt.Valid {
			rec.ClosedAt = parseTime(closedAt.String)
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}

// PendingRelease 는 그 달의 마감 해제 요청서(MCR)가 결재 중이면 그 문서 번호다(없으면 0). 해제 결재 중 상태는 문서 상태에서 파생한다.
func (SQLMonthClose) PendingRelease(ctx context.Context, q contract.Tx, month string) (int64, error) {
	// 해제 요청서의 월은 fields(JSON) 안에 있다. JSON 함수는 두 방언이 달라 후보를 읽어 Go 에서 가린다(결재 중인 MCR 문서는 소수다).
	rows, err := q.QueryContext(ctx, `SELECT p.id, p.fields FROM purchase_requests p JOIN approval_forms f ON f.id = p.form_id
		WHERE `+contract.SQLEntity(ctx, "p")+` AND f.code = 'MCR' AND p.status IN ('submitted','in_review') ORDER BY p.id DESC`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var fields sql.NullString
		if err := rows.Scan(&id, &fields); err != nil {
			return 0, err
		}
		var f struct {
			Month string `json:"month"`
		}
		if json.Unmarshal([]byte(fields.String), &f) == nil && f.Month == month {
			return id, nil
		}
	}
	return 0, rows.Err()
}

// ClosedMonthsOf 는 지정한 법인에서 마감된 달의 집합이다(현재 법인과 무관하게 법인 번호로 읽는다: 부팅 이관용).
func (SQLMonthClose) ClosedMonthsOf(ctx context.Context, q contract.Tx, entityID int64) (map[string]bool, error) {
	rows, err := q.QueryContext(ctx, `SELECT month FROM month_closes WHERE entity_id = ? AND status = 'closed'`, entityID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var m string
		if err := rows.Scan(&m); err != nil {
			return nil, err
		}
		out[m] = true
	}
	return out, rows.Err()
}

// EarliestLedgerMonth 는 현재 법인 창고의 원장에 기록이 있는 가장 이른 달이다(기록이 없으면 빈 값). 다른 법인의 더 이른 기록은 마감 순서에 영향이 없다(B51 6절).
func (SQLMonthClose) EarliestLedgerMonth(ctx context.Context, q contract.Tx) (string, error) {
	var m sql.NullString
	err := q.QueryRowContext(ctx, `SELECT substr(MIN(COALESCE(s.book_date, date(s.created_at, '+9 hours'))), 1, 7) FROM stock_ledger s JOIN warehouses w ON w.id = s.warehouse_id AND `+contract.SQLEntity(ctx, "w")).Scan(&m)
	return m.String, err
}

// SaveClose 는 월을 마감 상태로 저장한다(처음이면 버전 1, 해제된 달이면 버전을 1 올린다). 새 버전을 돌려준다.
func (r SQLMonthClose) SaveClose(ctx context.Context, q contract.Tx, month string, actor int64, now time.Time) (int, error) {
	rec, ok, err := r.Get(ctx, q, month)
	if err != nil {
		return 0, err
	}
	if !ok {
		_, err = q.ExecContext(ctx, `INSERT INTO month_closes (entity_id, month, status, version, closed_by, closed_at) VALUES (`+contract.SQLEntityID(ctx)+`, ?, 'closed', 1, ?, ?)`, month, actor, fmtTime(now))
		return 1, mapErr(err)
	}
	v := rec.Version + 1
	_, err = q.ExecContext(ctx, `UPDATE month_closes SET status = 'closed', version = ?, closed_by = ?, closed_at = ?, released_at = NULL, released_doc_id = NULL WHERE `+contract.SQLEntity(ctx, "month_closes")+` AND month = ?`, v, actor, fmtTime(now), month)
	return v, mapErr(err)
}

// SaveRelease 는 마감된 월을 열림으로 바꾼다(해제 승인 후속 동작).
func (SQLMonthClose) SaveRelease(ctx context.Context, q contract.Tx, month string, docID int64, now time.Time) error {
	if err := dbutil.AcquireMonthClose(ctx, q, month); err != nil { // 그 달에 쓰는 중인 트랜잭션이 끝난 뒤에 연다(PG)
		return err
	}
	_, err := q.ExecContext(ctx, `UPDATE month_closes SET status = 'open', released_at = ?, released_doc_id = ? WHERE `+contract.SQLEntity(ctx, "month_closes")+` AND month = ? AND status = 'closed'`, fmtTime(now), nullID(docID), month)
	return mapErr(err)
}

// AddEvent 는 마감·해제 사건을 더한다(추가 전용).
func (SQLMonthClose) AddEvent(ctx context.Context, q contract.Tx, month, kind string, version int, actor, docID int64, now time.Time) error {
	_, err := q.ExecContext(ctx, `INSERT INTO month_close_events (entity_id, month, kind, version, actor_id, doc_id, at) VALUES (`+contract.SQLEntityID(ctx)+`,?,?,?,?,?,?)`, month, kind, version, actor, nullID(docID), fmtTime(now))
	return mapErr(err)
}

// SnapLine 은 월말 스냅샷 한 줄(품목·창고별)이다.
type SnapLine struct {
	ItemID, WarehouseID, EntityID                                                        int64
	OpenQty, OpenAmount, InQty, InAmount, OutQty, OutAmount                              int64
	TransferQty, TransferAmount, AdjustQty, AdjustAmount, CloseQty, CloseAmount, AvgCost int64
}

// BuildSnapshot 은 월(start~end 장부일, 양끝 포함)의 품목·창고별 스냅샷을 원장과 평가 표에서 계산한다. 현재 법인의 창고만 읽는다(B51 6절: 다른 법인 줄이 섞이지 않는다).
// 평균 단가는 (법인, 품목)의 마지막 평가 줄이다. 기초 = start 이전 합계, 입고·출고(양수)·이동(순증감)·조정은 월 안 합계, 기말 = 기초 + 변동. 기말 금액 = 창고 수량 × 월말 평균 단가(반올림 없이 정수 곱).
func (SQLMonthClose) BuildSnapshot(ctx context.Context, q contract.Tx, start, end string) ([]SnapLine, error) {
	rows, err := q.QueryContext(ctx, `SELECT item_id, warehouse_id,
			SUM(CASE WHEN d < ?1 THEN qty ELSE 0 END),
			SUM(CASE WHEN d >= ?1 AND d <= ?2 AND reason = 'receipt' THEN qty ELSE 0 END), SUM(CASE WHEN d >= ?1 AND d <= ?2 AND reason = 'receipt' THEN am ELSE 0 END),
			-SUM(CASE WHEN d >= ?1 AND d <= ?2 AND reason = 'issue' THEN qty ELSE 0 END), -SUM(CASE WHEN d >= ?1 AND d <= ?2 AND reason = 'issue' THEN am ELSE 0 END),
			SUM(CASE WHEN d >= ?1 AND d <= ?2 AND reason IN ('transfer_in','transfer_out') THEN qty ELSE 0 END), SUM(CASE WHEN d >= ?1 AND d <= ?2 AND reason IN ('transfer_in','transfer_out') THEN am ELSE 0 END),
			SUM(CASE WHEN d >= ?1 AND d <= ?2 AND reason = 'adjust' THEN qty ELSE 0 END), SUM(CASE WHEN d >= ?1 AND d <= ?2 AND reason = 'adjust' THEN am ELSE 0 END),
			SUM(CASE WHEN d <= ?2 THEN qty ELSE 0 END), MAX(eid)
		FROM (SELECT s.item_id, s.warehouse_id, w.entity_id AS eid, s.reason, s.qty_delta AS qty, COALESCE(v.amount, 0) AS am, COALESCE(s.book_date, date(s.created_at, '+9 hours')) AS d
			FROM stock_ledger s JOIN warehouses w ON w.id = s.warehouse_id AND `+contract.SQLEntity(ctx, "w")+` LEFT JOIN stock_valuation v ON v.ledger_id = s.id)
		WHERE d <= ?2 GROUP BY item_id, warehouse_id ORDER BY item_id, warehouse_id`, start, end)
	if err != nil {
		return nil, err
	}
	var out []SnapLine
	for rows.Next() {
		var l SnapLine
		if err := rows.Scan(&l.ItemID, &l.WarehouseID, &l.OpenQty, &l.InQty, &l.InAmount, &l.OutQty, &l.OutAmount, &l.TransferQty, &l.TransferAmount, &l.AdjustQty, &l.AdjustAmount, &l.CloseQty, &l.EntityID); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, l)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	prevDay := func(d string) string {
		t, _ := time.Parse("2006-01-02", d)
		return t.AddDate(0, 0, -1).Format("2006-01-02")
	}
	kept := out[:0]
	for _, l := range out {
		if l.OpenQty == 0 && l.InQty == 0 && l.OutQty == 0 && l.TransferQty == 0 && l.AdjustQty == 0 && l.CloseQty == 0 {
			continue
		}
		var err error
		if l.AvgCost, err = avgCostAt(ctx, q, l.EntityID, l.ItemID, end); err != nil {
			return nil, err
		}
		startAvg, err := avgCostAt(ctx, q, l.EntityID, l.ItemID, prevDay(start))
		if err != nil {
			return nil, err
		}
		l.OpenAmount = l.OpenQty * startAvg
		l.CloseAmount = l.CloseQty * l.AvgCost
		kept = append(kept, l)
	}
	return kept, nil
}

// InsertSnapshot 은 마감 버전의 스냅샷 줄을 더한다(추가 전용). 줄 수와 기말 금액 합계를 돌려준다.
func (SQLMonthClose) InsertSnapshot(ctx context.Context, q contract.Tx, month string, version int, lines []SnapLine) (n int, closeAmount int64, err error) {
	for _, l := range lines {
		if _, err := q.ExecContext(ctx, `INSERT INTO month_close_lines (entity_id, month, version, item_id, warehouse_id, open_qty, open_amount, in_qty, in_amount, out_qty, out_amount,
			transfer_qty, transfer_amount, adjust_qty, adjust_amount, close_qty, close_amount, avg_cost) VALUES (`+contract.SQLEntityID(ctx)+`,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			month, version, l.ItemID, l.WarehouseID, l.OpenQty, l.OpenAmount, l.InQty, l.InAmount, l.OutQty, l.OutAmount, l.TransferQty, l.TransferAmount, l.AdjustQty, l.AdjustAmount, l.CloseQty, l.CloseAmount, l.AvgCost); err != nil {
			return 0, 0, mapErr(err)
		}
		closeAmount += l.CloseAmount
	}
	return len(lines), closeAmount, nil
}

// CloseAmount 는 마감된 달의 최신 버전 스냅샷 기말 금액 합계다.
func (SQLMonthClose) CloseAmount(ctx context.Context, q contract.Tx, month string, version int) (int64, error) {
	var a sql.NullInt64
	err := q.QueryRowContext(ctx, `SELECT SUM(close_amount) FROM month_close_lines WHERE `+contract.SQLEntity(ctx, "")+` AND month = ? AND version = ?`, month, version).Scan(&a)
	return a.Int64, err
}

// PrecheckDoc 는 점검 목록의 문서 한 줄(차단·경고)이다.
type PrecheckDoc struct {
	DocID   int64
	DocNo   string
	CountNo string
	Status  string
}

// ApprovingAdjustments 는 실사 기준일이 그 달이고 조정 전표가 결재 중인 실사 표다(차단 항목).
func (SQLMonthClose) ApprovingAdjustments(ctx context.Context, q contract.Tx, month string) ([]PrecheckDoc, error) {
	return queryDocs(ctx, q, `SELECT COALESCE(c.doc_id,0), COALESCE(p.no,''), c.no, c.status FROM stock_counts c LEFT JOIN purchase_requests p ON p.id = c.doc_id
		WHERE c.warehouse_id IN (SELECT id FROM warehouses WHERE `+contract.SQLEntity(ctx, "")+`) AND c.status = 'approving' AND substr(c.base_date,1,7) = ? ORDER BY c.id`, month)
}

// OpenCounts 는 기준일이 그 달인 작성 중·반려 실사 표다(경고 항목).
func (SQLMonthClose) OpenCounts(ctx context.Context, q contract.Tx, month string) ([]PrecheckDoc, error) {
	return queryDocs(ctx, q, `SELECT 0, '', c.no, c.status FROM stock_counts c WHERE c.warehouse_id IN (SELECT id FROM warehouses WHERE `+contract.SQLEntity(ctx, "")+`) AND c.status IN ('draft','rejected') AND substr(c.base_date,1,7) = ? ORDER BY c.id`, month)
}

func queryDocs(ctx context.Context, q contract.Tx, query, month string) ([]PrecheckDoc, error) {
	rows, err := q.QueryContext(ctx, query, month)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PrecheckDoc
	for rows.Next() {
		var d PrecheckDoc
		if err := rows.Scan(&d.DocID, &d.DocNo, &d.CountNo, &d.Status); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// LatePOs 는 납기일이 그 달 말 이전(포함)인데 입고가 끝나지 않은 발주다(미처리 입고, 경고 항목).
func (SQLMonthClose) LatePOs(ctx context.Context, q contract.Tx, end string) ([]string, error) {
	rows, err := q.QueryContext(ctx, `SELECT no FROM purchase_orders WHERE `+contract.SQLEntity(ctx, "purchase_orders")+` AND status IN ('open','partial') AND due_date IS NOT NULL AND due_date <= ? ORDER BY due_date, id`, end)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var no string
		if err := rows.Scan(&no); err != nil {
			return nil, err
		}
		out = append(out, no)
	}
	return out, rows.Err()
}

// WarnedAdjustments 는 현재 법인의 그 달 장부일의 평가 줄 중 평균 단가를 몰라 0 원으로 평가한 조정 행 수다(경고 항목).
func (SQLMonthClose) WarnedAdjustments(ctx context.Context, q contract.Tx, month string) (int, error) {
	var n int
	err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM stock_valuation WHERE `+contract.SQLEntity(ctx, "")+` AND warned = 1 AND substr(book_date,1,7) = ?`, month).Scan(&n)
	return n, err
}

// ClosedMonths 는 법인별로 기간(월 YYYY-MM 범위) 안에서 마감된 달 수다.
func (SQLMonthClose) ClosedMonthsIn(ctx context.Context, q contract.Tx, scope contract.EntityScope, fromMonth, toMonth string) (map[int64]int, error) {
	rows, err := q.QueryContext(ctx, `SELECT entity_id, COUNT(*) FROM month_closes WHERE `+contract.SQLEntityIn(scope, "month_closes")+` AND status = 'closed' AND month >= ? AND month <= ? GROUP BY entity_id`, fromMonth, toMonth)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]int{}
	for rows.Next() {
		var id int64
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		out[id] = n
	}
	return out, rows.Err()
}
