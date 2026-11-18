package main

import (
	"context"
	"database/sql"
	"fmt"
	"testing"

	"github.com/choibongseok/procure-ledger/evals"
	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/llmgw"
)

// 기준 구현(시험 전용): 명세 31 의 3.3 규칙을 메모리에 그대로 옮긴 전표 훅이다. 실제 전표 서비스(erp)가 없어도
// 골든셋 12건이 실제 증빙 파이프라인(mock 추출 → 판정 → 확정)과 하니스 채점 위에서 서로 맞는지 확인한다.
type refRig struct {
	db       *sql.DB
	inactive map[string]bool
	journals map[int64][]*evals.JournalExpect1 // 입고 번호 -> 전표
	queue    map[int64][]string                // 발주 번호 -> 대기 사유
	poOf     map[int64]int64                   // 입고 번호 -> 발주 번호
}

func newRefRig(db *sql.DB) *refRig {
	return &refRig{db: db, inactive: map[string]bool{}, journals: map[int64][]*evals.JournalExpect1{}, queue: map[int64][]string{}, poOf: map[int64]int64{}}
}

func (r *refRig) Hook() domain.PurchaseJournalHook { return r }

func (r *refRig) SetInactiveAccounts(_ context.Context, codes []string) error {
	r.inactive = map[string]bool{}
	for _, c := range codes {
		r.inactive[c] = true
	}
	return nil
}

func (r *refRig) Result(_ context.Context, poID int64) (journalResult, error) {
	var res journalResult
	for rc, js := range r.journals {
		if r.poOf[rc] != poID {
			continue
		}
		for _, j := range js {
			res.Journals = append(res.Journals, *j)
		}
	}
	res.QueueReasons = r.queue[poID]
	return res, nil
}

func (r *refRig) OnVerdictConfirmed(ctx context.Context, tx contract.Tx, v domain.Verdict) (domain.JournalOutcome, error) {
	q := func(reason string) (domain.JournalOutcome, error) {
		r.queue[v.POID] = append(r.queue[v.POID], reason)
		return domain.JournalOutcome{Queued: true, Reason: reason}, nil
	}
	switch v.ConfirmedOutcome {
	case domain.OutcomeMismatch:
		return q("mismatch_confirmed")
	case domain.OutcomeHold:
		return q("hold_confirmed")
	}
	// 금액: 입고 줄 수량 × 발주 줄 단가의 합(LLM 값이 아니다).
	rows, err := tx.QueryContext(ctx, `SELECT rl.quantity, pl.unit_price FROM receipt_lines rl JOIN po_lines pl ON pl.id = rl.po_line_id WHERE rl.receipt_id = ?`, v.ReceiptID)
	if err != nil {
		return domain.JournalOutcome{}, err
	}
	var supply int64
	for rows.Next() {
		var qty, price int64
		if err := rows.Scan(&qty, &price); err != nil {
			rows.Close()
			return domain.JournalOutcome{}, err
		}
		supply += qty * price
	}
	rows.Close()
	var vendor string
	if err := tx.QueryRowContext(ctx, `SELECT v.name FROM purchase_orders p JOIN vendors v ON v.id = p.vendor_id WHERE p.id = ?`, v.POID).Scan(&vendor); err != nil {
		return domain.JournalOutcome{}, err
	}
	// 추출 값이 있는 판정만 문서 합계와 비교한다. 추출이 없는(판단 보류) 판정은 비교 대상이 없어 건너뛴다(재무가 확정한 책임).
	if len(v.Extraction.Lines) > 0 && v.Extraction.TotalAmount != supply {
		return q("total_differs")
	}
	if r.inactive["1210"] {
		return q("rule_account_inactive")
	}
	r.poOf[v.ReceiptID] = v.POID
	if js := r.journals[v.ReceiptID]; len(js) > 0 { // 한 입고 한 전표: 증빙만 연결
		js[0].Evidence++
		return domain.JournalOutcome{Attached: true}, nil
	}
	vat := supply / 10
	lines := []evals.JournalLine{{Account: "1210", Side: "D", Amount: supply}}
	if vat > 0 {
		lines = append(lines, evals.JournalLine{Account: "1310", Side: "D", Amount: vat})
	}
	lines = append(lines, evals.JournalLine{Account: "2110", Side: "C", Amount: supply + vat})
	r.journals[v.ReceiptID] = []*evals.JournalExpect1{{Kind: "auto_purchase", Status: "draft", Vendor: vendor, Evidence: 1, Lines: lines}}
	return domain.JournalOutcome{Created: true, JournalNo: fmt.Sprintf("JV-TEST-%05d", v.ReceiptID)}, nil
}

func withRefRig(t *testing.T) {
	t.Helper()
	old := newJournalRig
	newJournalRig = func(db *sql.DB, _ contract.TxManager, _ audit.Recorder) (journalRig, error) {
		return newRefRig(db), nil
	}
	t.Cleanup(func() { newJournalRig = old })
}

// 골든셋 12건이 기준 구현 위에서 전부 통과하고(critical 포함) 하니스 합격 기준(90%, 하한 10)에 든다.
func TestJournalGoldenSetPassesOnReferenceRig(t *testing.T) {
	withRefRig(t)
	rep, err := Run(context.Background(), Config{Provider: llmgw.MockProvider{}, PolicyPattern: "mock*", Only: "journals"})
	if err != nil {
		t.Fatal(err)
	}
	p, total := rep.counts("journals")
	if total != 12 || p != 12 {
		for _, c := range rep.Results {
			t.Logf("%s pass=%v %s", c.ID, c.Pass, c.Detail)
		}
		t.Fatalf("자동 전표 케이스 %d/%d 통과(전부 통과해야 함)", p, total)
	}
	if !rep.Passed() {
		t.Fatal("합격 기준에 들어야 함")
	}
}

// 채점기가 실제로 틀린 전표를 잡는지: 규칙을 어기는 구현(부가세를 반올림, 금액을 문서 합계로)은 케이스가 실패한다.
type wrongRig struct{ *refRig }

func (w wrongRig) OnVerdictConfirmed(ctx context.Context, tx contract.Tx, v domain.Verdict) (domain.JournalOutcome, error) {
	out, err := w.refRig.OnVerdictConfirmed(ctx, tx, v)
	for rc, js := range w.refRig.journals {
		_ = rc
		for _, j := range js {
			for i := range j.Lines {
				if j.Lines[i].Account == "1310" {
					j.Lines[i].Amount++ // 부가세가 한 푼 어긋남
					j.Lines[len(j.Lines)-1].Amount++
				}
			}
		}
	}
	return out, err
}
func (w wrongRig) Hook() domain.PurchaseJournalHook { return w }

func TestJournalRunnerCatchesWrongJournal(t *testing.T) {
	old := newJournalRig
	newJournalRig = func(db *sql.DB, _ contract.TxManager, _ audit.Recorder) (journalRig, error) {
		return wrongRig{newRefRig(db)}, nil
	}
	t.Cleanup(func() { newJournalRig = old })
	rep, err := Run(context.Background(), Config{Provider: llmgw.MockProvider{}, PolicyPattern: "mock*", Only: "journals"})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Passed() {
		t.Fatal("틀린 전표 구현은 합격하면 안 됨")
	}
	failedCritical := 0
	for _, c := range rep.Results {
		if c.Critical && !c.Pass {
			failedCritical++
		}
	}
	if failedCritical == 0 {
		t.Fatal("critical 주입 케이스도 틀린 금액을 잡아야 함")
	}
}

func TestJournalSuiteIsSkippedWithoutRigAndRejectedWhenAskedAlone(t *testing.T) {
	old := newJournalRig
	newJournalRig = nil
	t.Cleanup(func() { newJournalRig = old })
	rep, err := Run(context.Background(), Config{Provider: llmgw.MockProvider{}, PolicyPattern: "mock*", Only: "quotes"})
	if err != nil {
		t.Fatal(err)
	}
	if _, n := rep.counts("journals"); n != 0 {
		t.Fatal("전표 서비스가 연결되지 않았으면 journals 묶음은 없어야 함")
	}
	if _, err := Run(context.Background(), Config{Provider: llmgw.MockProvider{}, PolicyPattern: "mock*", Only: "journals"}); err == nil {
		t.Fatal("연결 전에 --only journals 는 오류여야 함")
	}
}
