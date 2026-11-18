package setup

import (
	"context"
	"database/sql"
	"errors"
	"log"
	"time"
)

// 설치 상태 값(setup_state.status).
const (
	StatusPending = "pending"
	StatusDone    = "done"
)

type queryRower interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// Status 는 설치 상태다. 행이 없으면(비정상) done 으로 본다: 마법사를 여는 쪽이 위험하므로 읽지 못하면 닫는다.
func Status(ctx context.Context, q queryRower) (string, error) {
	var s string
	err := q.QueryRowContext(ctx, `SELECT status FROM setup_state WHERE id = 1`).Scan(&s)
	if errors.Is(err, sql.ErrNoRows) {
		return StatusDone, nil
	}
	if err != nil {
		return StatusDone, err
	}
	return s, nil
}

// Reconcile 은 기동 때 설치 상태를 확정한다(명세 4.1-3): 사용자가 한 명이라도 있는데 pending 이면 done 으로 고친다
// (수동 INSERT 로 첫 관리자를 만든 환경, dev IdP 시드 환경 보호). 바꿨으면 true 다. 두 프로세스가 동시에 불러도 안전하다(조건부 갱신).
func Reconcile(ctx context.Context, db *sql.DB, now func() time.Time) (bool, error) {
	if now == nil {
		now = time.Now
	}
	res, err := db.ExecContext(ctx, `UPDATE setup_state SET status = 'done', completed_at = ? WHERE id = 1 AND status = 'pending' AND EXISTS (SELECT 1 FROM users)`,
		now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	if n > 0 {
		log.Printf("setup: 사용자가 이미 있어 초기 설정을 완료한 것으로 처리했습니다(마법사는 열리지 않습니다)")
	}
	return n > 0, nil
}
