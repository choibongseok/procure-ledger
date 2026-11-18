package service_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/choibongseok/procure-ledger/internal/apiv1"
	"github.com/choibongseok/procure-ledger/internal/changes"
	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/rbac"
	"github.com/choibongseok/procure-ledger/internal/service"
)

// B42 1단계: 연동 읽기 API(/api/v1)가 실제 서비스 위에서 범위·마스킹·쪽 나누기·변경 피드 규칙을 지키는지 본다.
// 인증은 platform 몫이라 시험용 Authenticator 가 토큰 이름으로 주체와 범위를 주입한다.

type testAuth struct {
	scopes  map[string][]string // 토큰 → 범위
	extra   []contract.Permission
	limited bool
}

func (a testAuth) AuthenticateAPI(_ context.Context, r *http.Request) (apiv1.Caller, error) {
	tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if tok == "" || a.scopes[tok] == nil {
		return apiv1.Caller{}, contract.ErrUnauthenticated
	}
	if tok == "limited" {
		return apiv1.Caller{}, apiv1.ErrRateLimited{RetryAfterSeconds: 17}
	}
	perms, err := apiv1.PermsFor(a.scopes[tok])
	if err != nil {
		return apiv1.Caller{}, err
	}
	for _, p := range a.extra {
		perms[p] = struct{}{}
	}
	return apiv1.Caller{
		Principal: contract.Principal{UserID: uFinance, Name: "API(" + tok + ")", Role: "api", Channel: contract.ChannelAPI, Auth: contract.AuthAPI, Perms: perms, TokenID: 1},
		Scopes:    a.scopes[tok],
		TokenID:   1,
	}, nil
}

func (e *env) apiServer(t *testing.T, auth testAuth) *httptest.Server {
	t.Helper()
	b := service.Base{DB: e.db, Txs: contract.SQLTxManager{DB: e.db}, Audit: e.rec, Masker: rbac.Masker{}, Org: leftAwareOrg{e.org}, Now: func() time.Time { return e.now }}
	inv := service.NewInventoryService(b)
	read := service.NewAPIReadService(b, inv, service.NewJournalService(b, service.NewApprovalDocService(b)))
	svc := apiv1.New(apiv1.Options{Read: read, DB: e.db, Auth: auth})
	rt := contract.NewRouteTable()
	rt.Guard = rbac.NewGuard(e.rec, contract.SQLTxManager{DB: e.db})
	svc.Register(rt)
	srv := httptest.NewServer(svc.Wrap(rt.Handler()))
	t.Cleanup(srv.Close)
	return srv
}

func apiGet(t *testing.T, srv *httptest.Server, tok, path string) (int, map[string]any, string, http.Header) {
	t.Helper()
	req, _ := http.NewRequest("GET", srv.URL+path, nil)
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	return res.StatusCode, m, string(raw), res.Header
}

var allScopes = []string{"items:read", "vendors:read", "purchase_orders:read", "receipts:read", "journals:read", "payables:read", "changes:read"}

func (e *env) apiAll(t *testing.T) *httptest.Server {
	return e.apiServer(t, testAuth{scopes: map[string][]string{"all": allScopes, "vend": {"vendors:read"}, "vendchg": {"vendors:read", "changes:read"}, "none": {}, "limited": {"items:read"}}})
}

func dataOf(m map[string]any) []any {
	d, _ := m["data"].([]any)
	return d
}

// 여섯 자원이 화면과 같은 서비스로 나가고, 사람 정보와 원문(계좌·사업자번호·전화)이 어디에도 없다.
func TestAPIReadResourcesMaskedAndPersonFree(t *testing.T) {
	e := newEnv(t)
	e.jvForm(t)
	v := e.vendor(t) // 계좌 110-123-456789, 사업자번호 123-45-67890, 전화 02-123-4567
	po := e.orderFor(t, v, "2026-12-01", 1_000, 5)
	e.receiveAll(t, po)
	e.payable(t, v.ID, 100_000)
	srv := e.apiAll(t)
	banned := []string{"110-123-456789", "123-45-67890", "02-123-4567", "created_by", "approved_by", "requested_by", "received_by", "employee_no", "requester", "approver", "created_at_by"}
	counts := map[string]int{}
	for _, path := range []string{"items", "vendors", "purchase-orders", "receipts", "journals", "payables"} {
		code, m, raw, h := apiGet(t, srv, "all", "/api/v1/"+path)
		if code != 200 || h.Get("X-Request-Id") == "" || !strings.Contains(h.Get("Content-Type"), "application/json") {
			t.Fatalf("%s: %d %s", path, code, raw)
		}
		for _, b := range banned {
			if strings.Contains(raw, b) {
				t.Errorf("%s 응답에 %q 가 있다: %s", path, b, raw)
			}
		}
		counts[path] = len(dataOf(m))
		if m["has_more"] != false || m["next_cursor"] != nil {
			t.Errorf("%s: 한 쪽이면 더 없음: %v %v", path, m["has_more"], m["next_cursor"])
		}
	}
	for path, n := range counts {
		if n == 0 {
			t.Errorf("%s 가 비었다", path)
		}
	}
	// 표현 확인: 품목 재고(원장 합)·발주 줄·입고 줄·전표 줄·채무 상태.
	_, m, _, _ := apiGet(t, srv, "all", "/api/v1/items")
	foundStock := false
	for _, x := range dataOf(m) {
		if it := x.(map[string]any); it["id"] == float64(po.Lines[0].ItemID) {
			foundStock = it["on_hand"] == float64(5)
		}
	}
	if !foundStock {
		t.Errorf("입고한 품목의 재고가 5 여야 한다: %v", m)
	}
	_, m, _, _ = apiGet(t, srv, "all", "/api/v1/purchase-orders")
	pol := dataOf(m)[0].(map[string]any)
	if pol["no"] != po.No || pol["status"] != "received" || len(pol["lines"].([]any)) != 1 || pol["lines"].([]any)[0].(map[string]any)["received_quantity"] != float64(5) {
		t.Errorf("발주: %v", pol)
	}
	_, m, _, _ = apiGet(t, srv, "all", "/api/v1/receipts")
	rc := dataOf(m)[0].(map[string]any)
	if rc["po_no"] != po.No || len(rc["lines"].([]any)) != 1 {
		t.Errorf("입고: %v", rc)
	}
	_, m, _, _ = apiGet(t, srv, "all", "/api/v1/journals")
	j := dataOf(m)[0].(map[string]any)
	if len(j["lines"].([]any)) < 2 || j["status"] == "" {
		t.Errorf("전표: %v", j)
	}
	_, m, _, _ = apiGet(t, srv, "all", "/api/v1/payables")
	if p := dataOf(m)[0].(map[string]any); p["status"] != "open" || p["amount"] != float64(100_000) {
		t.Errorf("채무: %v", p)
	}
	// 거래처 필드는 가려진 값이다(값이 비어 있지 않고 원문과 다르다).
	_, m, _, _ = apiGet(t, srv, "all", "/api/v1/vendors")
	vd := dataOf(m)[0].(map[string]any)
	for _, k := range []string{"business_no", "account_no", "phone"} {
		if s, _ := vd[k].(string); s == "" || strings.Contains(s, "456789") {
			t.Errorf("거래처 %s 는 가린 값이어야 한다: %q", k, s)
		}
	}
}

// 오작동한 인증 구현이 주체에 mask:bypass 를 실어 줘도 API 채널은 원문을 내지 않는다(D6).
func TestAPIChannelNeverReturnsRawEvenWithMaskBypass(t *testing.T) {
	e := newEnv(t)
	e.vendor(t)
	srv := e.apiServer(t, testAuth{scopes: map[string][]string{"vend": {"vendors:read"}}, extra: []contract.Permission{contract.PermMaskBypass}})
	code, _, raw, _ := apiGet(t, srv, "vend", "/api/v1/vendors")
	if code != 200 {
		t.Fatalf("%d %s", code, raw)
	}
	for _, s := range []string{"110-123-456789", "123-45-67890", "02-123-4567"} {
		if strings.Contains(raw, s) {
			t.Fatalf("mask:bypass 가 있어도 원문이 나가면 안 된다: %s", raw)
		}
	}
}

func TestAPIPagingWalksEveryRowOnce(t *testing.T) {
	e := newEnv(t)
	want := map[int64]bool{}
	for i := 0; i < 5; i++ {
		it := e.item(t, fmt.Sprintf("PG-%d", i), 0, false)
		want[it.ID] = true
	}
	srv := e.apiAll(t)
	got := map[int64]int{}
	path, pages := "/api/v1/items?limit=2", 0
	for path != "" {
		code, m, raw, _ := apiGet(t, srv, "all", path)
		if code != 200 {
			t.Fatalf("%d %s", code, raw)
		}
		pages++
		for _, x := range dataOf(m) {
			got[int64(x.(map[string]any)["id"].(float64))]++
		}
		if c, ok := m["next_cursor"].(string); ok && m["has_more"] == true {
			path = "/api/v1/items?limit=2&cursor=" + c
		} else {
			path = ""
		}
		if pages > 10 {
			t.Fatal("쪽 나누기가 끝나지 않는다")
		}
	}
	if pages != 3 || len(got) != 5 {
		t.Fatalf("쪽 %d, 받은 품목 %d", pages, len(got))
	}
	for id := range want {
		if got[id] != 1 {
			t.Errorf("품목 %d 를 %d 번 받음", id, got[id])
		}
	}
	// 잘못된 매개변수는 400.
	for _, p := range []string{"?limit=0", "?limit=201", "?limit=x", "?after=-1", "?cursor=zzz", "?cursor=c1.5"} {
		if code, _, _, _ := apiGet(t, srv, "all", "/api/v1/items"+p); code != 400 {
			t.Errorf("%s: %d", p, code)
		}
	}
	// after 숫자도 같은 뜻이다.
	ids := make([]int64, 0, len(want))
	for id := range want {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	_, m, _, _ := apiGet(t, srv, "all", fmt.Sprintf("/api/v1/items?after=%d", ids[3]))
	if len(dataOf(m)) != 1 {
		t.Errorf("after: %v", m)
	}
}

func TestAPIAuthAndScopes(t *testing.T) {
	e := newEnv(t)
	e.vendor(t)
	e.item(t, "SC-1", 0, false)
	srv := e.apiAll(t)
	if code, m, _, h := apiGet(t, srv, "", "/api/v1/items"); code != 401 || h.Get("WWW-Authenticate") != "Bearer" || m["error"].(map[string]any)["code"] != "unauthenticated" {
		t.Errorf("토큰 없음: %d %v", code, m)
	}
	if code, m, _, _ := apiGet(t, srv, "bogus", "/api/v1/items"); code != 401 || m["error"].(map[string]any)["code"] != "unauthenticated" {
		t.Errorf("모르는 토큰: %d", code)
	}
	if code, _, _, h := apiGet(t, srv, "limited", "/api/v1/items"); code != 429 || h.Get("Retry-After") != "17" {
		t.Errorf("한도: %d %s", code, h.Get("Retry-After"))
	}
	// 범위가 없는 자원은 403, 있는 자원은 200. 오류에 요청 번호가 있다.
	code, m, raw, _ := apiGet(t, srv, "vend", "/api/v1/items")
	if code != 403 || m["error"].(map[string]any)["code"] != "forbidden_scope" || m["error"].(map[string]any)["request_id"] == "" {
		t.Errorf("범위 밖: %d %s", code, raw)
	}
	if code, _, _, _ := apiGet(t, srv, "vend", "/api/v1/vendors"); code != 200 {
		t.Errorf("범위 안: %d", code)
	}
	if code, _, _, _ := apiGet(t, srv, "none", "/api/v1/vendors"); code != 401 && code != 403 {
		t.Errorf("범위 없는 토큰: %d", code)
	}
	// 변경 피드는 changes:read 가 있어야 한다.
	if code, _, _, _ := apiGet(t, srv, "vend", "/api/v1/changes/head"); code != 403 {
		t.Errorf("changes:read 없는 피드: %d", code)
	}
	// OpenAPI 문서는 인증 없이 나간다(값 없음).
	code, m, _, _ = apiGet(t, srv, "", "/api/v1/openapi.json")
	if code != 200 || m["openapi"] != "3.1.0" {
		t.Errorf("OpenAPI: %d", code)
	}
	// 오류 본문에 내부 정보가 없다.
	_, _, raw, _ = apiGet(t, srv, "vend", "/api/v1/items")
	for _, bad := range []string{"SELECT", "sql:", "goroutine", "scim", "ledger:read"} {
		if strings.Contains(raw, bad) {
			t.Errorf("오류 본문에 %q", bad)
		}
	}
}

// 변경 피드: head 에서 시작해 새 변경만 받고, 토큰 범위의 자원만 보이며, 만료·미래·형식 오류 커서를 거부한다.
func TestAPIChangesFeed(t *testing.T) {
	e := newEnv(t)
	srv := e.apiAll(t)
	_, m, _, _ := apiGet(t, srv, "all", "/api/v1/changes/head")
	head := m["data"].(map[string]any)["cursor"].(string)
	if !strings.HasPrefix(head, "c1.") {
		t.Fatalf("head: %v", m)
	}
	v, err := e.inventory.CreateVendor(as(uFinance), domain.Vendor{Name: "피드상사", BusinessNo: "300-00-00001"})
	if err != nil {
		t.Fatal(err)
	}
	it := e.item(t, "FEED-1", 0, false)
	code, m, raw, _ := apiGet(t, srv, "all", "/api/v1/changes?cursor="+head)
	if code != 200 {
		t.Fatalf("%d %s", code, raw)
	}
	var seen []string
	for _, x := range dataOf(m) {
		c := x.(map[string]any)
		seen = append(seen, fmt.Sprintf("%s:%v:%s", c["resource"], c["id"], c["op"]))
		if c["seq_str"] == "" || c["at"] == "" {
			t.Errorf("줄 형식: %v", c)
		}
	}
	if fmt.Sprint(seen) != fmt.Sprintf("[vendor:%d:created item:%d:created]", v.ID, it.ID) {
		t.Fatalf("변경 줄: %v", seen)
	}
	for _, banned := range []string{"피드상사", "300-00-00001", "FEED-1"} {
		if strings.Contains(raw, banned) {
			t.Errorf("피드에 값이 있다: %s", banned)
		}
	}
	next := m["next_cursor"].(string)
	// 이어 읽으면 비고, 커서는 유지된다.
	_, m, _, _ = apiGet(t, srv, "all", "/api/v1/changes?cursor="+next)
	if len(dataOf(m)) != 0 || m["next_cursor"] != next {
		t.Errorf("이어 읽기: %v", m)
	}
	// 범위: vendors:read+changes:read 토큰은 거래처 줄만 보고, 다른 자원은 요청해도 403.
	_, m, _, _ = apiGet(t, srv, "vendchg", "/api/v1/changes?cursor="+head)
	if d := dataOf(m); len(d) != 1 || d[0].(map[string]any)["resource"] != "vendor" {
		t.Errorf("범위가 거래처뿐인 토큰: %v", m)
	}
	if code, _, _, _ := apiGet(t, srv, "vendchg", "/api/v1/changes?cursor="+head+"&resource=item"); code != 403 {
		t.Errorf("범위 밖 자원 요청: %d", code)
	}
	if code, _, _, _ := apiGet(t, srv, "all", "/api/v1/changes?cursor="+head+"&resource=vendor,journal"); code != 200 {
		t.Errorf("자원 필터: %d", code)
	}
	// 형식·미래·만료.
	for _, c := range []string{"", "x", "c1.abc"} {
		if code, m, _, _ := apiGet(t, srv, "all", "/api/v1/changes?cursor="+c); code != 400 || m["error"].(map[string]any)["code"] != "invalid_argument" {
			t.Errorf("커서 %q: %d", c, code)
		}
	}
	if code, _, _, _ := apiGet(t, srv, "all", "/api/v1/changes?cursor="+changes.EncodeCursor(999999)); code != 400 {
		t.Errorf("미래 커서: %d", code)
	}
	if _, err := e.db.Exec(`INSERT INTO app_settings (key, value) VALUES ('changes.purged_through', '100000')`); err != nil {
		t.Fatal(err)
	}
	if code, m, _, _ := apiGet(t, srv, "all", "/api/v1/changes?cursor=c1.0"); code != 410 || m["error"].(map[string]any)["code"] != "cursor_expired" {
		t.Errorf("만료 커서: %d %v", code, m)
	}
}
