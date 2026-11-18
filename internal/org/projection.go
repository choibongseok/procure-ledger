package org

import (
	"context"
	"database/sql"
	"fmt"
	"github.com/choibongseok/procure-ledger/internal/access"
	"log"
	"sort"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
)

const settingProjectionDay = "org_projection_day"

func hasSpans(r *Result) map[int64]bool {
	m := map[int64]bool{}
	for _, p := range r.People {
		m[p.UserID] = true
	}
	return m
}

// afterRebuild 는 이벤트를 등록한 직후 오늘 기준 호환 투영을 맞춘다(같은 트랜잭션).
// 등록 전 구간(before)과 비교해 오늘 재직 상태가 바뀐 사람을 찾는다.
func (s *Service) afterRebuild(ctx context.Context, tx contract.Tx, before, after *Result, today contract.Day) error {
	prev := NewSnap(before, today)
	prevHad := hasSpans(before)
	_, err := s.project(ctx, tx, after, NewSnap(after, today), func(u int64) bool { return prevHad[u] && prev.Employed(u) })
	return err
}

// project 는 조직에 등록된 사용자의 users.department_id·position·active 를 오늘 기준으로 맞춘다(11.2).
// prevEmployed 는 "이전에 재직 중이었는가"이고, 재직이 아니던 사람이 재직이 되면 계정을 다시 켠다.
// 재직이 아닌 사람의 계정은 항상 끈다(세션·토큰·위임 정리 포함). 바뀐 사람 수를 돌려준다.
func (s *Service) project(ctx context.Context, tx contract.Tx, res *Result, now *Snap, prevEmployed func(int64) bool) (int, error) {
	registered := hasSpans(res)
	ids := map[int64]bool{}
	for id := range registered {
		ids[id] = true
	}
	rows, err := tx.QueryContext(ctx, `SELECT user_id FROM org_people`)
	if err != nil {
		return 0, err
	}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		ids[id] = true
	}
	rows.Close()
	list := make([]int64, 0, len(ids))
	for id := range ids {
		list = append(list, id)
	}
	sort.Slice(list, func(i, j int) bool { return list[i] < list[j] })

	changed := 0
	err = withGuard(ctx, tx, func() error {
		for _, uid := range list {
			var active int
			var dept int64
			var pos sql.NullString
			if err := tx.QueryRowContext(ctx, `SELECT active, department_id, position FROM users WHERE id = ?`, uid).Scan(&active, &dept, &pos); err != nil {
				if err == sql.ErrNoRows {
					continue
				}
				return err
			}
			emp := registered[uid] && now.Employed(uid)
			if !emp {
				if active == 1 {
					reason := "발령 취소"
					if registered[uid] {
						reason = "입사 전"
						for _, ps := range res.People {
							if ps.UserID == uid && ps.Left && ps.From <= now.Day {
								reason = "퇴사"
							}
						}
					}
					if err := s.deactivate(ctx, tx, uid, reason); err != nil {
						return err
					}
					changed++
				}
				continue
			}
			p := now.People[uid]
			wantDept, wantPos := now.DeptOf(p.Primary), now.PositionOf(uid)
			wantActive := active
			if !prevEmployed(uid) && active == 0 {
				wantActive = 1
			}
			if wantDept != dept || wantPos != pos.String || wantActive != active {
				var posArg any
				if wantPos != "" {
					posArg = wantPos
				}
				if _, err := tx.ExecContext(ctx, `UPDATE users SET department_id = ?, position = ?, active = ? WHERE id = ?`, wantDept, posArg, wantActive, uid); err != nil {
					return err
				}
				changed++
			}
		}
		return nil
	})
	if err == nil {
		// 소속·직위·조직장·재직이 바뀌었으니 조직·직위·조직장 부여의 실효 권한을 같은 트랜잭션에서 다시 계산한다(B33).
		err = access.Recompute(ctx, tx, s.Today())
	}
	return changed, err
}

// deactivate 는 재직이 아닌 사람의 계정을 끄고 세션·개인 토큰·위임을 정리한다. 행위자는 시스템이다.
func (s *Service) deactivate(ctx context.Context, tx contract.Tx, uid int64, reason string) error {
	now := s.now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `UPDATE users SET active = 0 WHERE id = ?`, uid); err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, `UPDATE sessions SET revoked_at = ? WHERE user_id = ? AND revoked_at IS NULL`, now, uid)
	if err != nil {
		return err
	}
	sessions, _ := res.RowsAffected()
	trows, err := tx.QueryContext(ctx, `SELECT id FROM personal_tokens WHERE user_id = ? AND revoked_at IS NULL`, uid)
	if err != nil {
		return err
	}
	var tokens []int64
	for trows.Next() {
		var id int64
		if err := trows.Scan(&id); err != nil {
			trows.Close()
			return err
		}
		tokens = append(tokens, id)
	}
	trows.Close()
	for _, id := range tokens {
		if _, err := tx.ExecContext(ctx, `UPDATE personal_tokens SET revoked_at = ? WHERE id = ?`, now, id); err != nil {
			return err
		}
		if err := s.audit.Record(ctx, tx, audit.Entry{Actor: systemActor, Action: "token.revoke", TargetType: audit.TargetToken,
			TargetID: fmt.Sprint(id), Result: audit.ResultSuccess,
			Diff: []audit.DiffItem{{Key: "revoked_at", New: now}, {Key: "reason", New: reason}}}); err != nil {
			return err
		}
	}
	drows, err := tx.QueryContext(ctx, `SELECT id FROM approval_delegations WHERE (delegator_id = ? OR delegate_id = ?) AND revoked_at IS NULL AND end_at > ?`, uid, uid, now)
	if err != nil {
		return err
	}
	var delegs []int64
	for drows.Next() {
		var id int64
		if err := drows.Scan(&id); err != nil {
			drows.Close()
			return err
		}
		delegs = append(delegs, id)
	}
	drows.Close()
	for _, id := range delegs {
		if _, err := tx.ExecContext(ctx, `UPDATE approval_delegations SET revoked_at = ? WHERE id = ?`, now, id); err != nil {
			return err
		}
		if err := s.audit.Record(ctx, tx, audit.Entry{Actor: systemActor, Action: "delegation.revoke", TargetType: audit.TargetDelegation,
			TargetID: fmt.Sprint(id), Result: audit.ResultSuccess,
			Diff: []audit.DiffItem{{Key: "revoked_at", New: now}, {Key: "reason", New: reason}}}); err != nil {
			return err
		}
	}
	return s.audit.Record(ctx, tx, audit.Entry{Actor: systemActor, Action: "user.update", TargetType: audit.TargetUser,
		TargetID: fmt.Sprint(uid), Result: audit.ResultSuccess,
		Diff: []audit.DiffItem{{Key: "active", Old: "1", New: "0"}, {Key: "reason", New: reason}, {Key: "sessions", New: fmt.Sprint(sessions)}}})
}

// RefreshProjection 은 날짜가 바뀌었을 때(또는 기동할 때) 오늘 기준 투영을 다시 맞춘다. 멱등이다.
// 마지막으로 맞춘 날짜는 app_settings 에 두고, 그 날짜의 재직 상태와 오늘을 비교해 다시 켜야 할 사람을 찾는다.
// 서버 프로세스만 부른다(erp-mcp 는 부르지 않는다. 퇴사 인증 거부는 이 갱신과 무관하게 즉시 적용된다).
func (s *Service) RefreshProjection(ctx context.Context) (int, error) {
	today := s.Today()
	changed := 0
	err := s.txs.WithTx(ctx, func(tx contract.Tx) error {
		res, err := s.foldAll(ctx, tx)
		if err != nil {
			return err
		}
		var last string
		err = tx.QueryRowContext(ctx, `SELECT value FROM app_settings WHERE key = ?`, settingProjectionDay).Scan(&last)
		if err != nil && err != sql.ErrNoRows {
			return err
		}
		prev := NewSnap(res, contract.Day(last))
		now := NewSnap(res, today)
		prevFn := func(u int64) bool { return last == "" || prev.Employed(u) } // 첫 실행은 되살림 없이 상태만 맞춘다
		n, err := s.project(ctx, tx, res, now, prevFn)
		if err != nil {
			return err
		}
		changed = n
		if _, err := tx.ExecContext(ctx, `INSERT INTO app_settings (key, value, updated_at) VALUES (?,?,?)
			ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
			settingProjectionDay, string(today), s.now().UTC().Format(time.RFC3339)); err != nil {
			return err
		}
		if n > 0 {
			return s.audit.Record(ctx, tx, audit.Entry{Actor: systemActor, Action: "org.projection_apply", TargetType: audit.TargetOrgUnit,
				TargetID: "all", Result: audit.ResultSuccess,
				Diff: []audit.DiffItem{{Key: "changed_users", New: fmt.Sprint(n)}, {Key: "date", New: string(today)}}})
		}
		return nil
	})
	s.invalidate()
	return changed, err
}

// Run 은 서버가 켜져 있는 동안 1분마다 KST 날짜가 바뀌었는지 확인해 투영을 갱신한다. 기동할 때 한 번 맞춘다.
func (s *Service) Run(ctx context.Context) {
	var lastRun contract.Day
	tick := func() {
		today := s.Today()
		if today == lastRun {
			return
		}
		if _, err := s.RefreshProjection(ctx); err != nil {
			log.Printf("org: 조직 현황 반영 실패: %v", err)
			return
		}
		lastRun = today
	}
	tick()
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			tick()
		}
	}
}
