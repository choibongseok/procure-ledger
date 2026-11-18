package webhooks

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/choibongseok/procure-ledger/internal/changes"
	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/holders"
	"github.com/choibongseok/procure-ledger/internal/netsafe"
	"github.com/choibongseok/procure-ledger/internal/repository"
)

// coalesce 는 새 발송 건이 기다리는 시간이다: 같은 객체의 연이은 변경이 한 번에 모여 들어오는 것을 너그럽게 받는다(명세 9.3).
const coalesce = 2 * time.Second

// Tick 은 한 번의 작업 주기다: 변경 피드를 발송 건으로 바꾸고(Scan), 보낼 때가 된 건을 보낸다(Dispatch). 보낸 건수를 돌려준다.
// 서버는 주기적으로(예: 2초) 부른다. 여러 번 동시에 불려도 같은 건이 두 번 나가지 않는다(claim 이 조건부 갱신이다).
func (s *Service) Tick(ctx context.Context) (int, error) {
	if err := s.Scan(ctx); err != nil {
		return 0, err
	}
	n, err := s.Dispatch(ctx)
	if err != nil {
		return n, err
	}
	return n, s.maybePurge(ctx)
}

// Scan 은 사용 중인 구독마다 피드의 새 줄을 필터해 발송 건으로 만든다. 같은 트랜잭션에서 구독의 위치(cursor_seq)를 옮기므로 줄이 빠지거나 두 번 만들어지지 않는다.
// 중지된 구독은 위치가 멈춰 있다가 재개하면 따라잡는다.
func (s *Service) Scan(ctx context.Context) error {
	rows, err := s.o.DB.QueryContext(ctx, `SELECT id FROM webhook_subscriptions WHERE active = 1 AND paused_reason = '' ORDER BY id`)
	if err != nil {
		return err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, id := range ids {
		for i := 0; i < scanBatchesLimit; i++ {
			more, err := s.scanOne(ctx, id)
			if err != nil {
				return err
			}
			if !more {
				break
			}
		}
	}
	return nil
}

func (s *Service) scanOne(ctx context.Context, id int64) (bool, error) {
	more := false
	err := s.o.Txs.WithTx(ctx, func(tx contract.Tx) error {
		sub, err := s.loadSub(ctx, tx, id)
		if err != nil {
			return err
		}
		if !sub.active || sub.paused != "" {
			return nil
		}
		evs, hasMore, head, err := changes.After(ctx, tx, sub.cursor, scanBatch, nil)
		now := s.now()
		if errors.Is(err, changes.ErrCursorExpired) {
			// 보존 기간이 지나 줄이 정리됐다: 빠진 구간은 되살릴 수 없으니 현재 위치로 건너뛰고 구독에 표시한다(소비자는 API 로 다시 적재한다).
			_, err := tx.ExecContext(ctx, `UPDATE webhook_subscriptions SET cursor_seq = ?, last_error_code = ?, last_error_at = ?, updated_at = ? WHERE id = ?`, head, CodeFeedGap, ts(now), ts(now), id)
			return err
		}
		if err != nil {
			return err
		}
		more = hasMore
		last := sub.cursor
		// 구독이 한 법인에 묶여 있으면(entity_id > 0) 그 법인의 변경만 발송한다(0 = 전체, B44).
		var subEntity int64
		_ = tx.QueryRowContext(ctx, `SELECT entity_id FROM webhook_subscriptions WHERE id = ?`, id).Scan(&subEntity)
		for _, e := range evs {
			last = e.Seq
			if subEntity > 0 && e.EntityID != subEntity {
				continue
			}
			if !matches(sub.events, e.Resource, e.Op, e.Status) {
				continue
			}
			if e.ActorChannel == string(contract.ChannelAPI) && !sub.includeAPI {
				continue // 연동 API 가 만든 변경은 기본으로 받지 않는다(되돌림 고리 방지, D16)
			}
			// 합치기: 같은 객체의 아직 창 안(next_try_at 이 미래)에 있는 첫 시도 전 대기 건이 있으면 그 건은 건너뜀(coalesced)으로 두고 이 건이 건수를 이어받는다.
			merged := 0
			var oldID int64
			var oldCo int
			switch err := tx.QueryRowContext(ctx, `SELECT id, coalesced FROM webhook_deliveries WHERE subscription_id = ? AND resource = ? AND object_id = ? AND status = 'queued' AND attempts = 0 AND next_try_at > ? ORDER BY event_seq DESC LIMIT 1`,
				id, e.Resource, e.ObjectID, ts(now)).Scan(&oldID, &oldCo); {
			case err == nil:
				if err := s.setStatus(ctx, tx, oldID, StatusQueued, StatusSkipped, `last_code = ?`, CodeCoalesced); err != nil {
					return err
				}
				merged = oldCo + 1
			case !errors.Is(err, sql.ErrNoRows):
				return err
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO webhook_deliveries (subscription_id, event_seq, resource, object_id, event_type, event_status, event_at, next_try_at, created_at, coalesced)
				VALUES (?,?,?,?,?,?,?,?,?,?) ON CONFLICT (subscription_id, event_seq) DO NOTHING`,
				id, e.Seq, e.Resource, e.ObjectID, e.Resource+"."+e.Op, e.Status, e.At, ts(now.Add(coalesce)), ts(now), merged); err != nil {
				return err
			}
		}
		if last != sub.cursor {
			_, err = tx.ExecContext(ctx, `UPDATE webhook_subscriptions SET cursor_seq = ? WHERE id = ?`, last, id)
		}
		return err
	})
	return more, err
}

type due struct {
	id, subID, seq, objectID  int64
	resource, typ, status, at string
	attempts                  int
	coalesced                 int
	test                      bool
}

// Dispatch 는 보낼 때가 된 발송 건을 구독별 분당 상한(webhook.per_min)과 서버 동시 상한 안에서 보낸다. 상한을 넘는 건은 대기로 두었다가 다음 주기로 미룬다(실패로 세지 않는다).
// 같은 구독의 같은 객체는 번호 순서로만 나간다(앞 건이 대기·전송 중이면 뒤 건은 기다린다).
func (s *Service) Dispatch(ctx context.Context) (int, error) {
	now := s.now()
	// 15분 넘게 전송 중인 건(서버가 중간에 멈춘 경우)은 대기로 되돌린다.
	if _, err := s.o.DB.ExecContext(ctx, `UPDATE webhook_deliveries SET status = 'queued' WHERE status = 'sending' AND claimed_at <> '' AND claimed_at < ?`, ts(now.Add(-reclaimAfter))); err != nil {
		return 0, err
	}
	perMin := perMinDefault
	if n, err := strconv.Atoi(s.setting(ctx, s.o.DB, "webhook.per_min")); err == nil && n > 0 {
		perMin = n
	}
	subs, err := s.activeSubs(ctx)
	if err != nil {
		return 0, err
	}
	var work []due
	for _, subID := range subs {
		var used int // 최근 1분에 이 구독으로 시도했거나 가져간 건수
		since := ts(now.Add(-time.Minute))
		if err := s.o.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM webhook_deliveries WHERE subscription_id = ? AND (claimed_at >= ? OR last_try_at >= ?)`, subID, since, since).Scan(&used); err != nil {
			return 0, err
		}
		budget := perMin - used
		if budget <= 0 {
			continue
		}
		if budget > dispatchBatch {
			budget = dispatchBatch
		}
		ds, err := s.dueFor(ctx, subID, now, budget)
		if err != nil {
			return 0, err
		}
		work = append(work, ds...)
	}
	var wg sync.WaitGroup
	sem := make(chan struct{}, s.o.Concurrency)
	var mu sync.Mutex
	sent := 0
	var firstErr error
	for _, d := range work {
		d := d
		claimed, err := s.claim(ctx, d.id)
		if err != nil {
			return sent, err
		}
		if !claimed {
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			err := s.deliver(ctx, d)
			mu.Lock()
			defer mu.Unlock()
			sent++
			if err != nil && firstErr == nil {
				firstErr = err
			}
		}()
	}
	wg.Wait()
	return sent, firstErr
}

func (s *Service) activeSubs(ctx context.Context) ([]int64, error) {
	rows, err := s.o.DB.QueryContext(ctx, `SELECT id FROM webhook_subscriptions WHERE active = 1 AND paused_reason = '' ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (s *Service) dueFor(ctx context.Context, subID int64, now time.Time, limit int) ([]due, error) {
	rows, err := s.o.DB.QueryContext(ctx, `SELECT d.id, d.subscription_id, d.event_seq, d.object_id, d.resource, d.event_type, d.event_status, d.event_at, d.attempts, d.coalesced
		FROM webhook_deliveries d
		WHERE d.subscription_id = ? AND d.status = 'queued' AND d.next_try_at <= ?
		  AND NOT EXISTS (SELECT 1 FROM webhook_deliveries p WHERE p.subscription_id = d.subscription_id AND p.resource = d.resource AND p.object_id = d.object_id
		                  AND p.event_seq < d.event_seq AND p.status IN ('queued','sending'))
		ORDER BY d.id LIMIT ?`, subID, ts(now), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []due
	for rows.Next() {
		var d due
		if err := rows.Scan(&d.id, &d.subID, &d.seq, &d.objectID, &d.resource, &d.typ, &d.status, &d.at, &d.attempts, &d.coalesced); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *Service) claim(ctx context.Context, id int64) (bool, error) {
	res, err := s.o.DB.ExecContext(ctx, `UPDATE webhook_deliveries SET status = 'sending', claimed_at = ? WHERE id = ? AND status = 'queued'`, ts(s.now()), id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// payload 는 요청 본문이다. 값(금액·이름·연락처)은 없고 자원·번호·동작·상태·API 주소만 있다.
type payload struct {
	ID         int64  `json:"id"`
	Type       string `json:"type"`
	Resource   string `json:"resource"`
	ObjectID   int64  `json:"object_id"`
	Op         string `json:"op"`
	Status     string `json:"status,omitempty"`
	OccurredAt string `json:"occurred_at"`
	APIURL     string `json:"api_url"`
	Test       bool   `json:"test,omitempty"`
}

var apiPaths = map[string]string{
	changes.ResItem: "items", changes.ResVendor: "vendors", changes.ResPurchaseOrder: "purchase-orders", changes.ResReceipt: "receipts", changes.ResJournal: "journals", changes.ResPayable: "payables", changes.ResTaxInvoice: "tax-invoices",
}

func (s *Service) buildBody(d due) ([]byte, error) {
	base := strings.TrimRight(s.o.BaseURL, "/")
	if base == "" {
		return nil, errNoBase
	}
	op := d.typ[strings.Index(d.typ, ".")+1:]
	pl := payload{ID: d.seq, Type: d.typ, Resource: d.resource, ObjectID: d.objectID, Op: op, Status: d.status, OccurredAt: d.at,
		APIURL: fmt.Sprintf("%s/api/v1/%s?id=%d", base, apiPaths[d.resource], d.objectID), Test: d.test}
	if d.test {
		pl.Type, pl.APIURL = "ping", base+"/api/v1/changes/head"
	}
	return json.Marshal(pl)
}

var errNoBase = errors.New("no base url")

// Sign 은 서명 값이다: hex(HMAC-SHA256(비밀, 시각 + "." + 본문)). 소비자는 같은 식으로 계산해 비교하고 시각이 5분 안인지 본다.
func Sign(secret string, unix int64, body []byte) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte(strconv.FormatInt(unix, 10)))
	m.Write([]byte("."))
	m.Write(body)
	return hex.EncodeToString(m.Sum(nil))
}

// signatureHeader 는 X-PL-Signature 값이다. 비밀 교체 뒤 24시간 동안은 옛 비밀의 서명도 쉼표로 이어 붙인다.
func (s *Service) signatureHeader(b secretBlob, unix int64, body []byte) string {
	h := "v1=" + Sign(b.Secret, unix, body)
	if b.Prev != "" {
		if until, err := time.Parse(time.RFC3339, b.PrevUntil); err == nil && s.now().Before(until) {
			h += ",v1=" + Sign(b.Prev, unix, body)
		}
	}
	return h
}

// result 는 한 번의 전송 결과다.
type result struct {
	ok         bool
	code       string // 실패 사유 코드
	retryable  bool
	retryAfter time.Duration
}

func classify(status int, retryAfter string) result {
	switch {
	case status >= 200 && status < 300:
		return result{ok: true}
	case status == 429:
		return result{code: CodeRate, retryable: true, retryAfter: parseRetryAfter(retryAfter)}
	case status == 410:
		return result{code: CodeGone}
	case status == 401 || status == 403:
		return result{code: CodeAuth}
	case status == 408 || status == 425:
		return result{code: Code5xx, retryable: true}
	case status >= 300 && status < 400:
		return result{code: CodeRedirect}
	case status >= 500:
		return result{code: Code5xx, retryable: true, retryAfter: parseRetryAfter(retryAfter)}
	default:
		return result{code: Code4xx}
	}
}

func parseRetryAfter(v string) time.Duration {
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || n <= 0 {
		return 0
	}
	d := time.Duration(n) * time.Second
	if d > retryAfterCap {
		d = retryAfterCap
	}
	return d
}

// post 는 구독 주소로 서명한 요청을 한 번 보낸다. 주소·응답 본문은 어디에도 남기지 않는다.
func (s *Service) post(ctx context.Context, sub subRow, d due) result {
	b, code := s.open(sub.sealed)
	if code != "" {
		return result{code: code}
	}
	body, err := s.buildBody(d)
	if errors.Is(err, errNoBase) {
		return result{code: CodeNoBaseURL}
	}
	if err != nil {
		return result{code: CodePayload}
	}
	opts := s.urlOptions(ctx, s.o.DB)
	u, err := netsafe.ValidateURL(b.URL, opts) // 허용 목록이 나중에 좁아졌을 수 있으니 보낼 때도 본다
	if err != nil {
		return result{code: CodeBlocked}
	}
	unix := s.now().Unix()
	rctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(rctx, http.MethodPost, u.String(), bytes.NewReader(body))
	if err != nil {
		return result{code: CodePayload}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "ProcureLedger-Webhook/1")
	req.Header.Set("X-PL-Event-Id", strconv.FormatInt(d.seq, 10))
	req.Header.Set("X-PL-Event-Type", d.typ)
	req.Header.Set("X-PL-Delivery", strconv.FormatInt(d.id, 10))
	if d.coalesced > 0 {
		req.Header.Set("X-PL-Coalesced", strconv.Itoa(d.coalesced)) // 창 안에서 이 건에 합쳐진 앞 건의 수
	}
	req.Header.Set("X-PL-Timestamp", strconv.FormatInt(unix, 10))
	req.Header.Set("X-PL-Signature", s.signatureHeader(b, unix, body))
	resp, err := s.client.Do(req)
	if err != nil {
		if errors.Is(err, netsafe.ErrBlockedAddr) {
			return result{code: CodeBlocked}
		}
		return result{code: CodeNetwork, retryable: true}
	}
	defer resp.Body.Close()
	_ = netsafe.ReadLimited(resp.Body)
	return classify(resp.StatusCode, resp.Header.Get("Retry-After"))
}

// deliver 는 claim 한 건을 보내고 결과를 한 트랜잭션으로 기록한다(건 상태 + 구독 연속 실패 + 필요하면 자동 중지와 감사).
func (s *Service) deliver(ctx context.Context, d due) error {
	sub, err := s.loadSub(ctx, s.o.DB, d.subID)
	var res result
	if err != nil {
		res = result{code: CodeBadSecret}
	} else {
		res = s.post(ctx, sub, d)
	}
	return s.finish(ctx, d, sub.name, res)
}

func (s *Service) finish(ctx context.Context, d due, subName string, res result) error {
	now := s.now()
	attempts := d.attempts + 1
	return s.o.Txs.WithTx(ctx, func(tx contract.Tx) error {
		switch {
		case res.ok:
			if err := s.setStatus(ctx, tx, d.id, StatusSending, StatusSent, `attempts = ?, last_try_at = ?, last_code = '', sent_at = ?`, attempts, ts(now), ts(now)); err != nil {
				return err
			}
			_, err := tx.ExecContext(ctx, `UPDATE webhook_subscriptions SET consecutive_failures = 0, last_ok_at = ?, last_error_code = '', last_error_at = '' WHERE id = ?`, ts(now), d.subID)
			return err
		case res.retryable && attempts <= maxRetries:
			wait := backoffFor(attempts)
			if res.retryAfter > wait {
				wait = res.retryAfter
			}
			return s.setStatus(ctx, tx, d.id, StatusSending, StatusQueued, `attempts = ?, last_try_at = ?, last_code = ?, next_try_at = ?, claimed_at = ''`, attempts, ts(now), res.code, ts(now.Add(wait)))
		}
		code := res.code
		if res.retryable {
			code = CodeRetryOver // 마지막 재시도까지 실패했다
		}
		if err := s.setStatus(ctx, tx, d.id, StatusSending, StatusFailed, `attempts = ?, last_try_at = ?, last_code = ?`, attempts, ts(now), code); err != nil {
			return err
		}
		return s.recordFinalFailure(ctx, tx, d, subName, code, now)
	})
}

// setStatus 는 상태 전이 표(CanTransition)에 있는 전이만 한다. 조건부 갱신이라 이미 다른 상태로 바뀐 건은 건드리지 않는다.
func (s *Service) setStatus(ctx context.Context, tx contract.Tx, id int64, from, to, set string, args ...any) error {
	if !CanTransition(from, to) {
		return fmt.Errorf("webhooks: 허용되지 않는 전이 %s → %s", from, to)
	}
	args = append(args, id, from)
	res, err := tx.ExecContext(ctx, `UPDATE webhook_deliveries SET status = '`+to+`', `+set+` WHERE id = ? AND status = ?`, args...)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return stateErr("발송 건의 상태가 이미 바뀌었습니다")
	}
	return nil
}

// recordFinalFailure 는 포기한 건 하나를 구독의 연속 실패에 더한다. 연속 5건이면 구독을 자동 중지한다(D8).
func (s *Service) recordFinalFailure(ctx context.Context, tx contract.Tx, d due, name, code string, now time.Time) error {
	if _, err := tx.ExecContext(ctx, `UPDATE webhook_subscriptions SET consecutive_failures = consecutive_failures + 1, last_error_code = ?, last_error_at = ? WHERE id = ?`, code, ts(now), d.subID); err != nil {
		return err
	}
	var fails int
	var active int
	var paused string
	if err := tx.QueryRowContext(ctx, `SELECT consecutive_failures, active, paused_reason FROM webhook_subscriptions WHERE id = ?`, d.subID).Scan(&fails, &active, &paused); err != nil {
		return err
	}
	if fails < autoPauseAfter || active != 1 || paused != "" {
		return nil
	}
	if _, err := tx.ExecContext(ctx, `UPDATE webhook_subscriptions SET paused_reason = 'auto', updated_at = ? WHERE id = ?`, ts(now), d.subID); err != nil {
		return err
	}
	if err := s.record(ctx, tx, systemActor, "webhook.sub_auto_pause", d.subID, audit.DiffItem{Key: "name", New: name}, audit.DiffItem{Key: "failures", New: strconv.Itoa(fails)}, audit.DiffItem{Key: "result_code", New: code}); err != nil {
		return err
	}
	return s.notifyPaused(ctx, tx, d.subID, name, fails, now)
}

// notifyPaused 는 자동 중지를 관리 권한자에게 알린다(앱 안 알림 + 메일 기본 켜짐인 종류 webhook_paused). 같은 트랜잭션이고, 발송 건 생성 실패는 알림 자체를 막지 않는다.
// 알림 문구에는 구독 이름·횟수뿐이고 주소·비밀·사유 본문은 없다.
func (s *Service) notifyPaused(ctx context.Context, tx contract.Tx, subID int64, name string, fails int, now time.Time) error {
	if s.o.ManagePerm == "" {
		return nil
	}
	// 구독이 한 법인에 묶여 있으면(entity_id > 0) 그 법인에서 권한이 효력인 사람에게만, 전체(0)면 권한을 가진 활성 사용자 전원에게 알린다(B44).
	var subEntity int64
	_ = tx.QueryRowContext(ctx, `SELECT entity_id FROM webhook_subscriptions WHERE id = ?`, subID).Scan(&subEntity)
	var ids []int64
	if subEntity > 0 {
		var err error
		if ids, err = holders.Of(ctx, tx, s.o.ManagePerm, subEntity, contract.DayOf(now)); err != nil {
			return err
		}
	} else {
		rows, err := tx.QueryContext(ctx, `SELECT DISTINCT e.user_id FROM user_effective_permissions e JOIN users u ON u.id = e.user_id AND u.active = 1 WHERE e.permission = ? ORDER BY e.user_id`, string(s.o.ManagePerm))
		if err != nil {
			return err
		}
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			ids = append(ids, id)
		}
		if err := rows.Close(); err != nil {
			return err
		}
	}
	for _, uid := range ids {
		nid, err := repository.SQLNotifications{}.Insert(ctx, tx, repository.NotificationInsert{UserID: uid, Kind: "webhook_paused", Title: "변경 웹훅 구독이 자동 중지되었습니다",
			Body: fmt.Sprintf("'%s' 구독의 발송이 연속 %d번 실패해 멈췄습니다. 변경 웹훅 화면에서 시험 보내기로 확인한 뒤 다시 켜세요.", name, fails),
			Link: "/admin/integrations/webhooks", RefType: "webhook_subscription", RefID: subID, Now: now})
		if err != nil {
			return err
		}
		if s.o.Deliver != nil {
			if derr := s.o.Deliver(ctx, tx, nid, uid, "webhook_paused", now); derr != nil {
				log.Printf("webhooks: 자동 중지 알림의 발송 건 생성 실패(앱 안 알림은 만들어짐, 알림 %d): %v", nid, derr)
			}
		}
	}
	return nil
}

// ---- 보존 정리 ----

const purgeEvery = time.Hour

func (s *Service) maybePurge(ctx context.Context) error {
	s.mu.Lock()
	now := s.now()
	if !s.lastPurge.IsZero() && now.Sub(s.lastPurge) < purgeEvery {
		s.mu.Unlock()
		return nil
	}
	s.lastPurge = now
	s.mu.Unlock()
	_, err := s.Purge(ctx)
	return err
}

// Purge 는 webhook.keep_days(기본 365) 보다 오래된 발송 건 중 전달됨(sent)·건너뜀(skipped)만 지운다. 아직 못 보낸 건(대기·전송 중·실패)은 나이와 상관없이 남긴다
// (실패는 관리자가 다시 보낼 수 있다). 지운 건수를 돌려준다.
func (s *Service) Purge(ctx context.Context) (int64, error) {
	days := 365
	if n, err := strconv.Atoi(s.setting(ctx, s.o.DB, "webhook.keep_days")); err == nil && n > 0 {
		days = n
	}
	res, err := s.o.DB.ExecContext(ctx, `DELETE FROM webhook_deliveries WHERE status IN ('sent','skipped') AND created_at < ?`, ts(s.now().AddDate(0, 0, -days)))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// FeedKeepFloor 는 변경 피드 보존 정리(D12, apitoken.PurgeChanges)가 지우면 안 되는 경계다: 이 번호 이하의 줄만 지울 수 있다. 사용 중인 구독(중지되지 않은 것)이 아직 읽지 않은
// 줄(cursor_seq 보다 큰 번호)은 남겨야 하므로 그 구독들의 cursor_seq 최솟값이다(사용 중인 구독이 없으면 ok=false, 제한 없음). 중지된 구독은 붙잡지 않는다: 재개 때 커서 만료로
// 건너뛰고 feed_gap 이 표시된다. 발송 건은 피드 줄의 자원·번호·동작·상태를 복사해 두므로 건 자체는 줄 보존에 의존하지 않는다.
func FeedKeepFloor(ctx context.Context, q contract.Tx) (floor int64, ok bool, err error) {
	var v sql.NullInt64
	if err = q.QueryRowContext(ctx, `SELECT MIN(cursor_seq) FROM webhook_subscriptions WHERE active = 1 AND paused_reason = ''`).Scan(&v); err != nil {
		return 0, false, err
	}
	return v.Int64, v.Valid, nil
}

// ---- 시험 보내기·발송 기록·다시 보내기 ----

// SendTest 는 시험 이벤트(test:true)를 지금 한 번 보낸다. 발송 건·연속 실패 횟수에 영향이 없고, 결과의 한국어 문장만 돌려준다(비어 있으면 성공).
func (s *Service) SendTest(ctx context.Context, id int64) (string, error) {
	p, err := s.authorize(ctx)
	if err != nil {
		return "", err
	}
	sub, err := s.loadSub(ctx, s.o.DB, id)
	if err != nil {
		return "", err
	}
	d := due{subID: id, resource: changes.ResItem, typ: changes.ResItem + "." + changes.OpUpdated, at: ts(s.now()), test: true}
	res := s.post(ctx, sub, d)
	err = s.o.Txs.WithTx(ctx, func(tx contract.Tx) error {
		result := "성공"
		if !res.ok {
			result = res.code
		}
		return s.record(ctx, tx, p, "webhook.sub_test", id, audit.DiffItem{Key: "name", New: sub.name}, audit.DiffItem{Key: "result_code", New: result})
	})
	if err != nil {
		return "", err
	}
	if res.ok {
		return "", nil
	}
	return ReasonText(res.code), nil
}

// DeliveryView 는 발송 기록 한 건이다(본문·주소·응답 없음).
type DeliveryView struct {
	ID        int64  `json:"id"`
	EventSeq  int64  `json:"eventSeq"`
	EventType string `json:"eventType"`
	Title     string `json:"title"` // 한국어 이벤트 이름
	ObjectID  int64  `json:"objectId"`
	Status    string `json:"status"`
	State     string `json:"state"` // 한국어 상태
	Attempts  int    `json:"attempts"`
	Reason    string `json:"reason"` // 한국어 사유
	NextTryAt string `json:"nextTryAt"`
	SentAt    string `json:"sentAt"`
	CreatedAt string `json:"createdAt"`
}

var statusText = map[string]string{StatusQueued: "대기", StatusSending: "전송 중", StatusSent: "전달됨", StatusFailed: "실패(포기)", StatusSkipped: "건너뜀"}

// Deliveries 는 구독의 발송 기록이다(최근 순, status 로 거를 수 있다).
func (s *Service) Deliveries(ctx context.Context, subID int64, status string, limit int) ([]DeliveryView, error) {
	if _, err := s.authorize(ctx); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	q := `SELECT id, event_seq, event_type, object_id, status, attempts, last_code, next_try_at, sent_at, created_at FROM webhook_deliveries WHERE subscription_id = ?`
	args := []any{subID}
	if status != "" {
		q += ` AND status = ?`
		args = append(args, status)
	}
	rows, err := s.o.DB.QueryContext(ctx, q+` ORDER BY id DESC LIMIT ?`, append(args, limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DeliveryView
	for rows.Next() {
		var v DeliveryView
		var code string
		if err := rows.Scan(&v.ID, &v.EventSeq, &v.EventType, &v.ObjectID, &v.Status, &v.Attempts, &code, &v.NextTryAt, &v.SentAt, &v.CreatedAt); err != nil {
			return nil, err
		}
		v.State, v.Reason, v.Title = statusText[v.Status], ReasonText(code), eventTitle(v.EventType)
		out = append(out, v)
	}
	return out, rows.Err()
}

func eventTitle(typ string) string {
	i := strings.Index(typ, ".")
	if i < 0 {
		return typ
	}
	return resourceTitles[typ[:i]] + " " + opTitles[typ[i+1:]]
}

// Resend 는 포기한 건(실패)을 대기로 되돌려 다시 보낸다. 건마다 5번까지.
func (s *Service) Resend(ctx context.Context, deliveryID int64) error {
	p, err := s.authorize(ctx)
	if err != nil {
		return err
	}
	return s.o.Txs.WithTx(ctx, func(tx contract.Tx) error {
		var subID int64
		var status string
		var resends int
		err := tx.QueryRowContext(ctx, `SELECT subscription_id, status, resends FROM webhook_deliveries WHERE id = ?`, deliveryID).Scan(&subID, &status, &resends)
		if errors.Is(err, sql.ErrNoRows) {
			return contract.ErrNotFound
		}
		if err != nil {
			return err
		}
		if !CanTransition(status, StatusQueued) || status != StatusFailed {
			return stateErr("실패한 건만 다시 보낼 수 있습니다.")
		}
		if resends >= maxResends {
			return stateErr("한 건은 %d번까지만 다시 보낼 수 있습니다.", maxResends)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE webhook_deliveries SET status = 'queued', attempts = 0, resends = resends + 1, next_try_at = ?, claimed_at = '', last_code = '' WHERE id = ? AND status = 'failed'`, ts(s.now()), deliveryID); err != nil {
			return err
		}
		return s.record(ctx, tx, p, "webhook.delivery_resend", subID, audit.DiffItem{Key: "delivery_id", New: strconv.FormatInt(deliveryID, 10)})
	})
}
