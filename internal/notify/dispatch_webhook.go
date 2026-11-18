package notify

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
)

// 웹훅 발송 작업(B36 단계 2). 큐·상태 전이·재시도 간격·포기 규칙은 1단계 메일과 같은 표를 쓰고(Transitions, backoff),
// 묶음 단위만 "사람·종류"가 아니라 "대상·종류"다: 같은 방으로 가는 같은 종류의 건은 건수 한 줄로 합쳐진다(D15).

// claimWebhooks 는 받을 때가 된 웹훅 건을 가져온다. 각 건은 조건부 갱신(queued → sending)이라 두 작업이 같은 건을 가져가려 해도 한 곳만 성공한다.
func (s *Service) claimWebhooks(ctx context.Context, now time.Time) ([]*claimed, error) {
	rows, err := s.o.DB.QueryContext(ctx, `SELECT d.id, d.target_id, d.channel, d.kind, d.attempts, COALESCE(n.link, '')
		FROM notification_deliveries d LEFT JOIN notifications n ON n.id = d.notification_id
		WHERE d.status = 'queued' AND d.target_id IS NOT NULL AND d.channel IN ('slack','teams','kakaowork','termspace') AND d.next_try_at <= ? ORDER BY d.next_try_at, d.id LIMIT 200`, ts(now))
	if err != nil {
		return nil, err
	}
	type cand struct {
		id, target int64
		channel    string
		kind       string
		attempts   int
		link       string
	}
	var cands []cand
	for rows.Next() {
		var c cand
		if err := rows.Scan(&c.id, &c.target, &c.channel, &c.kind, &c.attempts, &c.link); err != nil {
			rows.Close()
			return nil, err
		}
		cands = append(cands, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	byKey := map[string]*claimed{}
	var out []*claimed
	for _, c := range cands {
		res, err := s.o.DB.ExecContext(ctx, `UPDATE notification_deliveries SET status = 'sending', claimed_at = ? WHERE id = ? AND status = 'queued'`, ts(now), c.id)
		if err != nil {
			return nil, err
		}
		if n, _ := res.RowsAffected(); n != 1 {
			continue
		}
		key := fmt.Sprintf("%d|%s", c.target, c.kind)
		g := byKey[key]
		if g == nil {
			g = &claimed{channel: c.channel, targetID: c.target, kind: c.kind, link: c.link, attempts: c.attempts}
			byKey[key] = g
			out = append(out, g)
		}
		g.ids = append(g.ids, c.id)
		if c.attempts > g.attempts {
			g.attempts = c.attempts
		}
	}
	return out, nil
}

// admitWebhooks 는 대상별 분당 상한(notify.target_per_min, 기본 20) 안에서 이번 주기에 보낼 묶음을 고른다. 넘는 묶음은 한 분 뒤로 미룬다(실패 아님).
func (s *Service) admitWebhooks(ctx context.Context, groups []*claimed, now time.Time) []*claimed {
	perMin := intSetting(ctx, s.o.DB, "notify.target_per_min", 20)
	count := map[int64]int{}
	var out []*claimed
	for _, g := range groups {
		n, seen := count[g.targetID]
		if !seen {
			_ = s.o.DB.QueryRowContext(ctx, `SELECT COUNT(DISTINCT COALESCE(digest_id, id)) FROM notification_deliveries WHERE status = 'sent' AND target_id = ? AND sent_at >= ?`, g.targetID, ts(now.Add(-time.Minute))).Scan(&n)
		}
		if n >= perMin {
			count[g.targetID] = n
			s.finish(ctx, g.ids, StatusQueued, "", g.attempts, now.Add(time.Minute), 0)
			continue
		}
		count[g.targetID] = n + 1
		out = append(out, g)
	}
	return out
}

// deliverWebhook 는 한 묶음(같은 대상·종류)을 보낸다.
func (s *Service) deliverWebhook(ctx context.Context, g *claimed) {
	now := s.now()
	t, err := s.loadTarget(ctx, s.o.DB, g.targetID)
	if err != nil || t.active != 1 || t.paused != "" {
		s.finish(ctx, g.ids, StatusSkipped, CodeTargetPaused, g.attempts, now, 0)
		return
	}
	if strings.TrimSpace(s.o.BaseURL) == "" {
		s.finish(ctx, g.ids, StatusSkipped, CodeNoBaseURL, g.attempts, now, 0)
		return
	}
	sec, err := s.openSecret(t.sealed)
	if err != nil {
		code := sendCode(err)
		if code == CodeNoSealer {
			s.finish(ctx, g.ids, StatusSkipped, code, g.attempts, now, 0)
		} else {
			s.finish(ctx, g.ids, StatusFailed, code, g.attempts, now, 0)
			s.targetFailed(ctx, t, code)
		}
		return
	}
	text, err := ComposeWebhook(g.kind, len(g.ids), s.o.BaseURL, g.link)
	if err != nil {
		code := CodeBadLink
		if strings.Contains(err.Error(), "앱 주소") {
			code = CodeNoBaseURL
		}
		s.finish(ctx, g.ids, StatusFailed, code, g.attempts, now, 0)
		return
	}
	if err := s.checkTargetURL(ctx, t.typ, sec); err != nil {
		s.finish(ctx, g.ids, StatusFailed, CodeHookBlocked, g.attempts, now, 0)
		s.targetFailed(ctx, t, CodeHookBlocked)
		return
	}
	sctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	sendErr := s.sender().Send(sctx, WebhookTarget{Type: t.typ, URL: sec.URL, Token: sec.Token, Conversation: sec.Conversation}, text)
	if sendErr == nil {
		var digest int64
		if len(g.ids) > 1 {
			digest = g.ids[0]
		}
		s.finish(ctx, g.ids, StatusSent, "", g.attempts, now, digest)
		s.targetOK(ctx, t.id, now)
		return
	}
	code, retry, after := CodeHookNetwork, true, time.Duration(0)
	if se, ok := sendErr.(*SendError); ok {
		code, retry, after = se.Code, se.Retry, se.After
	}
	attempts := g.attempts + 1
	max := intSetting(ctx, s.o.DB, "notify.max_attempts", 5)
	switch {
	case !retry:
		s.finish(ctx, g.ids, StatusFailed, code, attempts, now, 0)
		s.targetFailed(ctx, t, code)
	case attempts > max:
		s.finish(ctx, g.ids, StatusFailed, CodeRetryOver, attempts, now, 0)
		s.targetFailed(ctx, t, CodeRetryOver)
	default:
		idx := attempts - 1
		if idx >= len(backoff) {
			idx = len(backoff) - 1
		}
		wait := backoff[idx]
		if after > 0 { // 429 의 Retry-After 는 상한 1시간 안에서 따른다
			if after > time.Hour {
				after = time.Hour
			}
			if after > wait {
				wait = after
			}
		}
		s.finish(ctx, g.ids, StatusQueued, code, attempts, now.Add(wait), 0)
	}
}

func (s *Service) targetOK(ctx context.Context, id int64, now time.Time) {
	if _, err := s.o.DB.ExecContext(ctx, `UPDATE notification_targets SET consecutive_failures = 0, last_ok_at = ?, last_error_code = '' WHERE id = ?`, ts(now), id); err != nil {
		log.Printf("notify: 웹훅 대상 %d 상태 갱신 실패: %v", id, err)
	}
}

// targetFailed 는 발송이 최종 실패(영구 오류·재시도 초과)했을 때 연속 실패를 더하고, 상한(notify.target_fail_pause, 기본 5)에 닿으면 대상을 자동 중지한다.
// 중지는 감사에 남기고(시스템) 관리자(알림 채널 관리 권한자)에게 앱 안 알림을 보낸다.
func (s *Service) targetFailed(ctx context.Context, t targetRow, code string) {
	now := ts(s.now())
	limit := intSetting(ctx, s.o.DB, "notify.target_fail_pause", 5)
	err := s.o.Txs.WithTx(ctx, func(tx contract.Tx) error {
		if _, err := tx.ExecContext(ctx, `UPDATE notification_targets SET consecutive_failures = consecutive_failures + 1, last_error_code = ?, last_error_at = ? WHERE id = ?`, code, now, t.id); err != nil {
			return err
		}
		var fails, active int
		var paused string
		if err := tx.QueryRowContext(ctx, `SELECT consecutive_failures, active, paused_reason FROM notification_targets WHERE id = ?`, t.id).Scan(&fails, &active, &paused); err != nil {
			return err
		}
		if fails < limit || active != 1 || paused != "" {
			return nil
		}
		if _, err := tx.ExecContext(ctx, `UPDATE notification_targets SET paused_reason = 'auto', updated_at = ? WHERE id = ?`, now, t.id); err != nil {
			return err
		}
		if err := s.o.Audit.Record(ctx, tx, audit.Entry{Actor: contract.Principal{Channel: contract.ChannelWeb}, Action: "notification.target_auto_pause", TargetType: audit.TargetNotification,
			TargetID: fmt.Sprintf("target-%d", t.id), Result: audit.ResultSuccess,
			Diff: []audit.DiffItem{{Key: "target_name", New: t.name}, {Key: "failures", New: fmt.Sprint(fails)}, {Key: "result_code", New: code}}}); err != nil {
			return err
		}
		// 관리자에게 앱 안 알림(외부 채널로는 나가지 않는 종류다).
		_, err := tx.ExecContext(ctx, `INSERT INTO notifications (user_id, kind, title, body, link, ref_type, ref_id, created_at)
			SELECT DISTINCT e.user_id, 'notify_target_paused', '알림 채널이 일시 중지되었습니다', CAST(? AS TEXT), '/admin/notifications', 'notification_target', CAST(? AS BIGINT), CAST(? AS TEXT)
			FROM user_effective_permissions e JOIN users u ON u.id = e.user_id AND u.active = 1 WHERE e.permission = ?`,
			fmt.Sprintf("'%s' 채널의 발송이 연속 %d번 실패해 멈췄습니다. 알림 채널 화면에서 시험 발송을 해 성공하면 다시 켜집니다.", t.name, fails), t.id, now, string(contract.PermNotificationManage))
		return err
	})
	if err != nil {
		log.Printf("notify: 웹훅 대상 %d 실패 기록 오류: %v", t.id, err)
	}
}
