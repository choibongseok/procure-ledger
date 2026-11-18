package service

import (
	"context"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/repository"
)

// ActingNotice 는 결재 화면의 위임 결재 안내에 쓰는 값이다(위임자와 위임 기간).
type ActingNotice struct {
	DelegatorID   int64
	DelegatorName string
	From, To      time.Time
}

// ActingFor 는 호출자가 prID 의 지금 차례 단계를 직접 결재자가 아니라 유효한 위임으로 대신 결재하게 되는지 알려 준다.
// 대신 결재가 아니면(직접 결재자이거나 결재할 수 없으면) ok=false 다. 결재(decide)와 같은 판정(직접 결재자 우선, 없으면 findActing)을 쓴다.
func (s *ApprovalService) ActingFor(ctx context.Context, prID int64) (ActingNotice, bool, error) {
	none := ActingNotice{}
	p, err := contract.Authorize(ctx, contract.PermApprovalDecide)
	if err != nil {
		return none, false, err
	}
	if ctx, err = s.prefetchOrgFacts(ctx); err != nil {
		return none, false, err
	}
	rec, err := s.Purchases.GetPR(ctx, s.DB, prID, repository.ScopeAll)
	if err != nil {
		return none, false, nil // 없는 건과 볼 수 없는 건을 구별하지 않는다
	}
	if (rec.Status != domain.PRSubmitted && rec.Status != domain.PRInReview) || rec.RequesterID == p.UserID {
		return none, false, nil
	}
	pending, err := s.Approvals.PendingSteps(ctx, s.DB, prID, rec.Round)
	if err != nil {
		return none, false, err
	}
	decided, err := s.Delegations.DecidedIDs(ctx, s.DB, prID)
	if err != nil {
		return none, false, err
	}
	if hasID(decided, p.UserID) {
		return none, false, nil
	}
	e := s.elig()
	now := s.now()
	day := contract.DayOf(now)
	refOf := func(st domain.ApprovalStep) repository.StepRef {
		role := st.Role
		if role == "" {
			role = domain.RoleApprove
		}
		return repository.StepRef{PRID: prID, Round: rec.Round, Seq: st.Seq, Kind: st.Kind, Status: st.Status, Role: role, Stage: st.Stage, RequesterID: rec.RequesterID}
	}
	for _, st := range pending {
		direct, err := e.direct(ctx, s.DB, refOf(st), day)
		if err != nil {
			return none, false, err
		}
		if hasID(direct, p.UserID) {
			return none, false, nil // 직접 결재자면 위임 안내가 없다
		}
	}
	for _, st := range pending {
		r := refOf(st)
		if r.Role != domain.RoleApprove {
			continue
		}
		d, ok, err := e.findActing(ctx, s.DB, p.UserID, r, now)
		if err != nil {
			return none, false, err
		}
		if ok {
			return ActingNotice{DelegatorID: d.DelegatorID, DelegatorName: d.DelegatorName, From: d.From, To: d.To}, true, nil
		}
	}
	return none, false, nil
}
