package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/choibongseok/procure-ledger/internal/apitoken"
	"github.com/choibongseok/procure-ledger/internal/bootstrap"
	"github.com/choibongseok/procure-ledger/internal/config"
	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/deps"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/dbtest"
	"github.com/choibongseok/procure-ledger/internal/export"
	"github.com/choibongseok/procure-ledger/internal/importx"
)

// B51 6단계(명세 15절 "격리 표 시험(⑤)"): 법인 범위가 한 법인(A=MAIN)뿐인 사용자가 다른 법인(B=ABC)의 재고 자원을 읽거나 쓰면 모두 거부되고,
// 읽을 수 있는 응답에는 B 의 값이 한 건도 없다. 표의 행은 명세 문서의 표에서 읽는다: 행을 더하고 이 시험의 표에 없으면, 표에 있는데 문서에 없어도 실패한다.
//
// 값 표식: B 쪽 창고 이름 "계열창고"(코드 WH-B·WH-B2), 로트 LOT-B, 단가 7771·금액 310840, B 만의 이동 전표 번호 접두 "ABC-MV".
// 법인 이름 "계열사"는 표식이 아니다(법인 간 이동 전표는 상대 법인 이름만 남긴다).
var isoForeign = []string{"계열창고", "WH-B", "LOT-B", "7771", "7,771", "310840", "310,840", "ABC-MV", "987654", "987,654"}

type isoEnv struct {
	t   *testing.T
	ctx context.Context
	cfg config.Config
	d   deps.Deps
	h   http.Handler

	abc, whA, whA2, whB, whB2, item, lotA, lotB int64
	tokU, tokBoth, tokGroup, tokNoVal           string // A 만(재무) / 두 법인(관리자+재무) / 그룹 범위 / A 만(금액 권한 없음)
	apiA, apiB                                  string // 연동 API 토큰(A 에 묶임 / B 에 묶임)
	before                                      map[string]int
}

func (e *isoEnv) exec(q string, args ...any) int64 {
	e.t.Helper()
	var id int64
	if err := e.d.DB.QueryRowContext(e.ctx, q+` RETURNING id`, args...).Scan(&id); err != nil {
		e.t.Fatalf("시드: %v\n%s", err, q)
	}
	return id
}

func (e *isoEnv) must(q string, args ...any) {
	e.t.Helper()
	if _, err := e.d.DB.ExecContext(e.ctx, q, args...); err != nil {
		e.t.Fatalf("시드: %v\n%s", err, q)
	}
}

func (e *isoEnv) count(q string, args ...any) int {
	e.t.Helper()
	var n int
	if err := e.d.DB.QueryRowContext(e.ctx, q, args...).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

// call 은 개인 토큰(또는 API 토큰)으로 요청을 보낸다. hdr 가 비어 있지 않으면 X-Entity-ID 로 현재 법인을 지정한다.
func (e *isoEnv) call(tok, method, path, hdr, body string) (int, string) {
	e.t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, rd)
	req.Header.Set("Authorization", "Bearer "+tok)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if hdr != "" {
		req.Header.Set("X-Entity-ID", hdr)
	}
	w := httptest.NewRecorder()
	e.h.ServeHTTP(w, req)
	raw, _ := io.ReadAll(w.Body)
	return w.Code, string(raw)
}

// openingXLSX 는 기초 재고 가져오기 양식(머리글 + 한 행)을 만든다.
func (e *isoEnv) openingXLSX(sku, wh, lot, qty, cost string) []byte {
	e.t.Helper()
	tg := importx.Targets[domain.ImportOpeningStock]
	cols := make([]export.Column, len(tg.Columns))
	row := make([]export.Cell, len(tg.Columns))
	vals := map[string]string{"sku": sku, "warehouse": wh, "lot": lot, "qty": qty, "unit_cost": cost}
	for i, c := range tg.Columns {
		cols[i] = export.Column{Label: c.Header(), Kind: export.KindText}
		row[i] = vals[c.ID]
	}
	data, err := export.WriteXLSX(export.Table{Dataset: export.Dataset{Code: tg.Code, Label: tg.Label, Columns: cols}, Rows: [][]export.Cell{row}}, export.Meta{At: time.Now(), By: "시험", Filters: fmt.Sprintf("양식 버전 %d", importx.TemplateVersion)})
	if err != nil {
		e.t.Fatal(err)
	}
	return data
}

func (e *isoEnv) upload(tok, path, hdr string, file []byte) (int, string) {
	e.t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("file", "opening.xlsx")
	_, _ = fw.Write(file)
	_ = mw.WriteField("baseDate", "2026-11-05")
	_ = mw.Close()
	req := httptest.NewRequest("POST", path, &buf)
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	if hdr != "" {
		req.Header.Set("X-Entity-ID", hdr)
	}
	w := httptest.NewRecorder()
	e.h.ServeHTTP(w, req)
	return w.Code, w.Body.String()
}

func (e *isoEnv) noForeign(label, body string) {
	e.t.Helper()
	for _, f := range isoForeign {
		if strings.Contains(body, f) {
			e.t.Errorf("%s: 다른 법인 값 %q 이 응답에 있다\n%s", label, f, cut(body))
		}
	}
}

func cut(s string) string {
	if len(s) > 600 {
		return s[:600] + "..."
	}
	return s
}

func rejected(code int) bool { return code == 400 || code == 403 || code == 404 }

// read 는 읽기 한 줄의 표 시험이다: A 만 보는 사용자는 (1) 기본·MAIN 지정에서 A 값(wantA)만 받고, (2) ABC 지정은 거부되고,
// (3) 두 법인을 보는 사용자가 MAIN 을 고르면 역시 B 값이 없고, ABC 를 고르면 B 값(wantB)이 보인다(시험이 비어 있지 않다는 대조).
func (e *isoEnv) read(path, wantA, wantB string) { e.readOpt(path, wantA, wantB, false) }

// readOpt 의 bothMayShowB 는 두 법인을 보는 사용자가 MAIN 을 골라도 B 쪽 이름이 보이는 것이 맞는 자원(양쪽 법인이 걸친 이동 전표)이다.
func (e *isoEnv) readOpt(path, wantA, wantB string, bothMayShowB bool) {
	e.t.Helper()
	for _, hdr := range []string{"", "MAIN"} {
		code, body := e.call(e.tokU, "GET", path, hdr, "")
		if code != 200 || (wantA != "" && !strings.Contains(body, wantA)) {
			e.t.Errorf("GET %s (A 전용, 법인=%q): %d, A 값 %q 가 없다\n%s", path, hdr, code, wantA, cut(body))
		}
		e.noForeign("GET "+path+" (A 전용, 법인="+hdr+")", body)
	}
	code, body := e.call(e.tokU, "GET", path, "ABC", "")
	if !rejected(code) {
		e.t.Errorf("GET %s (A 전용, 법인=ABC): %d(기대 400·403·404)\n%s", path, code, cut(body))
	}
	e.noForeign("GET "+path+" (A 전용, 법인=ABC)", body)
	code, body = e.call(e.tokBoth, "GET", path, "MAIN", "")
	if code != 200 {
		e.t.Errorf("GET %s (두 법인, 법인=MAIN): %d\n%s", path, code, cut(body))
	}
	if !bothMayShowB {
		e.noForeign("GET "+path+" (두 법인, 법인=MAIN)", body)
	}
	code, body = e.call(e.tokBoth, "GET", path, "ABC", "")
	if code != 200 || (wantB != "" && !strings.Contains(body, wantB)) {
		e.t.Errorf("GET %s (두 법인, 법인=ABC): %d, B 값 %q 가 없다(대조 실패)\n%s", path, code, wantB, cut(body))
	}
}

// denyWrite 는 A 만 보는 사용자의 쓰기가 거부됨을 본다(성공하면 실패). 상태가 실제로 안 바뀐 것은 마지막에 건수로 확인한다.
func (e *isoEnv) denyWrite(label, method, path, hdr, body string) {
	e.t.Helper()
	code, resp := e.call(e.tokU, method, path, hdr, body)
	if !rejected(code) {
		e.t.Errorf("%s: %s %s (법인=%q) → %d(기대 400·403·404)\n%s", label, method, path, hdr, code, cut(resp))
	}
	e.noForeign(label, resp)
}

func (e *isoEnv) snapshot() map[string]int {
	return map[string]int{
		"stock_ledger":    e.count(`SELECT COUNT(*) FROM stock_ledger`),
		"stock_transfers": e.count(`SELECT COUNT(*) FROM stock_transfers`),
		"stock_valuation": e.count(`SELECT COUNT(*) FROM stock_valuation`),
		"month_closes":    e.count(`SELECT COUNT(*) FROM month_closes`),
		"month_close_ev":  e.count(`SELECT COUNT(*) FROM month_close_events`),
		"journals":        e.count(`SELECT COUNT(*) FROM journals`),
	}
}

func newIsoEnv(t *testing.T) *isoEnv {
	t.Helper()
	dbtest.SetEnv(t)
	t.Setenv("PL_DEV_IDP", "1")
	t.Setenv("PL_LLM_PROVIDER", "mock")
	cfg := config.Load()
	ctx := context.Background()
	d, closeDB, err := bootstrap.Build(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(closeDB)
	e := &isoEnv{t: t, ctx: ctx, cfg: cfg, d: d}
	e.abc = e.exec(`INSERT INTO entities (code, name, is_default, active, version, created_at, updated_at) VALUES ('ABC', '계열사', 0, 1, 1, 'x', 'x')`)
	e.whA = e.exec(`INSERT INTO warehouses (code, name, entity_id, created_at, updated_at) VALUES ('WH-A', '본사창고', 1, 't', 't')`)
	e.whA2 = e.exec(`INSERT INTO warehouses (code, name, entity_id, created_at, updated_at) VALUES ('WH-A2', '본사보조창고', 1, 't', 't')`)
	e.whB = e.exec(`INSERT INTO warehouses (code, name, entity_id, created_at, updated_at) VALUES ('WH-B', '계열창고', ?, 't', 't')`, e.abc)
	e.whB2 = e.exec(`INSERT INTO warehouses (code, name, entity_id, created_at, updated_at) VALUES ('WH-B2', '계열보조창고', ?, 't', 't')`, e.abc)
	e.item = e.exec(`INSERT INTO items (sku, name, safety_stock, track_lot) VALUES ('B51-ISO', 'B51 격리 품목', 60, 1)`)
	e.lotA = e.exec(`INSERT INTO lots (item_id, lot_no) VALUES (?, 'LOT-A')`, e.item)
	e.lotB = e.exec(`INSERT INTO lots (item_id, lot_no) VALUES (?, 'LOT-B')`, e.item)
	var dept, adminID int64
	if err := d.DB.QueryRowContext(ctx, `SELECT id, department_id FROM users WHERE email = 'admin@pl.test'`).Scan(&adminID, &dept); err != nil {
		t.Fatal(err)
	}
	led := func(lot, wh, qty int64, ent, unit int64) {
		id := e.exec(`INSERT INTO stock_ledger (item_id, lot_id, qty_delta, reason, actor_id, warehouse_id, book_date) VALUES (?, ?, ?, 'receipt', ?, ?, '2026-10-05')`, e.item, lot, qty, adminID, wh)
		e.must(`INSERT INTO stock_valuation (ledger_id, item_id, book_date, in_unit_cost, unit_cost, amount, bal_qty, bal_amount, avg_cost, warned, entity_id, entity_basis) VALUES (?, ?, '2026-10-05', ?, ?, ?, ?, ?, ?, 0, ?, 1)`,
			id, e.item, unit, unit, qty*unit, qty, qty*unit, unit, ent)
	}
	led(e.lotA, e.whA, 50, 1, 100)
	led(e.lotB, e.whB, 40, e.abc, 7771)                                                          // 금액 310,840
	for _, c := range []struct{ ent, wh, amt int64 }{{1, e.whA, 5000}, {e.abc, e.whB, 310840}} { // 마감된 2026-10
		e.exec(`INSERT INTO month_close_lines (month, version, item_id, warehouse_id, open_qty, open_amount, in_qty, in_amount, out_qty, out_amount, transfer_qty, transfer_amount, adjust_qty, adjust_amount, close_qty, close_amount, avg_cost, entity_id)
			VALUES ('2026-10', 1, ?, ?, 0,0,0,0,0,0,0,0,0,0, 1, ?, 1, ?)`, e.item, c.wh, c.amt, c.ent)
		e.must(`INSERT INTO month_closes (month, status, version, closed_by, closed_at, entity_id) VALUES ('2026-10', 'closed', 1, ?, '2026-11-01T00:00:00Z', ?)`, adminID, c.ent)
	}
	mkUser := func(email string, grants []struct {
		role  string
		scope string
		ent   int64
	}) string {
		id := e.exec(`INSERT INTO users (email, name, role, department_id) VALUES (?, ?, 'staff', ?)`, email, email, dept)
		for _, g := range grants {
			if g.scope == contract.ScopeGroup {
				e.exec(`INSERT INTO role_grants (role_id, target_type, target_id, source, created_at, scope) SELECT id, 'user', ?, 'manual', 't', 'group' FROM roles WHERE key = ?`, id, g.role)
				continue
			}
			e.exec(`INSERT INTO role_grants (role_id, target_type, target_id, source, created_at, scope, scope_entity_id) SELECT id, 'user', ?, 'manual', 't', 'entity', ? FROM roles WHERE key = ?`, id, g.ent, g.role)
		}
		tok, _, err := d.Tokens.Issue(ctx, contract.Principal{UserID: id, Role: contract.RoleStaff, Name: email, DepartmentID: dept}, "B51 격리 시험", 0)
		if err != nil {
			t.Fatal(err)
		}
		return tok
	}
	type g = struct {
		role  string
		scope string
		ent   int64
	}
	e.tokU = mkUser("iso-only-a@pl.test", []g{{"finance", "entity", 1}, {"staff", "entity", 1}})
	e.tokBoth = mkUser("iso-both@pl.test", []g{{"admin", "entity", 1}, {"admin", "entity", e.abc}, {"finance", "entity", 1}, {"finance", "entity", e.abc}})
	e.tokNoVal = mkUser("iso-noval@pl.test", []g{{"staff", "entity", 1}})
	e.tokGroup = mkUser("iso-group@pl.test", []g{{"admin", "group", 0}, {"finance", "group", 0}})
	// 내보내기 속도 제한(사용자당 분당 5회)은 이 시험의 대상이 아니다: 호출마다 시각이 2분 지나가게 해 막히지 않게 한다.
	tick := time.Now()
	d.Export.Limiter = export.NewLimiter(func() time.Time { tick = tick.Add(2 * time.Minute); return tick })
	e.h = handlerFor(buildRoutes(cfg, d), d)

	// 연동 API 토큰: A 에 묶인 것과 B 에 묶인 것(발급자 범위가 그 법인에 있어야 발급된다).
	issuer := func(ents ...int64) contract.Principal {
		set := contract.NewEntitySet(ents...)
		return contract.Principal{UserID: adminID, Role: contract.RoleAdmin, Name: "iso", DepartmentID: dept, OwnEntities: ents,
			EntityPerms: map[contract.Permission]contract.EntitySet{contract.PermAPIManage: set, contract.PermInventoryRead: set}}
	}
	if err := d.APITokens.SetEnabled(ctx, issuer(1, e.abc), true); err != nil {
		t.Fatal(err)
	}
	if e.apiA, _, err = d.APITokens.Issue(ctx, issuer(1), apitoken.IssueInput{Name: "A 재고", Scopes: []string{"items:read", "changes:read"}, EntityID: 1}); err != nil {
		t.Fatal(err)
	}
	if e.apiB, _, err = d.APITokens.Issue(ctx, issuer(1, e.abc), apitoken.IssueInput{Name: "B 재고", Scopes: []string{"items:read", "changes:read"}, EntityID: e.abc}); err != nil {
		t.Fatal(err)
	}

	// 법인 간 이동·B 안의 이동은 두 법인에 권한이 있는 사용자가 서비스 경로(HTTP)로 만든다(원장·평가·동결 금액이 실제 코드로 생긴다).
	lines := func(lot, qty int64) string {
		return fmt.Sprintf(`"lines":[{"itemId":%d,"lotId":%d,"qty":%d}]`, e.item, lot, qty)
	}
	if code, body := e.call(e.tokBoth, "POST", "/api/stock-transfers", "MAIN", fmt.Sprintf(`{"fromId":%d,"toId":%d,"moveDate":"2026-11-10",%s}`, e.whA, e.whB, lines(e.lotA, 2))); code != 201 {
		t.Fatalf("법인 간 이동 시드(A→B): %d %s", code, body)
	}
	if code, body := e.call(e.tokBoth, "POST", "/api/stock-transfers", "ABC", fmt.Sprintf(`{"fromId":%d,"toId":%d,"moveDate":"2026-11-11",%s}`, e.whB, e.whB2, lines(e.lotB, 3))); code != 201 {
		t.Fatalf("B 안의 이동 시드: %d %s", code, body)
	}
	e.before = e.snapshot()
	return e
}

// isoRows 는 명세 15절 격리 표의 행 이름(첫 열)을 문서에서 읽는다.
func isoRowsFromSpec(t *testing.T) []string {
	t.Helper()
	raw, err := os.ReadFile("../../해당 기능 명세")
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	i := strings.Index(s, "**격리 표 시험(⑤)**")
	if i < 0 {
		t.Fatal("명세에서 격리 표를 찾지 못했다")
	}
	s = s[i:]
	if j := strings.Index(s, "**화면·문구·방언**"); j > 0 {
		s = s[:j]
	}
	var rows []string
	for _, ln := range strings.Split(s, "\n") {
		if !strings.HasPrefix(ln, "| ") || strings.HasPrefix(ln, "| 자원") || strings.HasPrefix(ln, "|---") {
			continue
		}
		cells := strings.Split(ln, "|")
		if len(cells) > 2 {
			rows = append(rows, strings.TrimSpace(cells[1]))
		}
	}
	return rows
}

type isoCheck func(e *isoEnv)

func isoTable() map[string]isoCheck {
	return map[string]isoCheck{
		"품목 평균 단가·재고 금액": func(e *isoEnv) {
			e.read(fmt.Sprintf("/api/items/%d/stock", e.item), "LOT-A", "LOT-B")
			e.read("/api/stock/below-safety", "B51-ISO", "")
		},
		"수불부(JSON·내보내기·금액 포함)": func(e *isoEnv) {
			q := fmt.Sprintf("itemId=%d&from=2026-09-01&to=2026-11-30", e.item)
			e.read("/api/stock-ledger?"+q, "본사창고", "계열창고")
			e.read("/api/stock-ledger/export?format=csv&"+q, "본사창고", "계열창고")
			e.read("/api/stock-ledger/export-valued?format=csv&"+q, "본사창고", "계열창고")
		},
		"월별 재고 현황(마감·잠정, 내보내기 2종)": func(e *isoEnv) {
			for _, m := range []string{"2026-10", "2026-11"} {
				wantB := "계열창고"
				e.read("/api/inventory/month-stock?month="+m, "본사창고", wantB)
				e.read("/api/inventory/month-stock/export?format=csv&month="+m, "본사창고", wantB)
				e.read("/api/inventory/month-stock/export-valued?format=csv&month="+m, "본사창고", wantB)
			}
		},
		"월 마감 목록·점검·상태": func(e *isoEnv) {
			e.read("/api/inventory/months", "2026-10", "2026-10")
			e.read("/api/inventory/months/2026-10/status", "5000", "310840")
			e.read("/api/inventory/months/2026-11/precheck", "2026-11", "2026-11")
			e.denyWrite("B 마감 해제 요청", "POST", "/api/inventory/months/2026-10/release-request", "ABC", `{"reason":"격리 시험"}`)
		},
		"월 마감 실행": func(e *isoEnv) {
			e.denyWrite("B 마감", "POST", "/api/inventory/months/2026-11/close", "ABC", `{}`)
			if n := e.count(`SELECT COUNT(*) FROM month_closes WHERE entity_id = ? AND month = '2026-11'`, e.abc); n != 0 {
				e.t.Errorf("A 만 보는 사용자가 B 의 2026-11 을 마감했다")
			}
		},
		"이동 목록": func(e *isoEnv) {
			// A 가 출발 또는 도착인 전표만: A→B 전표는 보이되 상대 창고 이름은 없고, B 안의 이동(ABC-MV)은 목록에 없다.
			e.readOpt("/api/stock-transfers", "MV-", "ABC-MV", true)
			e.denyWrite("B 창고 간 이동", "POST", "/api/stock-transfers", "ABC", fmt.Sprintf(`{"fromId":%d,"toId":%d,"moveDate":"2026-11-12","lines":[{"itemId":%d,"lotId":%d,"qty":1}]}`, e.whB, e.whB2, e.item, e.lotB))
			e.denyWrite("B 창고 간 이동(법인 지정 없이)", "POST", "/api/stock-transfers", "", fmt.Sprintf(`{"fromId":%d,"toId":%d,"moveDate":"2026-11-12","lines":[{"itemId":%d,"lotId":%d,"qty":1}]}`, e.whB, e.whB2, e.item, e.lotB))
			e.denyWrite("B 창고 간 이동 미리보기", "POST", "/api/stock-transfers/preview", "ABC", fmt.Sprintf(`{"fromId":%d,"toId":%d,"moveDate":"2026-11-12","lines":[{"itemId":%d,"lotId":%d,"qty":1}]}`, e.whB, e.whB2, e.item, e.lotB))
		},
		"법인 간 이동": func(e *isoEnv) {
			body := func(from, to, lot int64) string {
				return fmt.Sprintf(`{"fromId":%d,"toId":%d,"moveDate":"2026-11-12","lines":[{"itemId":%d,"lotId":%d,"qty":1}]}`, from, to, e.item, lot)
			}
			e.denyWrite("A→B 이동(B 권한 없음)", "POST", "/api/stock-transfers", "", body(e.whA, e.whB, e.lotA))
			e.denyWrite("A→B 이동(MAIN 지정)", "POST", "/api/stock-transfers", "MAIN", body(e.whA, e.whB, e.lotA))
			e.denyWrite("B→A 이동", "POST", "/api/stock-transfers", "", body(e.whB, e.whA, e.lotB))
			e.denyWrite("A→B 이동 미리보기", "POST", "/api/stock-transfers/preview", "", body(e.whA, e.whB, e.lotA))
			// 도착 창고 후보에 B 창고가 없다(양쪽 권한이 있는 사용자는 보인다).
			code, resp := e.call(e.tokU, "GET", "/api/stock-transfers?targets=1", "", "")
			if code != 200 {
				e.t.Errorf("후보 조회: %d %s", code, resp)
			}
			e.noForeign("도착 창고 후보(A 전용)", resp)
			if _, resp = e.call(e.tokBoth, "GET", "/api/stock-transfers?targets=1", "MAIN", ""); !strings.Contains(resp, "계열창고") {
				e.t.Errorf("도착 창고 후보 대조(두 법인): %s", cut(resp))
			}
		},
		"가져오기 대조(재고)": func(e *isoEnv) {
			e.read("/api/imports/reconcile", "stock", "stock")
			e.read("/api/imports/reconcile/export?format=csv", "", "")
			// 재고 수량·금액은 현재 법인 기준이다: A 는 수량 48, B 는 40+2=42(A→B 이동 2 반영, B 안의 이동은 합이 같다).
			stock := func(tok, ent string) (qty, amt int64) {
				code, body := e.call(tok, "GET", "/api/imports/reconcile", ent, "")
				if code != 200 {
					e.t.Fatalf("대조 %s: %d %s", ent, code, body)
				}
				var rep struct {
					Rows []struct {
						Code   string `json:"code"`
						System int64  `json:"system"`
					} `json:"rows"`
				}
				if err := json.Unmarshal([]byte(body), &rep); err != nil {
					e.t.Fatal(err)
				}
				for _, r := range rep.Rows {
					switch r.Code {
					case "stock_qty":
						qty = r.System
					case "stock_amount":
						amt = r.System
					}
				}
				return
			}
			qa, _ := stock(e.tokU, "")
			qb, _ := stock(e.tokBoth, "ABC")
			if qa != 48 || qb != 42 {
				e.t.Errorf("대조의 재고 수량이 법인 기준이 아니다: A=%d(기대 48) B=%d(기대 42)", qa, qb)
			}
			// B 기초 재고 쓰기: A 만 보는 사용자가 B 창고 코드로 기초 재고를 올리면 오류 행이고(없는 창고와 같다), 법인을 ABC 로 지정하면 요청 자체가 거부된다.
			xl := e.openingXLSX("B51-ISO", "WH-B", "LOT-B", "5", "7771")
			code, resp := e.upload(e.tokU, "/api/imports/opening_stock/preview", "", xl)
			// 응답은 호출자가 올린 행의 키를 되돌려 줄 뿐이고, 이유는 없는 창고 코드와 같아야 한다(다른 법인 창고의 존재를 알리지 않는다).
			_, unknown := e.upload(e.tokU, "/api/imports/opening_stock/preview", "", e.openingXLSX("B51-ISO", "WH-ZZ", "LOT-B", "5", "7771"))
			reason := func(s string) string {
				var out struct {
					Rows []struct{ Verdict, Reason string } `json:"rows"`
				}
				_ = json.Unmarshal([]byte(s), &out)
				if len(out.Rows) != 1 {
					return "(행 없음) " + cut(s)
				}
				return out.Rows[0].Verdict + ":" + out.Rows[0].Reason
			}
			if code != 200 || !strings.HasPrefix(reason(resp), "error:") || reason(resp) != reason(unknown) {
				e.t.Errorf("B 창고로의 기초 재고 미리보기가 A 에서 통과했거나 이유가 다르다: %d %q vs %q", code, reason(resp), reason(unknown))
			}
			if code, resp = e.upload(e.tokU, "/api/imports/opening_stock/preview", "ABC", xl); !rejected(code) {
				e.t.Errorf("B 기초 재고 미리보기(법인=ABC): %d(기대 거부) %s", code, cut(resp))
			}
			// 대조: 두 법인을 보는 사용자가 ABC 에서 같은 파일을 올리면 통과한다(시험이 비어 있지 않다는 확인).
			if code, resp = e.upload(e.tokBoth, "/api/imports/opening_stock/preview", "ABC", xl); code != 200 || !strings.Contains(resp, `"add"`) {
				e.t.Errorf("대조(두 법인, ABC): %d %s", code, cut(resp))
			}
		},
		"재고 목록 내보내기": func(e *isoEnv) {
			e.read("/api/stock/export?format=csv", "B51-ISO", "계열창고")
			// 수량: A 는 48(50-2), B 는 로트별 2·37·3. 합산이나 B 수량이 A 파일에 있으면 안 된다.
			_, a := e.call(e.tokU, "GET", "/api/stock/export?format=csv", "", "")
			_, b := e.call(e.tokBoth, "GET", "/api/stock/export?format=csv", "ABC", "")
			if !strings.Contains(a, ",48,") || strings.Contains(a, ",37,") || strings.Contains(a, "계열") {
				e.t.Errorf("A 재고 파일 수량: %s", cut(a))
			}
			if !strings.Contains(b, ",37,") || strings.Contains(b, ",48,") || strings.Contains(b, "본사") {
				e.t.Errorf("B 재고 파일 수량(대조): %s", cut(b))
			}
		},
		"MCP 5개 도구": func(e *isoEnv) {
			call := func(tok, tool string, args map[string]any) (string, bool) {
				body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": tool, "arguments": args}})
				req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(body))
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("Accept", "application/json, text/event-stream")
				req.Header.Set("Authorization", "Bearer "+tok)
				w := httptest.NewRecorder()
				e.h.ServeHTTP(w, req)
				s := w.Body.String()
				if i := strings.Index(s, "data: "); i >= 0 {
					s = s[i+6:]
				}
				var env struct {
					Result struct {
						IsError bool `json:"isError"`
						Content []struct {
							Text string `json:"text"`
						} `json:"content"`
					} `json:"result"`
					Error any `json:"error"`
				}
				if err := json.Unmarshal([]byte(strings.TrimSpace(s)), &env); err != nil {
					e.t.Fatalf("%s 응답 해석: %v\n%s", tool, err, s)
				}
				var sb strings.Builder
				for _, c := range env.Result.Content {
					sb.WriteString(c.Text)
				}
				return sb.String(), env.Result.IsError || env.Error != nil
			}
			ledger := map[string]any{"item_id": e.item, "from": "2026-09-01", "to": "2026-11-30"}
			tools := []struct {
				name string
				args map[string]any
			}{
				{"get_stock", map[string]any{"item_id": e.item}},
				{"list_stock_alerts", map[string]any{}},
				{"get_item_ledger", ledger},
				{"get_month_close_status", map[string]any{"month": "2026-10"}},
				{"draft_reorder", map[string]any{"item_ids": []int64{e.item}}},
			}
			var refusal string
			for _, tl := range tools {
				txt, isErr := call(e.tokU, tl.name, tl.args)
				if isErr && tl.name != "draft_reorder" {
					e.t.Errorf("%s (A 전용): 오류 %s", tl.name, txt)
				}
				e.noForeign("MCP "+tl.name+" (A 전용)", txt)
				withB := map[string]any{"entity": "ABC"}
				for k, v := range tl.args {
					withB[k] = v
				}
				txt, isErr = call(e.tokU, tl.name, withB)
				if !isErr || !strings.Contains(txt, "찾을 수 없는 법인") {
					e.t.Errorf("MCP %s entity=ABC (A 전용): 거부가 아니다 %v %s", tl.name, isErr, txt)
				}
				e.noForeign("MCP "+tl.name+" entity=ABC (A 전용)", txt)
				if refusal == "" {
					refusal = txt
				} else if txt != refusal {
					e.t.Errorf("MCP %s: 거부 문구가 다른 도구와 다르다 %q != %q", tl.name, txt, refusal)
				}
			}
			// 대조: 두 법인을 보는 사용자는 entity=ABC 로 B 값을 받는다.
			if txt, isErr := call(e.tokBoth, "get_stock", map[string]any{"item_id": e.item, "entity": "ABC"}); isErr || !strings.Contains(txt, "LOT-B") {
				e.t.Errorf("MCP 대조(두 법인, ABC): %v %s", isErr, txt)
			}
		},
		"API v1 품목 재고": func(e *isoEnv) {
			itemOnHand := func(tok string) (int64, string) {
				code, body := e.call(tok, "GET", "/api/v1/items", "", "")
				if code != 200 {
					e.t.Fatalf("/api/v1/items: %d %s", code, body)
				}
				var out struct {
					Data []struct {
						SKU    string `json:"sku"`
						OnHand int64  `json:"on_hand"`
					} `json:"data"`
				}
				if err := json.Unmarshal([]byte(body), &out); err != nil {
					e.t.Fatal(err)
				}
				for _, it := range out.Data {
					if it.SKU == "B51-ISO" {
						return it.OnHand, body
					}
				}
				e.t.Fatalf("품목이 없다: %s", cut(body))
				return 0, ""
			}
			a, body := itemOnHand(e.apiA)
			e.noForeign("API v1 품목(A 토큰)", body)
			b, _ := itemOnHand(e.apiB)
			if a != 48 || b != 42 {
				e.t.Errorf("API v1 재고 수량은 토큰이 묶인 법인 기준이어야 한다: A=%d(기대 48) B=%d(기대 42)", a, b)
			}
			// 헤더로 다른 법인을 지정해도 토큰의 법인이 우선한다.
			if a2, _ := itemOnHand(e.apiA); a2 != a {
				e.t.Errorf("API v1: %d != %d", a2, a)
			}
			code, resp := e.call(e.apiA, "GET", "/api/v1/items", "ABC", "")
			if code == 200 {
				var out struct {
					Data []struct {
						OnHand int64 `json:"on_hand"`
					} `json:"data"`
				}
				_ = json.Unmarshal([]byte(resp), &out)
				for _, it := range out.Data {
					if it.OnHand == 42 {
						e.t.Errorf("API v1: 헤더로 다른 법인의 수량을 읽었다 %s", cut(resp))
					}
				}
			}
			// 발급 단계의 상한은 apitoken 시험이 지킨다(발급자 범위 밖 법인 토큰 거부).
		},
		"변경 피드(품목 재고 변경)": func(e *isoEnv) {
			ids := func(tok string) map[int64]int {
				code, body := e.call(tok, "GET", "/api/v1/changes?cursor=c1.0&resource=item", "", "")
				if code != 200 {
					e.t.Fatalf("변경 피드: %d %s", code, body)
				}
				var out struct {
					Data []struct {
						Seq string `json:"seq_str"`
						ID  int64  `json:"id"`
					} `json:"data"`
				}
				if err := json.Unmarshal([]byte(body), &out); err != nil {
					e.t.Fatal(err)
				}
				m := map[int64]int{}
				for _, c := range out.Data {
					m[c.ID]++
				}
				return m
			}
			total := func(m map[int64]int) (n int) {
				for _, v := range m {
					n += v
				}
				return
			}
			wantA := e.count(`SELECT COUNT(*) FROM change_events WHERE resource = 'item' AND entity_id = 1`)
			wantB := e.count(`SELECT COUNT(*) FROM change_events WHERE resource = 'item' AND entity_id = ?`, e.abc)
			gotA, gotB := total(ids(e.apiA)), total(ids(e.apiB))
			if gotA != wantA || gotB != wantB || wantB == 0 {
				e.t.Errorf("변경 피드의 법인 거름: A 토큰 %d건(기대 %d) B 토큰 %d건(기대 %d)", gotA, wantA, gotB, wantB)
			}
			// 재고를 바꾼 원장 행의 법인(창고 법인)대로 기록된다: A→B 이동은 A 쪽 1건(출고)·B 쪽 1건(입고), B 안의 이동은 B 쪽 2건.
			if wantA != 1 || wantB != 3 {
				e.t.Errorf("재고 변경 이벤트가 원장 행의 법인과 다르다: A=%d(기대 1) B=%d(기대 3)", wantA, wantB)
			}
		},
	}
}

func TestStockIsolationTableB51(t *testing.T) {
	e := newIsoEnv(t)
	table := isoTable()
	var names []string
	for n := range table {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		t.Run(n, func(t *testing.T) {
			e.t = t
			table[n](e)
		})
	}
	e.t = t
	// 거부된 쓰기는 아무것도 바꾸지 않았다(원장·평가·이동 전표·마감·전표 행 수 불변).
	after := e.snapshot()
	for k, v := range e.before {
		if after[k] != v {
			t.Errorf("거부된 쓰기가 %s 를 바꿨다: %d → %d", k, v, after[k])
		}
	}
}

// B51 6단계(명세 8.2·AC-5140·AC-5141): 재고 내보내기 5종의 entity 매개변수 표 구동 시험(B44 12절 규칙).
// 빈 값 = 현재 법인, 코드 = 그 권한이 그 법인에서 효력일 때만, "*" = 그룹 범위일 때만. 모르는 코드는 400, 범위 밖 코드와 "*" 는 403, 모두 감사 denied.
func TestStockExportEntityParamTableB51(t *testing.T) {
	const (
		msgBad       = "조건이 올바르지 않습니다. 화면의 조건을 확인한 뒤 다시 내보내세요."
		msgForbidden = "이 자료를 내보낼 권한이 없습니다. 필요하면 관리자에게 권한을 요청하세요."
	)
	e := newIsoEnv(t)
	q := fmt.Sprintf("format=csv&itemId=%d&from=2026-09-01&to=2026-11-30&month=2026-10", e.item)
	routes := []struct {
		path   string
		valued bool // 금액 포함(inventory:valuation_read 필요)
	}{
		{"/api/stock/export", false},
		{"/api/stock-ledger/export", false},
		{"/api/stock-ledger/export-valued", true},
		{"/api/inventory/month-stock/export", false},
		{"/api/inventory/month-stock/export-valued", true},
	}
	denied := func() int {
		return e.count(`SELECT COUNT(*) FROM audit_logs WHERE action = 'export.run' AND result = 'denied'`)
	}
	type cs struct {
		who    string
		tok    func() string
		entity string
		want   int
		msg    string // 본문에 있어야 하는 문구(거부일 때)
		wantA  bool   // 본문에 A 창고 값
		wantB  bool   // 본문에 B 창고 값
	}
	for _, r := range routes {
		r := r
		t.Run(r.path, func(t *testing.T) {
			e.t = t
			cases := []cs{
				{"A 전용", func() string { return e.tokU }, "", 200, "", true, false},
				{"A 전용", func() string { return e.tokU }, "MAIN", 200, "", true, false},
				{"A 전용(소문자 코드)", func() string { return e.tokU }, "main", 200, "", true, false},
				{"A 전용", func() string { return e.tokU }, "ABC", 403, msgForbidden, false, false},
				{"A 전용", func() string { return e.tokU }, "*", 403, msgForbidden, false, false},
				{"A 전용", func() string { return e.tokU }, "NOPE", 400, msgBad, false, false},
				{"A 전용", func() string { return e.tokU }, "A B", 400, msgBad, false, false},
				{"두 법인", func() string { return e.tokBoth }, "ABC", 200, "", false, true},
				{"두 법인", func() string { return e.tokBoth }, "MAIN", 200, "", true, false},
				{"두 법인", func() string { return e.tokBoth }, "*", 403, msgForbidden, false, false},
				{"두 법인", func() string { return e.tokBoth }, "NOPE", 400, msgBad, false, false},
				{"그룹 범위", func() string { return e.tokGroup }, "*", 200, "", true, true},
				{"그룹 범위", func() string { return e.tokGroup }, "ABC", 200, "", false, true},
				{"그룹 범위", func() string { return e.tokGroup }, "NOPE", 400, msgBad, false, false},
			}
			if r.valued { // 금액 권한이 없으면 금액 포함 파일은 아예 받지 못한다(법인과 별개 규칙).
				// 라우트 권한 거부는 내보내기 본체에 닿기 전이라 감사는 access.denied 다.
				if code, body := e.call(e.tokNoVal, "GET", r.path+"?"+q, "", ""); code != 403 {
					t.Errorf("%s (금액 권한 없음): %d(기대 403)\n%s", r.path, code, cut(body))
				}
			}
			for _, c := range cases {
				before := denied()
				code, body := e.call(c.tok(), "GET", r.path+"?"+q+"&entity="+url.QueryEscape(c.entity), "", "")
				label := fmt.Sprintf("%s %s?entity=%q (%s)", "GET", r.path, c.entity, c.who)
				if code != c.want {
					t.Errorf("%s: %d(기대 %d)\n%s", label, code, c.want, cut(body))
					continue
				}
				if c.want != 200 {
					if !strings.Contains(body, c.msg) {
						t.Errorf("%s: 문구가 다르다 %q", label, cut(body))
					}
					if got := denied(); got != before+1 {
						t.Errorf("%s: 감사 denied 가 %d → %d(1건 늘어야 한다)", label, before, got)
					}
					e.noForeign(label, body)
					continue
				}
				if got := denied(); got != before {
					t.Errorf("%s: 성공인데 감사 denied 가 늘었다", label)
				}
				hasA, hasB := strings.Contains(body, "본사창고"), strings.Contains(body, "계열창고")
				if r.path == "/api/stock/export" || strings.Contains(r.path, "month-stock") || strings.Contains(r.path, "stock-ledger") {
					if hasA != c.wantA || hasB != c.wantB {
						t.Errorf("%s: 본사창고=%v(기대 %v) 계열창고=%v(기대 %v)\n%s", label, hasA, c.wantA, hasB, c.wantB, cut(body))
					}
				}
				if !c.wantB {
					e.noForeign(label, body)
				}
				// 전체 모드(D14): 월별 재고 현황 파일은 법인 열이 붙고 평균 단가 칸이 비어 있다.
				if c.entity == "*" && strings.Contains(r.path, "month-stock") {
					first := strings.SplitN(strings.TrimPrefix(body, "\ufeff"), "\n", 2)[0]
					if !strings.Contains(first, "법인") {
						t.Errorf("%s: 전체 모드 파일에 법인 열이 없다: %s", label, first)
					}
					if !strings.Contains(body, "본사창고") || !strings.Contains(body, "계열창고") {
						t.Errorf("%s: 전체 모드 파일에 두 법인 줄이 모두 있어야 한다", label)
					}
				}
				// 법인 한 곳을 고른 월별 파일에는 법인 열이 없다(법인이 둘 이상인 설치의 정보 시트에만 법인 이름).
				if c.entity != "*" && strings.Contains(r.path, "month-stock") {
					first := strings.SplitN(strings.TrimPrefix(body, "\ufeff"), "\n", 2)[0]
					if strings.HasPrefix(first, "법인") {
						t.Errorf("%s: 법인을 하나 고른 파일에 법인 열이 붙었다: %s", label, first)
					}
				}
			}
		})
	}
	e.t = t
	// 금액 권한이 없는 사용자의 수량 파일에는 금액 열이 없다(AC-5141).
	for _, p := range []string{"/api/stock-ledger/export", "/api/inventory/month-stock/export"} {
		code, body := e.call(e.tokNoVal, "GET", p+"?"+q, "", "")
		if code != 200 {
			t.Errorf("%s (금액 권한 없음): %d", p, code)
			continue
		}
		first := strings.SplitN(strings.TrimPrefix(body, "\ufeff"), "\n", 2)[0]
		for _, bad := range []string{"금액", "단가"} {
			if strings.Contains(first, bad) {
				t.Errorf("%s: 금액 권한이 없는데 머리글에 %q 가 있다: %s", p, bad, first)
			}
		}
	}
}

// B51 판정 후속: 수불부의 기초·합계·기말·행 잔량이 행과 같은 창고 법인 조건으로 읽힌다.
// 두 법인이 같은 품목을 날짜를 달리해 입고해 기초가 0 이 아니게 하고(A 7·B 10, 둘 다 조회 시작일 이전), 값을 정확히 비교한다.
// 행만 거르고 합계를 안 거르던 결함이 되돌아오면(기초 17 등 섞인 값) 실패한다.
func TestStockLedgerTotalsEntityB51(t *testing.T) {
	e := newIsoEnv(t)
	var adminID int64
	if err := e.d.DB.QueryRowContext(e.ctx, `SELECT id FROM users WHERE email = 'admin@pl.test'`).Scan(&adminID); err != nil {
		t.Fatal(err)
	}
	for _, r := range []struct{ lot, wh, qty int64 }{{e.lotA, e.whA, 7}, {e.lotB, e.whB, 10}} {
		e.must(`INSERT INTO stock_ledger (item_id, lot_id, qty_delta, reason, actor_id, warehouse_id, book_date) VALUES (?, ?, ?, 'receipt', ?, ?, '2026-09-03')`, e.item, r.lot, r.qty, adminID, r.wh)
	}
	type page struct {
		Opening, TotalIn, TotalOut, Closing int64
		Total                               int
		Rows                                []struct{ Balance int64 }
	}
	get := func(tok, ent string) page {
		t.Helper()
		code, body := e.call(tok, "GET", fmt.Sprintf("/api/stock-ledger?itemId=%d&from=2026-10-01&to=2026-11-30", e.item), ent, "")
		if code != 200 {
			t.Fatalf("수불부 %s: %d %s", ent, code, body)
		}
		var p page
		if err := json.Unmarshal([]byte(body), &p); err != nil {
			t.Fatal(err)
		}
		return p
	}
	check := func(label string, p page, opening, in, out, closing int64, rows int) {
		t.Helper()
		if p.Opening != opening || p.TotalIn != in || p.TotalOut != out || p.Closing != closing || p.Total != rows || len(p.Rows) != rows {
			t.Errorf("%s: 기초 %d(%d) 입고 %d(%d) 출고 %d(%d) 기말 %d(%d) 행 %d(%d)", label, p.Opening, opening, p.TotalIn, in, p.TotalOut, out, p.Closing, closing, p.Total, rows)
		}
		if rows > 0 && p.Rows[len(p.Rows)-1].Balance != closing {
			t.Errorf("%s: 마지막 행 잔량 %d, 기말 %d", label, p.Rows[len(p.Rows)-1].Balance, closing)
		}
	}
	// A(MAIN): 기초 7, 10-05 입고 50, 11-10 법인 간 이동 출고 2 → 기말 55.
	check("A 만 보는 사용자", get(e.tokU, ""), 7, 50, 2, 55, 2)
	check("두 법인 사용자가 MAIN", get(e.tokBoth, "MAIN"), 7, 50, 2, 55, 2)
	// B(ABC): 기초 10, 10-05 입고 40, 11-10 이동 입고 2, 11-11 B 안의 이동 출고 3·입고 3 → 기말 52.
	check("두 법인 사용자가 ABC", get(e.tokBoth, "ABC"), 10, 45, 3, 52, 4)
	// 전체 모드(그룹 범위): 두 법인 합산, 행과 같은 범위다(기초 17).
	g := get(e.tokGroup, "*")
	if g.Opening != 17 || g.Closing != g.Opening+g.TotalIn-g.TotalOut || (len(g.Rows) > 0 && g.Rows[len(g.Rows)-1].Balance != g.Closing) {
		t.Errorf("전체 모드: %+v", g)
	}
}
