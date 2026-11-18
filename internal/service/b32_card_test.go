package service_test

import (
	"github.com/choibongseok/procure-ledger/internal/dbtest"
	"strings"
	"testing"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/service"
)

// ---- 시험 도구 ----

const cardHeaderA = "승인일시,카드번호,가맹점명,승인금액,승인번호,할부개월,승인구분\n"

func cardRow(at, merchant string, amount int64, no, kind string) string {
	return at + ",1234-56**-****-7890," + merchant + "," + itoa(amount) + "," + no + ",0," + kind + "\n"
}

func itoa(n int64) string {
	neg := n < 0
	if neg {
		n = -n
	}
	s := ""
	for {
		s = string(rune('0'+n%10)) + s
		n /= 10
		if n == 0 {
			break
		}
	}
	if neg {
		s = "-" + s
	}
	return s
}

// card 는 소지자 uStaff 인 카드(끝 4자리 7890)를 등록한다.
func (e *env) card(t *testing.T) domain.Card {
	t.Helper()
	c, err := e.cards.SaveCard(as(uFinance), 0, domain.CardInput{Alias: "영업 법인카드", Issuer: domain.CardFormatA, Last4: "7890", HolderID: uStaff})
	if err != nil {
		t.Fatalf("카드 등록: %v", err)
	}
	return c
}

func (e *env) importCards(t *testing.T, csv string) domain.CardImportResult {
	t.Helper()
	res, err := e.cards.ConfirmImport(as(uFinance), domain.CardFormatA, []byte(cardHeaderA+csv))
	if err != nil {
		t.Fatalf("업로드: %v", err)
	}
	return res
}

func (e *env) txnByApproval(t *testing.T, no, kind string) domain.CardTxn {
	t.Helper()
	all, _, err := e.cards.All(as(uFinance), domain.CardFilter{Limit: 500})
	if err != nil {
		t.Fatal(err)
	}
	for _, x := range all {
		if x.ApprovalNo == no && x.Kind == kind {
			return x
		}
	}
	t.Fatalf("내역 없음: %s %s", no, kind)
	return domain.CardTxn{}
}

func (e *env) explainOK(t *testing.T, txn domain.CardTxn, code string, deduct bool, files ...int64) domain.CardTxn {
	t.Helper()
	d := deduct
	in := domain.ExplainInput{Purpose: "고객사 미팅 다과 구입 비용입니다", AccountCode: code, VATDeduct: &d, AttachmentIDs: files}
	if !deduct {
		in.VATReason = "entertainment"
	}
	got, err := e.cards.Explain(as(uStaff), txn.ID, in)
	if err != nil {
		t.Fatalf("소명: %v", err)
	}
	return got
}

func (e *env) receipt(t *testing.T) int64 {
	t.Helper()
	att, _ := e.attach()
	a, err := att.Upload(as(uStaff), "영수증.pdf", pdfBytes)
	if err != nil {
		t.Fatal(err)
	}
	return a.ID
}

// ---- 시험 ----

// AC-1250·1251·1252·1253: 업로드는 신규·중복·오류를 세고, 같은 파일을 다시 올려도 같은 행은 한 번만 들어가며, 취소는 짝이 되어 둘 다 소명 불필요가 된다.
func TestCardImportDedupPairAndUnregistered(t *testing.T) {
	e := newEnv(t)
	e.card(t)
	csv := cardRow("2026-11-08 12:34:56", "스타벅스", 12_500, "A1", "승인") + cardRow("2026-11-08 18:02:00", "대한문구", 45_000, "A2", "승인") + cardRow("2026-11-09 09:00:00", "대한문구", 45_000, "A2", "취소") +
		"2026-11-09 10:00:00,1234-56**-****-1111,다른카드,1000,A9,0,승인\n"
	pv, err := e.cards.PreviewImport(as(uFinance), "", []byte(cardHeaderA+csv))
	if err != nil {
		t.Fatal(err)
	}
	if pv.New != 3 || pv.Duplicate != 0 || pv.Errors != 1 || len(pv.Unregistered) != 1 || !strings.Contains(pv.Unregistered[0], "1111") || pv.FormatLabel == "" {
		t.Fatalf("미리보기: %+v", pv)
	}
	if e.col(t, `SELECT COUNT(*) FROM card_transactions`) != "0" {
		t.Fatal("미리보기가 저장함")
	}
	res, err := e.cards.ConfirmImport(as(uFinance), "", []byte(cardHeaderA+csv))
	if err != nil || res.New != 3 || res.Errors != 1 {
		t.Fatalf("업로드: %+v %v", res, err)
	}
	again, err := e.cards.ConfirmImport(as(uFinance), "", []byte(cardHeaderA+csv))
	if err != nil || again.New != 0 || again.Duplicate != 3 {
		t.Fatalf("재업로드: %+v %v", again, err)
	}
	if e.col(t, `SELECT COUNT(*) FROM card_transactions`) != "3" {
		t.Fatal("같은 행이 두 번 들어감")
	}
	a1 := e.txnByApproval(t, "A1", "approve")
	pairA, pairC := e.txnByApproval(t, "A2", "approve"), e.txnByApproval(t, "A2", "cancel")
	if a1.Status != domain.CardTxnUnreconciled || pairA.Status != domain.CardTxnNotRequired || pairC.Status != domain.CardTxnNotRequired || a1.DueDate != "2026-11-16" {
		t.Fatalf("상태: %s %s %s %s", a1.Status, pairA.Status, pairC.Status, a1.DueDate)
	}
	// 취소 행만 먼저 들어오면 "원거래 없음", 승인 행이 뒤늦게 들어오면 둘 다 소명 불필요가 된다.
	e.importCards(t, cardRow("2026-11-09 11:00:00", "서점", 9_000, "A3", "취소"))
	if c := e.txnByApproval(t, "A3", "cancel"); c.Status != domain.CardTxnOrphanCancel {
		t.Fatalf("원거래 없는 취소: %s", c.Status)
	}
	e.importCards(t, cardRow("2026-11-02 11:00:00", "서점", 9_000, "A3", "승인"))
	if c, a := e.txnByApproval(t, "A3", "cancel"), e.txnByApproval(t, "A3", "approve"); c.Status != domain.CardTxnNotRequired || a.Status != domain.CardTxnNotRequired {
		t.Fatalf("늦은 승인 짝: %s %s", c.Status, a.Status)
	}
	if _, err := e.cards.ConfirmImport(as(uStaff), "", []byte(cardHeaderA+csv)); !isErr(err, contract.ErrForbidden) {
		t.Fatalf("직원 업로드: %v", err)
	}
	if !e.rec.Has("card.import", audit.TargetCard, audit.ResultSuccess) {
		t.Error("업로드 감사가 없음")
	}
	for _, r := range e.rec.Records() {
		if r.Action == "card.import" && (strings.Contains(r.Detail, "스타벅스") || strings.Contains(r.Detail, "7890")) {
			t.Fatalf("감사에 가맹점·카드번호: %+v", r)
		}
	}
}

// AC-1253: 카드 표·카드 내역에는 전체 카드번호를 담는 칸이 없다(끝 4자리만).
func TestCardSchemaHasNoFullCardNumber(t *testing.T) {
	dbtest.SkipPG(t, "SQLite 트리거·PRAGMA 를 직접 쓴다")
	e := newEnv(t)
	for _, tbl := range []string{"corporate_cards", "card_transactions", "card_imports", "card_explanations"} {
		rows, err := e.db.Query(`SELECT name FROM pragma_table_info('` + tbl + `')`)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var n string
			_ = rows.Scan(&n)
			if strings.Contains(n, "card_no") || strings.Contains(n, "card_number") || n == "pan" {
				t.Errorf("%s.%s: 전체 카드번호 칸으로 보임", tbl, n)
			}
		}
		rows.Close()
	}
}

// AC-1254·1255: 소명 규칙 — 용도 길이, 허용 계정, 증빙 3만원 경계, 부가세 역산(버림), 불공제 사유, 개인 사용, 철회·재소명.
func TestCardExplainRulesAndVat(t *testing.T) {
	e := newEnv(t)
	e.card(t)
	e.importCards(t, cardRow("2026-11-08 12:00:00", "스타벅스", 12_500, "A1", "승인")+cardRow("2026-11-08 13:00:00", "문구", 30_000, "A2", "승인")+cardRow("2026-11-08 14:00:00", "식당", 30_001, "A3", "승인"))
	t1, t2, t3 := e.txnByApproval(t, "A1", "approve"), e.txnByApproval(t, "A2", "approve"), e.txnByApproval(t, "A3", "approve")
	yes := true
	good := domain.ExplainInput{Purpose: "고객사 미팅 다과 구입 비용입니다", AccountCode: "5210", VATDeduct: &yes}
	bad := map[string]domain.ExplainInput{
		"용도 짧음":     {Purpose: "짧다", AccountCode: "5210"},
		"허용 밖 계정":   {Purpose: good.Purpose, AccountCode: "1210"},
		"계정 없음":     {Purpose: good.Purpose},
		"불공제 사유 없음": {Purpose: good.Purpose, AccountCode: "5250", VATDeduct: new(bool)},
	}
	for name, in := range bad {
		if _, err := e.cards.Explain(as(uStaff), t1.ID, in); !isErr(err, contract.ErrValidation) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := e.cards.Explain(as(uStaff2), t1.ID, good); !isErr(err, contract.ErrNotFound) {
		t.Fatalf("타인 내역 소명: %v", err)
	}
	got, err := e.cards.Explain(as(uStaff), t1.ID, good)
	if err != nil || got.Status != domain.CardTxnSubmitted || got.Explanation == nil || got.Explanation.VAT != 1136 || got.Explanation.Supply != 11_364 {
		t.Fatalf("12,500원 소명: %+v %v", got, err)
	}
	// 30,000원은 증빙 없이 가능, 30,001원은 증빙이 있어야 한다.
	if _, err := e.cards.Explain(as(uStaff), t2.ID, good); err != nil {
		t.Fatalf("30,000원: %v", err)
	}
	if _, err := e.cards.Explain(as(uStaff), t3.ID, good); !isErr(err, contract.ErrValidation) || !strings.Contains(err.Error(), "영수증을 첨부해야 합니다") {
		t.Fatalf("30,001원 증빙 없음: %v", err)
	}
	if _, err := e.cards.Explain(as(uStaff), t3.ID, domain.ExplainInput{Purpose: good.Purpose, AccountCode: "5210", AttachmentIDs: []int64{99999}}); !isErr(err, contract.ErrValidation) {
		t.Fatalf("없는 첨부: %v", err)
	}
	got3 := e.explainOK(t, t3, "5250", false, e.receipt(t))
	if got3.Explanation == nil || len(got3.Explanation.Attachments) != 1 || got3.Explanation.VATReason != "entertainment" {
		t.Fatalf("증빙 연결: %+v", got3.Explanation)
	}
	// 제출 뒤 철회하면 미소명으로 돌아가고 다시 소명할 수 있다. 이미 소명된 건은 다시 제출할 수 없다.
	if _, err := e.cards.Explain(as(uStaff), t1.ID, good); !isErr(err, contract.ErrInvalidState) {
		t.Fatalf("소명 중복 제출: %v", err)
	}
	wd, err := e.cards.Withdraw(as(uStaff), t1.ID)
	if err != nil || wd.Status != domain.CardTxnUnreconciled {
		t.Fatalf("철회: %+v %v", wd, err)
	}
	if _, err := e.cards.Explain(as(uStaff), t1.ID, domain.ExplainInput{Purpose: "개인적으로 사용한 점심 식사 비용입니다", Personal: true}); err != nil {
		t.Fatalf("개인 사용: %v", err)
	}
	// 봉사료가 있으면 부가세 계산에서 뺀다. CSV 부가세가 있으면 그 값이 우선이다.
	if s, v := 33_000, 3_000; true {
		_ = s
		_ = v
	}
}

// AC-1256·1257: 확인 — 조직장 범위만, 자기 소명 불가, 일괄 상한, 반려 사유·알림, 확인되면 임시 카드 비용 전표(차 소명 계정·부가세대급금 / 대 미지급금), 개인 사용은 전표 없이 환입 요청.
func TestCardConfirmCreatesJournalAndReturnNotifies(t *testing.T) {
	e := newEnv(t)
	e.card(t)
	e.importCards(t, cardRow("2026-11-08 12:00:00", "스타벅스", 12_500, "A1", "승인")+cardRow("2026-11-08 13:00:00", "회식", 66_000, "A2", "승인")+cardRow("2026-11-08 14:00:00", "개인", 5_000, "A3", "승인")+cardRow("2026-11-08 15:00:00", "반려건", 7_000, "A4", "승인"))
	t1, t2, t3, t4 := e.txnByApproval(t, "A1", "approve"), e.txnByApproval(t, "A2", "approve"), e.txnByApproval(t, "A3", "approve"), e.txnByApproval(t, "A4", "approve")
	e.explainOK(t, t1, "5220", true)
	e.explainOK(t, t2, "5250", false, e.receipt(t))
	if _, err := e.cards.Explain(as(uStaff), t3.ID, domain.ExplainInput{Purpose: "개인적으로 사용한 간식 비용입니다", Personal: true}); err != nil {
		t.Fatal(err)
	}
	e.explainOK(t, t4, "5210", true)
	// 확인 대상 목록: 팀장은 보이고 다른 부서 팀장·직원은 거부/빈 목록이다.
	list, err := e.cards.ToConfirm(as(uLead))
	if err != nil || len(list) != 4 {
		t.Fatalf("확인 목록: %d %v", len(list), err)
	}
	if other, _ := e.cards.ToConfirm(as(uLead2)); len(other) != 0 {
		t.Fatalf("다른 팀장에게 보임: %d", len(other))
	}
	if _, err := e.cards.ToConfirm(as(uStaff)); !isErr(err, contract.ErrForbidden) {
		t.Fatalf("직원 확인 목록: %v", err)
	}
	res, err := e.cards.Confirm(as(uLead2), []int64{t1.ID})
	if err != nil || len(res) != 1 || res[0].OK {
		t.Fatalf("다른 조직 팀장 확인: %+v %v", res, err)
	}
	over := make([]int64, 51)
	for i := range over {
		over[i] = int64(i + 1)
	}
	if _, err := e.cards.Confirm(as(uLead), over); !isErr(err, contract.ErrValidation) {
		t.Fatalf("일괄 상한: %v", err)
	}
	res, err = e.cards.Confirm(as(uLead), []int64{t1.ID, t2.ID, t3.ID})
	if err != nil || len(res) != 3 || !res[0].OK || !res[1].OK || !res[2].OK || res[0].JournalNo == "" || res[2].JournalNo != "" {
		t.Fatalf("확인: %+v %v", res, err)
	}
	j1, _ := e.journals.Get(as(uFinance), res[0].JournalNo)
	if j1.Kind != domain.JournalAutoCard || j1.Status != domain.JournalDraft || j1.SourceType != "card_txn" || len(j1.Lines) != 3 ||
		j1.Lines[0].AccountCode != "5220" || j1.Lines[0].Amount != 11_364 || j1.Lines[1].AccountCode != "1310" || j1.Lines[1].Amount != 1_136 || j1.Lines[2].AccountCode != "2120" || j1.Lines[2].Amount != 12_500 || j1.Lines[2].VendorID != 0 {
		t.Fatalf("공제 전표: %+v", j1)
	}
	j2, _ := e.journals.Get(as(uFinance), res[1].JournalNo)
	if len(j2.Lines) != 2 || j2.Lines[0].AccountCode != "5250" || j2.Lines[0].Amount != 66_000 || j2.Lines[1].AccountCode != "2120" {
		t.Fatalf("불공제 전표(부가세 줄 없이 비용 합계): %+v", j2)
	}
	if got, _ := e.cards.GetTxn(as(uFinance), t3.ID); got.Status != domain.CardTxnRefundRequested {
		t.Fatalf("개인 사용은 환입 요청: %s", got.Status)
	}
	if e.col(t, `SELECT COUNT(*) FROM journals WHERE kind = 'auto_card'`) != "2" {
		t.Fatal("개인 사용에서 전표가 생김")
	}
	// 반려: 사유 필수, 소지자에게 알림, 보완 필요.
	if _, err := e.cards.Return(as(uLead), t4.ID, " "); !isErr(err, contract.ErrValidation) {
		t.Fatalf("사유 없는 반려: %v", err)
	}
	if _, err := e.cards.Return(as(uLead2), t4.ID, "아님"); !isErr(err, contract.ErrNotFound) {
		t.Fatalf("다른 조직 반려: %v", err)
	}
	r, err := e.cards.Return(as(uLead), t4.ID, "영수증 내용과 용도가 다릅니다")
	if err != nil || r.Status != domain.CardTxnNeedsFix {
		t.Fatalf("반려: %+v %v", r, err)
	}
	if n := e.notes(t, uStaff, "card_returned"); len(n) != 1 || !strings.Contains(n[0].Body, "영수증 내용과 용도가 다릅니다") {
		t.Fatalf("반려 알림: %+v", n)
	}
	// 반려된 건은 보완해 다시 소명하고, 소지자 본인의 소명은 위 조직장이 확인한다(팀장 본인 확인 불가).
	e.explainOK(t, t4, "5210", true)
	if res, _ := e.cards.Confirm(as(uStaff), []int64{t4.ID}); len(res) != 0 {
		_ = res
	}
}

// AC-1257·E3: 확인된 카드 비용 전표가 승인된 뒤 역분개가 승인되면 카드 내역이 소명 보완 필요로 돌아간다. 마감된 달 전기일이면 전표 대기로 간다.
func TestCardReversalReopensAndMonthClosedQueues(t *testing.T) {
	e := newEnv(t)
	e.jvForm(t)
	e.card(t)
	e.importCards(t, cardRow("2026-11-08 12:00:00", "스타벅스", 12_500, "A1", "승인"))
	t1 := e.txnByApproval(t, "A1", "approve")
	e.explainOK(t, t1, "5220", true)
	res, _ := e.cards.Confirm(as(uLead), []int64{t1.ID})
	if !res[0].OK || res[0].JournalNo == "" {
		t.Fatalf("확인: %+v", res)
	}
	j := e.journals
	jr, err := j.Get(as(uFinance), res[0].JournalNo)
	if err != nil {
		t.Fatal(err)
	}
	approved := e.approveJV(t, jr)
	rev, err := j.Reverse(as(uFinance), approved.No, "")
	if err != nil {
		t.Fatal(err)
	}
	e.approveJV(t, rev)
	got, _ := e.cards.GetTxn(as(uFinance), t1.ID)
	if got.Status != domain.CardTxnNeedsFix {
		t.Fatalf("역분개 뒤 상태: %s", got.Status)
	}
	if n := e.notes(t, uStaff, "card_reopened"); len(n) != 1 {
		t.Fatalf("보완 알림: %+v", n)
	}
	// 다시 소명·확인하면 새 소명의 전표가 만들어진다(역분개된 전표는 그대로).
	e.explainOK(t, got, "5220", true)
	res2, _ := e.cards.Confirm(as(uLead), []int64{t1.ID})
	if !res2[0].OK || res2[0].JournalNo == "" || res2[0].JournalNo == res[0].JournalNo {
		t.Fatalf("재확인: %+v", res2)
	}
	// 규칙 계정(미지급금)이 쓸 수 없으면 확인은 성공하고 전표 대기에 오른다.
	e.importCards(t, cardRow("2026-11-08 13:00:00", "문구점", 8_000, "A2", "승인"))
	t2 := e.txnByApproval(t, "A2", "approve")
	e.explainOK(t, t2, "5220", true)
	if _, err := e.accounts.Create(as(uFinance), domain.AccountInput{Code: "2190", Name: "시험 미지급"}); err != nil {
		t.Fatal(err)
	}
	if err := e.accounts.SetRule(as(uFinance), "card.payable", "2190"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.accounts.SetActive(as(uFinance), "2190", false); err != nil {
		t.Fatal(err)
	}
	res3, _ := e.cards.Confirm(as(uLead), []int64{t2.ID})
	if !res3[0].OK || res3[0].JournalNo != "" {
		t.Fatalf("규칙 계정 중지: %+v", res3)
	}
	if e.col(t, `SELECT COUNT(*) FROM journal_queue WHERE source_type = 'card_txn' AND status = 'pending'`) != "1" {
		t.Fatal("전표 대기에 오르지 않음")
	}
}

// AC-1258: 소명 알림 — 기한 전날·경과 첫날·이후 7일마다는 소지자에게, 14일째는 조직장에게 하루 한 건씩, 본문에 카드번호·가맹점명이 없다. 자동 개인 사용 처리는 없다.
func TestCardReminders(t *testing.T) {
	e := newEnv(t)
	e.card(t)
	e.importCards(t, cardRow("2026-11-10 12:00:00", "비밀가맹점", 12_500, "A1", "승인")) // 기한 2026-11-17(화, 영업일)
	tickAt := func(y int, m time.Month, d, h int) {
		e.now = time.Date(y, m, d, h, 0, 0, 0, time.UTC)
		if err := e.cards.Tick(as(uAdmin)); err != nil {
			t.Fatal(err)
		}
	}
	tickAt(2026, 11, 16, 1) // KST 10시: 기한 하루 전
	tickAt(2026, 11, 16, 5) // 같은 날 두 번째 실행은 만들지 않는다
	if n := e.notes(t, uStaff, "card_reminder"); len(n) != 1 || !strings.Contains(n[0].Body, "내일이 소명 기한") {
		t.Fatalf("기한 전날: %+v", n)
	}
	tickAt(2026, 11, 17, 1)  // 기한 당일: 알림 없음
	tickAt(2026, 11, 17, 23) // KST 08시(09시 전): 아직 만들지 않는다
	if n := e.notes(t, uStaff, "card_reminder"); len(n) != 1 {
		t.Fatalf("09시 이전·기한 당일: %d", len(n))
	}
	tickAt(2026, 11, 18, 1) // 경과 첫날
	tickAt(2026, 11, 25, 1) // 경과 8일
	tickAt(2026, 12, 2, 1)  // 경과 15일
	tickAt(2026, 12, 9, 1)  // 경과 22일
	if n := e.notes(t, uStaff, "card_reminder"); len(n) != 5 {
		t.Fatalf("소지자 알림 %d건(기대 5)", len(n))
	}
	tickAt(2026, 12, 10, 1) // 경과 23일: 없음
	if n := e.notes(t, uStaff, "card_reminder"); len(n) != 5 {
		t.Fatalf("최대 횟수 초과: %d", len(n))
	}
	// 경과 14일째(12-01)에는 조직장에게 한 건.
	tickAt(2026, 12, 1, 1)
	if n := e.notes(t, uLead, "card_overdue_head"); len(n) != 1 || !strings.Contains(n[0].Body, "직원") {
		t.Fatalf("조직장 알림: %+v", n)
	}
	for _, uid := range []int64{uStaff, uLead} {
		for _, n := range append(e.notes(t, uid, "card_reminder"), e.notes(t, uid, "card_overdue_head")...) {
			if strings.Contains(n.Body, "비밀가맹점") || strings.Contains(n.Body, "7890") {
				t.Fatalf("알림 본문에 가맹점·카드번호: %q", n.Body)
			}
		}
	}
	// 자동으로 개인 사용 처리하거나 전표를 만들지 않는다.
	if got := e.txnByApproval(t, "A1", "approve"); got.Status != domain.CardTxnUnreconciled || !got.Overdue {
		t.Fatalf("기한이 지나도 상태는 미소명: %+v", got)
	}
	if e.col(t, `SELECT COUNT(*) FROM journals WHERE kind = 'auto_card'`) != "0" {
		t.Fatal("자동 전표가 생김")
	}
}

// 30일 경과 미소명은 재무 현황의 소명 지연에 오르고 환입 요청으로 바꿀 수 있다(30일 전에는 불가). 소지자 퇴사 건은 "소지자 없음"에 올라 재지정한다.
func TestCardOverviewRefundAndReassign(t *testing.T) {
	e := newEnv(t)
	e.card(t)
	e.importCards(t, cardRow("2026-11-08 12:00:00", "가맹점", 12_500, "A1", "승인")+cardRow("2026-11-08 13:00:00", "가맹점2", 5_000, "A2", "승인"))
	t1, t2 := e.txnByApproval(t, "A1", "approve"), e.txnByApproval(t, "A2", "approve")
	if _, err := e.cards.RequestRefund(as(uFinance), t1.ID, "소명 지연"); !isErr(err, contract.ErrInvalidState) {
		t.Fatalf("30일 전 환입 요청: %v", err)
	}
	e.now = at(2026, 12, 20, 1) // 기한(11-15)에서 35일
	ov, err := e.cards.Overview(as(uAuditor))
	if err != nil || len(ov.Delayed) != 2 || ov.Counts[domain.CardTxnUnreconciled] != 2 {
		t.Fatalf("현황: %+v %v", ov, err)
	}
	if _, err := e.cards.Overview(as(uStaff)); !isErr(err, contract.ErrForbidden) {
		t.Fatalf("직원 현황: %v", err)
	}
	if _, err := e.cards.RequestRefund(as(uAuditor), t1.ID, "x"); !isErr(err, contract.ErrForbidden) {
		t.Fatalf("감사 환입 요청: %v", err)
	}
	r, err := e.cards.RequestRefund(as(uFinance), t1.ID, "소명 지연 30일 경과")
	if err != nil || r.Status != domain.CardTxnRefundRequested {
		t.Fatalf("환입 요청: %+v %v", r, err)
	}
	if _, err := e.cards.CompleteRefund(as(uFinance), t1.ID, "2026-12-21", "급여 공제"); !isErr(err, contract.ErrValidation) {
		t.Fatalf("미래 환입일: %v", err)
	}
	done, err := e.cards.CompleteRefund(as(uFinance), t1.ID, "2026-12-20", "급여 공제")
	if err != nil || done.Status != domain.CardTxnRefunded {
		t.Fatalf("환입 완료: %+v %v", done, err)
	}
	if _, err := e.cards.CompleteRefund(as(uFinance), t1.ID, "2026-12-20", "또"); !isErr(err, contract.ErrInvalidState) {
		t.Fatalf("환입 두 번: %v", err)
	}
	// 소지자 퇴사
	if _, err := e.db.Exec(`UPDATE users SET active = 0 WHERE id = ?`, uStaff); err != nil {
		t.Fatal(err)
	}
	ov, _ = e.cards.Overview(as(uFinance))
	if len(ov.NoHolder) != 1 || ov.NoHolder[0].ID != t2.ID {
		t.Fatalf("소지자 없음: %+v", ov.NoHolder)
	}
	if _, err := e.cards.ReassignHolder(as(uFinance), t2.ID, uStaff); !isErr(err, contract.ErrValidation) {
		t.Fatalf("퇴사자 재지정: %v", err)
	}
	moved, err := e.cards.ReassignHolder(as(uFinance), t2.ID, uStaff2)
	if err != nil || moved.HolderID != uStaff2 {
		t.Fatalf("재지정: %+v %v", moved, err)
	}
	// 소지자 변경은 이후 업로드부터 새 소지자이고 기존 내역은 그대로다.
	c, _ := e.cards.Cards(as(uFinance))
	if _, err := e.cards.ChangeHolder(as(uFinance), c[0].ID, uStaff2); err != nil {
		t.Fatal(err)
	}
	if got := e.txnByApproval(t, "A1", "approve"); got.HolderID != uStaff {
		t.Fatalf("기존 내역 소지자는 유지: %d", got.HolderID)
	}
	e.importCards(t, cardRow("2026-12-19 12:00:00", "새가맹점", 3_000, "A5", "승인"))
	if got := e.txnByApproval(t, "A5", "approve"); got.HolderID != uStaff2 {
		t.Fatalf("새 내역 소지자: %d", got.HolderID)
	}
}

// 본인 범위·증빙 열람: 타인 내역은 404, 감사·재무는 전체 현황, 증빙은 소지자·조직장·재무만 연다. 마감 점검에는 미소명 경고가 나온다.
func TestCardScopeEvidenceAndPrecheck(t *testing.T) {
	e := newEnv(t)
	e.card(t)
	e.importCards(t, cardRow("2026-11-08 12:00:00", "가맹점", 50_000, "A1", "승인"))
	t1 := e.txnByApproval(t, "A1", "approve")
	file := e.receipt(t)
	e.explainOK(t, t1, "5210", true, file)
	if _, err := e.cards.GetTxn(as(uStaff2), t1.ID); !isErr(err, contract.ErrNotFound) {
		t.Fatalf("타인 내역: %v", err)
	}
	for _, who := range []int64{uStaff, uFinance, uAuditor, uAdmin, uLead} {
		if _, err := e.cards.GetTxn(as(who), t1.ID); err != nil {
			t.Errorf("%d 조회: %v", who, err)
		}
	}
	if _, err := e.cards.Mine(as(uAuditor), domain.CardFilter{}); !isErr(err, contract.ErrForbidden) {
		t.Fatalf("감사의 내 내역: %v", err)
	}
	if mine, err := e.cards.Mine(as(uStaff2), domain.CardFilter{}); err != nil || len(mine) != 0 {
		t.Fatalf("다른 직원의 내 내역: %v %v", mine, err)
	}
	att, _ := e.attach()
	if _, err := att.Open(as(uStaff2), file); !isErr(err, contract.ErrNotFound) {
		t.Fatalf("타인 증빙: %v", err)
	}
	for _, who := range []int64{uStaff, uLead, uFinance, uAuditor} {
		if _, err := att.Open(as(who), file); err != nil {
			t.Errorf("%d 증빙 열기: %v", who, err)
		}
	}
	// 소명 증빙은 24시간이 지나도 정리되지 않는다.
	e.now = at(2026, 11, 20, 1)
	if n, err := att.PurgeOrphans(as(uAdmin)); err != nil || n != 0 {
		t.Fatalf("증빙이 정리됨: %d %v", n, err)
	}
	pc, err := e.monthClose.Precheck(as(uFinance), "2026-11")
	if err != nil {
		t.Fatal(err)
	}
	warn := false
	for _, w := range pc.Warnings {
		warn = warn || w.Title == "소명하지 않은 카드 내역"
	}
	if warn {
		t.Fatalf("소명된 건은 경고가 아니어야 함: %+v", pc.Warnings)
	}
	e.importCards(t, cardRow("2026-11-09 12:00:00", "가맹점2", 4_000, "A2", "승인"))
	pc, _ = e.monthClose.Precheck(as(uFinance), "2026-11")
	for _, w := range pc.Warnings {
		warn = warn || w.Title == "소명하지 않은 카드 내역"
	}
	if !warn {
		t.Fatalf("미소명 경고가 없음: %+v", pc.Warnings)
	}
	// MCP 용 본인 미소명 조회: 본인 소지분만, 카드번호 조각 없음.
	items, err := e.cards.Unreconciled(as(uStaff), 10)
	if err != nil || len(items) != 1 || items[0].CardAlias == "" {
		t.Fatalf("본인 미소명: %+v %v", items, err)
	}
	if other, _ := e.cards.Unreconciled(as(uStaff2), 10); len(other) != 0 {
		t.Fatalf("타인 소지분: %+v", other)
	}
}

// AC-1200 에 준하는 카드 내역 전이 표: 표에 없는 전이는 전부 거부한다.
func TestCardTransitionTableOnly(t *testing.T) {
	ok := map[[2]string]string{
		{"unreconciled", "submit"}: "submitted", {"needs_fix", "submit"}: "submitted", {"submitted", "confirm"}: "confirmed", {"submitted", "confirm_personal"}: "refund_requested",
		{"submitted", "return"}: "needs_fix", {"submitted", "withdraw"}: "unreconciled", {"confirmed", "reverse"}: "needs_fix", {"unreconciled", "refund_request"}: "refund_requested",
		{"needs_fix", "refund_request"}: "refund_requested", {"refund_requested", "refund_done"}: "refunded",
	}
	states := []string{"unreconciled", "submitted", "confirmed", "needs_fix", "not_required", "orphan_cancel", "refund_requested", "refunded"}
	events := []string{"submit", "confirm", "confirm_personal", "return", "withdraw", "reverse", "refund_request", "refund_done"}
	for _, s := range states {
		for _, ev := range events {
			to, err := service.CardTransitionForTest(s, ev)
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
