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

// POLineInfo 는 입고 검사용 발주 줄 정보다. Received 는 같은 트랜잭션에서 읽은 입고 누계(receipt_lines 합)다.
type POLineInfo struct {
	ID        int64
	POID      int64
	ItemID    int64
	Quantity  int64
	Received  int64
	UnitPrice int64 // 발주 단가(공급가액, 원). 입고 줄의 재고 평가 단가다(B27)
}

// POInsert 는 발주 생성 입력이다.
type POInsert struct {
	PRID     int64
	VendorID int64
	Total    int64
	Lines    []domain.PRLine
	Now      time.Time
	// B25: 구매요청의 희망 납기·납품처를 발주 생성 때 복사한다(발주 뒤 요청이 바뀌어도 발주서는 그대로).
	DueDate       string
	DeliveryPlace string
}

// InventoryRepository 는 품목·재고·발주·입고 저장소다. 재고 수량 컬럼은 없고 원장 합 뷰로만 읽는다.
type InventoryRepository interface {
	InsertItem(ctx context.Context, q contract.Tx, it domain.Item) (int64, error)
	SearchItems(ctx context.Context, q contract.Tx, query string, limit int) ([]domain.Item, error)
	GetItem(ctx context.Context, q contract.Tx, id int64) (domain.Item, error)
	ItemStock(ctx context.Context, q contract.Tx, itemID int64) (domain.ItemStock, error)
	BelowSafety(ctx context.Context, q contract.Tx) ([]domain.ItemStock, error)
	Expiring(ctx context.Context, q contract.Tx, today time.Time, withinDays int) ([]domain.ExpiringLot, error)

	InsertVendor(ctx context.Context, q contract.Tx, v domain.Vendor) (int64, error)
	ListVendors(ctx context.Context, q contract.Tx) ([]domain.Vendor, error)

	CreatePurchaseOrder(ctx context.Context, q contract.Tx, in POInsert) (int64, error)
	GetPO(ctx context.Context, q contract.Tx, id int64, scope ViewScope) (domain.PurchaseOrder, error)
	ListPOs(ctx context.Context, q contract.Tx, status domain.POStatus, limit, offset int, scope ViewScope) ([]domain.PurchaseOrder, error)
	CountPOs(ctx context.Context, q contract.Tx, status domain.POStatus, scope ViewScope) (int, error)
	POLine(ctx context.Context, q contract.Tx, poLineID int64) (POLineInfo, error)
	RefreshPOStatus(ctx context.Context, q contract.Tx, poID int64) (domain.POStatus, error)

	FindOrCreateLot(ctx context.Context, q contract.Tx, itemID int64, lotNo, expiry string) (int64, error)
	InsertReceipt(ctx context.Context, q contract.Tx, poID, userID int64, at time.Time) (int64, error)
	InsertReceiptLine(ctx context.Context, q contract.Tx, receiptID, poLineID, itemID, qty, lotID int64) error
	InsertLedger(ctx context.Context, q contract.Tx, itemID, lotID, qty int64, reason string, refType string, refID, actorID int64, at time.Time) error
	InsertLedgerWH(ctx context.Context, q contract.Tx, whID, itemID, lotID, qty int64, reason string, refType string, refID, actorID int64, at time.Time) error
	// AppendLedger 는 원장 한 줄과 그 평가 줄을 같은 트랜잭션에서 쓴다(B27). InsertLedgerWH 는 장부일·단가를 기본값으로 이 함수를 부른다.
	AppendLedger(ctx context.Context, q contract.Tx, e LedgerEntry) error
	// ItemValuation 은 품목의 현재 평균 단가와 잔액 금액이다(평가 줄이 없으면 ok=false, B27).
	ItemValuation(ctx context.Context, q contract.Tx, itemID int64) (avg, amount int64, ok bool, err error)
	// ItemValuations 는 전체 모드(그룹 범위)의 법인별 평가다(B51 D14).
	ItemValuations(ctx context.Context, q contract.Tx, itemID int64) ([]EntityValuation, error)
	GetReceipt(ctx context.Context, q contract.Tx, id int64, scope ViewScope) (domain.Receipt, error)
}

// SQLInventory 는 InventoryRepository 의 SQLite 구현이다.
type SQLInventory struct{}

func scanItem(s rowScanner) (domain.Item, error) {
	var it domain.Item
	var track int
	err := s.Scan(&it.ID, &it.SKU, &it.Name, &it.Unit, &it.SafetyStock, &track)
	it.TrackLot = track == 1
	return it, err
}

const itemCols = `id, sku, name, unit, safety_stock, track_lot`

func getItem(ctx context.Context, q contract.Tx, id int64) (domain.Item, error) {
	it, err := scanItem(q.QueryRowContext(ctx, `SELECT `+itemCols+` FROM items WHERE id = ? AND active = 1`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return it, contract.ErrNotFound
	}
	return it, err
}

// InsertItem 은 품목 마스터를 등록한다. SKU 중복은 ErrValidation.
func (SQLInventory) InsertItem(ctx context.Context, q contract.Tx, it domain.Item) (int64, error) {
	id, err := dbutil.InsertID(ctx, q, `INSERT INTO items (sku, name, unit, safety_stock, track_lot) VALUES (?,?,?,?,?)`,
		it.SKU, it.Name, it.Unit, it.SafetyStock, boolInt(it.TrackLot))
	if err != nil {
		return 0, mapErr(err)
	}
	if err := changes.Note(ctx, q, changes.ResItem, id, changes.OpCreated, ""); err != nil {
		return 0, err
	}
	return id, nil
}

// SearchItems 는 이름·품목코드 부분 일치 검색이다.
func (SQLInventory) SearchItems(ctx context.Context, q contract.Tx, query string, limit int) ([]domain.Item, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	p := likePattern(query)
	rows, err := q.QueryContext(ctx, `SELECT `+itemCols+` FROM items WHERE active = 1
		AND (lower(name) LIKE lower(?) ESCAPE '\' OR lower(sku) LIKE lower(?) ESCAPE '\') ORDER BY id LIMIT ?`, p, p, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Item
	for rows.Next() {
		it, err := scanItem(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// GetItem 은 사용 중인 품목 한 건이다.
func (SQLInventory) GetItem(ctx context.Context, q contract.Tx, id int64) (domain.Item, error) {
	return getItem(ctx, q, id)
}

func lotsOf(ctx context.Context, q contract.Tx, itemID int64) ([]domain.LotStock, error) {
	// 재고는 현재 법인의 창고 원장 합이다(B44: 품목·로트는 법인 공통이고 수량은 법인 소유 창고에만 있다). 전체 모드·법인 없는 context 는 모든 창고다.
	rows, err := q.QueryContext(ctx, `SELECT l.id, l.lot_no, COALESCE(l.expiry_date,''), SUM(s.qty_delta)
		FROM lots l JOIN stock_ledger s ON s.lot_id = l.id JOIN warehouses w ON w.id = s.warehouse_id
		WHERE `+contract.SQLEntity(ctx, "w")+` AND l.item_id = ? GROUP BY l.id, l.lot_no, l.expiry_date HAVING SUM(s.qty_delta) <> 0
		ORDER BY COALESCE(l.expiry_date,'9999-12-31'), l.lot_no`, itemID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.LotStock
	for rows.Next() {
		var l domain.LotStock
		if err := rows.Scan(&l.LotID, &l.LotNo, &l.ExpiryDate, &l.Quantity); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// ItemStock 은 원장 합으로 계산한 재고다.
func (r SQLInventory) ItemStock(ctx context.Context, q contract.Tx, itemID int64) (domain.ItemStock, error) {
	it, err := getItem(ctx, q, itemID)
	if err != nil {
		return domain.ItemStock{}, err
	}
	var onHand int64
	if err := q.QueryRowContext(ctx, `SELECT COALESCE(SUM(s.qty_delta), 0) FROM stock_ledger s JOIN warehouses w ON w.id = s.warehouse_id WHERE `+contract.SQLEntity(ctx, "w")+` AND s.item_id = ?`, itemID).Scan(&onHand); err != nil {
		return domain.ItemStock{}, err
	}
	lots, err := lotsOf(ctx, q, itemID)
	if err != nil {
		return domain.ItemStock{}, err
	}
	return domain.ItemStock{Item: it, OnHand: onHand, Lots: lots, BelowSafety: onHand < it.SafetyStock}, nil
}

// BelowSafety 는 재고가 안전재고 미달인 품목이다.
func (r SQLInventory) BelowSafety(ctx context.Context, q contract.Tx) ([]domain.ItemStock, error) {
	// 수량은 현재 법인 창고의 원장 합이다. 품목(안전재고)은 법인 공통이므로, 현재 법인 창고에 원장이 한 줄도 없는 품목은 다른 법인이 다루는 품목으로 보고 경고에서 뺀다
	// (원장이 어디에도 없는 새 품목은 법인이 하나일 때와 같이 0 개로 경고한다). 법인이 하나면 모든 창고가 그 법인이라 결과가 같다.
	rows, err := q.QueryContext(ctx, `SELECT i.id FROM items i
		LEFT JOIN (SELECT s.item_id AS item_id, SUM(s.qty_delta) AS qty FROM stock_ledger s JOIN warehouses w ON w.id = s.warehouse_id WHERE `+contract.SQLEntity(ctx, "w")+` GROUP BY s.item_id) t ON t.item_id = i.id
		WHERE i.active = 1 AND COALESCE(t.qty, 0) < i.safety_stock
		  AND (t.item_id IS NOT NULL OR NOT EXISTS (SELECT 1 FROM stock_ledger x WHERE x.item_id = i.id))
		ORDER BY i.id`)
	if err != nil {
		return nil, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	var out []domain.ItemStock
	for _, id := range ids {
		st, err := r.ItemStock(ctx, q, id)
		if err != nil {
			return nil, err
		}
		out = append(out, st)
	}
	return out, nil
}

// Expiring 는 재고가 남은 로트 중 유효기한이 today+withinDays 이하인 것(이미 지난 것 포함)이다.
func (SQLInventory) Expiring(ctx context.Context, q contract.Tx, today time.Time, withinDays int) ([]domain.ExpiringLot, error) {
	day := today.UTC().Truncate(24 * time.Hour)
	cutoff := day.AddDate(0, 0, withinDays).Format("2006-01-02")
	rows, err := q.QueryContext(ctx, `SELECT i.id, i.sku, i.name, i.unit, i.safety_stock, i.track_lot, l.id, l.lot_no, l.expiry_date, SUM(s.qty_delta)
		FROM lots l JOIN items i ON i.id = l.item_id JOIN stock_ledger s ON s.lot_id = l.id JOIN warehouses w ON w.id = s.warehouse_id
		WHERE `+contract.SQLEntity(ctx, "w")+` AND i.active = 1 AND l.expiry_date IS NOT NULL AND l.expiry_date <= ?
		GROUP BY i.id, i.sku, i.name, i.unit, i.safety_stock, i.track_lot, l.id, l.lot_no, l.expiry_date HAVING SUM(s.qty_delta) > 0
		ORDER BY l.expiry_date, i.id`, cutoff)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.ExpiringLot
	for rows.Next() {
		var e domain.ExpiringLot
		var track int
		if err := rows.Scan(&e.Item.ID, &e.Item.SKU, &e.Item.Name, &e.Item.Unit, &e.Item.SafetyStock, &track,
			&e.Lot.LotID, &e.Lot.LotNo, &e.Lot.ExpiryDate, &e.Lot.Quantity); err != nil {
			return nil, err
		}
		e.Item.TrackLot = track == 1
		if exp, err := time.Parse("2006-01-02", e.Lot.ExpiryDate); err == nil {
			e.DaysToExpire = int(exp.Sub(day).Hours() / 24)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// InsertVendor 는 거래처를 등록한다.
func (SQLInventory) InsertVendor(ctx context.Context, q contract.Tx, v domain.Vendor) (int64, error) {
	sealed, err := SealAccount(v.AccountNo) // 계좌번호는 봉해서 저장한다(키가 없으면 계좌번호가 있는 등록은 거부)
	if err != nil {
		return 0, err
	}
	id, err := dbutil.InsertID(ctx, q, `INSERT INTO vendors (name, business_no, account_no, phone, email) VALUES (?,?,?,?,?)`,
		v.Name, v.BusinessNo, sealed, v.Phone, v.Email)
	if err != nil {
		return 0, mapErr(err)
	}
	if err := changes.Note(ctx, q, changes.ResVendor, id, changes.OpCreated, ""); err != nil {
		return 0, err
	}
	return id, nil
}

// ListVendors 는 거래처 목록이다(원문 그대로. 마스킹은 서비스가 한다).
func (SQLInventory) ListVendors(ctx context.Context, q contract.Tx) ([]domain.Vendor, error) {
	rows, err := q.QueryContext(ctx, `SELECT id, name, business_no, account_no, phone, email FROM vendors ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Vendor
	for rows.Next() {
		var v domain.Vendor
		if err := rows.Scan(&v.ID, &v.Name, &v.BusinessNo, &v.AccountNo, &v.Phone, &v.Email); err != nil {
			return nil, err
		}
		v.AccountNo = OpenAccount(v.AccountNo)
		out = append(out, v)
	}
	return out, rows.Err()
}

// CreatePurchaseOrder 는 승인된 PR 로 발주와 발주 줄을 만든다.
func (SQLInventory) CreatePurchaseOrder(ctx context.Context, q contract.Tx, in POInsert) (int64, error) {
	id, err := dbutil.InsertID(ctx, q, `INSERT INTO purchase_orders (entity_id, no, pr_id, vendor_id, status, total_amount, created_at, due_date, delivery_place) VALUES (`+contract.SQLEntityID(ctx)+`, ?,?,?, 'open', ?, ?, ?, ?)`,
		fmt.Sprintf("TMP-%d-%d", in.Now.UnixNano(), in.PRID), in.PRID, nullID(in.VendorID), in.Total, fmtTime(in.Now), nullStr(in.DueDate), placeOrDefault(in.DeliveryPlace))
	if err != nil {
		return 0, mapErr(err)
	}
	if _, err := q.ExecContext(ctx, `UPDATE purchase_orders SET no = ? WHERE `+contract.SQLEntity(ctx, "purchase_orders")+` AND id = ?`, fmt.Sprintf("PO-%06d", id), id); err != nil {
		return 0, err
	}
	if err := changes.Note(ctx, q, changes.ResPurchaseOrder, id, changes.OpCreated, string(domain.POOpen)); err != nil {
		return 0, err
	}
	for _, l := range in.Lines {
		if _, err := q.ExecContext(ctx, `INSERT INTO po_lines (po_id, item_id, quantity, unit_price) VALUES (?,?,?,?)`,
			id, l.ItemID, l.Quantity, l.UnitPrice); err != nil {
			return 0, err
		}
	}
	return id, nil
}

const poSelect = `SELECT id, no, pr_id, COALESCE(vendor_id,0), status, total_amount, created_at, COALESCE(due_date,''), delivery_place,
	COALESCE((SELECT e.name FROM vendors v JOIN entities e ON e.id = v.linked_entity_id WHERE v.id = purchase_orders.vendor_id AND v.linked_entity_id > 0 AND v.linked_entity_id <> purchase_orders.entity_id),'') FROM (SELECT * FROM purchase_orders WHERE {entity_id}) purchase_orders`

func scanPO(s rowScanner) (domain.PurchaseOrder, error) {
	var po domain.PurchaseOrder
	var status, created string
	err := s.Scan(&po.ID, &po.No, &po.PRID, &po.VendorID, &status, &po.TotalAmount, &created, &po.DueDate, &po.DeliveryPlace, &po.Intercompany)
	po.Status, po.CreatedAt = domain.POStatus(status), parseTime(created)
	return po, err
}

func poLines(ctx context.Context, q contract.Tx, poID int64) ([]domain.POLine, error) {
	rows, err := q.QueryContext(ctx, `SELECT l.id, l.item_id, i.name, l.quantity, l.unit_price,
		COALESCE((SELECT SUM(r.quantity) FROM receipt_lines r WHERE r.po_line_id = l.id), 0)
		FROM po_lines l JOIN items i ON i.id = l.item_id WHERE l.po_id = ? ORDER BY l.id`, poID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.POLine
	for rows.Next() {
		var l domain.POLine
		if err := rows.Scan(&l.ID, &l.ItemID, &l.ItemName, &l.Quantity, &l.UnitPrice, &l.ReceivedQty); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// poScopeCond 는 발주가 원 구매요청의 열람 범위 안인지 보는 조건절과 인자다(조건 자체는 ViewScope.Cond 한 곳). 전체이면 비어 있다.
func poScopeCond(ctx context.Context, scope ViewScope) (string, []any) {
	if scope.All {
		return "", nil
	}
	cond, args := scope.Cond()
	return ` AND pr_id IN (SELECT p.id FROM purchase_requests p WHERE ` + contract.SQLEntity(ctx, "p") + ` AND ` + cond + `)`, args
}

// GetPO 는 발주 한 건이다(줄의 입고 누계 포함). 원 구매요청이 열람 범위 밖이면 ErrNotFound.
func (SQLInventory) GetPO(ctx context.Context, q contract.Tx, id int64, scope ViewScope) (domain.PurchaseOrder, error) {
	query, args := contract.EntitySQL(ctx, poSelect)+` WHERE id = ?`, []any{id}
	if c, a := poScopeCond(ctx, scope); c != "" {
		query += c
		args = append(args, a...)
	}
	po, err := scanPO(q.QueryRowContext(ctx, query, args...))
	if errors.Is(err, sql.ErrNoRows) {
		return po, contract.ErrNotFound
	}
	if err != nil {
		return po, err
	}
	po.Lines, err = poLines(ctx, q, id)
	return po, err
}

// POOriginRequester 는 발주의 원 구매요청 기안자(requester_id)다. 권한 분리(B41 po.request_receive)가 입고 처리 트랜잭션 안에서 앞 단계 행위자로 읽는다.
func (SQLInventory) POOriginRequester(ctx context.Context, q contract.Tx, poID int64) (int64, error) {
	var id int64
	err := q.QueryRowContext(ctx, `SELECT r.requester_id FROM purchase_orders o JOIN purchase_requests r ON r.id = o.pr_id WHERE `+contract.SQLEntity(ctx, "o")+` AND o.id = ?`, poID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, contract.ErrNotFound
	}
	return id, err
}

// poStatusCond 는 상태 조건이다. 쉼표로 여러 값("open,partial")을 받고 단일 값은 그대로다.
func poStatusCond(status domain.POStatus) (string, []any) {
	var vals []any
	for _, v := range strings.Split(string(status), ",") {
		if v = strings.TrimSpace(v); v != "" {
			vals = append(vals, v)
		}
	}
	if len(vals) == 0 {
		return "", nil
	}
	return ` AND status IN (?` + strings.Repeat(",?", len(vals)-1) + `)`, vals
}

// CountPOs 는 같은 조건(상태·열람 범위)의 발주 전체 건수다.
func (SQLInventory) CountPOs(ctx context.Context, q contract.Tx, status domain.POStatus, scope ViewScope) (int, error) {
	query, args := contract.EntitySQL(ctx, poSelect)+` WHERE 1=1`, []any{}
	if c, a := poStatusCond(status); c != "" {
		query += c
		args = append(args, a...)
	}
	if c, a := poScopeCond(ctx, scope); c != "" {
		query += c
		args = append(args, a...)
	}
	var n int
	err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM (`+query+`)`, args...).Scan(&n)
	return n, err
}

// ListPOs 는 발주 목록이다(최신 순, offset+limit).
func (SQLInventory) ListPOs(ctx context.Context, q contract.Tx, status domain.POStatus, limit, offset int, scope ViewScope) ([]domain.PurchaseOrder, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	query, args := contract.EntitySQL(ctx, poSelect)+` WHERE 1=1`, []any{}
	if c, a := poStatusCond(status); c != "" {
		query += c
		args = append(args, a...)
	}
	if c, a := poScopeCond(ctx, scope); c != "" {
		query += c
		args = append(args, a...)
	}
	query += ` ORDER BY id DESC LIMIT ? OFFSET ?`
	if offset < 0 {
		offset = 0
	}
	args = append(args, limit, offset)
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	var out []domain.PurchaseOrder
	for rows.Next() {
		po, err := scanPO(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, po)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for i := range out {
		if out[i].Lines, err = poLines(ctx, q, out[i].ID); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// POLine 은 입고 검사용 발주 줄이다. 입고 누계는 호출한 트랜잭션 안에서 읽는다.
func (SQLInventory) POLine(ctx context.Context, q contract.Tx, poLineID int64) (POLineInfo, error) {
	var l POLineInfo
	err := q.QueryRowContext(ctx, `SELECT l.id, l.po_id, l.item_id, l.quantity,
		COALESCE((SELECT SUM(r.quantity) FROM receipt_lines r WHERE r.po_line_id = l.id), 0), l.unit_price
		FROM po_lines l WHERE l.id = ?`, poLineID).Scan(&l.ID, &l.POID, &l.ItemID, &l.Quantity, &l.Received, &l.UnitPrice)
	if errors.Is(err, sql.ErrNoRows) {
		return l, contract.ErrNotFound
	}
	return l, err
}

// RefreshPOStatus 는 입고 누계로 발주 상태를 다시 계산해 저장한다.
func (SQLInventory) RefreshPOStatus(ctx context.Context, q contract.Tx, poID int64) (domain.POStatus, error) {
	var total, received int64
	err := q.QueryRowContext(ctx, `SELECT SUM(l.quantity),
		SUM(COALESCE((SELECT SUM(r.quantity) FROM receipt_lines r WHERE r.po_line_id = l.id), 0))
		FROM po_lines l WHERE l.po_id = ?`, poID).Scan(&total, &received)
	if err != nil {
		return "", err
	}
	status := domain.POPartial
	switch {
	case received == 0:
		status = domain.POOpen
	case received >= total:
		status = domain.POReceived
	}
	var prev string
	if err := q.QueryRowContext(ctx, `SELECT status FROM purchase_orders WHERE `+contract.SQLEntity(ctx, "purchase_orders")+` AND id = ?`, poID).Scan(&prev); err != nil {
		return "", err
	}
	if _, err = q.ExecContext(ctx, `UPDATE purchase_orders SET status = ? WHERE `+contract.SQLEntity(ctx, "purchase_orders")+` AND id = ?`, string(status), poID); err != nil {
		return status, err
	}
	if prev != string(status) {
		if err := changes.Note(ctx, q, changes.ResPurchaseOrder, poID, changes.OpStatusChanged, string(status)); err != nil {
			return status, err
		}
	}
	return status, nil
}

// FindOrCreateLot 는 (품목, 로트번호) 로트를 찾거나 만든다. 같은 로트에 다른 유효기한을 주면 ErrValidation.
func (SQLInventory) FindOrCreateLot(ctx context.Context, q contract.Tx, itemID int64, lotNo, expiry string) (int64, error) {
	var id int64
	var exp sql.NullString
	err := q.QueryRowContext(ctx, `SELECT id, expiry_date FROM lots WHERE item_id = ? AND lot_no = ?`, itemID, lotNo).Scan(&id, &exp)
	if err == nil {
		if expiry != "" && exp.String != expiry {
			return 0, fmt.Errorf("%w: 이미 있는 로트의 유효기한과 다릅니다", contract.ErrValidation)
		}
		return id, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	id, err = dbutil.InsertID(ctx, q, `INSERT INTO lots (item_id, lot_no, expiry_date) VALUES (?,?,?)`, itemID, lotNo, nullStr(expiry))
	if err != nil {
		return 0, mapErr(err)
	}
	return id, nil
}

// InsertReceipt 는 입고 헤더를 넣는다.
func (SQLInventory) InsertReceipt(ctx context.Context, q contract.Tx, poID, userID int64, at time.Time) (int64, error) {
	id, err := dbutil.InsertID(ctx, q, `INSERT INTO receipts (entity_id, po_id, received_by, received_at) VALUES (`+contract.SQLEntityID(ctx)+`, ?,?,?)`, poID, userID, fmtTime(at))
	if err != nil {
		return 0, err
	}
	if err := changes.Note(ctx, q, changes.ResReceipt, id, changes.OpCreated, ""); err != nil {
		return 0, err
	}
	return id, nil
}

// InsertReceiptLine 은 입고 줄을 넣는다. 발주 수량 초과는 DB 트리거가 최후로 막고 ErrOverReceipt 로 옮긴다.
func (SQLInventory) InsertReceiptLine(ctx context.Context, q contract.Tx, receiptID, poLineID, itemID, qty, lotID int64) error {
	_, err := q.ExecContext(ctx, `INSERT INTO receipt_lines (receipt_id, po_line_id, item_id, quantity, lot_id) VALUES (?,?,?,?,?)`,
		receiptID, poLineID, itemID, qty, nullID(lotID))
	return mapErr(err)
}

// InsertLedger 는 원장에 한 줄을 추가한다. 재고를 바꾸는 유일한 경로다.
// 창고를 주지 않는 호출은 기본 창고(1)다(B24 호환).
func (r SQLInventory) InsertLedger(ctx context.Context, q contract.Tx, itemID, lotID, qty int64, reason, refType string, refID, actorID int64, at time.Time) error {
	return r.InsertLedgerWH(ctx, q, 1, itemID, lotID, qty, reason, refType, refID, actorID, at)
}

// InsertLedgerWH 는 창고를 지정해 원장에 한 줄을 추가한다. 입고·출고·이동·조정이 모두 이 한 함수를 거친다(B24, AC-402).
func (r SQLInventory) InsertLedgerWH(ctx context.Context, q contract.Tx, whID, itemID, lotID, qty int64, reason, refType string, refID, actorID int64, at time.Time) error {
	return r.AppendLedger(ctx, q, LedgerEntry{WarehouseID: whID, ItemID: itemID, LotID: lotID, Qty: qty, Reason: reason, RefType: refType, RefID: refID, ActorID: actorID, At: at})
}

// ItemValuation 은 현재 법인 품목의 가장 나중 평가 줄에서 평균 단가와 잔액 금액을 읽는다(평가 키는 (법인, 품목)이다, B51).
// 현재 법인이 정해지지 않은 context 는 기본 법인으로 본다. 전체 모드는 ItemValuations 를 쓴다.
func (SQLInventory) ItemValuation(ctx context.Context, q contract.Tx, itemID int64) (avg, amount int64, ok bool, err error) {
	return lastValuation(ctx, q, contract.WriteEntityID(ctx), itemID, "9999-12-31")
}

// EntityValuation 은 한 법인의 품목 평가(평균 단가·잔액 금액)다.
type EntityValuation struct {
	EntityID    int64
	EntityName  string
	AvgCost     int64
	StockAmount int64
}

// ItemValuations 는 품목의 평가를 법인별로 나누어 읽는다(전체 모드, 명세 4.3·D14: 단가는 법인마다 달라 합산하지 않는다). 평가 줄이 있는 법인만 법인 번호 순으로 돌려준다.
func (SQLInventory) ItemValuations(ctx context.Context, q contract.Tx, itemID int64) ([]EntityValuation, error) {
	rows, err := q.QueryContext(ctx, `SELECT e.id, e.name FROM entities e WHERE e.id IN (SELECT DISTINCT entity_id FROM stock_valuation WHERE `+contract.SQLEntity(ctx, "")+` AND item_id = ?) ORDER BY e.id`, itemID)
	if err != nil {
		return nil, err
	}
	var out []EntityValuation
	for rows.Next() {
		var v EntityValuation
		if err := rows.Scan(&v.EntityID, &v.EntityName); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, v)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for i := range out {
		out[i].AvgCost, out[i].StockAmount, _, err = lastValuation(ctx, q, out[i].EntityID, itemID, "9999-12-31")
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// LedgerEntry 는 원장에 더할 한 줄이다. BookDate(KST 날짜)가 비면 기록한 날이고, UnitCost 는 입고 줄의 발주 단가(공급가액, 원)다.
type LedgerEntry struct {
	WarehouseID, ItemID, LotID, Qty int64
	Reason, RefType                 string
	RefID, ActorID                  int64
	At                              time.Time
	BookDate                        string
	UnitCost                        int64
}

// AppendLedger 는 재고를 바꾸는 쓰기의 유일한 원장 삽입이다. 같은 트랜잭션에서 평가 줄(이동평균 단가·금액)도 쓰고,
// 장부일이 뒤쪽 줄보다 앞서는 소급 줄이면 뒤쪽 평가 줄을 다시 계산한다(원장은 건드리지 않는다).
func (SQLInventory) AppendLedger(ctx context.Context, q contract.Tx, e LedgerEntry) error {
	day := e.BookDate
	if day == "" {
		day = string(contract.DayOf(e.At))
	}
	id, err := dbutil.InsertID(ctx, q, `INSERT INTO stock_ledger (item_id, lot_id, qty_delta, reason, ref_type, ref_id, actor_id, created_at, warehouse_id, book_date) VALUES (?,?,?,?,?,?,?,?,?,?)`,
		e.ItemID, nullID(e.LotID), e.Qty, e.Reason, e.RefType, e.RefID, e.ActorID, fmtTime(e.At), e.WarehouseID, day)
	if err != nil {
		return mapErr(err)
	}
	// 평가 키는 (법인, 품목)이고 법인은 창고가 정한다(B51). 호출자가 법인을 넘기지 않는다.
	entityID, err := entityOfWarehouse(ctx, q, e.WarehouseID)
	if err != nil {
		return err
	}
	// 재고 수량(원장 합)이 바뀌었으므로 품목 변경으로 남긴다. 법인은 요청의 현재 법인이 아니라 창고의 법인이다:
	// 법인 간 이동의 도착 행은 호출자의 현재 법인과 다른 법인 재고를 바꾸므로, 도착 법인의 변경 피드·웹훅에 실려야 한다(B51 6단계).
	if err := changes.NoteIn(ctx, q, entityID, changes.ResItem, e.ItemID, changes.OpUpdated, ""); err != nil {
		return err
	}
	return valuateNewRow(ctx, q, entityID, e.ItemID, id, day, e.UnitCost)
}

// GetReceipt 는 입고 한 건이다. 원 구매요청이 열람 범위 밖이면 ErrNotFound.
func (SQLInventory) GetReceipt(ctx context.Context, q contract.Tx, id int64, scope ViewScope) (domain.Receipt, error) {
	var rc domain.Receipt
	var at string
	query, args := `SELECT id, po_id, received_by, received_at FROM receipts WHERE `+contract.SQLEntity(ctx, "receipts")+` AND id = ?`, []any{id}
	if !scope.All {
		cond, cargs := scope.Cond()
		query += ` AND po_id IN (SELECT id FROM purchase_orders WHERE ` + contract.SQLEntity(ctx, "purchase_orders") + ` AND pr_id IN (SELECT p.id FROM purchase_requests p WHERE ` + contract.SQLEntity(ctx, "p") + ` AND ` + cond + `))`
		args = append(args, cargs...)
	}
	err := q.QueryRowContext(ctx, query, args...).Scan(&rc.ID, &rc.POID, &rc.ReceivedBy, &at)
	if errors.Is(err, sql.ErrNoRows) {
		return rc, contract.ErrNotFound
	}
	if err != nil {
		return rc, err
	}
	rc.ReceivedAt = parseTime(at)
	rows, err := q.QueryContext(ctx, `SELECT r.po_line_id, r.item_id, r.quantity, COALESCE(l.lot_no,''), COALESCE(l.expiry_date,'')
		FROM receipt_lines r LEFT JOIN lots l ON l.id = r.lot_id WHERE r.receipt_id = ? ORDER BY r.id`, id)
	if err != nil {
		return rc, err
	}
	defer rows.Close()
	for rows.Next() {
		var l domain.ReceiptLine
		if err := rows.Scan(&l.POLineID, &l.ItemID, &l.Quantity, &l.LotNo, &l.ExpiryDate); err != nil {
			return rc, err
		}
		rc.Lines = append(rc.Lines, l)
	}
	return rc, rows.Err()
}

func placeOrDefault(p string) string {
	if p == "" {
		return "본사 창고"
	}
	return p
}

// VendorLinkRow 는 거래처의 이름과 연결 법인이다.
func (SQLInventory) VendorLinkRow(ctx context.Context, q contract.Tx, id int64) (name string, linked int64, err error) {
	err = q.QueryRowContext(ctx, `SELECT name, linked_entity_id FROM vendors WHERE id = ?`, id).Scan(&name, &linked)
	if errors.Is(err, sql.ErrNoRows) {
		return "", 0, contract.ErrNotFound
	}
	return
}

// SetVendorLinkedEntity 는 거래처의 연결 법인을 바꾸고 변경 피드에 한 줄 남긴다(B44, 0 = 연결 없음).
func (SQLInventory) SetVendorLinkedEntity(ctx context.Context, q contract.Tx, id, entityID int64) error {
	if _, err := q.ExecContext(ctx, `UPDATE vendors SET linked_entity_id = ? WHERE id = ?`, entityID, id); err != nil {
		return err
	}
	return changes.Note(ctx, q, changes.ResVendor, id, changes.OpUpdated, "")
}
