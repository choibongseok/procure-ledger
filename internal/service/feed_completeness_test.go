package service_test

import (
	"fmt"
	"math/rand"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/contract/domain"
)

// B42 AC-4208(피드 완전성 속성 시험): 임의 순서의 업무 쓰기(성공·롤백 섞음) 뒤에 변경 피드가 표의 현재 상태와 어긋나지 않는다.
//  (a) 모든 추적 행에 created 줄이 있고, 피드의 모든 줄이 존재하는 객체를 가리킨다(롤백된 쓰기의 줄이 없다)
//  (b) 상태가 있는 자원(발주·전표)은 마지막 상태 줄이 표의 현재 상태와 같다
//  (c) 지급·예약·취소가 있었던 채무 항목, 원장이 움직인 품목은 created 뒤에 updated/canceled 줄이 있다
//  (d) 번호는 엄격히 증가한다.

type feedRow struct {
	seq    int64
	op     string
	status string
}

func (e *env) feed(t *testing.T) map[string][]feedRow {
	t.Helper()
	rows, err := e.db.Query(`SELECT seq, resource, object_id, op, status FROM change_events ORDER BY seq`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string][]feedRow{}
	var last int64
	for rows.Next() {
		var r feedRow
		var res string
		var id int64
		if err := rows.Scan(&r.seq, &res, &id, &r.op, &r.status); err != nil {
			t.Fatal(err)
		}
		if r.seq <= last {
			t.Fatalf("번호가 증가하지 않음: %d 다음 %d", last, r.seq)
		}
		last = r.seq
		k := fmt.Sprintf("%s:%d", res, id)
		out[k] = append(out[k], r)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func (e *env) ids(t *testing.T, q string) []int64 {
	t.Helper()
	rows, err := e.db.Query(q)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		out = append(out, id)
	}
	return out
}

func (e *env) checkFeed(t *testing.T, label string) {
	t.Helper()
	feed := e.feed(t)
	exists := map[string]bool{}
	for res, q := range map[string]string{
		"item": `SELECT id FROM items`, "vendor": `SELECT id FROM vendors`, "purchase_order": `SELECT id FROM purchase_orders`,
		"receipt": `SELECT id FROM receipts`, "journal": `SELECT id FROM journals`, "payable": `SELECT id FROM payables`,
	} {
		for _, id := range e.ids(t, q) {
			k := fmt.Sprintf("%s:%d", res, id)
			exists[k] = true
			evs := feed[k]
			if len(evs) == 0 || evs[0].op != "created" {
				t.Errorf("%s: %s 에 created 줄이 없다: %+v", label, k, evs)
			}
		}
	}
	for k := range feed {
		if !exists[k] {
			t.Errorf("%s: 피드가 없는 객체 %s 를 가리킨다(롤백된 쓰기의 줄)", label, k)
		}
	}
	lastStatus := func(k string) string {
		s := ""
		for _, r := range feed[k] {
			if r.status != "" {
				s = r.status
			}
		}
		return s
	}
	for _, c := range []struct{ res, table string }{{"purchase_order", "purchase_orders"}, {"journal", "journals"}} {
		rows, err := e.db.Query(`SELECT id, status FROM ` + c.table)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var id int64
			var st string
			if err := rows.Scan(&id, &st); err != nil {
				t.Fatal(err)
			}
			if got := lastStatus(fmt.Sprintf("%s:%d", c.res, id)); got != st {
				t.Errorf("%s: %s:%d 의 마지막 상태 줄 %q, 표 %q", label, c.res, id, got, st)
			}
		}
		rows.Close()
	}
	after := func(k string, ops ...string) bool {
		evs := feed[k]
		for _, r := range evs[1:] {
			for _, op := range ops {
				if r.op == op {
					return true
				}
			}
		}
		return false
	}
	for _, id := range e.ids(t, `SELECT id FROM payables WHERE paid_amount > 0 OR reserved_amount > 0 OR canceled = 1`) {
		if !after(fmt.Sprintf("payable:%d", id), "updated", "canceled") {
			t.Errorf("%s: 채무 항목 %d 의 지급·예약·취소 줄이 없다", label, id)
		}
	}
	for _, id := range e.ids(t, `SELECT DISTINCT item_id FROM stock_ledger`) {
		if !after(fmt.Sprintf("item:%d", id), "updated") {
			t.Errorf("%s: 품목 %d 의 재고 변경 줄이 없다", label, id)
		}
	}
}

func TestChangeFeedCompletenessProperty(t *testing.T) {
	for _, seed := range []int64{1, 2, 3} {
		seed := seed
		t.Run(fmt.Sprintf("seed%d", seed), func(t *testing.T) {
			e := newEnv(t)
			e.payForm(t)
			bank := e.bank(t, "주거래")
			v := e.vendor(t)
			rnd := rand.New(rand.NewSource(seed))
			var openPOs []domain.PurchaseOrder
			var payables []int64
			step := 0
			for ; step < 14; step++ {
				switch rnd.Intn(8) {
				case 0:
					e.item(t, fmt.Sprintf("PROP-%d-%d", seed, step), 0, false)
				case 1:
					if _, err := e.inventory.CreateVendor(as(uFinance), domain.Vendor{Name: fmt.Sprintf("속성상사%d-%d", seed, step), BusinessNo: fmt.Sprintf("40%d-00-%05d", seed, step)}); err != nil {
						t.Fatal(err)
					}
				case 2:
					openPOs = append(openPOs, e.orderFor(t, v, "2026-12-01", 1_000, int64(2+rnd.Intn(3))))
				case 3: // 부분 입고(롤백되는 초과 입고 시도 포함)
					if len(openPOs) > 0 {
						po := openPOs[rnd.Intn(len(openPOs))]
						_, _ = e.inventory.Receive(as(uFinance), recv(po.ID, po.Lines[0].ID, po.Lines[0].Quantity+50, "", "")) // 초과 → 롤백
						_, _ = e.inventory.Receive(as(uFinance), recv(po.ID, po.Lines[0].ID, 1, "", ""))
					}
				case 4:
					payables = append(payables, e.payable(t, v.ID, int64(10_000*(1+rnd.Intn(5)))))
				case 5: // 지급 요청→승인→(미래 지급일로 롤백)→지급 처리
					if len(payables) > 0 {
						pid := payables[len(payables)-1]
						payables = payables[:len(payables)-1]
						amt := e.payableAmount(t, pid)
						p := e.requested(t, e.payInput(v, bank.ID, domain.PaymentItemInput{PayableID: pid, Amount: amt}))
						e.approvePay(t, p)
						_, _ = e.payments.Settle(as(uFinance2), p.No, domain.SettleInput{PaidDate: "2999-01-01"}) // 롤백
						if _, err := e.payments.Settle(as(uFinance2), p.No, domain.SettleInput{}); err != nil {
							t.Fatal(err)
						}
					}
				case 6: // 전표 초안 → 취소
					j, err := e.journals.Create(as(uFinance), domain.JournalInput{PostingDate: "2026-11-10", TradeDate: "2026-11-05", Memo: "속성 시험", Lines: []domain.JournalLineInput{
						{AccountCode: "1210", Side: "D", Amount: 5_000}, {AccountCode: "2110", Side: "C", Amount: 5_000, VendorID: v.ID}}})
					if err != nil {
						t.Fatal(err)
					}
					if rnd.Intn(2) == 0 {
						if _, err := e.journals.Cancel(as(uFinance), j.No, "속성 시험 취소"); err != nil {
							t.Fatal(err)
						}
					}
				case 7: // 승인 요청 도중 롤백되는 작성(없는 계정)
					_, _ = e.journals.Create(as(uFinance), domain.JournalInput{PostingDate: "2026-11-10", TradeDate: "2026-11-05", Lines: []domain.JournalLineInput{
						{AccountCode: "9999", Side: "D", Amount: 1}, {AccountCode: "2110", Side: "C", Amount: 1}}})
				}
				e.checkFeed(t, fmt.Sprintf("seed %d step %d", seed, step))
			}
			if n := len(e.feed(t)); n < 10 {
				t.Fatalf("시나리오가 피드를 거의 만들지 못했다: 객체 %d 개", n)
			}
		})
	}
}

func (e *env) payableAmount(t *testing.T, id int64) int64 {
	t.Helper()
	var n int64
	if err := e.db.QueryRow(`SELECT amount FROM payables WHERE id = ?`, id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
