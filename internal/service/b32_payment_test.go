package service_test

import (
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/service"
)

// ---- 시험 도구 ----

// payForm 은 PAY 양식의 조직 코드를 시험 조직에 맞춰 게시한다: 재무팀장 = FIN(재무2), 재무본부장 = BUY(부서장), 대표 = PL.
func (e *env) payForm(t *testing.T) {
	t.Helper()
	e.jvForm(t)
	f := e.forms()
	v, err := f.CreateVersionDraft(as(uAdmin), "PAY")
	if err != nil {
		t.Fatal(err)
	}
	def := v.Definition
	for i := range def.Line.Steps {
		switch def.Line.Steps[i].UnitCode {
		case "T-FIN":
			def.Line.Steps[i].UnitCode = "FIN"
		case "DIV-MG":
			def.Line.Steps[i].UnitCode = "BUY"
		case "CO":
			def.Line.Steps[i].UnitCode = "PL"
		}
	}
	def.Recipients.CC = nil
	if _, err := f.SaveVersionDraft(as(uAdmin), v.ID, domain.FormVersionInput{Definition: def}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Publish(as(uAdmin), v.ID); err != nil {
		t.Fatal(err)
	}
}

func (e *env) bank(t *testing.T, alias string) domain.BankAccount {
	t.Helper()
	b, err := e.funds.SaveAccount(as(uFinance), 0, domain.BankAccountInput{Alias: alias, BankName: "국민은행", AccountNo: "123-456-789012", OpeningBalance: 0, OpeningDate: "2026-01-01"})
	if err != nil {
		t.Fatalf("계좌 등록: %v", err)
	}
	return b
}

// payable 은 승인된 매입 전표(금액 amount)를 만들고 채무 항목 번호를 돌려준다.
func (e *env) payable(t *testing.T, vendorID, amount int64) int64 {
	t.Helper()
	e.approveJV(t, e.purchaseJV(t, vendorID, amount))
	var id int64
	if err := e.db.QueryRow(`SELECT MAX(id) FROM payables WHERE vendor_id = ?`, vendorID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func (e *env) payInput(v domain.Vendor, bankID int64, items ...domain.PaymentItemInput) domain.PaymentInput {
	return domain.PaymentInput{VendorID: v.ID, BankAccountID: bankID, Items: items}
}

func (e *env) payRow(t *testing.T, id int64) (paid, reserved int64) {
	t.Helper()
	if err := e.db.QueryRow(`SELECT paid_amount, reserved_amount FROM payables WHERE id = ?`, id).Scan(&paid, &reserved); err != nil {
		t.Fatal(err)
	}
	return
}

// approvePay 는 지급 요청서를 재무팀장(재무2)이 승인하게 한다(합계가 크면 uHead·uCEO 도 순서대로).
func (e *env) approvePay(t *testing.T, p domain.Payment, who ...int64) {
	t.Helper()
	if len(who) == 0 {
		who = []int64{uFinance2}
	}
	for _, w := range who {
		decideAs(t, e, w, p.DocID, true, "확인")
	}
}

// requested 는 지급 요청을 만들고 승인 요청까지 한다.
func (e *env) requested(t *testing.T, in domain.PaymentInput) domain.Payment {
	t.Helper()
	p, err := e.payments.Create(as(uFinance), in)
	if err != nil {
		t.Fatalf("작성: %v", err)
	}
	p, err = e.payments.RequestApproval(as(uFinance), p.No)
	if err != nil {
		t.Fatalf("승인 요청: %v", err)
	}
	return p
}

// ---- 시험 ----

// AC-1200: 지급 건 상태 전이는 표대로만 되고 표에 없는 전이는 전부 거부된다.
func TestPaymentTransitionTableOnly(t *testing.T) {
	ok := map[[2]string]string{
		{"draft", "request"}: "requested", {"requested", "approve"}: "approved", {"requested", "reject"}: "draft", {"requested", "reset"}: "draft",
		{"draft", "cancel"}: "canceled", {"requested", "cancel"}: "canceled", {"approved", "cancel"}: "canceled", {"approved", "settle"}: "paid", {"paid", "reverse"}: "paid_canceled",
	}
	states := []string{"draft", "requested", "approved", "paid", "canceled", "paid_canceled"}
	events := []string{"request", "approve", "reject", "reset", "cancel", "settle", "reverse"}
	for _, s := range states {
		for _, ev := range events {
			to, err := service.PaymentTransitionForTest(s, ev)
			want, allowed := ok[[2]string{s, ev}]
			if allowed && (err != nil || to != want) {
				t.Fatalf("%s/%s: %q %v(기대 %q)", s, ev, to, err, want)
			}
			if !allowed && !isErr(err, contract.ErrInvalidState) {
				t.Fatalf("%s/%s 는 거부되어야 함: %q %v", s, ev, to, err)
			}
		}
	}
}

// AC-1201·1206: 부분 지급 — 예약이 올라가고, 승인 후 다른 재무가 지급 처리하면 지급 전표(승인 상태)·누계·자금 행이 생기고 채무 검산이 맞는다.
func TestPaymentPartialFlowAndLedger(t *testing.T) {
	e := newEnv(t)
	e.payForm(t)
	v := e.vendor(t)
	bank := e.bank(t, "주거래")
	pid := e.payable(t, v.ID, 100_000)

	cands, err := e.payments.Candidates(as(uFinance), v.ID)
	if err != nil || len(cands) != 1 || cands[0].Available != 100_000 {
		t.Fatalf("후보: %+v %v", cands, err)
	}
	p := e.requested(t, e.payInput(v, bank.ID, domain.PaymentItemInput{PayableID: pid, Amount: 40_000}))
	if p.Status != domain.PaymentRequested || p.DocID == 0 || p.PayeeAccount == "110-123-456789" || !strings.HasSuffix(p.PayeeAccount, "6789") {
		t.Fatalf("요청: %+v", p)
	}
	if paid, res := e.payRow(t, pid); paid != 0 || res != 40_000 {
		t.Fatalf("예약: paid %d reserved %d", paid, res)
	}
	// 같은 항목의 남은 60,000 은 두 번째 요청으로 가능하고, 그 이상은 거부된다.
	if _, err := e.payments.Create(as(uFinance), e.payInput(v, bank.ID, domain.PaymentItemInput{PayableID: pid, Amount: 60_001})); !isErr(err, contract.ErrValidation) || !strings.Contains(err.Error(), "지급 가능액을 넘습니다") {
		t.Fatalf("초과 작성: %v", err)
	}
	e.approvePay(t, p)
	// 작성자는 지급 처리할 수 없다(D8).
	if _, err := e.payments.Settle(as(uFinance), p.No, domain.SettleInput{}); !isErr(err, contract.ErrForbidden) {
		t.Fatalf("작성자 처리: %v", err)
	}
	if _, err := e.payments.Settle(as(uAdmin), p.No, domain.SettleInput{}); !isErr(err, contract.ErrForbidden) {
		t.Fatalf("관리자 처리: %v", err)
	}
	done, err := e.payments.Settle(as(uFinance2), p.No, domain.SettleInput{TransferRef: "TR-0001", Memo: "시험 이체"})
	if err != nil || done.Status != domain.PaymentPaid || done.JournalNo == "" || done.TransferRef != "TR-0001" || done.PaidDate != "2026-11-10" {
		t.Fatalf("지급 처리: %+v %v", done, err)
	}
	if paid, res := e.payRow(t, pid); paid != 40_000 || res != 0 {
		t.Fatalf("누계: paid %d reserved %d", paid, res)
	}
	j, err := e.journals.Get(as(uFinance), done.JournalNo)
	if err != nil || j.Kind != domain.JournalAutoPayment || j.Status != domain.JournalApproved || j.DocID != p.DocID || j.SourceType != "payment" ||
		len(j.Lines) != 2 || j.Lines[0].AccountCode != "2110" || j.Lines[0].Side != "D" || j.Lines[0].Amount != 40_000 || j.Lines[0].VendorID != v.ID || j.Lines[1].AccountCode != "1020" || j.Lines[1].Side != "C" {
		t.Fatalf("지급 전표: %+v %v", j, err)
	}
	_, pay := e.ledgerSvcs()
	rep, err := pay.ByVendor(as(uFinance), "2026-11-10")
	if err != nil || rep.Total != 60_000 || !rep.Matches {
		t.Fatalf("채무 검산: %+v %v", rep, err)
	}
	d, err := e.funds.Daily(as(uFinance), domain.FundDailyFilter{From: "2026-11-01", To: "2026-11-30"})
	if err != nil || d.TotalWithdraw != 40_000 || d.Closing != -40_000 || d.LedgerBalance != -40_000 || !d.Matches || len(d.Rows) != 1 {
		t.Fatalf("자금 일보: %+v %v", d, err)
	}
	// 같은 건을 다시 처리할 수 없고, 같은 참조번호를 다른 지급 건에 쓸 수 없다.
	if _, err := e.payments.Settle(as(uFinance2), p.No, domain.SettleInput{}); !isErr(err, contract.ErrInvalidState) || !strings.Contains(err.Error(), "이미 지급 처리된") {
		t.Fatalf("재처리: %v", err)
	}
	second := e.requested(t, e.payInput(v, bank.ID, domain.PaymentItemInput{PayableID: pid, Amount: 60_000}))
	e.approvePay(t, second)
	if _, err := e.payments.Settle(as(uFinance2), second.No, domain.SettleInput{TransferRef: "TR-0001"}); !isErr(err, contract.ErrInvalidState) || !strings.Contains(err.Error(), "이체 참조번호는 이미") {
		t.Fatalf("참조번호 중복: %v", err)
	}
	if _, err := e.payments.Settle(as(uFinance2), second.No, domain.SettleInput{TransferRef: "TR-0002"}); err != nil {
		t.Fatalf("두 번째 지급: %v", err)
	}
	if rep, _ := pay.ByVendor(as(uFinance), "2026-11-10"); rep.Total != 0 || len(rep.Rows) != 0 || !rep.Matches {
		t.Fatalf("전액 지급 뒤: %+v", rep)
	}
	if _, err := e.payments.Create(as(uFinance), e.payInput(v, bank.ID, domain.PaymentItemInput{PayableID: pid, Amount: 1})); !isErr(err, contract.ErrValidation) {
		t.Fatalf("이미 전액 지급된 항목: %v", err)
	}
	for _, a := range []string{"payment.create", "payment.request", "payment.approve", "payment.settle"} {
		if !e.rec.Has(a, audit.TargetPayment, audit.ResultSuccess) {
			t.Errorf("감사 %s 없음", a)
		}
	}
	if !e.rec.Has("journal.auto_payment", audit.TargetJournal, audit.ResultSuccess) || !e.rec.Has("payment.denied_duplicate", audit.TargetPayment, audit.ResultDenied) {
		t.Error("지급 전표·중복 거부 감사가 없음")
	}
}

// AC-1201: 반려·회수·취소가 예약을 푼다.
func TestPaymentReservationReleased(t *testing.T) {
	e := newEnv(t)
	e.payForm(t)
	v := e.vendor(t)
	bank := e.bank(t, "주거래")
	pid := e.payable(t, v.ID, 100_000)
	in := e.payInput(v, bank.ID, domain.PaymentItemInput{PayableID: pid, Amount: 100_000})

	a := e.requested(t, in)
	if _, res := e.payRow(t, pid); res != 100_000 {
		t.Fatalf("예약 %d", res)
	}
	decideAs(t, e, uFinance2, a.DocID, false, "반려 사유")
	if got, _ := e.payments.Get(as(uFinance), a.No); got.Status != domain.PaymentDraft {
		t.Fatalf("반려 뒤 상태: %s", got.Status)
	}
	if _, res := e.payRow(t, pid); res != 0 {
		t.Fatalf("반려 뒤 예약 %d", res)
	}
	// 다시 요청한 뒤 승인 요청 취소(회수)
	if _, err := e.payments.RequestApproval(as(uFinance), a.No); err != nil {
		t.Fatalf("재요청: %v", err)
	}
	if _, err := e.payments.Cancel(as(uFinance), a.No, " "); !isErr(err, contract.ErrValidation) {
		t.Fatalf("사유 없는 취소: %v", err)
	}
	c, err := e.payments.Cancel(as(uFinance), a.No, "필요 없음")
	if err != nil || c.Status != domain.PaymentCanceled {
		t.Fatalf("취소: %+v %v", c, err)
	}
	if _, res := e.payRow(t, pid); res != 0 {
		t.Fatalf("취소 뒤 예약 %d", res)
	}
	// 승인된 뒤 지급 처리 전 취소도 예약을 푼다.
	b := e.requested(t, in)
	e.approvePay(t, b)
	if _, res := e.payRow(t, pid); res != 100_000 {
		t.Fatalf("승인 뒤 예약 유지 %d", res)
	}
	if _, err := e.payments.Cancel(as(uFinance), b.No, "승인 뒤 취소"); err != nil {
		t.Fatal(err)
	}
	if _, res := e.payRow(t, pid); res != 0 {
		t.Fatalf("승인 취소 뒤 예약 %d", res)
	}
	if _, err := e.payments.Settle(as(uFinance2), b.No, domain.SettleInput{}); !isErr(err, contract.ErrInvalidState) {
		t.Fatalf("취소된 건 지급 처리: %v", err)
	}
}

// AC-1202: 두 요청이 같은 항목의 마지막 잔액을 동시에 예약하면 한쪽만 성공한다.
func TestPaymentConcurrentReservation(t *testing.T) {
	e := newEnv(t)
	e.payForm(t)
	v := e.vendor(t)
	bank := e.bank(t, "주거래")
	pid := e.payable(t, v.ID, 100_000)
	in := e.payInput(v, bank.ID, domain.PaymentItemInput{PayableID: pid, Amount: 100_000})
	var nos []string
	for i := 0; i < 2; i++ {
		d, err := e.payments.Create(as(uFinance), in)
		if err != nil {
			t.Fatal(err)
		}
		nos = append(nos, d.No)
	}
	var wg sync.WaitGroup
	errs := make([]error, len(nos))
	for i := range nos {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = e.payments.RequestApproval(as(uFinance), nos[i])
		}(i)
	}
	wg.Wait()
	okN := 0
	for _, err := range errs {
		if err == nil {
			okN++
		} else if !strings.Contains(err.Error(), "지급 가능액을 넘습니다") {
			t.Fatalf("예상 밖 오류: %v", err)
		}
	}
	if okN != 1 {
		t.Fatalf("성공 %d건(기대 1): %v", okN, errs)
	}
	if paid, res := e.payRow(t, pid); paid != 0 || res != 100_000 {
		t.Fatalf("예약 %d/%d", paid, res)
	}
	// DB 트리거(CHECK)도 합계 초과를 막는다.
	if _, err := e.db.Exec(`UPDATE payables SET reserved_amount = reserved_amount + 1 WHERE id = ?`, pid); err == nil {
		t.Fatal("예약 초과가 DB 에서 통과됨")
	}
}

// AC-1204: 같은 건 동시 지급 처리는 한쪽만 성공한다.
func TestPaymentConcurrentSettleOnce(t *testing.T) {
	e := newEnv(t)
	e.payForm(t)
	v := e.vendor(t)
	bank := e.bank(t, "주거래")
	pid := e.payable(t, v.ID, 50_000)
	p := e.requested(t, e.payInput(v, bank.ID, domain.PaymentItemInput{PayableID: pid, Amount: 50_000}))
	e.approvePay(t, p)
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = e.payments.Settle(as(uFinance2), p.No, domain.SettleInput{})
		}(i)
	}
	wg.Wait()
	okN := 0
	for _, err := range errs {
		if err == nil {
			okN++
		}
	}
	if okN != 1 {
		t.Fatalf("성공 %d건(기대 1): %v", okN, errs)
	}
	if paid, res := e.payRow(t, pid); paid != 50_000 || res != 0 {
		t.Fatalf("누계 %d/%d", paid, res)
	}
	if e.col(t, `SELECT COUNT(*) FROM journals WHERE kind = 'auto_payment'`) != "1" || e.col(t, `SELECT COUNT(*) FROM fund_movements`) != "1" {
		t.Fatal("지급 전표·자금 행이 한 번만 만들어져야 함")
	}
}

// AC-1205: 지급 처리 트랜잭션 — 감사 실패를 주입하면 전부 취소되고 같은 건을 다시 처리할 수 있다.
func TestPaymentSettleAtomic(t *testing.T) {
	e := newEnv(t)
	e.payForm(t)
	v := e.vendor(t)
	bank := e.bank(t, "주거래")
	pid := e.payable(t, v.ID, 50_000)
	p := e.requested(t, e.payInput(v, bank.ID, domain.PaymentItemInput{PayableID: pid, Amount: 50_000}))
	e.approvePay(t, p)
	e.rec.FailWith(errors.New("감사 실패 주입"))
	if _, err := e.payments.Settle(as(uFinance2), p.No, domain.SettleInput{}); err == nil {
		t.Fatal("감사 실패인데 지급 처리됨")
	}
	e.rec.FailWith(nil)
	if paid, res := e.payRow(t, pid); paid != 0 || res != 50_000 {
		t.Fatalf("롤백 실패: %d/%d", paid, res)
	}
	if e.col(t, `SELECT COUNT(*) FROM payments`) != "0" || e.col(t, `SELECT COUNT(*) FROM fund_movements`) != "0" || e.col(t, `SELECT COUNT(*) FROM journals WHERE kind = 'auto_payment'`) != "0" {
		t.Fatal("지급 행·자금 행·지급 전표가 남음")
	}
	if got, _ := e.payments.Get(as(uFinance), p.No); got.Status != domain.PaymentApproved {
		t.Fatalf("상태 %s", got.Status)
	}
	if _, err := e.payments.Settle(as(uFinance2), p.No, domain.SettleInput{}); err != nil {
		t.Fatalf("재처리: %v", err)
	}
}

// AC-1207: 결재선 — 3천만원 이상이면 재무본부장, 1억원 이상이면 대표까지.
func TestPaymentLineByAmount(t *testing.T) {
	e := newEnv(t)
	e.payForm(t)
	v := e.vendor(t)
	bank := e.bank(t, "주거래")
	for _, c := range []struct {
		amount   int64
		deciders []int64
	}{{29_999_999, []int64{uFinance2}}, {30_000_000, []int64{uFinance2, uHead}}, {100_000_000, []int64{uFinance2, uHead, uCEO}}} {
		j := e.purchaseJV(t, v.ID, c.amount)
		j, err := e.journals.RequestApproval(as(uFinance), j.No)
		if err != nil {
			t.Fatal(err)
		}
		decideAs(t, e, uFinance2, j.DocID, true, "확인")
		if c.amount >= 100_000_000 {
			decideAs(t, e, uCEO, j.DocID, true, "확인") // 전표는 1억원 이상이면 대표(시험 조직의 재무본부장 자리)까지
		}
		var pid int64
		if err := e.db.QueryRow(`SELECT MAX(id) FROM payables`).Scan(&pid); err != nil {
			t.Fatal(err)
		}
		p := e.requested(t, e.payInput(v, bank.ID, domain.PaymentItemInput{PayableID: pid, Amount: c.amount}))
		for i, w := range c.deciders {
			got, _ := e.payments.Get(as(uFinance), p.No)
			if got.Status != domain.PaymentRequested {
				t.Fatalf("%d원: %d번째 결재 전에 승인됨", c.amount, i)
			}
			decideAs(t, e, w, p.DocID, true, "확인")
		}
		if got, _ := e.payments.Get(as(uFinance), p.No); got.Status != domain.PaymentApproved {
			t.Fatalf("%d원: 승인 안 됨 %s", c.amount, got.Status)
		}
	}
}

// AC-1210: 지급 취소 — 역분개 승인으로 누계가 복원되고 자금 일보에 반대 행이 남는다. 두 번 취소할 수 없다.
func TestPaymentReverseRestoresPayable(t *testing.T) {
	e := newEnv(t)
	e.payForm(t)
	v := e.vendor(t)
	bank := e.bank(t, "주거래")
	pid := e.payable(t, v.ID, 100_000)
	p := e.requested(t, e.payInput(v, bank.ID, domain.PaymentItemInput{PayableID: pid, Amount: 100_000}))
	e.approvePay(t, p)
	done, err := e.payments.Settle(as(uFinance2), p.No, domain.SettleInput{})
	if err != nil {
		t.Fatal(err)
	}
	// 지급이 끝난 매입 전표는 역분개할 수 없다(E8).
	purchase := e.col(t, `SELECT j.no FROM journals j JOIN payables p ON p.journal_id = j.id WHERE p.id = ?`, pid)
	if _, err := e.journals.Reverse(as(uFinance), purchase, ""); !isErr(err, contract.ErrValidation) || !strings.Contains(err.Error(), "지급이 시작된 채무") {
		t.Fatalf("지급 뒤 매입 전표 역분개: %v", err)
	}
	if _, err := e.payments.Reverse(as(uFinance), p.No, " "); !isErr(err, contract.ErrValidation) {
		t.Fatalf("사유 없는 지급 취소: %v", err)
	}
	rev, err := e.payments.Reverse(as(uFinance), p.No, "반송")
	if err != nil || rev.Status != domain.PaymentPaid || rev.ReversalNo == "" {
		t.Fatalf("지급 취소 요청: %+v %v", rev, err)
	}
	if _, err := e.payments.Reverse(as(uFinance), p.No, "또"); err == nil {
		t.Fatal("두 번째 지급 취소가 통과됨")
	}
	rj, _ := e.journals.Get(as(uFinance), rev.ReversalNo)
	if rj.Status != domain.JournalRequested || rj.DocID == 0 {
		t.Fatalf("역분개 전표가 승인 요청되지 않음: %+v", rj)
	}
	if paid, _ := e.payRow(t, pid); paid != 100_000 {
		t.Fatalf("승인 전에는 누계가 그대로여야 함: %d", paid)
	}
	decideAs(t, e, uFinance2, rj.DocID, true, "확인")
	if paid, res := e.payRow(t, pid); paid != 0 || res != 0 {
		t.Fatalf("복원: %d/%d", paid, res)
	}
	got, _ := e.payments.Get(as(uFinance), p.No)
	if got.Status != domain.PaymentPaidCanceled || got.ReverseReason != "반송" {
		t.Fatalf("상태: %+v", got)
	}
	_ = done
	d, _ := e.funds.Daily(as(uFinance), domain.FundDailyFilter{From: "2026-11-01", To: "2026-11-30"})
	if d.TotalWithdraw != 100_000 || d.TotalDeposit != 100_000 || d.Closing != 0 || !d.Matches {
		t.Fatalf("자금 일보: %+v", d)
	}
	if e.col(t, `SELECT COUNT(*) FROM fund_movements`) != "2" {
		t.Fatal("자금 행은 지우지 않고 반대 행을 더해야 함")
	}
	if _, err := e.db.Exec(`DELETE FROM fund_movements`); err == nil {
		t.Fatal("자금 행이 지워짐")
	}
	if _, err := e.db.Exec(`UPDATE payments SET memo = 'x'`); err == nil {
		t.Fatal("지급 행이 수정됨")
	}
	// 지급 취소된 항목은 새 요청으로 다시 지급할 수 있다.
	if cands, _ := e.payments.Candidates(as(uFinance), v.ID); len(cands) != 1 || cands[0].Available != 100_000 {
		t.Fatalf("재지급 후보: %+v", cands)
	}
}

// AC-1211·E7: 거래처 계좌가 없으면 요청할 수 없고, 승인 뒤 바뀌면 확인 없이는 처리할 수 없다. 계좌번호는 응답에 마스킹으로만 나간다.
func TestPaymentPayeeAccountRules(t *testing.T) {
	e := newEnv(t)
	e.payForm(t)
	v := e.vendor(t)
	bank := e.bank(t, "주거래")
	pid := e.payable(t, v.ID, 100_000)
	if _, err := e.db.Exec(`UPDATE vendors SET account_no = '' WHERE id = ?`, v.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.payments.Create(as(uFinance), e.payInput(v, bank.ID, domain.PaymentItemInput{PayableID: pid, Amount: 1000})); !isErr(err, contract.ErrValidation) || !strings.Contains(err.Error(), "거래처의 계좌번호가 등록되지 않았습니다") {
		t.Fatalf("계좌 없음: %v", err)
	}
	if _, err := e.db.Exec(`UPDATE vendors SET account_no = '110-123-456789' WHERE id = ?`, v.ID); err != nil {
		t.Fatal(err)
	}
	p := e.requested(t, e.payInput(v, bank.ID, domain.PaymentItemInput{PayableID: pid, Amount: 100_000}))
	e.approvePay(t, p)
	if _, err := e.db.Exec(`UPDATE vendors SET account_no = '999-888-777666' WHERE id = ?`, v.ID); err != nil {
		t.Fatal(err)
	}
	got, _ := e.payments.Get(as(uFinance), p.No)
	if !got.AccountDiffers || strings.Contains(got.PayeeAccount, "110-123") {
		t.Fatalf("계좌 변경 감지·마스킹: %+v", got)
	}
	if _, err := e.payments.Settle(as(uFinance2), p.No, domain.SettleInput{}); !isErr(err, contract.ErrInvalidState) || !strings.Contains(err.Error(), "거래처 계좌가 변경되었습니다") {
		t.Fatalf("확인 없는 처리: %v", err)
	}
	if _, err := e.payments.Settle(as(uFinance2), p.No, domain.SettleInput{ConfirmAccount: true}); err != nil {
		t.Fatalf("확인 후 처리: %v", err)
	}
	// 원문 보기: mask:bypass 만, 감사가 남고 감사 실패면 원문이 나가지 않는다.
	if _, err := e.funds.RevealPayee(as(uStaff), p.No); !isErr(err, contract.ErrForbidden) {
		t.Fatalf("직원 원문 보기: %v", err)
	}
	if _, err := e.funds.RevealPayee(as(uAuditor), p.No); !isErr(err, contract.ErrForbidden) {
		t.Fatalf("감사 원문 보기: %v", err)
	}
	e.rec.FailWith(errors.New("주입"))
	if raw, err := e.funds.RevealPayee(as(uFinance), p.No); err == nil || raw != "" {
		t.Fatalf("감사 실패인데 원문이 나감: %q %v", raw, err)
	}
	e.rec.FailWith(nil)
	raw, err := e.funds.RevealPayee(as(uFinance), p.No)
	if err != nil || raw != "110-123-456789" || !e.rec.Has("account.reveal", audit.TargetPayment, audit.ResultSuccess) {
		t.Fatalf("원문 보기: %q %v", raw, err)
	}
	for _, r := range e.rec.Records() {
		for _, d := range r.Diff {
			if strings.Contains(d.New, "110-123") || strings.Contains(d.Old, "110-123") || strings.Contains(d.New, "456789") {
				t.Fatalf("감사 diff 에 계좌번호가 들어감: %s %+v", r.Action, d)
			}
		}
		if strings.Contains(r.Detail, "110-123") {
			t.Fatalf("감사 비고에 계좌번호: %s", r.Detail)
		}
	}
}

// AC-1212: 같은 거래처·같은 합계·7일 안의 진행 중 건이 있으면 경고하되 막지 않는다.
func TestPaymentDuplicateWarning(t *testing.T) {
	e := newEnv(t)
	e.payForm(t)
	v := e.vendor(t)
	bank := e.bank(t, "주거래")
	a := e.payable(t, v.ID, 50_000)
	b := e.payable(t, v.ID, 50_000)
	first := e.requested(t, e.payInput(v, bank.ID, domain.PaymentItemInput{PayableID: a, Amount: 50_000}))
	second, err := e.payments.Create(as(uFinance), e.payInput(v, bank.ID, domain.PaymentItemInput{PayableID: b, Amount: 50_000}))
	if err != nil || len(second.Warnings) != 1 || !strings.Contains(second.Warnings[0], first.No) {
		t.Fatalf("중복 경고: %+v %v", second, err)
	}
}

// AC-1209: 마감된 달의 지급일은 처리가 거부되고, 마감 전 점검에는 승인 요청 중(차단)·승인 대기(경고)가 나온다.
func TestPaymentMonthClose(t *testing.T) {
	e := newEnv(t)
	e.closeFixture(t)
	e.now = at(2026, 11, 25, 1)
	e.payForm(t)
	v := e.vendor(t)
	bank := e.bank(t, "주거래")
	a := e.payable(t, v.ID, 50_000)
	b := e.payable(t, v.ID, 30_000)
	approved := e.requested(t, e.payInput(v, bank.ID, domain.PaymentItemInput{PayableID: a, Amount: 50_000}))
	e.approvePay(t, approved)
	pending := e.requested(t, e.payInput(v, bank.ID, domain.PaymentItemInput{PayableID: b, Amount: 30_000}))
	_ = pending
	// 예정일(12-05)이 속한 12월 점검에서 확인한다.
	pc, err := e.monthClose.Precheck(as(uFinance), "2026-12")
	if err != nil {
		t.Fatal(err)
	}
	blk, warn := false, false
	for _, x := range pc.Blockers {
		blk = blk || strings.Contains(x.Title, "승인 요청 중인 지급 요청서")
	}
	for _, x := range pc.Warnings {
		warn = warn || strings.Contains(x.Title, "지급 처리 전 승인 건")
	}
	if !blk || !warn {
		t.Fatalf("점검: 차단 %v 경고 %v (%+v)", blk, warn, pc)
	}
	// 승인 요청 중 건을 정리하고 11월을 마감한 뒤 11월 지급일로 처리하면 거부된다.
	if _, err := e.payments.Cancel(as(uFinance), pending.No, "정리"); err != nil {
		t.Fatal(err)
	}
	e.now = at(2026, 12, 2, 1)
	if _, err := e.monthClose.Close(as(uFinance), "2026-10"); err != nil {
		t.Fatalf("10월 마감: %v", err)
	}
	if _, err := e.monthClose.Close(as(uFinance), "2026-11"); err != nil {
		t.Fatalf("11월 마감: %v", err)
	}
	if _, err := e.payments.Settle(as(uFinance2), approved.No, domain.SettleInput{PaidDate: "2026-11-28"}); !isErr(err, contract.ErrValidation) || !strings.Contains(err.Error(), "마감된 월입니다. 재무팀에 마감 해제를 요청하세요.") {
		t.Fatalf("마감 월 지급: %v", err)
	}
	if _, err := e.payments.Settle(as(uFinance2), approved.No, domain.SettleInput{PaidDate: "2026-12-02"}); err != nil {
		t.Fatalf("열린 달 지급: %v", err)
	}
}

// AC-1230·1231·1232: 자금 계좌·입금 기록·자금 일보. 기초 잔액은 수정할 수 없고, 입금은 임시 전표를 만들며 승인 뒤에야 잔액에 들어간다.
func TestFundAccountsDepositsAndDaily(t *testing.T) {
	e := newEnv(t)
	e.payForm(t)
	if _, err := e.funds.SaveAccount(as(uAuditor), 0, domain.BankAccountInput{Alias: "x", BankName: "x", AccountNo: "123-4567", OpeningDate: "2026-01-01"}); !isErr(err, contract.ErrForbidden) {
		t.Fatalf("감사 계좌 등록: %v", err)
	}
	bank := e.bank(t, "주거래")
	if bank.AccountNo != "***-***-**9012" {
		t.Fatalf("마스킹: %q", bank.AccountNo)
	}
	if _, err := e.db.Exec(`UPDATE bank_accounts SET opening_balance = 5 WHERE id = ?`, bank.ID); err == nil {
		t.Fatal("기초 잔액이 수정됨")
	}
	if _, err := e.funds.SaveAccount(as(uFinance), 0, domain.BankAccountInput{Alias: "주거래", BankName: "x", AccountNo: "123-4567", OpeningDate: "2026-01-01"}); !isErr(err, contract.ErrValidation) {
		t.Fatalf("별칭 중복: %v", err)
	}
	if _, err := e.funds.RecordDeposit(as(uAdmin), domain.DepositInput{BankAccountID: bank.ID, Date: "2026-11-09", Amount: 1000, Memo: "관리자"}); !isErr(err, contract.ErrForbidden) {
		t.Fatalf("관리자 입금 기록: %v", err)
	}
	j, err := e.funds.RecordDeposit(as(uFinance), domain.DepositInput{BankAccountID: bank.ID, Date: "2026-11-09", Amount: 250_000, Memo: "환급금 입금"})
	if err != nil || j.Status != domain.JournalDraft || j.Kind != domain.JournalGeneral || len(j.Lines) != 2 || j.Lines[0].AccountCode != "1020" || j.Lines[1].AccountCode != "4910" {
		t.Fatalf("입금 전표: %+v %v", j, err)
	}
	d, _ := e.funds.Daily(as(uAuditor), domain.FundDailyFilter{From: "2026-11-01", To: "2026-11-30"})
	if d.TotalDeposit != 0 || d.Closing != 0 || d.PendingCount != 1 || len(d.Rows) != 1 || d.Rows[0].PendingDeposit != 250_000 || !d.Matches {
		t.Fatalf("승인 전 입금은 표시만: %+v", d)
	}
	e.approveJV(t, j)
	d, _ = e.funds.Daily(as(uFinance), domain.FundDailyFilter{From: "2026-11-01", To: "2026-11-30"})
	if d.TotalDeposit != 250_000 || d.Closing != 250_000 || d.LedgerBalance != 250_000 || !d.Matches {
		t.Fatalf("승인 뒤: %+v", d)
	}
	next, _ := e.funds.Daily(as(uFinance), domain.FundDailyFilter{From: "2026-11-10", To: "2026-11-30"})
	if next.Opening != 250_000 || len(next.Rows) != 0 || next.Closing != 250_000 {
		t.Fatalf("이월 잔액: %+v", next)
	}
	accs, _ := e.funds.Accounts(as(uFinance), false)
	if len(accs) != 1 || accs[0].Balance != 250_000 {
		t.Fatalf("계좌 잔액: %+v", accs)
	}
	if _, err := e.funds.RecordDeposit(as(uFinance), domain.DepositInput{BankAccountID: bank.ID, Date: "2026-11-09", Amount: 0, Memo: "x"}); !isErr(err, contract.ErrValidation) {
		t.Fatalf("0원: %v", err)
	}
	if _, err := e.funds.SetActive(as(uFinance), bank.ID, false); err != nil {
		t.Fatal(err)
	}
	if _, err := e.funds.RecordDeposit(as(uFinance), domain.DepositInput{BankAccountID: bank.ID, Date: "2026-11-09", Amount: 1000, Memo: "중지 계좌"}); !isErr(err, contract.ErrValidation) {
		t.Fatalf("중지 계좌 입금: %v", err)
	}
	if _, err := e.funds.Daily(as(uFinance), domain.FundDailyFilter{From: "2025-01-01", To: "2026-11-30"}); !isErr(err, contract.ErrValidation) {
		t.Fatalf("366일 초과: %v", err)
	}
	_ = strconv.Itoa
}

// AC-1233: 지급 일정표 — 만기 도래·승인된 지급·승인 요청 중, 만기 경과 합계, 자금 부족 경고.
func TestPaymentCalendar(t *testing.T) {
	e := newEnv(t)
	e.payForm(t)
	v := e.vendor(t)
	bank := e.bank(t, "주거래")
	a := e.payable(t, v.ID, 80_000) // 만기 2026-12-05
	b := e.payable(t, v.ID, 20_000)
	approved := e.requested(t, e.payInput(v, bank.ID, domain.PaymentItemInput{PayableID: a, Amount: 80_000}))
	e.approvePay(t, approved)
	cal, err := e.payments.Calendar(as(uAuditor), "2026-12-01", "2026-12-07")
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]int{}
	for _, x := range cal.Entries {
		kinds[x.Kind]++
	}
	if kinds["due"] != 1 || kinds["payment"] != 1 || cal.ScheduledTotal != 80_000 || cal.OverdueTotal != 0 || !cal.ShortageWarn {
		t.Fatalf("일정표: %+v", cal)
	}
	_ = b
	if _, err := e.payments.Calendar(as(uFinance), "2026-12-07", "2026-12-01"); !isErr(err, contract.ErrValidation) {
		t.Fatalf("기간 오류: %v", err)
	}
	if _, err := e.payments.Calendar(as(uStaff), "", ""); !isErr(err, contract.ErrForbidden) {
		t.Fatalf("직원 일정표: %v", err)
	}
	e.now = at(2026, 12, 20, 1)
	late, _ := e.payments.Calendar(as(uFinance), "2026-12-20", "2026-12-26")
	if late.OverdueTotal != 20_000 {
		t.Fatalf("만기 경과: %+v", late)
	}
	delayed := false
	for _, x := range late.Entries {
		delayed = delayed || x.Delayed
	}
	if !delayed {
		t.Fatalf("지연 건이 일정표에 없음: %+v", late)
	}
}

// 권한: 관리자·감사는 조회만, 지급·계좌 쓰기는 재무만.
func TestPaymentPermissions(t *testing.T) {
	e := newEnv(t)
	e.payForm(t)
	v := e.vendor(t)
	bank := e.bank(t, "주거래")
	pid := e.payable(t, v.ID, 10_000)
	in := e.payInput(v, bank.ID, domain.PaymentItemInput{PayableID: pid, Amount: 10_000})
	for _, who := range []int64{uAdmin, uAuditor, uStaff, uLead} {
		if _, err := e.payments.Create(as(who), in); !isErr(err, contract.ErrForbidden) {
			t.Errorf("%d 작성: %v", who, err)
		}
	}
	for _, who := range []int64{uAdmin, uAuditor} {
		if _, err := e.payments.List(as(who), domain.PaymentFilter{}); err != nil {
			t.Errorf("%d 조회: %v", who, err)
		}
		if _, err := e.funds.Accounts(as(who), false); err != nil {
			t.Errorf("%d 계좌 조회: %v", who, err)
		}
	}
	for _, who := range []int64{uStaff, uLead} {
		if _, err := e.payments.List(as(who), domain.PaymentFilter{}); !isErr(err, contract.ErrForbidden) {
			t.Errorf("%d 조회: %v", who, err)
		}
	}
	if _, err := e.funds.SaveAccount(as(uAdmin), 0, domain.BankAccountInput{Alias: "관리자계좌", BankName: "x", AccountNo: "123-4567", OpeningDate: "2026-01-01"}); err != nil {
		t.Errorf("관리자 계좌 등록(계좌만 가능): %v", err)
	}
}
