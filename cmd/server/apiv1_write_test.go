package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/access"
	"github.com/choibongseok/procure-ledger/internal/apitoken"
	"github.com/choibongseok/procure-ledger/internal/bootstrap"
	"github.com/choibongseok/procure-ledger/internal/changes"
	"github.com/choibongseok/procure-ledger/internal/config"
	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/deps"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/dbtest"
)

// B42 3단계: 조립된 서버에서 쓰기 API(거래처·품목 가져오기, 전표 초안)가 서비스 층을 우회하지 않는지 본다.
type writeEnv struct {
	t     *testing.T
	d     deps.Deps
	h     http.Handler
	admin contract.Principal
	tok   string // 쓰기·읽기 범위 토큰
}

func newWriteEnv(t *testing.T, scopes ...string) *writeEnv {
	t.Helper()
	dbtest.SetEnv(t)
	t.Setenv("PL_DEV_IDP", "1")
	t.Setenv("PL_LLM_PROVIDER", "mock")
	t.Setenv("PL_ACCOUNT_SECRET_KEY", base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))) // 거래처 계좌번호 봉인 키(시험용)
	cfg := config.Load()
	ctx := context.Background()
	d, closeDB, err := bootstrap.Build(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(closeDB)
	e := &writeEnv{t: t, d: d, h: handlerFor(buildRoutes(cfg, d), d)}
	var role string
	p := contract.Principal{Channel: contract.ChannelWeb, Auth: contract.AuthSession}
	if err := d.DB.QueryRowContext(ctx, `SELECT id, name, role, department_id FROM users WHERE email = 'admin@pl.test'`).Scan(&p.UserID, &p.Name, &role, &p.DepartmentID); err != nil {
		t.Fatal(err)
	}
	p.Role = contract.Role(role)
	if p.Perms, err = access.PermsOf(ctx, d.DB, p.UserID); err != nil {
		t.Fatal(err)
	}
	// 쓰기 범위는 발급자가 대응하는 사람 권한(전표 작성·가져오기)을 가져야 발급된다(D2). 시험의 발급자는 관리자 권한에 전표 작성 권한을 더한 주체다.
	p.Perms = contract.NewPermSet(append(permList(p.Perms), contract.PermLedgerWrite)...)
	e.admin = p
	if err := d.APITokens.SetEnabled(ctx, p, true); err != nil {
		t.Fatal(err)
	}
	e.tok = e.issue("쓰기 시험", scopes...)
	return e
}

func (e *writeEnv) issue(name string, scopes ...string) string {
	e.t.Helper()
	raw, _, err := e.d.APITokens.Issue(context.Background(), e.admin, apitoken.IssueInput{Name: name, Scopes: scopes})
	if err != nil {
		e.t.Fatal(err)
	}
	return raw
}

func (e *writeEnv) do(method, path, token string, body any, hdr map[string]string) *httptest.ResponseRecorder {
	e.t.Helper()
	var rd *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, rd)
	req.RemoteAddr = "127.0.0.1:5000"
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	e.h.ServeHTTP(w, req)
	return w
}

func (e *writeEnv) count(q string, args ...any) int {
	e.t.Helper()
	var n int
	if err := e.d.DB.QueryRow(q, args...).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

type dataResp[T any] struct {
	Data  T `json:"data"`
	Error struct {
		Code, Message string
	} `json:"error"`
}

func decode[T any](t *testing.T, w *httptest.ResponseRecorder) dataResp[T] {
	t.Helper()
	var r dataResp[T]
	if err := json.Unmarshal(w.Body.Bytes(), &r); err != nil {
		t.Fatalf("응답 해석: %v %s", err, w.Body.String())
	}
	return r
}

type previewData struct {
	BatchNo    string `json:"batch_no"`
	ApplyToken string `json:"apply_token"`
	Applicable bool   `json:"applicable"`
	Counts     struct{ Total, Add, Same, Change, Error int }
	Rows       []struct {
		Verdict string
		Reason  string
		Changes []struct{ Column, Old, New string }
	}
}

func TestAPIv1VendorImportThroughImportFlow(t *testing.T) {
	e := newWriteEnv(t, "vendors:write", "vendors:read", "changes:read")
	rows := []map[string]any{
		{"name": "연동상사", "business_no": "321-54-98760", "payment_days": 45, "account_no": "110-123-456789", "email": "order@yeondong.example"},
		{"name": "둘째상사", "business_no": "321-54-98761"},
	}
	before := e.count(`SELECT COUNT(*) FROM vendors`)
	headBefore, _ := changes.Head(context.Background(), e.d.DB)
	w := e.do("POST", "/api/v1/imports/vendors/preview", e.tok, map[string]any{"rows": rows}, nil)
	if w.Code != 200 {
		t.Fatalf("미리보기: %d %s", w.Code, w.Body.String())
	}
	pv := decode[previewData](t, w).Data
	if pv.Counts.Add != 2 || !pv.Applicable || pv.ApplyToken == "" || pv.BatchNo == "" {
		t.Fatalf("미리보기 결과: %+v", pv)
	}
	if e.count(`SELECT COUNT(*) FROM vendors`) != before {
		t.Fatal("미리보기는 아무것도 반영하지 않는다")
	}
	// 원문 금지(D6): 사업자번호·계좌·이메일 원문이 응답에 없다.
	for _, raw := range []string{"321-54-98760", "110-123-456789", "order@yeondong.example", "3215498760"} {
		if strings.Contains(w.Body.String(), raw) {
			t.Fatalf("미리보기 응답에 원문이 있다: %s", raw)
		}
	}
	apply := map[string]any{"apply_token": pv.ApplyToken, "rows": rows}
	// 반영 토큰이 틀리면 거부, 아무것도 반영되지 않는다.
	if bad := e.do("POST", "/api/v1/imports/vendors/batches/"+pv.BatchNo+"/apply", e.tok, map[string]any{"apply_token": "x" + pv.ApplyToken, "rows": rows}, nil); bad.Code < 400 {
		t.Fatalf("틀린 반영 토큰: %d", bad.Code)
	}
	if e.count(`SELECT COUNT(*) FROM vendors`) != before {
		t.Fatal("거부된 반영이 데이터를 바꿨다")
	}
	key := map[string]string{"Idempotency-Key": "apply-1"}
	w1 := e.do("POST", "/api/v1/imports/vendors/batches/"+pv.BatchNo+"/apply", e.tok, apply, key)
	if w1.Code != 200 {
		t.Fatalf("반영: %d %s", w1.Code, w1.Body.String())
	}
	if e.count(`SELECT COUNT(*) FROM vendors`) != before+2 {
		t.Fatal("거래처 2건이 반영되어야 한다")
	}
	// 같은 키·같은 본문의 재요청은 같은 결과(재생), 다시 반영하지 않는다.
	w2 := e.do("POST", "/api/v1/imports/vendors/batches/"+pv.BatchNo+"/apply", e.tok, apply, key)
	if w2.Code != 200 || w2.Header().Get("Idempotent-Replayed") != "true" || w2.Body.String() != w1.Body.String() {
		t.Fatalf("멱등 재생: %d replayed=%q\n%s\n%s", w2.Code, w2.Header().Get("Idempotent-Replayed"), w1.Body.String(), w2.Body.String())
	}
	if e.count(`SELECT COUNT(*) FROM vendors`) != before+2 {
		t.Fatal("재생이 다시 반영했다")
	}
	// 같은 키로 다른 본문은 409.
	if w3 := e.do("POST", "/api/v1/imports/vendors/batches/"+pv.BatchNo+"/apply", e.tok, map[string]any{"apply_token": pv.ApplyToken, "rows": rows[:1]}, key); w3.Code != http.StatusConflict {
		t.Fatalf("같은 키의 다른 본문: %d %s", w3.Code, w3.Body.String())
	}
	// 키 없이 다시 보내면 일회용 토큰이 이미 쓰였다.
	if w4 := e.do("POST", "/api/v1/imports/vendors/batches/"+pv.BatchNo+"/apply", e.tok, apply, nil); w4.Code < 400 {
		t.Fatalf("일회용 토큰 재사용: %d", w4.Code)
	}
	// 다시 미리보기: 전부 같은 행.
	again := decode[previewData](t, e.do("POST", "/api/v1/imports/vendors/preview", e.tok, map[string]any{"rows": rows}, nil)).Data
	if again.Counts.Same != 2 || again.Applicable {
		t.Fatalf("같은 파일 재실행은 동일만 나온다: %+v", again)
	}
	// 감사: 미리보기·반영(API 채널)과 공통 흐름의 반영 감사가 모두 있다. 값은 없다.
	for _, a := range []string{"api.import_preview", "api.import_apply", "import.apply"} {
		if e.count(`SELECT COUNT(*) FROM audit_logs WHERE action = ?`, a) == 0 {
			t.Errorf("감사 %s 가 없다", a)
		}
	}
	if e.count(`SELECT COUNT(*) FROM audit_logs WHERE channel = 'api' AND action = 'api.import_apply' AND (diff LIKE '%연동상사%' OR diff LIKE '%98760%')`) != 0 {
		t.Fatal("감사에 행 값이 있다")
	}
	// 변경 피드: 쓴 거래처가 actor_channel=api 줄로 남는다(웹훅에서 기본 제외되는 근거, D16).
	evs, _, _, err := changes.After(context.Background(), e.d.DB, headBefore, 100, []string{changes.ResVendor})
	if err != nil {
		t.Fatal(err)
	}
	apiLines := 0
	for _, ev := range evs {
		if ev.ActorChannel == "api" {
			apiLines++
		}
	}
	if apiLines < 2 {
		t.Fatalf("API 가 쓴 거래처 변경 줄이 api 채널로 남아야 한다: %+v", evs)
	}
}

func TestAPIv1ImportRowErrorsBlockApply(t *testing.T) {
	e := newWriteEnv(t, "items:write")
	rows := []map[string]any{{"sku": "API-OK-1", "name": "정상 품목", "unit": "EA"}, {"sku": "bad sku!", "name": "오류 품목", "unit": "EA"}}
	w := e.do("POST", "/api/v1/imports/items/preview", e.tok, map[string]any{"rows": rows}, nil)
	pv := decode[previewData](t, w).Data
	if w.Code != 200 || pv.Counts.Error != 1 || pv.Applicable || pv.ApplyToken != "" {
		t.Fatalf("오류 행이 있으면 반영할 수 없다: %d %+v", w.Code, pv)
	}
	if e.count(`SELECT COUNT(*) FROM items WHERE sku = 'API-OK-1'`) != 0 {
		t.Fatal("미리보기가 반영했다")
	}
	// 알 수 없는 열 이름은 400, 범위 밖 대상은 403.
	if bad := e.do("POST", "/api/v1/imports/items/preview", e.tok, map[string]any{"rows": []map[string]any{{"sku": "A1", "name": "x", "unit": "EA", "price": 1}}}, nil); bad.Code != 400 {
		t.Fatalf("알 수 없는 열: %d %s", bad.Code, bad.Body.String())
	}
	if forb := e.do("POST", "/api/v1/imports/vendors/preview", e.tok, map[string]any{"rows": []map[string]any{{"name": "x"}}}, nil); forb.Code != 403 {
		t.Fatalf("범위 밖: %d", forb.Code)
	}
	if e.count(`SELECT COUNT(*) FROM audit_logs WHERE channel = 'api' AND action = 'api.import_preview' AND result = 'error'`) == 0 {
		t.Fatal("실패한 쓰기도 감사에 남아야 한다")
	}
}

type draftData struct {
	No          string `json:"no"`
	Status      string `json:"status"`
	TotalAmount int64  `json:"total_amount"`
}

func draftBody(ref string, amount int64) map[string]any {
	return map[string]any{"posting_date": "2026-11-10", "memo": "연동 시험 전표", "external_ref": ref,
		"lines": []map[string]any{{"account_code": "5220", "side": "D", "amount": amount}, {"account_code": "1010", "side": "C", "amount": amount}}}
}

func TestAPIv1JournalDraftIsDraftOnlyAndIdempotent(t *testing.T) {
	e := newWriteEnv(t, "journals:draft", "journals:read", "changes:read")
	w := e.do("POST", "/api/v1/journals", e.tok, draftBody("ERP-1001", 50000), map[string]string{"Idempotency-Key": "k1"})
	if w.Code != http.StatusCreated {
		t.Fatalf("초안 제출: %d %s", w.Code, w.Body.String())
	}
	dr := decode[draftData](t, w).Data
	if dr.Status != "draft" || dr.TotalAmount != 50000 || dr.No == "" {
		t.Fatalf("초안: %+v", dr)
	}
	var createdBy int64
	var source string
	if err := e.d.DB.QueryRow(`SELECT created_by, source_type FROM journals WHERE no = ?`, dr.No).Scan(&createdBy, &source); err != nil {
		t.Fatal(err)
	}
	if createdBy != e.admin.UserID || source != "api" {
		t.Fatalf("작성자는 토큰 발급자, 출처는 api: %d %s", createdBy, source)
	}
	// 같은 external_ref·같은 내용: 같은 전표(200). 다른 내용: 409.
	again := e.do("POST", "/api/v1/journals", e.tok, draftBody("ERP-1001", 50000), nil)
	if again.Code != 200 || decode[draftData](t, again).Data.No != dr.No {
		t.Fatalf("같은 external_ref 는 같은 전표: %d %s", again.Code, again.Body.String())
	}
	if diff := e.do("POST", "/api/v1/journals", e.tok, draftBody("ERP-1001", 60000), nil); diff.Code != http.StatusConflict {
		t.Fatalf("같은 external_ref 다른 내용: %d %s", diff.Code, diff.Body.String())
	}
	if e.count(`SELECT COUNT(*) FROM journals WHERE source_type = 'api'`) != 1 {
		t.Fatal("전표는 한 건만 있어야 한다")
	}
	// Idempotency-Key 재생.
	rp := e.do("POST", "/api/v1/journals", e.tok, draftBody("ERP-1001", 50000), map[string]string{"Idempotency-Key": "k1"})
	if rp.Code != http.StatusCreated || rp.Header().Get("Idempotent-Replayed") != "true" || rp.Body.String() != w.Body.String() {
		t.Fatalf("멱등 재생: %d %s", rp.Code, rp.Body.String())
	}
	// 균형이 안 맞거나 알 수 없는 계정은 400, 마감된 달은 409 이고 아무것도 남지 않는다.
	bad := draftBody("ERP-1002", 100)
	bad["lines"] = []map[string]any{{"account_code": "5220", "side": "D", "amount": 100}, {"account_code": "1010", "side": "C", "amount": 90}}
	if w := e.do("POST", "/api/v1/journals", e.tok, bad, nil); w.Code != 400 || !strings.Contains(w.Body.String(), "차변 합계와 대변 합계가 같지 않습니다") {
		t.Fatalf("차대 불균형 초안은 400: %d %s", w.Code, w.Body.String()) // 화면의 일반 임시 저장과 달리 연동 초안은 균형이 맞아야 한다(명세 10.3)
	}
	unk := draftBody("ERP-1003", 100)
	unk["lines"] = []map[string]any{{"account_code": "9999", "side": "D", "amount": 100}, {"account_code": "1010", "side": "C", "amount": 100}}
	if w := e.do("POST", "/api/v1/journals", e.tok, unk, nil); w.Code != 400 {
		t.Fatalf("알 수 없는 계정: %d %s", w.Code, w.Body.String())
	}
	if _, err := e.d.DB.Exec(`INSERT INTO month_closes (month, status, version, closed_by, closed_at) VALUES ('2020-01','closed',1,?,'2020-02-01T00:00:00Z')`, e.admin.UserID); err != nil {
		t.Fatal(err)
	}
	closed := draftBody("ERP-1004", 100)
	closed["posting_date"] = "2020-01-15"
	n := e.count(`SELECT COUNT(*) FROM journals`)
	if w := e.do("POST", "/api/v1/journals", e.tok, closed, nil); w.Code < 400 {
		t.Fatalf("마감된 달: %d %s", w.Code, w.Body.String())
	}
	if e.count(`SELECT COUNT(*) FROM journals`) != n {
		t.Fatal("마감된 달 거부가 전표를 남겼다")
	}
	if e.count(`SELECT COUNT(*) FROM audit_logs WHERE channel = 'api' AND action = 'api.journal_draft' AND result = 'denied'`) == 0 {
		t.Fatal("마감된 달 거부가 감사에 남아야 한다")
	}
	// 승인 경로는 API 에 없다.
	for _, p := range []string{"/api/v1/journals/" + dr.No + "/approve", "/api/v1/journals/" + dr.No + "/request-approval", "/api/v1/journals/" + dr.No + "/cancel"} {
		if w := e.do("POST", p, e.tok, map[string]any{}, nil); w.Code != 404 && w.Code != 405 {
			t.Fatalf("API 에는 승인·요청·취소 경로가 없다: %s → %d", p, w.Code)
		}
	}
	// 이 초안은 보통 임시 전표와 같은 서비스 경로를 탄다: 재무(ledger:write)가 화면과 같은 함수로 고칠 수 있고, 고친 전표는 API 출처 표시를 유지한 채 임시 상태다.
	ctx := context.Background()
	var fin contract.Principal
	var role string
	if err := e.d.DB.QueryRow(`SELECT id, name, role, department_id FROM users WHERE email = 'finance@pl.test'`).Scan(&fin.UserID, &fin.Name, &role, &fin.DepartmentID); err != nil {
		t.Fatal(err)
	}
	fin.Role, fin.Channel, fin.Auth = contract.Role(role), contract.ChannelWeb, contract.AuthSession
	var err error
	if fin.Perms, err = access.PermsOf(ctx, e.d.DB, fin.UserID); err != nil {
		t.Fatal(err)
	}
	upd := domain.JournalInput{PostingDate: "2026-11-10", Memo: "재무가 고친 전표", Lines: []domain.JournalLineInput{{AccountCode: "5220", Side: "D", Amount: 50000}, {AccountCode: "1010", Side: "C", Amount: 50000}}}
	if j, err := e.d.Journals.Update(contract.WithPrincipal(ctx, fin), dr.No, upd); err != nil || j.Status != "draft" || j.SourceType != "api" {
		t.Fatalf("API 초안은 일반 임시 전표 경로를 탄다: %v %+v", err, j)
	}
}

func TestAPIv1WriteScopesAndRateLimit(t *testing.T) {
	e := newWriteEnv(t, "vendors:write")
	// 전표 초안 범위가 없으면 403.
	if w := e.do("POST", "/api/v1/journals", e.tok, draftBody("X-1", 100), nil); w.Code != 403 {
		t.Fatalf("범위 밖: %d", w.Code)
	}
	// 읽기 토큰으로는 쓸 수 없다.
	ro := e.issue("읽기만", "vendors:read", "journals:read")
	if w := e.do("POST", "/api/v1/imports/vendors/preview", ro, map[string]any{"rows": []map[string]any{{"name": "x"}}}, nil); w.Code != 403 {
		t.Fatalf("읽기 토큰으로 쓰기: %d", w.Code)
	}
	// 분당 쓰기 한도: 2회까지, 셋째는 429 + Retry-After. 읽기 한도와 따로다.
	if _, err := e.d.DB.Exec(`UPDATE app_settings SET value = '2' WHERE key = 'api.write_per_min'`); err != nil {
		t.Fatal(err)
	}
	rows := map[string]any{"rows": []map[string]any{{"name": "한도 시험", "business_no": "321-54-98770"}}}
	for i := 0; i < 2; i++ {
		if w := e.do("POST", "/api/v1/imports/vendors/preview", e.tok, rows, nil); w.Code != 200 {
			t.Fatalf("한도 안 %d: %d %s", i, w.Code, w.Body.String())
		}
	}
	w := e.do("POST", "/api/v1/imports/vendors/preview", e.tok, rows, nil)
	if w.Code != http.StatusTooManyRequests || w.Header().Get("Retry-After") == "" {
		t.Fatalf("쓰기 한도 초과: %d %v", w.Code, w.Header())
	}
	if e.count(`SELECT COUNT(*) FROM audit_logs WHERE action = 'api.rate_limited' AND channel = 'api'`) != 1 {
		t.Fatal("첫 거부만 감사에 남는다")
	}
	if w := e.do("POST", "/api/v1/imports/vendors/preview", e.tok, rows, nil); w.Code != http.StatusTooManyRequests || e.count(`SELECT COUNT(*) FROM audit_logs WHERE action = 'api.rate_limited' AND channel = 'api'`) != 1 {
		t.Fatal("이어지는 거부는 창 요약으로 센다(감사 1행)")
	}
	// 읽기 호출은 영향이 없다.
	if r := e.do("GET", "/api/v1/vendors", e.issue("읽기", "vendors:read"), nil, nil); r.Code != 200 {
		t.Fatalf("읽기는 쓰기 한도와 따로: %d", r.Code)
	}
}

// 쓰기 요청 본문 검증: 상한을 넘거나 JSON 이 아니면 400·413, 키 형식 오류는 400.
func TestAPIv1WriteBodyChecks(t *testing.T) {
	e := newWriteEnv(t, "vendors:write", "journals:draft")
	if w := e.do("POST", "/api/v1/journals", e.tok, nil, nil); w.Code != 400 {
		t.Fatalf("빈 본문: %d", w.Code)
	}
	if w := e.do("POST", "/api/v1/journals", e.tok, draftBody("R-1", 10), map[string]string{"Idempotency-Key": "bad key!"}); w.Code != 400 {
		t.Fatalf("키 형식: %d", w.Code)
	}
	big := bytes.Repeat([]byte("x"), 6<<20)
	req := httptest.NewRequest("POST", "/api/v1/journals", bytes.NewReader(big))
	req.RemoteAddr = "127.0.0.1:5000"
	req.Header.Set("Authorization", "Bearer "+e.tok)
	w := httptest.NewRecorder()
	e.h.ServeHTTP(w, req)
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("본문 상한: %d", w.Code)
	}
}

// 피드 완전성: API 로 쓴 거래처·전표 초안이 변경 피드에 한 줄도 빠지지 않고 api 채널 줄로 나타난다(커서를 따라 읽어 확인한다).
func TestAPIv1FeedCompletenessIncludesWrites(t *testing.T) {
	e := newWriteEnv(t, "vendors:write", "journals:draft", "vendors:read", "journals:read", "changes:read")
	type head struct {
		Data struct{ Cursor string } `json:"data"`
	}
	var h head
	_ = json.Unmarshal(e.do("GET", "/api/v1/changes/head", e.tok, nil, nil).Body.Bytes(), &h)
	cursor := h.Data.Cursor
	var journalNos []string
	for i := 0; i < 4; i++ {
		w := e.do("POST", "/api/v1/journals", e.tok, draftBody("FEED-"+string(rune('A'+i)), int64(1000*(i+1))), nil)
		if w.Code != 201 {
			t.Fatalf("초안 %d: %d %s", i, w.Code, w.Body.String())
		}
		journalNos = append(journalNos, decode[draftData](t, w).Data.No)
	}
	rows := []map[string]any{{"name": "피드상사 1", "business_no": "321-54-98780"}, {"name": "피드상사 2", "business_no": "321-54-98781"}}
	pv := decode[previewData](t, e.do("POST", "/api/v1/imports/vendors/preview", e.tok, map[string]any{"rows": rows}, nil)).Data
	if w := e.do("POST", "/api/v1/imports/vendors/batches/"+pv.BatchNo+"/apply", e.tok, map[string]any{"apply_token": pv.ApplyToken, "rows": rows}, nil); w.Code != 200 {
		t.Fatalf("반영: %d %s", w.Code, w.Body.String())
	}
	type line struct {
		Resource string `json:"resource"`
		ID       int64  `json:"id"`
		Op       string `json:"op"`
	}
	var got []line
	for page := 0; page < 20; page++ {
		w := e.do("GET", "/api/v1/changes?limit=3&cursor="+cursor, e.tok, nil, nil)
		if w.Code != 200 {
			t.Fatalf("피드: %d %s", w.Code, w.Body.String())
		}
		var r struct {
			Data       []line `json:"data"`
			NextCursor string `json:"next_cursor"`
			HasMore    bool   `json:"has_more"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &r); err != nil {
			t.Fatal(err)
		}
		got = append(got, r.Data...)
		cursor = r.NextCursor
		if !r.HasMore {
			break
		}
	}
	// DB 의 api 채널 줄 수와 API 가 돌려준 줄 수가 같다(한 줄도 빠지지 않는다).
	want := e.count(`SELECT COUNT(*) FROM change_events WHERE actor_channel = 'api' AND resource IN ('journal','vendor') AND seq > ?`, mustSeq(t, h.Data.Cursor))
	if len(got) != want || want < 6 {
		t.Fatalf("피드 줄 수 %d, DB 의 api 줄 수 %d: %+v", len(got), want, got)
	}
	for _, no := range journalNos {
		var id int64
		if err := e.d.DB.QueryRow(`SELECT id FROM journals WHERE no = ?`, no).Scan(&id); err != nil {
			t.Fatal(err)
		}
		found := false
		for _, l := range got {
			found = found || (l.Resource == "journal" && l.ID == id && l.Op == "created")
		}
		if !found {
			t.Fatalf("전표 %s 의 생성 줄이 없다", no)
		}
	}
}

func mustSeq(t *testing.T, cursor string) int64 {
	t.Helper()
	n, err := changes.DecodeCursor(cursor)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func permList(s contract.PermSet) []contract.Permission {
	var out []contract.Permission
	for k := range s {
		out = append(out, k)
	}
	return out
}
