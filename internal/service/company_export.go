package service

import "github.com/choibongseok/procure-ledger/internal/contract/domain"

// ValidateCompanyInput 은 회사 정보 입력의 형식 검사(공백 정리 포함)다. 회사 설정 화면(Update)과 설치 마법사(B38)가 같은 규칙을 쓰도록 노출한다.
func ValidateCompanyInput(in *domain.CompanyInput) error { return validateCompany(in) }
