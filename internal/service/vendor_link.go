package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/repository"
)

// 거래처의 연결 법인(B44 명세 5·11절): 거래처가 그룹의 다른 법인이면 그 법인을 연결해 둔다(법인 간 거래 표시의 바탕, 3단계). 연결 법인 번호 0 은 연결 없음이다.
// 사업자번호가 현재 법인 밖의 다른 법인과 같으면 그 법인을 제안한다(제안은 화면 안내일 뿐 자동으로 연결하지 않는다).

// VendorLink 는 거래처 한 곳의 연결 상태다(사업자번호·계좌 등 민감 값은 싣지 않는다).
type VendorLink struct {
	VendorID        int64
	Name            string
	LinkedEntityID  int64
	LinkedEntity    string
	SuggestedEntity int64
	SuggestedName   string
}

// ListVendorLinks 는 거래처별 연결 법인과 제안이다(purchase:read).
func (s *InventoryService) ListVendorLinks(ctx context.Context) ([]VendorLink, error) {
	if _, err := contract.Authorize(ctx, contract.PermPurchaseRead); err != nil {
		return nil, err
	}
	cur := contract.WriteEntityID(ctx)
	rows, err := s.DB.QueryContext(ctx, `SELECT v.id, v.name, v.linked_entity_id, COALESCE(le.name,''),
		COALESCE((SELECT e.id FROM entities e WHERE e.active = 1 AND e.id <> ? AND e.business_no <> '' AND REPLACE(e.business_no,'-','') = REPLACE(v.business_no,'-','') ORDER BY e.id LIMIT 1),0),
		COALESCE((SELECT e.name FROM entities e WHERE e.active = 1 AND e.id <> ? AND e.business_no <> '' AND REPLACE(e.business_no,'-','') = REPLACE(v.business_no,'-','') ORDER BY e.id LIMIT 1),'')
		FROM vendors v LEFT JOIN entities le ON le.id = v.linked_entity_id ORDER BY v.id`, cur, cur)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []VendorLink{}
	for rows.Next() {
		var l VendorLink
		if err := rows.Scan(&l.VendorID, &l.Name, &l.LinkedEntityID, &l.LinkedEntity, &l.SuggestedEntity, &l.SuggestedName); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// SetVendorLink 는 거래처의 연결 법인을 정한다(0 = 해제). 연결 법인은 활성 법인이어야 하고 현재 법인 자신일 수 없다. 감사 vendor.link(법인 번호만).
func (s *InventoryService) SetVendorLink(ctx context.Context, vendorID, entityID int64) (VendorLink, error) {
	p, err := s.authorize(ctx, contract.PermInventoryManage, "vendor.link", audit.TargetVendor)
	if err != nil {
		return VendorLink{}, err
	}
	var out VendorLink
	err = s.write(ctx, p, "vendor.link", audit.TargetVendor, vendorID, func(tx contract.Tx) error {
		name, old, err := (repository.SQLInventory{}).VendorLinkRow(ctx, tx, vendorID)
		if err != nil {
			return err
		}
		if entityID != 0 {
			if entityID == contract.WriteEntityID(ctx) {
				return validation("현재 법인 자신은 연결할 수 없습니다.")
			}
			var active int
			if err := tx.QueryRowContext(ctx, `SELECT active FROM entities WHERE id = ?`, entityID).Scan(&active); errors.Is(err, sql.ErrNoRows) {
				return validation("연결할 법인을 찾을 수 없습니다.")
			} else if err != nil {
				return err
			}
			if active != 1 {
				return validation("비활성 법인은 연결할 수 없습니다.")
			}
		}
		if err := (repository.SQLInventory{}).SetVendorLinkedEntity(ctx, tx, vendorID, entityID); err != nil {
			return err
		}
		out = VendorLink{VendorID: vendorID, Name: name, LinkedEntityID: entityID}
		return s.record(ctx, tx, p, "vendor.link", audit.TargetVendor, vendorID, audit.DiffItem{Key: "linked_entity", Old: strconv.FormatInt(old, 10), New: fmt.Sprint(entityID)})
	})
	return out, err
}
