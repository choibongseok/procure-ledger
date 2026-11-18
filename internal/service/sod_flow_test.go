package service_test

import (
	"strings"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/sod"
)

// B41 ③ 3단계(erp): 입고 확인·카드 소명 확인의 판정 지점과, 서로 다른 사람의 업무 흐름 회귀(모든 규칙이 차단이어도 통과).

// 입고 확인: 앞 단계 행위자는 그 발주의 원 구매요청 기안자다. 차단은 입고 전체를 롤백하고 롤백 밖에 기록되며, 경고는 같은 트랜잭션에 남는다.
func TestSoDReceiveCarriesRequesterBlockAndWarn(t *testing.T) {
	f := &fakeSoD{force: true, mode: map[string]string{sod.RulePORequestReceive: sod.ModeBlock}}
	e := newEnvSoD(t, f)
	v := e.poVendor(t, "입고상사", "777-77-77777")
	po := e.orderFor(t, v, "2026-12-01", 1_000, 5) // 기안자 uStaff
	_, err := e.inventory.Receive(as(uFinance), recv(po.ID, po.Lines[0].ID, 5, "", ""))
	if c, ok := sod.IsBlocked(err); !ok || c.Rule != sod.RulePORequestReceive || c.Other != uStaff || c.Actor.UserID != uFinance || c.Subject.Type != "purchase_request" || c.Subject.ID != po.PRID {
		t.Fatalf("입고가 차단되어야 함(Other=기안자): %v", err)
	}
	var n int
	if err := e.db.QueryRow(`SELECT COUNT(*) FROM receipts`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("차단됐는데 입고 행이 있음: %d %v", n, err)
	}
	if len(f.blocked) != 1 || f.blocked[0].Rule != sod.RulePORequestReceive {
		t.Fatalf("롤백 밖 기록: %+v", f.blocked)
	}
	// 경고: 통과하고 같은 트랜잭션에 기록. 뒤 단계(초과 입고)가 실패하면 기록도 사라진다.
	f.mode[sod.RulePORequestReceive] = sod.ModeWarn
	f.blocked = nil
	if _, err := e.inventory.Receive(as(uFinance), recv(po.ID, po.Lines[0].ID, 99, "", "")); err == nil {
		t.Fatal("초과 입고가 거부되어야 함")
	}
	if e.testRows(t) != 0 {
		t.Fatalf("롤백됐는데 경고 기록이 남음: %d", e.testRows(t))
	}
	if _, err := e.inventory.Receive(as(uFinance), recv(po.ID, po.Lines[0].ID, 5, "", "")); err != nil {
		t.Fatalf("경고 규칙은 통과해야 함: %v", err)
	}
	if e.testRows(t) != 1 {
		t.Fatalf("경고 기록 %d건", e.testRows(t))
	}
}

// 카드 소명 확인: 소지자 본인이 자기 소명을 확인하려 하면 규칙 모드대로. 판정기가 없어도(내장 판정) 예전처럼 막힌다.
func (e *env) leadCard(t *testing.T) domain.CardTxn {
	t.Helper()
	if _, err := e.cards.SaveCard(as(uFinance), 0, domain.CardInput{Alias: "팀장 법인카드", Issuer: domain.CardFormatA, Last4: "7890", HolderID: uLead}); err != nil {
		t.Fatalf("카드 등록: %v", err)
	}
	e.importCards(t, cardRow("2026-11-08 12:00:00", "팀장가맹점", 30_000, "L1", "승인"))
	txn := e.txnByApproval(t, "L1", "approve")
	d := true
	if _, err := e.cards.Explain(as(uLead), txn.ID, domain.ExplainInput{Purpose: "고객사 미팅 다과 구입 비용입니다", AccountCode: "5220", VATDeduct: &d}); err != nil {
		t.Fatalf("소명: %v", err)
	}
	return txn
}

func TestSoDCardHolderConfirmWithoutEvaluatorStaysBlocked(t *testing.T) {
	e := newEnv(t)
	txn := e.leadCard(t)
	res, err := e.cards.Confirm(as(uLead), []int64{txn.ID}) // 소지자 본인 = 확인자
	if err != nil || len(res) != 1 || res[0].OK || !strings.Contains(res[0].Error, "모두 할 수 없습니다") {
		t.Fatalf("본인 소명 확인이 막혀야 함: %+v %v", res, err)
	}
	if got, err := e.cards.GetTxn(as(uFinance), txn.ID); err != nil || got.Status != domain.CardTxnSubmitted {
		t.Fatalf("상태가 바뀜: %+v %v", got, err)
	}
}

func TestSoDCardHolderConfirmModes(t *testing.T) {
	f := &fakeSoD{mode: map[string]string{sod.RuleCardHolderConfirm: sod.ModeBlock}}
	e := newEnvSoD(t, f)
	txn := e.leadCard(t)
	res, _ := e.cards.Confirm(as(uLead), []int64{txn.ID})
	if len(res) != 1 || res[0].OK {
		t.Fatalf("차단되어야 함: %+v", res)
	}
	if len(f.blocked) != 1 || f.blocked[0].Rule != sod.RuleCardHolderConfirm || f.blocked[0].Other != uLead || f.blocked[0].Actor.UserID != uLead || f.blocked[0].Subject.Type != "card_txn" {
		t.Fatalf("롤백 밖 기록: %+v", f.blocked)
	}
	// 관리자가 경고로 낮추면 소지자 본인 확인이 통과하고 같은 트랜잭션에 위반이 남는다(D10: locked 가 아니다).
	f.mode[sod.RuleCardHolderConfirm] = sod.ModeWarn
	res, _ = e.cards.Confirm(as(uLead), []int64{txn.ID})
	if len(res) != 1 || !res[0].OK {
		t.Fatalf("경고 모드는 통과해야 함: %+v", res)
	}
	if e.testRows(t) != 1 {
		t.Fatalf("경고 기록 %d건", e.testRows(t))
	}
}

// 서로 다른 사람의 업무 흐름(구매요청→결재→발주→입고→전표→지급)은 모든 규칙이 차단이어도 그대로 통과하고, 각 판정이 앞 단계 행위자를 정확히 넘긴다.
func TestSoDFullFlowWithDistinctPeopleIsNotBlocked(t *testing.T) {
	block := map[string]string{}
	for _, r := range sod.Rules {
		if r.Kind == sod.KindTxn {
			block[r.Code] = sod.ModeBlock
		}
	}
	f := &fakeSoD{mode: block}
	e := newEnvSoD(t, f)
	e.payForm(t)
	v, bank := e.vendor(t), e.bank(t, "주거래")
	po := e.orderFor(t, v, "2026-12-01", 1_000, 5)                                                           // 기안 uStaff, 승인 uLead
	e.receiveAll(t, po)                                                                                      // 입고 uFinance
	pid := e.payable(t, v.ID, 30_000_000)                                                                    // 전표 작성 uFinance, 승인 uFinance2
	p := e.requested(t, e.payInput(v, bank.ID, domain.PaymentItemInput{PayableID: pid, Amount: 30_000_000})) // 작성 uFinance
	e.approvePay(t, p, uFinance2, uHead)                                                                     // 마지막 승인자 uHead
	if _, err := e.payments.Settle(as(uFinance2), p.No, domain.SettleInput{}); err != nil {                  // 처리 uFinance2
		t.Fatalf("서로 다른 사람의 흐름은 통과해야 함: %v", err)
	}
	if len(f.blocked) != 0 {
		t.Fatalf("차단 기록이 있다: %+v", f.blocked)
	}
	want := map[string][2]int64{ // 규칙 → {앞 단계 행위자, 행위자}
		sod.RulePORequestReceive: {uStaff, uFinance},
		sod.RuleJVCreateApprove:  {uFinance, uFinance2},
		sod.RulePayCreateApprove: {uFinance, uHead},
		sod.RulePayCreateSettle:  {uFinance, uFinance2},
		sod.RulePayApproveSettle: {uHead, uFinance2},
	}
	for rule, w := range want {
		cs := f.rules(rule)
		if len(cs) == 0 {
			t.Errorf("%s 판정이 호출되지 않음", rule)
			continue
		}
		last := cs[len(cs)-1]
		if last.Other != w[0] || last.Actor.UserID != w[1] {
			t.Errorf("%s: Other=%d Actor=%d (기대 %d/%d)", rule, last.Other, last.Actor.UserID, w[0], w[1])
		}
	}
}
