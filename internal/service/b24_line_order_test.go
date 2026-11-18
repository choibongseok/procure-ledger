package service_test

import (
	"strings"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/contract/domain"
)

// headApproversFirst(조직장 결재 단계를 앞으로) 정렬은 기본 양식의 결재선 순서를 바꾸지 않는다: 일반 기안서·협조전·지출 결의서·구매 품의서.
func TestExistingFormLineOrderUnchanged(t *testing.T) {
	e := newEnv(t)
	kinds := func(pv domain.LinePreview) string {
		var out []string
		for _, s := range pv.Steps {
			if s.Omitted {
				continue
			}
			out = append(out, string(s.Kind)+"/"+s.Role)
		}
		return strings.Join(out, ",")
	}
	cases := []struct {
		name string
		in   domain.DocLineInput
		want string
	}{
		{"일반 기안서", domain.DocLineInput{FormCode: "GEN"}, "team_lead/approve,dept_head/approve"},
		{"업무 협조전", domain.DocLineInput{FormCode: "COOP", FieldUnit: unitSales1}, "team_lead/approve,dept_head/approve,agree/agree"},
		{"지출 결의서 3천만 원 이상", domain.DocLineInput{FormCode: "EXP", Amount: 35_000_000}, "team_lead/approve,dept_head/approve,finance/approve"},
	}
	for _, c := range cases {
		pv, err := e.docs().PreviewLine(as(uStaff), c.in)
		if err != nil || !pv.Complete {
			t.Fatalf("%s: %+v %v", c.name, pv.Problems, err)
		}
		if got := kinds(pv); got != c.want {
			t.Fatalf("%s 결재선 순서가 바뀜: %s (기대 %s)", c.name, got, c.want)
		}
		for i, s := range pv.Steps {
			if s.Order != i+1 {
				t.Fatalf("%s 단계 번호: %+v", c.name, pv.Steps)
			}
		}
	}
	// 구매 품의서(금액 구간 규칙): 팀장 → 부서장 → 재무.
	pv, err := e.liner().PreviewLine(as(uStaff), domain.LinePreviewInput{Amount: 20_000_000})
	if err != nil || kinds(pv) != "team_lead/,dept_head/,finance/" { // 구매 품의서 단계는 역할 표시가 비어 있다(기존 동작 그대로)
		t.Fatalf("구매 품의서: %s %v", kinds(pv), err)
	}
	// 구매팀장이 기본 단계로 들어가는 재고 조정 전표만 조직장 단계가 맨 앞이다.
	e.adjForm(t)
	pv, err = e.docs().PreviewLine(as(uFinance), domain.DocLineInput{FormCode: "ADJ"})
	if err != nil || !pv.Complete || len(pv.Steps) != 2 || pv.Steps[0].ApproverID != uHead || pv.Steps[1].Kind != "finance" || pv.Steps[0].Order != 1 || pv.Steps[1].Order != 2 {
		t.Fatalf("재고 조정 전표 결재선: %+v %v", pv.Steps, err)
	}
}
