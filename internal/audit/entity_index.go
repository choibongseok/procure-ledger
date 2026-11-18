package audit

import (
	"context"
	"database/sql"

	"github.com/choibongseok/procure-ledger/internal/dbutil"
)

// EnsureEntityIndex 는 감사 로그의 법인 색인 (entity_id, seq)을 만든다(B44 명세 13.1.1 보완 4). 마이그레이션은 열만 더하고 색인은 만들지 않는다:
// 감사 표는 체인 잠금 아래 기록되므로 큰 표에서 PG 의 일반 CREATE INDEX 는 색인을 만드는 동안 모든 기록을 막는다. 그래서 별도 단계로 두고
// PG 는 CONCURRENTLY(쓰기를 막지 않음)로, SQLite 는 일반 문장으로 만든다. 이미 있으면 아무 일도 하지 않는다(멱등). 법인 열이 없는 스키마에서는 오류다.
func EnsureEntityIndex(ctx context.Context, db *sql.DB) error {
	if dbutil.DialectOf(db).Kind() == dbutil.KindPostgres {
		_, err := db.ExecContext(ctx, `CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_audit_entity ON audit_logs(entity_id, seq)`)
		return err
	}
	_, err := db.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_audit_entity ON audit_logs(entity_id, seq)`)
	return err
}
