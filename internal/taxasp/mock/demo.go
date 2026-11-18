package mock

import (
	"time"

	"github.com/choibongseok/procure-ledger/internal/taxasp"
)

// 시연용 가상 수신 건(B43 2단계, D18). 시연 데이터(internal/demo)의 거래처 사업자번호(000-00-00001~3)와 발주 금액에 맞춘 건과, 일부러 어긋나게 만든 건이 섞여 있다.
// 모두 가상이다(승인번호는 결정적이고 실제 국세청 번호가 아니다). 작성일은 기준일(KST 오늘)에서 며칠 전으로 잡는다.

// DemoReceived 는 buyerBiz 가 공급받는자인 시연용 수신 건 목록이다. today 는 YYYY-MM-DD 로 쓸 기준일이다.
func DemoReceived(buyerBiz string, today time.Time) []taxasp.Received {
	buyer := taxasp.Party{BizNo: buyerBiz, Name: "에이엑스 주식회사", Rep: "대표"}
	mk := func(daysAgo int, supplierBiz, name string, supply, tax int64, memo string) taxasp.Received {
		d := today.AddDate(0, 0, -daysAgo).Format("2006-01-02")
		sup := taxasp.Party{BizNo: supplierBiz, Name: name, Rep: "대표"}
		inv := taxasp.Invoice{TaxType: "taxable", IssueDate: d, Supplier: sup, Buyer: buyer, SupplyTotal: supply, TaxTotal: tax, Total: supply + tax, ChargeType: "claim", Remark: memo,
			Lines: []taxasp.Line{{Day: d[5:], Name: memo, Qty: 1, UnitPrice: supply, Supply: supply, Tax: tax}}}
		return taxasp.Received{ApprovalNo: approvalNo(d, supplierBiz, memo, int64(daysAgo)+7), Invoice: inv}
	}
	return []taxasp.Received{
		mk(2, "0000000002", "대성전자", 3_000_000, 300_000, "모니터 구매분"),    // 발주·입고와 정확히 일치하는 건
		mk(3, "0000000003", "미래사무기기", 1_000_000, 100_000, "복사용지 구매분"), // 일치
		mk(4, "0000000003", "미래사무기기", 410_000, 41_000, "토너 구매분"),      // 공급가액 불일치(입고 합과 다름)
		mk(5, "0000000001", "한빛상사", 500_000, 50_000, "입고 전 거래분"),      // 입고 없음
		mk(6, "0000000009", "미등록상사", 120_000, 12_000, "거래처에 없는 공급자"),  // 거래처 미등록
	}
}

// seedDemo 는 시연용 수신 건을 한 번만 채운다(이미 채웠으면 아무것도 하지 않는다).
func (s *Store) seedDemo(buyerBiz string) {
	s.mu.Lock()
	if s.demoSeeded {
		s.mu.Unlock()
		return
	}
	s.demoSeeded = true
	s.mu.Unlock()
	s.Seed(DemoReceived(buyerBiz, time.Now().In(kst))...)
}

var kst = time.FixedZone("KST", 9*60*60)
