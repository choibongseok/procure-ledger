package mcp_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
)

// 가짜 월 마감 서비스: 금액 거름은 서비스 몫이라 호출자 권한(inventory:valuation_read)으로 흉내 낸다.
type stubClose struct {
	domain.MonthCloseService
	asked []string
}

func (s *stubClose) Status(ctx context.Context, month string) (domain.MonthStatus, error) {
	s.asked = append(s.asked, month)
	if _, err := time.Parse("2006-01", month); err != nil {
		return domain.MonthStatus{}, fmt.Errorf("%w: 월 형식이 올바르지 않습니다", contract.ErrValidation)
	}
	p, _ := contract.PrincipalFrom(ctx)
	st := domain.MonthStatus{Month: month, State: domain.MonthOpen}
	switch month {
	case "2026-10":
		amt := int64(123456789)
		st = domain.MonthStatus{Month: month, State: domain.MonthClosed, Version: 2, ClosedAt: time.Date(2026, 11, 3, 6, 5, 0, 0, time.UTC), Warnings: 2}
		if p.Can(contract.PermInventoryValuation) {
			st.CloseAmount = &amt
		}
	case "2026-09":
		st = domain.MonthStatus{Month: month, State: domain.MonthReleasePending, Version: 1, ClosedAt: time.Date(2026, 10, 2, 1, 0, 0, 0, time.UTC)}
	case "2026-08":
		st.Blockers, st.Warnings = 3, 1
	}
	return st, nil
}

func TestGetMonthCloseStatusOutputAndAmountScope(t *testing.T) {
	b, d := newDeps()
	sc := &stubClose{}
	d.MonthClose = sc

	// 금액 권한이 있는 호출자(재무·감사): 기말 금액 합계가 나온다. 마감 일시는 KST 로 보인다.
	for _, who := range []contract.Principal{financeP, auditorP} {
		res, text := call(t, connect(t, d, who), contract.ToolGetMonthCloseStatus, map[string]any{"month": "2026-10"})
		if res.IsError {
			t.Fatal(text)
		}
		m := parseJSON(t, text)
		if m["status"] != "마감됨" || m["version"] != float64(2) || m["closed_at"] != "2026-11-03 15:05" || m["warnings"] != float64(2) || m["blockers"] != float64(0) || m["close_amount"] != float64(123456789) {
			t.Fatalf("%s: %s", who.Name, text)
		}
	}
	// 금액 권한이 없는 호출자(직원·결재자): 금액 키가 응답에 없다.
	for _, who := range []contract.Principal{staffP, managerP} {
		_, text := call(t, connect(t, d, who), contract.ToolGetMonthCloseStatus, map[string]any{"month": "2026-10"})
		if strings.Contains(text, "close_amount") || strings.Contains(text, "123456789") || parseJSON(t, text)["status"] != "마감됨" {
			t.Fatalf("%s 에게 금액이 나감: %s", who.Name, text)
		}
	}
	// 영문 코드·마감자·전표 내용은 나가지 않는다.
	_, text := call(t, connect(t, d, financeP), contract.ToolGetMonthCloseStatus, map[string]any{"month": "2026-10"})
	for _, bad := range []string{"closed", "release_pending", "closed_by", "actor", "doc", "user"} {
		if strings.Contains(text, `"`+bad+`"`) {
			t.Fatalf("나가면 안 되는 키 %q: %s", bad, text)
		}
	}
	// 해제 결재 중·열림(차단 있음) 표시.
	_, text = call(t, connect(t, d, staffP), contract.ToolGetMonthCloseStatus, map[string]any{"month": "2026-09"})
	if m := parseJSON(t, text); m["status"] != "해제 결재 중" || m["version"] != float64(1) {
		t.Fatalf("해제 결재 중: %s", text)
	}
	_, text = call(t, connect(t, d, staffP), contract.ToolGetMonthCloseStatus, map[string]any{"month": "2026-08"})
	if m := parseJSON(t, text); m["status"] != "열림" || m["blockers"] != float64(3) || m["closed_at"] != nil || !strings.Contains(m["message"].(string), "차단 항목이 3건") {
		t.Fatalf("차단 있는 열린 달: %s", text)
	}
	// 기본값은 KST 직전 달이다.
	now := time.Now().In(contract.KST)
	want := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, contract.KST).AddDate(0, -1, 0).Format("2006-01")
	if _, text = call(t, connect(t, d, staffP), contract.ToolGetMonthCloseStatus, map[string]any{}); sc.asked[len(sc.asked)-1] != want || parseJSON(t, text)["month"] != want {
		t.Fatalf("기본 월 %s: asked=%v %s", want, sc.asked, text)
	}
	// 오류: 형식, 서비스 없음.
	for _, bad := range []string{"10월", "2026-13x", "2026/10"} {
		if res, text := call(t, connect(t, d, staffP), contract.ToolGetMonthCloseStatus, map[string]any{"month": bad}); !res.IsError || !strings.Contains(text, "YYYY-MM") {
			t.Fatalf("%q: %v %s", bad, res.IsError, text)
		}
	}
	// 감사: 대상은 월 마감 "status:월" 하나이고 금액·점검 값은 남지 않는다.
	n := 0
	for _, r := range b.Audit.Records() {
		if r.Action == "mcp."+contract.ToolGetMonthCloseStatus {
			n++
			if r.TargetType != audit.TargetMonthClose || !strings.HasPrefix(r.TargetID, "status") || strings.Contains(r.Detail, "123456789") {
				t.Fatalf("감사: %+v", r)
			}
		}
	}
	if n == 0 {
		t.Fatal("감사 기록 없음")
	}
	d.MonthClose = nil
	if res, text := call(t, connect(t, d, staffP), contract.ToolGetMonthCloseStatus, map[string]any{}); !res.IsError || !strings.Contains(text, "설정되지 않았습니다") {
		t.Fatalf("서비스 없음: %s", text)
	}
}
