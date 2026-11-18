package service_test

import (
	"errors"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/contract/fake"
	"github.com/choibongseok/procure-ledger/internal/service"
)

func (e *env) counts() (*service.StockCountService, *service.LedgerService) {
	b := service.Base{DB: e.db, Txs: contract.SQLTxManager{DB: e.db}, Audit: e.rec, Masker: fake.Masker{}, Org: leftAwareOrg{e.org}, Now: func() time.Time { return e.now }}
	return service.NewStockCountService(b), service.NewLedgerService(b)
}

// adjForm 은 재고 조정 전표 양식의 구매팀 조직 코드를 시험 조직(BUY)에 맞춘 판을 게시한다(운영도 결재 양식 관리 화면에서 같은 일을 한다).
func (e *env) adjForm(t *testing.T) {
	t.Helper()
	f := e.forms()
	v, err := f.CreateVersionDraft(as(uAdmin), "ADJ")
	if err != nil {
		t.Fatal(err)
	}
	def := v.Definition
	for i := range def.Line.Steps {
		if def.Line.Steps[i].UnitCode == "DEP-PU" {
			def.Line.Steps[i].UnitCode = "BUY"
		}
	}
	if _, err := f.SaveVersionDraft(as(uAdmin), v.ID, domain.FormVersionInput{Definition: def}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Publish(as(uAdmin), v.ID); err != nil {
		t.Fatal(err)
	}
}

func counted(n int64) *int64 { return &n }

// stockSetup 은 본사 창고에 로트 없는 품목 A 50개와 로트 품목 B(로트 L1 30개)를 넣는다.
func (e *env) stockSetup(t *testing.T) (a, b domain.Item, lot int64) {
	t.Helper()
	a = e.item(t, "CNT-A", 0, false)
	b = e.item(t, "CNT-B", 0, true)
	e.seedPlain(t, a.ID, 1, 50)
	lot = e.seedLot(t, b.ID, "L1", "2027-12-31", 1, 30, "2026-09-10T00:00:00Z")
	return
}

func findLine(c domain.StockCount, itemID int64) domain.StockCountLine {
	for _, l := range c.Lines {
		if l.ItemID == itemID {
			return l
		}
	}
	return domain.StockCountLine{}
}

// AC-430~433 실사 대표 흐름: 작성(스냅샷) → 입력 → 상신(기준일 뒤 변동 경고) → 구매팀장·재무 결재 → 승인 뒤에만 원장에 조정 줄(실제 − 스냅샷).
func TestStockCountFlowAppliesAfterApproval(t *testing.T) {
	e := newEnv(t)
	e.adjForm(t)
	cs, _ := e.counts()
	s := e.stock()
	a, b, lot := e.stockSetup(t)
	if _, err := cs.Create(as(uStaff), 1); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("직원의 실사: %v", err)
	}
	c, err := cs.Create(as(uFinance), 1)
	if err != nil || !strings.HasPrefix(c.No, "SC-2026-") || c.Status != domain.CountDraft || len(c.Lines) != 2 || findLine(c, a.ID).SystemQty != 50 || findLine(c, b.ID).SystemQty != 30 {
		t.Fatalf("실사 표 작성: %+v %v", c, err)
	}
	if _, err := cs.Create(as(uFinance), 1); !errors.Is(err, contract.ErrInvalidState) || !strings.Contains(err.Error(), "진행 중인 실사") {
		t.Fatalf("한 창고에 진행 중 실사는 하나: %v", err)
	}
	if _, err := cs.Submit(as(uFinance), c.ID); !errors.Is(err, contract.ErrValidation) || !strings.Contains(err.Error(), "입력하지 않은 줄") {
		t.Fatalf("미입력 줄이 있으면 상신 불가: %v", err)
	}
	// 실사 중 정상 출고(스냅샷 뒤 변동): 반영은 스냅샷과의 차이로 한다(D8).
	if _, err := s.is.Issue(as(uFinance), domain.IssueInput{DepartmentID: deptBuy, WarehouseID: 1, Lines: []domain.IssueLineInput{{ItemID: a.ID, Qty: 2}}}); err != nil {
		t.Fatal(err)
	}
	c, err = cs.Save(as(uFinance), c.ID, domain.CountSaveInput{Lines: []domain.CountLineInput{
		{ItemID: a.ID, CountedQty: counted(48), Reason: "파손"}, {ItemID: b.ID, LotID: lot, CountedQty: counted(30)}}})
	if err != nil || c.DiffLines != 1 || c.Uncounted != 0 || findLine(c, a.ID).Diff != -2 {
		t.Fatalf("저장: %+v %v", c, err)
	}
	// 차이 사유가 없는 차이 줄은 상신 불가.
	c2, _ := cs.Save(as(uFinance), c.ID, domain.CountSaveInput{Lines: []domain.CountLineInput{{ItemID: a.ID, CountedQty: counted(48), Reason: " "}}})
	if _, err := cs.Submit(as(uFinance), c2.ID); !errors.Is(err, contract.ErrValidation) || !strings.Contains(err.Error(), "차이 사유") {
		t.Fatalf("사유 없는 차이: %v", err)
	}
	if _, err := cs.Save(as(uFinance), c.ID, domain.CountSaveInput{Lines: []domain.CountLineInput{{ItemID: a.ID, CountedQty: counted(48), Reason: "파손"}}}); err != nil {
		t.Fatal(err)
	}
	sub, err := cs.Submit(as(uFinance), c.ID)
	if err != nil || sub.Status != domain.CountApproving || sub.DocID == 0 || sub.MovedSince < 1 {
		t.Fatalf("상신(기준일 이후 변동 경고만, 막지 않음): %+v %v", sub, err)
	}
	// 승인 전까지 재고는 그대로다(출고 뒤 48).
	if whQty(t, e, 1, a.ID, 0) != 48 {
		t.Fatalf("승인 전 재고가 바뀜: %d", whQty(t, e, 1, a.ID, 0))
	}
	doc, err := e.docs().Get(as(uFinance), sub.DocID)
	if err != nil || doc.FormCode != "ADJ" || len(doc.Steps) != 2 || doc.Steps[0].AssigneeID != uHead || doc.Steps[1].Kind != "finance" || !strings.HasPrefix(doc.No, "ADJ-2026-") {
		t.Fatalf("조정 전표 결재선(구매팀장 → 재무): %+v %v", doc.Steps, err)
	}
	if !e.rec.Has("stock_count.submit", "stock_count", "success") {
		t.Fatal("상신 감사 없음")
	}
	// 일반 문서 API 로는 조정 전표를 만들거나 상신할 수 없다.
	if _, err := e.docs().CreateDraft(as(uFinance), domain.DocDraftInput{FormCode: "ADJ", Title: "수동", Fields: map[string]any{"count_no": "x", "warehouse": "x", "base_date": "2026-11-10"}}); !errors.Is(err, contract.ErrValidation) {
		t.Fatalf("수동 조정 전표 작성: %v", err)
	}
	// 결재: 구매팀장 → 재무. 최종 승인 트랜잭션에서 원장에 조정 줄이 생긴다.
	decideAs(t, e, uHead, sub.DocID, true, "")
	if whQty(t, e, 1, a.ID, 0) != 48 {
		t.Fatal("최종 승인 전에 재고가 바뀜")
	}
	res := decideAs(t, e, uFinance2, sub.DocID, true, "")
	if res.Request.Status != domain.PRApproved {
		t.Fatalf("상태 %s", res.Request.Status)
	}
	if whQty(t, e, 1, a.ID, 0) != 46 || whQty(t, e, 1, b.ID, lot) != 30 {
		t.Fatalf("조정 반영(48 - 2 = 46, 차이 0 인 줄은 그대로): A %d", whQty(t, e, 1, a.ID, 0))
	}
	if n := count(t, e.db, `SELECT COUNT(*) FROM stock_ledger WHERE reason='adjust' AND ref_type='stock_count' AND ref_id=? AND qty_delta=-2 AND warehouse_id=1`, c.ID); n != 1 {
		t.Fatalf("조정 원장 행 %d", n)
	}
	got, _ := cs.Get(as(uFinance), c.ID)
	if got.Status != domain.CountApplied || got.AppliedAt == nil || !e.rec.Has("stock_count.apply", "stock_count", "success") {
		t.Fatalf("반영 완료: %+v", got.Status)
	}
	if _, err := cs.Save(as(uFinance), c.ID, domain.CountSaveInput{Lines: []domain.CountLineInput{{ItemID: a.ID, CountedQty: counted(1)}}}); !errors.Is(err, contract.ErrInvalidState) {
		t.Fatalf("끝난 실사 표 수정: %v", err)
	}
	if _, err := e.db.Exec(`UPDATE stock_counts SET status = 'draft' WHERE id = ?`, c.ID); err == nil {
		t.Fatal("DB 트리거가 끝난 실사 표 변경을 막지 못함")
	}
}

// AC-432 반려와 취소: 상신 취소·회수로 조정 전표가 임시저장이 되면 실사 표는 작성 중으로, 반려되면 반려와 사유로 돌아가고 고쳐 다시 상신할 수 있다.
func TestStockCountRejectCancelAndResubmit(t *testing.T) {
	e := newEnv(t)
	e.adjForm(t)
	cs, _ := e.counts()
	a, _, _ := e.stockSetup(t)
	c, _ := cs.Create(as(uFinance), 1)
	if _, err := cs.Save(as(uFinance), c.ID, domain.CountSaveInput{Lines: []domain.CountLineInput{{ItemID: a.ID, CountedQty: counted(45), Reason: "분실"}}}); err != nil {
		t.Fatal(err)
	}
	// 실제 수량이 비어 있는 다른 줄도 채운다.
	full, _ := cs.Get(as(uFinance), c.ID)
	var in []domain.CountLineInput
	for _, l := range full.Lines {
		cq := l.SystemQty
		if l.ItemID == a.ID {
			cq = 45
		}
		in = append(in, domain.CountLineInput{ItemID: l.ItemID, LotID: l.LotID, CountedQty: &cq, Reason: l.Reason})
	}
	if _, err := cs.Save(as(uFinance), c.ID, domain.CountSaveInput{Lines: in}); err != nil {
		t.Fatal(err)
	}
	sub, err := cs.Submit(as(uFinance), c.ID)
	if err != nil {
		t.Fatal(err)
	}
	// 상신 취소 → 작성 중.
	if _, err := e.docs().CancelSubmit(as(uFinance), sub.DocID, "수정"); err != nil {
		t.Fatalf("상신 취소: %v", err)
	}
	if got, _ := cs.Get(as(uFinance), c.ID); got.Status != domain.CountDraft {
		t.Fatalf("상신 취소 뒤 실사 표 %s", got.Status)
	}
	sub, err = cs.Submit(as(uFinance), c.ID)
	if err != nil || sub.Status != domain.CountApproving {
		t.Fatalf("다시 상신: %+v %v", sub, err)
	}
	decideAs(t, e, uHead, sub.DocID, false, "수량 재확인")
	got, _ := cs.Get(as(uFinance), c.ID)
	if got.Status != domain.CountRejected || got.RejectReason != "수량 재확인" || !e.rec.Has("stock_count.reject", "stock_count", "success") {
		t.Fatalf("반려: %+v", got)
	}
	if n := whQty(t, e, 1, a.ID, 0); n != 50 {
		t.Fatalf("반려인데 재고가 바뀜: %d", n)
	}
	// 반려된 실사 표를 고쳐 다시 상신, 이번엔 승인.
	if _, err := cs.Save(as(uFinance), c.ID, domain.CountSaveInput{Lines: []domain.CountLineInput{{ItemID: a.ID, CountedQty: counted(47), Reason: "재확인"}}}); err != nil {
		t.Fatalf("반려 뒤 수정: %v", err)
	}
	sub, err = cs.Submit(as(uFinance), c.ID)
	if err != nil || sub.Status != domain.CountApproving {
		t.Fatalf("반려 뒤 재상신: %+v %v", sub, err)
	}
	decideAs(t, e, uHead, sub.DocID, true, "")
	decideAs(t, e, uFinance2, sub.DocID, true, "")
	if n := whQty(t, e, 1, a.ID, 0); n != 47 {
		t.Fatalf("재상신 승인 뒤 재고 %d", n)
	}
	if got, _ := cs.Get(as(uFinance), c.ID); got.Status != domain.CountApplied {
		t.Fatalf("상태 %s", got.Status)
	}
}

// 차이가 없으면 상신 없이 종료되고, 작성 중인 실사 표는 폐기할 수 있다(만든 사람만).
func TestStockCountNoDiffAndDiscard(t *testing.T) {
	e := newEnv(t)
	e.adjForm(t)
	cs, _ := e.counts()
	a, _, _ := e.stockSetup(t)
	c, _ := cs.Create(as(uFinance), 1)
	var in []domain.CountLineInput
	for _, l := range c.Lines {
		cq := l.SystemQty
		in = append(in, domain.CountLineInput{ItemID: l.ItemID, LotID: l.LotID, CountedQty: &cq})
	}
	if _, err := cs.Save(as(uFinance), c.ID, domain.CountSaveInput{Lines: in}); err != nil {
		t.Fatal(err)
	}
	done, err := cs.Submit(as(uFinance), c.ID)
	if err != nil || done.Status != domain.CountNoDiff || done.DocID != 0 {
		t.Fatalf("차이 없음 종료: %+v %v", done, err)
	}
	if n := count(t, e.db, `SELECT COUNT(*) FROM stock_ledger WHERE reason='adjust'`); n != 0 {
		t.Fatal("차이가 없는데 조정 행이 생김")
	}
	c2, err := cs.Create(as(uFinance), 1)
	if err != nil {
		t.Fatalf("종료된 뒤 새 실사: %v", err)
	}
	if err := cs.Discard(as(uAdmin), c2.ID); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("만든 사람이 아닌 폐기: %v", err)
	}
	if err := cs.Discard(as(uFinance), c2.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := cs.Get(as(uFinance), c2.ID); got.Status != domain.CountDiscarded {
		t.Fatalf("상태 %s", got.Status)
	}
	if _, err := cs.Submit(as(uFinance), c2.ID); !errors.Is(err, contract.ErrInvalidState) {
		t.Fatalf("폐기된 실사 상신: %v", err)
	}
	_ = a
}

// E9: 승인 시점에 조정 결과가 음수 재고가 되면 승인이 거부되고 문서·재고가 그대로다.
func TestStockCountApprovalRefusedWhenStockWouldGoNegative(t *testing.T) {
	e := newEnv(t)
	e.adjForm(t)
	cs, _ := e.counts()
	s := e.stock()
	a, _, _ := e.stockSetup(t)
	c, _ := cs.Create(as(uFinance), 1)
	full, _ := cs.Get(as(uFinance), c.ID)
	var in []domain.CountLineInput
	for _, l := range full.Lines {
		cq, reason := l.SystemQty, ""
		if l.ItemID == a.ID {
			cq, reason = 10, "대량 분실" // 50 → 10 (차이 -40)
		}
		in = append(in, domain.CountLineInput{ItemID: l.ItemID, LotID: l.LotID, CountedQty: &cq, Reason: reason})
	}
	if _, err := cs.Save(as(uFinance), c.ID, domain.CountSaveInput{Lines: in}); err != nil {
		t.Fatal(err)
	}
	sub, err := cs.Submit(as(uFinance), c.ID)
	if err != nil {
		t.Fatal(err)
	}
	// 결재 중에 정상 출고 20 개 → 남은 30, 조정 -40 을 적용하면 음수.
	if _, err := s.is.Issue(as(uFinance), domain.IssueInput{DepartmentID: deptBuy, WarehouseID: 1, Lines: []domain.IssueLineInput{{ItemID: a.ID, Qty: 20}}}); err != nil {
		t.Fatal(err)
	}
	decideAs(t, e, uHead, sub.DocID, true, "")
	_, derr := e.approval.Decide(as(uFinance2), sub.DocID, domain.Decision{Approve: true})
	if !errors.Is(derr, contract.ErrInvalidState) || !strings.Contains(derr.Error(), "재고가 부족해 조정을 반영할 수 없습니다") {
		t.Fatalf("음수 재고가 되는 승인: %v", derr)
	}
	doc, _ := e.docs().Get(as(uFinance), sub.DocID)
	if doc.Status == domain.PRApproved || whQty(t, e, 1, a.ID, 0) != 30 {
		t.Fatalf("승인이 취소돼야 함: 상태 %s 재고 %d", doc.Status, whQty(t, e, 1, a.ID, 0))
	}
	if got, _ := cs.Get(as(uFinance), c.ID); got.Status != domain.CountApproving {
		t.Fatalf("실사 표 상태 %s", got.Status)
	}
}

// 훅은 docHookTable 한 곳에만 있다: ADJ(재고 조정 전표)·LEAVE(휴가 신청서, B50)·MCR(월 마감 해제 요청서, B27)·JV(전표 승인 요청서, B31)만 등록되어 있고 새 양식에 훅을 더하면 이 테스트가 바뀐 것을 알린다.
func TestDocHookTableIsFixed(t *testing.T) {
	got := service.DocHookForms()
	sort.Strings(got)
	if len(got) != 5 || got[0] != "ADJ" || got[1] != "JV" || got[2] != "LEAVE" || got[3] != "MCR" || got[4] != "PAY" { // LEAVE: 휴가 신청서(B50)
		t.Fatalf("결재 훅 목록이 바뀜: %v", got)
	}
}

// AC-440~441 수불부: 기초·입고·출고·기말, 이동은 각각 더함, 창고·로트 필터, 기간 경계, 쪽 나누기, 한국어 오류.
func TestLedgerRegister(t *testing.T) {
	e := newEnv(t)
	_, lg := e.counts()
	s := e.stock()
	w2, _ := s.wh.Save(as(uFinance), domain.WarehouseInput{Code: "WH-02", Name: "물류센터"})
	it := e.item(t, "LED-1", 0, true)
	lot := e.seedLot(t, it.ID, "L1", "2027-12-31", 1, 90, "2026-10-01T00:00:00Z") // 기초 90(10월)
	// 11월: 입고 40(11-03), 출고 10(11-10: 시험 시계), 이동 20(11-10)
	if _, err := e.db.Exec(`INSERT INTO stock_ledger (item_id, lot_id, qty_delta, reason, ref_type, ref_id, actor_id, created_at, warehouse_id) VALUES (?,?,40,'receipt','receipt',123,?,'2026-11-03T01:00:00Z',1)`, it.ID, lot, uFinance); err != nil {
		t.Fatal(err)
	}
	if _, err := s.is.Issue(as(uFinance), domain.IssueInput{DepartmentID: deptSales, WarehouseID: 1, Lines: []domain.IssueLineInput{{ItemID: it.ID, LotID: lot, Qty: 10}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.tr.Transfer(as(uFinance), domain.TransferInput{FromID: 1, ToID: w2.ID, Lines: []domain.TransferLineInput{{ItemID: it.ID, LotID: lot, Qty: 20}}}); err != nil {
		t.Fatal(err)
	}
	f := domain.LedgerFilter{ItemID: it.ID, From: "2026-11-01", To: "2026-11-11"}
	pg, err := lg.Ledger(as(uStaff), f)
	if err != nil {
		t.Fatal(err)
	}
	// 전체 창고: 기초 90 + 입고 60(40+이동 입고 20) − 출고 30(10+이동 출고 20) = 기말 120 = 재고 합계
	if pg.Opening != 90 || pg.TotalIn != 60 || pg.TotalOut != 30 || pg.Closing != 120 || pg.Total != 4 || len(pg.Rows) != 4 {
		t.Fatalf("합계: %+v", pg)
	}
	if st, _ := e.inventory.GetStock(as(uStaff), it.ID); st.OnHand != pg.Closing {
		t.Fatalf("기말이 재고 합계와 다름: %d vs %d", pg.Closing, st.OnHand)
	}
	kinds := []string{}
	for _, r := range pg.Rows {
		kinds = append(kinds, r.KindLabel)
	}
	if strings.Join(kinds, ",") != "입고,출고,이동 출고,이동 입고" || pg.Rows[0].DocNo != "RC-000123" || !strings.HasPrefix(pg.Rows[1].DocNo, "OUT-2026-") || !strings.HasPrefix(pg.Rows[2].DocNo, "MV-2026-") || pg.Rows[2].DocNo != pg.Rows[3].DocNo {
		t.Fatalf("구분·전표 번호: %+v", pg.Rows)
	}
	if pg.Rows[0].In != 40 || pg.Rows[0].Balance != 130 || pg.Rows[1].Out != 10 || pg.Rows[1].Balance != 120 || pg.Rows[3].Balance != 120 {
		t.Fatalf("잔량: %+v", pg.Rows)
	}
	// 창고 필터: 본사 창고만(이동 출고만 보이고 입고는 도착 창고에 있다).
	f.WarehouseID = 1
	pg, _ = lg.Ledger(as(uAuditor), f)
	if pg.Opening != 90 || pg.TotalIn != 40 || pg.TotalOut != 30 || pg.Closing != 100 || pg.Total != 3 {
		t.Fatalf("본사 창고: %+v", pg)
	}
	f.WarehouseID = w2.ID
	pg, _ = lg.Ledger(as(uAuditor), f)
	if pg.Opening != 0 || pg.TotalIn != 20 || pg.Closing != 20 {
		t.Fatalf("물류센터: %+v", pg)
	}
	// 기간 경계: 시작일 직전까지가 기초, 종료일 당일 행 포함.
	pg, _ = lg.Ledger(as(uStaff), domain.LedgerFilter{ItemID: it.ID, From: "2026-11-03", To: "2026-11-03"})
	if pg.Opening != 90 || pg.TotalIn != 40 || pg.TotalOut != 0 || pg.Closing != 130 || pg.Total != 1 {
		t.Fatalf("하루 조회: %+v", pg)
	}
	pg, _ = lg.Ledger(as(uStaff), domain.LedgerFilter{ItemID: it.ID, From: "2026-12-01", To: "2026-12-31"})
	if len(pg.Rows) != 0 || pg.Opening != 120 || pg.Closing != 120 {
		t.Fatalf("기록 없는 기간: %+v", pg)
	}
	// 쪽 나누기와 오류 문구.
	pg, _ = lg.Ledger(as(uStaff), domain.LedgerFilter{ItemID: it.ID, From: "2026-11-01", To: "2026-11-11", PageSize: 2, Page: 2})
	if len(pg.Rows) != 2 || pg.Total != 4 || pg.Rows[0].KindLabel != "이동 출고" || pg.Rows[0].Balance != 100 {
		t.Fatalf("둘째 쪽: %+v", pg)
	}
	for _, c := range []struct {
		f    domain.LedgerFilter
		want string
	}{{domain.LedgerFilter{}, "품목을 고르세요"}, {domain.LedgerFilter{ItemID: it.ID, From: "2026-11-10", To: "2026-11-01"}, "종료일은 시작일 이후"}, {domain.LedgerFilter{ItemID: 9999}, "품목을 찾을 수 없습니다"}} {
		if _, err := lg.Ledger(as(uStaff), c.f); !errors.Is(err, contract.ErrValidation) || !strings.Contains(err.Error(), c.want) {
			t.Fatalf("%+v: %v", c.f, err)
		}
	}
}
