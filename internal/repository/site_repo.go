package repository

import (
	"context"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
)

// 사업장 읽기(B44 3단계). 사업장 등록·수정은 법인 관리(internal/entity)가 맡고, 여기서는 선택기와 부가세 집계가 읽기만 한다.

type SQLSites struct{}

// Sites 는 법인의 사업장이다(사업장 번호 순). activeOnly 면 활성만.
func (SQLSites) Sites(ctx context.Context, q contract.Tx, entityID int64, activeOnly bool) ([]domain.SiteInfo, error) {
	query := `SELECT id, site_no, name, kind, vat_mode FROM entity_sites WHERE entity_id = ?`
	if activeOnly {
		query += ` AND active = 1`
	}
	rows, err := q.QueryContext(ctx, query+` ORDER BY site_no`, entityID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.SiteInfo
	for rows.Next() {
		var s domain.SiteInfo
		if err := rows.Scan(&s.ID, &s.SiteNo, &s.Name, &s.Kind, &s.VATMode); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
