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
	"github.com/choibongseok/procure-ledger/internal/dbtest"
)

type usageStats struct {
	Calls       int     `json:"calls"`
	Success     int     `json:"success"`
	Denied      int     `json:"denied"`
	AuthFailed  int     `json:"authFailed"`
	RateLimited int     `json:"rateLimited"`
	Errors      int     `json:"errors"`
	RejectRate  float64 `json:"rejectRate"`
	AvgMS       int64   `json:"avgMs"`
	MaxMS       int64   `json:"maxMs"`
}

type usageBody struct {
	From       string     `json:"from"`
	To         string     `json:"to"`
	RatePerMin int        `json:"ratePerMin"`
	Total      usageStats `json:"total"`
	Tools      []struct {
		Tool string `json:"tool"`
		usageStats
	} `json:"tools"`
	Tokens []struct {
		TokenID int64  `json:"tokenId"`
		Name    string `json:"name"`
		Prefix  string `json:"prefix"`
		Owner   string `json:"owner"`
		usageStats
	} `json:"tokens"`
}

// 실제 서비스 조립 위에서 사용 현황 API 를 HTTP 로 검사한다: 권한(admin·auditor 만, 나머지 403·비로그인 401),
// 호출·거부·한도 초과·오류의 집계, 토큰 이름·소유자 표시(원문·해시 없음), 기간 경계, 조회가 감사 레코드·체인을 건드리지 않음,
// 분당 한도(HTTP /mcp)와 한도 초과 감사.
func TestMCPUsageOverHTTP(t *testing.T) {
	dbtest.SetEnv(t)
	t.Setenv("PL_DEV_IDP", "1")
	t.Setenv("PL_LLM_PROVIDER", "mock")
	t.Setenv("PL_MCP_RATE_PER_MIN", "4")
	cfg := config.Load()
	ctx := context.Background()
	d, closeDB, err := bootstrap.Build(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer closeDB()
	h := handlerFor(buildRoutes(cfg, d), d)

	tokens := map[string]string{}
	for _, email := range []string{"staff@pl.test", "lead@pl.test", "finance@pl.test", "admin@pl.test", "auditor@pl.test"} {
		var p contract.Principal
		var role string
		if err := d.DB.QueryRowContext(ctx, `SELECT id, role, department_id FROM users WHERE email = ?`, email).Scan(&p.UserID, &role, &p.DepartmentID); err != nil {
			t.Fatal(err)
		}
		p.Role, p.Name = contract.Role(role), email
		tok, _, err := d.Tokens.Issue(ctx, p, "토큰-"+email, 0)
		if err != nil {
			t.Fatal(err)
		}
		tokens[email] = tok
	}
	get := func(path, email string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", path, nil)
		if email != "" {
			req.Header.Set("Authorization", "Bearer "+tokens[email])
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w
	}
	mcpCall := func(tok, tool string, args any) map[string]any {
		body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": tool, "arguments": args}})
		req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		req.Header.Set("Authorization", "Bearer "+tok)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		out := map[string]any{}
		raw, _ := io.ReadAll(w.Body)
		_ = json.Unmarshal(raw, &out)
		return out
	}
	isErr := func(out map[string]any) bool {
		res, _ := out["result"].(map[string]any)
		e, _ := res["isError"].(bool)
		return e
	}
	text := func(out map[string]any) string {
		b, _ := json.Marshal(out)
		return string(b)
	}

	// 권한
	if w := get("/api/mcp/usage", ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("비로그인 401: %d", w.Code)
	}
	for _, email := range []string{"staff@pl.test", "lead@pl.test", "finance@pl.test"} {
		if w := get("/api/mcp/usage", email); w.Code != http.StatusForbidden {
			t.Fatalf("%s 는 403: %d", email, w.Code)
		}
	}

	// 호출: staff 토큰으로 한도(4번)까지 성공 3 + 권한 거부 1, 이어서 한도 초과 2번. lead 토큰은 따로 센다.
	staff := tokens["staff@pl.test"]
	for i := 0; i < 3; i++ {
		if out := mcpCall(staff, contract.ToolSearchItems, map[string]any{"query": "볼트"}); isErr(out) {
			t.Fatalf("성공 호출 %d: %s", i, text(out))
		}
	}
	if out := mcpCall(staff, contract.ToolApproveOrReject, map[string]any{"pr_id": 1, "approve": true}); !isErr(out) || !strings.Contains(text(out), "권한") {
		t.Fatalf("권한 거부: %s", text(out))
	}
	for i := 0; i < 2; i++ {
		if out := mcpCall(staff, contract.ToolSearchItems, map[string]any{"query": "볼트"}); !isErr(out) || !strings.Contains(text(out), "호출 한도") {
			t.Fatalf("한도 초과 %d: %s", i, text(out))
		}
	}
	// 도구 실행 오류: lead 는 결재 권한이 있지만 없는 구매요청이라 오류.
	if out := mcpCall(tokens["lead@pl.test"], contract.ToolApproveOrReject, map[string]any{"pr_id": 987654, "approve": true}); !isErr(out) {
		t.Fatalf("없는 구매요청은 오류: %s", text(out))
	}

	// 조회 전후로 감사 레코드 수와 체인 검증 결과가 같다. 먼저 한 번 조회해 감사 로그에 조회 자체가 남지 않는지도 본다.
	countAudit := func() int {
		var n int
		if err := d.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_logs`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	beforeN := countAudit()
	vBefore, err := d.AuditReader.Verify(ctx)
	if err != nil || !vBefore.ChainOK {
		t.Fatalf("체인: %+v %v", vBefore, err)
	}

	w := get("/api/mcp/usage", "admin@pl.test")
	if w.Code != http.StatusOK {
		t.Fatalf("admin 200: %d %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), tokens["staff@pl.test"]) {
		t.Fatal("토큰 원문이 응답에 있음")
	}
	var u usageBody
	if err := json.Unmarshal(w.Body.Bytes(), &u); err != nil {
		t.Fatal(err)
	}
	if u.RatePerMin != 4 {
		t.Fatalf("한도 표시: %d", u.RatePerMin)
	}
	// 합계: 성공 3, 권한 거부 1, 한도 초과 2, 오류 1
	if u.Total.Calls != 7 || u.Total.Success != 3 || u.Total.Denied != 1 || u.Total.RateLimited != 2 || u.Total.Errors != 1 {
		t.Fatalf("합계: %+v", u.Total)
	}
	if want := 4.0 / 7.0; u.Total.RejectRate < want-0.001 || u.Total.RejectRate > want+0.001 {
		t.Fatalf("거부율: %v", u.Total.RejectRate)
	}
	tool := map[string]usageStats{}
	for _, r := range u.Tools {
		tool[r.Tool] = r.usageStats
	}
	// 첫 거부 1건은 도구에 붙고, 이어진 거부(요약·미정산 창)는 어느 도구인지 몰라 "도구 미상" 행에 붙는다(B17).
	if s := tool[contract.ToolSearchItems]; s.Calls != 4 || s.Success != 3 || s.RateLimited != 1 {
		t.Fatalf("search_items: %+v", s)
	}
	if s := tool["rate_limited_summary"]; s.Calls != 1 || s.RateLimited != 1 {
		t.Fatalf("도구 미상 행: %+v", s)
	}
	if s := tool[contract.ToolApproveOrReject]; s.Calls != 2 || s.Denied != 1 || s.Errors != 1 {
		t.Fatalf("approve_or_reject: %+v", s)
	}
	// 토큰은 이름·앞자리·소유자로 보인다.
	foundStaff := false
	for _, tk := range u.Tokens {
		if tk.Name == "토큰-staff@pl.test" {
			foundStaff = true
			if tk.Calls != 6 || tk.Prefix == "" || tk.Owner == "" || strings.Contains(tk.Prefix, tokens["staff@pl.test"]) {
				t.Fatalf("staff 토큰 행: %+v", tk)
			}
		}
	}
	if !foundStaff {
		t.Fatalf("토큰 행 없음: %+v", u.Tokens)
	}

	// 조회(admin·auditor 모두)는 감사 레코드를 만들지 않고 체인에 영향이 없다.
	if w := get("/api/mcp/usage", "auditor@pl.test"); w.Code != http.StatusOK {
		t.Fatalf("auditor 200: %d", w.Code)
	}
	if n := countAudit(); n != beforeN {
		t.Fatalf("조회가 감사 레코드를 만듦: %d -> %d", beforeN, n)
	}
	vAfter, err := d.AuditReader.Verify(ctx)
	if err != nil || !vAfter.ChainOK || vAfter.Checked != vBefore.Checked || vAfter.HeadHash != vBefore.HeadHash {
		t.Fatalf("조회 뒤 체인: %+v (전 %+v)", vAfter, vBefore)
	}

	// 기간: 오늘이 들어간 기간은 위 호출을 모두 세고, 어제까지만이면 0건이다(종료일 다음 날 0시 미만까지).
	today := time.Now().Format("2006-01-02")
	yesterday := time.Now().AddDate(0, 0, -1).Format("2006-01-02")
	var inRange, before usageBody
	_ = json.Unmarshal(get("/api/mcp/usage?from="+today+"&to="+today, "admin@pl.test").Body.Bytes(), &inRange)
	if inRange.Total.Calls != 7 {
		t.Fatalf("오늘 기간: %+v", inRange.Total)
	}
	_ = json.Unmarshal(get("/api/mcp/usage?from="+yesterday+"&to="+yesterday, "admin@pl.test").Body.Bytes(), &before)
	if before.Total.Calls != 0 || before.Tools == nil {
		t.Fatalf("어제 기간은 비어야 함: %+v", before.Total)
	}
	for _, q := range []string{"?from=abc", "?to=2026-13-40", "?from=" + today + "&to=" + yesterday, "?from=2020-01-01&to=" + today} {
		if w := get("/api/mcp/usage"+q, "admin@pl.test"); w.Code != http.StatusBadRequest {
			t.Fatalf("%s 는 400: %d", q, w.Code)
		}
	}

	// 한도 초과 거부 2회 중 첫 거부만 감사 로그에 남고(채널 mcp, denied, B17) 종료 기록에 시간·토큰 항목이 있다.
	// 나머지는 한도 창 카운터에 있고 사용 현황은 그것까지 합쳐 센다(위 합계 RateLimited 2).
	var n int
	if err := d.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_logs WHERE channel = 'mcp' AND result = 'denied' AND detail LIKE '%rate_limited%' AND diff LIKE '%duration_ms%' AND diff LIKE '%token_id%'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("한도 초과 감사는 첫 거부 1건이어야 함: %d %v", n, err)
	}

	// 기간 필터가 감사 조회 인덱스를 탄다(채널·시각). SQLite 는 실행 계획으로 보고, PG 는 작은 표에서 계획이 순차 검색을 고를 수 있어
	// (플래너 선택이라 시험이 흔들린다) 같은 열로 만든 인덱스가 있는지를 본다.
	if dbtest.PG() {
		var def string
		if err := d.DB.QueryRowContext(ctx, `SELECT indexdef FROM pg_indexes WHERE tablename = 'audit_logs' AND indexname = 'idx_audit_channel_at'`).Scan(&def); err != nil {
			t.Fatalf("감사 채널·시각 인덱스가 없음: %v", err)
		}
		if !strings.Contains(def, "(channel, at)") {
			t.Fatalf("감사 인덱스 열 구성: %s", def)
		}
		return
	}
	rows, err := d.DB.QueryContext(ctx, `EXPLAIN QUERY PLAN SELECT seq FROM audit_logs WHERE channel = ? AND at >= ? AND at < ? ORDER BY seq ASC`, "mcp", "2026-01-01", "2027-01-01")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	plan := ""
	for rows.Next() {
		var a, b, c int
		var detail string
		if err := rows.Scan(&a, &b, &c, &detail); err != nil {
			t.Fatal(err)
		}
		plan += detail + ";"
	}
	if !strings.Contains(plan, "idx_audit_channel_at") {
		t.Fatalf("감사 조회가 채널·시각 인덱스를 타지 않음: %s", plan)
	}
}

// 분당 한도가 설정되지 않으면(기본 0) 무제한이고 응답에도 한도 0 이 표시된다.
func TestMCPUsageNoLimitByDefault(t *testing.T) {
	dbtest.SetEnv(t)
	t.Setenv("PL_DEV_IDP", "1")
	t.Setenv("PL_LLM_PROVIDER", "mock")
	cfg := config.Load()
	if cfg.MCPRatePerMin != 0 {
		t.Fatalf("기본 한도는 0: %d", cfg.MCPRatePerMin)
	}
	ctx := context.Background()
	d, closeDB, err := bootstrap.Build(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer closeDB()
	if d.MCPRate != nil {
		t.Fatal("한도 0 이면 한도기가 없어야 함")
	}
}
