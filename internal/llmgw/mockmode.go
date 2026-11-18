package llmgw

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract/llm"
)

// 모의 공급자의 동작 모드(B45 명세 9절). CI 가 대체·재시도·회로 차단을 결정적으로 시험할 때 쓴다.
//
//	""·ok            정상(MockProvider 와 같은 응답)
//	fail_5xx         ClassHTTP5xx
//	fail_429         ClassRateLimited
//	fail_auth        ClassAuth
//	fail_bad_request ClassBadRequest
//	slow:<ms>        ms 만큼 기다린 뒤 정상(ctx 가 끝나면 시간 초과)
//	flaky:<n>        처음 n 번만 ClassHTTP5xx, 그 뒤 정상
var mockCalls sync.Map // 공급자 코드 → *callCounter

type callCounter struct {
	mu sync.Mutex
	n  int
}

func (c *callCounter) next() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.n++
	return c.n
}

// ResetMockCounters 는 flaky 모드의 호출 횟수를 지운다(시험용).
func ResetMockCounters() { mockCalls = sync.Map{} }

// ValidMockMode 는 모드 문자열이 올바른지다.
func ValidMockMode(m string) bool {
	switch m {
	case "", "ok", "fail_5xx", "fail_429", "fail_auth", "fail_bad_request":
		return true
	}
	for _, p := range []string{"slow:", "flaky:"} {
		if strings.HasPrefix(m, p) {
			n, err := strconv.Atoi(strings.TrimPrefix(m, p))
			return err == nil && n >= 0 && n <= 600000
		}
	}
	return false
}

// ModeMockProvider 는 모드가 있는 결정적 모의 공급자다.
type ModeMockProvider struct {
	Code string
	Mode string
}

// Name 은 Provider 를 구현한다.
func (m ModeMockProvider) Name() string { return "mock" }

// Complete 는 Provider 를 구현한다.
func (m ModeMockProvider) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	switch {
	case m.Mode == "fail_5xx":
		return llm.Response{}, &ProviderError{Class: ClassHTTP5xx, Status: 503}
	case m.Mode == "fail_429":
		return llm.Response{}, &ProviderError{Class: ClassRateLimited, Status: 429}
	case m.Mode == "fail_auth":
		return llm.Response{}, &ProviderError{Class: ClassAuth, Status: 401}
	case m.Mode == "fail_bad_request":
		return llm.Response{}, &ProviderError{Class: ClassBadRequest, Status: 400}
	case strings.HasPrefix(m.Mode, "slow:"):
		ms, _ := strconv.Atoi(strings.TrimPrefix(m.Mode, "slow:"))
		select {
		case <-time.After(time.Duration(ms) * time.Millisecond):
		case <-ctx.Done():
			return llm.Response{}, &ProviderError{Class: ClassTimeout}
		}
	case strings.HasPrefix(m.Mode, "flaky:"):
		n, _ := strconv.Atoi(strings.TrimPrefix(m.Mode, "flaky:"))
		v, _ := mockCalls.LoadOrStore(m.Code, &callCounter{})
		if v.(*callCounter).next() <= n {
			return llm.Response{}, &ProviderError{Class: ClassHTTP5xx, Status: 503}
		}
	}
	return MockProvider{}.Complete(ctx, req)
}
