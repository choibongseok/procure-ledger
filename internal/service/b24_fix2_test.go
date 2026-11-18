package service_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
)

// 0021: 작성 중·반려가 아닌 실사 표의 줄은 SQL 로도 지우거나 더할 수 없다(결재 중·반영 완료·차이 없음·폐기).
func TestStockCountLinesGuardedByTriggers(t *testing.T) {
	e := newEnv(t)
	e.adjForm(t)
	cs, _ := e.counts()
	a, _, _ := e.stockSetup(t)
	c, _ := cs.Create(as(uFinance), 1)
	// 작성 중: 줄 추가·삭제 가능(전산 수량 새로 고침이 쓴다).
	if _, err := e.db.Exec(`DELETE FROM stock_count_lines WHERE count_id = ? AND item_id = ?`, c.ID, a.ID); err != nil {
		t.Fatalf("작성 중 표의 줄 삭제는 허용: %v", err)
	}
	if _, err := e.db.Exec(`INSERT INTO stock_count_lines (count_id, item_id, lot_key, system_qty) VALUES (?,?,0,50)`, c.ID, a.ID); err != nil {
		t.Fatalf("작성 중 표에 줄 추가는 허용: %v", err)
	}
	// 결재 중·끝난 상태로 바꾼 뒤에는 둘 다 거부.
	for _, st := range []string{"approving", "applied", "no_diff", "discarded"} {
		if _, err := e.db.Exec(`UPDATE stock_counts SET status = ? WHERE id = ?`, st, c.ID); err != nil {
			t.Fatalf("%s 로 변경: %v", st, err)
		}
		if _, err := e.db.Exec(`DELETE FROM stock_count_lines WHERE count_id = ?`, c.ID); err == nil || !strings.Contains(err.Error(), "지울 수 없습니다") {
			t.Fatalf("%s 상태에서 줄 삭제가 허용됨: %v", st, err)
		}
		if _, err := e.db.Exec(`INSERT INTO stock_count_lines (count_id, item_id, lot_key, system_qty) VALUES (?,999,0,1)`, c.ID); err == nil || !strings.Contains(err.Error(), "더할 수 없습니다") {
			t.Fatalf("%s 상태에서 줄 추가가 허용됨: %v", st, err)
		}
		if st == "applied" || st == "no_diff" || st == "discarded" {
			break // 끝난 표는 상태 자체를 되돌릴 수 없다(0020 트리거)
		}
	}
}

// 반려된 실사 표: 전산 수량을 새로 고쳐 차이가 0 이면 차이 없음으로 끝낼 수 있고, 폐기할 수 있으며, 남아 있는 동안 같은 창고에 새 실사를 만들 수 없다.
func TestRejectedStockCountCanFinishOrBeDiscarded(t *testing.T) {
	e := newEnv(t)
	e.adjForm(t)
	cs, _ := e.counts()
	a, _, _ := e.stockSetup(t)
	reject := func() domain.StockCount {
		c, err := cs.Create(as(uFinance), 1)
		if err != nil {
			t.Fatal(err)
		}
		full, _ := cs.Get(as(uFinance), c.ID)
		var in []domain.CountLineInput
		for _, l := range full.Lines {
			cq, reason := l.SystemQty, ""
			if l.ItemID == a.ID {
				cq, reason = 45, "분실"
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
		decideAs(t, e, uHead, sub.DocID, false, "다시 세어 보세요")
		got, _ := cs.Get(as(uFinance), c.ID)
		if got.Status != domain.CountRejected {
			t.Fatalf("반려 상태 %s", got.Status)
		}
		return got
	}
	r1 := reject()
	if _, err := cs.Create(as(uFinance), 1); !errors.Is(err, contract.ErrInvalidState) || !strings.Contains(err.Error(), r1.No) {
		t.Fatalf("반려 표가 남아 있으면 새 실사 불가: %v", err)
	}
	// 실제 재고가 50 으로 확인돼 새로 고침 뒤 차이가 0 이 되면 상신 없이 차이 없음으로 끝난다.
	if _, err := cs.Refresh(as(uFinance), r1.ID); err != nil {
		t.Fatal(err)
	}
	full, _ := cs.Get(as(uFinance), r1.ID)
	var in []domain.CountLineInput
	for _, l := range full.Lines {
		cq := l.SystemQty
		in = append(in, domain.CountLineInput{ItemID: l.ItemID, LotID: l.LotID, CountedQty: &cq})
	}
	if _, err := cs.Save(as(uFinance), r1.ID, domain.CountSaveInput{Lines: in}); err != nil {
		t.Fatal(err)
	}
	done, err := cs.Submit(as(uFinance), r1.ID)
	if err != nil || done.Status != domain.CountNoDiff {
		t.Fatalf("반려 표의 차이 없음 종료: %+v %v", done, err)
	}
	// 새 실사를 만들고 다시 반려시킨 뒤 폐기한다.
	r2 := reject()
	if err := cs.Discard(as(uFinance), r2.ID); err != nil {
		t.Fatalf("반려 표 폐기: %v", err)
	}
	if got, _ := cs.Get(as(uFinance), r2.ID); got.Status != domain.CountDiscarded {
		t.Fatalf("상태 %s", got.Status)
	}
	if _, err := cs.Create(as(uFinance), 1); err != nil {
		t.Fatalf("폐기 뒤 새 실사: %v", err)
	}
}

// 시스템 전용 양식(재고 조정 전표 ADJ)은 기안 시작 목록과 일반 양식 조회에 나오지 않고 양식 관리 목록에는 남는다.
func TestSystemOnlyFormHiddenFromDraftStart(t *testing.T) {
	e := newEnv(t)
	f := e.forms()
	list, err := f.ListUsable(as(uFinance))
	if err != nil {
		t.Fatal(err)
	}
	for _, x := range list {
		if x.Code == "ADJ" {
			t.Fatalf("기안 시작 목록에 재고 조정 전표가 보임: %+v", list)
		}
	}
	if _, err := f.Get(as(uFinance), "ADJ"); !errors.Is(err, contract.ErrNotFound) {
		t.Fatalf("일반 양식 조회: %v", err)
	}
	admin, err := f.List(as(uAdmin))
	found := false
	for _, x := range admin {
		found = found || x.Code == "ADJ"
	}
	if err != nil || !found {
		t.Fatalf("양식 관리 목록에는 남아야 함: %v", err)
	}
}
