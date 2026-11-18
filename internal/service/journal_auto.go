package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/repository"
)

// 자동 전표(B31 3.3·3.4): 증빙 판정 확정 → 임시 매입 전표, 지출 결의서 최종 승인 → 임시 비용 전표. 전표를 못 만들면 전표 대기 행을 남기고 오류 없이 돌려준다(확정·승인은 막지 않는다).
//   - 금액·계정·거래처는 입고 줄·발주 줄·규칙 표·결의서 필드에서만 온다. LLM 이 읽은 값과 문서 본문은 쓰지 않는다(문서 합계는 비교에만 쓴다).
//   - 항상 임시로 만든다. 승인은 일반 전표와 같은 절차(승인 요청 → 결재)를 탄다.
//   - 감사 행위자는 판정을 확정한 재무 사용자(또는 결의서의 최종 승인자)이고 detail 에 auto=true 를 남긴다.

// autoJournal 은 자동 전표 생성 한 곳이다. 확정·승인 트랜잭션의 tx 로만 읽고 쓴다.
type autoJournal struct {
	Base
	Repo repository.SQLLedger
}

var _ domain.PurchaseJournalHook = (*JournalService)(nil)

// OnVerdictConfirmed 는 확정된 판정에서 자동 매입 전표(또는 전표 대기 행)를 만든다(domain.PurchaseJournalHook).
func (s *JournalService) OnVerdictConfirmed(ctx context.Context, tx contract.Tx, v domain.Verdict) (domain.JournalOutcome, error) {
	p, ok := contract.PrincipalFrom(ctx)
	if !ok {
		return domain.JournalOutcome{}, errors.New("확정한 사용자를 알 수 없습니다")
	}
	in := purchaseInput{verdictID: v.ID, poID: v.POID, receiptID: v.ReceiptID, outcome: v.ConfirmedOutcome}
	// 추출 값이 없는 판정(판단 보류: 스키마 위반·원문 근거 없음 등)은 비교할 문서 합계가 없으므로 합계 비교를 건너뛴다(3.3.2).
	if len(v.Extraction.Lines) > 0 || v.Extraction.TotalAmount != 0 || v.Extraction.Supplier != "" {
		t := v.Extraction.TotalAmount
		in.docTotal = &t
	}
	return autoJournal{Base: s.Base, Repo: s.Repo}.purchase(ctx, tx, p, in)
}

type purchaseInput struct {
	verdictID, poID, receiptID int64
	outcome                    domain.Outcome
	docTotal                   *int64
	posting                    string // 전기일(비면 오늘 KST). 전표 대기 "다시 만들기"에서 직접 지정한다
}

func queueText(code, detail string) string {
	t := domain.QueueReasonLabels[code]
	if detail != "" {
		t += ": " + detail
	}
	return t
}

func (a autoJournal) today() string { return a.now().In(contract.KST).Format("2006-01-02") }

// enqueue 는 대기 행을 쓰고(처리 전 행이면 사유를 갱신) 감사를 남긴다.
func (a autoJournal) enqueue(ctx context.Context, tx contract.Tx, p contract.Principal, row repository.QueueRow, code, detail string) (domain.JournalOutcome, error) {
	row.Reason, row.ReasonText = code, queueText(code, detail)
	if err := a.Repo.UpsertQueue(ctx, tx, row, a.now()); err != nil {
		return domain.JournalOutcome{}, err
	}
	saved, err := a.Repo.QueueBySource(ctx, tx, row.SourceType, row.SourceID)
	if err != nil {
		return domain.JournalOutcome{}, err
	}
	if err := a.Audit.Record(ctx, tx, audit.Entry{Actor: p, Action: "journal.queue", TargetType: audit.TargetJournal, TargetID: fmt.Sprintf("Q%d", saved.ID), Result: audit.ResultSuccess,
		Detail: "auto=true 전표 대기", Diff: []audit.DiffItem{{Key: "reason", New: row.ReasonText}, {Key: "source", New: fmt.Sprintf("%s:%d", row.SourceType, row.SourceID)}}}); err != nil {
		return domain.JournalOutcome{}, err
	}
	return domain.JournalOutcome{Queued: true, Reason: row.ReasonText}, nil
}

// ruleAccount 는 규칙 키의 계정이다. 규칙이 없거나 계정이 중지됐으면 ok=false 와 화면용 이름을 돌려준다.
func (a autoJournal) ruleAccount(ctx context.Context, tx contract.Tx, key, label string) (acc domain.Account, name string, ok bool, err error) {
	acc, found, err := a.Repo.RuleAccount(ctx, tx, key)
	if err != nil {
		return acc, "", false, err
	}
	if !found || !acc.Active {
		return acc, label, false, nil
	}
	return acc, label, true, nil
}

// purchase 는 매입 전표 한 건을 만들거나 전표 대기·증빙 연결로 처리한다(3.3).
func (a autoJournal) purchase(ctx context.Context, tx contract.Tx, p contract.Principal, in purchaseInput) (domain.JournalOutcome, error) {
	row := repository.QueueRow{SourceType: "verdict", SourceID: in.verdictID, POID: in.poID, ReceiptID: in.receiptID, DocTotal: in.docTotal}
	switch in.outcome {
	case domain.OutcomeMatch:
	case domain.OutcomeMismatch:
		return a.enqueue(ctx, tx, p, row, domain.QueueMismatchConfirmed, "")
	default:
		return a.enqueue(ctx, tx, p, row, domain.QueueHoldConfirmed, "")
	}
	// 같은 판정으로는 전표가 한 번만 생긴다(재시도·동시 확정, E14).
	if _, no, found, err := a.Repo.JournalBySource(ctx, tx, "verdict", in.verdictID); err != nil {
		return domain.JournalOutcome{}, err
	} else if found {
		return domain.JournalOutcome{JournalNo: no}, nil
	}
	if in.receiptID == 0 {
		return a.enqueue(ctx, tx, p, row, domain.QueueNoReceipt, "입고 없이 판정한 증빙은 전표를 만들 수 없습니다")
	}
	facts, err := a.Repo.ReceiptFacts(ctx, tx, in.receiptID)
	if errors.Is(err, contract.ErrNotFound) {
		return a.enqueue(ctx, tx, p, row, domain.QueueNoReceipt, "입고를 찾을 수 없습니다")
	}
	if err != nil {
		return domain.JournalOutcome{}, err
	}
	row.POID, row.VendorID = facts.POID, facts.VendorID
	amount := facts.Supply
	row.Amount = &amount
	// 한 입고 한 전표(D8): 이미 취소되지 않은 매입 전표가 있으면 증빙만 연결한다.
	if id, no, found, err := a.Repo.PurchaseJournalForReceipt(ctx, tx, in.receiptID); err != nil {
		return domain.JournalOutcome{}, err
	} else if found {
		if err := a.Repo.AddEvidence(ctx, tx, id, domain.JournalEvidence{Kind: "verdict", RefID: in.verdictID}, a.now()); err != nil {
			return domain.JournalOutcome{}, err
		}
		if err := a.Audit.Record(ctx, tx, audit.Entry{Actor: p, Action: "journal.attach_evidence", TargetType: audit.TargetJournal, TargetID: no, Result: audit.ResultSuccess,
			Detail: "auto=true 증빙 연결", Diff: []audit.DiffItem{{Key: "evidence", New: fmt.Sprintf("verdict:%d", in.verdictID)}}}); err != nil {
			return domain.JournalOutcome{}, err
		}
		return domain.JournalOutcome{JournalNo: no, Attached: true}, nil
	}
	if facts.Supply <= 0 {
		return a.enqueue(ctx, tx, p, row, domain.QueueNoAmount, "입고 금액이 0원입니다")
	}
	if in.docTotal != nil && *in.docTotal != facts.Supply {
		return a.enqueue(ctx, tx, p, row, domain.QueueTotalDiffers, fmt.Sprintf("증빙 합계 %s, 입고 금액 %s", moneyLabel(*in.docTotal), moneyLabel(facts.Supply)))
	}
	if facts.VendorID == 0 {
		return a.enqueue(ctx, tx, p, row, domain.QueueVendorMissing, "발주에 거래처가 없습니다")
	}
	if ok, err := a.Repo.VendorExists(ctx, tx, facts.VendorID); err != nil {
		return domain.JournalOutcome{}, err
	} else if !ok {
		return a.enqueue(ctx, tx, p, row, domain.QueueVendorMissing, "거래처를 찾을 수 없습니다")
	}
	inv, invName, ok1, err := a.ruleAccount(ctx, tx, "purchase.inventory", "재고자산")
	if err != nil {
		return domain.JournalOutcome{}, err
	}
	rate, err := a.vatRateOn(ctx, tx, string(contract.DayOf(facts.ReceivedAt))) // 입고일(거래일)의 부가세율
	if err != nil {
		return domain.JournalOutcome{}, err
	}
	vat := domain.VATFromSupply(facts.Supply, rate) // 원 미만 버림, 전표 전체에 한 번(D4)
	vatAcc, vatName, ok2, err := a.ruleAccount(ctx, tx, "purchase.vat", "부가세대급금")
	if err != nil {
		return domain.JournalOutcome{}, err
	}
	pay, payName, ok3, err := a.ruleAccount(ctx, tx, "purchase.payable", "외상매입금")
	if err != nil {
		return domain.JournalOutcome{}, err
	}
	switch {
	case !ok1:
		return a.enqueue(ctx, tx, p, row, domain.QueueRuleAccount, invName)
	case vat > 0 && !ok2:
		return a.enqueue(ctx, tx, p, row, domain.QueueRuleAccount, vatName)
	case !ok3:
		return a.enqueue(ctx, tx, p, row, domain.QueueRuleAccount, payName)
	}
	posting := in.posting
	if posting == "" {
		posting = a.today()
	}
	if err := a.assertMonthOpen(ctx, tx, posting); err != nil {
		return a.enqueue(ctx, tx, p, row, domain.QueueMonthClosed, posting[:7])
	}
	lines := []repository.LineInsert{{AccountID: inv.ID, Side: "D", Amount: facts.Supply}}
	if vat > 0 {
		lines = append(lines, repository.LineInsert{AccountID: vatAcc.ID, Side: "D", Amount: vat})
	}
	lines = append(lines, repository.LineInsert{AccountID: pay.ID, Side: "C", Amount: facts.Supply + vat, VendorID: facts.VendorID})
	memo := truncateRunes(fmt.Sprintf("자동 매입 전표: 발주 %s 입고 %d", facts.PONo, in.receiptID), 200)
	no, err := a.insert(ctx, tx, p, repository.JournalInsert{Kind: domain.JournalAutoPurchase, PostingDate: posting, TradeDate: facts.ReceivedAt.In(contract.KST).Format("2006-01-02"), SiteID: facts.SiteID,
		Memo: memo, SourceType: "verdict", SourceID: in.verdictID, Total: facts.Supply + vat}, lines, domain.JournalEvidence{Kind: "verdict", RefID: in.verdictID})
	if err != nil {
		return domain.JournalOutcome{}, err
	}
	if err := a.Audit.Record(ctx, tx, audit.Entry{Actor: p, Action: "journal.auto_purchase", TargetType: audit.TargetJournal, TargetID: no, Result: audit.ResultSuccess, Detail: "auto=true 자동 매입 전표",
		Diff: []audit.DiffItem{{Key: "source", New: fmt.Sprintf("verdict:%d", in.verdictID)}, {Key: "supply", New: formatWon(facts.Supply)}, {Key: "vat", New: formatWon(vat)}}}); err != nil {
		return domain.JournalOutcome{}, err
	}
	return domain.JournalOutcome{Created: true, JournalNo: no}, nil
}

// insert 는 임시 자동 전표 한 건(헤더·줄·증빙)을 쓰고 번호를 돌려준다.
func (a autoJournal) insert(ctx context.Context, tx contract.Tx, p contract.Principal, h repository.JournalInsert, lines []repository.LineInsert, ev domain.JournalEvidence) (string, error) {
	now := a.now()
	no, err := a.Repo.NextJournalNo(ctx, tx, now.In(contract.KST).Year())
	if err != nil {
		return "", err
	}
	h.No, h.CreatedBy, h.Now = no, p.UserID, now
	id, err := a.Repo.InsertJournal(ctx, tx, h)
	if err != nil {
		return "", err
	}
	if err := a.Repo.ReplaceLines(ctx, tx, id, lines); err != nil {
		return "", err
	}
	if ev.Kind != "" {
		if err := a.Repo.AddEvidence(ctx, tx, id, ev, now); err != nil {
			return "", err
		}
	}
	return no, nil
}

// ---- 지출 결의서 승인 → 비용 전표(3.4) ----

// 지출 구분 → 규칙 키. 그 밖의 값은 기타 비용이다.
var expenseRuleKeys = map[string]string{"복리후생": "expense.welfare", "출장": "expense.trip", "소모품": "expense.supplies", "교육": "expense.training", "기타": "expense.other"}

type expenseInput struct {
	docID     int64
	docNo     string
	kind      string
	spendDate string
	amount    int64
}

func expenseFromRecord(rec repository.PRRecord) expenseInput {
	in := expenseInput{docID: rec.ID, docNo: rec.No, amount: rec.Amount}
	if k, ok := rec.Fields["spend_kind"].(string); ok {
		in.kind = k
	}
	if d, ok := rec.Fields["spend_date"].(string); ok {
		in.spendDate = d
	}
	if t, ok := rec.Fields["total"].(float64); ok && t > 0 {
		in.amount = int64(t)
	}
	return in
}

// expense 는 지출 결의서 한 건의 임시 비용 전표를 만들거나 전표 대기로 올린다. 부가세는 분리하지 않는다(D2).
func (a autoJournal) expense(ctx context.Context, tx contract.Tx, p contract.Principal, in expenseInput) (domain.JournalOutcome, error) {
	amt := in.amount
	row := repository.QueueRow{SourceType: "approval_doc", SourceID: in.docID, Amount: &amt}
	if _, no, found, err := a.Repo.JournalBySource(ctx, tx, "approval_doc", in.docID); err != nil {
		return domain.JournalOutcome{}, err
	} else if found {
		return domain.JournalOutcome{JournalNo: no}, nil
	}
	if in.amount <= 0 {
		return a.enqueue(ctx, tx, p, row, domain.QueueNoAmount, "결의서 금액이 없습니다")
	}
	key, ok := expenseRuleKeys[in.kind]
	if !ok {
		key = "expense.other"
	}
	exp, expName, ok1, err := a.ruleAccount(ctx, tx, key, "비용 계정("+strings.TrimSpace(in.kind)+")")
	if err != nil {
		return domain.JournalOutcome{}, err
	}
	pay, payName, ok2, err := a.ruleAccount(ctx, tx, "expense.payable", "미지급금")
	if err != nil {
		return domain.JournalOutcome{}, err
	}
	switch {
	case !ok1:
		return a.enqueue(ctx, tx, p, row, domain.QueueRuleAccount, expName)
	case !ok2:
		return a.enqueue(ctx, tx, p, row, domain.QueueRuleAccount, payName)
	}
	posting := a.today()
	if err := a.assertMonthOpen(ctx, tx, posting); err != nil {
		return a.enqueue(ctx, tx, p, row, domain.QueueMonthClosed, posting[:7])
	}
	trade := in.spendDate
	if !validDate(trade) {
		trade = posting
	}
	memo := truncateRunes(fmt.Sprintf("자동 비용 전표: 지출 결의서 %s", in.docNo), 200)
	lines := []repository.LineInsert{{AccountID: exp.ID, Side: "D", Amount: in.amount}, {AccountID: pay.ID, Side: "C", Amount: in.amount}}
	no, err := a.insert(ctx, tx, p, repository.JournalInsert{Kind: domain.JournalAutoExpense, PostingDate: posting, TradeDate: trade, Memo: memo,
		SourceType: "approval_doc", SourceID: in.docID, Total: in.amount}, lines, domain.JournalEvidence{})
	if err != nil {
		return domain.JournalOutcome{}, err
	}
	if err := a.Audit.Record(ctx, tx, audit.Entry{Actor: p, Action: "journal.auto_expense", TargetType: audit.TargetJournal, TargetID: no, Result: audit.ResultSuccess, Detail: "auto=true 자동 비용 전표",
		Diff: []audit.DiffItem{{Key: "source", New: fmt.Sprintf("approval_doc:%s", in.docNo)}, {Key: "amount", New: formatWon(in.amount)}, {Key: "account_code", New: exp.Code}}}); err != nil {
		return domain.JournalOutcome{}, err
	}
	return domain.JournalOutcome{Created: true, JournalNo: no}, nil
}

// expenseJournalHook 은 지출 결의서(EXP)가 최종 승인되면 같은 트랜잭션에서 임시 비용 전표를 만든다. 실패는 승인을 막지 않고 전표 대기로 간다.
// 시스템 전용 양식이 아니므로 docHookTable 이 아니라 postApprovalTable 에 둔다(EXP 는 일반 기안으로 만든다).
func expenseJournalHook(ctx context.Context, b Base, tx contract.Tx, p contract.Principal, rec repository.PRRecord) error {
	_, err := autoJournal{Base: b}.expense(ctx, tx, p, expenseFromRecord(rec))
	return err
}

// ---- 일괄 승인 요청 ----

const maxJournalBatch = 20

// RequestApprovalBatch 는 임시 전표 여러 건을 건별로 승인 요청한다(20건 상한). 건마다 독립이라 한 건의 실패가 다른 건을 막지 않는다.
func (s *JournalService) RequestApprovalBatch(ctx context.Context, nos []string) ([]domain.JournalBatchResult, error) {
	if _, err := s.authorize(ctx, contract.PermLedgerWrite, "journal.request", audit.TargetJournal); err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var uniq []string
	for _, n := range nos {
		n = strings.TrimSpace(n)
		if n != "" && !seen[n] {
			seen[n] = true
			uniq = append(uniq, n)
		}
	}
	if len(uniq) == 0 {
		return nil, validation("승인 요청할 전표를 고르세요.")
	}
	if len(uniq) > maxJournalBatch {
		return nil, validation("한 번에 %d건까지 승인 요청할 수 있습니다.", maxJournalBatch)
	}
	out := make([]domain.JournalBatchResult, 0, len(uniq))
	for _, n := range uniq {
		if _, err := s.RequestApproval(ctx, n); err != nil {
			out = append(out, domain.JournalBatchResult{No: n, Error: strings.TrimPrefix(err.Error(), contract.ErrValidation.Error()+": ")})
			continue
		}
		out = append(out, domain.JournalBatchResult{No: n, OK: true})
	}
	return out, nil
}

var _ = utf8.RuneCountInString
