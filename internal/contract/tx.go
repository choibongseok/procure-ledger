package contract

import (
	"context"
	"database/sql"
	"fmt"
)

// Tx 는 저장소·감사 기록기가 쓰는 트랜잭션 핸들이다. *sql.Tx 가 그대로 만족한다.
// 쓰기 요청과 감사 기록은 같은 Tx 로 묶는다(audit.Recorder.Record 가 Tx 를 받는 이유).
type Tx interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// TxManager 는 fn 을 한 트랜잭션에서 실행한다. fn 이 오류를 돌려주거나 패닉하면 롤백한다.
// 서비스의 쓰기 메서드는 "비즈니스 변경 + 감사 기록"을 한 WithTx 안에서 하고,
// 감사 기록이 실패하면 오류를 그대로 돌려 전체를 롤백한다.
type TxManager interface {
	WithTx(ctx context.Context, fn func(tx Tx) error) error
}

// SQLTxManager 는 database/sql 기반 TxManager 다. (구현: platform, 골격에서 제공)
type SQLTxManager struct{ DB *sql.DB }

// WithTx 는 TxManager 를 구현한다.
func (m SQLTxManager) WithTx(ctx context.Context, fn func(tx Tx) error) (err error) {
	tx, err := m.DB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("트랜잭션 시작: %w", err)
	}
	defer func() {
		if r := recover(); r != nil {
			_ = tx.Rollback()
			panic(r)
		}
	}()
	if err = fn(tx); err != nil {
		if rbErr := tx.Rollback(); rbErr != nil {
			return fmt.Errorf("%w (롤백 실패: %v)", err, rbErr)
		}
		return err
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("커밋: %w", err)
	}
	return nil
}
