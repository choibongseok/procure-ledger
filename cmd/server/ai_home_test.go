package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/bootstrap"
	"github.com/choibongseok/procure-ledger/internal/config"
	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/dbtest"
)

const homeDoc = "거래명세서\n공급자: 한빛상사 010-9999-8888 a@b.example\n사업자번호: 123-45-67890\n품목: 볼트 M8 | 수량 10 | 단가 500 | 금액 5000\n합계: 5000"

// 실제 서비스 조립 위에서 홈 카드 API 를 HTTP 로 검사한다.
//   - pending-confirm: finance 200(건수가 0 이 아님: 선택적 인터페이스 단언이 조립에서 성립), 그 외 역할 403, 비로그인 401, 원문 0건.
//   - usage/today: 비로그인 401, mine 에 타인 호출이 섞이지 않음, 전사 all 은 llm:usage_read 보유 역할에게만.
func TestHomeCardAPIsOverHTTP(t *testing.T) {
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
	h := handlerFor(buildRoutes(cfg, d), d)

	tokens := map[string]string{}
	var staffDept, staffID int64
	for _, email := range []string{"staff@pl.test", "staff2@pl.test", "finance@pl.test", "admin@pl.test", "lead@pl.test", "auditor@pl.test"} {
		var p contract.Principal
		var role string
		if err := d.DB.QueryRowContext(ctx, `SELECT id, role, department_id FROM users WHERE email = ?`, email).Scan(&p.UserID, &role, &p.DepartmentID); err != nil {
			t.Fatal(err)
		}
		p.Role, p.Name = contract.Role(role), email
		tok, _, err := d.Tokens.Issue(ctx, p, "home", 0)
		if err != nil {
			t.Fatal(err)
		}
		tokens[email] = tok
		if email == "staff@pl.test" {
			staffDept, staffID = p.DepartmentID, p.UserID
		}
	}

	exec := func(q string, args ...any) int64 {
		return dbtest.ExecID(t, d.DB, q, args...)
	}
	now := "2026-11-10T00:00:00Z"
	vendor := exec(`INSERT INTO vendors (name, business_no) VALUES ('한빛상사', '123-45-67890')`)
	item := exec(`INSERT INTO items (sku, name) VALUES ('HOME-1', '볼트 M8')`)
	pr := exec(`INSERT INTO purchase_requests (no, requester_id, department_id, vendor_id, title, status, total_amount, created_at, updated_at)
		VALUES ('PR-HOME-1', ?, ?, ?, '홈', 'ordered', 5000, ?, ?)`, staffID, staffDept, vendor, now, now)
	po := exec(`INSERT INTO purchase_orders (no, pr_id, vendor_id, status, total_amount, created_at) VALUES ('PO-HOME-1', ?, ?, 'open', 5000, ?)`, pr, vendor, now)
	exec(`INSERT INTO po_lines (po_id, item_id, quantity, unit_price) VALUES (?, ?, 10, 500)`, po, item)

	do := func(method, path, email string, body any) *httptest.ResponseRecorder {
		var rd *bytes.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rd = bytes.NewReader(b)
		} else {
			rd = bytes.NewReader(nil)
		}
		req := httptest.NewRequest(method, path, rd)
		req.Header.Set("Content-Type", "application/json")
		if email != "" {
			req.Header.Set("Authorization", "Bearer "+tokens[email])
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w
	}

	// staff 가 판정을 올린다(LLM 호출 1회가 staff 사용량으로 남는다).
	if w := do("POST", "/api/documents/verify", "staff@pl.test", map[string]any{"filename": "a.txt", "text": homeDoc, "poId": po}); w.Code != http.StatusOK {
		t.Fatalf("판정 업로드: %d %s", w.Code, w.Body.String())
	}

	// ---- pending-confirm ----
	const pc = "/api/documents/pending-confirm"
	if w := do("GET", pc, "", nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("비로그인은 401: %d", w.Code)
	}
	for _, email := range []string{"staff@pl.test", "staff2@pl.test", "admin@pl.test", "lead@pl.test", "auditor@pl.test"} {
		if w := do("GET", pc, email, nil); w.Code != http.StatusForbidden {
			t.Errorf("%s 는 403 이어야 함: %d %s", email, w.Code, w.Body.String())
		}
	}
	w := do("GET", pc, "finance@pl.test", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("finance 는 200: %d %s", w.Code, w.Body.String())
	}
	var pend struct {
		Total int `json:"total"`
		Items []struct {
			ID       int64  `json:"id"`
			Supplier string `json:"supplier"`
		} `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &pend); err != nil {
		t.Fatal(err)
	}
	if pend.Total != 1 || len(pend.Items) != 1 {
		t.Fatalf("실제 조립에서 확정 대기 1건이어야 함(0 이면 선택적 인터페이스 단언 실패): %s", w.Body.String())
	}
	for _, raw := range []string{"010-9999-8888", "a@b.example", "123-45-67890", "1234567890"} {
		if strings.Contains(w.Body.String(), raw) {
			t.Fatalf("응답에 원문 %s 이 나감: %s", raw, w.Body.String())
		}
	}

	// ---- usage/today ----
	const ut = "/api/llm/usage/today"
	if w := do("GET", ut, "", nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("비로그인은 401: %d", w.Code)
	}
	type today struct {
		Mine struct {
			Calls int64 `json:"calls"`
		} `json:"mine"`
		All *struct {
			Calls int64 `json:"calls"`
		} `json:"all"`
	}
	get := func(email string) today {
		w := do("GET", ut, email, nil)
		if w.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", email, w.Code, w.Body.String())
		}
		var out today
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	if s := get("staff@pl.test"); s.Mine.Calls < 1 || s.All != nil {
		t.Fatalf("staff: 본인 호출만, 전사 합계 없음: %+v", s)
	}
	if s2 := get("staff2@pl.test"); s2.Mine.Calls != 0 || s2.All != nil {
		t.Fatalf("staff2 의 mine 에 타인 호출이 섞임: %+v", s2)
	}
	if l := get("lead@pl.test"); l.All != nil {
		t.Fatalf("manager 는 llm:usage_read 가 없어 전사 합계 없음: %+v", l)
	}
	for _, email := range []string{"finance@pl.test", "admin@pl.test", "auditor@pl.test"} {
		if r := get(email); r.All == nil || r.All.Calls < 1 || r.Mine.Calls != 0 && email == "auditor@pl.test" {
			t.Fatalf("%s: 전사 합계가 있어야 함: %+v", email, r)
		}
	}

	// ---- usage/limits: 역할별 오늘 사용량·한도 ----
	const ul = "/api/llm/usage/limits"
	exec(`INSERT INTO llm_daily_quotas (scope_type, scope_value, daily_token_limit) VALUES ('role','staff',50000),('role','finance',200000),('user',?,10)`, "1")
	if w := do("GET", ul, "", nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("limits 비로그인은 401: %d", w.Code)
	}
	for _, email := range []string{"staff@pl.test", "staff2@pl.test", "lead@pl.test"} {
		if w := do("GET", ul, email, nil); w.Code != http.StatusForbidden {
			t.Errorf("limits %s 는 403 이어야 함: %d %s", email, w.Code, w.Body.String())
		}
	}
	type limitsResp struct {
		Roles []struct {
			Role       string `json:"role"`
			UsedTokens int64  `json:"usedTokens"`
			Limit      *int64 `json:"dailyTokenLimit"`
		} `json:"roles"`
	}
	getLimits := func(email string) (limitsResp, string) {
		w := do("GET", ul, email, nil)
		if w.Code != http.StatusOK {
			t.Fatalf("limits %s: %d %s", email, w.Code, w.Body.String())
		}
		var out limitsResp
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out, w.Body.String()
	}
	fin, finBody := getLimits("finance@pl.test")
	if len(fin.Roles) != 1 || fin.Roles[0].Role != "finance" || fin.Roles[0].Limit == nil || *fin.Roles[0].Limit != 200000 {
		t.Fatalf("finance 는 본인 역할 한 줄: %s", finBody)
	}
	adm, admBody := getLimits("admin@pl.test")
	seen := map[string]int64{}
	for _, r := range adm.Roles {
		seen[r.Role] = r.UsedTokens
	}
	if _, ok := seen["finance"]; !ok || seen["staff"] == 0 || len(adm.Roles) != 2 {
		t.Fatalf("admin 은 전 역할 행(staff 사용량 포함, 사용자 범위 한도는 제외): %s", admBody)
	}
	for _, body := range []string{finBody, admBody} {
		for _, raw := range []string{"010-9999-8888", "a@b.example", "123-45-67890", "pl.test", "userId", "user_id"} {
			if strings.Contains(body, raw) {
				t.Fatalf("limits 응답에 식별값·원문 %s 이 나감: %s", raw, body)
			}
		}
	}
}
