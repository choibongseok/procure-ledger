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

// BedrockProvider 는 AWS Bedrock Runtime 의 Converse API 공급자다(B45 3단계). SDK 없이 net/http + SigV4 로 직접 부른다.
// 요청 형식은 공개 문서 수준의 가정이다: POST https://bedrock-runtime.<리전>.amazonaws.com/model/<모델ID>/converse,
// 본문 {"system":[{"text"}],"messages":[{"role","content":[{"text"}]}],"inferenceConfig":{"maxTokens"}},
// 응답 output.message.content[].text · usage.inputTokens/outputTokens · stopReason. **운영 전 최신 문서와 실제 계정으로 확인 필요.**
type BedrockProvider struct {
	region   string
	creds    awsCreds
	host     string
	baseURL  string // 시험 전용(httptest). 비면 실제 호스트
	hc       *http.Client
	now      func() time.Time
	allowLoc bool
}

// BedrockConfig 는 봉인된 설정에서 풀어낸 값이다.
type BedrockConfig struct {
	Region, AccessKeyID, SecretAccessKey, SessionToken string
}

var regionRe = regexp.MustCompile(`^[a-z]{2}(-[a-z]+)+-[0-9]$`)

// NewBedrock 은 리전 형식을 검사하고 공급자를 만든다. 호스트는 리전에서 조립하는 고정 패턴이라 임의 주소를 넣을 곳이 없다.
// testBaseURL 은 로컬 시험(allowLocal)에서만 쓰인다.
func NewBedrock(c BedrockConfig, allowLocal bool, timeout time.Duration, testBaseURL string) (*BedrockProvider, error) {
	if !regionRe.MatchString(c.Region) {
		return nil, errors.New("리전 형식이 올바르지 않습니다(예: ap-northeast-2)")
	}
	if strings.TrimSpace(c.AccessKeyID) == "" || strings.TrimSpace(c.SecretAccessKey) == "" {
		return nil, errors.New("액세스 키와 비밀 키를 입력하세요")
	}
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	p := &BedrockProvider{region: c.Region, creds: awsCreds{c.AccessKeyID, c.SecretAccessKey, c.SessionToken}, host: "bedrock-runtime." + c.Region + ".amazonaws.com",
		hc: netsafe.NewClient(netsafe.ClientOptions{AllowLocal: allowLocal, Timeout: timeout}), now: time.Now, allowLoc: allowLocal}
	if allowLocal && testBaseURL != "" {
		p.baseURL = strings.TrimRight(testBaseURL, "/")
	}
	return p, nil
}

// Name 은 Provider 를 구현한다.
func (p *BedrockProvider) Name() string { return KindBedrock }

// BedrockHost 는 리전의 런타임 호스트다(공급자 행의 hint 에 쓴다).
func BedrockHost(region string) string { return "bedrock-runtime." + region + ".amazonaws.com" }

type brText struct {
	Text string `json:"text"`
}

type brMessage struct {
	Role    string   `json:"role"`
	Content []brText `json:"content"`
}

type brRequest struct {
	System          []brText    `json:"system,omitempty"`
	Messages        []brMessage `json:"messages"`
	InferenceConfig struct {
		MaxTokens int `json:"maxTokens,omitempty"`
	} `json:"inferenceConfig"`
}

type brResponse struct {
	Output struct {
		Message struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"message"`
	} `json:"output"`
	Usage struct {
		InputTokens  int `json:"inputTokens"`
		OutputTokens int `json:"outputTokens"`
	} `json:"usage"`
	StopReason string `json:"stopReason"`
}

// BedrockRequestBody 는 Converse 요청 본문이다(본문 골든 시험이 쓴다).
func BedrockRequestBody(req llm.Request) ([]byte, error) {
	var b brRequest
	if req.System != "" {
		b.System = []brText{{Text: req.System}}
	}
	for _, m := range req.Messages {
		role := m.Role
		if role != "assistant" {
			role = "user"
		}
		b.Messages = append(b.Messages, brMessage{Role: role, Content: []brText{{Text: m.Content}}})
	}
	b.InferenceConfig.MaxTokens = req.MaxTokens
	return json.Marshal(b)
}

// Complete 는 Provider 를 구현한다. 이미지는 지원하지 않는다(supports_images=0). 오류는 ProviderError(분류)다.
func (p *BedrockProvider) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	if len(req.Images) > 0 || req.Model == "" {
		return llm.Response{}, &ProviderError{Class: ClassBadRequest}
	}
	body, err := BedrockRequestBody(req)
	if err != nil {
		return llm.Response{}, &ProviderError{Class: ClassBadRequest}
	}
	encPath := "/model/" + awsEscape(req.Model) + "/converse"
	target := "https://" + p.host + encPath
	host := p.host
	if p.baseURL != "" {
		target = p.baseURL + encPath
		if u, err := url.Parse(p.baseURL); err == nil {
			host = u.Host
		}
	}
	auth, amzDate := signV4(http.MethodPost, host, encPath, nil, body, p.creds, p.region, "bedrock", p.now())
	hr, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return llm.Response{}, &ProviderError{Class: ClassBadRequest}
	}
	hr.Header.Set("Content-Type", "application/json")
	hr.Header.Set("X-Amz-Date", amzDate)
	hr.Header.Set("Authorization", auth)
	if p.creds.SessionToken != "" {
		hr.Header.Set("X-Amz-Security-Token", p.creds.SessionToken)
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
	var out brResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return llm.Response{}, &ProviderError{Class: ClassBadRequest, Status: resp.StatusCode}
	}
	var text strings.Builder
	for _, c := range out.Output.Message.Content {
		text.WriteString(c.Text)
	}
	if text.Len() == 0 {
		return llm.Response{}, &ProviderError{Class: ClassBadRequest, Status: resp.StatusCode}
	}
	return llm.Response{Text: text.String(), Model: req.Model, Usage: llm.Usage{InputTokens: out.Usage.InputTokens, OutputTokens: out.Usage.OutputTokens}, StopReason: out.StopReason}, nil
}
