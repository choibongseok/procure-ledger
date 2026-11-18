package service

import (
	"context"
	"fmt"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/repository"
)

// ImportReconcileService 는 기초 데이터 대조 보고서다(B35 3.9). 새 시스템 값은 코드가 DB 에서 집계하고, 원 시스템 수치는 호출한 사람이 넣은 값이다.
// 차이가 있으면 원인 후보를 문장으로 안내한다. 읽기 전용이고 아무것도 저장하지 않는다.
type ImportReconcileService struct {
	Base
	Imp repository.SQLImport
}

var _ domain.ImportReconcileService = (*ImportReconcileService)(nil)

func NewImportReconcile(b Base) *ImportReconcileService { return &ImportReconcileService{Base: b} }

func (s *ImportReconcileService) Report(ctx context.Context, in domain.ReconcileInput) (domain.ReconcileReport, error) {
	var out domain.ReconcileReport
	if _, err := contract.Authorize(ctx, contract.PermImportRead); err != nil {
		return out, err
	}
	c, err := s.Imp.ReconCounts(ctx, s.DB)
	if err != nil {
		return out, err
	}
	pending, err := s.Imp.PendingOpeningJournalsAll(ctx, s.DB)
	if err != nil {
		return out, err
	}
	out.PendingOpeningCount = pending
	sys := map[string]int64{domain.ReconAccounts: c.Accounts, domain.ReconVendors: c.Vendors, domain.ReconItems: c.Items, domain.ReconStockQty: c.StockQty,
		domain.ReconStockValue: c.StockValue, domain.ReconPayable: c.PayableTotal, domain.ReconBudget: c.BudgetTotal}
	for _, code := range domain.ReconcileCodes {
		row := domain.ReconcileRow{Code: code, Label: domain.ReconcileLabels[code], System: sys[code]}
		if v, ok := in.Original[code]; ok {
			orig := v
			diff := row.System - orig
			row.Original, row.Diff = &orig, &diff
			row.Verdict = "match"
			if diff != 0 {
				row.Verdict = "diff"
			}
		}
		if row.Verdict == "diff" {
			switch code {
			case domain.ReconAccounts:
				if c.InactiveAccounts > 0 {
					row.Notes = append(row.Notes, fmt.Sprintf("중지된 계정과목 %d개가 포함되었습니다.", c.InactiveAccounts))
				}
			case domain.ReconItems:
				if c.InactiveItems > 0 {
					row.Notes = append(row.Notes, fmt.Sprintf("중지된 품목 %d종이 포함되었습니다.", c.InactiveItems))
				}
			case domain.ReconStockValue, domain.ReconPayable:
				if pending > 0 {
					row.Notes = append(row.Notes, fmt.Sprintf("승인 전 기초 전표 %d건이 있어 계정별 원장에는 아직 들어가지 않았습니다.", pending))
				}
			}
		}
		out.Rows = append(out.Rows, row)
	}
	return out, nil
}
