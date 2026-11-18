package service

import (
	"context"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/repository"
)

// APIReadService 는 연동 API(B42, 7절)의 읽기 자원 서비스다.
// 같은 서비스 함수·같은 규칙을 쓴다: 거래처는 InventoryService.ListVendors(마스킹: API 채널은 contract.RawFieldAllowed 가 거짓이라 항상 가림),
// 전표는 JournalService.Get(권한·줄 구성), 발주·입고는 열람 범위(viewScope)를 적용한다. 목록은 번호 오름차순·after 다음부터 limit 개다.
// 사람 정보(작성자·승인자·사번·연락처)는 어떤 표현에도 담지 않는다.
type APIReadService struct {
	Base
	Inventory  *InventoryService
	JournalSvc *JournalService
	Repo       repository.SQLAPIRead
}

var _ domain.APIReadService = (*APIReadService)(nil)

// NewAPIReadService 는 읽기 서비스를 만든다.
func NewAPIReadService(b Base, inv *InventoryService, j *JournalService) *APIReadService {
	return &APIReadService{Base: b, Inventory: inv, JournalSvc: j}
}

// 한 쪽 최대 행 수(명세 7.3).
const apiMaxLimit = 200

func apiLimit(limit int) int {
	if limit <= 0 || limit > apiMaxLimit {
		return apiMaxLimit
	}
	return limit
}

func (s *APIReadService) Items(ctx context.Context, after int64, limit int) ([]domain.APIItem, bool, error) {
	if _, err := contract.Authorize(ctx, contract.PermInventoryRead); err != nil {
		return nil, false, err
	}
	limit = apiLimit(limit)
	rows, err := s.Repo.ItemsAfter(ctx, s.DB, after, limit+1)
	if err != nil {
		return nil, false, err
	}
	rows, more := trimMore(rows, limit)
	return rows, more, nil
}

func (s *APIReadService) Vendors(ctx context.Context, after int64, limit int) ([]domain.Vendor, bool, error) {
	all, err := s.Inventory.ListVendors(ctx) // 권한(purchase:read)·마스킹은 서비스가 정한다
	if err != nil {
		return nil, false, err
	}
	limit = apiLimit(limit)
	var out []domain.Vendor
	for _, v := range all { // ListVendors 는 번호 오름차순이다
		if v.ID > after {
			out = append(out, v)
		}
	}
	out, more := trimMore(out, limit)
	return out, more, nil
}

func (s *APIReadService) PurchaseOrders(ctx context.Context, after int64, limit int) ([]domain.PurchaseOrder, bool, error) {
	p, err := contract.Authorize(ctx, contract.PermInventoryRead)
	if err != nil {
		return nil, false, err
	}
	scope, err := s.viewScope(ctx, p)
	if err != nil {
		return nil, false, err
	}
	limit = apiLimit(limit)
	rows, err := s.Repo.POsAfter(ctx, s.DB, after, limit+1, scope)
	if err != nil {
		return nil, false, err
	}
	rows, more := trimMore(rows, limit)
	return rows, more, nil
}

func (s *APIReadService) Receipts(ctx context.Context, after int64, limit int) ([]domain.APIReceipt, bool, error) {
	p, err := contract.Authorize(ctx, contract.PermInventoryRead)
	if err != nil {
		return nil, false, err
	}
	scope, err := s.viewScope(ctx, p)
	if err != nil {
		return nil, false, err
	}
	limit = apiLimit(limit)
	rows, err := s.Repo.ReceiptsAfter(ctx, s.DB, after, limit+1, scope)
	if err != nil {
		return nil, false, err
	}
	rows, more := trimMore(rows, limit)
	return rows, more, nil
}

func (s *APIReadService) Journals(ctx context.Context, after int64, limit int) ([]domain.Journal, bool, error) {
	if _, err := contract.Authorize(ctx, contract.PermLedgerRead); err != nil {
		return nil, false, err
	}
	limit = apiLimit(limit)
	nos, err := s.Repo.JournalsAfter(ctx, s.DB, after, limit+1)
	if err != nil {
		return nil, false, err
	}
	nos, more := trimMore(nos, limit)
	out := make([]domain.Journal, 0, len(nos))
	for _, no := range nos {
		j, err := s.JournalSvc.Get(ctx, no)
		if err != nil {
			return nil, false, err
		}
		out = append(out, j)
	}
	return out, more, nil
}

func (s *APIReadService) Payables(ctx context.Context, after int64, limit int) ([]domain.APIPayable, bool, error) {
	if _, err := contract.Authorize(ctx, contract.PermLedgerRead); err != nil {
		return nil, false, err
	}
	limit = apiLimit(limit)
	rows, err := s.Repo.PayablesAfter(ctx, s.DB, after, limit+1)
	if err != nil {
		return nil, false, err
	}
	rows, more := trimMore(rows, limit)
	return rows, more, nil
}

// TaxInvoices 는 세금계산서 읽기다(B43 3단계). 권한은 taxinvoice:read 이고 사업자번호는 호출 채널(API)이 원문을 낼 수 없어 항상 가려서 나간다.
func (s *APIReadService) TaxInvoices(ctx context.Context, after int64, limit int) ([]domain.APITaxInvoice, bool, error) {
	p, err := contract.Authorize(ctx, contract.PermTaxInvoiceRead)
	if err != nil {
		return nil, false, err
	}
	limit = apiLimit(limit)
	rows, err := s.Repo.TaxInvoicesAfter(ctx, s.DB, after, limit+1)
	if err != nil {
		return nil, false, err
	}
	rows, more := trimMore(rows, limit)
	mask := func(b string) string {
		if b == "" || s.Masker == nil {
			return b
		}
		return s.Masker.MaskField(contract.Principal{Role: contract.RoleStaff, Channel: p.Channel}, contract.FieldBusinessNo, b)
	}
	for i := range rows {
		rows[i].SupplierBiz, rows[i].BuyerBiz = mask(rows[i].SupplierBiz), mask(rows[i].BuyerBiz)
	}
	return rows, more, nil
}

// trimMore 는 limit+1 개까지 읽은 목록에서 더 있는지 알려 주고 limit 개로 자른다.
func trimMore[T any](rows []T, limit int) ([]T, bool) {
	if len(rows) > limit {
		return rows[:limit], true
	}
	return rows, false
}
