package policysearch

import (
	"context"
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/choibongseok/procure-ledger/internal/contract"
)

// 이전 규칙(역할 이름 비교)을 그대로 옮긴 기준이다. 새 권한 기준 규칙이 기본 템플릿 사용자에게 같은 결과를 주는지 보는 데만 쓴다.
func oldVisible(d Doc, role contract.Role, dept string) bool {
	if role == "" {
		return false
	}
	roleOK := d.AllRoles
	for _, r := range d.Roles {
		roleOK = roleOK || r == string(role)
	}
	if !roleOK {
		return false
	}
	if d.AllDepts {
		return true
	}
	for _, x := range d.Depts {
		if x != "" && x == dept {
			return true
		}
	}
	return false
}

// 기존 템플릿 사용자의 결과가 바뀌지 않는다: 내장 문서 전체 × 기본 역할 5개 × 부서 4가지에서 이전 규칙과 같고(색인·서비스 독립 확인 둘 다),
// 서비스를 거친 검색 결과의 문서 집합도 같다.
func TestTemplateUsersSeeTheSameDocumentsAsBefore(t *testing.T) {
	docs := realIndex(t).Docs()
	if len(docs) < 15 {
		t.Fatalf("내장 문서가 너무 적음: %d", len(docs))
	}
	for _, d := range docs {
		for _, role := range contract.Roles {
			for _, dept := range []string{"구매팀", "경영지원팀", "영업팀", ""} {
				v := vw(string(role), dept)
				want := oldVisible(d, role, dept)
				if got := d.Visible(v); got != want {
					t.Fatalf("%s role=%s dept=%q: 이전 %v 새 %v", d.Slug, role, dept, want, got)
				}
				if got := rawAllows(d, v); got != want {
					t.Fatalf("%s role=%s dept=%q: 독립 확인 %v 이전 %v", d.Slug, role, dept, got, want)
				}
			}
		}
	}
	// 서비스 경로: 같은 질의의 문서 집합이 이전 규칙의 열람 가능 집합 안에 있고, 재무 문서는 템플릿 별로 그대로다.
	e := newEnv(t, nil)
	for role, wantSees := range map[contract.Role]bool{contract.RoleFinance: true, contract.RoleAdmin: true, contract.RoleAuditor: true, contract.RoleManager: false, contract.RoleStaff: false} {
		hits, err := e.svc.Search(ctxAs(role, 2), "지급 요청 이체 참조번호 중복 지급 처리 분리", 20)
		if err != nil {
			t.Fatal(err)
		}
		saw := false
		for _, h := range hits {
			saw = saw || h.DocSlug == "finance-payment"
		}
		if saw != wantSees {
			t.Fatalf("%s: 지급 규정 열람 %v (기대 %v)", role, saw, wantSees)
		}
	}
}

func principalWith(perms ...contract.Permission) contract.Principal {
	return contract.Principal{UserID: 77, Name: "신입", Role: "", RoleNames: []string{"사용자 정의"}, DepartmentID: 2, Channel: contract.ChannelWeb,
		Perms: contract.NewPermSet(perms...)}
}

func docSlugs(t *testing.T, e env, p contract.Principal, q string) map[string]bool {
	t.Helper()
	hits, err := e.svc.Search(contract.WithPrincipal(context.Background(), p), q, 20)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]bool{}
	for _, h := range hits {
		out[h.DocSlug] = true
	}
	return out
}

// 사용자 정의 역할(기본 템플릿 이름이 아닌, 권한만 가진 사용자)은 자기 권한에 맞는 문서를 본다.
func TestCustomRoleUsersSeeDocumentsByPermission(t *testing.T) {
	e := newEnv(t, nil)
	const payQ = "지급 요청 이체 참조번호 중복 지급"
	// 1) 정책 검색 권한만 있는 신입: roles: all 문서만 보이고 재무·감사 문서는 안 보인다(검색 자체는 된다).
	only := docSlugs(t, e, principalWith(contract.PermPolicySearch), payQ)
	if only["finance-payment"] || only["finance-journal"] || only["audit-security"] {
		t.Fatalf("권한 없는 문서가 보임: %v", only)
	}
	if got := docSlugs(t, e, principalWith(contract.PermPolicySearch), "금액 구간별 결재선"); !got["purchase-approval-line"] {
		t.Fatalf("roles: all 문서는 보여야 함(Role 이 비어 있어도): %v", got)
	}
	// 2) 지급 권한을 가진 사용자 정의 역할: 재무 문서가 보인다(finance 템플릿 이름이 없어도).
	pay := docSlugs(t, e, principalWith(contract.PermPolicySearch, contract.PermPaymentRead), payQ)
	if !pay["finance-payment"] {
		t.Fatalf("지급 권한자가 지급 규정을 못 봄: %v", pay)
	}
	if pay["audit-security"] {
		t.Fatalf("지급 권한이 감사 규정까지 열면 안 됨: %v", pay)
	}
	// 3) 감사 권한(audit:read)을 가진 역할: 문서 머리말이 auditor 를 적은 문서(감사·재무 규정)가 보이고, 감사 권한이 없는 지급 권한자는 감사 규정이 안 보인다.
	aud := docSlugs(t, e, principalWith(contract.PermPolicySearch, contract.PermAuditRead), "감사 로그 해시 체인 지급 요청 이체")
	if !aud["audit-security"] || !aud["finance-payment"] {
		t.Fatalf("감사 권한자: %v", aud)
	}
	// 4) 정책 검색 권한이 없으면 거절된다(역할 이름과 무관).
	if _, err := e.svc.Search(contract.WithPrincipal(context.Background(), principalWith(contract.PermPaymentRead)), payQ, 5); err == nil {
		t.Fatal("policy:search 가 없으면 거절되어야 함")
	}
	// 5) Role 도 Perms 도 없는 Principal 은 아무것도 못 본다(fail-closed).
	bare := contract.Principal{UserID: 78, DepartmentID: 2, Channel: contract.ChannelWeb}
	if _, err := e.svc.Search(contract.WithPrincipal(context.Background(), bare), payQ, 5); err == nil {
		t.Fatal("권한 계산이 없는 Principal 은 거절되어야 함")
	}
}

// 웹·MCP 가 같은 서비스를 쓰므로 같은 질의는 같은 결과다: 사용자 정의 역할 사용자와 같은 권한을 가진 템플릿 사용자의 결과가 같다.
func TestCustomRoleEqualsTemplateWithSamePermissions(t *testing.T) {
	e := newEnv(t, nil)
	fin := contract.Principal{UserID: 5, Role: contract.RoleFinance, DepartmentID: 2, Channel: contract.ChannelWeb}
	clone := contract.Principal{UserID: 6, Role: "", DepartmentID: 2, Channel: contract.ChannelWeb, Perms: ViewerPerms(fin)}
	for _, q := range []string{"지급 요청 이체 참조번호", "금액 구간별 결재선", "증빙 판정 재무 확정", "카드 소명 기한"} {
		a, b := docSlugs(t, e, fin, q), docSlugs(t, e, clone, q)
		if len(a) != len(b) {
			t.Fatalf("%q: 템플릿 %v 사용자 정의 %v", q, a, b)
		}
		for k := range a {
			if !b[k] {
				t.Fatalf("%q: %s 가 사용자 정의 역할에 안 보임", q, k)
			}
		}
	}
}

// 문서 머리말에 권한 이름을 직접 쓸 수 있다. 알 수 없는 권한 이름은 문서 문제로 보고하고 그 문서는 열리지 않는다.
func TestDocumentScopeCanNamePermissions(t *testing.T) {
	src := fstest.MapFS{
		"vat.md":  {Data: []byte("---\ntitle: 부가세 규정\nroles: vat:read\ndepartments: all\n---\n## 제1조 (집계)\n부가세 매입 집계는 기간별로 낸다 가나다라\n")},
		"all.md":  {Data: []byte("---\ntitle: 공통\nroles: all\ndepartments: all\n---\n## 제1조 (공통)\n부가세 공통 안내 마바사\n")},
		"typo.md": {Data: []byte("---\ntitle: 오타\nroles: vat:reed\ndepartments: all\n---\n## 제1조 (가)\n본문 부가세\n")},
	}
	res, err := Load(src)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Problems) != 1 || !strings.Contains(res.Problems[0], "typo.md") || len(res.Index.Docs()) != 2 {
		t.Fatalf("docs=%d problems=%v", len(res.Index.Docs()), res.Problems)
	}
	e := newEnv(t, func() fs.FS { return src })
	with := docSlugs(t, e, principalWith(contract.PermPolicySearch, contract.PermVATRead), "부가세 매입 집계 기간별")
	without := docSlugs(t, e, principalWith(contract.PermPolicySearch), "부가세 매입 집계 기간별")
	if !with["vat"] || without["vat"] || !without["all"] {
		t.Fatalf("권한 이름 범위: 있음 %v 없음 %v", with, without)
	}
}
