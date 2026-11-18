package service

import (
	"context"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/repository"
)

// CreateOpening 은 기초 데이터 가져오기(B35)가 같은 트랜잭션에서 기초 전표(임시)를 만드는 유일한 길이다. 호출한 쪽이 권한·트랜잭션을 맡고,
// 전표 규칙은 화면 입력(Create)과 같다: 계정은 사용 중이어야 하고, 전기일이 마감된 달이면 거부하며, 차변 합계와 대변 합계가 같아야 한다.
// 승인은 B31 절차를 그대로 탄다(재무가 승인 요청 → 재무팀장 결재). 승인 전에는 계정별 원장·시산표에 들어가지 않는다.
func (s *JournalService) CreateOpening(ctx context.Context, tx contract.Tx, p contract.Principal, in domain.JournalInput, batchNo string) (domain.Journal, error) {
	in.Kind = domain.JournalOpening
	d, err := s.normalize(ctx, tx, in)
	if err != nil {
		return domain.Journal{}, err
	}
	if err := s.assertMonthOpen(ctx, tx, d.posting); err != nil {
		return domain.Journal{}, err
	}
	if d.debit != d.credit {
		return domain.Journal{}, balanceError(d.debit, d.credit)
	}
	now := s.now()
	no, err := s.Repo.NextJournalNo(ctx, tx, now.In(contract.KST).Year())
	if err != nil {
		return domain.Journal{}, err
	}
	id, err := s.Repo.InsertJournal(ctx, tx, repository.JournalInsert{No: no, Kind: d.kind, PostingDate: d.posting, TradeDate: d.trade, Memo: d.memo, Total: d.debit, CreatedBy: p.UserID, Now: now})
	if err != nil {
		return domain.Journal{}, err
	}
	if err := s.Repo.ReplaceLines(ctx, tx, id, d.lines); err != nil {
		return domain.Journal{}, err
	}
	j, err := s.Repo.Journal(ctx, tx, no)
	if err != nil {
		return domain.Journal{}, err
	}
	err = s.Audit.Record(ctx, tx, audit.Entry{Actor: p, Action: "journal.opening_import", TargetType: audit.TargetJournal, TargetID: no, Result: audit.ResultSuccess,
		Detail: "기초 전표 생성(기초 데이터 가져오기 " + batchNo + ")",
		Diff:   []audit.DiffItem{{Key: "journal_kind", New: domain.JournalKindLabels[d.kind]}, {Key: "posting_date", New: d.posting}, {Key: "debit_total", New: formatWon(d.debit)}}})
	return j, err
}
