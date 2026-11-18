package service_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/sod"
)

// B43 1단계(erp): 세금계산서 모델·상태 전이·발행 검증·SoD·전송 작업 입구·수기 등록.

func (e *env) taxCompany(t *testing.T) {
	t.Helper()
	if _, err := e.company.Update(as(uAdmin), domain.CompanyInput{Name: "에이엑스 주식회사", BusinessNo: "123-45-67890", Representative: "홍길동", Address: "서울"}, 0); err != nil {
		t.Fatal(err)
	}
}

func taxInput(date string, supply int64) domain.TaxInvoiceInput {
	return domain.TaxInvoiceInput{IssueDate: date, Buyer: domain.TaxParty{BizNo: "220-81-62517", Name: "한빛상사", Rep: "김한빛", Email: "tax@hanbit.example"},
		Lines: []domain.TaxLineInput{{Name: "복사용지 A4", Qty: 10, UnitPrice: supply / 10, Supply: supply}}}
}

func (e *env) taxDraft(t *testing.T, supply int64) domain.TaxInvoice {
	t.Helper()
	v, err := e.tax.Create(as(uFinance), taxInput("2026-11-09", supply))
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func taxErr(t *testing.T, err, want error) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("오류 기대 %v, 실제 %v", want, err)
	}
}

func TestTaxInvoiceCreateComputesTaxAndSnapshots(t *testing.T) {
	e := newEnv(t)
	e.taxCompany(t)
	v := e.taxDraft(t, 100_000)
	if v.Status != domain.TaxDraft || v.Direction != domain.TaxSale || v.SupplyTotal != 100_000 || v.TaxTotal != 10_000 || v.Total != 110_000 || !strings.HasPrefix(v.No, "TI-") {
		t.Fatalf("작성: %+v", v)
	}
	if v.Supplier.BizNo != "1234567890" || v.Supplier.Name != "에이엑스 주식회사" || v.Buyer.BizNo != "2208162517" {
		t.Fatalf("공급자는 회사 정보, 사업자번호는 숫자만: %+v %+v", v.Supplier, v.Buyer)
	}
	if v.Buyer.Email == "tax@hanbit.example" || v.Buyer.Email == "" {
		t.Fatalf("이메일은 가려서 나간다: %q", v.Buyer.Email)
	}
	// 영세율·면세는 세액 0, 줄별 절사.
	zero := taxInput("2026-11-09", 100_000)
	zero.TaxType = domain.TaxZero
	if z, err := e.tax.Create(as(uFinance), zero); err != nil || z.TaxTotal != 0 || z.Total != 100_000 {
		t.Fatalf("영세율: %v %+v", err, z)
	}
	odd := taxInput("2026-11-09", 0)
	odd.Lines = []domain.TaxLineInput{{Name: "a", Supply: 15}, {Name: "b", Supply: 15}} // 줄별 1원 절사 ×2 = 2원, 합계 30의 10% = 3원과 다르다
	if o, err := e.tax.Create(as(uFinance), odd); err != nil || o.TaxTotal != 2 {
		t.Fatalf("세액은 줄별 절사의 합: %v %+v", err, o)
	}
	// 입력 검증.
	for name, in := range map[string]domain.TaxInvoiceInput{
		"작성일 형식":    func() domain.TaxInvoiceInput { i := taxInput("2026/11/09", 1000); return i }(),
		"줄 없음":      func() domain.TaxInvoiceInput { i := taxInput("2026-11-09", 1000); i.Lines = nil; return i }(),
		"음수 공급가액":   func() domain.TaxInvoiceInput { i := taxInput("2026-11-09", 1000); i.Lines[0].Supply = -5; return i }(),
		"사업자번호 자릿수": func() domain.TaxInvoiceInput { i := taxInput("2026-11-09", 1000); i.Buyer.BizNo = "123"; return i }(),
		"과세 유형":     func() domain.TaxInvoiceInput { i := taxInput("2026-11-09", 1000); i.TaxType = "x"; return i }(),
	} {
		if _, err := e.tax.Create(as(uFinance), in); !errors.Is(err, contract.ErrValidation) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestTaxInvoicePermissions(t *testing.T) {
	e := newEnv(t)
	e.taxCompany(t)
	v := e.taxDraft(t, 50_000)
	// 읽기: 재무·관리자·감사. 쓰기·발행: 재무만. 직원·팀장은 아무것도 못 한다.
	for _, u := range []int64{uFinance, uAdmin, uAuditor} {
		if _, err := e.tax.Get(as(u), v.ID); err != nil {
			t.Errorf("사용자 %d 읽기: %v", u, err)
		}
	}
	for _, u := range []int64{uStaff, 2} {
		if _, err := e.tax.Get(as(u), v.ID); !errors.Is(err, contract.ErrForbidden) {
			t.Errorf("사용자 %d 읽기는 거부: %v", u, err)
		}
		if _, err := e.tax.Create(as(u), taxInput("2026-11-09", 1000)); !errors.Is(err, contract.ErrForbidden) {
			t.Errorf("사용자 %d 작성은 거부: %v", u, err)
		}
	}
	for _, u := range []int64{uAdmin, uAuditor} {
		if _, err := e.tax.Create(as(u), taxInput("2026-11-09", 1000)); !errors.Is(err, contract.ErrForbidden) {
			t.Errorf("사용자 %d 작성은 거부(관리자·감사는 쓰지 않는다): %v", u, err)
		}
		if _, err := e.tax.Issue(as(u), v.ID); !errors.Is(err, contract.ErrForbidden) {
			t.Errorf("사용자 %d 발행은 거부: %v", u, err)
		}
	}
	if !e.rec.Has("taxinvoice.create", "tax_invoice", "denied") {
		t.Error("거부는 감사에 남는다")
	}
}

// 전이 표: 모든 (방향, 상태, 이벤트) 조합이 표와 같이 움직이고 표에 없으면 거부된다. DB 트리거도 같은 쌍을 허용·거부한다.
func TestTaxInvoiceTransitionTableMatchesTrigger(t *testing.T) {
	e := newEnv(t)
	statuses := []string{domain.TaxDraft, domain.TaxIssued, domain.TaxSent, domain.TaxAccepted, domain.TaxFailed, domain.TaxCanceled}
	allowed := map[[3]string]bool{}
	for _, tr := range [][3]string{
		{"sale", "draft", "issued"}, {"sale", "draft", "canceled"}, {"sale", "issued", "sent"}, {"sale", "issued", "failed"}, {"sale", "sent", "accepted"}, {"sale", "sent", "failed"},
		{"sale", "failed", "issued"}, {"sale", "failed", "canceled"}, {"purchase", "accepted", "canceled"},
	} {
		allowed[tr] = true
	}
	n := 0
	for _, dir := range []string{"sale", "purchase"} {
		for _, from := range statuses {
			for _, to := range statuses {
				if from == to {
					continue
				}
				n++
				approval := ""
				if from == "accepted" {
					approval = strings.Repeat("A", 22) + string(rune('0'+n/10%10)) + string(rune('0'+n%10))
				}
				// 시작 상태의 행을 직접 만든다(트리거는 UPDATE 만 막는다).
				var id int64
				err := e.db.QueryRow(`INSERT INTO tax_invoices (seq_no, no, direction, tax_type, status, approval_no, issue_date, created_by, created_at, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?) RETURNING id`,
					1000+n, "TX-"+strings.Repeat("0", 3)+string(rune('A'+n%26))+string(rune('a'+n/26)), dir, "taxable", from, approval, "2026-11-09", 4, "2026-11-09T00:00:00Z", "2026-11-09T00:00:00Z").Scan(&id)
				if err != nil {
					t.Fatal(err)
				}
				_, uerr := e.db.Exec(`UPDATE tax_invoices SET status = ? WHERE id = ?`, to, id)
				if want := allowed[[3]string{dir, from, to}]; (uerr == nil) != want {
					t.Errorf("트리거: %s %s → %s 허용 기대 %v, 오류 %v", dir, from, to, want, uerr)
				}
			}
		}
	}
}

func TestTaxInvoiceIssueFlowAndFrozenFields(t *testing.T) {
	e := newEnv(t)
	v := e.taxDraft(t, 100_000)
	// 회사 정보가 없으면 발행은 거부된다.
	if _, err := e.tax.Issue(as(uFinance), v.ID); !errors.Is(err, contract.ErrInvalidState) || !strings.Contains(err.Error(), "회사 정보") {
		t.Fatalf("회사 정보 없음: %v", err)
	}
	e.taxCompany(t)
	// 미래 작성일 거부.
	fut, _ := e.tax.Create(as(uFinance), taxInput("2026-11-20", 1000))
	if _, err := e.tax.Issue(as(uFinance), fut.ID); !errors.Is(err, contract.ErrValidation) {
		t.Fatalf("미래 작성일: %v", err)
	}
	got, err := e.tax.Issue(as(uFinance), v.ID)
	if err != nil || got.Status != domain.TaxIssued || got.IssuedBy != uFinance || got.NextTryAt == "" {
		t.Fatalf("발행: %v %+v", err, got)
	}
	if _, err := e.tax.Update(as(uFinance), v.ID, taxInput("2026-11-09", 5000)); !errors.Is(err, contract.ErrInvalidState) {
		t.Fatalf("발행 뒤 수정 거부: %v", err)
	}
	if _, err := e.tax.Issue(as(uFinance), v.ID); !errors.Is(err, contract.ErrInvalidState) {
		t.Fatalf("발행 두 번 거부: %v", err)
	}
	if _, err := e.tax.Cancel(as(uFinance), v.ID, "사유"); !errors.Is(err, contract.ErrInvalidState) {
		t.Fatalf("발행 대기 건은 취소할 수 없다: %v", err)
	}
	// DB 직접 변경 방어: 금액·삭제.
	if _, err := e.db.Exec(`UPDATE tax_invoices SET total = 1, supply_total = 1, tax_total = 0 WHERE id = ?`, v.ID); err == nil {
		t.Fatal("발행 뒤 금액을 DB 에서 고칠 수 있다")
	}
	if _, err := e.db.Exec(`DELETE FROM tax_invoices WHERE id = ?`, v.ID); err == nil {
		t.Fatal("세금계산서를 지울 수 있다")
	}
	if _, err := e.db.Exec(`UPDATE tax_invoice_lines SET supply = 1 WHERE invoice_id = ?`, v.ID); err == nil {
		t.Fatal("발행 뒤 줄을 고칠 수 있다")
	}
	if !e.rec.Has("taxinvoice.issue", "tax_invoice", "success") {
		t.Error("발행 감사")
	}
}

func TestTaxInvoiceSoDCreateIssue(t *testing.T) {
	f := &fakeSoD{mode: map[string]string{sod.RuleTaxCreateIssue: sod.ModeBlock}}
	e := newEnvSoD(t, f)
	e.taxCompany(t)
	v := e.taxDraft(t, 100_000) // 작성자 = uFinance
	if _, err := e.tax.Issue(as(uFinance), v.ID); err == nil {
		t.Fatal("작성자가 발행하면 차단 모드에서 막혀야 한다")
	} else if _, ok := sod.IsBlocked(err); !ok {
		t.Fatalf("SoD 차단 오류: %v", err)
	}
	if got, _ := e.tax.Get(as(uFinance), v.ID); got.Status != domain.TaxDraft {
		t.Fatalf("차단되면 상태가 그대로: %s", got.Status)
	}
	if cs := f.rules(sod.RuleTaxCreateIssue); len(cs) == 0 || cs[0].Other != uFinance || cs[0].Subject.Type != "tax_invoice" {
		t.Fatalf("판정 호출: %+v", cs)
	}
	// 다른 재무 담당자는 발행할 수 있다.
	if got, err := e.tax.Issue(as(uFinance2), v.ID); err != nil || got.Status != domain.TaxIssued {
		t.Fatalf("다른 사람의 발행: %v %+v", err, got)
	}
}

func TestTaxInvoiceDispatchStoreFullFlow(t *testing.T) {
	e := newEnv(t)
	e.taxCompany(t)
	ctx := context.Background()
	v := e.taxDraft(t, 100_000)
	if _, err := e.tax.Issue(as(uFinance2), v.ID); err != nil {
		t.Fatal(err)
	}
	e.now = e.now.Add(time.Second)
	jobs, err := e.tax.ClaimDue(ctx, e.now, 10)
	if err != nil || len(jobs) != 1 || jobs[0].Kind != "issue" || jobs[0].Invoice.ID != v.ID || jobs[0].Attempt != 1 {
		t.Fatalf("가져가기: %v %+v", err, jobs)
	}
	if again, _ := e.tax.ClaimDue(ctx, e.now, 10); len(again) != 0 {
		t.Fatal("가져간 건을 다시 가져가면 안 된다")
	}
	// 일시 오류 → 1분 뒤 재시도.
	if err := e.tax.Retry(ctx, v.ID, "unavailable", 503); err != nil {
		t.Fatal(err)
	}
	if got, _ := e.tax.Get(as(uFinance), v.ID); got.Status != domain.TaxIssued || got.Attempts != 1 || got.LastCode != "unavailable" || got.LastCodeText == "" {
		t.Fatalf("재시도 예약: %+v", got)
	}
	if early, _ := e.tax.ClaimDue(ctx, e.now.Add(30*time.Second), 10); len(early) != 0 {
		t.Fatal("백오프 전에는 가져가지 않는다")
	}
	e.now = e.now.Add(61 * time.Second)
	jobs, _ = e.tax.ClaimDue(ctx, e.now, 10)
	if len(jobs) != 1 || jobs[0].Attempt != 2 {
		t.Fatalf("재시도 가져가기: %+v", jobs)
	}
	if err := e.tax.Submitted(ctx, v.ID, "REF-1", 202); err != nil {
		t.Fatal(err)
	}
	if got, _ := e.tax.Get(as(uFinance), v.ID); got.Status != domain.TaxSent || got.ProviderRef != "REF-1" {
		t.Fatalf("전송됨: %+v", got)
	}
	// 아직 수신 전 → 다음 조회 예약, 이어서 수신 확인.
	e.now = e.now.Add(2 * time.Minute)
	jobs, _ = e.tax.ClaimDue(ctx, e.now, 10)
	if len(jobs) != 1 || jobs[0].Kind != "status" || jobs[0].ProviderRef != "REF-1" {
		t.Fatalf("상태 조회 가져가기: %+v", jobs)
	}
	if err := e.tax.StillPending(ctx, v.ID); err != nil {
		t.Fatal(err)
	}
	e.now = e.now.Add(10 * time.Minute)
	if jobs, _ = e.tax.ClaimDue(ctx, e.now, 10); len(jobs) != 1 {
		t.Fatalf("다음 조회: %+v", jobs)
	}
	approval := "20261110A1B2C3D4E5F60001"
	if err := e.tax.Accepted(ctx, v.ID, approval); err != nil {
		t.Fatal(err)
	}
	got, _ := e.tax.Get(as(uFinance), v.ID)
	if got.Status != domain.TaxAccepted || got.ApprovalNo != approval || got.AcceptedAt == "" || len(got.Transmissions) < 3 {
		t.Fatalf("국세청 수신: %+v", got)
	}
	for _, tr := range got.Transmissions {
		if tr.CodeLabel == "" {
			t.Errorf("전송 기록의 한국어 문장: %+v", tr)
		}
	}
	// 접수된 건은 취소·재전송·수정이 안 된다.
	if _, err := e.tax.Cancel(as(uFinance), v.ID, "사유"); !errors.Is(err, contract.ErrInvalidState) {
		t.Fatalf("접수 건 취소: %v", err)
	}
	if _, err := e.tax.Resend(as(uFinance), v.ID); !errors.Is(err, contract.ErrInvalidState) {
		t.Fatalf("접수 건 재전송: %v", err)
	}
	// 같은 승인번호를 다른 매출 건이 받으면 failed(duplicate_approval_no).
	v2 := e.taxDraft(t, 20_000)
	e.tax.Issue(as(uFinance2), v2.ID)
	e.now = e.now.Add(time.Second)
	e.tax.ClaimDue(ctx, e.now, 10)
	if err := e.tax.Submitted(ctx, v2.ID, "REF-2", 202); err != nil {
		t.Fatal(err)
	}
	if err := e.tax.Accepted(ctx, v2.ID, approval); err != nil {
		t.Fatal(err)
	}
	if got, _ := e.tax.Get(as(uFinance), v2.ID); got.Status != domain.TaxFailed || got.LastCode != "duplicate_approval_no" || got.ApprovalNo != "" {
		t.Fatalf("중복 승인번호: %+v", got)
	}
	for _, a := range []string{"taxinvoice.send", "taxinvoice.accept", "taxinvoice.fail"} {
		if !e.rec.Has(a, "tax_invoice", "success") {
			t.Errorf("감사 %s", a)
		}
	}
}

func TestTaxInvoiceRetryLimitFailAndResend(t *testing.T) {
	e := newEnv(t)
	e.taxCompany(t)
	ctx := context.Background()
	v := e.taxDraft(t, 100_000)
	e.tax.Issue(as(uFinance2), v.ID)
	// 자동 재시도 5회(백오프 1분·5분·30분·2시간·6시간) 뒤 여섯 번째 실패는 retry_over.
	for i := 0; i < 6; i++ {
		e.now = e.now.Add(7 * time.Hour)
		jobs, _ := e.tax.ClaimDue(ctx, e.now, 10)
		if len(jobs) != 1 {
			t.Fatalf("%d번째 가져가기: %+v", i+1, jobs)
		}
		if err := e.tax.Retry(ctx, v.ID, "network", 0); err != nil {
			t.Fatal(err)
		}
	}
	got, _ := e.tax.Get(as(uFinance), v.ID)
	if got.Status != domain.TaxFailed || got.LastCode != "retry_over" || got.Attempts != 6 {
		t.Fatalf("재시도 초과: %+v", got)
	}
	// 수동 다시 전송은 건당 5회.
	for i := 0; i < 5; i++ {
		if _, err := e.tax.Resend(as(uFinance), v.ID); err != nil {
			t.Fatalf("%d번째 다시 전송: %v", i+1, err)
		}
		e.now = e.now.Add(time.Hour)
		e.tax.ClaimDue(ctx, e.now, 10)
		if err := e.tax.Fail(ctx, v.ID, "rejected_validation", 422); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := e.tax.Resend(as(uFinance), v.ID); !errors.Is(err, contract.ErrInvalidState) {
		t.Fatalf("여섯 번째 다시 전송은 거부: %v", err)
	}
	if got, err := e.tax.Cancel(as(uFinance), v.ID, "반려되어 새로 작성"); err != nil || got.Status != domain.TaxCanceled {
		t.Fatalf("포기(취소): %v %+v", err, got)
	}
}

func (e *env) taxAccepted(t *testing.T, supply int64, approval string) domain.TaxInvoice {
	t.Helper()
	ctx := context.Background()
	v := e.taxDraft(t, supply)
	if _, err := e.tax.Issue(as(uFinance2), v.ID); err != nil {
		t.Fatal(err)
	}
	e.now = e.now.Add(time.Second)
	e.tax.ClaimDue(ctx, e.now, 10)
	if err := e.tax.Submitted(ctx, v.ID, "R-"+approval[:6], 202); err != nil {
		t.Fatal(err)
	}
	if err := e.tax.Accepted(ctx, v.ID, approval); err != nil {
		t.Fatal(err)
	}
	got, _ := e.tax.Get(as(uFinance), v.ID)
	return got
}

func TestTaxInvoiceAmendRules(t *testing.T) {
	e := newEnv(t)
	e.taxCompany(t)
	orig := e.taxAccepted(t, 100_000, "20261110AAAAAAAAAAAA0001")
	// 환입: 원 건 줄을 음수로 복사한 초안이 만들어지고 발행된다.
	am, err := e.tax.AmendDraft(as(uFinance), orig.ID, domain.AmendReturn)
	if err != nil || am.AmendOfID != orig.ID || am.AmendReason != domain.AmendReturn || am.SupplyTotal != -100_000 || am.TaxTotal != -10_000 || am.Status != domain.TaxDraft {
		t.Fatalf("수정 초안: %v %+v", err, am)
	}
	// 원 공급가액을 넘는 환입은 거부, 양수 환입도 거부.
	over := taxInput("2026-11-10", 1)
	over.Lines = []domain.TaxLineInput{{Name: "환입", Supply: -150_000}}
	over.AmendReason = domain.AmendReturn
	if _, err := e.tax.Update(as(uFinance), am.ID, over); err != nil {
		t.Fatal(err)
	}
	if _, err := e.tax.Issue(as(uFinance2), am.ID); !errors.Is(err, contract.ErrValidation) {
		t.Fatalf("원 금액을 넘는 환입: %v", err)
	}
	half := taxInput("2026-11-10", 1)
	half.Lines = []domain.TaxLineInput{{Name: "환입", Supply: -40_000}}
	if _, err := e.tax.Update(as(uFinance), am.ID, half); err != nil {
		t.Fatal(err)
	}
	if got, err := e.tax.Issue(as(uFinance2), am.ID); err != nil || got.Status != domain.TaxIssued {
		t.Fatalf("부분 환입 발행: %v %+v", err, got)
	}
	// 접수 전 건·매입 건·수정이 아닌 사유는 수정 발행이 안 된다.
	if _, err := e.tax.AmendDraft(as(uFinance), am.ID, domain.AmendReturn); !errors.Is(err, contract.ErrInvalidState) {
		t.Fatalf("접수 전 건 수정 발행: %v", err)
	}
	if _, err := e.tax.AmendDraft(as(uFinance), orig.ID, "bogus"); !errors.Is(err, contract.ErrValidation) {
		t.Fatalf("알 수 없는 사유: %v", err)
	}
	// 원 건 화면에 정정 건 번호가 보인다.
	if got, _ := e.tax.Get(as(uFinance), orig.ID); got.AmendedByNo != am.No {
		t.Fatalf("정정한 건 번호: %q", got.AmendedByNo)
	}
	// 음수 공급가액은 수정 사유가 있을 때만 DB 도 허용한다.
	if _, err := e.db.Exec(`UPDATE tax_invoices SET amend_reason = '', supply_total = -5, total = -5 WHERE id = ?`, am.ID); err == nil {
		t.Fatal("사유 없는 음수 금액")
	}
}

func TestTaxInvoiceRegisterReceivedRejectsDuplicateApproval(t *testing.T) {
	e := newEnv(t)
	in := domain.TaxReceivedInput{ApprovalNo: "20261108abcdefgh12345678", IssueDate: "2026-11-08", Supplier: domain.TaxParty{BizNo: "220-81-62517", Name: "한빛상사", Rep: "김한빛"}, SupplyTotal: 200_000, TaxTotal: 20_000}
	// 회사 정보가 없으면 거부.
	if _, err := e.tax.RegisterReceived(as(uFinance), in); !errors.Is(err, contract.ErrInvalidState) {
		t.Fatalf("회사 사업자번호 없음: %v", err)
	}
	e.taxCompany(t)
	v, err := e.tax.RegisterReceived(as(uFinance), in)
	if err != nil || v.Status != domain.TaxAccepted || v.Direction != domain.TaxPurchase || v.ApprovalNo != "20261108ABCDEFGH12345678" || v.Buyer.BizNo != "1234567890" {
		t.Fatalf("수기 등록: %v %+v", err, v)
	}
	dup := in
	dup.ApprovalNo = strings.ToLower(in.ApprovalNo)
	if _, err := e.tax.RegisterReceived(as(uFinance), dup); !errors.Is(err, contract.ErrValidation) || !strings.Contains(err.Error(), v.No) {
		t.Fatalf("중복 승인번호: %v", err)
	}
	// DB 유일 색인도 막는다.
	if _, err := e.db.Exec(`INSERT INTO tax_invoices (seq_no, no, direction, tax_type, status, approval_no, issue_date, created_by, created_at, updated_at) VALUES (99,'TI-DUP','purchase','taxable','accepted',?, '2026-11-08',4,'x','x')`, v.ApprovalNo); err == nil {
		t.Fatal("DB 가 중복 승인번호를 허용했다")
	}
	for name, bad := range map[string]func(*domain.TaxReceivedInput){
		"승인번호 길이": func(i *domain.TaxReceivedInput) { i.ApprovalNo = "123" },
		"음수 공급가액": func(i *domain.TaxReceivedInput) { i.ApprovalNo = "20261108ZZZZZZZZ00000001"; i.SupplyTotal = -1 },
		"면세인데 세액": func(i *domain.TaxReceivedInput) {
			i.ApprovalNo = "20261108ZZZZZZZZ00000002"
			i.TaxType = domain.TaxExempt
		},
		"미래 작성일": func(i *domain.TaxReceivedInput) {
			i.ApprovalNo = "20261108ZZZZZZZZ00000003"
			i.IssueDate = "2026-12-31"
		},
		"공급자 번호": func(i *domain.TaxReceivedInput) { i.ApprovalNo = "20261108ZZZZZZZZ00000004"; i.Supplier.BizNo = "12" },
	} {
		i := in
		bad(&i)
		if _, err := e.tax.RegisterReceived(as(uFinance), i); !errors.Is(err, contract.ErrValidation) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// 세액이 계산값과 다르면 경고만 한다.
	warn := in
	warn.ApprovalNo, warn.TaxTotal = "20261108ZZZZZZZZ00000005", 19_999
	if w, err := e.tax.RegisterReceived(as(uFinance), warn); err != nil || len(w.Warnings) == 0 {
		t.Fatalf("세액 차이는 경고: %v %+v", err, w)
	}
	// 수기 등록 건은 취소할 수 있고, 취소 뒤에도 승인번호는 계속 점유된다(재등록 불가).
	if got, err := e.tax.CancelReceived(as(uFinance), v.ID, "잘못 등록"); err != nil || got.Status != domain.TaxCanceled {
		t.Fatalf("등록 취소: %v %+v", err, got)
	}
	if _, err := e.tax.RegisterReceived(as(uFinance), in); !errors.Is(err, contract.ErrValidation) {
		t.Fatalf("취소한 건의 승인번호 재등록: %v", err)
	}
	if _, err := e.tax.Cancel(as(uFinance), v.ID, "x"); err == nil {
		t.Fatal("매입 건을 매출 취소 경로로 취소할 수 없다")
	}
}

func TestTaxInvoiceListFiltersAndMasking(t *testing.T) {
	e := newEnv(t)
	e.taxCompany(t)
	a := e.taxAccepted(t, 10_000, "20261110LISTAAAAAAAA0001")
	e.tax.RegisterReceived(as(uFinance), domain.TaxReceivedInput{ApprovalNo: "20261105LISTBBBBBBBB0002", IssueDate: "2026-11-05", Supplier: domain.TaxParty{BizNo: "1068123456", Name: "대한물산", Rep: "박"}, SupplyTotal: 5000, TaxTotal: 500})
	page, err := e.tax.List(as(uAuditor), domain.TaxInvoiceFilter{})
	if err != nil || page.Total != 2 || len(page.Rows) != 2 || page.Rows[0].IssueDate < page.Rows[1].IssueDate {
		t.Fatalf("목록: %v %+v", err, page)
	}
	for _, f := range []struct {
		f    domain.TaxInvoiceFilter
		want int
	}{
		{domain.TaxInvoiceFilter{Direction: "sale"}, 1}, {domain.TaxInvoiceFilter{Direction: "purchase"}, 1}, {domain.TaxInvoiceFilter{Status: "accepted"}, 2},
		{domain.TaxInvoiceFilter{Counterparty: "한빛"}, 1}, {domain.TaxInvoiceFilter{Counterparty: "대한"}, 1}, {domain.TaxInvoiceFilter{ApprovalNo: strings.ToLower(a.ApprovalNo)}, 1},
		{domain.TaxInvoiceFilter{From: "2026-11-08"}, 1}, {domain.TaxInvoiceFilter{To: "2026-11-06"}, 1}, {domain.TaxInvoiceFilter{Counterparty: "%"}, 0},
	} {
		if got, err := e.tax.List(as(uFinance), f.f); err != nil || got.Total != f.want {
			t.Errorf("%+v → %d(%v), 기대 %d", f.f, got.Total, err, f.want)
		}
	}
	if _, err := e.tax.List(as(uFinance), domain.TaxInvoiceFilter{From: "bad"}); !errors.Is(err, contract.ErrValidation) {
		t.Fatalf("기간 형식: %v", err)
	}
	// MCP 채널(3단계에서 쓴다)로 부르면 사업자번호도 가려서 나간다. 이메일은 모든 채널에서 가려진다.
	mcp := contract.WithPrincipal(context.Background(), func() contract.Principal {
		p := users[uFinance]
		p.Channel = contract.ChannelMCP
		return p
	}())
	got, err := e.tax.Get(mcp, a.ID)
	if err != nil || got.Buyer.BizNo == "2208162517" || got.Supplier.BizNo == "1234567890" {
		t.Fatalf("MCP 채널의 사업자번호는 가려진다: %v %+v", err, got.Buyer)
	}
}
