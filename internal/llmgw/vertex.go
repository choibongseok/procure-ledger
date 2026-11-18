package llmgw

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract/llm"
	"github.com/choibongseok/procure-ledger/internal/netsafe"
)

// VertexProvider 는 Google Vertex AI 의 Gemini generateContent 공급자다(B45 3단계). SDK 없이 net/http 로 직접 부른다.
// 인증: 서비스 계정 JSON 의 개인키로 RS256 JWT 를 서명해 https://oauth2.googleapis.com/token 에서 액세스 토큰을 받는다(토큰 주소는 JSON 의 token_uri 를 따르지 않고 고정한다: SSRF 방지).
// 호출: POST https://<로케이션>-aiplatform.googleapis.com/v1/projects/<프로젝트>/locations/<로케이션>/publishers/google/models/<모델>:generateContent,
// 본문 {"systemInstruction":{"parts":[{"text"}]},"contents":[{"role","parts":[{"text"}]}],"generationConfig":{"maxOutputTokens"}},
// 응답 candidates[0].content.parts[].text · usageMetadata.promptTokenCount/candidatesTokenCount. 파트너 모델(rawPredict)은 지원하지 않는다.
// **운영 전 최신 문서와 실제 계정으로 확인 필요.**
type VertexProvider struct {
	project, location string
	clientEmail       string
	key               *rsa.PrivateKey
	hc                *http.Client
	now               func() time.Time
	apiBase           string // 시험 전용
	tokenURL          string
}

// VertexConfig 는 봉인된 설정에서 풀어낸 값이다.
type VertexConfig struct {
	Project, Location, ServiceAccountJSON string
}

const vertexTokenURL = "https://oauth2.googleapis.com/token"

var (
	projectRe  = regexp.MustCompile(`^[a-z][a-z0-9-]{4,28}[a-z0-9]$`)
	locationRe = regexp.MustCompile(`^(global|[a-z]+(-[a-z]+)*[0-9]+)$`)
	tokenCache sync.Map // 서비스 계정 이메일 → *cachedToken
)

type cachedToken struct {
	mu      sync.Mutex
	token   string
	expires time.Time
}

// VertexHost 는 로케이션의 API 호스트다.
func VertexHost(location string) string {
	if location == "global" {
		return "aiplatform.googleapis.com"
	}
	return location + "-aiplatform.googleapis.com"
}

// NewVertex 는 설정을 검사하고 공급자를 만든다. testAPIBase·testTokenURL 은 로컬 시험(allowLocal)에서만 쓰인다.
func NewVertex(c VertexConfig, allowLocal bool, timeout time.Duration, testAPIBase, testTokenURL string) (*VertexProvider, error) {
	if !projectRe.MatchString(c.Project) {
		return nil, errors.New("프로젝트 ID 형식이 올바르지 않습니다")
	}
	if !locationRe.MatchString(c.Location) {
		return nil, errors.New("로케이션 형식이 올바르지 않습니다(예: us-central1)")
	}
	var sa struct {
		Email string `json:"client_email"`
		Key   string `json:"private_key"`
	}
	if err := json.Unmarshal([]byte(c.ServiceAccountJSON), &sa); err != nil || sa.Email == "" || sa.Key == "" {
		return nil, errors.New("서비스 계정 JSON 형식이 올바르지 않습니다")
	}
	blk, _ := pem.Decode([]byte(sa.Key))
	if blk == nil {
		return nil, errors.New("서비스 계정 개인키를 읽을 수 없습니다")
	}
	var key *rsa.PrivateKey
	if k, err := x509.ParsePKCS8PrivateKey(blk.Bytes); err == nil {
		rk, ok := k.(*rsa.PrivateKey)
		if !ok {
			return nil, errors.New("서비스 계정 개인키는 RSA 여야 합니다")
		}
		key = rk
	} else if rk, err := x509.ParsePKCS1PrivateKey(blk.Bytes); err == nil {
		key = rk
	} else {
		return nil, errors.New("서비스 계정 개인키를 읽을 수 없습니다")
	}
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	p := &VertexProvider{project: c.Project, location: c.Location, clientEmail: sa.Email, key: key, now: time.Now, tokenURL: vertexTokenURL,
		hc: netsafe.NewClient(netsafe.ClientOptions{AllowLocal: allowLocal, Timeout: timeout})}
	if allowLocal {
		p.apiBase, p.tokenURL = strings.TrimRight(testAPIBase, "/"), orDefault(testTokenURL, vertexTokenURL)
	}
	return p, nil
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

// Name 은 Provider 를 구현한다.
func (p *VertexProvider) Name() string { return KindVertex }

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// signJWT 는 서비스 계정 JWT(RS256)를 만든다. 시험이 서명을 공개키로 검증한다.
func (p *VertexProvider) signJWT(now time.Time) (string, error) {
	head := b64([]byte(`{"alg":"RS256","typ":"JWT"}`))
	claims, _ := json.Marshal(map[string]any{
		"iss": p.clientEmail, "scope": "https://www.googleapis.com/auth/cloud-platform", "aud": vertexTokenURL,
		"iat": now.Unix(), "exp": now.Add(time.Hour).Unix(),
	})
	signing := head + "." + b64(claims)
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, p.key, crypto.SHA256, sum[:])
	if err != nil {
		return "", err
	}
	return signing + "." + b64(sig), nil
}

// accessToken 은 캐시된 토큰(만료 60초 전까지)이나 새로 교환한 토큰을 돌려준다. 토큰은 메모리에만 있다.
func (p *VertexProvider) accessToken(ctx context.Context) (string, error) {
	v, _ := tokenCache.LoadOrStore(p.clientEmail+"|"+p.tokenURL, &cachedToken{})
	ct := v.(*cachedToken)
	ct.mu.Lock()
	defer ct.mu.Unlock()
	if ct.token != "" && p.now().Before(ct.expires.Add(-60*time.Second)) {
		return ct.token, nil
	}
	jwt, err := p.signJWT(p.now())
	if err != nil {
		return "", &ProviderError{Class: ClassAuth}
	}
	form := url.Values{"grant_type": {"urn:ietf:params:oauth:grant-type:jwt-bearer"}, "assertion": {jwt}}
	hr, err := http.NewRequestWithContext(ctx, http.MethodPost, p.tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", &ProviderError{Class: ClassBadRequest}
	}
	hr.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := p.hc.Do(hr)
	if err != nil {
		return "", &ProviderError{Class: ClassOf(err)}
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		cls := classOfStatus(resp.StatusCode)
		if resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusUnauthorized {
			cls = ClassAuth // 잘못된 서비스 계정·서명은 인증 실패다
		}
		return "", &ProviderError{Class: cls, Status: resp.StatusCode}
	}
	var out struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || out.AccessToken == "" {
		return "", &ProviderError{Class: ClassAuth, Status: resp.StatusCode}
	}
	ct.token, ct.expires = out.AccessToken, p.now().Add(time.Duration(out.ExpiresIn)*time.Second)
	return ct.token, nil
}

type vxPart struct {
	Text string `json:"text"`
}

type vxContent struct {
	Role  string   `json:"role,omitempty"`
	Parts []vxPart `json:"parts"`
}

type vxRequest struct {
	SystemInstruction *vxContent  `json:"systemInstruction,omitempty"`
	Contents          []vxContent `json:"contents"`
	GenerationConfig  struct {
		MaxOutputTokens int `json:"maxOutputTokens,omitempty"`
	} `json:"generationConfig"`
}

type vxResponse struct {
	Candidates []struct {
		Content      vxContent `json:"content"`
		FinishReason string    `json:"finishReason"`
	} `json:"candidates"`
	UsageMetadata struct {
		PromptTokenCount     int `json:"promptTokenCount"`
		CandidatesTokenCount int `json:"candidatesTokenCount"`
	} `json:"usageMetadata"`
}

// VertexRequestBody 는 generateContent 요청 본문이다(본문 골든 시험이 쓴다).
func VertexRequestBody(req llm.Request) ([]byte, error) {
	var b vxRequest
	if req.System != "" {
		b.SystemInstruction = &vxContent{Parts: []vxPart{{Text: req.System}}}
	}
	for _, m := range req.Messages {
		role := "user"
		if m.Role == "assistant" {
			role = "model"
		}
		b.Contents = append(b.Contents, vxContent{Role: role, Parts: []vxPart{{Text: m.Content}}})
	}
	b.GenerationConfig.MaxOutputTokens = req.MaxTokens
	return json.Marshal(b)
}

// Complete 는 Provider 를 구현한다. 이미지는 지원하지 않는다(supports_images=0).
func (p *VertexProvider) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	if len(req.Images) > 0 || req.Model == "" || strings.ContainsAny(req.Model, "/?#:") {
		return llm.Response{}, &ProviderError{Class: ClassBadRequest}
	}
	body, err := VertexRequestBody(req)
	if err != nil {
		return llm.Response{}, &ProviderError{Class: ClassBadRequest}
	}
	tok, err := p.accessToken(ctx)
	if err != nil {
		return llm.Response{}, err
	}
	base := "https://" + VertexHost(p.location)
	if p.apiBase != "" {
		base = p.apiBase
	}
	target := base + "/v1/projects/" + p.project + "/locations/" + p.location + "/publishers/google/models/" + url.PathEscape(req.Model) + ":generateContent"
	hr, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return llm.Response{}, &ProviderError{Class: ClassBadRequest}
	}
	hr.Header.Set("Content-Type", "application/json")
	hr.Header.Set("Authorization", "Bearer "+tok)
	resp, err := p.hc.Do(hr)
	if err != nil {
		return llm.Response{}, &ProviderError{Class: ClassOf(err)}
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxProviderBody))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return llm.Response{}, &ProviderError{Class: classOfStatus(resp.StatusCode), Status: resp.StatusCode}
	}
	var out vxResponse
	if err := json.Unmarshal(raw, &out); err != nil || len(out.Candidates) == 0 {
		return llm.Response{}, &ProviderError{Class: ClassBadRequest, Status: resp.StatusCode}
	}
	var text strings.Builder
	for _, pt := range out.Candidates[0].Content.Parts {
		text.WriteString(pt.Text)
	}
	if text.Len() == 0 {
		return llm.Response{}, &ProviderError{Class: ClassBadRequest, Status: resp.StatusCode}
	}
	return llm.Response{Text: text.String(), Model: req.Model, Usage: llm.Usage{InputTokens: out.UsageMetadata.PromptTokenCount, OutputTokens: out.UsageMetadata.CandidatesTokenCount}, StopReason: out.Candidates[0].FinishReason}, nil
}
