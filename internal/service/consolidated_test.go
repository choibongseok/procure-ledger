package service_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/dbtest"
	"github.com/choibongseok/procure-ledger/internal/service"
)

// B44 3단계(erp): 연결 재무 집계. 합산만 하고 내부 거래는 표 3 에 참고로만 나온다.

// postIn 은 법인 안에서 전표를 만들어 승인까지 끝낸다.
func (e *env) postIn(t *testing.T, entityID int64, date string, lines ...domain.JournalLineInput) domain.Journal {
	t.Helper()
	ctx := inEntity(uFinance, entityID)
	j, err := e.journals.Create(ctx, domain.JournalInput{PostingDate: date, TradeDate: date, Memo: "연결 집계 시험", Lines: lines})
	if err != nil {
		t.Fatalf("전표 입력: %v", err)
	}
	j, err = e.journals.RequestApproval(ctx, j.No)
	if err != nil {
		t.Fatalf("승인 요청: %v", err)
	}
	decideAs(t, e, uFinance2, j.DocID, true, "확인")
	return j
}

func dl(code, side string, amount, vendor int64) domain.JournalLineInput {
	return domain.JournalLineInput{AccountCode: code, Side: side, Amount: amount, VendorID: vendor}
}

func consRow(c domain.Consolidated, code string) *domain.ConsolidatedAccount {
	for i := range c.Trial {
		if c.Trial[i].Code == code {
			return &c.Trial[i]
		}
	}
	return nil
}

func TestConsolidatedSummarySumsAndIntercompanyIsReferenceOnly(t *testing.T) {
	e := newEnv(t)
	e.jvForm(t)
	abc := e.secondEntity(t)
	svc := service.NewConsolidatedService(e.journals.Base)
	// 연결 거래처: 기본 법인이 계열사(abc)를 거래처로 둔다. 계열사는 본사를 거래처로 둔다.
	toABC := e.poVendor(t, "계열사 거래처", "220-81-62517")
	toMain := e.poVendor(t, "본사 거래처", "100-00-00001")
	if _, err := e.db.Exec(`UPDATE vendors SET linked_entity_id = ? WHERE id = ?`, abc, toABC.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.Exec(`UPDATE vendors SET linked_entity_id = ? WHERE id = ?`, contract.DefaultEntityID, toMain.ID); err != nil {
		t.Fatal(err)
	}
	m, a := contract.DefaultEntityID, abc
	e.postIn(t, m, "2026-11-03", dl("1020", "D", 100000, 0), dl("4110", "C", 100000, 0))
	e.postIn(t, m, "2026-11-04", dl("5210", "D", 30000, 0), dl("1020", "C", 30000, 0))
	e.postIn(t, a, "2026-11-05", dl("1020", "D", 50000, 0), dl("3110", "C", 50000, 0))
	e.postIn(t, m, "2026-11-06", dl("1210", "D", 20000, 0), dl("2110", "C", 20000, toABC.ID))  // 본사 → 계열사 채무
	e.postIn(t, a, "2026-11-07", dl("1010", "D", 18000, toMain.ID), dl("4110", "C", 18000, 0)) // 계열사 → 본사 채권(2000 차이)
	e.postIn(t, m, "2026-10-20", dl("1020", "D", 7000, 0), dl("3110", "C", 7000, 0))           // 전월: 자산은 누계, 수익·비용은 기간만

	c, err := svc.Summary(inEntity(uFinance, 0), domain.ConsolidatedFilter{Unit: "month", Month: "2026-11"})
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Entities) != 2 || c.Entities[0].ID != m || c.Entities[1].ID != a {
		t.Fatalf("열: %+v", c.Entities)
	}
	// 1020: 본사 100000 − 30000 + 7000(누계) = 77000, 계열사 50000.
	r := consRow(c, "1020")
	if r == nil || r.Amounts[0] != 77000 || r.Amounts[1] != 50000 || r.Total != 127000 {
		t.Fatalf("1020: %+v", r)
	}
	// 수익: 기간 안만(본사 100000, 계열사 18000). 비용: 본사 30000.
	if r := consRow(c, "4110"); r == nil || r.Amounts[0] != 100000 || r.Amounts[1] != 18000 || r.Total != 118000 {
		t.Fatalf("4110: %+v", r)
	}
	sum := map[string]domain.ConsolidatedSummaryRow{}
	for _, s := range c.Summary {
		sum[s.Kind] = s
	}
	if sum["revenue"].Total != 118000 || sum["expense"].Total != 30000 || sum["net"].Total != 88000 || sum["net"].Amounts[0] != 70000 || sum["net"].Amounts[1] != 18000 {
		t.Fatalf("요약: %+v", c.Summary)
	}
	// 표 2 의 합계는 표 1 의 단순 합이다(상계 없음): 부채 20000 이 그대로 들어 있다.
	if sum["liability"].Total != 20000 {
		t.Fatalf("부채: %+v", sum["liability"])
	}
	// 표 3: 두 쌍이 참고로 나오고 합계에 반영되지 않는다. 본사→계열사 채무 20000 과 계열사→본사 채권 18000 의 차이 2000.
	if len(c.Intercompany) != 2 {
		t.Fatalf("내부 거래: %+v", c.Intercompany)
	}
	var mainToABC *domain.ConsolidatedPair
	for i := range c.Intercompany {
		if c.Intercompany[i].FromID == m && c.Intercompany[i].ToID == a {
			mainToABC = &c.Intercompany[i]
		}
	}
	if mainToABC == nil || mainToABC.Payable != 20000 || mainToABC.Receivable != 18000 || mainToABC.Gap != 2000 || !mainToABC.CounterpartRecorded {
		t.Fatalf("본사→계열사: %+v", mainToABC)
	}
	// 마감 배지: 아무도 마감하지 않았으면 모두 "마감 전", 계열사만 11월을 마감하면 계열사만 마감.
	if c.Entities[0].Closed || c.Entities[1].Closed {
		t.Fatalf("마감 전이어야 한다: %+v", c.Entities)
	}
	if _, err := e.db.Exec(`INSERT INTO month_closes (entity_id, month, status, version, closed_by, closed_at) VALUES (?, '2026-11', 'closed', 1, ?, '2026-12-01T00:00:00Z')`, abc, uAdmin); err != nil {
		t.Fatal(err)
	}
	c, err = svc.Summary(inEntity(uFinance, 0), domain.ConsolidatedFilter{Unit: "month", Month: "2026-11"})
	if err != nil {
		t.Fatal(err)
	}
	if c.Entities[0].Closed || !c.Entities[1].Closed {
		t.Fatalf("마감 배지: %+v", c.Entities)
	}
	// 분기·연 단위는 기간의 달 수만큼 마감이 있어야 마감이다.
	cq, err := svc.Summary(inEntity(uFinance, 0), domain.ConsolidatedFilter{Unit: "quarter", Year: 2026, Quarter: 4})
	if err != nil {
		t.Fatal(err)
	}
	if cq.Entities[1].Closed || cq.Entities[1].ClosedMonths != 1 || cq.Entities[1].Months != 3 {
		t.Fatalf("분기 마감: %+v", cq.Entities[1])
	}
	// 10월 거래는 분기 합에 들어간다(수익·비용도 분기 전체).
	if r := consRow(cq, "3110"); r == nil || r.Amounts[0] != 7000 {
		t.Fatalf("분기 자본: %+v", r)
	}
	if _, err := svc.Summary(inEntity(uFinance, 0), domain.ConsolidatedFilter{Unit: "week"}); !errors.Is(err, contract.ErrValidation) {
		t.Fatalf("단위: %v", err)
	}
}

func TestConsolidatedScopeFollowsPermissionEntities(t *testing.T) {
	e := newEnv(t)
	e.jvForm(t)
	abc := e.secondEntity(t)
	svc := service.NewConsolidatedService(e.journals.Base)
	e.postIn(t, contract.DefaultEntityID, "2026-11-03", dl("1020", "D", 100000, 0), dl("4110", "C", 100000, 0))
	e.postIn(t, abc, "2026-11-05", dl("1020", "D", 50000, 0), dl("3110", "C", 50000, 0))
	as2 := func(set contract.EntitySet, a contract.ActiveEntity) context.Context {
		p := users[uFinance]
		p.Channel = contract.ChannelWeb
		p.EntityPerms = map[contract.Permission]contract.EntitySet{contract.PermConsolidatedRead: set}
		return contract.WithActiveEntity(contract.WithPrincipal(context.Background(), p), a)
	}
	f := domain.ConsolidatedFilter{Unit: "month", Month: "2026-11"}
	// 그룹 범위: 전체 법인. 법인 하나에만 효력이 있으면 그 법인만 합산되고 일부 법인만의 합임을 알린다.
	c, err := svc.Summary(as2(contract.EntitySet{All: true}, contract.ActiveEntity{All: true}), f)
	if err != nil || len(c.Entities) != 2 || c.PartialScope {
		t.Fatalf("그룹: %+v %v", c.Entities, err)
	}
	c, err = svc.Summary(as2(contract.NewEntitySet(abc), contract.ActiveEntity{ID: abc}), f)
	if err != nil || len(c.Entities) != 1 || c.Entities[0].ID != abc || !c.PartialScope {
		t.Fatalf("계열사만: %+v %v", c.Entities, err)
	}
	if r := consRow(c, "1020"); r == nil || r.Total != 50000 {
		t.Fatalf("계열사만의 합: %+v", r)
	}
	// 효력이 없는 법인에 있으면 거부된다. 직원은 권한이 없다.
	if _, err := svc.Summary(as2(contract.NewEntitySet(abc), contract.ActiveEntity{ID: contract.DefaultEntityID}), f); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("효력 없는 법인: %v", err)
	}
	if _, err := svc.Summary(inEntity(uStaff, contract.DefaultEntityID), f); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("직원: %v", err)
	}
}

// 11.1: 연결 거래처(vendors.linked_entity_id)를 상대로 한 발주·채무·전표·세금계산서에 "법인 간 거래" 표시가 붙고, 연결이 없으면 붙지 않는다.
func TestIntercompanyBadges(t *testing.T) {
	e := newEnv(t)
	e.jvForm(t)
	abc := e.secondEntity(t) // 사업자번호 220-81-62517
	linked := e.poVendor(t, "계열사 거래처", "220-81-62517")
	plain := e.poVendor(t, "일반 거래처", "123-45-67890")
	if _, err := e.db.Exec(`UPDATE vendors SET linked_entity_id = ? WHERE id = ?`, abc, linked.ID); err != nil {
		t.Fatal(err)
	}
	ctx := inEntity(uFinance, contract.DefaultEntityID)
	// 발주
	po := e.orderFor(t, linked, "2026-12-31", 1000, 3)
	got, err := e.inventory.GetPurchaseOrder(ctx, po.ID)
	if err != nil || got.Intercompany != "계열사" {
		t.Fatalf("발주 배지: %q %v", got.Intercompany, err)
	}
	if got2, err := e.inventory.GetPurchaseOrder(ctx, e.orderFor(t, plain, "2026-12-31", 1000, 3).ID); err != nil || got2.Intercompany != "" {
		t.Fatalf("일반 거래처 발주에는 배지가 없다: %q %v", got2.Intercompany, err)
	}
	// 전표(상세·목록)와 채무
	j := e.approveJV(t, e.purchaseJV(t, linked.ID, 55_000))
	jd, err := e.journals.Get(ctx, j.No)
	if err != nil || jd.Intercompany != "계열사" {
		t.Fatalf("전표 배지: %q %v", jd.Intercompany, err)
	}
	page, err := e.journals.List(ctx, domain.JournalFilter{})
	if err != nil || len(page.Rows) == 0 || page.Rows[0].Intercompany != "계열사" {
		t.Fatalf("전표 목록 배지: %+v %v", page.Rows, err)
	}
	_, pay := e.ledgerSvcs()
	rep, err := pay.ByVendor(ctx, "")
	if err != nil || len(rep.Rows) != 1 || rep.Rows[0].Intercompany != "계열사" {
		t.Fatalf("채무 배지: %+v %v", rep.Rows, err)
	}
	// 연결 법인 쪽에서 보면 자기 자신이라 배지가 없다.
	if jd2, _ := e.journals.Get(inEntity(uFinance, abc), j.No); jd2.No != "" {
		t.Fatalf("다른 법인의 전표가 보인다")
	}
	// 세금계산서: 공급자 사업자번호가 다른 법인의 것이면 파생 표시, 아니면 없다.
	e.taxCompany(t)
	inv := e.taxPurchase(t, linked, 100_000, "2026-11-10")
	if v, err := e.tax.Get(ctx, inv.ID); err != nil || v.Intercompany != "계열사" {
		t.Fatalf("세금계산서 배지: %q %v", v.Intercompany, err)
	}
	if v, err := e.tax.Get(ctx, e.taxPurchase(t, plain, 100_000, "2026-11-10").ID); err != nil || v.Intercompany != "" {
		t.Fatalf("일반 세금계산서: %q %v", v.Intercompany, err)
	}
}

// 11.3: 대결자(위임)·결재선에 다른 법인 사람을 넣을 수 없다. 겸직자는 겸직 법인에서 지정할 수 있다.
func TestCrossEntityPeopleCannotBeAssigned(t *testing.T) {
	e := newEnv(t)
	abc := e.secondEntity(t)
	dbtest.FKOff(t, e.db)
	for _, tr := range []string{"trg_org_unit_spans_guard_ins", "trg_org_person_spans_guard_ins", "trg_org_concurrent_spans_guard_ins"} {
		dbtest.DropTrigger(t, e.db, tr) // 조직 서비스 밖에서 구간을 넣는 시험이라 보호 트리거를 푼다
	}
	// 대결자 후보 uHead 를 계열사 조직 소속으로 둔다(기본 법인의 uLead 가 그를 대결자로 지정하려 한다).
	if _, err := e.db.Exec(`INSERT INTO org_unit_spans (unit_id, valid_from, code, name, kind, entity_id) VALUES (9001, '2000-01-01', 'ABC-T', '계열사팀', 'team', ?)`, abc); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.Exec(`INSERT INTO org_person_spans (user_id, valid_from, left_flag, rank_id, primary_unit_id) VALUES (?, '2000-01-01', 0, 1, 9001)`, uHead); err != nil {
		t.Fatal(err)
	}
	ctx := inEntity(uLead, contract.DefaultEntityID)
	_, err := e.delegation.Create(ctx, e.absenceIn(uHead, e.now.Add(24*time.Hour), 3, "vacation"))
	if !errors.Is(err, contract.ErrValidation) || !strings.Contains(err.Error(), "같은 법인 사람만 지정할 수 있습니다") {
		t.Fatalf("다른 법인 대결자: %v", err)
	}
	// 겸직: 기본 법인 조직에도 겸직으로 걸면 지정할 수 있다.
	if _, err := e.db.Exec(`INSERT INTO org_unit_spans (unit_id, valid_from, code, name, kind, entity_id) VALUES (9002, '2000-01-01', 'MAIN-T', '본사팀', 'team', ?)`, contract.DefaultEntityID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.Exec(`INSERT INTO org_concurrent_spans (user_id, unit_id, valid_from) VALUES (?, 9002, '2000-01-01')`, uHead); err != nil {
		t.Fatal(err)
	}
	if _, err := e.delegation.Create(ctx, e.absenceIn(uHead, e.now.Add(24*time.Hour), 3, "vacation")); err != nil {
		t.Fatalf("겸직자: %v", err)
	}
}
