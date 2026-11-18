package handler

import (
	"context"
	"net/url"
	"testing"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/export"
)

type stubPR struct{}

func (stubPR) ListAll(_ context.Context, f domain.PRFilter, max int) ([]domain.PurchaseRequest, error) {
	return []domain.PurchaseRequest{{No: "PR-1", Title: "제목", RequesterName: "김", RequesterUnitPath: "구매팀", TotalAmount: 1000, Status: domain.PRInReview, CreatedAt: time.Now()}}, nil
}

type stubExportLists struct{}

func (stubExportLists) Receipts(context.Context, domain.ReceiptFilter, int) ([]domain.ReceiptRow, error) {
	return []domain.ReceiptRow{{ReceiptNo: "RC-000001", PONo: "PO-1", ItemName: "품목", Quantity: 3, WarehouseName: "본사 창고", ReceivedAt: time.Now()}}, nil
}
func (stubExportLists) PurchaseOrders(context.Context, domain.POExportFilter, int) ([]domain.POExportRow, error) {
	return []domain.POExportRow{{No: "PO-1", Status: domain.POOpen, TotalAmount: 5, VendorName: "거래처", CreatedAt: time.Now(), DeliveryPlace: "본사"}}, nil
}
func (stubExportLists) Stock(context.Context, domain.StockExportFilter, int) ([]domain.StockExportRow, error) {
	return []domain.StockExportRow{{WarehouseName: "본사 창고", SKU: "A", ItemName: "품목", Quantity: 1}}, nil
}

type stubLedger struct{}

func (stubLedger) Ledger(_ context.Context, f domain.LedgerFilter) (domain.LedgerPage, error) {
	rows := []domain.LedgerRow{{Date: "2026-11-02", KindLabel: "입고", Warehouse: "본사 창고", DocNo: "RC-1", In: 5, Balance: 15, UnitCost: 10, Amount: 50, Valued: true}}
	return domain.LedgerPage{Opening: 10, Rows: rows, TotalIn: 5, Closing: 15, Total: 1}, nil
}

type stubInbox struct{}

func (stubInbox) List(context.Context, domain.InboxBox, domain.InboxFilter) ([]domain.InboxItem, error) {
	return []domain.InboxItem{{No: "TMP-1", Title: "문서", FormName: "구매 품의서", RequesterName: "김", Status: domain.PRDraft, UpdatedAt: time.Now()}}, nil
}
func (stubInbox) Counts(context.Context) (map[domain.InboxBox]domain.InboxCount, error) {
	return nil, nil
}
func (stubInbox) MarkRead(context.Context, int64) error { return nil }

type stubBudget struct{ domain.BudgetService }

func (stubBudget) List(context.Context, string) ([]domain.BudgetRow, error) {
	return []domain.BudgetRow{{DepartmentName: "구매팀", Amount: 100, Used: 10, InProgress: 5, Balance: 85, UsagePct: 10, Badge: domain.BadgeOK}, {DepartmentName: "영업팀", UsagePct: -1, Badge: domain.BadgeOK}}, nil
}

// 모든 데이터셋의 열 수와 행의 칸 수가 같고, 열 이름이 비어 있지 않고, 건수 확인과 행 읽기가 같은 값을 준다(AC-720·711).
func TestListExportSourcesShape(t *testing.T) {
	srcs := []export.Source{
		prExport(stubPR{}), poExport(stubExportLists{}), receiptExport(stubExportLists{}), stockExport(stubExportLists{}),
		ledgerExport(stubLedger{}, false), ledgerExport(stubLedger{}, true), inboxExport(stubInbox{}), budgetExport(stubBudget{}),
	}
	seen := map[string]bool{}
	for _, s := range srcs {
		ds := s.Dataset()
		if seen[ds.Code] {
			t.Fatalf("데이터셋 코드 중복: %s", ds.Code)
		}
		seen[ds.Code] = true
		q := url.Values{"itemId": {"1"}, "box": {"drafted"}}
		rows, err := s.Rows(context.Background(), q, false, 100)
		if err != nil || len(rows) == 0 {
			t.Fatalf("%s 행: %v %v", ds.Code, rows, err)
		}
		for i, r := range rows {
			if len(r) != len(ds.Columns) {
				t.Fatalf("%s 행 %d 칸 수 %d, 열 %d", ds.Code, i, len(r), len(ds.Columns))
			}
		}
		n, more, err := s.Count(context.Background(), q, 100)
		if err != nil || more || n != len(rows) {
			t.Fatalf("%s 건수 %d(%v) %v, 행 %d", ds.Code, n, more, err, len(rows))
		}
		for _, c := range ds.Columns {
			if c.Label == "" {
				t.Fatalf("%s: 열 이름이 비었습니다", ds.Code)
			}
		}
	}
}

// 수불부: 첫 행 기초, 끝 행 기말이고 기초 + 입고 − 출고 = 기말이다(AC-722). 금액 열은 금액 데이터셋에만 있다.
func TestLedgerExportOpeningClosing(t *testing.T) {
	q := url.Values{"itemId": {"1"}}
	rows, _ := ledgerExport(stubLedger{}, false).Rows(context.Background(), q, false, 100)
	if len(rows) != 3 || rows[0][1] != "기초" || rows[0][7] != int64(10) || rows[2][1] != "기말" || rows[2][7] != int64(15) {
		t.Fatalf("기초·기말 줄: %v", rows)
	}
	if len(ledgerExport(stubLedger{}, false).Dataset().Columns) != 8 || len(ledgerExport(stubLedger{}, true).Dataset().Columns) != 10 {
		t.Fatal("수량 8열, 금액 포함 10열이어야 함")
	}
}

// 상한 초과 판정은 max+1 행을 읽어 더 있음을 알린다.
func TestListSourceCountMore(t *testing.T) {
	n, more, err := receiptExport(stubExportLists{}).Count(context.Background(), url.Values{}, 0)
	if err != nil || n != 1 || !more {
		t.Fatalf("n=%d more=%v err=%v", n, more, err)
	}
}
