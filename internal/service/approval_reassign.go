package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/repository"
)

// 관리자 결재자 재지정(B21 3.8). 스냅샷 행은 고치지 않고 재지정 기록(approval_step_reassignments)을 추가한다.
// 후보 계산(approversOf)은 뷰 approval_step_current 가 최신 재지정을 따른다.

const maxReassignReasonRunes = 500

var reassignCauseLabel = map[string]string{"leave": "퇴사", "transfer": "전보", "absence": "장기 부재", "other": "기타"}

// ReassignService 는 domain.ReassignService 의 구현이다. 권한 approval:reassign(admin).
type ReassignService struct {
	Base
	Purchases   repository.PurchaseRepository
	Approvals   repository.ApprovalRepository
	Delegations repository.DelegationRepository
}

var _ domain.ReassignService = (*ReassignService)(nil)

// NewReassignService 는 SQLite 저장소로 재지정 서비스를 만든다.
func NewReassignService(b Base) *ReassignService {
	return &ReassignService{Base: b, Purchases: repository.SQLPurchases{}, Approvals: repository.SQLApprovals{}, Delegations: repository.SQLDelegations{}}
}

func (s *ReassignService) elig() eligibility {
	return eligibility{approvals: s.Approvals, delegations: s.Delegations, purchases: s.Purchases, org: s.Org}
}

// reassignTarget 은 재지정할 수 있는 단계를 찾는다. 재지정 조건 중 건·단계에 관한 것(3.8 의 2~4)을 검사한다.
func (s *ReassignService) reassignTarget(ctx context.Context, tx contract.Tx, prID int64, seq int) (repository.PRRecord, domain.ApprovalStep, error) {
	rec, err := s.Purchases.GetPR(ctx, tx, prID, repository.ScopeAll)
	if err != nil {
		return rec, domain.ApprovalStep{}, err
	}
	if rec.Status != domain.PRSubmitted && rec.Status != domain.PRInReview {
		return rec, domain.ApprovalStep{}, fmt.Errorf("%w: 결재 진행 중인 건만 결재자를 바꿀 수 있습니다", contract.ErrInvalidState)
	}
	var step domain.ApprovalStep
	found := false
	for _, st := range rec.Steps {
		if st.Seq == seq {
			step, found = st, true
		}
	}
	if !found {
		return rec, step, fmt.Errorf("%w: 결재 단계를 찾을 수 없습니다", contract.ErrNotFound)
	}
	if isPool(step.Kind) {
		return rec, step, validation("재무 단계는 재무 담당자 역할을 가진 사람이 맡습니다. 재무 담당 역할을 부여하면 바로 결재할 수 있습니다")
	}
	if step.Status != domain.StepPending && step.Status != domain.StepWaiting {
		return rec, step, fmt.Errorf("%w: 이미 결재가 끝난 단계는 결재자를 바꿀 수 없습니다", contract.ErrInvalidState)
	}
	return rec, step, nil
}

// newApproverProblem 은 사용자 u 가 이 단계의 새 결재자가 될 수 없는 이유 문장이다(자격 표, 3.8). 문제가 없으면 "".
func (s *ReassignService) newApproverProblem(ctx context.Context, tx contract.Tx, rec repository.PRRecord, step domain.ApprovalStep, u repository.LineUserRec, current []int64) (string, error) {
	e := s.elig()
	day := contract.DayOf(s.now())
	role := step.Role
	if role == "" {
		role = domain.RoleApprove
	}
	perm := contract.PermApprovalDecide
	if role == domain.RoleAgree {
		perm = contract.PermApprovalAgree
	}
	ok, err := e.usableFor(ctx, tx, u.ID, day, role)
	if err != nil {
		return "", err
	}
	if !u.Active || (!ok && u.Can(perm)) {
		if u.Active && e.onLeave(ctx, u.ID, day) {
			return "휴직 중인 사용자는 결재자가 될 수 없습니다.", nil
		}
		return "재직 중이 아닌 사용자입니다.", nil
	}
	if !u.Can(perm) {
		if role == domain.RoleAgree {
			return "합의 권한이 없는 사용자는 합의자가 될 수 없습니다.", nil
		}
		return "결재 권한이 없는 사용자는 결재자가 될 수 없습니다.", nil
	}
	if role != domain.RoleAgree {
		// 직책 단계는 그 직책 이상의 조직장이어야 한다. 합의 단계는 층 요건이 없다(명세 3.8).
		ki, _ := kindOf(step.Kind)
		level, err := s.headLevel(ctx, u.ID, day)
		if err != nil {
			return "", err
		}
		if level < ki.Level {
			return fmt.Sprintf("%s님은 %s 이상의 조직장이어야 이 단계를 맡을 수 있습니다.", u.Name, ki.Label), nil
		}
	}
	if u.ID == rec.RequesterID {
		return "요청자 본인은 결재자가 될 수 없습니다.", nil
	}
	for _, st := range rec.Steps {
		if st.Seq == step.Seq || isPool(st.Kind) {
			continue
		}
		ids, err := s.Approvals.StepAssignees(ctx, tx, rec.ID, rec.Round, st.Seq)
		if err != nil {
			return "", err
		}
		if hasID(ids, u.ID) {
			return fmt.Sprintf("%s님은 이 결재선의 다른 단계를 이미 맡고 있습니다.", u.Name), nil
		}
	}
	decided, err := s.Delegations.DecidedIDs(ctx, tx, rec.ID)
	if err != nil {
		return "", err
	}
	if hasID(decided, u.ID) {
		return fmt.Sprintf("%s님은 이 건의 다른 단계를 이미 결재했습니다.", u.Name), nil
	}
	if hasID(current, u.ID) {
		return "이미 이 단계의 결재자입니다.", nil
	}
	return "", nil
}

// headLevel 은 사용자가 조직장(또는 직무대행자)인 조직 중 가장 높은 층이다. 아니면 0.
func (s *ReassignService) headLevel(ctx context.Context, userID int64, day contract.Day) (int, error) {
	if s.Org == nil {
		return 0, errors.New("조직 서비스가 연결되지 않았습니다")
	}
	best := 0
	units, err := s.Org.UnitsHeadedBy(ctx, userID, day)
	if err != nil && !errors.Is(err, contract.ErrNotInOrg) && !errors.Is(err, contract.ErrNotFound) {
		return 0, err
	}
	for _, u := range units {
		best = max(best, u.Kind.Level())
	}
	person, err := s.Org.Person(ctx, userID, day)
	if err != nil && !errors.Is(err, contract.ErrNotInOrg) && !errors.Is(err, contract.ErrNotFound) {
		return 0, err
	}
	if err == nil {
		for _, pl := range person.Acting {
			best = max(best, pl.Unit.Kind.Level())
		}
	}
	return best, nil
}

// ReassignStep 은 건 prID 의 현재 차수에서 단계 seq 의 결재자를 바꾼다.
func (s *ReassignService) ReassignStep(ctx context.Context, prID int64, seq int, in domain.ReassignInput) (domain.PurchaseRequest, error) {
	p, err := s.authorize(ctx, contract.PermApprovalReassign, "approval.reassign", audit.TargetApproval)
	if err != nil {
		return domain.PurchaseRequest{}, err
	}
	causeLabel, ok := reassignCauseLabel[in.Cause]
	if !ok {
		return domain.PurchaseRequest{}, validation("구분을 선택하세요")
	}
	reason := strings.TrimSpace(in.Reason)
	if reason == "" {
		return domain.PurchaseRequest{}, validation("사유를 입력해야 합니다")
	}
	if utf8.RuneCountInString(reason) > maxReassignReasonRunes {
		return domain.PurchaseRequest{}, validation("사유는 %d자 이내로 입력하세요", maxReassignReasonRunes)
	}
	// 사전 검증: 조직 조회(자기 DB 핸들로 읽는다)가 필요해 트랜잭션 밖에서 한다. 트랜잭션 안에서는 상태가 그대로인지만 다시 확인한다.
	lines := repository.SQLLines{}
	rec0, step0, err := s.reassignTarget(ctx, s.DB, prID, seq)
	if err != nil {
		return domain.PurchaseRequest{}, err
	}
	ref := repository.StepRef{PRID: prID, Round: rec0.Round, Seq: seq, Kind: step0.Kind, Status: step0.Status, Role: step0.Role, Stage: step0.Stage, RequesterID: rec0.RequesterID}
	current, err := s.Approvals.StepAssignees(ctx, s.DB, prID, rec0.Round, seq)
	if err != nil {
		return domain.PurchaseRequest{}, err
	}
	usableNow, err := s.elig().direct(ctx, s.DB, ref, contract.DayOf(s.now()))
	if err != nil {
		return domain.PurchaseRequest{}, err
	}
	if len(usableNow) > 0 && !in.ConfirmActive {
		name := ""
		if u, err := lines.User(ctx, s.DB, usableNow[0]); err == nil {
			name = u.Name
		}
		return domain.PurchaseRequest{}, validation("현재 결재자 %s님은 재직 중이며 결재할 수 있습니다. 그래도 바꾸려면 확인이 필요합니다", name)
	}
	nu, err := lines.User(ctx, s.DB, in.UserID)
	if errors.Is(err, contract.ErrNotFound) {
		return domain.PurchaseRequest{}, validation("새 결재자를 찾을 수 없습니다")
	}
	if err != nil {
		return domain.PurchaseRequest{}, err
	}
	if problem, err := s.newApproverProblem(ctx, s.DB, rec0, step0, nu, current); err != nil {
		return domain.PurchaseRequest{}, err
	} else if problem != "" {
		return domain.PurchaseRequest{}, validation("%s", strings.TrimSuffix(problem, "."))
	}
	if len(current) == 0 {
		return domain.PurchaseRequest{}, fmt.Errorf("%w: 현재 결재자를 알 수 없는 단계입니다", contract.ErrInvalidState)
	}
	fromID, fromName := current[0], ""
	if u, err := lines.User(ctx, s.DB, fromID); err == nil {
		fromName = u.Name
	}
	if ctx, err = s.prefetchOrgFacts(ctx); err != nil {
		return domain.PurchaseRequest{}, err
	}

	var out repository.PRRecord
	err = s.write(ctx, p, "approval.reassign", audit.TargetApproval, prID, func(tx contract.Tx) error {
		rec, step, err := s.reassignTarget(ctx, tx, prID, seq)
		if err != nil {
			return err
		}
		cur, err := s.Approvals.StepAssignees(ctx, tx, prID, rec.Round, seq)
		if err != nil {
			return err
		}
		if len(cur) != len(current) || (len(cur) > 0 && cur[0] != current[0]) {
			return fmt.Errorf("%w: 처리하는 동안 결재 단계가 바뀌었습니다. 화면을 새로 고치세요", contract.ErrInvalidState)
		}
		now := s.now()
		if err := s.Approvals.InsertReassignment(ctx, tx, repository.ReassignInsert{PRID: prID, Round: rec.Round, Seq: seq, FromID: fromID, FromName: fromName,
			ToID: nu.ID, ToName: nu.Name, Cause: in.Cause, Reason: reason, ActorID: p.UserID, Now: now}); err != nil {
			return err
		}
		if err := s.Approvals.InsertHistory(ctx, tx, repository.HistoryInsert{PRID: prID, Round: rec.Round, Seq: seq, ActorID: p.UserID, Action: domain.ActionReassign,
			Comment: fmt.Sprintf("결재자 변경: %s님 → %s님 (구분: %s) 사유: %s", fromName, nu.Name, causeLabel, reason), At: now}); err != nil {
			return err
		}
		notified, err := s.notifyReassign(ctx, tx, rec, step, fromID, fromName, nu, reason, now)
		if err != nil {
			return err
		}
		confirm := "아니오"
		if in.ConfirmActive {
			confirm = "예"
		}
		if err := s.record(ctx, tx, p, "approval.reassign", audit.TargetApproval, prID,
			audit.DiffItem{Key: "step", New: fmt.Sprintf("%d/%s", seq, step.Kind)},
			audit.DiffItem{Key: "from", New: fromName},
			audit.DiffItem{Key: "to", New: nu.Name},
			audit.DiffItem{Key: "cause", New: causeLabel},
			audit.DiffItem{Key: "reason", New: reason},
			audit.DiffItem{Key: "round", New: fmt.Sprint(rec.Round)},
			audit.DiffItem{Key: "notified", New: fmt.Sprint(notified)},
			audit.DiffItem{Key: "confirm_active", New: confirm}); err != nil {
			return err
		}
		out, err = s.Purchases.GetPR(ctx, tx, prID, repository.ScopeAll)
		return err
	})
	if err != nil {
		return domain.PurchaseRequest{}, err
	}
	pr := toPR(out)
	_ = s.decorate(ctx, s.DB, &pr)
	return pr, nil
}

// notifyReassign 은 요청자·새 결재자·(재직 중인) 원 결재자에게 앱 안 알림을 만든다(3.9). 만든 알림 수를 돌려준다.
func (s *ReassignService) notifyReassign(ctx context.Context, tx contract.Tx, rec repository.PRRecord, step domain.ApprovalStep, fromID int64, fromName string, nu repository.LineUserRec, reason string, now time.Time) (int, error) {
	ki, _ := kindOf(step.Kind)
	label := ki.Label
	if step.Role == domain.RoleAgree {
		label = "합의"
	}
	subject := fmt.Sprintf("「%s」(%s) 건의 %d단계(%s)", rec.Title, rec.No, step.Seq, label)
	n := 0
	send := func(userID int64, title, body, link string) error {
		n++
		return s.notify(ctx, tx, repository.NotificationInsert{UserID: userID, Kind: "approval_reassigned", Title: title, Body: body, Link: link,
			RefType: "purchase_request", RefID: rec.ID, Now: now})
	}
	prLink := fmt.Sprintf("/purchase-requests/%d", rec.ID)
	inboxLink := fmt.Sprintf("/approvals/%d", rec.ID)
	if rec.FormKind != "" && rec.FormKind != string(domain.FormKindPurchase) {
		prLink = fmt.Sprintf("/docs/%d", rec.ID) // 양식 문서는 문서 상세에서 처리한다
		inboxLink = prLink
	}
	if err := send(rec.RequesterID, "결재자가 바뀌었습니다",
		fmt.Sprintf("%s 결재자가 %s님에서 %s님으로 바뀌었습니다. 사유: %s", subject, fromName, nu.Name, reason), prLink); err != nil {
		return n, err
	}
	when := "앞 단계가 끝나면 결재 차례가 됩니다."
	if step.Status == domain.StepPending {
		when = "지금 결재 차례입니다."
	}
	if err := send(nu.ID, "결재자로 지정되었습니다", fmt.Sprintf("%s 결재자로 지정되었습니다. %s", subject, when), inboxLink); err != nil {
		return n, err
	}
	if fromID != rec.RequesterID && fromID != nu.ID {
		if ok, err := s.elig().usable(ctx, tx, fromID, contract.DayOf(now)); err != nil {
			return n, err
		} else if ok {
			if err := send(fromID, "결재자에서 제외되었습니다", fmt.Sprintf("「%s」(%s) 건의 %d단계 결재자에서 제외되었습니다.", rec.Title, rec.No, step.Seq), prLink); err != nil {
				return n, err
			}
		}
	}
	return n, nil
}

// ReassignCandidates 는 새 결재자 자격을 만족하는 사람이다(이름·조직명 부분 일치, 최대 50명, 연락처 없음).
func (s *ReassignService) ReassignCandidates(ctx context.Context, prID int64, seq int, text string) ([]domain.ReassignCandidate, error) {
	if _, err := contract.Authorize(ctx, contract.PermApprovalReassign); err != nil {
		return nil, err
	}
	rec, step, err := s.reassignTarget(ctx, s.DB, prID, seq)
	if err != nil {
		return nil, err
	}
	current, err := s.Approvals.StepAssignees(ctx, s.DB, prID, rec.Round, seq)
	if err != nil {
		return nil, err
	}
	lines := repository.SQLLines{}
	var users []repository.LineUserRec
	perm := contract.PermApprovalDecide
	if step.Role == domain.RoleAgree {
		perm = contract.PermApprovalAgree // 합의는 직원도 맡을 수 있다(합의 권한을 가진 사람 전체)
	}
	users, err = lines.ActiveByPerm(ctx, s.DB, perm)
	if err != nil {
		return nil, err
	}
	day := contract.DayOf(s.now())
	needle := strings.ToLower(strings.TrimSpace(text))
	out := []domain.ReassignCandidate{}
	for _, u := range users {
		problem, err := s.newApproverProblem(ctx, s.DB, rec, step, u, current)
		if err != nil {
			return nil, err
		}
		if problem != "" {
			continue
		}
		c := domain.ReassignCandidate{UserID: u.ID, Name: u.Name}
		if units, err := s.Org.UnitsHeadedBy(ctx, u.ID, day); err == nil {
			best := contract.OrgUnit{}
			for _, hu := range units {
				if hu.Kind.Level() > best.Kind.Level() {
					best = hu
				}
			}
			if best.ID != 0 {
				c.Title, c.UnitName = contract.HeadTitle(best.Kind), best.Name
				if path, err := s.Org.Path(ctx, best.ID, day); err == nil {
					c.UnitPath = contract.OrgPathText(path)
				}
			}
		}
		if c.Title == "" {
			if person, err := s.Org.Person(ctx, u.ID, day); err == nil {
				for _, pl := range person.Acting {
					c.Acting, c.Title, c.UnitName = true, contract.HeadTitle(pl.Unit.Kind), pl.Unit.Name
					c.UnitPath = contract.OrgPathText(pl.Path)
					break
				}
			}
		}
		if needle != "" && !strings.Contains(strings.ToLower(c.Name+" "+c.UnitName+" "+c.UnitPath), needle) {
			continue
		}
		out = append(out, c)
		if len(out) >= 50 {
			break
		}
	}
	return out, nil
}
