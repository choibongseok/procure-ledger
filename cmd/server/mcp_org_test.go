package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/choibongseok/procure-ledger/internal/bootstrap"
	"github.com/choibongseok/procure-ledger/internal/config"
	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/dbtest"
	"github.com/choibongseok/procure-ledger/internal/org"
)

// 실제 조직 서비스(개발 시드 60명) 위에서 조직 도구를 HTTP /mcp 로 부른다: 사람·조직이 나오고, admin 토큰이어도 이메일·휴대전화·사번이 나가지 않는다.
func TestMCPOrgToolsOverRealOrgService(t *testing.T) {
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
	// 서버 기동이 하는 개발용 조직 시드(약 60명)를 넣는다.
	if did, err := d.Org.(*org.Service).SeedDev(ctx); err != nil || !did {
		t.Fatalf("개발용 조직 시드: %v %v", did, err)
	}
	h := handlerFor(buildRoutes(cfg, d), d)

	token := func(email string) string {
		var p contract.Principal
		var role string
		if err := d.DB.QueryRowContext(ctx, `SELECT id, role, department_id FROM users WHERE email = ?`, email).Scan(&p.UserID, &role, &p.DepartmentID); err != nil {
			t.Fatal(err)
		}
		p.Role, p.Name = contract.Role(role), email
		tok, _, err := d.Tokens.Issue(ctx, p, "조직 시험-"+email, 0)
		if err != nil {
			t.Fatal(err)
		}
		return tok
	}
	mcpCall := func(tok, tool string, args any) (bool, map[string]any, string) {
		body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": tool, "arguments": args}})
		req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		req.Header.Set("Authorization", "Bearer "+tok)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		raw, _ := io.ReadAll(w.Body)
		var out struct {
			Result struct {
				IsError bool `json:"isError"`
				Content []struct {
					Text string `json:"text"`
				} `json:"content"`
			} `json:"result"`
		}
		_ = json.Unmarshal(raw, &out)
		text := ""
		for _, c := range out.Result.Content {
			text += c.Text
		}
		m := map[string]any{}
		_ = json.Unmarshal([]byte(text), &m)
		return out.Result.IsError, m, text
	}

	for _, who := range []string{"admin@pl.test", "finance@pl.test", "staff@pl.test"} {
		tok := token(who)
		isErr, m, text := mcpCall(tok, "search_people", map[string]any{"query": "구매"})
		if isErr || m["count"] == nil || m["count"].(float64) == 0 {
			t.Fatalf("%s 사람 검색: %s", who, text)
		}
		for _, bad := range []string{"@pl.test", "010-", "\"email\"", "\"phone\"", "employee"} {
			if strings.Contains(text, bad) {
				t.Fatalf("%s 에게 나가면 안 되는 값 %q: %s", who, bad, text)
			}
		}
		isErr, m, text = mcpCall(tok, "get_org_unit", map[string]any{"unit": "구매1팀", "include_members": true})
		if isErr {
			t.Fatalf("%s 조직 조회: %s", who, text)
		}
		unit := m["unit"].(map[string]any)
		head := m["head"].(map[string]any)
		if unit["name"] != "구매1팀" || head["title"] != "팀장" || head["vacant"] != false || m["member_count_total"].(float64) < 1 {
			t.Fatalf("%s 조직 조회 값: %s", who, text)
		}
		if strings.Contains(text, "@pl.test") || strings.Contains(text, "010-") {
			t.Fatalf("%s 조직 조회에 연락처: %s", who, text)
		}
	}

	// 결재선 미리보기(who_approves): 실제 결재선 서비스 위에서 본인 구매 품의서·양식 문서가 나오고 연락처는 없다.
	staffTok := token("staff@pl.test")
	isErr, m, text := mcpCall(staffTok, "who_approves", map[string]any{"amount": 12000000})
	if isErr || m["steps"] == nil || len(m["steps"].([]any)) == 0 || m["form"].(map[string]any)["code"] != "PUR" {
		t.Fatalf("구매 품의서 결재선: %s", text)
	}
	if strings.Contains(text, "@pl.test") || strings.Contains(text, "010-") || strings.Contains(text, "\"approve\"") {
		t.Fatalf("결재선에 나가면 안 되는 값: %s", text)
	}
	isErr, m, text = mcpCall(staffTok, "who_approves", map[string]any{"form_code": "GEN"})
	if isErr || m["form"].(map[string]any)["code"] != "GEN" || m["steps"] == nil {
		t.Fatalf("일반 기안서 결재선: %s", text)
	}
	// 양식 문서에서 다른 사람을 지정하면 서비스가 거절한다.
	if isErr, _, text = mcpCall(staffTok, "who_approves", map[string]any{"form_code": "GEN", "requester_id": 1}); !isErr || !strings.Contains(text, "본인 것만 볼 수 있습니다") {
		t.Fatalf("양식 타인 질의: %v %s", isErr, text)
	}
	// 금액 구간 양식의 금액 누락은 한국어 오류다.
	if isErr, _, text = mcpCall(staffTok, "who_approves", map[string]any{}); !isErr || !strings.Contains(text, "금액(원)을 알려 주세요.") {
		t.Fatalf("금액 누락: %v %s", isErr, text)
	}

	// 수불부(get_item_ledger): 실제 수불부 서비스 위에서 기초·기말이 맞고 영문 종류 코드·처리자·금액이 없다.
	var itemID int64
	if err := d.DB.QueryRowContext(ctx, `SELECT id FROM items ORDER BY id LIMIT 1`).Scan(&itemID); err != nil {
		itemID = dbtest.InsertID(t, d.DB, `INSERT INTO items (sku, name, unit) VALUES ('LED-1', '수불부 시험 품목', 'EA')`)
	}
	isErr, m, text = mcpCall(staffTok, "get_item_ledger", map[string]any{"item_id": itemID, "from": "2020-01-01"})
	if isErr || m["rows"] == nil || m["closing"] == nil {
		t.Fatalf("수불부: %s", text)
	}
	if m["opening"].(float64)+m["total_in"].(float64)-m["total_out"].(float64) != m["closing"].(float64) {
		t.Fatalf("기초+입고-출고=기말: %s", text)
	}
	for _, bad := range []string{"receipt", "issue", "transfer", "adjust", "actor", "user", "amount"} {
		if strings.Contains(text, "\""+bad+"\"") {
			t.Fatalf("나가면 안 되는 키·값 %q: %s", bad, text)
		}
	}

	// 예산 잔액(get_budget_status): 재무가 이번 달 구매팀 예산을 등록하면 직원은 본인 부서 잔액과 가상 금액 초과 확인을 받고,
	// 다른 부서를 지정하면 예산 조회 권한이 없어 거절된다.
	var purchaseDept int64
	if err := d.DB.QueryRowContext(ctx, `SELECT department_id FROM users WHERE email = 'staff@pl.test'`).Scan(&purchaseDept); err != nil {
		t.Fatal(err)
	}
	var finID int64
	var finDept int64
	if err := d.DB.QueryRowContext(ctx, `SELECT id, department_id FROM users WHERE email = 'finance@pl.test'`).Scan(&finID, &finDept); err != nil {
		t.Fatal(err)
	}
	month := time.Now().In(contract.KST).Format("2006-01")
	fctx := contract.WithPrincipal(ctx, contract.Principal{UserID: finID, Name: "재무", Role: contract.RoleFinance, DepartmentID: finDept, Channel: contract.ChannelWeb})
	if _, err := d.Budget.Set(fctx, domain.BudgetInput{DepartmentID: purchaseDept, Month: month, Amount: 3000000, Reason: "시험"}); err != nil {
		t.Fatal(err)
	}
	isErr, m, text = mcpCall(staffTok, "get_budget_status", map[string]any{"amount": 4000000})
	if isErr || m["budget"] != float64(3000000) || m["registered"] != true || m["check"].(map[string]any)["over_amount"] != float64(1000000) {
		t.Fatalf("본인 부서 예산: %s", text)
	}
	if isErr, m, text = mcpCall(staffTok, "get_budget_status", map[string]any{"department": "구매팀"}); isErr || m["budget"] != float64(3000000) {
		t.Fatalf("본인 부서 이름은 허용되어야 함: %s", text)
	}
	if isErr, _, text = mcpCall(staffTok, "get_budget_status", map[string]any{"department": "영업팀"}); !isErr {
		t.Fatalf("타 부서 지정은 예산 조회 권한이 없어 거절되어야 함: %s", text)
	}
	if isErr, m, text = mcpCall(token("finance@pl.test"), "get_budget_status", map[string]any{"department": "구매팀"}); isErr || m["budget"] != float64(3000000) {
		t.Fatalf("재무의 타 부서 조회: %s", text)
	}

	// 월 마감 상태(get_month_close_status): 실제 마감 서비스 위에서 열린 달과 마감된 달(버전 1)을 본다.
	// 재무는 기말 금액 합계 키를 받고, 직원 응답에는 금액 키가 없다. 마감 실행은 도구로 불가능하다.
	prev := time.Date(time.Now().In(contract.KST).Year(), time.Now().In(contract.KST).Month(), 1, 0, 0, 0, 0, contract.KST).AddDate(0, -1, 0).Format("2006-01")
	finTok := token("finance@pl.test")
	isErr, m, text = mcpCall(staffTok, "get_month_close_status", map[string]any{})
	if isErr || m["month"] != prev || m["status"] != "열림" || m["version"] != float64(0) || m["closed_at"] != nil {
		t.Fatalf("기본(직전 달) 열린 달: %s", text)
	}
	// 직전 달 15일에 입고 한 줄(100개, 단가 1,000원)을 직접 넣어 마감할 재고 기록을 만든다(원장 삽입과 평가 줄).
	lid := dbtest.InsertID(t, d.DB, `INSERT INTO stock_ledger (item_id, qty_delta, reason, ref_type, ref_id, actor_id, created_at, warehouse_id, book_date) VALUES (?, 100, 'receipt', 'receipt', 1, ?, ?, 1, ?)`,
		itemID, finID, prev+"-15T03:00:00Z", prev+"-15")
	var ierr error
	if _, ierr = d.DB.ExecContext(ctx, `INSERT INTO stock_valuation (ledger_id, item_id, book_date, in_unit_cost, unit_cost, amount, bal_qty, bal_amount, avg_cost) VALUES (?, ?, ?, 1000, 1000, 100000, 100, 100000, 1000)`, lid, itemID, prev+"-15"); ierr != nil {
		t.Fatal(ierr)
	}
	if _, err := d.MonthClose.Close(fctx, prev); err != nil {
		t.Fatalf("직전 달 마감: %v", err)
	}
	isErr, m, text = mcpCall(finTok, "get_month_close_status", map[string]any{"month": prev})
	if isErr || m["status"] != "마감됨" || m["version"] != float64(1) || m["closed_at"] == nil || m["close_amount"] != float64(100000) || m["blockers"] != float64(0) {
		t.Fatalf("재무의 마감된 달: %s", text)
	}
	isErr, m, text = mcpCall(staffTok, "get_month_close_status", map[string]any{"month": prev})
	if isErr || m["status"] != "마감됨" || m["version"] != float64(1) || strings.Contains(text, "close_amount") {
		t.Fatalf("직원 응답에 금액 키가 있으면 안 됨: %s", text)
	}
	// 이번 달은 아직 마감할 수 없고 상태는 열림이다. 형식 오류는 한국어 오류.
	cur := time.Now().In(contract.KST).Format("2006-01")
	if isErr, m, text = mcpCall(staffTok, "get_month_close_status", map[string]any{"month": cur}); isErr || m["status"] != "열림" {
		t.Fatalf("이번 달: %s", text)
	}
	if isErr, _, text = mcpCall(staffTok, "get_month_close_status", map[string]any{"month": "10월"}); !isErr || !strings.Contains(text, "YYYY-MM") {
		t.Fatalf("형식 오류: %v %s", isErr, text)
	}
	// 감사: 월 마감 대상 status:월 로 남고 금액은 남지 않는다.
	var cnt int
	if err := d.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_logs WHERE action = 'mcp.get_month_close_status' AND target_type = 'month_close' AND target_id = ?`, "status:"+prev).Scan(&cnt); err != nil || cnt < 2 {
		t.Fatalf("감사 기록 %d건: %v", cnt, err)
	}
}
