// Package apiv1 는 연동 REST API(/api/v1, B42)다.(라우트·OpenAPI), platform(토큰 발급·인증·속도 제한), erp(읽기 자원 서비스·변경 피드).
//
// 원칙: API 는 새 읽기 경로가 아니라 기존 서비스 함수(권한·부서 격리·마스킹)로 들어가는 입구다. 응답 값은 화면보다 적게 나간다(사람 정보 없음,
// 거래처의 사업자번호·계좌·연락처는 API 채널이 원문을 낼 수 없어 항상 가려서 나간다).
package apiv1

import (
	"fmt"

	"github.com/choibongseok/procure-ledger/internal/changes"
	"github.com/choibongseok/procure-ledger/internal/contract"
)

// ScopeDef 는 범위 이름이 붙이는 기존 권한과 그 범위가 변경 피드에서 여는 자원이다(명세 7.2). 이 표가 코드의 유일한 정본이다.
type ScopeDef struct {
	Name     string
	Perms    []contract.Permission
	Resource string // 변경 피드에서 이 범위가 볼 수 있는 자원("" 이면 없음)
	Title    string // 한국어 설명(발급 화면·문서)
	// IssuerRequires 는 이 범위를 발급하는 사람이 가져야 하는 "사람 권한"이다(D2 발급 시점 상한). Perms 중 내부 권한(어느 사용자도 갖지 못하는 것)은 상한 검사에서 빠지므로,
	// 내부 권한만으로 열리는 범위가 발급자 상한을 우회하지 않게 대응하는 사람 권한을 여기에 적는다. 내부가 아닌 Perms 는 그대로 발급자가 가져야 한다.
	IssuerRequires []contract.Permission
	Write          bool // 쓰기 범위(거래처·품목 가져오기, 전표 초안). 발급 화면이 경고를 붙이는 기준이다(B42 3단계)
}

// Scopes 는 발급할 수 있는 범위 전체다. 위험 권한(contract.HighRiskPermissions)·mask:bypass·role:manage 는 어떤 범위에도 붙지 않는다(시험이 고정한다).
var Scopes = []ScopeDef{
	{Name: "items:read", Perms: []contract.Permission{contract.PermInventoryRead}, Resource: changes.ResItem, Title: "품목과 재고 수량 읽기"},
	{Name: "vendors:read", Perms: []contract.Permission{contract.PermPurchaseRead}, Resource: changes.ResVendor, Title: "거래처 읽기(사업자번호·계좌·연락처는 항상 가려서 나간다)"},
	{Name: "purchase_orders:read", Perms: []contract.Permission{contract.PermInventoryRead, contract.PermPurchaseReadAll, contract.PermDocumentReadAll}, Resource: changes.ResPurchaseOrder, Title: "발주 읽기(전사 범위)"},
	{Name: "receipts:read", Perms: []contract.Permission{contract.PermInventoryRead, contract.PermPurchaseReadAll, contract.PermDocumentReadAll}, Resource: changes.ResReceipt, Title: "입고 읽기(전사 범위)"},
	{Name: "journals:read", Perms: []contract.Permission{contract.PermLedgerRead}, Resource: changes.ResJournal, Title: "전표 읽기(금액 포함)"},
	{Name: "payables:read", Perms: []contract.Permission{contract.PermLedgerRead}, Resource: changes.ResPayable, Title: "매입 채무 읽기"},
	{Name: "tax_invoices:read", Perms: []contract.Permission{contract.PermTaxInvoiceRead}, Resource: changes.ResTaxInvoice, Title: "세금계산서 읽기(사업자번호는 항상 가려서 나가고 이메일·주소·대표자·품목 이름은 없다)"},
	{Name: ScopeChangesRead, Title: "변경 피드 읽기(읽기 범위를 가진 자원의 줄만)"},
	// 쓰기 범위(D10): 거래처·품목 가져오기와 전표 초안뿐이다. 승인·지급·입출고·구매요청은 열지 않는다.
	{Name: ScopeVendorsWrite, Perms: []contract.Permission{contract.PermImportMaster}, Write: true, Title: "거래처 가져오기(미리보기 후 반영, 이름·사업자번호·결제 조건·연락처·계좌를 만들거나 바꾼다)"},
	{Name: ScopeItemsWrite, Perms: []contract.Permission{contract.PermImportMaster}, Write: true, Title: "품목 가져오기(미리보기 후 반영)"},
	{Name: ScopeJournalsDraft, Perms: []contract.Permission{contract.PermAPIJournalDraft}, IssuerRequires: []contract.Permission{contract.PermLedgerWrite}, Write: true, Title: "전표 초안 제출(초안까지만, 승인은 재무가 화면에서). 발급자는 승인자가 아니어야 한다"},
}

// 쓰기 범위 이름.
const (
	ScopeVendorsWrite  = "vendors:write"
	ScopeItemsWrite    = "items:write"
	ScopeJournalsDraft = "journals:draft"
)

// ScopeChangesRead 는 변경 피드(/changes)를 읽는 범위다. 피드는 호출자가 읽기 범위를 가진 자원의 줄만 돌려준다.
const ScopeChangesRead = "changes:read"

// ScopeByName 은 범위 정의를 찾는다.
func ScopeByName(name string) (ScopeDef, bool) {
	for _, s := range Scopes {
		if s.Name == name {
			return s, true
		}
	}
	return ScopeDef{}, false
}

// PermsFor 는 범위 이름들이 가리키는 기존 권한의 합집합에 내부 권한 api:access 를 더한 집합이다. 알 수 없는 범위는 오류다.
// API 주체(토큰)의 Perms 는 이것뿐이다: 발급자의 권한을 물려받지 않는다.
func PermsFor(scopes []string) (contract.PermSet, error) {
	perms := []contract.Permission{contract.PermAPIAccess}
	for _, n := range scopes {
		def, ok := ScopeByName(n)
		if !ok {
			return nil, fmt.Errorf("%w: 알 수 없는 범위 %q", contract.ErrValidation, n)
		}
		perms = append(perms, def.Perms...)
	}
	return contract.NewPermSet(perms...), nil
}

// ResourcesFor 는 범위들이 변경 피드에서 열어 주는 자원 목록이다.
func ResourcesFor(scopes []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, n := range scopes {
		if def, ok := ScopeByName(n); ok && def.Resource != "" && !seen[def.Resource] {
			seen[def.Resource] = true
			out = append(out, def.Resource)
		}
	}
	return out
}

// HasScope 는 범위 목록에 이름이 있는지다.
func HasScope(scopes []string, name string) bool {
	for _, s := range scopes {
		if s == name {
			return true
		}
	}
	return false
}
