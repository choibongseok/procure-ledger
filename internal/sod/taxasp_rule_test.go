package sod

import (
	"testing"

	"github.com/choibongseok/procure-ledger/internal/contract"
)

// B43 명세 12.3: 대행사 설정(인증서 관리)과 세금계산서 발행 권한을 한 사람이 함께 가지면 hold 규칙 위반(기본 경고)이다.
func TestTaxASPIssueHoldRuleDeclared(t *testing.T) {
	var found *Rule
	for i := range Rules {
		if Rules[i].Code == "hold.taxasp_issue" {
			found = &Rules[i]
		}
	}
	if found == nil {
		t.Fatal("규칙 hold.taxasp_issue 가 없다")
	}
	if found.Kind != KindHold || found.Default != ModeWarn || found.Locked {
		t.Fatalf("종류·기본 모드: %+v", *found)
	}
	if found.PermA != contract.PermTaxASPManage || found.PermB != contract.PermTaxInvoiceIssue {
		t.Fatalf("권한 쌍: %s + %s", found.PermA, found.PermB)
	}
	// 기본 템플릿에는 쌍이 없다(관리자는 발행 권한이 없고 재무는 대행사 설정이 없다). 커스텀 역할로 합칠 때만 위반이 된다.
	for _, r := range contract.Roles {
		if contract.RoleHas(r, found.PermA) && contract.RoleHas(r, found.PermB) {
			t.Fatalf("기본 템플릿 %s 이 쌍을 함께 가짐", r)
		}
	}
}
