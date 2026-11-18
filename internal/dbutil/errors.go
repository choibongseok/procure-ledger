package dbutil

import (
	"errors"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
)

// SQLSTATE 코드(PostgreSQL). SQLite 는 오류 문구로 판별한다(드라이버가 코드를 문자열로 주기 때문에 기존 코드와 같은 방식).
const (
	sqlstateUnique       = "23505"
	sqlstateForeignKey   = "23503"
	sqlstateCheck        = "23514"
	sqlstateNotNull      = "23502"
	sqlstateRaise        = "P0001" // plpgsql RAISE EXCEPTION (트리거 보호 장치)
	sqlstateSerialize    = "40001"
	sqlstateDeadlock     = "40P01"
	sqlstateLockTimeout  = "55P03"
	sqlstateQueryTimeout = "57014"
)

func pgCode(err error) string {
	var pe *pgconn.PgError
	if errors.As(err, &pe) {
		return pe.Code
	}
	return ""
}

func has(err error, sub string) bool { return err != nil && strings.Contains(err.Error(), sub) }

// IsUnique 는 유일 제약 위반인지다(SQLite "UNIQUE constraint failed", PG 23505).
func IsUnique(err error) bool {
	return pgCode(err) == sqlstateUnique || has(err, "UNIQUE constraint failed")
}

// IsForeignKey 는 외래 키 위반인지다.
func IsForeignKey(err error) bool {
	return pgCode(err) == sqlstateForeignKey || has(err, "FOREIGN KEY constraint failed")
}

// IsCheck 는 CHECK 제약 위반인지다.
func IsCheck(err error) bool {
	return pgCode(err) == sqlstateCheck || has(err, "CHECK constraint failed")
}

// IsNotNull 은 NOT NULL 위반인지다.
func IsNotNull(err error) bool {
	return pgCode(err) == sqlstateNotNull || has(err, "NOT NULL constraint failed")
}

// IsGuard 는 트리거 보호 장치(RAISE)가 msg 를 담아 중단한 오류인지다. 두 방언의 트리거 메시지는 글자까지 같아야 한다(B37 AC-1712).
// PG 는 RAISE EXCEPTION 이 SQLSTATE P0001 이고 메시지가 오류 문자열에 들어 있다.
func IsGuard(err error, msg string) bool {
	if err == nil {
		return false
	}
	if pgCode(err) == sqlstateRaise {
		var pe *pgconn.PgError
		return errors.As(err, &pe) && strings.Contains(pe.Message, msg)
	}
	return has(err, msg)
}

// IsRetryable 은 트랜잭션을 처음부터 다시 하면 성공할 수 있는 PG 오류(직렬화 실패 40001, 교착 40P01)인지다.
func IsRetryable(err error) bool {
	c := pgCode(err)
	return c == sqlstateSerialize || c == sqlstateDeadlock
}

// IsLockTimeout 은 잠금 대기 시간 초과(55P03)나 문장 시간 초과(57014)인지다.
func IsLockTimeout(err error) bool {
	c := pgCode(err)
	return c == sqlstateLockTimeout || c == sqlstateQueryTimeout
}

// IsUndefinedColumn 은 없는 열을 쓴 오류인지다(PG SQLSTATE 42703). SQLite 에는 해당하는 판별이 쓰이지 않는다.
func IsUndefinedColumn(err error) bool { return pgCode(err) == "42703" }
