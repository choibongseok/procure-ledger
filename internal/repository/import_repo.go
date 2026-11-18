package repository

import (
	"context"
	"database/sql"
	"errors"

	"github.com/choibongseok/procure-ledger/internal/changes"
	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
)

// SQLImport 는 기초 데이터 가져오기(B35)의 대상별 반영이 쓰는 조회·변경이다. 기존 화면의 저장 함수(InsertItem 등)가 못 하는 "변경"만 여기 있고,
// 원장·전표·채무에 들어가는 길은 기존 단일 함수(AppendLedger·전표 서비스)를 그대로 쓴다.
type SQLImport struct{}

// ItemRow 는 품목 한 건(사용 중지 포함)과 현재 재고 합이다.
type ItemRow struct {
	domain.Item
	Active bool
	OnHand int64
}

// ItemBySKU 는 SKU(대소문자 구분 없음)의 품목이다(중지된 품목 포함). 없으면 ok=false.
func (SQLImport) ItemBySKU(ctx context.Context, q contract.Tx, sku string) (ItemRow, bool, error) {
	var r ItemRow
	var lot, active int
	err := q.QueryRowContext(ctx, `SELECT i.id, i.sku, i.name, i.unit, i.safety_stock, i.track_lot, i.active, COALESCE(s.on_hand,0)
		FROM items i LEFT JOIN v_item_stock s ON s.item_id = i.id WHERE lower(i.sku) = lower(?)`, sku).
		Scan(&r.ID, &r.SKU, &r.Name, &r.Unit, &r.SafetyStock, &lot, &active, &r.OnHand)
	if errors.Is(err, sql.ErrNoRows) {
		return r, false, nil
	}
	r.TrackLot, r.Active = lot == 1, active == 1
	return r, err == nil, err
}

// UpdateItem 은 품목의 이름·단위·안전재고·로트 관리를 바꾼다(SKU 는 바꾸지 않는다).
func (SQLImport) UpdateItem(ctx context.Context, q contract.Tx, it domain.Item) error {
	if _, err := q.ExecContext(ctx, `UPDATE items SET name = ?, unit = ?, safety_stock = ?, track_lot = ? WHERE id = ?`, it.Name, it.Unit, it.SafetyStock, boolInt(it.TrackLot), it.ID); err != nil {
		return mapErr(err)
	}
	return changes.Note(ctx, q, changes.ResItem, it.ID, changes.OpUpdated, "")
}

// VendorRow 는 거래처 한 건과 결제 조건(비어 있으면 0 이고 Terms 가 거짓)이다.
type VendorRow struct {
	domain.Vendor
	PaymentDays int
	HasTerms    bool
}

// VendorsAll 은 모든 거래처(계좌번호는 풀어서)와 결제 조건이다.
func (SQLImport) VendorsAll(ctx context.Context, q contract.Tx) ([]VendorRow, error) {
	rows, err := q.QueryContext(ctx, `SELECT id, name, business_no, account_no, phone, email, payment_days FROM vendors ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []VendorRow
	for rows.Next() {
		var v VendorRow
		var days sql.NullInt64
		if err := rows.Scan(&v.ID, &v.Name, &v.BusinessNo, &v.AccountNo, &v.Phone, &v.Email, &days); err != nil {
			return nil, err
		}
		v.AccountNo = OpenAccount(v.AccountNo)
		v.PaymentDays, v.HasTerms = int(days.Int64), days.Valid
		out = append(out, v)
	}
	return out, rows.Err()
}

// UpdateVendor 는 거래처의 이름·사업자번호·연락처를 바꾸고, 계좌번호는 새 값이 있을 때만 바꾼다(봉해서 저장).
func (SQLImport) UpdateVendor(ctx context.Context, q contract.Tx, v domain.Vendor, replaceAccount bool) error {
	if replaceAccount {
		sealed, err := SealAccount(v.AccountNo)
		if err != nil {
			return err
		}
		if _, err = q.ExecContext(ctx, `UPDATE vendors SET name = ?, business_no = ?, account_no = ?, phone = ?, email = ? WHERE id = ?`, v.Name, v.BusinessNo, sealed, v.Phone, v.Email, v.ID); err != nil {
			return mapErr(err)
		}
		return changes.Note(ctx, q, changes.ResVendor, v.ID, changes.OpUpdated, "")
	}
	if _, err := q.ExecContext(ctx, `UPDATE vendors SET name = ?, business_no = ?, phone = ?, email = ? WHERE id = ?`, v.Name, v.BusinessNo, v.Phone, v.Email, v.ID); err != nil {
		return mapErr(err)
	}
	return changes.Note(ctx, q, changes.ResVendor, v.ID, changes.OpUpdated, "")
}

// DepartmentByName 은 부서 이름(정확히 같은 것)의 번호다.
func (SQLImport) DepartmentByName(ctx context.Context, q contract.Tx, name string) (int64, bool, error) {
	var id int64
	err := q.QueryRowContext(ctx, `SELECT id FROM departments WHERE name = ?`, name).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	return id, err == nil, err
}
