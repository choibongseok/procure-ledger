package entity

import (
	"context"
	"database/sql"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/holders"
)

// PermissionHolders 는 그 법인에서 권한이 효력인 활성 사용자다(구현은 internal/holders).
func PermissionHolders(ctx context.Context, q holderQuerier, perm contract.Permission, entityID int64, day contract.Day) ([]int64, error) {
	return holders.Of(ctx, q, perm, entityID, day)
}

type holderQuerier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}
