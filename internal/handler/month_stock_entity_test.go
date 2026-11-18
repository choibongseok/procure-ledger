package handler

import (
	"context"
	"net/url"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
)

type stubMonthStock struct{ rep domain.MonthStockReport }

func (s stubMonthStock) Report(context.Context, domain.MonthStockFilter) (domain.MonthStockReport, error) {
	return s.rep, nil
}

// 전체 모드(entity=*) 내보내기는 법인 열이 맨 앞에 붙고 평균 단가 칸이 비며, 법인 하나의 파일은 열이 그대로다(B51 4.3·8.2).
func TestMonthStockExportAllModeAddsEntityColumn(t *testing.T) {
	rep := domain.MonthStockReport{AllEntities: true, HasAmounts: true, Rows: []domain.MonthStockRow{{SKU: "A", ItemName: "품목", WarehouseName: "창고", WarehouseActive: true, EntityName: "본사", AvgCost: 0, CloseAmount: 10}}}
	src := &monthStockSource{svc: stubMonthStock{rep}, valued: true}
	one := src.DatasetFor(contract.WithActiveEntity(context.Background(), contract.ActiveEntity{ID: 1}))
	all := src.DatasetFor(contract.WithActiveEntity(context.Background(), contract.ActiveEntity{All: true}))
	if len(all.Columns) != len(one.Columns)+1 || all.Columns[0].Label != "법인" || len(one.Columns) != len(src.Dataset().Columns) {
		t.Fatalf("열: 전체 %d, 하나 %d", len(all.Columns), len(one.Columns))
	}
	rows, err := src.Rows(context.Background(), url.Values{}, false, 100)
	if err != nil || len(rows) != 2 || rows[0][0] != "본사" || len(rows[0]) != len(all.Columns) || len(rows[1]) != len(all.Columns) {
		t.Fatalf("행: %v %v", rows, err)
	}
	if rows[0][1+9] != nil { // 법인 열 + 기본 9칸 뒤가 평균 단가
		t.Fatalf("전체 모드 평균 단가 칸은 비어야 합니다: %v", rows[0])
	}
}
