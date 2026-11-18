package service

import (
	"context"
	"fmt"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/repository"
)

// 카드 비용 전표(B32 3.3.6): 부서장이 소명을 확인하면 같은 트랜잭션에서 임시 전표를 만든다.
//   차변 소명 계정(공급가, 불공제면 합계) / 차변 부가세대급금 1310(공제 부가세, 있을 때) / 대변 미지급금 2120(승인금액, 거래처 없음).
//   전기일 = 확인일(KST), 거래일 = 사용일, 출처 card_txn(source_id = 소명 번호: 같은 소명에 취소되지 않은 전표는 하나, 역분개 뒤 다시 소명·확인하면 새 소명의 전표다).
//   규칙 계정(card.payable·card.vat)이 중지됐거나 전기일이 마감된 달이면 전표 대기로 올리고 확인은 성공한다.

type cardExpenseInput struct {
	explanationID int64
	txnID         int64
	accountID     int64
	supply, vat   int64
	total         int64
	deduct        bool
	merchant      string
	cardAlias     string
	tradeDate     string
}

// cardExpense 는 임시 카드 비용 전표를 만들거나 전표 대기에 올린다.
func (a autoJournal) cardExpense(ctx context.Context, tx contract.Tx, p contract.Principal, in cardExpenseInput) (domain.JournalOutcome, error) {
	total := in.total
	row := repository.QueueRow{SourceType: "card_txn", SourceID: in.explanationID, Amount: &total}
	if _, no, found, err := a.Repo.JournalBySource(ctx, tx, "card_txn", in.explanationID); err != nil {
		return domain.JournalOutcome{}, err
	} else if found {
		return domain.JournalOutcome{JournalNo: no}, nil
	}
	exp, found, err := a.Repo.AccountByID(ctx, tx, in.accountID)
	if err != nil {
		return domain.JournalOutcome{}, err
	}
	if !found || !exp.Active {
		return a.enqueue(ctx, tx, p, row, domain.QueueRuleAccount, "소명 계정")
	}
	pay, payName, ok, err := a.ruleAccount(ctx, tx, "card.payable", "미지급금")
	if err != nil {
		return domain.JournalOutcome{}, err
	}
	if !ok {
		return a.enqueue(ctx, tx, p, row, domain.QueueRuleAccount, payName)
	}
	var vatAcc domain.Account
	if in.deduct && in.vat > 0 {
		v, vatName, vok, err := a.ruleAccount(ctx, tx, "card.vat", "부가세대급금")
		if err != nil {
			return domain.JournalOutcome{}, err
		}
		if !vok {
			return a.enqueue(ctx, tx, p, row, domain.QueueRuleAccount, vatName)
		}
		vatAcc = v
	}
	posting := a.today()
	if err := a.assertMonthOpen(ctx, tx, posting); err != nil {
		return a.enqueue(ctx, tx, p, row, domain.QueueMonthClosed, posting[:7])
	}
	var lines []repository.LineInsert
	if in.deduct {
		lines = append(lines, repository.LineInsert{AccountID: exp.ID, Side: "D", Amount: in.supply})
		if in.vat > 0 {
			lines = append(lines, repository.LineInsert{AccountID: vatAcc.ID, Side: "D", Amount: in.vat})
		}
	} else {
		lines = append(lines, repository.LineInsert{AccountID: exp.ID, Side: "D", Amount: in.total})
	}
	lines = append(lines, repository.LineInsert{AccountID: pay.ID, Side: "C", Amount: in.total})
	trade := in.tradeDate
	if !validDate(trade) {
		trade = posting
	}
	memo := truncateRunes(fmt.Sprintf("카드 비용: %s (%s)", in.merchant, in.cardAlias), 200)
	no, err := a.insert(ctx, tx, p, repository.JournalInsert{Kind: domain.JournalAutoCard, PostingDate: posting, TradeDate: trade, Memo: memo,
		SourceType: "card_txn", SourceID: in.explanationID, Total: in.total}, lines, domain.JournalEvidence{})
	if err != nil {
		return domain.JournalOutcome{}, err
	}
	if err := a.Audit.Record(ctx, tx, audit.Entry{Actor: p, Action: "journal.auto_card", TargetType: audit.TargetJournal, TargetID: no, Result: audit.ResultSuccess, Detail: "auto=true 카드 비용 전표",
		Diff: []audit.DiffItem{{Key: "source", New: fmt.Sprintf("card_txn:%d", in.txnID)}, {Key: "amount", New: formatWon(in.total)}, {Key: "account_code", New: exp.Code}}}); err != nil {
		return domain.JournalOutcome{}, err
	}
	return domain.JournalOutcome{Created: true, JournalNo: no}, nil
}
