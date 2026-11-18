package service_test

import (
	"testing"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/service"
)

// AC-1230: 기초 잔액이 있는 계좌에서 조회 시작일이 기초 기준일과 같아도(경계) 일보 행의 기초·기말에 기초 잔액이 들어간다.
func TestFundDailyOpeningBalanceBoundary(t *testing.T) {
	e := newEnv(t)
	bank, err := e.funds.SaveAccount(as(uFinance), 0, domain.BankAccountInput{Alias: "주거래", BankName: "국민은행", AccountNo: "123-456-789012", OpeningBalance: 50_000_000, OpeningDate: "2026-11-05"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.Exec(`INSERT INTO fund_movements (bank_account_id, move_date, kind, amount, memo, created_by, created_at) VALUES (?, '2026-11-08', 'payment', -91000, '시험 출금', 4, '2026-11-08T00:00:00Z')`, bank.ID); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name, from string
		opening    int64
	}{{"시작일 = 기초 기준일", "2026-11-05", 0}, {"시작일이 기준일보다 앞", "2026-11-01", 0}, {"시작일이 기준일 뒤", "2026-11-06", 50_000_000}} {
		d, err := e.funds.Daily(as(uFinance), domain.FundDailyFilter{From: c.from, To: "2026-11-30"})
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if len(d.Rows) != 1 || d.Rows[0].Opening != 50_000_000 || d.Rows[0].Closing != 49_909_000 || d.Closing != 49_909_000 || d.Opening != c.opening {
			t.Fatalf("%s: 이월 %d 행 %+v 기말 %d", c.name, d.Opening, d.Rows, d.Closing)
		}
	}
}

// AC-1260: "내 카드 내역" 단건 경로는 본인 소지분만 열린다. 재무라도 타인 소지 내역은 찾을 수 없음이고, 전체 조회는 card:read_all 경로(GetTxn)로만 한다.
func TestCardMineGetIsOwnOnly(t *testing.T) {
	e := newEnv(t)
	e.card(t)
	e.importCards(t, cardRow("2026-11-08 12:00:00", "스타벅스", 12_500, "A1", "승인"))
	t1 := e.txnByApproval(t, "A1", "approve")
	if got, err := e.cards.GetMyTxn(as(uStaff), t1.ID); err != nil || got.ID != t1.ID {
		t.Fatalf("본인: %+v %v", got, err)
	}
	for _, who := range []int64{uFinance, uStaff2, uLead} {
		if _, err := e.cards.GetMyTxn(as(who), t1.ID); !isErr(err, contract.ErrNotFound) && !isErr(err, contract.ErrForbidden) {
			t.Errorf("%d 가 타인 내역을 엶: %v", who, err)
		}
	}
	if _, err := e.cards.GetTxn(as(uFinance), t1.ID); err != nil {
		t.Fatalf("재무 전체 조회 경로: %v", err)
	}
}

// 부가세 집계: 단위 없이 시작일·종료일만 오면 사용자 지정 기간으로 읽는다(기본값으로 조용히 바꾸지 않는다).
func TestVATFromToWithoutUnitIsCustom(t *testing.T) {
	e := newEnv(t)
	vat := service.NewVATService(e.journals.Base)
	got, err := vat.PurchaseSummary(as(uFinance), domain.VATFilter{From: "2026-01-01", To: "2026-03-31"})
	if err != nil || got.From != "2026-01-01" || got.To != "2026-03-31" {
		t.Fatalf("%+v %v", got, err)
	}
	if _, err := vat.PurchaseSummary(as(uFinance), domain.VATFilter{From: "2025-01-01", To: "2026-11-30"}); !isErr(err, contract.ErrValidation) {
		t.Fatalf("12개월 초과: %v", err)
	}
	if _, err := vat.PurchaseSummary(as(uFinance), domain.VATFilter{From: "2026-02-01"}); !isErr(err, contract.ErrValidation) {
		t.Fatalf("종료일 없음: %v", err)
	}
}
