package service

import (
	"context"
	"fmt"
	"github.com/choibongseok/procure-ledger/internal/dbutil"
	"strings"
	"unicode/utf8"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/repository"
)

// 상신 취소(B23 D9 확정): 아무도 처리(승인·반려·동의·이견)하지 않은 문서는 기안자가 언제든 취소해 임시저장으로 되돌린다(차수가 오른다).
// 누군가 한 번이라도 처리했으면 취소할 수 없다(반려를 받아 고친다). 결재자가 없어 멈춘 건의 회수(B11)는 그대로 둔다.
// 처리 전이라는 판정은 취소 트랜잭션 안에서 이력을 다시 세어 한다(병렬 합의 중 한 명이 방금 처리한 경우도 막는다). DB 트리거가 최후 방어선이다.

const (
	actionCancelSubmit domain.ApprovalAction = "cancel_submit"
	maxCancelReason                          = 300
)

// cancelSubmit 은 두 서비스(구매 품의서·양식 문서)가 공유하는 취소 본체다. wantPurchase 는 이 호출이 구매 품의서를 다루는지다.
func (b Base) cancelSubmit(ctx context.Context, p contract.Principal, id int64, reason string, wantPurchase bool, auditAction string, tt audit.TargetType) (repository.PRRecord, error) {
	reason = strings.TrimSpace(reason)
	if utf8.RuneCountInString(reason) > maxCancelReason {
		return repository.PRRecord{}, validation("취소 사유는 %d자 이내로 입력하세요", maxCancelReason)
	}
	var out repository.PRRecord
	approvals, purchases := repository.SQLApprovals{}, repository.SQLPurchases{}
	err := b.write(ctx, p, auditAction, tt, id, func(tx contract.Tx) error {
		if err := dbutil.AcquireKeyLock(ctx, tx, fmt.Sprintf("pr:%d", id)); err != nil { // 같은 문서에 대한 상태 전이(상신·결재·취소·회수)를 PG 에서 줄 세운다
			return err
		}
		rec, err := purchases.GetPR(ctx, tx, id, repository.ScopeAll)
		if err != nil {
			return err
		}
		isPurchase := rec.FormKind == "" || rec.FormKind == string(domain.FormKindPurchase)
		if isPurchase != wantPurchase || rec.RequesterID != p.UserID {
			return contract.ErrNotFound // 남의 문서·다른 종류의 문서는 존재를 숨긴다
		}
		to, err := transition(rec.Status, evCancelSubmit)
		if err != nil {
			return err
		}
		var processed int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM approval_history WHERE pr_id = ? AND round = ? AND action IN ('approve','reject','agree','object')`, id, rec.Round).Scan(&processed); err != nil {
			return err
		}
		if processed > 0 {
			return fmt.Errorf("%w: 이미 처리된 문서입니다. 상신을 취소할 수 없습니다", contract.ErrInvalidState)
		}
		// 알림 대상은 취소 전의 처리 대기자(와 유효한 수임자)와 참조 대상이다.
		pend, err := approvals.PendingSteps(ctx, tx, id, rec.Round)
		if err != nil {
			return err
		}
		var summary []string
		for _, st := range pend {
			role := st.Role
			if role == "" {
				role = domain.RoleApprove
			}
			summary = append(summary, fmt.Sprintf("%d/%s/%s", st.Seq, role, st.Title))
		}
		handlers, err := b.pendingHandlers(ctx, tx, id, rec.Round)
		if err != nil {
			return err
		}
		ccs, err := b.ccUsers(ctx, tx, id, rec.Round)
		if err != nil {
			return err
		}
		round, now := rec.Round+1, b.now()
		if err := approvals.InsertHistory(ctx, tx, repository.HistoryInsert{PRID: id, Round: rec.Round, ActorID: p.UserID, Action: actionCancelSubmit, Comment: reason, At: now}); err != nil {
			return err
		}
		if err := purchases.SetPRState(ctx, tx, id, to, round, "", now); err != nil {
			return err
		}
		if err := runResetHook(ctx, b, tx, p, rec); err != nil {
			return err
		}
		body := fmt.Sprintf("%s님이 「%s」(%s) 상신을 취소했습니다. 더 이상 처리할 필요가 없습니다.", rec.RequesterName, rec.Title, rec.No)
		notified, err := b.notifyUsers(ctx, tx, append(handlers, ccs...), p.UserID, noteSubmissionCanceled, "상신이 취소되었습니다", body, docLink(rec.FormKind, id), id)
		if err != nil {
			return err
		}
		diff := []audit.DiffItem{
			{Key: "status", Old: string(rec.Status), New: string(to)},
			{Key: "round", Old: fmt.Sprint(rec.Round), New: fmt.Sprint(round)},
			{Key: "pending_steps", New: strings.Join(summary, ",")},
			{Key: "notified", New: fmt.Sprint(notified)},
		}
		if reason != "" {
			diff = append(diff, audit.DiffItem{Key: "reason", New: reason})
		}
		if err := b.record(ctx, tx, p, auditAction, tt, id, diff...); err != nil {
			return err
		}
		out, err = purchases.GetPR(ctx, tx, id, repository.ScopeAll)
		return err
	})
	return out, err
}

// CancelSubmit 은 구매 품의서의 상신을 취소한다(요청자 본인, purchase:create).
func (s *PurchaseService) CancelSubmit(ctx context.Context, id int64, reason string) (domain.PurchaseRequest, error) {
	p, err := s.authorize(ctx, contract.PermPurchaseCreate, "purchase_request.cancel_submit", audit.TargetPurchaseRequest)
	if err != nil {
		return domain.PurchaseRequest{}, err
	}
	rec, err := s.Base.cancelSubmit(ctx, p, id, reason, true, "purchase_request.cancel_submit", audit.TargetPurchaseRequest)
	return toPR(rec), err
}

// CancelSubmit 은 양식 문서의 상신을 취소한다(기안자 본인, draft:create).
func (s *ApprovalDocService) CancelSubmit(ctx context.Context, id int64, reason string) (domain.PurchaseRequest, error) {
	p, err := s.authorize(ctx, contract.PermDraftCreate, "approval_doc.cancel_submit", audit.TargetApprovalDoc)
	if err != nil {
		return domain.PurchaseRequest{}, err
	}
	rec, err := s.Base.cancelSubmit(ctx, p, id, reason, false, "approval_doc.cancel_submit", audit.TargetApprovalDoc)
	if err != nil {
		return domain.PurchaseRequest{}, err
	}
	return s.present(ctx, p, rec), nil
}

// markCancelable 는 요청자 본인에게 "상신 취소가 지금 가능한가"를 채운다(취소 본체와 같은 조건: 상신·결재중이고 현재 차수에 처리 이력이 없다).
func (b Base) markCancelable(ctx context.Context, q contract.Tx, p contract.Principal, pr *domain.PurchaseRequest) {
	if p.UserID != pr.RequesterID || (pr.Status != domain.PRSubmitted && pr.Status != domain.PRInReview) {
		return
	}
	var processed int
	if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM approval_history WHERE pr_id = ? AND round = ? AND action IN ('approve','reject','agree','object')`, pr.ID, pr.Round).Scan(&processed); err == nil {
		pr.CanCancelSubmit = processed == 0
	}
}
