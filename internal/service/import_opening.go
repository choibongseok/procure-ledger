package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/repository"
)

// 기초 재고·미지급 잔액(B35, 3.6~3.8). 수량과 채무 잔액은 직접 고치지 않는다.
//   - 기초 재고: 원장 단일 쓰기 함수(AppendLedger)에 출처 opening_import 로만 들어가고(장부일 = 기준일, 이동평균 평가 줄이 같이 생긴다),
//     같은 묶음에서 기초 전표(재고자산 / 기초 이월) 임시 한 건이 생긴다. 전표 차변이 평가 줄 합과 다르면 전체가 취소된다.
//   - 미지급 잔액: 기초 전표(기초 이월 / 외상매입금 거래처 줄)로만 들어가고, 승인되면 채무 항목이 원 시스템의 발생일·만기일로 생긴다.
//   - 한 번 반영한 키는 다시 올려도 바뀌지 않는다(같은 값은 동일, 다른 값은 오류: 정정은 재고 실사 조정이나 전표 역분개로 한다).
//   - 기준일이 속한 달이 마감되었으면 반영할 수 없다(미리보기와 반영 두 시점 모두).

// batchRefID 는 묶음 번호(IMP-YYYY-NNNN)를 원장 출처 번호(YYYY*1,000,000 + NNNN)로 바꾼다. 형식이 다르면 0.
func batchRefID(no string) int64 {
	var y, n int64
	if _, err := fmt.Sscanf(no, "IMP-%d-%d", &y, &n); err != nil {
		return 0
	}
	return y*1_000_000 + n
}

var reBizOnly = regexp.MustCompile(`^[0-9-]+$`)

// openingRules 는 기초 전표가 쓰는 계정이다. 중지되었거나 없으면 오류로 알린다(E13).
type openingRules struct{ debitStock, equity, payable domain.Account }

func openingAccounts(ctx context.Context, q contract.Tx, ledger repository.SQLLedger, needStock, needPayable bool) (openingRules, error) {
	var r openingRules
	get := func(key, label string) (domain.Account, error) {
		a, found, err := ledger.RuleAccount(ctx, q, key)
		if err != nil {
			return a, err
		}
		if !found || !a.Active {
			return a, validation("기초 전표 규칙 계정을 쓸 수 없습니다(%s). 계정과목 화면의 자동 전표 규칙에서 사용 중인 계정으로 바꾸세요.", label)
		}
		return a, nil
	}
	var err error
	if r.equity, err = get("opening.equity", "기초 이월 이익잉여금"); err != nil {
		return r, err
	}
	if needStock {
		if r.debitStock, err = get("purchase.inventory", "재고자산"); err != nil {
			return r, err
		}
	}
	if needPayable {
		if r.payable, err = get("purchase.payable", "외상매입금"); err != nil {
			return r, err
		}
	}
	return r, nil
}

// ---- ⑥ 기초 재고 ----

// ImportOpeningStockHandler 는 기초 재고 어댑터다. 키는 (SKU, 창고 코드, 로트 번호).
type ImportOpeningStockHandler struct {
	Base
	Imp      repository.SQLImport
	Inv      repository.SQLInventory
	Stock    repository.SQLStock
	Ledger   repository.SQLLedger
	Journals *JournalService
}

var _ domain.ImportTargetHandler = (*ImportOpeningStockHandler)(nil)

func NewImportOpeningStock(b Base, j *JournalService) *ImportOpeningStockHandler {
	return &ImportOpeningStockHandler{Base: b, Journals: j}
}

func (h *ImportOpeningStockHandler) Target() string { return domain.ImportOpeningStock }

func baseDateOK(in domain.ImportPlanInput) error {
	if !validDate(in.BaseDate) {
		return validation("기준일은 YYYY-MM-DD 형식으로 입력하세요.")
	}
	return nil
}

func lotKeyOf(sku, wh, lot string) string {
	k := sku + " · " + strings.ToUpper(wh)
	if lot != "" {
		k += " · " + lot
	}
	return k
}

func (h *ImportOpeningStockHandler) Plan(ctx context.Context, q contract.Tx, rows []domain.ImportRow, in domain.ImportPlanInput) (domain.ImportPlan, error) {
	var plan domain.ImportPlan
	if err := baseDateOK(in); err != nil {
		return plan, err
	}
	if err := h.assertMonthOpen(ctx, q, in.BaseDate); err != nil {
		h.noteClosedBaseDate(ctx, q, in, err)
		return plan, err
	}
	if _, err := openingAccounts(ctx, q, h.Ledger, true, false); err != nil {
		return plan, err
	}
	whs, err := h.Stock.Warehouses(ctx, q)
	if err != nil {
		return plan, err
	}
	// 큰 파일에서 행마다 조회하지 않도록 품목·로트·기존 기초 입고를 한 번에 읽는다(N+1 제거).
	items, err := h.Imp.ItemsAll(ctx, q)
	if err != nil {
		return plan, err
	}
	lotIDs, err := h.Imp.LotsAll(ctx, q)
	if err != nil {
		return plan, err
	}
	opened, err := h.Imp.OpeningStockAll(ctx, q)
	if err != nil {
		return plan, err
	}
	whByCode := map[string]*domain.Warehouse{}
	for i := range whs {
		whByCode[strings.ToUpper(whs[i].Code)] = &whs[i]
	}
	seen := map[string]int{} // 파일 안 같은 (품목, 창고, 로트): 입력 문자열이 아니라 확정된 번호로 판정한다
	var amount, qtySum int64
	skus := map[string]bool{}
	perSKU := map[string]int64{}
	safety := map[string]int64{}
	for _, r := range rows {
		sku, whCode, lot := r.Cells["sku"], r.Cells["warehouse"], r.Cells["lot"]
		qty, _ := strconv.ParseInt(r.Cells["qty"], 10, 64)
		cost, _ := strconv.ParseInt(r.Cells["unit_cost"], 10, 64)
		key := lotKeyOf(sku, whCode, lot)
		it, found := items[strings.ToLower(sku)]
		wh := whByCode[strings.ToUpper(whCode)]
		switch {
		case !found:
			plan.Verdicts = append(plan.Verdicts, verdictOf(r, key, domain.ImportVerdictError, "이 SKU 는 아직 등록되지 않았습니다. 품목 가져오기를 먼저 하세요.", nil))
			continue
		case !it.Active:
			plan.Verdicts = append(plan.Verdicts, verdictOf(r, key, domain.ImportVerdictError, "사용이 중지된 품목입니다.", nil))
			continue
		case wh == nil:
			plan.Verdicts = append(plan.Verdicts, verdictOf(r, key, domain.ImportVerdictError, "이 창고 코드는 등록되지 않았습니다. 창고 가져오기를 먼저 하세요.", nil))
			continue
		case !wh.Active:
			plan.Verdicts = append(plan.Verdicts, verdictOf(r, key, domain.ImportVerdictError, "사용이 중지된 창고입니다.", nil))
			continue
		case it.TrackLot && lot == "":
			plan.Verdicts = append(plan.Verdicts, verdictOf(r, key, domain.ImportVerdictError, "로트 관리 품목은 로트 번호가 필요합니다.", nil))
			continue
		case !it.TrackLot && (lot != "" || r.Cells["expiry"] != ""):
			plan.Verdicts = append(plan.Verdicts, verdictOf(r, key, domain.ImportVerdictError, "로트 관리 품목이 아니면 로트 번호·유효기한을 비워 두세요.", nil))
			continue
		}
		var lotID int64
		if lot != "" {
			lotID = lotIDs[repository.LotKey(it.ID, lot)]
		}
		okey := repository.OpeningKey{Item: it.ID, Warehouse: wh.ID, Lot: lotID}
		dupKey := fmt.Sprintf("%d|%d|%s", it.ID, wh.ID, lot)
		if first, dup := seen[dupKey]; dup {
			plan.Verdicts = append(plan.Verdicts, verdictOf(r, key, domain.ImportVerdictError, fmt.Sprintf("파일 안에 같은 품목·창고·로트가 두 번 있습니다(%d번째 줄과 같은 대상).", first), nil))
			continue
		}
		seen[dupKey] = r.RowNo
		cur, exists := opened[okey]
		curQty, curCost := cur.Qty, cur.UnitCost
		if exists {
			if curQty == qty && curCost == cost {
				plan.Verdicts = append(plan.Verdicts, verdictOf(r, key, domain.ImportVerdictSame, "", nil))
			} else {
				plan.Verdicts = append(plan.Verdicts, verdictOf(r, key, domain.ImportVerdictError,
					fmt.Sprintf("이미 기초로 반영된 값과 다릅니다(수량 %d → 파일 %d). 정정은 재고 실사 조정이나 전표 역분개로 하세요.", curQty, qty), nil))
			}
			continue
		}
		v := verdictOf(r, key, domain.ImportVerdictAdd, "", nil)
		if cost == 0 {
			v.Warn = "단가가 0원이라 금액 0원으로 평가됩니다."
		}
		plan.Verdicts = append(plan.Verdicts, v)
		amount += qty * cost
		qtySum += qty
		skus[sku] = true
		perSKU[strings.ToLower(sku)] += qty
		safety[strings.ToLower(sku)] = it.SafetyStock
	}
	for i, v := range plan.Verdicts {
		if v.Verdict != domain.ImportVerdictAdd || v.Warn != "" {
			continue
		}
		low := strings.ToLower(rows[i].Cells["sku"])
		if s := safety[low]; s > 0 && perSKU[low] < s {
			plan.Verdicts[i].Warn = "기초 수량이 안전재고보다 적습니다."
		}
	}
	if len(skus) > 0 {
		plan.Summary = []string{fmt.Sprintf("품목 %d종·수량 합 %s·재고 금액 합 %s", len(skus), formatWon(qtySum), wonText(amount))}
	}
	return plan, nil
}

func (h *ImportOpeningStockHandler) Apply(ctx context.Context, tx contract.Tx, p contract.Principal, rows []domain.ImportRow, plan []domain.ImportRowVerdict, in domain.ImportPlanInput) (domain.ImportApplyOutcome, error) {
	var out domain.ImportApplyOutcome
	if err := baseDateOK(in); err != nil {
		return out, err
	}
	if err := h.assertMonthOpen(ctx, tx, in.BaseDate); err != nil {
		return out, err
	}
	rules, err := openingAccounts(ctx, tx, h.Ledger, true, false)
	if err != nil {
		return out, err
	}
	ref := batchRefID(in.BatchNo)
	whs, err := h.Stock.Warehouses(ctx, tx)
	if err != nil {
		return out, err
	}
	items, err := h.Imp.ItemsAll(ctx, tx)
	if err != nil {
		return out, err
	}
	whByCode := map[string]int64{}
	for _, w := range whs {
		whByCode[strings.ToUpper(w.Code)] = w.ID
	}
	lotCache := map[string]int64{}
	var total int64
	count := 0
	for i, r := range rows {
		if plan[i].Verdict != domain.ImportVerdictAdd {
			continue
		}
		qty, _ := strconv.ParseInt(r.Cells["qty"], 10, 64)
		cost, _ := strconv.ParseInt(r.Cells["unit_cost"], 10, 64)
		it, found := items[strings.ToLower(r.Cells["sku"])]
		if !found {
			return out, fmt.Errorf("%w: 미리보기 이후 품목이 바뀌었습니다. 다시 미리보기를 하세요.", contract.ErrInvalidState)
		}
		whID := whByCode[strings.ToUpper(r.Cells["warehouse"])]
		if whID == 0 {
			return out, fmt.Errorf("%w: 미리보기 이후 창고가 바뀌었습니다. 다시 미리보기를 하세요.", contract.ErrInvalidState)
		}
		var lotID int64
		if lot := r.Cells["lot"]; lot != "" {
			ck := repository.LotKey(it.ID, lot)
			if id, ok := lotCache[ck]; ok {
				lotID = id
			} else {
				if lotID, err = h.Inv.FindOrCreateLot(ctx, tx, it.ID, lot, r.Cells["expiry"]); err != nil {
					return out, err
				}
				lotCache[ck] = lotID
			}
		}
		if err := h.Inv.AppendLedger(ctx, tx, repository.LedgerEntry{WarehouseID: whID, ItemID: it.ID, LotID: lotID, Qty: qty, Reason: "receipt",
			RefType: repository.OpeningRef, RefID: ref, ActorID: p.UserID, At: h.now(), BookDate: in.BaseDate, UnitCost: cost}); err != nil {
			return out, err
		}
		total += qty * cost
		count++
	}
	if count == 0 {
		return out, nil
	}
	// 검산: 평가 줄 금액 합과 기초 전표 차변이 같아야 한다(어긋나면 전체 취소).
	if sum, err := h.Imp.OpeningValuationSum(ctx, tx, ref); err != nil {
		return out, err
	} else if sum != total {
		return out, fmt.Errorf("%w: 기초 재고 평가 금액이 전표 금액과 다릅니다. 반영을 취소했습니다.", contract.ErrInvalidState)
	}
	if total > 0 {
		j, err := h.Journals.CreateOpening(ctx, tx, p, domain.JournalInput{PostingDate: in.BaseDate, TradeDate: in.BaseDate, Memo: "기초 재고 가져오기 " + in.BatchNo,
			Lines: []domain.JournalLineInput{{AccountCode: rules.debitStock.Code, Side: "D", Amount: total}, {AccountCode: rules.equity.Code, Side: "C", Amount: total}}}, in.BatchNo)
		if err != nil {
			return out, err
		}
		out.JournalNos = append(out.JournalNos, j.No)
	}
	if err := h.Audit.Record(ctx, tx, audit.Entry{Actor: p, Action: "stock.opening_import", TargetType: audit.TargetItem, TargetID: in.BatchNo, Result: audit.ResultSuccess,
		Detail: "기초 재고 입고(기초 데이터 가져오기 " + in.BatchNo + ")", Diff: []audit.DiffItem{{Key: "rows", New: strconv.Itoa(count)}, {Key: "base_date", New: in.BaseDate}}}); err != nil {
		return out, err
	}
	return out, nil
}

// ---- ⑦ 미지급 잔액 ----

// ImportOpeningPayableHandler 는 미지급 잔액 어댑터다. 키는 (거래처, 문서 번호).
type ImportOpeningPayableHandler struct {
	Base
	Imp      repository.SQLImport
	Ledger   repository.SQLLedger
	Journals *JournalService
}

var _ domain.ImportTargetHandler = (*ImportOpeningPayableHandler)(nil)

func NewImportOpeningPayable(b Base, j *JournalService) *ImportOpeningPayableHandler {
	return &ImportOpeningPayableHandler{Base: b, Journals: j}
}

func (h *ImportOpeningPayableHandler) Target() string { return domain.ImportOpeningPayable }

// vendorOf 는 거래처 칸(사업자번호 또는 이름)에 해당하는 거래처다.
func vendorOf(all *vendorIndex, cell string) (*repository.VendorRow, bool) {
	if reBizOnly.MatchString(cell) && len(vendorDigits(cell)) == 10 {
		return all.match(vendorDigits(cell), "")
	}
	return all.match("", cell)
}

func (h *ImportOpeningPayableHandler) dueOf(ctx context.Context, q contract.Tx, v *repository.VendorRow, trade, due string) (string, error) {
	if due != "" {
		return due, nil
	}
	days, err := h.Ledger.VendorPaymentDays(ctx, q, v.ID)
	if err != nil {
		return "", err
	}
	return parseDay(trade).AddDate(0, 0, days).Format("2006-01-02"), nil
}

func (h *ImportOpeningPayableHandler) Plan(ctx context.Context, q contract.Tx, rows []domain.ImportRow, in domain.ImportPlanInput) (domain.ImportPlan, error) {
	var plan domain.ImportPlan
	if err := baseDateOK(in); err != nil {
		return plan, err
	}
	if err := h.assertMonthOpen(ctx, q, in.BaseDate); err != nil {
		h.noteClosedBaseDate(ctx, q, in, err)
		return plan, err
	}
	if _, err := openingAccounts(ctx, q, h.Ledger, false, true); err != nil {
		return plan, err
	}
	rowsAll, err := h.Imp.VendorsAll(ctx, q)
	if err != nil {
		return plan, err
	}
	all := newVendorIndex(rowsAll)
	opened, err := h.Imp.OpeningPayablesAll(ctx, q)
	if err != nil {
		return plan, err
	}
	seen := map[string]int{} // 파일 안 같은 (거래처, 문서 번호): 사업자번호로 쓰든 이름으로 쓰든 확정된 거래처 번호로 판정한다
	var amount int64
	items := 0
	vendors := map[int64]bool{}
	for _, r := range rows {
		docNo := r.Cells["doc_no"]
		amt, _ := strconv.ParseInt(r.Cells["amount"], 10, 64)
		key := r.Cells["vendor"] + " · " + docNo
		if reBizOnly.MatchString(r.Cells["vendor"]) {
			key = "사업자번호 " + maskTail(vendorDigits(r.Cells["vendor"])) + " · " + docNo
		}
		v, amb := vendorOf(all, r.Cells["vendor"])
		switch {
		case amb:
			plan.Verdicts = append(plan.Verdicts, verdictOf(r, key, domain.ImportVerdictError, "같은 이름의 거래처가 여럿이라 정할 수 없습니다. 사업자번호를 쓰세요.", nil))
			continue
		case v == nil:
			plan.Verdicts = append(plan.Verdicts, verdictOf(r, key, domain.ImportVerdictError, "등록되지 않은 거래처입니다. 거래처 가져오기를 먼저 하세요.", nil))
			continue
		}
		dupKey := fmt.Sprintf("%d|%s", v.ID, docNo)
		if first, dup := seen[dupKey]; dup {
			plan.Verdicts = append(plan.Verdicts, verdictOf(r, key, domain.ImportVerdictError, fmt.Sprintf("파일 안에 같은 거래처·문서 번호가 두 번 있습니다(%d번째 줄과 같은 항목).", first), nil))
			continue
		}
		seen[dupKey] = r.RowNo
		if cur, exists := opened[dupKey]; exists {
			if cur == amt {
				plan.Verdicts = append(plan.Verdicts, verdictOf(r, key, domain.ImportVerdictSame, "", nil))
			} else {
				plan.Verdicts = append(plan.Verdicts, verdictOf(r, key, domain.ImportVerdictError,
					fmt.Sprintf("이미 기초로 반영된 값과 다릅니다(잔액 %s → 파일 %s). 정정은 전표 역분개로 하세요.", wonText(cur), wonText(amt)), nil))
			}
			continue
		}
		due, err := h.dueOf(ctx, q, v, r.Cells["trade_date"], r.Cells["due_date"])
		if err != nil {
			return plan, err
		}
		vd := verdictOf(r, key, domain.ImportVerdictAdd, "", nil)
		if due < in.BaseDate {
			vd.Warn = "만기가 기준일 이전이라 이미 지난 채무입니다."
		}
		plan.Verdicts = append(plan.Verdicts, vd)
		amount += amt
		items++
		vendors[v.ID] = true
	}
	if items > 0 {
		plan.Summary = []string{fmt.Sprintf("거래처 %d곳·항목 %d건·잔액 합 %s", len(vendors), items, wonText(amount))}
	}
	return plan, nil
}

// openingJournalChunk 는 기초 전표 한 건에 넣는 외상매입금 줄의 상한이다(차변 한 줄을 빼고 전표 줄 상한 200).
const openingJournalChunk = maxJournalLines - 1

func (h *ImportOpeningPayableHandler) Apply(ctx context.Context, tx contract.Tx, p contract.Principal, rows []domain.ImportRow, plan []domain.ImportRowVerdict, in domain.ImportPlanInput) (domain.ImportApplyOutcome, error) {
	var out domain.ImportApplyOutcome
	if err := baseDateOK(in); err != nil {
		return out, err
	}
	if err := h.assertMonthOpen(ctx, tx, in.BaseDate); err != nil {
		return out, err
	}
	rules, err := openingAccounts(ctx, tx, h.Ledger, false, true)
	if err != nil {
		return out, err
	}
	rowsAll, err := h.Imp.VendorsAll(ctx, tx)
	if err != nil {
		return out, err
	}
	all := newVendorIndex(rowsAll)
	type item struct {
		vendor     *repository.VendorRow
		docNo      string
		amount     int64
		trade, due string
	}
	var items []item
	for i, r := range rows {
		if plan[i].Verdict != domain.ImportVerdictAdd {
			continue
		}
		v, amb := vendorOf(all, r.Cells["vendor"])
		if v == nil || amb {
			return out, fmt.Errorf("%w: 미리보기 이후 거래처가 바뀌었습니다. 다시 미리보기를 하세요.", contract.ErrInvalidState)
		}
		amt, _ := strconv.ParseInt(r.Cells["amount"], 10, 64)
		due, err := h.dueOf(ctx, tx, v, r.Cells["trade_date"], r.Cells["due_date"])
		if err != nil {
			return out, err
		}
		items = append(items, item{vendor: v, docNo: r.Cells["doc_no"], amount: amt, trade: r.Cells["trade_date"], due: due})
	}
	for start := 0; start < len(items); start += openingJournalChunk {
		end := start + openingJournalChunk
		if end > len(items) {
			end = len(items)
		}
		chunk := items[start:end]
		var sum int64
		lines := []domain.JournalLineInput{{AccountCode: rules.equity.Code, Side: "D"}}
		for _, it := range chunk {
			sum += it.amount
			lines = append(lines, domain.JournalLineInput{AccountCode: rules.payable.Code, Side: "C", Amount: it.amount, VendorID: it.vendor.ID, Memo: it.docNo})
		}
		lines[0].Amount = sum
		j, err := h.Journals.CreateOpening(ctx, tx, p, domain.JournalInput{PostingDate: in.BaseDate, TradeDate: in.BaseDate, Memo: "기초 미지급 잔액 가져오기 " + in.BatchNo, Lines: lines}, in.BatchNo)
		if err != nil {
			return out, err
		}
		ids, err := h.Imp.JournalLineIDs(ctx, tx, j.ID)
		if err != nil || len(ids) != len(lines) {
			return out, fmt.Errorf("기초 전표 줄을 읽지 못했습니다: %w", err)
		}
		for k, it := range chunk {
			if err := h.Imp.InsertOpeningPayableLine(ctx, tx, repository.OpeningPayableLine{LineID: ids[k+1], VendorID: it.vendor.ID, DocNo: it.docNo, TradeDate: it.trade, DueDate: it.due, BatchNo: in.BatchNo}, h.now()); err != nil {
				return out, err
			}
		}
		out.JournalNos = append(out.JournalNos, j.No)
	}
	return out, nil
}

// noteClosedBaseDate 는 기준일이 마감된 달이라 거부된 사실을 감사(import.denied_closed, denied)에 남긴다(명세 7절).
// 미리보기(읽기) 때만 남긴다: 트랜잭션 안(반영)에서는 오류로 롤백되므로 따로 남기지 않는다(반영은 미리보기를 통과한 뒤에만 도달한다).
func (b Base) noteClosedBaseDate(ctx context.Context, q contract.Tx, in domain.ImportPlanInput, err error) {
	var closed *repository.MonthClosedError
	if !errors.As(err, &closed) {
		return
	}
	if _, inTx := q.(*sql.Tx); inTx {
		return
	}
	p, ok := contract.PrincipalFrom(ctx)
	if !ok || len(in.BaseDate) < 7 {
		return
	}
	b.recordDenied(ctx, p, "import.denied_closed", audit.TargetImport, 0, "기준일 월 "+in.BaseDate[:7])
}
