package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/bootstrap"
	"github.com/choibongseok/procure-ledger/internal/config"
	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/dbtest"
)

type policyHTTPHit struct {
	DocSlug     string `json:"doc_slug"`
	DocTitle    string `json:"doc_title"`
	ClauseNo    string `json:"clause_no"`
	Heading     string `json:"heading"`
	Text        string `json:"text"`
	Source      string `json:"source"`
	Reason      string `json:"reason"`
	ReasonLabel string `json:"reason_label"`
}

// 실제 서비스 조립 위에서 규정 검색 API 를 HTTP 로 검사한다: 비로그인 401, 역할·부서별 열람 범위, 응답에 건수 필드가 없음,
// 작성 화면용 관련 조항, 입력 검증, 감사 기록(질의 마스킹).
func TestPolicySearchOverHTTP(t *testing.T) {
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
	for _, email := range []string{"staff@pl.test", "staff2@pl.test", "finance@pl.test", "admin@pl.test", "lead@pl.test", "auditor@pl.test"} {
		var p contract.Principal
		var role string
		if err := d.DB.QueryRowContext(ctx, `SELECT id, role, department_id FROM users WHERE email = ?`, email).Scan(&p.UserID, &role, &p.DepartmentID); err != nil {
			t.Fatal(err)
		}
		p.Role, p.Name = contract.Role(role), email
		tok, _, err := d.Tokens.Issue(ctx, p, "policy", 0)
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
	hits := func(w *httptest.ResponseRecorder) []policyHTTPHit {
		t.Helper()
		if w.Code != http.StatusOK {
			t.Fatalf("200 이어야 함: %d %s", w.Code, w.Body.String())
		}
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
			t.Fatal(err)
		}
		if len(raw) != 1 {
			t.Fatalf("응답에는 hits 외 필드(건수·더 있음 등)가 없어야 함: %s", w.Body.String())
		}
		var out []policyHTTPHit
		if err := json.Unmarshal(raw["hits"], &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	has := func(hs []policyHTTPHit, slug string) bool {
		for _, h := range hs {
			if h.DocSlug == slug {
				return true
			}
		}
		return false
	}
	q := func(s string) string { return "/api/policies/search?limit=20&q=" + url.QueryEscape(s) }

	// 비로그인
	if w := get(q("결재선"), ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("비로그인은 401: %d", w.Code)
	}
	if w := get("/api/policies/related?amount=1000", ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("비로그인 related 는 401: %d", w.Code)
	}

	// 조항 원문과 출처
	hs := hits(get(q("금액 구간별 결재선"), "staff@pl.test"))
	if len(hs) == 0 || hs[0].DocTitle != "구매 결재 규정" || hs[0].ClauseNo != "제2조" || hs[0].Heading != "금액 구간별 결재선" ||
		hs[0].Source != "구매 결재 규정 제2조(금액 구간별 결재선)" || !strings.Contains(hs[0].Text, "1000만원 이상") {
		t.Fatalf("출처·원문: %+v", hs)
	}

	// 역할별 열람 범위
	const evid = "증빙 판정 재무 확정 이미지 증빙 감사 로그 해시 체인"
	for _, email := range []string{"staff@pl.test", "staff2@pl.test", "lead@pl.test"} {
		hs := hits(get(q(evid), email))
		if has(hs, "evidence-verification") || has(hs, "audit-security") {
			t.Errorf("%s 에게 재무·감사 전용 규정이 나옴: %+v", email, hs)
		}
	}
	if hs := hits(get(q(evid), "finance@pl.test")); !has(hs, "evidence-verification") || has(hs, "audit-security") {
		t.Errorf("finance 는 증빙 규정만 본다: %+v", hs)
	}
	if hs := hits(get(q(evid), "auditor@pl.test")); !has(hs, "evidence-verification") || !has(hs, "audit-security") {
		t.Errorf("auditor 는 둘 다 본다: %+v", hs)
	}
	if hs := hits(get(q("결재 위임 수임자"), "staff@pl.test")); has(hs, "approval-delegation") {
		t.Errorf("staff 에게 위임 규정이 나옴")
	}
	if hs := hits(get(q("결재 위임 수임자"), "lead@pl.test")); !has(hs, "approval-delegation") {
		t.Errorf("결재자는 위임 규정을 본다")
	}

	// 부서별 열람 범위(같은 staff 역할, 다른 부서)
	if hs := hits(get(q("영업팀 샘플 견적 번호"), "staff2@pl.test")); !has(hs, "sales-team-purchase") || has(hs, "purchasing-team-reorder") {
		t.Errorf("영업팀 직원: %+v", hs)
	}
	if hs := hits(get(q("영업팀 샘플 견적 번호 재주문 거래처"), "staff@pl.test")); has(hs, "sales-team-purchase") || !has(hs, "purchasing-team-reorder") {
		t.Errorf("구매팀 직원: %+v", hs)
	}
	// 범위 밖에만 있는 내용은 0건이고 응답 모양은 같다
	if hs := hits(get(q("추출 근거 인용 구간 원문"), "staff@pl.test")); has(hs, "evidence-verification") {
		t.Errorf("범위 밖 문서: %+v", hs)
	}

	// 작성 화면용 관련 조항
	// 구간 경계: 99만9,999원 / 100만원 / 999만9,999원 / 1,000만원 / 9,999만9,999원 / 1억원
	for amt, clause := range map[string]string{"0": "제3조", "999999": "제3조", "1000000": "제4조", "9999999": "제4조", "10000000": "제5조", "99999999": "제5조", "100000000": "제6조"} {
		hs := hits(get("/api/policies/related?amount="+amt, "staff@pl.test"))
		if len(hs) != 1 || hs[0].DocSlug != "purchase-approval-line" || hs[0].ClauseNo != clause {
			t.Fatalf("금액 %s: 기대 %s, 실제 %+v", amt, clause, hs)
		}
	}
	// 규정과 무관한 품목 이름은 구간 조항 1건뿐이고, 응답에 reason 표시가 있다.
	hs = hits(get("/api/policies/related?amount=5000000&items="+url.QueryEscape("볼트 M8,복사용지"), "staff@pl.test"))
	if len(hs) != 1 || hs[0].DocSlug != "purchase-approval-line" || hs[0].ClauseNo != "제4조" || hs[0].Reason != "tier" || hs[0].ReasonLabel != "금액 구간 결재선" {
		t.Fatalf("금액+무관한 품목: %+v", hs)
	}
	hs = hits(get("/api/policies/related?amount=5000000&items="+url.QueryEscape("유효기한 임박 시약"), "staff@pl.test"))
	if len(hs) < 2 || len(hs) > 3 || hs[0].Reason != "tier" || hs[1].Reason != "item" || hs[1].ReasonLabel != "품목 관련 규정" {
		t.Fatalf("금액+품목: %+v", hs)
	}
	if w := get("/api/policies/related?amount=abc", "staff@pl.test"); w.Code != http.StatusBadRequest {
		t.Errorf("금액 형식 오류는 400: %d", w.Code)
	}
	if w := get("/api/policies/related?amount=-5", "staff@pl.test"); w.Code != http.StatusBadRequest {
		t.Errorf("음수 금액은 400: %d", w.Code)
	}
	if w := get("/api/policies/search?q=", "staff@pl.test"); w.Code != http.StatusBadRequest {
		t.Errorf("빈 검색어는 400: %d", w.Code)
	}

	// 감사: 누가·어떤 질의로·몇 건. 질의의 사업자번호는 마스킹되어 남는다.
	if w := get(q("결재 문의 123-45-67890 kim@corp.example"), "staff@pl.test"); w.Code != http.StatusOK {
		t.Fatal(w.Code)
	}
	var actor int64
	var role, channel, diff string
	err = d.DB.QueryRowContext(ctx, `SELECT actor_id, actor_role, channel, diff FROM audit_logs WHERE action = 'policy.search' AND diff LIKE '%문의%' ORDER BY seq DESC LIMIT 1`).
		Scan(&actor, &role, &channel, &diff)
	if err != nil {
		t.Fatalf("검색 감사 기록이 있어야 함: %v", err)
	}
	if role != "staff" || channel != "web" || actor == 0 || !strings.Contains(diff, "count") {
		t.Fatalf("감사 내용: %d %s %s %s", actor, role, channel, diff)
	}
	if strings.Contains(diff, "123-45-67890") || strings.Contains(diff, "kim@corp.example") {
		t.Fatalf("질의의 민감 정보가 감사 로그에 그대로 남음: %s", diff)
	}
	if res, err := d.AuditReader.Verify(ctx); err != nil || !res.ChainOK {
		t.Fatalf("감사 체인: %+v %v", res, err)
	}
}
