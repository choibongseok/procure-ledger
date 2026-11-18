package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/choibongseok/procure-ledger/internal/bootstrap"
	"github.com/choibongseok/procure-ledger/internal/config"
	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/dbtest"
	"github.com/choibongseok/procure-ledger/internal/mcptool"
)

// AC-4804 안전장치(B48 판정 후속): 법인이 둘인 시드로 조회 자원 8종 각각 "다른 법인 데이터가 결과에 없다"를 확인하는 표 시험이다.
// 카탈로그에 자원을 더하면 이 표에도 넣어야 한다(표에 없는 자원이 있으면 실패한다). 법인 소유가 아닌 자원(품목)은 두 법인이 같은 결과를 본다는 것을 확인한다.
//
// 어댑터는 호출자 context(현재 법인)를 서비스에 그대로 넘기므로, 시험은 현재 법인만 바꿔 같은 질의를 법인마다 돌려 본다.
type entityProbe struct {
	seed   func(t *testing.T, e int64, tag string) // 법인 e 에 표식(tag)이 있는 행을 만든다
	query  mcptool.Query
	marker func(row map[string]any) string // 결과 행에서 표식을 읽는다
	global bool                            // 법인 소유가 아닌 자원: 두 법인이 같은 행을 본다
}

func TestCustomToolResourcesAreEntityIsolated(t *testing.T) {
	dbtest.SetEnv(t)
	t.Setenv("PL_DEV_IDP", "1")
	t.Setenv("PL_LLM_PROVIDER", "mock")
	cfg := config.Load()
	ctx := context.Background()
	d, closeDB, err := bootstrap.Build(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer closeDB()

	exec := func(q string, args ...any) int64 {
		t.Helper()
		var id int64
		if err := d.DB.QueryRowContext(ctx, q+` RETURNING id`, args...).Scan(&id); err != nil {
			t.Fatalf("시드 실패: %v\n%s", err, q)
		}
		return id
	}
	one := func(q string, args ...any) int64 {
		t.Helper()
		var id int64
		if err := d.DB.QueryRowContext(ctx, q, args...).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	abc := exec(`INSERT INTO entities (code, name, is_default, active, version, created_at, updated_at) VALUES ('ABC', '계열사', 0, 1, 1, 'x', 'x')`)
	var admin contract.Principal
	var role string
	if err := d.DB.QueryRowContext(ctx, `SELECT id, role, department_id FROM users WHERE email = 'admin@pl.test'`).Scan(&admin.UserID, &role, &admin.DepartmentID); err != nil {
		t.Fatal(err)
	}
	admin.Role, admin.Name, admin.Channel = contract.Role(role), "admin", contract.ChannelMCP
	admin.Perms = contract.NewPermSet(contract.AllPermissions...)
	inEntity := func(e int64) context.Context {
		return contract.WithActiveEntity(contract.WithPrincipal(ctx, admin), contract.ActiveEntity{ID: e})
	}
	entityName := map[int64]string{contract.DefaultEntityID: "E1", abc: "E2"}
	dept := admin.DepartmentID
	pad := func(e int64, n string) string { return n + "-" + entityName[e] }
	vendorOf := map[int64]int64{}
	for e, n := range entityName {
		vendorOf[e] = exec(`INSERT INTO vendors (name) VALUES (?)`, "거래처-"+n)
	}
	prOf := map[int64]int64{}
	journalOf := func(t *testing.T, e int64) (int64, int64) {
		j, err := d.Journals.Create(inEntity(e), domain.JournalInput{PostingDate: "2026-11-10", TradeDate: "2026-11-05", Memo: "격리 시험 " + entityName[e], Lines: []domain.JournalLineInput{
			{AccountCode: "1210", Side: "D", Amount: 5000}, {AccountCode: "1310", Side: "C", Amount: 5000}}})
		if err != nil {
			t.Fatalf("전표 입력: %v", err)
		}
		id := one(`SELECT id FROM journals WHERE no = ?`, j.No)
		return id, one(`SELECT id FROM journal_lines WHERE journal_id = ? ORDER BY id LIMIT 1`, id)
	}
	str := func(f string) func(map[string]any) string {
		return func(r map[string]any) string { s, _ := r[f].(string); return s }
	}
	probes := map[string]entityProbe{
		"purchase_requests": {
			seed: func(t *testing.T, e int64, tag string) {
				prOf[e] = exec(`INSERT INTO purchase_requests (no, requester_id, department_id, vendor_id, title, status, form_id, created_at, updated_at, entity_id) VALUES (?,?,?,?,?, 'draft', 1, '2026-11-10T00:00:00Z', '2026-11-10T00:00:00Z', ?)`,
					pad(e, "PR"), admin.UserID, dept, vendorOf[e], tag, e)
			},
			query:  mcptool.Query{Limit: 50},
			marker: str("title"),
		},
		"purchase_orders": {
			seed: func(t *testing.T, e int64, tag string) {
				exec(`INSERT INTO purchase_orders (no, pr_id, vendor_id, status, total_amount, created_at, entity_id) VALUES (?,?,?, 'open', 5000, '2026-11-10T00:00:00Z', ?)`, tag, prOf[e], vendorOf[e], e)
			},
			query:  mcptool.Query{Limit: 50},
			marker: str("no"),
		},
		"items": {
			seed: func(t *testing.T, e int64, tag string) {
				if e == contract.DefaultEntityID {
					exec(`INSERT INTO items (sku, name) VALUES ('ISO-ITEM', ?)`, "격리 품목")
				}
			},
			query:  mcptool.Query{Limit: 50, Conds: []mcptool.Cond{{Field: "sku", Op: mcptool.OpPrefix, Value: "ISO-"}}},
			marker: str("sku"),
			global: true,
		},
		"stock_alerts": {
			seed: func(t *testing.T, e int64, tag string) {
				wh := exec(`INSERT INTO warehouses (code, name, entity_id, created_at, updated_at) VALUES (?, ?, ?, 't', 't')`, "WH-"+entityName[e], "창고-"+entityName[e], e)
				item := one(`SELECT COALESCE((SELECT id FROM items WHERE sku = 'STK-'||?), 0)`, entityName[e])
				if item == 0 {
					item = exec(`INSERT INTO items (sku, name) VALUES ('STK-'||?, ?)`, entityName[e], "재고 품목")
				}
				lot := exec(`INSERT INTO lots (item_id, lot_no, expiry_date) VALUES (?, ?, ?)`, item, tag, time.Now().AddDate(0, 0, 10).Format("2006-01-02"))
				exec(`INSERT INTO stock_ledger (item_id, lot_id, qty_delta, reason, actor_id, warehouse_id) VALUES (?, ?, 5, 'receipt', ?, ?)`, item, lot, admin.UserID, wh)
			},
			query:  mcptool.Query{Limit: 50, Conds: []mcptool.Cond{{Field: "kind", Op: mcptool.OpEq, Value: "expiring"}}},
			marker: str("lot_no"),
		},
		"payables": {
			seed: func(t *testing.T, e int64, tag string) {
				jid, line := journalOf(t, e)
				exec(`INSERT INTO payables (vendor_id, journal_id, line_id, trade_date, due_date, amount, entity_id) VALUES (?,?,?, '2026-11-05', '2026-12-05', 5000, ?)`, vendorOf[e], jid, line, e)
			},
			query:  mcptool.Query{Limit: 50, Sort: "outstanding", Dir: "desc"},
			marker: str("vendor_name"),
		},
		"journals": {
			seed:  func(t *testing.T, e int64, tag string) { journalOf(t, e) },
			query: mcptool.Query{Limit: 50},
			marker: func(r map[string]any) string {
				if no, _ := r["no"].(string); strings.HasPrefix(no, "ABC-") {
					return "E2"
				}
				return "E1"
			},
		},
		"tax_invoices": {
			seed: func(t *testing.T, e int64, tag string) {
				exec(`INSERT INTO tax_invoices (seq_no, no, direction, tax_type, status, approval_no, issue_date, created_by, created_at, updated_at, entity_id) VALUES (?,?, 'sale', 'taxable', 'draft', '', '2026-11-08', ?, 't', 't', ?)`,
					100+e, tag, admin.UserID, e)
			},
			query:  mcptool.Query{Limit: 50},
			marker: str("no"),
		},
		"approval_docs": {
			seed: func(t *testing.T, e int64, tag string) {
				exec(`INSERT INTO purchase_requests (no, requester_id, department_id, title, status, form_id, created_at, updated_at, entity_id) VALUES (?,?,?,?, 'draft', 2, '2026-11-10T00:00:00Z', '2026-11-10T00:00:00Z', ?)`,
					pad(e, "DOC"), admin.UserID, dept, tag, e)
			},
			query:  mcptool.Query{Limit: 50},
			marker: str("title"),
		},
	}

	cat := mcptool.BuiltinCatalog(d)
	// 표 완전성: 카탈로그의 모든 자원이 표에 있고, 표에 없는 자원 이름은 없다.
	known := map[string]bool{}
	for _, r := range cat.All() {
		known[r.Name] = true
		p, ok := probes[r.Name]
		if !ok {
			t.Fatalf("자원 %q 의 법인 격리 시험이 표에 없습니다(새 자원은 법인 거름 시험을 표에 넣어야 합니다)", r.Name)
		}
		if p.global == r.EntityOwned {
			t.Fatalf("자원 %q: 표의 global(%v)과 카탈로그의 EntityOwned(%v)가 어긋납니다", r.Name, p.global, r.EntityOwned)
		}
	}
	for name := range probes {
		if !known[name] {
			t.Fatalf("표에 카탈로그에 없는 자원 %q 가 있습니다", name)
		}
	}

	// 시드: 자원마다 법인 1·2 에 각각 한 행(품목은 법인과 무관하게 한 행). 표식은 "<자원>-E1/E2".
	tag := func(name string, e int64) string { return name + "-" + entityName[e] }
	for _, name := range []string{"purchase_requests", "purchase_orders", "items", "stock_alerts", "payables", "journals", "tax_invoices", "approval_docs"} {
		for _, e := range []int64{contract.DefaultEntityID, abc} {
			probes[name].seed(t, e, tag(name, e))
		}
	}

	for _, r := range cat.All() {
		p := probes[r.Name]
		seen := map[int64][]string{}
		for _, e := range []int64{contract.DefaultEntityID, abc} {
			rows, err := r.List(inEntity(e), p.query)
			if err != nil {
				t.Fatalf("%s(법인 %s): %v", r.Name, entityName[e], err)
			}
			for _, row := range rows {
				seen[e] = append(seen[e], p.marker(row))
			}
		}
		if p.global {
			if len(seen[contract.DefaultEntityID]) == 0 || strings.Join(seen[contract.DefaultEntityID], ",") != strings.Join(seen[abc], ",") {
				t.Errorf("%s: 법인 소유가 아닌 자원은 두 법인이 같은 결과를 봐야 합니다: %v / %v", r.Name, seen[contract.DefaultEntityID], seen[abc])
			}
			continue
		}
		for _, e := range []int64{contract.DefaultEntityID, abc} {
			own, other := tag(r.Name, e), tag(r.Name, map[int64]int64{contract.DefaultEntityID: abc, abc: contract.DefaultEntityID}[e])
			if r.Name == "payables" {
				own, other = "거래처-"+entityName[e], "거래처-"+entityName[map[int64]int64{contract.DefaultEntityID: abc, abc: contract.DefaultEntityID}[e]]
			}
			if r.Name == "journals" {
				own, other = entityName[e], map[int64]string{contract.DefaultEntityID: "E2", abc: "E1"}[e]
			}
			joined := strings.Join(seen[e], ",")
			if !strings.Contains(joined, own) {
				t.Errorf("%s: 법인 %s 의 결과에 자기 법인 데이터(%s)가 없습니다: %v", r.Name, entityName[e], own, seen[e])
			}
			for _, m := range seen[e] {
				if m == other {
					t.Errorf("%s: 법인 %s 의 결과에 다른 법인의 데이터(%s)가 있습니다: %v", r.Name, entityName[e], other, seen[e])
				}
			}
		}
	}
}
