package policysearch

import "github.com/choibongseok/procure-ledger/internal/contract"

// vw 는 시험용 Viewer 다: 기본 템플릿 역할의 실효 권한(ViewerPerms)을 가진 호출자. 역할이 비면 권한 계산이 없는 호출자라 아무것도 못 본다.
func vw(role string, dept string) Viewer {
	if role == "" {
		return Viewer{Dept: dept}
	}
	return Viewer{Perms: ViewerPerms(contract.Principal{UserID: 1, Role: contract.Role(role)}), Dept: dept}
}
