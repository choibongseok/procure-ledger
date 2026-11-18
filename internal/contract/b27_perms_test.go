package contract_test

import (
	"testing"

	"github.com/choibongseok/procure-ledger/internal/contract"
)

// B27: 월 마감(inventory:close)은 재무만, 재고 단가·금액 조회(inventory:valuation_read)는 재무·관리자·감사만 가진다.
// 관리자는 마감하지 않는다(재무 업무). 직원·결재자에게는 금액 열이 나가지 않는다.
func TestInventoryCloseAndValuationRoles(t *testing.T) {
	want := map[contract.Role]struct{ close, valuation bool }{
		contract.RoleAdmin:   {false, true},
		contract.RoleFinance: {true, true},
		contract.RoleAuditor: {false, true},
		contract.RoleManager: {false, false},
		contract.RoleStaff:   {false, false},
	}
	for role, w := range want {
		has := func(p contract.Permission) bool {
			for _, x := range contract.RolePermissions[role] {
				if x == p {
					return true
				}
			}
			return false
		}
		if got := has(contract.PermInventoryClose); got != w.close {
			t.Errorf("%s 의 inventory:close = %v(기대 %v)", role, got, w.close)
		}
		if got := has(contract.PermInventoryValuation); got != w.valuation {
			t.Errorf("%s 의 inventory:valuation_read = %v(기대 %v)", role, got, w.valuation)
		}
	}
}
