// Package kit 는 docverify 테스트와 평가 러너(cmd/eval)가 공유하는 시드·스텁이다. 운영 코드는 이 패키지를 쓰지 않는다.
// 실제 DB 에 사용자·부서·거래처·발주·입고 행을 만들어(외래 키 만족) 실제 SQL 경로를 그대로 태우고,
// 발주·입고 조회(InventoryService)는 같은 값을 메모리에서 돌려주는 스텁을 쓴다(erp 서비스 없이도 돈다).
package kit

import (
	"context"
	"database/sql"
	"fmt"
	"github.com/choibongseok/procure-ledger/internal/dbutil"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
)

// 기본 시드 신원. SeedBase 가 만든다.
const (
	DeptID       int64 = 1
	StaffID      int64 = 101
	FinanceID    int64 = 102
	OtherDeptID  int64 = 2
	OtherStaffID int64 = 103
)

// Principal 은 시드 신원에 대응하는 Principal 이다.
func Principal(role contract.Role) contract.Principal {
	switch role {
	case contract.RoleFinance:
		return contract.Principal{UserID: FinanceID, Name: "재무", Role: role, DepartmentID: DeptID, Channel: contract.ChannelWeb}
	default:
		return contract.Principal{UserID: StaffID, Name: "직원", Role: role, DepartmentID: DeptID, Channel: contract.ChannelWeb}
	}
}

// SeedBase 는 부서 2개와 사용자 3명을 멱등하게 만든다.
func SeedBase(ctx context.Context, db *sql.DB) error {
	stmts := []struct {
		q    string
		args []any
	}{
		{`INSERT INTO departments (id, name) VALUES (?, ?) ON CONFLICT DO NOTHING`, []any{DeptID, "구매팀"}},
		{`INSERT INTO departments (id, name) VALUES (?, ?) ON CONFLICT DO NOTHING`, []any{OtherDeptID, "영업팀"}},
		{`INSERT INTO users (id, email, name, role, department_id) VALUES (?,?,?,?,?) ON CONFLICT DO NOTHING`, []any{StaffID, "staff@pl.test", "직원", "staff", DeptID}},
		{`INSERT INTO users (id, email, name, role, department_id) VALUES (?,?,?,?,?) ON CONFLICT DO NOTHING`, []any{FinanceID, "finance@pl.test", "재무", "finance", DeptID}},
		{`INSERT INTO users (id, email, name, role, department_id) VALUES (?,?,?,?,?) ON CONFLICT DO NOTHING`, []any{OtherStaffID, "staff2@pl.test", "타부서직원", "staff", OtherDeptID}},
	}
	for _, s := range stmts {
		if _, err := db.ExecContext(ctx, s.q, s.args...); err != nil {
			return fmt.Errorf("기본 시드: %w", err)
		}
	}
	// PG 는 id 를 직접 넣어도 식별자 시퀀스가 앞서지 않으므로 맞춰 둔다(이후 id 없이 넣는 시험 행과 충돌하지 않게). SQLite 는 아무 일도 하지 않는다.
	for _, t := range []string{"departments", "users"} {
		if err := dbutil.SyncSequence(ctx, db, t); err != nil {
			return fmt.Errorf("기본 시드 시퀀스: %w", err)
		}
	}
	return nil
}

// LineSpec 은 발주 줄 명세다.
type LineSpec struct {
	Name      string `json:"name"`
	Qty       int64  `json:"qty"`
	UnitPrice int64  `json:"unitPrice"`
}

// ReceiptSpec 은 입고 줄 명세다(Line 은 발주 줄 인덱스).
type ReceiptSpec struct {
	Line int   `json:"line"`
	Qty  int64 `json:"qty"`
}

// POSpec 은 발주·입고 시드 명세다. Receipt 가 비면 입고 없음.
type POSpec struct {
	VendorName       string        `json:"vendorName"`
	VendorBusinessNo string        `json:"vendorBusinessNo"`
	Lines            []LineSpec    `json:"lines"`
	Receipt          []ReceiptSpec `json:"receipt,omitempty"`
}

// Inventory 는 시드된 발주·입고를 돌려주는 domain.InventoryService 스텁이다. 나머지 메서드는 호출되면 안 된다.
type Inventory struct {
	domain.InventoryService
	POs      map[int64]domain.PurchaseOrder
	Receipts map[int64]domain.Receipt
}

// NewInventory 는 빈 스텁을 만든다.
func NewInventory() *Inventory {
	return &Inventory{POs: map[int64]domain.PurchaseOrder{}, Receipts: map[int64]domain.Receipt{}}
}

// GetPurchaseOrder 는 InventoryService 를 구현한다.
func (i *Inventory) GetPurchaseOrder(ctx context.Context, id int64) (domain.PurchaseOrder, error) {
	if _, err := contract.Authorize(ctx, contract.PermInventoryRead); err != nil {
		return domain.PurchaseOrder{}, err
	}
	po, ok := i.POs[id]
	if !ok {
		return domain.PurchaseOrder{}, contract.ErrNotFound
	}
	return po, nil
}

// GetReceipt 는 InventoryService 를 구현한다.
func (i *Inventory) GetReceipt(ctx context.Context, id int64) (domain.Receipt, error) {
	if _, err := contract.Authorize(ctx, contract.PermInventoryRead); err != nil {
		return domain.Receipt{}, err
	}
	r, ok := i.Receipts[id]
	if !ok {
		return domain.Receipt{}, contract.ErrNotFound
	}
	return r, nil
}

var seq int64

// Seed 는 명세대로 DB 행(거래처·품목·PR·PO·입고)을 만들고 스텁에도 등록한다. 만든 PO·입고 ID 를 돌려준다.
func (i *Inventory) Seed(ctx context.Context, db *sql.DB, spec POSpec) (poID, receiptID int64, err error) {
	seq++
	n := seq
	now := "2026-11-10T00:00:00Z"
	vendorID, err := dbutil.InsertID(ctx, db, `INSERT INTO vendors (name, business_no) VALUES (?, ?)`, spec.VendorName, spec.VendorBusinessNo)
	if err != nil {
		return 0, 0, err
	}
	var total int64
	for _, l := range spec.Lines {
		total += l.Qty * l.UnitPrice
	}
	prID, err := dbutil.InsertID(ctx, db, `INSERT INTO purchase_requests (no, requester_id, department_id, vendor_id, title, status, total_amount, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?,?,?)`, fmt.Sprintf("PR-SEED-%d", n), StaffID, DeptID, vendorID, "시드", "ordered", total, now, now)
	if err != nil {
		return 0, 0, err
	}
	poID, err = dbutil.InsertID(ctx, db, `INSERT INTO purchase_orders (no, pr_id, vendor_id, status, total_amount, created_at) VALUES (?,?,?,?,?,?)`,
		fmt.Sprintf("PO-%06d", prID), prID, vendorID, "open", total, now)
	if err != nil {
		return 0, 0, err
	}
	po := domain.PurchaseOrder{ID: poID, No: fmt.Sprintf("PO-%06d", prID), PRID: prID, VendorID: vendorID, Status: domain.POOpen, TotalAmount: total}
	itemIDs := make([]int64, len(spec.Lines))
	for idx, l := range spec.Lines {
		itemIDs[idx], err = dbutil.InsertID(ctx, db, `INSERT INTO items (sku, name) VALUES (?, ?)`, fmt.Sprintf("SK-%d-%d", prID, idx), l.Name)
		if err != nil {
			return 0, 0, err
		}
		lid, err := dbutil.InsertID(ctx, db, `INSERT INTO po_lines (po_id, item_id, quantity, unit_price) VALUES (?,?,?,?)`, poID, itemIDs[idx], l.Qty, l.UnitPrice)
		if err != nil {
			return 0, 0, err
		}
		po.Lines = append(po.Lines, domain.POLine{ID: lid, ItemID: itemIDs[idx], ItemName: l.Name, Quantity: l.Qty, UnitPrice: l.UnitPrice})
	}
	i.POs[poID] = po
	if len(spec.Receipt) > 0 {
		receiptID, err = dbutil.InsertID(ctx, db, `INSERT INTO receipts (po_id, received_by, received_at) VALUES (?,?,?)`, poID, FinanceID, now)
		if err != nil {
			return 0, 0, err
		}
		rc := domain.Receipt{ID: receiptID, POID: poID, ReceivedBy: FinanceID}
		for _, rl := range spec.Receipt {
			if rl.Line < 0 || rl.Line >= len(po.Lines) {
				return 0, 0, fmt.Errorf("입고 줄 인덱스 오류: %d", rl.Line)
			}
			pl := po.Lines[rl.Line]
			if _, err = db.ExecContext(ctx, `INSERT INTO receipt_lines (receipt_id, po_line_id, item_id, quantity) VALUES (?,?,?,?)`, receiptID, pl.ID, pl.ItemID, rl.Qty); err != nil {
				return 0, 0, err
			}
			rc.Lines = append(rc.Lines, domain.ReceiptLine{POLineID: pl.ID, ItemID: pl.ItemID, Quantity: rl.Qty})
		}
		i.Receipts[receiptID] = rc
	}
	return poID, receiptID, nil
}
