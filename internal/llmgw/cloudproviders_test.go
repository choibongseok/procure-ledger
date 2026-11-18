package llmgw

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract/llm"
)

// 외부 공급자 구현은 모의 서버로 요청 **형식**(경로·헤더·본문 골든·서명)과 응답 해석·오류 분류만 시험한다(실제 호출 없음).

var fixedNow = time.Date(2026, 11, 14, 9, 30, 0, 0, time.UTC)

func TestBedrockRequestFormatAndParsing(t *testing.T) {
	var gotPath, gotAuth, gotDate, gotBody string
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth, gotDate = r.URL.EscapedPath(), r.Header.Get("Authorization"), r.Header.Get("X-Amz-Date")
		var b strings.Builder
		buf := make([]byte, 4096)
		for {
			n, err := r.Body.Read(buf)
			b.Write(buf[:n])
			if err != nil {
				break
			}
		}
		gotBody = b.String()
		if status != http.StatusOK {
			w.WriteHeader(status)
			return
		}
		_, _ = w.Write([]byte(`{"output":{"message":{"content":[{"text":"안녕"},{"text":"하세요"}]}},"usage":{"inputTokens":12,"outputTokens":5},"stopReason":"end_turn"}`))
	}))
	defer srv.Close()
	p, err := NewBedrock(BedrockConfig{Region: "ap-northeast-2", AccessKeyID: "AKIDTEST", SecretAccessKey: "secret", SessionToken: "tok"}, true, 5*time.Second, srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	p.now = func() time.Time { return fixedNow }
	req := llm.Request{Model: "anthropic.claude-3-haiku-20240307-v1:0", System: "sys", MaxTokens: 50, Messages: []llm.Message{{Role: "user", Content: "hi"}}}
	resp, err := p.Complete(context.Background(), req)
	if err != nil || resp.Text != "안녕하세요" || resp.Usage.InputTokens != 12 || resp.Usage.OutputTokens != 5 || resp.StopReason != "end_turn" {
		t.Fatalf("응답: %+v %v", resp, err)
	}
	if gotPath != "/model/anthropic.claude-3-haiku-20240307-v1%3A0/converse" {
		t.Fatalf("요청 경로: %s", gotPath)
	}
	const wantBody = `{"system":[{"text":"sys"}],"messages":[{"role":"user","content":[{"text":"hi"}]}],"inferenceConfig":{"maxTokens":50}}`
	if gotBody != wantBody {
		t.Fatalf("본문 골든:\n got  %s\n want %s", gotBody, wantBody)
	}
	if gotDate != "20261114T093000Z" || !strings.HasPrefix(gotAuth, "AWS4-HMAC-SHA256 Credential=AKIDTEST/20261114/ap-northeast-2/bedrock/aws4_request, SignedHeaders=host;x-amz-date;x-amz-security-token, Signature=") {
		t.Fatalf("서명 헤더: %s / %s", gotDate, gotAuth)
	}
	// 같은 입력·시각이면 서명이 같다(결정적).
	host := strings.TrimPrefix(srv.URL, "http://")
	want, _ := signV4("POST", host, gotPath, nil, []byte(wantBody), awsCreds{"AKIDTEST", "secret", "tok"}, "ap-northeast-2", "bedrock", fixedNow)
	if gotAuth != want {
		t.Fatalf("서명이 다시 계산한 값과 다르다")
	}
	// 오류 분류와 문장에 값이 없음.
	for code, cls := range map[int]ErrClass{429: ClassRateLimited, 503: ClassHTTP5xx, 403: ClassAuth, 400: ClassBadRequest} {
		status = code
		_, err := p.Complete(context.Background(), req)
		if ClassOf(err) != cls || strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), srv.URL) {
			t.Errorf("HTTP %d: %v (%s)", code, err, ClassOf(err))
		}
	}
	// 고정 호스트: 이미지·빈 모델은 거부, 리전 형식 검사.
	if _, err := p.Complete(context.Background(), llm.Request{Model: "m", Images: []llm.Image{{MediaType: "image/png", Data: []byte{1}}}}); ClassOf(err) != ClassBadRequest {
		t.Errorf("이미지: %v", err)
	}
	for _, bad := range []string{"", "evil.com/x", "us-east", "US-EAST-1"} {
		if _, err := NewBedrock(BedrockConfig{Region: bad, AccessKeyID: "a", SecretAccessKey: "b"}, false, 0, ""); err == nil {
			t.Errorf("리전 %q 는 거부돼야 한다", bad)
		}
	}
	if BedrockHost("ap-northeast-2") != "bedrock-runtime.ap-northeast-2.amazonaws.com" {
		t.Fatal("호스트 패턴")
	}
}

func testSAJSON(t *testing.T) (string, *rsa.PublicKey) {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(k)
	if err != nil {
		t.Fatal(err)
	}
	pemKey := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
	j, _ := json.Marshal(map[string]string{"client_email": "svc@pl.test", "private_key": pemKey, "token_uri": "https://evil.example.com/token"})
	return string(j), &k.PublicKey
}

func TestVertexAuthAndRequestFormat(t *testing.T) {
	tokenCache = sync.Map{}
	saJSON, pub := testSAJSON(t)
	var tokenCalls int
	var gotPath, gotAuth, gotBody string
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tokenCalls++
		_ = r.ParseForm()
		if r.Form.Get("grant_type") != "urn:ietf:params:oauth:grant-type:jwt-bearer" {
			w.WriteHeader(400)
			return
		}
		parts := strings.Split(r.Form.Get("assertion"), ".")
		if len(parts) != 3 {
			w.WriteHeader(400)
			return
		}
		sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
		sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
		if rsa.VerifyPKCS1v15(pub, crypto.SHA256, sum[:], sig) != nil {
			w.WriteHeader(401)
			return
		}
		cb, _ := base64.RawURLEncoding.DecodeString(parts[1])
		var claims map[string]any
		_ = json.Unmarshal(cb, &claims)
		if claims["iss"] != "svc@pl.test" || claims["aud"] != "https://oauth2.googleapis.com/token" || claims["scope"] != "https://www.googleapis.com/auth/cloud-platform" {
			w.WriteHeader(401)
			return
		}
		_, _ = w.Write([]byte(`{"access_token":"ya29.test-token","expires_in":3600}`))
	}))
	defer tokenSrv.Close()
	status := http.StatusOK
	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth = r.URL.EscapedPath(), r.Header.Get("Authorization")
		b := make([]byte, 4096)
		n, _ := r.Body.Read(b)
		gotBody = string(b[:n])
		if status != http.StatusOK {
			w.WriteHeader(status)
			return
		}
		_, _ = w.Write([]byte(`{"candidates":[{"content":{"parts":[{"text":"안녕"},{"text":"하세요"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":9,"candidatesTokenCount":4}}`))
	}))
	defer apiSrv.Close()
	p, err := NewVertex(VertexConfig{Project: "proj-abc12", Location: "us-central1", ServiceAccountJSON: saJSON}, true, 5*time.Second, apiSrv.URL, tokenSrv.URL)
	if err != nil {
		t.Fatal(err)
	}
	p.now = func() time.Time { return fixedNow }
	req := llm.Request{Model: "gemini-test", System: "sys", MaxTokens: 50, Messages: []llm.Message{{Role: "user", Content: "hi"}, {Role: "assistant", Content: "yo"}}}
	resp, err := p.Complete(context.Background(), req)
	if err != nil || resp.Text != "안녕하세요" || resp.Usage.InputTokens != 9 || resp.Usage.OutputTokens != 4 || resp.StopReason != "STOP" {
		t.Fatalf("응답: %+v %v", resp, err)
	}
	if gotPath != "/v1/projects/proj-abc12/locations/us-central1/publishers/google/models/gemini-test:generateContent" || gotAuth != "Bearer ya29.test-token" {
		t.Fatalf("요청: %s %s", gotPath, gotAuth)
	}
	const wantBody = `{"systemInstruction":{"parts":[{"text":"sys"}]},"contents":[{"role":"user","parts":[{"text":"hi"}]},{"role":"model","parts":[{"text":"yo"}]}],"generationConfig":{"maxOutputTokens":50}}`
	if gotBody != wantBody {
		t.Fatalf("본문 골든:\n got  %s\n want %s", gotBody, wantBody)
	}
	// 토큰은 만료 전까지 재사용된다(토큰 서버 1회).
	if _, err := p.Complete(context.Background(), req); err != nil || tokenCalls != 1 {
		t.Fatalf("토큰 재사용: calls=%d err=%v", tokenCalls, err)
	}
	// 만료 60초 전부터는 다시 교환한다.
	p.now = func() time.Time { return fixedNow.Add(3560 * time.Second) }
	if _, err := p.Complete(context.Background(), req); err != nil || tokenCalls != 2 {
		t.Fatalf("토큰 갱신: calls=%d err=%v", tokenCalls, err)
	}
	for code, cls := range map[int]ErrClass{429: ClassRateLimited, 503: ClassHTTP5xx, 403: ClassAuth, 400: ClassBadRequest} {
		status = code
		_, err := p.Complete(context.Background(), req)
		if ClassOf(err) != cls || strings.Contains(err.Error(), "ya29") || strings.Contains(err.Error(), "PRIVATE") {
			t.Errorf("HTTP %d: %v (%s)", code, err, ClassOf(err))
		}
	}
	// 입력 검사: 형식이 틀린 프로젝트·로케이션·서비스 계정, 모델 이름의 경로 문자.
	for _, c := range []VertexConfig{{"X", "us-central1", saJSON}, {"proj-abc12", "evil.com", saJSON}, {"proj-abc12", "us-central1", "{}"}, {"proj-abc12", "us-central1", `{"client_email":"a","private_key":"nope"}`}} {
		if _, err := NewVertex(c, false, 0, "", ""); err == nil {
			t.Errorf("%+v 는 거부돼야 한다", c)
		}
	}
	if _, err := p.Complete(context.Background(), llm.Request{Model: "a/b", Messages: []llm.Message{{Role: "user", Content: "x"}}}); ClassOf(err) != ClassBadRequest {
		t.Errorf("모델 경로 문자: %v", err)
	}
	if VertexHost("us-central1") != "us-central1-aiplatform.googleapis.com" || VertexHost("global") != "aiplatform.googleapis.com" {
		t.Fatal("호스트 패턴")
	}
	_ = url.PathEscape
}
