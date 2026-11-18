package handler_test

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/contract/deps"
	"github.com/choibongseok/procure-ledger/internal/contract/fake"
	"github.com/choibongseok/procure-ledger/internal/dbtest"
	"github.com/choibongseok/procure-ledger/internal/docverify"
	"github.com/choibongseok/procure-ledger/internal/docverify/kit"
	"github.com/choibongseok/procure-ledger/internal/handler"
	"github.com/choibongseok/procure-ledger/internal/llmgw"
	"github.com/choibongseok/procure-ledger/migrations"
)

type aiEnv struct {
	h     http.Handler
	rt    *contract.RouteTable
	rec   *fake.Recorder
	inv   *kit.Inventory
	d     deps.Deps
	poID  int64
	store *llmgw.Store
}

// withRole 은 테스트용 인증 대용: X-Role 헤더로 Principal 을 넣는다(실서비스의 인증 미들웨어는 platform).
func withRole(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if role := r.Header.Get("X-Role"); role != "" {
			p := kit.Principal(contract.Role(role))
			if role == string(contract.RoleAdmin) || role == string(contract.RoleAuditor) {
				p.UserID = 104
			}
			r = r.WithContext(contract.WithPrincipal(r.Context(), p))
		}
		next.ServeHTTP(w, r)
	})
}

func newAIEnv(t *testing.T) *aiEnv {
	t.Helper()
	ctx := context.Background()
	db := dbtest.Open(t)
	if err := migrations.Apply(ctx, db); err != nil {
		t.Fatal(err)
	}
	if err := kit.SeedBase(ctx, db); err != nil {
		t.Fatal(err)
	}
	rec := fake.NewRecorder()
	txs := contract.SQLTxManager{DB: db}
	gw, err := llmgw.New(llmgw.Options{Provider: llmgw.MockProvider{}, DefaultModel: "mock-1", DB: db, Txs: txs, Audit: rec, Masker: fake.Masker{}})
	if err != nil {
		t.Fatal(err)
	}
	if err := llmgw.EnsureDefaultPolicy(ctx, db, "mock*"); err != nil {
		t.Fatal(err)
	}
	inv := kit.NewInventory()
	svc := &docverify.Service{DB: db, Txs: txs, Audit: rec, LLM: gw, Inventory: inv, Vendors: docverify.SQLVendors{DB: db}, Masker: fake.Masker{}}
	d := deps.Deps{DB: db, Txs: txs, Audit: rec, Masker: fake.Masker{}, LLM: gw, Documents: svc, Inventory: inv}
	rt := contract.NewRouteTable()
	handler.RegisterDocVerify(rt, d)
	handler.RegisterLLM(rt, d)
	po, _, err := inv.Seed(ctx, db, kit.POSpec{VendorName: "한빛상사", VendorBusinessNo: "123-45-67890",
		Lines: []kit.LineSpec{{Name: "볼트 M8", Qty: 10, UnitPrice: 500}}})
	if err != nil {
		t.Fatal(err)
	}
	return &aiEnv{h: withRole(rt.Handler()), rt: rt, rec: rec, inv: inv, d: d, poID: po, store: &llmgw.Store{DB: db, Txs: txs, Audit: rec}}
}

const doc = "거래명세서\n공급자: 한빛상사\n사업자번호: 123-45-67890\n품목: 볼트 M8 | 수량 10 | 단가 500 | 금액 5000\n합계: 5000"

func (e *aiEnv) do(method, path, role string, body []byte, ctype string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	if role != "" {
		req.Header.Set("X-Role", role)
	}
	w := httptest.NewRecorder()
	e.h.ServeHTTP(w, req)
	return w
}

// 라우트 권한 선언 누락 검사(AI 라우트). verify.sh 2단계가 이름으로 찾는다.
func TestPermissionDeclarationAIRoutes(t *testing.T) {
	e := newAIEnv(t)
	routes := e.rt.Routes()
	if len(routes) < 9 {
		t.Fatalf("AI 라우트가 등록되어야 함: %d", len(routes))
	}
	if problems := contract.CheckRoutes(routes); len(problems) != 0 {
		t.Fatalf("권한 선언 문제: %v", problems)
	}
	for _, r := range routes {
		if r.Permission == contract.PermPublic || r.Permission == "" {
			t.Errorf("%s %s: AI 라우트는 공개일 수 없음", r.Method, r.Pattern)
		}
		path := strings.NewReplacer("{id}", "1").Replace(r.Pattern)
		if w := e.do(r.Method, path, "", nil, ""); w.Code != http.StatusUnauthorized {
			t.Errorf("%s %s: 미인증 %d (401 기대)", r.Method, r.Pattern, w.Code)
		}
	}
}

func upload(t *testing.T, poID int64, text string) ([]byte, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("po_id", strconv.FormatInt(poID, 10))
	fw, _ := mw.CreateFormFile("file", "거래명세서.txt")
	_, _ = fw.Write([]byte(text))
	_ = mw.Close()
	return buf.Bytes(), mw.FormDataContentType()
}

func TestUploadVerifyConfirmFlow(t *testing.T) {
	e := newAIEnv(t)
	body, ct := upload(t, e.poID, doc)
	w := e.do("POST", "/api/documents/verify", "staff", body, ct)
	if w.Code != http.StatusOK {
		t.Fatalf("업로드: %d %s", w.Code, w.Body.String())
	}
	var v struct {
		ID         int64  `json:"id"`
		Outcome    string `json:"outcome"`
		Items      []struct{ Field, Result string }
		Extraction struct {
			BusinessNo string `json:"businessNo"`
		} `json:"extraction"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	if v.Outcome != "match" || len(v.Items) == 0 {
		t.Fatalf("판정: %+v", v)
	}
	if strings.Contains(w.Body.String(), "123-45-67890") {
		t.Fatalf("staff 응답에 사업자번호 원문: %s", w.Body.String())
	}
	if w := e.do("GET", "/api/documents/verdicts/"+strconv.FormatInt(v.ID, 10), "staff", nil, ""); w.Code != http.StatusOK {
		t.Fatalf("조회: %d", w.Code)
	}
	if w := e.do("GET", "/api/documents/verdicts", "staff", nil, ""); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"verdicts"`) {
		t.Fatalf("목록: %d %s", w.Code, w.Body.String())
	}
	confirm := []byte(`{"outcome":"match","comment":"확인"}`)
	path := "/api/documents/verdicts/" + strconv.FormatInt(v.ID, 10) + "/confirm"
	if w := e.do("POST", path, "staff", confirm, "application/json"); w.Code != http.StatusForbidden {
		t.Fatalf("staff 확정은 403: %d", w.Code)
	}
	if w := e.do("POST", path, "finance", confirm, "application/json"); w.Code != http.StatusOK {
		t.Fatalf("finance 확정: %d %s", w.Code, w.Body.String())
	}
	if w := e.do("POST", path, "finance", confirm, "application/json"); w.Code != http.StatusConflict {
		t.Fatalf("재확정은 409: %d", w.Code)
	}
	if !e.rec.Has("verdict.confirm", audit.TargetVerdict, audit.ResultSuccess) {
		t.Fatal("확정 감사 로그")
	}
}

func TestJSONUploadAndValidationErrors(t *testing.T) {
	e := newAIEnv(t)
	js, _ := json.Marshal(map[string]any{"filename": "a.txt", "text": doc, "poId": e.poID})
	if w := e.do("POST", "/api/documents/verify", "staff", js, "application/json"); w.Code != http.StatusOK {
		t.Fatalf("JSON 업로드: %d %s", w.Code, w.Body.String())
	}
	js, _ = json.Marshal(map[string]any{"text": doc})
	if w := e.do("POST", "/api/documents/verify", "staff", js, "application/json"); w.Code != http.StatusBadRequest {
		t.Fatalf("PO 없음은 400: %d", w.Code)
	}
	js, _ = json.Marshal(map[string]any{"text": doc, "poId": 9999})
	if w := e.do("POST", "/api/documents/verify", "staff", js, "application/json"); w.Code != http.StatusNotFound {
		t.Fatalf("없는 PO 는 404: %d", w.Code)
	}
	if w := e.do("POST", "/api/documents/verify", "auditor", js, "application/json"); w.Code != http.StatusForbidden {
		t.Fatalf("auditor 는 403: %d", w.Code)
	}
	// 깨진 PDF 는 400.
	up := func(name string, data []byte) *httptest.ResponseRecorder {
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		_ = mw.WriteField("po_id", strconv.FormatInt(e.poID, 10))
		fw, _ := mw.CreateFormFile("file", name)
		_, _ = fw.Write(data)
		_ = mw.Close()
		return e.do("POST", "/api/documents/verify", "staff", buf.Bytes(), mw.FormDataContentType())
	}
	if w := up("x.pdf", []byte("%PDF-1.4 broken")); w.Code != http.StatusBadRequest {
		t.Fatalf("깨진 PDF 는 400: %d", w.Code)
	}
	// 텍스트 PDF 는 접수되고(판정은 코드), 텍스트 레이어가 없는 PDF 는 판단 보류 + 근거 문구.
	if w := up("s.pdf", kit.TextPDF([]string{"Statement", "Total 5000"})); w.Code != http.StatusOK {
		t.Fatalf("텍스트 PDF: %d %s", w.Code, w.Body.String())
	}
	w := up("scan.pdf", kit.BlankPDF())
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"outcome":"hold"`) || !strings.Contains(w.Body.String(), "텍스트 레이어가 없는 PDF") {
		t.Fatalf("스캔본 PDF 는 보류: %d %s", w.Code, w.Body.String())
	}
}

func TestQuotaExceededIs429(t *testing.T) {
	e := newAIEnv(t)
	admin := contract.WithPrincipal(context.Background(), contract.Principal{UserID: 104, Channel: contract.ChannelWeb, Role: contract.RoleAdmin, DepartmentID: kit.DeptID})
	if err := e.store.SetQuota(admin, llmgw.Quota{ScopeType: "role", ScopeValue: "staff", DailyLimit: 2300}); err != nil {
		t.Fatal(err)
	}
	js, _ := json.Marshal(map[string]any{"text": doc, "poId": e.poID})
	if w := e.do("POST", "/api/documents/verify", "staff", js, "application/json"); w.Code != http.StatusOK {
		t.Fatalf("첫 호출: %d %s", w.Code, w.Body.String())
	}
	if w := e.do("POST", "/api/documents/verify", "staff", js, "application/json"); w.Code != http.StatusTooManyRequests {
		t.Fatalf("한도 초과는 429: %d %s", w.Code, w.Body.String())
	}
}

func TestLLMAdminRoutes(t *testing.T) {
	e := newAIEnv(t)
	if w := e.do("GET", "/api/llm/usage", "staff", nil, ""); w.Code != http.StatusForbidden {
		t.Fatalf("staff 사용량 조회 403: %d", w.Code)
	}
	if w := e.do("GET", "/api/llm/usage", "auditor", nil, ""); w.Code != http.StatusOK {
		t.Fatalf("auditor 사용량: %d", w.Code)
	}
	if w := e.do("POST", "/api/llm/policies", "auditor", []byte(`{"modelPattern":"x","allowed":true}`), "application/json"); w.Code != http.StatusForbidden {
		t.Fatalf("auditor 정책 변경 403: %d", w.Code)
	}
	w := e.do("POST", "/api/llm/policies", "admin", []byte(`{"role":"staff","modelPattern":"mock*","allowed":false}`), "application/json")
	if w.Code != http.StatusCreated {
		t.Fatalf("admin 정책 추가: %d %s", w.Code, w.Body.String())
	}
	if w := e.do("PUT", "/api/llm/quotas", "admin", []byte(`{"scopeType":"role","scopeValue":"staff","dailyTokenLimit":1000}`), "application/json"); w.Code != http.StatusOK {
		t.Fatalf("한도 설정: %d %s", w.Code, w.Body.String())
	}
	if w := e.do("PUT", "/api/llm/quotas", "admin", []byte(`{"scopeType":"x","scopeValue":"staff","dailyTokenLimit":1}`), "application/json"); w.Code != http.StatusBadRequest {
		t.Fatalf("잘못된 범위 400: %d", w.Code)
	}
	if w := e.do("GET", "/api/llm/policies", "admin", nil, ""); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "mock*") {
		t.Fatalf("정책 목록: %d %s", w.Code, w.Body.String())
	}
	if !e.rec.Has("llm_policy.add", audit.TargetLLMPolicy, audit.ResultSuccess) {
		t.Fatal("정책 변경 감사 로그")
	}
}

// 평가 이력 조회는 eval:read(관리자·감사)만 가능하고, 이력은 저장된 요약을 돌려준다.
func TestEvalRunsRoute(t *testing.T) {
	e := newAIEnv(t)
	_, err := llmgw.RecordEvalRun(context.Background(), e.d.Txs, e.rec, llmgw.EvalRun{Provider: "mock", Model: "mock-1", Total: 10, Passed: 9, PassRatePct: 90, ThresholdPct: 90,
		NewFailures: []string{"docs/x"}, Cases: []llmgw.EvalCase{{Suite: "docs", ID: "x", Detail: "판정 hold"}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, role := range []string{"staff", "manager", "finance"} {
		if w := e.do("GET", "/api/eval/runs", role, nil, ""); w.Code != http.StatusForbidden {
			t.Errorf("%s 평가 이력 403 기대: %d", role, w.Code)
		}
	}
	for _, role := range []string{"admin", "auditor"} {
		w := e.do("GET", "/api/eval/runs", role, nil, "")
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"newFailures":["docs/x"]`) || !strings.Contains(w.Body.String(), `"passRatePct":90`) {
			t.Errorf("%s 평가 이력: %d %s", role, w.Code, w.Body.String())
		}
	}
	if !e.rec.Has("eval.run", audit.TargetEval, audit.ResultSuccess) {
		t.Fatal("평가 실행 감사 로그")
	}
}
