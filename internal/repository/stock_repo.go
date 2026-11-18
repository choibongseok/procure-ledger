package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/dbutil"
)

// SQLStock 은 창고·출고·이동 저장소다(B24). 재고를 바꾸는 쓰기는 모두 SQLInventory.InsertLedgerWH 한 곳을 거친다.
type SQLStock struct{}

// ---- 창고 ----

const warehouseSelect = `SELECT w.id, w.code, w.name, w.location, w.active,
	(SELECT COUNT(*) FROM (SELECT item_id FROM stock_ledger WHERE warehouse_id = w.id GROUP BY item_id HAVING SUM(qty_delta) <> 0)),
	w.site_id, COALESCE((SELECT s.name FROM entity_sites s WHERE s.id = w.site_id),'')
	FROM (SELECT * FROM warehouses WHERE {entity_id}) w`

func scanWarehouse(s rowScanner) (domain.Warehouse, error) {
	var w domain.Warehouse
	var active int
	err := s.Scan(&w.ID, &w.Code, &w.Name, &w.Location, &active, &w.ItemCount, &w.SiteID, &w.SiteName)
	w.Active = active == 1
	return w, err
}

// Warehouses 는 창고 전부다(사용 중이 먼저, 코드 순).
func (SQLStock) Warehouses(ctx context.Context, q contract.Tx) ([]domain.Warehouse, error) {
	rows, err := q.QueryContext(ctx, contract.EntitySQL(ctx, warehouseSelect)+` ORDER BY w.active DESC, w.code`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Warehouse
	for rows.Next() {
		w, err := scanWarehouse(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// Warehouse 는 창고 한 곳이다. 없으면 ErrNotFound.
func (SQLStock) Warehouse(ctx context.Context, q contract.Tx, id int64) (domain.Warehouse, error) {
	w, err := scanWarehouse(q.QueryRowContext(ctx, contract.EntitySQL(ctx, warehouseSelect)+` WHERE w.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return w, contract.ErrNotFound
	}
	return w, err
}

// SiteUsable 은 사업장이 현재 법인의 활성 사업장인지 본다(0 = 주사업장은 늘 가능).
func (SQLStock) SiteUsable(ctx context.Context, q contract.Tx, siteID int64) (bool, error) {
	return SQLLedger{}.SiteUsable(ctx, q, siteID)
}

func (SQLStock) InsertWarehouse(ctx context.Context, q contract.Tx, in domain.WarehouseInput, now time.Time) (int64, error) {
	id, err := dbutil.InsertID(ctx, q, `INSERT INTO warehouses (entity_id, code, name, location, active, created_at, updated_at, site_id) VALUES (`+contract.SQLEntityID(ctx)+`, ?,?,?,1,?,?,?)`,
		in.Code, in.Name, in.Location, fmtTime(now), fmtTime(now), in.SiteID)
	if err != nil {
		return 0, mapErr(err)
	}
	return id, nil
}

func (SQLStock) UpdateWarehouse(ctx context.Context, q contract.Tx, in domain.WarehouseInput, now time.Time) error {
	res, err := q.ExecContext(ctx, `UPDATE warehouses SET code = ?, name = ?, location = ?, updated_at = ?, site_id = ? WHERE `+contract.SQLEntity(ctx, "warehouses")+` AND id = ?`, in.Code, in.Name, in.Location, fmtTime(now), in.SiteID, in.ID)
	if err != nil {
		return mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return contract.ErrNotFound
	}
	return nil
}

func (SQLStock) SetWarehouseActive(ctx context.Context, q contract.Tx, id int64, active bool, now time.Time) error {
	_, err := q.ExecContext(ctx, `UPDATE warehouses SET active = ?, updated_at = ? WHERE `+contract.SQLEntity(ctx, "warehouses")+` AND id = ?`, boolInt(active), fmtTime(now), id)
	return mapErr(err)
}

// WarehouseHasStock 은 그 창고에 재고(합계 ≠ 0 인 품목·로트)가 남아 있는지다.
func (SQLStock) WarehouseHasStock(ctx context.Context, q contract.Tx, id int64) (bool, error) {
	var n int
	err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM v_wh_lot_stock WHERE warehouse_id = ? AND on_hand <> 0`, id).Scan(&n)
	return n > 0, err
}

func (SQLStock) ActiveWarehouseCount(ctx context.Context, q contract.Tx) (int, error) {
	var n int
	err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM warehouses WHERE `+contract.SQLEntity(ctx, "warehouses")+` AND active = 1`).Scan(&n)
	return n, err
}

// WhLotQty 는 창고·품목·로트(0 이면 로트 없음)의 현재 재고다.
func (SQLStock) WhLotQty(ctx context.Context, q contract.Tx, whID, itemID, lotID int64) (int64, error) {
	var n sql.NullInt64
	err := q.QueryRowContext(ctx, `SELECT SUM(qty_delta) FROM stock_ledger WHERE warehouse_id = ? AND item_id = ? AND COALESCE(lot_id,0) = ?`, whID, itemID, lotID).Scan(&n)
	return n.Int64, err
}

// ItemWarehouseQty 는 품목의 창고별 재고(0 이 아닌 창고만)다.
func (SQLStock) ItemWarehouseQty(ctx context.Context, q contract.Tx, itemID int64) ([]domain.WarehouseQty, error) {
	rows, err := q.QueryContext(ctx, `SELECT w.id, w.name, SUM(s.qty_delta) FROM stock_ledger s JOIN warehouses w ON w.id = s.warehouse_id
		WHERE `+contract.SQLEntity(ctx, "w")+` AND s.item_id = ? GROUP BY w.id HAVING SUM(s.qty_delta) <> 0 ORDER BY w.code`, itemID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.WarehouseQty
	for rows.Next() {
		var x domain.WarehouseQty
		if err := rows.Scan(&x.WarehouseID, &x.WarehouseName, &x.OnHand); err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

// WarehouseLots 는 품목의 창고·로트별 재고(0 이 아닌 줄)다. whID 가 0 이 아니면 그 창고만.
func (SQLStock) WarehouseLots(ctx context.Context, q contract.Tx, itemID, whID int64) ([]domain.WarehouseLot, error) {
	where, args := `s.item_id = ?`, []any{itemID}
	if whID != 0 {
		where, args = where+` AND s.warehouse_id = ?`, append(args, whID)
	}
	rows, err := q.QueryContext(ctx, `SELECT w.id, w.name, COALESCE(l.id,0), COALESCE(l.lot_no,''), COALESCE(l.expiry_date,''), SUM(s.qty_delta)
		FROM stock_ledger s JOIN warehouses w ON w.id = s.warehouse_id LEFT JOIN lots l ON l.id = s.lot_id
		WHERE `+contract.SQLEntity(ctx, "w")+` AND `+where+` GROUP BY w.id, w.name, w.code, COALESCE(l.id,0), l.lot_no, l.expiry_date HAVING SUM(s.qty_delta) <> 0
		ORDER BY w.code, COALESCE(l.expiry_date,'9999-12-31'), COALESCE(l.lot_no,'')`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.WarehouseLot
	for rows.Next() {
		var x domain.WarehouseLot
		if err := rows.Scan(&x.WarehouseID, &x.WarehouseName, &x.LotID, &x.LotNo, &x.ExpiryDate, &x.Quantity); err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

// LotCandidates 는 그 창고에서 재고가 있는 품목의 로트다(로트 관리 품목이 아니면 lot 0 한 줄). 정렬은 호출자가 한다.
func (SQLStock) LotCandidates(ctx context.Context, q contract.Tx, whID, itemID int64) ([]domain.IssueAllocation, error) {
	rows, err := q.QueryContext(ctx, `SELECT COALESCE(l.id,0), COALESCE(l.lot_no,''), COALESCE(l.expiry_date,''), SUM(s.qty_delta),
			COALESCE((SELECT MIN(r.created_at) FROM stock_ledger r WHERE r.lot_id = l.id AND r.reason = 'receipt'), '')
		FROM stock_ledger s LEFT JOIN lots l ON l.id = s.lot_id
		WHERE s.warehouse_id = ? AND s.item_id = ?
		GROUP BY COALESCE(l.id,0), l.id, l.lot_no, l.expiry_date HAVING SUM(s.qty_delta) > 0 ORDER BY COALESCE(l.id,0)`, whID, itemID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.IssueAllocation
	for rows.Next() {
		a := domain.IssueAllocation{ItemID: itemID}
		if err := rows.Scan(&a.LotID, &a.LotNo, &a.Expiry, &a.OnHand, &a.Received); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// LotInfo 는 로트의 번호·유효기한이다(없으면 ErrNotFound). 품목과 맞는지도 확인한다.
func (SQLStock) LotInfo(ctx context.Context, q contract.Tx, itemID, lotID int64) (string, string, error) {
	var no, exp string
	err := q.QueryRowContext(ctx, `SELECT lot_no, COALESCE(expiry_date,'') FROM lots WHERE id = ? AND item_id = ?`, lotID, itemID).Scan(&no, &exp)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", contract.ErrNotFound
	}
	return no, exp, err
}

// DepartmentName 은 부서 이름이다(없으면 ErrNotFound).
func (SQLStock) DepartmentName(ctx context.Context, q contract.Tx, id int64) (string, error) {
	var n string
	err := q.QueryRowContext(ctx, `SELECT name FROM departments WHERE id = ?`, id).Scan(&n)
	if errors.Is(err, sql.ErrNoRows) {
		return "", contract.ErrNotFound
	}
	return n, err
}

// Departments 는 부서 마스터 전부다.
func (SQLStock) Departments(ctx context.Context, q contract.Tx) ([]domain.DepartmentRef, error) {
	rows, err := q.QueryContext(ctx, `SELECT id, name FROM departments ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.DepartmentRef
	for rows.Next() {
		var d domain.DepartmentRef
		if err := rows.Scan(&d.ID, &d.Name); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// NextNo 는 전표 번호의 다음 순번이다(연도별, 처리 트랜잭션 안에서 채번한다).
func (SQLStock) NextNo(ctx context.Context, q contract.Tx, prefix string, year int) (string, error) {
	eid := contract.WriteEntityID(ctx)
	n, err := NextEntityCounter(ctx, q, eid, prefix, year)
	if err != nil {
		return "", err
	}
	pre, err := EntityNoPrefix(ctx, q, eid)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s%s-%d-%04d", pre, prefix, year, n), nil
}

// ---- 출고 전표 ----

func (SQLStock) InsertIssueSlip(ctx context.Context, q contract.Tx, no string, in domain.IssueInput, actor int64, now time.Time) (int64, error) {
	id, err := dbutil.InsertID(ctx, q, `INSERT INTO issue_slips (no, department_id, warehouse_id, issue_date, method, memo, actor_id, created_at) VALUES (?,?,?,?,?,?,?,?)`,
		no, in.DepartmentID, in.WarehouseID, in.IssueDate, in.Method, in.Memo, actor, fmtTime(now))
	if err != nil {
		return 0, mapErr(err)
	}
	return id, nil
}

func (SQLStock) InsertIssueLine(ctx context.Context, q contract.Tx, slipID int64, l domain.IssueLineInput) error {
	_, err := q.ExecContext(ctx, `INSERT INTO issue_slip_lines (slip_id, item_id, lot_id, qty) VALUES (?,?,?,?)`, slipID, l.ItemID, nullID(l.LotID), l.Qty)
	return mapErr(err)
}

// IssueSlips 는 최근 출고 전표(줄 포함)다. id 가 0 이 아니면 그 전표 한 건.
func (SQLStock) IssueSlips(ctx context.Context, q contract.Tx, id int64, limit int) ([]domain.IssueSlip, error) {
	where, args := ``, []any{}
	if id != 0 {
		where, args = ` WHERE s.id = ?`, append(args, id)
	}
	args = append(args, limit)
	rows, err := q.QueryContext(ctx, contract.EntitySQL(ctx, `SELECT s.id, s.no, s.department_id, d.name, s.warehouse_id, w.name, s.issue_date, s.method, s.memo, u.name, s.created_at
		FROM issue_slips s JOIN departments d ON d.id = s.department_id JOIN warehouses w ON w.id = s.warehouse_id AND {entity_id:w} JOIN users u ON u.id = s.actor_id`+where+`
		ORDER BY s.id DESC LIMIT ?`), args...)
	if err != nil {
		return nil, err
	}
	var out []domain.IssueSlip
	for rows.Next() {
		var s domain.IssueSlip
		var at string
		if err := rows.Scan(&s.ID, &s.No, &s.DepartmentID, &s.DepartmentName, &s.WarehouseID, &s.WarehouseName, &s.IssueDate, &s.Method, &s.Memo, &s.ActorName, &at); err != nil {
			rows.Close()
			return nil, err
		}
		s.CreatedAt = parseTime(at)
		out = append(out, s)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for i := range out {
		lr, err := q.QueryContext(ctx, `SELECT l.item_id, i.name, i.sku, COALESCE(l.lot_id,0), COALESCE(t.lot_no,''), l.qty FROM issue_slip_lines l
			JOIN items i ON i.id = l.item_id LEFT JOIN lots t ON t.id = l.lot_id WHERE l.slip_id = ? ORDER BY l.id`, out[i].ID)
		if err != nil {
			return nil, err
		}
		for lr.Next() {
			var x domain.IssueSlipLine
			if err := lr.Scan(&x.ItemID, &x.ItemName, &x.SKU, &x.LotID, &x.LotNo, &x.Qty); err != nil {
				lr.Close()
				return nil, err
			}
			out[i].Lines = append(out[i].Lines, x)
		}
		if err := lr.Close(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// ---- 이동 전표 ----

// WarehouseRef 는 법인 조건 없이 읽은 창고 한 곳이다(법인 간 이동의 도착 창고 판정용). 법인 이름·활성 여부를 같이 읽는다.
type WarehouseRef struct {
	domain.Warehouse
	EntityID     int64
	EntityName   string
	EntityActive bool
}

// WarehouseAny 는 현재 법인과 무관하게 창고 한 곳을 읽는다. 없으면 ErrNotFound. 호출자(서비스)가 범위·권한을 판정한 뒤에만 값을 밖으로 낸다.
func (SQLStock) WarehouseAny(ctx context.Context, q contract.Tx, id int64) (WarehouseRef, error) {
	var w WarehouseRef
	var active, eact int
	err := q.QueryRowContext(ctx, `SELECT w.id, w.code, w.name, w.location, w.active, w.entity_id, e.name, e.active
		FROM warehouses w JOIN entities e ON e.id = w.entity_id WHERE w.id = ?`, id).Scan(&w.ID, &w.Code, &w.Name, &w.Location, &active, &w.EntityID, &w.EntityName, &eact)
	if errors.Is(err, sql.ErrNoRows) {
		return w, contract.ErrNotFound
	}
	w.Active, w.EntityActive = active == 1, eact == 1
	return w, err
}

// TransferTargets 는 법인 id 집합 안의 사용 중인 창고다(법인 번호·코드 순). 법인 간 이동의 도착 창고 후보 조회에 쓴다.
func (SQLStock) TransferTargets(ctx context.Context, q contract.Tx, entityIDs []int64) ([]domain.TransferTarget, error) {
	if len(entityIDs) == 0 {
		return nil, nil
	}
	cond, args := contract.EntityScope{IDs: entityIDs}.Cond("w")
	rows, err := q.QueryContext(ctx, `SELECT w.id, w.code, w.name, w.entity_id, e.name FROM warehouses w JOIN entities e ON e.id = w.entity_id
		WHERE w.active = 1 AND e.active = 1 AND `+cond+` ORDER BY w.entity_id, w.code`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.TransferTarget
	for rows.Next() {
		var t domain.TransferTarget
		if err := rows.Scan(&t.ID, &t.Code, &t.Name, &t.EntityID, &t.EntityName); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// EntityName 은 법인 이름이다(없으면 빈 글자).
func (SQLStock) EntityName(ctx context.Context, q contract.Tx, id int64) (string, error) {
	var n string
	err := q.QueryRowContext(ctx, `SELECT name FROM entities WHERE id = ?`, id).Scan(&n)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return n, err
}

// InsertTransfer 는 이동 전표 머리를 넣는다. 출발·도착 법인은 두 창고가 속한 법인이다(같으면 같은 법인 이동).
func (SQLStock) InsertTransfer(ctx context.Context, q contract.Tx, no string, in domain.TransferInput, fromEntity, toEntity, actor int64, now time.Time) (int64, error) {
	id, err := dbutil.InsertID(ctx, q, `INSERT INTO stock_transfers (no, from_wh_id, to_wh_id, move_date, memo, actor_id, created_at, from_entity_id, to_entity_id) VALUES (?,?,?,?,?,?,?,?,?)`,
		no, in.FromID, in.ToID, in.MoveDate, in.Memo, actor, fmtTime(now), fromEntity, toEntity)
	if err != nil {
		return 0, mapErr(err)
	}
	return id, nil
}

// InsertTransferLine 은 이동 줄을 넣는다. costAmount 는 법인 간 이동의 동결 금액(원, 절대값)이고 같은 법인 이동은 0 이다.
func (SQLStock) InsertTransferLine(ctx context.Context, q contract.Tx, trID int64, l domain.TransferLineInput, costAmount int64) error {
	_, err := q.ExecContext(ctx, `INSERT INTO stock_transfer_lines (transfer_id, item_id, lot_id, qty, cost_amount) VALUES (?,?,?,?,?)`, trID, l.ItemID, nullID(l.LotID), l.Qty, costAmount)
	return mapErr(err)
}

// transferEntityCond 는 이동 전표(별칭 t)를 현재 법인에서 보이게 하는 조건이다: 출발 법인 또는 도착 법인이 현재 법인이면 보인다(명세 7.4).
// 전체 모드·법인 미정 context 는 조건이 없다. 법인 번호는 정수라 SQL 에 직접 넣는다.
func transferEntityCond(ctx context.Context) string {
	if a, ok := contract.ActiveEntityFrom(ctx); ok && !a.All && a.ID > 0 {
		return fmt.Sprintf("(t.from_entity_id = %d OR t.to_entity_id = %d)", a.ID, a.ID)
	}
	return "1=1"
}

// Transfers 는 최근 이동 전표(줄 포함)다. id 가 0 이 아니면 그 전표 한 건. 출발 법인 또는 도착 법인이 현재 법인인 전표만 보인다.
// 상대 법인의 창고 이름은 그대로 읽어 돌려주므로 호출자(서비스)가 범위 밖 법인의 창고 이름을 지운다.
func (SQLStock) Transfers(ctx context.Context, q contract.Tx, id int64, limit int) ([]domain.TransferSlip, error) {
	where, args := ` WHERE `+transferEntityCond(ctx), []any{}
	if id != 0 {
		where, args = where+` AND t.id = ?`, append(args, id)
	}
	args = append(args, limit)
	rows, err := q.QueryContext(ctx, `SELECT t.id, t.no, t.from_wh_id, f.name, t.to_wh_id, w.name, t.move_date, t.memo, u.name, t.created_at,
			t.from_entity_id, fe.name, t.to_entity_id, te.name
		FROM stock_transfers t JOIN warehouses f ON f.id = t.from_wh_id JOIN warehouses w ON w.id = t.to_wh_id JOIN users u ON u.id = t.actor_id
		JOIN entities fe ON fe.id = t.from_entity_id JOIN entities te ON te.id = t.to_entity_id`+where+`
		ORDER BY t.id DESC LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	var out []domain.TransferSlip
	for rows.Next() {
		var s domain.TransferSlip
		var at string
		if err := rows.Scan(&s.ID, &s.No, &s.FromID, &s.FromName, &s.ToID, &s.ToName, &s.MoveDate, &s.Memo, &s.ActorName, &at, &s.FromEntityID, &s.FromEntityName, &s.ToEntityID, &s.ToEntityName); err != nil {
			rows.Close()
			return nil, err
		}
		s.CreatedAt = parseTime(at)
		s.Intercompany = s.FromEntityID != s.ToEntityID
		out = append(out, s)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for i := range out {
		lr, err := q.QueryContext(ctx, `SELECT i.name, i.sku, COALESCE(t.lot_no,''), l.qty, l.cost_amount FROM stock_transfer_lines l
			JOIN items i ON i.id = l.item_id LEFT JOIN lots t ON t.id = l.lot_id WHERE l.transfer_id = ? ORDER BY l.id`, out[i].ID)
		if err != nil {
			return nil, err
		}
		for lr.Next() {
			var x domain.TransferSlipLine
			var cost int64
			if err := lr.Scan(&x.ItemName, &x.SKU, &x.LotNo, &x.Qty, &cost); err != nil {
				lr.Close()
				return nil, err
			}
			out[i].CostAmount += cost
			out[i].Lines = append(out[i].Lines, x)
		}
		if err := lr.Close(); err != nil {
			return nil, err
		}
	}
	return out, nil
}
