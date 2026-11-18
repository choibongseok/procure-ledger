// Package repository 는 erp 업무 저장소다(SQLite). 서비스는 인터페이스만 본다.
// 모든 메서드는 contract.Tx 를 받는다: 쓰기는 서비스의 WithTx 가 넘긴 트랜잭션, 읽기는 *sql.DB.
// 부서 격리는 scopeDept(0 이면 전체)를 조회 조건절에 넣어 저장소에서도 한 번 더 막는다.
package repository

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/dbutil"
)

const timeLayout = time.RFC3339

func fmtTime(t time.Time) string { return t.UTC().Format(timeLayout) }

func parseTime(s string) time.Time {
	t, err := time.Parse(timeLayout, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

// nullID 는 0 을 NULL 로 바꾼다.
func nullID(id int64) any {
	if id == 0 {
		return nil
	}
	return id
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// mapErr 는 SQLite 제약 오류를 계약 오류로 옮긴다(서비스 검사를 뚫고 온 최후 방어선).
func mapErr(err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "PO 수량을 넘는 입고"):
		return contract.ErrOverReceipt
	case strings.Contains(msg, "본인이 올린 요청은 결재할 수 없습니다"):
		return contract.ErrSelfApproval
	case strings.Contains(msg, "사용 중지된 창고에는 들일 수 없습니다"):
		return fmt.Errorf("%w: 사용 중지된 창고에는 들일 수 없습니다", contract.ErrInvalidState)
	case strings.Contains(msg, "마감된 월입니다"):
		return &MonthClosedError{} // 마감 월 쓰기 잠금 트리거(0026)가 중단한 경우도 서비스 검사와 같은 오류로 돌려준다
	case strings.Contains(msg, "UNIQUE constraint failed") || dbutil.IsUnique(err): // SQLite 와 PostgreSQL 의 유일 제약 위반
		return errors.Join(contract.ErrValidation, errors.New("이미 등록된 값입니다"))
	}
	return err
}

// likePattern 은 LIKE 검색어의 와일드카드를 이스케이프한다(ESCAPE '\').
func likePattern(q string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return "%" + r.Replace(q) + "%"
}

// tmpNo 는 상신 전 임시 번호("TMP-" + 무작위 16자)다. 두 방언이 같게 쓰도록 DB 함수(randomblob 등) 대신 Go 에서 만든다.
func tmpNo() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return "TMP-" + hex.EncodeToString(b[:])
}
