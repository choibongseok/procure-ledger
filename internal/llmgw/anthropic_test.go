package llmgw_test

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract/llm"
	"github.com/choibongseok/procure-ledger/internal/llmgw"
)

func TestAnthropicProviderRequestShape(t *testing.T) {
	var gotPath, gotKey, gotVer string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotKey, gotVer = r.URL.Path, r.Header.Get("x-api-key"), r.Header.Get("anthropic-version")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"model":"m-test","stop_reason":"end_turn","content":[{"type":"text","text":"안녕"}],"usage":{"input_tokens":11,"output_tokens":3}}`))
	}))
	defer srv.Close()
	t.Setenv("PL_TEST_KEY", "sk-test-not-real")
	p, err := llmgw.NewAnthropic("PL_TEST_KEY", srv.URL, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := p.Complete(context.Background(), llm.Request{Model: "m-test", System: "sys", Messages: []llm.Message{{Role: "user", Content: "q"}}, MaxTokens: 50,
		Images: []llm.Image{{MediaType: "image/png", Data: []byte{1, 2}}}})
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/v1/messages" || gotKey != "sk-test-not-real" || gotVer != llmgw.AnthropicAPIVersion {
		t.Fatalf("요청 헤더·경로: %s %s %s", gotPath, gotKey, gotVer)
	}
	if gotBody["model"] != "m-test" || gotBody["system"] != "sys" {
		t.Fatalf("본문: %v", gotBody)
	}
	for _, banned := range []string{"temperature", "top_p", "top_k", "tool_choice", "thinking"} {
		if _, ok := gotBody[banned]; ok {
			t.Fatalf("보내지 않아야 할 필드: %s", banned)
		}
	}
	if resp.Text != "안녕" || resp.Usage.InputTokens != 11 || resp.Usage.OutputTokens != 3 {
		t.Fatalf("응답: %+v", resp)
	}
}

func TestAnthropicMissingKeyIsNotAPanic(t *testing.T) {
	t.Setenv("PL_TEST_KEY_EMPTY", "")
	if _, err := llmgw.NewAnthropic("PL_TEST_KEY_EMPTY", "", time.Second); !errors.Is(err, llmgw.ErrNoAPIKey) {
		t.Fatalf("키가 없으면 ErrNoAPIKey: %v", err)
	}
}

func TestAnthropicErrorDoesNotLeakKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		_, _ = w.Write([]byte(`{"error":{"type":"authentication_error","message":"invalid x-api-key"}}`))
	}))
	defer srv.Close()
	t.Setenv("PL_TEST_KEY", "sk-secret-123")
	p, _ := llmgw.NewAnthropic("PL_TEST_KEY", srv.URL, time.Second)
	_, err := p.Complete(context.Background(), llm.Request{Model: "m", Messages: []llm.Message{{Role: "user", Content: "q"}}})
	if err == nil || strings.Contains(err.Error(), "sk-secret-123") {
		t.Fatalf("오류에 키가 섞임: %v", err)
	}
}

// 공급자 SDK·API 주소는 게이트웨이 밖에서 쓰지 않는다(R2 불변식).
func TestProviderCodeStaysInsideGateway(t *testing.T) {
	root := "."
	for i := 0; i < 6; i++ {
		if _, err := os.Stat(filepath.Join(root, "go.mod")); err == nil {
			break
		}
		root = filepath.Join("..", root)
	}
	banned := []string{"api.anthropic.com", "anthropic-sdk-go", "sashabaranov/go-openai", "generative-ai-go"}
	gwDir := filepath.Join(root, "internal", "llmgw")
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case "node_modules", ".git", "dist":
				return filepath.SkipDir
			}
			if path == gwDir {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, w := range banned {
			if strings.Contains(string(b), w) {
				t.Errorf("%s: 게이트웨이 밖에서 공급자 코드(%q)를 쓰고 있습니다", path, w)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
