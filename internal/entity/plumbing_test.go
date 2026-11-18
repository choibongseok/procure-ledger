package entity_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/dbtest"
	"github.com/choibongseok/procure-ledger/internal/entity"
	"github.com/choibongseok/procure-ledger/internal/org"
	"github.com/choibongseok/procure-ledger/internal/repository"
)

// B44 2단계 platform(0075~0077): 법인 열·법인별 카운터·법인별 월 마감과 저장소 배관.

func (e *env) newEntity(code string) int64 {
	e.t.Helper()
	got, err := e.svc.Create(e.ctx(), entity.Input{Code: code, Name: code + " 법인"})
	if err != nil {
		e.t.Fatal(err)
	}
	return got.ID
}

func TestEntityColumnsExist(t *testing.T) {
	e := newEnv(t)
	for _, tbl := range []string{"journals", "payables", "payment_requests", "bank_accounts", "corporate_cards", "card_imports", "card_transactions", "budgets", "budget_revisions",
		"purchase_requests", "purchase_orders", "receipts", "tax_invoices", "company_assets", "import_batches", "warehouses", "change_events", "api_tokens", "month_close_events", "month_close_lines",
		"inventory_counters", "document_counters", "month_closes", "tax_asp_settings", "tax_sync_state"} {
		if _, err := e.db.Exec(`SELECT entity_id FROM ` + tbl + ` WHERE 1 = 0`); err != nil {
			t.Errorf("%s 에 entity_id 열이 없다: %v", tbl, err)
		}
	}
}

// 외래 키 없이 만든 법인 열(SQLite 는 기본값이 있는 NOT NULL 열에 외래 키를 못 붙인다)이 항상 있는 법인을 가리키는지 맞춘다. 법인을 만든 뒤 업무 행이 쌓인 상태에서도 고아 값이 없어야 한다.
func TestEntityColumnsPointAtEntities(t *testing.T) {
	e := newEnv(t)
	for _, tbl := range []string{"journals", "payables", "payment_requests", "bank_accounts", "corporate_cards", "card_imports", "card_transactions", "budgets", "budget_revisions",
		"purchase_requests", "purchase_orders", "receipts", "tax_invoices", "company_assets", "import_batches", "warehouses", "change_events", "api_tokens",
		"inventory_counters", "document_counters", "month_closes", "tax_asp_settings", "tax_sync_state"} {
		if n := e.count(`SELECT COUNT(*) FROM ` + tbl + ` t WHERE NOT EXISTS (SELECT 1 FROM entities WHERE id = t.entity_id)`); n != 0 {
			t.Errorf("%s: 없는 법인을 가리키는 행 %d", tbl, n)
		}
	}
}

func dbExecID(t *testing.T, e *env, q string, a ...any) int64 {
	t.Helper()
	return dbtest.ExecID(t, e.db, q, a...)
}

// 번호 카운터는 법인별이다: 같은 접두·연도라도 법인마다 따로 올라가고, 접두는 기본 법인이 아니면 법인 코드가 붙는다(D4).
func TestCountersAreIndependentPerEntity(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	abc := e.newEntity("ABC")
	tx := contract.SQLTxManager{DB: e.db}
	next := func(ent int64) int64 {
		var n int64
		if err := tx.WithTx(ctx, func(q contract.Tx) error {
			var err error
			n, err = repository.NextEntityCounter(ctx, q, ent, "JV", 2026)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if a, b, c, d := next(1), next(1), next(abc), next(1); a != 1 || b != 2 || c != 1 || d != 3 {
		t.Fatalf("번호가 법인별이 아니다: %d %d %d %d", a, b, c, d)
	}
	var seq int64
	if err := tx.WithTx(ctx, func(q contract.Tx) error {
		var err error
		seq, err = repository.NextDocSeq(ctx, q, abc, 1, 0, 2026)
		if err == nil {
			seq, err = repository.NextDocSeq(ctx, q, abc, 1, 0, 2026)
		}
		return err
	}); err != nil || seq != 2 {
		t.Fatalf("문서 일련번호: %d %v", seq, err)
	}
	if p, err := repository.EntityNoPrefix(ctx, e.db, 1); err != nil || p != "" {
		t.Fatalf("기본 법인 접두: %q %v", p, err)
	}
	if p, err := repository.EntityNoPrefix(ctx, e.db, abc); err != nil || p != "ABC-" {
		t.Fatalf("계열사 접두: %q %v", p, err)
	}
	if _, err := repository.EntityNoPrefix(ctx, e.db, 999); !errors.Is(err, contract.ErrNotFound) {
		t.Fatalf("없는 법인: %v", err)
	}
}

func TestEnsureEntityWritable(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	abc := e.newEntity("ABC")
	if err := repository.EnsureEntityWritable(ctx, e.db, abc); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.SetActive(e.ctx(), abc, false); err != nil {
		t.Fatal(err)
	}
	if err := repository.EnsureEntityWritable(ctx, e.db, abc); !errors.Is(err, contract.ErrInvalidState) || !strings.Contains(err.Error(), "비활성 법인") {
		t.Fatalf("비활성 법인: %v", err)
	}
	if err := repository.EnsureEntityWritable(ctx, e.db, 999); !errors.Is(err, contract.ErrNotFound) {
		t.Fatalf("없는 법인: %v", err)
	}
	sc := contract.EntityScope{IDs: []int64{1, abc}}
	if c, args := repository.EntityCond(sc, "j"); c != "j.entity_id IN (?,?)" || len(args) != 2 {
		t.Fatalf("조건: %s", c)
	}
}

// 월 마감은 법인별이다(AC-4408): A 법인이 마감한 달에도 B 법인은 쓸 수 있고, A 의 마감 보호(전표·재고)는 유지된다.
func TestMonthCloseIsPerEntity(t *testing.T) {
	e := newEnv(t)
	abc := e.newEntity("ABC")
	insertJournal := func(ent int64, no, date string) error {
		_, err := e.db.Exec(`INSERT INTO journals (no, kind, status, posting_date, trade_date, created_by, created_at, entity_id) VALUES (?, 'general', 'draft', ?, ?, 900, 't', ?)`, no, date, date, ent)
		return err
	}
	if _, err := e.db.Exec(`INSERT INTO month_closes (entity_id, month, status, version, closed_by, closed_at) VALUES (1, '2026-10', 'closed', 1, 900, 't')`); err != nil {
		t.Fatal(err)
	}
	if err := insertJournal(1, "JV-A", "2026-10-05"); err == nil || !strings.Contains(err.Error(), "마감된 월입니다") {
		t.Fatalf("마감한 법인의 전표는 막혀야 한다: %v", err)
	}
	if err := insertJournal(abc, "ABC-JV-1", "2026-10-05"); err != nil {
		t.Fatalf("다른 법인의 전표는 막히면 안 된다: %v", err)
	}
	// 같은 달을 두 법인이 따로 마감할 수 있고, 마감 상태 갱신 보호는 법인 키를 바꾸지 못한다.
	if _, err := e.db.Exec(`INSERT INTO month_closes (entity_id, month, status, version, closed_by, closed_at) VALUES (?, '2026-10', 'closed', 1, 900, 't')`, abc); err != nil {
		t.Fatalf("다른 법인의 같은 달 마감: %v", err)
	}
	if _, err := e.db.Exec(`UPDATE month_closes SET entity_id = 1 WHERE entity_id = ?`, abc); err == nil {
		t.Fatal("마감 행의 법인을 바꿀 수 있다")
	}
	// 재고: 마감 여부는 창고의 법인으로 본다.
	whA := dbExecID(t, e, `INSERT INTO warehouses (code, name, entity_id, created_at, updated_at) VALUES ('WH-A', '법인1 창고', 1, 't', 't')`)
	whB := dbExecID(t, e, `INSERT INTO warehouses (code, name, entity_id, created_at, updated_at) VALUES ('WH-B', '계열사 창고', ?, 't', 't')`, abc)
	if _, err := e.db.Exec(`INSERT INTO month_closes (entity_id, month, status, version, closed_by, closed_at) VALUES (1, '2026-09', 'closed', 1, 900, 't')`); err != nil {
		t.Fatal(err)
	}
	ins := func(wh int64) error {
		itemID := dbExecID(t, e, `INSERT INTO items (sku, name, unit) VALUES (?, '품목', 'EA')`, "SKU-"+strings.Repeat("x", int(wh)))
		_, err := e.db.Exec(`INSERT INTO stock_ledger (item_id, warehouse_id, qty_delta, reason, actor_id, book_date, created_at) VALUES (?, ?, 1, 'adjust', 900, '2026-09-10', '2026-09-10T00:00:00Z')`, itemID, wh)
		return err
	}
	if err := ins(whA); err == nil || !strings.Contains(err.Error(), "마감된 월입니다") {
		t.Fatalf("마감한 법인의 창고 입고는 막혀야 한다: %v", err)
	}
	if err := ins(whB); err != nil {
		t.Fatalf("다른 법인의 창고는 막히면 안 된다: %v", err)
	}
}

// 전표·지급 요청·세금계산서의 법인은 만든 뒤 바꿀 수 없다(변경 불가 불변식).
func TestEntityIsImmutableOnJournalPaymentTaxInvoice(t *testing.T) {
	e := newEnv(t)
	abc := e.newEntity("ABC")
	jid := dbExecID(t, e, `INSERT INTO journals (no, kind, status, posting_date, trade_date, created_by, created_at, entity_id) VALUES ('JV-X', 'general', 'draft', '2026-03-01', '2026-03-01', 900, 't', 1)`)
	if _, err := e.db.Exec(`UPDATE journals SET entity_id = ? WHERE id = ?`, abc, jid); err == nil || !strings.Contains(err.Error(), "법인은 바꿀 수 없습니다") {
		t.Fatalf("전표 법인 변경이 막혀야 한다: %v", err)
	}
	if _, err := e.db.Exec(`UPDATE journals SET entity_id = 1 WHERE id = ?`, jid); err != nil {
		t.Fatalf("같은 값 갱신은 된다: %v", err)
	}
}

// AC-4416: 조직을 다른 법인의 조직도 아래로 옮기는 이동은 그 조직에 업무 데이터(예산·구매요청)가 있으면 거부된다. 없으면 된다.
func TestUnitMoveAcrossEntitiesRefusedWithBusinessData(t *testing.T) {
	e := newEnv(t)
	rootA := e.unit("CO", "본사", contract.OrgCompany, 0)
	deptA := e.unit("DA", "구매팀", contract.OrgDepartment, rootA)
	deptC := e.unit("DC", "총무팀", contract.OrgDepartment, rootA)
	rootB := e.unit("AB", "계열사 본사", contract.OrgCompany, 0)
	if _, err := e.svc.Create(e.ctx(), entity.Input{Code: "ABC", Name: "계열사", RootUnitID: rootB}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.Exec(`INSERT INTO budgets (department_id, month, amount, updated_by, updated_at, entity_id) VALUES (?, '2026-03', 1000, 900, 't', 1)`, deptA); err != nil {
		t.Fatal(err)
	}
	move := func(unit int64) error {
		_, err := e.org.Register(context.Background(), e.admin, org.Command{Kind: org.KindUnitMove, UnitID: unit, ParentID: rootB, Effective: "2026-03-01", Reason: "이동", Confirm: true})
		return err
	}
	if err := move(deptA); err == nil || !strings.Contains(err.Error(), "업무 데이터가 있는 조직") {
		t.Fatalf("업무 데이터가 있는 조직의 법인 이동은 거부돼야 한다: %v", err)
	}
	if err := move(deptC); err != nil {
		t.Fatalf("업무 데이터가 없으면 이동된다: %v", err)
	}
}

// B44 낮음 ⑨: 법인이 둘이 되는 등록에서 조직에 배치되지 않은 활성 사용자가 있으면 경고를 준다(막지 않는다). 배치된 사용자는 세지 않는다.
func TestCreateWarnsAboutUnplacedUsers(t *testing.T) {
	e := newEnv(t)
	root := e.unit("CO", "본사", contract.OrgCompany, 0)
	dept := e.unit("DA", "구매팀", contract.OrgDepartment, root)
	e.hire("placed@pl.test", dept)
	got, err := e.svc.Create(e.ctx(), entity.Input{Code: "ABC", Name: "계열사"})
	if err != nil {
		t.Fatal(err)
	}
	// 관리자(900)는 조직에 배치되지 않았다: 1명. 배치된 사원은 세지 않는다.
	if len(got.Warnings) != 1 || !strings.Contains(got.Warnings[0], "조직에 배치되지 않은 사용자 1명") {
		t.Fatalf("경고: %v", got.Warnings)
	}
	if _, err := e.db.Exec(`UPDATE users SET active = 0 WHERE id = 900`); err != nil {
		t.Fatal(err)
	}
	got2, err := e.svc.Create(e.ctx(), entity.Input{Code: "XYZ", Name: "또"})
	if err != nil || len(got2.Warnings) != 0 {
		t.Fatalf("미배치 활성 사용자가 없으면 경고가 없다: %v %v", got2.Warnings, err)
	}
}
