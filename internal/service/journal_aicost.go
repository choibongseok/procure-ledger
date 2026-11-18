package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/repository"
)

// 부서별 AI 비용 배부 전표(B49 3단계, 명세 49 8절): 배부 확정 트랜잭션이 부르는 회계 쪽 입구다. 호출자의 tx 로만 읽고 쓴다.
//   - 차변은 부서별 비용 줄(계정은 호출자가 정한 코드, 줄에 부서 번호), 대변은 미지급금 한 줄(규칙 expense.payable)이다. 항상 임시로 만든다.
//   - 승인은 일반 전표와 같은 절차를 탄다. 전표 승인 권한은 새로 만들지 않는다.

var _ domain.AICostJournalHook = (*JournalService)(nil)

// CreateAICostDraft 는 배부 실행의 임시 비용 전표를 만든다.
func (s *JournalService) CreateAICostDraft(ctx context.Context, tx contract.Tx, in domain.AICostJournalInput) (string, error) {
	p, ok := contract.PrincipalFrom(ctx)
	if !ok {
		return "", errors.New("확정한 사용자를 알 수 없습니다")
	}
	if len(in.Lines) == 0 {
		return "", validation("전표 줄이 없습니다")
	}
	a := autoJournal{Base: s.Base, Repo: s.Repo}
	exp, found, err := a.Repo.AccountByCode(ctx, tx, in.ExpenseCode)
	if err != nil {
		return "", err
	}
	if !found || !exp.Active || exp.Kind != "expense" {
		return "", fmt.Errorf("%w: 비용 계정 %s 을 쓸 수 없습니다. 계정 설정을 확인하세요", contract.ErrInvalidState, in.ExpenseCode)
	}
	pay, _, okPay, err := a.ruleAccount(ctx, tx, "expense.payable", "미지급금")
	if err != nil {
		return "", err
	}
	if !okPay {
		return "", fmt.Errorf("%w: 미지급금 계정을 쓸 수 없습니다. 계정 규칙을 확인하세요", contract.ErrInvalidState)
	}
	if err := a.assertMonthOpen(ctx, tx, in.PostingDate); err != nil {
		return "", fmt.Errorf("%w: 이 달 장부가 마감되어 전표를 만들 수 없습니다. 장부 마감을 해제한 뒤 다시 확정하세요.", contract.ErrInvalidState)
	}
	var total int64
	lines := make([]repository.LineInsert, 0, len(in.Lines)+1)
	for _, l := range in.Lines {
		if l.Amount <= 0 {
			continue
		}
		total += l.Amount
		lines = append(lines, repository.LineInsert{AccountID: exp.ID, Side: "D", Amount: l.Amount, Memo: truncateRunes(l.Memo, 200), DepartmentID: l.DepartmentID})
	}
	if total <= 0 {
		return "", validation("전표 금액이 0 입니다")
	}
	lines = append(lines, repository.LineInsert{AccountID: pay.ID, Side: "C", Amount: total})
	no, err := a.insert(ctx, tx, p, repository.JournalInsert{Kind: domain.JournalAutoAICost, PostingDate: in.PostingDate, TradeDate: in.TradeDate, Memo: truncateRunes(in.Memo, 200),
		SourceType: "aicost_run", SourceID: in.RunID, Total: total}, lines, domain.JournalEvidence{})
	if err != nil {
		return "", err
	}
	if err := a.Audit.Record(ctx, tx, audit.Entry{Actor: p, Action: "journal.auto_aicost", TargetType: audit.TargetJournal, TargetID: no, Result: audit.ResultSuccess, Detail: "auto=true AI 비용 배부 전표",
		Diff: []audit.DiffItem{{Key: "source", New: fmt.Sprintf("aicost_run:%d", in.RunID)}, {Key: "amount", New: formatWon(total)}, {Key: "account_code", New: exp.Code}}}); err != nil {
		return "", err
	}
	return no, nil
}

// AICostJournalStatus 는 전표 상태를 읽는다.
func (s *JournalService) AICostJournalStatus(ctx context.Context, tx contract.Tx, no string) (string, error) {
	j, err := s.Repo.Journal(ctx, tx, strings.TrimSpace(no))
	if err != nil {
		return "", err
	}
	return j.Status, nil
}

// CancelAICostDraft 는 임시 전표를 취소한다. 승인 요청 중(승인 문서가 걸려 있음)이거나 승인된 전표는 거부한다.
func (s *JournalService) CancelAICostDraft(ctx context.Context, tx contract.Tx, no, reason string) error {
	p, ok := contract.PrincipalFrom(ctx)
	if !ok {
		return errors.New("처리한 사용자를 알 수 없습니다")
	}
	j, err := s.Repo.Journal(ctx, tx, strings.TrimSpace(no))
	if err != nil {
		return err
	}
	if j.Kind != domain.JournalAutoAICost {
		return fmt.Errorf("%w: AI 비용 배부 전표가 아닙니다", contract.ErrInvalidState)
	}
	switch j.Status {
	case domain.JournalCanceled:
		return nil
	case domain.JournalApproved:
		return fmt.Errorf("%w: 승인된 전표가 연결되어 있어 해제할 수 없습니다. 먼저 전표를 역분개하세요.", contract.ErrInvalidState)
	case domain.JournalRequested:
		return fmt.Errorf("%w: 전표가 승인 요청 중입니다. 먼저 전표의 승인 요청을 회수하세요.", contract.ErrInvalidState)
	}
	if err := s.Repo.SetStatus(ctx, tx, j.ID, domain.JournalCanceled, p.UserID, s.now(), truncateRunes(reason, 200)); err != nil {
		return err
	}
	return s.Audit.Record(ctx, tx, audit.Entry{Actor: p, Action: "journal.cancel", TargetType: audit.TargetJournal, TargetID: j.No, Result: audit.ResultSuccess, Detail: "AI 비용 배부 해제로 전표 취소",
		Diff: []audit.DiffItem{{Key: "reason", New: fmt.Sprintf("%d자", len([]rune(reason)))}}})
}
