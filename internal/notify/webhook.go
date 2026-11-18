package notify

// 웹훅 발송기(B36 단계 2, 명세 4.2·4.6·8절). 슬랙·MS 팀즈·카카오워크 채널(방)로 건수·종류·앱 링크만 담은 한 줄을 보낸다.
//
// 보안 원칙
//   - 주소는 종류별 허용 호스트(+관리자가 더한 호스트)만 받는다. https 만(로컬 시험용 예외는 AllowLocal).
//   - 연결 직전에 실제로 연결할 IP 를 검사한다(Dialer.Control): 이름이 등록 때는 공인 주소였다가 나중에 사설 주소로 바뀌는 DNS 재해석 우회를 막는다.
//     루프백·사설·링크로컬(클라우드 메타데이터 포함)·CGNAT·멀티캐스트·미지정 주소는 거부한다.
//   - 리다이렉트를 따르지 않고, 환경 프록시를 쓰지 않고(프록시가 검사를 우회하지 못하게), 응답 본문은 4KB 까지만 읽는다.
//   - 오류 문구와 기록에는 주소·응답 본문을 넣지 않는다. 사유 코드만 남긴다.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/choibongseok/procure-ledger/internal/netsafe"
)

// WebhookTarget 은 발송기에 넘기는 풀린 대상 정보다. 로그·오류에 넣지 않는다.
type WebhookTarget struct {
	Type         string // slack | teams | kakaowork | termspace
	URL          string
	Token        string // 카카오워크 앱 키(Authorization: Bearer)
	Conversation string // 카카오워크 대화방 ID
}

// WebhookSender 는 채널(방)로 한 줄을 보내는 수단이다. 시험은 가짜나 모의 서버에 붙인 실제 구현을 쓴다.
type WebhookSender interface {
	Send(ctx context.Context, t WebhookTarget, text string) error
}

// SendError 는 발송 실패를 사유 코드와 재시도 여부로 나눈 오류다. Error() 는 한국어 사유 문장만 돌려준다(주소·응답 본문 없음).
type SendError struct {
	Code  string
	Retry bool
	After time.Duration // 429 의 Retry-After(상한 1시간은 호출자가 적용)
}

func (e *SendError) Error() string { return ReasonText(e.Code) }

// 웹훅 사유 코드.
const (
	CodeHookNetwork   = "hook_network"
	CodeHook5xx       = "hook_5xx"
	CodeHookRate      = "hook_rate"
	CodeHookNoService = "hook_no_service"
	CodeHookAuth      = "hook_auth"
	CodeHookPayload   = "hook_payload"
	CodeHook4xx       = "hook_4xx"
	CodeHookRejected  = "hook_rejected"
	CodeHookBlocked   = "hook_blocked"
	CodeTargetPaused  = "target_paused"
	CodeNoSealer      = "no_sealer"
	CodeBadSecret     = "bad_secret"
)

func init() {
	for k, v := range map[string]string{
		CodeHookNetwork:   "채널 서버에 연결하지 못했습니다.",
		CodeHook5xx:       "채널 서버가 일시적으로 받지 못했습니다.",
		CodeHookRate:      "채널 서버가 요청이 많다고 거절했습니다.",
		CodeHookNoService: "웹훅이 없어졌거나 꺼져 있습니다. 주소를 다시 등록하세요.",
		CodeHookAuth:      "채널이 요청을 거절했습니다(권한 또는 앱 키를 확인하세요).",
		CodeHookPayload:   "채널이 메시지 형식을 받아들이지 않았습니다.",
		CodeHook4xx:       "채널이 요청을 거절했습니다.",
		CodeHookRejected:  "채널이 메시지를 받지 않았다고 응답했습니다.",
		CodeHookBlocked:   "허용되지 않은 주소라 보내지 않았습니다(내부 주소·허용 목록 밖).",
		CodeTargetPaused:  "웹훅 대상이 중지되어 있습니다.",
		CodeNoSealer:      "웹훅 주소를 풀 수 있는 키(PL_ACCOUNT_SECRET_KEY)가 없습니다.",
		CodeBadSecret:     "저장된 웹훅 주소를 풀 수 없습니다(키가 바뀌었을 수 있습니다). 주소를 다시 등록하세요.",
	} {
		reasonText[k] = v
	}
}

// ---- 허용 호스트 ----

// builtinHosts 는 종류별 기본 허용 호스트다. "*.도메인" 은 그 도메인의 하위 이름(한 단계 이상)을 뜻한다.
// 슬랙: hooks.slack.com(GovSlack 은 slack-gov.com). 팀즈: 워크플로 웹훅(logic.azure.com·powerplatform.com)과 기존 webhook.office.com.
// 카카오워크: 봇 API 주소 api.kakaowork.com. termspace: termspace.io 와 그 하위 이름(슬랙처럼 수신 웹훅).
var builtinHosts = map[string][]string{
	"slack":     {"hooks.slack.com", "hooks.slack-gov.com"},
	"teams":     {"*.webhook.office.com", "*.logic.azure.com", "*.powerplatform.com"},
	"kakaowork": {"api.kakaowork.com"},
	"termspace": {"termspace.io", "*.termspace.io"}, // 온프렘 호스트는 notify.extra_hosts 로 더한다
}

// BuiltinHosts 는 화면에 보일 종류별 기본 허용 호스트다.
func BuiltinHosts() map[string][]string {
	out := map[string][]string{}
	for k, v := range builtinHosts {
		out[k] = append([]string(nil), v...)
	}
	return out
}

func hostMatches(pattern, host string) bool { return netsafe.HostMatches(pattern, host) } // 판정은 internal/netsafe 와 같다(B42 D3)

// AllowedHost 는 host 가 종류 typ 의 기본 허용 호스트이거나 관리자가 더한 호스트(extra)인지다.
func AllowedHost(typ, host string, extra []string) bool {
	for _, p := range builtinHosts[typ] {
		if hostMatches(p, host) {
			return true
		}
	}
	for _, p := range extra {
		if hostMatches(strings.TrimSpace(p), host) {
			return true
		}
	}
	return false
}

// ValidHostPattern 은 관리자가 더하는 허용 호스트 항목의 형식(예: hooks.example.com, *.example.com)을 검사한다.
func ValidHostPattern(p string) bool { return netsafe.ValidHostPattern(p) }

var errBlockedAddr = netsafe.ErrBlockedAddr

// blockedIP 는 웹훅이 연결하면 안 되는 주소인지다(internal/netsafe 와 같은 판정).
func blockedIP(ip netip.Addr) bool { return netsafe.BlockedIP(ip) }

// ValidateWebhookURL 은 등록·발송 때 주소를 검사한다: https, 사용자 정보 없음, 호스트 이름(IP 아님), 기본 포트, 허용 호스트.
// allowLocal 이면 루프백 주소의 http 와 임의 포트를 허용하고 호스트 허용 목록을 건너뛴다(로컬 시험 전용: PL_NOTIFY_INSECURE_LOCAL=1).
func ValidateWebhookURL(raw, typ string, extra []string, allowLocal bool) (*url.URL, error) {
	u, err := netsafe.ValidateURL(raw, netsafe.URLOptions{AllowedHost: func(host string) bool { return AllowedHost(typ, host, extra) }, AllowLocal: allowLocal})
	if err != nil {
		return nil, err
	}
	local := false
	if allowLocal {
		if ip, perr := netip.ParseAddr(u.Hostname()); (perr == nil && ip.IsLoopback()) || strings.EqualFold(u.Hostname(), "localhost") {
			local = true
		}
	}
	if typ == "termspace" && !termspacePathOK(u) { // 경로 모양은 로컬 시험에서도 검사한다
		return nil, errors.New(termspacePathMsg)
	}
	if !local && typ == "kakaowork" && u.Path != "/v1/messages.send" {
		return nil, errors.New("카카오워크 주소는 https://api.kakaowork.com/v1/messages.send 여야 합니다.")
	}
	return u, nil
}

// termspace 수신 웹훅 주소의 경로는 /hooks/tsh_<43자 base64url>(토큰 = "tsh_" + 32바이트 base64url 무패딩)이다. 쿼리는 쓰지 않는다.
const termspacePathMsg = "termspace 웹훅 주소는 https://<호스트>/hooks/tsh_<토큰 43자> 모양이어야 합니다."

var termspacePathRe = regexp.MustCompile(`^/hooks/tsh_[A-Za-z0-9_-]{43}$`)

func termspacePathOK(u *url.URL) bool {
	return u.RawQuery == "" && termspacePathRe.MatchString(u.EscapedPath())
}

// termspaceMaxText 는 termspace 메시지 text 의 최대 글자 수(룬)다.
const termspaceMaxText = 4000

// termspaceText 는 text 를 최대 4000자로 줄인다. 넘으면 본문 쪽을 자르고 끝의 링크(마지막 http(s) 토큰)는 통째로 남긴다.
func termspaceText(text string) string {
	if utf8.RuneCountInString(text) <= termspaceMaxText {
		return text
	}
	link := ""
	body := text
	fields := strings.Fields(text)
	for i := len(fields) - 1; i >= 0; i-- {
		if strings.HasPrefix(fields[i], "http://") || strings.HasPrefix(fields[i], "https://") {
			link = fields[i]
			if idx := strings.LastIndex(text, link); idx >= 0 {
				body = strings.TrimRight(text[:idx], " \t\r\n")
			}
			break
		}
	}
	if link == "" || utf8.RuneCountInString(link) > termspaceMaxText/2 {
		r := []rune(text)
		return string(r[:termspaceMaxText-1]) + "…"
	}
	room := termspaceMaxText - utf8.RuneCountInString(link) - 2 // "…" 과 공백 한 칸
	r := []rune(body)
	if len(r) > room {
		r = r[:room]
	}
	return string(r) + "… " + link
}

// ---- HTTP 발송기 ----

// HTTPSenderOptions 는 HTTP 발송기 설정이다.
type HTTPSenderOptions struct {
	// AllowLocal 은 루프백 주소로의 연결을 허용한다(로컬 시험 전용).
	AllowLocal bool
	// Timeout 은 요청 전체 제한이다(기본 10초). 연결 제한은 5초다.
	Timeout time.Duration
}

type httpSender struct{ client *http.Client }

// NewHTTPSender 는 SSRF 방어가 들어간 HTTP 발송기를 만든다.
func NewHTTPSender(o HTTPSenderOptions) WebhookSender {
	return &httpSender{client: netsafe.NewClient(netsafe.ClientOptions{AllowLocal: o.AllowLocal, Timeout: o.Timeout})}
}

// slackEscape 는 슬랙 메시지 텍스트에서 특수문자를 이스케이프한다(& < >).
func slackEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}

// Payload 는 종류별 요청 본문(JSON)과 추가 헤더다. 형식은 각 서비스 공식 문서를 따른다.
//   - 슬랙 Incoming Webhook: {"text": "..."}  (https://docs.slack.dev/messaging/sending-messages-using-incoming-webhooks)
//   - 팀즈 Workflows 웹훅("Teams 웹훅 요청을 받을 때"): Adaptive Card 가 든 message (learn.microsoft.com/microsoftteams/platform/webhooks-and-connectors).
//     Office 365 커넥터 웹훅은 2026-05 종료되었다.
//   - 카카오워크: 웹훅 주소 방식이 아니라 봇 API POST https://api.kakaowork.com/v1/messages.send, Authorization: Bearer 앱 키,
//     본문 {"conversation_id", "text"}. 공식 개발자 문서에서 앱 키·대화방 ID 없이 주소 하나로 받는 수신 웹훅은 확인하지 못했다.
func Payload(t WebhookTarget, text string) ([]byte, http.Header, error) {
	h := http.Header{}
	h.Set("Content-Type", "application/json")
	var v any
	switch t.Type {
	case "slack":
		v = map[string]any{"text": slackEscape(text)}
	case "teams":
		v = map[string]any{
			"type": "message",
			"attachments": []any{map[string]any{
				"contentType": "application/vnd.microsoft.card.adaptive",
				"contentUrl":  nil,
				"content": map[string]any{
					"$schema": "http://adaptivecards.io/schemas/adaptive-card.json",
					"type":    "AdaptiveCard",
					"version": "1.2",
					"body":    []any{map[string]any{"type": "TextBlock", "text": text, "wrap": true}},
				},
			}},
		}
	case "termspace":
		// 일반 텍스트다: 슬랙식 & < > 이스케이프를 하지 않는다. 4000자를 넘으면 줄이되 링크는 남긴다.
		v = map[string]any{"text": termspaceText(text)}
	case "kakaowork":
		if t.Token == "" || t.Conversation == "" {
			return nil, nil, &SendError{Code: CodeBadSecret}
		}
		h.Set("Authorization", "Bearer "+t.Token)
		var conv any = t.Conversation
		if n, err := strconv.ParseInt(t.Conversation, 10, 64); err == nil {
			conv = n
		}
		v = map[string]any{"conversation_id": conv, "text": text}
	default:
		return nil, nil, &SendError{Code: CodeHookPayload}
	}
	b, err := json.Marshal(v)
	return b, h, err
}

func (s *httpSender) Send(ctx context.Context, t WebhookTarget, text string) error {
	body, hdr, err := Payload(t, text)
	if err != nil {
		var se *SendError
		if errors.As(err, &se) {
			return se
		}
		return &SendError{Code: CodeHookPayload}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.URL, bytes.NewReader(body))
	if err != nil {
		return &SendError{Code: CodeHookBlocked}
	}
	for k, vs := range hdr {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	req.Header.Set("User-Agent", "ProcureLedger-notify")
	resp, err := s.client.Do(req)
	if err != nil {
		if errors.Is(err, errBlockedAddr) {
			return &SendError{Code: CodeHookBlocked}
		}
		return &SendError{Code: CodeHookNetwork, Retry: true}
	}
	defer resp.Body.Close()
	b := netsafe.ReadLimited(resp.Body) // 응답 본문은 4KB 까지만, 기록·오류에 쓰지 않는다
	return classifyHTTP(t.Type, resp.StatusCode, b, resp.Header.Get("Retry-After"))
}

// classifyHTTP 는 응답을 사유 코드와 재시도 여부로 나눈다: 2xx 성공(카카오워크는 본문 success=false 면 실패), 429·5xx 재시도(Retry-After 존중),
// 400 형식 오류·401/403 권한·404/410 웹훅 없음·그 밖의 4xx 는 즉시 포기다.
func classifyHTTP(typ string, status int, body []byte, retryAfter string) error {
	switch {
	case status >= 200 && status < 300:
		if typ == "kakaowork" {
			var r struct {
				Success *bool `json:"success"`
			}
			if json.Unmarshal(body, &r) == nil && r.Success != nil && !*r.Success {
				return &SendError{Code: CodeHookRejected}
			}
		}
		return nil
	case status == http.StatusTooManyRequests:
		e := &SendError{Code: CodeHookRate, Retry: true}
		if n, err := strconv.Atoi(strings.TrimSpace(retryAfter)); err == nil && n > 0 {
			e.After = time.Duration(n) * time.Second
		}
		return e
	case status >= 500:
		return &SendError{Code: CodeHook5xx, Retry: true}
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return &SendError{Code: CodeHookAuth}
	case status == http.StatusNotFound || status == http.StatusGone:
		return &SendError{Code: CodeHookNoService}
	case status == http.StatusBadRequest:
		return &SendError{Code: CodeHookPayload}
	}
	return &SendError{Code: CodeHook4xx}
}
