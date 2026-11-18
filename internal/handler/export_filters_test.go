package handler

import (
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/export"
)

const garbage = "홍길동 010-1234-5678"

// 조건 매개변수 이름 전체. 어느 원천이든 이 이름들을 읽는다.
var filterParams = []string{"status", "from", "to", "box", "tab", "month", "departmentId", "requesterId", "warehouseId", "poId", "itemId", "lotId",
	"belowSafety", "formCode", "q", "action", "actorId", "targetId", "channel", "targetType", "result", "actorName", "asOf", "unitId", "headOnly", "includeLeft"}

func allExportSources() map[string]export.Source {
	return map[string]export.Source{
		"purchase_requests": prExport(stubPR{}), "purchase_orders": poExport(stubExportLists{}), "receipts": receiptExport(stubExportLists{}),
		"stock": stockExport(stubExportLists{}), "ledger": ledgerExport(stubLedger{}, false), "ledger_valued": ledgerExport(stubLedger{}, true),
		"inbox": inboxExport(stubInbox{}), "budgets": budgetExport(stubBudget{}),
		"report_purchase": purchaseReportExport(nil), "report_vendors": vendorReportExport(nil), "report_approvals": approvalReportExport(nil),
		"month_stock": &monthStockSource{}, "audit_logs": &auditExportSource{h: &adminHandlers{}}, "org_people": &orgPeopleSource{},
	}
}

// 모든 조건 매개변수에 임의 문자열이 들어와도 (1) 종류가 있는 조건은 거부 대상이 되고 (2) 감사·정보 시트에 쓸 조건 문장에 그 문자열이 남지 않는다(AC-704).
func TestEveryExportSourceRejectsGarbageAndNeverLogsIt(t *testing.T) {
	for code, src := range allExportSources() {
		q := url.Values{}
		for _, k := range filterParams {
			q.Set(k, garbage)
		}
		items := src.Filters(q)
		if text := export.FilterText(items); strings.Contains(text, "홍길동") || strings.Contains(text, "010-1234") {
			t.Errorf("%s: 조건 문장에 요청 값이 남음 %q", code, text)
		}
		for _, it := range items {
			if it.Free || it.Kind == export.FilterHidden {
				continue
			}
			if bad := export.CheckFilters([]export.FilterItem{it}); len(bad) == 0 && it.Value != "" {
				t.Errorf("%s: 조건 %q 가 임의 문자열을 통과시킴(종류 %d)", code, it.Name, it.Kind)
			}
		}
	}
}

// 검증자가 재현한 두 요청: 문서함 box 와 구매요청 status.
func TestReportedFilterRepros(t *testing.T) {
	inbox := inboxExport(stubInbox{}).Filters(url.Values{"box": {garbage}})
	if bad := export.CheckFilters(inbox); len(bad) != 1 || bad[0] != "문서함" {
		t.Errorf("문서함 box: %v", bad)
	}
	if got := export.FilterText(inbox); got != "문서함=잘못된 값" {
		t.Errorf("문서함 조건 문장 %q", got)
	}
	pr := prExport(stubPR{}).Filters(url.Values{"status": {"홍길동"}})
	if bad := export.CheckFilters(pr); len(bad) != 1 || bad[0] != "상태" {
		t.Errorf("구매요청 status: %v", bad)
	}
	ok := prExport(stubPR{}).Filters(url.Values{"status": {"in_review"}, "from": {"2026-11-01"}})
	if bad := export.CheckFilters(ok); len(bad) != 0 {
		t.Errorf("올바른 조건이 거부됨 %v", bad)
	}
	if got := export.FilterText(ok); got != "상태=결재중; 작성일 시작=2026-11-01" {
		t.Errorf("올바른 조건 문장 %q", got)
	}
}

// AC-706·711: 내보내기 코드는 역할을 비교하지 않고(권한은 라우트 선언 한 곳), 원천은 저장소를 직접 부르거나 전체 열람 범위를 쓰지 않는다(서비스가 범위를 정한다).
func TestExportCodeHasNoRoleCheckOrScopeBypass(t *testing.T) {
	var files []string
	for _, pat := range []string{"export_*.go", "report_register.go", "month_stock_register.go"} {
		m, _ := filepath.Glob(pat)
		files = append(files, m...)
	}
	m, _ := filepath.Glob(filepath.Join("..", "export", "*.go"))
	files = append(files, m...)
	var checked int
	role := regexp.MustCompile(`\bRole\b|\bRole[A-Z]\w*|contract\.Role|\.Role\b`)
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		checked++
		src := string(b)
		if loc := role.FindStringIndex(src); loc != nil {
			t.Errorf("%s: 역할 비교가 있음(%q). 권한은 라우트 선언으로만 건다", f, src[loc[0]:loc[1]])
		}
		if strings.Contains(src, "ScopeAll") {
			t.Errorf("%s: ScopeAll 사용 금지(열람 범위는 서비스가 정한다)", f)
		}
		if strings.Contains(src, `"github.com/choibongseok/procure-ledger/internal/repository"`) {
			t.Errorf("%s: 내보내기 코드가 repository 를 직접 import 함", f)
		}
	}
	if checked < 8 {
		t.Fatalf("검사 대상 파일이 너무 적음(%d). 경로 패턴 확인", checked)
	}
}
