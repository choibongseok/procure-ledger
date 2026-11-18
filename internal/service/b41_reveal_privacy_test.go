package service_test

import (
	"context"
	"errors"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/privacy"
)

// 시험용 Noter: 같은 트랜잭션에서 불린 기록을 모으고, 실패를 주입할 수 있다.
type noteSink struct {
	notes []privacy.Access
	fail  error
}

func (n *noteSink) Note(ctx context.Context, p contract.Principal, a privacy.Access) error {
	return n.NoteTx(ctx, nil, p, a)
}
func (n *noteSink) NoteTx(_ context.Context, _ contract.Tx, _ contract.Principal, a privacy.Access) error {
	if n.fail != nil {
		return n.fail
	}
	n.notes = append(n.notes, a)
	return nil
}

// B41 AC-4102: 계좌 원문 보기(RevealAccount)는 개인정보 조회 기록을 같은 트랜잭션에서 남기고, 기록하지 못하면 원문을 주지 않는다.
func TestRevealAccountNotesPrivacyAccess(t *testing.T) {
	e := newEnv(t)
	sink := &noteSink{}
	e.funds.Privacy = sink
	bank := e.bank(t, "주거래")
	raw, err := e.funds.RevealAccount(as(uFinance), bank.ID, "account")
	if err != nil || raw == "" {
		t.Fatalf("원문 보기: %q %v", raw, err)
	}
	if len(sink.notes) != 1 {
		t.Fatalf("조회 기록 %d건", len(sink.notes))
	}
	a := sink.notes[0]
	if a.Category != privacy.CatAccount || a.Screen != privacy.ScreenAccountReveal || len(a.Subjects) != 1 || a.Subjects[0].Type != privacy.SubjectCompany || a.Subjects[0].ID != bank.ID {
		t.Errorf("조회 기록 내용: %+v", a)
	}
	// 기록 실패: 원문이 나가지 않는다(감사 기록도 같은 트랜잭션이라 함께 취소된다).
	sink.fail = errors.New("주입")
	if raw, err := e.funds.RevealAccount(as(uFinance), bank.ID, "account"); err == nil || raw != "" {
		t.Fatalf("조회 기록 실패인데 원문이 나감: %q %v", raw, err)
	}
	before := len(e.rec.Records())
	sink.fail = nil
	if _, err := e.funds.RevealAccount(as(uFinance), bank.ID, "account"); err != nil {
		t.Fatal(err)
	}
	if len(e.rec.Records()) != before+1 {
		t.Errorf("실패한 시도의 감사가 남아 있음(롤백되지 않음)")
	}
}
