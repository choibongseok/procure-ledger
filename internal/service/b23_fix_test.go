package service_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
)

// AC-211: 수신 건은 호출자가 처리할 수 있는지(CanHandle)를 서비스가 알려 주고, 도착 알림 문구는 조사를 붙여 쓴다.
func TestReceiptCanHandleAndNotificationWording(t *testing.T) {
	e := newEnv(t)
	_, rc, _ := e.stage2()
	pr := e.genSubmitted(t, uStaff, "수신 표시", domain.LineEdit{Role: "receive", TargetType: "unit", TargetID: unitSales1})
	decideAs(t, e, uLead, pr.ID, true, "")
	decideAs(t, e, uHead, pr.ID, true, "")
	list, err := rc.List(as(uStaff), domain.DocReceiptFilter{DocID: pr.ID})
	if err != nil || len(list) != 1 || list[0].CanHandle {
		t.Fatalf("기안자는 수신 처리 권한이 없음: %+v %v", list, err)
	}
	list, err = rc.List(as(uLead2), domain.DocReceiptFilter{DocID: pr.ID})
	if err != nil || len(list) != 1 || !list[0].CanHandle {
		t.Fatalf("수신처 조직장은 처리할 수 있음: %+v %v", list, err)
	}
	notes, _ := e.notification.List(as(uLead2), domain.NotificationFilter{})
	found := false
	for _, n := range notes {
		if strings.Contains(n.Body, "영업1팀에 도착했습니다. 접수해 주세요.") {
			found = true
		}
		if strings.Contains(n.Body, " 에 ") || strings.Contains(n.Body, " 에서 ") {
			t.Fatalf("조사 앞에 공백: %s", n.Body)
		}
	}
	if !found {
		t.Fatalf("도착 알림 문구: %+v", notes)
	}
	if _, err := rc.Accept(as(uLead2), list[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := rc.Return(as(uLead2), list[0].ID, "관할 아님"); err != nil {
		t.Fatal(err)
	}
	notes, _ = e.notification.List(as(uStaff), domain.NotificationFilter{})
	for _, n := range notes {
		if strings.Contains(n.Body, "반송") && !strings.Contains(n.Body, "영업1팀에서 반송되었습니다") {
			t.Fatalf("반송 알림 문구: %s", n.Body)
		}
	}
}

// AC-212: 수신 담당자는 그 조직과 하위 조직의 재직 구성원 중에서 지정한다(담당자 지정과 같은 규칙). 퇴사자는 목록·지정에서 빠진다.
func TestReceiverRuleIncludesSubUnits(t *testing.T) {
	e := newEnv(t)
	_, rc, _ := e.stage2()
	// 구매팀(부서) 조직장은 부서장 한 명이고 직원은 하위 구매1팀에 있다.
	if err := rc.AddReceiver(as(uHead), unitBuy, uStaff); err != nil {
		t.Fatalf("하위 조직 구성원을 수신 담당자로: %v", err)
	}
	if err := rc.AddReceiver(as(uHead), unitBuy, uStaff2); !errors.Is(err, contract.ErrValidation) {
		t.Fatalf("다른 조직 소속은 거부: %v", err)
	}
	list, err := rc.Receivers(as(uHead), unitBuy)
	if err != nil || len(list) != 1 || list[0].UserID != uStaff {
		t.Fatalf("목록: %+v %v", list, err)
	}
	e.deactivate(t, uStaff)
	if list, _ = rc.Receivers(as(uHead), unitBuy); len(list) != 0 {
		t.Fatalf("퇴사자가 목록에 남음: %+v", list)
	}
	if err := rc.AddReceiver(as(uHead), unitBuy, uStaff); !errors.Is(err, contract.ErrValidation) {
		t.Fatalf("퇴사자 지정은 거부: %v", err)
	}
}

// 전결 규정 관리: 금액 필드가 없는 양식(구매 품의서 포함)에는 규정을 둘 수 없고, 목록은 근거 규정 문서의 한국어 제목을 함께 돌려준다.
func TestAuthorityRuleRejectsFormWithoutAmountFieldAndShowsTitle(t *testing.T) {
	e := newEnv(t)
	a := e.authority()
	a.PolicyDocs = func() []domain.PolicyRef {
		return []domain.PolicyRef{{Slug: "approval-authority", Title: "전결 규정"}}
	}
	for _, code := range []string{"PUR", "GEN", "COOP"} {
		_, err := a.Save(as(uAdmin), domain.AuthorityRuleInput{FormCode: code, MinAmount: 0, FinalLevel: 1, Active: true})
		if !errors.Is(err, contract.ErrValidation) || !strings.Contains(err.Error(), "금액 필드가 없어 전결 규정을 둘 수 없습니다") {
			t.Fatalf("%s: %v", code, err)
		}
	}
	rules, err := a.List(as(uAdmin))
	if err != nil || len(rules) != 1 || rules[0].PolicyTitle != "전결 규정" {
		t.Fatalf("제목: %+v %v", rules, err)
	}
	pol, err := a.Policies(as(uAdmin))
	if err != nil || len(pol) != 1 || pol[0].Title != "전결 규정" {
		t.Fatalf("규정 문서 목록: %+v %v", pol, err)
	}
	if _, err := a.Policies(as(uStaff)); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("권한: %v", err)
	}
}
