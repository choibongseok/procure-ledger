package service

import (
	"context"
	"fmt"
	"log"
	"time"
	"unicode/utf8"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/repository"
)

// 부재·대결(B29 ①, 3절). 부재는 위임(approval_delegations)의 kind='absence' 행이다.
// 누가 결재할 수 있는지는 후보 계산(approval_eligibility.go)이 한 곳에서 정하고, 부재는 "기간 안의 유효한 위임"으로 거기에 들어간다.
// 대결자의 대결자는 없다: 후보 계산이 배정자의 직접 위임만 보므로 연쇄가 생기지 않는다(시험으로 확인).

const maxAbsenceDays = 90

var kstZone = time.FixedZone("KST", 9*3600)

// absencePeriodText 는 "2026. 11. 20.~11. 24."(같은 해면 끝의 연도 생략) 꼴의 기간 문구다.
func absencePeriodText(from, to time.Time) string {
	f, t := from.In(kstZone), to.In(kstZone)
	end := fmt.Sprintf("%d. %d.", t.Month(), t.Day())
	if t.Year() != f.Year() {
		end = fmt.Sprintf("%d. ", t.Year()) + end
	}
	return fmt.Sprintf("%d. %d. %d.~%s", f.Year(), f.Month(), f.Day(), end)
}

// checkAbsence 는 부재 등록 입력을 검사하고 위임자·대결자를 돌려준다(읽기만 한다).
func (s *DelegationService) checkAbsence(ctx context.Context, p contract.Principal, in domain.DelegationInput) (repository.UserRec, repository.UserRec, error) {
	var from, to repository.UserRec
	if _, ok := domain.AbsenceReasons[in.Reason]; !ok {
		return from, to, validation("부재 사유를 고르세요.")
	}
	if in.DelegateID == 0 {
		return from, to, validation("대결자를 고르세요.")
	}
	if utf8.RuneCountInString(in.Note) > 100 {
		return from, to, validation("메모는 100자 이내로 입력하세요.")
	}
	now := s.now()
	if in.From.IsZero() || in.To.IsZero() || !in.To.After(in.From) || in.From.Before(now.Add(-time.Minute)) {
		return from, to, validation("부재 기간이 올바르지 않습니다. 시작은 지금 이후, 종료는 시작 이후로 정하세요.")
	}
	if in.To.Sub(in.From) > maxAbsenceDays*24*time.Hour {
		return from, to, validation("부재는 한 번에 90일까지 등록할 수 있습니다. 더 길면 나누어 등록하세요.")
	}
	from, err := s.Delegations.User(ctx, s.DB, p.UserID)
	if err != nil {
		return from, to, err
	}
	day := contract.DayOf(now)
	title, err := s.delegatorTitle(ctx, from, day)
	if err != nil {
		return from, to, err
	}
	if title == "" {
		return from, to, fmt.Errorf("%w: 결재 단계를 맡고 있지 않아 부재를 등록할 수 없습니다", contract.ErrForbidden)
	}
	if o, found, err := s.Delegations.OverlappingAbsence(ctx, s.DB, from.ID, in.From, in.To); err != nil {
		return from, to, err
	} else if found {
		return from, to, validation("이미 등록된 부재(%s)와 기간이 겹칩니다. 기간을 바꾸거나 기존 부재를 해제하세요.", absencePeriodText(o.From, o.To))
	}
	to, err = s.Delegations.User(ctx, s.DB, in.DelegateID)
	if err != nil {
		return from, to, validation("이 사람은 대결자로 지정할 수 없습니다(사용자를 찾을 수 없습니다). 후보 목록에서 다른 사람을 고르세요.")
	}
	if ok, err := s.sameEntityTarget(ctx, s.DB, "user", to.ID, day); err != nil {
		return from, to, err
	} else if !ok {
		return from, to, validation(errDifferentEntity)
	}
	if ok, err := s.canDelegateTo(ctx, from, to, day); err != nil {
		return from, to, err
	} else if !ok {
		return from, to, validation("이 사람은 대결자로 지정할 수 없습니다(%s 결재는 같은 직책 이상의 조직장만 맡을 수 있습니다). 후보 목록에서 다른 사람을 고르세요.", title)
	}
	if _, away, err := s.Delegations.OverlappingAbsence(ctx, s.DB, to.ID, in.From, in.To); err != nil {
		return from, to, err
	} else if away {
		return from, to, validation("이 사람은 대결자로 지정할 수 없습니다(같은 기간에 부재 중입니다). 후보 목록에서 다른 사람을 고르세요.")
	}
	return from, to, nil
}

// pendingFor 는 지금 그 사람의 차례인 결재 건수다(재무는 재무 단계 포함). 같은 문서는 한 번만 센다.
func (s *DelegationService) pendingFor(ctx context.Context, tx contract.Tx, userID int64) (int, error) {
	apr := repository.SQLApprovals{}
	refs, err := apr.AssignedOpenSteps(ctx, tx, userID)
	if err != nil {
		return 0, err
	}
	if u, err := s.Delegations.User(ctx, tx, userID); err == nil && u.Can(contract.PermFinanceStage) {
		fin, err := apr.FinanceOpenSteps(ctx, tx)
		if err != nil {
			return 0, err
		}
		refs = append(refs, fin...)
	}
	seen := map[int64]bool{}
	for _, r := range refs {
		if r.Status == domain.StepPending && r.Role == "approve" {
			seen[r.PRID] = true
		}
	}
	return len(seen), nil
}

func (s *DelegationService) notifyAbsenceAssigned(ctx context.Context, tx contract.Tx, from, to repository.UserRec, in domain.DelegationInput) error {
	n, err := s.pendingFor(ctx, tx, from.ID)
	if err != nil {
		return err
	}
	return s.notify(ctx, tx, repository.NotificationInsert{UserID: to.ID, Kind: "absence_assigned", Title: "대결자로 지정되었습니다",
		Body: fmt.Sprintf("%s님이 %s 부재 중이라 대결자로 지정되었습니다. 지금 대결할 수 있는 결재가 %d건 있습니다.", from.Name, absencePeriodText(in.From, in.To), n),
		Link: "/approvals", RefType: "delegation"})
}

func (s *DelegationService) notifyAbsenceRevoked(ctx context.Context, tx contract.Tx, d repository.DelegationRec, now time.Time) error {
	if err := s.notify(ctx, tx, repository.NotificationInsert{UserID: d.DelegateID, Kind: "absence_revoked", Title: "부재가 해제되었습니다",
		Body: fmt.Sprintf("%s님이 부재(%s)를 해제해 대결자 지정이 끝났습니다.", d.DelegatorName, absencePeriodText(d.From, d.To)), Link: "/approvals", RefType: "delegation", RefID: d.ID, Now: now}); err != nil {
		return err
	}
	return s.notifyAbsenceEnded(ctx, tx, d, now)
}

func (s *DelegationService) notifyAbsenceEnded(ctx context.Context, tx contract.Tx, d repository.DelegationRec, now time.Time) error {
	n, err := s.pendingFor(ctx, tx, d.DelegatorID)
	if err != nil {
		return err
	}
	return s.notify(ctx, tx, repository.NotificationInsert{UserID: d.DelegatorID, Kind: "absence_ended", Title: "부재가 끝났습니다",
		Body: fmt.Sprintf("부재가 끝났습니다. 대기 중인 결재 %d건이 있습니다.", n), Link: "/approvals", RefType: "delegation", RefID: d.ID, Now: now})
}

// Tick 은 예정 부재의 시작 알림(대결자에게)과 종료 알림(원결재자에게)을 만든다. 서버 주기 작업이 부르며, 표시 열이 있어 여러 번 불러도 한 번씩만 만든다.
func (s *DelegationService) Tick(ctx context.Context) error {
	now := s.now()
	// 휴일(주말·공휴일)에는 새 알림을 만들지 않는다. 시작·종료 대상은 시각으로 고르고 표시 열로 한 번만 만들므로 다음 영업일 첫 실행에서 합쳐진다(B34).
	if ok, err := s.isBusinessDay(ctx, s.DB, s.kstDay()); err != nil || !ok {
		return err
	}
	started, err := s.Delegations.NoticeStarted(ctx, s.DB, now)
	if err != nil {
		return err
	}
	ended, err := s.Delegations.NoticeEnded(ctx, s.DB, now)
	if err != nil {
		return err
	}
	for _, d := range started {
		d := d
		err := s.Txs.WithTx(ctx, func(tx contract.Tx) error {
			if ok, err := s.Delegations.MarkNotified(ctx, tx, d.ID, "start", now); err != nil || !ok {
				return err
			}
			n, err := s.pendingFor(ctx, tx, d.DelegatorID)
			if err != nil {
				return err
			}
			return s.notify(ctx, tx, repository.NotificationInsert{UserID: d.DelegateID, Kind: "absence_started", Title: "대결 기간이 시작되었습니다",
				Body: fmt.Sprintf("%s님의 부재(%s)가 시작되었습니다. 지금 대결할 수 있는 결재가 %d건 있습니다.", d.DelegatorName, absencePeriodText(d.From, d.To), n),
				Link: "/approvals", RefType: "delegation", RefID: d.ID, Now: now})
		})
		if err != nil {
			return err
		}
	}
	for _, d := range ended {
		d := d
		err := s.Txs.WithTx(ctx, func(tx contract.Tx) error {
			if ok, err := s.Delegations.MarkNotified(ctx, tx, d.ID, "end", now); err != nil || !ok {
				return err
			}
			return s.notifyAbsenceEnded(ctx, tx, d, now)
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// Run 은 서버가 켜져 있는 동안 1분마다 부재 시작·종료 알림을 만든다(기동 때 한 번도 한다). 부재의 효력 자체는 이 작업과 무관하게 후보 계산이 기간으로 판정한다.
func (s *DelegationService) Run(ctx context.Context) {
	tick := func() {
		if err := s.Tick(ctx); err != nil && ctx.Err() == nil {
			log.Printf("absence: 부재 알림 생성 실패: %v", err)
		}
	}
	tick()
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			tick()
		}
	}
}

// annotateAbsence 는 결재선 미리보기의 각 단계에 결재자의 부재(종료 시각과 대결자)를 붙인다. 부재 조회가 실패해도 미리보기는 그대로 돌려준다(안내일 뿐이다).
func (b Base) annotateAbsence(ctx context.Context, steps []domain.LineStep) {
	now := b.now()
	repo := repository.SQLDelegations{}
	for i := range steps {
		if steps[i].ApproverID == 0 || steps[i].Omitted || steps[i].Missing {
			continue
		}
		dels, err := repo.ActiveFrom(ctx, b.DB, steps[i].ApproverID, now, 0)
		if err != nil {
			return
		}
		for _, d := range dels {
			if d.Kind == domain.DelegationKindAbsence {
				end := d.To
				steps[i].AbsentUntil, steps[i].AbsenceDelegate = &end, d.DelegateName
				break
			}
		}
	}
}
