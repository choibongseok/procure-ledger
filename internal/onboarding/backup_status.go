package onboarding

import (
	"context"
	"database/sql"
	"errors"

	"github.com/choibongseok/procure-ledger/internal/contract"
)

// Run 은 backup_runs 한 행이다. 세트 이름·크기·체인 머리 번호만 보이고 경로·호스트·비밀은 없다.
type Run struct {
	ID         int64  `json:"id"`
	Kind       string `json:"kind"` // backup | restore | rehearsal
	StartedAt  string `json:"startedAt"`
	FinishedAt string `json:"finishedAt"`
	Result     string `json:"result"` // success | failed
	SetName    string `json:"setName"`
	DBKind     string `json:"dbKind"`
	Bytes      int64  `json:"bytes"`
	ChainSeq   int64  `json:"chainSeq"`
	ErrorCode  string `json:"errorCode,omitempty"`
}

// BackupStatus 는 백업 상태 화면의 내용이다(읽기 전용: 실행 버튼은 없다. 백업·복구는 서버 호스트의 erp-admin 으로만 한다).
type BackupStatus struct {
	// Last 는 종류별 마지막 성공 기록이다(없으면 키가 없다).
	Last map[string]Run `json:"last"`
	// LastFailed 는 종류별로 마지막 성공보다 나중에 실패한 기록이다(있으면 화면이 경고한다).
	LastFailed map[string]Run `json:"lastFailed"`
	// Recent 는 최근 기록 20건(새 것부터)이다.
	Recent []Run `json:"recent"`
	// Available 는 backup_runs 표가 읽히는지다(마이그레이션 전 DB 면 false).
	Available bool `json:"available"`
}

const runCols = `id, kind, started_at, finished_at, result, set_name, db_kind, bytes, chain_seq, error_code`

func scanRun(sc interface{ Scan(...any) error }) (Run, error) {
	var r Run
	err := sc.Scan(&r.ID, &r.Kind, &r.StartedAt, &r.FinishedAt, &r.Result, &r.SetName, &r.DBKind, &r.Bytes, &r.ChainSeq, &r.ErrorCode)
	return r, err
}

// BackupStatus 는 마지막 백업·리허설·복구 기록을 모은다(backup:read).
func (s *Service) BackupStatus(ctx context.Context) (BackupStatus, error) {
	if _, err := contract.Authorize(ctx, contract.PermBackupRead); err != nil {
		return BackupStatus{}, err
	}
	out := BackupStatus{Last: map[string]Run{}, LastFailed: map[string]Run{}, Recent: []Run{}}
	if _, err := count(ctx, s.db, `SELECT COUNT(*) FROM backup_runs`); err != nil {
		return out, nil // 표가 없으면(마이그레이션 전) 기록 없음으로 보인다
	}
	out.Available = true
	for _, kind := range []string{"backup", "rehearsal", "restore"} {
		r, err := scanRun(s.db.QueryRowContext(ctx, `SELECT `+runCols+` FROM backup_runs WHERE kind = ? AND result = 'success' ORDER BY id DESC LIMIT 1`, kind))
		switch {
		case err == nil:
			out.Last[kind] = r
		case !errors.Is(err, sql.ErrNoRows):
			return out, err
		}
		after := int64(0)
		if r.ID != 0 {
			after = r.ID
		}
		f, ferr := scanRun(s.db.QueryRowContext(ctx, `SELECT `+runCols+` FROM backup_runs WHERE kind = ? AND result = 'failed' AND id > ? ORDER BY id DESC LIMIT 1`, kind, after))
		switch {
		case ferr == nil:
			out.LastFailed[kind] = f
		case !errors.Is(ferr, sql.ErrNoRows):
			return out, ferr
		}
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+runCols+` FROM backup_runs ORDER BY id DESC LIMIT 20`)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return out, err
		}
		out.Recent = append(out.Recent, r)
	}
	return out, rows.Err()
}
