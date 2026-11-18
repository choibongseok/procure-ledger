package webhooks

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/choibongseok/procure-ledger/internal/changes"
	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/dbutil"
	"github.com/choibongseok/procure-ledger/internal/netsafe"
)

// Service 는 구독 관리와 발송 작업이다.
type Service struct {
	o      Options
	client *http.Client

	mu        sync.Mutex
	lastPurge time.Time
}

// New 는 Service 를 만든다.
func New(o Options) *Service {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Concurrency <= 0 {
		o.Concurrency = 8
	}
	c := o.Client
	if c == nil {
		c = netsafe.NewClient(netsafe.ClientOptions{AllowLocal: o.AllowLocal})
	}
	return &Service{o: o, client: c}
}

func (s *Service) now() time.Time { return s.o.Now().UTC() }

const tsLayout = "2006-01-02T15:04:05Z"

func ts(t time.Time) string { return t.UTC().Format(tsLayout) }

func valErr(format string, a ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{contract.ErrValidation}, a...)...)
}

func stateErr(format string, a ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{contract.ErrInvalidState}, a...)...)
}

// systemActor 는 사용자 없는 시스템 작업(자동 중지)의 감사 행위자다(알림 웹훅의 자동 중지와 같다).
var systemActor = contract.Principal{Channel: contract.ChannelWeb}

// authorize 는 관리 호출의 권한을 확인한다. ManagePerm 이 비어 있으면(연결 전) 거부한다.
func (s *Service) authorize(ctx context.Context) (contract.Principal, error) {
	if s.o.ManagePerm == "" {
		return contract.Principal{}, fmt.Errorf("%w: 웹훅 관리 권한이 연결되지 않았습니다", contract.ErrForbidden)
	}
	return contract.Authorize(ctx, s.o.ManagePerm)
}

// ---- 이벤트 종류 ----

// EventType 은 구독이 고를 수 있는 이벤트 종류 하나다(화면의 체크 목록).
type EventType struct {
	Code  string `json:"code"`  // 자원.동작 또는 자원.동작:상태
	Title string `json:"title"` // 한국어 이름
}

var resourceTitles = map[string]string{
	changes.ResItem: "품목", changes.ResVendor: "거래처", changes.ResPurchaseOrder: "발주", changes.ResReceipt: "입고", changes.ResJournal: "전표", changes.ResPayable: "채무", changes.ResTaxInvoice: "세금계산서",
}

var opTitles = map[string]string{
	changes.OpCreated: "생성", changes.OpUpdated: "변경", changes.OpStatusChanged: "상태 변경", changes.OpCanceled: "취소",
}

// 상태가 있는 자원의 상태 값(상태 변경 이벤트를 상태별로 고를 수 있다).
var resourceStatuses = map[string]map[string]string{
	changes.ResPurchaseOrder: {"open": "입고 전", "partial": "일부 입고", "received": "입고 완료"},
	changes.ResJournal:       {"draft": "임시", "requested": "승인 요청", "approved": "승인", "canceled": "취소"},
	changes.ResTaxInvoice:    {"issued": "발행 대기", "sent": "전송됨", "accepted": "국세청 수신", "failed": "실패", "canceled": "취소"},
}

// EventTypes 는 구독이 고를 수 있는 이벤트 종류 전체다(자원 × 동작, 상태가 있는 자원의 상태 변경은 상태별로도).
func EventTypes() []EventType {
	var out []EventType
	for _, r := range changes.Resources {
		for _, op := range []string{changes.OpCreated, changes.OpUpdated, changes.OpStatusChanged, changes.OpCanceled} {
			out = append(out, EventType{Code: r + "." + op, Title: resourceTitles[r] + " " + opTitles[op]})
			if op == changes.OpStatusChanged {
				states := resourceStatuses[r]
				keys := make([]string, 0, len(states))
				for k := range states {
					keys = append(keys, k)
				}
				sort.Strings(keys)
				for _, k := range keys {
					out = append(out, EventType{Code: r + "." + op + ":" + k, Title: resourceTitles[r] + " " + states[k] + "(으)로 상태 변경"})
				}
			}
		}
	}
	return out
}

func validEventCode(code string) bool {
	for _, e := range EventTypes() {
		if e.Code == code {
			return true
		}
	}
	return false
}

// matches 는 피드 줄(자원·동작·상태)이 구독의 이벤트 목록과 맞는지다. "자원.동작" 은 모든 상태에, "자원.동작:상태" 는 그 상태에만 맞는다.
func matches(events []string, resource, op, status string) bool {
	base := resource + "." + op
	for _, e := range events {
		if e == base || (status != "" && e == base+":"+status) {
			return true
		}
	}
	return false
}

// ---- 구독 ----

// secretBlob 은 봉인해 저장하는 값이다(주소·서명 비밀·교체 중인 옛 비밀).
type secretBlob struct {
	URL       string `json:"url"`
	Secret    string `json:"secret"`
	Prev      string `json:"prev,omitempty"`
	PrevUntil string `json:"prev_until,omitempty"` // RFC3339
}

// View 는 구독의 화면·응답용 표현이다. 주소·서명 비밀은 어디에도 없다(호스트만).
type View struct {
	ID           int64    `json:"id"`
	Name         string   `json:"name"`
	Hint         string   `json:"hint"`
	Events       []string `json:"events"`
	IncludeAPI   bool     `json:"includeApi"`
	Active       bool     `json:"active"`
	PausedReason string   `json:"pausedReason"` // "" | auto | manual
	State        string   `json:"state"`        // 한국어 상태
	Failures     int      `json:"failures"`
	LastOKAt     string   `json:"lastOkAt"`
	LastError    string   `json:"lastError"` // 한국어 문장
	LastErrorAt  string   `json:"lastErrorAt"`
	Queued       int      `json:"queued"`
	Failed       int      `json:"failed"`
	CreatedAt    string   `json:"createdAt"`
	// Entity 는 구독이 묶인 법인 이름이다(0 이면 "전체 법인"). 활성 법인이 둘 이상인 설치에서만 채워진다(하나면 빠져 응답이 그대로다, B44).
	Entity string `json:"entity,omitempty"`
}

// Created 는 등록 결과다. Secret 은 이 응답에서 한 번만 보이고 다시 볼 수 없다(잃으면 비밀을 교체한다).
type Created struct {
	View   View   `json:"subscription"`
	Secret string `json:"secret"`
}

// CreateInput 은 구독 등록 입력이다.
type CreateInput struct {
	Name       string
	URL        string
	Events     []string
	IncludeAPI bool // 연동 API 가 만든 변경도 받을지(기본 아니오: 되돌림 고리 방지, D16)
	// EntityID 는 구독이 묶일 법인이다(B44). 0 이면 권한자에게 그룹 범위가 있을 때 전체(0), 아니면 현재 법인(없으면 기본 법인). 양수는 권한자의 범위 안일 때만 된다.
	EntityID int64
}

func (s *Service) setting(ctx context.Context, q contract.Tx, key string) string {
	var v string
	if err := q.QueryRowContext(ctx, `SELECT value FROM app_settings WHERE key = ?`, key).Scan(&v); err != nil {
		return ""
	}
	return strings.TrimSpace(v)
}

func splitList(v string) []string {
	var out []string
	for _, p := range strings.Split(v, ",") {
		if p = strings.ToLower(strings.TrimSpace(p)); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// urlOptions 는 주소 검사 설정이다: 허용 호스트 목록(webhook.allow_hosts)이 비어 있으면 공인 IP 인 모든 호스트(연결 직전 검사가 막는다), 있으면 그 목록 밖은 거부한다(D5).
func (s *Service) urlOptions(ctx context.Context, q contract.Tx) netsafe.URLOptions {
	o := netsafe.URLOptions{AllowLocal: s.o.AllowLocal, Ports: splitList(s.setting(ctx, q, "webhook.extra_ports"))}
	if hosts := splitList(s.setting(ctx, q, "webhook.allow_hosts")); len(hosts) > 0 {
		o.AllowedHost = func(host string) bool {
			for _, p := range hosts {
				if netsafe.HostMatches(p, host) {
					return true
				}
			}
			return false
		}
	}
	return o
}

func (s *Service) resolveCheck(ctx context.Context, host string) error {
	if s.o.Resolve != nil {
		return s.o.Resolve(ctx, host)
	}
	if err := netsafe.ResolveCheck(ctx, host, s.o.AllowLocal, nil); err != nil {
		return valErr("%s", err.Error())
	}
	return nil
}

func (s *Service) seal(b secretBlob) (string, error) {
	raw, err := json.Marshal(b)
	if err != nil {
		return "", err
	}
	return s.o.Sealer.SealFor(sealPurpose, string(raw))
}

func (s *Service) open(stored string) (secretBlob, string) {
	var b secretBlob
	if s.o.Sealer == nil {
		return b, CodeNoSealer
	}
	raw, err := s.o.Sealer.OpenFor(sealPurpose, stored)
	if err != nil || json.Unmarshal([]byte(raw), &b) != nil || b.URL == "" || b.Secret == "" {
		return secretBlob{}, CodeBadSecret
	}
	return b, ""
}

func newSecret() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "whsec_" + base64.RawURLEncoding.EncodeToString(b), nil
}

// hint 는 구독 주소의 호스트만 돌려준다. 경로·쿼리는 어떤 부분도 싣지 않는다(주소 끝이 토큰인 수신처가 있어 끝 몇 글자도 노출하면 안 된다).
func hint(rawURL string) string {
	host := rawURL
	if i := strings.Index(rawURL, "://"); i >= 0 {
		host = rawURL[i+3:]
	}
	if i := strings.IndexAny(host, "/?#"); i >= 0 {
		host = host[:i]
	}
	if i := strings.LastIndex(host, "@"); i >= 0 { // 주소 안의 사용자 정보는 호스트가 아니다
		host = host[i+1:]
	}
	return host
}

// hostOnly 는 저장된 hint 를 표시할 때 호스트만 남긴다: 이전에 저장된 "호스트…끝6자" 형식의 끝 부분을 마이그레이션 없이 화면·응답에서 가린다.
func hostOnly(stored string) string {
	if i := strings.Index(stored, "…"); i >= 0 {
		return stored[:i]
	}
	return stored
}

func normalizeEvents(in []string) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	for _, e := range in {
		e = strings.TrimSpace(e)
		if e == "" || seen[e] {
			continue
		}
		if !validEventCode(e) {
			return nil, valErr("알 수 없는 이벤트 종류입니다.")
		}
		seen[e] = true
		out = append(out, e)
	}
	if len(out) == 0 {
		return nil, valErr("받을 이벤트 종류를 하나 이상 고르세요.")
	}
	if len(out) > maxEventsPerSub {
		return nil, valErr("이벤트 종류는 %d개까지 고를 수 있습니다.", maxEventsPerSub)
	}
	sort.Strings(out)
	return out, nil
}

func validName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if n := utf8.RuneCountInString(name); n < 1 || n > 40 {
		return "", valErr("이름은 1~40자여야 합니다.")
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			return "", valErr("이름에 제어문자를 쓸 수 없습니다.")
		}
	}
	return name, nil
}

func (s *Service) record(ctx context.Context, tx contract.Tx, p contract.Principal, action string, id int64, diff ...audit.DiffItem) error {
	return s.o.Audit.Record(ctx, tx, audit.Entry{Actor: p, Action: action, TargetType: audit.TargetWebhook, TargetID: strconv.FormatInt(id, 10), Result: audit.ResultSuccess, Diff: diff})
}

// Create 는 구독을 등록한다. 주소·서명 비밀은 한 덩어리로 봉인해 저장하고 감사에는 어느 쪽도 없다(주소가 바뀌었는지도 아니다). 서명 비밀은 응답에서 한 번만 돌려준다.
// 새 구독은 등록한 시점의 변경 피드 위치부터 받는다.
func (s *Service) Create(ctx context.Context, in CreateInput) (Created, error) {
	p, err := s.authorize(ctx)
	if err != nil {
		return Created{}, err
	}
	if s.o.Sealer == nil {
		return Created{}, stateErr("구독 정보를 안전하게 저장할 키(PL_ACCOUNT_SECRET_KEY)가 서버에 없어 등록할 수 없습니다.")
	}
	name, err := validName(in.Name)
	if err != nil {
		return Created{}, err
	}
	subEntity := in.EntityID
	switch {
	case subEntity > 0:
		if !p.CanIn(s.o.ManagePerm, subEntity) {
			return Created{}, fmt.Errorf("%w: 그 법인에서 웹훅을 관리할 권한이 없습니다", contract.ErrForbidden)
		}
	case p.EntitiesFor(s.o.ManagePerm).All:
		subEntity = 0
	default:
		if subEntity = contract.EntityFrom(ctx); subEntity <= 0 {
			subEntity = contract.DefaultEntityID
		}
		if !p.CanIn(s.o.ManagePerm, subEntity) {
			return Created{}, fmt.Errorf("%w: 그 법인에서 웹훅을 관리할 권한이 없습니다", contract.ErrForbidden)
		}
	}
	events, err := normalizeEvents(in.Events)
	if err != nil {
		return Created{}, err
	}
	u, err := netsafe.ValidateURL(in.URL, s.urlOptions(ctx, s.o.DB))
	if err != nil {
		return Created{}, valErr("%s", err.Error())
	}
	if !(s.o.AllowLocal && isLocalHost(u.Hostname())) {
		if err := s.resolveCheck(ctx, u.Hostname()); err != nil {
			return Created{}, err
		}
	}
	secret, err := newSecret()
	if err != nil {
		return Created{}, err
	}
	sealed, err := s.seal(secretBlob{URL: strings.TrimSpace(in.URL), Secret: secret})
	if err != nil {
		return Created{}, err
	}
	evJSON, _ := json.Marshal(events)
	now := ts(s.now())
	var id int64
	err = s.o.Txs.WithTx(ctx, func(tx contract.Tx) error {
		head, err := changes.Head(ctx, tx)
		if err != nil {
			return err
		}
		err = tx.QueryRowContext(ctx, `INSERT INTO webhook_subscriptions (name, secret_sealed, hint, events_json, include_api, cursor_seq, created_by, created_at, updated_at, entity_id) VALUES (?,?,?,?,?,?,?,?,?,?) RETURNING id`,
			name, sealed, hint(strings.TrimSpace(in.URL)), string(evJSON), boolInt(in.IncludeAPI), head, nullID(p.UserID), now, now, subEntity).Scan(&id)
		if err != nil {
			if dbutil.IsUnique(err) {
				return valErr("같은 이름의 구독이 이미 있습니다.")
			}
			return err
		}
		return s.record(ctx, tx, p, "webhook.sub_save", id, audit.DiffItem{Key: "name", New: name}, audit.DiffItem{Key: "events", New: strconv.Itoa(len(events)) + "종"},
			audit.DiffItem{Key: "include_api", New: yesNo(in.IncludeAPI)})
	})
	if err != nil {
		return Created{}, err
	}
	v, err := s.view(ctx, id)
	if err != nil {
		return Created{}, err
	}
	return Created{View: v, Secret: secret}, nil
}

func isLocalHost(h string) bool {
	if strings.EqualFold(h, "localhost") {
		return true
	}
	ip, err := netip.ParseAddr(h)
	return err == nil && ip.IsLoopback()
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func yesNo(b bool) string {
	if b {
		return "예"
	}
	return "아니오"
}

func nullID(id int64) any {
	if id == 0 {
		return nil
	}
	return id
}

type subRow struct {
	id         int64
	name       string
	sealed     string
	hint       string
	events     []string
	includeAPI bool
	active     bool
	paused     string
	failures   int
	cursor     int64
}

const subCols = `id, name, secret_sealed, hint, events_json, include_api, active, paused_reason, consecutive_failures, cursor_seq`

func scanSub(sc interface{ Scan(...any) error }) (subRow, error) {
	var r subRow
	var ev string
	var inc, act int
	if err := sc.Scan(&r.id, &r.name, &r.sealed, &r.hint, &ev, &inc, &act, &r.paused, &r.failures, &r.cursor); err != nil {
		return r, err
	}
	_ = json.Unmarshal([]byte(ev), &r.events)
	r.includeAPI, r.active = inc == 1, act == 1
	return r, nil
}

func (s *Service) loadSub(ctx context.Context, q contract.Tx, id int64) (subRow, error) {
	r, err := scanSub(q.QueryRowContext(ctx, `SELECT `+subCols+` FROM webhook_subscriptions WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return r, contract.ErrNotFound
	}
	return r, err
}

func stateText(active bool, paused string) string {
	switch {
	case paused == "auto":
		return "자동 중지(연속 실패)"
	case paused == "manual" || !active:
		return "중지됨"
	}
	return "사용 중"
}

func (s *Service) view(ctx context.Context, id int64) (View, error) {
	vs, err := s.views(ctx, id)
	if err != nil {
		return View{}, err
	}
	if len(vs) == 0 {
		return View{}, contract.ErrNotFound
	}
	return vs[0], nil
}

func (s *Service) views(ctx context.Context, only int64) ([]View, error) {
	q := `SELECT id, name, hint, events_json, include_api, active, paused_reason, consecutive_failures, last_ok_at, last_error_code, last_error_at, created_at,
		(SELECT COUNT(*) FROM webhook_deliveries d WHERE d.subscription_id = s.id AND d.status IN ('queued','sending')),
		(SELECT COUNT(*) FROM webhook_deliveries d WHERE d.subscription_id = s.id AND d.status = 'failed'),
		s.entity_id, COALESCE((SELECT e.name FROM entities e WHERE e.id = s.entity_id),'')
		FROM webhook_subscriptions s`
	var args []any
	if only != 0 {
		q += ` WHERE id = ?`
		args = append(args, only)
	}
	q += ` ORDER BY id`
	var activeEntities int
	if err := s.o.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM entities WHERE active = 1`).Scan(&activeEntities); err != nil {
		return nil, err
	}
	rows, err := s.o.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []View
	for rows.Next() {
		var v View
		var ev, code, entName string
		var inc, act int
		var entID int64
		if err := rows.Scan(&v.ID, &v.Name, &v.Hint, &ev, &inc, &act, &v.PausedReason, &v.Failures, &v.LastOKAt, &code, &v.LastErrorAt, &v.CreatedAt, &v.Queued, &v.Failed, &entID, &entName); err != nil {
			return nil, err
		}
		if activeEntities > 1 {
			v.Entity = entName
			if entID == 0 {
				v.Entity = "전체 법인"
			}
		}
		_ = json.Unmarshal([]byte(ev), &v.Events)
		v.Hint = hostOnly(v.Hint)
		v.IncludeAPI, v.Active = inc == 1, act == 1
		v.State = stateText(v.Active, v.PausedReason)
		v.LastError = ReasonText(code)
		out = append(out, v)
	}
	return out, rows.Err()
}

// List 는 구독 목록이다(주소·비밀 없음).
func (s *Service) List(ctx context.Context) ([]View, error) {
	if _, err := s.authorize(ctx); err != nil {
		return nil, err
	}
	return s.views(ctx, 0)
}

// Pause 는 구독을 중지한다. 이미 쌓인 대기 건은 그대로 두었다가 재개하면 이어서 보낸다(중지 중의 변경도 피드 위치가 그대로라 재개 때 따라잡는다).
func (s *Service) Pause(ctx context.Context, id int64) (View, error) {
	return s.setPaused(ctx, id, true)
}

// Resume 은 구독을 다시 켠다. 연속 실패 횟수를 지운다.
func (s *Service) Resume(ctx context.Context, id int64) (View, error) {
	return s.setPaused(ctx, id, false)
}

func (s *Service) setPaused(ctx context.Context, id int64, pause bool) (View, error) {
	p, err := s.authorize(ctx)
	if err != nil {
		return View{}, err
	}
	err = s.o.Txs.WithTx(ctx, func(tx contract.Tx) error {
		sub, err := s.loadSub(ctx, tx, id)
		if err != nil {
			return err
		}
		now := ts(s.now())
		if pause {
			if _, err := tx.ExecContext(ctx, `UPDATE webhook_subscriptions SET paused_reason = 'manual', updated_at = ? WHERE id = ?`, now, id); err != nil {
				return err
			}
			return s.record(ctx, tx, p, "webhook.sub_pause", id, audit.DiffItem{Key: "name", New: sub.name})
		}
		if _, err := tx.ExecContext(ctx, `UPDATE webhook_subscriptions SET paused_reason = '', active = 1, consecutive_failures = 0, updated_at = ? WHERE id = ?`, now, id); err != nil {
			return err
		}
		return s.record(ctx, tx, p, "webhook.sub_resume", id, audit.DiffItem{Key: "name", New: sub.name})
	})
	if err != nil {
		return View{}, err
	}
	return s.view(ctx, id)
}

// RotateSecret 은 서명 비밀을 새로 만든다. 새 비밀은 이 응답에서 한 번만 보인다. 24시간 동안은 옛 비밀의 서명도 함께 보내 끊김 없이 교체한다.
func (s *Service) RotateSecret(ctx context.Context, id int64) (string, error) {
	p, err := s.authorize(ctx)
	if err != nil {
		return "", err
	}
	if s.o.Sealer == nil {
		return "", stateErr("구독 정보를 안전하게 저장할 키가 서버에 없어 비밀을 교체할 수 없습니다.")
	}
	secret, err := newSecret()
	if err != nil {
		return "", err
	}
	err = s.o.Txs.WithTx(ctx, func(tx contract.Tx) error {
		sub, err := s.loadSub(ctx, tx, id)
		if err != nil {
			return err
		}
		b, code := s.open(sub.sealed)
		if code != "" {
			return stateErr("%s", ReasonText(code))
		}
		b.Prev, b.PrevUntil, b.Secret = b.Secret, s.now().Add(rotateOverlap).Format(time.RFC3339), secret
		sealed, err := s.seal(b)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE webhook_subscriptions SET secret_sealed = ?, updated_at = ? WHERE id = ?`, sealed, ts(s.now()), id); err != nil {
			return err
		}
		return s.record(ctx, tx, p, "webhook.sub_secret_rotate", id, audit.DiffItem{Key: "name", New: sub.name}, audit.DiffItem{Key: "overlap_hours", New: "24"})
	})
	if err != nil {
		return "", err
	}
	return secret, nil
}
