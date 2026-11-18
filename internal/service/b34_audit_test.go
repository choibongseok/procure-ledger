package service_test

import (
	"testing"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
)

// AC-1402: 회사 설정의 모든 쓰기는 감사 기록과 같은 트랜잭션이다. 기록이 실패하면 요청이 실패하고 아무것도 바뀌지 않는다.
func TestCompanySettingsAuditFailureRollsBack(t *testing.T) {
	e := newEnv(t) // 시계 2026-11-10(KST)
	ci, err := e.company.Update(as(uAdmin), domain.CompanyInput{Name: "에이엑스", Phone: "02-1"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.company.SetAsset(as(uAdmin), domain.CompanyAssetSeal, testPNG(t, 20, 20)); err != nil {
		t.Fatal(err)
	}
	if _, err := e.company.AddVATRate(as(uAdmin), "2027-01-01", 11); err != nil {
		t.Fatal(err)
	}
	var rateID int64
	if err := e.db.QueryRow(`SELECT id FROM vat_rates WHERE effective_from='2027-01-01'`).Scan(&rateID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.company.AddHolidays(as(uAdmin), []domain.Holiday{{Date: "2026-12-31", Name: "연말"}}); err != nil {
		t.Fatal(err)
	}
	cnt := func(q string) int { return count(t, e.db, q) }
	before := map[string]int{
		"settings": cnt(`SELECT COUNT(*) FROM company_settings WHERE phone='02-1' AND fiscal_start_month=1 AND version=1`),
		"assets":   cnt(`SELECT COUNT(*) FROM company_assets`),
		"vat":      cnt(`SELECT COUNT(*) FROM vat_rates`),
		"vatLive":  cnt(`SELECT COUNT(*) FROM vat_rates WHERE canceled_at IS NOT NULL`),
		"holidays": cnt(`SELECT COUNT(*) FROM holidays`),
	}
	e.rec.FailWith(errBoom)
	defer e.rec.FailWith(nil)
	steps := map[string]error{}
	_, steps["update"] = e.company.Update(as(uAdmin), domain.CompanyInput{Name: "에이엑스", Phone: "02-2"}, ci.Version)
	_, steps["logo"] = e.company.SetAsset(as(uAdmin), domain.CompanyAssetLogo, testPNG(t, 30, 30))
	_, steps["seal"] = e.company.SetAsset(as(uAdmin), domain.CompanyAssetSeal, testPNG(t, 31, 31))
	_, steps["fiscal"] = e.company.SetFiscalStart(as(uAdmin), 4, ci.Version)
	_, steps["vat_add"] = e.company.AddVATRate(as(uAdmin), "2027-06-01", 12)
	steps["vat_cancel"] = e.company.CancelVATRate(as(uAdmin), rateID)
	_, steps["holiday_add"] = e.company.AddHolidays(as(uAdmin), []domain.Holiday{{Date: "2026-12-25", Name: "성탄절"}})
	steps["holiday_remove"] = e.company.RemoveHoliday(as(uAdmin), "2026-12-31")
	for name, err := range steps {
		if !isErr(err, contract.ErrAuditFailed) {
			t.Errorf("%s: 감사 실패는 요청 실패여야 함: %v", name, err)
		}
	}
	after := map[string]int{
		"settings": cnt(`SELECT COUNT(*) FROM company_settings WHERE phone='02-1' AND fiscal_start_month=1 AND version=1`),
		"assets":   cnt(`SELECT COUNT(*) FROM company_assets`),
		"vat":      cnt(`SELECT COUNT(*) FROM vat_rates`),
		"vatLive":  cnt(`SELECT COUNT(*) FROM vat_rates WHERE canceled_at IS NOT NULL`),
		"holidays": cnt(`SELECT COUNT(*) FROM holidays`),
	}
	for k, v := range before {
		if after[k] != v {
			t.Errorf("%s: 감사 실패인데 데이터가 바뀜 %d → %d", k, v, after[k])
		}
	}
}

// 상태 때문에 거부된 변경(마감된 월이 있는 시작월 변경, E3)과 서비스 안에서 거부된 조회도 거부 감사로 남는다.
func TestCompanyDomainAndReadDenialsAreAudited(t *testing.T) {
	e := newEnv(t)
	e.closeFixture(t)
	c, err := e.company.Update(as(uAdmin), domain.CompanyInput{Name: "에이엑스"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.monthClose.Close(as(uFinance), "2026-10"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.company.SetFiscalStart(as(uAdmin), 4, c.Version); !isErr(err, contract.ErrInvalidState) {
		t.Fatalf("마감된 월이 있으면 409 로 거부: %v", err)
	}
	if !e.rec.Has("company.fiscal_set", audit.TargetSetting, audit.ResultDenied) {
		t.Error("시작월 변경 거부 감사가 없음")
	}
	if _, err := e.company.Fiscal(as(uStaff)); !isErr(err, contract.ErrForbidden) {
		t.Fatalf("직원의 회계연도 조회: %v", err)
	}
	if !e.rec.Has("company.fiscal_read", audit.TargetSetting, audit.ResultDenied) {
		t.Error("조회 거부 감사가 없음")
	}
	if _, err := e.company.VATRates(as(uStaff)); !isErr(err, contract.ErrForbidden) || !e.rec.Has("company.vat_rate_read", audit.TargetSetting, audit.ResultDenied) {
		t.Errorf("세율 조회 거부: %v", err)
	}
	if _, err := e.company.Holidays(as(uStaff), 2026); !isErr(err, contract.ErrForbidden) || !e.rec.Has("company.holiday_read", audit.TargetSetting, audit.ResultDenied) {
		t.Errorf("공휴일 조회 거부: %v", err)
	}
}
