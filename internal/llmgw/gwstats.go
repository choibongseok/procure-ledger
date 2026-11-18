package llmgw

import (
	"context"
	"fmt"

	"github.com/choibongseok/procure-ledger/internal/contract/audit"
)

func (g *Gateway) statsRoute(ctx context.Context) int64 {
	if st := statsFrom(ctx); st != nil {
		return st.routeID
	}
	return 0
}

func (g *Gateway) statsAttempts(ctx context.Context) int {
	if st := statsFrom(ctx); st != nil && st.attempts > 0 {
		return st.attempts
	}
	return 1
}

func (g *Gateway) statsFrom(ctx context.Context) string {
	if st := statsFrom(ctx); st != nil {
		return st.fallbackFrom
	}
	return ""
}

func (g *Gateway) statsReason(ctx context.Context) string {
	if st := statsFrom(ctx); st != nil {
		return st.reason
	}
	return ""
}

func (g *Gateway) statsLatency(ctx context.Context) int64 {
	if st := statsFrom(ctx); st != nil {
		return st.latencyMS
	}
	return 0
}

// routeDetail 은 llm.complete 감사 Detail 에 붙는 경로 표시다(규칙이 맞은 호출만).
func (g *Gateway) routeDetail(ctx context.Context) string {
	st := statsFrom(ctx)
	if st == nil || st.routeID == 0 {
		return ""
	}
	d := fmt.Sprintf(" route=%d attempts=%d latency_ms=%d", st.routeID, g.statsAttempts(ctx), st.latencyMS)
	if st.fallbackFrom != "" {
		d += fmt.Sprintf(" fallback_from=%s fallback_reason=%s", st.fallbackFrom, st.reason)
	}
	if st.fallbackOff != "" {
		d += " fallback_disabled=" + st.fallbackOff
	}
	return d
}

// routeDiff 는 대체가 일어난 호출의 감사 diff 다(공급자 코드·사유·시도 횟수). 대체가 없으면 nil 이라 기존 기록은 그대로다.
func (g *Gateway) routeDiff(ctx context.Context) []audit.DiffItem {
	st := statsFrom(ctx)
	if st == nil {
		return nil
	}
	var out []audit.DiffItem
	if st.fallbackOff != "" {
		out = append(out, audit.DiffItem{Key: "fallback_disabled", New: FallbackOffLabel(st.fallbackOff)})
	}
	if st.fallbackFrom == "" {
		return out
	}
	return append(out, []audit.DiffItem{
		{Key: "fallback_from", New: st.fallbackFrom},
		{Key: "provider_code", New: g.provName(ctx)},
		{Key: "fallback_reason", New: ReasonLabel(st.reason)},
		{Key: "llm_attempts", New: fmt.Sprint(g.statsAttempts(ctx))},
	}...)
}

// FallbackOffLabel 은 대체를 붙이지 못한 이유의 한국어 표시다(감사 diff 가 쓴다).
func FallbackOffLabel(reason string) string {
	switch reason {
	case "model_policy":
		return "대체 모델이 모델 허용 목록에 없음"
	case "unavailable":
		return "대체 공급자를 쓸 수 없음"
	}
	return "알 수 없음"
}

// Breakers 는 이 게이트웨이의 회로 상태 모음이다(관리 서비스가 공유하고 시험이 읽는다).
func (g *Gateway) Breakers() *BreakerSet { return g.breakers }
