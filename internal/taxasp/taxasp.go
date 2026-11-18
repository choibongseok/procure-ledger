// Package taxasp 는 국세청 연계 대행사(ASP) 인터페이스와 전송 작업이다(B43, 8절).
//
// 이 저장소에는 인터페이스·결정적 모의 구현(taxasp/mock)·적합성 시험 키트(taxasp/taxasptest)·전송 작업만 있다. 실제 대행사 계약·인증서·규격 어댑터는 도입 회사의 몫이고,
// 회사가 만든 어댑터는 같은 키트를 자기 저장소에서 돌려 최소 공통 계약을 확인한다(D2).
//
// 원칙
//   - 요청·응답 형식은 우리 도메인 형(Invoice·Party·Line, 금액 정수 원, 날짜 YYYY-MM-DD)이다. 대행사 규격과의 변환은 어댑터가 한다.
//   - 오류는 분류된 코드를 가진 *Error 로만 돌려준다. 원문 응답 본문·주소·키를 오류 문장에 넣지 않는다.
//   - 복호화한 설정(Config)은 프로세스 메모리 안에서만 어댑터에 넘기고 로그·오류·감사·기록에 싣지 않는다.
//   - 대행사 호출은 netsafe(연결 직전 IP 검사, 리다이렉트 미추종, 고정 호스트)로만 나가야 한다. 어댑터가 자기 HTTP 클라이언트를 만들 때 netsafe.NewClient 를 쓴다.
package taxasp

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"

	"github.com/choibongseok/procure-ledger/internal/contract"
)

// Capabilities 는 어댑터가 지원하는 일이다.
type Capabilities struct {
	ServerSigning bool // 서버가 인증서로 서명한 XML 을 전달한다(false 면 대행사가 서명을 대행한다, D3)
	ListReceived  bool // 수신 목록 조회를 지원한다
	StatusPoll    bool // 접수 결과 조회를 지원한다
	AmendIssue    bool // 수정세금계산서 발행을 지원한다
}

// Config 는 복호화한 대행사 설정이다. 메모리 안에서만 쓰고 어디에도 기록하지 않는다.
type Config struct {
	Provider string
	Endpoint string // https 주소(어댑터가 netsafe 로 검사한다)
	APIKey   string
	CertPEM  string
	KeyPEM   string
	Password string
	// MockMode 는 모의 구현의 시나리오다(none·transient·permanent·delayed·conflict). 실제 어댑터는 무시한다.
	MockMode string
	// MockSeed 가 "demo" 면 모의 수신 목록에 시연용 가상 수신 건(MockBuyer 가 공급받는자)을 한 번 채운다(시연·로컬 확인용, 실제 어댑터는 무시한다).
	MockSeed  string
	MockBuyer string
}

// Party 는 공급자·공급받는자 한 쪽이다.
type Party struct {
	BizNo    string // 사업자번호(숫자 10자리)
	BranchNo string
	Name     string
	Rep      string
	Addr     string
	Email    string
}

// Line 은 품목 줄이다.
type Line struct {
	Day       string // MM-DD
	Name      string
	Spec      string
	Qty       int64
	UnitPrice int64
	Supply    int64
	Tax       int64
	Remark    string
}

// Invoice 는 대행사에 보내는(또는 받는) 세금계산서 내용이다.
type Invoice struct {
	No          string // 우리 내부 번호(TI-000001). 멱등 키로도 쓴다
	TaxType     string // taxable | zero | exempt
	IssueDate   string // YYYY-MM-DD
	Supplier    Party
	Buyer       Party
	Lines       []Line
	SupplyTotal int64
	TaxTotal    int64
	Total       int64
	ChargeType  string // receipt | claim
	Remark      string
	// 수정세금계산서
	AmendReason       string
	AmendOfApprovalNo string
}

// IssueRequest 는 발행 요청이다. IdempotencyKey 가 같으면 같은 결과를 돌려줘야 한다.
type IssueRequest struct {
	IdempotencyKey string
	Invoice        Invoice
}

// IssueResult 는 발행 요청의 접수 결과다. Ref 는 이후 상태 조회에 쓰는 대행사 참조(비밀이 아니다).
type IssueResult struct {
	Ref        string
	HTTPStatus int
}

// StatusKind 는 접수 결과의 종류다.
type StatusKind string

const (
	StatusPending  StatusKind = "pending"  // 아직 국세청 수신 전
	StatusAccepted StatusKind = "accepted" // 국세청 수신(승인번호)
	StatusRejected StatusKind = "rejected" // 대행사·국세청 반려
)

// StatusResult 는 접수 결과 조회의 답이다.
type StatusResult struct {
	Kind       StatusKind
	ApprovalNo string // accepted 일 때
	Code       string // rejected 일 때 우리 코드 표의 사유(모르면 provider_rejected)
	HTTPStatus int
}

// ListQuery 는 수신 목록 조회 조건이다. 기간은 작성일(YYYY-MM-DD, 양 끝 포함), Cursor 는 이전 쪽이 준 불투명 값이다.
type ListQuery struct {
	From, To string
	Cursor   string
	Limit    int
}

// Received 는 수신한 세금계산서 한 건이다.
type Received struct {
	ApprovalNo string
	Invoice    Invoice
}

// ReceivedPage 는 수신 목록 한 쪽이다.
type ReceivedPage struct {
	Items      []Received
	NextCursor string // 더 없으면 빈 값
	HasMore    bool
}

// Error 는 분류된 어댑터 오류다. Code 는 domain.TaxCodeLabels 의 코드(network·timeout·unavailable·auth·cert_expired·rejected_validation·duplicate·quota)이고
// 문장에는 값이 없다.
type Error struct {
	Code      string
	Retryable bool
	HTTP      int
}

func (e *Error) Error() string { return "대행사 오류: " + e.Code }

// Err 는 *Error 를 만든다. 일시 오류(network·timeout·unavailable·quota)는 재시도 가능으로 둔다.
func Err(code string, httpStatus int) *Error {
	switch code {
	case "network", "timeout", "unavailable", "quota":
		return &Error{Code: code, Retryable: true, HTTP: httpStatus}
	}
	return &Error{Code: code, HTTP: httpStatus}
}

// AsError 는 오류에서 *Error 를 꺼낸다. 분류되지 않은 오류는 재시도 가능한 network 로 본다(컨텍스트 취소·시간 초과는 timeout).
func AsError(err error) *Error {
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return Err("timeout", 0)
	}
	return Err("network", 0)
}

// Provider 는 대행사 한 곳과의 대화다. 구현은 호출마다 Configure 로 받은 설정으로 연결한다.
type Provider interface {
	Name() string
	Capabilities() Capabilities
	Configure(ctx context.Context, c Config) error
	Issue(ctx context.Context, req IssueRequest) (IssueResult, error)
	Status(ctx context.Context, ref string) (StatusResult, error)
	ListReceived(ctx context.Context, q ListQuery) (ReceivedPage, error)
}

// SettingsSource 는 쓸 수 있는 대행사 설정(복호화됨)을 준다. 구현은 대행사 설정 저장소(tax_asp_settings, platform)다. 켜져 있지 않거나 설정이 없으면 ok=false.
type SettingsSource interface {
	Active(ctx context.Context) (cfg Config, ok bool, err error)
}

// Registry 는 대행사 이름 → 어댑터 공장이다. 조립(bootstrap)이 쓸 어댑터를 등록한다.
type Registry struct {
	mu sync.Mutex
	f  map[string]func() Provider
}

// NewRegistry 는 빈 등록부를 만든다.
func NewRegistry() *Registry { return &Registry{f: map[string]func() Provider{}} }

// Register 는 이름으로 공장을 등록한다.
func (r *Registry) Register(name string, factory func() Provider) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.f[name] = factory
}

// Names 는 등록된 대행사 이름을 정렬해 돌려준다(대행사 설정 화면의 선택 목록).
func (r *Registry) Names() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, 0, len(r.f))
	for n := range r.f {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Has 는 이름의 어댑터가 등록돼 있는지 본다.
func (r *Registry) Has(name string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.f[name] != nil
}

// Resolve 는 설정에 맞는 어댑터를 만들어 설정을 건넨다. 설정이 없거나 꺼져 있으면 ErrNotConfigured 다.
func (r *Registry) Resolve(ctx context.Context, src SettingsSource) (Provider, error) {
	cfg, ok, err := src.Active(ctx)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrNotConfigured
	}
	r.mu.Lock()
	factory := r.f[cfg.Provider]
	r.mu.Unlock()
	if factory == nil {
		return nil, fmt.Errorf("%w: 알 수 없는 대행사 어댑터입니다", ErrNotConfigured)
	}
	p := factory()
	if err := p.Configure(ctx, cfg); err != nil {
		return nil, err
	}
	return p, nil
}

// ErrNotConfigured 는 대행사 설정이 없거나 꺼져 있다는 뜻이다.
var ErrNotConfigured = errors.New("대행사 연결이 설정되지 않았거나 꺼져 있습니다")

// Ready 는 서비스의 발행 전 검사(domain 쪽 ASPReady)로 쓰는 함수를 만든다. 오류 문장은 업무 용어 한국어다.
func Ready(src SettingsSource, reg *Registry) func(ctx context.Context) error {
	return func(ctx context.Context) error {
		cfg, ok, err := src.Active(ctx)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("%w: 대행사 연결이 설정되지 않았거나 꺼져 있어 발행할 수 없습니다. 관리자에게 전자세금계산서 대행사 설정을 요청하세요", contract.ErrInvalidState)
		}
		reg.mu.Lock()
		_, known := reg.f[cfg.Provider]
		reg.mu.Unlock()
		if !known {
			return fmt.Errorf("%w: 설정된 대행사 어댑터를 쓸 수 없습니다. 관리자에게 알려 주세요", contract.ErrInvalidState)
		}
		return nil
	}
}
