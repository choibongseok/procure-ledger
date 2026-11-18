package notify

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
)

// 웹훅 대상 관리(B36 단계 2, 명세 4.6). 슬랙·팀즈·카카오워크 채널(방) 단위 공용 발송 대상이다(D2).
// 주소(와 카카오워크의 앱 키·대화방 ID)는 봉인한 값 하나로만 저장하고 화면·응답·감사·기록 어디에도 평문이 없다(D5).

// Sealer 는 비밀값 봉인기다(acctsecret.Cipher 가 만족한다). 용도(purpose)별 AAD 로 계좌번호 칸과 섞이지 않는다.
type Sealer interface {
	SealFor(purpose, plain string) (string, error)
	OpenFor(purpose, stored string) (string, error)
}

const sealPurpose = "notify-target"

// TargetTypeName 은 대상 종류의 한국어 이름이다.
func TargetTypeName(t string) string { return ChannelName(t) }

func validTargetType(t string) bool {
	return t == "slack" || t == "teams" || t == "kakaowork" || t == "termspace"
}

// TargetView 는 화면에 보이는 웹훅 대상이다. 주소·앱 키는 없다. Hint 는 호스트뿐이다.
type TargetView struct {
	ID           int64    `json:"id"`
	Name         string   `json:"name"`
	Type         string   `json:"type"`
	TypeName     string   `json:"typeName"`
	Hint         string   `json:"hint"`
	Kinds        []string `json:"kinds"`
	KindNames    []string `json:"kindNames"`
	Active       bool     `json:"active"`
	AutoPaused   bool     `json:"autoPaused"`
	State        string   `json:"state"` // 사용 중 | 사용 중지 | 자동 중지
	Failures     int      `json:"failures"`
	LastOKAt     string   `json:"lastOkAt,omitempty"`
	LastError    string   `json:"lastError,omitempty"` // 한국어 사유 문장
	LastErrorAt  string   `json:"lastErrorAt,omitempty"`
	CreatedAt    string   `json:"createdAt"`
	NeedsToken   bool     `json:"needsToken"` // 카카오워크: 앱 키·대화방 ID 가 필요한 종류
	PausedReason string   `json:"-"`
}

// KindView 는 웹훅 대상이 받을 알림 종류 선택지다.
type KindView struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
}

// TargetList 는 관리 화면이 한 번에 읽는 묶음이다.
type TargetList struct {
	Targets      []TargetView        `json:"targets"`
	Kinds        []KindView          `json:"kinds"`
	SealReady    bool                `json:"sealReady"` // 봉인 키(PL_ACCOUNT_SECRET_KEY)가 있어 등록할 수 있는지
	BaseURLSet   bool                `json:"baseUrlSet"`
	ExtraHosts   []string            `json:"extraHosts"`
	BuiltinHosts map[string][]string `json:"builtinHosts"`
}

// TargetInput 은 등록·수정 입력이다. 수정에서 URL·Token·Conversation 을 비우면 기존 값을 유지한다.
type TargetInput struct {
	Name         string   `json:"name"`
	Type         string   `json:"type"`
	URL          string   `json:"url"`
	Token        string   `json:"token"`
	Conversation string   `json:"conversation"`
	Kinds        []string `json:"kinds"`
	Active       *bool    `json:"active"`
}

type secretBlob struct {
	URL          string `json:"u"`
	Token        string `json:"t,omitempty"`
	Conversation string `json:"c,omitempty"`
}

func valErr(format string, a ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{contract.ErrValidation}, a...)...)
}

func stateErr(format string, a ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{contract.ErrInvalidState}, a...)...)
}

func (s *Service) extraHosts(ctx context.Context, q querier) []string {
	var v string
	if err := q.QueryRowContext(ctx, `SELECT value FROM app_settings WHERE key = 'notify.extra_hosts'`).Scan(&v); err != nil {
		return nil
	}
	var out []string
	for _, p := range strings.Split(v, ",") {
		if p = strings.ToLower(strings.TrimSpace(p)); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func (s *Service) allowLocal() bool { return s.o.AllowLocalWebhook }

// resolveCheck 는 호스트 이름을 지금 해석해 모든 주소가 연결 가능한 공인 주소인지 본다(등록·시험 때 빠른 거부용).
// 실제 연결 직전의 검사는 발송기의 Dialer.Control 이 한다(DNS 재해석 우회 방지).
func (s *Service) resolveCheck(ctx context.Context, host string) error {
	if s.o.Resolve != nil {
		return s.o.Resolve(ctx, host)
	}
	rctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	addrs, err := net.DefaultResolver.LookupNetIP(rctx, "ip", host)
	if err != nil || len(addrs) == 0 {
		return valErr("웹훅 주소의 호스트를 찾을 수 없습니다(%s).", host)
	}
	for _, a := range addrs {
		if blockedIP(a) && !(s.allowLocal() && a.Unmap().IsLoopback()) {
			return valErr("웹훅 주소가 내부 주소로 연결됩니다. 내부 주소는 쓸 수 없습니다.")
		}
	}
	return nil
}

// hint 는 대상 주소의 호스트만 돌려준다(경로·쿼리·대화방 식별자는 어떤 부분도 싣지 않는다: termspace 처럼 주소 끝이 토큰인 수신처가 있다).
func hint(typ, rawURL, conv string) string {
	host := rawURL
	if i := strings.Index(rawURL, "://"); i >= 0 {
		host = rawURL[i+3:]
	}
	if i := strings.IndexAny(host, "/?#"); i >= 0 {
		host = host[:i]
	}
	if i := strings.LastIndex(host, "@"); i >= 0 {
		host = host[i+1:]
	}
	return host
}

// hostOnly 는 저장된 hint 를 표시할 때 호스트만 남긴다(이전에 저장된 "호스트…끝6자" 의 끝 부분을 마이그레이션 없이 가린다).
func hostOnly(stored string) string {
	if i := strings.Index(stored, "…"); i >= 0 {
		return stored[:i]
	}
	return stored
}

func parseKinds(js string) []string {
	var ks []string
	_ = json.Unmarshal([]byte(js), &ks)
	return ks
}

func (s *Service) view(typ, name, hintText, kindsJSON string, active int, paused string, fails int, lastOK, lastErr, lastErrAt, created string, id int64) TargetView {
	ks := parseKinds(kindsJSON)
	names := make([]string, 0, len(ks))
	for _, k := range ks {
		names = append(names, KindName(k))
	}
	v := TargetView{ID: id, Name: name, Type: typ, TypeName: TargetTypeName(typ), Hint: hostOnly(hintText), Kinds: ks, KindNames: names, Active: active == 1,
		AutoPaused: paused != "", Failures: fails, LastOKAt: lastOK, LastError: ReasonText(lastErr), LastErrorAt: lastErrAt, CreatedAt: created, NeedsToken: typ == "kakaowork"}
	if ks == nil {
		v.Kinds, v.KindNames = []string{}, []string{}
	}
	switch {
	case active != 1:
		v.State = "사용 중지"
	case paused != "":
		v.State = "자동 중지"
	default:
		v.State = "사용 중"
	}
	return v
}

// Targets 는 웹훅 대상 목록과 선택지다(notification:manage).
func (s *Service) Targets(ctx context.Context) (TargetList, error) {
	if _, err := contract.Authorize(ctx, contract.PermNotificationManage); err != nil {
		return TargetList{}, err
	}
	out := TargetList{Targets: []TargetView{}, SealReady: s.o.Sealer != nil, BaseURLSet: strings.TrimSpace(s.o.BaseURL) != "", BuiltinHosts: BuiltinHosts()}
	for _, k := range Kinds {
		out.Kinds = append(out.Kinds, KindView{Kind: k.Kind, Name: k.Name})
	}
	out.ExtraHosts = s.extraHosts(ctx, s.o.DB)
	if out.ExtraHosts == nil {
		out.ExtraHosts = []string{}
	}
	rows, err := s.o.DB.QueryContext(ctx, `SELECT id, name, type, hint, kinds_json, active, paused_reason, consecutive_failures, last_ok_at, last_error_code, last_error_at, created_at FROM notification_targets ORDER BY id`)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var name, typ, h, kj, paused, lastOK, lastErr, lastErrAt, created string
		var active, fails int
		if err := rows.Scan(&id, &name, &typ, &h, &kj, &active, &paused, &fails, &lastOK, &lastErr, &lastErrAt, &created); err != nil {
			return out, err
		}
		out.Targets = append(out.Targets, s.view(typ, name, h, kj, active, paused, fails, lastOK, lastErr, lastErrAt, created, id))
	}
	return out, rows.Err()
}

func normalizeKinds(in []string) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	for _, k := range in {
		if _, ok := KindByCode(k); !ok {
			return nil, valErr("알 수 없는 알림 종류가 있습니다.")
		}
		if !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	sort.Strings(out)
	if out == nil {
		out = []string{}
	}
	return out, nil
}

func validName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || utf8.RuneCountInString(name) > 40 {
		return "", valErr("이름은 1자 이상 40자 이하로 입력하세요.")
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			return "", valErr("이름에 제어문자를 쓸 수 없습니다.")
		}
	}
	return name, nil
}

// buildSecret 은 입력에서 봉인할 값을 검증하고 만든다. 종류별 필수 항목과 주소 규칙(허용 호스트·내부 주소 차단)을 본다.
func (s *Service) buildSecret(ctx context.Context, typ string, in TargetInput, old *secretBlob) (secretBlob, error) {
	b := secretBlob{}
	if old != nil {
		b = *old
	}
	if strings.TrimSpace(in.URL) != "" || old == nil {
		u, err := ValidateWebhookURL(in.URL, typ, s.extraHosts(ctx, s.o.DB), s.allowLocal())
		if err != nil {
			return b, valErr("%s", err.Error())
		}
		if err := s.resolveCheck(ctx, u.Hostname()); err != nil {
			return b, err
		}
		b.URL = strings.TrimSpace(in.URL)
	}
	if typ == "kakaowork" {
		if t := strings.TrimSpace(in.Token); t != "" {
			if len(t) > 512 || strings.ContainsAny(t, "\r\n ") {
				return b, valErr("앱 키 형식이 올바르지 않습니다.")
			}
			b.Token = t
		}
		if c := strings.TrimSpace(in.Conversation); c != "" {
			if len(c) > 64 || strings.ContainsAny(c, "\r\n \"\\") {
				return b, valErr("대화방 ID 형식이 올바르지 않습니다.")
			}
			b.Conversation = c
		}
		if b.Token == "" || b.Conversation == "" {
			return b, valErr("카카오워크는 앱 키와 대화방 ID 가 필요합니다.")
		}
	} else if strings.TrimSpace(in.Token) != "" || strings.TrimSpace(in.Conversation) != "" {
		return b, valErr("앱 키·대화방 ID 는 카카오워크에만 씁니다.")
	}
	return b, nil
}

func (s *Service) seal(b secretBlob) (string, error) {
	raw, err := json.Marshal(b)
	if err != nil {
		return "", err
	}
	return s.o.Sealer.SealFor(sealPurpose, string(raw))
}

func (s *Service) openSecret(stored string) (secretBlob, error) {
	var b secretBlob
	if s.o.Sealer == nil {
		return b, &SendError{Code: CodeNoSealer}
	}
	raw, err := s.o.Sealer.OpenFor(sealPurpose, stored)
	if err != nil || json.Unmarshal([]byte(raw), &b) != nil || b.URL == "" {
		return secretBlob{}, &SendError{Code: CodeBadSecret}
	}
	return b, nil
}

func (s *Service) requireSealer() error {
	if s.o.Sealer == nil {
		return stateErr("웹훅 주소를 안전하게 저장할 키(PL_ACCOUNT_SECRET_KEY)가 서버에 없어 등록할 수 없습니다. 키를 설정한 뒤 다시 기동하세요.")
	}
	return nil
}

func kindsLabel(ks []string) string {
	names := make([]string, 0, len(ks))
	for _, k := range ks {
		names = append(names, KindName(k))
	}
	if len(names) == 0 {
		return "(없음)"
	}
	return strings.Join(names, ", ")
}

// CreateTarget 은 웹훅 대상을 등록한다(notification:manage). 주소는 즉시 봉인하며 감사에는 주소가 없다.
func (s *Service) CreateTarget(ctx context.Context, in TargetInput) (TargetView, error) {
	p, err := contract.Authorize(ctx, contract.PermNotificationManage)
	if err != nil {
		return TargetView{}, err
	}
	if err := s.requireSealer(); err != nil {
		return TargetView{}, err
	}
	name, err := validName(in.Name)
	if err != nil {
		return TargetView{}, err
	}
	if !validTargetType(in.Type) {
		return TargetView{}, valErr("대상 종류는 슬랙·팀즈·카카오워크 중에서 고르세요.")
	}
	ks, err := normalizeKinds(in.Kinds)
	if err != nil {
		return TargetView{}, err
	}
	blob, err := s.buildSecret(ctx, in.Type, in, nil)
	if err != nil {
		return TargetView{}, err
	}
	sealed, err := s.seal(blob)
	if err != nil {
		return TargetView{}, err
	}
	kj, _ := json.Marshal(ks)
	active := 1
	if in.Active != nil && !*in.Active {
		active = 0
	}
	now := ts(s.now())
	var id int64
	h := hint(in.Type, blob.URL, blob.Conversation)
	err = s.o.Txs.WithTx(ctx, func(tx contract.Tx) error {
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM notification_targets WHERE name = ?`, name).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return valErr("같은 이름의 웹훅 대상이 이미 있습니다.")
		}
		err := tx.QueryRowContext(ctx, `INSERT INTO notification_targets(name, type, secret_sealed, hint, kinds_json, active, created_by, created_at, updated_at) VALUES (?,?,?,?,?,?,?,?,?) RETURNING id`,
			name, in.Type, sealed, h, string(kj), active, p.UserID, now, now).Scan(&id)
		if err != nil {
			return err
		}
		return s.o.Audit.Record(ctx, tx, audit.Entry{Actor: p, Action: "notification.target_save", TargetType: audit.TargetNotification, TargetID: fmt.Sprintf("target-%d", id), Result: audit.ResultSuccess,
			Diff: []audit.DiffItem{{Key: "target_name", New: name}, {Key: "target_type", New: ChannelName(in.Type)}, {Key: "kinds", New: kindsLabel(ks)}, {Key: "active", New: boolWord(active == 1)}, {Key: "url_changed", New: "예"}}})
	})
	if err != nil {
		return TargetView{}, err
	}
	return s.view(in.Type, name, h, string(kj), active, "", 0, "", "", "", now, id), nil
}

func boolWord(b bool) string {
	if b {
		return "예"
	}
	return "아니오"
}

type targetRow struct {
	id                      int64
	name, typ, sealed, hint string
	kindsJSON               string
	active                  int
	paused                  string
	fails                   int
}

func (s *Service) loadTarget(ctx context.Context, q querier, id int64) (targetRow, error) {
	var t targetRow
	err := q.QueryRowContext(ctx, `SELECT id, name, type, secret_sealed, hint, kinds_json, active, paused_reason, consecutive_failures FROM notification_targets WHERE id = ?`, id).
		Scan(&t.id, &t.name, &t.typ, &t.sealed, &t.hint, &t.kindsJSON, &t.active, &t.paused, &t.fails)
	if errors.Is(err, sql.ErrNoRows) {
		return t, fmt.Errorf("%w: 웹훅 대상을 찾을 수 없습니다.", contract.ErrNotFound)
	}
	return t, err
}

// UpdateTarget 은 이름·받을 종류를 바꾸고, 새 주소(와 앱 키·대화방 ID)를 주면 바꾼다(notification:manage). 비운 비밀 항목은 기존 값을 유지한다.
func (s *Service) UpdateTarget(ctx context.Context, id int64, in TargetInput) (TargetView, error) {
	p, err := contract.Authorize(ctx, contract.PermNotificationManage)
	if err != nil {
		return TargetView{}, err
	}
	if err := s.requireSealer(); err != nil {
		return TargetView{}, err
	}
	t, err := s.loadTarget(ctx, s.o.DB, id)
	if err != nil {
		return TargetView{}, err
	}
	name, err := validName(in.Name)
	if err != nil {
		return TargetView{}, err
	}
	ks, err := normalizeKinds(in.Kinds)
	if err != nil {
		return TargetView{}, err
	}
	sealed, h, urlChanged := t.sealed, t.hint, false
	if strings.TrimSpace(in.URL) != "" || strings.TrimSpace(in.Token) != "" || strings.TrimSpace(in.Conversation) != "" {
		old, err := s.openSecret(t.sealed)
		if err != nil {
			old = secretBlob{} // 풀 수 없으면 새로 모두 받는다
			if strings.TrimSpace(in.URL) == "" {
				return TargetView{}, valErr("저장된 주소를 풀 수 없습니다. 주소를 다시 입력하세요.")
			}
		}
		blob, err := s.buildSecret(ctx, t.typ, in, &old)
		if err != nil {
			return TargetView{}, err
		}
		if sealed, err = s.seal(blob); err != nil {
			return TargetView{}, err
		}
		h, urlChanged = hint(t.typ, blob.URL, blob.Conversation), true
	}
	kj, _ := json.Marshal(ks)
	now := ts(s.now())
	var oldKinds = parseKinds(t.kindsJSON)
	err = s.o.Txs.WithTx(ctx, func(tx contract.Tx) error {
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM notification_targets WHERE name = ? AND id <> ?`, name, id).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return valErr("같은 이름의 웹훅 대상이 이미 있습니다.")
		}
		if _, err := tx.ExecContext(ctx, `UPDATE notification_targets SET name = ?, secret_sealed = ?, hint = ?, kinds_json = ?, updated_at = ? WHERE id = ?`, name, sealed, h, string(kj), now, id); err != nil {
			return err
		}
		diff := []audit.DiffItem{}
		if name != t.name {
			diff = append(diff, audit.DiffItem{Key: "target_name", Old: t.name, New: name})
		}
		if string(kj) != t.kindsJSON {
			diff = append(diff, audit.DiffItem{Key: "kinds", Old: kindsLabel(oldKinds), New: kindsLabel(ks)})
		}
		if urlChanged {
			diff = append(diff, audit.DiffItem{Key: "url_changed", New: "예"})
		}
		return s.o.Audit.Record(ctx, tx, audit.Entry{Actor: p, Action: "notification.target_save", TargetType: audit.TargetNotification, TargetID: fmt.Sprintf("target-%d", id), Result: audit.ResultSuccess, Diff: diff})
	})
	if err != nil {
		return TargetView{}, err
	}
	nt, err := s.loadView(ctx, id)
	return nt, err
}

func (s *Service) loadView(ctx context.Context, id int64) (TargetView, error) {
	var name, typ, h, kj, paused, lastOK, lastErr, lastErrAt, created string
	var active, fails int
	err := s.o.DB.QueryRowContext(ctx, `SELECT name, type, hint, kinds_json, active, paused_reason, consecutive_failures, last_ok_at, last_error_code, last_error_at, created_at FROM notification_targets WHERE id = ?`, id).
		Scan(&name, &typ, &h, &kj, &active, &paused, &fails, &lastOK, &lastErr, &lastErrAt, &created)
	if err != nil {
		return TargetView{}, err
	}
	return s.view(typ, name, h, kj, active, paused, fails, lastOK, lastErr, lastErrAt, created, id), nil
}

// SetTargetActive 는 대상을 사용 중지하거나 다시 켠다(notification:manage). 다시 켜면 자동 중지 상태와 연속 실패 횟수도 풀린다.
func (s *Service) SetTargetActive(ctx context.Context, id int64, active bool) (TargetView, error) {
	p, err := contract.Authorize(ctx, contract.PermNotificationManage)
	if err != nil {
		return TargetView{}, err
	}
	t, err := s.loadTarget(ctx, s.o.DB, id)
	if err != nil {
		return TargetView{}, err
	}
	now := ts(s.now())
	action, was := "notification.target_pause", t.active == 1 && t.paused == ""
	if active {
		action = "notification.target_resume"
	}
	err = s.o.Txs.WithTx(ctx, func(tx contract.Tx) error {
		if active {
			if _, err := tx.ExecContext(ctx, `UPDATE notification_targets SET active = 1, paused_reason = '', consecutive_failures = 0, updated_at = ? WHERE id = ?`, now, id); err != nil {
				return err
			}
		} else if _, err := tx.ExecContext(ctx, `UPDATE notification_targets SET active = 0, updated_at = ? WHERE id = ?`, now, id); err != nil {
			return err
		}
		return s.o.Audit.Record(ctx, tx, audit.Entry{Actor: p, Action: action, TargetType: audit.TargetNotification, TargetID: fmt.Sprintf("target-%d", id), Result: audit.ResultSuccess,
			Diff: []audit.DiffItem{{Key: "target_name", New: t.name}, {Key: "active", Old: boolWord(was), New: boolWord(active)}}})
	})
	if err != nil {
		return TargetView{}, err
	}
	return s.loadView(ctx, id)
}

// TestTarget 은 고정 문구 시험 메시지를 바로 보낸다(notification:manage). 결과는 발송 기록(종류 "연결 시험")과 감사에 코드로만 남는다.
// 성공하면 자동 중지 상태가 풀린다. 실패해도 연속 실패 횟수(자동 중지 판정)에는 더하지 않는다.
func (s *Service) TestTarget(ctx context.Context, id int64) (TargetView, error) {
	p, err := contract.Authorize(ctx, contract.PermNotificationManage)
	if err != nil {
		return TargetView{}, err
	}
	t, err := s.loadTarget(ctx, s.o.DB, id)
	if err != nil {
		return TargetView{}, err
	}
	var sendErr error
	if sec, err := s.openSecret(t.sealed); err != nil {
		sendErr = err
	} else if sendErr = s.checkTargetURL(ctx, t.typ, sec); sendErr == nil {
		sctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		sendErr = s.sender().Send(sctx, WebhookTarget{Type: t.typ, URL: sec.URL, Token: sec.Token, Conversation: sec.Conversation}, TestText)
		cancel()
	}
	code, res, status := "ok", audit.ResultSuccess, StatusSent
	if sendErr != nil {
		code, res, status = sendCode(sendErr), audit.ResultError, StatusFailed
	}
	now := ts(s.now())
	rerr := s.o.Txs.WithTx(ctx, func(tx contract.Tx) error {
		lastErr := ""
		if code != "ok" {
			lastErr = code
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO notification_deliveries(target_id, channel, kind, status, attempts, next_try_at, last_error_code, created_at, sent_at) VALUES (?,?,?,?,1,?,?,?,?)`,
			id, t.typ, KindTest, status, now, lastErr, now, sentAt(status, now)); err != nil {
			return err
		}
		if sendErr == nil {
			if _, err := tx.ExecContext(ctx, `UPDATE notification_targets SET paused_reason = '', consecutive_failures = 0, last_ok_at = ?, last_error_code = '', updated_at = ? WHERE id = ?`, now, now, id); err != nil {
				return err
			}
		} else if _, err := tx.ExecContext(ctx, `UPDATE notification_targets SET last_error_code = ?, last_error_at = ?, updated_at = ? WHERE id = ?`, code, now, now, id); err != nil {
			return err
		}
		diff := []audit.DiffItem{{Key: "target_name", New: t.name}, {Key: "result_code", New: code}}
		if sendErr == nil && t.paused != "" {
			diff = append(diff, audit.DiffItem{Key: "active", Old: "자동 중지", New: "사용 중"})
		}
		return s.o.Audit.Record(ctx, tx, audit.Entry{Actor: p, Action: "notification.target_test", TargetType: audit.TargetNotification, TargetID: fmt.Sprintf("target-%d", id), Result: res, Diff: diff})
	})
	if rerr != nil {
		return TargetView{}, rerr
	}
	if sendErr != nil {
		return TargetView{}, stateErr("시험 메시지를 보내지 못했습니다. %s", ReasonText(code))
	}
	return s.loadView(ctx, id)
}

func sentAt(status, now string) string {
	if status == StatusSent {
		return now
	}
	return ""
}

// sendCode 는 발송 오류에서 사유 코드를 뽑는다.
func sendCode(err error) string {
	var se *SendError
	if errors.As(err, &se) {
		return se.Code
	}
	return CodeHookNetwork
}

func (s *Service) sender() WebhookSender {
	if s.o.Webhook != nil {
		return s.o.Webhook
	}
	return s.defaultSender
}

// checkTargetURL 은 발송 직전 주소 규칙을 다시 본다(허용 호스트가 빠졌거나 설정이 바뀐 경우). 어기면 SendError(차단)이다.
func (s *Service) checkTargetURL(ctx context.Context, typ string, sec secretBlob) error {
	u, err := ValidateWebhookURL(sec.URL, typ, s.extraHosts(ctx, s.o.DB), s.allowLocal())
	if err != nil {
		return &SendError{Code: CodeHookBlocked}
	}
	if ip, perr := netip.ParseAddr(u.Hostname()); perr == nil && blockedIP(ip) && !(s.allowLocal() && ip.Unmap().IsLoopback()) {
		return &SendError{Code: CodeHookBlocked}
	}
	return nil
}

// SetExtraHosts 는 관리자가 더하는 허용 호스트 목록을 정한다(notification:manage). 사설 메신저 등을 위한 것이며 항목은 호스트 이름이나 *.도메인 이다(IP 불가).
func (s *Service) SetExtraHosts(ctx context.Context, hosts []string) ([]string, error) {
	p, err := contract.Authorize(ctx, contract.PermNotificationManage)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var norm []string
	for _, h := range hosts {
		h = strings.ToLower(strings.TrimSpace(h))
		if h == "" {
			continue
		}
		if !ValidHostPattern(h) {
			return nil, valErr("허용 호스트 형식이 올바르지 않습니다(예: hooks.example.com 또는 *.example.com): %s", h)
		}
		if !seen[h] {
			seen[h] = true
			norm = append(norm, h)
		}
	}
	if len(norm) > 50 {
		return nil, valErr("허용 호스트는 50개까지 추가할 수 있습니다.")
	}
	sort.Strings(norm)
	old := s.extraHosts(ctx, s.o.DB)
	val := strings.Join(norm, ",")
	err = s.o.Txs.WithTx(ctx, func(tx contract.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO app_settings(key, value) VALUES ('notify.extra_hosts', ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, val); err != nil {
			return err
		}
		return s.o.Audit.Record(ctx, tx, audit.Entry{Actor: p, Action: "notification.hosts_set", TargetType: audit.TargetNotification, TargetID: "hosts", Result: audit.ResultSuccess,
			Diff: []audit.DiffItem{{Key: "hosts", Old: hostsLabel(old), New: hostsLabel(norm)}}})
	})
	if err != nil {
		return nil, err
	}
	if norm == nil {
		norm = []string{}
	}
	return norm, nil
}

func hostsLabel(h []string) string {
	if len(h) == 0 {
		return "(없음)"
	}
	return strings.Join(h, ", ")
}
