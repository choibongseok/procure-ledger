package service_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
)

// 전송 작업의 가져가기(ClaimDue)와 대조 확정이 동시에 불려도 한 건·한 입고가 두 번 처리되지 않는다(AC-4319).

func TestTaxClaimDueConcurrentCallersNeverShareAJob(t *testing.T) {
	e := newEnv(t)
	e.taxCompany(t)
	e.taxWorker("none", true)
	const total = 6
	for i := 0; i < total; i++ {
		e.taxIssued(t, int64(10_000*(i+1)))
	}
	e.now = e.now.Add(2 * time.Second) // 발행 직후 대기 시간이 지나게 한다
	var mu sync.Mutex
	seen := map[int64]int{}
	var wg sync.WaitGroup
	for g := 0; g < 5; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			jobs, err := e.tax.ClaimDue(context.Background(), e.now, total)
			if err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			for _, j := range jobs {
				seen[j.ID]++
			}
			mu.Unlock()
		}()
	}
	wg.Wait()
	if len(seen) != total {
		t.Fatalf("모든 건이 한 번은 가져가져야 한다: %d/%d %v", len(seen), total, seen)
	}
	for id, n := range seen {
		if n != 1 {
			t.Fatalf("건 %d 을 %d 번 가져갔다", id, n)
		}
	}
}

func TestTaxMatchConfirmConcurrentOnSameReceiptOnlyOneWins(t *testing.T) {
	e := newEnv(t)
	e.taxCompany(t)
	v, _ := e.taxVendor(t, 100_000)
	a := e.taxPurchase(t, v, 100_000, "2026-11-10")
	b := e.taxPurchase(t, v, 100_000, "2026-11-10") // 둘 다 같은 입고를 후보로 제안받는다
	ma, mb := e.matchFor(t, a.No, "proposed"), e.matchFor(t, b.No, "proposed")
	if ma.Receipts[0].ReceiptID != mb.Receipts[0].ReceiptID {
		t.Fatalf("같은 입고를 가리켜야 이 시험이 성립한다: %+v %+v", ma.Receipts, mb.Receipts)
	}
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i, id := range []int64{ma.ID, mb.ID} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = e.tax.Confirm(as(uFinance), id, "")
		}()
	}
	wg.Wait()
	ok := 0
	for _, err := range errs {
		switch {
		case err == nil:
			ok++
		case !errors.Is(err, contract.ErrInvalidState):
			t.Fatalf("거부는 상태 충돌이어야 한다: %v", err)
		}
	}
	if ok != 1 {
		t.Fatalf("동시 확정은 정확히 하나만 성공해야 한다: %v", errs)
	}
	var flagged int
	if err := e.db.QueryRow(`SELECT COUNT(*) FROM tax_invoice_match_receipts WHERE receipt_id = ? AND confirmed = 1`, ma.Receipts[0].ReceiptID).Scan(&flagged); err != nil || flagged != 1 {
		t.Fatalf("입고의 확정 연결은 하나: %d %v", flagged, err)
	}
}

// D7: 월 마감 등으로 자동 전표를 못 만들면 확정 응답과 대조 상세가 사유 한 줄을 보인다. 끄면 안내도 없다.
func TestTaxMatchAutoJournalSkipShowsReason(t *testing.T) {
	e := newEnv(t)
	e.jvForm(t)
	e.taxCompany(t)
	if _, err := e.db.Exec(`INSERT INTO app_settings (key, value, updated_at) VALUES ('taxinvoice.auto_journal', 'on', '2026-11-13T00:00:00Z') ON CONFLICT (key) DO UPDATE SET value = excluded.value`); err != nil {
		t.Fatal(err)
	}
	po, _ := e.receivedPO(t, 20_000, 1)
	inv := e.invoiceForReceipt(t, po.VendorID, 20_000)
	m := e.matchFor(t, inv.No, "proposed")
	// 전기일(오늘)이 속한 달을 마감한다.
	if _, err := e.db.Exec(`INSERT INTO month_closes (month, status, version, closed_by, closed_at) VALUES (?, 'closed', 1, ?, '2026-11-09T01:00:00Z')`, e.now.Format("2006-01"), uAdmin); err != nil {
		t.Fatal(err)
	}
	got, err := e.tax.Confirm(as(uFinance), m.ID, "")
	if err != nil || got.Status != domain.MatchConfirmed {
		t.Fatalf("확정은 막히지 않는다: %v %+v", err, got)
	}
	if cnt, _, _ := e.countTaxJournals(t); got.JournalNote == "" || cnt != 0 {
		t.Fatalf("사유 한 줄이 응답에 있어야 하고 전표는 없다: %q", got.JournalNote)
	}
	if d, _ := e.tax.GetMatch(as(uFinance), m.ID); d.JournalNote != got.JournalNote {
		t.Fatalf("상세에도 같은 사유: %q", d.JournalNote)
	}
	// 설정을 끄면 안내가 없다.
	if _, err := e.db.Exec(`UPDATE app_settings SET value = 'off' WHERE key = 'taxinvoice.auto_journal'`); err != nil {
		t.Fatal(err)
	}
	if d, _ := e.tax.GetMatch(as(uFinance), m.ID); d.JournalNote != "" {
		t.Fatalf("꺼져 있으면 안내가 없다: %q", d.JournalNote)
	}
}
