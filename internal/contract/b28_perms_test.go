package contract_test

import (
	"testing"

	"github.com/choibongseok/procure-ledger/internal/contract"
)

// B28: 업무 보고서(report:read)는 결재자·재무·감사·관리자에게만 있고 직원에게는 없다. 내보내기 전용 권한은 없다(읽기 권한과 같다).
func TestReportReadRoles(t *testing.T) {
	want := map[contract.Role]bool{
		contract.RoleAdmin: true, contract.RoleFinance: true, contract.RoleManager: true, contract.RoleAuditor: true, contract.RoleStaff: false,
	}
	for role, w := range want {
		got := false
		for _, p := range contract.RolePermissions[role] {
			got = got || p == contract.PermReportRead
		}
		if got != w {
			t.Errorf("%s 의 report:read = %v(기대 %v)", role, got, w)
		}
	}
	for _, p := range contract.AllPermissions {
		if string(p) == "export:run" || string(p) == "export:read" {
			t.Errorf("내보내기 전용 권한 %q 가 있음(읽기 권한과 같아야 함)", p)
		}
	}
}
