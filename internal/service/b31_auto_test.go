package service_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/dbutil"
	"github.com/choibongseok/procure-ledger/internal/service"
)

// 자동 전표(B31 단계 2) 시험 도구: 발주·입고를 만들고 판정 행을 심어 훅을 직접 부른다(확정 트랜잭션과 같은 방식으로 tx 를 넘긴다).

func (e *env) verdictFor(t *testing.T, po domain.PurchaseOrder, receiptID int64) int64 {
	t.Helper()
	docID, err := dbutil.InsertID(context.Background(), e.db, `INSERT INTO documents (kind, filename, content_type, sha256, uploaded_by, department_id, po_id, receipt_id, created_at) VALUES ('transaction_statement','x.txt','text/plain','h',1,1,?,?,'2026-11-10T00:00:00Z')`, po.ID, receiptID)
	if err != nil {
		t.Fatal(err)
	}
	id, err := dbutil.InsertID(context.Background(), e.db, `INSERT INTO document_verdicts (document_id, po_id, receipt_id, outcome, items_json, created_at, confirmed_by, confirmed_outcome, confirmed_at) VALUES (?,?,?, 'match','[]','2026-11-10T00:00:00Z',4,'match','2026-11-10T00:00:00Z')`, docID, po.ID, nullInt(receiptID))
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func nullInt(v int64) any {
	if v == 0 {
		return nil
	}
	return v
}

// receivedPO 는 거래처의 단가·수량 발주를 만들어 전량 입고하고 입고 번호를 돌려준다.
func (e *env) receivedPO(t *testing.T, price, qty int64) (domain.PurchaseOrder, int64) {
	t.Helper()
	skuSeqAuto++
	v := e.poVendor(t, fmt.Sprintf("자동상사%d", skuSeqAuto), fmt.Sprintf("777-77-%05d", skuSeqAuto))
	po := e.orderFor(t, v, "", price, qty)
	rc, err := e.inventory.Receive(as(uFinance), recv(po.ID, po.Lines[0].ID, po.Lines[0].Quantity, "", ""))
	if err != nil {
		t.Fatal(err)
	}
	return po, rc.ID
}

var skuSeqAuto int

func (e *env) hook(t *testing.T, who int64, v domain.Verdict) (domain.JournalOutcome, error) {
	t.Helper()
	var out domain.JournalOutcome
	ctx := as(who)
	err := contract.SQLTxManager{DB: e.db}.WithTx(ctx, func(tx contract.Tx) error {
		var herr error
		out, herr = e.journals.OnVerdictConfirmed(ctx, tx, v)
		return herr
	})
	return out, err
}

func (e *env) queueReasons(t *testing.T) []string {
	t.Helper()
	rows, err := e.db.Query(`SELECT reason FROM journal_queue WHERE source_type = 'verdict' AND reason <> 'pre_adoption' ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		out = append(out, s)
	}
	return out
}

func matchVerdict(id int64, po domain.PurchaseOrder, rc int64, total *int64) domain.Verdict {
	v := domain.Verdict{ID: id, POID: po.ID, ReceiptID: rc, ConfirmedOutcome: domain.OutcomeMatch, ConfirmedBy: 4}
	if total != nil {
		v.Extraction = domain.Extraction{TotalAmount: *total, Supplier: "x", Lines: []domain.ExtractedLine{{Name: "x", Quantity: 1}}}
	}
	return v
}

// AC-1110: 일치 확정이 임시 매입 전표(재고자산/부가세대급금/외상매입금)를 만든다. 금액은 입고×발주 단가이고 부가세는 버림이다(12,345원 → 1,234원).
func TestAutoPurchaseJournalAmountsAndVatFloor(t *testing.T) {
	e := newEnv(t)
	po, rc := e.receivedPO(t, 12_345, 1)
	total := int64(12_345)
	out, err := e.hook(t, uFinance, matchVerdict(e.verdictFor(t, po, rc), po, rc, &total))
	if err != nil || !out.Created || out.JournalNo == "" {
		t.Fatalf("%+v %v", out, err)
	}
	j, err := e.journals.Get(as(uFinance), out.JournalNo)
	if err != nil {
		t.Fatal(err)
	}
	if j.Kind != domain.JournalAutoPurchase || j.Status != domain.JournalDraft || j.SourceType != "verdict" || len(j.Lines) != 3 || len(j.Evidence) != 1 {
		t.Fatalf("전표: %+v", j)
	}
	want := []struct {
		code, side string
		amt        int64
	}{{"1210", "D", 12_345}, {"1310", "D", 1_234}, {"2110", "C", 13_579}}
	for i, w := range want {
		l := j.Lines[i]
		if l.AccountCode != w.code || l.Side != w.side || l.Amount != w.amt {
			t.Errorf("줄 %d: %+v (기대 %+v)", i+1, l, w)
		}
	}
	if j.Lines[2].VendorID == 0 {
		t.Error("외상매입금 줄에 거래처가 없음")
	}
	// 같은 판정으로 다시 부르면 같은 전표다(멱등, E14).
	again, err := e.hook(t, uFinance, matchVerdict(e.lastVerdict(t), po, rc, &total))
	if err != nil || again.Created || again.JournalNo != out.JournalNo {
		t.Fatalf("같은 판정 재호출: %+v %v", again, err)
	}
}

func (e *env) lastVerdict(t *testing.T) int64 {
	t.Helper()
	var id int64
	if err := e.db.QueryRow(`SELECT MAX(id) FROM document_verdicts`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

// stopInventoryRule 은 재고자산 규칙을 새 계정(1290)으로 돌리고 그 계정을 중지(true)하거나 다시 쓰게(false) 한다. 시스템 계정은 중지할 수 없어서 규칙 대상을 바꿔 시험한다.
func (e *env) stopInventoryRule(t *testing.T, stop bool) {
	t.Helper()
	if stop {
		if _, err := e.accounts.Create(as(uFinance), domain.AccountInput{Code: "1290", Name: "시험 재고"}); err != nil {
			t.Fatal(err)
		}
		if err := e.accounts.SetRule(as(uFinance), "purchase.inventory", "1290"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := e.accounts.SetActive(as(uFinance), "1290", !stop); err != nil {
		t.Fatal(err)
	}
}

func (e *env) queue() *service.JournalQueueService { return e.jqueue }

// 공급가액 9원 이하는 부가세 줄이 없다(E16).
func TestAutoPurchaseNoVatLineForTinyAmount(t *testing.T) {
	e := newEnv(t)
	po, rc := e.receivedPO(t, 9, 1)
	out, err := e.hook(t, uFinance, matchVerdict(e.verdictFor(t, po, rc), po, rc, nil))
	if err != nil || !out.Created {
		t.Fatalf("%+v %v", out, err)
	}
	j, _ := e.journals.Get(as(uFinance), out.JournalNo)
	if len(j.Lines) != 2 || j.Lines[0].Amount != 9 || j.Lines[1].Amount != 9 {
		t.Fatalf("줄: %+v", j.Lines)
	}
}

// AC-1111: 같은 입고의 두 번째 증빙은 전표를 만들지 않고 기존 전표에 증빙만 연결한다. 취소되면 새 전표를 만든다(E7).
func TestAutoPurchaseOneJournalPerReceipt(t *testing.T) {
	e := newEnv(t)
	po, rc := e.receivedPO(t, 1000, 100)
	total := int64(100_000)
	first, err := e.hook(t, uFinance, matchVerdict(e.verdictFor(t, po, rc), po, rc, &total))
	if err != nil || !first.Created {
		t.Fatalf("%+v %v", first, err)
	}
	second, err := e.hook(t, uFinance, matchVerdict(e.verdictFor(t, po, rc), po, rc, &total))
	if err != nil || second.Created || !second.Attached || second.JournalNo != first.JournalNo {
		t.Fatalf("두 번째 증빙: %+v %v", second, err)
	}
	j, _ := e.journals.Get(as(uFinance), first.JournalNo)
	if len(j.Evidence) != 2 {
		t.Fatalf("증빙 연결 %d건(기대 2)", len(j.Evidence))
	}
	if _, err := e.journals.Cancel(as(uFinance), first.JournalNo, "다시 만들기"); err != nil {
		t.Fatal(err)
	}
	third, err := e.hook(t, uFinance, matchVerdict(e.verdictFor(t, po, rc), po, rc, &total))
	if err != nil || !third.Created || third.JournalNo == first.JournalNo {
		t.Fatalf("취소 뒤 새 전표: %+v %v", third, err)
	}
}

// AC-1112·1113: 불일치·보류 확정, 합계 불일치, 규칙 계정 중지는 전표를 만들지 않고 전표 대기에 오른다. 확정(훅)은 오류 없이 끝난다.
func TestAutoPurchaseQueuesInsteadOfFailing(t *testing.T) {
	e := newEnv(t)
	po, rc := e.receivedPO(t, 1000, 100)
	mm := matchVerdict(e.verdictFor(t, po, rc), po, rc, nil)
	mm.ConfirmedOutcome = domain.OutcomeMismatch
	if out, err := e.hook(t, uFinance, mm); err != nil || !out.Queued || out.Created {
		t.Fatalf("불일치 확정: %+v %v", out, err)
	}
	hd := matchVerdict(e.verdictFor(t, po, rc), po, rc, nil)
	hd.ConfirmedOutcome = domain.OutcomeHold
	if out, err := e.hook(t, uFinance, hd); err != nil || !out.Queued {
		t.Fatalf("보류 확정: %+v %v", out, err)
	}
	bad := int64(90_000)
	if out, err := e.hook(t, uFinance, matchVerdict(e.verdictFor(t, po, rc), po, rc, &bad)); err != nil || !out.Queued || !strings.Contains(out.Reason, "증빙 합계와 입고 금액이 다름") {
		t.Fatalf("합계 불일치: %+v %v", out, err)
	}
	e.stopInventoryRule(t, true)
	ok := int64(100_000)
	out, err := e.hook(t, uFinance, matchVerdict(e.verdictFor(t, po, rc), po, rc, &ok))
	if err != nil || !out.Queued || !strings.Contains(out.Reason, "규칙 계정 사용 불가: 재고자산") {
		t.Fatalf("규칙 계정 중지: %+v %v", out, err)
	}
	got := strings.Join(e.queueReasons(t), ",")
	if got != "mismatch_confirmed,hold_confirmed,total_differs,rule_account_inactive" {
		t.Fatalf("대기 사유: %s", got)
	}
	if e.col(t, `SELECT COUNT(*) FROM journals WHERE kind = 'auto_purchase'`) != "0" {
		t.Fatal("전표가 만들어짐")
	}
}

// 추출 값이 없는 판정(보류)을 재무가 일치로 확정하면 합계 비교를 건너뛰고 입고 금액으로 전표를 만든다(3.3.2).
func TestAutoPurchaseSkipsTotalCheckWithoutExtraction(t *testing.T) {
	e := newEnv(t)
	po, rc := e.receivedPO(t, 1000, 100)
	out, err := e.hook(t, uFinance, matchVerdict(e.verdictFor(t, po, rc), po, rc, nil))
	if err != nil || !out.Created {
		t.Fatalf("%+v %v", out, err)
	}
	j, _ := e.journals.Get(as(uFinance), out.JournalNo)
	if j.TotalAmount != 110_000 {
		t.Fatalf("합계 %d", j.TotalAmount)
	}
}

// 전표 대기 처리: 규칙을 고친 뒤 다시 만들기, 수동 입력 연결, 전표 불필요(사유 필수), 처리된 행은 다시 처리할 수 없다.
func TestJournalQueueHandling(t *testing.T) {
	e := newEnv(t)
	q := e.queue()
	po, rc := e.receivedPO(t, 1000, 100)
	total := int64(100_000)
	e.stopInventoryRule(t, true)
	if out, err := e.hook(t, uFinance, matchVerdict(e.verdictFor(t, po, rc), po, rc, &total)); err != nil || !out.Queued {
		t.Fatalf("%+v %v", out, err)
	}
	items, err := q.List(as(uAuditor), "pending")
	if err != nil || len(items) != 1 || items[0].Reason != domain.QueueRuleAccount {
		t.Fatalf("목록: %+v %v", items, err)
	}
	if _, err := q.Retry(as(uStaff), items[0].ID, ""); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("권한 없는 처리: %v", err)
	}
	if _, err := q.Retry(as(uAuditor), items[0].ID, ""); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("감사는 처리할 수 없음: %v", err)
	}
	// 규칙 계정이 아직 중지라 다시 만들기는 대기로 남는다.
	again, err := q.Retry(as(uFinance), items[0].ID, "")
	if err != nil || again.Status != domain.QueuePending {
		t.Fatalf("계속 실패: %+v %v", again, err)
	}
	e.stopInventoryRule(t, false)
	done, err := q.Retry(as(uFinance), items[0].ID, "")
	if err != nil || done.Status != domain.QueueCreated || done.JournalNo == "" {
		t.Fatalf("다시 만들기: %+v %v", done, err)
	}
	if _, err := q.Skip(as(uFinance), items[0].ID, "필요 없음"); !errors.Is(err, contract.ErrInvalidState) {
		t.Fatalf("처리된 건 재처리: %v", err)
	}
	if _, err := e.db.Exec(`DELETE FROM journal_queue`); err == nil {
		t.Fatal("대기 기록이 지워짐")
	}
	// 불일치 확정 건: 다시 만들기는 거부, 불필요는 사유 필수, 수동은 전표 연결.
	po2, rc2 := e.receivedPO(t, 500, 10)
	mm := matchVerdict(e.verdictFor(t, po2, rc2), po2, rc2, nil)
	mm.ConfirmedOutcome = domain.OutcomeMismatch
	if _, err := e.hook(t, uFinance, mm); err != nil {
		t.Fatal(err)
	}
	pend, _ := q.List(as(uFinance), "pending")
	if len(pend) != 1 {
		t.Fatalf("처리 전 %d건", len(pend))
	}
	if _, err := q.Retry(as(uFinance), pend[0].ID, ""); !errors.Is(err, contract.ErrInvalidState) {
		t.Fatalf("불일치 다시 만들기: %v", err)
	}
	if _, err := q.Skip(as(uFinance), pend[0].ID, " "); !errors.Is(err, contract.ErrValidation) {
		t.Fatalf("사유 없는 불필요: %v", err)
	}
	man, err := q.Manual(as(uFinance), pend[0].ID, done.JournalNo)
	if err != nil || man.Status != domain.QueueManual || man.JournalNo != done.JournalNo {
		t.Fatalf("수동: %+v %v", man, err)
	}
}

// AC-1114: 지출 결의서 최종 승인이 임시 비용 전표(비용/미지급금)를 만들고 지출 구분별 규칙 계정을 쓴다. 부가세는 분리하지 않는다.
func TestAutoExpenseJournalOnExpenseApproval(t *testing.T) {
	e := newEnv(t)
	att, docs := e.attach()
	ev, err := att.Upload(as(uStaff), "영수증.pdf", pdfBytes)
	if err != nil {
		t.Fatal(err)
	}
	d, err := docs.CreateDraft(as(uStaff), domain.DocDraftInput{FormCode: "EXP", Title: "출장비", Fields: map[string]any{
		"spend_date": "2026-11-05", "spend_kind": "출장", "payee": "홍길동", "evidence": []any{float64(ev.ID)},
		"items": []any{map[string]any{"name": "KTX", "amount": float64(60000)}}, "total": float64(60000)}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := docs.Submit(as(uStaff), d.ID); err != nil {
		t.Fatal(err)
	}
	res := decideAs(t, e, uLead, d.ID, true, "팀장") // 6만원은 팀장 한 단계로 끝난다
	if res.Request.Status != domain.PRApproved {
		t.Fatalf("결재 완료가 아님: %s", res.Request.Status)
	}
	page, err := e.journals.List(as(uFinance), domain.JournalFilter{Kind: domain.JournalAutoExpense})
	if err != nil || len(page.Rows) != 1 {
		t.Fatalf("비용 전표: %+v %v", page, err)
	}
	j, _ := e.journals.Get(as(uFinance), page.Rows[0].No)
	if j.Status != domain.JournalDraft || j.SourceType != "approval_doc" || j.SourceID != d.ID || len(j.Lines) != 2 || j.TradeDate != "2026-11-05" {
		t.Fatalf("전표: %+v", j)
	}
	if j.Lines[0].AccountCode != "5230" || j.Lines[0].Side != "D" || j.Lines[1].AccountCode != "2120" || j.Lines[1].Amount != 60000 {
		t.Fatalf("줄: %+v", j.Lines)
	}
	// 구매 품의서는 전표를 만들지 않는다.
	if p := e.submitted(t, uStaff, 500_000); p.ID == 0 {
		t.Fatal("준비")
	}
	approve(t, e, uLead, e.lastPR(t))
	page, _ = e.journals.List(as(uFinance), domain.JournalFilter{Kind: domain.JournalAutoExpense})
	if len(page.Rows) != 1 {
		t.Fatalf("구매 품의서에서 전표가 생김: %d", len(page.Rows))
	}
}

func (e *env) lastPR(t *testing.T) int64 {
	t.Helper()
	var id int64
	if err := e.db.QueryRow(`SELECT MAX(id) FROM purchase_requests`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

// 일괄 승인 요청: 20건 상한, 건별 독립(한 건 실패가 다른 건을 막지 않는다).
func TestJournalRequestBatch(t *testing.T) {
	e := newEnv(t)
	e.jvForm(t)
	v := e.vendor(t)
	a := e.purchaseJV(t, v.ID, 1000)
	b := e.purchaseJV(t, v.ID, 2000)
	res, err := e.journals.RequestApprovalBatch(as(uFinance), []string{a.No, "JV-0000-00000", b.No, a.No})
	if err != nil || len(res) != 3 {
		t.Fatalf("%+v %v", res, err)
	}
	if !res[0].OK || res[1].OK || res[1].Error == "" || !res[2].OK {
		t.Fatalf("건별 결과: %+v", res)
	}
	many := make([]string, 21)
	for i := range many {
		many[i] = strings.Repeat("x", i+1)
	}
	if _, err := e.journals.RequestApprovalBatch(as(uFinance), many); !errors.Is(err, contract.ErrValidation) {
		t.Fatalf("상한: %v", err)
	}
	if _, err := e.journals.RequestApprovalBatch(as(uStaff), []string{a.No}); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("권한: %v", err)
	}
	_ = context.Background
}
