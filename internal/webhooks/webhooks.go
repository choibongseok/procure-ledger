// Package webhooks 는 변경 이벤트 웹훅(B42 2단계, 9절)이다.(발송), platform(HTTP 관리 API·화면·권한 연결).
//
// 변경 피드(internal/changes)의 줄을 구독별 발송 건으로 바꾸고, 서명한 요청을 구독 주소로 보낸다. 요청 본문에는 값이 없다(자원·번호·동작·상태·API 주소뿐):
// 이벤트는 알림이고 값은 소비자가 API 로 읽는다.
//
// 보장(소비자 계약)
//   - 전달은 적어도 한 번이다. 같은 이벤트는 같은 X-PL-Event-Id(피드 번호)로 오므로 소비자가 중복을 걸러야 한다.
//   - 같은 구독 안에서 같은 객체(자원+번호)의 이벤트는 번호 순서로 보낸다: 앞 건이 대기·전송 중이면 뒤 건은 기다린다.
//     앞 건이 포기(failed)되면 뒤 건이 이어서 나간다(소비자는 api_url 로 읽은 최신 상태가 정본이다). 다른 객체는 서로 기다리지 않는다.
//   - 서명: X-PL-Signature: v1=<hex HMAC-SHA256(비밀, 시각 + "." + 본문)>. 비밀 교체 뒤 24시간은 옛 비밀의 서명도 함께 보낸다(v1=새,v1=옛).
//   - 주소 검사·연결 직전 IP 검사·리다이렉트 미추종·응답 4KB 상한은 알림 웹훅과 같은 부품(internal/netsafe)이다.
//
// 이 패키지는 서비스 API(구독 만들기·목록·중지·재개·비밀 교체·시험 보내기·발송 기록·다시 보내기)와 발송 작업(Run·Tick)까지다.
// HTTP 라우트·화면·권한 연결(webhook:manage)은 platform 이 이 서비스 위에 얹는다(Options.ManagePerm 이 비어 있으면 모든 관리 호출이 거부된다).
package webhooks

import (
	"context"
	"database/sql"
	"net/http"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
)

// Sealer 는 비밀값 봉인기다(acctsecret.Cipher 가 만족한다). 용도(purpose)별 AAD 로 계좌번호·알림 웹훅 주소 칸과 섞이지 않는다.
type Sealer interface {
	SealFor(purpose, plain string) (string, error)
	OpenFor(purpose, stored string) (string, error)
}

const sealPurpose = "webhook-sub"

// Options 는 Service 구성이다.
type Options struct {
	DB     *sql.DB
	Txs    contract.TxManager
	Audit  audit.Recorder
	Sealer Sealer
	// ManagePerm 은 관리 호출(구독 만들기·중지·재개·비밀 교체·시험·다시 보내기·기록 보기)에 요구하는 권한이다(platform 이 webhook:manage 로 정한다). 비면 모두 거부한다.
	ManagePerm contract.Permission
	// BaseURL 은 서버의 접속 주소(PL_BASE_URL)다. 요청 본문의 api_url 에 쓴다. 비면 보내지 않는다(no_base_url).
	BaseURL string
	// AllowLocal 은 루프백 주소(와 http)로의 발송을 허용한다(PL_NOTIFY_INSECURE_LOCAL=1, 로컬 시험 전용).
	AllowLocal bool
	// Client 는 HTTP 클라이언트다. 비면 internal/netsafe 의 안전한 클라이언트를 만든다.
	Client *http.Client
	// Resolve 는 등록 때 호스트 해석 검사를 바꾼다(시험용). 비면 netsafe.ResolveCheck.
	Resolve func(ctx context.Context, host string) error
	// Deliver 는 자동 중지 알림의 메일·웹훅 발송 건을 만든다(notify.Service.Enqueue). 비면 앱 안 알림만 만든다.
	Deliver func(ctx context.Context, tx contract.Tx, notificationID, userID int64, kind string, at time.Time) error
	Now     func() time.Time
	// Concurrency 는 서버 전체 동시 발송 상한이다(기본 8).
	Concurrency int
}

// 재시도·중지 규칙(명세 9.4, D8).
const (
	maxRetries       = 8 // 첫 시도 뒤 재시도 횟수. 마지막 재시도까지 실패하면 포기(retry_over)
	autoPauseAfter   = 5 // 연속 최종 실패 횟수
	reclaimAfter     = 15 * time.Minute
	rotateOverlap    = 24 * time.Hour
	retryAfterCap    = time.Hour
	perMinDefault    = 60
	maxEventsPerSub  = 60
	maxResends       = 5
	scanBatch        = 500
	scanBatchesLimit = 20
	dispatchBatch    = 100
)

// backoff 는 재시도 간격이다(1분, 5분, 30분, 2시간, 6시간, 12시간, 24시간, 이후 24시간).
var backoff = []time.Duration{time.Minute, 5 * time.Minute, 30 * time.Minute, 2 * time.Hour, 6 * time.Hour, 12 * time.Hour, 24 * time.Hour}

func backoffFor(retry int) time.Duration { // retry: 1부터
	if retry < 1 {
		retry = 1
	}
	if retry > len(backoff) {
		return backoff[len(backoff)-1]
	}
	return backoff[retry-1]
}

// 발송 건 상태.
const (
	StatusQueued  = "queued"
	StatusSending = "sending"
	StatusSent    = "sent"
	StatusFailed  = "failed"
	StatusSkipped = "skipped"
)

// deliveryTransitions 는 발송 건 상태 전이 표 한 곳이다(DB 트리거 trg_webhook_deliveries_status 와 같다). 표에 없는 전이는 거부한다.
var deliveryTransitions = map[string][]string{
	StatusQueued:  {StatusSending, StatusSkipped},
	StatusSending: {StatusSent, StatusQueued, StatusFailed},
	StatusFailed:  {StatusQueued},
}

// CanTransition 은 상태 전이가 표에 있는지다.
func CanTransition(from, to string) bool {
	for _, t := range deliveryTransitions[from] {
		if t == to {
			return true
		}
	}
	return false
}

// 사유 코드와 한국어 문장(응답 본문·주소는 어디에도 남기지 않는다).
const (
	CodeNetwork    = "hook_network"
	Code5xx        = "hook_5xx"
	CodeRate       = "hook_rate"
	CodeGone       = "hook_gone"
	CodeAuth       = "hook_auth"
	Code4xx        = "hook_4xx"
	CodeRedirect   = "hook_redirect"
	CodeBlocked    = "hook_blocked"
	CodeRetryOver  = "retry_over"
	CodeBadSecret  = "bad_secret"
	CodeNoSealer   = "no_sealer"
	CodeNoBaseURL  = "no_base_url"
	CodePayload    = "hook_payload"
	CodeFeedGap    = "feed_gap"
	CodeCoalesced  = "coalesced"
	codeAutoPaused = "auto_paused"
)

var reasonText = map[string]string{
	CodeNetwork:    "수신 서버에 연결하지 못했습니다.",
	Code5xx:        "수신 서버가 일시적으로 받지 못했습니다.",
	CodeRate:       "수신 서버가 요청이 많다고 거절했습니다.",
	CodeGone:       "수신 주소가 없어졌거나 폐기되었습니다. 구독 주소를 확인하세요.",
	CodeAuth:       "수신 서버가 요청을 거절했습니다(권한 또는 서명 검증을 확인하세요).",
	Code4xx:        "수신 서버가 요청을 받아들이지 않았습니다.",
	CodeRedirect:   "수신 서버가 다른 주소로 보내려 했습니다(이동은 따르지 않습니다).",
	CodeBlocked:    "허용되지 않은 주소라 보내지 않았습니다(내부 주소·허용 목록 밖).",
	CodeRetryOver:  "재시도 횟수를 넘어 포기했습니다.",
	CodeBadSecret:  "저장된 구독 정보를 풀 수 없습니다(키가 바뀌었을 수 있습니다). 구독을 다시 등록하세요.",
	CodeNoSealer:   "구독 정보를 풀 수 있는 키(PL_ACCOUNT_SECRET_KEY)가 없습니다.",
	CodeNoBaseURL:  "서버 접속 주소(PL_BASE_URL)가 설정되지 않아 보내지 않았습니다.",
	CodePayload:    "요청 본문을 만들지 못했습니다.",
	CodeCoalesced:  "같은 객체의 연이은 변경이라 마지막 한 건으로 합쳤습니다.",
	CodeFeedGap:    "변경 기록의 보존 기간이 지나 일부 이벤트가 빠졌습니다. API 로 다시 적재하세요.",
	codeAutoPaused: "연속 실패로 자동 중지되었습니다.",
}

// ReasonText 는 사유 코드의 한국어 문장이다(없는 코드는 빈 문자열).
func ReasonText(code string) string { return reasonText[code] }
