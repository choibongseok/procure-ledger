package llmgw

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
)

// ErrClass 는 공급자 호출 실패의 분류다(B45 명세 6절). 2단계의 재시도·대체 판단이 이 값을 쓴다.
type ErrClass string

const (
	ClassTimeout     ErrClass = "timeout"      // 시간 초과(재시도 가능)
	ClassRateLimited ErrClass = "rate_limited" // 429(재시도 가능)
	ClassHTTP5xx     ErrClass = "http_5xx"     // 5xx(재시도 가능)
	ClassNetwork     ErrClass = "network"      // 연결 거부·DNS·TLS(재시도 가능)
	ClassAuth        ErrClass = "auth"         // 401·403, 키 오류(재시도 불가)
	ClassBadRequest  ErrClass = "bad_request"  // 400·모델 없음·콘텐츠 거부(재시도 불가)
	ClassUnknown     ErrClass = "unknown"
)

// Retryable 은 같은 요청을 다시 보내 볼 만한 분류인지다.
func (c ErrClass) Retryable() bool {
	switch c {
	case ClassTimeout, ClassRateLimited, ClassHTTP5xx, ClassNetwork:
		return true
	}
	return false
}

// ProviderError 는 공급자가 돌려주는 분류된 오류다. 문장에 주소·키·응답 본문을 넣지 않는다.
type ProviderError struct {
	Class  ErrClass
	Status int // HTTP 상태(없으면 0)
}

func (e *ProviderError) Error() string {
	if e.Status > 0 {
		return fmt.Sprintf("공급자 호출 실패(%s, HTTP %d)", e.Class, e.Status)
	}
	return fmt.Sprintf("공급자 호출 실패(%s)", e.Class)
}

// ClassOf 는 오류의 분류를 돌려준다. ProviderError 가 아니면 context 기한·네트워크 오류를 보고 추정한다.
func ClassOf(err error) ErrClass {
	var pe *ProviderError
	if errors.As(err, &pe) {
		return pe.Class
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return ClassTimeout
	}
	var ne net.Error
	if errors.As(err, &ne) {
		if ne.Timeout() {
			return ClassTimeout
		}
		return ClassNetwork
	}
	return ClassUnknown
}

// classOfStatus 는 HTTP 상태를 분류로 바꾼다.
func classOfStatus(code int) ErrClass {
	switch {
	case code == http.StatusTooManyRequests:
		return ClassRateLimited
	case code == http.StatusUnauthorized || code == http.StatusForbidden:
		return ClassAuth
	case code >= 500:
		return ClassHTTP5xx
	case code >= 400:
		return ClassBadRequest
	}
	return ClassUnknown
}
