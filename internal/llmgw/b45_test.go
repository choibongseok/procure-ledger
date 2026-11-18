package llmgw_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/choibongseok/procure-ledger/internal/acctsecret"
	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/llm"
	"github.com/choibongseok/procure-ledger/internal/llmgw"
)

func (e *env) sealer(t *testing.T) *acctsecret.Cipher {
	t.Helper()
	c, err := acctsecret.New(bytes.Repeat([]byte{9}, 32))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func (e *env) admin(t *testing.T, local bool) *llmgw.ProviderAdmin {
	t.Helper()
	return &llmgw.ProviderAdmin{DB: e.db, Txs: e.txs, Audit: e.rec, Sealer: e.sealer(t), AllowLocal: local, Now: func() time.Time { return e.now },
		// 시험은 네트워크를 쓰지 않는다: 공개 이름은 공인 주소로 해석된다고 본다(해석 검사 자체는 adminprov_address_test.go).
		Resolve: func(context.Context, string, bool) error { return nil }}
}

func adminCtx(perms ...contract.Permission) context.Context {
	return contract.WithPrincipal(context.Background(), contract.Principal{UserID: 1, Role: contract.RoleAdmin, Channel: contract.ChannelWeb, Auth: contract.AuthSession, Perms: contract.NewPermSet(perms...)})
}

func (e *env) gwWith(t *testing.T, local bool) *llmgw.Gateway {
	t.Helper()
	gw, err := llmgw.New(llmgw.Options{Provider: e.spy, DefaultModel: "mock-1", DB: e.db, Txs: e.txs, Audit: e.rec, Masker: e.mask, Sealer: e.sealer(t), AllowLocal: local,
		Prices: llmgw.Prices{InPerMTok: 1_000_000, OutPerMTok: 2_000_000}, Now: func() time.Time { return e.now }})
	if err != nil {
		t.Fatal(err)
	}
	return gw
}

func (e *env) usageProvider(t *testing.T) (provider, status string) {
	t.Helper()
	if err := e.db.QueryRow(`SELECT provider, status FROM llm_usage ORDER BY id DESC LIMIT 1`).Scan(&provider, &status); err != nil {
		t.Fatal(err)
	}
	return
}

// AC-4501: 공급자·규칙 행이 없는 설치는 기본 경로(기본 공급자·기본 모델)로 이전과 같게 간다.
func TestNoRoutesUsesDefaultPath(t *testing.T) {
	e := newEnv(t)
	e.allow(t, "", 0, "*", 1)
	gw := e.gwWith(t, false)
	if _, err := gw.Complete(as(contract.RoleStaff, 1, 10), llm.Request{Purpose: "docverify.extract", Messages: []llm.Message{{Role: "user", Content: "hi"}}}); err != nil {
		t.Fatal(err)
	}
	if p, st := e.usageProvider(t); p != "mock" || st != "ok" || e.spy.calls != 1 {
		t.Fatalf("기본 경로: provider=%s status=%s spy=%d", p, st, e.spy.calls)
	}
}

// 규칙이 맞으면 규칙의 공급자(코드)·모델로 가고, 사용량·감사에 공급자 코드가 남는다. 규칙에 안 맞는 용도는 기본 경로다.
func TestRouteSelectsProviderAndRecordsCode(t *testing.T) {
	e := newEnv(t)
	e.allow(t, "", 0, "*", 1)
	ad := e.admin(t, false)
	ctx := adminCtx(contract.PermLLMProviderManage, contract.PermLLMPolicyManage)
	pv, err := ad.SaveProvider(ctx, llmgw.ProviderInput{Code: "mock-inhouse", Kind: "mock", Name: "사내 모의", DefaultModel: "inhouse-1", PriceIn: 3_000_000, PriceOut: 3_000_000})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ad.SetActive(ctx, pv.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := ad.SaveRoute(ctx, llmgw.RouteInput{PurposePattern: "docverify.*", PrimaryProviderID: pv.ID, Active: true}); err != nil {
		t.Fatal(err)
	}
	gw := e.gwWith(t, false)
	resp, err := gw.Complete(as(contract.RoleStaff, 1, 10), llm.Request{Purpose: "docverify.extract", Messages: []llm.Message{{Role: "user", Content: "hi"}}})
	if err != nil {
		t.Fatal(err)
	}
	if p, st := e.usageProvider(t); p != "mock-inhouse" || st != "ok" || e.spy.calls != 0 {
		t.Fatalf("규칙 경로: provider=%s status=%s 기본 공급자 호출=%d", p, st, e.spy.calls)
	}
	var model string
	var cost int64
	if err := e.db.QueryRow(`SELECT model, cost_micro_usd FROM llm_usage ORDER BY id DESC LIMIT 1`).Scan(&model, &cost); err != nil || model != "inhouse-1" || resp.Model == "" {
		t.Fatalf("모델: %q %v", model, err)
	}
	if cost == 0 {
		t.Fatal("공급자 가격이 적용되지 않았다")
	}
	// 규칙에 안 맞는 용도는 기본 경로.
	if _, err := gw.Complete(as(contract.RoleStaff, 1, 10), llm.Request{Purpose: "policy.search", Messages: []llm.Message{{Role: "user", Content: "hi"}}}); err != nil {
		t.Fatal(err)
	}
	if p, _ := e.usageProvider(t); p != "mock" || e.spy.calls != 1 {
		t.Fatalf("규칙 밖 용도: provider=%s spy=%d", p, e.spy.calls)
	}
	// 꺼진 공급자의 규칙은 후보에서 빠져 기본 경로로 간다.
	if _, err := ad.SetActive(ctx, pv.ID, false); err != nil {
		t.Fatal(err)
	}
	if _, err := gw.Complete(as(contract.RoleStaff, 1, 10), llm.Request{Purpose: "docverify.extract", Messages: []llm.Message{{Role: "user", Content: "hi"}}}); err != nil {
		t.Fatal(err)
	}
	if p, _ := e.usageProvider(t); p != "mock" {
		t.Fatalf("꺼진 공급자: provider=%s", p)
	}
}

// 모의 동작 모드: 실패 모드는 분류된 오류로 호출이 실패하고 사용량 error 로 남는다(대체는 2단계).
func TestMockModeFailureIsClassified(t *testing.T) {
	e := newEnv(t)
	e.allow(t, "", 0, "*", 1)
	ad := e.admin(t, false)
	ctx := adminCtx(contract.PermLLMProviderManage, contract.PermLLMPolicyManage)
	pv, _ := ad.SaveProvider(ctx, llmgw.ProviderInput{Code: "mock-down", Kind: "mock", Name: "고장", Mode: "fail_5xx"})
	if _, err := ad.SetActive(ctx, pv.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := ad.SaveRoute(ctx, llmgw.RouteInput{PurposePattern: "*", PrimaryProviderID: pv.ID, Active: true}); err != nil {
		t.Fatal(err)
	}
	gw := e.gwWith(t, false)
	_, err := gw.Complete(as(contract.RoleStaff, 1, 10), llm.Request{Purpose: "x", Messages: []llm.Message{{Role: "user", Content: "hi"}}})
	if err == nil || llmgw.ClassOf(err) != llmgw.ClassHTTP5xx {
		t.Fatalf("5xx 로 분류돼야 한다: %v (%s)", err, llmgw.ClassOf(err))
	}
	if p, st := e.usageProvider(t); p != "mock-down" || st != "error" {
		t.Fatalf("사용량: %s %s", p, st)
	}
	if llmgw.ValidMockMode("nope") || !llmgw.ValidMockMode("flaky:2") || !llmgw.ValidMockMode("slow:100") {
		t.Fatal("모드 검증")
	}
}

// OpenAI 호환: 공급자 설정은 봉인되고(응답·감사에 값 없음), 호출이 채팅 완성 형식으로 나가며 응답·사용량이 파싱된다. 오류는 분류된다.
func TestOpenAICompatEndToEnd(t *testing.T) {
	var gotAuth, gotPath string
	var gotBody map[string]any
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotPath = r.Header.Get("Authorization"), r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		if status != http.StatusOK {
			w.WriteHeader(status)
			return
		}
		_, _ = w.Write([]byte(`{"model":"vllm-x","choices":[{"message":{"content":"안녕"},"finish_reason":"stop"}],"usage":{"prompt_tokens":11,"completion_tokens":7}}`))
	}))
	defer srv.Close()
	e := newEnv(t)
	e.allow(t, "", 0, "*", 1)
	ad := e.admin(t, true) // httptest 는 루프백이라 로컬 시험 허용
	ctx := adminCtx(contract.PermLLMProviderManage, contract.PermLLMPolicyManage)
	const secretKey = "sk-very-secret-123"
	pv, err := ad.SaveProvider(ctx, llmgw.ProviderInput{Code: "vllm-hq", Kind: "openai_compat", Name: "사내 vLLM", DefaultModel: "vllm-x", BaseURL: srv.URL + "/v1", APIKey: secretKey})
	if err != nil {
		t.Fatal(err)
	}
	var sealed string
	if err := e.db.QueryRow(`SELECT secret_sealed FROM llm_providers WHERE id = ?`, pv.ID).Scan(&sealed); err != nil || !strings.HasPrefix(sealed, "enc:v1:") || strings.Contains(sealed, secretKey) {
		t.Fatalf("봉인: %q %v", sealed, err)
	}
	if b, _ := json.Marshal(pv); strings.Contains(string(b), secretKey) || strings.Contains(string(b), srv.URL) || !pv.HasSecret {
		t.Fatalf("응답에 비밀이 있다: %s", b)
	}
	if _, err := ad.SetActive(ctx, pv.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := ad.SaveRoute(ctx, llmgw.RouteInput{PurposePattern: "*", PrimaryProviderID: pv.ID, Active: true}); err != nil {
		t.Fatal(err)
	}
	gw := e.gwWith(t, true)
	resp, err := gw.Complete(as(contract.RoleStaff, 1, 10), llm.Request{System: "sys", Purpose: "x", MaxTokens: 50, Messages: []llm.Message{{Role: "user", Content: "hi"}}})
	if err != nil || resp.Text != "안녕" || resp.Usage.InputTokens != 11 || resp.Usage.OutputTokens != 7 {
		t.Fatalf("응답: %+v %v", resp, err)
	}
	if gotAuth != "Bearer "+secretKey || gotPath != "/v1/chat/completions" || gotBody["model"] != "vllm-x" {
		t.Fatalf("요청: auth=%q path=%q body=%v", gotAuth, gotPath, gotBody)
	}
	for _, rec := range e.rec.Records() {
		if strings.Contains(rec.Detail, secretKey) || strings.Contains(rec.Detail, srv.URL) {
			t.Fatalf("감사에 값이 있다: %s", rec.Detail)
		}
		for _, d := range rec.Diff {
			if strings.Contains(d.New, secretKey) || strings.Contains(d.Old, secretKey) || strings.Contains(d.New, srv.URL) {
				t.Fatalf("감사 diff 에 값이 있다: %+v", d)
			}
		}
	}
	// 오류 분류.
	for code, want := range map[int]llmgw.ErrClass{429: llmgw.ClassRateLimited, 503: llmgw.ClassHTTP5xx, 401: llmgw.ClassAuth, 400: llmgw.ClassBadRequest} {
		status = code
		_, err := gw.Complete(as(contract.RoleStaff, 1, 10), llm.Request{Purpose: "x", Messages: []llm.Message{{Role: "user", Content: "hi"}}})
		if llmgw.ClassOf(err) != want {
			t.Errorf("HTTP %d: 기대 %s, 실제 %s (%v)", code, want, llmgw.ClassOf(err), err)
		}
		if err != nil && (strings.Contains(err.Error(), secretKey) || strings.Contains(err.Error(), srv.URL)) {
			t.Errorf("오류 문장에 값이 있다: %v", err)
		}
	}
	// 시험 연결.
	status = http.StatusOK
	if res, err := ad.Test(ctx, pv.ID); err != nil || !res.OK || res.Code != "ok" {
		t.Fatalf("시험 연결: %+v %v", res, err)
	}
}

// 등록 규칙: 사설·루프백·http·사용자 정보 주소 거부, 권한·낙관적 잠금·종류·코드 검증, 리허설 거부.
func TestProviderAdminRules(t *testing.T) {
	e := newEnv(t)
	ad := e.admin(t, false)
	ctx := adminCtx(contract.PermLLMProviderManage, contract.PermLLMPolicyManage)
	for _, bad := range []string{"https://127.0.0.1/v1", "https://10.0.0.5/v1", "https://169.254.169.254/v1", "http://llm.example.com/v1", "https://u:p@llm.example.com/v1", "https://localhost/v1", "https://svc.internal/v1"} {
		_, err := ad.SaveProvider(ctx, llmgw.ProviderInput{Code: "bad-one", Kind: "openai_compat", Name: "x", BaseURL: bad, APIKey: "k"})
		if !errors.Is(err, contract.ErrValidation) {
			t.Errorf("%s: 거부돼야 한다: %v", bad, err)
		}
	}
	if n := e.count(t, `SELECT COUNT(*) FROM llm_providers`); n != 0 {
		t.Fatalf("거부된 저장이 행을 만들었다: %d", n)
	}
	if _, err := ad.SaveProvider(ctx, llmgw.ProviderInput{Code: "x", Kind: "mock", Name: "x"}); !errors.Is(err, contract.ErrValidation) {
		t.Errorf("짧은 코드: %v", err)
	}
	if _, err := ad.SaveProvider(ctx, llmgw.ProviderInput{Code: "br-1", Kind: "bedrock", Name: "x", APIKey: "k"}); !errors.Is(err, contract.ErrValidation) {
		t.Errorf("리전·키 없는 bedrock: %v", err)
	}
	ok, err := ad.SaveProvider(ctx, llmgw.ProviderInput{Code: "pub-llm", Kind: "openai_compat", Name: "공개", BaseURL: "https://llm.example.com/v1", APIKey: "k1", DefaultModel: "m"})
	if err != nil || ok.Hint != "llm.example.com" || ok.Active {
		t.Fatalf("공개 https 등록: %+v %v", ok, err)
	}
	// 낙관적 잠금과 비밀 칸 유지.
	up, err := ad.SaveProvider(ctx, llmgw.ProviderInput{ID: ok.ID, Code: "pub-llm", Kind: "openai_compat", Name: "공개2", Version: ok.Version})
	if err != nil || up.Name != "공개2" || !up.HasSecret || up.Version != ok.Version+1 {
		t.Fatalf("수정(비밀 칸 비움 = 유지): %+v %v", up, err)
	}
	if _, err := ad.SaveProvider(ctx, llmgw.ProviderInput{ID: ok.ID, Code: "pub-llm", Kind: "openai_compat", Name: "충돌", Version: ok.Version}); !errors.Is(err, contract.ErrInvalidState) {
		t.Errorf("오래된 버전: %v", err)
	}
	// 권한 없음.
	if _, err := ad.List(adminCtx()); !errors.Is(err, contract.ErrForbidden) {
		t.Errorf("권한 없는 목록: %v", err)
	}
	if _, err := ad.SaveProvider(adminCtx(contract.PermLLMPolicyManage), llmgw.ProviderInput{Code: "no-perm", Kind: "mock", Name: "x"}); !errors.Is(err, contract.ErrForbidden) {
		t.Errorf("provider_manage 없는 저장: %v", err)
	}
	// 리허설: 외부 공급자 등록·켜기 거부.
	re := *ad
	re.Rehearsal = true
	if _, err := re.SaveProvider(ctx, llmgw.ProviderInput{Code: "re-llm", Kind: "openai_compat", Name: "x", BaseURL: "https://llm.example.com/v1", APIKey: "k"}); !errors.Is(err, contract.ErrInvalidState) {
		t.Errorf("리허설 등록: %v", err)
	}
	if _, err := re.SetActive(ctx, ok.ID, true); !errors.Is(err, contract.ErrInvalidState) {
		t.Errorf("리허설 켜기: %v", err)
	}
	// 키 없음(봉인기 없음)이면 외부 공급자는 등록 불가.
	nk := *ad
	nk.Sealer = nil
	if _, err := nk.SaveProvider(ctx, llmgw.ProviderInput{Code: "nk-llm", Kind: "openai_compat", Name: "x", BaseURL: "https://llm.example.com/v1", APIKey: "k"}); !errors.Is(err, contract.ErrInvalidState) {
		t.Errorf("봉인 키 없음: %v", err)
	}
	// 규칙: 대체는 다른 공급자, 용도 패턴 검증, 시뮬레이터.
	a2, _ := ad.SaveProvider(ctx, llmgw.ProviderInput{Code: "mock-two", Kind: "mock", Name: "둘"})
	if _, err := ad.SaveRoute(ctx, llmgw.RouteInput{PurposePattern: "x.*", PrimaryProviderID: a2.ID, FallbackProviderID: a2.ID, Active: true}); !errors.Is(err, contract.ErrValidation) {
		t.Errorf("주·대체 같음: %v", err)
	}
	if _, err := ad.SaveRoute(ctx, llmgw.RouteInput{PurposePattern: "Bad Pattern", PrimaryProviderID: a2.ID, Active: true}); !errors.Is(err, contract.ErrValidation) {
		t.Errorf("용도 패턴: %v", err)
	}
	if _, err := ad.SetActive(ctx, a2.ID, true); err != nil {
		t.Fatal(err)
	}
	rt, err := ad.SaveRoute(ctx, llmgw.RouteInput{PurposePattern: "docverify.*", PrimaryProviderID: a2.ID, FallbackProviderID: ok.ID, Active: true})
	if err != nil || rt.PrimaryCode != "mock-two" || rt.FallbackCode != "pub-llm" {
		t.Fatalf("규칙 저장: %+v %v", rt, err)
	}
	sim, err := ad.Simulate(ctx, llmgw.SimInput{Purpose: "docverify.extract"})
	if err != nil || !sim.Matched || sim.PrimaryCode != "mock-two" || sim.FallbackCode != "pub-llm" {
		t.Fatalf("시뮬레이터: %+v %v", sim, err)
	}
	if sim, _ := ad.Simulate(ctx, llmgw.SimInput{Purpose: "policy.search"}); !sim.DefaultPath {
		t.Fatalf("규칙 없는 용도는 기본 경로: %+v", sim)
	}
	if err := ad.DeleteRoute(ctx, rt.ID); err != nil {
		t.Fatal(err)
	}
	// 감사: 동작이 남고 값이 없다.
	seen := map[string]bool{}
	for _, rec := range e.rec.Records() {
		seen[rec.Action] = true
	}
	for _, a := range []string{"llm.provider_save", "llm.provider_activate", "llm.route_save", "llm.route_delete"} {
		if !seen[a] {
			t.Errorf("감사 %s 가 없다", a)
		}
	}
}

func (e *env) count(t *testing.T, q string) int {
	t.Helper()
	var n int
	if err := e.db.QueryRow(q).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
