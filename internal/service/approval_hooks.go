package service

import (
	"context"
	"fmt"
	"strings"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/repository"
)

// docHooks 는 시스템 양식 문서의 결재 결과에 따라 같은 트랜잭션에서 하는 후속 동작이다(B24 D11).
// 훅 목록은 docHookTable 한 곳에만 둔다(구매 품의서의 발주 생성이 하던 자리와 같다). 일반 양식에는 훅이 없다.
type docHooks struct {
	onApproved func(ctx context.Context, b Base, tx contract.Tx, p contract.Principal, rec repository.PRRecord) error
	onRejected func(ctx context.Context, b Base, tx contract.Tx, p contract.Principal, rec repository.PRRecord, reason string) error
	onReset    func(ctx context.Context, b Base, tx contract.Tx, p contract.Principal, rec repository.PRRecord) error // 상신 취소·회수로 임시저장이 됨
}

var docHookTable = map[string]docHooks{
	"ADJ":   {onApproved: applyStockCountHook, onRejected: rejectStockCountHook, onReset: resetStockCountHook},
	"JV":    {onApproved: approveJournalHook, onRejected: rejectJournalHook, onReset: resetJournalHook}, // B31: 전표 승인 요청서(승인 때 전표 승인·채무 항목, 반려·회수 때 전표를 임시로)
	"PAY":   {onApproved: approvePaymentHook, onRejected: rejectPaymentHook, onReset: resetPaymentHook}, // B32: 지급 요청서(승인 때 지급 건 승인·예약 유지, 반려·회수 때 예약 해제)
	"LEAVE": {onApproved: approveLeaveHook, onRejected: rejectLeaveHook, onReset: resetLeaveHook},       // B50: 휴가 신청서(승인 때 예약을 사용으로, 반려·회수 때 예약 복원)
	"MCR":   {onApproved: releaseMonthHook, onRejected: rejectReleaseHook},                              // B27: 월 마감 해제 요청서(상신 취소·회수는 달이 마감 그대로라 후속 동작이 없다)
}

// isSystemOnlyForm 은 시스템이 만드는 전용 양식(결재 훅이 있는 양식)인지다. 기안 시작 목록과 일반 양식 조회에서 뺀다.
func isSystemOnlyForm(code string) bool {
	_, ok := docHookTable[code]
	return ok
}

// DocHookForms 는 결재 훅이 있는 양식 코드 목록이다(훅이 한 곳에서만 늘어나는지 테스트가 고정한다).
func DocHookForms() []string {
	out := make([]string, 0, len(docHookTable))
	for k := range docHookTable {
		out = append(out, k)
	}
	return out
}

// runApprovedHook·runRejectedHook·runResetHook 은 해당 양식에 훅이 있을 때만 부른다.
func runApprovedHook(ctx context.Context, b Base, tx contract.Tx, p contract.Principal, rec repository.PRRecord) error {
	if h, ok := docHookTable[rec.FormCode]; ok && h.onApproved != nil {
		return h.onApproved(ctx, b, tx, p, rec)
	}
	if f, ok := postApprovalTable[rec.FormCode]; ok {
		return f(ctx, b, tx, p, rec)
	}
	return nil
}

// postApprovalTable 은 일반 기안으로 만드는 양식(시스템 전용이 아닌)이 최종 승인된 뒤 같은 트랜잭션에서 하는 후속 동작이다. docHookTable 과 달리 양식을 시스템 전용으로 만들지 않는다.
// B31: 지출 결의서(EXP)의 최종 승인이 임시 비용 전표를 만든다(실패는 승인을 막지 않고 전표 대기로 간다).
var postApprovalTable = map[string]func(ctx context.Context, b Base, tx contract.Tx, p contract.Principal, rec repository.PRRecord) error{
	"EXP": expenseJournalHook,
}

func runRejectedHook(ctx context.Context, b Base, tx contract.Tx, p contract.Principal, rec repository.PRRecord, reason string) error {
	if h, ok := docHookTable[rec.FormCode]; ok && h.onRejected != nil {
		return h.onRejected(ctx, b, tx, p, rec, reason)
	}
	return nil
}

func runResetHook(ctx context.Context, b Base, tx contract.Tx, p contract.Principal, rec repository.PRRecord) error {
	if h, ok := docHookTable[rec.FormCode]; ok && h.onReset != nil {
		return h.onReset(ctx, b, tx, p, rec)
	}
	return nil
}

// applyStockCountHook 은 재고 조정 전표가 최종 승인되면 같은 트랜잭션에서 실사 차이를 원장에 조정 줄로 반영한다(D8: 실제 − 스냅샷 전산 수량).
// 반영 결과가 음수 재고가 되면 오류를 돌려 승인 전체가 취소된다(E9).
func applyStockCountHook(ctx context.Context, b Base, tx contract.Tx, p contract.Principal, rec repository.PRRecord) error {
	sb := stockBase{Base: b}
	counts := repository.SQLStockCount{}
	c, _, err := counts.CountByDoc(ctx, tx, rec.ID)
	if err != nil {
		return validation("실사 표와 연결되지 않은 재고 조정 전표는 승인할 수 없습니다")
	}
	if _, err := countTransition(c.Status, cevApprove); err != nil {
		return err
	}
	wh, err := sb.Stock.Warehouse(ctx, tx, c.WarehouseID)
	if err != nil {
		return err
	}
	var applied []string
	for _, l := range c.Lines {
		if l.CountedQty == nil || l.Diff == 0 {
			continue
		}
		item, err := sb.Inv.GetItem(ctx, tx, l.ItemID)
		if err != nil {
			return fmt.Errorf("%w: 조정할 품목을 찾을 수 없습니다(%s)", contract.ErrInvalidState, l.ItemName)
		}
		cl := checkedLine{stockLine: stockLine{l.ItemID, l.LotID, l.Diff}, item: item, lotNo: l.LotNo}
		if err := sb.ledgerAppend(ctx, tx, wh.ID, cl, l.Diff, "adjust", "stock_count", c.ID, p.UserID, wh.Name); err != nil {
			return fmt.Errorf("%w: 재고가 부족해 조정을 반영할 수 없습니다. 실사 표를 새로 고쳐 다시 올리세요. (%v)", contract.ErrInvalidState, err)
		}
		lot := ""
		if l.LotNo != "" {
			lot = "(" + l.LotNo + ")"
		}
		applied = append(applied, fmt.Sprintf("%s%s %+d", l.ItemName, lot, l.Diff))
	}
	if err := counts.SetStatus(ctx, tx, c.ID, domain.CountApplied, 0, "", b.now()); err != nil {
		return err
	}
	return b.record(ctx, tx, p, "stock_count.apply", audit.TargetStockCount, c.ID,
		audit.DiffItem{Key: "doc_no", New: c.No}, audit.DiffItem{Key: "adjust_doc_no", New: rec.No}, audit.DiffItem{Key: "lines", New: strings.Join(applied, ", ")})
}

// rejectStockCountHook 은 조정 전표가 반려되면 실사 표를 반려 상태로 바꾸고 사유를 남긴다.
func rejectStockCountHook(ctx context.Context, b Base, tx contract.Tx, p contract.Principal, rec repository.PRRecord, reason string) error {
	counts := repository.SQLStockCount{}
	c, _, err := counts.CountByDoc(ctx, tx, rec.ID)
	if err != nil {
		return nil // 연결된 실사 표가 없으면 할 일이 없다
	}
	if _, err := countTransition(c.Status, cevReject); err != nil {
		return err
	}
	if err := counts.SetStatus(ctx, tx, c.ID, domain.CountRejected, 0, reason, b.now()); err != nil {
		return err
	}
	return b.record(ctx, tx, p, "stock_count.reject", audit.TargetStockCount, c.ID, audit.DiffItem{Key: "doc_no", New: c.No}, audit.DiffItem{Key: "reason", New: reason})
}

// resetStockCountHook 은 조정 전표가 상신 취소·회수로 임시저장이 되면 실사 표를 작성 중으로 되돌린다.
func resetStockCountHook(ctx context.Context, b Base, tx contract.Tx, p contract.Principal, rec repository.PRRecord) error {
	counts := repository.SQLStockCount{}
	c, _, err := counts.CountByDoc(ctx, tx, rec.ID)
	if err != nil {
		return nil
	}
	if _, err := countTransition(c.Status, cevReset); err != nil {
		return err
	}
	return counts.SetStatus(ctx, tx, c.ID, domain.CountDraft, 0, "", b.now())
}
