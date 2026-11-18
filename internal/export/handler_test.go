package export_test

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/export"
	"github.com/choibongseok/procure-ledger/internal/privacy"
)

type fakeTx struct{ contract.Tx }

type fakeTxs struct{}

func (fakeTxs) WithTx(ctx context.Context, fn func(tx contract.Tx) error) error { return fn(fakeTx{}) }

type recorder struct {
	entries []audit.Entry
	fail    bool
}

func (r *recorder) Record(_ context.Context, _ contract.Tx, e audit.Entry) error {
	if r.fail {
		return errors.New("감사 기록 실패")
	}
	r.entries = append(r.entries, e)
	return nil
}

type source struct {
	n       int
	rows    [][]export.Cell
	err     error
	gotRaw  bool
	more    bool
	sensCol bool
	// noPrivacySpec 이면 민감 열이 있는데 개인정보 조회 기록 명세를 선언하지 않은 데이터 세트다.
	noPrivacySpec bool
	// serviceNotes 이면 원문을 만드는 서비스 층이 기록하는 데이터 세트다(핸들러는 다시 기록하지 않는다).
	serviceNotes bool
}

func (s *source) Dataset() export.Dataset {
	cols := []export.Column{{Label: "이름", Kind: export.KindText}, {Label: "금액(원)", Kind: export.KindMoney}}
	if s.sensCol {
		cols = append(cols, export.Column{Label: "이메일", Kind: export.KindText, Sensitive: true})
	}
	ds := export.Dataset{Code: "people", Label: "조직 구성원", Columns: cols}
	if s.sensCol && !s.noPrivacySpec {
		ds.Privacy = &export.PrivacySpec{Category: privacy.CatContact, Fields: []string{privacy.FieldEmail}, ServiceNotes: s.serviceNotes}
	}
	return ds
}
func (s *source) Filters(q url.Values) []export.FilterItem {
	return []export.FilterItem{export.Date("기준일", q.Get("asOf")), export.Free("검색어", q.Get("q"))}
}
func (s *source) Count(context.Context, url.Values, int) (int, bool, error) {
	if s.err != nil {
		return 0, false, s.err
	}
	return s.n, s.more, nil
}
func (s *source) Rows(_ context.Context, _ url.Values, raw bool, _ int) ([][]export.Cell, error) {
	s.gotRaw = raw
	return s.rows, s.err
}

func newServer(t *testing.T, src export.Source, max int, rec *recorder) (*export.Env, *contract.RouteTable) {
	t.Helper()
	env := export.NewEnv(fakeTxs{}, rec, max)
	env.Privacy = &noter{}
	env.Now = func() time.Time { return time.Date(2026, 11, 12, 5, 30, 0, 0, time.UTC) }
	rt := contract.NewRouteTable()
	export.Register(rt, "/api/people/export", contract.PermOrgRead, env, src)
	return env, rt
}

func do(rt *contract.RouteTable, role contract.Role, uid int64, rawURL string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("GET", rawURL, nil)
	req = req.WithContext(contract.WithPrincipal(req.Context(), contract.Principal{UserID: uid, Name: "홍길동", Role: role, Channel: contract.ChannelWeb}))
	w := httptest.NewRecorder()
	rt.Handler().ServeHTTP(w, req)
	return w
}

func diffOf(e audit.Entry, key string) string {
	for _, d := range e.Diff {
		if d.Key == key {
			return d.New
		}
	}
	return "<없음>"
}

// AC-706: 등록기는 권한이 필수이고(빈 권한 패닉), 권한 없는 역할은 403 이다.
func TestRegisterRequiresPermissionAndForbidsOthers(t *testing.T) {
	rec := &recorder{}
	env := export.NewEnv(fakeTxs{}, rec, 100)
	rt := contract.NewRouteTable()
	func() {
		defer func() {
			if recover() == nil {
				t.Error("빈 권한 선언이 패닉 없이 등록됨")
			}
		}()
		export.Register(rt, "/api/x/export", "", env, &source{})
	}()
	_, rt = newServer(t, &source{n: 1, rows: [][]export.Cell{{"가", int64(1)}}}, 100, rec)
	if w := do(rt, "unknown-role", 5, "/api/people/export?format=csv"); w.Code != http.StatusForbidden {
		t.Errorf("권한 없는 역할 %d", w.Code)
	}
}

// AC-707: 성공하면 파일이 내려가고 감사 export.run 이 남는다. 조건은 값까지, 자유 입력은 "입력함"만.
func TestExportCSVAndAudit(t *testing.T) {
	rec := &recorder{}
	src := &source{n: 2, rows: [][]export.Cell{{"가나", int64(1000)}, {"=위험", int64(-5)}}}
	_, rt := newServer(t, src, 100, rec)
	w := do(rt, contract.RoleStaff, 5, "/api/people/export?format=csv&asOf=2026-11-01&q="+url.QueryEscape("홍길동"))
	if w.Code != 200 {
		t.Fatalf("상태 %d: %s", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/csv") {
		t.Errorf("Content-Type %q", ct)
	}
	cd := w.Header().Get("Content-Disposition")
	if !strings.Contains(cd, "attachment") || !strings.Contains(cd, "filename*=UTF-8''"+url.PathEscape("조직 구성원_20261112_1430.csv")) {
		t.Errorf("Content-Disposition %q", cd)
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Error("no-store 가 아님")
	}
	if !bytes.HasPrefix(w.Body.Bytes(), []byte{0xEF, 0xBB, 0xBF}) || !strings.Contains(w.Body.String(), "'=위험") {
		t.Errorf("본문 %q", w.Body.String())
	}
	if len(rec.entries) != 1 {
		t.Fatalf("감사 기록 %d건", len(rec.entries))
	}
	e := rec.entries[0]
	if e.Action != "export.run" || e.TargetType != audit.TargetExport || e.TargetID != "people" || e.Result != audit.ResultSuccess {
		t.Errorf("감사 항목 %+v", e)
	}
	for k, want := range map[string]string{"dataset": "조직 구성원", "format": "csv", "exported_rows": "2", "columns": "2", "filters": "기준일=2026-11-01; 검색어=입력함", "raw_sensitive": "아니오"} {
		if got := diffOf(e, k); got != want {
			t.Errorf("감사 %s = %q 기대 %q", k, got, want)
		}
	}
	if strings.Contains(e.Detail+diffOf(e, "filters"), "홍길동") {
		t.Error("검색어 값이 감사에 남음")
	}
}

func TestExportXLSXIsZip(t *testing.T) {
	rec := &recorder{}
	_, rt := newServer(t, &source{n: 1, rows: [][]export.Cell{{"가", int64(1)}}}, 100, rec)
	w := do(rt, contract.RoleStaff, 5, "/api/people/export?format=xlsx")
	if w.Code != 200 || !strings.Contains(w.Header().Get("Content-Type"), "spreadsheetml.sheet") {
		t.Fatalf("상태 %d %q", w.Code, w.Header().Get("Content-Type"))
	}
	if _, err := zip.NewReader(bytes.NewReader(w.Body.Bytes()), int64(w.Body.Len())); err != nil {
		t.Fatalf("XLSX 가 zip 이 아님: %v", err)
	}
}

func TestBadFormatIs400(t *testing.T) {
	_, rt := newServer(t, &source{}, 100, &recorder{})
	for _, f := range []string{"", "pdf", "XLSX"} {
		if w := do(rt, contract.RoleStaff, 5, "/api/people/export?format="+f); w.Code != 400 || !strings.Contains(w.Body.String(), "엑셀(XLSX) 또는 CSV") {
			t.Errorf("형식 %q → %d %q", f, w.Code, w.Body.String())
		}
	}
}

// AC-704: 상한 초과는 파일 없이 거부되고 실제 건수·상한이 문구에 들어가며 감사(error + limit)가 남는다.
func TestLimitExceededRejectsBeforeBuilding(t *testing.T) {
	rec := &recorder{}
	src := &source{n: 12340}
	_, rt := newServer(t, src, 10000, rec)
	w := do(rt, contract.RoleStaff, 5, "/api/people/export?format=csv")
	if w.Code != 400 || !strings.Contains(w.Body.String(), "12,340건") || !strings.Contains(w.Body.String(), "최대 10,000건") {
		t.Fatalf("상한 초과 %d %q", w.Code, w.Body.String())
	}
	if src.rows != nil || src.gotRaw {
		t.Error("상한 초과인데 행을 읽음")
	}
	if len(rec.entries) != 1 || rec.entries[0].Result != audit.ResultError || diffOf(rec.entries[0], "limit") != "10000" {
		t.Errorf("감사 %+v", rec.entries)
	}
	// 거부 기록은 내보낸 건수가 아니라 조건에 맞는 건수 키를 쓴다(파일은 만들어지지 않았다)
	if e := rec.entries[0]; diffOf(e, "matched_rows") != "12340" || diffOf(e, "exported_rows") != "<없음>" {
		t.Errorf("상한 초과 감사 건수 키 matched=%q exported=%q", diffOf(e, "matched_rows"), diffOf(e, "exported_rows"))
	}
	// 건수를 끝까지 세지 않은 원천(more): "N건을 넘어" 문구
	_, rtMore := newServer(t, &source{n: 100, more: true}, 100, &recorder{})
	if w := do(rtMore, contract.RoleStaff, 9, "/api/people/export?format=csv"); w.Code != 400 || !strings.Contains(w.Body.String(), "상한(100건)을 넘습니다") {
		t.Errorf("more 문구 %d %q", w.Code, w.Body.String())
	}
	// 건수 확인 뒤 데이터가 늘어난 경우(E7)
	src2 := &source{n: 1, rows: make([][]export.Cell, 0)}
	for i := 0; i < 3; i++ {
		src2.rows = append(src2.rows, []export.Cell{"가", int64(i)})
	}
	_, rt2 := newServer(t, src2, 2, &recorder{})
	if w := do(rt2, contract.RoleStaff, 6, "/api/people/export?format=csv"); w.Code != 400 {
		t.Errorf("행 읽기 뒤 상한 재확인 %d", w.Code)
	}
}

// AC-709: 민감 열이 있는 데이터셋의 원문 포함은 mask:bypass 를 서버가 다시 판정한다. 기본은 가린 값(raw=false).
func TestRawSensitiveNeedsMaskBypass(t *testing.T) {
	rec := &recorder{}
	src := &source{n: 1, sensCol: true, rows: [][]export.Cell{{"가", int64(1), "a***@x.com"}}}
	_, rt := newServer(t, src, 100, rec)
	// 기본: 관리자여도 raw=false 로 부른다
	if w := do(rt, contract.RoleAdmin, 1, "/api/people/export?format=csv"); w.Code != 200 || src.gotRaw {
		t.Fatalf("기본 내보내기 %d raw=%v", w.Code, src.gotRaw)
	}
	// 권한 없는 raw=1 → 403 + 감사 denied
	rec.entries = nil
	if w := do(rt, contract.RoleStaff, 5, "/api/people/export?format=csv&raw=1"); w.Code != 403 {
		t.Fatalf("직원의 원문 포함 %d", w.Code)
	}
	if len(rec.entries) != 1 || rec.entries[0].Result != audit.ResultDenied {
		t.Errorf("감사 %+v", rec.entries)
	}
	// 감사 담당도 mask:bypass 가 없다
	if w := do(rt, contract.RoleAuditor, 6, "/api/people/export?format=csv&raw=1"); w.Code != 403 {
		t.Errorf("감사의 원문 포함 %d", w.Code)
	}
	// 권한자 + raw=1 → 원문 요청이 소스에 전달되고 감사에 "예"
	rec.entries = nil
	if w := do(rt, contract.RoleFinance, 7, "/api/people/export?format=csv&raw=1"); w.Code != 200 || !src.gotRaw {
		t.Fatalf("재무의 원문 포함 %d raw=%v", w.Code, src.gotRaw)
	}
	if diffOf(rec.entries[0], "raw_sensitive") != "예" {
		t.Errorf("원문 포함이 감사에 안 남음: %+v", rec.entries[0])
	}
	// 민감 열이 없는 데이터셋에서는 raw=1 이 의미 없다(권한 없어도 거부하지 않고 raw 를 전달하지 않는다)
	plain := &source{n: 1, rows: [][]export.Cell{{"가", int64(1)}}}
	_, rt2 := newServer(t, plain, 100, &recorder{})
	if w := do(rt2, contract.RoleStaff, 8, "/api/people/export?format=csv&raw=1"); w.Code != 200 || plain.gotRaw {
		t.Errorf("민감 열 없는 데이터셋 raw=1 → %d raw=%v", w.Code, plain.gotRaw)
	}
}

// AC-707: 감사 기록이 실패하면 파일을 보내지 않는다.
func TestAuditFailureBlocksFile(t *testing.T) {
	rec := &recorder{fail: true}
	_, rt := newServer(t, &source{n: 1, rows: [][]export.Cell{{"가", int64(1)}}}, 100, rec)
	w := do(rt, contract.RoleStaff, 5, "/api/people/export?format=csv")
	if w.Code != 500 || strings.Contains(w.Body.String(), "가") || w.Header().Get("Content-Disposition") != "" {
		t.Fatalf("감사 실패인데 파일이 나감: %d %q", w.Code, w.Body.String())
	}
}

// AC-705: 분당 5회·동시 1건 제한이 429 로 걸린다.
func TestRateLimited(t *testing.T) {
	rec := &recorder{}
	_, rt := newServer(t, &source{n: 1, rows: [][]export.Cell{{"가", int64(1)}}}, 100, rec)
	for i := 0; i < export.PerUserPerMinute; i++ {
		if w := do(rt, contract.RoleStaff, 5, "/api/people/export?format=csv"); w.Code != 200 {
			t.Fatalf("%d번째 %d", i+1, w.Code)
		}
	}
	w := do(rt, contract.RoleStaff, 5, "/api/people/export?format=csv")
	if w.Code != 429 || !strings.Contains(w.Body.String(), "너무 자주") {
		t.Fatalf("제한 %d %q", w.Code, w.Body.String())
	}
	if last := rec.entries[len(rec.entries)-1]; last.Result != audit.ResultDenied || last.Detail != "속도 제한" {
		t.Errorf("제한 감사 %+v", last)
	}
	if w := do(rt, contract.RoleStaff, 99, "/api/people/export?format=csv"); w.Code != 200 {
		t.Errorf("다른 사용자 %d", w.Code)
	}
}

func TestSourceErrorsMapToStatus(t *testing.T) {
	for _, c := range []struct {
		err  error
		code int
		sub  string
	}{
		{contract.ErrValidation, 400, "조건이 올바르지 않습니다"},
		{contract.ErrForbidden, 403, "권한이 없습니다"},
		{errors.New("sql: 내부 오류 상세"), 500, "파일을 만들지 못했습니다"},
	} {
		rec := &recorder{}
		_, rt := newServer(t, &source{err: c.err}, 100, rec)
		w := do(rt, contract.RoleStaff, int64(10+c.code), "/api/people/export?format=csv")
		if w.Code != c.code || !strings.Contains(w.Body.String(), c.sub) || strings.Contains(w.Body.String(), "내부 오류 상세") {
			t.Errorf("%v → %d %q", c.err, w.Code, w.Body.String())
		}
		if len(rec.entries) != 1 || rec.entries[0].Result == audit.ResultSuccess {
			t.Errorf("실패 감사 %+v", rec.entries)
		}
	}
}

func TestParseMaxRows(t *testing.T) {
	for in, want := range map[string]int{"": 10000, "100": 100, "100000": 100000, "99": -1, "100001": -1, "abc": -1, "-5": -1} {
		if got := export.ParseMaxRows(in); got != want {
			t.Errorf("ParseMaxRows(%q)=%d 기대 %d", in, got, want)
		}
	}
}

// 허용 형식 밖의 조건 값: 파일을 만들기 전에 400, 감사에는 값 대신 "잘못된 값"만 남는다(요청의 임의 문자열이 감사에 새지 않는다).
func TestInvalidFilterValueRejectedAndNotAudited(t *testing.T) {
	rec := &recorder{}
	src := &source{n: 1, rows: [][]export.Cell{{"가", int64(1)}}}
	_, rt := newServer(t, src, 100, rec)
	w := do(rt, contract.RoleStaff, 5, "/api/people/export?format=csv&asOf="+url.QueryEscape("홍길동 010-1234-5678"))
	if w.Code != 400 {
		t.Fatalf("잘못된 조건 %d %q", w.Code, w.Body.String())
	}
	if src.rows != nil && src.gotRaw {
		t.Error("거부했는데 행을 읽음")
	}
	if len(rec.entries) != 1 || rec.entries[0].Result != audit.ResultError {
		t.Fatalf("감사 %+v", rec.entries)
	}
	e := rec.entries[0]
	if got := diffOf(e, "filters"); got != "기준일=잘못된 값" {
		t.Errorf("감사 filters %q", got)
	}
	all := e.Detail + diffOf(e, "filters")
	for _, d := range e.Diff {
		all += d.Old + d.New
	}
	if strings.Contains(all, "홍길동") || strings.Contains(all, "010-1234") {
		t.Errorf("감사에 요청 값이 남음 %q", all)
	}
}

// noter 는 개인정보 조회 기록을 모으는 시험용 Noter 다.
type noter struct {
	notes []privacy.Access
	fail  bool
}

func (n *noter) Note(_ context.Context, _ contract.Principal, a privacy.Access) error {
	return n.NoteTx(context.Background(), nil, contract.Principal{}, a)
}
func (n *noter) NoteTx(_ context.Context, _ contract.Tx, _ contract.Principal, a privacy.Access) error {
	if n.fail {
		return errors.New("기록 실패")
	}
	n.notes = append(n.notes, a)
	return nil
}

// B41: 원문 포함 내보내기는 감사와 함께 개인정보 조회 기록을 남기고, 기록하지 못하면 파일을 보내지 않는다(fail-closed).
func TestRawExportNotesPrivacyAccess(t *testing.T) {
	rec := &recorder{}
	src := &source{n: 2, sensCol: true, rows: [][]export.Cell{{"가", int64(1), "RAWVALUE-A"}, {"나", int64(2), "RAWVALUE-B"}}}
	env, rt := newServer(t, src, 100, rec)
	nt := env.Privacy.(*noter)
	if w := do(rt, contract.RoleFinance, 7, "/api/people/export?format=csv"); w.Code != 200 || len(nt.notes) != 0 {
		t.Fatalf("원문 포함이 아니면 조회 기록이 없어야 함: %d %d", w.Code, len(nt.notes))
	}
	if w := do(rt, contract.RoleFinance, 7, "/api/people/export?format=csv&raw=1"); w.Code != 200 || len(nt.notes) != 1 {
		t.Fatalf("원문 포함: %d 기록 %d", w.Code, len(nt.notes))
	}
	a := nt.notes[0]
	if !a.Export || a.Category != privacy.CatContact || a.Dataset != "people" || a.Screen != "내보내기 조직 구성원" || a.UnknownSubjects != 2 {
		t.Errorf("조회 기록 내용: %+v", a)
	}
	// 기록 실패: 파일이 나가지 않는다.
	nt.fail = true
	w := do(rt, contract.RoleFinance, 7, "/api/people/export?format=csv&raw=1")
	if w.Code != 500 || strings.Contains(w.Body.String(), "RAWVALUE-A") {
		t.Errorf("기록 실패 시 파일을 보냄: %d %q", w.Code, w.Body.String())
	}
	// 개인정보 명세를 선언하지 않은 민감 데이터 세트, 기록 장치가 없는 환경도 원문 포함을 거부한다.
	bare := &source{n: 1, sensCol: true, noPrivacySpec: true, rows: [][]export.Cell{{"가", int64(1), "RAWVALUE-A"}}}
	_, rt2 := newServer(t, bare, 100, &recorder{})
	if w := do(rt2, contract.RoleFinance, 7, "/api/people/export?format=csv&raw=1"); w.Code != 500 {
		t.Errorf("명세 없는 데이터 세트의 원문 포함: %d", w.Code)
	}
	env3, rt3 := newServer(t, &source{n: 1, sensCol: true, rows: [][]export.Cell{{"가", int64(1), "RAWVALUE-A"}}}, 100, &recorder{})
	env3.Privacy = nil
	if w := do(rt3, contract.RoleFinance, 7, "/api/people/export?format=csv&raw=1"); w.Code != 500 {
		t.Errorf("기록 장치 없는 원문 포함: %d", w.Code)
	}
}

// B41: 서비스 층이 기록하는 데이터 세트는 핸들러가 다시 기록하지 않는다(한 응답 한 기록). 서비스가 기록하지 못하면 Rows 의 오류로 파일이 나가지 않는다.
func TestServiceNotedDatasetIsNotRecordedTwice(t *testing.T) {
	src := &source{n: 1, sensCol: true, serviceNotes: true, rows: [][]export.Cell{{"가", int64(1), "RAWVALUE-A"}}}
	env, rt := newServer(t, src, 100, &recorder{})
	nt := env.Privacy.(*noter)
	if w := do(rt, contract.RoleFinance, 7, "/api/people/export?format=csv&raw=1"); w.Code != 200 || len(nt.notes) != 0 {
		t.Fatalf("서비스가 기록하는 데이터 세트: %d, 핸들러 기록 %d건(0건이어야 함)", w.Code, len(nt.notes))
	}
	env.Privacy = nil // 핸들러 쪽 기록기가 없어도 서비스가 기록하므로 내보낼 수 있다
	if w := do(rt, contract.RoleFinance, 7, "/api/people/export?format=csv&raw=1"); w.Code != 200 {
		t.Errorf("핸들러 기록기 없음: %d", w.Code)
	}
	// 서비스가 기록하지 못해 Rows 가 실패하면 파일이 나가지 않는다.
	failing := &source{n: 1, sensCol: true, serviceNotes: true, err: errors.New("열람 기록을 남기지 못함")}
	_, rt2 := newServer(t, failing, 100, &recorder{})
	if w := do(rt2, contract.RoleFinance, 7, "/api/people/export?format=csv&raw=1"); w.Code == 200 {
		t.Errorf("서비스 기록 실패인데 파일이 나감: %d", w.Code)
	}
}
