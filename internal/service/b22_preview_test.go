package service_test

import (
	"errors"
	"reflect"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
)

// B22 5.1: LineService.PreviewLine 이 FormCode·FieldUnit 으로 양식 문서의 결재선도 같은 입구에서 돌려주고, 구매 품의서 경로는 그대로다.
func TestLinePreviewAcceptsFormCode(t *testing.T) {
	e := newEnv(t)
	ln := e.liner()
	// 양식 문서: 양식 문서 서비스의 미리보기와 같은 결과(AC-24 와 같은 취지).
	want, err := e.docs().PreviewLine(as(uStaff), domain.DocLineInput{FormCode: "COOP", FieldUnit: unitSales1})
	if err != nil || !want.Complete {
		t.Fatalf("기준 미리보기: %+v %v", want.Problems, err)
	}
	got, err := ln.PreviewLine(as(uStaff), domain.LinePreviewInput{FormCode: "COOP", FieldUnit: unitSales1})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("같은 입구인데 결과가 다름:\n%+v\n%+v", got, want)
	}
	roles := map[string]bool{}
	for _, s := range got.Steps {
		roles[s.Role] = true
	}
	if !roles["approve"] || !roles["agree"] {
		t.Fatalf("역할이 없음: %+v", got.Steps)
	}
	// 부서를 안 주면 오류가 아니라 Complete=false 와 문제 문장이다.
	pv, err := ln.PreviewLine(as(uStaff), domain.LinePreviewInput{FormCode: "COOP"})
	if err != nil || pv.Complete || len(pv.Problems) == 0 {
		t.Fatalf("부서 미선택: %+v %v", pv, err)
	}
	// 본인 것만: 다른 요청자는 거부, 권한 없는 admin·auditor 는 서비스의 거부를 그대로 받는다. 오늘 기준만.
	if _, err := ln.PreviewLine(as(uStaff), domain.LinePreviewInput{FormCode: "COOP", FieldUnit: unitSales1, RequesterID: uStaff2}); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("타인 요청자: %v", err)
	}
	for _, who := range []int64{uAdmin, uAuditor} {
		if _, err := ln.PreviewLine(as(who), domain.LinePreviewInput{FormCode: "COOP", FieldUnit: unitSales1}); !errors.Is(err, contract.ErrForbidden) {
			t.Fatalf("사용자 %d: %v", who, err)
		}
	}
	if _, err := ln.PreviewLine(as(uStaff), domain.LinePreviewInput{FormCode: "COOP", FieldUnit: unitSales1, AsOf: "2026-01-01"}); !errors.Is(err, contract.ErrValidation) {
		t.Fatalf("과거 기준일: %v", err)
	}
	// 구매 품의서: 코드가 없거나 PUR 이면 기존 결과와 같다.
	base, err := ln.PreviewLine(as(uStaff), domain.LinePreviewInput{Amount: 500_000})
	if err != nil {
		t.Fatal(err)
	}
	pur, err := ln.PreviewLine(as(uStaff), domain.LinePreviewInput{Amount: 500_000, FormCode: "PUR"})
	if err != nil || !reflect.DeepEqual(base, pur) {
		t.Fatalf("PUR 경로가 달라짐: %v", err)
	}
	if _, err := ln.PreviewLine(as(uStaff), domain.LinePreviewInput{Amount: 0}); !errors.Is(err, contract.ErrValidation) {
		t.Fatalf("금액 0: %v", err)
	}
}

// B22 8.1: 소멸 목록 행이 역할·단계·양식을 알려 주고 구매 품의서와 양식 문서를 모두 낸다.
func TestOrphanListCarriesFormRoleAndStage(t *testing.T) {
	e := newEnv(t)
	doc := e.genSubmitted(t, uStaff, "소멸 양식 문서", domain.LineEdit{Role: "agree", TargetType: "user", TargetID: uLead3, Group: 1},
		domain.LineEdit{Role: "agree", TargetType: "user", TargetID: uFinance, Group: 1})
	pur := e.submitted(t, uStaff, 500_000)
	e.deactivate(t, uLead)
	e.deactivate(t, uLead3)
	orphans, err := e.liner().ListOrphanedSteps(as(uAdmin), "")
	if err != nil {
		t.Fatal(err)
	}
	byKey := map[string]domain.OrphanStep{}
	for _, o := range orphans {
		byKey[o.PRTitle+"/"+o.Role] = o
	}
	d, ok := byKey["소멸 양식 문서/approve"]
	if ok && d.RequesterName != "직원" {
		t.Fatalf("요청자 이름: %+v", d)
	}
	if !ok || d.FormCode != "GEN" || d.FormName != "일반 기안서" || d.FormKind != "general" || d.Stage != 1 || d.Reason != "퇴사" || !d.Reassignable {
		t.Fatalf("양식 문서 결재 단계: %+v", orphans)
	}
	p, ok := byKey[pur.Title+"/approve"]
	if !ok || p.FormCode != "PUR" || p.FormName != "구매 품의서" || p.FormKind != "purchase" {
		t.Fatalf("구매 품의서 결재 단계: %+v", orphans)
	}
	// 합의 단계는 앞 결재가 끝난 뒤에야 열려 있는 단계가 된다: 결재가 안 끝나도 열린 합의 단계(대기)는 목록 대상이다.
	a, ok := byKey["소멸 양식 문서/agree"]
	if !ok || a.Title != "합의" || a.FormCode != "GEN" || a.Stage < 2 || a.Reason != "퇴사" || !a.Reassignable || a.ApproverID != uLead3 {
		t.Fatalf("합의 단계: %+v", orphans)
	}
	_ = doc
}
