package service

import (
	"context"
	"errors"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/repository"
)

// LedgerService 는 품목별 수불부 조회다(B24 ④, 권한 inventory:read). 기록 열람이라 처리자·금액을 싣지 않고 감사 기록도 남기지 않는다.
type LedgerService struct {
	Base
	Counts repository.SQLStockCount
	Inv    repository.SQLInventory
}

var _ domain.LedgerService = (*LedgerService)(nil)

// NewLedgerService 는 SQLite 저장소로 수불부 서비스를 만든다.
func NewLedgerService(b Base) *LedgerService { return &LedgerService{Base: b} }

const (
	defaultLedgerPage = 50
	maxLedgerPage     = 200
)

// Ledger 는 기초 · 기간 행(쪽 나누기) · 입고·출고 합계 · 기말을 돌려준다. 이동 출고와 이동 입고는 각각 더한다(상쇄하지 않는다).
func (s *LedgerService) Ledger(ctx context.Context, f domain.LedgerFilter) (domain.LedgerPage, error) {
	p, err := contract.Authorize(ctx, contract.PermInventoryRead)
	if err != nil {
		return domain.LedgerPage{}, err
	}
	if f.ItemID == 0 {
		return domain.LedgerPage{}, validation("품목을 고르세요. 수불부는 품목마다 봅니다.")
	}
	if _, err := s.Inv.GetItem(ctx, s.DB, f.ItemID); errors.Is(err, contract.ErrNotFound) {
		return domain.LedgerPage{}, validation("품목을 찾을 수 없습니다")
	} else if err != nil {
		return domain.LedgerPage{}, err
	}
	today := contract.DayOf(s.now())
	from, to := string(f.From), string(f.To)
	if to == "" {
		to = string(today)
	}
	if from == "" {
		from = string(today)[:8] + "01" // 이번 달 1일
	}
	for _, d := range []string{from, to} {
		if _, err := time.Parse("2006-01-02", d); err != nil {
			return domain.LedgerPage{}, validation("기간은 연-월-일(예 2026-12-18) 형식으로 입력하세요")
		}
	}
	if to < from {
		return domain.LedgerPage{}, validation("종료일은 시작일 이후여야 합니다.")
	}
	size := f.PageSize
	if size <= 0 {
		size = defaultLedgerPage
	}
	if size > maxLedgerPage {
		size = maxLedgerPage
	}
	page := f.Page
	if page < 1 {
		page = 1
	}
	pg, err := s.Counts.Ledger(ctx, s.DB, f, from, to, size, (page-1)*size)
	if err != nil {
		return pg, err
	}
	// 단가·금액은 재고 평가 조회 권한이 있는 역할에게만 내보낸다(서비스가 거른다, 화면 숨김만으로 두지 않는다).
	if !p.Can(contract.PermInventoryValuation) {
		for i := range pg.Rows {
			pg.Rows[i].UnitCost, pg.Rows[i].Amount, pg.Rows[i].Valued = 0, 0, false
		}
	}
	return pg, nil
}
