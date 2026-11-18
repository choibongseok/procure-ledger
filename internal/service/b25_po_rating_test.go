package service_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/dbutil"
)

func at(y int, m time.Month, d, h int) time.Time { return time.Date(y, m, d, h, 0, 0, 0, time.UTC) }

func (e *env) poVendor(t *testing.T, name, bizNo string) domain.Vendor {
	t.Helper()
	v, err := e.inventory.CreateVendor(as(uFinance), domain.Vendor{Name: name, BusinessNo: bizNo, Phone: "02-111-2222"})
	if err != nil {
		t.Fatal(err)
	}
	v.BusinessNo = bizNo // 서비스 응답은 가려져 있어 원래 값으로 되돌린다
	return v
}

// orderFor 는 거래처 v 에 대한 발주를 만든다(작성·상신·팀장 승인). 시각은 e.now 로 정한다.
func (e *env) orderFor(t *testing.T, v domain.Vendor, due string, price, qty int64) domain.PurchaseOrder {
	t.Helper()
	skuSeq++
	it := e.item(t, fmt.Sprintf("PO-%d", skuSeq), 0, false)
	pr, err := e.purchase.CreateDraft(as(uStaff), domain.DraftInput{VendorID: v.ID, Title: "발주서 시험", Reason: "x", DueDate: due,
		Lines: []domain.PRLineInput{{ItemID: it.ID, Quantity: qty, UnitPrice: price}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.purchase.Submit(as(uStaff), pr.ID); err != nil {
		t.Fatal(err)
	}
	res := approve(t, e, uLead, pr.ID)
	if res.PurchaseOrder == nil {
		t.Fatal("발주가 만들어지지 않음")
	}
	return *res.PurchaseOrder
}

func (e *env) receiveAll(t *testing.T, po domain.PurchaseOrder) {
	t.Helper()
	if _, err := e.inventory.Receive(as(uFinance), recv(po.ID, po.Lines[0].ID, po.Lines[0].Quantity, "", "")); err != nil {
		t.Fatal(err)
	}
}

// ---- ③ 발주서 ----

func TestPOPrintVATFloorsAndTotals(t *testing.T) {
	e := newEnv(t)
	v := e.poVendor(t, "발주상사", "555-55-55555")
	po := e.orderFor(t, v, "", 12_345, 1) // 공급가액 12,345 → 부가세 1,234(원 미만 버림) → 합계 13,579
	pr, _ := e.purchase.Get(as(uStaff), po.PRID)
	p, err := e.poPrint.Print(as(uStaff), pr.ID)
	if err != nil {
		t.Fatal(err)
	}
	if p.Supply != 12_345 || p.VAT != 1_234 || p.Total != 13_579 || p.PONo != po.No || len(p.Lines) != 1 || p.Lines[0].Unit != "EA" {
		t.Fatalf("%+v", p)
	}
	if p.DueDate != "" || p.DeliveryPlace != "본사 창고" || p.SupplierName != "발주상사" || p.Requester != "직원" || p.Company != "PL 주식회사" {
		t.Fatalf("납기·납품처·공급자·발주처: %+v", p)
	}
	for _, c := range []struct{ supply, vat int64 }{{0, 0}, {9, 0}, {10, 1}, {19, 1}, {100, 10}, {1_999_999, 199_999}} {
		if got := domain.POVAT(c.supply); got != c.vat {
			t.Errorf("부가세(%d) = %d, 기대 %d", c.supply, got, c.vat)
		}
	}
}

func TestPOPrintMaskingAndScope(t *testing.T) {
	e := newEnv(t)
	v := e.poVendor(t, "마스킹상사", "555-55-55555")
	po := e.orderFor(t, v, "2026-12-01", 1_000, 2)
	for _, who := range []int64{uFinance, uAdmin} {
		p, err := e.poPrint.Print(as(who), po.PRID)
		if err != nil || p.BusinessNo != "555-55-55555" || p.Phone != "02-111-2222" || p.Masked || p.DueDate != "2026-12-01" {
			t.Fatalf("%d: %+v %v", who, p, err)
		}
	}
	for _, who := range []int64{uStaff, uAuditor} {
		p, err := e.poPrint.Print(as(who), po.PRID)
		if err != nil || p.BusinessNo == "555-55-55555" || p.Phone == "02-111-2222" || !p.Masked || p.BusinessNo == "" {
			t.Fatalf("%d 는 가려진 값이어야 함: %+v %v", who, p, err)
		}
	}
	// 타 부서는 존재를 드러내지 않는다.
	if _, err := e.poPrint.Print(as(uStaff2), po.PRID); !isErr(err, contract.ErrNotFound) {
		t.Fatalf("타 부서: %v", err)
	}
	// 거래처 연락처가 비면 안내용 표시가 켜진다.
	bare, err := e.inventory.CreateVendor(as(uFinance), domain.Vendor{Name: "연락처없음", BusinessNo: "666-66-66666"})
	if err != nil {
		t.Fatal(err)
	}
	po2 := e.orderFor(t, bare, "", 1_000, 1)
	if p, err := e.poPrint.Print(as(uFinance), po2.PRID); err != nil || !p.ContactEmpty {
		t.Fatalf("%+v %v", p, err)
	}
}

func TestPOPrintBeforeApprovalAndAudit(t *testing.T) {
	e := newEnv(t)
	pr := e.submitted(t, uStaff, 1_000)
	if _, err := e.poPrint.Print(as(uStaff), pr.ID); !isErr(err, contract.ErrInvalidState) || !strings.Contains(err.Error(), "아직 승인되지 않은 구매요청이라 발주서가 없습니다") {
		t.Fatalf("승인 전: %v", err)
	}
	v := e.poVendor(t, "감사상사", "555-55-55555")
	po := e.orderFor(t, v, "", 1_000, 1)
	if _, err := e.poPrint.Print(as(uStaff), po.PRID); err != nil {
		t.Fatal(err)
	}
	// 화면을 여는 것만으로는 감사가 남지 않고, 인쇄 버튼에서만 po.print 가 남는다.
	n := func() int {
		c := 0
		for _, r := range e.rec.Records() {
			if r.Action == "po.print" && r.Result == audit.ResultSuccess && r.TargetType == audit.TargetPurchaseOrder {
				c++
			}
		}
		return c
	}
	if n() != 0 {
		t.Fatal("조회만으로 po.print 가 남음")
	}
	if err := e.poPrint.RecordPrint(as(uStaff), po.PRID); err != nil {
		t.Fatal(err)
	}
	if n() != 1 {
		t.Fatalf("po.print %d건", n())
	}
	if err := e.poPrint.RecordPrint(as(uStaff2), po.PRID); !isErr(err, contract.ErrNotFound) {
		t.Fatalf("타 부서 인쇄 기록: %v", err)
	}
}

// ---- ④ 거래처 평가 ----

func TestRatingOnTimeLateAndNoDue(t *testing.T) {
	e := newEnv(t)
	v := e.poVendor(t, "평가상사", "555-55-55555")
	e.now = at(2026, 11, 1, 1) // 11-01 10시 KST
	onDay := e.orderFor(t, v, "2026-11-05", 1_000, 1)
	late := e.orderFor(t, v, "2026-11-03", 1_000, 1)
	noDue := e.orderFor(t, v, "", 1_000, 1)
	open := e.orderFor(t, v, "2026-11-04", 1_000, 1) // 입고 없이 납기를 넘김
	e.now = at(2026, 11, 5, 1)
	e.receiveAll(t, onDay) // 납기 당일 입고는 납기 내
	e.receiveAll(t, noDue)
	e.now = at(2026, 11, 7, 1)
	e.receiveAll(t, late) // 4일 늦음
	e.now = at(2026, 11, 10, 1)
	_ = open
	rows, err := e.rating.Rate(as(uFinance), "2026-11-01", "2026-11-30")
	if err != nil || len(rows) != 1 {
		t.Fatalf("%v %v", rows, err)
	}
	r := rows[0]
	if r.Orders != 3 || r.WithDue != 2 || r.OnTime != 1 || r.NoDue != 1 || r.OnTimePct != 50 || r.OnTimeBadge != domain.RatingSmall || r.LateOpen != 1 {
		t.Fatalf("%+v", r)
	}
	d, err := e.rating.Detail(as(uAuditor), v.ID, "2026-11-01", "2026-11-30")
	if err != nil || len(d.Late) != 1 || d.Late[0].DaysLate != 4 || d.Late[0].PONo != late.No || len(d.OpenLate) != 1 || d.OpenLate[0].DaysLate != 6 {
		t.Fatalf("%+v %v", d, err)
	}
	// 기간 경계: 입고일이 기간 밖이면 빠진다(첫날·마지막 날 포함).
	if rows, _ := e.rating.Rate(as(uFinance), "2026-11-06", "2026-11-30"); len(rows) != 1 || rows[0].Orders != 1 {
		t.Fatalf("11-06 이후: %+v", rows)
	}
	if rows, _ := e.rating.Rate(as(uFinance), "2026-11-05", "2026-11-05"); len(rows) != 1 || rows[0].Orders != 2 {
		t.Fatalf("하루 기간(입고일 포함): %+v", rows)
	}
	// 입고 완료·확정 판정이 없는 기간에도 납기를 넘긴 미입고 발주가 있는 거래처는 행으로 나온다(점수 없음, 미입고 건수만).
	rows, _ = e.rating.Rate(as(uFinance), "2026-12-01", "2026-12-31")
	if len(rows) != 1 || rows[0].Orders != 0 || rows[0].LateOpen != 1 || rows[0].OnTimeBadge != domain.RatingNone || rows[0].OnTimePct != -1 || rows[0].MismatchBadge != domain.RatingNone {
		t.Fatalf("미입고만 있는 거래처: %+v", rows)
	}
	if d, err := e.rating.Detail(as(uFinance), v.ID, "2026-12-01", "2026-12-31"); err != nil || len(d.OpenLate) != 1 || d.Orders != 0 {
		t.Fatalf("미입고만 있는 거래처 상세: %+v %v", d, err)
	}
}

// 발주도 판정도 미입고도 없는 거래처는 평가 표에 나오지 않는다(거래 없는 기간).
func TestRatingEmptyWhenNothingToShow(t *testing.T) {
	e := newEnv(t)
	e.poVendor(t, "조용상사", "888-88-88888")
	if rows, err := e.rating.Rate(as(uFinance), "2026-11-01", "2026-11-30"); err != nil || len(rows) != 0 {
		t.Fatalf("%+v %v", rows, err)
	}
}

// 같은 이름·같은 사업자번호의 거래처는 새로 등록할 수 없다(기존 데이터는 그대로).
func TestVendorDuplicateRegistrationBlocked(t *testing.T) {
	e := newEnv(t)
	e.poVendor(t, "다온상사", "999-99-99999")
	dup := map[string]domain.Vendor{
		"같은 이름":        {Name: "다온상사", BusinessNo: "111-11-11111"},
		"공백·대소문자 달라도":  {Name: " 다온 상사 ", BusinessNo: ""},
		"같은 사업자번호":     {Name: "다른이름", BusinessNo: "999-99-99999"},
		"하이픈 없는 사업자번호": {Name: "또다른이름", BusinessNo: "9999999999"},
	}
	for name, v := range dup {
		_, err := e.inventory.CreateVendor(as(uFinance), v)
		if !isErr(err, contract.ErrValidation) || !strings.Contains(err.Error(), "이미 등록되어 있습니다(다온상사)") {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if _, err := e.inventory.CreateVendor(as(uFinance), domain.Vendor{Name: "새상사", BusinessNo: "123-12-12345"}); err != nil {
		t.Fatalf("서로 다른 거래처: %v", err)
	}
	// 사업자번호가 비어 있는 두 거래처는 같은 번호로 보지 않는다.
	if _, err := e.inventory.CreateVendor(as(uFinance), domain.Vendor{Name: "번호없는상사"}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.inventory.CreateVendor(as(uFinance), domain.Vendor{Name: "번호없는상사2"}); err != nil {
		t.Fatalf("번호 없는 거래처끼리: %v", err)
	}
}

func TestRatingVerdictsConfirmedOnly(t *testing.T) {
	e := newEnv(t)
	v := e.poVendor(t, "판정상사", "555-55-55555")
	po := e.orderFor(t, v, "", 1_000, 1)
	e.receiveAll(t, po)
	ins := func(outcome, confirmed string, confirmedAt any) {
		doc, err := dbutil.InsertID(context.Background(), e.db, `INSERT INTO documents (kind, filename, content_type, sha256, uploaded_by, department_id, po_id, created_at) VALUES ('transaction_statement','a.txt','text/plain','x',?,?,?,?)`, uFinance, deptFin, po.ID, "2026-11-10T00:00:00Z")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := e.db.Exec(`INSERT INTO document_verdicts (document_id, po_id, outcome, items_json, created_at, confirmed_by, confirmed_outcome, confirmed_at) VALUES (?,?,?,?,?,?,?,?)`,
			doc, po.ID, outcome, "[]", "2026-11-10T00:00:00Z", nilIf(confirmedAt != nil, uFinance), nilIf(confirmedAt != nil, confirmed), confirmedAt); err != nil {
			t.Fatal(err)
		}
	}
	ins("match", "match", "2026-11-10T01:00:00Z")
	ins("mismatch", "mismatch", "2026-11-10T02:00:00Z")
	ins("mismatch", "hold", "2026-11-10T03:00:00Z")     // 확정 결과가 판단 보류: 제외
	ins("match", "", nil)                               // 확정 전(AI 제안만): 제외
	ins("mismatch", "mismatch", "2026-10-01T00:00:00Z") // 확정일이 기간 밖: 제외
	rows, err := e.rating.Rate(as(uAdmin), "2026-11-01", "2026-11-30")
	if err != nil || len(rows) != 1 {
		t.Fatalf("%v %v", rows, err)
	}
	if r := rows[0]; r.Verdicts != 2 || r.Mismatches != 1 || r.MismatchPct != 50 || r.MismatchBadge != domain.RatingSmall {
		t.Fatalf("%+v", r)
	}
	d, _ := e.rating.Detail(as(uAdmin), v.ID, "2026-11-01", "2026-11-30")
	if len(d.Mismatched) != 1 || d.Mismatched[0].PONo != po.No {
		t.Fatalf("%+v", d.Mismatched)
	}
	// 거래가 없어도 거래처가 있으면 상세는 빈 값으로 나온다. 없는 거래처는 404.
	other := e.poVendor(t, "조용한상사", "777-77-77777")
	if d, err := e.rating.Detail(as(uAdmin), other.ID, "2026-11-01", "2026-11-30"); err != nil || d.Orders != 0 || d.OnTimePct != -1 {
		t.Fatalf("%+v %v", d, err)
	}
	if _, err := e.rating.Detail(as(uAdmin), 9999, "2026-11-01", "2026-11-30"); !isErr(err, contract.ErrNotFound) {
		t.Fatalf("없는 거래처: %v", err)
	}
}

func nilIf(cond bool, v any) any {
	if !cond {
		return nil
	}
	return v
}

func TestRatingPermissionsAndPeriodErrors(t *testing.T) {
	e := newEnv(t)
	for _, who := range []int64{uStaff, uLead} {
		if _, err := e.rating.Rate(as(who), "2026-11-01", "2026-11-30"); !isErr(err, contract.ErrForbidden) {
			t.Fatalf("%d: %v", who, err)
		}
	}
	for _, who := range []int64{uFinance, uAdmin, uAuditor} {
		if _, err := e.rating.Rate(as(who), "2026-11-01", "2026-11-30"); err != nil {
			t.Fatalf("%d: %v", who, err)
		}
	}
	if _, err := e.rating.Rate(as(uFinance), "2026-11-30", "2026-11-01"); !isErr(err, contract.ErrValidation) || !strings.Contains(err.Error(), "종료일은 시작일 이후여야 합니다.") {
		t.Fatalf("기간 순서: %v", err)
	}
	if _, err := e.rating.Rate(as(uFinance), "11/01", "2026-11-30"); !isErr(err, contract.ErrValidation) {
		t.Fatalf("형식: %v", err)
	}
}
