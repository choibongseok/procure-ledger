// Package llm 은 LLM 호출 계약이다. 구현: ai (internal/llmgw, 공급자 anthropic·mock).
// 모든 모델 호출은 이 인터페이스를 거치고, 게이트웨이가 모델 허용 목록·일일 한도·전송 전 마스킹·
// 사용량 기록·감사 로그를 처리한다. 쓰는 쪽(docverify 등)은 공급자 SDK 를 직접 부르지 않는다.
//
// 모델 ID 는 코드에 하드코딩하지 않는다. Request.Model 이 비면 게이트웨이가 설정(Config.DefaultModel)을 쓴다.
package llm

import (
	"context"
	"time"
)

// Message 는 대화 한 턴이다. Role 은 "user" 또는 "assistant".
type Message struct {
	Role    string
	Content string
}

// Image 는 이미지 입력(이미지 증빙)이다.
type Image struct {
	MediaType string // "image/png" 등
	Data      []byte
}

// Request 는 한 번의 호출이다. 호출자 신원은 context 의 contract.Principal 에서 읽는다.
type Request struct {
	Model     string // 비우면 설정의 기본 모델
	System    string
	Messages  []Message
	Images    []Image
	MaxTokens int
	Purpose   string // 사용량 집계용 라벨. 예: "docverify.extract"
}

// Usage 는 토큰 사용량이다.
type Usage struct {
	InputTokens  int
	OutputTokens int
}

// Response 는 호출 결과다.
type Response struct {
	Text       string
	Model      string
	Usage      Usage
	StopReason string
}

// Client 는 LLM 호출기다. 한도 초과는 contract.ErrQuotaExceeded, 허용되지 않은 모델은
// contract.ErrModelNotAllowed 를 돌려준다.
type Client interface {
	Complete(ctx context.Context, req Request) (Response, error)
}

// Config 는 게이트웨이 설정 자리다. 값은 환경변수·설정 파일에서 채우고 코드에는 기본 ID 를 두지 않는다.
// (기본 모델 ID 는 공급자 문서로 확인한 뒤 배포 설정에 넣는다.)
type Config struct {
	Provider     string        // "anthropic" | "mock"
	DefaultModel string        // 비어 있으면 anthropic 공급자는 기동 실패
	APIKeyEnv    string        // API 키를 읽을 환경변수 이름(키 자체는 설정에 두지 않는다)
	Timeout      time.Duration // 호출 타임아웃
}
