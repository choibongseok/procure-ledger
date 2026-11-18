package dbutil

import (
	"context"
	"hash/fnv"
	"sync/atomic"

	"github.com/choibongseok/procure-ledger/internal/contract"
)

// 명시적 잠금 지점(B37 7.2). SQLite 는 연결 하나가 쓰기를 직렬화하므로 모두 아무것도 하지 않는다.
// PostgreSQL 에서는 트랜잭션이 끝날 때까지 유지되는 advisory lock 으로 직렬화가 필요한 곳을 보호한다.
// 이 단계(1단계)는 자리와 도우미만 만들고, 호출 지점(감사 체인·번호 발급·월 마감·입고)에 연결하는 것은 4단계다.

// LockName 은 advisory lock 이름이다. 이름 문자열의 64비트 해시를 키로 쓴다.
type LockName string

const (
	// LockAuditChain 은 감사 해시 체인에 기록을 붙이는 구간을 직렬화한다(S1).
	LockAuditChain LockName = "procure-ledger:audit-chain"
	// LockSetupComplete 는 설치 마법사의 완료 트랜잭션을 직렬화한다(B38). 동시에 온 완료 요청 중 하나만 일하고 나머지는 끝난 뒤 done 을 보고 물러난다.
	LockSetupComplete LockName = "procure-ledger:setup-complete"
	// LockLLMQuota 는 LLM 게이트웨이의 일일 한도 확인·예약 구간을 직렬화한다(읽은 사용량 위에 예약을 쌓으므로 동시 예약이 한도를 넘지 않게).
	LockLLMQuota LockName = "procure-ledger:llm-quota"
	// LockChangeFeed 는 변경 피드(change_events)에 줄을 붙이는 구간을 직렬화한다(B42): 번호 순서 = 커밋 순서라 커서 소비자가 줄을 놓치지 않는다.
	LockChangeFeed LockName = "procure-ledger:change-feed"
	// LockServerInstance 는 한 데이터베이스에 서버가 하나만 뜨게 한다(세션 수준, 7.4).
	LockServerInstance LockName = "procure-ledger:server-instance"
)

// LockKey 는 이름의 64비트 키다(고정된 FNV-1a).
func LockKey(n LockName) int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(n))
	return int64(h.Sum64())
}

// AcquireTxLock 은 PG 에서 트랜잭션 수준 advisory lock 을 잡는다(커밋·롤백에 풀린다. 같은 트랜잭션에서 다시 불러도 안전하다).
// SQLite 에서는 아무 일도 하지 않는다.
func AcquireTxLock(ctx context.Context, tx contract.Tx, d Dialect, n LockName) error {
	if d == nil || !d.HasExplicitLocks() {
		return nil
	}
	_, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(?)`, LockKey(n))
	return err
}

// Active 는 이 프로세스가 연 데이터베이스의 방언이다(기본 SQLite, OpenPG 가 성공하면 PostgreSQL). 한 프로세스는 한 종류의 DB 만 쓴다.
// 서비스·저장소가 잠금 구문을 넣을지 가릴 때만 쓴다(SQL 조각은 가능하면 공통 부분집합으로 쓰고, 방언 분기를 늘리지 않는다).
type dialectBox struct{ d Dialect }

var active atomic.Value // dialectBox

func Active() Dialect {
	if b, ok := active.Load().(dialectBox); ok {
		return b.d
	}
	return SQLite
}

// SetActive 는 방언을 정한다(OpenPG 가 부른다).
func SetActive(d Dialect) { active.Store(dialectBox{d}) }

// monthLockKey 는 월 마감 잠금의 PG 쪽 키 식이다. 트리거 함수(pgdialect)가 같은 식을 쓴다: hashtextextended('procure-ledger:month:'||월, 0).
const monthLockExpr = `hashtextextended('procure-ledger:month:' || ?, 0)`

var skipLocks atomic.Bool

// SkipLocksForTest 는 월 마감·키 잠금(S3·S5)을 건너뛰게 한다. "잠금을 뺀 틀린 구현"이 병렬 시험에 걸리는지 보이려는 시험 전용 장치이며
// 운영 코드는 부르지 않는다. 돌려주는 함수로 되돌린다.
func SkipLocksForTest() (restore func()) {
	skipLocks.Store(true)
	return func() { skipLocks.Store(false) }
}

// AcquireMonthClose 는 그 달 마감·해제 트랜잭션이 월 행 쓰기를 모두 기다리고 막도록 배타 잠금을 잡는다(S3, PG 전용·커밋까지).
// 그 달에 쓰는 쪽은 트리거 함수가 같은 키의 공유 잠금을 잡으므로, 마감은 진행 중인 쓰기가 끝난 뒤에 시작하고 이후의 쓰기는 마감 결과를 본다.
func AcquireMonthClose(ctx context.Context, tx contract.Tx, month string) error {
	if !Active().HasExplicitLocks() || skipLocks.Load() {
		return nil
	}
	_, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(`+monthLockExpr+`)`, month)
	return err
}

// AcquireKeyLock 은 임의 키(예: 재고 줄 "stock:창고:품목", 예산 "budget:부서:월")의 배타 잠금을 트랜잭션 끝까지 잡는다(S5, PG 전용).
// 읽고 판단하고 쓰는 사이에 같은 대상에 끼어드는 쓰기를 줄 세운다. 잠금 순서는 월 → 업무 대상(교착 방지).
func AcquireKeyLock(ctx context.Context, tx contract.Tx, key string) error {
	if !Active().HasExplicitLocks() || skipLocks.Load() {
		return nil
	}
	_, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('procure-ledger:' || ?, 0))`, key)
	return err
}

// SyncSequence 는 id 를 직접 넣은 뒤 PG 의 식별 열 시퀀스를 그 표의 최대 id 로 올린다(안 하면 다음 자동 번호가 직접 넣은 id 와 겹친다).
// SQLite 는 최대 rowid + 1 을 쓰므로 할 일이 없다. table 은 코드 상수만 넘긴다.
func SyncSequence(ctx context.Context, tx contract.Tx, table string) error {
	if !Active().HasExplicitLocks() {
		return nil
	}
	_, err := tx.ExecContext(ctx, `SELECT setval(pg_get_serial_sequence('`+table+`', 'id'), GREATEST((SELECT COALESCE(MAX(id), 0) FROM `+table+`), 1))`)
	return err
}

// InsertID 는 `INSERT ... ` 문 q 에 RETURNING id 를 붙여 새 행의 id 를 돌려준다(LastInsertId 대체; SQLite 3.35+ 와 PG 공통).
// q 는 RETURNING 이 없는 단일 INSERT 문이어야 한다.
func InsertID(ctx context.Context, tx contract.Tx, q string, args ...any) (int64, error) {
	var id int64
	if err := tx.QueryRowContext(ctx, q+ReturningID, args...).Scan(&id); err != nil {
		return 0, err
	}
	return id, nil
}
