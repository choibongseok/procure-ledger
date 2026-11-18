package bootstrap

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"github.com/choibongseok/procure-ledger/internal/acctsecret"
	"github.com/choibongseok/procure-ledger/internal/aicost"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/evalset"
	"github.com/choibongseok/procure-ledger/internal/mcptool"
	"io/fs"
	"log"
	"os"
	"strconv"

	"github.com/choibongseok/procure-ledger/docs/policies"
	"github.com/choibongseok/procure-ledger/internal/config"
	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/deps"
	"github.com/choibongseok/procure-ledger/internal/docverify"
	"github.com/choibongseok/procure-ledger/internal/knowledge"
	"github.com/choibongseok/procure-ledger/internal/llmgw"
	"github.com/choibongseok/procure-ledger/internal/mcpusage"
	"github.com/choibongseok/procure-ledger/internal/policysearch"
)

func envInt64(key string) int64 {
	n, _ := strconv.ParseInt(os.Getenv(key), 10, 64)
	return n
}

// WireAI 는 ai 서비스를 만들어 d.LLM(게이트웨이)·d.Documents 에 채운다.
// 모델 ID 는 cfg.LLM.DefaultModel(PL_LLM_MODEL)에서만 온다. 공급자는 PL_LLM_PROVIDER(mock 기본, anthropic 수동).
// 가격표는 PL_LLM_PRICE_IN_MICRO_PER_MTOK·PL_LLM_PRICE_OUT_MICRO_PER_MTOK(1백만 토큰당 1e-6 USD)이고 비면 비용 0.
// 플랫폼(감사·마스킹·트랜잭션)과 업무(재고) 조립이 먼저 끝나 있어야 한다. 빠졌으면 조용히 넘어가지 않고 기동을 실패시킨다.
func WireAI(cfg config.Config, d *deps.Deps) error {
	switch {
	case d.DB == nil || d.Txs == nil:
		return errors.New("wireAI: DB·트랜잭션 관리자가 없습니다")
	case d.Audit == nil || d.Masker == nil:
		return errors.New("wireAI: 감사 기록기·마스킹이 없습니다(wirePlatform 이 먼저 채워야 합니다)")
	case d.Privacy == nil:
		return errors.New("wireAI: 개인정보 조회 기록기가 없습니다(wirePlatform 이 먼저 채워야 합니다: 원문 경로는 기록 없이 열 수 없습니다)")
	case d.Inventory == nil:
		return errors.New("wireAI: 재고 서비스가 없습니다(wireERP 가 먼저 채워야 합니다)")
	}

	var prov llmgw.Provider
	model := cfg.LLM.DefaultModel
	policyPattern := model
	switch cfg.LLM.Provider {
	case "", "mock":
		prov = llmgw.MockProvider{}
		if model == "" {
			model = "mock-1"
		}
		policyPattern = "mock*"
	case "anthropic":
		p, err := llmgw.NewAnthropic(cfg.LLM.APIKeyEnv, "", cfg.LLM.Timeout)
		if err != nil {
			return fmt.Errorf("wireAI: %w", err)
		}
		prov = p
	default:
		return fmt.Errorf("wireAI: 알 수 없는 LLM 공급자 %q (mock|anthropic)", cfg.LLM.Provider)
	}

	// B45: 공급자 설정(봉인)을 풀 키. 없으면 mock 외 공급자를 등록·사용할 수 없고 기본 경로(환경변수)만 쓴다.
	var sealer llmgw.Sealer
	if key, kerr := acctsecret.DecodeKey(cfg.AccountSecretKey); kerr == nil && key != nil {
		if c, cerr := acctsecret.New(key); cerr == nil {
			sealer = c
		}
	}
	breakers := llmgw.NewBreakerSet(nil)
	d.LLMProviders = &llmgw.ProviderAdmin{DB: d.DB, Txs: d.Txs, Audit: d.Audit, Sealer: sealer, AllowLocal: cfg.NotifyInsecureLocal, Rehearsal: cfg.Rehearsal, Breakers: breakers}
	gw, err := llmgw.New(llmgw.Options{
		Sealer: sealer, AllowLocal: cfg.NotifyInsecureLocal, Rehearsal: cfg.Rehearsal, Breakers: breakers,
		Provider: prov, DefaultModel: model, DB: d.DB, Txs: d.Txs, Audit: d.Audit, Masker: d.Masker, Timeout: cfg.LLM.Timeout,
		Prices: llmgw.Prices{InPerMTok: envInt64("PL_LLM_PRICE_IN_MICRO_PER_MTOK"), OutPerMTok: envInt64("PL_LLM_PRICE_OUT_MICRO_PER_MTOK")},
	})
	if err != nil {
		return fmt.Errorf("wireAI: %w", err)
	}
	if err := llmgw.EnsureDefaultPolicy(context.Background(), d.DB, policyPattern); err != nil {
		return fmt.Errorf("wireAI: 기본 모델 정책: %w", err)
	}
	d.LLM = gw
	if d.Knowledge != nil {
		d.KnowledgeAnswer = &knowledge.Answerer{Search: d.Knowledge, LLM: gw, Txs: d.Txs, Audit: d.Audit, Masker: d.Masker}
	}
	es, err := evalset.New(evalset.Options{DB: d.DB, Txs: d.Txs, Audit: d.Audit, Masker: d.Masker, LLM: gw, Force: llmgw.WithForcedProvider, Rehearsal: cfg.Rehearsal})
	if err != nil {
		return fmt.Errorf("wireAI: 평가 세트: %w", err)
	}
	if err := es.Recover(context.Background()); err != nil {
		return fmt.Errorf("wireAI: 평가 실행 정리: %w", err)
	}
	d.EvalSets = es
	mt, err := mcptool.New(mcptool.Options{DB: d.DB, Txs: d.Txs, Audit: d.Audit, Catalog: mcptool.BuiltinCatalog(*d), Masker: d.Masker, Eval: es})
	if err != nil {
		return fmt.Errorf("wireAI: 사용자 정의 도구: %w", err)
	}
	d.MCPTools = mt
	var acJournals domain.AICostJournalHook
	if h, ok := d.Journals.(domain.AICostJournalHook); ok {
		acJournals = h // WireERP 가 먼저 채운다(없으면 배부 확정을 할 수 없다)
	}
	ac, err := aicost.New(aicost.Options{DB: d.DB, Txs: d.Txs, Audit: d.Audit, Journals: acJournals, SoD: d.SoD})
	if err != nil {
		return fmt.Errorf("wireAI: AI 비용: %w", err)
	}
	d.AICost = ac
	d.AICostTool = ac
	d.LLMProviders.Gate = es // 라우팅 저장 전 평가(B46 3단계)
	d.Documents = &docverify.Service{
		DB: d.DB, Txs: d.Txs, Audit: d.Audit, LLM: gw, Inventory: d.Inventory,
		Vendors: docverify.SQLVendors{DB: d.DB}, Masker: d.Masker, Privacy: d.Privacy,
		JournalHook: d.JournalHook, // WireERP 가 먼저 채운다(nil 이면 전표를 만들지 않는다)
	}
	pol, err := newPolicyService(d)
	if err != nil {
		return fmt.Errorf("wireAI: 사내 규정 색인: %w", err)
	}
	d.Policy = pol
	// 개인 토큰별 분당 MCP 호출 한도(B13). 0 이면 한도 없음이라 nil 로 둔다(타입 있는 nil 이 인터페이스에 들어가지 않게).
	if cfg.MCPRatePerUserPerMin < 0 {
		return fmt.Errorf("wireAI: PL_MCP_RATE_PER_USER_PER_MIN 은 0 이상의 정수여야 합니다(0 은 사용자 합산 한도 없음)")
	}
	if l := mcpusage.NewRateLimiter(d.DB, cfg.MCPRatePerMin, cfg.MCPRatePerUserPerMin); l != nil {
		l.Audit = d.Audit
		d.MCPRate = l
	}
	return nil
}

// newPolicyService 는 규정 검색(B12)을 만든다. 규정 문서는 기본으로 바이너리에 내장된 docs/policies 이고,
// PL_POLICY_DIR 이 있으면 그 디스크 폴더를 읽으며 내용이 바뀌면 다시 색인한다.
func newPolicyService(d *deps.Deps) (*policysearch.Service, error) {
	svc := &policysearch.Service{
		Txs: d.Txs, Audit: d.Audit, Masker: d.Masker,
		Source: func() fs.FS { return policies.FS },
		Depts: func(ctx context.Context, id int64) (string, error) {
			var name string
			err := d.DB.QueryRowContext(ctx, `SELECT name FROM departments WHERE id = ?`, id).Scan(&name)
			if errors.Is(err, sql.ErrNoRows) {
				return "", contract.ErrNotFound
			}
			return name, err
		},
	}
	if dir := os.Getenv("PL_POLICY_DIR"); dir != "" {
		svc.Source = func() fs.FS { return os.DirFS(dir) }
		svc.Reloadable = true
	}
	if err := svc.Init(); err != nil {
		return nil, err
	}
	for _, p := range svc.Problems() {
		log.Printf("wireAI: 규정 문서 제외: %s", p)
	}
	return svc, nil
}
