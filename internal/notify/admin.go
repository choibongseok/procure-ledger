package notify

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
)

// ChannelName 은 채널의 한국어 이름이다(영문 코드를 화면에 보이지 않는다).
func ChannelName(ch string) string {
	switch ch {
	case "email":
		return "메일"
	case "slack":
		return "슬랙"
	case "teams":
		return "팀즈"
	case "kakaowork":
		return "카카오워크"
	case "termspace":
		return "termspace"
	}
	return "채널"
}

// Delivery 는 발송 기록 한 줄이다. 메시지 본문·이메일 주소·웹훅 주소는 어디에도 없다(명세 4.7).
type Delivery struct {
	ID           int64  `json:"id"`
	Kind         string `json:"kind"`
	KindName     string `json:"kindName"`
	Channel      string `json:"channel"`
	ChannelName  string `json:"channelName"`
	Recipient    string `json:"recipient"` // 사람은 이름, 웹훅은 대상 이름
	Status       string `json:"status"`
	Attempts     int    `json:"attempts"`
	Reason       string `json:"reason,omitempty"` // 실패·건너뜀·재시도 사유의 한국어 문장
	NextTryAt    string `json:"nextTryAt,omitempty"`
	CreatedAt    string `json:"createdAt"`
	SentAt       string `json:"sentAt,omitempty"`
	CanResend    bool   `json:"canResend"`
	GroupedCount int    `json:"groupedCount,omitempty"` // 같은 묶음(한 통)으로 나간 건수
}

// DeliveryFilter 는 발송 기록 조회 조건이다. From·To 는 YYYY-MM-DD(KST 달력일).
type DeliveryFilter struct {
	Status  string
	Channel string
	Kind    string
	From    string
	To      string
	Limit   int
}

// Status 는 관리 화면의 메일 카드와 큐 요약이다.
type QueueStatus struct {
	MailConfigured bool `json:"mailConfigured"`
	BaseURLSet     bool `json:"baseUrlSet"`
	Queued         int  `json:"queued"`
	Sending        int  `json:"sending"`
	Failed         int  `json:"failed"`
	QueuedWarn     bool `json:"queuedWarn"` // 대기 건이 1만 건을 넘었다(E20)
}

func (s *Service) canView(ctx context.Context) (contract.Principal, error) {
	p, ok := contract.PrincipalFrom(ctx)
	if !ok {
		return p, contract.ErrUnauthenticated
	}
	if !p.Can(contract.PermNotificationManage) && !p.Can(contract.PermAuditRead) {
		return p, contract.ErrForbidden
	}
	return p, nil
}

// QueueStatus 는 메일 설정 상태와 큐 건수다(notification:manage).
func (s *Service) QueueStatus(ctx context.Context) (QueueStatus, error) {
	if _, err := contract.Authorize(ctx, contract.PermNotificationManage); err != nil {
		return QueueStatus{}, err
	}
	out := QueueStatus{MailConfigured: s.o.Mailer != nil, BaseURLSet: strings.TrimSpace(s.o.BaseURL) != ""}
	rows, err := s.o.DB.QueryContext(ctx, `SELECT status, COUNT(*) FROM notification_deliveries WHERE status IN ('queued','sending','failed') GROUP BY status`)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var st string
		var n int
		if err := rows.Scan(&st, &n); err != nil {
			return out, err
		}
		switch st {
		case StatusQueued:
			out.Queued = n
		case StatusSending:
			out.Sending = n
		case StatusFailed:
			out.Failed = n
		}
	}
	out.QueuedWarn = out.Queued > 10000
	return out, rows.Err()
}

// Deliveries 는 발송 기록이다(notification:manage 또는 audit:read). 최신순이고 기본 100건·최대 500건이다.
func (s *Service) Deliveries(ctx context.Context, f DeliveryFilter) ([]Delivery, error) {
	p, err := s.canView(ctx)
	if err != nil {
		return nil, err
	}
	switch f.Status {
	case "", StatusQueued, StatusHeld, StatusSending, StatusSent, StatusFailed, StatusSkipped:
	default:
		return nil, fmt.Errorf("%w: 상태 조건이 올바르지 않습니다.", contract.ErrValidation)
	}
	switch f.Channel {
	case "", "email", "slack", "teams", "kakaowork", "termspace":
	default:
		return nil, fmt.Errorf("%w: 채널 조건이 올바르지 않습니다.", contract.ErrValidation)
	}
	if f.Kind != "" {
		if _, ok := KindByCode(f.Kind); !ok {
			return nil, fmt.Errorf("%w: 알림 종류 조건이 올바르지 않습니다.", contract.ErrValidation)
		}
	}
	from, to := "", ""
	if f.From != "" {
		t, err := time.Parse("2006-01-02", f.From)
		if err != nil {
			return nil, fmt.Errorf("%w: 시작일은 2026-11-12 와 같은 형식으로 입력하세요.", contract.ErrValidation)
		}
		from = ts(time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, kst))
	}
	if f.To != "" {
		t, err := time.Parse("2006-01-02", f.To)
		if err != nil {
			return nil, fmt.Errorf("%w: 종료일은 2026-11-12 와 같은 형식으로 입력하세요.", contract.ErrValidation)
		}
		to = ts(time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, kst).AddDate(0, 0, 1))
	}
	limit := f.Limit
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.o.DB.QueryContext(ctx, `SELECT d.id, d.kind, d.channel, COALESCE(u.name, t.name, ''), d.status, d.attempts, d.last_error_code, d.next_try_at, d.created_at, d.sent_at, d.resends,
			(SELECT COUNT(*) FROM notification_deliveries x WHERE x.digest_id IS NOT NULL AND x.digest_id = d.digest_id)
		FROM notification_deliveries d LEFT JOIN users u ON u.id = d.user_id LEFT JOIN notification_targets t ON t.id = d.target_id
		WHERE (? = '' OR d.status = ?) AND (? = '' OR d.channel = ?) AND (? = '' OR d.kind = ?) AND (? = '' OR d.created_at >= ?) AND (? = '' OR d.created_at < ?)
		ORDER BY d.id DESC LIMIT ?`, f.Status, f.Status, f.Channel, f.Channel, f.Kind, f.Kind, from, from, to, to, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Delivery{}
	for rows.Next() {
		var d Delivery
		var code, next string
		var resends int
		if err := rows.Scan(&d.ID, &d.Kind, &d.Channel, &d.Recipient, &d.Status, &d.Attempts, &code, &next, &d.CreatedAt, &d.SentAt, &resends, &d.GroupedCount); err != nil {
			return nil, err
		}
		d.KindName, d.ChannelName, d.Reason = KindName(d.Kind), ChannelName(d.Channel), ReasonText(code)
		if d.Status == StatusQueued || d.Status == StatusSending {
			d.NextTryAt = next
		}
		d.CanResend = d.Status == StatusFailed && resends < 1 && p.Can(contract.PermNotificationManage)
		if d.GroupedCount < 2 {
			d.GroupedCount = 0
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

var kst = time.FixedZone("KST", 9*3600)

// Resend 는 실패한 건을 한 번만 다시 보낸다(notification:manage). 시도 횟수를 비우고 바로 큐에 올리며 감사에 남긴다.
func (s *Service) Resend(ctx context.Context, id int64) error {
	p, err := contract.Authorize(ctx, contract.PermNotificationManage)
	if err != nil {
		return err
	}
	return s.o.Txs.WithTx(ctx, func(tx contract.Tx) error {
		var kind, channel, status string
		var resends int
		if err := tx.QueryRowContext(ctx, `SELECT kind, channel, status, resends FROM notification_deliveries WHERE id = ?`, id).Scan(&kind, &channel, &status, &resends); err != nil {
			return fmt.Errorf("%w: 발송 건을 찾을 수 없습니다.", contract.ErrNotFound)
		}
		if status != StatusFailed {
			return fmt.Errorf("%w: 실패한 건만 다시 보낼 수 있습니다.", contract.ErrInvalidState)
		}
		if resends >= 1 {
			return fmt.Errorf("%w: 이미 한 번 다시 보냈습니다. 같은 알림이 필요하면 업무 화면에서 다시 만드세요.", contract.ErrInvalidState)
		}
		res, err := tx.ExecContext(ctx, `UPDATE notification_deliveries SET status = 'queued', attempts = 0, resends = resends + 1, next_try_at = ?, last_error_code = '' WHERE id = ? AND status = 'failed' AND resends < 1`, ts(s.now()), id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return fmt.Errorf("%w: 이미 처리된 건입니다.", contract.ErrInvalidState)
		}
		return s.o.Audit.Record(ctx, tx, audit.Entry{Actor: p, Action: "notification.resend", TargetType: audit.TargetNotification, TargetID: fmt.Sprint(id), Result: audit.ResultSuccess,
			Diff: []audit.DiffItem{{Key: "channel", New: ChannelName(channel)}, {Key: "notification_kind", New: KindName(kind)}}})
	})
}

// MailTest 는 관리자 본인 이메일로 고정 문구 시험 메일을 보낸다(큐를 거치지 않고 바로, notification:manage). 결과 코드만 감사에 남기고 받는 주소는 남기지 않는다.
func (s *Service) MailTest(ctx context.Context) error {
	p, err := contract.Authorize(ctx, contract.PermNotificationManage)
	if err != nil {
		return err
	}
	if s.o.Mailer == nil {
		return fmt.Errorf("%w: 메일 서버가 설정되지 않았습니다. 서버 환경변수(PL_SMTP_*)를 설정한 뒤 다시 기동하세요.", contract.ErrInvalidState)
	}
	var email string
	if err := s.o.DB.QueryRowContext(ctx, `SELECT email FROM users WHERE id = ?`, p.UserID).Scan(&email); err != nil {
		return fmt.Errorf("%w: 내 이메일을 찾을 수 없습니다.", contract.ErrNotFound)
	}
	sctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	sendErr := s.o.Mailer.Send(sctx, email, subjectPrefix+"메일 시험", "ProcureLedger 메일 연결 시험입니다.\n")
	code, res := "ok", audit.ResultSuccess
	if sendErr != nil {
		code, _ = classify(sendErr)
		res = audit.ResultError
	}
	rerr := s.o.Txs.WithTx(ctx, func(tx contract.Tx) error {
		return s.o.Audit.Record(ctx, tx, audit.Entry{Actor: p, Action: "notification.mail_test", TargetType: audit.TargetNotification, TargetID: "mail", Result: res,
			Diff: []audit.DiffItem{{Key: "result_code", New: code}}})
	})
	if sendErr != nil {
		return fmt.Errorf("%w: 시험 메일을 보내지 못했습니다. %s", contract.ErrInvalidState, ReasonText(code))
	}
	return rerr
}

// Purge 는 보존 기간(notify.keep_days, 기본 365)이 지난 끝난 발송 건을 지운다(sent·failed·skipped). 지운 건수를 돌려주고 1건 이상이면 감사에 남긴다(시스템).
func (s *Service) Purge(ctx context.Context) (int, error) {
	days := intSetting(ctx, s.o.DB, "notify.keep_days", 365)
	cutoff := ts(s.now().AddDate(0, 0, -days))
	n := 0
	err := s.o.Txs.WithTx(ctx, func(tx contract.Tx) error {
		res, err := tx.ExecContext(ctx, `DELETE FROM notification_deliveries WHERE status IN ('sent','failed','skipped') AND created_at < ?`, cutoff)
		if err != nil {
			return err
		}
		c, _ := res.RowsAffected()
		n = int(c)
		if n == 0 {
			return nil
		}
		return s.o.Audit.Record(ctx, tx, audit.Entry{Actor: contract.Principal{Channel: contract.ChannelWeb}, Action: "notification.purge", TargetType: audit.TargetNotification, TargetID: "deliveries", Result: audit.ResultSuccess,
			Diff: []audit.DiffItem{{Key: "purged_count", New: fmt.Sprint(n)}}})
	})
	return n, err
}
