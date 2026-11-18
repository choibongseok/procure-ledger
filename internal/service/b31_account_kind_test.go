package service_test

import (
	"strings"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
)

// 계정 등록 때 구분이 코드 앞 숫자와 다르면 400 이다(조용히 코드 기준으로 저장하지 않는다). 구분을 비우면 코드 앞 숫자로 정한다.
func TestAccountCreateKindMustMatchCode(t *testing.T) {
	e := newEnv(t)
	if _, err := e.accounts.Create(as(uFinance), domain.AccountInput{Code: "5301", Name: "시험비", Kind: domain.AccountAsset}); !isErr(err, contract.ErrValidation) || !strings.Contains(err.Error(), "다릅니다") {
		t.Fatalf("구분 불일치: %v", err)
	}
	a, err := e.accounts.Create(as(uFinance), domain.AccountInput{Code: "5301", Name: "시험비", Kind: domain.AccountExpense})
	if err != nil || a.Kind != domain.AccountExpense {
		t.Fatalf("일치: %+v %v", a, err)
	}
	b, err := e.accounts.Create(as(uFinance), domain.AccountInput{Code: "5302", Name: "시험비2"})
	if err != nil || b.Kind != domain.AccountExpense {
		t.Fatalf("구분 생략: %+v %v", b, err)
	}
}
