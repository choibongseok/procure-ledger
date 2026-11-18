package backup

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/choibongseok/procure-ledger/internal/audit"
	caudit "github.com/choibongseok/procure-ledger/internal/contract/audit"
)

// RunRecord 는 backup_runs 한 행과 짝이 되는 감사 한 건이다.
type RunRecord struct {
	Kind      string // backup | restore | rehearsal
	Result    string // success | failed
	SetName   string
	DBKind    string
	ErrorCode string
	Bytes     int64
	ChainSeq  int64
	Started   time.Time
	Finished  time.Time
	Action    string   // system.backup 등
	Warnings  []string // 경고 항목(업무 불변식 위반 등). 있으면 backup_runs 오류 코드 칸이 아니라 감사 상세에 남긴다
	Detail    string   // 자유 텍스트(비밀·개인정보 금지)
}

type rowQuerier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func tableExists(ctx context.Context, q rowQuerier, name string) bool {
	var n int
	if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name = ?`, name).Scan(&n); err == nil {
		return n > 0
	}
	if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM information_schema.tables WHERE table_name = ?`, name).Scan(&n); err == nil {
		return n > 0
	}
	return false
}

// RecordRun 은 라이브 DB 에 실행 기록(backup_runs)과 감사를 한 트랜잭션으로 남긴다. backup_runs 표가 아직 없으면(마이그레이션 전의 DB) 감사만 남긴다.
// 호출한 쪽은 실패를 경고로만 다룬다: 이미 끝난 백업·복구를 이 기록 때문에 실패로 만들지 않는다.
func RecordRun(ctx context.Context, db *sql.DB, r RunRecord) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if tableExists(ctx, tx, "backup_runs") {
		if _, err := tx.ExecContext(ctx, `INSERT INTO backup_runs (kind, started_at, finished_at, result, set_name, db_kind, bytes, chain_seq, error_code, tool_version, warnings)
			VALUES (?,?,?,?,?,?,?,?,?,?,?)`, r.Kind, r.Started.UTC().Format(time.RFC3339), r.Finished.UTC().Format(time.RFC3339), r.Result, r.SetName, r.DBKind,
			r.Bytes, r.ChainSeq, r.ErrorCode, ToolVersion, strings.Join(r.Warnings, "; ")); err != nil {
			return fmt.Errorf("backup_runs 기록 실패: %w", err)
		}
	}
	res := caudit.ResultSuccess
	detail := r.Detail
	if len(r.Warnings) > 0 {
		detail += fmt.Sprintf(" / 경고 %d건: %s", len(r.Warnings), strings.Join(r.Warnings, "; "))
	}
	if r.Result != "success" {
		res = caudit.ResultError
	}
	target := r.SetName
	if target == "" {
		target = "-"
	}
	if err := audit.New(db).Record(ctx, tx, caudit.Entry{Actor: Actor, Action: r.Action, TargetType: caudit.TargetSystem, TargetID: target, Result: res, Detail: detail}); err != nil {
		return err
	}
	return tx.Commit()
}
