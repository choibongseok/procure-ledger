package service_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/repository"
	"github.com/choibongseok/procure-ledger/internal/service"
)

// orderedPO 는 수량 qty 짜리 PO 를 만든다(승인까지 마친 상태).
func orderedPO(t *testing.T, e *env, it domain.Item, qty int64) domain.PurchaseOrder {
	t.Helper()
	v := e.vendor(t)
	pr, err := e.purchase.CreateDraft(as(uStaff), domain.DraftInput{VendorID: v.ID, Title: "발주", Lines: []domain.PRLineInput{{ItemID: it.ID, Quantity: qty, UnitPrice: 1000}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.purchase.Submit(as(uStaff), pr.ID); err != nil {
		t.Fatal(err)
	}
	res := approve(t, e, uLead, pr.ID)
	if res.PurchaseOrder == nil {
		t.Fatal("PO 없음")
	}
	return *res.PurchaseOrder
}

func recv(poID, lineID, qty int64, lot, exp string) domain.ReceiveInput {
	return domain.ReceiveInput{POID: poID, Lines: []domain.ReceiptLine{{POLineID: lineID, Quantity: qty, LotNo: lot, ExpiryDate: exp}}}
}

func TestPartialReceiptAndLedgerSum(t *testing.T) {
	e := newEnv(t)
	it := e.item(t, "PAPER", 100, true)
	po := orderedPO(t, e, it, 10)
	line := po.Lines[0].ID

	rc, err := e.inventory.Receive(as(uFinance), recv(po.ID, line, 4, "L1", "2027-01-31"))
	if err != nil || len(rc.Lines) != 1 || rc.Lines[0].LotNo != "L1" || rc.Lines[0].ExpiryDate != "2027-01-31" {
		t.Fatalf("1차 입고: %+v %v", rc, err)
	}
	if got, _ := e.inventory.GetPurchaseOrder(as(uStaff), po.ID); got.Status != domain.POPartial || got.Lines[0].ReceivedQty != 4 {
		t.Fatalf("부분 입고 후 PO: %+v", got)
	}
	if _, err = e.inventory.Receive(as(uFinance), recv(po.ID, line, 6, "L2", "2027-06-30")); err != nil {
		t.Fatal(err)
	}
	if got, _ := e.inventory.GetPurchaseOrder(as(uStaff), po.ID); got.Status != domain.POReceived {
		t.Fatalf("전량 입고 후 PO: %s", got.Status)
	}

	st, err := e.inventory.GetStock(as(uStaff), it.ID)
	if err != nil || st.OnHand != 10 || len(st.Lots) != 2 || st.Lots[0].LotNo != "L1" || st.Lots[0].Quantity != 4 {
		t.Fatalf("재고: %+v %v", st, err)
	}
	// 재고는 원장 합과 같다.
	if n := count(t, e.db, `SELECT COALESCE(SUM(qty_delta),0) FROM stock_ledger WHERE item_id=?`, it.ID); n != 10 {
		t.Fatalf("원장 합 %d", n)
	}
	// 원장은 수정·삭제할 수 없다.
	if _, err := e.db.Exec(`UPDATE stock_ledger SET qty_delta = 99`); err == nil {
		t.Fatal("원장 수정이 가능함")
	}
	if _, err := e.db.Exec(`DELETE FROM stock_ledger`); err == nil {
		t.Fatal("원장 삭제가 가능함")
	}
	if !e.rec.Has("receipt.create", audit.TargetReceipt, audit.ResultSuccess) {
		t.Fatal("입고 감사 기록 없음")
	}
}

func TestOverReceiptRejectedAndRolledBack(t *testing.T) {
	e := newEnv(t)
	it := e.item(t, "OVER", 0, false)
	po := orderedPO(t, e, it, 10)
	line := po.Lines[0].ID

	if _, err := e.inventory.Receive(as(uFinance), recv(po.ID, line, 7, "", "")); err != nil {
		t.Fatal(err)
	}
	_, err := e.inventory.Receive(as(uFinance), recv(po.ID, line, 4, "", ""))
	is(t, err, contract.ErrOverReceipt)
	// 한 입고 안에서 같은 줄을 나눠 넣어 우회해도 누계로 막힌다. 앞 줄까지 전부 롤백된다.
	_, err = e.inventory.Receive(as(uFinance), domain.ReceiveInput{POID: po.ID, Lines: []domain.ReceiptLine{
		{POLineID: line, Quantity: 2}, {POLineID: line, Quantity: 2},
	}})
	is(t, err, contract.ErrOverReceipt)

	st, _ := e.inventory.GetStock(as(uStaff), it.ID)
	if st.OnHand != 7 || count(t, e.db, `SELECT COUNT(*) FROM receipts`) != 1 {
		t.Fatalf("거부된 입고가 남음: 재고 %d, 입고 %d건", st.OnHand, count(t, e.db, `SELECT COUNT(*) FROM receipts`))
	}
	// 정확히 잔량만큼은 들어간다.
	if _, err = e.inventory.Receive(as(uFinance), recv(po.ID, line, 3, "", "")); err != nil {
		t.Fatal(err)
	}
	_, err = e.inventory.Receive(as(uFinance), recv(po.ID, line, 1, "", ""))
	is(t, err, contract.ErrOverReceipt)

	// 서비스 검사를 건너뛰고 SQL 로 넣어도 DB 트리거가 막는다.
	if _, err := e.db.Exec(`INSERT INTO receipts (po_id, received_by, received_at) VALUES (?,?, 'x')`, po.ID, uFinance); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.Exec(`INSERT INTO receipt_lines (receipt_id, po_line_id, item_id, quantity) VALUES (99, ?, ?, 1)`, line, it.ID); err == nil {
		t.Fatal("트리거가 초과 입고를 막지 못함")
	}
}

// 동시 입고 두 건 이상이 잔량을 초과하지 못한다. 파일 DB 에서 고루틴으로 한꺼번에 밀어 넣는다.
func TestConcurrentReceiptsCannotExceedPO(t *testing.T) {
	e := newEnvAt(t, "concurrent.db")
	it := e.item(t, "RACE", 0, false)
	po := orderedPO(t, e, it, 10)
	line := po.Lines[0].ID

	const workers, each = 12, 3 // 12 x 3 = 36 시도, 잔량 10 → 3건만 성공(9) 해야 함
	var wg sync.WaitGroup
	var mu sync.Mutex
	ok, over := 0, 0
	var other []error
	start := make(chan struct{})
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := e.inventory.Receive(as(uFinance), recv(po.ID, line, each, "", ""))
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				ok++
			case isErr(err, contract.ErrOverReceipt):
				over++
			default:
				other = append(other, err)
			}
		}()
	}
	close(start)
	wg.Wait()
	if len(other) > 0 {
		t.Fatalf("예상 밖 오류: %v", other)
	}
	if ok != 3 || over != workers-3 {
		t.Fatalf("성공 %d, 초과 거부 %d (기대 3, %d)", ok, over, workers-3)
	}
	st, _ := e.inventory.GetStock(as(uStaff), it.ID)
	got, _ := e.inventory.GetPurchaseOrder(as(uStaff), po.ID)
	if st.OnHand != 9 || got.Lines[0].ReceivedQty != 9 || got.Status != domain.POPartial {
		t.Fatalf("재고 %d, 입고 누계 %d, PO %s", st.OnHand, got.Lines[0].ReceivedQty, got.Status)
	}
}

func TestReceiveValidation(t *testing.T) {
	e := newEnv(t)
	lotItem := e.item(t, "LOT", 0, true)
	po := orderedPO(t, e, lotItem, 10)
	other := orderedPO(t, e, e.item(t, "OTHER", 0, false), 5)
	line := po.Lines[0].ID

	cases := map[string]domain.ReceiveInput{
		"로트 번호 없음": recv(po.ID, line, 1, "", ""),
		"유효기한 형식":  recv(po.ID, line, 1, "L1", "2027/01/01"),
		"수량 0":     recv(po.ID, line, 0, "L1", ""),
		"줄 없음":     {POID: po.ID},
		"다른 발주의 줄": recv(po.ID, other.Lines[0].ID, 1, "", ""),
		"없는 줄":     recv(po.ID, 9999, 1, "L1", ""),
		"품목 불일치":   {POID: po.ID, Lines: []domain.ReceiptLine{{POLineID: line, ItemID: 9999, Quantity: 1, LotNo: "L1"}}},
	}
	for name, in := range cases {
		if _, err := e.inventory.Receive(as(uFinance), in); !isErr(err, contract.ErrValidation) {
			t.Errorf("%s: 입력 오류 기대, %v", name, err)
		}
	}
	_, err := e.inventory.Receive(as(uFinance), recv(9999, line, 1, "L1", ""))
	is(t, err, contract.ErrNotFound)

	// 같은 로트에 다른 유효기한을 주면 거부.
	if _, err := e.inventory.Receive(as(uFinance), recv(po.ID, line, 1, "L1", "2027-01-01")); err != nil {
		t.Fatal(err)
	}
	_, err = e.inventory.Receive(as(uFinance), recv(po.ID, line, 1, "L1", "2027-02-02"))
	is(t, err, contract.ErrValidation)
	if n := count(t, e.db, `SELECT COUNT(*) FROM receipts`); n != 1 {
		t.Fatalf("검증 실패한 입고가 남음: %d", n)
	}
}

func TestReceiveRequiresPermissionAndIsAudited(t *testing.T) {
	e := newEnv(t)
	it := e.item(t, "PERM", 0, false)
	po := orderedPO(t, e, it, 5)
	for _, id := range []int64{uStaff, uLead, uAuditor} {
		before := e.rec.Count()
		_, err := e.inventory.Receive(as(id), recv(po.ID, po.Lines[0].ID, 1, "", ""))
		is(t, err, contract.ErrForbidden)
		if e.rec.Count() != before+1 || !e.rec.Has("receipt.create", audit.TargetReceipt, audit.ResultDenied) {
			t.Fatalf("사용자 %d 거부 기록 없음", id)
		}
	}
	if count(t, e.db, `SELECT COUNT(*) FROM stock_ledger`) != 0 {
		t.Fatal("권한 없는 입고가 원장에 남음")
	}
	// 품목·거래처 등록도 inventory:manage.
	_, err := e.inventory.CreateItem(as(uStaff), service.ItemInput{SKU: "X", Name: "x"})
	is(t, err, contract.ErrForbidden)
	_, err = e.inventory.CreateVendor(as(uStaff), domain.Vendor{Name: "x"})
	is(t, err, contract.ErrForbidden)

	// 감사 기록이 실패하면 입고도 실패하고 아무것도 남지 않는다.
	e.rec.FailWith(errBoom)
	_, err = e.inventory.Receive(as(uFinance), recv(po.ID, po.Lines[0].ID, 1, "", ""))
	is(t, err, contract.ErrAuditFailed)
	if count(t, e.db, `SELECT COUNT(*) FROM stock_ledger`)+count(t, e.db, `SELECT COUNT(*) FROM receipts`) != 0 {
		t.Fatal("감사 실패인데 입고가 남음")
	}
	e.rec.FailWith(nil)
	if _, err = e.inventory.Receive(as(uFinance), recv(po.ID, po.Lines[0].ID, 1, "", "")); err != nil {
		t.Fatal(err)
	}
}

func TestBelowSafetyAndExpiringLists(t *testing.T) {
	e := newEnv(t)
	low := e.item(t, "LOW", 50, true)     // 안전재고 50, 입고 10 → 미달
	fine := e.item(t, "FINE", 5, false)   // 안전재고 5, 입고 8 → 충족
	empty := e.item(t, "EMPTY", 1, false) // 입고 없음 → 미달
	poLow, poFine := orderedPO(t, e, low, 10), orderedPO(t, e, fine, 8)

	today := e.now.Format("2006-01-02")
	soon := e.now.AddDate(0, 0, 10).Format("2006-01-02")
	later := e.now.AddDate(0, 0, 90).Format("2006-01-02")
	past := e.now.AddDate(0, 0, -3).Format("2006-01-02")
	_ = today
	if _, err := e.inventory.Receive(as(uFinance), domain.ReceiveInput{POID: poLow.ID, Lines: []domain.ReceiptLine{
		{POLineID: poLow.Lines[0].ID, Quantity: 4, LotNo: "SOON", ExpiryDate: soon},
		{POLineID: poLow.Lines[0].ID, Quantity: 3, LotNo: "LATER", ExpiryDate: later},
		{POLineID: poLow.Lines[0].ID, Quantity: 3, LotNo: "PAST", ExpiryDate: past},
	}}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.inventory.Receive(as(uFinance), recv(poFine.ID, poFine.Lines[0].ID, 8, "", "")); err != nil {
		t.Fatal(err)
	}

	below, err := e.inventory.ListBelowSafety(as(uStaff))
	if err != nil || len(below) != 2 || below[0].Item.ID != low.ID || below[1].Item.ID != empty.ID || !below[0].BelowSafety {
		t.Fatalf("안전재고 미달: %+v %v", below, err)
	}
	if below[0].OnHand != 10 || below[1].OnHand != 0 {
		t.Fatalf("미달 수량: %d %d", below[0].OnHand, below[1].OnHand)
	}

	exp, err := e.inventory.ListExpiring(as(uStaff), 30)
	if err != nil || len(exp) != 2 {
		t.Fatalf("임박(30일): %+v %v", exp, err)
	}
	if exp[0].Lot.LotNo != "PAST" || exp[0].DaysToExpire != -3 || exp[1].Lot.LotNo != "SOON" || exp[1].DaysToExpire != 10 {
		t.Fatalf("임박 목록 내용: %+v", exp)
	}
	if exp, _ = e.inventory.ListExpiring(as(uStaff), 100); len(exp) != 3 {
		t.Fatalf("임박(100일) %d건", len(exp))
	}
	_, err = e.inventory.ListExpiring(as(uStaff), -1)
	is(t, err, contract.ErrValidation)
	_ = time.Second
}

func TestItemSearchAndMasterRules(t *testing.T) {
	e := newEnv(t)
	e.item(t, "A-100", 0, false)
	e.item(t, "B_200", 0, false)
	got, err := e.inventory.SearchItems(as(uStaff), "a-1", 10)
	if err != nil || len(got) != 1 || got[0].SKU != "A-100" {
		t.Fatalf("검색: %+v %v", got, err)
	}
	if got, _ = e.inventory.SearchItems(as(uStaff), "%", 10); len(got) != 0 {
		t.Fatalf("와일드카드가 이스케이프되지 않음: %d건", len(got))
	}
	if got, _ = e.inventory.SearchItems(as(uStaff), "", 10); len(got) != 2 {
		t.Fatalf("전체 검색 %d건", len(got))
	}
	// SKU 중복·검증.
	_, err = e.inventory.CreateItem(as(uFinance), service.ItemInput{SKU: "A-100", Name: "중복"})
	is(t, err, contract.ErrValidation)
	_, err = e.inventory.CreateItem(as(uFinance), service.ItemInput{SKU: "", Name: "x"})
	is(t, err, contract.ErrValidation)
	_, err = e.inventory.CreateItem(as(uFinance), service.ItemInput{SKU: "N", Name: "x", SafetyStock: -1})
	is(t, err, contract.ErrValidation)
	// 없는 품목 재고.
	_, err = e.inventory.GetStock(as(uStaff), 9999)
	is(t, err, contract.ErrNotFound)
	// 미인증.
	_, err = e.inventory.SearchItems(contractNoUser(), "", 1)
	is(t, err, contract.ErrUnauthenticated)
}

func TestVendorFieldsMaskedByRole(t *testing.T) {
	e := newEnv(t)
	e.vendor(t)
	staffView, err := e.inventory.ListVendors(as(uStaff))
	if err != nil || len(staffView) != 1 {
		t.Fatalf("%v %v", staffView, err)
	}
	if staffView[0].BusinessNo == "123-45-67890" || staffView[0].AccountNo == "110-123-456789" || staffView[0].Phone == "02-123-4567" {
		t.Fatalf("직원에게 원문 노출: %+v", staffView[0])
	}
	finView, _ := e.inventory.ListVendors(as(uFinance))
	if finView[0].BusinessNo != "123-45-67890" {
		t.Fatalf("재무는 원문을 봐야 함: %+v", finView[0])
	}
	// DB 에는 구분 필드로 원문이 저장된다.
	if n := count(t, e.db, `SELECT COUNT(*) FROM vendors WHERE business_no='123-45-67890' AND account_no<>''`); n != 1 {
		t.Fatal("구분 필드 저장 확인 실패")
	}
	// 감사 기록에는 민감 값이 없다.
	for _, r := range e.rec.Records() {
		for _, d := range r.Diff {
			if d.New == "123-45-67890" {
				t.Fatal("감사 로그에 사업자번호 원문")
			}
		}
	}
}

// PO·입고 조회는 원 구매요청의 부서를 따른다(PR 과 같은 규칙).
func TestPurchaseOrderAndReceiptFollowRequestDepartment(t *testing.T) {
	e := newEnv(t)
	it := e.item(t, "ISO", 0, false)
	po := orderedPO(t, e, it, 10) // 구매팀(1) 요청
	rc, err := e.inventory.Receive(as(uFinance), recv(po.ID, po.Lines[0].ID, 4, "", ""))
	if err != nil {
		t.Fatal(err)
	}

	// 타 부서(영업팀) 직원·팀장: 단건은 존재를 숨기고 목록에서는 빠진다.
	for _, id := range []int64{uStaff2, uLead2} {
		_, err := e.inventory.GetPurchaseOrder(as(id), po.ID)
		is(t, err, contract.ErrNotFound)
		_, err = e.inventory.GetReceipt(as(id), rc.ID)
		is(t, err, contract.ErrNotFound)
		list, err := e.inventory.ListPurchaseOrders(as(id), domain.POFilter{})
		if err != nil || len(list) != 0 {
			t.Fatalf("사용자 %d 에게 타 부서 PO 목록 노출: %v %v", id, list, err)
		}
	}
	// 타 부서 직원은 입고 처리도 못 한다(권한 거부).
	_, err = e.inventory.Receive(as(uStaff2), recv(po.ID, po.Lines[0].ID, 1, "", ""))
	is(t, err, contract.ErrForbidden)

	// 같은 부서와 전 부서 조회 권한(재무·관리자·감사)은 본다.
	for _, id := range []int64{uStaff, uLead, uHead, uFinance, uAdmin, uAuditor} {
		if got, err := e.inventory.GetPurchaseOrder(as(id), po.ID); err != nil || got.ID != po.ID {
			t.Fatalf("사용자 %d PO 조회: %v", id, err)
		}
		if got, err := e.inventory.GetReceipt(as(id), rc.ID); err != nil || got.ID != rc.ID {
			t.Fatalf("사용자 %d 입고 조회: %v", id, err)
		}
		if list, err := e.inventory.ListPurchaseOrders(as(id), domain.POFilter{}); err != nil || len(list) != 1 {
			t.Fatalf("사용자 %d PO 목록: %v %v", id, list, err)
		}
	}
	// 재고 수량 조회는 부서와 무관하다.
	if st, err := e.inventory.GetStock(as(uStaff2), it.ID); err != nil || st.OnHand != 4 {
		t.Fatalf("재고 조회: %+v %v", st, err)
	}

	// 서비스 검사를 건너뛰어도 저장소 조건절이 막는다.
	repo := repository.SQLInventory{}
	if _, err := repo.GetPO(context.Background(), e.db, po.ID, repository.ViewScope{UserID: uStaff2, DeptID: deptSales}); !isErr(err, contract.ErrNotFound) {
		t.Fatalf("저장소 PO 조건절: %v", err)
	}
	if _, err := repo.GetReceipt(context.Background(), e.db, rc.ID, repository.ViewScope{UserID: uStaff2, DeptID: deptSales}); !isErr(err, contract.ErrNotFound) {
		t.Fatalf("저장소 입고 조건절: %v", err)
	}
	if l, _ := repo.ListPOs(context.Background(), e.db, "", 10, 0, repository.ViewScope{UserID: uStaff2, DeptID: deptSales}); len(l) != 0 {
		t.Fatal("저장소 목록 조건절")
	}
	if _, err := repo.GetPO(context.Background(), e.db, po.ID, repository.ViewScope{UserID: uStaff, DeptID: deptBuy}); err != nil {
		t.Fatalf("같은 부서 저장소 조회: %v", err)
	}
}
