package service_test

import (
	"strings"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/service"
)

// jvForm 은 전표 승인 요청서 양식의 재무팀 조직 코드(T-FIN)를 시험 조직(FIN)에 맞춘 판을 게시하고 재무팀장을 재무 담당2(uFinance2)로 정한다.
func (e *env) jvForm(t *testing.T) {
	t.Helper()
	f := e.forms()
	v, err := f.CreateVersionDraft(as(uAdmin), "JV")
	if err != nil {
		t.Fatal(err)
	}
	def := v.Definition
	for i := range def.Line.Steps {
		if def.Line.Steps[i].UnitCode == "T-FIN" {
			def.Line.Steps[i].UnitCode = "FIN"
		}
		if def.Line.Steps[i].UnitCode == "DIV-MG" {
			def.Line.Steps[i].UnitCode = "PL" // 시험 조직에서는 회사 단위의 장(대표)이 재무본부장 자리다
		}
	}
	def.Recipients.CC = nil
	if _, err := f.SaveVersionDraft(as(uAdmin), v.ID, domain.FormVersionInput{Definition: def}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Publish(as(uAdmin), v.ID); err != nil {
		t.Fatal(err)
	}
	e.org.SetHead(unitFin, uFinance2)
}

func (e *env) purchaseJV(t *testing.T, vendorID, amount int64) domain.Journal {
	t.Helper()
	j, err := e.journals.Create(as(uFinance), domain.JournalInput{PostingDate: "2026-11-10", TradeDate: "2026-11-05", Memo: "매입 시험", Lines: []domain.JournalLineInput{
		{AccountCode: "1210", Side: "D", Amount: amount}, {AccountCode: "2110", Side: "C", Amount: amount, VendorID: vendorID}}})
	if err != nil {
		t.Fatalf("전표 입력: %v", err)
	}
	return j
}

func (e *env) approveJV(t *testing.T, j domain.Journal) domain.Journal {
	t.Helper()
	j, err := e.journals.RequestApproval(as(uFinance), j.No)
	if err != nil {
		t.Fatalf("승인 요청: %v", err)
	}
	decideAs(t, e, uFinance2, j.DocID, true, "확인")
	got, err := e.journals.Get(as(uFinance), j.No)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// AC-1100·1102: 상태 전이 표 밖의 전이는 모두 거부된다(표 안의 전이만 통과).
func TestJournalTransitionTableOnly(t *testing.T) {
	ok := map[[2]string]string{
		{"draft", "request"}: "requested", {"requested", "approve"}: "approved", {"requested", "reject"}: "draft",
		{"requested", "reset"}: "draft", {"draft", "cancel"}: "canceled", {"requested", "cancel"}: "canceled",
	}
	states := []string{"draft", "requested", "approved", "canceled"}
	events := []string{"request", "approve", "reject", "reset", "cancel"}
	for _, s := range states {
		for _, ev := range events {
			to, err := service.JournalTransitionForTest(s, ev)
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

// AC-1102·1103: 입력 검증 문구, 번호는 연도별 순번, 임시 저장은 차대가 달라도 되지만 승인 요청은 일치해야 한다(문구에 금액).
func TestJournalInputValidationAndBalance(t *testing.T) {
	e := newEnv(t)
	v := e.vendor(t)
	base := domain.JournalInput{PostingDate: "2026-11-10", Memo: "시험", Lines: []domain.JournalLineInput{{AccountCode: "1210", Side: "D", Amount: 1000}, {AccountCode: "2110", Side: "C", Amount: 1000, VendorID: v.ID}}}
	cases := []struct {
		name string
		mut  func(*domain.JournalInput)
		want string
	}{
		{"줄 부족", func(in *domain.JournalInput) { in.Lines = in.Lines[:1] }, "줄은 2개 이상"},
		{"적요 없음", func(in *domain.JournalInput) { in.Memo = " " }, "적요는 1~200자"},
		{"거래처 없음", func(in *domain.JournalInput) { in.Lines[1].VendorID = 0 }, "외상매입금 줄은 거래처를 골라야 합니다."},
		{"금액 0", func(in *domain.JournalInput) { in.Lines[0].Amount = 0 }, "금액은 1원 이상"},
		{"없는 계정", func(in *domain.JournalInput) { in.Lines[0].AccountCode = "9999" }, "알 수 없는 계정과목"},
		{"날짜 형식", func(in *domain.JournalInput) { in.PostingDate = "내일" }, "전기일은 YYYY-MM-DD"},
		{"자동 종류 직접 입력", func(in *domain.JournalInput) { in.Kind = "auto_purchase" }, "일반 전표와 기초 전표만"},
	}
	for _, c := range cases {
		in := base
		in.Lines = append([]domain.JournalLineInput(nil), base.Lines...)
		c.mut(&in)
		if _, err := e.journals.Create(as(uFinance), in); !isErr(err, contract.ErrValidation) || !strings.Contains(err.Error(), c.want) {
			t.Fatalf("%s: %v", c.name, err)
		}
	}
	if _, err := e.accounts.SetActive(as(uFinance), "5250", false); err != nil {
		t.Fatal(err)
	}
	in := base
	in.Lines = []domain.JournalLineInput{{AccountCode: "5250", Side: "D", Amount: 1000}, {AccountCode: "1010", Side: "C", Amount: 1000}}
	if _, err := e.journals.Create(as(uFinance), in); !isErr(err, contract.ErrValidation) || !strings.Contains(err.Error(), "중지된 계정과목입니다(5250 접대비).") {
		t.Fatalf("중지 계정: %v", err)
	}
	// 차대가 다른 임시 저장은 된다. 번호는 순번이고 승인 요청은 거부된다.
	bad := base
	bad.Lines = []domain.JournalLineInput{{AccountCode: "1210", Side: "D", Amount: 1_100_000}, {AccountCode: "2110", Side: "C", Amount: 1_000_000, VendorID: v.ID}}
	j1, err := e.journals.Create(as(uFinance), bad)
	if err != nil || j1.No != "JV-2026-00001" || j1.Status != "draft" {
		t.Fatalf("임시 저장: %+v %v", j1, err)
	}
	if _, err := e.journals.RequestApproval(as(uFinance), j1.No); !isErr(err, contract.ErrValidation) || !strings.Contains(err.Error(), "차변 합계와 대변 합계가 같지 않습니다(차변 1,100,000원, 대변 1,000,000원).") {
		t.Fatalf("차대 불일치 요청: %v", err)
	}
	// DB 트리거가 서버 검사를 우회한 상태 변경도 막는다.
	if _, err := e.db.Exec(`UPDATE journals SET status = 'requested' WHERE no = ?`, j1.No); err == nil || !strings.Contains(err.Error(), "차변 합계와 대변 합계가 같지 않습니다") {
		t.Fatalf("트리거: %v", err)
	}
	j2, _ := e.journals.Create(as(uFinance), base)
	if j2.No != "JV-2026-00002" {
		t.Fatalf("번호 %s", j2.No)
	}
}

// AC-1104·1105·1120: 승인 요청이 JV 문서를 만들고(일반 기안 불가) 재무팀장 승인으로 전표가 승인되며 같은 트랜잭션에서 채무 항목이 생기고(만기 = 거래일 + 30일),
// 승인된 전표는 서버·트리거 어디서도 고칠 수 없다.
func TestJournalApprovalCreatesPayablesAndIsImmutable(t *testing.T) {
	e := newEnv(t)
	e.jvForm(t)
	v := e.vendor(t)
	j := e.purchaseJV(t, v.ID, 110_000)
	if _, err := e.docs().CreateDraft(as(uFinance), domain.DocDraftInput{FormCode: "JV", Title: "수동", Fields: map[string]any{"journal_no": j.No}}); !isErr(err, contract.ErrValidation) || !strings.Contains(err.Error(), "전표 화면에서 승인 요청") {
		t.Fatalf("일반 작성: %v", err)
	}
	if _, err := e.journals.Create(as(uStaff), domain.JournalInput{}); !isErr(err, contract.ErrForbidden) {
		t.Fatalf("직원: %v", err)
	}
	got := e.approveJV(t, j)
	if got.Status != "approved" || got.ApprovedBy == "" || got.ApprovedAt == nil || got.DocNo == "" {
		t.Fatalf("승인 뒤: %+v", got)
	}
	var amount, paid int64
	var due string
	if err := e.db.QueryRow(`SELECT amount, paid_amount, due_date FROM payables WHERE journal_id = ?`, got.ID).Scan(&amount, &paid, &due); err != nil || amount != 110_000 || paid != 0 || due != "2026-12-05" {
		t.Fatalf("채무 항목: %d %d %s %v", amount, paid, due, err)
	}
	if rec, ok := lastRecord(e, "journal.approve"); !ok || !hasDiff(rec, "payable_count", "1") {
		t.Fatalf("승인 감사: %+v", rec)
	}
	// 수정 경로가 없다: 서비스 거부, 줄·헤더 SQL 거부.
	in := domain.JournalInput{PostingDate: "2026-11-10", Memo: "고침", Lines: []domain.JournalLineInput{{AccountCode: "1210", Side: "D", Amount: 5}, {AccountCode: "2110", Side: "C", Amount: 5, VendorID: v.ID}}}
	if _, err := e.journals.Update(as(uFinance), got.No, in); !isErr(err, contract.ErrInvalidState) {
		t.Fatalf("승인된 전표 수정: %v", err)
	}
	if _, err := e.db.Exec(`UPDATE journals SET memo = 'x' WHERE id = ?`, got.ID); err == nil || !strings.Contains(err.Error(), "승인된 전표는 수정할 수 없습니다") {
		t.Fatalf("헤더 트리거: %v", err)
	}
	if _, err := e.db.Exec(`UPDATE journal_lines SET amount = 1 WHERE journal_id = ?`, got.ID); err == nil {
		t.Fatal("승인된 전표의 줄이 수정됨")
	}
	if _, err := e.db.Exec(`DELETE FROM journals WHERE id = ?`, got.ID); err == nil {
		t.Fatal("전표가 삭제됨")
	}
	if _, err := e.journals.Cancel(as(uFinance), got.No, "취소 시도"); !isErr(err, contract.ErrInvalidState) || !strings.Contains(err.Error(), "역분개하세요") {
		t.Fatalf("승인된 전표 취소: %v", err)
	}
}

// 반려·회수면 전표가 임시로 돌아가 고쳐 다시 요청할 수 있고, 승인 요청 중 취소는 문서를 회수한다.
func TestJournalRejectResetAndCancel(t *testing.T) {
	e := newEnv(t)
	e.jvForm(t)
	v := e.vendor(t)
	j := e.purchaseJV(t, v.ID, 50_000)
	req, err := e.journals.RequestApproval(as(uFinance), j.No)
	if err != nil || req.Status != "requested" || req.DocID == 0 {
		t.Fatalf("요청: %+v %v", req, err)
	}
	if _, err := e.journals.Update(as(uFinance), j.No, domain.JournalInput{PostingDate: "2026-11-10", Memo: "x", Lines: nil}); !isErr(err, contract.ErrInvalidState) {
		t.Fatalf("승인 요청 중 수정: %v", err)
	}
	decideAs(t, e, uFinance2, req.DocID, false, "금액 확인")
	back, _ := e.journals.Get(as(uFinance), j.No)
	if back.Status != "draft" {
		t.Fatalf("반려 뒤 상태 %s", back.Status)
	}
	again, err := e.journals.RequestApproval(as(uFinance), j.No)
	if err != nil || again.Status != "requested" || again.DocID == req.DocID {
		t.Fatalf("재요청: %+v %v", again, err)
	}
	can, err := e.journals.Cancel(as(uFinance), j.No, "중복 입력")
	if err != nil || can.Status != "canceled" || can.CancelReason != "중복 입력" {
		t.Fatalf("취소: %+v %v", can, err)
	}
	if _, err := e.journals.Cancel(as(uFinance), j.No, "또"); !isErr(err, contract.ErrInvalidState) {
		t.Fatalf("취소된 전표 재취소: %v", err)
	}
	if _, err := e.journals.Cancel(as(uFinance), j.No, " "); !isErr(err, contract.ErrValidation) {
		t.Fatalf("사유 없음: %v", err)
	}
}

// AC-1105·1120·E8·E9: 역분개는 한 번만, 차대 반대, 원 전표에 정정됨 표시, 승인되면 채무 항목 취소, 지급·예약이 있으면 거부.
func TestJournalReversal(t *testing.T) {
	e := newEnv(t)
	e.jvForm(t)
	v := e.vendor(t)
	orig := e.approveJV(t, e.purchaseJV(t, v.ID, 90_000))
	if _, err := e.journals.Reverse(as(uFinance), "JV-2026-99999", ""); !isErr(err, contract.ErrNotFound) {
		t.Fatalf("없는 전표: %v", err)
	}
	if _, err := e.db.Exec(`UPDATE payables SET reserved_amount = 10 WHERE journal_id = ?`, orig.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.journals.Reverse(as(uFinance), orig.No, ""); !isErr(err, contract.ErrValidation) || !strings.Contains(err.Error(), "지급이 시작된 채무는 역분개할 수 없습니다.") {
		t.Fatalf("예약 중 역분개: %v", err)
	}
	if _, err := e.db.Exec(`UPDATE payables SET reserved_amount = 0 WHERE journal_id = ?`, orig.ID); err != nil {
		t.Fatal(err)
	}
	rev, err := e.journals.Reverse(as(uFinance), orig.No, "")
	if err != nil || rev.Kind != "reversal" || rev.Status != "draft" || rev.ReversesNo != orig.No || len(rev.Lines) != 2 {
		t.Fatalf("역분개 생성: %+v %v", rev, err)
	}
	for i, l := range rev.Lines {
		if l.Amount != orig.Lines[i].Amount || l.Side == orig.Lines[i].Side {
			t.Fatalf("줄 %d 가 반대가 아님: %+v / %+v", i, l, orig.Lines[i])
		}
	}
	if _, err := e.journals.Reverse(as(uFinance), orig.No, ""); !isErr(err, contract.ErrInvalidState) || !strings.Contains(err.Error(), "이미 역분개된 전표입니다.") {
		t.Fatalf("두 번째 역분개: %v", err)
	}
	if _, err := e.journals.Reverse(as(uFinance), rev.No, ""); !isErr(err, contract.ErrInvalidState) {
		t.Fatalf("역분개의 역분개: %v", err)
	}
	approved := e.approveJV(t, rev)
	if approved.Status != "approved" {
		t.Fatalf("역분개 승인: %+v", approved)
	}
	after, _ := e.journals.Get(as(uFinance), orig.No)
	if !after.Corrected || after.ReversedByNo != rev.No {
		t.Fatalf("정정됨 표시: %+v", after)
	}
	var canceled int
	if err := e.db.QueryRow(`SELECT canceled FROM payables WHERE journal_id = ?`, orig.ID).Scan(&canceled); err != nil || canceled != 1 {
		t.Fatalf("채무 항목 취소: %d %v", canceled, err)
	}
}

// AC-1130·1131: 마감된 달의 전기일은 입력·요청·승인이 거부되고(서버·트리거), 승인 요청 중 전표는 마감 전 점검의 차단 항목이다.
func TestJournalMonthCloseLock(t *testing.T) {
	e := newEnv(t)
	e.closeFixture(t)
	e.jvForm(t)
	v := e.vendor(t)
	// 열린 달(2026-11)에 승인 요청 중인 전표가 있으면 점검이 차단한다.
	j := e.purchaseJV(t, v.ID, 70_000)
	req, err := e.journals.RequestApproval(as(uFinance), j.No)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.monthClose.Close(as(uFinance), "2026-10"); err != nil {
		t.Fatalf("10월 마감: %v", err)
	}
	pc, err := e.monthClose.Precheck(as(uFinance), "2026-11")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, b := range pc.Blockers {
		found = found || strings.Contains(b.Title, "승인 요청 중인 전표")
	}
	if !found || pc.Closable {
		t.Fatalf("승인 요청 중 전표가 차단 항목이어야 함: %+v", pc)
	}
	decideAs(t, e, uFinance2, req.DocID, true, "확인")
	// 마감된 10월 전기일.
	in := domain.JournalInput{PostingDate: "2026-10-20", Memo: "마감월", Lines: []domain.JournalLineInput{{AccountCode: "1210", Side: "D", Amount: 10}, {AccountCode: "2110", Side: "C", Amount: 10, VendorID: v.ID}}}
	if _, err := e.journals.Create(as(uFinance), in); err == nil || err.Error() != closedMsg {
		t.Fatalf("마감월 입력: %v", err)
	}
	if _, err := e.db.Exec(`INSERT INTO journals (no, kind, status, posting_date, trade_date, memo, created_by, created_at) VALUES ('X-1','general','draft','2026-10-20','2026-10-20','x',1,'2026-11-10T00:00:00Z')`); err == nil || !strings.Contains(err.Error(), "마감된 월입니다") {
		t.Fatalf("트리거: %v", err)
	}
	if rec, ok := lastRecord(e, "journal.create"); !ok || rec.Result != "denied" {
		t.Fatalf("마감 월 거부 감사가 denied 로 남아야 함: %+v %v", rec, ok)
	}
}

// AC-1101·1140: 계정과목 규칙·권한. 코드·구분 불변, 시스템 계정 중지 거부, 규칙 계정 구분 검사, 역할별 접근.
func TestAccountsRulesAndPermissions(t *testing.T) {
	e := newEnv(t)
	as1 := e.accounts
	list, err := as1.List(as(uAuditor), true)
	if err != nil || len(list) != 21 {
		t.Fatalf("초기 계정 %d개: %v", len(list), err)
	}
	if _, err := as1.List(as(uStaff), false); !isErr(err, contract.ErrForbidden) {
		t.Fatalf("직원 조회: %v", err)
	}
	if _, err := as1.Create(as(uAuditor), domain.AccountInput{Code: "5300", Name: "시험비"}); !isErr(err, contract.ErrForbidden) {
		t.Fatalf("감사 변경: %v", err)
	}
	if _, err := e.journals.Create(as(uAdmin), domain.JournalInput{}); !isErr(err, contract.ErrForbidden) {
		t.Fatalf("관리자는 전표를 쓰지 않는다: %v", err)
	}
	a, err := as1.Create(as(uAdmin), domain.AccountInput{Code: "5300", Name: "시험비"})
	if err != nil || a.Kind != "expense" {
		t.Fatalf("추가: %+v %v", a, err)
	}
	if _, err := as1.Create(as(uAdmin), domain.AccountInput{Code: "5300", Name: "또"}); !isErr(err, contract.ErrValidation) {
		t.Fatalf("코드 중복: %v", err)
	}
	if _, err := as1.Create(as(uAdmin), domain.AccountInput{Code: "6100", Name: "나쁜코드"}); !isErr(err, contract.ErrValidation) {
		t.Fatalf("코드 형식: %v", err)
	}
	if _, err := as1.SetActive(as(uFinance), "2110", false); !isErr(err, contract.ErrValidation) || !strings.Contains(err.Error(), "시스템 계정은 중지할 수 없습니다") {
		t.Fatalf("시스템 계정 중지: %v", err)
	}
	if _, err := e.db.Exec(`UPDATE accounts SET code = '5301' WHERE code = '5300'`); err == nil {
		t.Fatal("코드가 바뀜")
	}
	if _, err := e.db.Exec(`DELETE FROM accounts WHERE code = '5300'`); err == nil {
		t.Fatal("계정이 삭제됨")
	}
	if r, err := as1.Rename(as(uFinance), "2110", "외상매입금(거래처)"); err != nil || r.Name != "외상매입금(거래처)" {
		t.Fatalf("이름 변경: %+v %v", r, err)
	}
	if err := as1.SetRule(as(uFinance), "purchase.inventory", "2110"); !isErr(err, contract.ErrValidation) {
		t.Fatalf("규칙 구분 불일치: %v", err)
	}
	if err := as1.SetRule(as(uFinance), "purchase.inventory", "1220"); err != nil {
		t.Fatalf("규칙 변경: %v", err)
	}
	if rec, ok := lastRecord(e, "account.rule_set"); !ok || !hasDiff(rec, "account_code", "1220") {
		t.Fatalf("규칙 감사: %+v", rec)
	}
	if err := as1.SetRule(as(uFinance), "nope.key", "1220"); !isErr(err, contract.ErrNotFound) {
		t.Fatalf("없는 규칙: %v", err)
	}
}

// AC-1104(D6): 차변 합계가 1억원 미만이면 재무팀장 한 단계, 1억원 이상이면 재무본부장 단계가 더해진다.
func TestJournalLineAddsDivisionHeadAtOneHundredMillion(t *testing.T) {
	e := newEnv(t)
	e.jvForm(t)
	v := e.vendor(t)
	small := e.purchaseJV(t, v.ID, 99_999_999)
	small, err := e.journals.RequestApproval(as(uFinance), small.No)
	if err != nil {
		t.Fatal(err)
	}
	decideAs(t, e, uFinance2, small.DocID, true, "확인")
	if got, _ := e.journals.Get(as(uFinance), small.No); got.Status != "approved" {
		t.Fatalf("1억원 미만은 한 단계로 승인되어야 함: %s", got.Status)
	}
	big := e.purchaseJV(t, v.ID, 100_000_000)
	big, err = e.journals.RequestApproval(as(uFinance), big.No)
	if err != nil {
		t.Fatal(err)
	}
	decideAs(t, e, uFinance2, big.DocID, true, "확인")
	if got, _ := e.journals.Get(as(uFinance), big.No); got.Status != "requested" {
		t.Fatalf("1억원 이상은 재무본부장 결재가 남아야 함: %s", got.Status)
	}
	decideAs(t, e, uCEO, big.DocID, true, "승인")
	if got, _ := e.journals.Get(as(uFinance), big.No); got.Status != "approved" {
		t.Fatalf("재무본부장 승인 뒤 확정되어야 함: %s", got.Status)
	}
}
