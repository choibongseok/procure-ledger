package service

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/privacy"
	"github.com/choibongseok/procure-ledger/internal/repository"
	"github.com/choibongseok/procure-ledger/internal/sod"
)

const maxReceiptLines = 200

// InventoryService 는 domain.InventoryService 의 구현이다.
// 재고 수량은 원장 합으로만 읽고, 쓰는 길은 입고(Receive)가 원장에 줄을 추가하는 것 하나뿐이다.
type InventoryService struct {
	Base
	Repo repository.InventoryRepository
}

var _ domain.InventoryService = (*InventoryService)(nil)

// NewInventoryService 는 SQLite 저장소로 재고 서비스를 만든다.
func NewInventoryService(b Base) *InventoryService {
	return &InventoryService{Base: b, Repo: repository.SQLInventory{}}
}

// ItemInput 은 품목 등록 입력이다.
type ItemInput struct {
	SKU         string
	Name        string
	Unit        string
	SafetyStock int64
	TrackLot    bool
}

// CreateItem 은 품목 마스터를 등록한다(inventory:manage). 도메인 계약 밖의 확장 메서드다.
func (s *InventoryService) CreateItem(ctx context.Context, in ItemInput) (domain.Item, error) {
	p, err := s.authorize(ctx, contract.PermInventoryManage, "item.create", audit.TargetItem)
	if err != nil {
		return domain.Item{}, err
	}
	sku, name, unit := strings.TrimSpace(in.SKU), strings.TrimSpace(in.Name), strings.TrimSpace(in.Unit)
	if sku == "" || name == "" {
		return domain.Item{}, validation("품목 코드와 이름이 필요합니다")
	}
	if unit == "" {
		unit = "EA"
	}
	if in.SafetyStock < 0 {
		return domain.Item{}, validation("안전재고는 0 이상이어야 합니다")
	}
	var out domain.Item
	err = s.write(ctx, p, "item.create", audit.TargetItem, 0, func(tx contract.Tx) error {
		id, err := s.Repo.InsertItem(ctx, tx, domain.Item{SKU: sku, Name: name, Unit: unit, SafetyStock: in.SafetyStock, TrackLot: in.TrackLot})
		if err != nil {
			return err
		}
		if err := s.record(ctx, tx, p, "item.create", audit.TargetItem, id,
			audit.DiffItem{Key: "sku", New: sku}, audit.DiffItem{Key: "name", New: name},
			audit.DiffItem{Key: "safety_stock", New: fmt.Sprint(in.SafetyStock)}); err != nil {
			return err
		}
		out, err = s.Repo.GetItem(ctx, tx, id)
		return err
	})
	return out, err
}

// CreateVendor 는 거래처를 등록한다(inventory:manage). 감사 대상은 vendor 다.
func (s *InventoryService) CreateVendor(ctx context.Context, in domain.Vendor) (domain.Vendor, error) {
	p, err := s.authorize(ctx, contract.PermInventoryManage, "vendor.create", audit.TargetVendor)
	if err != nil {
		return domain.Vendor{}, err
	}
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" {
		return domain.Vendor{}, validation("거래처 이름이 필요합니다")
	}
	var id int64
	err = s.write(ctx, p, "vendor.create", audit.TargetVendor, 0, func(tx contract.Tx) error {
		var err error
		// 같은 이름·같은 사업자번호의 거래처는 새로 등록하지 않는다(기존 데이터는 건드리지 않고 새 등록만 막는다).
		existing, err := s.Repo.ListVendors(ctx, tx)
		if err != nil {
			return err
		}
		if err := checkVendorDuplicate(in, existing); err != nil {
			return err
		}
		if id, err = s.Repo.InsertVendor(ctx, tx, in); err != nil {
			return err
		}
		// 사업자번호 등 민감 값은 감사 로그에 남기지 않는다.
		return s.Audit.Record(ctx, tx, audit.Entry{
			Actor: p, Action: "vendor.create", TargetType: audit.TargetVendor, TargetID: fmt.Sprint(id),
			Result: audit.ResultSuccess, Detail: "거래처 등록", Diff: []audit.DiffItem{{Key: "name", New: in.Name}},
		})
	})
	if err != nil {
		return domain.Vendor{}, err
	}
	in.ID = id
	return s.maskVendor(p, in), nil
}

// ListVendors 는 거래처 목록이다. 사업자번호·계좌·연락처는 mask:bypass 가 없으면 가려서 돌려준다.
func (s *InventoryService) ListVendors(ctx context.Context) ([]domain.Vendor, error) {
	p, err := contract.Authorize(ctx, contract.PermPurchaseRead)
	if err != nil {
		return nil, err
	}
	vs, err := s.Repo.ListVendors(ctx, s.DB)
	if err != nil {
		return nil, err
	}
	raw := map[int64][]string{}
	for i := range vs {
		orig := vs[i]
		vs[i] = s.maskVendor(p, orig)
		if rf := vendorRawFields(orig, vs[i]); len(rf) > 0 {
			raw[orig.ID] = rf
		}
	}
	// 원문(계좌·사업자번호·전화·이메일)이 실린 거래처가 있으면 응답 한 번에 범주별로 한 번씩 기록한다. 기록하지 못하면 목록을 내주지 않는다(fail-closed).
	if err := s.noteAll(ctx, p, vendorAccesses(privacy.ScreenVendorList, raw)); err != nil {
		return nil, err
	}
	return vs, nil
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// maskVendor 는 구분 저장된 민감 필드를 역할에 따라 가린다. Masker 가 없으면 전부 가린다(fail-closed).
func (s *InventoryService) maskVendor(p contract.Principal, v domain.Vendor) domain.Vendor {
	m := func(kind contract.FieldKind, val string) string {
		if val == "" {
			return ""
		}
		if s.Masker == nil {
			return "****"
		}
		return s.Masker.MaskField(p, kind, val)
	}
	v.BusinessNo = m(contract.FieldBusinessNo, v.BusinessNo)
	v.AccountNo = m(contract.FieldAccountNo, v.AccountNo)
	v.Phone = m(contract.FieldPhone, v.Phone)
	v.Email = m(contract.FieldEmail, v.Email)
	return v
}

// SearchItems 는 품목 검색이다.
func (s *InventoryService) SearchItems(ctx context.Context, query string, limit int) ([]domain.Item, error) {
	if _, err := contract.Authorize(ctx, contract.PermInventoryRead); err != nil {
		return nil, err
	}
	return s.Repo.SearchItems(ctx, s.DB, strings.TrimSpace(query), limit)
}

// GetStock 은 품목 재고다(원장 합).
func (s *InventoryService) GetStock(ctx context.Context, itemID int64) (domain.ItemStock, error) {
	p, err := contract.Authorize(ctx, contract.PermInventoryRead)
	if err != nil {
		return domain.ItemStock{}, err
	}
	st, err := s.Repo.ItemStock(ctx, s.DB, itemID)
	if err != nil {
		return st, err
	}
	st.ByWarehouse, err = repository.SQLStock{}.ItemWarehouseQty(ctx, s.DB, itemID)
	if err != nil {
		return st, err
	}
	st.WarehouseLots, err = repository.SQLStock{}.WarehouseLots(ctx, s.DB, itemID, 0)
	if err != nil {
		return st, err
	}
	return s.withValuation(ctx, p, st)
}

// withValuation 은 재고 평가 조회 권한이 있는 호출자에게만 평균 단가·재고 금액을 채운다(B27).
func (s *InventoryService) withValuation(ctx context.Context, p contract.Principal, st domain.ItemStock) (domain.ItemStock, error) {
	if !p.Can(contract.PermInventoryValuation) {
		return st, nil
	}
	if a, has := contract.ActiveEntityFrom(ctx); has && a.All { // 전체 모드: 법인마다 단가가 달라 합산하지 않고 법인별로 나눈다(B51 D14)
		vals, err := s.Repo.ItemValuations(ctx, s.DB, st.Item.ID)
		if err != nil {
			return st, err
		}
		for _, v := range vals {
			st.ValuationByEntity = append(st.ValuationByEntity, domain.EntityValuation{EntityID: v.EntityID, EntityName: v.EntityName, AvgCost: v.AvgCost, StockAmount: v.StockAmount})
		}
		return st, nil
	}
	avg, amt, ok, err := s.Repo.ItemValuation(ctx, s.DB, st.Item.ID)
	if err != nil {
		return st, err
	}
	st.AvgCost, st.StockAmount, st.HasValuation = avg, amt, ok
	return st, nil
}

// GetStockAt 은 한 창고의 재고다(B24, 명세 3.3): 합계·로트·안전재고 미달 판정이 그 창고 기준이다. warehouseID 가 0 이면 GetStock 과 같다(모든 창고 합계).
// 없는 창고는 ErrValidation 이다. 안전재고는 품목 단위 값이라 창고 선택 시에는 "그 창고 수량이 안전재고보다 적은가"를 참고로 보인다.
func (s *InventoryService) GetStockAt(ctx context.Context, itemID, warehouseID int64) (domain.ItemStock, error) {
	if warehouseID == 0 {
		return s.GetStock(ctx, itemID)
	}
	p, err := contract.Authorize(ctx, contract.PermInventoryRead)
	if err != nil {
		return domain.ItemStock{}, err
	}
	stock := repository.SQLStock{}
	if _, err := stock.Warehouse(ctx, s.DB, warehouseID); errors.Is(err, contract.ErrNotFound) {
		return domain.ItemStock{}, validation("알 수 없는 창고입니다")
	} else if err != nil {
		return domain.ItemStock{}, err
	}
	st, err := s.Repo.ItemStock(ctx, s.DB, itemID)
	if err != nil {
		return st, err
	}
	if st.ByWarehouse, err = stock.ItemWarehouseQty(ctx, s.DB, itemID); err != nil {
		return st, err
	}
	if st.WarehouseLots, err = stock.WarehouseLots(ctx, s.DB, itemID, warehouseID); err != nil {
		return st, err
	}
	st.OnHand, st.Lots = 0, nil
	for _, l := range st.WarehouseLots {
		st.OnHand += l.Quantity
		if l.LotID != 0 {
			st.Lots = append(st.Lots, domain.LotStock{LotID: l.LotID, LotNo: l.LotNo, ExpiryDate: l.ExpiryDate, Quantity: l.Quantity})
		}
	}
	st.BelowSafety = st.OnHand < st.Item.SafetyStock
	return s.withValuation(ctx, p, st)
}

// ListBelowSafety 는 안전재고 미달 품목이다.
func (s *InventoryService) ListBelowSafety(ctx context.Context) ([]domain.ItemStock, error) {
	if _, err := contract.Authorize(ctx, contract.PermInventoryRead); err != nil {
		return nil, err
	}
	return s.Repo.BelowSafety(ctx, s.DB)
}

// ListExpiring 은 withinDays 일 안에 유효기한이 끝나는(이미 지난 것 포함) 재고 로트다.
func (s *InventoryService) ListExpiring(ctx context.Context, withinDays int) ([]domain.ExpiringLot, error) {
	if _, err := contract.Authorize(ctx, contract.PermInventoryRead); err != nil {
		return nil, err
	}
	if withinDays < 0 || withinDays > 3650 {
		return nil, validation("임박 기준 일수가 올바르지 않습니다")
	}
	return s.Repo.Expiring(ctx, s.DB, s.now(), withinDays)
}

// GetPurchaseOrder 는 발주 한 건이다. 원 구매요청의 열람 범위를 따른다(purchase:read_all 이 없으면 범위 밖 건은 ErrNotFound).
func (s *InventoryService) GetPurchaseOrder(ctx context.Context, id int64) (domain.PurchaseOrder, error) {
	p, err := contract.Authorize(ctx, contract.PermInventoryRead)
	if err != nil {
		return domain.PurchaseOrder{}, err
	}
	scope, err := s.viewScope(ctx, p)
	if err != nil {
		return domain.PurchaseOrder{}, err
	}
	return s.Repo.GetPO(ctx, s.DB, id, scope)
}

// ListPurchaseOrders 는 발주 목록이다.
func (s *InventoryService) ListPurchaseOrders(ctx context.Context, f domain.POFilter) ([]domain.PurchaseOrder, error) {
	p, err := contract.Authorize(ctx, contract.PermInventoryRead)
	if err != nil {
		return nil, err
	}
	scope, err := s.viewScope(ctx, p)
	if err != nil {
		return nil, err
	}
	return s.Repo.ListPOs(ctx, s.DB, f.Status, f.Limit, f.Offset, scope)
}

// CountPurchaseOrders 는 같은 조건의 발주 전체 건수다(계약 밖 확장: 목록 화면의 쪽 이동). 열람 범위는 목록과 같다.
func (s *InventoryService) CountPurchaseOrders(ctx context.Context, f domain.POFilter) (int, error) {
	p, err := contract.Authorize(ctx, contract.PermInventoryRead)
	if err != nil {
		return 0, err
	}
	scope, err := s.viewScope(ctx, p)
	if err != nil {
		return 0, err
	}
	return s.Repo.CountPOs(ctx, s.DB, f.Status, scope)
}

// GetReceipt 는 입고 한 건이다.
func (s *InventoryService) GetReceipt(ctx context.Context, id int64) (domain.Receipt, error) {
	p, err := contract.Authorize(ctx, contract.PermInventoryRead)
	if err != nil {
		return domain.Receipt{}, err
	}
	scope, err := s.viewScope(ctx, p)
	if err != nil {
		return domain.Receipt{}, err
	}
	return s.Repo.GetReceipt(ctx, s.DB, id, scope)
}

// Receive 는 발주 기준 입고를 처리한다(부분 입고 허용).
// 입고 누계는 같은 트랜잭션 안에서 읽고, 줄마다 잔량을 넘으면 ErrOverReceipt 로 전체를 롤백한다.
// 동시 입고는 트랜잭션 직렬화와 DB 트리거(trg_receipt_no_over)가 이중으로 막는다.
func (s *InventoryService) Receive(ctx context.Context, in domain.ReceiveInput) (domain.Receipt, error) {
	p, err := s.authorize(ctx, contract.PermInventoryManage, "receipt.create", audit.TargetReceipt)
	if err != nil {
		return domain.Receipt{}, err
	}
	if len(in.Lines) == 0 || len(in.Lines) > maxReceiptLines {
		return domain.Receipt{}, validation("입고 줄은 1건 이상 %d건 이하여야 합니다", maxReceiptLines)
	}
	scope, err := s.viewScope(ctx, p)
	if err != nil {
		return domain.Receipt{}, err
	}
	whID := in.WarehouseID
	if whID == 0 {
		whID = 1 // 기본 창고(본사 창고)
	}
	var out domain.Receipt
	err = s.write(ctx, p, "receipt.create", audit.TargetReceipt, in.POID, func(tx contract.Tx) error {
		po, err := s.Repo.GetPO(ctx, tx, in.POID, scope)
		if err != nil {
			return err
		}
		if w, werr := (repository.SQLStock{}).Warehouse(ctx, tx, whID); errors.Is(werr, contract.ErrNotFound) {
			return validation("입고 창고를 찾을 수 없습니다")
		} else if werr != nil {
			return werr
		} else if !w.Active {
			return validation("사용 중지된 창고에는 입고할 수 없습니다: %s", w.Name)
		}
		// 권한 분리: 구매요청을 기안한 사람이 그 발주의 입고까지 확인하면 규칙 모드대로(기본 경고). 앞 단계 행위자는 같은 트랜잭션에서 읽는다.
		origin, err := (repository.SQLInventory{}).POOriginRequester(ctx, tx, po.ID)
		if err != nil {
			return err
		}
		if _, err := s.sodCheck(ctx, tx, p, sod.RulePORequestReceive, origin, sod.Ref{Type: "purchase_request", ID: po.PRID}); err != nil {
			return err
		}
		now := s.now()
		receiptID, err := s.Repo.InsertReceipt(ctx, tx, po.ID, p.UserID, now)
		if err != nil {
			return err
		}
		for i, l := range in.Lines {
			if l.Quantity <= 0 || l.Quantity > maxLineQty {
				return validation("%d번째 줄: 입고 수량이 올바르지 않습니다", i+1)
			}
			pl, err := s.Repo.POLine(ctx, tx, l.POLineID)
			if err != nil || pl.POID != po.ID {
				return validation("%d번째 줄: 이 발주의 품목 줄이 아닙니다", i+1)
			}
			if l.ItemID != 0 && l.ItemID != pl.ItemID {
				return validation("%d번째 줄: 품목이 발주와 다릅니다", i+1)
			}
			if pl.Received+l.Quantity > pl.Quantity {
				return fmt.Errorf("%w: %d번째 줄(발주 %d, 기입고 %d, 이번 %d)", contract.ErrOverReceipt, i+1, pl.Quantity, pl.Received, l.Quantity)
			}
			item, err := s.Repo.GetItem(ctx, tx, pl.ItemID)
			if err != nil {
				return err
			}
			var lotID int64
			lotNo, expiry := strings.TrimSpace(l.LotNo), strings.TrimSpace(l.ExpiryDate)
			if expiry != "" {
				if _, err := time.Parse("2006-01-02", expiry); err != nil {
					return validation("%d번째 줄: 유효기한은 연-월-일(예 2026-12-18) 형식으로 입력하세요", i+1)
				}
			}
			if item.TrackLot {
				if lotNo == "" {
					return validation("%d번째 줄: 로트 번호가 필요한 품목입니다", i+1)
				}
				if lotID, err = s.Repo.FindOrCreateLot(ctx, tx, item.ID, lotNo, expiry); err != nil {
					return err
				}
			}
			if err := s.Repo.InsertReceiptLine(ctx, tx, receiptID, pl.ID, item.ID, l.Quantity, lotID); err != nil {
				return err
			}
			if err := s.assertMonthOpen(ctx, tx, string(contract.DayOf(now))); err != nil {
				return err
			}
			if err := s.Repo.AppendLedger(ctx, tx, repository.LedgerEntry{WarehouseID: whID, ItemID: item.ID, LotID: lotID, Qty: l.Quantity, Reason: "receipt", RefType: "receipt", RefID: receiptID, ActorID: p.UserID, At: now, UnitCost: pl.UnitPrice}); err != nil {
				return err
			}
		}
		newStatus, err := s.Repo.RefreshPOStatus(ctx, tx, po.ID)
		if err != nil {
			return err
		}
		if err := s.record(ctx, tx, p, "receipt.create", audit.TargetReceipt, receiptID,
			audit.DiffItem{Key: "po_id", New: fmt.Sprint(po.ID)},
			audit.DiffItem{Key: "po_status", Old: string(po.Status), New: string(newStatus)},
			audit.DiffItem{Key: "lines", New: fmt.Sprint(len(in.Lines))}); err != nil {
			return err
		}
		out, err = s.Repo.GetReceipt(ctx, tx, receiptID, repository.ScopeAll)
		return err
	})
	return out, err
}

// vendorNameKey 는 거래처 이름 비교용 키다(공백 제거, 소문자).
func vendorNameKey(n string) string {
	return strings.ToLower(strings.Join(strings.Fields(n), ""))
}

func vendorDigits(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// checkVendorDuplicate 는 새 거래처가 기존과 같은 이름이거나 같은 사업자번호이면 다음 할 일과 함께 거부한다.
func checkVendorDuplicate(in domain.Vendor, existing []domain.Vendor) error {
	key, biz := vendorNameKey(in.Name), vendorDigits(in.BusinessNo)
	for _, v := range existing {
		if key != "" && vendorNameKey(v.Name) == key {
			return validation("같은 이름의 거래처가 이미 등록되어 있습니다(%s). 새로 등록하지 말고 목록에서 기존 거래처를 고르세요. 다른 회사라면 이름을 구분해 다시 등록하세요", v.Name)
		}
		if len(biz) >= 10 && vendorDigits(v.BusinessNo) == biz {
			return validation("같은 사업자번호의 거래처가 이미 등록되어 있습니다(%s). 새로 등록하지 말고 기존 거래처를 쓰거나 사업자번호를 확인하세요", v.Name)
		}
	}
	return nil
}
