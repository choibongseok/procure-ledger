package handler

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"strings"

	"github.com/choibongseok/procure-ledger/internal/contract"
)

// auditEntityScope 는 감사 로그를 읽는 법인 범위다(B44 13.1): audit:read 가 효력인 법인 범위(현재 법인이 정해져 있으면 그 법인)에서, 선택 매개변수 entity(법인 코드, "*" 는 범위 전체)로 더 좁힌다.
// 범위 밖 법인을 고르면 403 이다. 저장소는 범위 안의 법인 사건과 법인과 무관한 사건(entity_id = 0)만 돌려준다.
func (h *adminHandlers) auditEntityScope(ctx context.Context, q url.Values) (*contract.EntityScope, error) {
	scope, err := contract.ReadScope(ctx, contract.PermAuditRead)
	if err != nil {
		return nil, err
	}
	code := strings.ToUpper(strings.TrimSpace(q.Get("entity")))
	if code == "" || code == "*" {
		return &scope, nil
	}
	if h.d.DB == nil {
		return &scope, nil
	}
	var id int64
	if err := h.d.DB.QueryRowContext(ctx, `SELECT id FROM entities WHERE code = ?`, code).Scan(&id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, contract.ErrValidation
		}
		return nil, err
	}
	if !scope.All && !scope.Has(id) {
		return nil, contract.ErrForbidden
	}
	return &contract.EntityScope{IDs: []int64{id}}, nil
}
