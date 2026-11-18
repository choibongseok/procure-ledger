package service_test

import (
	"testing"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
)

// 카드 소지자 변경(재무·관리자): 변경 이력이 남고, 변경 전에 올라온 내역은 원 소지자에게 남으며, 이후 업로드는 새 소지자다. 감사는 조회만 한다.
func TestCardHolderChangeHistoryAndPermissions(t *testing.T) {
	e := newEnv(t)
	c := e.card(t) // 소지자 uStaff
	e.importCards(t, cardRow("2026-11-08 12:00:00", "스타벅스", 12_500, "A1", "승인"))
	for _, who := range []int64{uAuditor, uStaff, uLead} {
		if _, err := e.cards.ChangeHolder(as(who), c.ID, uStaff2); !isErr(err, contract.ErrForbidden) {
			t.Errorf("%d 소지자 변경: %v", who, err)
		}
	}
	got, err := e.cards.ChangeHolder(as(uAdmin), c.ID, uStaff2)
	if err != nil || got.HolderID != uStaff2 {
		t.Fatalf("관리자 변경: %+v %v", got, err)
	}
	if _, err := e.cards.ChangeHolder(as(uFinance), c.ID, uStaff); err != nil {
		t.Fatal(err)
	}
	hist, err := e.cards.HolderHistory(as(uAuditor), c.ID)
	if err != nil || len(hist) != 3 || hist[0].HolderName != "직원" || hist[1].HolderName != "영업직원" || hist[2].HolderName != "직원" || hist[0].ChangedBy == "" {
		t.Fatalf("이력(최근 먼저): %+v %v", hist, err)
	}
	if _, err := e.cards.HolderHistory(as(uStaff), c.ID); !isErr(err, contract.ErrForbidden) {
		t.Fatalf("직원의 이력 조회: %v", err)
	}
	// 변경 전 내역은 원 소지자에게 남고, 변경 뒤 올라온 내역은 그때의 소지자다.
	if _, err := e.cards.ChangeHolder(as(uAdmin), c.ID, uStaff2); err != nil {
		t.Fatal(err)
	}
	if t1 := e.txnByApproval(t, "A1", "approve"); t1.HolderID != uStaff {
		t.Fatalf("기존 내역 소지자: %d", t1.HolderID)
	}
	e.importCards(t, cardRow("2026-11-09 12:00:00", "새가맹점", 3_000, "A2", "승인"))
	if t2 := e.txnByApproval(t, "A2", "approve"); t2.HolderID != uStaff2 {
		t.Fatalf("새 내역 소지자: %d", t2.HolderID)
	}
	if !e.rec.Has("card.holder_change", audit.TargetCard, audit.ResultSuccess) {
		t.Error("소지자 변경 감사가 없음")
	}
	// 퇴사한 소지자의 미소명 건 재지정: 관리자도 할 수 있고 감사·직원은 못 한다.
	if _, err := e.db.Exec(`UPDATE users SET active = 0 WHERE id = ?`, uStaff); err != nil {
		t.Fatal(err)
	}
	t1 := e.txnByApproval(t, "A1", "approve")
	if _, err := e.cards.ReassignHolder(as(uAuditor), t1.ID, uStaff2); !isErr(err, contract.ErrForbidden) {
		t.Fatalf("감사 재지정: %v", err)
	}
	if moved, err := e.cards.ReassignHolder(as(uAdmin), t1.ID, uStaff2); err != nil || moved.HolderID != uStaff2 {
		t.Fatalf("관리자 재지정: %+v %v", moved, err)
	}
	_ = domain.CardTxnUnreconciled
}
