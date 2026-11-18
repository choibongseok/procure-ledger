package service

import (
	"context"
	"errors"
	"fmt"
	"github.com/choibongseok/procure-ledger/internal/dbutil"
	"strings"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/repository"
)

const (
	maxPRLines    = 100
	maxLineQty    = 1_000_000
	maxUnitPrice  = 10_000_000_000
	maxTitleRunes = 200
)

// PurchaseService 는 domain.PurchaseService 의 구현이다.
type PurchaseService struct {
	Base
	Repo      repository.PurchaseRepository
	Approvals repository.ApprovalRepository
	// Inventory 는 재주문 초안의 안전재고 미달 판정용이다.
	Inventory repository.InventoryRepository
	// Delegations 는 결재자 부재 시 위임 수임자가 있는지 확인하는 데 쓴다.
	Delegations repository.DelegationRepository
}

var _ domain.PurchaseService = (*PurchaseService)(nil)

// NewPurchaseService 는 SQLite 저장소로 구매요청 서비스를 만든다.
func NewPurchaseService(b Base) *PurchaseService {
	return &PurchaseService{Base: b, Repo: repository.SQLPurchases{}, Approvals: repository.SQLApprovals{}, Inventory: repository.SQLInventory{}, Delegations: repository.SQLDelegations{}}
}

func toPR(r repository.PRRecord) domain.PurchaseRequest { return r.PurchaseRequest }

// buildLines 는 입력을 검사하고 줄 금액(수량×단가)과 합계를 계산한다. 품목·거래처 존재는 tx 안에서 확인한다.
func (s *PurchaseService) buildLines(ctx context.Context, tx contract.Tx, in domain.DraftInput) ([]domain.PRLine, int64, error) {
	title := strings.TrimSpace(in.Title)
	if title == "" || len([]rune(title)) > maxTitleRunes {
		return nil, 0, validation("제목은 1자 이상 %d자 이하여야 합니다", maxTitleRunes)
	}
	if len(in.Lines) == 0 || len(in.Lines) > maxPRLines {
		return nil, 0, validation("품목은 1건 이상 %d건 이하여야 합니다", maxPRLines)
	}
	if in.VendorID != 0 {
		ok, err := s.Repo.VendorExists(ctx, tx, in.VendorID)
		if err != nil {
			return nil, 0, err
		}
		if !ok {
			return nil, 0, validation("거래처를 찾을 수 없습니다")
		}
	}
	ids := make([]int64, 0, len(in.Lines))
	for _, l := range in.Lines {
		ids = append(ids, l.ItemID)
	}
	items, err := s.Repo.ActiveItems(ctx, tx, ids)
	if err != nil {
		return nil, 0, err
	}
	var total int64
	lines := make([]domain.PRLine, 0, len(in.Lines))
	for i, l := range in.Lines {
		it, ok := items[l.ItemID]
		if !ok {
			return nil, 0, validation("%d번째 줄: 품목을 찾을 수 없습니다", i+1)
		}
		if l.Quantity <= 0 || l.Quantity > maxLineQty {
			return nil, 0, validation("%d번째 줄: 수량은 1 이상 %d 이하여야 합니다", i+1, maxLineQty)
		}
		if l.UnitPrice < 0 || l.UnitPrice > maxUnitPrice {
			return nil, 0, validation("%d번째 줄: 단가가 올바르지 않습니다", i+1)
		}
		amt := l.Quantity * l.UnitPrice
		total += amt
		lines = append(lines, domain.PRLine{ItemID: it.ID, ItemName: it.Name, Quantity: l.Quantity, UnitPrice: l.UnitPrice, Amount: amt})
	}
	return lines, total, nil
}

// CreateDraft 는 구매요청을 임시저장한다.
func (s *PurchaseService) CreateDraft(ctx context.Context, in domain.DraftInput) (domain.PurchaseRequest, error) {
	return s.createDraft(ctx, in, nil)
}

// createDraft 는 웹과 재주문 초안이 함께 쓰는 유일한 임시저장 경로다. prepare 는 같은 트랜잭션에서 입력을 보정·검사한다(없을 수 있다).
func (s *PurchaseService) createDraft(ctx context.Context, in domain.DraftInput, prepare func(tx contract.Tx, p contract.Principal, in *domain.DraftInput) error, extra ...audit.DiffItem) (domain.PurchaseRequest, error) {
	p, err := s.authorize(ctx, contract.PermPurchaseCreate, "purchase_request.create", audit.TargetPurchaseRequest)
	if err != nil {
		return domain.PurchaseRequest{}, err
	}
	// 조직 조회는 트랜잭션 밖에서 한다(DB 연결이 하나라 트랜잭션 안에서 조직 서비스를 부르면 멈춘다).
	ownerUnit, err := s.ownerUnitOf(ctx, p.UserID)
	if err != nil {
		return domain.PurchaseRequest{}, err
	}
	var out repository.PRRecord
	err = s.write(ctx, p, "purchase_request.create", audit.TargetPurchaseRequest, 0, func(tx contract.Tx) error {
		if prepare != nil {
			if err := prepare(tx, p, &in); err != nil {
				return err
			}
		}
		lines, total, err := s.buildLines(ctx, tx, in)
		if err != nil {
			return err
		}
		due, place, err := normalizeDelivery(in.DueDate, in.DeliveryPlace, s.now())
		if err != nil {
			return err
		}
		id, err := s.Repo.InsertPR(ctx, tx, repository.PRInsert{
			RequesterID: p.UserID, DepartmentID: p.DepartmentID, OwnerUnitID: ownerUnit, VendorID: in.VendorID,
			Title: strings.TrimSpace(in.Title), Reason: in.Reason, Total: total, Lines: lines, Now: s.now(),
		})
		if err != nil {
			return err
		}
		if err := (repository.SQLBudget{}).SetDelivery(ctx, tx, id, due, place); err != nil {
			return err
		}
		if err := s.Repo.SetDraftEdits(ctx, tx, id, normalizeEdits(in.Edits)); err != nil {
			return err
		}
		diff := append([]audit.DiffItem{
			{Key: "status", New: string(domain.PRDraft)},
			{Key: "title", New: strings.TrimSpace(in.Title)},
			{Key: "total_amount", New: fmt.Sprint(total)}}, extra...)
		if err := s.record(ctx, tx, p, "purchase_request.create", audit.TargetPurchaseRequest, id, diff...); err != nil {
			return err
		}
		out, err = s.Repo.GetPR(ctx, tx, id, repository.ScopeAll)
		return err
	})
	return toPR(out), err
}

// DraftReorder 는 안전재고 미달 품목으로 구매요청 임시저장 한 건을 만든다(상신하지 않는다). 웹과 같은 createDraft 경로를 탄다.
// 수량은 안전재고-현재고(최소 1), 단가는 그 품목의 가장 최근 발주 단가(없으면 0)다.
// 안전재고 미달이 아닌 품목, 같은 부서에 이미 그 품목의 임시저장 초안이 있는 경우는 거부한다(중복 초안 방지).
func (s *PurchaseService) DraftReorder(ctx context.Context, in domain.ReorderInput) (domain.PurchaseRequest, error) {
	p, err := contract.Authorize(ctx, contract.PermPurchaseCreate)
	if err == nil && !p.Can(contract.PermInventoryRead) {
		err = contract.ErrForbidden
	}
	if err != nil {
		if errors.Is(err, contract.ErrForbidden) {
			s.recordDenied(ctx, p, "purchase_request.create", audit.TargetPurchaseRequest, 0, string(contract.PermInventoryRead))
		}
		return domain.PurchaseRequest{}, err
	}
	reason := strings.TrimSpace(in.Reason)
	if reason == "" {
		reason = "안전재고 미달 재주문"
	}
	scope, err := s.viewScope(ctx, p)
	if err != nil {
		return domain.PurchaseRequest{}, err
	}
	return s.createDraft(ctx, domain.DraftInput{VendorID: in.VendorID, Title: "안전재고 재주문", Reason: reason},
		func(tx contract.Tx, p contract.Principal, d *domain.DraftInput) error {
			if len(in.ItemIDs) == 0 || len(in.ItemIDs) > maxPRLines {
				return validation("재주문할 품목은 1건 이상 %d건 이하여야 합니다", maxPRLines)
			}
			below, err := s.Inventory.BelowSafety(ctx, tx)
			if err != nil {
				return err
			}
			short := map[int64]domain.ItemStock{}
			for _, st := range below {
				short[st.Item.ID] = st
			}
			ids, seen := []int64{}, map[int64]bool{}
			for _, id := range in.ItemIDs {
				if seen[id] {
					continue
				}
				seen[id] = true
				if _, ok := short[id]; !ok {
					return validation("안전재고에 미달한 품목만 재주문 초안으로 만들 수 있습니다(품목 번호 %d)", id)
				}
				ids = append(ids, id)
			}
			drafts, err := s.Repo.DraftNosByItem(ctx, tx, ids, scope)
			if err != nil {
				return err
			}
			for _, id := range ids {
				if no, ok := drafts[id]; ok {
					return fmt.Errorf("%w: %s 품목은 이미 임시저장 중인 구매요청(%s)이 있어 새 초안을 만들지 않습니다", contract.ErrInvalidState, short[id].Item.Name, no)
				}
			}
			prices, err := s.Repo.LatestUnitPrices(ctx, tx, ids)
			if err != nil {
				return err
			}
			for _, id := range ids {
				st := short[id]
				d.Lines = append(d.Lines, domain.PRLineInput{ItemID: id, Quantity: max(st.Item.SafetyStock-st.OnHand, 1), UnitPrice: prices[id]})
			}
			return nil
		}, audit.DiffItem{Key: "source", New: "reorder"})
}

// ownDraft 는 수정·상신 대상을 읽고 요청자 본인인지 확인한다. 타 부서는 존재를 숨긴다(ErrNotFound).
func (s *PurchaseService) ownRequest(ctx context.Context, tx contract.Tx, p contract.Principal, scope repository.ViewScope, id int64) (repository.PRRecord, error) {
	return s.ownRequestKind(ctx, tx, p, scope, id, true)
}

// ownRequestKind 는 ownRequest 에 문서 종류(구매 품의서 여부)를 지정한다. 종류가 다르면 읽기와 같은 404 다.
func (s *PurchaseService) ownRequestKind(ctx context.Context, tx contract.Tx, p contract.Principal, scope repository.ViewScope, id int64, wantPurchase bool) (repository.PRRecord, error) {
	rec, err := s.Repo.GetPR(ctx, tx, id, scope)
	if err != nil {
		return rec, err
	}
	// 구매 쓰기 경로(수정·상신·재상신·회수)는 구매 품의서만, 양식 문서의 회수는 양식 문서만 다룬다. 다른 종류는 존재를 드러내지 않는다.
	if isPurchaseKind(rec.FormKind) != wantPurchase {
		return rec, contract.ErrNotFound
	}
	if rec.RequesterID != p.UserID {
		return rec, fmt.Errorf("%w: 요청자 본인만 수정·상신할 수 있습니다", contract.ErrForbidden)
	}
	return rec, nil
}

// UpdateDraft 는 임시저장·반려 상태의 구매요청 내용을 고친다.
func (s *PurchaseService) UpdateDraft(ctx context.Context, id int64, in domain.DraftInput) (domain.PurchaseRequest, error) {
	p, err := s.authorize(ctx, contract.PermPurchaseCreate, "purchase_request.update", audit.TargetPurchaseRequest)
	if err != nil {
		return domain.PurchaseRequest{}, err
	}
	scope, err := s.viewScope(ctx, p)
	if err != nil {
		return domain.PurchaseRequest{}, err
	}
	ownerUnit, err := s.ownerUnitOf(ctx, p.UserID)
	if err != nil {
		return domain.PurchaseRequest{}, err
	}
	var out repository.PRRecord
	err = s.write(ctx, p, "purchase_request.update", audit.TargetPurchaseRequest, id, func(tx contract.Tx) error {
		rec, err := s.ownRequest(ctx, tx, p, scope, id)
		if err != nil {
			return err
		}
		if !canEdit(rec.Status) {
			return fmt.Errorf("%w: %s 상태의 요청은 수정할 수 없습니다", contract.ErrInvalidState, StatusLabel(rec.Status))
		}
		lines, total, err := s.buildLines(ctx, tx, in)
		if err != nil {
			return err
		}
		title := strings.TrimSpace(in.Title)
		due, place, err := normalizeDelivery(in.DueDate, in.DeliveryPlace, s.now())
		if err != nil {
			return err
		}
		if err := s.Repo.UpdatePR(ctx, tx, id, title, in.Reason, in.VendorID, total, lines, s.now()); err != nil {
			return err
		}
		if err := (repository.SQLBudget{}).SetDelivery(ctx, tx, id, due, place); err != nil {
			return err
		}
		if err := s.syncQuoteSelection(ctx, tx, rec, in.VendorID); err != nil {
			return err
		}
		if err := s.refreshOwner(ctx, tx, p.UserID, id, ownerUnit); err != nil {
			return err
		}
		if err := s.Repo.SetDraftEdits(ctx, tx, id, normalizeEdits(in.Edits)); err != nil {
			return err
		}
		if err := s.record(ctx, tx, p, "purchase_request.update", audit.TargetPurchaseRequest, id,
			audit.DiffItem{Key: "title", Old: rec.Title, New: title},
			audit.DiffItem{Key: "total_amount", Old: fmt.Sprint(rec.TotalAmount), New: fmt.Sprint(total)}); err != nil {
			return err
		}
		out, err = s.Repo.GetPR(ctx, tx, id, repository.ScopeAll)
		return err
	})
	return toPR(out), err
}

// Submit 은 구매요청을 상신(반려건은 재상신)한다. 금액 구간의 결재선이 이 차수에 만들어진다.
func (s *PurchaseService) Submit(ctx context.Context, id int64) (domain.PurchaseRequest, error) {
	p, err := s.authorize(ctx, contract.PermPurchaseCreate, "purchase_request.submit", audit.TargetPurchaseRequest)
	if err != nil {
		return domain.PurchaseRequest{}, err
	}
	scope, err := s.viewScope(ctx, p)
	if err != nil {
		return domain.PurchaseRequest{}, err
	}
	// 결재선 계산 뒤 조직·사용자 상태가 바뀌면(퇴사 발령, 권한 변경 등) 결재선을 다시 만들어 두 번까지 재시도한다.
	var out repository.PRRecord
	for attempt := 0; ; attempt++ {
		out, err = s.submitOnce(ctx, p, scope, id)
		if (errors.Is(err, errOrgChanged) || errors.Is(err, errBudgetChanged)) && attempt < 2 {
			continue
		}
		return toPR(out), err
	}
}

// errOrgChanged 는 판정(결재선 계산·결재 후보)을 한 뒤 트랜잭션 안에서 조직·사용자 상태가 달라졌음을 뜻한다. 상신은 결재선을 다시 만들고, prefetchOrgFacts 를 쓰는 쓰기는 write 가 사실을 다시 읽어 두 번까지 재시도한다.
var errOrgChanged = fmt.Errorf("%w: 처리하는 동안 조직 정보가 바뀌었습니다. 다시 시도해 주세요", contract.ErrInvalidState)

// errBudgetChanged 는 상신 전에 한 예산 판정이 트랜잭션 안에서 달라졌음을 뜻한다(그 사이 다른 상신·예산 수정). 결재선을 다시 만들어 재시도한다.
var errBudgetChanged = fmt.Errorf("%w: 처리하는 동안 예산 현황이 바뀌었습니다. 다시 시도해 주세요", contract.ErrInvalidState)

func (s *PurchaseService) submitOnce(ctx context.Context, p contract.Principal, scope repository.ViewScope, id int64) (repository.PRRecord, error) {
	// 결재선은 트랜잭션 밖에서 만든다(조직 조회가 자기 DB 핸들로 읽는다). 안에서는 금액이 그대로인지만 확인하고 스냅샷을 저장한다.
	asOf := contract.DayOf(s.now())
	var (
		preLine   domain.LinePreview
		preMeta   lineMeta
		preErr    error
		preAmount int64
		havePre   bool
		preVer    string
		preEdits  string
		preRecs   []domain.DocRecipient
		preProbs  []string
		preBudget domain.BudgetCheck
		preDept   int64
	)
	preVer, verr := repository.SQLLines{}.OrgVersion(ctx, s.DB)
	if verr != nil {
		return repository.PRRecord{}, verr
	}
	if pre, perr := s.Repo.GetPR(ctx, s.DB, id, scope); perr == nil && pre.RequesterID == p.UserID && isPurchaseKind(pre.FormKind) {
		preAmount = pre.TotalAmount
		// B25: 예산 초과 판정은 결재선 계산 앞에서 한다. 초과면 재무 단계를 강제로 더한다(미리보기와 같은 buildLineAt 입력).
		bu, berr := repository.SQLDelegations{}.User(ctx, s.DB, pre.RequesterID)
		if berr != nil {
			return repository.PRRecord{}, berr
		}
		preDept = bu.DepartmentID
		if preBudget, berr = s.judgeBudget(ctx, s.DB, preDept, preAmount, id, budgetMonthOf(s.now())); berr != nil {
			return repository.PRRecord{}, berr
		}
		preLine, preMeta, preErr = s.buildLineAt(ctx, s.DB, pre.RequesterID, preAmount, asOf, lineOpts{ForceFinance: preBudget.State == domain.BudgetOver})
		havePre = true
		if preErr == nil {
			preEdits, _ = s.Repo.DraftEdits(ctx, s.DB, id)
			preRecs, preProbs = s.purchaseRecipients(ctx, preLine, pre.RequesterID, parseEdits(preEdits), asOf)
		}
	}
	var out repository.PRRecord
	err := s.write(ctx, p, "purchase_request.submit", audit.TargetPurchaseRequest, id, func(tx contract.Tx) error {
		if err := dbutil.AcquireKeyLock(ctx, tx, fmt.Sprintf("pr:%d", id)); err != nil { // 같은 문서에 대한 상태 전이(상신·결재·취소·회수)를 PG 에서 줄 세운다
			return err
		}
		rec, err := s.ownRequest(ctx, tx, p, scope, id)
		if err != nil {
			return err
		}
		if ver, err := (repository.SQLLines{}).OrgVersion(ctx, tx); err != nil {
			return err
		} else if ver != preVer {
			return errOrgChanged
		}
		ev, action, auditAction := evSubmit, domain.ActionSubmit, "purchase_request.submit"
		if rec.Status == domain.PRRejected {
			ev, action, auditAction = evResubmit, domain.ActionResubmit, "purchase_request.resubmit"
		}
		to, err := transition(rec.Status, ev)
		if err != nil {
			return err
		}
		if rec.VendorID == 0 {
			return validation("상신하려면 거래처를 선택해야 합니다")
		}
		if len(rec.Lines) == 0 {
			return validation("품목이 없는 요청은 상신할 수 없습니다")
		}
		// B25 ②: 최저가가 아닌 견적을 골랐는데 사유가 없으면 거부한다. 사유는 결재 이력의 상신 의견으로 남는다.
		quoteNote, err := s.checkQuoteForSubmit(ctx, tx, rec)
		if err != nil {
			return err
		}
		// 조직도로 결재선을 만든다(미리보기와 같은 buildLine). 만들 수 없으면 단계별 사유와 함께 거부하고 감사 로그에 남긴다.
		if !havePre || rec.TotalAmount != preAmount {
			return fmt.Errorf("%w: 상신하는 동안 내용이 바뀌었습니다. 다시 시도해 주세요", contract.ErrInvalidState)
		}
		if preErr != nil {
			return preErr
		}
		line, meta := preLine, preMeta
		if !line.Complete {
			return lineUnavailable{problems: line.Problems}
		}
		if len(preProbs) > 0 {
			return lineUnavailable{problems: preProbs}
		}
		if curEdits, err := s.Repo.DraftEdits(ctx, tx, id); err != nil {
			return err
		} else if curEdits != preEdits {
			return fmt.Errorf("%w: 상신하는 동안 참조·수신이 바뀌었습니다. 다시 시도해 주세요", contract.ErrInvalidState)
		}
		round := rec.Round
		if ev == evResubmit {
			round++
		}
		now := s.now()
		approvals := s.Approvals
		// 요청자의 현재 소속으로 귀속 조직·부서를 갱신하고(상신 뒤 고정), 이 차수의 결재선 스냅샷을 저장한다.
		ru, err := s.Delegations.User(ctx, tx, rec.RequesterID)
		if err != nil {
			return err
		}
		if err := s.Repo.SetOwner(ctx, tx, id, meta.UnitID, ru.DepartmentID); err != nil {
			return err
		}
		// B25: 같은 트랜잭션에서 예산을 다시 판정해 스냅샷을 저장한다. 사전 판정(결재선에 재무가 들어갔는지)과 달라지면 재시도한다.
		// 같은 부서·월의 동시 상신이 같은 잔액을 읽고 둘 다 통과하지 않게 줄 세운다(PG, S5).
		if err := dbutil.AcquireKeyLock(ctx, tx, fmt.Sprintf("budget:%d:%s", ru.DepartmentID, budgetMonthOf(now))); err != nil {
			return err
		}
		bud, err := s.judgeBudget(ctx, tx, ru.DepartmentID, rec.TotalAmount, id, budgetMonthOf(now))
		if err != nil {
			return err
		}
		if ru.DepartmentID != preDept || bud.Month != preBudget.Month || (bud.State == domain.BudgetOver) != (preBudget.State == domain.BudgetOver) {
			return errBudgetChanged
		}
		if err := (repository.SQLBudget{}).SetBudgetSnapshot(ctx, tx, id, withFinanceAdded(snapshotOf(bud), line)); err != nil {
			return err
		}
		if err := approvals.InsertLine(ctx, tx, id, round, repository.LineSnapshot{
			AsOfDay: string(asOf), RequesterUnitID: meta.UnitID, RequesterUnitPath: line.UnitPath, RequesterRank: meta.Rank,
			NotesJSON: notesJSON(line.Notes), Now: now, EditsJSON: preEdits,
		}, snapshotSteps(line)); err != nil {
			return err
		}
		if err := approvals.InsertRecipients(ctx, tx, id, round, preRecs); err != nil {
			return err
		}
		if err := approvals.InsertHistory(ctx, tx, repository.HistoryInsert{PRID: id, Round: round, ActorID: p.UserID, Action: action, Comment: quoteNote, At: now}); err != nil {
			return err
		}
		if err := s.Repo.SetPRState(ctx, tx, id, to, round, "", now); err != nil {
			return err
		}
		if err := s.record(ctx, tx, p, auditAction, audit.TargetPurchaseRequest, id,
			audit.DiffItem{Key: "status", Old: string(rec.Status), New: string(to)},
			audit.DiffItem{Key: "round", Old: fmt.Sprint(rec.Round), New: fmt.Sprint(round)},
			audit.DiffItem{Key: "line", New: lineSummary(line)},
			audit.DiffItem{Key: "requester_unit", New: line.UnitPath},
			audit.DiffItem{Key: "line_as_of", New: string(asOf)},
			audit.DiffItem{Key: "line_notes", New: strings.Join(line.Notes, ";")},
			audit.DiffItem{Key: "budget_check", New: budgetCheckText(bud)}); err != nil {
			return err
		}
		out, err = s.Repo.GetPR(ctx, tx, id, repository.ScopeAll)
		if err != nil {
			return err
		}
		return s.notifySubmitted(ctx, tx, out, p.UserID)
	})
	return out, err
}

func (s *PurchaseService) elig() eligibility {
	return eligibility{approvals: s.Approvals, delegations: s.Delegations, purchases: s.Repo, org: s.Org}
}

// ownerUnitOf 는 사용자의 현재 주소속 조직 번호다. 조직에 등록되지 않았으면 0 이다.
func (b Base) ownerUnitOf(ctx context.Context, userID int64) (int64, error) {
	if b.Org == nil {
		return 0, nil
	}
	person, err := b.Org.Person(ctx, userID, contract.DayOf(b.now()))
	switch {
	case errors.Is(err, contract.ErrNotInOrg), errors.Is(err, contract.ErrNotFound):
		return 0, nil
	case err != nil:
		return 0, err
	}
	return person.Primary.Unit.ID, nil
}

// refreshOwner 는 임시저장 건의 귀속 조직·부서를 요청자의 현재 소속으로 갱신한다(상신하면 고정된다, 명세 3.3).
func (s *PurchaseService) refreshOwner(ctx context.Context, tx contract.Tx, requesterID, prID, unit int64) error {
	u, err := s.Delegations.User(ctx, tx, requesterID)
	if err != nil {
		return err
	}
	return s.Repo.SetOwner(ctx, tx, prID, unit, u.DepartmentID)
}

// stalled 는 진행 중인 건의 지금 차례 단계를 결재할 사람이 한 명도 없는지다(배정자 퇴사·권한 상실, 위임 기간 만료·해제 등으로 생긴다).
func (s *PurchaseService) stalled(ctx context.Context, q contract.Tx, status domain.PRStatus, steps []domain.ApprovalStep, round int, requester, prID int64) (bool, error) {
	if status != domain.PRSubmitted && status != domain.PRInReview {
		return false, nil
	}
	for _, st := range steps {
		if st.Status != domain.StepPending {
			continue
		}
		ref := repository.StepRef{PRID: prID, Round: round, Seq: st.Seq, Kind: st.Kind, Status: st.Status, RequesterID: requester}
		ids, err := s.elig().approversOf(ctx, q, ref, s.now(), 0, true)
		return len(ids) == 0, err
	}
	return false, nil
}

// StalledIDs 는 prs 중 지금 차례 단계의 결재자가 없는 진행 중 건의 ID 집합이다(화면의 "결재자 없음" 표시용).
func (s *PurchaseService) StalledIDs(ctx context.Context, prs []domain.PurchaseRequest) (map[int64]bool, error) {
	out := map[int64]bool{}
	for _, p := range prs {
		ok, err := s.stalled(ctx, s.DB, p.Status, p.Steps, p.Round, p.RequesterID, p.ID)
		if err != nil {
			return nil, err
		}
		if ok {
			out[p.ID] = true
		}
	}
	return out, nil
}

// actionWithdraw 는 회수의 결재 이력 동작이다(마이그레이션 0006 의 action 값).
const actionWithdraw domain.ApprovalAction = "withdraw"

// Withdraw 는 결재자가 없어 멈춘 상신 건을 요청자가 임시저장으로 되돌린다(회수).
// 결재할 수 있는 사람이 있는 건은 회수할 수 없다. 차수를 올려 이전 결재선 기록을 보존하고, 다시 상신하면 새 결재선이 만들어진다.
func (s *PurchaseService) Withdraw(ctx context.Context, id int64) (domain.PurchaseRequest, error) {
	p, err := s.authorize(ctx, contract.PermPurchaseCreate, "purchase_request.withdraw", audit.TargetPurchaseRequest)
	if err != nil {
		return domain.PurchaseRequest{}, err
	}
	out, err := s.withdrawKind(ctx, p, id, true, "purchase_request.withdraw", audit.TargetPurchaseRequest)
	return toPR(out), err
}

// withdrawKind 는 구매 품의서(wantPurchase)와 양식 문서가 같이 쓰는 회수 본체다. 조건·이력·감사가 같다(B11, D9).
func (s *PurchaseService) withdrawKind(ctx context.Context, p contract.Principal, id int64, wantPurchase bool, action string, tt audit.TargetType) (repository.PRRecord, error) {
	scope, err := s.viewScope(ctx, p)
	if err != nil {
		return repository.PRRecord{}, err
	}
	if ctx, err = s.prefetchOrgFacts(ctx); err != nil {
		return repository.PRRecord{}, err
	}
	var out repository.PRRecord
	err = s.write(ctx, p, action, tt, id, func(tx contract.Tx) error {
		if err := dbutil.AcquireKeyLock(ctx, tx, fmt.Sprintf("pr:%d", id)); err != nil { // 같은 문서에 대한 상태 전이(상신·결재·취소·회수)를 PG 에서 줄 세운다
			return err
		}
		rec, err := s.ownRequestKind(ctx, tx, p, scope, id, wantPurchase)
		if err != nil {
			return err
		}
		to, err := transition(rec.Status, evWithdraw)
		if err != nil {
			return err
		}
		stuck, err := s.stalled(ctx, tx, rec.Status, toPR(rec).Steps, rec.Round, rec.RequesterID, id)
		if err != nil {
			return err
		}
		if !stuck {
			return fmt.Errorf("%w: 결재할 수 있는 결재자가 있어 회수할 수 없습니다", contract.ErrInvalidState)
		}
		round, now := rec.Round+1, s.now()
		// 회수도 결재 이력에 한 줄 남긴다(감사 로그와 같은 트랜잭션). 이력은 수정·삭제되지 않는다. 회수한 차수의 이력으로 남긴다.
		if err := s.Approvals.InsertHistory(ctx, tx, repository.HistoryInsert{PRID: id, Round: rec.Round, ActorID: p.UserID, Action: actionWithdraw, At: now}); err != nil {
			return err
		}
		if err := s.Repo.SetPRState(ctx, tx, id, to, round, "", now); err != nil {
			return err
		}
		if !wantPurchase {
			if err := runResetHook(ctx, s.Base, tx, p, rec); err != nil {
				return err
			}
		}
		diff := []audit.DiffItem{
			{Key: "status", Old: string(rec.Status), New: string(to)},
			{Key: "round", Old: fmt.Sprint(rec.Round), New: fmt.Sprint(round)}}
		if !wantPurchase {
			diff = append([]audit.DiffItem{{Key: "doc_no", New: rec.No}}, diff...)
		}
		if err := s.record(ctx, tx, p, action, tt, id, diff...); err != nil {
			return err
		}
		out, err = s.Repo.GetPR(ctx, tx, id, repository.ScopeAll)
		return err
	})
	return out, err
}

// Get 은 구매요청 한 건이다. 타 부서 건은 purchase:read_all 이 없으면 존재를 숨긴다(ErrNotFound).
func (s *PurchaseService) Get(ctx context.Context, id int64) (domain.PurchaseRequest, error) {
	p, err := contract.Authorize(ctx, contract.PermPurchaseRead)
	if err != nil {
		return domain.PurchaseRequest{}, err
	}
	scope, err := s.viewScope(ctx, p)
	if err != nil {
		return domain.PurchaseRequest{}, err
	}
	rec, err := s.Repo.GetPR(ctx, s.DB, id, scope)
	if errors.Is(err, contract.ErrNotFound) && !scope.All {
		// 위임으로 결재하게 된 건은 열람 범위 밖이어도 볼 수 있다(위임이 유효한 동안, 그 건에 한해서만).
		if ok, verr := s.elig().canView(ctx, s.DB, id, p.UserID, s.now()); verr == nil && ok {
			rec, err = s.Repo.GetPR(ctx, s.DB, id, repository.ScopeAll)
		}
	}
	if err != nil {
		return domain.PurchaseRequest{}, err
	}
	if rec.FormKind != "" && rec.FormKind != string(domain.FormKindPurchase) {
		return domain.PurchaseRequest{}, contract.ErrNotFound // 구매 API 는 구매 품의서만 다룬다(양식 문서는 문서 API)
	}
	s.touchRead(ctx, p, id)
	pr := toPR(rec)
	s.markCancelable(ctx, s.DB, p, &pr)
	pr.Editable = requestEditable(p, pr.RequesterID, pr.Status)
	if rec.RequesterID == p.UserID && (rec.Status == domain.PRDraft || rec.Status == domain.PRRejected) {
		// 기안자 본인의 임시저장·반려 건에는 저장해 둔 참조·수신 편집을 싣는다(자격을 잃은 대상은 뺀 것과 안내 문구).
		if raw, derr := s.Repo.DraftEdits(ctx, s.DB, rec.ID); derr == nil {
			pr.DraftEdits, pr.EditNotes = s.pruneEdits(ctx, parseEdits(raw))
		}
	}
	if err := s.decorate(ctx, s.DB, &pr); err != nil {
		return domain.PurchaseRequest{}, err
	}
	one := []domain.PurchaseRequest{pr}
	s.markPRNames(ctx, one)
	return one[0], nil
}

// List 는 구매요청 목록이다. 부서 격리가 저장소 조건절에 들어간다.
func (s *PurchaseService) List(ctx context.Context, f domain.PRFilter) ([]domain.PurchaseRequest, error) {
	p, err := contract.Authorize(ctx, contract.PermPurchaseRead)
	if err != nil {
		return nil, err
	}
	if err := validatePRPeriod(f); err != nil {
		return nil, err
	}
	scope, err := s.viewScope(ctx, p)
	if err != nil {
		return nil, err
	}
	recs, err := s.Repo.ListPRs(ctx, s.DB, repository.PRQuery{Filter: f, Scope: scope, Only: "purchase"})
	if err != nil {
		return nil, err
	}
	out := make([]domain.PurchaseRequest, len(recs))
	for i, r := range recs {
		out[i] = toPR(r)
	}
	s.markPRNames(ctx, out)
	return out, nil
}

// Count 는 List 와 같은 조건·열람 범위의 전체 건수다(계약 밖 확장: 목록 화면의 쪽 이동). afterId·limit·offset 은 무시한다.
func (s *PurchaseService) Count(ctx context.Context, f domain.PRFilter) (int, error) {
	p, err := contract.Authorize(ctx, contract.PermPurchaseRead)
	if err != nil {
		return 0, err
	}
	if err := validatePRPeriod(f); err != nil {
		return 0, err
	}
	scope, err := s.viewScope(ctx, p)
	if err != nil {
		return 0, err
	}
	return s.Repo.CountPRs(ctx, s.DB, repository.PRQuery{Filter: f, Scope: scope, Only: "purchase"})
}

// ListAll 은 내보내기용 목록이다: 쪽 나누기 없이 조건에 맞는 구매요청을 max+1 건까지 읽는다(호출한 쪽이 상한 초과를 판정). 열람 범위·권한은 List 와 같다.
func (s *PurchaseService) ListAll(ctx context.Context, f domain.PRFilter, max int) ([]domain.PurchaseRequest, error) {
	p, err := contract.Authorize(ctx, contract.PermPurchaseRead)
	if err != nil {
		return nil, err
	}
	if err := validatePRPeriod(f); err != nil {
		return nil, err
	}
	scope, err := s.viewScope(ctx, p)
	if err != nil {
		return nil, err
	}
	recs, err := s.Repo.ListPRs(ctx, s.DB, repository.PRQuery{Filter: f, Scope: scope, Only: "purchase", Max: max + 1})
	if err != nil {
		return nil, err
	}
	out := make([]domain.PurchaseRequest, len(recs))
	for i, r := range recs {
		out[i] = toPR(r)
	}
	s.markPRNames(ctx, out)
	return out, nil
}

func validatePRPeriod(f domain.PRFilter) error {
	if f.AfterID != 0 && f.Sort != "" {
		return validation("정렬과 이어보기 커서는 같이 쓸 수 없습니다")
	}
	switch f.Sort {
	case "", "created_at", "total_amount":
	default:
		return validation("정렬 기준은 작성일 또는 금액만 쓸 수 있습니다")
	}
	switch f.Order {
	case "", "asc", "desc":
	default:
		return validation("정렬 방향은 오름차순 또는 내림차순만 쓸 수 있습니다")
	}
	for _, d := range []string{f.From, f.To} {
		if d == "" {
			continue
		}
		if _, err := time.Parse("2006-01-02", d); err != nil {
			return validation("날짜는 YYYY-MM-DD 형식으로 입력하세요")
		}
	}
	if f.From != "" && f.To != "" && f.To < f.From {
		return validation("종료일은 시작일 이후여야 합니다.")
	}
	return nil
}

// MyRequestSummary 는 내가 올린 구매요청의 상태별 건수와, 그중 결재자가 없어 멈춘 건수다(홈 화면 카드용, 읽기 전용).
// 요청자 본인 것만 세므로 부서 격리를 따로 걸 필요가 없다.
func (s *PurchaseService) MyRequestSummary(ctx context.Context) (map[domain.PRStatus]int, int, error) {
	p, err := contract.Authorize(ctx, contract.PermPurchaseRead)
	if err != nil {
		return nil, 0, err
	}
	counts, err := s.Repo.CountByRequester(ctx, s.DB, p.UserID)
	if err != nil {
		return nil, 0, err
	}
	stalled := 0
	for _, st := range []domain.PRStatus{domain.PRSubmitted, domain.PRInReview} {
		if counts[st] == 0 {
			continue
		}
		recs, err := s.Repo.ListPRs(ctx, s.DB, repository.PRQuery{Filter: domain.PRFilter{Status: st, RequesterID: p.UserID, Limit: 200}, Scope: repository.ScopeAll, Only: "purchase"})
		if err != nil {
			return nil, 0, err
		}
		for _, r := range recs {
			stuck, err := s.stalled(ctx, s.DB, r.Status, r.Steps, r.Round, r.RequesterID, r.ID)
			if err != nil {
				return nil, 0, err
			}
			if stuck {
				stalled++
			}
		}
	}
	return counts, stalled, nil
}

// isPurchaseKind 는 양식 종류가 구매 품의서인지다(기존 행·종류 미상은 구매 품의서로 본다).
func isPurchaseKind(kind string) bool { return kind == "" || kind == string(domain.FormKindPurchase) }
