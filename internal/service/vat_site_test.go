package service_test

import (
	"errors"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/service"
)

// B44 3단계(erp) 14.2: 부가세는 사업장(신고 단위)으로 가르고, 총괄납부 사업장은 주사업장 집계에 합산된다. 전체 모드는 거부한다.

func (e *env) addSite(t *testing.T, entityID int64, no, name, kind, mode string) int64 {
	t.Helper()
	res, err := e.db.Exec(`INSERT INTO entity_sites (entity_id, site_no, name, kind, vat_mode, active, created_at, updated_at) VALUES (?, ?, ?, ?, ?, 1, 'x', 'x')`, entityID, no, name, kind, mode)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	if id == 0 { // PG 는 LastInsertId 를 주지 않는다
		if err := e.db.QueryRow(`SELECT id FROM entity_sites WHERE entity_id = ? AND site_no = ?`, entityID, no).Scan(&id); err != nil {
			t.Fatal(err)
		}
	}
	return id
}

func (e *env) vatJV(t *testing.T, site int64, vat int64) {
	t.Helper()
	ctx := inEntity(uFinance, contract.DefaultEntityID)
	j, err := e.journals.Create(ctx, domain.JournalInput{PostingDate: "2026-11-10", TradeDate: "2026-11-05", Memo: "부가세 사업장 시험", SiteID: site, Lines: []domain.JournalLineInput{
		{AccountCode: "1310", Side: "D", Amount: vat}, {AccountCode: "2120", Side: "C", Amount: vat}}})
	if err != nil {
		t.Fatalf("전표 입력: %v", err)
	}
	j, err = e.journals.RequestApproval(ctx, j.No)
	if err != nil {
		t.Fatal(err)
	}
	decideAs(t, e, uFinance2, j.DocID, true, "확인")
}

func TestVATBySiteAndConsolidatedPayment(t *testing.T) {
	e := newEnv(t)
	e.jvForm(t)
	var head int64
	if err := e.db.QueryRow(`SELECT id FROM entity_sites WHERE entity_id = 1 AND kind = 'head'`).Scan(&head); err != nil {
		t.Fatal(err)
	}
	branch := e.addSite(t, 1, "0001", "지점(총괄납부)", "branch", "consolidated")
	factory := e.addSite(t, 1, "0002", "공장(별도신고)", "factory", "own")
	e.vatJV(t, 0, 1000)       // 지정 없음 = 주사업장
	e.vatJV(t, branch, 2000)  // 총괄납부 → 주사업장에 합산
	e.vatJV(t, factory, 4000) // 별도 신고
	vat := service.NewVATService(e.journals.Base)
	ctx := inEntity(uFinance, contract.DefaultEntityID)
	month := domain.VATFilter{Unit: "month", Month: "2026-11"}
	sum := func(site int64) domain.VATPurchase {
		f := month
		f.SiteID = site
		v, err := vat.PurchaseSummary(ctx, f)
		if err != nil {
			t.Fatalf("사업장 %d: %v", site, err)
		}
		return v
	}
	all := sum(0)
	if all.LedgerVAT != 7000 || all.SiteScoped || len(all.Sites) != 3 {
		t.Fatalf("법인 전체: %+v", all)
	}
	bySite := map[string]int64{}
	for _, r := range all.Sites {
		bySite[r.Name] = r.VAT
	}
	if bySite["주사업장"] != 1000 || bySite["지점(총괄납부)"] != 2000 || bySite["공장(별도신고)"] != 4000 {
		t.Fatalf("사업장별: %+v", all.Sites)
	}
	h := sum(head)
	if h.LedgerVAT != 3000 || !h.SiteScoped || len(h.ConsolidatedIn) != 1 || h.ConsolidatedIn[0] != "지점(총괄납부)" {
		t.Fatalf("주사업장(총괄납부 합산): %+v", h)
	}
	if f := sum(factory); f.LedgerVAT != 4000 || len(f.ConsolidatedIn) != 0 {
		t.Fatalf("공장: %+v", f)
	}
	if b := sum(branch); b.LedgerVAT != 2000 {
		t.Fatalf("지점 단독: %+v", b)
	}
	// 다른 법인의 사업장·없는 사업장은 거부, 전체 모드는 거부.
	abc := e.secondEntity(t)
	other := e.addSite(t, abc, "0001", "계열사 지점", "branch", "own")
	f := month
	f.SiteID = other
	if _, err := vat.PurchaseSummary(ctx, f); !errors.Is(err, contract.ErrValidation) {
		t.Fatalf("다른 법인의 사업장: %v", err)
	}
	if _, err := vat.PurchaseSummary(inEntity(uFinance, 0), month); !errors.Is(err, contract.ErrValidation) {
		t.Fatalf("전체 모드: %v", err)
	}
	// 전표 입력: 다른 법인의 사업장을 달 수 없다.
	if _, err := e.journals.Create(ctx, domain.JournalInput{PostingDate: "2026-11-10", Memo: "x", SiteID: other, Lines: []domain.JournalLineInput{
		{AccountCode: "1310", Side: "D", Amount: 1}, {AccountCode: "2120", Side: "C", Amount: 1}}}); !errors.Is(err, contract.ErrValidation) {
		t.Fatalf("전표의 다른 법인 사업장: %v", err)
	}
	// 사업장이 하나뿐인 법인은 사업장 표시가 없다(동작 불변).
	one, err := vat.PurchaseSummary(inEntity(uFinance, abc), month)
	if err != nil || len(one.Sites) != 0 {
		t.Fatalf("사업장 하나: %+v %v", one.Sites, err)
	}
}
