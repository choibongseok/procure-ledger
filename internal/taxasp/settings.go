package taxasp

import (
	"context"
	"strings"

	"github.com/choibongseok/procure-ledger/internal/contract"
)

// Static 은 고정 설정이다(시험·조립용).
type Static struct {
	Cfg Config
	On  bool
}

var _ SettingsSource = Static{}

func (s Static) Active(context.Context) (Config, bool, error) { return s.Cfg, s.On, nil }

// WithDemoSeed 는 설정 원천을 감싸, 모의 대행사일 때 app_settings 의 taxasp.mock_seed=demo 이면 시연용 수신 건을 채우도록 Config 에 표시한다(B43 2단계).
// 값은 회사 사업자번호(공급받는자)뿐이고 비밀이 아니다. 실제 대행사 설정에는 아무 영향이 없다.
type WithDemoSeed struct {
	Inner SettingsSource
	DB    contract.Tx
}

var _ SettingsSource = WithDemoSeed{}

func (w WithDemoSeed) Active(ctx context.Context) (Config, bool, error) {
	cfg, ok, err := w.Inner.Active(ctx)
	if err != nil || !ok || cfg.Provider != "mock" {
		return cfg, ok, err
	}
	var seed string
	if err := w.DB.QueryRowContext(ctx, `SELECT value FROM app_settings WHERE key = 'taxasp.mock_seed'`).Scan(&seed); err != nil || strings.TrimSpace(seed) != "demo" {
		return cfg, ok, nil
	}
	cfg.MockSeed = "demo"
	var biz string
	_ = w.DB.QueryRowContext(ctx, `SELECT business_no FROM company_settings WHERE id = 1`).Scan(&biz)
	cfg.MockBuyer = ""
	for _, r := range biz {
		if r >= '0' && r <= '9' {
			cfg.MockBuyer += string(r)
		}
	}
	return cfg, ok, nil
}
