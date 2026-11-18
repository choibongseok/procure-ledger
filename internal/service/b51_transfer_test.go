package service_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/choibongseok/procure-ledger/internal/backup"
	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/contract/fake"
	"github.com/choibongseok/procure-ledger/internal/repository"
	"github.com/choibongseok/procure-ledger/internal/service"
)

// 법인 간 재고 이동(B51 4단계, 명세 7절·AC-5130~5138).

type interEnv struct {
	*env
	abc  int64
	wh2  int64 // 둘째 법인(ABC)의 창고
	item domain.Item
	s    stockSvcs
}

// newInterEnv 는 기본 법인 창고(1)에 10개 @100(10월 5일), 둘째 법인 창고에 5개 @300(10월 6일)을 입고한 상태를 만든다.
func newInterEnv(t *testing.T) *interEnv {
	t.Helper()
	e := newEnv(t)
	abc := e.secondEntity(t)
	if _, err := e.db.Exec(`INSERT INTO warehouses (id, code, name, created_at, updated_at, entity_id) VALUES (95,'ABC-W','계열사 창고','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z',?)`, abc); err != nil {
		t.Fatal(err)
	}
	it := e.item(t, "IC-1", 0, false)
	ie := &interEnv{env: e, abc: abc, wh2: 95, item: it, s: e.stock()}
	ie.receive(t, 1, 10, 100, "2026-10-05")
	ie.receive(t, 95, 5, 300, "2026-10-06")
	return ie
}

// receive 는 평가를 거치는 입고 원장 줄을 넣는다(입고 처리 경로 대신 저장소 입구를 직접 쓴다).
func (e *interEnv) receive(t *testing.T, wh, qty, cost int64, day string) {
	t.Helper()
	err := repository.SQLInventory{}.AppendLedger(context.Background(), e.db, repository.LedgerEntry{WarehouseID: wh, ItemID: e.item.ID, Qty: qty, Reason: "receipt", RefType: "receipt", RefID: 9000 + qty, ActorID: uFinance, At: e.now, BookDate: day, UnitCost: cost})
	if err != nil {
		t.Fatal(err)
	}
}

// val 은 (법인, 품목)의 마지막 평가 줄의 잔량·잔액·평균이다.
func (e *interEnv) val(t *testing.T, entity int64) (qty, amt, avg int64) {
	t.Helper()
	if err := e.db.QueryRow(`SELECT bal_qty, bal_amount, avg_cost FROM stock_valuation WHERE entity_id = ? AND item_id = ? ORDER BY book_date DESC, ledger_id DESC LIMIT 1`, entity, e.item.ID).Scan(&qty, &amt, &avg); err != nil {
		t.Fatalf("평가 줄(법인 %d): %v", entity, err)
	}
	return
}

func (e *interEnv) entityHash(t *testing.T, entity int64) string {
	t.Helper()
	rows, err := e.db.Query(`SELECT ledger_id, book_date, unit_cost, amount, bal_qty, bal_amount, avg_cost FROM stock_valuation WHERE entity_id = ? ORDER BY ledger_id`, entity)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var sb strings.Builder
	for rows.Next() {
		var a, c, d, f, g, h int64
		var day string
		if err := rows.Scan(&a, &day, &c, &d, &f, &g, &h); err != nil {
			t.Fatal(err)
		}
		fmt.Fprintln(&sb, a, day, c, d, f, g, h)
	}
	return sb.String()
}

// scoped 는 법인별 권한 범위를 정한 요청이다(현재 법인 cur).
func scoped(user, cur int64, manage, read, valuation []int64) context.Context {
	p := users[user]
	p.Channel = contract.ChannelWeb
	p.EntityPerms = map[contract.Permission]contract.EntitySet{
		contract.PermInventoryManage: contract.NewEntitySet(manage...), contract.PermInventoryRead: contract.NewEntitySet(read...), contract.PermInventoryValuation: contract.NewEntitySet(valuation...),
	}
	return contract.WithActiveEntity(contract.WithPrincipal(context.Background(), p), contract.ActiveEntity{ID: cur})
}

func (e *interEnv) invariants(t *testing.T) {
	t.Helper()
	if v := backup.CheckInvariants(context.Background(), e.db); len(v) != 0 {
		t.Fatalf("불변식 위반: %v", v)
	}
}

// AC-5130·5132·5133·5138: 법인 간 이동은 원장 두 줄과 전표 한 건, 같은 동결 금액, 합계 보존, 전표(분개) 없음, 감사 diff.
func TestInterEntityTransferValuesAndConserves(t *testing.T) {
	e := newInterEnv(t)
	jv := count(t, e.db, `SELECT COUNT(*) FROM journals`)
	in := domain.TransferInput{FromID: 1, ToID: e.wh2, MoveDate: "2026-10-20", Lines: []domain.TransferLineInput{{ItemID: e.item.ID, Qty: 4}}}

	pv, err := e.s.tr.Preview(inEntity(uFinance, contract.DefaultEntityID), in)
	if err != nil || len(pv) != 2 || !pv[0].Intercompany || pv[0].EntityName == "" || !pv[0].Valued || pv[0].UnitCost != 100 || pv[0].Amount != 400 || pv[1].Amount != 400 || pv[0].NoAvg {
		t.Fatalf("미리보기: %+v %v", pv, err)
	}
	slip, err := e.s.tr.Transfer(inEntity(uFinance, contract.DefaultEntityID), in)
	if err != nil || !slip.Intercompany || slip.FromEntityID != 1 || slip.ToEntityID != e.abc || slip.ToEntityName != "계열사" || slip.CostAmount != 400 || !strings.HasPrefix(slip.No, "MV-2026-") {
		t.Fatalf("이동: %+v %v", slip, err)
	}
	if n := count(t, e.db, `SELECT COUNT(*) FROM stock_ledger WHERE ref_type='stock_transfer' AND ref_id=?`, slip.ID); n != 2 {
		t.Fatalf("원장 행 %d", n)
	}
	if n := count(t, e.db, `SELECT COUNT(*) FROM stock_transfers WHERE from_entity_id = 1 AND to_entity_id = ?`, e.abc); n != 1 {
		t.Fatalf("전표 법인 열 %d", n)
	}
	// 출발 법인 평균은 그대로, 도착 법인은 입고 금액만큼 늘어난다. 두 법인 합계는 이동 전후 같다(수량 15, 금액 1000+1500).
	if q, a, avg := e.val(t, 1); q != 6 || a != 600 || avg != 100 {
		t.Fatalf("출발 법인: %d %d %d", q, a, avg)
	}
	if q, a, _ := e.val(t, e.abc); q != 9 || a != 1900 {
		t.Fatalf("도착 법인: %d %d", q, a)
	}
	var outAmt, inAmt int64
	_ = e.db.QueryRow(`SELECT amount FROM stock_valuation WHERE ledger_id IN (SELECT id FROM stock_ledger WHERE ref_type='stock_transfer' AND ref_id=? AND reason='transfer_out')`, slip.ID).Scan(&outAmt)
	_ = e.db.QueryRow(`SELECT amount FROM stock_valuation WHERE ledger_id IN (SELECT id FROM stock_ledger WHERE ref_type='stock_transfer' AND ref_id=? AND reason='transfer_in')`, slip.ID).Scan(&inAmt)
	if outAmt != -400 || inAmt != 400 {
		t.Fatalf("출고·입고 금액: %d %d", outAmt, inAmt)
	}
	if count(t, e.db, `SELECT COUNT(*) FROM journals`) != jv {
		t.Fatal("법인 간 이동이 전표를 만들었습니다(D7)")
	}
	e.invariants(t)
	// 감사: 법인 간 표시와 두 법인 이름, 도착 창고 이름은 남지 않는다.
	var found bool
	for _, a := range e.rec.Records() {
		if a.Action != "stock_transfer.create" || a.Result != "success" {
			continue
		}
		keys := map[string]string{}
		for _, d := range a.Diff {
			keys[d.Key] = d.New
		}
		found = keys["intercompany"] == "예" && keys["to_entity"] == "계열사" && keys["from_entity"] != "" && keys["cost_amount"] != "" && keys["to_warehouse"] == ""
	}
	if !found {
		t.Fatal("감사 diff 에 법인 간 이동 표시가 없음")
	}
	// 양쪽 법인의 이동 목록에 같은 전표가 보이고, 수불부 행에 상대 법인 이름이 붙는다.
	for _, ent := range []int64{1, e.abc} {
		list, err := e.s.tr.ListRecent(inEntity(uFinance, ent), 10)
		if err != nil || len(list) != 1 || list[0].No != slip.No || !list[0].Intercompany || list[0].CostAmount != 400 {
			t.Fatalf("법인 %d 목록: %+v %v", ent, list, err)
		}
	}
	for ent, want := range map[int64]string{1: "법인 간 이동 출고(계열사 법인)", e.abc: "법인 간 이동 입고("} {
		var found bool
		led := e.ledgerFor(t, ent)
		for _, r := range led.Rows {
			if r.Intercompany && strings.HasPrefix(r.KindLabel, want) {
				found = true
			}
		}
		if !found {
			t.Fatalf("법인 %d 수불부에 %q 행이 없음: %+v", ent, want, led.Rows)
		}
	}
}

func (e *interEnv) ledgerFor(t *testing.T, ent int64) domain.LedgerPage {
	t.Helper()
	svc := service.NewLedgerService(service.Base{DB: e.db, Txs: contract.SQLTxManager{DB: e.db}, Audit: e.rec, Masker: fake.Masker{}, Now: func() time.Time { return e.now }})
	p, err := svc.Ledger(inEntity(uFinance, ent), domain.LedgerFilter{ItemID: e.item.ID, From: "2026-01-01", To: "2026-12-31", PageSize: 50})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// AC-5134: 출발 법인에서 이동 앞 날짜로 소급 입고가 들어와도 이동 줄 금액은 동결이라 도착 법인은 그대로다.
func TestInterEntityFrozenCostSurvivesBackdatedReceipt(t *testing.T) {
	e := newInterEnv(t)
	if _, err := e.s.tr.Transfer(inEntity(uFinance, 1), domain.TransferInput{FromID: 1, ToID: e.wh2, MoveDate: "2026-10-20", Lines: []domain.TransferLineInput{{ItemID: e.item.ID, Qty: 4}}}); err != nil {
		t.Fatal(err)
	}
	beforeABC := e.entityHash(t, e.abc)
	e.receive(t, 1, 10, 200, "2026-10-10") // 평균이 바뀌지만 이동 줄 400 은 그대로
	if after := e.entityHash(t, e.abc); after != beforeABC {
		t.Fatalf("도착 법인 평가 줄이 바뀜\n%s\n%s", beforeABC, after)
	}
	var out int64
	if err := e.db.QueryRow(`SELECT amount FROM stock_valuation WHERE ledger_id IN (SELECT id FROM stock_ledger WHERE reason='transfer_out')`).Scan(&out); err != nil || out != -400 {
		t.Fatalf("출고 줄 금액: %d %v", out, err)
	}
	// 출발 법인: 10@100 + 10@200 - 4(동결 400) → 수량 16, 금액 3000-400 = 2600
	if q, a, _ := e.val(t, 1); q != 16 || a != 2600 {
		t.Fatalf("출발 법인: %d %d", q, a)
	}
	e.invariants(t)
}

// AC-5132 경계: 전량 이동은 잔액 전부, 반올림 0.5 올림, 평균 없음은 0원 + 경고.
func TestInterEntityCostBoundaries(t *testing.T) {
	e := newInterEnv(t)
	inv := repository.SQLInventory{}
	costs, err := inv.TransferCosts(context.Background(), e.db, 1, "2026-10-31", []repository.TransferCostLine{{ItemID: e.item.ID, Qty: 10}})
	if err != nil || costs[0] != 1000 {
		t.Fatalf("전량: %v %v", costs, err)
	}
	// 7@100 + 3@133 = 잔액 1099, 평균 109.9. 4개 이동 → 1099*4/10 = 439.6 → 440.
	e.receive(t, 1, 3, 133, "2026-10-07")
	// 앞 입고 10@100 이 있어 평균은 (1000+399)/13.
	costs, err = inv.TransferCosts(context.Background(), e.db, 1, "2026-10-31", []repository.TransferCostLine{{ItemID: e.item.ID, Qty: 4}, {ItemID: e.item.ID, Qty: 9}})
	if err != nil || costs[0] != 430 || costs[1] != 969 || costs[0]+costs[1] != 1399 {
		t.Fatalf("반올림·연속 줄: %v %v", costs, err)
	}
	// 평균을 모르는 품목(둘째 법인에서 출발할 줄이 없는 품목)은 0원.
	other := e.item2(t)
	costs, err = inv.TransferCosts(context.Background(), e.db, 1, "2026-10-31", []repository.TransferCostLine{{ItemID: other.ID, Qty: 1}})
	if err != nil || costs[0] != 0 {
		t.Fatalf("평균 없음: %v %v", costs, err)
	}
	e.seedPlain(t, other.ID, 1, 5) // 평가 줄 없는 재고(기초 단가 없음)로 이동하면 0원 이동 + 경고
	pv, err := e.s.tr.Preview(inEntity(uFinance, 1), domain.TransferInput{FromID: 1, ToID: e.wh2, MoveDate: "2026-10-31", Lines: []domain.TransferLineInput{{ItemID: other.ID, Qty: 1}}})
	if err != nil || len(pv) != 2 || !pv[0].NoAvg || pv[0].Amount != 0 {
		t.Fatalf("평균 없음 미리보기: %+v %v", pv, err)
	}
}

// deniedCount 는 법인 간 이동 거부로 남은 감사 기록 수다.
func (e *interEnv) deniedCount() int {
	n := 0
	for _, r := range e.rec.Records() {
		if r.Action == "stock_transfer.create" && r.Result == "denied" {
			n++
		}
	}
	return n
}

func (e *interEnv) item2(t *testing.T) domain.Item { return e.env.item(t, "IC-2", 0, false) }

// AC-5131: 한 법인에만 권한이 있으면 만들 수 없다. 범위 밖 법인의 창고는 없는 창고와 같은 응답, 범위 안인데 권한이 없으면 403, 둘 다 감사에 거부로 남는다.
func TestInterEntityPermissionPairs(t *testing.T) {
	e := newInterEnv(t)
	in := domain.TransferInput{FromID: 1, ToID: e.wh2, MoveDate: "2026-10-20", Lines: []domain.TransferLineInput{{ItemID: e.item.ID, Qty: 1}}}
	rows := count(t, e.db, `SELECT COUNT(*) FROM stock_ledger`)
	one := []int64{contract.DefaultEntityID}
	both := []int64{contract.DefaultEntityID, e.abc}

	// 범위 밖(기본 법인만 볼 수 있음): 없는 창고처럼.
	before := e.deniedCount()
	if _, err := e.s.tr.Transfer(scoped(uFinance, 1, one, one, one), in); !errors.Is(err, contract.ErrValidation) || !strings.Contains(err.Error(), "도착 창고를 찾을 수 없습니다") {
		t.Fatalf("범위 밖: %v", err)
	}
	if _, err := e.s.tr.Preview(scoped(uFinance, 1, one, one, one), in); !errors.Is(err, contract.ErrValidation) {
		t.Fatalf("범위 밖 미리보기: %v", err)
	}
	if e.deniedCount() != before+2 {
		t.Fatal("범위 밖 도착 창고 거부가 감사에 남지 않음")
	}
	// 범위 안(읽기 가능) + 권한 없음: 403.
	if _, err := e.s.tr.Transfer(scoped(uFinance, 1, one, both, both), in); !errors.Is(err, contract.ErrForbidden) || !strings.Contains(err.Error(), "도착 법인에서 재고를 관리할 권한이 없어") {
		t.Fatalf("권한 없음: %v", err)
	}
	// 도착 후보는 두 법인 모두에서 권한이 있을 때만 나온다.
	if ts, err := e.s.tr.Targets(scoped(uFinance, 1, one, both, both)); err != nil || len(ts) != 0 {
		t.Fatalf("권한 없는 후보: %+v %v", ts, err)
	}
	if ts, err := e.s.tr.Targets(scoped(uFinance, 1, both, both, both)); err != nil || len(ts) != 1 || ts[0].ID != e.wh2 || ts[0].EntityName != "계열사" {
		t.Fatalf("후보: %+v %v", ts, err)
	}
	if count(t, e.db, `SELECT COUNT(*) FROM stock_ledger`) != rows || count(t, e.db, `SELECT COUNT(*) FROM stock_transfers`) != 0 {
		t.Fatal("거부된 이동이 기록을 남김")
	}
	// 두 법인 모두 권한이 있으면 된다. 직원은 어느 쪽이든 안 된다.
	if _, err := e.s.tr.Transfer(scoped(uFinance, 1, both, both, one), in); err != nil {
		t.Fatalf("양쪽 권한: %v", err)
	}
	if _, err := e.s.tr.Transfer(inEntity(uStaff, 1), in); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("직원: %v", err)
	}
	// 금액은 valuation_read 가 있는 법인에서만 보인다.
	list, err := e.s.tr.ListRecent(scoped(uFinance, 1, both, both, []int64{e.abc}), 5)
	if err != nil || len(list) != 1 || list[0].CostAmount != 100 || !list[0].Valued {
		t.Fatalf("금액 있음: %+v %v", list, err)
	}
	list, err = e.s.tr.ListRecent(scoped(uFinance, 1, both, both, nil), 5)
	if err != nil || len(list) != 1 || list[0].CostAmount != 0 || list[0].Valued {
		t.Fatalf("금액 없음: %+v %v", list, err)
	}
}

// D8: 상대 법인이 읽는 사람의 범위 밖이면 상대 창고 이름은 숨기고 법인 이름만 남긴다.
func TestInterEntityHidesCounterpartWarehouse(t *testing.T) {
	e := newInterEnv(t)
	if _, err := e.s.tr.Transfer(inEntity(uFinance, 1), domain.TransferInput{FromID: 1, ToID: e.wh2, MoveDate: "2026-10-20", Lines: []domain.TransferLineInput{{ItemID: e.item.ID, Qty: 1}}}); err != nil {
		t.Fatal(err)
	}
	abcOnly := []int64{e.abc}
	list, err := e.s.tr.ListRecent(scoped(uFinance, e.abc, abcOnly, abcOnly, abcOnly), 5)
	if err != nil || len(list) != 1 {
		t.Fatalf("도착 법인 목록: %+v %v", list, err)
	}
	s := list[0]
	if s.FromName != "" || s.FromID != 0 || s.FromEntityName == "" || s.ToName != "계열사 창고" {
		t.Fatalf("출발 창고가 숨겨져야 함: %+v", s)
	}
	// 출발 법인에서 범위가 한 법인뿐인 사용자도 같다.
	list, _ = e.s.tr.ListRecent(scoped(uFinance, 1, []int64{1}, []int64{1}, []int64{1}), 5)
	if len(list) != 1 || list[0].ToName != "" || list[0].ToEntityName != "계열사" || list[0].FromName == "" {
		t.Fatalf("도착 창고가 숨겨져야 함: %+v", list)
	}
	// 무관한 법인에서는 보이지 않는다.
	if _, err := e.db.Exec(`INSERT INTO entities (code, name, business_no, is_default, active, version, created_at, updated_at) VALUES ('XYZ', '제삼법인', '1234567890', 0, 1, 1, 'x', 'x')`); err != nil {
		t.Fatal(err)
	}
	var third int64
	_ = e.db.QueryRow(`SELECT id FROM entities WHERE code='XYZ'`).Scan(&third)
	if list, _ = e.s.tr.ListRecent(inEntity(uFinance, third), 5); len(list) != 0 {
		t.Fatalf("제삼 법인에 전표가 보임: %+v", list)
	}
}

// AC-5135: 어느 한 법인이라도 이동일 월이 마감이면 거부된다(출발·도착 각각 문구가 다르다).
func TestInterEntityRejectsClosedMonthOnEitherSide(t *testing.T) {
	e := newInterEnv(t)
	in := domain.TransferInput{FromID: 1, ToID: e.wh2, MoveDate: "2026-10-20", Lines: []domain.TransferLineInput{{ItemID: e.item.ID, Qty: 1}}}
	rows := count(t, e.db, `SELECT COUNT(*) FROM stock_ledger`)
	// 도착 법인만 마감 → 도착 문구, 출발 법인도 마감하면 현행 마감 문구가 먼저다(마감 행은 지울 수 없어 이어서 닫는다).
	for _, c := range []struct {
		closed int64
		want   string
	}{{e.abc, "도착 법인(계열사)의 해당 월이 마감되어 이동할 수 없습니다."}, {1, "마감된 월입니다. 재무팀에 마감 해제를 요청하세요."}} {
		if _, err := e.db.Exec(`INSERT INTO month_closes (month, status, version, closed_at, entity_id) VALUES ('2026-10','closed',1,'2026-11-01T00:00:00Z',?)`, c.closed); err != nil {
			t.Fatal(err)
		}
		denied := e.deniedCount()
		if _, err := e.s.tr.Transfer(inEntity(uFinance, 1), in); !errors.Is(err, contract.ErrValidation) || !strings.Contains(err.Error(), c.want) {
			t.Fatalf("법인 %d 마감: %v", c.closed, err)
		}
		if e.deniedCount() != denied+1 {
			t.Fatal("마감 거부가 감사에 남지 않음")
		}
		if _, err := e.s.tr.Preview(inEntity(uFinance, 1), in); !errors.Is(err, contract.ErrValidation) {
			t.Fatalf("마감 미리보기: %v", err)
		}
	}
	if count(t, e.db, `SELECT COUNT(*) FROM stock_ledger`) != rows {
		t.Fatal("마감 거부된 이동이 원장에 남음")
	}
	// 열린 달이면 된다.
	in.MoveDate = "2026-11-05"
	if _, err := e.s.tr.Transfer(inEntity(uFinance, 1), in); err != nil {
		t.Fatalf("열린 달: %v", err)
	}
}

// AC-5133: 임의의 이동을 여러 번(양방향) 해도 두 법인의 수량 합·금액 합이 변하지 않고 양쪽 불변식 위반이 없다.
func TestInterEntityTransfersConserveTotals(t *testing.T) {
	e := newInterEnv(t)
	sum := func() (int64, int64) {
		q1, a1, _ := e.val(t, 1)
		q2, a2, _ := e.val(t, e.abc)
		return q1 + q2, a1 + a2
	}
	q0, a0 := sum()
	if q0 != 15 || a0 != 2500 {
		t.Fatalf("시작: %d %d", q0, a0)
	}
	moves := []struct {
		from, to, qty int64
		day           string
	}{{1, 95, 3, "2026-10-10"}, {95, 1, 4, "2026-10-12"}, {1, 95, 5, "2026-10-15"}, {95, 1, 2, "2026-10-20"}, {1, 95, 1, "2026-10-25"}, {95, 1, 7, "2026-10-28"}, {1, 95, 6, "2026-10-30"}}
	for i, m := range moves {
		cur := int64(1)
		if m.from == 95 {
			cur = e.abc
		}
		if _, err := e.s.tr.Transfer(inEntity(uFinance, cur), domain.TransferInput{FromID: m.from, ToID: m.to, MoveDate: m.day, Lines: []domain.TransferLineInput{{ItemID: e.item.ID, Qty: m.qty}}}); err != nil {
			t.Fatalf("이동 %d: %v", i, err)
		}
		if q, a := sum(); q != q0 || a != a0 {
			t.Fatalf("이동 %d 뒤 합계 %d %d, 기대 %d %d", i, q, a, q0, a0)
		}
		e.invariants(t)
	}
	if n := count(t, e.db, `SELECT COUNT(*) FROM stock_transfers WHERE from_entity_id <> to_entity_id`); n != len(moves) {
		t.Fatalf("법인 간 전표 %d", n)
	}
}

// 같은 법인 이동은 그대로다: 전표 법인 열이 같고 동결 금액이 없으며, 목록에 법인 간 표시가 없다.
func TestSameEntityTransferStaysPlain(t *testing.T) {
	e := newInterEnv(t)
	w2, err := e.s.wh.Save(inEntity(uFinance, 1), domain.WarehouseInput{Code: "WH-02", Name: "물류센터"})
	if err != nil {
		t.Fatal(err)
	}
	slip, err := e.s.tr.Transfer(inEntity(uFinance, 1), domain.TransferInput{FromID: 1, ToID: w2.ID, Lines: []domain.TransferLineInput{{ItemID: e.item.ID, Qty: 2}}})
	if err != nil || slip.Intercompany || slip.CostAmount != 0 {
		t.Fatalf("같은 법인 이동: %+v %v", slip, err)
	}
	if n := count(t, e.db, `SELECT COUNT(*) FROM stock_transfer_lines WHERE cost_amount <> 0`); n != 0 {
		t.Fatalf("동결 금액이 같은 법인 이동에 남음 %d", n)
	}
	if q, a, avg := e.val(t, 1); q != 10 || a != 1000 || avg != 100 {
		t.Fatalf("같은 법인 이동이 평가를 바꿈: %d %d %d", q, a, avg)
	}
}

// D15: 창고의 법인은 만든 뒤 바꿀 수 없다(DB 트리거).
func TestWarehouseEntityIsImmutable(t *testing.T) {
	e := newInterEnv(t)
	if _, err := e.db.Exec(`UPDATE warehouses SET entity_id = 1 WHERE id = ?`, e.wh2); err == nil || !strings.Contains(err.Error(), "창고의 법인은 바꿀 수 없습니다") {
		t.Fatalf("창고 법인 변경이 막히지 않음: %v", err)
	}
	if _, err := e.db.Exec(`UPDATE warehouses SET name = '새 이름' WHERE id = ?`, e.wh2); err != nil {
		t.Fatalf("이름 변경은 되어야 함: %v", err)
	}
}

// AC-5130: 둘째 줄에서 실패하면 첫 줄까지 모두 취소된다(원장·평가·전표·번호 카운터가 그대로).
func TestInterEntityTransferRollsBackAsOne(t *testing.T) {
	e := newInterEnv(t)
	other := e.item2(t)
	e.receive2(t, other.ID, 1, 2, 50, "2026-10-08")
	snap := func() [4]int {
		return [4]int{count(t, e.db, `SELECT COUNT(*) FROM stock_ledger`), count(t, e.db, `SELECT COUNT(*) FROM stock_valuation`), count(t, e.db, `SELECT COUNT(*) FROM stock_transfers`), count(t, e.db, `SELECT COALESCE(SUM(last),0) FROM inventory_counters`)}
	}
	before := snap()
	hash := e.entityHash(t, e.abc)
	_, err := e.s.tr.Transfer(inEntity(uFinance, 1), domain.TransferInput{FromID: 1, ToID: e.wh2, MoveDate: "2026-10-20", Lines: []domain.TransferLineInput{{ItemID: e.item.ID, Qty: 3}, {ItemID: other.ID, Qty: 3}}})
	if !errors.Is(err, contract.ErrValidation) || !strings.Contains(err.Error(), "재고가 부족합니다") {
		t.Fatalf("둘째 줄 실패: %v", err)
	}
	if after := snap(); after != before || e.entityHash(t, e.abc) != hash {
		t.Fatalf("실패한 이동이 기록을 남김: %v → %v", before, after)
	}
}

func (e *interEnv) receive2(t *testing.T, item, wh, qty, cost int64, day string) {
	t.Helper()
	err := repository.SQLInventory{}.AppendLedger(context.Background(), e.db, repository.LedgerEntry{WarehouseID: wh, ItemID: item, Qty: qty, Reason: "receipt", RefType: "receipt", RefID: 9100 + item, ActorID: uFinance, At: e.now, BookDate: day, UnitCost: cost})
	if err != nil {
		t.Fatal(err)
	}
}

// AC-5137: 두 법인을 반대 방향으로 동시에 이동해도 교착하지 않는다(PG 에서는 평가 잠금 순서가 법인 번호 순서라야 한다). 끝난 뒤 합계는 그대로다.
func TestInterEntityOppositeDirectionsDoNotDeadlock(t *testing.T) {
	e := newInterEnv(t)
	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for i := 0; i < 12; i++ {
		from, to, cur := int64(1), e.wh2, int64(1)
		if i%2 == 1 {
			from, to, cur = e.wh2, 1, e.abc
		}
		wg.Add(1)
		go func(from, to, cur int64) {
			defer wg.Done()
			_, err := e.s.tr.Transfer(inEntity(uFinance, cur), domain.TransferInput{FromID: from, ToID: to, MoveDate: "2026-10-25", Lines: []domain.TransferLineInput{{ItemID: e.item.ID, Qty: 1}}})
			if err != nil && !errors.Is(err, contract.ErrValidation) { // 재고가 모자란 거부만 허용한다. 교착·직렬화 실패는 허용하지 않는다
				errs <- err
			}
		}(from, to, cur)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("동시 이동 실패: %v", err)
	}
	q1, a1, _ := e.val(t, 1)
	q2, a2, _ := e.val(t, e.abc)
	if q1+q2 != 15 || a1+a2 != 2500 {
		t.Fatalf("합계가 바뀜: 수량 %d 금액 %d", q1+q2, a1+a2)
	}
	e.invariants(t)
}
