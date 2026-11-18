package dbutil

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
)

// ErrInstanceBusy 는 같은 데이터베이스를 다른 서버가 이미 쓰고 있다는 뜻이다(B37 7.4).
var ErrInstanceBusy = errors.New("다른 ProcureLedger 서버가 이 데이터베이스를 쓰고 있습니다. 같은 PL_DB_URL 로 서버를 둘 띄울 수 없습니다")

// AcquireInstanceLock 은 PG 에서 서버 단일 인스턴스 잠금을 잡는다: 풀과 따로 붙잡아 둔 전용 연결에서 세션 수준 pg_try_advisory_lock 을 건다.
// 이미 다른 세션이 잡고 있으면 ErrInstanceBusy 를 돌려준다. 연결이 끊기면(프로세스 종료 포함) PG 가 자동으로 푼다.
// 돌려주는 release 는 잠금을 풀고 그 연결을 닫는다. SQLite 는 아무 일도 하지 않는다(파일 잠금과 단일 연결이 이미 있다).
func AcquireInstanceLock(ctx context.Context, db *sql.DB, d Dialect) (release func(), err error) {
	if d == nil || !d.HasExplicitLocks() {
		return func() {}, nil
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("단일 인스턴스 잠금용 연결을 열지 못했습니다: %w", err)
	}
	key := LockKey(LockServerInstance)
	var got bool
	if err := conn.QueryRowContext(ctx, `SELECT pg_try_advisory_lock(?)`, key).Scan(&got); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("단일 인스턴스 잠금을 확인하지 못했습니다: %w", err)
	}
	if !got {
		_ = conn.Close()
		return nil, ErrInstanceBusy
	}
	return func() {
		// 잠금을 풀지 못해도 연결을 풀로 돌려보내지 않고 버려서 세션째 끝낸다.
		_, _ = conn.ExecContext(context.Background(), `SELECT pg_advisory_unlock(?)`, key)
		_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		_ = conn.Close()
	}, nil
}
