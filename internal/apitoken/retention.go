package apitoken

import (
	"context"
	"database/sql"
	"errors"
	"log"
	"strconv"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/dbutil"
	"github.com/choibongseok/procure-ledger/internal/webhooks"
)

// 변경 피드 보존(명세 8.6, D12): changes.keep_days(기본 90일, 7~365). 서버 주기 작업이 하루 한 번 보존 기간이 지난 줄을 지운다.
// 사용 중인 웹훅 구독이 아직 읽지 않은 줄은 지우지 않는다(webhooks.FeedKeepFloor).
const (
	keyKeepDays    = "changes.keep_days"
	keyPurgedThru  = "changes.purged_through"
	DefaultKeepDay = 90
	MinKeepDays    = 7
	MaxKeepDays    = 365
	changeTSLayout = "2006-01-02T15:04:05.000000000Z"
)

func (s *Service) keepDays(ctx context.Context) int {
	if v, ok := s.readSetting(ctx, keyKeepDays); ok {
		if n, err := strconv.Atoi(v); err == nil && n >= MinKeepDays && n <= MaxKeepDays {
			return n
		}
	}
	return DefaultKeepDay
}

// PurgeChanges 는 보존 기간이 지난 변경 피드 줄을 지우고 지운 건수를 돌려준다. 지운 마지막 번호를 changes.purged_through 에 남겨
// 그보다 오래된 커서가 410(cursor_expired)이 되게 한다. 감사 changes.purge(건수만).
// 피드 표는 추가 전용 트리거로 지킨다: 같은 트랜잭션 안에서만 트리거를 일시 해제한다(SQLite: DROP/CREATE, PG: DISABLE/ENABLE TRIGGER USER, 표 소유자 권한).
func (s *Service) PurgeChanges(ctx context.Context) (int64, error) {
	cut := s.now().UTC().AddDate(0, 0, -s.keepDays(ctx)).Format(changeTSLayout)
	var deleted int64
	err := s.txs.WithTx(ctx, func(tx contract.Tx) error {
		// Note 와 같은 순서로 피드 잠금을 먼저 잡는다(PG 교착 방지, 쓰는 쪽 트랜잭션이 끝난 뒤 지운다).
		if err := dbutil.AcquireTxLock(ctx, tx, dbutil.Active(), dbutil.LockChangeFeed); err != nil {
			return err
		}
		var maxSeq sql.NullInt64
		if err := tx.QueryRowContext(ctx, `SELECT MAX(seq) FROM change_events WHERE at < ?`, cut).Scan(&maxSeq); err != nil {
			return err
		}
		if !maxSeq.Valid {
			return nil
		}
		// 변경 웹훅 구독(B42 D12)이 아직 읽지 않은 줄은 지우지 않는다: 사용 중인 구독의 가장 느린 위치까지만 지운다.
		// 중지된 구독은 붙잡지 않는다(재개하면 커서 만료로 건너뛰고 feed_gap 으로 표시된다).
		if floor, ok, err := webhooks.FeedKeepFloor(ctx, tx); err != nil {
			return err
		} else if ok && floor < maxSeq.Int64 {
			if floor <= 0 {
				return nil
			}
			maxSeq.Int64 = floor
		}
		pg := dbutil.DialectOf(s.db).HasExplicitLocks()
		if pg {
			if _, err := tx.ExecContext(ctx, `ALTER TABLE change_events DISABLE TRIGGER USER`); err != nil {
				return err
			}
		} else if _, err := tx.ExecContext(ctx, `DROP TRIGGER trg_change_events_no_delete`); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `DELETE FROM change_events WHERE seq <= ?`, maxSeq.Int64)
		if err != nil {
			return err
		}
		deleted, _ = res.RowsAffected()
		if pg {
			if _, err := tx.ExecContext(ctx, `ALTER TABLE change_events ENABLE TRIGGER USER`); err != nil {
				return err
			}
		} else if _, err := tx.ExecContext(ctx, `CREATE TRIGGER trg_change_events_no_delete BEFORE DELETE ON change_events
BEGIN SELECT RAISE(ABORT, 'change_events 는 추가 전용입니다'); END`); err != nil {
			return err
		}
		var prev int64
		var v string
		if err := tx.QueryRowContext(ctx, `SELECT value FROM app_settings WHERE key = ?`, keyPurgedThru).Scan(&v); err == nil {
			prev, _ = strconv.ParseInt(v, 10, 64)
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if maxSeq.Int64 > prev {
			prev = maxSeq.Int64
		}
		if err := s.writeSetting(ctx, tx, keyPurgedThru, strconv.FormatInt(prev, 10), 0); err != nil {
			return err
		}
		return s.audit.Record(ctx, tx, audit.Entry{Actor: contract.Principal{Role: "system", Channel: contract.ChannelCLI}, Action: "changes.purge", TargetType: audit.TargetChangeFeed, TargetID: "change_events", Result: audit.ResultSuccess,
			Diff: []audit.DiffItem{{Key: "rows", New: strconv.FormatInt(deleted, 10)}}})
	})
	return deleted, err
}

// Run 은 서버 주기 작업이다: 30초마다 끝난 분의 읽기 호출 요약을 감사에 남기고, 하루 한 번 옛 분 창과 변경 피드를 정리한다. 끝날 때 남은 요약을 모두 남긴다.
func (s *Service) Run(ctx context.Context) {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	var lastDay string
	for {
		select {
		case <-ctx.Done():
			fctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			s.Flush(fctx, true)
			cancel()
			return
		case <-t.C:
		}
		s.Flush(ctx, false)
		if day := s.now().UTC().Format("2006-01-02"); day != lastDay {
			lastDay = day
			cutMinute := s.now().Unix()/60 - 2*1440
			if _, err := s.db.ExecContext(ctx, `DELETE FROM api_rate_windows WHERE minute < ?`, cutMinute); err != nil {
				log.Printf("apitoken: 옛 분 창 정리 실패: %v", err)
			}
			if n, err := s.PurgeChanges(ctx); err != nil {
				log.Printf("apitoken: 변경 피드 정리 실패: %v", err)
			} else if n > 0 {
				log.Printf("apitoken: 변경 피드 %d건 정리", n)
			}
		}
	}
}
