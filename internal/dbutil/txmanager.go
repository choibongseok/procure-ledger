package dbutil

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
)

// ErrBusy 는 잠금 대기 초과·교착/직렬화 실패 재시도 초과를 뜻한다. 호출자는 503 "서버가 바빠 잠시 뒤 다시 시도해 주세요"로 바꾼다.
// (인증 패키지의 같은 이름 오류와 통합할지는 구현 3·4단계에서 정한다.)
var ErrBusy = errors.New("서버가 바빠 잠시 뒤 다시 시도해 주세요")

// NewTxManager 는 방언에 맞는 TxManager 를 만든다.
// SQLite 는 기존 contract.SQLTxManager 그대로다(동작 불변: 연결 하나가 쓰기를 직렬화한다).
// PostgreSQL 은 READ COMMITTED 트랜잭션과 교착/직렬화 실패 재시도를 더한 PGTxManager 다.
func NewTxManager(db *sql.DB, d Dialect) contract.TxManager {
	if d != nil && d.Kind() == KindPostgres {
		return PGTxManager{DB: db}
	}
	return contract.SQLTxManager{DB: db}
}

// PGTxManager 는 PostgreSQL 용 TxManager 다(B37 7.1, D4).
//   - 기본 격리는 READ COMMITTED 다. 감사 해시 체인은 "잠금을 잡은 뒤 최신 커밋된 마지막 행"을 읽어야 하므로 스냅샷 격리를 기본으로 쓰지 않는다.
//   - SQLSTATE 40001(직렬화 실패)·40P01(교착)이면 fn 을 새 트랜잭션에서 처음부터 다시 실행한다(최대 MaxRetries 번 다시, 기본 3).
//     그래서 fn 은 DB 쓰기 외의 부작용(메일·LLM·HTTP)이 없어야 한다.
//   - 재시도를 넘기거나 잠금 대기 시간 초과(55P03)면 ErrBusy 를 감싸 돌려준다.
type PGTxManager struct {
	DB         *sql.DB
	MaxRetries int                 // 0 이면 3
	Backoff    []time.Duration     // 비면 20ms, 60ms, 180ms
	Sleep      func(time.Duration) // 시험에서 대기를 건너뛴다. nil 이면 time.Sleep
}

var _ contract.TxManager = PGTxManager{}

// WithTx 는 contract.TxManager 를 구현한다(READ COMMITTED).
func (m PGTxManager) WithTx(ctx context.Context, fn func(tx contract.Tx) error) error {
	return m.WithTxIso(ctx, sql.LevelReadCommitted, fn)
}

// WithTxIso 는 격리 수준을 지정한다. 이번 범위에서는 쓰는 곳이 없다(특정 흐름만 SERIALIZABLE 로 올릴 자리).
func (m PGTxManager) WithTxIso(ctx context.Context, iso sql.IsolationLevel, fn func(tx contract.Tx) error) error {
	retries := m.MaxRetries
	if retries <= 0 {
		retries = 3
	}
	backoff := m.Backoff
	if len(backoff) == 0 {
		backoff = []time.Duration{20 * time.Millisecond, 60 * time.Millisecond, 180 * time.Millisecond}
	}
	sleep := m.Sleep
	if sleep == nil {
		sleep = time.Sleep
	}
	var err error
	for attempt := 0; ; attempt++ {
		err = m.once(ctx, iso, fn)
		if err == nil {
			return nil
		}
		if IsLockTimeout(err) {
			return fmt.Errorf("%w: %v", ErrBusy, err)
		}
		if !IsRetryable(err) {
			return err
		}
		if attempt >= retries {
			return fmt.Errorf("%w: 재시도 %d회 초과: %v", ErrBusy, retries, err)
		}
		d := backoff[len(backoff)-1]
		if attempt < len(backoff) {
			d = backoff[attempt]
		}
		sleep(d)
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
}

func (m PGTxManager) once(ctx context.Context, iso sql.IsolationLevel, fn func(tx contract.Tx) error) (err error) {
	tx, err := m.DB.BeginTx(ctx, &sql.TxOptions{Isolation: iso})
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
