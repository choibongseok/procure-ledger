package service

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/repository"
)

// 계정과목·자동 전표 규칙(B31 3.1). 조회 ledger:read, 변경 account:manage(재무·관리자). 모든 변경은 감사에 남는다.

// AccountService 는 domain.AccountService 의 구현이다.
type AccountService struct {
	Base
	Repo repository.SQLLedger
}

var _ domain.AccountService = (*AccountService)(nil)

func NewAccountService(b Base) *AccountService { return &AccountService{Base: b} }

var accountCode = regexp.MustCompile(`^[1-5][0-9]{3}$`)

// ruleLabels 는 자동 전표 규칙 키의 한국어 이름이다(키는 코드가 쓰는 값이라 새 키는 코드와 함께만 늘어난다).
var ruleLabels = map[string]string{
	"purchase.inventory": "매입 전표: 재고자산(차변)", "purchase.vat": "매입 전표: 부가세대급금(차변)", "purchase.payable": "매입 전표: 외상매입금(대변)",
	"expense.welfare": "지출 결의서 비용: 복리후생", "expense.trip": "지출 결의서 비용: 출장", "expense.supplies": "지출 결의서 비용: 소모품",
	"expense.training": "지출 결의서 비용: 교육", "expense.other": "지출 결의서 비용: 기타", "expense.payable": "지출 결의서 비용 전표: 미지급금(대변)",
	"payment.bank": "지급 전표: 보통예금(대변)", "card.payable": "카드 비용 전표: 미지급금(대변)", "card.vat": "카드 비용 전표: 부가세대급금(차변)",
	"opening.equity": "기초 전표: 기초 이월(이익잉여금)",
}

func kindOfCode(code string) string {
	switch code[0] {
	case '1':
		return domain.AccountAsset
	case '2':
		return domain.AccountLiability
	case '3':
		return domain.AccountEquity
	case '4':
		return domain.AccountRevenue
	}
	return domain.AccountExpense
}

// List 는 계정 목록이다(코드 순).
func (s *AccountService) List(ctx context.Context, includeInactive bool) ([]domain.Account, error) {
	if _, err := contract.Authorize(ctx, contract.PermLedgerRead); err != nil {
		return nil, err
	}
	return s.Repo.Accounts(ctx, s.DB, includeInactive)
}

func cleanAccountName(name string) (string, error) {
	n := strings.TrimSpace(name)
	if c := utf8.RuneCountInString(n); c < 1 || c > 40 {
		return "", validation("계정과목 이름은 1~40자로 입력하세요.")
	}
	return n, nil
}

// Create 는 계정을 더한다. 코드는 4자리 숫자이고 구분은 앞 숫자로 정해진다.
func (s *AccountService) Create(ctx context.Context, in domain.AccountInput) (domain.Account, error) {
	p, err := s.authorize(ctx, contract.PermAccountManage, "account.create", audit.TargetAccount)
	if err != nil {
		return domain.Account{}, err
	}
	code := strings.TrimSpace(in.Code)
	if !accountCode.MatchString(code) {
		return domain.Account{}, validation("계정 코드는 4자리 숫자(앞 숫자 1 자산·2 부채·3 자본·4 수익·5 비용)로 입력하세요.")
	}
	if in.Kind != "" && in.Kind != kindOfCode(code) {
		return domain.Account{}, validation("계정 구분(%s)이 코드 %s 의 앞 숫자가 뜻하는 구분(%s)과 다릅니다.", domain.AccountKindLabels[in.Kind], code, domain.AccountKindLabels[kindOfCode(code)])
	}
	name, err := cleanAccountName(in.Name)
	if err != nil {
		return domain.Account{}, err
	}
	var out domain.Account
	err = s.write(ctx, p, "account.create", audit.TargetAccount, 0, func(tx contract.Tx) error {
		if _, found, err := s.Repo.AccountByCode(ctx, tx, code); err != nil {
			return err
		} else if found {
			return validation("이미 있는 계정 코드입니다(%s).", code)
		}
		if err := s.Repo.InsertAccount(ctx, tx, code, name, kindOfCode(code), s.now()); err != nil {
			return err
		}
		out, _, err = s.Repo.AccountByCode(ctx, tx, code)
		if err != nil {
			return err
		}
		return s.Audit.Record(ctx, tx, audit.Entry{Actor: p, Action: "account.create", TargetType: audit.TargetAccount, TargetID: code, Result: audit.ResultSuccess, Detail: "계정과목 추가",
			Diff: []audit.DiffItem{{Key: "code", New: code}, {Key: "name", New: name}}})
	})
	return out, err
}

// Rename 은 이름을 바꾼다(시스템 계정도 이름은 바꿀 수 있다).
func (s *AccountService) Rename(ctx context.Context, code, name string) (domain.Account, error) {
	p, err := s.authorize(ctx, contract.PermAccountManage, "account.update", audit.TargetAccount)
	if err != nil {
		return domain.Account{}, err
	}
	n, err := cleanAccountName(name)
	if err != nil {
		return domain.Account{}, err
	}
	var out domain.Account
	err = s.write(ctx, p, "account.update", audit.TargetAccount, 0, func(tx contract.Tx) error {
		a, found, err := s.Repo.AccountByCode(ctx, tx, code)
		if err != nil {
			return err
		}
		if !found {
			return contract.ErrNotFound
		}
		if err := s.Repo.RenameAccount(ctx, tx, code, n); err != nil {
			return err
		}
		out = a
		out.Name = n
		return s.Audit.Record(ctx, tx, audit.Entry{Actor: p, Action: "account.update", TargetType: audit.TargetAccount, TargetID: code, Result: audit.ResultSuccess, Detail: "계정과목 이름 변경",
			Diff: []audit.DiffItem{{Key: "name", Old: a.Name, New: n}}})
	})
	return out, err
}

// SetActive 는 사용 여부를 바꾼다. 쓰인 계정은 삭제할 수 없고 중지만 되며, 시스템 계정은 중지할 수 없다.
func (s *AccountService) SetActive(ctx context.Context, code string, active bool) (domain.Account, error) {
	action := "account.deactivate"
	if active {
		action = "account.activate"
	}
	p, err := s.authorize(ctx, contract.PermAccountManage, action, audit.TargetAccount)
	if err != nil {
		return domain.Account{}, err
	}
	var out domain.Account
	err = s.write(ctx, p, action, audit.TargetAccount, 0, func(tx contract.Tx) error {
		a, found, err := s.Repo.AccountByCode(ctx, tx, code)
		if err != nil {
			return err
		}
		if !found {
			return contract.ErrNotFound
		}
		if !active && a.System {
			return validation("시스템 계정은 중지할 수 없습니다(%s %s). 자동 전표 규칙이 쓰는 계정입니다.", a.Code, a.Name)
		}
		if err := s.Repo.SetAccountActive(ctx, tx, code, active); err != nil {
			return err
		}
		out = a
		out.Active = active
		return s.Audit.Record(ctx, tx, audit.Entry{Actor: p, Action: action, TargetType: audit.TargetAccount, TargetID: code, Result: audit.ResultSuccess, Detail: "계정과목 사용 여부 변경",
			Diff: []audit.DiffItem{{Key: "active", Old: fmt.Sprint(a.Active), New: fmt.Sprint(active)}}})
	})
	return out, err
}

// Rules 는 자동 전표 규칙 표다(한국어 이름 포함). 규칙 계정이 중지됐으면 Active 가 거짓이다.
func (s *AccountService) Rules(ctx context.Context) ([]domain.AccountRule, error) {
	if _, err := contract.Authorize(ctx, contract.PermLedgerRead); err != nil {
		return nil, err
	}
	rs, err := s.Repo.Rules(ctx, s.DB)
	if err != nil {
		return nil, err
	}
	for i := range rs {
		rs[i].Label = ruleLabels[rs[i].Key]
	}
	return rs, nil
}

// SetRule 은 규칙의 계정을 바꾼다. 계정은 사용 중이어야 하고 구분이 규칙의 성격과 맞아야 한다(차변 비용·자산, 대변 부채).
func (s *AccountService) SetRule(ctx context.Context, key, accountCode string) error {
	p, err := s.authorize(ctx, contract.PermAccountManage, "account.rule_set", audit.TargetAccount)
	if err != nil {
		return err
	}
	if _, ok := ruleLabels[key]; !ok {
		return contract.ErrNotFound
	}
	return s.write(ctx, p, "account.rule_set", audit.TargetAccount, 0, func(tx contract.Tx) error {
		a, found, err := s.Repo.AccountByCode(ctx, tx, accountCode)
		if err != nil {
			return err
		}
		if !found {
			return validation("알 수 없는 계정과목입니다(%s).", accountCode)
		}
		if !a.Active {
			return validation("중지된 계정과목입니다(%s %s).", a.Code, a.Name)
		}
		want := ruleKind(key)
		if want != "" && a.Kind != want {
			return validation("이 규칙에는 %s 계정을 지정해야 합니다.", domain.AccountKindLabels[want])
		}
		old, _, err := s.Repo.RuleAccount(ctx, tx, key)
		if err != nil {
			return err
		}
		if err := s.Repo.SetRule(ctx, tx, key, a.ID, p.UserID, s.now()); err != nil {
			return err
		}
		return s.Audit.Record(ctx, tx, audit.Entry{Actor: p, Action: "account.rule_set", TargetType: audit.TargetAccount, TargetID: key, Result: audit.ResultSuccess, Detail: "자동 전표 규칙 수정",
			Diff: []audit.DiffItem{{Key: "rule_key", New: key}, {Key: "account_code", Old: old.Code, New: a.Code}}})
	})
}

// ruleKind 는 규칙이 요구하는 계정 구분이다(비면 제한 없음).
func ruleKind(key string) string {
	switch {
	case key == "purchase.inventory" || key == "purchase.vat" || key == "card.vat":
		return domain.AccountAsset
	case strings.HasSuffix(key, ".payable"):
		return domain.AccountLiability
	case key == "payment.bank":
		return domain.AccountAsset
	case key == "opening.equity":
		return domain.AccountEquity
	case strings.HasPrefix(key, "expense."):
		return domain.AccountExpense
	}
	return ""
}
