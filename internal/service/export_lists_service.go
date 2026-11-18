package service

import (
	"context"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/repository"
)

// ExportListService 는 domain.ExportListService 의 구현이다(B28 erp): 입고 이력·발주·재고 현황 목록. 읽기 전용이고 inventory:read, 열람 범위는 구매요청과 같다.
type ExportListService struct {
	Base
	Repo repository.SQLExportLists
}

var _ domain.ExportListService = (*ExportListService)(nil)

func NewExportListService(b Base) *ExportListService { return &ExportListService{Base: b} }

func clampLimit(n int) int {
	if n <= 0 || n > 100001 {
		return 100001
	}
	return n
}

func validDay(s string) error {
	if s == "" {
		return nil
	}
	if _, err := time.Parse("2006-01-02", s); err != nil {
		return validation("날짜는 YYYY-MM-DD 형식으로 입력하세요")
	}
	return nil
}

// Receipts 는 입고 이력이다(입고일·창고·발주 조건).
func (s *ExportListService) Receipts(ctx context.Context, f domain.ReceiptFilter, limit int) ([]domain.ReceiptRow, error) {
	p, err := contract.Authorize(ctx, contract.PermInventoryRead)
	if err != nil {
		return nil, err
	}
	if err := validDay(f.From); err != nil {
		return nil, err
	}
	if err := validDay(f.To); err != nil {
		return nil, err
	}
	if f.From != "" && f.To != "" && f.To < f.From {
		return nil, validation("종료일은 시작일 이후여야 합니다.")
	}
	scope, err := s.viewScope(ctx, p)
	if err != nil {
		return nil, err
	}
	return s.Repo.Receipts(ctx, s.DB, f, scope, clampLimit(limit))
}

// PurchaseOrders 는 발주 목록이다(상태 조건).
func (s *ExportListService) PurchaseOrders(ctx context.Context, f domain.POExportFilter, limit int) ([]domain.POExportRow, error) {
	p, err := contract.Authorize(ctx, contract.PermInventoryRead)
	if err != nil {
		return nil, err
	}
	scope, err := s.viewScope(ctx, p)
	if err != nil {
		return nil, err
	}
	return s.Repo.PurchaseOrders(ctx, s.DB, f.Status, scope, clampLimit(limit))
}

// Stock 는 창고·품목·로트별 재고 현황이다.
func (s *ExportListService) Stock(ctx context.Context, f domain.StockExportFilter, limit int) ([]domain.StockExportRow, error) {
	if _, err := contract.Authorize(ctx, contract.PermInventoryRead); err != nil {
		return nil, err
	}
	return s.Repo.Stock(ctx, s.DB, f, clampLimit(limit))
}
