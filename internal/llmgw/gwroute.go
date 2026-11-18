package llmgw

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/llm"
)

type routedKey struct{}
type statsKey struct{}

// side 는 경로의 한쪽(주 또는 대체) 공급자다.
type side struct {
	code     string
	model    string
	provider Provider
	timeout  time.Duration
	prices   Prices
	images   bool // 이미지를 받을 수 있는가
}

// routed 는 한 호출에 적용된 경로다(규칙이 맞았을 때만 있다).
type routed struct {
	rule         RouteRule
	primary      side
	fallback     *side  // nil 이면 대체 없음
	fallbackSkip string // 대체를 붙이지 못한 이유(감사 표시용: model_policy|unavailable)
	maxAttempts  int
	totalTimeout time.Duration
}

// callStats 는 한 호출의 시도 결과를 사용량·감사에 옮기려고 context 에 싣는 집계다(Complete 가 만들고 invoke 가 채운다).
type callStats struct {
	routeID      int64
	attempts     int
	fallbackFrom string
	fallbackOff  string // 대체를 붙이지 못한 이유(model_policy|unavailable)
	reason       string
	latencyMS    int64
	code         string // 최종 공급자 코드(비면 경로 주 공급자 또는 기본)
	model        string
	prices       *Prices
}

func routedFrom(ctx context.Context) *routed {
	r, _ := ctx.Value(routedKey{}).(*routed)
	return r
}

func statsFrom(ctx context.Context) *callStats {
	s, _ := ctx.Value(statsKey{}).(*callStats)
	return s
}

// provName 은 사용량·감사에 적는 공급자 이름이다. 최종 공급자 코드가 있으면 그것, 경로가 있으면 주 공급자 코드, 없으면 기본 공급자 이름이다.
func (g *Gateway) provName(ctx context.Context) string {
	if st := statsFrom(ctx); st != nil && st.code != "" {
		return st.code
	}
	if r := routedFrom(ctx); r != nil {
		return r.primary.code
	}
	return g.o.Provider.Name()
}

// costCtx 는 비용이다. 최종 공급자(없으면 경로의 주 공급자)에 가격이 있으면 그 가격, 없으면 기본 가격표를 쓴다.
func (g *Gateway) costCtx(ctx context.Context, u llm.Usage) int64 {
	var pr *Prices
	if st := statsFrom(ctx); st != nil && st.prices != nil {
		pr = st.prices
	} else if r := routedFrom(ctx); r != nil {
		pr = &r.primary.prices
	}
	if pr != nil && (pr.InPerMTok != 0 || pr.OutPerMTok != 0) {
		return (int64(u.InputTokens)*pr.InPerMTok + int64(u.OutputTokens)*pr.OutPerMTok) / 1_000_000
	}
	return g.cost(u)
}

func (g *Gateway) buildSide(ctx context.Context, row ProviderRow, model string) (side, error) {
	prov, err := Build(row, BuildOptions{Sealer: g.o.Sealer, AllowLocal: g.o.AllowLocal, Rehearsal: g.o.Rehearsal})
	if err != nil {
		return side{}, err
	}
	if model == "" {
		model = row.DefaultModel
	}
	to := time.Duration(row.TimeoutMS) * time.Millisecond
	if to <= 0 {
		to = g.o.Timeout
	}
	return side{code: row.Code, model: model, provider: prov, timeout: to, prices: Prices{InPerMTok: row.PriceIn, OutPerMTok: row.PriceOut}, images: row.SupportsImages}, nil
}

// route 는 호출자·용도에 맞는 규칙을 골라 공급자를 만든다(B45 명세 5절). 규칙이 하나도 없거나 맞지 않으면 nil(기본 경로).
// 규칙의 주 공급자가 꺼져 있으면 그 규칙은 후보에서 빠진다. 설정을 풀 수 없으면 오류다(조용히 기본 경로로 가지 않는다: 의도한 공급자가 아닐 수 있다).
// 대체 공급자는 켜져 있고 설정을 풀 수 있고 대체 모델이 모델 허용 목록을 통과할 때만 붙는다(아니면 대체 없이 주 공급자만, 이유는 fallbackSkip).
func (g *Gateway) route(ctx context.Context, p contract.Principal, req llm.Request) (*routed, error) {
	if code, ok := forcedProviderFrom(ctx); ok {
		return g.forcedRoute(ctx, code)
	}
	rules, err := LoadRoutes(ctx, g.o.DB, true)
	if err != nil || len(rules) == 0 {
		return nil, nil // 표가 없는 이전 스키마·규칙 없음은 기본 경로
	}
	keys, kerr := roleKeys(ctx, g.o.DB, p)
	if kerr != nil {
		keys = nil
	}
	if p.Role != "" {
		keys = append(keys, string(p.Role))
	}
	usable := rules[:0:0]
	rows := map[int64]ProviderRow{}
	for _, r := range rules {
		row, ok := rows[r.PrimaryProviderID]
		if !ok {
			row, err = LoadProvider(ctx, g.o.DB, r.PrimaryProviderID)
			if errors.Is(err, sql.ErrNoRows) {
				continue
			}
			if err != nil {
				return nil, err
			}
			rows[r.PrimaryProviderID] = row
		}
		if row.Active {
			usable = append(usable, r)
		}
	}
	rule, ok := PickRoute(usable, Subject{Purpose: req.Purpose, DepartmentID: p.DepartmentID, RoleKeys: keys})
	if !ok {
		return nil, nil
	}
	prim, err := g.buildSide(ctx, rows[rule.PrimaryProviderID], rule.PrimaryModel)
	if err != nil {
		return nil, err
	}
	rt := &routed{rule: rule, primary: prim, maxAttempts: rule.MaxAttempts, totalTimeout: time.Duration(rule.TotalTimeoutMS) * time.Millisecond}
	if rt.maxAttempts < 1 {
		rt.maxAttempts = 1
	}
	if rt.totalTimeout <= 0 {
		rt.totalTimeout = 60 * time.Second
	}
	if rule.FallbackProviderID != 0 {
		rt.fallback, rt.fallbackSkip = g.buildFallback(ctx, p, rule)
	}
	return rt, nil
}

func (g *Gateway) buildFallback(ctx context.Context, p contract.Principal, rule RouteRule) (*side, string) {
	row, err := LoadProvider(ctx, g.o.DB, rule.FallbackProviderID)
	if err != nil || !row.Active {
		return nil, "unavailable"
	}
	sd, err := g.buildSide(ctx, row, rule.FallbackModel)
	if err != nil {
		return nil, "unavailable"
	}
	// 대체 모델도 모델 허용 목록을 통과해야 한다(정책을 대체로 우회하지 못한다).
	if err := g.checkModel(ctx, p, sd.model); err != nil {
		return nil, "model_policy"
	}
	return &sd, ""
}

type forcedKey struct{}

// WithForcedProvider 는 이 호출을 규칙과 무관하게 코드가 가리키는 공급자로 보낸다(평가 실행용, B46). 대체는 붙지 않는다.
// 마스킹·모델 허용·한도·사용량·감사는 그대로 적용된다. 공급자가 없거나 꺼져 있으면 호출이 실패한다.
func WithForcedProvider(ctx context.Context, code string) context.Context {
	return context.WithValue(ctx, forcedKey{}, code)
}

func forcedProviderFrom(ctx context.Context) (string, bool) {
	c, ok := ctx.Value(forcedKey{}).(string)
	return c, ok && c != ""
}

func (g *Gateway) forcedRoute(ctx context.Context, code string) (*routed, error) {
	var id int64
	if err := g.o.DB.QueryRowContext(ctx, `SELECT id FROM llm_providers WHERE code = ?`, code).Scan(&id); err != nil {
		return nil, errors.New("지정한 공급자를 찾을 수 없습니다")
	}
	row, err := LoadProvider(ctx, g.o.DB, id)
	if err != nil {
		return nil, err
	}
	if !row.Active {
		return nil, errors.New("지정한 공급자가 꺼져 있습니다")
	}
	sd, err := g.buildSide(ctx, row, "")
	if err != nil {
		return nil, err
	}
	return &routed{rule: RouteRule{}, primary: sd, maxAttempts: 1, totalTimeout: sd.timeout}, nil
}
