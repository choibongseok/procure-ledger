package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/repository"
)

// DelegationService 는 domain.DelegationService 의 구현이다. 결재자가 기간을 정해 같은 직책 이상에게 결재를 맡긴다.
type DelegationService struct {
	Base
	Delegations repository.DelegationRepository
}

var _ domain.DelegationService = (*DelegationService)(nil)

// NewDelegationService 는 SQLite 저장소로 위임 서비스를 만든다.
func NewDelegationService(b Base) *DelegationService {
	return &DelegationService{Base: b, Delegations: repository.SQLDelegations{}}
}

// elig 는 위임 서열·후보 판정(approval_eligibility.go)을 이 서비스의 저장소로 만든다.
func (s *DelegationService) elig() eligibility {
	return eligibility{approvals: repository.SQLApprovals{}, delegations: s.Delegations, purchases: repository.SQLPurchases{}, org: s.Org}
}

// delegatorTitle 은 위임자가 맡는 결재 직책 이름이다(재무 단계 구조 권한은 "재무", 조직장은 가장 높은 조직의 직책). 맡는 결재 단계가 없으면 "".
func (s *DelegationService) delegatorTitle(ctx context.Context, u repository.UserRec, day contract.Day) (string, error) {
	switch {
	case u.Can(contract.PermFinanceStage):
		return "재무", nil
	case u.Can(contract.PermApprovalDecide):
		_, unit, err := s.elig().headRank(ctx, u.ID, day)
		if err != nil || unit.ID == 0 {
			return "", err
		}
		return contract.HeadTitle(unit.Kind), nil
	}
	return "", nil
}

// canDelegateTo 는 직책 서열 규칙의 유일한 정의다(B11, 명세 3.6). 재무는 재무에게만, 조직장(결재 권한 manager)은 자기와 같거나 더 높은 층의 조직장에게 맡길 수 있다.
// 자기 자신, 비활성 사용자, 결재 권한이 없는 역할은 수임자가 될 수 없다. 부서는 따지지 않는다.
func (s *DelegationService) canDelegateTo(ctx context.Context, from, to repository.UserRec, day contract.Day) (bool, error) {
	if from.ID == to.ID || !from.Active || !to.Active {
		return false, nil
	}
	// 위임 자격: 둘 다 결재 권한(approval:decide)이 있고 재무 단계 구조 권한 보유 여부가 같아야 한다.
	if !from.Can(contract.PermApprovalDecide) || !to.Can(contract.PermApprovalDecide) {
		return false, nil
	}
	fromStage, toStage := from.Can(contract.PermFinanceStage), to.Can(contract.PermFinanceStage)
	if fromStage != toStage {
		return false, nil
	}
	if fromStage {
		return true, nil
	}
	e := s.elig()
	fr, err := e.rankOf(ctx, from, day)
	if err != nil || fr == 0 {
		return false, err
	}
	tr, err := e.rankOf(ctx, to, day)
	return tr >= fr, err
}

func toDelegation(r repository.DelegationRec) domain.Delegation {
	return domain.Delegation{ID: r.ID, DelegatorID: r.DelegatorID, DelegatorName: r.DelegatorName,
		DelegateID: r.DelegateID, DelegateName: r.DelegateName, From: r.From, To: r.To, RevokedAt: r.RevokedAt, CreatedAt: r.CreatedAt, Kind: r.Kind, Reason: r.Reason, Note: r.Note}
}

func ts(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// Create 는 위임을 지정한다. 기간은 필수이고 끝이 지금보다 뒤여야 한다.
func (s *DelegationService) Create(ctx context.Context, in domain.DelegationInput) (domain.Delegation, error) {
	p, err := s.authorize(ctx, contract.PermApprovalDelegate, "delegation.create", audit.TargetDelegation)
	if err != nil {
		return domain.Delegation{}, err
	}
	// 위임자·수임자 검사는 조직 조회(자기 DB 핸들로 읽는다)가 필요해 트랜잭션 밖에서 한다.
	from, to, err := s.checkCreate(ctx, p, in)
	if err != nil {
		if errors.Is(err, contract.ErrForbidden) {
			s.recordDenied(ctx, p, "delegation.create", audit.TargetDelegation, 0, err.Error())
		}
		return domain.Delegation{}, err
	}
	var out repository.DelegationRec
	err = s.write(ctx, p, "delegation.create", audit.TargetDelegation, 0, func(tx contract.Tx) error {
		id, err := s.Delegations.Insert(ctx, tx, repository.DelegationInsert{
			DelegatorID: from.ID, DelegateID: to.ID, From: in.From, To: in.To, Now: s.now(), Kind: in.Kind, Reason: in.Reason, Note: in.Note})
		if err != nil {
			return err
		}
		diff := []audit.DiffItem{
			{Key: "delegate_id", New: fmt.Sprint(to.ID)},
			{Key: "from", New: ts(in.From)},
			{Key: "to", New: ts(in.To)}}
		if in.Kind == domain.DelegationKindAbsence {
			diff = append(diff, audit.DiffItem{Key: "kind", New: "부재"}, audit.DiffItem{Key: "reason", New: domain.AbsenceReasons[in.Reason]})
			if err := s.notifyAbsenceAssigned(ctx, tx, from, to, in); err != nil {
				return err
			}
		}
		if err := s.record(ctx, tx, p, "delegation.create", audit.TargetDelegation, id, diff...); err != nil {
			return err
		}
		out, err = s.Delegations.Get(ctx, tx, id)
		return err
	})
	return toDelegation(out), err
}

// createLeaveAbsence 는 승인된 휴가의 부재 행을 만든다(B50 6.7). 위임 행은 이 서비스에서만 만든다. 만들었으면 번호를, 자격·기간·겹침 때문에 건너뛰면 0 과 이유를 돌려준다.
// 결재 트랜잭션 안에서 부르므로 트랜잭션 핸들로만 읽는다(직책 서열은 신청 때 LeaveService.checkDelegate 가 검사했다).
func (s *DelegationService) createLeaveAbsence(ctx context.Context, tx contract.Tx, delegatorID, delegateID int64, from, to time.Time) (int64, string, error) {
	delegator, err := s.Delegations.User(ctx, tx, delegatorID)
	if err != nil || !delegator.Active {
		return 0, "신청자가 재직 중이 아닙니다", nil
	}
	delegate, err := s.Delegations.User(ctx, tx, delegateID)
	if err != nil || !delegate.Active || !delegate.Can(contract.PermApprovalDecide) || delegate.ID == delegator.ID {
		return 0, "대결자가 결재할 수 있는 상태가 아닙니다", nil
	}
	if o, found, err := s.Delegations.OverlappingAbsence(ctx, tx, delegator.ID, from, to); err != nil {
		return 0, "", err
	} else if found {
		return 0, "이미 등록된 부재(" + absencePeriodText(o.From, o.To) + ")와 기간이 겹칩니다", nil
	}
	if _, away, err := s.Delegations.OverlappingAbsence(ctx, tx, delegate.ID, from, to); err != nil {
		return 0, "", err
	} else if away {
		return 0, "대결자가 같은 기간에 부재 중입니다", nil
	}
	in := domain.DelegationInput{DelegateID: delegate.ID, From: from, To: to, Kind: domain.DelegationKindAbsence, Reason: "vacation", Note: "휴가 신청 연동"}
	id, err := s.Delegations.Insert(ctx, tx, repository.DelegationInsert{DelegatorID: delegator.ID, DelegateID: delegate.ID, From: from, To: to, Now: s.now(), Kind: in.Kind, Reason: in.Reason, Note: in.Note})
	if err != nil {
		return 0, "", err
	}
	if err := s.notifyAbsenceAssigned(ctx, tx, delegator, delegate, in); err != nil {
		return 0, "", err
	}
	return id, "", nil
}

// Revoke 는 위임자 본인이 언제든 위임을 해제한다. 남의 위임은 존재를 숨긴다(ErrNotFound).
func (s *DelegationService) Revoke(ctx context.Context, id int64) error {
	p, err := s.authorize(ctx, contract.PermApprovalDelegate, "delegation.revoke", audit.TargetDelegation)
	if err != nil {
		return err
	}
	if ctx, err = s.prefetchOrgFacts(ctx); err != nil {
		return err
	}
	err = s.write(ctx, p, "delegation.revoke", audit.TargetDelegation, id, func(tx contract.Tx) error {
		d, err := s.Delegations.Get(ctx, tx, id)
		if err != nil {
			return err
		}
		if d.DelegatorID != p.UserID {
			return contract.ErrNotFound
		}
		now := s.now()
		if d.RevokedAt == nil && !now.Before(d.From) && now.Before(d.To) {
			n, err := s.stalledByRevoke(ctx, tx, d, now)
			if err != nil {
				return err
			}
			if n > 0 {
				return fmt.Errorf("%w: 이 위임을 해제하면 결재할 사람이 없어 멈추는 진행 중 결재 건이 %d건 있어 해제할 수 없습니다. 해당 건의 결재가 끝난 뒤 해제하거나 다른 결재자에게 새로 위임하세요: %w",
					contract.ErrInvalidState, n, errRevokeBlocked)
			}
		}
		if err := s.Delegations.Revoke(ctx, tx, id, now); err != nil {
			return fmt.Errorf("%w: 이미 해제된 위임입니다", contract.ErrInvalidState)
		}
		if d.Kind == domain.DelegationKindAbsence {
			if err := s.notifyAbsenceRevoked(ctx, tx, d, now); err != nil {
				return err
			}
		}
		return s.record(ctx, tx, p, "delegation.revoke", audit.TargetDelegation, id,
			audit.DiffItem{Key: "revoked_at", Old: "", New: ts(now)},
			audit.DiffItem{Key: "delegate_id", New: fmt.Sprint(d.DelegateID)})
	})
	if errors.Is(err, errRevokeBlocked) {
		s.recordDenied(ctx, p, "delegation.revoke", audit.TargetDelegation, id, err.Error())
	}
	return err
}

var errRevokeBlocked = errors.New("위임 해제 거부")

// stalledByRevoke 는 위임 d 를 해제하면 남은 단계(지금 차례와 뒤 단계 모두)를 서로 다른 결재자가 맡을 수 없게 되는 진행 중 건 수다.
// 위임자가 현재 결재자인 결재 전 단계(재무 위임자는 재무 단계 포함)가 있는 건만 센다.
func (s *DelegationService) stalledByRevoke(ctx context.Context, tx contract.Tx, d repository.DelegationRec, now time.Time) (int, error) {
	apr := repository.SQLApprovals{}
	refs, err := apr.AssignedOpenSteps(ctx, tx, d.DelegatorID)
	if err != nil {
		return 0, err
	}
	if from, err := s.Delegations.User(ctx, tx, d.DelegatorID); err == nil && from.Can(contract.PermFinanceStage) {
		fin, err := apr.FinanceOpenSteps(ctx, tx)
		if err != nil {
			return 0, err
		}
		refs = append(refs, fin...)
	}
	e := s.elig()
	n, seen := 0, map[int64]bool{}
	for _, r := range refs {
		if seen[r.PRID] {
			continue
		}
		seen[r.PRID] = true
		open, err := apr.OpenSteps(ctx, tx, r.PRID, r.Round)
		if err != nil {
			return 0, err
		}
		with, err := e.remainingAssignable(ctx, tx, open, now, 0, 0)
		if err != nil {
			return 0, err
		}
		without, err := e.remainingAssignable(ctx, tx, open, now, 0, d.ID)
		if err != nil {
			return 0, err
		}
		if with && !without {
			n++
		}
	}
	return n, nil
}

// ListMine 은 내가 맡긴 위임과 내가 받은 위임이다.
func (s *DelegationService) ListMine(ctx context.Context) ([]domain.Delegation, []domain.Delegation, error) {
	p, err := contract.Authorize(ctx, contract.PermApprovalDelegate)
	if err != nil {
		return nil, nil, err
	}
	g, err := s.Delegations.ListGiven(ctx, s.DB, p.UserID)
	if err != nil {
		return nil, nil, err
	}
	r, err := s.Delegations.ListReceived(ctx, s.DB, p.UserID)
	if err != nil {
		return nil, nil, err
	}
	gd, rd := mapDelegations(g), mapDelegations(r)
	s.markDelegationNames(ctx, gd)
	s.markDelegationNames(ctx, rd)
	return gd, rd, nil
}

func mapDelegations(in []repository.DelegationRec) []domain.Delegation {
	out := make([]domain.Delegation, len(in))
	for i, r := range in {
		out[i] = toDelegation(r)
	}
	return out
}

// Candidates 는 내가 위임할 수 있는 사람이다(직책 규칙 통과자).
func (s *DelegationService) Candidates(ctx context.Context) ([]domain.DelegateCandidate, error) {
	p, err := contract.Authorize(ctx, contract.PermApprovalDelegate)
	if err != nil {
		return nil, err
	}
	from, err := s.Delegations.User(ctx, s.DB, p.UserID)
	if err != nil {
		return nil, err
	}
	us, err := s.Delegations.Candidates(ctx, s.DB, p.UserID)
	if err != nil {
		return nil, err
	}
	day := contract.DayOf(s.now())
	out := []domain.DelegateCandidate{}
	for _, u := range us {
		ok, err := s.canDelegateTo(ctx, from, u, day)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		title, err := s.delegatorTitle(ctx, u, day)
		if err != nil {
			return nil, err
		}
		unit := ""
		if !u.Can(contract.PermFinanceStage) {
			if _, hu, err := s.elig().headRank(ctx, u.ID, day); err == nil {
				unit = hu.Name
			}
		}
		now := s.now()
		_, away, err := s.Delegations.OverlappingAbsence(ctx, s.DB, u.ID, now, now.Add(time.Second))
		if err != nil {
			return nil, err
		}
		out = append(out, domain.DelegateCandidate{ID: u.ID, Name: u.Name, Role: u.Role, DepartmentID: u.DepartmentID, Title: title, UnitName: unit, Away: away || s.elig().onLeave(ctx, u.ID, day)})
	}
	return out, nil
}

// checkCreate 는 위임 지정 입력을 검사하고 위임자·수임자를 돌려준다(읽기만 한다).
func (s *DelegationService) checkCreate(ctx context.Context, p contract.Principal, in domain.DelegationInput) (repository.UserRec, repository.UserRec, error) {
	var from, to repository.UserRec
	if in.Kind == domain.DelegationKindAbsence {
		return s.checkAbsence(ctx, p, in)
	}
	if in.From.IsZero() || in.To.IsZero() {
		return from, to, validation("위임 기간(시작·종료)을 입력해야 합니다")
	}
	if !in.To.After(in.From) {
		return from, to, validation("위임 종료일은 시작일보다 뒤여야 합니다")
	}
	if !in.To.After(s.now()) {
		return from, to, validation("이미 끝난 기간에는 위임할 수 없습니다")
	}
	from, err := s.Delegations.User(ctx, s.DB, p.UserID)
	if err != nil {
		return from, to, err
	}
	day := contract.DayOf(s.now())
	title, err := s.delegatorTitle(ctx, from, day)
	if err != nil {
		return from, to, err
	}
	if title == "" {
		return from, to, fmt.Errorf("%w: 결재 단계를 맡고 있지 않아 위임할 수 없습니다", contract.ErrForbidden)
	}
	to, err = s.Delegations.User(ctx, s.DB, in.DelegateID)
	if err != nil {
		return from, to, validation("위임받을 사용자를 찾을 수 없습니다")
	}
	if from.ID == to.ID {
		return from, to, validation("자기 자신에게는 위임할 수 없습니다")
	}
	if ok, err := s.sameEntityTarget(ctx, s.DB, "user", to.ID, day); err != nil {
		return from, to, err
	} else if !ok {
		return from, to, validation(errDifferentEntity)
	}
	if ok, err := s.canDelegateTo(ctx, from, to, day); err != nil {
		return from, to, err
	} else if !ok {
		return from, to, validation("%s 결재는 같은 직책 이상의 결재자에게만 위임할 수 있습니다", title)
	}
	return from, to, nil
}
