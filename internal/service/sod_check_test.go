package service_test

import (
	"context"
	"sync"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/sod"
)

// B41 ③ 2단계(erp): 지급·전표 판정 지점이 sod.Evaluate 를 같은 트랜잭션에서 부르고, 차단은 롤백 뒤 롤백 밖에 기록되며, 경고는 같은 트랜잭션에 남는지 본다.
// 판정기는 시험용 가짜다(규칙별 모드, force 는 같은 사람이 아니어도 적용해 배관만 시험한다).

type fakeSoD struct {
	mu      sync.Mutex
	mode    map[string]string // 규칙 코드 → warn|block (없으면 off)
	force   bool
	checks  []sod.Check
	blocked []sod.Check
}

func (f *fakeSoD) Evaluate(ctx context.Context, tx contract.Tx, c sod.Check) (sod.Decision, error) {
	f.mu.Lock()
	f.checks = append(f.checks, c)
	f.mu.Unlock()
	if c.Other == 0 || (!f.force && c.Other != c.Actor.UserID) {
		return sod.Decision{Mode: sod.ModeOff}, nil
	}
	switch f.mode[c.Rule] {
	case sod.ModeWarn:
		// 같은 트랜잭션에 쓴 행: 업무가 롤백되면 같이 사라져야 한다.
		if _, err := tx.ExecContext(ctx, `INSERT INTO app_settings (key, value) VALUES (?, '1') ON CONFLICT DO NOTHING`, "sod.test."+c.Rule+"."+c.Subject.No); err != nil {
			return sod.Decision{}, err
		}
		return sod.Decision{Mode: sod.ModeWarn, Violation: true}, nil
	case sod.ModeBlock:
		return sod.Decision{Mode: sod.ModeBlock, Violation: true}, &sod.BlockedError{Check: c}
	}
	return sod.Decision{Mode: sod.ModeOff}, nil
}

func (f *fakeSoD) RecordBlocked(_ context.Context, c sod.Check) {
	f.mu.Lock()
	f.blocked = append(f.blocked, c)
	f.mu.Unlock()
}

func (f *fakeSoD) rules(rule string) []sod.Check {
	var out []sod.Check
	for _, c := range f.checks {
		if c.Rule == rule {
			out = append(out, c)
		}
	}
	return out
}

func (e *env) settledCount(t *testing.T) int {
	t.Helper()
	var n int
	if err := e.db.QueryRow(`SELECT COUNT(*) FROM payments`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func (e *env) testRows(t *testing.T) int {
	t.Helper()
	var n int
	if err := e.db.QueryRow(`SELECT COUNT(*) FROM app_settings WHERE key LIKE 'sod.test.%'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// 회귀(B32 D8): 판정기를 꽂지 않아도 지급 요청 작성자의 지급 처리는 막힌다(locked 규칙은 내장 판정). 아무것도 바뀌지 않는다.
func TestSoDLockedPayCreateSettleStaysBlockedWithoutEvaluator(t *testing.T) {
	e := newEnv(t)
	e.payForm(t)
	v, bank := e.vendor(t), e.bank(t, "주거래")
	pid := e.payable(t, v.ID, 100_000)
	p := e.requested(t, e.payInput(v, bank.ID, domain.PaymentItemInput{PayableID: pid, Amount: 100_000}))
	e.approvePay(t, p)
	_, err := e.payments.Settle(as(uFinance), p.No, domain.SettleInput{}) // uFinance 가 작성자
	if _, ok := sod.IsBlocked(err); !ok || !isErr(err, contract.ErrForbidden) {
		t.Fatalf("작성자의 지급 처리가 차단되어야 함: %v", err)
	}
	if e.settledCount(t) != 0 {
		t.Fatal("차단됐는데 지급 행이 생김")
	}
	if got, err := e.payments.Settle(as(uFinance2), p.No, domain.SettleInput{}); err != nil || got.Status != domain.PaymentPaid {
		t.Fatalf("다른 재무 담당자는 처리할 수 있어야 함: %v %+v", err, got)
	}
}

// 판정 지점은 앞 단계 행위자를 같은 트랜잭션에서 읽어 Other 로 넘긴다: 지급 처리(작성자·승인자), 지급 승인 훅(작성자), 전표 승인 훅(작성자).
func TestSoDChecksCarryPriorActors(t *testing.T) {
	f := &fakeSoD{}
	e := newEnvSoD(t, f)
	e.payForm(t)
	v, bank := e.vendor(t), e.bank(t, "주거래")
	pid := e.payable(t, v.ID, 100_000)
	if cs := f.rules(sod.RuleJVCreateApprove); len(cs) == 0 || cs[0].Other != uFinance || cs[0].Actor.UserID != uFinance2 || cs[0].Subject.Type != "journal" || cs[0].Subject.No == "" {
		t.Fatalf("전표 승인 판정: %+v", cs)
	}
	p := e.requested(t, e.payInput(v, bank.ID, domain.PaymentItemInput{PayableID: pid, Amount: 100_000}))
	e.approvePay(t, p)
	if cs := f.rules(sod.RulePayCreateApprove); len(cs) != 1 || cs[0].Other != uFinance || cs[0].Actor.UserID != uFinance2 || cs[0].Subject != (sod.Ref{Type: "payment", ID: cs[0].Subject.ID, No: p.No}) {
		t.Fatalf("지급 최종 승인 판정: %+v", cs)
	}
	if _, err := e.payments.Settle(as(uFinance2), p.No, domain.SettleInput{}); err != nil {
		t.Fatal(err)
	}
	cs, as2 := f.rules(sod.RulePayCreateSettle), f.rules(sod.RulePayApproveSettle)
	if len(cs) != 1 || cs[0].Other != uFinance || cs[0].Actor.UserID != uFinance2 || len(as2) != 1 || as2[0].Other != uFinance2 || as2[0].Subject.No != p.No {
		t.Fatalf("지급 처리 판정: %+v / %+v", cs, as2)
	}
	if len(f.blocked) != 0 {
		t.Fatalf("차단 기록이 있다: %+v", f.blocked)
	}
}

// 차단: 같은 트랜잭션에서 거부되어 지급 전체가 롤백되고, 롤백 뒤에 한 번만 롤백 밖에 기록된다.
func TestSoDBlockRollsBackAndRecordsOnce(t *testing.T) {
	f := &fakeSoD{mode: map[string]string{sod.RulePayApproveSettle: sod.ModeBlock}}
	e := newEnvSoD(t, f)
	e.payForm(t)
	v, bank := e.vendor(t), e.bank(t, "주거래")
	pid := e.payable(t, v.ID, 100_000)
	p := e.requested(t, e.payInput(v, bank.ID, domain.PaymentItemInput{PayableID: pid, Amount: 100_000}))
	e.approvePay(t, p)
	paid0, reserved0 := e.payRow(t, pid)
	_, err := e.payments.Settle(as(uFinance2), p.No, domain.SettleInput{}) // 승인자 = 처리자
	c, ok := sod.IsBlocked(err)
	if !ok || !isErr(err, contract.ErrForbidden) || c.Rule != sod.RulePayApproveSettle {
		t.Fatalf("차단되어야 함: %v", err)
	}
	if e.settledCount(t) != 0 {
		t.Fatal("차단됐는데 지급 행이 남음")
	}
	if paid, reserved := e.payRow(t, pid); paid != paid0 || reserved != reserved0 {
		t.Fatalf("차단됐는데 채무 누계가 바뀜: %d/%d -> %d/%d", paid0, reserved0, paid, reserved)
	}
	if len(f.blocked) != 1 || f.blocked[0].Rule != sod.RulePayApproveSettle || f.blocked[0].Actor.UserID != uFinance2 || f.blocked[0].Subject.No != p.No {
		t.Fatalf("롤백 밖 기록: %+v", f.blocked)
	}
}

// 경고: 같은 트랜잭션에 위반 기록을 남기고 통과한다. 지급이 롤백되면 위반 기록도 사라진다.
func TestSoDWarnIsInSameTransaction(t *testing.T) {
	f := &fakeSoD{mode: map[string]string{sod.RulePayApproveSettle: sod.ModeWarn}}
	e := newEnvSoD(t, f)
	e.payForm(t)
	v, bank := e.vendor(t), e.bank(t, "주거래")
	pid := e.payable(t, v.ID, 100_000)
	p := e.requested(t, e.payInput(v, bank.ID, domain.PaymentItemInput{PayableID: pid, Amount: 100_000}))
	e.approvePay(t, p)
	// 판정 뒤에 실패하는 지급(지급일이 미래): 경고 기록도 롤백된다.
	if _, err := e.payments.Settle(as(uFinance2), p.No, domain.SettleInput{PaidDate: "2999-01-01"}); err == nil {
		t.Fatal("미래 지급일이 거부되어야 함")
	}
	if e.testRows(t) != 0 {
		t.Fatalf("롤백됐는데 경고 기록이 남음: %d", e.testRows(t))
	}
	if len(f.blocked) != 0 {
		t.Fatal("경고는 차단 기록을 만들지 않는다")
	}
	got, err := e.payments.Settle(as(uFinance2), p.No, domain.SettleInput{})
	if err != nil || got.Status != domain.PaymentPaid {
		t.Fatalf("경고 규칙은 통과해야 함: %v %+v", err, got)
	}
	if e.testRows(t) != 1 {
		t.Fatalf("경고 기록 %d건(같은 트랜잭션에 1건이어야 함)", e.testRows(t))
	}
}

// 결재 후속 훅: 차단이면 최종 승인 전체가 롤백된다(지급 건·전표가 승인되지 않고 결재 문서도 승인되지 않는다).
func TestSoDBlockInApprovalHooksRollsBackApproval(t *testing.T) {
	f := &fakeSoD{force: true, mode: map[string]string{sod.RulePayCreateApprove: sod.ModeBlock}}
	e := newEnvSoD(t, f)
	e.payForm(t)
	v, bank := e.vendor(t), e.bank(t, "주거래")
	pid := e.payable(t, v.ID, 100_000)
	p := e.requested(t, e.payInput(v, bank.ID, domain.PaymentItemInput{PayableID: pid, Amount: 100_000}))
	_, err := e.approval.Decide(as(uFinance2), p.DocID, domain.Decision{Approve: true, Comment: "확인"})
	if err == nil {
		_, err = e.approval.Agree(as(uFinance2), p.DocID, domain.Decision{Approve: true, Comment: "확인"})
	}
	if _, ok := sod.IsBlocked(err); !ok {
		t.Fatalf("지급 승인이 차단되어야 함: %v", err)
	}
	got, gerr := e.payments.Get(as(uFinance), p.No)
	if gerr != nil || got.Status != domain.PaymentRequested {
		t.Fatalf("차단됐는데 지급 건 상태가 바뀜: %+v %v", got, gerr)
	}
	if len(f.blocked) != 1 || f.blocked[0].Rule != sod.RulePayCreateApprove {
		t.Fatalf("롤백 밖 기록: %+v", f.blocked)
	}
}

func TestSoDBlockInJournalApprovalHookRollsBackApproval(t *testing.T) {
	f := &fakeSoD{force: true, mode: map[string]string{sod.RuleJVCreateApprove: sod.ModeBlock}}
	e := newEnvSoD(t, f)
	e.jvForm(t)
	v := e.vendor(t)
	j, err := e.journals.RequestApproval(as(uFinance), e.purchaseJV(t, v.ID, 50_000).No)
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.approval.Decide(as(uFinance2), j.DocID, domain.Decision{Approve: true, Comment: "확인"})
	if err == nil {
		_, err = e.approval.Agree(as(uFinance2), j.DocID, domain.Decision{Approve: true, Comment: "확인"})
	}
	if _, ok := sod.IsBlocked(err); !ok {
		t.Fatalf("전표 승인이 차단되어야 함: %v", err)
	}
	got, gerr := e.journals.Get(as(uFinance), j.No)
	if gerr != nil || got.Status != domain.JournalRequested {
		t.Fatalf("차단됐는데 전표 상태가 바뀜: %+v %v", got, gerr)
	}
	if len(f.blocked) != 1 || f.blocked[0].Rule != sod.RuleJVCreateApprove || f.blocked[0].Subject.No != j.No {
		t.Fatalf("롤백 밖 기록: %+v", f.blocked)
	}
}
