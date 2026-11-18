package llmgw

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract/llm"
)

// AnthropicAPIVersion 은 Messages API 버전 헤더 값이다.
const AnthropicAPIVersion = "2023-06-01"

const anthropicDefaultBaseURL = "https://api.anthropic.com"

// ErrNoAPIKey 는 키 환경변수가 비어 있을 때의 오류다. 호출하는 쪽(수동 실행)이 "건너뜀"으로 다룰 수 있다.
var ErrNoAPIKey = errors.New("llmgw: API 키 환경변수가 비어 있습니다")

// AnthropicProvider 는 Anthropic Messages API 공급자다(수동 실행용).
type AnthropicProvider struct {
	apiKey  string
	baseURL string
	hc      *http.Client
}

// NewAnthropic 은 keyEnv 환경변수에서 키를 읽어 공급자를 만든다. 키가 없으면 ErrNoAPIKey.
// baseURL 이 비어 있으면 공식 주소를 쓴다(테스트는 httptest 주소를 넣는다).
func NewAnthropic(keyEnv, baseURL string, timeout time.Duration) (*AnthropicProvider, error) {
	key := strings.TrimSpace(os.Getenv(keyEnv))
	if key == "" {
		return nil, fmt.Errorf("%w (%s)", ErrNoAPIKey, keyEnv)
	}
	if baseURL == "" {
		baseURL = anthropicDefaultBaseURL
	}
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &AnthropicProvider{apiKey: key, baseURL: strings.TrimRight(baseURL, "/"), hc: &http.Client{Timeout: timeout}}, nil
}

// Name 은 Provider 를 구현한다.
func (p *AnthropicProvider) Name() string { return "anthropic" }

type anthropicBlock struct {
	Type   string           `json:"type"`
	Text   string           `json:"text,omitempty"`
	Source *anthropicSource `json:"source,omitempty"`
}

type anthropicSource struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
}

type anthropicMessage struct {
	Role    string           `json:"role"`
	Content []anthropicBlock `json:"content"`
}

type anthropicRequest struct {
	Model     string             `json:"model"`
	MaxTokens int                `json:"max_tokens"`
	System    string             `json:"system,omitempty"`
	Messages  []anthropicMessage `json:"messages"`
}

type anthropicResponse struct {
	Model      string `json:"model"`
	StopReason string `json:"stop_reason"`
	Content    []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	Usage struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
	Error *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

// Complete 는 Provider 를 구현한다. 샘플링 파라미터·강제 tool_choice 는 보내지 않는다.
func (p *AnthropicProvider) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	if req.Model == "" {
		return llm.Response{}, errors.New("llmgw: 모델이 지정되지 않았습니다")
	}
	max := req.MaxTokens
	if max <= 0 {
		max = 2048
	}
	body := anthropicRequest{Model: req.Model, MaxTokens: max, System: req.System}
	for i, m := range req.Messages {
		blocks := []anthropicBlock{}
		if m.Role == "user" && i == lastUser(req.Messages) {
			for _, img := range req.Images {
				blocks = append(blocks, anthropicBlock{Type: "image", Source: &anthropicSource{
					Type: "base64", MediaType: img.MediaType, Data: base64.StdEncoding.EncodeToString(img.Data)}})
			}
		}
		blocks = append(blocks, anthropicBlock{Type: "text", Text: m.Content})
		body.Messages = append(body.Messages, anthropicMessage{Role: m.Role, Content: blocks})
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return llm.Response{}, err
	}
	hr, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/v1/messages", bytes.NewReader(raw))
	if err != nil {
		return llm.Response{}, err
	}
	hr.Header.Set("content-type", "application/json")
	hr.Header.Set("x-api-key", p.apiKey)
	hr.Header.Set("anthropic-version", AnthropicAPIVersion)
	resp, err := p.hc.Do(hr)
	if err != nil {
		return llm.Response{}, fmt.Errorf("anthropic 호출 실패: %w", redactKey(err, p.apiKey))
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return llm.Response{}, err
	}
	var out anthropicResponse
	if err := json.Unmarshal(data, &out); err != nil {
		return llm.Response{}, fmt.Errorf("anthropic 응답 해석 실패(HTTP %d)", resp.StatusCode)
	}
	if resp.StatusCode/100 != 2 || out.Error != nil {
		msg := ""
		if out.Error != nil {
			msg = out.Error.Type + ": " + out.Error.Message
		}
		return llm.Response{}, fmt.Errorf("anthropic 오류(HTTP %d) %s", resp.StatusCode, msg)
	}
	var sb strings.Builder
	for _, c := range out.Content {
		if c.Type == "text" {
			sb.WriteString(c.Text)
		}
	}
	if out.StopReason == "refusal" {
		return llm.Response{}, errors.New("anthropic 이 요청을 거절했습니다(refusal)")
	}
	return llm.Response{
		Text:       sb.String(),
		Model:      out.Model,
		Usage:      llm.Usage{InputTokens: out.Usage.InputTokens, OutputTokens: out.Usage.OutputTokens},
		StopReason: out.StopReason,
	}, nil
}

func lastUser(ms []llm.Message) int {
	idx := -1
	for i, m := range ms {
		if m.Role == "user" {
			idx = i
		}
	}
	return idx
}

// redactKey 는 오류 문자열에 키가 섞여 들어가는 일을 막는다.
func redactKey(err error, key string) error {
	if key == "" || !strings.Contains(err.Error(), key) {
		return err
	}
	return errors.New(strings.ReplaceAll(err.Error(), key, "[KEY]"))
}
