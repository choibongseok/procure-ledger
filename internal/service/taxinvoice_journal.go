package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/repository"
)

// 전자 세금계산서 확정에서 자동 매입 전표(B43 3단계, D7, 명세 5.4·AC-4320).
//   - 설정 app_settings 의 taxinvoice.auto_journal = "on" 일 때만 한다(기본 꺼짐).
//   - 입고에 이미 취소되지 않은 매입 전표가 있으면 그 전표를 연결할 뿐 새로 만들지 않는다(한 입고 한 전표). 없으면 임시 전표를 만든다(출처 tax_invoice, 출처 번호 = 입고 번호).
//   - 항상 임시다. 승인은 일반 전표와 같은 절차(승인 요청 → 결재, SoD)를 탄다.
//   - 월 마감·계정 규칙 누락·금액 0·거래처 없음이면 전표를 만들지 않고 감사 detail 에 사유를 남긴다(확정 자체는 막지 않는다). 전표 대기 표는 출처 종류가 판정/결의서뿐이라 쓰지 않는다.

const settingAutoJournal = "taxinvoice.auto_journal"

func (s *TaxInvoiceService) autoJournalOn(ctx context.Context, q contract.Tx) bool {
	var v string
	if err := q.QueryRowContext(ctx, `SELECT value FROM app_settings WHERE key = ?`, settingAutoJournal).Scan(&v); err != nil {
		return false
	}
	return v == "on"
}

// autoJournalForReceipt 는 입고 한 건의 임시 매입 전표를 만든다. 만들지 못하면 사유 글자(비면 만들었거나 이미 있음)를 돌려준다.
func (s *TaxInvoiceService) autoJournalForReceipt(ctx context.Context, tx contract.Tx, p contract.Principal, invoiceNo string, receiptID int64) (id int64, reason string, err error) {
	a := autoJournal{Base: s.Base, Repo: s.Ledger}
	if id, _, found, err := s.Ledger.PurchaseJournalForReceipt(ctx, tx, receiptID); err != nil {
		return 0, "", err
	} else if found {
		return id, "", nil
	}
	if why, err := s.autoJournalWhy(ctx, tx, receiptID); err != nil {
		return 0, "", err
	} else if why != "" {
		return 0, why, nil
	}
	facts, err := s.Ledger.ReceiptFacts(ctx, tx, receiptID)
	if err != nil {
		return 0, "", err
	}
	inv, _, ok1, err := a.ruleAccount(ctx, tx, "purchase.inventory", "재고자산")
	if err != nil {
		return 0, "", err
	}
	rate, err := a.vatRateOn(ctx, tx, string(contract.DayOf(facts.ReceivedAt)))
	if err != nil {
		return 0, "", err
	}
	vat := domain.VATFromSupply(facts.Supply, rate)
	vatAcc, _, ok2, err := a.ruleAccount(ctx, tx, "purchase.vat", "부가세대급금")
	if err != nil {
		return 0, "", err
	}
	pay, _, ok3, err := a.ruleAccount(ctx, tx, "purchase.payable", "외상매입금")
	if err != nil {
		return 0, "", err
	}
	if !ok1 || (vat > 0 && !ok2) || !ok3 {
		return 0, "계정 규칙이 없거나 중지됨", nil
	}
	posting := a.today()
	if err := a.assertMonthOpen(ctx, tx, posting); err != nil {
		return 0, "전기일이 마감된 월", nil
	}
	lines := []repository.LineInsert{{AccountID: inv.ID, Side: "D", Amount: facts.Supply}}
	if vat > 0 {
		lines = append(lines, repository.LineInsert{AccountID: vatAcc.ID, Side: "D", Amount: vat})
	}
	lines = append(lines, repository.LineInsert{AccountID: pay.ID, Side: "C", Amount: facts.Supply + vat, VendorID: facts.VendorID})
	memo := truncateRunes(fmt.Sprintf("자동 매입 전표(세금계산서 %s): 발주 %s 입고 %d", invoiceNo, facts.PONo, receiptID), 200)
	no, err := a.insert(ctx, tx, p, repository.JournalInsert{Kind: domain.JournalAutoPurchase, PostingDate: posting, TradeDate: facts.ReceivedAt.In(contract.KST).Format("2006-01-02"),
		Memo: memo, SourceType: "tax_invoice", SourceID: receiptID, Total: facts.Supply + vat}, lines, domain.JournalEvidence{})
	if err != nil {
		return 0, "", err
	}
	if err := s.Audit.Record(ctx, tx, audit.Entry{Actor: p, Action: "journal.auto_purchase", TargetType: audit.TargetJournal, TargetID: no, Result: audit.ResultSuccess, Detail: "auto=true 자동 매입 전표(세금계산서 확정)",
		Diff: []audit.DiffItem{{Key: "source", New: fmt.Sprintf("tax_invoice:%d", receiptID)}, {Key: "supply", New: formatWon(facts.Supply)}, {Key: "vat", New: formatWon(vat)}}}); err != nil {
		return 0, "", err
	}
	id, _, _, err = s.Ledger.JournalBySource(ctx, tx, "tax_invoice", receiptID)
	return id, "", err
}

// autoJournalWhy 는 자동 매입 전표를 지금 만들 수 없는 사유(없으면 빈 글자)다. 만들기 전 점검과 화면 안내가 같은 판정을 쓴다.
func (s *TaxInvoiceService) autoJournalWhy(ctx context.Context, q contract.Tx, receiptID int64) (string, error) {
	a := autoJournal{Base: s.Base, Repo: s.Ledger}
	facts, err := s.Ledger.ReceiptFacts(ctx, q, receiptID)
	if errors.Is(err, contract.ErrNotFound) {
		return "입고를 찾을 수 없습니다", nil
	}
	if err != nil {
		return "", err
	}
	switch {
	case facts.Supply <= 0:
		return "입고 금액이 0원입니다", nil
	case facts.VendorID == 0:
		return "발주에 거래처가 없습니다", nil
	}
	rate, err := a.vatRateOn(ctx, q, string(contract.DayOf(facts.ReceivedAt)))
	if err != nil {
		return "", err
	}
	vat := domain.VATFromSupply(facts.Supply, rate)
	_, _, ok1, err := a.ruleAccount(ctx, q, "purchase.inventory", "재고자산")
	if err != nil {
		return "", err
	}
	_, _, ok2, err := a.ruleAccount(ctx, q, "purchase.vat", "부가세대급금")
	if err != nil {
		return "", err
	}
	_, _, ok3, err := a.ruleAccount(ctx, q, "purchase.payable", "외상매입금")
	if err != nil {
		return "", err
	}
	if !ok1 || (vat > 0 && !ok2) || !ok3 {
		return "전표 계정 규칙이 없거나 중지되었습니다", nil
	}
	if err := a.assertMonthOpen(ctx, q, a.today()); err != nil {
		return "전기일이 속한 달이 마감되었습니다", nil
	}
	return "", nil
}

// fillJournalNote 는 확정된 대조에서 자동 매입 전표가 없는 입고가 있으면 사유 한 줄을 채운다(설정이 켜져 있을 때만). 화면이 재무 담당자에게 알려 주는 용도다.
func (s *TaxInvoiceService) fillJournalNote(ctx context.Context, q contract.Tx, m *domain.TaxMatch) {
	if m.Status != domain.MatchConfirmed || !s.autoJournalOn(ctx, q) {
		return
	}
	for _, r := range m.Receipts {
		if r.JournalNo != "" {
			continue
		}
		why, err := s.autoJournalWhy(ctx, q, r.ReceiptID)
		if err != nil || why == "" {
			continue
		}
		m.JournalNote = "자동 매입 전표를 만들지 못했습니다: " + why + ". 전표를 직접 작성해 연결하세요."
		return
	}
}
