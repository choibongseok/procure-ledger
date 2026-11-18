package service

import (
	"context"
	"errors"
	"fmt"
	"github.com/choibongseok/procure-ledger/internal/dbutil"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/repository"
)

// 재고 기능 깊이(B24,): 창고·출고·창고 이동 서비스.
// 재고를 바꾸는 쓰기는 모두 SQLInventory.InsertLedgerWH 한 함수를 거치고, 검증(창고·로트·음수 재고)은 이 파일의 ledgerAppend 가 한 곳에서 한다.

const (
	maxStockLines = 50
	maxMemoRunes  = 200
)

var warehouseCode = regexp.MustCompile(`^[A-Z0-9][A-Z0-9-]{1,19}$`)

// stockBase 는 세 서비스가 같이 쓰는 저장소 묶음이다.
type stockBase struct {
	Base
	Stock repository.SQLStock
	Inv   repository.SQLInventory
}

// activeWarehouse 는 사용 중인 창고를 확인한다.
func (s stockBase) activeWarehouse(ctx context.Context, q contract.Tx, id int64, label string) (domain.Warehouse, error) {
	w, err := s.Stock.Warehouse(ctx, q, id)
	if errors.Is(err, contract.ErrNotFound) {
		return w, validation("%s 창고를 찾을 수 없습니다", label)
	}
	if err != nil {
		return w, err
	}
	if !w.Active {
		return w, validation("사용 중지된 창고입니다: %s", w.Name)
	}
	return w, nil
}

// dayOrToday 는 YYYY-MM-DD 날짜를 검증한다(비면 오늘, 미래 불가).
func (s stockBase) dayOrToday(v, label string) (string, error) {
	today := string(contract.DayOf(s.now()))
	v = strings.TrimSpace(v)
	if v == "" {
		return today, nil
	}
	if _, err := time.Parse("2006-01-02", v); err != nil {
		return "", validation("%s은(는) 연-월-일(예 2026-12-18) 형식으로 입력하세요", label)
	}
	if v > today {
		return "", validation("%s은(는) 오늘 이후일 수 없습니다", label)
	}
	return v, nil
}

type stockLine struct{ itemID, lotID, qty int64 }

// checkLines 는 출고·이동 줄의 공통 검증이다: 개수·수량·중복·품목·로트 규칙(로트 관리 품목은 로트 필수, 아니면 로트 금지)·유효기한.
// rejectExpired 가 true 면 유효기한이 지난 로트를 거부한다(출고). 줄마다 품목과 로트 정보를 돌려준다.
type checkedLine struct {
	stockLine
	item   domain.Item
	lotNo  string
	expiry string
}

func (s stockBase) checkLines(ctx context.Context, q contract.Tx, in []stockLine, rejectExpired bool) ([]checkedLine, error) {
	if len(in) == 0 || len(in) > maxStockLines {
		return nil, validation("줄은 1건 이상 %d건 이하여야 합니다", maxStockLines)
	}
	today := string(contract.DayOf(s.now()))
	seen := map[[2]int64]bool{}
	out := make([]checkedLine, 0, len(in))
	for i, l := range in {
		if l.qty <= 0 || l.qty > maxLineQty {
			return nil, validation("%d번째 줄: 수량이 올바르지 않습니다", i+1)
		}
		key := [2]int64{l.itemID, l.lotID}
		if seen[key] {
			return nil, validation("%d번째 줄: 같은 품목·로트가 중복되었습니다. 한 줄로 합치세요", i+1)
		}
		seen[key] = true
		item, err := s.Inv.GetItem(ctx, q, l.itemID)
		if errors.Is(err, contract.ErrNotFound) {
			return nil, validation("%d번째 줄: 품목을 찾을 수 없습니다", i+1)
		}
		if err != nil {
			return nil, err
		}
		cl := checkedLine{stockLine: l, item: item}
		if item.TrackLot {
			if l.lotID == 0 {
				return nil, validation("%d번째 줄: 로트를 선택해야 하는 품목입니다(%s)", i+1, item.Name)
			}
			no, exp, err := s.Stock.LotInfo(ctx, q, item.ID, l.lotID)
			if errors.Is(err, contract.ErrNotFound) {
				return nil, validation("%d번째 줄: 이 품목의 로트가 아닙니다", i+1)
			}
			if err != nil {
				return nil, err
			}
			if rejectExpired && exp != "" && exp < today {
				return nil, validation("%d번째 줄: 유효기한이 지난 로트입니다(%s, %s). 선택할 수 없습니다", i+1, no, exp)
			}
			cl.lotNo, cl.expiry = no, exp
		} else if l.lotID != 0 {
			return nil, validation("%d번째 줄: 로트를 관리하지 않는 품목에는 로트를 지정할 수 없습니다(%s)", i+1, item.Name)
		}
		out = append(out, cl)
	}
	return out, nil
}

// ledgerAppend 는 재고를 바꾸는 쓰기의 유일한 입구다: 창고별 로트 재고를 트랜잭션 안에서 다시 읽어 음수 재고를 막고 원장에 한 줄을 더한다.
func (s stockBase) ledgerAppend(ctx context.Context, tx contract.Tx, whID int64, cl checkedLine, delta int64, reason, refType string, refID, actor int64, label string, bookDate ...string) error {
	if delta < 0 {
		// 같은 창고·품목의 동시 출고가 같은 잔량을 읽고 둘 다 통과하는 일을 막는다(PG, S5).
		if err := dbutil.AcquireKeyLock(ctx, tx, fmt.Sprintf("stock:%d:%d", whID, cl.itemID)); err != nil {
			return err
		}
		have, err := s.Stock.WhLotQty(ctx, tx, whID, cl.itemID, cl.lotID)
		if err != nil {
			return err
		}
		if have+delta < 0 {
			lot := ""
			if cl.lotNo != "" {
				lot = " 로트 " + cl.lotNo
			}
			return validation("재고가 부족합니다: %s%s (%s 현재 %s, 요청 %s)", cl.item.Name, lot, label, formatWon(have), formatWon(-delta))
		}
	}
	e := repository.LedgerEntry{WarehouseID: whID, ItemID: cl.itemID, LotID: cl.lotID, Qty: delta, Reason: reason, RefType: refType, RefID: refID, ActorID: actor, At: s.now()}
	if len(bookDate) > 0 {
		e.BookDate = bookDate[0] // 출고 전표는 출고일이 장부일이다(B27 D3)
	}
	day := e.BookDate
	if day == "" {
		day = string(contract.DayOf(e.At))
	}
	if err := s.assertMonthOpen(ctx, tx, day); err != nil {
		return err
	}
	return s.Inv.AppendLedger(ctx, tx, e)
}

func stockLineSummary(ls []checkedLine) string {
	parts := make([]string, len(ls))
	for i, l := range ls {
		lot := ""
		if l.lotNo != "" {
			lot = "(" + l.lotNo + ")"
		}
		parts[i] = fmt.Sprintf("%s%s %s", l.item.Name, lot, formatWon(l.qty))
	}
	return strings.Join(parts, ", ")
}

// ---------------------------------------------------------------- 창고

// WarehouseService 는 창고 마스터다(조회 inventory:read, 쓰기 inventory:manage).
type WarehouseService struct{ stockBase }

var _ domain.WarehouseService = (*WarehouseService)(nil)

// NewWarehouseService 는 SQLite 저장소로 창고 서비스를 만든다.
func NewWarehouseService(b Base) *WarehouseService {
	return &WarehouseService{stockBase{Base: b}}
}

func (s *WarehouseService) List(ctx context.Context) ([]domain.Warehouse, error) {
	if _, err := contract.Authorize(ctx, contract.PermInventoryRead); err != nil {
		return nil, err
	}
	out, err := s.Stock.Warehouses(ctx, s.DB)
	if out == nil {
		out = []domain.Warehouse{}
	}
	return out, err
}

func validWarehouse(in domain.WarehouseInput) (domain.WarehouseInput, error) {
	in.Code, in.Name, in.Location = strings.ToUpper(strings.TrimSpace(in.Code)), strings.TrimSpace(in.Name), strings.TrimSpace(in.Location)
	if !warehouseCode.MatchString(in.Code) {
		return in, validation("창고 코드는 영문 대문자·숫자·하이픈 2~20자여야 합니다(예: WH-03)")
	}
	if n := utf8.RuneCountInString(in.Name); n < 1 || n > 40 {
		return in, validation("창고 이름은 1~40자여야 합니다")
	}
	if utf8.RuneCountInString(in.Location) > 100 {
		return in, validation("위치는 100자 이내로 입력하세요")
	}
	return in, nil
}

func (s *WarehouseService) Save(ctx context.Context, in domain.WarehouseInput) (domain.Warehouse, error) {
	action := "warehouse.create"
	if in.ID != 0 {
		action = "warehouse.update"
	}
	p, err := s.authorize(ctx, contract.PermInventoryManage, action, audit.TargetWarehouse)
	if err != nil {
		return domain.Warehouse{}, err
	}
	in, err = validWarehouse(in)
	if err != nil {
		return domain.Warehouse{}, err
	}
	if ok, err := s.Stock.SiteUsable(ctx, s.DB, in.SiteID); err != nil {
		return domain.Warehouse{}, err
	} else if !ok {
		return domain.Warehouse{}, validation("사업장을 찾을 수 없거나 사용할 수 없습니다.")
	}
	var before domain.Warehouse
	if in.ID != 0 {
		if before, err = s.Stock.Warehouse(ctx, s.DB, in.ID); err != nil {
			return domain.Warehouse{}, err
		}
	}
	var out domain.Warehouse
	err = s.write(ctx, p, action, audit.TargetWarehouse, in.ID, func(tx contract.Tx) error {
		id := in.ID
		if id == 0 {
			if id, err = s.Stock.InsertWarehouse(ctx, tx, in, s.now()); err != nil {
				return err
			}
		} else if err := s.Stock.UpdateWarehouse(ctx, tx, in, s.now()); err != nil {
			return err
		}
		if out, err = s.Stock.Warehouse(ctx, tx, id); err != nil {
			return err
		}
		return s.record(ctx, tx, p, action, audit.TargetWarehouse, id,
			audit.DiffItem{Key: "code", Old: before.Code, New: out.Code}, audit.DiffItem{Key: "name", Old: before.Name, New: out.Name},
			audit.DiffItem{Key: "location", Old: before.Location, New: out.Location}, audit.DiffItem{Key: "site", Old: before.SiteName, New: out.SiteName})
	})
	if err != nil && errors.Is(err, contract.ErrValidation) && strings.Contains(err.Error(), "이미 등록된 값") {
		return domain.Warehouse{}, validation("이미 있는 창고 코드 또는 이름입니다")
	}
	return out, err
}

func (s *WarehouseService) SetActive(ctx context.Context, id int64, active bool) error {
	action := "warehouse.deactivate"
	if active {
		action = "warehouse.activate"
	}
	p, err := s.authorize(ctx, contract.PermInventoryManage, action, audit.TargetWarehouse)
	if err != nil {
		return err
	}
	w, err := s.Stock.Warehouse(ctx, s.DB, id)
	if err != nil {
		return err
	}
	if w.Active == active {
		return fmt.Errorf("%w: 이미 그 상태입니다", contract.ErrInvalidState)
	}
	return s.write(ctx, p, action, audit.TargetWarehouse, id, func(tx contract.Tx) error {
		if !active {
			if has, err := s.Stock.WarehouseHasStock(ctx, tx, id); err != nil {
				return err
			} else if has {
				return fmt.Errorf("%w: 재고가 남아 있는 창고는 사용을 중지할 수 없습니다", contract.ErrInvalidState)
			}
			if n, err := s.Stock.ActiveWarehouseCount(ctx, tx); err != nil {
				return err
			} else if n <= 1 {
				return fmt.Errorf("%w: 사용 중인 창고가 하나 이상 있어야 합니다", contract.ErrInvalidState)
			}
		}
		if err := s.Stock.SetWarehouseActive(ctx, tx, id, active, s.now()); err != nil {
			return err
		}
		return s.record(ctx, tx, p, action, audit.TargetWarehouse, id, audit.DiffItem{Key: "name", New: w.Name},
			audit.DiffItem{Key: "active", Old: fmt.Sprint(w.Active), New: fmt.Sprint(active)})
	})
}

// ---------------------------------------------------------------- 출고

// StockIssueService 는 부서 사용 출고다(inventory:manage).
type StockIssueService struct{ stockBase }

var _ domain.StockIssueService = (*StockIssueService)(nil)

// NewStockIssueService 는 SQLite 저장소로 출고 서비스를 만든다.
func NewStockIssueService(b Base) *StockIssueService { return &StockIssueService{stockBase{Base: b}} }

// ProposeIssue 는 방식별 로트 배분을 제안한다(저장하지 않는다). 유효기한이 지난 로트는 제안에서 빠진다.
func (s *StockIssueService) ProposeIssue(ctx context.Context, in domain.IssueProposeInput) ([]domain.IssueAllocation, error) {
	if _, err := contract.Authorize(ctx, contract.PermInventoryManage); err != nil {
		return nil, err
	}
	manual := in.Method == domain.IssueManual // 직접 고르기: 배분 없이 쓸 수 있는 로트 표만 돌려준다(수량 0)
	if in.Method != domain.IssueFIFO && in.Method != domain.IssueExpiry && !manual {
		return nil, validation("알 수 없는 로트 고르는 방식입니다")
	}
	if !manual && (in.Qty <= 0 || in.Qty > maxLineQty) {
		return nil, validation("출고 수량이 올바르지 않습니다")
	}
	if _, err := s.activeWarehouse(ctx, s.DB, in.WarehouseID, "출고"); err != nil {
		return nil, err
	}
	item, err := s.Inv.GetItem(ctx, s.DB, in.ItemID)
	if errors.Is(err, contract.ErrNotFound) {
		return nil, validation("품목을 찾을 수 없습니다")
	}
	if err != nil {
		return nil, err
	}
	cands, err := s.Stock.LotCandidates(ctx, s.DB, in.WarehouseID, item.ID)
	if err != nil {
		return nil, err
	}
	today := string(contract.DayOf(s.now()))
	usable := cands[:0]
	for _, c := range cands {
		if !item.TrackLot {
			if c.LotID == 0 {
				usable = append(usable, c)
			}
			continue
		}
		if c.LotID != 0 && (c.Expiry == "" || c.Expiry >= today) {
			usable = append(usable, c)
		}
	}
	fifo := func(a, b domain.IssueAllocation) bool {
		if a.Received != b.Received {
			return a.Received < b.Received
		}
		return a.LotID < b.LotID
	}
	sort.SliceStable(usable, func(i, j int) bool {
		a, b := usable[i], usable[j]
		if in.Method == domain.IssueExpiry && a.Expiry != b.Expiry {
			if a.Expiry == "" || b.Expiry == "" {
				return b.Expiry == "" // 기한 없는 로트는 맨 뒤
			}
			return a.Expiry < b.Expiry
		}
		return fifo(a, b)
	})
	var avail int64
	for _, c := range usable {
		avail += c.OnHand
	}
	if manual {
		return usable, nil
	}
	if avail < in.Qty {
		return nil, validation("출고할 수 있는 재고가 부족합니다: %s (출고 가능 %s, 요청 %s)", item.Name, formatWon(avail), formatWon(in.Qty))
	}
	rest := in.Qty
	out := []domain.IssueAllocation{}
	for _, c := range usable {
		if rest == 0 {
			break
		}
		take := c.OnHand
		if take > rest {
			take = rest
		}
		c.Qty = take
		rest -= take
		out = append(out, c)
	}
	return out, nil
}

// Issue 는 출고 전표를 처리한다: 전표 번호 채번 → 전표·줄 → 줄마다 원장 출고 행 → 감사, 한 트랜잭션.
func (s *StockIssueService) Issue(ctx context.Context, in domain.IssueInput) (domain.IssueSlip, error) {
	p, err := s.authorize(ctx, contract.PermInventoryManage, "stock_issue.create", audit.TargetStockIssue)
	if err != nil {
		return domain.IssueSlip{}, err
	}
	in.Memo = strings.TrimSpace(in.Memo)
	if utf8.RuneCountInString(in.Memo) > maxMemoRunes {
		return domain.IssueSlip{}, validation("비고는 %d자 이내로 입력하세요", maxMemoRunes)
	}
	if in.Method == "" {
		in.Method = domain.IssueManual
	}
	if in.Method != domain.IssueFIFO && in.Method != domain.IssueExpiry && in.Method != domain.IssueManual {
		return domain.IssueSlip{}, validation("알 수 없는 로트 고르는 방식입니다")
	}
	if in.IssueDate, err = s.dayOrToday(in.IssueDate, "출고일"); err != nil {
		return domain.IssueSlip{}, err
	}
	lines := make([]stockLine, len(in.Lines))
	for i, l := range in.Lines {
		lines[i] = stockLine{l.ItemID, l.LotID, l.Qty}
	}
	var out domain.IssueSlip
	err = s.write(ctx, p, "stock_issue.create", audit.TargetStockIssue, 0, func(tx contract.Tx) error {
		if err := s.assertMonthOpen(ctx, tx, in.IssueDate); err != nil { // 출고일이 마감된 달이면 전표를 만들지 않는다(B27)
			return err
		}
		dept, err := s.Stock.DepartmentName(ctx, tx, in.DepartmentID)
		if errors.Is(err, contract.ErrNotFound) {
			return validation("출고 부서를 선택하세요")
		}
		if err != nil {
			return err
		}
		wh, err := s.activeWarehouse(ctx, tx, in.WarehouseID, "출고")
		if err != nil {
			return err
		}
		checked, err := s.checkLines(ctx, tx, lines, true)
		if err != nil {
			return err
		}
		now := s.now()
		no, err := s.Stock.NextNo(ctx, tx, "OUT", now.Year())
		if err != nil {
			return err
		}
		slipID, err := s.Stock.InsertIssueSlip(ctx, tx, no, in, p.UserID, now)
		if err != nil {
			return err
		}
		for _, cl := range checked {
			if err := s.Stock.InsertIssueLine(ctx, tx, slipID, domain.IssueLineInput{ItemID: cl.itemID, LotID: cl.lotID, Qty: cl.qty}); err != nil {
				return err
			}
			if err := s.ledgerAppend(ctx, tx, wh.ID, cl, -cl.qty, "issue", "issue_slip", slipID, p.UserID, wh.Name, in.IssueDate); err != nil {
				return err
			}
		}
		slips, err := s.Stock.IssueSlips(ctx, tx, slipID, 1)
		if err != nil || len(slips) == 0 {
			return errors.Join(err, contract.ErrNotFound)
		}
		out = slips[0]
		diff := []audit.DiffItem{{Key: "doc_no", New: no}, {Key: "department", New: dept}, {Key: "warehouse", New: wh.Name},
			{Key: "method", New: issueMethodLabel(in.Method)}, {Key: "lines", New: stockLineSummary(checked)}, {Key: "memo", New: in.Memo}}
		// 소급 출고(출고일이 지난 날짜)면 그 날짜 뒤의 평가 줄이 다시 계산된다: 다시 계산된 줄 수를 남긴다(B27 명세 7절).
		if in.IssueDate != "" && in.IssueDate < string(contract.DayOf(now)) {
			n := 0
			for _, cl := range checked {
				c, err := s.Inv.RevaluedRowsAfter(ctx, tx, cl.itemID, in.IssueDate, slipID)
				if err != nil {
					return err
				}
				n += c
			}
			if n > 0 {
				diff = append(diff, audit.DiffItem{Key: "revalued_rows", New: fmt.Sprint(n)})
			}
		}
		return s.record(ctx, tx, p, "stock_issue.create", audit.TargetStockIssue, slipID, diff...)
	})
	return out, err
}

// Departments 는 출고 부서 선택지다.
func (s *StockIssueService) Departments(ctx context.Context) ([]domain.DepartmentRef, error) {
	if _, err := contract.Authorize(ctx, contract.PermInventoryManage); err != nil {
		return nil, err
	}
	out, err := s.Stock.Departments(ctx, s.DB)
	if out == nil {
		out = []domain.DepartmentRef{}
	}
	return out, err
}

func issueMethodLabel(m string) string {
	switch m {
	case domain.IssueFIFO:
		return "선입선출"
	case domain.IssueExpiry:
		return "유효기한 임박 우선"
	}
	return "직접 고르기"
}

func (s *StockIssueService) ListRecent(ctx context.Context, limit int) ([]domain.IssueSlip, error) {
	if _, err := contract.Authorize(ctx, contract.PermInventoryManage); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 100 {
		limit = 30
	}
	out, err := s.Stock.IssueSlips(ctx, s.DB, 0, limit)
	if out == nil {
		out = []domain.IssueSlip{}
	}
	return out, err
}

// ---------------------------------------------------------------- 이동

// StockTransferService 는 창고 간 이동이다(inventory:manage). 결재 없음, 출고 한 줄·입고 한 줄이 한 트랜잭션이다.
type StockTransferService struct{ stockBase }

var _ domain.StockTransferService = (*StockTransferService)(nil)

// NewStockTransferService 는 SQLite 저장소로 이동 서비스를 만든다.
func NewStockTransferService(b Base) *StockTransferService {
	return &StockTransferService{stockBase{Base: b}}
}

// transferPlan 은 이동의 출발·도착 창고와 법인이다. 두 법인이 다르면 법인 간 이동(내부 거래)이다(B51 7절).
type transferPlan struct {
	from, to             domain.Warehouse
	fromEntity, toEntity int64
	fromName, toName     string // 법인 이름
	checked              []checkedLine
}

func (pl transferPlan) inter() bool { return pl.fromEntity != pl.toEntity }

// errToEntityDenied 는 도착 법인에서 재고를 관리할 권한이 없을 때다(403).
var errToEntityDenied = fmt.Errorf("%w: 도착 법인에서 재고를 관리할 권한이 없어 이동할 수 없습니다.", contract.ErrForbidden)

// closedEntityError 는 법인 간 이동에서 어느 한 법인의 이동일 월이 마감되었을 때다(문구에 법인 이름이 들어간다). 거부 기록은 호출자가 남긴다.
type closedEntityError struct{ msg, month string }

func (e *closedEntityError) Error() string { return e.msg }
func (e *closedEntityError) Unwrap() error { return contract.ErrValidation }

// plan 은 출발·도착을 확인한다. 도착 창고가 다른 법인이면 그 법인이 호출자의 범위 안이고 inventory:manage 가 효력일 때만 법인 간 이동이 된다(D6).
// 범위 밖 법인의 창고는 없는 창고와 같은 응답이고, 거부 사실은 denied 로 돌려줘 호출자가 감사에 남긴다.
func (s *StockTransferService) plan(ctx context.Context, q contract.Tx, p contract.Principal, in domain.TransferInput) (pl transferPlan, denied string, err error) {
	if in.FromID == in.ToID {
		return pl, "", validation("출발 창고와 도착 창고가 같습니다")
	}
	if pl.from, err = s.activeWarehouse(ctx, q, in.FromID, "출발"); err != nil {
		return pl, "", err
	}
	fromRef, err := s.Stock.WarehouseAny(ctx, q, in.FromID)
	if err != nil {
		return pl, "", err
	}
	toRef, err := s.Stock.WarehouseAny(ctx, q, in.ToID)
	if errors.Is(err, contract.ErrNotFound) {
		return pl, "", validation("도착 창고를 찾을 수 없습니다")
	}
	if err != nil {
		return pl, "", err
	}
	pl.fromEntity, pl.toEntity, pl.fromName, pl.toName = fromRef.EntityID, toRef.EntityID, fromRef.EntityName, toRef.EntityName
	if !pl.inter() {
		if pl.to, err = s.activeWarehouse(ctx, q, in.ToID, "도착"); err != nil {
			return pl, "", err
		}
	} else {
		switch {
		case !p.SelectableEntities().Has(toRef.EntityID):
			return pl, "범위 밖 도착 창고", validation("도착 창고를 찾을 수 없습니다")
		case !p.CanIn(contract.PermInventoryManage, toRef.EntityID):
			return pl, "도착 법인 권한 없음", errToEntityDenied
		case !toRef.EntityActive || !toRef.Active:
			return pl, "", validation("사용 중지된 창고입니다: %s", toRef.Name)
		}
		pl.to = toRef.Warehouse
	}
	lines := make([]stockLine, len(in.Lines))
	for i, l := range in.Lines {
		lines[i] = stockLine{l.ItemID, l.LotID, l.Qty}
	}
	pl.checked, err = s.checkLines(ctx, q, lines, false)
	return pl, "", err
}

// interClosed 는 법인 간 이동의 이동일이 속한 달이 출발·도착 어느 법인에서든 마감되었는지 본다(D9).
func (s *StockTransferService) interClosed(ctx context.Context, q contract.Tx, pl transferPlan, day string) error {
	for _, c := range []struct {
		id          int64
		name, label string
	}{{pl.fromEntity, pl.fromName, "출발"}, {pl.toEntity, pl.toName, "도착"}} {
		closed, err := (repository.SQLMonthClose{}).ClosedFor(ctx, q, c.id, day)
		if err != nil {
			return err
		}
		if closed {
			return &closedEntityError{msg: fmt.Sprintf("%s: %s 법인(%s)의 해당 월이 마감되어 이동할 수 없습니다.", contract.ErrValidation.Error(), c.label, c.name), month: day[:7]}
		}
	}
	return nil
}

// frozenCosts 는 법인 간 이동 줄마다 이동 시점 출발 법인 평균으로 환산한 동결 금액이다. 같은 법인 이동은 nil 이다.
func (s *StockTransferService) frozenCosts(ctx context.Context, q contract.Tx, pl transferPlan, day string) ([]int64, error) {
	if !pl.inter() {
		return nil, nil
	}
	cl := make([]repository.TransferCostLine, len(pl.checked))
	for i, c := range pl.checked {
		cl[i] = repository.TransferCostLine{ItemID: c.itemID, Qty: c.qty}
	}
	return s.Inv.TransferCosts(ctx, q, pl.fromEntity, day, cl)
}

// Preview 는 이동이 만들 재고 기록(출고 줄·입고 줄)을 처리 전에 보여 준다. 재고 부족도 여기서 미리 알린다.
// 법인 간 이동이면 줄마다 법인 이름과(inventory:valuation_read 가 있을 때만) 이동 단가·금액을 붙이고, 평균 단가가 없으면 경고 표시를 붙인다.
func (s *StockTransferService) Preview(ctx context.Context, in domain.TransferInput) ([]domain.LedgerPreviewLine, error) {
	p, err := contract.Authorize(ctx, contract.PermInventoryManage)
	if err != nil {
		return nil, err
	}
	pl, denied, err := s.plan(ctx, s.DB, p, in)
	if denied != "" {
		s.recordDenied(ctx, p, "stock_transfer.create", audit.TargetStockTransfer, 0, denied)
	}
	if err != nil {
		return nil, err
	}
	var costs []int64
	if pl.inter() {
		day, err := s.dayOrToday(in.MoveDate, "이동일")
		if err != nil {
			return nil, err
		}
		if err := s.interClosed(ctx, s.DB, pl, day); err != nil {
			return nil, err
		}
		if costs, err = s.frozenCosts(ctx, s.DB, pl, day); err != nil {
			return nil, err
		}
	}
	valued := pl.inter() && p.CanIn(contract.PermInventoryValuation, pl.fromEntity)
	out := []domain.LedgerPreviewLine{}
	for i, cl := range pl.checked {
		have, err := s.Stock.WhLotQty(ctx, s.DB, pl.from.ID, cl.itemID, cl.lotID)
		if err != nil {
			return nil, err
		}
		if have < cl.qty {
			return nil, validation("출발 창고 재고가 부족합니다: %s %s (현재 %s, 이동 %s)", cl.item.Name, cl.lotNo, formatWon(have), formatWon(cl.qty))
		}
		o := domain.LedgerPreviewLine{Kind: "transfer_out", WarehouseName: pl.from.Name, ItemName: cl.item.Name, LotNo: cl.lotNo, Qty: -cl.qty}
		n := domain.LedgerPreviewLine{Kind: "transfer_in", WarehouseName: pl.to.Name, ItemName: cl.item.Name, LotNo: cl.lotNo, Qty: cl.qty}
		if pl.inter() {
			o.Intercompany, o.EntityName = true, pl.fromName
			n.Intercompany, n.EntityName = true, pl.toName
			o.NoAvg, n.NoAvg = costs[i] == 0, costs[i] == 0
			if valued {
				u := roundDivInt(costs[i], cl.qty)
				o.Valued, n.Valued = true, true
				o.UnitCost, n.UnitCost, o.Amount, n.Amount = u, u, costs[i], costs[i]
			}
		}
		out = append(out, o, n)
	}
	return out, nil
}

// roundDivInt 는 a ÷ b 를 0.5 올림으로 나눈다(a ≥ 0, b > 0).
func roundDivInt(a, b int64) int64 { return (2*a + b) / (2 * b) }

// Transfer 는 이동 전표를 처리한다. 줄마다 같은 로트의 이동 출고(출발)·이동 입고(도착) 두 원장 행이 같은 전표로 한 트랜잭션에 들어간다.
// 도착 창고가 다른 법인이면 법인 간 이동이다: 출발 법인 출고와 도착 법인 입고가 이동 시점 출발 법인 평균으로 동결한 같은 금액을 쓴다(자동 전표는 만들지 않는다, D7).
func (s *StockTransferService) Transfer(ctx context.Context, in domain.TransferInput) (domain.TransferSlip, error) {
	p, err := s.authorize(ctx, contract.PermInventoryManage, "stock_transfer.create", audit.TargetStockTransfer)
	if err != nil {
		return domain.TransferSlip{}, err
	}
	in.Memo = strings.TrimSpace(in.Memo)
	if utf8.RuneCountInString(in.Memo) > maxMemoRunes {
		return domain.TransferSlip{}, validation("비고는 %d자 이내로 입력하세요", maxMemoRunes)
	}
	if in.MoveDate, err = s.dayOrToday(in.MoveDate, "이동일"); err != nil {
		return domain.TransferSlip{}, err
	}
	// 범위 밖·권한 없는 도착 법인은 트랜잭션 밖에서 먼저 걸러 거부 기록을 남긴다(트랜잭션 안에서 다른 연결을 쓰지 않는다).
	// 그 밖의 오류(없는 창고·재고 부족 등)는 트랜잭션 안에서 현행 순서(마감 월 확인이 먼저)로 판정한다.
	if _, denied, perr := s.plan(ctx, s.DB, p, in); denied != "" {
		s.recordDenied(ctx, p, "stock_transfer.create", audit.TargetStockTransfer, 0, denied)
		return domain.TransferSlip{}, perr
	}
	var out domain.TransferSlip
	err = s.write(ctx, p, "stock_transfer.create", audit.TargetStockTransfer, 0, func(tx contract.Tx) error {
		if err := s.assertMonthOpen(ctx, tx, in.MoveDate); err != nil { // 이동일이 마감된 달이면 전표를 만들지 않는다(B27 검증자 D). 창고 확인보다 먼저다(현행 순서)
			return err
		}
		pl, _, err := s.plan(ctx, tx, p, in)
		if err != nil {
			return err
		}
		if pl.inter() { // 도착 법인(출발 법인은 위에서 확인했다)의 이동일 월이 마감이면 거부한다(D9)
			if err := s.interClosed(ctx, tx, pl, in.MoveDate); err != nil {
				return err
			}
		}
		var book []string
		if pl.inter() {
			book = []string{in.MoveDate} // 법인 간 이동은 이동일이 장부일이다(양쪽 법인의 평가가 같은 날짜 기준)
			// 잠금 순서: 품목 번호 순으로, 출발 창고 재고 잠금 → 두 법인의 평가 잠금(법인 번호 순서). 반대 방향 이동이 교착하지 않는다(명세 7.6).
			order := make([]int, len(pl.checked))
			for i := range order {
				order[i] = i
			}
			sort.Slice(order, func(a, b int) bool { return pl.checked[order[a]].itemID < pl.checked[order[b]].itemID })
			lo, hi := pl.fromEntity, pl.toEntity
			if lo > hi {
				lo, hi = hi, lo
			}
			for _, i := range order {
				if err := dbutil.AcquireKeyLock(ctx, tx, fmt.Sprintf("stock:%d:%d", pl.from.ID, pl.checked[i].itemID)); err != nil {
					return err
				}
				if err := repository.LockValuation(ctx, tx, lo, pl.checked[i].itemID); err != nil {
					return err
				}
				if err := repository.LockValuation(ctx, tx, hi, pl.checked[i].itemID); err != nil {
					return err
				}
			}
		}
		costs, err := s.frozenCosts(ctx, tx, pl, in.MoveDate)
		if err != nil {
			return err
		}
		now := s.now()
		no, err := s.Stock.NextNo(ctx, tx, "MV", now.Year())
		if err != nil {
			return err
		}
		trID, err := s.Stock.InsertTransfer(ctx, tx, no, in, pl.fromEntity, pl.toEntity, p.UserID, now)
		if err != nil {
			return err
		}
		var costSum int64
		for i, cl := range pl.checked {
			var cost int64
			if costs != nil {
				cost = costs[i]
				costSum += cost
			}
			if err := s.Stock.InsertTransferLine(ctx, tx, trID, domain.TransferLineInput{ItemID: cl.itemID, LotID: cl.lotID, Qty: cl.qty}, cost); err != nil {
				return err
			}
			if err := s.ledgerAppend(ctx, tx, pl.from.ID, cl, -cl.qty, "transfer_out", "stock_transfer", trID, p.UserID, pl.from.Name, book...); err != nil {
				return err
			}
			if err := s.ledgerAppend(ctx, tx, pl.to.ID, cl, cl.qty, "transfer_in", "stock_transfer", trID, p.UserID, pl.to.Name, book...); err != nil {
				return err
			}
		}
		slips, err := s.Stock.Transfers(ctx, tx, trID, 1)
		if err != nil || len(slips) == 0 {
			return errors.Join(err, contract.ErrNotFound)
		}
		out = slips[0]
		diff := []audit.DiffItem{{Key: "doc_no", New: no}, {Key: "from_warehouse", New: pl.from.Name}}
		if pl.inter() {
			// 감사 화면은 두 법인 관리자가 보므로 도착 창고 이름 대신 법인 이름만 남긴다(명세 위험 6).
			diff = append(diff, audit.DiffItem{Key: "intercompany", New: "예"}, audit.DiffItem{Key: "from_entity", New: pl.fromName}, audit.DiffItem{Key: "to_entity", New: pl.toName},
				audit.DiffItem{Key: "cost_amount", New: formatWon(costSum)})
		} else {
			diff = append(diff, audit.DiffItem{Key: "to_warehouse", New: pl.to.Name})
		}
		diff = append(diff, audit.DiffItem{Key: "lines", New: stockLineSummary(pl.checked)}, audit.DiffItem{Key: "memo", New: in.Memo})
		return s.record(ctx, tx, p, "stock_transfer.create", audit.TargetStockTransfer, trID, diff...)
	})
	var ce *closedEntityError
	if errors.As(err, &ce) {
		s.recordDenied(ctx, p, "stock_transfer.create", audit.TargetStockTransfer, 0, "마감된 월 "+ce.month)
	}
	if err == nil {
		out = s.shapeSlip(p, out)
	}
	return out, err
}

// shapeSlip 은 호출자의 범위에 맞게 법인 간 이동 전표를 다듬는다: 상대 법인이 읽는 사람의 범위 밖이면 상대 창고 이름·번호를 비우고 법인 이름만 남기며(D8),
// 금액은 inventory:valuation_read 가 있는 호출자에게만 보인다.
func (s *StockTransferService) shapeSlip(p contract.Principal, t domain.TransferSlip) domain.TransferSlip {
	if !t.Intercompany {
		t.CostAmount = 0
		return t
	}
	readable := p.EntitiesFor(contract.PermInventoryRead)
	if !readable.Has(t.FromEntityID) {
		t.FromName, t.FromID = "", 0
	}
	if !readable.Has(t.ToEntityID) {
		t.ToName, t.ToID = "", 0
	}
	t.Valued = p.CanIn(contract.PermInventoryValuation, t.FromEntityID) || p.CanIn(contract.PermInventoryValuation, t.ToEntityID)
	if !t.Valued {
		t.CostAmount = 0
	}
	return t
}

func (s *StockTransferService) ListRecent(ctx context.Context, limit int) ([]domain.TransferSlip, error) {
	p, err := contract.Authorize(ctx, contract.PermInventoryManage)
	if err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 100 {
		limit = 30
	}
	out, err := s.Stock.Transfers(ctx, s.DB, 0, limit)
	if out == nil {
		out = []domain.TransferSlip{}
	}
	for i := range out {
		out[i] = s.shapeSlip(p, out[i])
	}
	return out, err
}

// Targets 는 다른 법인의 도착 창고 후보다. 현재 법인에서 inventory:manage 가 효력이고, 후보 법인도 그 권한이 효력일 때만 나온다(D6).
func (s *StockTransferService) Targets(ctx context.Context) ([]domain.TransferTarget, error) {
	p, err := contract.Authorize(ctx, contract.PermInventoryManage)
	if err != nil {
		return nil, err
	}
	cur := contract.WriteEntityID(ctx)
	set := p.EntitiesFor(contract.PermInventoryManage)
	var ids []int64
	if set.All {
		rows, err := s.DB.QueryContext(ctx, `SELECT id FROM entities WHERE active = 1 AND id <> ? ORDER BY id`, cur)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return nil, err
			}
			ids = append(ids, id)
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
	} else {
		for _, id := range set.IDs {
			if id != cur {
				ids = append(ids, id)
			}
		}
	}
	out, err := s.Stock.TransferTargets(ctx, s.DB, ids)
	if out == nil {
		out = []domain.TransferTarget{}
	}
	return out, err
}
