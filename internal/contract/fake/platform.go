package fake

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
	"sync"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/llm"
)

// Authenticator 는 토큰 문자열 → Principal 맵으로 contract.Authenticator 를 흉내 낸다.
type Authenticator struct {
	mu     sync.Mutex
	tokens map[string]contract.Principal
}

// NewAuthenticator 는 빈 인증기를 만든다.
func NewAuthenticator() *Authenticator {
	return &Authenticator{tokens: map[string]contract.Principal{}}
}

// Add 는 토큰을 등록한다. Channel 이 비어 있으면 mcp 로 둔다.
func (a *Authenticator) Add(token string, p contract.Principal) {
	if p.Channel == "" {
		p.Channel = contract.ChannelMCP
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.tokens[token] = p
}

// Revoke 는 토큰을 폐기한다.
func (a *Authenticator) Revoke(token string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.tokens, token)
}

// AuthenticateToken 은 contract.Authenticator 를 구현한다. 모르는 토큰은 ErrUnauthenticated.
func (a *Authenticator) AuthenticateToken(_ context.Context, raw string) (contract.Principal, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	p, ok := a.tokens[raw]
	if !ok {
		return contract.Principal{}, contract.ErrUnauthenticated
	}
	return p, nil
}

// Masker 는 contract.Masker 의 단순 구현이다.
type Masker struct{}

var (
	reBizNo   = regexp.MustCompile(`\b\d{3}-\d{2}-\d{5}\b`)
	rePhone   = regexp.MustCompile(`\b0\d{1,2}-\d{3,4}-\d{4}\b`)
	reAccount = regexp.MustCompile(`\b\d{3,6}-\d{2,6}-\d{4,8}\b`)
)

// MaskField 는 mask:bypass 권한이 없거나 MCP 채널이면(B41 D6) 앞 2자만 남기고 가린다.
func (Masker) MaskField(p contract.Principal, _ contract.FieldKind, value string) string {
	if contract.RawFieldAllowed(p) || value == "" {
		return value
	}
	r := []rune(value)
	if len(r) <= 2 {
		return strings.Repeat("*", len(r))
	}
	return string(r[:2]) + strings.Repeat("*", len(r)-2)
}

// MaskText 는 사업자번호·전화·계좌 패턴을 [MASKED] 로 바꾼다.
func (Masker) MaskText(text string) string {
	text = reBizNo.ReplaceAllString(text, "[MASKED]")
	text = rePhone.ReplaceAllString(text, "[MASKED]")
	return reAccount.ReplaceAllString(text, "[MASKED]")
}

// MockLLM 은 llm.Client 의 결정적 가짜다. 같은 입력이면 같은 출력이 나온다(CI 용).
// Responder 가 있으면 그 결과를 쓰고, 없으면 입력 해시 기반 고정 문자열을 돌려준다.
// 실제 게이트웨이(ai llmgw)의 한도·마스킹은 흉내 내지 않는다. 필요하면 Err 로 오류를 주입한다.
type MockLLM struct {
	mu        sync.Mutex
	Responder func(req llm.Request) string
	Err       error
	calls     []llm.Request
}

// Complete 는 llm.Client 를 구현한다.
func (m *MockLLM) Complete(_ context.Context, req llm.Request) (llm.Response, error) {
	m.mu.Lock()
	m.calls = append(m.calls, req)
	err, responder := m.Err, m.Responder
	m.mu.Unlock()
	if err != nil {
		return llm.Response{}, err
	}
	var text string
	if responder != nil {
		text = responder(req)
	} else {
		text = "mock:" + digest(req)
	}
	model := req.Model
	if model == "" {
		model = "mock"
	}
	return llm.Response{
		Text:       text,
		Model:      model,
		Usage:      llm.Usage{InputTokens: approxTokens(req), OutputTokens: len(text)/4 + 1},
		StopReason: "end_turn",
	}, nil
}

// Calls 는 받은 요청 사본이다(마스킹 후 전송 여부 등 검사용).
func (m *MockLLM) Calls() []llm.Request {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]llm.Request(nil), m.calls...)
}

func digest(req llm.Request) string {
	h := sha256.New()
	h.Write([]byte(req.System))
	for _, msg := range req.Messages {
		h.Write([]byte("|" + msg.Role + ":" + msg.Content))
	}
	return hex.EncodeToString(h.Sum(nil))[:12]
}

func approxTokens(req llm.Request) int {
	n := len(req.System)
	for _, m := range req.Messages {
		n += len(m.Content)
	}
	return n/4 + 1
}
