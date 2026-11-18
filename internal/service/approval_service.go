package service

import (
	"context"
	"errors"
	"fmt"
	"github.com/choibongseok/procure-ledger/internal/dbutil"
	"sort"
	"strings"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/repository"
)

// ApprovalService 는 domain.ApprovalService 의 구현이다. 자기 결재 금지·결재 차례 검사·PO 생성이 여기 있다.
type ApprovalService struct {
	Base
	Purchases repository.PurchaseRepository
	Approvals repository.ApprovalRepository
	Inventory repository.InventoryRepository
	// Delegations 는 결재 위임 조회용이다(위임 건 결재함·위임 결재).
	Delegations repository.DelegationRepository
}

var _ domain.ApprovalService = (*ApprovalService)(nil)

// NewApprovalService 는 SQLite 저장소로 결재 서비스를 만든다.
func NewApprovalService(b Base) *ApprovalService {
	return &ApprovalService{Base: b, Purchases: repository.SQLPurchases{}, Approvals: repository.SQLApprovals{}, Inventory: repository.SQLInventory{}, Delegations: repository.SQLDelegations{}}
}

// elig 는 결재 후보·위임 판정(approval_eligibility.go)을 이 서비스의 저장소로 만든다.
func (s *ApprovalService) elig() eligibility {
	return eligibility{approvals: s.Approvals, delegations: s.Delegations, purchases: s.Purchases, org: s.Org}
}

// ListPending 은 지금 내 차례인 결재 건이다: 내가 현재 결재자인 단계(재무 역할은 재무 단계), 그리고 위임으로 맡은 단계. 본인 요청은 제외한다.
func (s *ApprovalService) ListPending(ctx context.Context) ([]domain.PurchaseRequest, error) {
	p, err := contract.Authorize(ctx, contract.PermApprovalRead)
	if err != nil {
		return nil, err
	}
	now := s.now()
	out := []domain.PurchaseRequest{}
	seen := map[int64]bool{}
	pendingRefs := func(refs []repository.StepRef) []repository.StepRef {
		var r []repository.StepRef
		for _, ref := range refs {
			if ref.Status == domain.StepPending {
				r = append(r, ref)
			}
		}
		return r
	}
	add := func(ref repository.StepRef, delegatorID int64, delegatorName string) error {
		if seen[ref.PRID] || ref.RequesterID == p.UserID {
			return nil
		}
		// 이 차수에서 이미 다른 단계를 결재한 사람은 이 단계를 결재할 수 없으므로 결재함에 올리지 않는다.
		decided, err := s.Delegations.DecidedIDs(ctx, s.DB, ref.PRID)
		if err != nil {
			return err
		}
		if hasID(decided, p.UserID) {
			return nil
		}
		rec, err := s.Purchases.GetPR(ctx, s.DB, ref.PRID, repository.ScopeAll)
		if err != nil {
			return err
		}
		pr := toPR(rec)
		pr.DelegatedFromID, pr.DelegatedFromName = delegatorID, delegatorName
		if err := s.decorate(ctx, s.DB, &pr); err != nil {
			return err
		}
		out = append(out, pr)
		seen[ref.PRID] = true
		return nil
	}
	// 결재함도 결재 판정과 같은 자격 함수(usable: 재직·결재 권한·조직 재직)를 거친다. 결재할 수 없는 사람의 결재함에는 건이 올라오지 않는다.
	e := s.elig()
	day := contract.DayOf(now)
	selfOK, err := e.usable(ctx, s.DB, p.UserID, day)
	if err != nil {
		return nil, err
	}
	own, err := s.Approvals.AssignedOpenSteps(ctx, s.DB, p.UserID)
	if err != nil {
		return nil, err
	}
	if p.Can(contract.PermFinanceStage) {
		fin, err := s.Approvals.FinanceOpenSteps(ctx, s.DB)
		if err != nil {
			return nil, err
		}
		own = append(own, fin...)
	}
	if selfOK {
		for _, ref := range pendingRefs(own) {
			if err := add(ref, 0, ""); err != nil {
				return nil, err
			}
		}
	}
	// 위임받은 건: 위임에 한해 타 부서 건이 열린다(그 밖의 열람 범위 밖 건은 그대로 숨겨진다).
	if p.Can(contract.PermApprovalDecide) && selfOK {
		dels, err := s.Delegations.ActiveTo(ctx, s.DB, p.UserID, now)
		if err != nil {
			return nil, err
		}
		for _, d := range dels {
			refs, err := s.Approvals.AssignedOpenSteps(ctx, s.DB, d.DelegatorID)
			if err != nil {
				return nil, err
			}
			if du, err := s.Delegations.User(ctx, s.DB, d.DelegatorID); err == nil && du.Can(contract.PermFinanceStage) {
				fin, err := s.Approvals.FinanceOpenSteps(ctx, s.DB)
				if err != nil {
					return nil, err
				}
				refs = append(refs, fin...)
			}
			for _, ref := range pendingRefs(refs) {
				if ref.RequesterID == d.DelegatorID || ref.Role == domain.RoleAgree {
					continue // 합의 단계는 위임할 수 없다(결재 판정과 같은 조건으로 목록에서도 뺀다)
				}
				// 위임자가 그 단계의 유효한 처리자일 때만(결재 판정 findActing 과 같은 delegatorMatches).
				if ok, err := e.delegatorMatches(ctx, s.DB, ref, d.DelegatorID, day); err != nil {
					return nil, err
				} else if !ok {
					continue
				}
				if err := add(ref, d.DelegatorID, d.DelegatorName); err != nil {
					return nil, err
				}
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	s.markPRNames(ctx, out)
	return out, nil
}

// Decide 는 내 차례인 결재 단계를 승인·반려한다.
// 마지막 단계 승인이면 같은 트랜잭션에서 발주(PO)를 만들고 구매요청을 발주완료로 바꾼다.
func (s *ApprovalService) Decide(ctx context.Context, prID int64, d domain.Decision) (domain.DecisionResult, error) {
	return s.decide(ctx, prID, d, contract.PermApprovalDecide)
}

// Agree 는 합의 단계를 동의·이견으로 처리한다(approval:agree). 결재 권한이 없는 직원도 합의자가 될 수 있어 따로 둔다.
// Decision.Approve 가 true 면 동의, false 면 이견(사유 필수)이다. 결재 단계는 이 메서드로 처리할 수 없다(후보 판정이 역할 권한을 본다).
func (s *ApprovalService) Agree(ctx context.Context, prID int64, d domain.Decision) (domain.DecisionResult, error) {
	return s.decide(ctx, prID, d, contract.PermApprovalAgree)
}

func (s *ApprovalService) decide(ctx context.Context, prID int64, d domain.Decision, gate contract.Permission) (domain.DecisionResult, error) {
	p, err := s.authorize(ctx, gate, "approval.decide", audit.TargetApproval)
	if err != nil {
		return domain.DecisionResult{}, err
	}
	// 조직 조회는 트랜잭션 밖에서 한다(열람 범위, 조직 기준 퇴사 상태).
	scope, err := s.viewScope(ctx, p)
	if err != nil {
		return domain.DecisionResult{}, err
	}
	if ctx, err = s.prefetchOrgFacts(ctx); err != nil {
		return domain.DecisionResult{}, err
	}
	// 결재 완료 때 수신 건과 알림을 만들려면 수신처 조직장이 필요하다. 조직 조회는 트랜잭션 밖에서 미리 한다.
	recvHeads := map[int64][]int64{}
	if pre, perr := s.Purchases.GetPR(ctx, s.DB, prID, repository.ScopeAll); perr == nil {
		for _, r := range pre.Recipients {
			if r.Kind == "receive" && r.TargetType == "unit" {
				recvHeads[r.TargetID] = s.unitHeadIDs(ctx, r.TargetID)
			}
		}
	}
	var res domain.DecisionResult
	err = s.write(ctx, p, "approval.decide", audit.TargetApproval, prID, func(tx contract.Tx) error {
		if err := dbutil.AcquireKeyLock(ctx, tx, fmt.Sprintf("pr:%d", prID)); err != nil { // 같은 문서에 대한 상태 전이(상신·결재·취소·회수)를 PG 에서 줄 세운다
			return err
		}
		rec, err := s.Purchases.GetPR(ctx, tx, prID, scope)
		if errors.Is(err, contract.ErrNotFound) && !scope.All {
			// 위임으로 결재할 수 있는 열람 범위 밖 건만 연다. 그 밖의 건은 계속 존재를 숨긴다.
			if ok, verr := s.elig().canView(ctx, tx, prID, p.UserID, s.now()); verr == nil && ok {
				rec, err = s.Purchases.GetPR(ctx, tx, prID, repository.ScopeAll)
			}
		}
		if err != nil {
			return err
		}
		if rec.RequesterID == p.UserID {
			return contract.ErrSelfApproval
		}
		if p.Channel == contract.ChannelMCP && rec.FormKind != "" && rec.FormKind != string(domain.FormKindPurchase) {
			return contract.ErrNotFound // MCP 는 구매 품의서만 다룬다(양식 문서는 웹에서만 처리)
		}
		pending, err := s.Approvals.PendingSteps(ctx, tx, prID, rec.Round)
		if err != nil || len(pending) == 0 {
			return fmt.Errorf("%w: 결재할 수 있는 상태가 아닙니다(%s)", contract.ErrInvalidState, StatusLabel(rec.Status))
		}
		e := s.elig()
		day := contract.DayOf(s.now())
		refOf := func(st domain.ApprovalStep) repository.StepRef {
			role := st.Role
			if role == "" {
				role = domain.RoleApprove
			}
			return repository.StepRef{PRID: prID, Round: rec.Round, Seq: st.Seq, Kind: st.Kind, Status: st.Status, Role: role, Stage: st.Stage, RequesterID: rec.RequesterID}
		}
		// 지금 처리할 차례인 단계(병렬 합의면 여럿) 중 내가 현재 처리자인 것. 없으면 결재 단계에 한해 유효한 위임으로 대신 결재한다.
		var step domain.ApprovalStep
		var ref repository.StepRef
		var onBehalfOf int64
		found := false
		for _, st := range pending {
			r := refOf(st)
			direct, err := e.direct(ctx, tx, r, day)
			if err != nil {
				return err
			}
			if hasID(direct, p.UserID) {
				step, ref, found = st, r, true
				break
			}
		}
		if !found {
			for _, st := range pending {
				r := refOf(st)
				if r.Role != domain.RoleApprove {
					continue // 합의는 위임할 수 없다
				}
				dg, ok, err := e.findActing(ctx, tx, p.UserID, r, s.now())
				if err != nil {
					return err
				}
				if ok {
					step, ref, found, onBehalfOf = st, r, true, dg.DelegatorID
					break
				}
			}
		}
		if !found {
			return fmt.Errorf("%w: 지금은 결재 차례가 아닙니다", contract.ErrForbidden)
		}
		isAgree := ref.Role == domain.RoleAgree
		// 한 사람이 같은 상신 차수에서 둘 이상의 단계(결재·합의)를 처리할 수 없다(대신 결재 포함). 재상신된 새 차수에서는 다시 할 수 있다.
		decided, err := s.Delegations.DecidedIDs(ctx, tx, prID)
		if err != nil {
			return err
		}
		if hasID(decided, p.UserID) {
			return fmt.Errorf("%w: 같은 구매요청의 다른 결재 단계를 이미 결재하셨습니다. 한 사람이 두 단계를 결재할 수 없습니다", contract.ErrForbidden)
		}
		open, err := s.Approvals.OpenSteps(ctx, tx, prID, rec.Round)
		if err != nil {
			return err
		}
		var later []repository.StepRef
		sameStageOthers := false
		for _, st := range open {
			switch {
			case st.Stage > step.Stage:
				later = append(later, st)
			case st.Stage == step.Stage && st.Seq != step.Seq && st.Status == domain.StepPending:
				sameStageOthers = true
			}
		}
		if d.Approve {
			// 이 승인으로 뒤 단계를 맡을 사람이 없어지면(예: 위임 수임자가 뒤 단계의 유일한 결재자) 결재가 멈추므로 승인을 받지 않는다.
			if ok, err := e.remainingAssignable(ctx, tx, later, s.now(), p.UserID, 0); err != nil {
				return err
			} else if !ok {
				return fmt.Errorf("%w: 이 단계를 승인하면 뒤 결재 단계를 맡을 다른 결재자가 없어 결재가 멈춥니다. 다른 결재자가 이 단계를 결재하거나, 관리자에게 결재자 지정을 요청하세요", contract.ErrForbidden)
			}
		}
		comment := strings.TrimSpace(d.Comment)
		if !d.Approve && comment == "" {
			if isAgree {
				return validation("합의 이견은 사유를 입력해야 합니다")
			}
			return validation("반려 사유를 입력해야 합니다")
		}
		isDoc := rec.FormKind != "" && rec.FormKind != string(domain.FormKindPurchase)
		var formDef domain.FormDefinition
		if isDoc && rec.FormVersionID != 0 {
			v, verr := repository.SQLForms{}.VersionByID(ctx, tx, rec.FormVersionID)
			if verr != nil {
				return verr
			}
			formDef = v.Definition
		}

		stepStatus, action, auditAction := domain.StepRejected, domain.ActionReject, "approval.reject"
		switch {
		case isAgree && d.Approve:
			stepStatus, action, auditAction = domain.StepAgreed, domain.ActionAgree, "approval.agree"
		case isAgree:
			stepStatus, action, auditAction = domain.StepObjected, domain.ActionObject, "approval.object"
		case d.Approve:
			stepStatus, action, auditAction = domain.StepApproved, domain.ActionApprove, "approval.approve"
		}
		// 문서를 반려하는 경우: 결재 반려, 또는 이견을 반려로 처리하는 양식(objection_action=reject)의 합의 이견.
		docRejected := !d.Approve && (!isAgree || formDef.Settings.ObjectionAction == "reject")
		ev := evApprovePartial
		switch {
		case docRejected:
			ev = evReject
		case !sameStageOthers && len(later) == 0:
			ev = evApproveFinal
		}
		to, err := transition(rec.Status, ev)
		if err != nil {
			return err
		}
		now := s.now()
		if err := s.Approvals.DecideStep(ctx, tx, prID, rec.Round, step.Seq, stepStatus, p.UserID, onBehalfOf, comment, now); err != nil {
			return err
		}
		if err := s.Approvals.InsertHistory(ctx, tx, repository.HistoryInsert{
			PRID: prID, Round: rec.Round, Seq: step.Seq, ActorID: p.UserID, Action: action, Comment: comment, At: now, OnBehalfOf: onBehalfOf,
		}); err != nil {
			return err
		}
		rejectReason := ""
		switch ev {
		case evReject:
			rejectReason = comment
			if isDoc {
				// 양식 문서는 처리하지 않은 단계(병렬 합의의 다른 합의자 포함)를 건너뜀으로 닫는다. 구매 품의서의 기존 표시는 그대로 둔다.
				if err := s.Approvals.SkipOpen(ctx, tx, prID, rec.Round); err != nil {
					return err
				}
				if err := runRejectedHook(ctx, s.Base, tx, p, rec, comment); err != nil {
					return err
				}
			}
		case evApprovePartial:
			if !sameStageOthers {
				if _, err := s.Approvals.ActivateNextStage(ctx, tx, prID, rec.Round); err != nil {
					return err
				}
			}
		}
		if err := s.Purchases.SetPRState(ctx, tx, prID, to, rec.Round, rejectReason, now); err != nil {
			return err
		}
		diff := []audit.DiffItem{
			{Key: "status", Old: string(rec.Status), New: string(to)},
			{Key: "step", New: fmt.Sprintf("%d/%s", step.Seq, step.Kind)},
			{Key: "comment", New: comment}}
		if onBehalfOf != 0 {
			diff = append(diff, audit.DiffItem{Key: "on_behalf_of", New: fmt.Sprint(onBehalfOf)})
		}
		if err := s.record(ctx, tx, p, auditAction, audit.TargetApproval, prID, diff...); err != nil {
			return err
		}

		// B23 단계 2 알림: 반려·이견·다음 차례·완료.
		switch {
		case ev == evReject:
			actors, aerr := s.stepActors(ctx, tx, prID, rec.Round)
			if aerr != nil {
				return aerr
			}
			if _, err := s.notifyUsers(ctx, tx, append(actors, rec.RequesterID), p.UserID, noteApprovalRejected, "문서가 반려되었습니다",
				fmt.Sprintf("「%s」(%s) 문서가 반려되었습니다. 사유: %s", rec.Title, rec.No, comment), docLink(rec.FormKind, prID), prID); err != nil {
				return err
			}
		case isAgree && !d.Approve:
			if _, err := s.notifyUsers(ctx, tx, []int64{rec.RequesterID}, p.UserID, noteAgreementObjected, "합의 이견이 남았습니다",
				fmt.Sprintf("「%s」(%s) 문서에 합의 이견이 남았습니다. 사유: %s", rec.Title, rec.No, comment), docLink(rec.FormKind, prID), prID); err != nil {
				return err
			}
		case ev == evApprovePartial && !sameStageOthers:
			if _, err := s.notifyPending(ctx, tx, rec, p.UserID); err != nil {
				return err
			}
		}
		if ev == evApproveFinal {
			ccs, cerr := s.ccUsers(ctx, tx, prID, rec.Round)
			if cerr != nil {
				return cerr
			}
			if _, err := s.notifyUsers(ctx, tx, append([]int64{rec.RequesterID}, ccs...), p.UserID, noteApprovalDone, "결재가 완료되었습니다",
				fmt.Sprintf("「%s」(%s) 문서의 결재가 완료되었습니다.", rec.Title, rec.No), docLink(rec.FormKind, prID), prID); err != nil {
				return err
			}
			// 수신처마다 수신 건을 만들고 접수 담당자(조직장·수신 담당자)에게 알린다.
			for _, r := range rec.Recipients {
				if r.Kind != "receive" || r.TargetType != "unit" {
					continue
				}
				if _, err := (repository.SQLReceipts{}).Create(ctx, tx, prID, rec.Round, r.TargetID, r.Name, now); err != nil {
					return err
				}
				handlers, herr := s.receiptHandlers(ctx, tx, r.TargetID, recvHeads[r.TargetID])
				if herr != nil {
					return herr
				}
				if _, err := s.notifyUsers(ctx, tx, handlers, 0, noteReceiptArrived, "수신 문서가 도착했습니다",
					fmt.Sprintf("「%s」(%s) 문서가 %s에 도착했습니다. 접수해 주세요.", rec.Title, rec.No, r.Name), "/inbox/receive", prID); err != nil {
					return err
				}
			}
		}
		if ev == evApproveFinal {
			// 사내 지식 검색(B47): 완료된 문서를 색인 대기열에 올린다(같은 트랜잭션이라 승인이 롤백되면 줄도 없다). 추출·색인은 워커가 한다.
			k := repository.SQLKnowledge{}
			if err := k.Enqueue(ctx, tx, "upsert", repository.KSrcApproval, prID, 0, "completed", now); err != nil {
				return err
			}
			atts, err := k.AttachmentIDsOfDoc(ctx, tx, prID)
			if err != nil {
				return err
			}
			for _, aid := range atts {
				if err := k.Enqueue(ctx, tx, "upsert", repository.KSrcAttachment, aid, 0, "completed", now); err != nil {
					return err
				}
			}
		}
		if ev == evApproveFinal && isDoc {
			// 양식 문서는 결재 완료에서 멈춘다(발주 없음). 완료 시각과 보존 기한을 남긴다.
			if err := s.Purchases.SetCompleted(ctx, tx, prID, now, retentionDate(now, formDef.RetentionYears), isLegalHold(rec.FormCode, formDef)); err != nil {
				return err
			}
			// 시스템 양식(재고 조정 전표)은 승인 직후 같은 트랜잭션에서 후속 동작을 한다. 실패하면 승인 전체가 취소된다.
			if err := runApprovedHook(ctx, s.Base, tx, p, rec); err != nil {
				return err
			}
		}
		if ev == evApproveFinal && !isDoc {
			ordered, err := transition(to, evIssuePO)
			if err != nil {
				return err
			}
			poID, err := s.Inventory.CreatePurchaseOrder(ctx, tx, repository.POInsert{
				PRID: prID, VendorID: rec.VendorID, Total: rec.TotalAmount, Lines: rec.Lines, Now: now,
				DueDate: rec.DueDate, DeliveryPlace: rec.DeliveryPlace,
			})
			if err != nil {
				return err
			}
			if err := s.Purchases.SetPurchaseOrder(ctx, tx, prID, poID); err != nil {
				return err
			}
			if err := s.Purchases.SetPRState(ctx, tx, prID, ordered, rec.Round, "", now); err != nil {
				return err
			}
			if err := s.record(ctx, tx, p, "purchase_order.create", audit.TargetPurchaseOrder, poID,
				audit.DiffItem{Key: "pr_id", New: fmt.Sprint(prID)},
				audit.DiffItem{Key: "total_amount", New: fmt.Sprint(rec.TotalAmount)}); err != nil {
				return err
			}
			if err := s.record(ctx, tx, p, "purchase_request.order", audit.TargetPurchaseRequest, prID,
				audit.DiffItem{Key: "status", Old: string(to), New: string(ordered)}); err != nil {
				return err
			}
			po, err := s.Inventory.GetPO(ctx, tx, poID, repository.ScopeAll)
			if err != nil {
				return err
			}
			res.PurchaseOrder = &po
		}
		out, err := s.Purchases.GetPR(ctx, tx, prID, repository.ScopeAll)
		res.Request = toPR(out)
		return err
	})
	if err != nil {
		return domain.DecisionResult{}, err
	}
	return res, nil
}

// History 는 결재 이력이다. 읽기 전용이며 수정·삭제 경로는 없다.
func (s *ApprovalService) History(ctx context.Context, prID int64) ([]domain.ApprovalRecord, error) {
	p, err := contract.Authorize(ctx, contract.PermApprovalRead)
	if err != nil {
		return nil, err
	}
	scope, err := s.viewScope(ctx, p)
	if err != nil {
		return nil, err
	}
	if _, err := s.Purchases.GetPR(ctx, s.DB, prID, scope); err != nil {
		ok, verr := false, error(nil)
		if errors.Is(err, contract.ErrNotFound) && !scope.All {
			ok, verr = s.elig().canView(ctx, s.DB, prID, p.UserID, s.now())
		}
		if verr != nil || !ok {
			return nil, err
		}
	}
	hist, err := s.Approvals.ListHistory(ctx, s.DB, prID)
	if err != nil {
		return nil, err
	}
	s.markHistoryNames(ctx, hist)
	return hist, nil
}

// retentionDate 는 결재 완료일(KST)부터 years 년 뒤 날짜다. 0(영구)이면 빈 문자열.
func retentionDate(at time.Time, years int) string {
	if years <= 0 {
		return ""
	}
	return at.In(kst).AddDate(years, 0, 0).Format("2006-01-02")
}
