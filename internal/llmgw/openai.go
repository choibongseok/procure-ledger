package llmgw

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract/llm"
	"github.com/choibongseok/procure-ledger/internal/netsafe"
)

// maxProviderBody 는 공급자 응답 본문의 읽기 상한이다.
const maxProviderBody = 8 << 20

// OpenAICompatProvider 는 OpenAI 호환 채팅 API(사내 모델·vLLM 등) 공급자다(B45).
// 요청: POST {base_url}/v1/chat/completions(base_url 이 /v1 로 끝나면 /chat/completions 만 붙인다), 헤더 Authorization: Bearer <키>.
// 형식은 공개 문서 수준의 가정이다(운영 전 확인 필요, 명세 7절). 주소는 공개 https 만이고 연결 직전 IP 검사·리다이렉트 불추종이다(netsafe).
type OpenAICompatProvider struct {
	baseURL    string
	apiKey     string
	headers    map[string]string
	allowLocal bool
	hc         *http.Client
}

// OpenAICompatConfig 는 봉인된 설정에서 풀어낸 값이다.
type OpenAICompatConfig struct {
	BaseURL      string            `json:"base_url"`
	APIKey       string            `json:"api_key"`
	ExtraHeaders map[string]string `json:"extra_headers,omitempty"`
}

// NewOpenAICompat 은 주소를 검사하고 공급자를 만든다. allowLocal 은 로컬 시험(루프백 허용) 전용이다.
func NewOpenAICompat(c OpenAICompatConfig, allowLocal bool, timeout time.Duration) (*OpenAICompatProvider, error) {
	if _, err := validateProviderURL(c.BaseURL, allowLocal); err != nil {
		return nil, err
	}
	if strings.TrimSpace(c.APIKey) == "" {
		return nil, errors.New("llmgw: API 키가 비어 있습니다")
	}
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &OpenAICompatProvider{baseURL: strings.TrimRight(strings.TrimSpace(c.BaseURL), "/"), apiKey: c.APIKey, headers: c.ExtraHeaders, allowLocal: allowLocal,
		hc: netsafe.NewClient(netsafe.ClientOptions{AllowLocal: allowLocal, Timeout: timeout})}, nil
}

// validateProviderURL 은 공급자 주소의 공통 검사다: 공개 https 만(netsafe.ValidateURL), 내부 이름 거부.
func validateProviderURL(raw string, allowLocal bool) (*url.URL, error) {
	u, err := netsafe.ValidateURL(raw, netsafe.URLOptions{AllowLocal: allowLocal})
	if err != nil {
		return nil, errors.New(strings.ReplaceAll(err.Error(), "웹훅 주소", "공급자 주소"))
	}
	if h := strings.ToLower(u.Hostname()); !allowLocal && (h == "localhost" || strings.HasSuffix(h, ".localhost") || strings.HasSuffix(h, ".local") || strings.HasSuffix(h, ".internal")) {
		return nil, errors.New("공급자 주소에 내부 주소는 쓸 수 없습니다.")
	}
	// 십진·8진·16진·줄임 표기 IP(2130706433, 0177.0.0.1, 0x7f.1, 127.1)는 시스템 해석기가 IP 로 받아 주므로 이름이 아니라 IP 로 보고 거부한다(루프백 시험은 예외).
	if !allowLocal && numericHostRe.MatchString(u.Hostname()) {
		return nil, errors.New("공급자 주소는 IP 숫자 표기가 아니라 호스트 이름이어야 합니다.")
	}
	// 질의 문자열(?…)은 쓰지 않는다: 키를 주소에 실어 보내는 설정이 감사·화면·로그에 새는 길이 된다. 경로 접두(/v1 등)만 허용한다.
	if u.RawQuery != "" || u.ForceQuery {
		return nil, errors.New("공급자 주소에 질의 문자열(?…)은 쓸 수 없습니다. 경로까지만 입력하세요.")
	}
	return u, nil
}

var numericHostRe = regexp.MustCompile(`^(0[xX][0-9a-fA-F]+|[0-9]+)(\.(0[xX][0-9a-fA-F]+|[0-9]+))*$`)

// Name 은 Provider 를 구현한다.
func (p *OpenAICompatProvider) Name() string { return "openai_compat" }

type oaMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type oaRequest struct {
	Model     string      `json:"model"`
	Messages  []oaMessage `json:"messages"`
	MaxTokens int         `json:"max_tokens,omitempty"`
}

type oaResponse struct {
	Model   string `json:"model"`
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}

func (p *OpenAICompatProvider) endpoint() string {
	if strings.HasSuffix(p.baseURL, "/v1") {
		return p.baseURL + "/chat/completions"
	}
	return p.baseURL + "/v1/chat/completions"
}

// Complete 는 Provider 를 구현한다. 오류는 ProviderError(분류)이고 주소·본문을 문장에 넣지 않는다.
func (p *OpenAICompatProvider) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	if len(req.Images) > 0 {
		return llm.Response{}, &ProviderError{Class: ClassBadRequest}
	}
	msgs := make([]oaMessage, 0, len(req.Messages)+1)
	if req.System != "" {
		msgs = append(msgs, oaMessage{Role: "system", Content: req.System})
	}
	for _, m := range req.Messages {
		msgs = append(msgs, oaMessage{Role: m.Role, Content: m.Content})
	}
	body, err := json.Marshal(oaRequest{Model: req.Model, Messages: msgs, MaxTokens: req.MaxTokens})
	if err != nil {
		return llm.Response{}, &ProviderError{Class: ClassBadRequest}
	}
	// 호출 직전에 주소를 다시 검사한다(저장 뒤 규칙이 바뀐 경우까지).
	if _, err := validateProviderURL(p.endpoint(), p.allowLocal); err != nil {
		return llm.Response{}, &ProviderError{Class: ClassBadRequest}
	}
	hr, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint(), bytes.NewReader(body))
	if err != nil {
		return llm.Response{}, &ProviderError{Class: ClassBadRequest}
	}
	hr.Header.Set("Content-Type", "application/json")
	hr.Header.Set("Authorization", "Bearer "+p.apiKey)
	for k, v := range p.headers {
		hr.Header.Set(k, v)
	}
	resp, err := p.hc.Do(hr)
	if err != nil {
		return llm.Response{}, &ProviderError{Class: ClassOf(err)}
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxProviderBody))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return llm.Response{}, &ProviderError{Class: classOfStatus(resp.StatusCode), Status: resp.StatusCode}
	}
	var out oaResponse
	if err := json.Unmarshal(raw, &out); err != nil || len(out.Choices) == 0 || out.Choices[0].Message.Content == "" {
		return llm.Response{}, &ProviderError{Class: ClassBadRequest, Status: resp.StatusCode}
	}
	return llm.Response{
		Text: out.Choices[0].Message.Content, Model: out.Model,
		Usage:      llm.Usage{InputTokens: out.Usage.PromptTokens, OutputTokens: out.Usage.CompletionTokens},
		StopReason: out.Choices[0].FinishReason,
	}, nil
}
