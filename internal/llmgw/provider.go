// Package llmgw 는 LLM 게이트웨이다.
//
// 모든 모델 호출은 Gateway.Complete(llm.Client) 를 지난다. 게이트웨이는 호출 전에
//  1. 호출자(Principal)의 역할·부서에 대한 모델 허용 목록을 확인하고(규칙이 없으면 거부),
//  2. 일일 토큰 한도를 확인·예약하고(한 트랜잭션, 초과 시 contract.ErrQuotaExceeded → HTTP 429),
//  3. 프롬프트의 사업자번호·계좌·연락처를 마스킹한 뒤 공급자에게 보낸다.
//
// 호출 뒤에는 사용량·비용(llm_usage)과 감사 로그(channel=llmgw)를 같은 트랜잭션으로 남긴다.
// 차단·실패도 사용량 행과 감사 로그를 남긴다. 감사 기록이 실패하면 호출 결과를 돌려주지 않는다.
//
// 공급자는 두 개다. mock 은 결정적이라 CI·make verify 가 쓰고, anthropic 은 수동 실행용이다.
// anthropic 공급자는 Messages API 를 net/http 로 직접 부른다(go.mod 에 SDK 를 추가하지 않는다).
// 모델 ID 는 코드에 없다. 설정(PL_LLM_MODEL)으로만 온다. 가격표도 설정(PL_LLM_PRICE_IN_MICRO_PER_MTOK,
// PL_LLM_PRICE_OUT_MICRO_PER_MTOK, 1백만 토큰당 1e-6 USD)이고 비어 있으면 비용은 0 으로 기록한다.
//
// 공급자 SDK·API 주소는 이 패키지 밖에서 쓰지 않는다(boundary_test.go 가 검사한다).
package llmgw

import (
	"context"

	"github.com/choibongseok/procure-ledger/internal/contract/llm"
)

// Provider 는 실제 모델 호출기다. 게이트웨이만 부른다.
type Provider interface {
	Name() string
	Complete(ctx context.Context, req llm.Request) (llm.Response, error)
}
