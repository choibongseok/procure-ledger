package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/repository"
)

// sqlLineDeps 는 lineDeps 의 SQLite 구현이다.
type sqlLineDeps struct {
	rules repository.SQLPurchases
	users repository.SQLLines
}

func (d sqlLineDeps) LineKinds(ctx context.Context, q contract.Tx, amount int64) ([]domain.StepKind, error) {
	return d.rules.LineKindsFor(ctx, q, amount)
}

func toLineUser(u repository.LineUserRec) lineUser {
	return lineUser{ID: u.ID, Name: u.Name, Role: u.Role, Active: u.Active, Perms: u.Perms}
}

func (d sqlLineDeps) LineUser(ctx context.Context, q contract.Tx, id int64) (lineUser, error) {
	u, err := d.users.User(ctx, q, id)
	return toLineUser(u), err
}

func (d sqlLineDeps) FinancePool(ctx context.Context, q contract.Tx) ([]lineUser, error) {
	rows, err := d.users.ActiveByPerm(ctx, q, contract.PermFinanceStage)
	if err != nil {
		return nil, err
	}
	out := make([]lineUser, len(rows))
	for i, u := range rows {
		out[i] = toLineUser(u)
	}
	return out, nil
}

// LineService 는 domain.LineService 의 구현이다. 결재선 미리보기와 결재자 소멸 목록은 읽기 전용이다.
type LineService struct {
	Base
	Approvals   repository.ApprovalRepository
	Delegations repository.DelegationRepository
	Purchases   repository.PurchaseRepository
}

var _ domain.LineService = (*LineService)(nil)

// NewLineService 는 SQLite 저장소로 결재선 서비스를 만든다.
func NewLineService(b Base) *LineService {
	return &LineService{Base: b, Approvals: repository.SQLApprovals{}, Delegations: repository.SQLDelegations{}, Purchases: repository.SQLPurchases{}}
}

func (s *LineService) elig() eligibility {
	return eligibility{approvals: s.Approvals, delegations: s.Delegations, purchases: s.Purchases, org: s.Org}
}

// buildLineAt 은 상신과 미리보기가 같이 쓰는 결재선 만들기다(기준일 asOf).
func (b Base) buildLineAt(ctx context.Context, q contract.Tx, requesterID, amount int64, asOf contract.Day, opt ...lineOpts) (domain.LinePreview, lineMeta, error) {
	return buildLine(ctx, q, b.Org, sqlLineDeps{}, requesterID, amount, asOf, opt...)
}

// asOfOrToday 는 기준일을 검증하고, 비면 오늘(KST)이다.
func (b Base) asOfOrToday(d contract.Day) (contract.Day, error) {
	if d == "" {
		return contract.DayOf(b.now()), nil
	}
	if _, err := time.Parse("2006-01-02", string(d)); err != nil {
		return "", validation("기준일은 연-월-일(예 2026-12-18) 형식으로 입력하세요")
	}
	return d, nil
}

// PreviewLine 은 요청자와 금액으로 결재선을 만들어 보여 준다. 본인 것은 항상, 타인은 요청자가 호출자의 열람 범위일 때만 가능하다.
func (s *LineService) PreviewLine(ctx context.Context, in domain.LinePreviewInput) (domain.LinePreview, error) {
	pv, err := s.previewLine(ctx, in)
	if err == nil {
		s.annotateAbsence(ctx, pv.Steps)
	}
	return pv, err
}

func (s *LineService) previewLine(ctx context.Context, in domain.LinePreviewInput) (domain.LinePreview, error) {
	p, err := contract.Authorize(ctx, contract.PermApprovalRead)
	if err != nil {
		return domain.LinePreview{}, err
	}
	asOf, err := s.asOfOrToday(in.AsOf)
	if err != nil {
		return domain.LinePreview{}, err
	}
	if in.FormCode != "" && in.FormCode != "PUR" {
		return s.previewDoc(ctx, p, in)
	}
	if in.Amount <= 0 {
		return domain.LinePreview{}, validation("금액은 1원 이상이어야 합니다.")
	}
	target := in.RequesterID
	if target == 0 {
		target = p.UserID
	}
	if target != p.UserID {
		ok, err := s.canSeeRequester(ctx, p, target)
		if err != nil {
			return domain.LinePreview{}, err
		}
		if !ok {
			return domain.LinePreview{}, fmt.Errorf("%w: 이 사람의 결재선은 볼 수 없습니다.", contract.ErrForbidden)
		}
	}
	// B25: 요청자 본인이거나 예산 조회 권한이 있으면 예산 초과 판정을 같이 한다(상신과 같은 judgeBudget).
	force := false
	if target == p.UserID || p.Can(contract.PermBudgetRead) {
		if chk, err := s.judgeFor(ctx, s.DB, target, in.Amount, 0, s.now()); err == nil && chk.State == domain.BudgetOver {
			force = true
		} else if err != nil {
			return domain.LinePreview{}, err
		}
	}
	pv, _, err := s.buildLineAt(ctx, s.DB, target, in.Amount, asOf, lineOpts{ForceFinance: force})
	return pv, err
}

// canSeeRequester 는 호출자가 요청자 target 의 결재선을 물을 수 있는지다: 전체 열람 권한, 같은 부서, 호출자가 조직장인 조직 범위.
func (s *LineService) canSeeRequester(ctx context.Context, p contract.Principal, target int64) (bool, error) {
	u, err := repository.SQLDelegations{}.User(ctx, s.DB, target)
	if err != nil {
		return false, err
	}
	if p.Can(contract.PermPurchaseReadAll) || (p.DepartmentID != 0 && p.DepartmentID == u.DepartmentID) {
		return true, nil
	}
	if s.Org == nil {
		return false, nil
	}
	day := contract.DayOf(s.now())
	person, err := s.Org.Person(ctx, target, day)
	if errors.Is(err, contract.ErrNotInOrg) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	scope, err := s.Org.HeadedScope(ctx, p.UserID, day)
	if err != nil {
		return false, err
	}
	for _, id := range scope {
		if id == person.Primary.Unit.ID {
			return true, nil
		}
	}
	return false, nil
}

// ListOrphanedSteps 는 결재 진행 중 건의 결재 전 단계 중 후보(현재 결재자와 그의 유효한 위임 수임자)가 한 명도 없는 단계다(B22 결재자 소멸 목록).
// 사유는 "퇴사"(재직 중이 아니거나 조직 기준 퇴사)와 "결재 권한 상실"이다. 재무 단계는 재무 담당자가 없을 때 "재무 담당자 없음"으로 나오고 재지정 대상이 아니다.
// 전보·조직장 교체는 배정자가 계속 결재할 수 있으므로 소멸이 아니다. 읽기 전용이다.
func (s *LineService) ListOrphanedSteps(ctx context.Context, asOf contract.Day) ([]domain.OrphanStep, error) {
	if _, err := contract.Authorize(ctx, contract.PermOrgManage); err != nil {
		return nil, err
	}
	day, err := s.asOfOrToday(asOf)
	if err != nil {
		return nil, err
	}
	e := s.elig()
	refs, err := s.Approvals.AllOpenSteps(ctx, s.DB)
	if err != nil {
		return nil, err
	}
	out := []domain.OrphanStep{}
	for _, ref := range refs {
		ids, err := e.approversOf(ctx, s.DB, ref, s.now(), 0, true)
		if err != nil {
			return nil, err
		}
		if len(ids) > 0 {
			continue
		}
		o := domain.OrphanStep{PRID: ref.PRID, RequesterID: ref.RequesterID, RequesterName: ref.RequesterName, PRTitle: ref.PRTitle, Round: ref.Round, Order: ref.Seq, Seq: ref.Seq,
			Title: stepKindLabel(ref.Kind), StepKind: ref.Kind, Reassignable: !isPool(ref.Kind), SinceDay: day, Role: ref.Role, FormKind: ref.FormKind, FormCode: ref.FormCode, FormName: ref.FormName, Stage: ref.Stage}
		if ref.Role == domain.RoleAgree {
			o.Title = "합의"
		}
		if isPool(ref.Kind) {
			o.Reason = "재무 담당자 없음"
			out = append(out, o)
			continue
		}
		assignees, err := s.Approvals.StepAssignees(ctx, s.DB, ref.PRID, ref.Round, ref.Seq)
		if err != nil {
			return nil, err
		}
		o.Reason = "결재 권한 상실"
		if ref.Role == domain.RoleAgree {
			o.Reason = "합의 권한 상실"
		}
		if len(assignees) > 0 {
			o.ApproverID = assignees[0]
			u, err := repository.SQLLines{}.User(ctx, s.DB, assignees[0])
			if err == nil {
				o.ApproverName = u.Name
				if !u.Active {
					o.Reason = "퇴사"
				}
			}
			if o.Reason != "퇴사" && s.Org != nil {
				if pe, err := s.Org.Person(ctx, assignees[0], day); err == nil {
					if !pe.Active {
						o.Reason = "퇴사"
					} else if pe.OnLeave {
						o.Reason = "휴직" // 휴직 중이라 결재 자격이 멈췄다(B50). 관리자가 장기 부재(absence) 원인으로 재지정한다
					}
				}
			}
		}
		out = append(out, o)
	}
	return out, nil
}

// previewDoc 는 양식 문서의 결재선 미리보기다(B22 who_approves·B23 작성 화면이 같은 입구를 쓴다). 호출자 본인 것만 물을 수 있고
// 권한 판정(draft:create)과 결재선 만들기는 양식 문서 서비스(ApprovalDocService.PreviewLine)가 한다. 구매 품의서 경로는 바꾸지 않는다.
func (s *LineService) previewDoc(ctx context.Context, p contract.Principal, in domain.LinePreviewInput) (domain.LinePreview, error) {
	if in.AsOf != "" {
		return domain.LinePreview{}, validation("양식 문서의 결재선은 오늘 기준으로만 볼 수 있습니다.")
	}
	if in.RequesterID != 0 && in.RequesterID != p.UserID {
		return domain.LinePreview{}, fmt.Errorf("%w: 양식 문서의 결재선은 본인 것만 볼 수 있습니다.", contract.ErrForbidden)
	}
	return NewApprovalDocService(s.Base).PreviewLine(ctx, domain.DocLineInput{FormCode: in.FormCode, Amount: in.Amount, FieldUnit: in.FieldUnit, Fields: in.Fields, Edits: in.Edits})
}
