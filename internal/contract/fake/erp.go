package fake

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
)

// ERP 는 domain.PurchaseService · ApprovalService · InventoryService 를 한 메모리 저장소로 구현한 가짜다.
// 실제 구현(erp)과 같은 규칙을 따른다: 권한 검사, 부서 격리, 자기 결재 금지, 반려 사유 필수,
// 초과 입고 금지, 재고 = 원장 합, 쓰기마다 감사 기록(기록 실패 시 변경 취소).
type ERP struct {
	mu  sync.Mutex
	rec audit.Recorder
	Now func() time.Time

	nextID  int64
	items   map[int64]domain.Item
	prs     map[int64]*domain.PurchaseRequest
	pos     map[int64]*domain.PurchaseOrder
	rcpts   map[int64]domain.Receipt
	history []domain.ApprovalRecord
	delegs  []fakeDeleg
	ledger  []ledgerRow
}

type ledgerRow struct {
	itemID int64
	lotNo  string
	expiry string
	qty    int64
}

// NewERP 는 감사 기록기를 받아 빈 저장소를 만든다.
func NewERP(rec audit.Recorder) *ERP {
	return &ERP{
		rec: rec, Now: time.Now,
		items: map[int64]domain.Item{}, prs: map[int64]*domain.PurchaseRequest{},
		pos: map[int64]*domain.PurchaseOrder{}, rcpts: map[int64]domain.Receipt{},
	}
}

func (e *ERP) id() int64 { e.nextID++; return e.nextID }

// AddItem 은 테스트 시드용 품목 등록이다(ID 를 채워 돌려준다). 서비스 계약에는 없다.
func (e *ERP) AddItem(it domain.Item) domain.Item {
	e.mu.Lock()
	defer e.mu.Unlock()
	it.ID = e.id()
	e.items[it.ID] = it
	return it
}

// SeedStock 은 테스트 시드용 입고 원장 행을 직접 넣는다.
func (e *ERP) SeedStock(itemID int64, lotNo, expiry string, qty int64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.ledger = append(e.ledger, ledgerRow{itemID, lotNo, expiry, qty})
}

// record 는 감사 기록을 남긴다. 실패하면 호출자는 변경을 되돌리고 오류를 돌려준다.
func (e *ERP) record(ctx context.Context, p contract.Principal, action string, tt audit.TargetType, id int64, res audit.Result, detail string, diff ...audit.DiffItem) error {
	return e.rec.Record(ctx, nil, audit.Entry{
		Actor: p, Action: action, TargetType: tt, TargetID: fmt.Sprint(id),
		Result: res, Detail: detail, Diff: diff,
	})
}

// deny 는 권한 거부를 감사에 남기고 오류를 돌려준다.
func (e *ERP) authorize(ctx context.Context, perm contract.Permission, action string, tt audit.TargetType) (contract.Principal, error) {
	p, err := contract.Authorize(ctx, perm)
	if err != nil {
		if p.UserID != 0 {
			_ = e.record(ctx, p, action, tt, 0, audit.ResultDenied, string(perm))
		}
		return p, err
	}
	return p, nil
}

func (e *ERP) canSee(p contract.Principal, pr *domain.PurchaseRequest) bool {
	return p.Can(contract.PermPurchaseReadAll) || pr.DepartmentID == p.DepartmentID
}

// ---- PurchaseService ----

func (e *ERP) buildLines(in domain.DraftInput) ([]domain.PRLine, int64, error) {
	if strings.TrimSpace(in.Title) == "" || len(in.Lines) == 0 {
		return nil, 0, fmt.Errorf("%w: 제목과 품목 줄이 필요합니다", contract.ErrValidation)
	}
	var total int64
	lines := make([]domain.PRLine, 0, len(in.Lines))
	for _, l := range in.Lines {
		it, ok := e.items[l.ItemID]
		if !ok || l.Quantity <= 0 || l.UnitPrice < 0 {
			return nil, 0, fmt.Errorf("%w: 품목 줄", contract.ErrValidation)
		}
		amt := l.Quantity * l.UnitPrice
		total += amt
		lines = append(lines, domain.PRLine{ID: e.id(), ItemID: it.ID, ItemName: it.Name, Quantity: l.Quantity, UnitPrice: l.UnitPrice, Amount: amt})
	}
	return lines, total, nil
}

// CreateDraft 는 domain.PurchaseService 를 구현한다.
func (e *ERP) CreateDraft(ctx context.Context, in domain.DraftInput) (domain.PurchaseRequest, error) {
	p, err := e.authorize(ctx, contract.PermPurchaseCreate, "purchase_request.create", audit.TargetPurchaseRequest)
	if err != nil {
		return domain.PurchaseRequest{}, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	lines, total, err := e.buildLines(in)
	if err != nil {
		return domain.PurchaseRequest{}, err
	}
	now := e.Now().UTC()
	pr := &domain.PurchaseRequest{
		ID: e.id(), RequesterID: p.UserID, RequesterName: p.Name, DepartmentID: p.DepartmentID,
		VendorID: in.VendorID, Title: in.Title, Reason: in.Reason, Status: domain.PRDraft,
		TotalAmount: total, Lines: lines, CreatedAt: now, UpdatedAt: now,
	}
	pr.No = fmt.Sprintf("PR-%05d", pr.ID)
	if err := e.record(ctx, p, "purchase_request.create", audit.TargetPurchaseRequest, pr.ID, audit.ResultSuccess, "",
		audit.DiffItem{Key: "status", New: string(pr.Status)}, audit.DiffItem{Key: "total_amount", New: fmt.Sprint(total)}); err != nil {
		return domain.PurchaseRequest{}, err
	}
	e.prs[pr.ID] = pr
	return *pr, nil
}

// DraftReorder 는 domain.PurchaseService 를 구현한다. 안전재고 미달 품목만 허용하고, 수량은 부족분이다.
func (e *ERP) DraftReorder(ctx context.Context, in domain.ReorderInput) (domain.PurchaseRequest, error) {
	if _, err := contract.Authorize(ctx, contract.PermInventoryRead); err != nil {
		return domain.PurchaseRequest{}, err
	}
	p, err := e.authorize(ctx, contract.PermPurchaseCreate, "purchase_request.create", audit.TargetPurchaseRequest)
	if err != nil {
		return domain.PurchaseRequest{}, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(in.ItemIDs) == 0 {
		return domain.PurchaseRequest{}, fmt.Errorf("%w: 품목이 필요합니다", contract.ErrValidation)
	}
	reason := in.Reason
	if strings.TrimSpace(reason) == "" {
		reason = "안전재고 미달 재주문"
	}
	draft := domain.DraftInput{VendorID: in.VendorID, Title: "안전재고 재주문", Reason: reason}
	for _, id := range in.ItemIDs {
		it, ok := e.items[id]
		if !ok {
			return domain.PurchaseRequest{}, fmt.Errorf("%w: 품목", contract.ErrNotFound)
		}
		st := e.stockLocked(it)
		if !st.BelowSafety {
			return domain.PurchaseRequest{}, fmt.Errorf("%w: 안전재고 미달 품목이 아닙니다", contract.ErrValidation)
		}
		var price, latest int64
		for _, po := range e.pos {
			for _, l := range po.Lines {
				if l.ItemID == id && po.ID >= latest {
					latest, price = po.ID, l.UnitPrice
				}
			}
		}
		draft.Lines = append(draft.Lines, domain.PRLineInput{ItemID: id, Quantity: it.SafetyStock - st.OnHand, UnitPrice: price})
	}
	lines, total, err := e.buildLines(draft)
	if err != nil {
		return domain.PurchaseRequest{}, err
	}
	now := e.Now().UTC()
	pr := &domain.PurchaseRequest{
		ID: e.id(), RequesterID: p.UserID, RequesterName: p.Name, DepartmentID: p.DepartmentID,
		VendorID: in.VendorID, Title: draft.Title, Reason: reason, Status: domain.PRDraft,
		TotalAmount: total, Lines: lines, CreatedAt: now, UpdatedAt: now,
	}
	pr.No = fmt.Sprintf("PR-%05d", pr.ID)
	if err := e.record(ctx, p, "purchase_request.create", audit.TargetPurchaseRequest, pr.ID, audit.ResultSuccess, "",
		audit.DiffItem{Key: "status", New: string(pr.Status)}, audit.DiffItem{Key: "total_amount", New: fmt.Sprint(total)},
		audit.DiffItem{Key: "source", New: "reorder"}); err != nil {
		return domain.PurchaseRequest{}, err
	}
	e.prs[pr.ID] = pr
	return *pr, nil
}

// UpdateDraft 는 domain.PurchaseService 를 구현한다. 임시저장·반려 상태만 수정 가능.
func (e *ERP) UpdateDraft(ctx context.Context, id int64, in domain.DraftInput) (domain.PurchaseRequest, error) {
	p, err := e.authorize(ctx, contract.PermPurchaseCreate, "purchase_request.update", audit.TargetPurchaseRequest)
	if err != nil {
		return domain.PurchaseRequest{}, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	pr, ok := e.prs[id]
	if !ok || !e.canSee(p, pr) {
		return domain.PurchaseRequest{}, contract.ErrNotFound
	}
	if pr.RequesterID != p.UserID {
		return domain.PurchaseRequest{}, contract.ErrForbidden
	}
	if pr.Status != domain.PRDraft && pr.Status != domain.PRRejected {
		return domain.PurchaseRequest{}, contract.ErrInvalidState
	}
	lines, total, err := e.buildLines(in)
	if err != nil {
		return domain.PurchaseRequest{}, err
	}
	if err := e.record(ctx, p, "purchase_request.update", audit.TargetPurchaseRequest, id, audit.ResultSuccess, "",
		audit.DiffItem{Key: "total_amount", Old: fmt.Sprint(pr.TotalAmount), New: fmt.Sprint(total)}); err != nil {
		return domain.PurchaseRequest{}, err
	}
	pr.Title, pr.Reason, pr.VendorID, pr.Lines, pr.TotalAmount, pr.UpdatedAt = in.Title, in.Reason, in.VendorID, lines, total, e.Now().UTC()
	return *pr, nil
}

// approvalLine 은 금액 구간별 결재선이다(실구현은 approval_line_rules 테이블).
func approvalLine(total int64) []domain.StepKind {
	switch {
	case total < 1_000_000:
		return []domain.StepKind{domain.StepTeamLead}
	case total < 10_000_000:
		return []domain.StepKind{domain.StepTeamLead, domain.StepDeptHead}
	default:
		return []domain.StepKind{domain.StepTeamLead, domain.StepDeptHead, domain.StepFinance}
	}
}

// Submit 은 domain.PurchaseService 를 구현한다.
func (e *ERP) Submit(ctx context.Context, id int64) (domain.PurchaseRequest, error) {
	p, err := e.authorize(ctx, contract.PermPurchaseCreate, "purchase_request.submit", audit.TargetPurchaseRequest)
	if err != nil {
		return domain.PurchaseRequest{}, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	pr, ok := e.prs[id]
	if !ok || !e.canSee(p, pr) {
		return domain.PurchaseRequest{}, contract.ErrNotFound
	}
	if pr.RequesterID != p.UserID {
		return domain.PurchaseRequest{}, contract.ErrForbidden
	}
	if pr.Status != domain.PRDraft && pr.Status != domain.PRRejected {
		return domain.PurchaseRequest{}, contract.ErrInvalidState
	}
	if pr.VendorID == 0 { // 실제 구현과 같게: 거래처 없는 상신은 400
		return domain.PurchaseRequest{}, fmt.Errorf("%w: 상신하려면 거래처가 필요합니다", contract.ErrValidation)
	}
	action := domain.ActionSubmit
	if pr.Status == domain.PRRejected {
		action = domain.ActionResubmit
	}
	var steps []domain.ApprovalStep
	for i, k := range approvalLine(pr.TotalAmount) {
		st := domain.StepWaiting
		if i == 0 {
			st = domain.StepPending
		}
		steps = append(steps, domain.ApprovalStep{Seq: i + 1, Kind: k, Status: st})
	}
	if err := e.record(ctx, p, "purchase_request.submit", audit.TargetPurchaseRequest, id, audit.ResultSuccess, "",
		audit.DiffItem{Key: "status", Old: string(pr.Status), New: string(domain.PRSubmitted)}); err != nil {
		return domain.PurchaseRequest{}, err
	}
	pr.Status, pr.Steps, pr.RejectReason, pr.UpdatedAt = domain.PRSubmitted, steps, "", e.Now().UTC()
	e.history = append(e.history, domain.ApprovalRecord{ID: e.id(), PRID: id, ActorID: p.UserID, Action: action, At: e.Now().UTC()})
	return *pr, nil
}

// Get 은 domain.PurchaseService 를 구현한다. 타 부서 건은 read_all 없이는 ErrNotFound(존재 은닉).
func (e *ERP) Get(ctx context.Context, id int64) (domain.PurchaseRequest, error) {
	p, err := contract.Authorize(ctx, contract.PermPurchaseRead)
	if err != nil {
		return domain.PurchaseRequest{}, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	pr, ok := e.prs[id]
	if !ok || !e.canSee(p, pr) {
		return domain.PurchaseRequest{}, contract.ErrNotFound
	}
	return *pr, nil
}

// List 는 domain.PurchaseService 를 구현한다.
func (e *ERP) List(ctx context.Context, f domain.PRFilter) ([]domain.PurchaseRequest, error) {
	p, err := contract.Authorize(ctx, contract.PermPurchaseRead)
	if err != nil {
		return nil, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []domain.PurchaseRequest
	for _, pr := range e.prs {
		if !e.canSee(p, pr) || pr.ID <= f.AfterID {
			continue
		}
		if f.Status != "" && pr.Status != f.Status || f.RequesterID != 0 && pr.RequesterID != f.RequesterID ||
			f.DepartmentID != 0 && pr.DepartmentID != f.DepartmentID {
			continue
		}
		out = append(out, *pr)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	if f.Limit > 0 && len(out) > f.Limit {
		out = out[:f.Limit]
	}
	return out, nil
}

// ---- ApprovalService ----

func stepRole(k domain.StepKind) contract.Role {
	if k == domain.StepFinance {
		return contract.RoleFinance
	}
	return contract.RoleManager
}

func currentStep(pr *domain.PurchaseRequest) *domain.ApprovalStep {
	for i := range pr.Steps {
		if pr.Steps[i].Status == domain.StepPending {
			return &pr.Steps[i]
		}
	}
	return nil
}

// ListPending 은 domain.ApprovalService 를 구현한다. 내 역할·부서 기준 차례 건만 돌려준다.
func (e *ERP) ListPending(ctx context.Context) ([]domain.PurchaseRequest, error) {
	p, err := contract.Authorize(ctx, contract.PermApprovalRead)
	if err != nil {
		return nil, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []domain.PurchaseRequest
	for _, pr := range e.prs {
		st := currentStep(pr)
		if st == nil || pr.RequesterID == p.UserID || stepRole(st.Kind) != p.Role {
			continue
		}
		if p.Role != contract.RoleFinance && pr.DepartmentID != p.DepartmentID {
			continue
		}
		out = append(out, *pr)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// Agree 는 domain.ApprovalService 를 구현한다. 가짜 구현은 합의 단계를 만들지 않으므로 항상 합의 차례가 아님으로 거부한다.
func (e *ERP) Agree(ctx context.Context, prID int64, d domain.Decision) (domain.DecisionResult, error) {
	if _, err := e.authorize(ctx, contract.PermApprovalAgree, "approval.decide", audit.TargetApproval); err != nil {
		return domain.DecisionResult{}, err
	}
	return domain.DecisionResult{}, fmt.Errorf("%w: 지금은 합의 차례가 아닙니다", contract.ErrForbidden)
}

// DecideBatch 는 domain.ApprovalService 를 구현한다. 가짜 구현은 건마다 Decide 를 부르고 실패는 건별 결과에 담는다.
func (e *ERP) DecideBatch(ctx context.Context, items []domain.BatchItem, comment string) ([]domain.BatchResult, error) {
	if _, err := e.authorize(ctx, contract.PermApprovalAgree, "approval.batch", audit.TargetApproval); err != nil {
		return nil, err
	}
	out := make([]domain.BatchResult, 0, len(items))
	for _, it := range items {
		res, err := e.Decide(ctx, it.DocID, domain.Decision{Approve: true, Comment: comment})
		if err != nil {
			out = append(out, domain.BatchResult{DocID: it.DocID, Message: err.Error()})
			continue
		}
		out = append(out, domain.BatchResult{DocID: it.DocID, OK: true, Message: "처리했습니다", Status: res.Request.Status})
	}
	return out, nil
}

// Decide 는 domain.ApprovalService 를 구현한다.
func (e *ERP) Decide(ctx context.Context, prID int64, d domain.Decision) (domain.DecisionResult, error) {
	p, err := e.authorize(ctx, contract.PermApprovalDecide, "approval.decide", audit.TargetApproval)
	if err != nil {
		return domain.DecisionResult{}, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	pr, ok := e.prs[prID]
	if !ok || !e.canSee(p, pr) && p.Role != contract.RoleFinance {
		return domain.DecisionResult{}, contract.ErrNotFound
	}
	st := currentStep(pr)
	if (pr.Status != domain.PRSubmitted && pr.Status != domain.PRInReview) || st == nil {
		return domain.DecisionResult{}, contract.ErrInvalidState
	}
	// 위임 결재면 위임자의 역할·부서로 차례를 판정한다. 위임자가 요청자여도 자기 결재 금지다.
	eff := p
	if d.OnBehalfOfID != 0 {
		dg := e.activeDeleg(d.OnBehalfOfID, p.UserID)
		if dg == nil {
			_ = e.record(ctx, p, "approval.decide", audit.TargetApproval, prID, audit.ResultDenied, "유효한 위임 없음")
			return domain.DecisionResult{}, contract.ErrForbidden
		}
		eff.UserID, eff.Role, eff.DepartmentID = dg.delegatorID, dg.role, dg.dept
	}
	if pr.RequesterID == p.UserID {
		_ = e.record(ctx, p, "approval.decide", audit.TargetApproval, prID, audit.ResultDenied, "자기 결재")
		return domain.DecisionResult{}, contract.ErrSelfApproval
	}
	if stepRole(st.Kind) != eff.Role || eff.Role != contract.RoleFinance && pr.DepartmentID != eff.DepartmentID {
		_ = e.record(ctx, p, "approval.decide", audit.TargetApproval, prID, audit.ResultDenied, "결재 차례 아님")
		return domain.DecisionResult{}, contract.ErrForbidden
	}
	if !d.Approve && strings.TrimSpace(d.Comment) == "" {
		return domain.DecisionResult{}, fmt.Errorf("%w: 반려 사유가 필요합니다", contract.ErrValidation)
	}

	// 변경은 사본에 적용하고 감사 기록이 성공한 뒤에 확정한다.
	cp := *pr
	cp.Steps = append([]domain.ApprovalStep(nil), pr.Steps...)
	cs := currentStep(&cp)
	now := e.Now().UTC()
	cs.ApproverID, cs.Comment, cs.DecidedAt = p.UserID, d.Comment, &now
	action := domain.ActionReject
	var po *domain.PurchaseOrder
	if d.Approve {
		action = domain.ActionApprove
		cs.Status = domain.StepApproved
		if cs.Seq < len(cp.Steps) {
			cp.Steps[cs.Seq].Status = domain.StepPending
			cp.Status = domain.PRInReview // 첫 승인 뒤 다음 단계가 남으면 결재중
		} else {
			cp.Status = domain.PROrdered
			po = e.makePO(&cp)
			cp.PurchaseOrderID = po.ID
		}
	} else {
		cs.Status = domain.StepRejected
		cp.Status, cp.RejectReason = domain.PRRejected, d.Comment
	}
	cp.UpdatedAt = now
	if err := e.record(ctx, p, "approval.decide", audit.TargetApproval, prID, audit.ResultSuccess, "",
		append([]audit.DiffItem{{Key: "status", Old: string(pr.Status), New: string(cp.Status)},
			{Key: "action", New: string(action)}}, onBehalfDiff(d.OnBehalfOfID)...)...); err != nil {
		return domain.DecisionResult{}, err
	}
	*pr = cp
	if po != nil {
		e.pos[po.ID] = po
	}
	e.history = append(e.history, domain.ApprovalRecord{ID: e.id(), PRID: prID, Seq: cs.Seq, ActorID: p.UserID, Action: action, Comment: d.Comment, At: now, OnBehalfOfID: d.OnBehalfOfID})
	return domain.DecisionResult{Request: *pr, PurchaseOrder: po}, nil
}

func (e *ERP) makePO(pr *domain.PurchaseRequest) *domain.PurchaseOrder {
	po := &domain.PurchaseOrder{ID: e.id(), PRID: pr.ID, VendorID: pr.VendorID, Status: domain.POOpen, TotalAmount: pr.TotalAmount, CreatedAt: e.Now().UTC()}
	po.No = fmt.Sprintf("PO-%05d", po.ID)
	for _, l := range pr.Lines {
		po.Lines = append(po.Lines, domain.POLine{ID: e.id(), ItemID: l.ItemID, ItemName: l.ItemName, Quantity: l.Quantity, UnitPrice: l.UnitPrice})
	}
	return po
}

// History 는 domain.ApprovalService 를 구현한다.
func (e *ERP) History(ctx context.Context, prID int64) ([]domain.ApprovalRecord, error) {
	p, err := contract.Authorize(ctx, contract.PermApprovalRead)
	if err != nil {
		return nil, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	pr, ok := e.prs[prID]
	if !ok || !e.canSee(p, pr) {
		return nil, contract.ErrNotFound
	}
	var out []domain.ApprovalRecord
	for _, h := range e.history {
		if h.PRID == prID {
			out = append(out, h)
		}
	}
	return out, nil
}

// ---- InventoryService ----

// SearchItems 는 domain.InventoryService 를 구현한다.
func (e *ERP) SearchItems(ctx context.Context, query string, limit int) ([]domain.Item, error) {
	if _, err := contract.Authorize(ctx, contract.PermInventoryRead); err != nil {
		return nil, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	q := strings.ToLower(query)
	var out []domain.Item
	for _, it := range e.items {
		if strings.Contains(strings.ToLower(it.Name), q) || strings.Contains(strings.ToLower(it.SKU), q) {
			out = append(out, it)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (e *ERP) stockLocked(it domain.Item) domain.ItemStock {
	type key struct{ lot, exp string }
	sums := map[key]int64{}
	var total int64
	for _, r := range e.ledger {
		if r.itemID == it.ID {
			sums[key{r.lotNo, r.expiry}] += r.qty
			total += r.qty
		}
	}
	st := domain.ItemStock{Item: it, OnHand: total, BelowSafety: total < it.SafetyStock}
	for k, q := range sums {
		st.Lots = append(st.Lots, domain.LotStock{LotNo: k.lot, ExpiryDate: k.exp, Quantity: q})
	}
	sort.Slice(st.Lots, func(i, j int) bool { return st.Lots[i].LotNo < st.Lots[j].LotNo })
	return st
}

// GetStock 은 domain.InventoryService 를 구현한다. 수량은 원장 합이다.
func (e *ERP) GetStock(ctx context.Context, itemID int64) (domain.ItemStock, error) {
	if _, err := contract.Authorize(ctx, contract.PermInventoryRead); err != nil {
		return domain.ItemStock{}, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	it, ok := e.items[itemID]
	if !ok {
		return domain.ItemStock{}, contract.ErrNotFound
	}
	return e.stockLocked(it), nil
}

// ListBelowSafety 는 domain.InventoryService 를 구현한다.
func (e *ERP) ListBelowSafety(ctx context.Context) ([]domain.ItemStock, error) {
	if _, err := contract.Authorize(ctx, contract.PermInventoryRead); err != nil {
		return nil, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []domain.ItemStock
	for _, it := range e.items {
		if st := e.stockLocked(it); st.BelowSafety {
			out = append(out, st)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Item.ID < out[j].Item.ID })
	return out, nil
}

// ListExpiring 은 domain.InventoryService 를 구현한다.
func (e *ERP) ListExpiring(ctx context.Context, withinDays int) ([]domain.ExpiringLot, error) {
	if _, err := contract.Authorize(ctx, contract.PermInventoryRead); err != nil {
		return nil, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	today := e.Now().UTC().Truncate(24 * time.Hour)
	var out []domain.ExpiringLot
	for _, it := range e.items {
		for _, lot := range e.stockLocked(it).Lots {
			if lot.ExpiryDate == "" || lot.Quantity <= 0 {
				continue
			}
			exp, err := time.Parse("2006-01-02", lot.ExpiryDate)
			if err != nil {
				continue
			}
			if days := int(exp.Sub(today).Hours() / 24); days <= withinDays {
				out = append(out, domain.ExpiringLot{Item: it, Lot: lot, DaysToExpire: days})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].DaysToExpire < out[j].DaysToExpire })
	return out, nil
}

// GetPurchaseOrder 는 domain.InventoryService 를 구현한다.
func (e *ERP) GetPurchaseOrder(ctx context.Context, id int64) (domain.PurchaseOrder, error) {
	if _, err := contract.Authorize(ctx, contract.PermInventoryRead); err != nil {
		return domain.PurchaseOrder{}, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	po, ok := e.pos[id]
	if !ok {
		return domain.PurchaseOrder{}, contract.ErrNotFound
	}
	return *po, nil
}

// ListPurchaseOrders 는 domain.InventoryService 를 구현한다.
func (e *ERP) ListPurchaseOrders(ctx context.Context, f domain.POFilter) ([]domain.PurchaseOrder, error) {
	if _, err := contract.Authorize(ctx, contract.PermInventoryRead); err != nil {
		return nil, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []domain.PurchaseOrder
	for _, po := range e.pos {
		if f.Status == "" || po.Status == f.Status {
			out = append(out, *po)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	if f.Limit > 0 && len(out) > f.Limit {
		out = out[:f.Limit]
	}
	return out, nil
}

// GetReceipt 는 domain.InventoryService 를 구현한다.
func (e *ERP) GetReceipt(ctx context.Context, id int64) (domain.Receipt, error) {
	if _, err := contract.Authorize(ctx, contract.PermInventoryRead); err != nil {
		return domain.Receipt{}, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	r, ok := e.rcpts[id]
	if !ok {
		return domain.Receipt{}, contract.ErrNotFound
	}
	return r, nil
}

// Receive 는 domain.InventoryService 를 구현한다. 부분 입고 허용, PO 수량 초과 금지.
func (e *ERP) Receive(ctx context.Context, in domain.ReceiveInput) (domain.Receipt, error) {
	p, err := e.authorize(ctx, contract.PermInventoryManage, "receipt.create", audit.TargetReceipt)
	if err != nil {
		return domain.Receipt{}, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	po, ok := e.pos[in.POID]
	if !ok {
		return domain.Receipt{}, contract.ErrNotFound
	}
	cp := *po
	cp.Lines = append([]domain.POLine(nil), po.Lines...)
	var rows []ledgerRow
	for _, l := range in.Lines {
		var pl *domain.POLine
		for i := range cp.Lines {
			if cp.Lines[i].ID == l.POLineID {
				pl = &cp.Lines[i]
			}
		}
		if pl == nil || l.Quantity <= 0 {
			return domain.Receipt{}, fmt.Errorf("%w: 입고 줄", contract.ErrValidation)
		}
		if pl.ReceivedQty+l.Quantity > pl.Quantity {
			return domain.Receipt{}, contract.ErrOverReceipt
		}
		pl.ReceivedQty += l.Quantity
		rows = append(rows, ledgerRow{pl.ItemID, l.LotNo, l.ExpiryDate, l.Quantity})
	}
	if len(rows) == 0 {
		return domain.Receipt{}, fmt.Errorf("%w: 입고 줄이 없습니다", contract.ErrValidation)
	}
	cp.Status = domain.POReceived
	for _, l := range cp.Lines {
		if l.ReceivedQty < l.Quantity {
			cp.Status = domain.POPartial
		}
	}
	rc := domain.Receipt{ID: e.id(), POID: po.ID, ReceivedBy: p.UserID, ReceivedAt: e.Now().UTC(), Lines: in.Lines}
	if err := e.record(ctx, p, "receipt.create", audit.TargetReceipt, rc.ID, audit.ResultSuccess, "",
		audit.DiffItem{Key: "po_status", Old: string(po.Status), New: string(cp.Status)}); err != nil {
		return domain.Receipt{}, err
	}
	*po = cp
	e.ledger = append(e.ledger, rows...)
	e.rcpts[rc.ID] = rc
	return rc, nil
}

// ---- DelegationService (결재 위임, 가짜) ----

type fakeDeleg struct {
	domain.Delegation
	role        contract.Role
	dept        int64
	delegatorID int64
}

func onBehalfDiff(id int64) []audit.DiffItem {
	if id == 0 {
		return nil
	}
	return []audit.DiffItem{{Key: "on_behalf_of", New: fmt.Sprint(id)}}
}

func (e *ERP) activeDeleg(delegatorID, delegateID int64) *fakeDeleg {
	now := e.Now().UTC()
	for i := range e.delegs {
		d := &e.delegs[i]
		if d.DelegatorID == delegatorID && d.DelegateID == delegateID && d.Active(now) {
			return d
		}
	}
	return nil
}

// Create 는 domain.DelegationService 를 구현한다. 위임자는 호출한 본인이다.
func (e *ERP) Create(ctx context.Context, in domain.DelegationInput) (domain.Delegation, error) {
	p, err := e.authorize(ctx, contract.PermApprovalDelegate, "delegation.create", audit.TargetDelegation)
	if err != nil {
		return domain.Delegation{}, err
	}
	if in.DelegateID == 0 || in.DelegateID == p.UserID || !in.To.After(in.From) {
		return domain.Delegation{}, fmt.Errorf("%w: 수임자·기간", contract.ErrValidation)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	d := fakeDeleg{role: p.Role, dept: p.DepartmentID, delegatorID: p.UserID}
	d.Delegation = domain.Delegation{ID: e.id(), DelegatorID: p.UserID, DelegatorName: p.Name, DelegateID: in.DelegateID,
		From: in.From.UTC(), To: in.To.UTC(), CreatedAt: e.Now().UTC()}
	if err := e.record(ctx, p, "delegation.create", audit.TargetDelegation, d.ID, audit.ResultSuccess, "",
		audit.DiffItem{Key: "delegate_id", New: fmt.Sprint(in.DelegateID)},
		audit.DiffItem{Key: "from", New: d.From.Format(time.RFC3339)},
		audit.DiffItem{Key: "to", New: d.To.Format(time.RFC3339)}); err != nil {
		return domain.Delegation{}, err
	}
	e.delegs = append(e.delegs, d)
	return d.Delegation, nil
}

// Revoke 는 domain.DelegationService 를 구현한다. 본인이 지정한 위임만 해제한다.
func (e *ERP) Revoke(ctx context.Context, id int64) error {
	p, err := e.authorize(ctx, contract.PermApprovalDelegate, "delegation.revoke", audit.TargetDelegation)
	if err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	for i := range e.delegs {
		d := &e.delegs[i]
		if d.ID != id || d.DelegatorID != p.UserID {
			continue
		}
		if d.RevokedAt != nil {
			return contract.ErrInvalidState
		}
		now := e.Now().UTC()
		if err := e.record(ctx, p, "delegation.revoke", audit.TargetDelegation, id, audit.ResultSuccess, "",
			audit.DiffItem{Key: "revoked_at", New: now.Format(time.RFC3339)}); err != nil {
			return err
		}
		d.RevokedAt = &now
		return nil
	}
	return contract.ErrNotFound
}

// ListMine 은 domain.DelegationService 를 구현한다.
func (e *ERP) ListMine(ctx context.Context) (given, received []domain.Delegation, err error) {
	p, err := contract.Authorize(ctx, contract.PermApprovalDelegate)
	if err != nil {
		return nil, nil, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, d := range e.delegs {
		if d.DelegatorID == p.UserID {
			given = append(given, d.Delegation)
		}
		if d.DelegateID == p.UserID {
			received = append(received, d.Delegation)
		}
	}
	return given, received, nil
}

// Candidates 는 domain.DelegationService 를 구현한다. 가짜는 사용자 목록이 없어 비어 있다.
func (e *ERP) Candidates(ctx context.Context) ([]domain.DelegateCandidate, error) {
	if _, err := contract.Authorize(ctx, contract.PermApprovalDelegate); err != nil {
		return nil, err
	}
	return nil, nil
}
