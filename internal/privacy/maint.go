package privacy

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"strconv"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
)

// 보관 기간(명세 5.8, D3): 기본 730일, 365~3650일. 법정 보관 기간은 회사 규모·정보 종류에 따라 달라 법무 확인이 필요하다.
const (
	DefaultRetentionDays = 730
	MinRetentionDays     = 365
	MaxRetentionDays     = 3650

	retentionKey = "privacy.retention_days"
	digestKey    = "privacy.digest_day"
)

// systemActor 는 서버 주기 작업·명령줄의 감사 행위자다(사용자 없는 시스템 작업, 경로 cli).
var systemActor = contract.Principal{Role: "system", Channel: contract.ChannelCLI}

var kst = time.FixedZone("KST", 9*3600)

type queryer interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func readSetting(ctx context.Context, q queryer, key string) (string, bool, error) {
	var v string
	err := q.QueryRowContext(ctx, `SELECT value FROM app_settings WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return v, err == nil, err
}

func writeSetting(ctx context.Context, tx contract.Tx, key, value string, by int64, now string) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO app_settings (key, value, updated_by, updated_at) VALUES (?,?,?,?)
		ON CONFLICT (key) DO UPDATE SET value = excluded.value, updated_by = excluded.updated_by, updated_at = excluded.updated_at`, key, value, by, now)
	return err
}

// RetentionDays 는 보관 기간(일)이다. 값이 없거나 범위 밖이면 기본값이다.
func (r *Recorder) RetentionDays(ctx context.Context) (int, error) {
	v, ok, err := readSetting(ctx, r.db, retentionKey)
	if err != nil {
		return 0, err
	}
	if !ok {
		return DefaultRetentionDays, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < MinRetentionDays || n > MaxRetentionDays {
		return DefaultRetentionDays, nil
	}
	return n, nil
}

type stateError string

func (e stateError) Error() string      { return string(e) }
func (stateError) Is(target error) bool { return target == contract.ErrInvalidState }

func verr(format string, a ...any) error {
	return fmt.Errorf("%w: %s", contract.ErrValidation, fmt.Sprintf(format, a...))
}

// SetRetention 은 보관 기간을 바꾼다(privacy:log_manage, 로그인 세션만). 즉시 파기하지 않고 다음 실행 때 파기한다. 감사 privacy.retention_set.
func (r *Recorder) SetRetention(ctx context.Context, p contract.Principal, days int) error {
	if _, err := contract.Authorize(contract.WithPrincipal(ctx, p), contract.PermPrivacyLogManage); err != nil {
		return err
	}
	if p.Auth == contract.AuthToken || p.Auth == contract.AuthSCIM {
		return fmt.Errorf("%w: 토큰으로는 보관 기간을 바꿀 수 없습니다(로그인 세션 필요)", contract.ErrForbidden)
	}
	if days < MinRetentionDays || days > MaxRetentionDays {
		return verr("보관 기간은 %d일 이상 %d일 이하여야 합니다.", MinRetentionDays, MaxRetentionDays)
	}
	p.Channel = contract.ChannelWeb
	return r.txs.WithTx(ctx, func(tx contract.Tx) error {
		old := DefaultRetentionDays
		if v, ok, err := readSetting(ctx, tx, retentionKey); err != nil {
			return err
		} else if ok {
			if n, e := strconv.Atoi(v); e == nil {
				old = n
			}
		}
		if err := writeSetting(ctx, tx, retentionKey, strconv.Itoa(days), p.UserID, r.now().UTC().Format(time.RFC3339Nano)); err != nil {
			return err
		}
		return r.audit.Record(ctx, tx, audit.Entry{Actor: p, Action: "privacy.retention_set", TargetType: audit.TargetPrivacyLog, TargetID: "retention", Result: audit.ResultSuccess,
			Diff: []audit.DiffItem{{Key: "retention_days", Old: strconv.Itoa(old), New: strconv.Itoa(days)}}})
	})
}

// PendingPurge 는 지금 보관 기간 기준으로 다음 실행 때 파기될 달(YYYY-MM)과 행 수다.
func (r *Recorder) PendingPurge(ctx context.Context, now time.Time) ([]PurgeItem, error) {
	days, err := r.RetentionDays(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := r.db.QueryContext(ctx, `SELECT substr(at, 1, 7) AS m, COUNT(*) FROM privacy_access_log GROUP BY substr(at, 1, 7) ORDER BY m`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cut := now.UTC().AddDate(0, 0, -days)
	var out []PurgeItem
	for rows.Next() {
		var it PurgeItem
		if err := rows.Scan(&it.Month, &it.Rows); err != nil {
			return nil, err
		}
		start, err := time.Parse("2006-01", it.Month)
		if err != nil {
			continue
		}
		if !start.AddDate(0, 1, 0).After(cut) { // 그 달이 끝난 시각이 기준보다 이르거나 같다
			out = append(out, it)
		} else {
			break // 오래된 달부터만 파기한다(체인이 한 구간으로 이어지게)
		}
	}
	return out, rows.Err()
}

// PurgeItem 은 파기 대상(또는 결과) 한 달이다.
type PurgeItem struct {
	Month string `json:"month"`
	Rows  int64  `json:"rows"`
}

// Purge 는 보관 기간이 지난 달의 행을 오래된 달부터 파기한다. dry 이면 대상만 돌려준다.
// 달마다 한 트랜잭션: 요약(privacy_log_segments)을 남기고, 보호 행을 넣은 채 정보주체·기록을 지우고, 감사 privacy.log_purge 를 남긴다.
func (r *Recorder) Purge(ctx context.Context, now time.Time, dry bool) ([]PurgeItem, error) {
	items, err := r.PendingPurge(ctx, now)
	if err != nil || dry || len(items) == 0 {
		return items, err
	}
	var done []PurgeItem
	for _, it := range items {
		err := r.txs.WithTx(ctx, func(tx contract.Tx) error {
			var first, last, n int64
			var head, prev string
			month := it.Month
			if err := tx.QueryRowContext(ctx, `SELECT MIN(id), MAX(id), COUNT(*) FROM privacy_access_log WHERE substr(at, 1, 7) = ?`, month).Scan(&first, &last, &n); err != nil {
				return err
			}
			if n == 0 {
				return nil
			}
			if last-first+1 != n {
				return stateError("조회 기록의 " + month + " 구간이 이어져 있지 않아 파기하지 않았습니다(먼저 privacy-verify 로 확인하세요)")
			}
			if err := tx.QueryRowContext(ctx, `SELECT hash FROM privacy_access_log WHERE id = ?`, last).Scan(&head); err != nil {
				return err
			}
			if err := tx.QueryRowContext(ctx, `SELECT prev_hash FROM privacy_access_log WHERE id = ?`, first).Scan(&prev); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO privacy_log_segments (month, first_id, last_id, rows, head_hash, prev_hash, purged_at) VALUES (?,?,?,?,?,?,?)`,
				month, first, last, n, head, prev, now.UTC().Format(time.RFC3339Nano)); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO privacy_write_guard (id) VALUES (1)`); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `DELETE FROM privacy_access_subjects WHERE log_id BETWEEN ? AND ?`, first, last); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `DELETE FROM privacy_access_log WHERE id BETWEEN ? AND ?`, first, last); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `DELETE FROM privacy_write_guard WHERE id = 1`); err != nil {
				return err
			}
			if err := r.audit.Record(ctx, tx, audit.Entry{Actor: systemActor, Action: "privacy.log_purge", TargetType: audit.TargetPrivacyLog, TargetID: month, Result: audit.ResultSuccess,
				Diff: []audit.DiffItem{{Key: "month", New: month}, {Key: "rows", New: strconv.FormatInt(n, 10)}}}); err != nil {
				return err
			}
			done = append(done, PurgeItem{Month: month, Rows: n})
			return nil
		})
		if err != nil {
			return done, err
		}
	}
	return done, nil
}

// dayRange 는 한국 시간 하루의 [시작, 끝) 을 고정 폭 UTC 문자열로 돌려준다.
func dayRange(day string) (string, string, error) {
	t, err := time.ParseInLocation("2006-01-02", day, kst)
	if err != nil {
		return "", "", err
	}
	return t.UTC().Format(atLayout), t.AddDate(0, 0, 1).UTC().Format(atLayout), nil
}

// Digest 는 그날(한국 시간)이 끝난 시점의 체인 머리를 감사 체인에 privacy.log_digest 로 남긴다(명세 5.4). 기록이 하나도 없으면 하지 않는다.
// 마지막 행들을 지우면 이 요약이 가리키는 행이 없어 Verify 가 짚는다.
func (r *Recorder) Digest(ctx context.Context, day string) (bool, error) {
	start, end, err := dayRange(day)
	if err != nil {
		return false, err
	}
	did := false
	err = r.txs.WithTx(ctx, func(tx contract.Tx) error {
		var headID int64
		var head string
		err := tx.QueryRowContext(ctx, `SELECT id, hash FROM privacy_access_log WHERE at < ? ORDER BY id DESC LIMIT 1`, end).Scan(&headID, &head)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		var rows int64
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM privacy_access_log WHERE at >= ? AND at < ?`, start, end).Scan(&rows); err != nil {
			return err
		}
		did = true
		return r.audit.Record(ctx, tx, audit.Entry{Actor: systemActor, Action: "privacy.log_digest", TargetType: audit.TargetPrivacyLog, TargetID: day, Result: audit.ResultSuccess,
			Diff: []audit.DiffItem{{Key: "day", New: day}, {Key: "rows", New: strconv.FormatInt(rows, 10)}, {Key: "head_id", New: strconv.FormatInt(headID, 10)}, {Key: "chain_head", New: head}}})
	})
	return did, err
}

// DigestIfDue 는 어제(한국 시간)의 요약이 아직 없으면 남긴다. 하루에 한 번만 효과가 있다(app_settings privacy.digest_day).
func (r *Recorder) DigestIfDue(ctx context.Context, now time.Time) error {
	yesterday := now.In(kst).AddDate(0, 0, -1).Format("2006-01-02")
	last, _, err := readSetting(ctx, r.db, digestKey)
	if err != nil {
		return err
	}
	if last >= yesterday {
		return nil
	}
	if _, err := r.Digest(ctx, yesterday); err != nil {
		return err
	}
	return r.txs.WithTx(ctx, func(tx contract.Tx) error {
		return writeSetting(ctx, tx, digestKey, yesterday, 0, now.UTC().Format(time.RFC3339Nano))
	})
}

// Run 은 서버 주기 작업이다: 시작할 때와 한 시간마다 하루 요약과 보관 기간 파기를 한다. 복구 리허설에서는 돌리지 않는다.
func (r *Recorder) Run(ctx context.Context) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		now := r.now()
		if err := r.DigestIfDue(ctx, now); err != nil {
			log.Printf("privacy: 하루 요약 실패: %v", err)
		}
		if _, err := r.Purge(ctx, now, false); err != nil {
			log.Printf("privacy: 보관 기간 파기 실패: %v", err)
		}
		if _, err := r.ExpireRequests(ctx); err != nil {
			log.Printf("privacy: 열람 요청 결과 만료 처리 실패: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
