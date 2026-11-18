package cicheck

import (
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/notify"
	"github.com/choibongseok/procure-ledger/internal/sod"
)

// 전자세금계산서 운영 가이드(docs/tax-invoice-guide.md)가 적은 권한·규칙·알림·사유 문장·모의 시나리오·화면 경로가 코드의 정본과 일치해야 한다(B43 3단계, 명세 15절).
const taxGuideDoc = "docs/tax-invoice-guide.md"

func TestTaxGuidePermissionsAndRulesExist(t *testing.T) {
	doc := readDoc(t, taxGuideDoc)
	known := map[string]bool{}
	for _, p := range contract.AllPermissions {
		known[string(p)] = true
	}
	seen := 0
	for _, m := range regexp.MustCompile("`([a-z]+:[a-z_]+)`").FindAllStringSubmatch(doc, -1) {
		seen++
		if !known[m[1]] {
			t.Errorf("%s 가 적은 권한 %s 가 contract.AllPermissions 에 없음", taxGuideDoc, m[1])
		}
	}
	if seen == 0 {
		t.Errorf("%s 에 권한 이름이 하나도 없음", taxGuideDoc)
	}
	// 필수로 설명할 권한 4개.
	for _, must := range []contract.Permission{contract.PermTaxInvoiceRead, contract.PermTaxInvoiceWrite, contract.PermTaxInvoiceIssue, contract.PermTaxASPManage} {
		if !strings.Contains(doc, "`"+string(must)+"`") {
			t.Errorf("%s 가 권한 %s 를 설명하지 않음", taxGuideDoc, must)
		}
	}
	rules := map[string]bool{}
	for _, r := range sod.Rules {
		rules[r.Code] = true
	}
	for _, m := range regexp.MustCompile("`((?:hold|tax)\\.[a-z_]+)`").FindAllStringSubmatch(doc, -1) {
		if !rules[m[1]] {
			t.Errorf("%s 가 적은 권한 분리 규칙 %s 가 sod.Rules 에 없음", taxGuideDoc, m[1])
		}
	}
	for _, must := range []string{"tax.create_issue", "hold.taxasp_issue"} {
		if !strings.Contains(doc, "`"+must+"`") {
			t.Errorf("%s 가 규칙 %s 를 설명하지 않음", taxGuideDoc, must)
		}
	}
}

func TestTaxGuideNotificationsAndReasonsMatchCode(t *testing.T) {
	doc := readDoc(t, taxGuideDoc)
	for _, kind := range []string{"tax_invoice_failed", "tax_invoice_sync", "tax_cert_expiring"} {
		k, ok := notify.KindByCode(kind)
		if !ok {
			t.Fatalf("알림 종류 %s 가 notify.Kinds 에 없음", kind)
		}
		if !strings.Contains(doc, k.Name) {
			t.Errorf("%s 가 알림 %q(%s)를 설명하지 않음", taxGuideDoc, k.Name, kind)
		}
	}
	// 화면에 나오는 사유 문장(domain.TaxCodeLabels) 전부가 문제 해결 표에 있어야 한다.
	codes := make([]string, 0, len(domain.TaxCodeLabels))
	for c := range domain.TaxCodeLabels {
		codes = append(codes, c)
	}
	sort.Strings(codes)
	for _, c := range codes {
		if !strings.Contains(doc, domain.TaxCodeLabels[c]) {
			t.Errorf("%s 의 문제 해결 표에 사유 문장 %q(%s)가 없음", taxGuideDoc, domain.TaxCodeLabels[c], c)
		}
	}
}

func TestTaxGuideMockModesAndScreensMatchCode(t *testing.T) {
	doc := readDoc(t, taxGuideDoc)
	src := readDoc(t, "internal/taxasp/mock/mock.go")
	m := regexp.MustCompile(`case "", ([^:]+):`).FindStringSubmatch(src)
	if m == nil {
		t.Fatal("모의 구현의 시나리오 목록(case \"\", …)을 찾지 못함")
	}
	for _, mode := range regexp.MustCompile(`"([a-z]+)"`).FindAllStringSubmatch(m[1], -1) {
		if !strings.Contains(doc, "`"+mode[1]+"`") {
			t.Errorf("%s 가 모의 시나리오 %s 를 설명하지 않음", taxGuideDoc, mode[1])
		}
	}
	// 문서가 적은 화면 경로는 웹 라우트에 있어야 한다.
	web := readDoc(t, "web/src/admin/routes.tsx") + readDoc(t, "web/src/finance/routes.tsx")
	for _, p := range regexp.MustCompile("`(/(?:admin|finance)/[a-z-]+)`").FindAllStringSubmatch(doc, -1) {
		if !strings.Contains(web, `"`+p[1]+`"`) {
			t.Errorf("%s 가 적은 화면 경로 %s 가 웹 라우트에 없음", taxGuideDoc, p[1])
		}
	}
	// 운영 문서의 키 교체 절에도 대행사 설정이 적혀 있어야 한다(키 교체 뒤 조용히 멈추는 사고 방지).
	if !strings.Contains(readDoc(t, "docs/operations.md"), "tax_asp_settings") {
		t.Error("docs/operations.md 의 키 교체 절에 대행사 설정(tax_asp_settings)이 없음")
	}
}
