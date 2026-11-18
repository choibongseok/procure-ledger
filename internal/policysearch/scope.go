package policysearch

import (
	"strings"

	"github.com/choibongseok/procure-ledger/internal/contract"
)

// B33: 문서 열람 범위는 역할 이름이 아니라 권한으로 판정한다. 사용자 정의 역할(이름이 아무것도 아닌 역할)을 가진 사람도 자기 권한에 맞는 문서를 본다.
//
// 문서 머리말 `roles:` 의 항목은 세 가지다.
//   - all: 모든 사용자
//   - 기본 템플릿 역할 이름(admin·finance·manager·staff·auditor): 아래 roleScopePerms 의 권한 중 하나라도 가진 사용자(동등 이관).
//   - 권한 이름(예 `ledger:read`, 콜론이 있다): 그 권한을 가진 사용자
//
// 이 표를 바꾸면 기존 문서의 열람 결과가 바뀌므로 `TestTemplateUsersSeeTheSameDocumentsAsBefore` 가 모든 내장 문서 × 5개 기본 역할로 이전 결과와 같은지 지킨다.
var roleScopePerms = map[string][]contract.Permission{
	"admin":   {contract.PermRoleManage},
	"finance": {contract.PermLedgerRead, contract.PermPaymentRead, contract.PermPaymentWrite},
	"manager": {contract.PermCardConfirm, contract.PermApprovalDecide},
	"auditor": {contract.PermAuditRead},
	"staff":   {contract.PermDocumentVerify},
}

// scopeTokenPerms 는 머리말 항목 하나가 요구하는 권한(하나라도)이다. 알 수 없는 항목은 nil(아무도 못 본다).
func scopeTokenPerms(token string) []contract.Permission {
	token = strings.ToLower(strings.TrimSpace(token))
	if perms, ok := roleScopePerms[token]; ok {
		return perms
	}
	if strings.Contains(token, ":") && isKnownPermission(token) {
		return []contract.Permission{contract.Permission(token)}
	}
	return nil
}

func isKnownPermission(s string) bool {
	for _, p := range contract.AllPermissions {
		if string(p) == s {
			return true
		}
	}
	return false
}

// ViewerPerms 는 호출자 Principal 이 가진 권한 집합이다(Can 으로 판정하므로 사용자 정의 역할의 실효 권한을 따른다).
func ViewerPerms(p contract.Principal) contract.PermSet {
	set := contract.NewPermSet()
	for _, perm := range contract.AllPermissions {
		if p.Can(perm) {
			set[perm] = struct{}{}
		}
	}
	return set
}

// perms 는 열람 판정에 쓸 권한 집합이다. 서비스가 만든 Viewer 는 Perms 가 채워져 있다. 없으면(권한 계산이 없는 호출자) ok=false 로
// 아무것도 보이지 않는다(fail-closed). 역할 이름은 보지 않는다.
func (v Viewer) perms() (contract.PermSet, bool) {
	if v.Perms == nil {
		return nil, false
	}
	return v.Perms, true
}

func holdsAny(set contract.PermSet, perms []contract.Permission) bool {
	for _, p := range perms {
		if set.Has(p) {
			return true
		}
	}
	return false
}
