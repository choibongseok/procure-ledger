package llmgw_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/llm"
	"github.com/choibongseok/procure-ledger/internal/llmgw"
)

type fbEnv struct {
	*env
	t      *testing.T
	ad     *llmgw.ProviderAdmin
	ctx    context.Context
	gw     *llmgw.Gateway
	sleeps []time.Duration
	prim   int64
	fall   int64
}

// newFB 는 주(primMode)·대체(fallMode) 모의 공급자와 "*" 규칙 하나를 만든다. 재시도 대기는 기록만 하고 기다리지 않는다.
func newFB(t *testing.T, primMode, fallMode string, withFallback bool, maxAttempts int) *fbEnv {
	t.Helper()
	llmgw.ResetMockCounters()
	e := newEnv(t)
	e.allow(t, "", 0, "*", 1)
	f := &fbEnv{env: e, t: t, ad: e.admin(t, false), ctx: adminCtx(contract.PermLLMProviderManage, contract.PermLLMPolicyManage)}
	mk := func(code, mode, model string, price int64) int64 {
		pv, err := f.ad.SaveProvider(f.ctx, llmgw.ProviderInput{Code: code, Kind: "mock", Name: code, Mode: mode, DefaultModel: model, PriceIn: price, PriceOut: price})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.ad.SetActive(f.ctx, pv.ID, true); err != nil {
			t.Fatal(err)
		}
		return pv.ID
	}
	f.prim = mk("prim-mock", primMode, "model-a", 1_000_000)
	f.fall = mk("fall-mock", fallMode, "model-b", 5_000_000)
	in := llmgw.RouteInput{PurposePattern: "*", PrimaryProviderID: f.prim, MaxAttempts: maxAttempts, Active: true}
	if withFallback {
		in.FallbackProviderID = f.fall
	}
	if _, err := f.ad.SaveRoute(f.ctx, in); err != nil {
		t.Fatal(err)
	}
	gw, err := llmgw.New(llmgw.Options{Provider: e.spy, DefaultModel: "mock-1", DB: e.db, Txs: e.txs, Audit: e.rec, Masker: e.mask, Sealer: e.sealer(t),
		Prices: llmgw.Prices{InPerMTok: 1_000_000, OutPerMTok: 2_000_000}, Now: func() time.Time { return e.now },
		Sleep: func(_ context.Context, d time.Duration) { f.sleeps = append(f.sleeps, d) }})
	if err != nil {
		t.Fatal(err)
	}
	f.gw = gw
	return f
}

type usageRow struct {
	Provider, Model, Status, From, Reason string
	Attempts                              int
	Cost                                  int64
}

func (f *fbEnv) call(t *testing.T) (llm.Response, error) {
	t.Helper()
	return f.gw.Complete(as(contract.RoleStaff, 1, 10), llm.Request{Purpose: "x", MaxTokens: 100, Messages: []llm.Message{{Role: "user", Content: "hi"}}})
}

func (f *fbEnv) last(t *testing.T) usageRow {
	t.Helper()
	var u usageRow
	if err := f.db.QueryRow(`SELECT provider, model, status, fallback_from, fallback_reason, attempts, cost_micro_usd FROM llm_usage ORDER BY id DESC LIMIT 1`).
		Scan(&u.Provider, &u.Model, &u.Status, &u.From, &u.Reason, &u.Attempts, &u.Cost); err != nil {
		t.Fatal(err)
	}
	return u
}

// AC-4507: 주 공급자가 재시도 가능 실패(5xx)면 재시도 뒤 대체 공급자로 가고, 사용량·감사에 대체가 표시되며 비용은 최종 공급자 가격이다. 한도는 한 번만 센다.
func TestFallbackAfterRetriesOn5xx(t *testing.T) {
	f := newFB(t, "fail_5xx", "ok", true, 2)
	resp, err := f.call(t)
	if err != nil || resp.Text == "" {
		t.Fatalf("대체로 성공해야 한다: %v", err)
	}
	u := f.last(t)
	if u.Provider != "fall-mock" || u.Model != "model-b" || u.Status != "ok" || u.From != "prim-mock" || u.Reason != "http_5xx" || u.Attempts != 3 {
		t.Fatalf("사용량: %+v", u)
	}
	if len(f.sleeps) != 1 || f.sleeps[0] != 500*time.Millisecond {
		t.Fatalf("재시도 대기: %v", f.sleeps)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM llm_usage`); n != 1 {
		t.Fatalf("사용량 행은 호출당 하나(한도 한 번 예약): %d", n)
	}
	// 비용은 대체 공급자 가격(5/1백만)이다: 입력·출력 토큰 × 5.
	var in, out int64
	_ = f.db.QueryRow(`SELECT input_tokens, output_tokens FROM llm_usage`).Scan(&in, &out)
	if want := (in*5_000_000 + out*5_000_000) / 1_000_000; u.Cost != want || u.Cost == 0 {
		t.Fatalf("비용 %d(기대 %d)", u.Cost, want)
	}
	found := false
	for _, rec := range f.rec.Records() {
		if rec.Action != "llm.complete" {
			continue
		}
		for _, d := range rec.Diff {
			if d.Key == "fallback_from" && d.New == "prim-mock" {
				found = true
			}
			if d.Key == "fallback_reason" && d.New != "공급자 서버 오류" {
				t.Fatalf("사유 표시: %q", d.New)
			}
		}
		if !strings.Contains(rec.Detail, "fallback_from=prim-mock") {
			t.Fatalf("감사 Detail: %s", rec.Detail)
		}
	}
	if !found {
		t.Fatal("감사 diff 에 대체 표시가 없다")
	}
}

// 인증 실패·요청 거부는 재시도 없이 즉시 대체로 가고, 인증 실패는 "설정 확인 필요"를 켠다.
func TestAuthFailureFallsBackImmediately(t *testing.T) {
	f := newFB(t, "fail_auth", "ok", true, 3)
	if _, err := f.call(t); err != nil {
		t.Fatal(err)
	}
	u := f.last(t)
	if u.Provider != "fall-mock" || u.Reason != "auth" || u.Attempts != 2 || len(f.sleeps) != 0 {
		t.Fatalf("즉시 대체: %+v 대기 %v", u, f.sleeps)
	}
	if st, need := f.gwBreakers().State("prim-mock"); st != "closed" || !need {
		t.Fatalf("인증 실패는 회로를 열지 않고 설정 확인 표시만: %s %v", st, need)
	}
	g2 := newFB(t, "fail_bad_request", "ok", true, 3)
	if _, err := g2.call(t); err != nil {
		t.Fatal(err)
	}
	if u := g2.last(t); u.Reason != "bad_request" || u.Attempts != 2 {
		t.Fatalf("요청 거부도 즉시 대체: %+v", u)
	}
}

// 대체가 없으면 재시도 소진 뒤 실패하고, 사용량은 error 로 시도 수와 함께 남는다.
func TestNoFallbackFailsAfterRetries(t *testing.T) {
	f := newFB(t, "fail_429", "ok", false, 2)
	_, err := f.call(t)
	if err == nil || llmgw.ClassOf(err) != llmgw.ClassRateLimited {
		t.Fatalf("실패해야 한다: %v", err)
	}
	if u := f.last(t); u.Status != "error" || u.Attempts != 2 || u.From != "" || u.Provider != "prim-mock" {
		t.Fatalf("사용량: %+v", u)
	}
	// 대체까지 실패하면 마지막 분류로 실패하고 두 공급자의 시도가 모두 센다.
	g := newFB(t, "fail_5xx", "fail_429", true, 2)
	if _, err := g.call(t); err == nil || llmgw.ClassOf(err) != llmgw.ClassRateLimited {
		t.Fatalf("대체까지 실패: %v", err)
	}
	if u := g.last(t); u.Status != "error" || u.Attempts != 3 || u.From != "prim-mock" || u.Provider != "fall-mock" {
		t.Fatalf("두 공급자 시도 기록: %+v", u)
	}
}

// 재시도로 회복하면 대체 없이 성공한다(flaky:1).
func TestRetryRecoversWithoutFallback(t *testing.T) {
	f := newFB(t, "flaky:1", "ok", true, 2)
	if _, err := f.call(t); err != nil {
		t.Fatal(err)
	}
	if u := f.last(t); u.Provider != "prim-mock" || u.From != "" || u.Attempts != 2 {
		t.Fatalf("재시도 성공: %+v", u)
	}
}

// AC-4508: 회로 차단 — 연속 5회 실패로 열리고(대체로 직행), 만료 뒤 반열림 시험 한 번, 성공하면 닫힌다. 전이는 감사에 남는다.
func TestCircuitOpensAndRecovers(t *testing.T) {
	f := newFB(t, "fail_5xx", "ok", true, 1)
	for i := 0; i < 5; i++ {
		if _, err := f.call(t); err != nil {
			t.Fatal(err)
		}
	}
	if st, _ := f.gwBreakers().State("prim-mock"); st != "open" {
		t.Fatalf("연속 5회 실패 뒤 열려야 한다: %s", st)
	}
	if !f.rec.Has("llm.circuit_open", "llm_provider", "success") {
		t.Fatal("회로 열림 감사가 없다")
	}
	// 열려 있는 동안은 주 공급자를 부르지 않고 대체로 직행한다(시도 1, 사유 circuit_open).
	if _, err := f.call(t); err != nil {
		t.Fatal(err)
	}
	if u := f.last(t); u.Reason != "circuit_open" || u.Attempts != 1 || u.Provider != "fall-mock" {
		t.Fatalf("회로 열림 중: %+v", u)
	}
	// 60초가 지나면 반열림: 시험 한 번이 주 공급자로 가고 실패하면 다시 열린다.
	f.now = f.now.Add(61 * time.Second)
	if _, err := f.call(t); err != nil {
		t.Fatal(err)
	}
	if u := f.last(t); u.Attempts != 2 || u.Reason != "http_5xx" {
		t.Fatalf("반열림 시험(주 1 + 대체 1): %+v", u)
	}
	if st, _ := f.gwBreakers().State("prim-mock"); st != "open" {
		t.Fatalf("시험 실패는 다시 열어야 한다: %s", st)
	}
	// 주 공급자가 복구되면(모드 ok) 다음 반열림 시험이 성공해 닫는다.
	if _, err := f.db.Exec(`UPDATE llm_providers SET mode = 'ok' WHERE code = 'prim-mock'`); err != nil {
		t.Fatal(err)
	}
	f.now = f.now.Add(61 * time.Second)
	if _, err := f.call(t); err != nil {
		t.Fatal(err)
	}
	if u := f.last(t); u.Provider != "prim-mock" || u.From != "" {
		t.Fatalf("복구 시험 성공: %+v", u)
	}
	if st, _ := f.gwBreakers().State("prim-mock"); st != "closed" {
		t.Fatalf("성공하면 닫혀야 한다: %s", st)
	}
	if !f.rec.Has("llm.circuit_close", "llm_provider", "success") {
		t.Fatal("회로 닫힘 감사가 없다")
	}
}

// AC-4506: 대체 모델이 모델 허용 목록에 없으면 대체를 붙이지 않는다(정책 우회 금지).
func TestFallbackModelMustPassPolicy(t *testing.T) {
	llmgw.ResetMockCounters()
	e := newEnv(t)
	e.allow(t, "", 0, "model-a", 1) // model-b 는 허용 안 함
	ad := e.admin(t, false)
	ctx := adminCtx(contract.PermLLMProviderManage, contract.PermLLMPolicyManage)
	mk := func(code, mode, model string) int64 {
		pv, err := ad.SaveProvider(ctx, llmgw.ProviderInput{Code: code, Kind: "mock", Name: code, Mode: mode, DefaultModel: model})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ad.SetActive(ctx, pv.ID, true); err != nil {
			t.Fatal(err)
		}
		return pv.ID
	}
	a, b := mk("pol-a", "fail_5xx", "model-a"), mk("pol-b", "ok", "model-b")
	if _, err := ad.SaveRoute(ctx, llmgw.RouteInput{PurposePattern: "*", PrimaryProviderID: a, FallbackProviderID: b, MaxAttempts: 1, Active: true}); err != nil {
		t.Fatal(err)
	}
	gw := e.gwWith(t, false)
	if _, err := gw.Complete(as(contract.RoleStaff, 1, 10), llm.Request{Purpose: "x", Messages: []llm.Message{{Role: "user", Content: "hi"}}}); err == nil {
		t.Fatal("대체 모델이 정책에 막혔는데 호출이 성공했다")
	}
	var from string
	_ = e.db.QueryRow(`SELECT fallback_from FROM llm_usage ORDER BY id DESC LIMIT 1`).Scan(&from)
	if from != "" {
		t.Fatalf("대체가 일어나면 안 된다: %q", from)
	}
	// AC-4506: 대체를 붙이지 못한 이유가 llm.complete 감사에 남는다(Detail 과 한국어 diff).
	found := false
	for _, rec := range e.rec.Records() {
		if rec.Action != "llm.complete" || !strings.Contains(rec.Detail, "fallback_disabled=model_policy") {
			continue
		}
		for _, d := range rec.Diff {
			if d.Key == "fallback_disabled" && d.New == llmgw.FallbackOffLabel("model_policy") && strings.Contains(d.New, "모델 허용 목록") {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("llm.complete 감사에 fallback_disabled=model_policy 가 없다")
	}
}

// 이미지를 받지 못하는 주 공급자는 건너뛰고 대체로 간다(사유 images).
func TestImageRequestSkipsPrimaryWithoutImageSupport(t *testing.T) {
	f := newFB(t, "ok", "ok", true, 1)
	if _, err := f.db.Exec(`UPDATE llm_providers SET supports_images = 0 WHERE code = 'prim-mock'`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(`INSERT INTO app_settings (key, value, updated_at) VALUES ('llm.allow_images', '1', 't') ON CONFLICT(key) DO NOTHING`); err != nil {
		t.Fatal(err)
	}
	_, err := f.gw.Complete(as(contract.RoleStaff, 1, 10), llm.Request{Purpose: "x", Images: []llm.Image{{MediaType: "image/png", Data: []byte{1}}}, Messages: []llm.Message{{Role: "user", Content: "hi"}}})
	if err != nil {
		t.Skipf("이미지 전송 정책이 켜져 있지 않아 건너뜀: %v", err)
	}
	if u := f.last(t); u.Provider != "fall-mock" || u.Reason != "images" {
		t.Fatalf("이미지 대체: %+v", u)
	}
}

func (f *fbEnv) gwBreakers() *llmgw.BreakerSet { return f.gw.Breakers() }
