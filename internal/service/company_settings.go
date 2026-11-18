package service

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/repository"
)

// 회사 설정의 회계·달력 부분(B34 단계 2·3,).
// 설정 읽기 함수(vatRateOn·fiscalStartMonth·isBusinessDay·nextBusinessDay)는 다른 서비스가 설정을 얻는 유일한 길이다. 설정이 비어 있으면 현행과 같다.

// ---- 설정 읽기 함수(다른 서비스가 호출) ----

// vatRateOn 은 그 날(거래일)에 유효한 부가세율(정수 %)이다.
func (b Base) vatRateOn(ctx context.Context, q contract.Tx, day string) (int, error) {
	return repository.SQLCompany{}.VATRateOn(ctx, q, day)
}

// fiscalStartMonth 는 회계연도 시작월이다(설정이 없으면 1).
func (b Base) fiscalStartMonth(ctx context.Context, q contract.Tx) (int, error) {
	m, _, _, err := repository.SQLCompany{}.FiscalStart(ctx, q)
	return m, err
}

func parseDay(day string) time.Time {
	t, _ := time.Parse("2006-01-02", day)
	return t
}

// fiscalRange 는 그 날이 속한 회계연도의 시작일·종료일이다(회계연도 이름은 끝나는 해). 시작월이 1 이면 역년이다.
func fiscalRange(day string, start int) (from, to string, name int) {
	t := parseDay(day)
	y := t.Year()
	if int(t.Month()) < start {
		y--
	}
	f := time.Date(y, time.Month(start), 1, 0, 0, 0, 0, time.UTC)
	e := f.AddDate(1, 0, -1)
	return f.Format("2006-01-02"), e.Format("2006-01-02"), e.Year()
}

// isBusinessDay 는 영업일 판정의 유일한 정의다: 토·일과 등록된 공휴일이 아닌 날(KST 날짜).
func (b Base) isBusinessDay(ctx context.Context, q contract.Tx, day string) (bool, error) {
	if wd := parseDay(day).Weekday(); wd == time.Saturday || wd == time.Sunday {
		return false, nil
	}
	h, err := repository.SQLCompany{}.IsHoliday(ctx, q, day)
	return !h, err
}

// nextBusinessDay 는 그 날 이후(그 날 포함) 첫 영업일이다. 공휴일을 끝없이 등록해도 멈추도록 60일까지만 찾는다.
func (b Base) nextBusinessDay(ctx context.Context, q contract.Tx, day string) (string, error) {
	d := parseDay(day)
	for i := 0; i < 60; i++ {
		s := d.AddDate(0, 0, i).Format("2006-01-02")
		ok, err := b.isBusinessDay(ctx, q, s)
		if err != nil {
			return "", err
		}
		if ok {
			return s, nil
		}
	}
	return day, nil
}

// ---- 서비스 ----

// readAccounting 은 회계 값(회계연도·세율·공휴일) 조회 권한이다: 회사 설정 관리자, 재무·감사(원장 조회 권한).
// 권한이 없으면 거부를 감사에 남긴다(action: 읽기 동작 이름). 라우트는 모든 로그인 사용자에게 열려 있어 이 거부는 서비스 안에서만 생긴다.
func (s *CompanyService) readAccounting(ctx context.Context, action string) error {
	p, ok := contract.PrincipalFrom(ctx)
	if !ok {
		return contract.ErrUnauthenticated
	}
	if p.Can(contract.PermCompanyManage) || p.Can(contract.PermLedgerRead) {
		return nil
	}
	_, err := s.authorize(ctx, contract.PermCompanyManage, action, audit.TargetSetting)
	return err
}

func (s *CompanyService) Fiscal(ctx context.Context) (domain.FiscalInfo, error) {
	if err := s.readAccounting(ctx, "company.fiscal_read"); err != nil {
		return domain.FiscalInfo{}, err
	}
	m, ver, configured, err := s.Repo.FiscalStart(ctx, s.DB)
	if err != nil {
		return domain.FiscalInfo{}, err
	}
	closed, err := s.Repo.ClosedMonthExists(ctx, s.DB)
	if err != nil {
		return domain.FiscalInfo{}, err
	}
	today := s.kstDay()
	f, t, name := fiscalRange(today, m)
	lf, lt, _ := fiscalRange(parseDay(f).AddDate(0, 0, -1).Format("2006-01-02"), m)
	return domain.FiscalInfo{StartMonth: m, Name: name, ThisFrom: f, ThisTo: t, LastFrom: lf, LastTo: lt, Closed: closed, Version: ver, Configured: configured}, nil
}

// errFiscalClosed 는 마감된 월이 있어 시작월을 바꿀 수 없다는 거부다. 상태에 따른 충돌이라 409(ErrInvalidState)이고 거부 감사가 남는다.
var errFiscalClosed = fmt.Errorf("%w: 마감된 월이 있어 회계연도 시작월을 바꿀 수 없습니다. 마감을 해제한 뒤 바꾸거나 지금 값을 유지하세요.", contract.ErrInvalidState)

// SetFiscalStart 는 회계연도 시작월을 바꾼다. 마감된 월이 하나라도 있으면 거부한다(지난 보고서가 달라지지 않게).
func (s *CompanyService) SetFiscalStart(ctx context.Context, month int, version int64) (domain.CompanyInfo, error) {
	p, err := s.authorize(ctx, contract.PermCompanyManage, "company.fiscal_set", audit.TargetSetting)
	if err != nil {
		return domain.CompanyInfo{}, err
	}
	if month < 1 || month > 12 {
		return domain.CompanyInfo{}, validation("회계연도 시작월은 1월부터 12월 사이여야 합니다.")
	}
	if err := requireDefaultEntityForCompanyEdit(ctx); err != nil {
		return domain.CompanyInfo{}, err
	}
	var out domain.CompanyInfo
	err = s.write(ctx, p, "company.fiscal_set", audit.TargetSetting, 0, func(tx contract.Tx) error {
		cur, _, configured, err := s.Repo.FiscalStart(ctx, tx)
		if err != nil {
			return err
		}
		if !configured {
			return validation("회사 정보를 먼저 저장한 뒤 회계연도 시작월을 정하세요.")
		}
		if cur == month { // 바뀐 것이 없으면 쓰지 않는다(version·감사 생략)
			out, err = s.Repo.Get(ctx, tx)
			return err
		}
		if closed, err := s.Repo.ClosedMonthExists(ctx, tx); err != nil {
			return err
		} else if closed && cur != month {
			return errFiscalClosed
		}
		ok, err := s.Repo.SetFiscalStart(ctx, tx, month, version, p.UserID, s.now())
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("%w: 다른 관리자가 먼저 바꿨습니다. 새로 고친 뒤 다시 저장하세요.", contract.ErrInvalidState)
		}
		if out, err = s.Repo.Get(ctx, tx); err != nil {
			return err
		}
		return s.record(ctx, tx, p, "company.fiscal_set", audit.TargetSetting, 0, audit.DiffItem{Key: "fiscal_start_month", Old: strconv.Itoa(cur), New: strconv.Itoa(month)})
	})
	if errors.Is(err, errFiscalClosed) { // 도메인 규칙 거부(E3)도 거부 감사를 남긴다(롤백된 트랜잭션 밖에서)
		s.recordDenied(ctx, p, "company.fiscal_set", audit.TargetSetting, 0, "마감된 월이 있어 시작월 변경 거부")
	}
	return out, err
}

func (s *CompanyService) VATRates(ctx context.Context) ([]domain.VATRate, error) {
	if err := s.readAccounting(ctx, "company.vat_rate_read"); err != nil {
		return nil, err
	}
	rows, err := s.Repo.VATRates(ctx, s.DB)
	if err != nil {
		return nil, err
	}
	today := s.kstDay()
	for i := range rows {
		rows[i].Pending = !rows[i].Canceled && rows[i].EffectiveFrom > today
	}
	return rows, nil
}

// AddVATRate 는 효력일부터 적용할 세율을 등록한다. 효력일은 오늘 이후이고 마감된 달이 아니어야 하며 수정·삭제는 없다.
func (s *CompanyService) AddVATRate(ctx context.Context, effectiveFrom string, percent int) (domain.VATRate, error) {
	p, err := s.authorize(ctx, contract.PermCompanyManage, "company.vat_rate_add", audit.TargetSetting)
	if err != nil {
		return domain.VATRate{}, err
	}
	if !validDate(effectiveFrom) {
		return domain.VATRate{}, validation("효력일은 2026-12-25 와 같은 형식으로 입력하세요.")
	}
	if percent < 0 || percent > 100 {
		return domain.VATRate{}, validation("세율은 0에서 100 사이의 정수(%%)여야 합니다.")
	}
	var out domain.VATRate
	err = s.write(ctx, p, "company.vat_rate_add", audit.TargetSetting, 0, func(tx contract.Tx) error {
		if effectiveFrom < s.kstDay() {
			return validation("효력일은 오늘 이후여야 합니다.")
		}
		if closed, err := s.Repo.MonthClosed(ctx, tx, effectiveFrom[:7]); err != nil {
			return err
		} else if closed {
			return validation("효력일은 오늘 이후이고 마감되지 않은 달이어야 합니다.")
		}
		if dup, err := s.Repo.VATRateDuplicate(ctx, tx, effectiveFrom); err != nil {
			return err
		} else if dup {
			return validation("같은 효력일의 세율이 이미 있습니다.")
		}
		id, err := s.Repo.InsertVATRate(ctx, tx, effectiveFrom, percent, p.UserID, s.now())
		if err != nil {
			return err
		}
		out = domain.VATRate{ID: id, EffectiveFrom: effectiveFrom, Percent: percent, Pending: effectiveFrom > s.kstDay()}
		return s.record(ctx, tx, p, "company.vat_rate_add", audit.TargetSetting, id,
			audit.DiffItem{Key: "vat_effective_from", New: effectiveFrom}, audit.DiffItem{Key: "vat_rate", New: strconv.Itoa(percent)})
	})
	return out, err
}

// CancelVATRate 는 아직 효력이 시작되지 않은 세율을 취소한다.
func (s *CompanyService) CancelVATRate(ctx context.Context, id int64) error {
	p, err := s.authorize(ctx, contract.PermCompanyManage, "company.vat_rate_cancel", audit.TargetSetting)
	if err != nil {
		return err
	}
	return s.write(ctx, p, "company.vat_rate_cancel", audit.TargetSetting, id, func(tx contract.Tx) error {
		r, err := s.Repo.VATRate(ctx, tx, id)
		if err != nil {
			return err
		}
		if r.Canceled {
			return fmt.Errorf("%w: 이미 취소된 세율입니다.", contract.ErrInvalidState)
		}
		if r.EffectiveFrom <= s.kstDay() {
			return fmt.Errorf("%w: 이미 효력이 시작된 세율은 취소할 수 없습니다. 더 늦은 효력일로 올바른 세율을 새로 등록하세요.", contract.ErrInvalidState)
		}
		if err := s.Repo.CancelVATRate(ctx, tx, id, p.UserID, s.now()); err != nil {
			return err
		}
		return s.record(ctx, tx, p, "company.vat_rate_cancel", audit.TargetSetting, id,
			audit.DiffItem{Key: "vat_effective_from", Old: r.EffectiveFrom}, audit.DiffItem{Key: "vat_rate", Old: strconv.Itoa(r.Percent)})
	})
}

// ---- 공휴일 ----

func (s *CompanyService) Holidays(ctx context.Context, year int) ([]domain.Holiday, error) {
	if err := s.readAccounting(ctx, "company.holiday_read"); err != nil {
		return nil, err
	}
	if year < 2000 || year > 2100 {
		year = parseDay(s.kstDay()).Year()
	}
	return s.Repo.Holidays(ctx, s.DB, year)
}

// ParseHolidayLines 는 일괄 입력(한 줄에 "YYYY-MM-DD 이름", 최대 100줄)을 읽는다. 오류 줄은 줄 번호와 사유로 돌려준다.
func ParseHolidayLines(text string) (items []domain.Holiday, problems []string) {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	n := 0
	seen := map[string]bool{}
	for i, raw := range lines {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		n++
		if n > 100 {
			problems = append(problems, fmt.Sprintf("%d번째 줄: 한 번에 100줄까지 입력할 수 있습니다.", i+1))
			break
		}
		date, name, _ := strings.Cut(line, " ")
		name = strings.TrimSpace(name)
		switch {
		case !validDate(date):
			problems = append(problems, fmt.Sprintf("%d번째 줄: 날짜는 2026-12-25 와 같은 형식이어야 합니다.", i+1))
		case name == "" || utf8.RuneCountInString(name) > 50:
			problems = append(problems, fmt.Sprintf("%d번째 줄: 이름은 1자 이상 50자 이내로 입력하세요.", i+1))
		case seen[date]:
			problems = append(problems, fmt.Sprintf("%d번째 줄: 같은 날짜가 위에 이미 있습니다.", i+1))
		default:
			seen[date] = true
			items = append(items, domain.Holiday{Date: date, Name: name})
		}
	}
	return items, problems
}

// AddHolidays 는 공휴일을 등록하고 새로 등록된 건수를 돌려준다(이미 있는 날짜는 건너뛴다).
func (s *CompanyService) AddHolidays(ctx context.Context, items []domain.Holiday) (int, error) {
	p, err := s.authorize(ctx, contract.PermCompanyManage, "company.holiday_add", audit.TargetSetting)
	if err != nil {
		return 0, err
	}
	if len(items) == 0 || len(items) > 100 {
		return 0, validation("공휴일은 한 번에 1건에서 100건까지 등록할 수 있습니다.")
	}
	for _, h := range items {
		if !validDate(h.Date) {
			return 0, validation("날짜는 2026-12-25 와 같은 형식으로 입력하세요.")
		}
		if n := utf8.RuneCountInString(strings.TrimSpace(h.Name)); n < 1 || n > 50 {
			return 0, validation("공휴일 이름은 1자 이상 50자 이내로 입력하세요.")
		}
	}
	added := 0
	err = s.write(ctx, p, "company.holiday_add", audit.TargetSetting, 0, func(tx contract.Tx) error {
		added = 0
		for _, h := range items {
			ok, err := s.Repo.InsertHoliday(ctx, tx, h.Date, strings.TrimSpace(h.Name), p.UserID, s.now())
			if err != nil {
				return err
			}
			if !ok {
				continue
			}
			added++
			if err := s.record(ctx, tx, p, "company.holiday_add", audit.TargetSetting, 0,
				audit.DiffItem{Key: "holiday_date", New: h.Date}, audit.DiffItem{Key: "holiday_name", New: strings.TrimSpace(h.Name)}); err != nil {
				return err
			}
		}
		return nil
	})
	return added, err
}

// RemoveHoliday 는 공휴일을 지운다. 지난 날짜(오늘 포함)는 이미 지난 알림·일정 판정이 달라지므로 지울 수 없다.
func (s *CompanyService) RemoveHoliday(ctx context.Context, date string) error {
	p, err := s.authorize(ctx, contract.PermCompanyManage, "company.holiday_remove", audit.TargetSetting)
	if err != nil {
		return err
	}
	if !validDate(date) {
		return validation("날짜는 2026-12-25 와 같은 형식으로 입력하세요.")
	}
	if date <= s.kstDay() {
		return validation("지난 날짜의 공휴일은 지울 수 없습니다.")
	}
	return s.write(ctx, p, "company.holiday_remove", audit.TargetSetting, 0, func(tx contract.Tx) error {
		name, found, err := s.Repo.DeleteHoliday(ctx, tx, date)
		if err != nil {
			return err
		}
		if !found {
			return contract.ErrNotFound
		}
		return s.record(ctx, tx, p, "company.holiday_remove", audit.TargetSetting, 0,
			audit.DiffItem{Key: "holiday_date", Old: date}, audit.DiffItem{Key: "holiday_name", Old: name})
	})
}
