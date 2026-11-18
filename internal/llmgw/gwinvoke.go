package llmgw

import (
	"context"
	"errors"
	"log"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/contract/llm"
)

// retryBackoff 는 같은 공급자를 다시 시도하기 전 대기다(D6: 500ms·1s).
var retryBackoff = []time.Duration{500 * time.Millisecond, time.Second}

func (g *Gateway) sleep(ctx context.Context, d time.Duration) {
	if g.o.Sleep != nil {
		g.o.Sleep(ctx, d)
		return
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
	case <-ctx.Done():
	}
}

// reasonOf 는 실패 분류를 사용량·감사의 대체 사유 값으로 바꾼다.
func reasonOf(c ErrClass) string {
	switch c {
	case ClassTimeout, ClassRateLimited, ClassHTTP5xx, ClassNetwork, ClassAuth, ClassBadRequest:
		return string(c)
	}
	return string(ClassUnknown)
}

// ReasonLabel 은 대체 사유의 한국어 표시다(감사 diff 와 화면이 쓴다).
func ReasonLabel(reason string) string {
	switch reason {
	case "timeout":
		return "시간 초과"
	case "rate_limited":
		return "호출 한도 초과"
	case "http_5xx":
		return "공급자 서버 오류"
	case "network":
		return "네트워크 오류"
	case "circuit_open":
		return "회로 차단 중"
	case "auth":
		return "인증 실패"
	case "bad_request":
		return "요청 거부"
	case "images":
		return "이미지 미지원"
	}
	return "알 수 없음"
}

// invoke 는 경로의 공급자를 부른다(B45 명세 6절 상태 기계). 전체 시간(total_timeout) 안에서
//  1. 주 공급자: 회로가 열려 있으면 건너뛰고, 아니면 최대 max_attempts 번 시도한다. 재시도 가능 실패(시간 초과·429·5xx·연결 오류)만 짧게 기다렸다가 다시 시도하고,
//     auth·bad_request 는 즉시 멈춘다.
//  2. 대체 공급자가 있으면 한 번 시도한다(재시도 없음). 대체 사유는 주 공급자의 마지막 실패 분류다.
//
// 한도 예약은 Complete 가 한 번만 했다. 비용은 최종 공급자 가격이다(st.prices). 결과는 st 에 채운다.
func (g *Gateway) invoke(ctx context.Context, rt *routed, req llm.Request, st *callStats) (llm.Response, error) {
	start := g.o.Now()
	defer func() { st.latencyMS = g.o.Now().Sub(start).Milliseconds() }()
	dctx, cancel := context.WithTimeout(ctx, rt.totalTimeout)
	defer cancel()
	hasImages := len(req.Images) > 0

	resp, class, err, tried := g.tryside(dctx, rt.primary, rt.maxAttempts, req, hasImages, st)
	if err == nil {
		g.settle(st, rt.primary)
		return resp, nil
	}
	if rt.fallback == nil {
		g.settle(st, rt.primary)
		return llm.Response{}, err
	}
	// 대체: 주 공급자의 실패 분류를 사유로 남긴다(시도하지 못했으면 그 이유).
	st.fallbackFrom = rt.primary.code
	st.reason = reasonOf(class)
	if !tried {
		st.reason = skipReason(class)
	}
	fresp, _, ferr, _ := g.tryside(dctx, *rt.fallback, 1, req, hasImages, st)
	g.settle(st, *rt.fallback)
	if ferr != nil {
		return llm.Response{}, ferr
	}
	return fresp, nil
}

func skipReason(c ErrClass) string {
	if c == "circuit_open" {
		return "circuit_open"
	}
	if c == "images" {
		return "images"
	}
	return reasonOf(c)
}

// settle 은 최종(또는 마지막으로 시도한) 공급자를 집계에 적는다.
func (g *Gateway) settle(st *callStats, s side) {
	st.code, st.model = s.code, s.model
	pr := s.prices
	st.prices = &pr
}

// tryside 는 공급자 한쪽에 최대 max 번 시도한다. 반환 class 는 마지막 실패 분류(건너뛰었으면 "circuit_open"·"images"), tried 는 실제로 호출했는지다.
func (g *Gateway) tryside(ctx context.Context, s side, max int, req llm.Request, hasImages bool, st *callStats) (llm.Response, ErrClass, error, bool) {
	if hasImages && !s.images {
		return llm.Response{}, "images", &ProviderError{Class: ClassBadRequest}, false
	}
	if !g.breakers.Allow(s.code) {
		return llm.Response{}, "circuit_open", &ProviderError{Class: ClassNetwork}, false
	}
	var lastErr error
	var class ErrClass
	for i := 0; i < max; i++ {
		st.attempts++
		actx, cancel := context.WithTimeout(ctx, s.timeout)
		r := req
		r.Model = s.model
		resp, err := s.provider.Complete(actx, r)
		cancel()
		if err == nil {
			g.breakers.Success(s.code)
			return resp, "", nil, true
		}
		lastErr, class = err, ClassOf(err)
		if ctx.Err() != nil && class == ClassUnknown {
			class = ClassTimeout // 전체 시간이 끝났다
		}
		if !class.Retryable() {
			if class == ClassAuth {
				g.breakers.AuthFailed(s.code)
			}
			g.breakers.Release(s.code)
			return llm.Response{}, class, lastErr, true
		}
		g.breakers.Failure(s.code)
		if i+1 >= max || ctx.Err() != nil {
			break
		}
		if state, _ := g.breakers.State(s.code); state == CircuitOpen {
			break // 방금 회로가 열렸다: 더 시도하지 않는다
		}
		g.sleep(ctx, retryBackoff[min(i, len(retryBackoff)-1)])
		if ctx.Err() != nil {
			break
		}
	}
	return llm.Response{}, class, lastErr, true
}

// recordCircuit 은 회로 전이를 감사에 남긴다(시스템 주체, 실패해도 호출은 막지 않는다).
func (g *Gateway) recordCircuit(code, event string) {
	action := "llm.circuit_open"
	if event == "close" {
		action = "llm.circuit_close"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := g.o.Txs.WithTx(ctx, func(tx contract.Tx) error {
		return g.o.Audit.Record(ctx, tx, audit.Entry{Actor: contract.Principal{Role: "system", Channel: contract.ChannelLLMGW}, Action: action, TargetType: audit.TargetLLMProvider,
			TargetID: code, Result: audit.ResultSuccess, Diff: []audit.DiffItem{{Key: "provider_code", New: code}}})
	})
	if err != nil && !errors.Is(err, context.Canceled) {
		log.Printf("llmgw: 회로 전이 감사 기록 실패(%s): %v", action, err)
	}
}
