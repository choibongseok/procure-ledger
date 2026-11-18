package service

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/repository"
)

// 재고 실사(B24 ③, 3.4·4절).
// 실사 표는 한 창고·한 기준일의 스냅샷이고, 차이는 조정 전표(양식 문서 ADJ) 결재가 끝난 뒤에만 원장에 조정 줄로 반영된다(수량 직접 수정 없음).

const maxCountReason = 100

// 실사 표 상태 전이 표: 표에 없는 전이는 전부 거부한다(전이는 이 한 곳에서만 정의한다).
type countEvent string

const (
	cevSubmit  countEvent = "submit"   // 조정 전표 상신
	cevNoDiff  countEvent = "no_diff"  // 차이 없음 종료
	cevApprove countEvent = "approve"  // 최종 승인(원장 반영)
	cevReject  countEvent = "reject"   // 반려
	cevReset   countEvent = "reset"    // 상신 취소·회수로 조정 전표가 임시저장으로 돌아감
	cevDiscard countEvent = "discard"  // 폐기
	cevEdit    countEvent = "edit"     // 줄 수정·전산 수량 새로 고침
	cevCreate  countEvent = "creation" // 만들기(상태 없음 → draft)
)

var countTransitions = map[string]map[countEvent]string{
	"":                    {cevCreate: domain.CountDraft},
	domain.CountDraft:     {cevEdit: domain.CountDraft, cevNoDiff: domain.CountNoDiff, cevSubmit: domain.CountApproving, cevDiscard: domain.CountDiscarded},
	domain.CountRejected:  {cevEdit: domain.CountRejected, cevNoDiff: domain.CountNoDiff, cevSubmit: domain.CountApproving, cevDiscard: domain.CountDiscarded},
	domain.CountApproving: {cevApprove: domain.CountApplied, cevReject: domain.CountRejected, cevReset: domain.CountDraft},
}

func countTransition(from string, ev countEvent) (string, error) {
	if to, ok := countTransitions[from][ev]; ok {
		return to, nil
	}
	return "", fmt.Errorf("%w: %s 상태의 실사 표에는 할 수 없는 동작입니다", contract.ErrInvalidState, countStatusLabel(from))
}

func countStatusLabel(s string) string {
	switch s {
	case domain.CountDraft:
		return "작성 중"
	case domain.CountApproving:
		return "결재 중"
	case domain.CountApplied:
		return "반영 완료"
	case domain.CountRejected:
		return "반려"
	case domain.CountNoDiff:
		return "차이 없음 종료"
	case domain.CountDiscarded:
		return "폐기"
	}
	return s
}

type sysFormKey struct{}

// withSystemForm 은 시스템이 만드는 양식 문서(재고 조정 전표)의 작성·상신을 허용하는 표시다. 일반 문서 API 는 이 표시 없이는 ADJ 문서를 만들지 못한다.
func withSystemForm(ctx context.Context) context.Context {
	return context.WithValue(ctx, sysFormKey{}, true)
}

// guardSystemForm 은 시스템 양식 문서를 일반 문서 API 로 만들거나 상신하는 것을 막는다.
func guardSystemForm(ctx context.Context, formCode string) error {
	if _, ok := docHookTable[formCode]; ok && ctx.Value(sysFormKey{}) == nil {
		if formCode == "JV" {
			return validation("전표 승인 요청서는 전표 화면에서 승인 요청을 해야 만들어집니다")
		}
		if formCode == "PAY" {
			return validation("지급 요청서는 지급 요청 화면에서 승인 요청을 해야 만들어집니다")
		}
		if formCode == "MCR" {
			return validation("월 마감 해제 요청서는 재고의 월 마감 화면에서 해제 요청을 해야 만들어집니다")
		}
		return validation("재고 조정 전표는 재고 실사 화면에서 조정 전표를 올려야 만들어집니다")
	}
	return nil
}

// StockCountService 는 재고 실사 서비스다.
type StockCountService struct {
	stockBase
	Counts repository.SQLStockCount
	Docs   *ApprovalDocService
}

var _ domain.StockCountService = (*StockCountService)(nil)

// NewStockCountService 는 SQLite 저장소로 실사 서비스를 만든다.
func NewStockCountService(b Base) *StockCountService {
	return &StockCountService{stockBase: stockBase{Base: b}, Docs: NewApprovalDocService(b)}
}

func (s *StockCountService) get(ctx context.Context, q contract.Tx, id int64) (domain.StockCount, int64, error) {
	return s.Counts.Count(ctx, q, id)
}

// Create 는 그 창고의 현재 재고로 실사 표를 만든다(전산 수량은 이 시점의 스냅샷).
func (s *StockCountService) Create(ctx context.Context, whID int64) (domain.StockCount, error) {
	p, err := s.authorize(ctx, contract.PermInventoryManage, "stock_count.create", audit.TargetStockCount)
	if err != nil {
		return domain.StockCount{}, err
	}
	var out domain.StockCount
	err = s.write(ctx, p, "stock_count.create", audit.TargetStockCount, 0, func(tx contract.Tx) error {
		wh, err := s.activeWarehouse(ctx, tx, whID, "실사")
		if err != nil {
			return err
		}
		var open string
		if e := tx.QueryRowContext(ctx, `SELECT no FROM stock_counts WHERE warehouse_id = ? AND status IN ('draft','approving','rejected') LIMIT 1`, wh.ID).Scan(&open); e == nil {
			return fmt.Errorf("%w: 이 창고에 진행 중인 실사가 있습니다: %s", contract.ErrInvalidState, open)
		}
		if _, err := countTransition("", cevCreate); err != nil {
			return err
		}
		now := s.now()
		if err := s.assertMonthOpen(ctx, tx, string(contract.DayOf(now))); err != nil {
			return err
		}
		no, err := s.Stock.NextNo(ctx, tx, "SC", now.Year())
		if err != nil {
			return err
		}
		snap, err := s.Counts.LedgerMaxID(ctx, tx)
		if err != nil {
			return err
		}
		id, err := s.Counts.InsertCount(ctx, tx, no, wh.ID, string(contract.DayOf(now)), p.UserID, snap, now)
		if err != nil {
			return err
		}
		lines, err := s.Counts.SnapshotQty(ctx, tx, wh.ID)
		if err != nil {
			return err
		}
		for _, l := range lines {
			if err := s.Counts.InsertLine(ctx, tx, id, l); err != nil {
				return err
			}
		}
		if out, _, err = s.get(ctx, tx, id); err != nil {
			return err
		}
		return s.record(ctx, tx, p, "stock_count.create", audit.TargetStockCount, id,
			audit.DiffItem{Key: "doc_no", New: no}, audit.DiffItem{Key: "warehouse", New: wh.Name}, audit.DiffItem{Key: "base_date", New: out.BaseDate},
			audit.DiffItem{Key: "lines", New: fmt.Sprintf("%d줄", len(lines))})
	})
	return out, err
}

func (s *StockCountService) Get(ctx context.Context, id int64) (domain.StockCount, error) {
	if _, err := contract.Authorize(ctx, contract.PermInventoryManage); err != nil {
		return domain.StockCount{}, err
	}
	c, snap, err := s.get(ctx, s.DB, id)
	if err != nil {
		return c, err
	}
	if c.Status == domain.CountDraft || c.Status == domain.CountRejected || c.Status == domain.CountApproving {
		c.MovedSince, err = s.Counts.MovedSince(ctx, s.DB, c.ID, c.WarehouseID, snap)
	}
	return c, err
}

func (s *StockCountService) List(ctx context.Context, f domain.CountFilter) ([]domain.StockCount, error) {
	if _, err := contract.Authorize(ctx, contract.PermInventoryManage); err != nil {
		return nil, err
	}
	out, err := s.Counts.Counts(ctx, s.DB, f)
	if out == nil {
		out = []domain.StockCount{}
	}
	return out, err
}

// editable 은 실사 표가 줄을 고칠 수 있는 상태인지 확인한다(작성 중·반려).
func editableCount(c domain.StockCount) error {
	_, err := countTransition(c.Status, cevEdit)
	return err
}

// Save 는 실제 수량과 차이 사유를 저장한다. 표에 없는 (품목, 로트)는 줄을 더한다(전산 수량은 그 창고의 지금 재고).
func (s *StockCountService) Save(ctx context.Context, id int64, in domain.CountSaveInput) (domain.StockCount, error) {
	p, err := s.authorize(ctx, contract.PermInventoryManage, "stock_count.update", audit.TargetStockCount)
	if err != nil {
		return domain.StockCount{}, err
	}
	if len(in.Lines) == 0 || len(in.Lines) > 500 {
		return domain.StockCount{}, validation("저장할 줄이 없거나 너무 많습니다")
	}
	var out domain.StockCount
	err = s.write(ctx, p, "stock_count.update", audit.TargetStockCount, id, func(tx contract.Tx) error {
		c, _, err := s.get(ctx, tx, id)
		if err != nil {
			return err
		}
		if err := editableCount(c); err != nil {
			return err
		}
		if err := s.assertMonthOpen(ctx, tx, c.BaseDate); err != nil { // 기준일이 마감된 달이면 수량 저장도 거부한다(B27 검증자 F)
			return err
		}
		for i, l := range in.Lines {
			if l.CountedQty != nil && (*l.CountedQty < 0 || *l.CountedQty > 1_000_000_000) {
				return validation("%d번째 줄: 실제 수량이 올바르지 않습니다", i+1)
			}
			l.Reason = strings.TrimSpace(l.Reason)
			if utf8.RuneCountInString(l.Reason) > maxCountReason {
				return validation("%d번째 줄: 차이 사유는 %d자 이내로 입력하세요", i+1, maxCountReason)
			}
			ok, err := s.Counts.UpdateLine(ctx, tx, id, l)
			if err != nil {
				return err
			}
			if ok {
				continue
			}
			// 표에 없는 줄: 새로 센 품목·로트를 더한다.
			item, err := s.Inv.GetItem(ctx, tx, l.ItemID)
			if errors.Is(err, contract.ErrNotFound) {
				return validation("%d번째 줄: 품목을 찾을 수 없습니다", i+1)
			}
			if err != nil {
				return err
			}
			if item.TrackLot != (l.LotID != 0) {
				return validation("%d번째 줄: 로트를 관리하는 품목은 로트를 골라야 하고, 아니면 로트를 지정할 수 없습니다(%s)", i+1, item.Name)
			}
			if l.LotID != 0 {
				if _, _, err := s.Stock.LotInfo(ctx, tx, item.ID, l.LotID); err != nil {
					return validation("%d번째 줄: 이 품목의 로트가 아닙니다", i+1)
				}
			}
			sys, err := s.Stock.WhLotQty(ctx, tx, c.WarehouseID, item.ID, l.LotID)
			if err != nil {
				return err
			}
			if err := s.Counts.InsertLine(ctx, tx, id, domain.StockCountLine{ItemID: item.ID, LotID: l.LotID, SystemQty: sys, CountedQty: l.CountedQty, Reason: l.Reason}); err != nil {
				return err
			}
		}
		if out, _, err = s.get(ctx, tx, id); err != nil {
			return err
		}
		return s.record(ctx, tx, p, "stock_count.update", audit.TargetStockCount, id,
			audit.DiffItem{Key: "doc_no", New: out.No}, audit.DiffItem{Key: "lines", New: fmt.Sprintf("%d줄 저장, 차이 %d줄", len(in.Lines), out.DiffLines)})
	})
	return out, err
}

// Refresh 는 전산 수량을 지금 재고로 다시 잡는다. 이미 입력한 실제 수량·사유는 같은 (품목, 로트)에 그대로 옮긴다.
func (s *StockCountService) Refresh(ctx context.Context, id int64) (domain.StockCount, error) {
	p, err := s.authorize(ctx, contract.PermInventoryManage, "stock_count.update", audit.TargetStockCount)
	if err != nil {
		return domain.StockCount{}, err
	}
	var out domain.StockCount
	err = s.write(ctx, p, "stock_count.update", audit.TargetStockCount, id, func(tx contract.Tx) error {
		c, _, err := s.get(ctx, tx, id)
		if err != nil {
			return err
		}
		if err := editableCount(c); err != nil {
			return err
		}
		if err := s.assertMonthOpen(ctx, tx, c.BaseDate); err != nil {
			return err
		}
		type key struct{ item, lot int64 }
		old := map[key]domain.StockCountLine{}
		for _, l := range c.Lines {
			old[key{l.ItemID, l.LotID}] = l
		}
		snap, err := s.Counts.LedgerMaxID(ctx, tx)
		if err != nil {
			return err
		}
		fresh, err := s.Counts.SnapshotQty(ctx, tx, c.WarehouseID)
		if err != nil {
			return err
		}
		seen := map[key]bool{}
		var lines []domain.StockCountLine
		for _, l := range fresh {
			k := key{l.ItemID, l.LotID}
			seen[k] = true
			if o, ok := old[k]; ok {
				l.CountedQty, l.Reason = o.CountedQty, o.Reason
			}
			lines = append(lines, l)
		}
		for k, o := range old { // 재고가 0 이 된 줄도 사용자가 더한 것이면 남긴다(전산 수량 0)
			if !seen[k] && (o.CountedQty != nil || o.Reason != "") {
				o.SystemQty = 0
				lines = append(lines, o)
			}
		}
		sort.Slice(lines, func(i, j int) bool {
			if lines[i].ItemID != lines[j].ItemID {
				return lines[i].ItemID < lines[j].ItemID
			}
			return lines[i].LotID < lines[j].LotID
		})
		if err := s.Counts.DeleteLines(ctx, tx, id); err != nil {
			return err
		}
		for _, l := range lines {
			if err := s.Counts.InsertLine(ctx, tx, id, l); err != nil {
				return err
			}
		}
		if err := s.Counts.SetSnapshot(ctx, tx, id, snap, s.now()); err != nil {
			return err
		}
		if out, _, err = s.get(ctx, tx, id); err != nil {
			return err
		}
		return s.record(ctx, tx, p, "stock_count.update", audit.TargetStockCount, id,
			audit.DiffItem{Key: "doc_no", New: out.No}, audit.DiffItem{Key: "lines", New: fmt.Sprintf("전산 수량 새로 고침 %d줄", len(lines))})
	})
	return out, err
}

func countSummary(c domain.StockCount) string {
	var parts []string
	for _, l := range c.Lines {
		if l.CountedQty == nil || l.Diff == 0 {
			continue
		}
		lot := ""
		if l.LotNo != "" {
			lot = "(" + l.LotNo + ")"
		}
		reason := ""
		if l.Reason != "" {
			reason = " - " + l.Reason
		}
		parts = append(parts, fmt.Sprintf("%s%s: 전산 %s → 실제 %s (차이 %+d)%s", l.ItemName, lot, formatWon(l.SystemQty), formatWon(*l.CountedQty), l.Diff, reason))
	}
	out := strings.Join(parts, "\n")
	if r := []rune(out); len(r) > 4900 {
		out = string(r[:4900]) + "…"
	}
	return out
}

// Submit 은 차이가 없으면 실사를 종료하고, 있으면 조정 전표(양식 문서 ADJ)를 만들어 상신한다. 상신 때 기준일 이후 변동은 경고만 한다.
func (s *StockCountService) Submit(ctx context.Context, id int64) (domain.StockCount, error) {
	p, err := s.authorize(ctx, contract.PermInventoryManage, "stock_count.submit", audit.TargetStockCount)
	if err != nil {
		return domain.StockCount{}, err
	}
	c, snap, err := s.get(ctx, s.DB, id)
	if err != nil {
		return c, err
	}
	if _, err := countTransition(c.Status, cevSubmit); err != nil {
		return c, err
	}
	if err := s.assertMonthOpen(ctx, s.DB, c.BaseDate); err != nil { // 기준일이 마감된 달이면 상신할 수 없다(B27)
		s.recordDenied(ctx, p, "stock_count.submit", audit.TargetStockCount, id, "마감된 월 "+c.BaseDate[:7])
		return c, err
	}
	if c.Uncounted > 0 {
		return c, validation("아직 실제 수량을 입력하지 않은 줄이 %d개 있습니다", c.Uncounted)
	}
	if c.DiffLines == 0 {
		to, err := countTransition(c.Status, cevNoDiff)
		if err != nil {
			return c, err
		}
		var out domain.StockCount
		err = s.write(ctx, p, "stock_count.submit", audit.TargetStockCount, id, func(tx contract.Tx) error {
			if err := s.Counts.SetStatus(ctx, tx, id, to, 0, "", s.now()); err != nil {
				return err
			}
			if out, _, err = s.get(ctx, tx, id); err != nil {
				return err
			}
			return s.record(ctx, tx, p, "stock_count.submit", audit.TargetStockCount, id,
				audit.DiffItem{Key: "doc_no", New: c.No}, audit.DiffItem{Key: "diff_lines", New: "0"})
		})
		return out, err
	}
	for _, l := range c.Lines {
		if l.CountedQty != nil && l.Diff != 0 && strings.TrimSpace(l.Reason) == "" {
			return c, validation("차이가 있는 줄에는 차이 사유를 입력해야 합니다: %s", l.ItemName)
		}
	}
	moved, err := s.Counts.MovedSince(ctx, s.DB, c.ID, c.WarehouseID, snap)
	if err != nil {
		return c, err
	}
	// 조정 전표 만들기·상신은 양식 문서 서비스가 한다(권한 draft:create, 결재선 구매팀장 → 재무). 실사 표와 문서의 연결은 상신 전에 먼저 남긴다.
	dctx := withSystemForm(ctx)
	fields := map[string]any{"count_no": c.No, "warehouse": c.WarehouseName, "base_date": c.BaseDate, "summary": countSummary(c)}
	title := fmt.Sprintf("재고 실사 조정 %s (%s)", c.No, c.WarehouseName)
	var doc domain.PurchaseRequest
	if c.DocID == 0 {
		doc, err = s.Docs.CreateDraft(dctx, domain.DocDraftInput{FormCode: "ADJ", Title: title, Fields: fields})
	} else {
		doc, err = s.Docs.UpdateDraft(dctx, c.DocID, domain.DocDraftInput{FormCode: "ADJ", Title: title, Fields: fields})
	}
	if err != nil {
		return c, err
	}
	if err := s.write(ctx, p, "stock_count.submit", audit.TargetStockCount, id, func(tx contract.Tx) error {
		return s.Counts.SetStatus(ctx, tx, id, c.Status, doc.ID, c.RejectReason, s.now())
	}); err != nil {
		return c, err
	}
	submitted, err := s.Docs.Submit(dctx, doc.ID)
	if err != nil {
		return c, err
	}
	var out domain.StockCount
	err = s.write(ctx, p, "stock_count.submit", audit.TargetStockCount, id, func(tx contract.Tx) error {
		to, err := countTransition(c.Status, cevSubmit)
		if err != nil {
			return err
		}
		if err := s.Counts.SetStatus(ctx, tx, id, to, doc.ID, "", s.now()); err != nil {
			return err
		}
		if out, _, err = s.get(ctx, tx, id); err != nil {
			return err
		}
		out.MovedSince = moved
		return s.record(ctx, tx, p, "stock_count.submit", audit.TargetStockCount, id,
			audit.DiffItem{Key: "doc_no", New: c.No}, audit.DiffItem{Key: "adjust_doc_no", New: submitted.No},
			audit.DiffItem{Key: "diff_lines", New: fmt.Sprint(c.DiffLines)}, audit.DiffItem{Key: "moved_since", New: fmt.Sprint(moved)})
	})
	return out, err
}

// Discard 는 작성 중인 실사 표를 폐기한다(상신 전만, 작성자 본인).
func (s *StockCountService) Discard(ctx context.Context, id int64) error {
	p, err := s.authorize(ctx, contract.PermInventoryManage, "stock_count.discard", audit.TargetStockCount)
	if err != nil {
		return err
	}
	return s.write(ctx, p, "stock_count.discard", audit.TargetStockCount, id, func(tx contract.Tx) error {
		c, _, err := s.get(ctx, tx, id)
		if err != nil {
			return err
		}
		if c.CreatedByName == "" {
			return contract.ErrNotFound
		}
		to, err := countTransition(c.Status, cevDiscard)
		if err != nil {
			return err
		}
		var by int64
		if err := tx.QueryRowContext(ctx, `SELECT created_by FROM stock_counts WHERE id = ?`, id).Scan(&by); err != nil {
			return err
		}
		if by != p.UserID {
			return fmt.Errorf("%w: 실사 표는 만든 사람만 폐기할 수 있습니다", contract.ErrForbidden)
		}
		if err := s.Counts.SetStatus(ctx, tx, id, to, 0, "", s.now()); err != nil {
			return err
		}
		return s.record(ctx, tx, p, "stock_count.discard", audit.TargetStockCount, id, audit.DiffItem{Key: "doc_no", New: c.No})
	})
}
