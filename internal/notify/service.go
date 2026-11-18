package notify

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"net"
	"net/mail"
	"net/textproto"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
)

// 발송 건 상태(명세 4.5).
const (
	StatusQueued  = "queued"
	StatusHeld    = "held"
	StatusSending = "sending"
	StatusSent    = "sent"
	StatusFailed  = "failed"
	StatusSkipped = "skipped"
)

// Transitions 는 허용하는 상태 전이 표 한 곳이다(DB 트리거도 같은 표를 막는다). 표에 없는 전이는 거부한다.
var Transitions = map[string][]string{
	StatusQueued:  {StatusSending, StatusSkipped},
	StatusHeld:    {StatusQueued, StatusSkipped},
	StatusSending: {StatusSent, StatusQueued, StatusFailed},
	StatusFailed:  {StatusQueued},
}

// CanTransit 은 from → to 가 허용되는 전이인지다.
func CanTransit(from, to string) bool {
	for _, t := range Transitions[from] {
		if t == to {
			return true
		}
	}
	return false
}

// 실패·건너뜀 사유 코드(화면에는 ReasonText 의 한국어 문장만 보인다).
const (
	CodeNoMailer     = "no_mailer"
	CodeNoBaseURL    = "no_base_url"
	CodeInactive     = "inactive"
	CodeBadAddress   = "bad_address"
	CodeSMTPTemp     = "smtp_temp"
	CodeSMTPPerm     = "smtp_perm"
	CodeNetwork      = "network"
	CodeRetryOver    = "retry_exceeded"
	CodeBadLink      = "bad_link"
	CodeStuckRecover = "recovered"
)

var reasonText = map[string]string{
	CodeNoMailer:     "메일 서버가 설정되지 않았습니다.",
	CodeNoBaseURL:    "앱 주소가 설정되지 않았습니다.",
	CodeInactive:     "수신자가 비활성입니다.",
	CodeBadAddress:   "수신 주소 오류입니다.",
	CodeSMTPTemp:     "메일 서버가 일시적으로 받지 못했습니다.",
	CodeSMTPPerm:     "메일 서버가 수신을 거절했습니다.",
	CodeNetwork:      "메일 서버에 연결하지 못했습니다.",
	CodeRetryOver:    "재시도 횟수를 넘겼습니다.",
	CodeBadLink:      "앱 링크가 올바르지 않습니다.",
	CodeStuckRecover: "발송 도중 중단되어 다시 시도합니다.",
}

// ReasonText 는 사유 코드의 한국어 문장이다.
func ReasonText(code string) string {
	if code == "" {
		return ""
	}
	if t, ok := reasonText[code]; ok {
		return t
	}
	return "알 수 없는 사유입니다."
}

const tsLayout = "2006-01-02T15:04:05Z"

func ts(t time.Time) string { return t.UTC().Format(tsLayout) }

// 재시도 간격(명세 4.5): 1분, 5분, 30분, 2시간, 6시간.
var backoff = []time.Duration{time.Minute, 5 * time.Minute, 30 * time.Minute, 2 * time.Hour, 6 * time.Hour}

// Mailer 는 메일 발송 수단이다(auth.Mailer 와 같은 모양이라 그대로 꽂힌다, D1).
type Mailer interface {
	Send(ctx context.Context, to, subject, body string) error
}

// Options 는 서비스 조립 인자다.
type Options struct {
	DB      contract.Tx
	Txs     contract.TxManager
	Audit   audit.Recorder
	Mailer  Mailer // nil 이면 메일 채널은 건너뜀("메일 서버 미설정")
	BaseURL string // PL_BASE_URL. 비면 외부 채널 발송을 건너뜀
	Now     func() time.Time
	Workers int           // 동시 발송 수(기본 4)
	Poll    time.Duration // 발송 작업 주기(기본 5초)

	// 웹훅 채널(단계 2). Sealer 가 nil 이면 웹훅 대상을 등록할 수 없고 이미 있는 대상의 발송은 건너뜀("키 없음")이다.
	Sealer Sealer
	// Webhook 은 웹훅 발송기다. nil 이면 SSRF 방어가 들어간 HTTP 발송기를 쓴다.
	Webhook WebhookSender
	// AllowLocalWebhook 은 루프백 주소로의 웹훅(와 http)을 허용한다. 로컬 시험 전용(PL_NOTIFY_INSECURE_LOCAL=1).
	AllowLocalWebhook bool
	// Resolve 는 호스트 이름 해석 검사를 대신한다(시험이 DNS 를 쓰지 않게). nil 이면 실제 해석한다.
	Resolve func(ctx context.Context, host string) error

	// BusinessDay 는 영업일 판정(B34)이다. 업무 시간 외 묶음 발송이 쓴다. nil 이면 모든 날을 영업일로 본다(SetBusinessDay 로 나중에 꽂을 수 있다).
	BusinessDay BusinessDayFunc
}

// Service 는 발송 큐·발송 작업·관리 기능이다.
type Service struct {
	o             Options
	defaultSender WebhookSender
	mu            sync.Mutex // 같은 프로세스 안에서 Tick 이 겹치지 않게
	run           sync.Mutex
}

// New 는 서비스를 만든다.
func New(o Options) *Service {
	if o.Workers <= 0 {
		o.Workers = 4
	}
	if o.Poll <= 0 {
		o.Poll = 5 * time.Second
	}
	return &Service{o: o, defaultSender: NewHTTPSender(HTTPSenderOptions{AllowLocal: o.AllowLocalWebhook})}
}

func (s *Service) now() time.Time {
	if s.o.Now != nil {
		return s.o.Now().UTC()
	}
	return time.Now().UTC()
}

// MailConfigured 는 메일 발송 수단이 있는지다(관리 화면의 메일 카드).
func (s *Service) MailConfigured() bool { return s.o.Mailer != nil }

type querier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// intSetting 은 양의 정수 설정을 읽는다(없거나 0 이하·숫자가 아니면 기본값).
func intSetting(ctx context.Context, q querier, key string, def int) int {
	return intSettingMin(ctx, q, key, def, 1)
}

// intSettingMin 은 설정별 최소값을 받는다. min 이상의 정수만 유효하고, 없거나 형식이 틀리거나 min 미만이면 기본값이다.
// 0 이 뜻을 가진 설정(합치기 창 0 = 합치지 않음)은 min 을 0 으로 읽는다.
func intSettingMin(ctx context.Context, q querier, key string, def, min int) int {
	var v string
	if err := q.QueryRowContext(ctx, `SELECT value FROM app_settings WHERE key = ?`, key).Scan(&v); err != nil {
		return def
	}
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || n < min {
		return def
	}
	return n
}

// Enqueue 는 알림 한 건이 받는 사람에게 메일로도 나가야 하면 발송 건(queued)을 만든다. 업무 쓰기와 같은 트랜잭션에서 부른다.
// 읽기 한 번과 쓰기 한 번뿐이고, 같은 알림·사람·채널은 한 번만 만든다(유일 색인). 기본값이 꺼져 있거나 표에 없는 종류는 건을 만들지 않는다.
// 오류는 호출한 쪽(Base.notify)이 삼킨다: 발송 쪽 문제가 업무와 앱 안 알림을 막지 않는다(AC-1600).
func (s *Service) Enqueue(ctx context.Context, tx contract.Tx, notificationID, userID int64, kind string, at time.Time) error {
	if _, ok := KindByCode(kind); !ok || notificationID == 0 {
		return nil
	}
	var first error
	if userID != 0 {
		first = s.enqueueMail(ctx, tx, notificationID, userID, kind, at)
	}
	// 웹훅 채널(방)은 받는 사람과 무관하게 종류만 본다. 같은 종류의 건은 발송 때 대상별로 한 줄로 합쳐진다.
	if err := s.enqueueWebhooks(ctx, tx, notificationID, kind, at); err != nil && first == nil {
		first = err
	}
	return first
}

// dueAt 은 발송 건이 나갈 수 있는 시각이다: 독촉은 합치지 않고 바로, 나머지는 합치기 창(기본 60초) 뒤에 같은 대상·종류를 한 번에 보낸다(E4).
func (s *Service) dueAt(ctx context.Context, tx contract.Tx, kind string, at time.Time) time.Time {
	if kind == "approval_nudge" {
		return at
	}
	return at.Add(time.Duration(intSettingMin(ctx, tx, "notify.window_sec", 60, 0)) * time.Second) // 0 은 "합치지 않음"(바로 보낸다)
}

// mailEnabled 는 그 사람에게 이 종류를 메일로 보낼지 정한다: 관리자 기본값(notification_defaults)이 바탕이고, 본인이 켜짐/꺼짐으로 바꿀 수 있다.
// 기본값 행이 없는 종류는 외부로 보내지 않는다. 필수 알림(locked)이 켜져 있으면 본인이 꺼도 무시된다. 앱 안 알림과는 무관하다(앱은 끌 수 없다).
func mailEnabled(ctx context.Context, q contract.Tx, userID int64, kind string) (bool, error) {
	var enabled, locked int
	err := q.QueryRowContext(ctx, `SELECT enabled, locked FROM notification_defaults WHERE kind = ? AND channel = 'email'`, kind).Scan(&enabled, &locked)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var mode string
	switch err := q.QueryRowContext(ctx, `SELECT mode FROM notification_prefs WHERE user_id = ? AND kind = ? AND channel = 'email'`, userID, kind).Scan(&mode); {
	case err == nil:
		if mode == "on" {
			return true, nil
		}
		if mode == "off" && !(locked == 1 && enabled == 1) {
			return false, nil
		}
	case !errors.Is(err, sql.ErrNoRows):
		return false, err
	}
	return enabled == 1, nil
}

func (s *Service) enqueueMail(ctx context.Context, tx contract.Tx, notificationID, userID int64, kind string, at time.Time) error {
	on, err := mailEnabled(ctx, tx, userID, kind)
	if err != nil || !on {
		return err
	}
	// 업무 시간 외 묶음: 켠 사람의 메일은 업무 시간 밖에는 보류하고 다음 영업일 업무 시작 때 한 통으로 보낸다. 독촉은 묶지 않는다(D9).
	if kind != "approval_nudge" && digestOn(ctx, tx, userID) && !s.InWorkHours(ctx, tx, at) {
		_, err = tx.ExecContext(ctx, `INSERT INTO notification_deliveries(notification_id, user_id, channel, kind, status, next_try_at, created_at) VALUES (?, ?, 'email', ?, 'held', ?, ?) ON CONFLICT DO NOTHING`,
			notificationID, userID, kind, ts(at), ts(at))
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO notification_deliveries(notification_id, user_id, channel, kind, status, next_try_at, created_at) VALUES (?, ?, 'email', ?, 'queued', ?, ?) ON CONFLICT DO NOTHING`,
		notificationID, userID, kind, ts(s.dueAt(ctx, tx, kind, at)), ts(at))
	return err
}

func (s *Service) enqueueWebhooks(ctx context.Context, tx contract.Tx, notificationID int64, kind string, at time.Time) error {
	rows, err := tx.QueryContext(ctx, `SELECT id, type, kinds_json FROM notification_targets WHERE active = 1 AND paused_reason = ''`)
	if err != nil {
		return err
	}
	type tgt struct {
		id  int64
		typ string
	}
	var hit []tgt
	for rows.Next() {
		var t tgt
		var kj string
		if err := rows.Scan(&t.id, &t.typ, &kj); err != nil {
			rows.Close()
			return err
		}
		for _, k := range parseKinds(kj) {
			if k == kind {
				hit = append(hit, t)
				break
			}
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	due := ts(s.dueAt(ctx, tx, kind, at))
	for _, t := range hit {
		if _, err := tx.ExecContext(ctx, `INSERT INTO notification_deliveries(notification_id, target_id, channel, kind, status, next_try_at, created_at) VALUES (?, ?, ?, ?, 'queued', ?, ?) ON CONFLICT DO NOTHING`,
			notificationID, t.id, t.typ, kind, due, ts(at)); err != nil {
			return err
		}
	}
	return nil
}

type claimed struct {
	summary  bool           // 업무 시간 외 묶음 요약 메일(묶음 번호가 같은 건)
	digestID int64          // 요약 묶음 번호
	counts   map[string]int // 요약의 종류별 건수
	channel  string         // 웹훅 묶음에서만 쓴다
	targetID int64          // 웹훅 묶음에서만 쓴다
	ids      []int64
	userID   int64
	kind     string
	link     string
	// 시도 횟수는 대표 건 기준이다(합친 건은 같은 사람·종류라 함께 움직인다).
	attempts int
}

// Tick 은 발송 작업 한 번이다: 중단된 건 회수 → 받을 때가 된 건 가져오기 → 같은 사람·종류 합치기 → 발송. 시험은 주입한 시계로 Tick 을 직접 부른다.
func (s *Service) Tick(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	if err := s.recoverStuck(ctx, now); err != nil {
		return err
	}
	if err := s.releaseHeld(ctx, now); err != nil {
		return err
	}
	groups, err := s.claim(ctx, now)
	if err != nil {
		return err
	}
	// 속도 제한은 동시 발송 전에 한 줄로 판정한다(동시에 여럿이 같은 상한을 보고 함께 넘기지 않게). 넘는 묶음은 실패가 아니라 다음 주기로 미룬다.
	groups = s.admit(ctx, groups, now)
	hooks, err := s.claimWebhooks(ctx, now)
	if err != nil {
		return err
	}
	groups = append(groups, s.admitWebhooks(ctx, hooks, now)...)
	sem := make(chan struct{}, s.o.Workers)
	var wg sync.WaitGroup
	for _, g := range groups {
		g := g
		sem <- struct{}{}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			if g.targetID != 0 {
				s.deliverWebhook(ctx, g)
				return
			}
			s.deliver(ctx, g)
		}()
	}
	wg.Wait()
	return nil
}

// recoverStuck 은 15분이 지나도 sending 인 건을 queued 로 돌린다(프로세스가 죽은 경우, E7: 적어도 한 번 전달).
func (s *Service) recoverStuck(ctx context.Context, now time.Time) error {
	cutoff := ts(now.Add(-15 * time.Minute))
	_, err := s.o.DB.ExecContext(ctx, `UPDATE notification_deliveries SET status = 'queued', attempts = attempts + 1, next_try_at = ?, last_error_code = ?, claimed_at = ''
		WHERE status = 'sending' AND claimed_at <> '' AND claimed_at < ?`, ts(now), CodeStuckRecover, cutoff)
	return err
}

// claim 은 받을 때가 된 건을 가져온다. 각 건은 조건부 갱신(queued → sending)이라 두 작업이 같은 건을 가져가려 해도 한 곳만 성공한다.
// 같은 사람·종류의 가져온 건은 한 묶음(메일 한 통)이 된다.
func (s *Service) claim(ctx context.Context, now time.Time) ([]*claimed, error) {
	rows, err := s.o.DB.QueryContext(ctx, `SELECT d.id, d.user_id, d.kind, d.attempts, COALESCE(n.link, ''), COALESCE(d.digest_id, 0)
		FROM notification_deliveries d LEFT JOIN notifications n ON n.id = d.notification_id
		WHERE d.status = 'queued' AND d.channel = 'email' AND d.next_try_at <= ? ORDER BY d.next_try_at, d.id LIMIT 200`, ts(now))
	if err != nil {
		return nil, err
	}
	type cand struct {
		id, user int64
		kind     string
		attempts int
		link     string
		digest   int64
	}
	var cands []cand
	for rows.Next() {
		var c cand
		if err := rows.Scan(&c.id, &c.user, &c.kind, &c.attempts, &c.link, &c.digest); err != nil {
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
			continue // 다른 작업이 먼저 가져갔다
		}
		// 묶음 번호가 붙은 대기 건은 업무 시간 외 묶음 요약이다: 사람·묶음 번호가 같은 건은 종류와 상관없이 메일 한 통이 된다.
		key := fmt.Sprintf("%d|%s", c.user, c.kind)
		if c.digest != 0 {
			key = fmt.Sprintf("%d|digest|%d", c.user, c.digest)
		}
		g := byKey[key]
		if g == nil {
			g = &claimed{userID: c.user, kind: c.kind, link: c.link, attempts: c.attempts}
			if c.digest != 0 {
				g.summary, g.digestID, g.counts = true, c.digest, map[string]int{}
			}
			byKey[key] = g
			out = append(out, g)
		}
		if g.summary {
			g.counts[c.kind]++
		}
		g.ids = append(g.ids, c.id)
		if c.attempts > g.attempts {
			g.attempts = c.attempts
		}
	}
	return out, nil
}

func (s *Service) finish(ctx context.Context, ids []int64, to, code string, attempts int, next time.Time, digest int64) {
	now := s.now()
	for _, id := range ids {
		var err error
		switch to {
		case StatusSent:
			_, err = s.o.DB.ExecContext(ctx, `UPDATE notification_deliveries SET status = 'sent', sent_at = ?, digest_id = ?, last_error_code = '', claimed_at = '' WHERE id = ? AND status = 'sending'`, ts(now), nullID(digest), id)
		case StatusFailed:
			_, err = s.o.DB.ExecContext(ctx, `UPDATE notification_deliveries SET status = 'failed', last_error_code = ?, attempts = ?, claimed_at = '' WHERE id = ? AND status = 'sending'`, code, attempts, id)
		case StatusSkipped:
			// sending 에서 곧바로 skipped 로 갈 수 없으므로(전이 표) queued 로 돌린 뒤 건너뜀으로 바꾼다.
			if _, err = s.o.DB.ExecContext(ctx, `UPDATE notification_deliveries SET status = 'queued', claimed_at = '' WHERE id = ? AND status = 'sending'`, id); err == nil {
				_, err = s.o.DB.ExecContext(ctx, `UPDATE notification_deliveries SET status = 'skipped', last_error_code = ? WHERE id = ? AND status = 'queued'`, code, id)
			}
		case StatusQueued:
			_, err = s.o.DB.ExecContext(ctx, `UPDATE notification_deliveries SET status = 'queued', attempts = ?, next_try_at = ?, last_error_code = ?, claimed_at = '' WHERE id = ? AND status = 'sending'`, attempts, ts(next), code, id)
		}
		if err != nil {
			log.Printf("notify: 발송 건 %d 상태 갱신 실패: %v", id, err)
		}
	}
}

func nullID(id int64) any {
	if id == 0 {
		return nil
	}
	return id
}

// deliver 는 한 묶음(같은 사람·종류)을 보낸다. 건너뜀·실패·재시도를 가려 상태를 바꾼다.
func (s *Service) deliver(ctx context.Context, g *claimed) {
	now := s.now()
	if s.o.Mailer == nil {
		s.finish(ctx, g.ids, StatusSkipped, CodeNoMailer, g.attempts, now, 0)
		return
	}
	if strings.TrimSpace(s.o.BaseURL) == "" {
		s.finish(ctx, g.ids, StatusSkipped, CodeNoBaseURL, g.attempts, now, 0)
		return
	}
	var email string
	var active int
	if err := s.o.DB.QueryRowContext(ctx, `SELECT email, active FROM users WHERE id = ?`, g.userID).Scan(&email, &active); err != nil || active != 1 {
		s.finish(ctx, g.ids, StatusSkipped, CodeInactive, g.attempts, now, 0)
		return
	}
	if _, err := mail.ParseAddress(email); err != nil || strings.ContainsAny(email, "\r\n") {
		s.finish(ctx, g.ids, StatusFailed, CodeBadAddress, g.attempts, now, 0)
		return
	}
	var msg Message
	var err error
	if g.summary {
		msg, err = ComposeDigest(g.counts, s.o.BaseURL)
	} else {
		msg, err = Compose(g.kind, len(g.ids), s.o.BaseURL, g.link)
	}
	if err != nil {
		code := CodeBadLink
		if strings.Contains(err.Error(), "앱 주소") {
			code = CodeNoBaseURL
		}
		s.finish(ctx, g.ids, StatusFailed, code, g.attempts, now, 0)
		return
	}
	sctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	err = s.o.Mailer.Send(sctx, email, msg.Subject, msg.Body)
	if err == nil {
		var digest int64
		if len(g.ids) > 1 {
			digest = g.ids[0] // 합친 건은 같은 묶음 번호를 갖는다
		}
		if g.summary {
			digest = g.digestID
		}
		s.finish(ctx, g.ids, StatusSent, "", g.attempts, now, digest)
		return
	}
	code, retry := classify(err)
	attempts := g.attempts + 1
	max := intSetting(ctx, s.o.DB, "notify.max_attempts", 5)
	switch {
	case !retry:
		s.finish(ctx, g.ids, StatusFailed, code, attempts, now, 0)
	case attempts > max: // 재시도 5회(1분·5분·30분·2시간·6시간 뒤)까지 해 보고 그래도 실패하면 포기한다
		s.finish(ctx, g.ids, StatusFailed, CodeRetryOver, attempts, now, 0)
	default:
		idx := attempts - 1
		if idx >= len(backoff) {
			idx = len(backoff) - 1
		}
		s.finish(ctx, g.ids, StatusQueued, code, attempts, now.Add(backoff[idx]), 0)
	}
}

// admit 은 이번 주기에 보낼 묶음을 속도 제한 안에서 고른다: 서버 전체 분당 상한과 한 수신자의 시간당 상한(메일은 한 묶음이 한 통)을
// 이미 나간 건수 + 이번 주기에 고른 건수로 센다. 넘는 묶음은 한 분 뒤로 미룬다(실패·시도 횟수 증가 없음).
func (s *Service) admit(ctx context.Context, groups []*claimed, now time.Time) []*claimed {
	perMin := intSetting(ctx, s.o.DB, "notify.mail_per_min", 60)
	perUser := intSetting(ctx, s.o.DB, "notify.mail_per_user_hour", 30)
	var minute int
	_ = s.o.DB.QueryRowContext(ctx, `SELECT COUNT(DISTINCT COALESCE(digest_id, id)) FROM notification_deliveries WHERE channel='email' AND status='sent' AND sent_at >= ?`, ts(now.Add(-time.Minute))).Scan(&minute)
	perUserCount := map[int64]int{}
	var out []*claimed
	for _, g := range groups {
		n, seen := perUserCount[g.userID]
		if !seen {
			_ = s.o.DB.QueryRowContext(ctx, `SELECT COUNT(DISTINCT COALESCE(digest_id, id)) FROM notification_deliveries WHERE channel='email' AND status='sent' AND user_id = ? AND sent_at >= ?`, g.userID, ts(now.Add(-time.Hour))).Scan(&n)
		}
		if minute >= perMin || n >= perUser {
			perUserCount[g.userID] = n
			s.finish(ctx, g.ids, StatusQueued, "", g.attempts, now.Add(time.Minute), 0)
			continue
		}
		minute++
		perUserCount[g.userID] = n + 1
		out = append(out, g)
	}
	return out
}

// classify 는 발송 오류를 사유 코드와 재시도 여부로 나눈다: SMTP 4xx·네트워크·시간 초과는 재시도, SMTP 5xx·주소·헤더 오류는 즉시 포기다.
func classify(err error) (code string, retry bool) {
	var te *textproto.Error
	if errors.As(err, &te) {
		if te.Code >= 500 {
			return CodeSMTPPerm, false
		}
		return CodeSMTPTemp, true
	}
	if strings.Contains(err.Error(), "줄바꿈") {
		return CodeBadAddress, false
	}
	var ne net.Error
	if errors.As(err, &ne) || errors.Is(err, context.DeadlineExceeded) {
		return CodeNetwork, true
	}
	return CodeNetwork, true
}

// Run 은 서버가 도는 동안 발송 작업을 주기로 돈다(erp-mcp 는 돌지 않는다). ctx 가 끝나면 돌려준다.
func (s *Service) Run(ctx context.Context) {
	t := time.NewTicker(s.o.Poll)
	defer t.Stop()
	purge := time.NewTicker(time.Hour)
	defer purge.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := s.Tick(ctx); err != nil && ctx.Err() == nil {
				log.Printf("notify: 발송 작업 오류: %v", err)
			}
		case <-purge.C:
			if _, err := s.Purge(ctx); err != nil && ctx.Err() == nil {
				log.Printf("notify: 보존 정리 오류: %v", err)
			}
		}
	}
}
