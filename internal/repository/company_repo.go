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

// SQLCompany 는 회사 설정 저장소다(B34). 설정 표(company_settings·company_assets)를 읽는 SQL 은 이 파일에만 둔다(소스 검사).
type SQLCompany struct{}

// Get 은 회사 정보다. 한 번도 저장하지 않았으면 Configured=false 인 빈 값이다.
func (SQLCompany) Get(ctx context.Context, q contract.Tx) (domain.CompanyInfo, error) {
	var in domain.CompanyInfo
	var at string
	// 기본 법인의 회사 정보는 company_settings 가 정본이고, 다른 법인의 것은 법인 관리의 법인 행이다(B44, 1단계 동기화 트리거).
	query := `SELECT name, business_no, representative, address, phone, version, updated_at FROM company_settings WHERE id = 1`
	var args []any
	if eid := contract.WriteEntityID(ctx); eid != contract.DefaultEntityID {
		query = `SELECT name, business_no, representative, address, phone, version, updated_at FROM entities WHERE id = ?`
		args = []any{eid}
	}
	err := q.QueryRowContext(ctx, query, args...).
		Scan(&in.Name, &in.BusinessNo, &in.Representative, &in.Address, &in.Phone, &in.Version, &at)
	if errors.Is(err, sql.ErrNoRows) {
		err = nil
	} else if err == nil {
		in.Configured = true
		in.UpdatedAt = parseTime(at)
	}
	if err != nil {
		return in, err
	}
	for _, c := range []struct {
		kind string
		dst  *bool
	}{{domain.CompanyAssetLogo, &in.HasLogo}, {domain.CompanyAssetSeal, &in.HasSeal}} {
		var n int
		if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM company_assets WHERE `+contract.SQLEntity(ctx, "company_assets")+` AND kind = ? AND current = 1`, c.kind).Scan(&n); err != nil {
			return in, err
		}
		*c.dst = n > 0
	}
	return in, nil
}

// Save 는 회사 정보를 저장한다. version 은 읽은 시점의 값(처음 저장은 0)이고 다르면 false 다(낙관적 잠금).
func (SQLCompany) Save(ctx context.Context, q contract.Tx, in domain.CompanyInput, version, actor int64, now time.Time) (bool, error) {
	if version == 0 {
		res, err := q.ExecContext(ctx, `INSERT INTO company_settings (id, name, business_no, representative, address, phone, version, updated_by, updated_at)
			VALUES (1, ?, ?, ?, ?, ?, 1, ?, ?) ON CONFLICT DO NOTHING`, in.Name, in.BusinessNo, in.Representative, in.Address, in.Phone, actor, fmtTime(now))
		if err != nil {
			return false, err
		}
		n, _ := res.RowsAffected()
		return n == 1, nil
	}
	res, err := q.ExecContext(ctx, `UPDATE company_settings SET name = ?, business_no = ?, representative = ?, address = ?, phone = ?, version = version + 1, updated_by = ?, updated_at = ?
		WHERE id = 1 AND version = ?`, in.Name, in.BusinessNo, in.Representative, in.Address, in.Phone, actor, fmtTime(now), version)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// SetAsset 은 새 이미지를 현재로 두고 이전 현재 행은 이력으로 남긴다.
func (SQLCompany) SetAsset(ctx context.Context, q contract.Tx, a domain.CompanyAsset, actor int64, now time.Time) error {
	if _, err := q.ExecContext(ctx, `UPDATE company_assets SET current = 0 WHERE `+contract.SQLEntity(ctx, "company_assets")+` AND kind = ? AND current = 1`, a.Kind); err != nil {
		return err
	}
	_, err := q.ExecContext(ctx, `INSERT INTO company_assets (entity_id, kind, content, content_type, size, sha256, width, height, uploaded_by, uploaded_at, current)
		VALUES (`+contract.SQLEntityID(ctx)+`, ?,?,?,?,?,?,?,?,?,1)`, a.Kind, a.Data, a.ContentType, a.Size, a.SHA256, a.Width, a.Height, actor, fmtTime(now))
	return err
}

// ---- 회계 설정(B34 단계 2) ----

// FiscalStart 는 회계연도 시작월이다(설정이 없으면 1 = 역년).
func (SQLCompany) FiscalStart(ctx context.Context, q contract.Tx) (month int, version int64, configured bool, err error) {
	if eid := contract.WriteEntityID(ctx); eid != contract.DefaultEntityID {
		err = q.QueryRowContext(ctx, `SELECT fiscal_start_month, version FROM entities WHERE id = ?`, eid).Scan(&month, &version)
	} else {
		err = q.QueryRowContext(ctx, `SELECT fiscal_start_month, version FROM company_settings WHERE id = 1`).Scan(&month, &version)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return 1, 0, false, nil
	}
	return month, version, err == nil, err
}

// SetFiscalStart 는 시작월을 바꾼다. version 이 다르면 false 다.
func (SQLCompany) SetFiscalStart(ctx context.Context, q contract.Tx, month int, version, actor int64, now time.Time) (bool, error) {
	res, err := q.ExecContext(ctx, `UPDATE company_settings SET fiscal_start_month = ?, version = version + 1, updated_by = ?, updated_at = ? WHERE id = 1 AND version = ?`,
		month, actor, fmtTime(now), version)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// ClosedMonthExists 는 마감된 월이 하나라도 있는지다(월 마감 읽기는 SQLMonthClose 를 거친다).
func (SQLCompany) ClosedMonthExists(ctx context.Context, q contract.Tx) (bool, error) {
	all, err := SQLMonthClose{}.All(ctx, q)
	for _, m := range all {
		if m.Status == "closed" {
			return true, err
		}
	}
	return false, err
}

// MonthClosed 는 그 월(YYYY-MM)이 마감되었는지다.
func (SQLCompany) MonthClosed(ctx context.Context, q contract.Tx, month string) (bool, error) {
	rec, ok, err := SQLMonthClose{}.Get(ctx, q, month)
	return ok && rec.Status == "closed", err
}

// VATRateOn 은 그 날(YYYY-MM-DD)에 유효한 세율이다: 취소되지 않은 효력일 중 그 날 이하의 가장 최근 것. 이력이 비어 있으면 10(현행).
func (SQLCompany) VATRateOn(ctx context.Context, q contract.Tx, day string) (int, error) {
	var pct int
	err := q.QueryRowContext(ctx, `SELECT rate_percent FROM vat_rates WHERE canceled = 0 AND effective_from <= ? ORDER BY effective_from DESC LIMIT 1`, day).Scan(&pct)
	if errors.Is(err, sql.ErrNoRows) {
		return 10, nil
	}
	return pct, err
}

// VATRates 는 세율 이력(효력일 내림차순)이다.
func (SQLCompany) VATRates(ctx context.Context, q contract.Tx) ([]domain.VATRate, error) {
	rows, err := q.QueryContext(ctx, `SELECT v.id, v.effective_from, v.rate_percent, COALESCE(u.name,''), v.created_at, v.canceled
		FROM vat_rates v LEFT JOIN users u ON u.id = v.created_by ORDER BY v.effective_from DESC, v.id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.VATRate{}
	for rows.Next() {
		var r domain.VATRate
		var at string
		var canceled int
		if err := rows.Scan(&r.ID, &r.EffectiveFrom, &r.Percent, &r.CreatedBy, &at, &canceled); err != nil {
			return nil, err
		}
		r.CreatedAt, r.Canceled = parseTime(at), canceled == 1
		out = append(out, r)
	}
	return out, rows.Err()
}

// VATRate 는 세율 한 줄이다. 없으면 contract.ErrNotFound.
func (SQLCompany) VATRate(ctx context.Context, q contract.Tx, id int64) (domain.VATRate, error) {
	var r domain.VATRate
	var canceled int
	err := q.QueryRowContext(ctx, `SELECT id, effective_from, rate_percent, canceled FROM vat_rates WHERE id = ?`, id).Scan(&r.ID, &r.EffectiveFrom, &r.Percent, &canceled)
	if errors.Is(err, sql.ErrNoRows) {
		return r, contract.ErrNotFound
	}
	r.Canceled = canceled == 1
	return r, err
}

// VATRateDuplicate 는 취소되지 않은 같은 효력일이 있는지다.
func (SQLCompany) VATRateDuplicate(ctx context.Context, q contract.Tx, effectiveFrom string) (bool, error) {
	var n int
	err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM vat_rates WHERE effective_from = ? AND canceled = 0`, effectiveFrom).Scan(&n)
	return n > 0, err
}

func (SQLCompany) InsertVATRate(ctx context.Context, q contract.Tx, effectiveFrom string, percent int, actor int64, now time.Time) (int64, error) {
	id, err := dbutil.InsertID(ctx, q, `INSERT INTO vat_rates (effective_from, rate_percent, created_by, created_at) VALUES (?,?,?,?)`, effectiveFrom, percent, actor, fmtTime(now))
	if err != nil {
		return 0, err
	}
	return id, nil
}

func (SQLCompany) CancelVATRate(ctx context.Context, q contract.Tx, id, actor int64, now time.Time) error {
	_, err := q.ExecContext(ctx, `UPDATE vat_rates SET canceled = 1, canceled_by = ?, canceled_at = ? WHERE id = ? AND canceled = 0`, actor, fmtTime(now), id)
	return err
}

// ---- 영업일 달력(B34 단계 3) ----

// Holidays 는 그 해의 공휴일(날짜 순)이다.
func (SQLCompany) Holidays(ctx context.Context, q contract.Tx, year int) ([]domain.Holiday, error) {
	rows, err := q.QueryContext(ctx, `SELECT h.date, h.name, COALESCE(u.name,'') FROM holidays h LEFT JOIN users u ON u.id = h.created_by
		WHERE h.date >= ? AND h.date <= ? ORDER BY h.date`, fmt.Sprintf("%04d-01-01", year), fmt.Sprintf("%04d-12-31", year))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.Holiday{}
	for rows.Next() {
		var h domain.Holiday
		if err := rows.Scan(&h.Date, &h.Name, &h.CreatedBy); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// IsHoliday 는 그 날이 등록된 공휴일인지다.
func (SQLCompany) IsHoliday(ctx context.Context, q contract.Tx, day string) (bool, error) {
	var n int
	err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM holidays WHERE date = ?`, day).Scan(&n)
	return n > 0, err
}

// InsertHoliday 는 공휴일을 등록한다. 이미 있으면 false.
func (SQLCompany) InsertHoliday(ctx context.Context, q contract.Tx, date, name string, actor int64, now time.Time) (bool, error) {
	res, err := q.ExecContext(ctx, `INSERT INTO holidays (date, name, created_by, created_at) VALUES (?,?,?,?) ON CONFLICT DO NOTHING`, date, name, actor, fmtTime(now))
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

func (SQLCompany) DeleteHoliday(ctx context.Context, q contract.Tx, date string) (string, bool, error) {
	var name string
	err := q.QueryRowContext(ctx, `SELECT name FROM holidays WHERE date = ?`, date).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	_, err = q.ExecContext(ctx, `DELETE FROM holidays WHERE date = ?`, date)
	return name, true, err
}

// Asset 은 종류별 현재 이미지다. 없으면 contract.ErrNotFound.
func (SQLCompany) Asset(ctx context.Context, q contract.Tx, kind string) (domain.CompanyAsset, error) {
	a := domain.CompanyAsset{Kind: kind}
	err := q.QueryRowContext(ctx, `SELECT content, content_type, size, sha256, width, height FROM company_assets WHERE `+contract.SQLEntity(ctx, "company_assets")+` AND kind = ? AND current = 1`, kind).
		Scan(&a.Data, &a.ContentType, &a.Size, &a.SHA256, &a.Width, &a.Height)
	if errors.Is(err, sql.ErrNoRows) {
		return a, contract.ErrNotFound
	}
	return a, err
}
