package service

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/repository"
)

// JournalQueueService 는 전표 대기 처리다(B31 3.3.8): 다시 만들기·수동 입력·전표 불필요. 처리된 행은 지워지지 않고 고칠 수 없다(트리거).
type JournalQueueService struct {
	Base
	Repo repository.SQLLedger
}

var _ domain.JournalQueueService = (*JournalQueueService)(nil)

func NewJournalQueueService(b Base) *JournalQueueService { return &JournalQueueService{Base: b} }

func toQueueItem(r repository.QueueRow, journalNo string) domain.QueueItem {
	return domain.QueueItem{ID: r.ID, SourceType: r.SourceType, SourceID: r.SourceID, SourceLabel: r.SourceName, Reason: r.Reason, ReasonText: r.ReasonText, PONo: r.PONo,
		VendorName: r.VendorName, Amount: r.Amount, Status: r.Status, JournalNo: journalNo, HandledBy: r.HandledBy, HandledAt: r.HandledAt, Note: r.Note, CreatedAt: r.CreatedAt}
}

// journalNoOf 는 처리된 행이 가리키는 전표 번호다(없으면 "").
func (s *JournalQueueService) journalNoOf(ctx context.Context, q contract.Tx, r repository.QueueRow) string {
	if r.JournalID == 0 {
		return ""
	}
	j, err := s.Repo.JournalByID(ctx, q, r.JournalID)
	if err != nil {
		return ""
	}
	return j.No
}

// List 는 대기 목록이다(status 가 비면 전부, 처리 전이 먼저).
func (s *JournalQueueService) List(ctx context.Context, status string) ([]domain.QueueItem, error) {
	if _, err := s.authorize(ctx, contract.PermLedgerRead, "journal.queue", audit.TargetJournal); err != nil {
		return nil, err
	}
	if status != "" {
		if _, ok := domain.QueueStatusLabels[status]; !ok {
			return nil, validation("알 수 없는 처리 상태입니다.")
		}
	}
	rows, err := s.Repo.ListQueue(ctx, s.DB, status)
	if err != nil {
		return nil, err
	}
	out := make([]domain.QueueItem, 0, len(rows))
	for _, r := range rows {
		out = append(out, toQueueItem(r, s.journalNoOf(ctx, s.DB, r)))
	}
	return out, nil
}

func (s *JournalQueueService) pendingRow(ctx context.Context, q contract.Tx, id int64) (repository.QueueRow, error) {
	r, err := s.Repo.QueueByID(ctx, q, id)
	if err != nil {
		return r, err
	}
	if r.Status != domain.QueuePending {
		return r, fmt.Errorf("%w: 이미 처리된 전표 대기 건입니다(%s)", contract.ErrInvalidState, domain.QueueStatusLabels[r.Status])
	}
	return r, nil
}

func (s *JournalQueueService) audited(ctx context.Context, tx contract.Tx, p contract.Principal, r repository.QueueRow, how, note string) error {
	diff := []audit.DiffItem{{Key: "reason", New: r.ReasonText}, {Key: "handle", New: how}}
	return s.Audit.Record(ctx, tx, audit.Entry{Actor: p, Action: "journal.queue_handle", TargetType: audit.TargetJournal, TargetID: fmt.Sprintf("Q%d", r.ID), Result: audit.ResultSuccess, Detail: note, Diff: diff})
}

// Retry 는 규칙·계정을 고친 뒤 같은 입력으로 전표를 다시 만든다. 또 실패하면 사유만 갱신되고 처리 전으로 남는다. 불일치·보류로 확정한 건은 다시 만들 수 없다.
func (s *JournalQueueService) Retry(ctx context.Context, id int64, postingDate string) (domain.QueueItem, error) {
	p, err := s.authorize(ctx, contract.PermLedgerWrite, "journal.queue_handle", audit.TargetJournal)
	if err != nil {
		return domain.QueueItem{}, err
	}
	postingDate = strings.TrimSpace(postingDate)
	if postingDate != "" && !validDate(postingDate) {
		return domain.QueueItem{}, validation("전기일은 YYYY-MM-DD 형식으로 입력하세요.")
	}
	var out domain.QueueItem
	err = s.write(ctx, p, "journal.queue_handle", audit.TargetJournal, id, func(tx contract.Tx) error {
		r, err := s.pendingRow(ctx, tx, id)
		if err != nil {
			return err
		}
		a := autoJournal{Base: s.Base, Repo: s.Repo}
		var res domain.JournalOutcome
		switch r.SourceType {
		case "verdict":
			if r.Reason == domain.QueueMismatchConfirmed || r.Reason == domain.QueueHoldConfirmed {
				return fmt.Errorf("%w: 불일치·보류로 확정한 증빙은 다시 만들 수 없습니다. 수동으로 입력하거나 전표 불필요로 처리하세요", contract.ErrInvalidState)
			}
			outcome, err := s.Repo.VerdictConfirmed(ctx, tx, r.SourceID)
			if err != nil {
				return err
			}
			res, err = a.purchase(ctx, tx, p, purchaseInput{verdictID: r.SourceID, poID: r.POID, receiptID: r.ReceiptID, outcome: outcome, docTotal: r.DocTotal, posting: postingDate})
			if err != nil {
				return err
			}
		case "approval_doc":
			rec, err := repository.SQLPurchases{}.GetPR(ctx, tx, r.SourceID, repository.ScopeAll)
			if err != nil {
				return err
			}
			res, err = a.expense(ctx, tx, p, expenseFromRecord(rec))
			if err != nil {
				return err
			}
		case "card_txn":
			cards := repository.SQLCards{}
			e, txnID, err := cards.ExplanationByID(ctx, tx, r.SourceID)
			if err != nil {
				return err
			}
			txn, err := cards.Txn(ctx, tx, txnID)
			if err != nil {
				return err
			}
			res, err = a.cardExpense(ctx, tx, p, cardExpenseInput{explanationID: e.ID, txnID: txnID, accountID: e.AccountID, supply: e.Supply, vat: e.VAT, total: txn.Amount, deduct: e.VATDeduct,
				merchant: txn.Merchant, cardAlias: txn.CardAlias, tradeDate: txn.UsedAt.In(contract.KST).Format("2006-01-02")})
			if err != nil {
				return err
			}
		default:
			return fmt.Errorf("%w: 알 수 없는 출처입니다", contract.ErrInvalidState)
		}
		if res.Queued {
			now, err := s.Repo.QueueByID(ctx, tx, id)
			if err != nil {
				return err
			}
			out = toQueueItem(now, "")
			return nil
		}
		var jid int64
		if j, jerr := s.Repo.Journal(ctx, tx, res.JournalNo); jerr == nil {
			jid = j.ID
		}
		if err := s.Repo.HandleQueue(ctx, tx, id, domain.QueueCreated, jid, p.UserID, "", s.now()); err != nil {
			return err
		}
		done, err := s.Repo.QueueByID(ctx, tx, id)
		if err != nil {
			return err
		}
		out = toQueueItem(done, res.JournalNo)
		return s.audited(ctx, tx, p, done, "다시 만들기", "전표 대기 처리: 다시 만들기 "+res.JournalNo)
	})
	return out, err
}

// Manual 은 재무가 직접 입력한 전표(취소되지 않은 것)를 이 대기 건의 처리로 연결한다. 증빙 판정이 출처이면 증빙도 전표에 연결한다.
func (s *JournalQueueService) Manual(ctx context.Context, id int64, journalNo string) (domain.QueueItem, error) {
	p, err := s.authorize(ctx, contract.PermLedgerWrite, "journal.queue_handle", audit.TargetJournal)
	if err != nil {
		return domain.QueueItem{}, err
	}
	journalNo = strings.TrimSpace(journalNo)
	var out domain.QueueItem
	err = s.write(ctx, p, "journal.queue_handle", audit.TargetJournal, id, func(tx contract.Tx) error {
		r, err := s.pendingRow(ctx, tx, id)
		if err != nil {
			return err
		}
		j, err := s.Repo.Journal(ctx, tx, journalNo)
		if err != nil {
			return validation("연결할 전표를 찾을 수 없습니다. 전표 번호를 확인하세요.")
		}
		if j.Status == domain.JournalCanceled {
			return validation("취소된 전표에는 연결할 수 없습니다.")
		}
		if r.SourceType == "verdict" {
			if err := s.Repo.AddEvidence(ctx, tx, j.ID, domain.JournalEvidence{Kind: "verdict", RefID: r.SourceID}, s.now()); err != nil {
				return err
			}
		}
		if err := s.Repo.HandleQueue(ctx, tx, id, domain.QueueManual, j.ID, p.UserID, "", s.now()); err != nil {
			return err
		}
		done, err := s.Repo.QueueByID(ctx, tx, id)
		if err != nil {
			return err
		}
		out = toQueueItem(done, j.No)
		return s.audited(ctx, tx, p, done, "수동 입력", "전표 대기 처리: 수동 입력 "+j.No)
	})
	return out, err
}

// Skip 은 전표가 필요 없는 건으로 처리한다(사유 필수).
func (s *JournalQueueService) Skip(ctx context.Context, id int64, note string) (domain.QueueItem, error) {
	p, err := s.authorize(ctx, contract.PermLedgerWrite, "journal.queue_handle", audit.TargetJournal)
	if err != nil {
		return domain.QueueItem{}, err
	}
	note = strings.TrimSpace(note)
	if c := utf8.RuneCountInString(note); c < 1 || c > 200 {
		return domain.QueueItem{}, validation("전표가 필요 없는 사유를 1~200자로 입력하세요.")
	}
	var out domain.QueueItem
	err = s.write(ctx, p, "journal.queue_handle", audit.TargetJournal, id, func(tx contract.Tx) error {
		r, err := s.pendingRow(ctx, tx, id)
		if err != nil {
			return err
		}
		if err := s.Repo.HandleQueue(ctx, tx, id, domain.QueueSkipped, 0, p.UserID, note, s.now()); err != nil {
			return err
		}
		done, err := s.Repo.QueueByID(ctx, tx, r.ID)
		if err != nil {
			return err
		}
		out = toQueueItem(done, "")
		return s.audited(ctx, tx, p, done, "전표 불필요", fmt.Sprintf("전표 대기 처리: 전표 불필요(사유 %d자)", utf8.RuneCountInString(note)))
	})
	return out, err
}
