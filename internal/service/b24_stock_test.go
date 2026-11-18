package service_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/contract/fake"
	"github.com/choibongseok/procure-ledger/internal/dbutil"
	"github.com/choibongseok/procure-ledger/internal/service"
)

type stockSvcs struct {
	wh *service.WarehouseService
	is *service.StockIssueService
	tr *service.StockTransferService
}

func (e *env) stock() stockSvcs {
	b := service.Base{DB: e.db, Txs: contract.SQLTxManager{DB: e.db}, Audit: e.rec, Masker: fake.Masker{}, Org: leftAwareOrg{e.org}, Now: func() time.Time { return e.now }}
	return stockSvcs{service.NewWarehouseService(b), service.NewStockIssueService(b), service.NewStockTransferService(b)}
}

// seedLot 은 로트와 그 입고 원장 행을 직접 넣는다(입고 처리 경로는 별도 테스트가 있다). receivedAt 으로 선입선출 순서를 정한다.
func (e *env) seedLot(t *testing.T, itemID int64, lotNo, expiry string, whID, qty int64, receivedAt string) int64 {
	t.Helper()
	var exp any
	if expiry != "" {
		exp = expiry
	}
	lotID, err := dbutil.InsertID(context.Background(), e.db, `INSERT INTO lots (item_id, lot_no, expiry_date) VALUES (?,?,?)`, itemID, lotNo, exp)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.Exec(`INSERT INTO stock_ledger (item_id, lot_id, qty_delta, reason, ref_type, ref_id, actor_id, created_at, warehouse_id) VALUES (?,?,?,'receipt','receipt',1,?,?,?)`,
		itemID, lotID, qty, uFinance, receivedAt, whID); err != nil {
		t.Fatal(err)
	}
	return lotID
}

func (e *env) seedPlain(t *testing.T, itemID, whID, qty int64) {
	t.Helper()
	if _, err := e.db.Exec(`INSERT INTO stock_ledger (item_id, qty_delta, reason, ref_type, ref_id, actor_id, warehouse_id, created_at) VALUES (?,?,'receipt','receipt',1,?,?,'2026-09-01T00:00:00Z')`, itemID, qty, uFinance, whID); err != nil {
		t.Fatal(err)
	}
}

func whQty(t *testing.T, e *env, whID, itemID, lotID int64) int64 {
	t.Helper()
	return int64(count(t, e.db, `SELECT COALESCE(SUM(qty_delta),0) FROM stock_ledger WHERE warehouse_id = ? AND item_id = ? AND COALESCE(lot_id,0) = ?`, whID, itemID, lotID))
}

// AC-400 창고 마스터: 형식·유일 검증, 재고가 남은 창고·마지막 사용 창고 중지 거부, 재사용, 권한, 감사.
func TestWarehouseMaster(t *testing.T) {
	e := newEnv(t)
	s := e.stock()
	list, err := s.wh.List(as(uStaff))
	if err != nil || len(list) != 1 || list[0].Code != "WH-01" || !list[0].Active {
		t.Fatalf("시드 창고: %+v %v", list, err)
	}
	if _, err := s.wh.Save(as(uStaff), domain.WarehouseInput{Code: "WH-02", Name: "물류센터"}); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("직원의 창고 등록: %v", err)
	}
	for _, bad := range []domain.WarehouseInput{{Code: "w", Name: "x"}, {Code: "WH 02", Name: "x"}, {Code: "WH-02", Name: ""}, {Code: "WH-02", Name: strings.Repeat("가", 41)}} {
		if _, err := s.wh.Save(as(uFinance), bad); !errors.Is(err, contract.ErrValidation) {
			t.Fatalf("잘못된 입력 %+v: %v", bad, err)
		}
	}
	w2, err := s.wh.Save(as(uFinance), domain.WarehouseInput{Code: "wh-02", Name: "물류센터", Location: "용인 A동"})
	if err != nil || w2.Code != "WH-02" || !w2.Active {
		t.Fatalf("등록: %+v %v", w2, err)
	}
	if _, err := s.wh.Save(as(uFinance), domain.WarehouseInput{Code: "WH-02", Name: "다른 이름"}); !errors.Is(err, contract.ErrValidation) {
		t.Fatalf("중복 코드: %v", err)
	}
	w2, err = s.wh.Save(as(uAdmin), domain.WarehouseInput{ID: w2.ID, Code: "WH-02", Name: "물류센터", Location: "용인 B동"})
	if err != nil || w2.Location != "용인 B동" {
		t.Fatalf("수정: %+v %v", w2, err)
	}
	it := e.item(t, "WH-ITEM", 0, false)
	e.seedPlain(t, it.ID, w2.ID, 5)
	if err := s.wh.SetActive(as(uFinance), w2.ID, false); !errors.Is(err, contract.ErrInvalidState) || !strings.Contains(err.Error(), "재고가 남아 있는 창고는 사용을 중지할 수 없습니다") {
		t.Fatalf("재고가 남은 창고 중지: %v", err)
	}
	if list, _ = s.wh.List(as(uStaff)); list[1].ItemCount != 1 {
		t.Fatalf("품목 수: %+v", list)
	}
	e.seedPlain(t, it.ID, w2.ID, -5)
	if err := s.wh.SetActive(as(uFinance), w2.ID, false); err != nil {
		t.Fatalf("재고 0 인 창고 중지: %v", err)
	}
	if err := s.wh.SetActive(as(uFinance), 1, false); !errors.Is(err, contract.ErrInvalidState) || !strings.Contains(err.Error(), "하나 이상") {
		t.Fatalf("마지막 사용 창고 중지: %v", err)
	}
	if err := s.wh.SetActive(as(uFinance), w2.ID, true); err != nil {
		t.Fatalf("재사용: %v", err)
	}
	for _, a := range []string{"warehouse.create", "warehouse.update", "warehouse.deactivate", "warehouse.activate"} {
		if !e.rec.Has(a, "warehouse", "success") {
			t.Fatalf("감사 %s 없음", a)
		}
	}
}

// AC-411 로트 배분 제안: 선입선출(첫 입고 시각 순), 유효기한 임박 우선(기한 없는 로트는 뒤), 지난 로트 제외, 재고 부족 안내.
func TestIssueProposals(t *testing.T) {
	e := newEnv(t)
	s := e.stock()
	it := e.item(t, "ISS-LOT", 0, true)
	a := e.seedLot(t, it.ID, "L-A", "2027-06-30", 1, 30, "2026-09-10T00:00:00Z") // 먼저 입고, 기한 늦음
	b := e.seedLot(t, it.ID, "L-B", "2026-12-18", 1, 30, "2026-09-24T00:00:00Z") // 나중 입고, 기한 빠름
	c := e.seedLot(t, it.ID, "L-C", "", 1, 40, "2026-11-03T00:00:00Z")           // 기한 없음
	e.seedLot(t, it.ID, "L-X", "2026-01-01", 1, 99, "2026-01-02T00:00:00Z")      // 이미 지남
	ids := func(al []domain.IssueAllocation) []int64 {
		var out []int64
		for _, x := range al {
			out = append(out, x.LotID)
		}
		return out
	}
	fifo, err := s.is.ProposeIssue(as(uFinance), domain.IssueProposeInput{WarehouseID: 1, ItemID: it.ID, Qty: 50, Method: domain.IssueFIFO})
	if err != nil || len(fifo) != 2 || fifo[0].LotID != a || fifo[0].Qty != 30 || fifo[1].LotID != b || fifo[1].Qty != 20 {
		t.Fatalf("선입선출: %+v %v", fifo, err)
	}
	exp, err := s.is.ProposeIssue(as(uFinance), domain.IssueProposeInput{WarehouseID: 1, ItemID: it.ID, Qty: 50, Method: domain.IssueExpiry})
	if err != nil || len(exp) != 2 || exp[0].LotID != b || exp[0].Qty != 30 || exp[1].LotID != a || exp[1].Qty != 20 {
		t.Fatalf("유효기한 임박 우선: %+v %v", exp, err)
	}
	all, _ := s.is.ProposeIssue(as(uFinance), domain.IssueProposeInput{WarehouseID: 1, ItemID: it.ID, Qty: 100, Method: domain.IssueExpiry})
	if got := ids(all); len(got) != 3 || got[2] != c {
		t.Fatalf("기한 없는 로트는 맨 뒤, 지난 로트 제외: %v", got)
	}
	if _, err := s.is.ProposeIssue(as(uFinance), domain.IssueProposeInput{WarehouseID: 1, ItemID: it.ID, Qty: 101, Method: domain.IssueFIFO}); !errors.Is(err, contract.ErrValidation) || !strings.Contains(err.Error(), "부족") {
		t.Fatalf("재고 부족(지난 로트 재고는 못 씀): %v", err)
	}
	if _, err := s.is.ProposeIssue(as(uStaff), domain.IssueProposeInput{WarehouseID: 1, ItemID: it.ID, Qty: 1, Method: domain.IssueFIFO}); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("직원: %v", err)
	}
	plain := e.item(t, "ISS-PLAIN", 0, false)
	e.seedPlain(t, plain.ID, 1, 12)
	p, err := s.is.ProposeIssue(as(uFinance), domain.IssueProposeInput{WarehouseID: 1, ItemID: plain.ID, Qty: 8, Method: domain.IssueFIFO})
	if err != nil || len(p) != 1 || p[0].LotID != 0 || p[0].Qty != 8 {
		t.Fatalf("로트 없는 품목: %+v %v", p, err)
	}
}

// AC-410 출고 처리: 번호 채번·원장 출고 행·검증 거부(재고 부족·로트 규칙·유효기한·중복·부서·창고·날짜)·롤백.
func TestIssueProcessAndRejections(t *testing.T) {
	e := newEnv(t)
	s := e.stock()
	it := e.item(t, "OUT-LOT", 0, true)
	a := e.seedLot(t, it.ID, "L-A", "2027-06-30", 1, 30, "2026-09-10T00:00:00Z")
	x := e.seedLot(t, it.ID, "L-X", "2026-01-01", 1, 10, "2026-01-02T00:00:00Z") // 유효기한 지남
	plain := e.item(t, "OUT-PLAIN", 0, false)
	e.seedPlain(t, plain.ID, 1, 20)
	ok := domain.IssueInput{DepartmentID: deptSales, WarehouseID: 1, Method: domain.IssueFIFO, Memo: "영업 사용",
		Lines: []domain.IssueLineInput{{ItemID: it.ID, LotID: a, Qty: 10}, {ItemID: plain.ID, Qty: 5}}}
	slip, err := s.is.Issue(as(uFinance), ok)
	if err != nil || !strings.HasPrefix(slip.No, "OUT-2026-") || len(slip.Lines) != 2 || slip.DepartmentName == "" {
		t.Fatalf("출고: %+v %v", slip, err)
	}
	if whQty(t, e, 1, it.ID, a) != 20 || whQty(t, e, 1, plain.ID, 0) != 15 {
		t.Fatal("원장에 출고 행이 반영되지 않음")
	}
	if n := count(t, e.db, `SELECT COUNT(*) FROM stock_ledger WHERE reason='issue' AND ref_type='issue_slip' AND ref_id=? AND qty_delta < 0 AND warehouse_id = 1`, slip.ID); n != 2 {
		t.Fatalf("출고 원장 행 %d", n)
	}
	if !e.rec.Has("stock_issue.create", "stock_issue", "success") {
		t.Fatal("감사 없음")
	}
	slip2, _ := s.is.Issue(as(uAdmin), domain.IssueInput{DepartmentID: deptBuy, WarehouseID: 1, Lines: []domain.IssueLineInput{{ItemID: plain.ID, Qty: 1}}})
	if !strings.HasSuffix(slip2.No, "-0002") {
		t.Fatalf("순번: %s", slip2.No)
	}
	if list, _ := s.is.ListRecent(as(uFinance), 10); len(list) != 2 || list[0].ID != slip2.ID {
		t.Fatalf("최근 목록: %+v", list)
	}
	before := count(t, e.db, `SELECT COUNT(*) FROM stock_ledger`)
	cases := []struct {
		name string
		in   domain.IssueInput
		want string
	}{
		{"재고 부족", domain.IssueInput{DepartmentID: deptBuy, WarehouseID: 1, Lines: []domain.IssueLineInput{{ItemID: plain.ID, Qty: 5}, {ItemID: it.ID, LotID: a, Qty: 21}}}, "재고가 부족합니다"},
		{"로트 필수", domain.IssueInput{DepartmentID: deptBuy, WarehouseID: 1, Lines: []domain.IssueLineInput{{ItemID: it.ID, Qty: 1}}}, "로트를 선택해야"},
		{"로트 금지", domain.IssueInput{DepartmentID: deptBuy, WarehouseID: 1, Lines: []domain.IssueLineInput{{ItemID: plain.ID, LotID: a, Qty: 1}}}, "로트를 관리하지 않는"},
		{"유효기한 지난 로트", domain.IssueInput{DepartmentID: deptBuy, WarehouseID: 1, Lines: []domain.IssueLineInput{{ItemID: it.ID, LotID: x, Qty: 1}}}, "유효기한이 지난 로트"},
		{"같은 줄 중복", domain.IssueInput{DepartmentID: deptBuy, WarehouseID: 1, Lines: []domain.IssueLineInput{{ItemID: plain.ID, Qty: 1}, {ItemID: plain.ID, Qty: 1}}}, "중복"},
		{"수량 0", domain.IssueInput{DepartmentID: deptBuy, WarehouseID: 1, Lines: []domain.IssueLineInput{{ItemID: plain.ID, Qty: 0}}}, "수량이 올바르지"},
		{"줄 없음", domain.IssueInput{DepartmentID: deptBuy, WarehouseID: 1}, "줄은 1건"},
		{"부서 없음", domain.IssueInput{DepartmentID: 999, WarehouseID: 1, Lines: []domain.IssueLineInput{{ItemID: plain.ID, Qty: 1}}}, "출고 부서"},
		{"창고 없음", domain.IssueInput{DepartmentID: deptBuy, WarehouseID: 99, Lines: []domain.IssueLineInput{{ItemID: plain.ID, Qty: 1}}}, "창고를 찾을 수 없습니다"},
		{"미래 날짜", domain.IssueInput{DepartmentID: deptBuy, WarehouseID: 1, IssueDate: "2999-01-01", Lines: []domain.IssueLineInput{{ItemID: plain.ID, Qty: 1}}}, "오늘 이후"},
		{"방식", domain.IssueInput{DepartmentID: deptBuy, WarehouseID: 1, Method: "zzz", Lines: []domain.IssueLineInput{{ItemID: plain.ID, Qty: 1}}}, "알 수 없는"},
	}
	for _, c := range cases {
		if _, err := s.is.Issue(as(uFinance), c.in); !errors.Is(err, contract.ErrValidation) || !strings.Contains(err.Error(), c.want) {
			t.Fatalf("%s: %v", c.name, err)
		}
	}
	if after := count(t, e.db, `SELECT COUNT(*) FROM stock_ledger`); after != before {
		t.Fatalf("거부된 출고가 원장에 남음: %d → %d (두 번째 줄 부족 시 앞 줄도 취소돼야 함)", before, after)
	}
	if _, err := s.is.Issue(as(uStaff), ok); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("직원: %v", err)
	}
	e.rec.FailWith(errBoom)
	_, err = s.is.Issue(as(uFinance), domain.IssueInput{DepartmentID: deptBuy, WarehouseID: 1, Lines: []domain.IssueLineInput{{ItemID: plain.ID, Qty: 1}}})
	e.rec.FailWith(nil)
	if !errors.Is(err, contract.ErrAuditFailed) || count(t, e.db, `SELECT COUNT(*) FROM stock_ledger`) != before || count(t, e.db, `SELECT COUNT(*) FROM issue_slips`) != 2 {
		t.Fatalf("감사 실패면 원장·전표가 남지 않아야 함: %v", err)
	}
}

// AC-412 동시 출고 두 건이 같은 로트의 마지막 수량을 넘겨 가지 못한다.
func TestConcurrentIssueCannotOversell(t *testing.T) {
	e := newEnv(t)
	s := e.stock()
	it := e.item(t, "OUT-RACE", 0, false)
	e.seedPlain(t, it.ID, 1, 10)
	var wg sync.WaitGroup
	errs := make([]error, 4)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = s.is.Issue(as(uFinance), domain.IssueInput{DepartmentID: deptBuy, WarehouseID: 1, Lines: []domain.IssueLineInput{{ItemID: it.ID, Qty: 4}}})
		}(i)
	}
	wg.Wait()
	ok := 0
	for _, err := range errs {
		if err == nil {
			ok++
		}
	}
	if ok != 2 || whQty(t, e, 1, it.ID, 0) != 2 {
		t.Fatalf("10개 재고에서 4개씩 4건: 성공 %d건, 남은 재고 %d", ok, whQty(t, e, 1, it.ID, 0))
	}
}

// AC-420 창고 이동: 같은 로트의 출고·입고 두 줄이 한 트랜잭션에 들어가고 합계 재고는 그대로, 부족·같은 창고·중지 창고는 거부, 중간 실패면 아무것도 남지 않는다.
func TestStockTransfer(t *testing.T) {
	e := newEnv(t)
	s := e.stock()
	w2, err := s.wh.Save(as(uFinance), domain.WarehouseInput{Code: "WH-02", Name: "물류센터"})
	if err != nil {
		t.Fatal(err)
	}
	it := e.item(t, "MV-LOT", 0, true)
	lot := e.seedLot(t, it.ID, "L-A", "2027-06-30", 1, 30, "2026-09-10T00:00:00Z")
	plain := e.item(t, "MV-PLAIN", 0, false)
	e.seedPlain(t, plain.ID, 1, 50)
	total := func(item int64) int64 {
		return int64(count(t, e.db, `SELECT COALESCE(SUM(qty_delta),0) FROM stock_ledger WHERE item_id = ?`, item))
	}
	in := domain.TransferInput{FromID: 1, ToID: w2.ID, Memo: "물류센터 보충", Lines: []domain.TransferLineInput{{ItemID: it.ID, LotID: lot, Qty: 20}, {ItemID: plain.ID, Qty: 10}}}
	pv, err := s.tr.Preview(as(uFinance), in)
	if err != nil || len(pv) != 4 || pv[0].Kind != "transfer_out" || pv[0].Qty != -20 || pv[1].Kind != "transfer_in" || pv[1].Qty != 20 || pv[1].WarehouseName != "물류센터" {
		t.Fatalf("미리보기: %+v %v", pv, err)
	}
	slip, err := s.tr.Transfer(as(uFinance), in)
	if err != nil || !strings.HasPrefix(slip.No, "MV-2026-") || len(slip.Lines) != 2 || slip.FromName != "본사 창고" {
		t.Fatalf("이동: %+v %v", slip, err)
	}
	if whQty(t, e, 1, it.ID, lot) != 10 || whQty(t, e, w2.ID, it.ID, lot) != 20 || whQty(t, e, 1, plain.ID, 0) != 40 || whQty(t, e, w2.ID, plain.ID, 0) != 10 {
		t.Fatal("창고별 재고가 맞지 않음")
	}
	if total(it.ID) != 30 || total(plain.ID) != 50 {
		t.Fatal("이동으로 합계 재고가 바뀜")
	}
	if n := count(t, e.db, `SELECT COUNT(*) FROM stock_ledger WHERE ref_type='stock_transfer' AND ref_id=? AND reason IN ('transfer_out','transfer_in')`, slip.ID); n != 4 {
		t.Fatalf("이동 원장 행 %d", n)
	}
	// 거부 사례
	before := count(t, e.db, `SELECT COUNT(*) FROM stock_ledger`)
	bad := []struct {
		name string
		in   domain.TransferInput
		want string
	}{
		{"같은 창고", domain.TransferInput{FromID: 1, ToID: 1, Lines: in.Lines}, "같습니다"},
		{"재고 초과", domain.TransferInput{FromID: 1, ToID: w2.ID, Lines: []domain.TransferLineInput{{ItemID: plain.ID, Qty: 5}, {ItemID: it.ID, LotID: lot, Qty: 11}}}, "재고가 부족합니다"},
		{"없는 창고", domain.TransferInput{FromID: 1, ToID: 99, Lines: in.Lines}, "도착 창고를 찾을 수 없습니다"},
		{"로트 필수", domain.TransferInput{FromID: 1, ToID: w2.ID, Lines: []domain.TransferLineInput{{ItemID: it.ID, Qty: 1}}}, "로트를 선택해야"},
	}
	for _, c := range bad {
		if _, err := s.tr.Transfer(as(uFinance), c.in); !errors.Is(err, contract.ErrValidation) || !strings.Contains(err.Error(), c.want) {
			t.Fatalf("%s: %v", c.name, err)
		}
	}
	if count(t, e.db, `SELECT COUNT(*) FROM stock_ledger`) != before {
		t.Fatal("거부·중간 실패한 이동이 원장에 남음(첫 줄만 반영되는 일이 없어야 함)")
	}
	// 중지된 창고로는 이동·입고할 수 없다.
	if err := s.wh.SetActive(as(uFinance), 1, false); !errors.Is(err, contract.ErrInvalidState) {
		t.Fatalf("재고가 있는 본사 창고 중지: %v", err)
	}
	w3, _ := s.wh.Save(as(uFinance), domain.WarehouseInput{Code: "WH-03", Name: "별관"})
	if err := s.wh.SetActive(as(uFinance), w3.ID, false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.tr.Transfer(as(uFinance), domain.TransferInput{FromID: 1, ToID: w3.ID, Lines: []domain.TransferLineInput{{ItemID: plain.ID, Qty: 1}}}); !errors.Is(err, contract.ErrValidation) {
		t.Fatalf("중지 창고로 이동: %v", err)
	}
	if _, err := e.db.Exec(`INSERT INTO stock_ledger (item_id, qty_delta, reason, actor_id, warehouse_id) VALUES (?,1,'receipt',?,?)`, plain.ID, uFinance, w3.ID); err == nil {
		t.Fatal("DB 트리거가 중지된 창고 입고를 막지 못함")
	}
	if _, err := s.tr.Transfer(as(uStaff), in); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("직원: %v", err)
	}
	if list, _ := s.tr.ListRecent(as(uFinance), 10); len(list) != 1 {
		t.Fatalf("최근 이동: %+v", list)
	}
	if !e.rec.Has("stock_transfer.create", "stock_transfer", "success") {
		t.Fatal("감사 없음")
	}
}

// AC-401 호환: 창고를 모르는 기존 경로의 원장 행은 기본 창고에 들어가고, GetStock 은 합계와 창고별 수량을 함께 준다.
func TestLedgerDefaultsToMainWarehouseAndStockByWarehouse(t *testing.T) {
	e := newEnv(t)
	s := e.stock()
	it := e.item(t, "COMPAT", 0, false)
	if _, err := e.db.Exec(`INSERT INTO stock_ledger (item_id, qty_delta, reason, actor_id, created_at) VALUES (?,7,'receipt',?,'2026-09-01T00:00:00Z')`, it.ID, uFinance); err != nil {
		t.Fatal(err)
	}
	if whQty(t, e, 1, it.ID, 0) != 7 {
		t.Fatal("창고를 안 준 행이 본사 창고에 속하지 않음")
	}
	w2, _ := s.wh.Save(as(uFinance), domain.WarehouseInput{Code: "WH-02", Name: "물류센터"})
	if _, err := s.tr.Transfer(as(uFinance), domain.TransferInput{FromID: 1, ToID: w2.ID, Lines: []domain.TransferLineInput{{ItemID: it.ID, Qty: 3}}}); err != nil {
		t.Fatal(err)
	}
	st, err := e.inventory.GetStock(as(uStaff), it.ID)
	if err != nil || st.OnHand != 7 || len(st.ByWarehouse) != 2 || st.ByWarehouse[0].OnHand != 4 || st.ByWarehouse[1].OnHand != 3 {
		t.Fatalf("GetStock: %+v %v", st, err)
	}
}
