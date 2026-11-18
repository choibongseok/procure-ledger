package domain

import (
	"context"
	"time"
)

// 회사 설정(B34,). 설정은 값 하나를 한 곳에서 읽고, 설정이 비어 있을 때의 동작은 현행과 같다.

// 이미지 종류.
const (
	CompanyAssetLogo = "logo"
	CompanyAssetSeal = "seal"
)

// CompanyInfo 는 회사 정보다. Configured 가 거짓이면 한 번도 저장하지 않은 상태이고 Name 은 비어 있다(출력은 조직도 최상위 조직 이름을 쓴다).
type CompanyInfo struct {
	Name           string
	BusinessNo     string
	Representative string
	Address        string
	Phone          string
	HasLogo        bool
	HasSeal        bool
	Configured     bool
	Version        int64
	UpdatedAt      time.Time
}

// CompanyInput 은 회사 정보 저장 입력이다. 회사명은 필수다.
type CompanyInput struct {
	Name           string
	BusinessNo     string
	Representative string
	Address        string
	Phone          string
}

// CompanyAsset 은 로고·직인 이미지 내용이다(PNG·JPEG).
type CompanyAsset struct {
	Kind        string
	ContentType string
	Data        []byte
	Size        int
	Width       int
	Height      int
	SHA256      string
}

// CompanyService 는 회사 설정 서비스다. 조회는 company:read(로그인한 모든 역할), 변경은 company:manage(admin)다.
type CompanyService interface {
	Info(ctx context.Context) (CompanyInfo, error)
	Update(ctx context.Context, in CompanyInput, version int64) (CompanyInfo, error)
	SetAsset(ctx context.Context, kind string, data []byte) (CompanyInfo, error)
	Asset(ctx context.Context, kind string) (CompanyAsset, error)

	// 회계 설정(B34 단계 2): 회계연도 시작월, 부가세율 효력일 이력.
	Fiscal(ctx context.Context) (FiscalInfo, error)
	SetFiscalStart(ctx context.Context, month int, version int64) (CompanyInfo, error)
	VATRates(ctx context.Context) ([]VATRate, error)
	AddVATRate(ctx context.Context, effectiveFrom string, percent int) (VATRate, error)
	CancelVATRate(ctx context.Context, id int64) error

	// 영업일 달력(B34 단계 3): 공휴일. 토·일은 자동 휴일이다.
	Holidays(ctx context.Context, year int) ([]Holiday, error)
	AddHolidays(ctx context.Context, items []Holiday) (int, error)
	RemoveHoliday(ctx context.Context, date string) error
}

// FiscalInfo 는 회계연도 설정과 이번·지난 회계연도 범위다. 회계연도 이름은 끝나는 해다(시작월 4 의 2026-04~2027-03 은 2027).
type FiscalInfo struct {
	StartMonth int
	Name       int
	ThisFrom   string
	ThisTo     string
	LastFrom   string
	LastTo     string
	Closed     bool // 마감된 월이 있어 시작월을 바꿀 수 없다
	Version    int64
	Configured bool
}

// VATRate 는 부가세율 이력 한 줄이다. 거래일에 유효한(취소되지 않은 가장 최근 효력일의) 세율을 쓴다.
type VATRate struct {
	ID            int64
	EffectiveFrom string
	Percent       int
	CreatedBy     string
	CreatedAt     time.Time
	Canceled      bool
	Pending       bool // 아직 효력이 시작되지 않았다(취소할 수 있다)
}

// Holiday 는 등록된 공휴일이다.
type Holiday struct {
	Date      string
	Name      string
	CreatedBy string
}

// VATFromSupply 는 공급가액 기준 부가세다(원 미만 버림). ratePercent 가 10 이면 공급가액 ÷ 10 과 같다.
func VATFromSupply(supply int64, ratePercent int) int64 {
	if supply <= 0 || ratePercent <= 0 {
		return 0
	}
	return supply * int64(ratePercent) / 100
}

// VATFromTotal 은 부가세 포함 합계에서 역산한 부가세다(원 미만 버림). ratePercent 가 10 이면 합계 ÷ 11 과 같다.
func VATFromTotal(total int64, ratePercent int) int64 {
	if total <= 0 || ratePercent <= 0 {
		return 0
	}
	return total * int64(ratePercent) / int64(100+ratePercent)
}
