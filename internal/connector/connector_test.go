package connector

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/choibongseok/procure-ledger/internal/connectortest"
)

func TestParseRejectsTokensUnknownTransformsAndFields(t *testing.T) {
	base := `{"version":1,"name":"t","direction":"in","target":"vendors","source":{"kind":"csv","path":"a.csv"},"fields":[{"to":"name","from":"N"}]%s}`
	if _, err := Parse([]byte(strings.Replace(base, "%s", "", 1))); err != nil {
		t.Fatalf("기본 매핑: %v", err)
	}
	bad := map[string]string{
		"토큰 모양(pli_)":      `,"note":"pli_abcdefghijklmnopqrstuv"`,
		"토큰 모양(Bearer)":    `,"note":"Bearer abcdefghijkl"`,
		"알 수 없는 항목(코드 실행)": `,"script":"rm -rf /"`,
	}
	for why, extra := range bad {
		if _, err := Parse([]byte(strings.Replace(base, "%s", extra, 1))); err == nil {
			t.Errorf("거부되어야 한다: %s", why)
		}
	}
	cases := map[string]string{
		"알 수 없는 변환":     `{"version":1,"name":"t","direction":"in","target":"vendors","source":{"kind":"csv","path":"a"},"fields":[{"to":"name","from":"N","transform":"eval"}]}`,
		"알 수 없는 도착 열":   `{"version":1,"name":"t","direction":"in","target":"vendors","source":{"kind":"csv","path":"a"},"fields":[{"to":"price","from":"N"}]}`,
		"방향 없음":         `{"version":1,"name":"t","target":"vendors","fields":[{"to":"name","from":"N"}]}`,
		"버전":            `{"version":2,"name":"t","direction":"in","target":"vendors","source":{"kind":"csv","path":"a"},"fields":[{"to":"name","from":"N"}]}`,
		"전표 필수 열 없음":    `{"version":1,"name":"t","direction":"in","target":"journals","source":{"kind":"csv","path":"a"},"fields":[{"to":"memo","from":"N"}]}`,
		"out 알 수 없는 필드": `{"version":1,"name":"t","direction":"out","target":"vendors","sink":{"kind":"csv","path":"o"},"fields":[{"to":"x","from":"secret"}]}`,
		"date 형식 없음":    `{"version":1,"name":"t","direction":"in","target":"vendors","source":{"kind":"csv","path":"a"},"fields":[{"to":"name","from":"N","transform":"date_ymd"}]}`,
	}
	for why, doc := range cases {
		if _, err := Parse([]byte(doc)); err == nil {
			t.Errorf("거부되어야 한다: %s", why)
		}
	}
}

func TestTransformsClosedList(t *testing.T) {
	ok := []struct {
		f    Field
		in   string
		want string
	}{
		{Field{Transform: "trim"}, "  a b ", "a b"},
		{Field{Transform: "upper"}, "abc", "ABC"},
		{Field{Transform: "int"}, " 1,234 ", "1234"},
		{Field{Transform: "digits_to_dashed_biz"}, "123 45 67890", "123-45-67890"},
		{Field{Transform: "date_ymd", Format: "YYYYMMDD"}, "20261110", "2026-11-10"},
		{Field{Transform: "date_ymd", Format: "DD/MM/YYYY"}, "10/11/2026", "2026-11-10"},
		{Field{Transform: "map", Map: map[string]string{"A": "1"}}, "A", "1"},
		{Field{Transform: "const", Value: "x"}, "ignored", "x"},
		{Field{Transform: "int", Default: "30"}, "", "30"},
	}
	for _, c := range ok {
		if got, err := c.f.Apply(c.in); err != nil || got != c.want {
			t.Errorf("%s(%q) = %q %v, want %q", c.f.Transform, c.in, got, err, c.want)
		}
	}
	for _, c := range []struct {
		f  Field
		in string
	}{{Field{Transform: "int"}, "12a"}, {Field{Transform: "digits_to_dashed_biz"}, "123"}, {Field{Transform: "date_ymd", Format: "YYYYMMDD"}, "2026-11-10"},
		{Field{Transform: "map", Map: map[string]string{"A": "1"}}, "B"}, {Field{Required: true}, "  "}} {
		_, err := c.f.Apply(c.in)
		if err == nil {
			t.Errorf("오류여야 한다: %+v %q", c.f, c.in)
		} else if strings.Contains(err.Error(), c.in) && strings.TrimSpace(c.in) != "" {
			t.Errorf("오류 문장에 값이 들어갔다: %v", err)
		}
	}
}

// ---- 가짜 ERP API(connector 의 호출 방식만 본다. 실제 서버 위의 시험은 cmd/server 에 있다) ----

type fakeAPI struct {
	srv       *httptest.Server
	previews  int
	applies   int
	journals  []map[string]any
	idemKeys  []string
	tooMany   int // 처음 N번의 POST 에 429
	rejectRow int // 미리보기가 오류로 돌려줄 행 번호(0 이면 없음)
}

func newFakeAPI(t *testing.T) *fakeAPI {
	f := &fakeAPI{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer pli_test_token_value_0001" {
			w.WriteHeader(401)
			return
		}
		if r.Method == "POST" && f.tooMany > 0 {
			f.tooMany--
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(429)
			return
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "/preview"):
			f.previews++
			var body struct{ Rows []map[string]string }
			_ = json.NewDecoder(r.Body).Decode(&body)
			resp := map[string]any{"data": map[string]any{"batch_no": "IMP-1", "apply_token": "tok", "applicable": true, "counts": map[string]int{"total": len(body.Rows), "add": len(body.Rows)}, "rows": []any{}}}
			if f.rejectRow > 0 {
				resp = map[string]any{"data": map[string]any{"batch_no": "IMP-1", "applicable": false, "counts": map[string]int{"total": len(body.Rows), "error": 1},
					"rows": []map[string]any{{"row_no": f.rejectRow, "verdict": "error", "reason": "이름: 필수 값입니다"}}}}
			}
			_ = json.NewEncoder(w).Encode(resp)
		case strings.Contains(r.URL.Path, "/apply"):
			f.applies++
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"batch_no": "IMP-1", "status": "applied", "counts": map[string]int{"total": 2}}})
		case r.URL.Path == "/api/v1/journals" && r.Method == "POST":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.journals = append(f.journals, body)
			f.idemKeys = append(f.idemKeys, r.Header.Get("Idempotency-Key"))
			w.WriteHeader(201)
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"no": "JV-1"}})
		case r.URL.Path == "/api/v1/vendors":
			after := r.URL.Query().Get("after")
			if after == "0" {
				_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{{"id": 1, "name": "가나", "business_no": "***-**-67890"}, {"id": 2, "name": "다라", "business_no": ""}}, "next_cursor": nil, "has_more": false})
			} else {
				_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{}, "next_cursor": nil, "has_more": false})
			}
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeAPI) client() *Client {
	return &Client{Base: f.srv.URL, Token: "pli_test_token_value_0001", Sleep: func(time.Duration) {}}
}

func writeFiles(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

const vendorMapping = `{"version":1,"name":"v-in","direction":"in","target":"vendors","source":{"kind":"csv","path":"v.csv"},"fields":[
 {"to":"name","from":"N","required":true,"trim":true},{"to":"business_no","from":"B","transform":"digits_to_dashed_biz"},{"to":"payment_days","from":"T","transform":"int","default":"30"}]}`

func TestInVendorsPreviewThenApplyAndDryRun(t *testing.T) {
	api := newFakeAPI(t)
	dir := writeFiles(t, map[string]string{"m.json": vendorMapping, "v.csv": "N,B,T\n가나,1234567890,\n다라,2345678901,45\n"})
	m, err := Load(filepath.Join(dir, "m.json"))
	if err != nil {
		t.Fatal(err)
	}
	res, err := Run(context.Background(), Options{Mapping: m, BaseDir: dir, Client: api.client(), DryRun: true})
	if err != nil || res.Code != CodeDryRun || api.previews != 1 || api.applies != 0 {
		t.Fatalf("--dry-run 은 미리보기만: %+v %v previews=%d applies=%d", res, err, api.previews, api.applies)
	}
	res, err = Run(context.Background(), Options{Mapping: m, BaseDir: dir, Client: api.client()})
	if err != nil || res.Code != CodeOK || res.OK != 2 || api.applies != 1 {
		t.Fatalf("반영: %+v %v applies=%d", res, err, api.applies)
	}
}

func TestInRowErrorsStopBeforeAnyCallAndApiRowErrorsBlockApply(t *testing.T) {
	api := newFakeAPI(t)
	dir := writeFiles(t, map[string]string{"m.json": vendorMapping, "v.csv": "N,B,T\n가나,12345,\n"}) // 사업자번호 자릿수 오류
	m, _ := Load(filepath.Join(dir, "m.json"))
	res, err := Run(context.Background(), Options{Mapping: m, BaseDir: dir, Client: api.client()})
	if err != nil || res.Code != CodeRowErrors || api.previews != 0 {
		t.Fatalf("변환 오류는 호출 전에 멈춘다: %+v %v previews=%d", res, err, api.previews)
	}
	if len(res.Messages) != 1 || !strings.Contains(res.Messages[0], "행 2") || strings.Contains(res.Messages[0], "12345") {
		t.Fatalf("행 번호와 사유만(값 없음): %v", res.Messages)
	}
	dir2 := writeFiles(t, map[string]string{"m.json": vendorMapping, "v.csv": "N,B,T\n가나,1234567890,\n다라,2345678901,45\n"})
	m2, _ := Load(filepath.Join(dir2, "m.json"))
	api.rejectRow = 3
	res, err = Run(context.Background(), Options{Mapping: m2, BaseDir: dir2, Client: api.client()})
	if err != nil || res.Code != CodeRowErrors || api.applies != 0 || !strings.Contains(strings.Join(res.Messages, ""), "행 3") {
		t.Fatalf("API 가 오류 행을 돌려주면 반영하지 않는다: %+v %v", res, err)
	}
}

func TestInSkipModeDropsBadRowsAndRetriesOn429(t *testing.T) {
	api := newFakeAPI(t)
	api.tooMany = 2
	doc := strings.Replace(vendorMapping, `"fields"`, `"on_error":"skip","fields"`, 1)
	dir := writeFiles(t, map[string]string{"m.json": doc, "v.csv": "N,B,T\n가나,12345,\n다라,2345678901,45\n"})
	m, err := Load(filepath.Join(dir, "m.json"))
	if err != nil {
		t.Fatal(err)
	}
	res, err := Run(context.Background(), Options{Mapping: m, BaseDir: dir, Client: api.client()})
	if err != nil || res.Failed != 1 || res.OK != 1 || api.applies != 1 {
		t.Fatalf("skip: 오류 행을 빼고 나머지를 보낸다: %+v %v", res, err)
	}
}

func TestInJournalsGroupByRefWithIdempotencyKeys(t *testing.T) {
	api := newFakeAPI(t)
	mapping := `{"version":1,"name":"j-in","direction":"in","target":"journals","source":{"kind":"csv","path":"j.csv"},"fields":[
	 {"to":"external_ref","from":"D","required":true},{"to":"posting_date","from":"P","transform":"date_ymd","format":"YYYYMMDD","required":true},{"to":"memo","from":"M","required":true},
	 {"to":"account_code","from":"A","required":true},{"to":"side","from":"S","transform":"map","map":{"DR":"D","CR":"C"},"required":true},{"to":"amount","from":"X","transform":"int","required":true}]}`
	dir := writeFiles(t, map[string]string{"m.json": mapping, "j.csv": "D,P,M,A,S,X\nA1,20261110,적요,5220,DR,\"1,000\"\nA1,20261110,적요,1010,CR,1000\nA2,20261111,둘째,5220,DR,500\nA2,20261111,둘째,1010,CR,500\n"})
	m, err := Load(filepath.Join(dir, "m.json"))
	if err != nil {
		t.Fatal(err)
	}
	res, err := Run(context.Background(), Options{Mapping: m, BaseDir: dir, Client: api.client()})
	if err != nil || res.OK != 2 || len(api.journals) != 2 {
		t.Fatalf("전표 2건: %+v %v", res, err)
	}
	if lines, _ := api.journals[0]["lines"].([]any); len(lines) != 2 || api.journals[0]["external_ref"] != "A1" {
		t.Fatalf("같은 external_ref 의 줄은 한 전표: %+v", api.journals[0])
	}
	// 같은 파일을 다시 실행해도 같은 키로 간다(서버가 멱등으로 받는다).
	before := append([]string(nil), api.idemKeys...)
	if _, err := Run(context.Background(), Options{Mapping: m, BaseDir: dir, Client: api.client()}); err != nil {
		t.Fatal(err)
	}
	if api.idemKeys[2] != before[0] || api.idemKeys[3] != before[1] || before[0] == before[1] {
		t.Fatalf("재실행은 같은 멱등 키: %v", api.idemKeys)
	}
	// 토큰 환경변수가 틀리면 401 오류로 끝난다.
	bad := &Client{Base: api.srv.URL, Token: "pli_wrong", Sleep: func(time.Duration) {}}
	if res, err := Run(context.Background(), Options{Mapping: m, BaseDir: dir, Client: bad}); err == nil || res.Code != CodeFailed {
		t.Fatalf("인증 실패: %+v %v", res, err)
	}
}

func TestOutCSVWithStateFileAndRESTSinkSafety(t *testing.T) {
	api := newFakeAPI(t)
	csvMap := `{"version":1,"name":"v-out","direction":"out","target":"vendors","sink":{"kind":"csv","path":"out/v.csv"},"fields":[{"to":"Id","from":"id"},{"to":"Name","from":"name","transform":"upper"},{"to":"Src","transform":"const","value":"pl"}]}`
	dir := writeFiles(t, map[string]string{"m.json": csvMap})
	m, err := Load(filepath.Join(dir, "m.json"))
	if err != nil {
		t.Fatal(err)
	}
	res, err := Run(context.Background(), Options{Mapping: m, BaseDir: dir, Client: api.client()})
	if err != nil || res.OK != 2 {
		t.Fatalf("out csv: %+v %v", res, err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "out", "v.csv"))
	if !strings.Contains(string(b), "Id,Name,Src") || !strings.Contains(string(b), "1,가나,pl") {
		t.Fatalf("CSV: %s", b)
	}
	st, _ := os.ReadFile(filepath.Join(dir, "connector_state.json"))
	if !strings.Contains(string(st), `"v-out": 2`) {
		t.Fatalf("마지막 id 상태: %s", st)
	}
	// 두 번째 실행은 새 항목이 없다(증분).
	if res, err := Run(context.Background(), Options{Mapping: m, BaseDir: dir, Client: api.client()}); err != nil || res.Total != 0 {
		t.Fatalf("증분: %+v %v", res, err)
	}
	// REST sink: 허용 안 된 주소는 거부, --allow-local 이면 모의 수신 서버로 인증 값(환경변수)과 함께 간다. 실패하면 상태를 바꾸지 않는다.
	recv := connectortest.New()
	defer recv.Close()
	restMap := `{"version":1,"name":"v-rest","direction":"out","target":"vendors","sink":{"kind":"rest","url":"` + recv.URL() + `/in","auth_env":"CONN_TEST_KEY","auth_scheme":"Bearer "},"fields":[{"to":"id","from":"id"},{"to":"name","from":"name"}]}`
	dir2 := writeFiles(t, map[string]string{"m.json": restMap})
	m2, err := Load(filepath.Join(dir2, "m.json"))
	if err != nil {
		t.Fatal(err)
	}
	getenv := func(k string) string {
		if k == "CONN_TEST_KEY" {
			return "k-123"
		}
		return ""
	}
	if _, err := Run(context.Background(), Options{Mapping: m2, BaseDir: dir2, Client: api.client(), Getenv: getenv}); err == nil {
		t.Fatal("루프백 http 주소는 --allow-local 없이 거부되어야 한다")
	}
	if len(recv.Requests()) != 0 {
		t.Fatal("거부된 주소로 요청이 나갔다")
	}
	recv.SetStatus(500)
	if _, err := Run(context.Background(), Options{Mapping: m2, BaseDir: dir2, Client: api.client(), Getenv: getenv, AllowLocal: true}); err == nil {
		t.Fatal("수신 서버 오류면 실패해야 한다")
	}
	if _, err := os.Stat(filepath.Join(dir2, "connector_state.json")); err == nil {
		t.Fatal("실패한 실행이 상태 파일을 바꿨다")
	}
	recv.SetStatus(200)
	if res, err := Run(context.Background(), Options{Mapping: m2, BaseDir: dir2, Client: api.client(), Getenv: getenv, AllowLocal: true}); err != nil || res.OK != 2 {
		t.Fatalf("REST 전송: %+v %v", res, err)
	}
	reqs := recv.Requests()
	if got := reqs[len(reqs)-1]; got.Auth != "Bearer k-123" || len(got.Rows) != 2 || got.Rows[0]["name"] != "가나" {
		t.Fatalf("수신 내용: %+v", got)
	}
	// 인증 환경변수가 비면 보내지 않는다.
	if _, err := Run(context.Background(), Options{Mapping: m2, BaseDir: dir2, Client: api.client(), Getenv: func(string) string { return "" }, AllowLocal: true}); err == nil {
		t.Fatal("인증 환경변수가 비면 거부")
	}
}

// --dry-run 도 실전송과 같은 주소·인증 검사를 받는다: 허용되지 않은 주소나 비어 있는 인증 환경변수는 시험 삼아서도 통과하지 않고, 어떤 요청도 나가지 않는다.
func TestOutDryRunAppliesSameRESTSinkChecks(t *testing.T) {
	api := newFakeAPI(t)
	recv := connectortest.New()
	defer recv.Close()
	mk := func(url, extra string) (*Mapping, string) {
		doc := `{"version":1,"name":"v-rest","direction":"out","target":"vendors","sink":{"kind":"rest","url":"` + url + `"` + extra + `},"fields":[{"to":"id","from":"id"}]}`
		dir := writeFiles(t, map[string]string{"m.json": doc})
		m, err := Load(filepath.Join(dir, "m.json"))
		if err != nil {
			t.Fatal(err)
		}
		return m, dir
	}
	run := func(m *Mapping, dir string, local bool, getenv func(string) string) (Result, error) {
		return Run(context.Background(), Options{Mapping: m, BaseDir: dir, Client: api.client(), DryRun: true, AllowLocal: local, Getenv: getenv})
	}
	// 루프백 http 는 --allow-local 없이는 거부(실전송과 같다).
	m, dir := mk(recv.URL()+"/in", "")
	if _, err := run(m, dir, false, nil); err == nil {
		t.Fatal("dry-run 도 허용되지 않은 주소를 거부해야 한다")
	}
	// IP 호스트·http(비로컬)·사용자 정보는 --allow-local 이어도 거부.
	for _, bad := range []string{"http://example.com/in", "https://10.0.0.5/in", "https://user" + ":pw@example.com/in"} {
		m, dir := mk(bad, "")
		if _, err := run(m, dir, true, nil); err == nil {
			t.Errorf("거부되어야 한다: %s", bad)
		}
	}
	// 인증 환경변수가 비면 거부, 있으면 통과하고 수신 서버에는 아무 요청도 가지 않는다.
	m, dir = mk(recv.URL()+"/in", `,"auth_env":"CONN_DRY_KEY"`)
	if _, err := run(m, dir, true, func(string) string { return "" }); err == nil {
		t.Fatal("dry-run 에서도 인증 환경변수가 비면 거부해야 한다")
	}
	res, err := run(m, dir, true, func(string) string { return "k" })
	if err != nil || res.Code != CodeDryRun {
		t.Fatalf("허용된 주소의 dry-run: %+v %v", res, err)
	}
	if len(recv.Requests()) != 0 {
		t.Fatal("dry-run 은 아무것도 보내지 않는다")
	}
}
