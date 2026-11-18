package privreport

import (
	"archive/zip"
	"bytes"
	"context"
	"database/sql"
	"encoding/xml"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/choibongseok/procure-ledger/internal/access"
	"github.com/choibongseok/procure-ledger/internal/audit"
	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/dbtest"
	"github.com/choibongseok/procure-ledger/internal/dbutil"
	"github.com/choibongseok/procure-ledger/internal/export"
	"github.com/choibongseok/procure-ledger/internal/privacy"
	"github.com/choibongseok/procure-ledger/internal/sod"
	"github.com/choibongseok/procure-ledger/migrations"
)

type env struct {
	t                                   *testing.T
	db                                  *sql.DB
	txs                                 contract.TxManager
	svc                                 *Service
	eng                                 *sod.Engine
	rec                                 *privacy.Recorder
	clock                               *time.Time
	admin, auditor, staff, subj1, subj2 int64
}

func newEnv(t *testing.T, now time.Time) *env {
	t.Helper()
	ctx := context.Background()
	db := dbtest.Open(t)
	if err := migrations.Apply(ctx, db); err != nil {
		t.Fatal(err)
	}
	e := &env{t: t, db: db, clock: &now}
	e.txs = dbutil.NewTxManager(db, dbutil.DialectOf(db))
	clock := func() time.Time { return *e.clock }
	var err error
	if e.rec, err = privacy.New(privacy.Options{DB: db, Txs: e.txs, Audit: audit.New(db), Now: clock}); err != nil {
		t.Fatal(err)
	}
	if e.eng, err = sod.New(sod.Options{DB: db, Txs: e.txs, Audit: audit.New(db), Now: clock}); err != nil {
		t.Fatal(err)
	}
	if e.svc, err = New(Options{DB: db, Txs: e.txs, Audit: audit.New(db), Priv: e.rec, Now: clock}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO departments (name) VALUES ('운영')`); err != nil {
		t.Fatal(err)
	}
	user := func(email, name, role string) int64 {
		var id int64
		if err := db.QueryRow(`INSERT INTO users (email, name, role, department_id) VALUES (?, ?, ?, (SELECT id FROM departments WHERE name = '운영')) RETURNING id`, email, name, role).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	e.admin, e.auditor, e.staff = user("a@x.test", "관리자", "admin"), user("u@x.test", "감사 담당", "auditor"), user("s@x.test", "직원", "staff")
	e.subj1, e.subj2 = user("s1@x.test", "정보주체 하나", "staff"), user("s2@x.test", "정보주체 둘", "staff")
	if _, err := access.SyncTemplates(ctx, db); err != nil {
		t.Fatal(err)
	}
	if _, err := access.New(db, e.txs, audit.New(db), clock).Reconcile(ctx, contract.Principal{Role: "system", Channel: contract.ChannelWeb}, true); err != nil {
		t.Fatal(err)
	}
	return e
}

func (e *env) p(id int64, role contract.Role, name string) contract.Principal {
	return contract.Principal{UserID: id, Name: name, Role: role, Channel: contract.ChannelWeb, Auth: contract.AuthSession}
}

func (e *env) count(q string, args ...any) int {
	e.t.Helper()
	var n int
	if err := e.db.QueryRow(q, args...).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

// seed 는 10월(KST) 한 달 동안의 위반·예외·조회 기록을 만든다.
func (e *env) seed() {
	e.t.Helper()
	ctx := context.Background()
	*e.clock = time.Date(2026, 10, 14, 1, 0, 0, 0, time.UTC) // 10:00 KST
	eval := func(rule string, id int64) error {
		var err error
		werr := e.txs.WithTx(ctx, func(tx contract.Tx) error {
			_, err = e.eng.Evaluate(ctx, tx, sod.Check{Rule: rule, Actor: e.p(e.staff, contract.RoleStaff, "직원"), Subject: sod.Ref{Type: "payment", ID: id, No: "PAY-" + string(rune('0'+id))}, Other: e.staff})
			return err
		})
		sod.NoteBlocked(ctx, e.eng, werr)
		return werr
	}
	for i := int64(1); i <= 2; i++ { // 경고 2건
		if err := eval(sod.RulePayCreateApprove, i); err != nil {
			e.t.Fatal(err)
		}
	}
	if err := eval(sod.RulePayApproveSettle, 3); err == nil { // 차단 1건
		e.t.Fatal("차단되어야 한다")
	}
	exID, err := e.eng.RequestException(contract.WithPrincipal(ctx, e.p(e.admin, contract.RoleAdmin, "관리자")), sod.ExceptionInput{Rule: sod.RulePayApproveSettle, SubjectType: "payment", SubjectID: 4, SubjectNo: "PAY-4", UserID: e.staff, Reason: "소규모 팀이라 한 사람이 처리해야 합니다"})
	if err != nil {
		e.t.Fatal(err)
	}
	if err := e.eng.DecideException(contract.WithPrincipal(ctx, e.p(e.auditor, contract.RoleAuditor, "감사 담당")), exID, true, ""); err != nil {
		e.t.Fatal(err)
	}
	if err := eval(sod.RulePayApproveSettle, 4); err != nil { // 예외 통과 1건
		e.t.Fatal(err)
	}
	adm := e.p(e.admin, contract.RoleAdmin, "관리자")
	note := func(count int, export bool) {
		a := privacy.Access{Category: privacy.CatContact, Screen: privacy.ScreenPersonCard, Fields: []string{privacy.FieldEmail}, Export: export}
		for i := 0; i < count; i++ {
			a.Subjects = append(a.Subjects, privacy.Subject{Type: privacy.SubjectUser, ID: int64(1000 + i)})
		}
		if err := e.rec.Note(ctx, adm, a); err != nil {
			e.t.Fatal(err)
		}
	}
	note(2, false)
	note(3, false)
	note(120, true)
	*e.clock = time.Date(2026, 11, 3, 1, 0, 0, 0, time.UTC)
}

// ---- 최소 XLSX 읽기 ----

type workbook struct {
	names  []string
	sheets map[string][][]string
}

var cellRe = regexp.MustCompile(`(?s)<c r="([A-Z]+)\d+"[^>]*?(?:/>|>(.*?)</c>)`)
var textRe = regexp.MustCompile(`(?s)<t[^>]*>(.*?)</t>|<v>(.*?)</v>`)

func readBook(t *testing.T, b []byte) workbook {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatalf("zip 이 아님: %v", err)
	}
	files := map[string]string{}
	for _, f := range zr.File {
		rc, _ := f.Open()
		body, _ := io.ReadAll(rc)
		rc.Close()
		files[f.Name] = string(body)
	}
	var wb struct {
		Sheets []struct {
			Name string `xml:"name,attr"`
		} `xml:"sheets>sheet"`
	}
	if err := xml.Unmarshal([]byte(files["xl/workbook.xml"]), &wb); err != nil {
		t.Fatal(err)
	}
	out := workbook{sheets: map[string][][]string{}}
	for i, s := range wb.Sheets {
		out.names = append(out.names, s.Name)
		body := files["xl/worksheets/sheet"+string(rune('1'+i))+".xml"]
		var rows [][]string
		for _, rowXML := range strings.Split(body, "<row ")[1:] {
			var row []string
			for _, m := range cellRe.FindAllStringSubmatch(rowXML, -1) {
				v := ""
				if tm := textRe.FindStringSubmatch(m[2]); tm != nil {
					v = tm[1] + tm[2]
				}
				col := int(m[1][0] - 'A') // 빈 칸은 XML 에 없으므로 열 글자로 자리를 맞춘다(시험 시트는 26열 미만)
				for len(row) <= col {
					row = append(row, "")
				}
				row[col] = v
			}
			rows = append(rows, row)
		}
		out.sheets[s.Name] = rows
	}
	return out
}

func (w workbook) text() string {
	var b strings.Builder
	for _, rows := range w.sheets {
		for _, r := range rows {
			b.WriteString(strings.Join(r, "|") + "\n")
		}
	}
	return b.String()
}

func findRow(rows [][]string, a, b string) []string {
	for _, r := range rows {
		if len(r) >= 2 && r[0] == a && r[1] == b {
			return r
		}
	}
	return nil
}

func TestBuildFiveSheetsWithExactNumbers(t *testing.T) {
	e := newEnv(t, time.Now())
	e.seed()
	res, err := e.svc.Build(context.Background(), e.p(e.auditor, contract.RoleAuditor, "감사 담당"), "2026-10")
	if err != nil {
		t.Fatal(err)
	}
	wb := readBook(t, res.Body)
	want := []string{"요약", "위반 목록", "예외 승인 사유", "조회 기록 요약", "보유 현황(hold)", "내보내기 정보"}
	if strings.Join(wb.names, ",") != strings.Join(want, ",") {
		t.Fatalf("시트: %v", wb.names)
	}
	sum := wb.sheets["요약"]
	for _, c := range [][3]string{{"권한 분리 위반(전체)", "경고(통과)", "2"}, {"권한 분리 위반(전체)", "차단", "1"}, {"권한 분리 위반(전체)", "예외 통과", "1"},
		{"조회 기록", "전체 요청", "3"}, {"조회 기록", "내보내기", "1"}, {"조회 기록(범주별)", "연락처", "3"}} {
		r := findRow(sum, c[0], c[1])
		if r == nil || r[2] != c[2] {
			t.Errorf("요약 %v → %v", c, r)
		}
	}
	if r := findRow(sum, "규칙별 위반", "지급 요청 작성자의 최종 승인 · 경고(통과)"); r == nil || r[2] != "2" {
		t.Errorf("규칙별 경고 건수 %v", r)
	}
	if r := findRow(sum, "규칙별 위반", "지급 승인자의 지급 처리 · 예외 통과"); r == nil || r[2] != "1" {
		t.Errorf("규칙별 예외 건수 %v", r)
	}
	if n := len(wb.sheets["위반 목록"]) - 1; n != 4 {
		t.Errorf("위반 목록 %d행(4행이어야 한다)", n)
	}
	exc := wb.sheets["예외 승인 사유"]
	if len(exc) != 2 || !strings.Contains(strings.Join(exc[1], "|"), "소규모 팀이라 한 사람이 처리해야 합니다") || !strings.Contains(strings.Join(exc[1], "|"), "감사 담당") {
		t.Errorf("예외 승인 사유 시트: %v", exc)
	}
	logSheet := wb.sheets["조회 기록 요약"]
	if r := findRow(logSheet, "범주·화면별", "연락처"); r == nil || r[6] != "3" || r[7] != "125" {
		t.Errorf("범주·화면별 요청 건수·정보주체 수: %v", r)
	}
	if r := findRow(logSheet, "한 번에 100명 초과", "연락처"); r == nil || r[3] != "관리자" || r[7] != "120" {
		t.Errorf("100명 초과 요청: %v", r)
	} else if v, err := strconv.ParseFloat(r[5], 64); err != nil || v < 40000 {
		t.Errorf("시각 칸은 엑셀 날짜(일련 번호) 숫자여야 한다: %q", r[5])
	}
	if r := wb.sheets["위반 목록"][1]; len(r) < 1 {
		t.Error("위반 목록 첫 행")
	} else if v, err := strconv.ParseFloat(r[0], 64); err != nil || v < 40000 {
		t.Errorf("위반 목록 시각 칸은 엑셀 날짜 숫자여야 한다: %q", r[0])
	}
	hold := wb.sheets["보유 현황(hold)"]
	holdRules := 0
	for _, r := range sod.Rules {
		if r.Kind == sod.KindHold {
			holdRules++
		}
	}
	if len(hold)-1 != holdRules {
		t.Errorf("보유 현황 %d행(규칙 %d개)", len(hold)-1, holdRules)
	}
	if res.Month != "2026-10" || !strings.Contains(res.FileName, "월별 점검 보고서_2026-10") {
		t.Errorf("이름·월: %+v", res)
	}
	// 정보주체 이름·이메일은 어디에도 없다. 열람자(관리자)는 있다.
	all := wb.text()
	for _, bad := range []string{"정보주체 하나", "정보주체 둘", "s1@x.test", "s2@x.test"} {
		if strings.Contains(all, bad) {
			t.Errorf("파일에 %q 가 있다", bad)
		}
	}
	// 다른 달은 비어 있다.
	res2, err := e.svc.Build(context.Background(), e.p(e.auditor, contract.RoleAuditor, "감사 담당"), "2026-09")
	if err != nil {
		t.Fatal(err)
	}
	wb2 := readBook(t, res2.Body)
	if len(wb2.sheets["위반 목록"]) != 1 || findRow(wb2.sheets["요약"], "권한 분리 위반(전체)", "차단")[2] != "0" {
		t.Error("9월에는 위반이 없어야 한다")
	}
	if _, err := e.svc.Build(context.Background(), e.p(e.auditor, contract.RoleAuditor, "감사 담당"), "2027-01"); err == nil {
		t.Error("미래 달은 거부")
	}
}

func get(e *env, p contract.Principal, query string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("GET", "/api/audit/privacy/report"+query, nil)
	req = req.WithContext(contract.WithPrincipal(req.Context(), p))
	w := httptest.NewRecorder()
	e.svc.handle(w, req)
	return w
}

func TestHandlerNeedsBothPermissionsAndAudits(t *testing.T) {
	e := newEnv(t, time.Now())
	e.seed()
	// 관리자: sod:read 는 있고 privacy:log_read 는 없다 → 403 + 거부 감사
	w := get(e, e.p(e.admin, contract.RoleAdmin, "관리자"), "?month=2026-10")
	if w.Code != http.StatusForbidden || e.count(`SELECT COUNT(*) FROM audit_logs WHERE action = 'export.run' AND result = 'denied' AND target_id = 'privacy_report'`) != 1 {
		t.Fatalf("관리자: %d %s", w.Code, w.Body.String())
	}
	// 직원: 둘 다 없다 → 403
	if w := get(e, e.p(e.staff, contract.RoleStaff, "직원"), "?month=2026-10"); w.Code != http.StatusForbidden {
		t.Fatalf("직원: %d", w.Code)
	}
	aud := e.p(e.auditor, contract.RoleAuditor, "감사 담당")
	if w := get(e, aud, "?month=2026-1"); w.Code != http.StatusBadRequest {
		t.Fatalf("월 형식: %d", w.Code)
	}
	if w := get(e, aud, "?month=2026-10&format=csv"); w.Code != http.StatusBadRequest {
		t.Fatalf("csv: %d", w.Code)
	}
	w = get(e, aud, "?month=2026-10")
	if w.Code != 200 || !strings.Contains(w.Header().Get("Content-Type"), "spreadsheetml") || !strings.Contains(w.Header().Get("Content-Disposition"), "attachment") {
		t.Fatalf("감사: %d %v", w.Code, w.Header())
	}
	if len(readBook(t, w.Body.Bytes()).names) != 6 {
		t.Fatal("시트 수")
	}
	if e.count(`SELECT COUNT(*) FROM audit_logs WHERE action = 'export.run' AND result = 'success' AND target_id = 'privacy_report'`) != 1 ||
		e.count(`SELECT COUNT(*) FROM audit_logs WHERE action = 'privacy.log_read' AND target_id = 'report'`) != 1 {
		t.Fatal("내보내기 감사와 조회 기록 읽기 감사가 한 건씩 남아야 한다")
	}
	// 월을 생략하면 지난달(11월 3일 기준 10월)이다.
	if w := get(e, aud, ""); w.Code != 200 {
		t.Fatalf("기본 월: %d", w.Code)
	}
}

func TestMonthlyNoticeOnFirstBusinessDay(t *testing.T) {
	e := newEnv(t, time.Date(2026, 11, 1, 1, 0, 0, 0, time.UTC)) // 일요일
	ctx := context.Background()
	if n, err := e.svc.Tick(ctx); err != nil || n != 0 {
		t.Fatalf("일요일에는 보내지 않는다: %d %v", n, err)
	}
	// 11월 2일(월)이 공휴일이면 첫 영업일은 3일이다.
	e.svc.SetBusinessDay(func(ctx context.Context, q contract.Tx, day string) (bool, error) {
		if day == "2026-11-02" {
			return false, nil
		}
		wd, _ := time.Parse("2006-01-02", day)
		return wd.Weekday() != time.Saturday && wd.Weekday() != time.Sunday, nil
	})
	*e.clock = time.Date(2026, 11, 2, 1, 0, 0, 0, time.UTC)
	if n, err := e.svc.Tick(ctx); err != nil || n != 0 {
		t.Fatalf("공휴일에는 보내지 않는다: %d %v", n, err)
	}
	*e.clock = time.Date(2026, 11, 3, 1, 0, 0, 0, time.UTC)
	n, err := e.svc.Tick(ctx)
	if err != nil || n != 1 {
		t.Fatalf("첫 영업일에 두 권한 보유자(감사)에게 한 건: %d %v", n, err)
	}
	if e.count(`SELECT COUNT(*) FROM notifications WHERE kind = 'privacy_monthly_report' AND user_id = ? AND ref_id = 202610`, e.auditor) != 1 {
		t.Fatal("감사에게 알림이 없다")
	}
	if e.count(`SELECT COUNT(*) FROM notifications WHERE kind = 'privacy_monthly_report' AND user_id IN (?, ?)`, e.admin, e.staff) != 0 {
		t.Fatal("두 권한이 없는 사람에게 알림이 갔다")
	}
	if n, err := e.svc.Tick(ctx); err != nil || n != 0 {
		t.Fatalf("같은 달 알림은 한 번만: %d %v", n, err)
	}
	*e.clock = time.Date(2026, 12, 1, 1, 0, 0, 0, time.UTC) // 12월 첫 영업일(화)
	if n, err := e.svc.Tick(ctx); err != nil || n != 1 {
		t.Fatalf("다음 달 알림: %d %v", n, err)
	}
}

func TestPrivacyLogExport(t *testing.T) {
	e := newEnv(t, time.Now())
	e.seed()
	env := export.NewEnv(e.txs, audit.New(e.db), 0)
	env.Now = func() time.Time { return *e.clock }
	rt := contract.NewRouteTable()
	e.svc.RegisterExport(rt, env)
	do := func(p contract.Principal, query string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", "/api/admin/privacy/log/export"+query, nil)
		req = req.WithContext(contract.WithPrincipal(req.Context(), p))
		w := httptest.NewRecorder()
		rt.Handler().ServeHTTP(w, req)
		return w
	}
	aud := e.p(e.auditor, contract.RoleAuditor, "감사 담당")
	w := do(aud, "?format=csv")
	if w.Code != 200 {
		t.Fatalf("감사 내보내기 %d %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	for _, want := range []string{"시각", "열람자", "정보주체 수", "조직도 사람 카드", "관리자", "120"} {
		if !strings.Contains(body, want) {
			t.Errorf("CSV 에 %q 가 없다", want)
		}
	}
	if strings.Contains(body, "정보주체 하나") || strings.Contains(body, "@x.test") {
		t.Error("CSV 에 정보주체 이름·이메일이 있다")
	}
	if got := strings.Count(strings.TrimSpace(body), "\n"); got < 3 {
		t.Errorf("행이 부족하다: %d", got)
	}
	// 내보내기 한 번에 감사 export.run 한 건과 조회 기록 읽기 감사 privacy.log_read 한 건(명세 5.5).
	if e.count(`SELECT COUNT(*) FROM audit_logs WHERE action = 'export.run' AND result = 'success' AND target_id = 'privacy_log'`) != 1 {
		t.Error("내보내기 감사가 한 건이어야 한다")
	}
	if e.count(`SELECT COUNT(*) FROM audit_logs WHERE action = 'privacy.log_read' AND target_id = 'log'`) != 1 {
		t.Error("내보내기가 조회 기록 읽기 감사를 한 번 남겨야 한다(건수 세기는 남기지 않는다)")
	}
	// 조건: 내보내기만, 범주.
	if w := do(aud, "?format=csv&export=1"); w.Code != 200 || strings.Count(strings.TrimSpace(w.Body.String()), "\n") != 1 {
		t.Errorf("내보내기만 필터: %d %q", w.Code, w.Body.String())
	}
	if w := do(aud, "?format=xlsx&category=account"); w.Code != 200 {
		t.Errorf("xlsx: %d", w.Code)
	}
	if w := do(aud, "?format=csv&category=bogus"); w.Code != http.StatusBadRequest {
		t.Errorf("잘못된 범주: %d", w.Code)
	}
	// 권한: privacy:log_read 가 없으면 거부(관리자·직원).
	for _, p := range []contract.Principal{e.p(e.admin, contract.RoleAdmin, "관리자"), e.p(e.staff, contract.RoleStaff, "직원")} {
		if w := do(p, "?format=csv"); w.Code != http.StatusForbidden {
			t.Errorf("%s: %d", p.Role, w.Code)
		}
	}
}
